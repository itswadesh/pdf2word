// Package web serves the drag-and-drop user interface on a loopback HTTP
// port and runs conversions as background jobs that the page polls.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"pdf2word/internal/convert"
	"pdf2word/internal/ocr"
)

//go:embed static/index.html
var static embed.FS

// Config configures a Server.
type Config struct {
	// Base is the conversion configuration; the page can override OCR mode
	// and language per file.
	Base convert.Options
	// WorkDir holds uploads and results. Empty means a fresh temp dir.
	WorkDir string
	// MaxUploadBytes caps a single upload (default 2 GiB).
	MaxUploadBytes int64
	// AllowRemote accepts requests addressed to any host, not just
	// localhost. Set it when the listener is bound to a network interface.
	AllowRemote bool
	Version     string
	Logf        func(format string, args ...any)
}

// clientCookie identifies a browser so that each one sees only the files it
// uploaded. Jobs stay reachable by their (unguessable) id regardless.
const clientCookie = "pdf2word_client"

// Server is the HTTP application. Create it with New.
type Server struct {
	cfg     Config
	mux     *http.ServeMux
	jobs    *jobStore
	sem     chan struct{} // one conversion at a time
	ownsDir bool

	mu         sync.Mutex
	lastSeen   time.Time // last request from the page
	pageOpened bool
}

// New prepares a Server. Call Close to remove its working directory.
func New(cfg Config) (*Server, error) {
	if cfg.MaxUploadBytes <= 0 {
		cfg.MaxUploadBytes = 2 << 30
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	ownsDir := false
	if cfg.WorkDir == "" {
		dir, err := os.MkdirTemp("", "pdf2word-web-")
		if err != nil {
			return nil, err
		}
		cfg.WorkDir = dir
		ownsDir = true
	} else if err := os.MkdirAll(cfg.WorkDir, 0o700); err != nil {
		return nil, err
	}

	s := &Server{
		cfg:      cfg,
		mux:      http.NewServeMux(),
		jobs:     newJobStore(cfg.WorkDir),
		sem:      make(chan struct{}, 1),
		ownsDir:  ownsDir,
		lastSeen: time.Now(),
	}
	s.mux.HandleFunc("GET /{$}", s.handleIndex)
	s.mux.HandleFunc("GET /api/info", s.handleInfo)
	s.mux.HandleFunc("POST /api/ping", s.handlePing)
	s.mux.HandleFunc("POST /api/convert", s.handleConvert)
	s.mux.HandleFunc("GET /api/jobs", s.handleJobs)
	s.mux.HandleFunc("GET /api/jobs/{id}", s.handleJob)
	s.mux.HandleFunc("POST /api/jobs/{id}/cancel", s.handleCancel)
	s.mux.HandleFunc("GET /api/jobs/{id}/download", s.handleDownload)
	return s, nil
}

// Handler returns the HTTP handler with loopback and origin protection.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.AllowRemote && !loopbackHost(r.Host) {
			http.Error(w, "this server only answers to localhost", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin := r.Header.Get("Origin"); origin != "" {
				u, err := url.Parse(origin)
				if err != nil || !strings.EqualFold(u.Host, r.Host) {
					http.Error(w, "cross-origin requests are not allowed", http.StatusForbidden)
					return
				}
			}
		}
		s.touch()
		w.Header().Set("Cache-Control", "no-store")
		s.mux.ServeHTTP(w, r)
	})
}

// Close removes the working directory if the server created it.
func (s *Server) Close() error {
	for _, j := range s.jobs.list() {
		j.cancel()
	}
	if s.ownsDir {
		return os.RemoveAll(s.cfg.WorkDir)
	}
	return nil
}

// IdleFor reports how long ago the page last talked to the server.
func (s *Server) IdleFor() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Since(s.lastSeen)
}

// PageOpened reports whether the UI was loaded at least once.
func (s *Server) PageOpened() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pageOpened
}

// Busy reports whether a conversion is queued or running.
func (s *Server) Busy() bool { return s.jobs.active() }

func (s *Server) touch() {
	s.mu.Lock()
	s.lastSeen = time.Now()
	s.mu.Unlock()
}

func loopbackHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// clientID returns the browser's identifier, or "" for clients without the
// cookie (e.g. curl).
func (s *Server) clientID(r *http.Request) string {
	if c, err := r.Cookie(clientCookie); err == nil {
		return c.Value
	}
	return ""
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.pageOpened = true
	s.mu.Unlock()
	if s.clientID(r) == "" {
		if id, err := newID(); err == nil {
			http.SetCookie(w, &http.Cookie{
				Name: clientCookie, Value: id, Path: "/",
				HttpOnly: true, SameSite: http.SameSiteStrictMode,
			})
		}
	}
	b, err := static.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "page missing from build", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}

// InfoView tells the page about the environment.
type InfoView struct {
	Version string   `json:"version"`
	OCR     OCRState `json:"ocr"`
}

// OCRState describes whether Tesseract is usable.
type OCRState struct {
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	Problem   string `json:"problem,omitempty"`
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	info := InfoView{Version: s.cfg.Version}
	if s.cfg.Base.Engine != nil {
		info.OCR = OCRState{Available: true, Version: s.cfg.Base.Engine.Name()}
	} else if p, err := ocr.Find(s.cfg.Base.TesseractPath); err != nil {
		info.OCR = OCRState{Problem: err.Error()}
	} else {
		t := &ocr.Tesseract{Path: p, Lang: s.cfg.Base.Lang}
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if v, err := t.Version(ctx); err != nil {
			info.OCR = OCRState{Path: p, Problem: err.Error()}
		} else {
			info.OCR = OCRState{Available: true, Path: p, Version: v}
		}
	}
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"busy": s.Busy()})
}

// handleJobs lists the requesting browser's own jobs.
func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	client := s.clientID(r)
	views := []JobView{}
	for _, j := range s.jobs.list() {
		if j.client == client {
			views = append(views, j.view(now))
		}
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobs.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such job")
		return
	}
	writeJSON(w, http.StatusOK, j.view(time.Now()))
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobs.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such job")
		return
	}
	j.cancel()
	writeJSON(w, http.StatusOK, j.view(time.Now()))
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobs.get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "no such job")
		return
	}
	if j.currentState() != StateDone {
		writeError(w, http.StatusConflict, "conversion has not finished")
		return
	}
	name := strings.TrimSuffix(j.filename, path.Ext(j.filename)) + ".docx"
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	http.ServeFile(w, r, j.output)
}

// handleConvert accepts a multipart upload (fields: ocr, lang, file) and
// queues a conversion. Fields must precede the file part.
func (s *Server) handleConvert(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected a multipart upload")
		return
	}

	mode := convert.OCRAuto
	lang := s.cfg.Base.Lang
	client := s.clientID(r)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			writeError(w, http.StatusBadRequest, "no file was uploaded")
			return
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "malformed upload: "+err.Error())
			return
		}
		switch part.FormName() {
		case "ocr":
			v, _ := io.ReadAll(io.LimitReader(part, 64))
			m, err := convert.ParseOCRMode(string(v))
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			mode = m
		case "lang":
			v, _ := io.ReadAll(io.LimitReader(part, 128))
			if l := strings.TrimSpace(string(v)); l != "" {
				lang = l
			}
		case "file":
			s.acceptFile(w, part, mode, lang, client)
			return
		default:
			io.Copy(io.Discard, part)
		}
	}
}

func (s *Server) acceptFile(w http.ResponseWriter, part *multipart.Part, mode convert.OCRMode, lang, client string) {
	filename := filepath.Base(strings.ReplaceAll(part.FileName(), "\\", "/"))
	if filename == "" || filename == "." || filename == "/" {
		filename = "document.pdf"
	}

	j, err := s.jobs.create(filename, mode, lang, client)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot create job: "+err.Error())
		return
	}
	size, err := saveUpload(j.input, part)
	if err != nil {
		s.jobs.remove(j.id)
		var mbe *http.MaxBytesError
		switch {
		case errors.As(err, &mbe):
			writeError(w, http.StatusRequestEntityTooLarge, "file is too large")
		case errors.Is(err, errNotPDF):
			writeError(w, http.StatusUnsupportedMediaType, fmt.Sprintf("%s is not a PDF file", filename))
		default:
			writeError(w, http.StatusBadRequest, "upload failed: "+err.Error())
		}
		return
	}
	j.size = size
	s.jobs.prune(time.Hour)
	go s.run(j)
	writeJSON(w, http.StatusAccepted, j.view(time.Now()))
}

var errNotPDF = errors.New("not a PDF")

// saveUpload streams the part to disk after checking the PDF signature.
func saveUpload(dst string, part io.Reader) (int64, error) {
	head := make([]byte, 5)
	n, err := io.ReadFull(part, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return 0, err
	}
	if n < 5 || string(head[:5]) != "%PDF-" {
		return 0, errNotPDF
	}
	f, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	written, err := io.Copy(f, io.MultiReader(strings.NewReader(string(head[:n])), part))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dst)
		return 0, err
	}
	return written, nil
}

// run executes a queued job; conversions run one at a time.
func (s *Server) run(j *job) {
	select {
	case s.sem <- struct{}{}:
	case <-j.ctx.Done():
		j.finish(convert.Report{}, j.ctx.Err())
		return
	}
	defer func() { <-s.sem }()

	j.start()
	opts := s.cfg.Base
	opts.OCR = j.ocr
	opts.Lang = j.lang
	opts.OnProgress = j.setProgress
	opts.Logf = func(format string, args ...any) { s.cfg.Logf("[%s] "+format, append([]any{j.id[:6]}, args...)...) }

	s.cfg.Logf("[%s] converting %s (ocr=%s lang=%s)", j.id[:6], j.filename, j.ocr, j.lang)
	rep, err := convert.Convert(j.ctx, j.input, j.output, opts)
	j.finish(rep, err)
	if err != nil {
		s.cfg.Logf("[%s] failed: %v", j.id[:6], err)
	} else {
		s.cfg.Logf("[%s] done: %d pages (%d text, %d ocr, %d empty)", j.id[:6], rep.Pages, rep.TextPages, rep.OCRPages, rep.EmptyPages)
	}
}
