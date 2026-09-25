package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"pdf2word/internal/convert"
	"pdf2word/internal/model"
)

// State is the lifecycle of a conversion job.
type State string

const (
	// StateStaged is a file uploaded with hold=1: saved and counted, shown
	// to its uploader, and converted only once they start it.
	StateStaged    State = "staged"
	StateQueued    State = "queued"
	StateRunning   State = "running"
	StateDone      State = "done"
	StateError     State = "error"
	StateCancelled State = "cancelled"
)

func (s State) terminal() bool {
	return s == StateDone || s == StateError || s == StateCancelled
}

// JobView is the JSON representation served to the page.
type JobView struct {
	ID        string      `json:"id"`
	Filename  string      `json:"filename"`
	Size      int64       `json:"size"`
	State     State       `json:"state"`
	Phase     string      `json:"phase"` // "reading" while the PDF is read, "" while pages are resolved
	Done      int         `json:"done"`
	Total     int         `json:"total"`
	Page      int         `json:"page"`
	Stage     string      `json:"stage"` // "text", "ocr", "empty" or ""
	Message   string      `json:"message,omitempty"`
	Report    *ReportView `json:"report,omitempty"`
	Download  string      `json:"download,omitempty"`
	Pages     int         `json:"pages,omitempty"` // page count, known once a staged file has been opened
	ElapsedMs int64       `json:"elapsedMs"`
}

// ReportView mirrors convert.Report for JSON.
type ReportView struct {
	Pages      int      `json:"pages"`
	TextPages  int      `json:"textPages"`
	OCRPages   int      `json:"ocrPages"`
	EmptyPages int      `json:"emptyPages"`
	Warnings   []string `json:"warnings"`
}

type job struct {
	mu sync.Mutex

	id       string
	filename string // original upload name (base name only)
	size     int64
	dir      string // per-job working directory
	input    string
	output   string
	ocr      convert.OCRMode
	lang     string
	client   string // browser that uploaded it ("" for cookie-less clients)

	state    State
	pages    int // page count of a staged file
	progress convert.Progress
	message  string
	report   *convert.Report
	created  time.Time
	started  time.Time
	finished time.Time

	ctx    context.Context
	cancel context.CancelFunc
}

// newID returns 96 random bits as hex, used for job and client identifiers.
func newID() (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func (j *job) view(now time.Time) JobView {
	j.mu.Lock()
	defer j.mu.Unlock()
	v := JobView{
		ID:       j.id,
		Filename: j.filename,
		Size:     j.size,
		State:    j.state,
		Phase:    j.progress.Phase,
		Done:     j.progress.Done,
		Total:    j.progress.Total,
		Page:     j.progress.Page,
		Pages:    j.pages,
		Stage:    stageLabel(j.progress),
		Message:  j.message,
	}
	if j.state == StateDone {
		v.Download = "/api/jobs/" + j.id + "/download"
	}
	if j.report != nil {
		v.Report = &ReportView{
			Pages:      j.report.Pages,
			TextPages:  j.report.TextPages,
			OCRPages:   j.report.OCRPages,
			EmptyPages: j.report.EmptyPages,
			Warnings:   append([]string{}, j.report.Warnings...),
		}
	}
	switch {
	case j.started.IsZero():
		v.ElapsedMs = 0
	case j.finished.IsZero():
		v.ElapsedMs = now.Sub(j.started).Milliseconds()
	default:
		v.ElapsedMs = j.finished.Sub(j.started).Milliseconds()
	}
	return v
}

func stageLabel(p convert.Progress) string {
	switch {
	case p.Total == 0 || p.Phase == convert.PhaseReading:
		return ""
	case p.Source == model.SourceOCR:
		return "ocr"
	case p.Source == model.SourceText:
		return "text"
	default:
		return "empty"
	}
}

func (j *job) setProgress(p convert.Progress) {
	j.mu.Lock()
	j.progress = p
	j.mu.Unlock()
}

func (j *job) start() {
	j.mu.Lock()
	j.state = StateRunning
	j.started = time.Now()
	j.mu.Unlock()
}

func (j *job) finish(rep convert.Report, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.finished = time.Now()
	j.report = &rep
	switch {
	case err == nil:
		j.state = StateDone
	case errors.Is(err, context.Canceled) || errors.Is(j.ctx.Err(), context.Canceled):
		j.state = StateCancelled
		j.message = "Stopped before finishing"
	default:
		j.state = StateError
		j.message = err.Error()
	}
}

func (j *job) currentState() State {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state
}

// jobStore keeps jobs in memory and their files under root.
type jobStore struct {
	mu   sync.Mutex
	root string
	jobs map[string]*job
}

func newJobStore(root string) *jobStore {
	return &jobStore{root: root, jobs: map[string]*job{}}
}

func (s *jobStore) create(filename string, ocr convert.OCRMode, lang, client string) (*job, error) {
	id, err := newID()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(s.root, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	j := &job{
		id:       id,
		filename: filename,
		dir:      dir,
		input:    filepath.Join(dir, "input.pdf"),
		output:   filepath.Join(dir, "output.docx"),
		ocr:      ocr,
		lang:     lang,
		client:   client,
		state:    StateQueued,
		created:  time.Now(),
		ctx:      ctx,
		cancel:   cancel,
	}
	s.mu.Lock()
	s.jobs[id] = j
	s.mu.Unlock()
	return j, nil
}

// remove forgets a job and deletes its files (used when an upload fails).
func (s *jobStore) remove(id string) {
	s.mu.Lock()
	j, ok := s.jobs[id]
	delete(s.jobs, id)
	s.mu.Unlock()
	if ok {
		j.cancel()
		os.RemoveAll(j.dir)
	}
}

func (s *jobStore) get(id string) (*job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok := s.jobs[id]
	return j, ok
}

// active reports whether any job is queued or running. A staged file is
// not work in progress: it waits for someone who may never come back.
func (s *jobStore) active() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.jobs {
		if st := j.currentState(); !st.terminal() && st != StateStaged {
			return true
		}
	}
	return false
}

// prune removes finished jobs, and staged files nobody started, once they
// are older than maxAge, with their files. release is called first with
// each id so anything holding the job's files open can let go.
func (s *jobStore) prune(maxAge time.Duration, release func(id string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-maxAge)
	for id, j := range s.jobs {
		j.mu.Lock()
		old := (j.state.terminal() && !j.finished.IsZero() && j.finished.Before(cutoff)) ||
			(j.state == StateStaged && j.created.Before(cutoff))
		j.mu.Unlock()
		if old {
			if release != nil {
				release(id)
			}
			os.RemoveAll(j.dir)
			delete(s.jobs, id)
		}
	}
}

// list returns all jobs, newest first.
func (s *jobStore) list() []*job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*job, 0, len(s.jobs))
	for _, j := range s.jobs {
		out = append(out, j)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].created.After(out[b].created) })
	return out
}
