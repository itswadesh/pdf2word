// Package web serves the drag-and-drop user interface on a loopback HTTP
// port and runs conversions as background jobs that the page polls.
package web

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
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
	// MaxUploadBytes caps a single upload (default DefaultMaxUploadBytes).
	MaxUploadBytes int64
	// AllowRemote accepts requests addressed to any host, not just
	// localhost. Set it when the listener is bound to a network interface.
	AllowRemote bool
	// PublicURL is the address the page is reachable at from outside, such
	// as https://pdf2word.example.com. It fills in the canonical link, the
	// sharing tags and the sitemap. Empty (a local or office instance)
	// leaves those relative and serves no sitemap.
	PublicURL string
	Version   string
	Logf      func(format string, args ...any)
}

// clientCookie identifies a browser so that each one sees only the files
// it uploaded, on every endpoint: listing, polling, cancelling and
// downloading all check it.
const clientCookie = "pdf2word_client"

// Server is the HTTP application. Create it with New.
type Server struct {
	cfg     Config
	mux     *http.ServeMux
	jobs    *jobStore
	sem     chan struct{} // one conversion at a time
	ownsDir bool
	preview previewer // thumbnails of the page being converted

	page     []byte        // index.html with the public URL and privacy copy filled in
	etag     string        // strong validator for that page
	stop     chan struct{} // closed by Close to end the retention sweeper
	stopOnce sync.Once

	mu         sync.Mutex
	lastSeen   time.Time // last request from the page
	pageOpened bool
}

// DefaultMaxUploadBytes is the largest PDF the page accepts unless
// Config.MaxUploadBytes says otherwise: 100 MB.
const DefaultMaxUploadBytes = 100 << 20

// Retention: uploads and results are deleted this long after a conversion
// finishes, on a timer, so the promise the page makes does not depend on
// somebody else turning up with another file.
// pageCacheControl lets a CDN hold the page briefly and serve a stale copy
// while it revalidates, so a slow origin is felt once rather than on every
// visit. The browser always revalidates, so a deploy is picked up at once.
const pageCacheControl = "public, max-age=0, s-maxage=300, stale-while-revalidate=86400"

const retention = time.Hour

// pruneInterval is how often the sweeper runs; a variable so tests can
// shorten it.
var pruneInterval = 5 * time.Minute

// The two versions of the page's privacy copy. Every sentence in both has
// to be true of the deployment it is served by.
const (
	localPrivacyCopy = "No account, no email address, no watermark on the result and no daily limit. " +
		"Files up to 100 MB are accepted. This copy of the converter runs on your own computer, so the " +
		"PDFs never travel anywhere, and everything is deleted an hour after conversion."
	hostedPrivacyCopy = "No account, no email address, no watermark on the result and no daily limit. " +
		"Files up to 100 MB are accepted. Your PDF is uploaded to this server to be converted, is never " +
		"shared with anyone, and is deleted an hour after the conversion finishes whether or not anyone " +
		"else visits. Only the browser that uploaded a file can see or download the result. If you would " +
		"rather the file never left your own machine, the converter is open source and you can run this " +
		"same program yourself."
	localFilesAnswer = "They stay on the machine running the converter and are deleted an hour after the " +
		"conversion finishes, on a timer. A file you choose but do not convert is deleted when you close " +
		"its window, or an hour after it was chosen. Each browser only sees the files it uploaded."
	hostedFilesAnswer = "They are uploaded to this server, kept only while the conversion runs, and deleted " +
		"an hour after it finishes, on a timer that does not wait for another visitor. A file is uploaded " +
		"as soon as you choose it, so its first page can be shown; if you close the window instead of " +
		"converting it, it is deleted there and then, and otherwise an hour later. Only the browser that " +
		"uploaded a file can see it or download the result, and nothing is passed to anyone else."
)

// New prepares a Server. Call Close to remove its working directory.
func New(cfg Config) (*Server, error) {
	if cfg.MaxUploadBytes <= 0 {
		cfg.MaxUploadBytes = DefaultMaxUploadBytes
	}
	if cfg.Logf == nil {
		cfg.Logf = func(string, ...any) {}
	}
	cfg.PublicURL = strings.TrimRight(strings.TrimSpace(cfg.PublicURL), "/")
	page, err := static.ReadFile("static/index.html")
	if err != nil {
		return nil, fmt.Errorf("page missing from build: %w", err)
	}
	// The page must tell the truth about where the file goes, and that
	// depends on who can reach this server. A copy bound to loopback only
	// converts the operator's own files on their own machine; one reachable
	// from the network is taking documents from other people.
	privacy, filesAnswer := localPrivacyCopy, localFilesAnswer
	if cfg.AllowRemote {
		privacy, filesAnswer = hostedPrivacyCopy, hostedFilesAnswer
	}
	for from, to := range map[string]string{
		"%PUBLIC_URL%":   cfg.PublicURL,
		"%PRIVACY%":      privacy,
		"%FILES_ANSWER%": filesAnswer,
	} {
		page = []byte(strings.ReplaceAll(string(page), from, to))
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
		page:     page,
		etag:     fmt.Sprintf(`"%x"`, sha256.Sum256(page)),
		stop:     make(chan struct{}),
		lastSeen: time.Now(),
	}
	go s.sweep()
	s.mux.HandleFunc("GET /{$}", s.handleIndex)
	s.mux.HandleFunc("GET /robots.txt", s.handleRobots)
	s.mux.HandleFunc("GET /sitemap.xml", s.handleSitemap)
	s.mux.HandleFunc("GET /api/info", s.handleInfo)
	s.mux.HandleFunc("POST /api/ping", s.handlePing)
	s.mux.HandleFunc("POST /api/convert", s.handleConvert)
	s.mux.HandleFunc("GET /api/jobs", s.handleJobs)
	s.mux.HandleFunc("GET /api/jobs/{id}", s.handleJob)
	s.mux.HandleFunc("POST /api/jobs/{id}/cancel", s.handleCancel)
	s.mux.HandleFunc("POST /api/jobs/{id}/start", s.handleStart)
	s.mux.HandleFunc("GET /api/jobs/{id}/download", s.handleDownload)
	s.mux.HandleFunc("GET /api/jobs/{id}/pages/{n}", s.handlePreview)
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
		// The page itself is identical for every visitor, so it may be cached
		// by the browser and by a CDN. Everything else is per-visitor state.
		if r.URL.Path == "/" {
			w.Header().Set("Cache-Control", pageCacheControl)
		} else {
			w.Header().Set("Cache-Control", "no-store")
		}
		s.mux.ServeHTTP(w, r)
	})
}

// sweep deletes finished jobs once they are older than the retention
// period, on the clock rather than on the next upload.
func (s *Server) sweep() {
	t := time.NewTicker(pruneInterval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			s.jobs.prune(retention, s.preview.forget)
		}
	}
}

// Close removes the working directory if the server created it.
func (s *Server) Close() error {
	s.stopOnce.Do(func() { close(s.stop) })
	for _, j := range s.jobs.list() {
		j.cancel()
	}
	s.preview.close()
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

// isHTTPS reports whether the visitor's connection is secure, including
// when TLS was terminated by a proxy in front of this server.
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("ETag", s.etag)
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, s.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Write(s.page)
}

// handleRobots keeps crawlers out of the job API and points them at the
// sitemap when the page has a public address.
func (s *Server) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	body := "User-agent: *\nDisallow: /api/\n"
	if s.cfg.PublicURL != "" {
		body += "\nSitemap: " + s.cfg.PublicURL + "/sitemap.xml\n"
	}
	io.WriteString(w, body)
}

// handleSitemap lists the one page this server has. Without a public
// address there is nothing to submit, so there is no sitemap either.
func (s *Server) handleSitemap(w http.ResponseWriter, r *http.Request) {
	if s.cfg.PublicURL == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>%s/</loc><changefreq>monthly</changefreq><priority>1.0</priority></url>
</urlset>
`, html.EscapeString(s.cfg.PublicURL))
}

// InfoView tells the page about the environment.
type InfoView struct {
	Version string   `json:"version"`
	OCR     OCRState `json:"ocr"`
	// MaxUploadBytes lets the page refuse oversized files before uploading.
	MaxUploadBytes int64 `json:"maxUploadBytes"`
}

// OCRState describes whether Tesseract is usable.
type OCRState struct {
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty"`
	Problem   string `json:"problem,omitempty"`
	// Languages are the installed language codes the page offers for
	// scanned pages; DefaultLang is the one it starts on.
	Languages   []string `json:"languages,omitempty"`
	DefaultLang string   `json:"defaultLang"`
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	info := InfoView{Version: s.cfg.Version, MaxUploadBytes: s.cfg.MaxUploadBytes}
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
			if langs, err := t.Languages(ctx); err == nil {
				info.OCR.Languages = langs
			}
		}
	}
	info.OCR.DefaultLang = s.defaultLang()
	writeJSON(w, http.StatusOK, info)
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"busy": s.Busy()})
}

// handleJobs lists the requesting browser's own jobs. It is also where a
// browser picks up its identity: the page is cached and so cannot carry a
// per-visitor cookie, and the page polls this endpoint as soon as it
// loads. The cookie applies from the next request onwards, so a caller
// that sent none is still answered from the cookie-less bucket here.
func (s *Server) handleJobs(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	client := s.clientID(r)
	if client == "" {
		if id, err := newID(); err == nil {
			http.SetCookie(w, &http.Cookie{
				Name: clientCookie, Value: id, Path: "/",
				HttpOnly: true, SameSite: http.SameSiteStrictMode,
				Secure: isHTTPS(r),
			})
		}
	}
	views := []JobView{}
	for _, j := range s.jobs.list() {
		// Staged files belong to the page that is choosing them, not to the
		// list of conversions.
		if j.client == client && j.currentState() != StateStaged {
			views = append(views, j.view(now))
		}
	}
	writeJSON(w, http.StatusOK, views)
}

// owns reports whether this request may act on the job. A job created by
// a browser carries that browser's client id and is reachable only by it;
// one created without a cookie (curl, a script) is reachable by its own
// unguessable id alone. Non-owners are told the job does not exist rather
// than that it is forbidden, so ids cannot be probed.
func (s *Server) owns(r *http.Request, j *job) bool {
	if j.client == "" {
		return true // created without a session; the unguessable id is the capability
	}
	c := s.clientID(r)
	if c == "" {
		return true // a caller that keeps no cookies (curl, the CLI) likewise
	}
	return j.client == c
}

func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobs.get(r.PathValue("id"))
	if !ok || !s.owns(r, j) {
		writeError(w, http.StatusNotFound, "no such job")
		return
	}
	writeJSON(w, http.StatusOK, j.view(time.Now()))
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobs.get(r.PathValue("id"))
	if !ok || !s.owns(r, j) {
		writeError(w, http.StatusNotFound, "no such job")
		return
	}
	// A staged file was never started: it and its files simply go.
	if j.currentState() == StateStaged {
		v := j.view(time.Now())
		v.State = StateCancelled
		s.preview.forget(j.id)
		s.jobs.remove(j.id)
		writeJSON(w, http.StatusOK, v)
		return
	}
	j.cancel()
	writeJSON(w, http.StatusOK, j.view(time.Now()))
}

// handleStart converts a staged file, in the language sent in the form
// field lang (the server's default when it is left out).
func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobs.get(r.PathValue("id"))
	if !ok || !s.owns(r, j) {
		writeError(w, http.StatusNotFound, "no such job")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4<<10)
	lang := strings.TrimSpace(r.FormValue("lang"))
	if lang != "" && !langPattern.MatchString(lang) {
		writeError(w, http.StatusBadRequest, "unknown language code "+strconv.Quote(lang))
		return
	}
	j.mu.Lock()
	if j.state != StateStaged {
		j.mu.Unlock()
		writeError(w, http.StatusConflict, "this file is not waiting to be converted")
		return
	}
	if lang != "" {
		j.lang = lang
	}
	j.state = StateQueued
	j.mu.Unlock()
	go s.run(j)
	writeJSON(w, http.StatusAccepted, j.view(time.Now()))
}

// stage keeps an uploaded file without converting it, after checking that
// PDFium can open it and counting its pages.
func (s *Server) stage(w http.ResponseWriter, j *job) {
	j.mu.Lock()
	j.state = StateStaged
	j.mu.Unlock()
	n, err := s.preview.pages(j)
	if err != nil {
		s.preview.forget(j.id)
		s.jobs.remove(j.id)
		msg := j.filename + " could not be opened as a PDF"
		if strings.Contains(strings.ToLower(err.Error()), "password") {
			msg = j.filename + " is protected by a password, so it cannot be converted"
		}
		writeError(w, http.StatusUnprocessableEntity, msg)
		return
	}
	j.mu.Lock()
	j.pages = n
	j.mu.Unlock()
	writeJSON(w, http.StatusAccepted, j.view(time.Now()))
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobs.get(r.PathValue("id"))
	if !ok || !s.owns(r, j) {
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

// handlePreview serves a picture of one page of a conversion in progress,
// for the scanner on the page: a thumbnail for the card, or ?size=large for
// the modal. Only the uploading browser gets it, and only until the
// conversion ends.
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	j, ok := s.jobs.get(r.PathValue("id"))
	if !ok || !s.owns(r, j) {
		writeError(w, http.StatusNotFound, "no such job")
		return
	}
	n, err := strconv.Atoi(r.PathValue("n"))
	if err != nil {
		writeError(w, http.StatusNotFound, "no such page")
		return
	}
	size := sizeCard
	switch r.URL.Query().Get("size") {
	case "":
	case sizeLarge.name:
		size = sizeLarge
	default:
		writeError(w, http.StatusBadRequest, "size must be large or left out")
		return
	}
	img, err := s.preview.thumbnail(j, n, size)
	switch {
	case errors.Is(err, errNoSuchPage), errors.Is(err, errPreviewGone):
		writeError(w, http.StatusNotFound, err.Error())
		return
	case err != nil:
		s.cfg.Logf("[%s] preview of page %d: %v", j.id[:6], n, err)
		writeError(w, http.StatusInternalServerError, "could not draw the page")
		return
	}
	// The same page of the same upload never changes, and only its owner
	// may see it, so the browser may keep it but a shared cache may not.
	w.Header().Set("Cache-Control", "private, max-age=600")
	w.Header().Set("Content-Type", "image/jpeg")
	w.Write(img)
}

// langPattern accepts Tesseract language codes joined with "+", such as
// "eng", "chi_sim" or "eng+ori". Codes that are well formed but not
// installed are dropped with a warning when the conversion runs.
var langPattern = regexp.MustCompile(`^[A-Za-z_]{1,32}(\+[A-Za-z_]{1,32}){0,7}$`)

// defaultLang is the language scanned pages are read in unless the page
// asks for another.
func (s *Server) defaultLang() string {
	if s.cfg.Base.Lang != "" {
		return s.cfg.Base.Lang
	}
	return ocr.DefaultLang
}

// handleConvert accepts a multipart upload (fields: ocr, lang, hold, file) and
// queues a conversion. Fields must precede the file part.
func (s *Server) handleConvert(w http.ResponseWriter, r *http.Request) {
	// The multipart framing adds a few hundred bytes to the file itself;
	// allow 64 KB for it so a file of exactly the limit still gets through.
	limit := s.cfg.MaxUploadBytes + 64<<10
	if r.ContentLength > limit {
		writeError(w, http.StatusRequestEntityTooLarge, s.tooLargeMessage())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "expected a multipart upload")
		return
	}

	mode := convert.OCRAuto
	lang := s.cfg.Base.Lang
	hold := false
	// Usually the page already has its identity from the first poll. If the
	// visitor dropped a file before that returned, mint it here so the job
	// is filed under the id their next poll will send.
	client := s.clientID(r)
	if client == "" {
		if id, err := newID(); err == nil {
			client = id
			http.SetCookie(w, &http.Cookie{
				Name: clientCookie, Value: client, Path: "/",
				HttpOnly: true, SameSite: http.SameSiteStrictMode,
				Secure: isHTTPS(r),
			})
		}
	}
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
				if !langPattern.MatchString(l) {
					writeError(w, http.StatusBadRequest, "unknown language code "+strconv.Quote(l))
					return
				}
				lang = l
			}
		case "hold":
			v, _ := io.ReadAll(io.LimitReader(part, 8))
			hold = strings.TrimSpace(string(v)) == "1"
		case "file":
			s.acceptFile(w, part, mode, lang, client, hold)
			return
		default:
			io.Copy(io.Discard, part)
		}
	}
}

// acceptFile saves the upload and starts converting it, or with hold only
// keeps it until the uploader starts it (see handleStart).
func (s *Server) acceptFile(w http.ResponseWriter, part *multipart.Part, mode convert.OCRMode, lang, client string, hold bool) {
	filename := filepath.Base(strings.ReplaceAll(part.FileName(), "\\", "/"))
	if filename == "" || filename == "." || filename == "/" {
		filename = "document.pdf"
	}
	if !strings.EqualFold(path.Ext(filename), ".pdf") {
		writeError(w, http.StatusUnsupportedMediaType, fmt.Sprintf("%s was not accepted: only .pdf files can be converted", filename))
		return
	}

	j, err := s.jobs.create(filename, mode, lang, client)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot create job: "+err.Error())
		return
	}
	size, err := saveUpload(j.input, part, s.cfg.MaxUploadBytes)
	if err != nil {
		s.jobs.remove(j.id)
		var mbe *http.MaxBytesError
		switch {
		case errors.As(err, &mbe), errors.Is(err, errTooLarge):
			writeError(w, http.StatusRequestEntityTooLarge, s.tooLargeMessage())
		case errors.Is(err, errNotPDF):
			writeError(w, http.StatusUnsupportedMediaType, fmt.Sprintf("%s is not a PDF file", filename))
		default:
			writeError(w, http.StatusBadRequest, "upload failed: "+err.Error())
		}
		return
	}
	j.size = size
	s.jobs.prune(retention, s.preview.forget)
	if hold {
		s.stage(w, j)
		return
	}
	go s.run(j)
	writeJSON(w, http.StatusAccepted, j.view(time.Now()))
}

var (
	errNotPDF   = errors.New("not a PDF")
	errTooLarge = errors.New("file exceeds the upload limit")
)

func (s *Server) tooLargeMessage() string {
	return fmt.Sprintf("file is larger than the %s limit", FormatSize(s.cfg.MaxUploadBytes))
}

// FormatSize renders a byte count the way the page does: whole MB or KB.
func FormatSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%d MB", (n+1<<19)>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", (n+1<<9)>>10)
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}

// saveUpload streams the part to disk after checking the PDF signature. A
// file longer than max bytes is discarded with errTooLarge.
func saveUpload(dst string, part io.Reader, max int64) (int64, error) {
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
	written, err := io.Copy(f, io.LimitReader(io.MultiReader(strings.NewReader(string(head[:n])), part), max+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && written > max {
		err = errTooLarge
	}
	if err != nil {
		os.Remove(dst)
		return 0, err
	}
	return written, nil
}

// run executes a queued job; conversions run one at a time.
func (s *Server) run(j *job) {
	// Deferred first, so it runs after the job is marked finished: the
	// previewer then refuses to reopen the document for it.
	defer s.preview.forget(j.id)
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
