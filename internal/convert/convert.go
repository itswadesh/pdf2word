// Package convert orchestrates the PDF → DOCX pipeline: read the text layer,
// decide per page whether OCR is needed, run it, and write the result.
package convert

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"pdf2word/internal/docx"
	"pdf2word/internal/model"
	"pdf2word/internal/ocr"
	"pdf2word/internal/pdfimage"
	"pdf2word/internal/pdftext"
	"pdf2word/internal/render"
)

// OCRMode controls when OCR is used.
type OCRMode string

const (
	// OCRAuto runs OCR only on pages that have (almost) no text layer.
	OCRAuto OCRMode = "auto"
	// OCROff never runs OCR.
	OCROff OCRMode = "off"
	// OCRForce runs OCR on every page, preferring the OCR result over the
	// text layer.
	OCRForce OCRMode = "force"
)

// ParseOCRMode parses a user-supplied mode string (case-insensitive).
// An empty string means OCRAuto.
func ParseOCRMode(s string) (OCRMode, error) {
	switch m := OCRMode(strings.ToLower(strings.TrimSpace(s))); m {
	case "", OCRAuto:
		return OCRAuto, nil
	case OCROff, OCRForce:
		return m, nil
	default:
		return "", fmt.Errorf("invalid OCR mode %q (want auto, off or force)", s)
	}
}

// DefaultMinTextChars is the text-layer size below which a page is treated
// as scanned in OCRAuto mode.
const DefaultMinTextChars = 20

// DefaultJobs is the default number of pages OCR'd concurrently: one
// Tesseract process per CPU, capped so a big machine does not thrash.
func DefaultJobs() int {
	n := runtime.NumCPU()
	if n > 8 {
		n = 8
	}
	if n < 1 {
		n = 1
	}
	return n
}

// ImageSource yields the images to OCR for a page (1-based). The default is
// a full-page render (render.Renderer); pdfimage.Reader (embedded images
// only) is the fallback; tests supply fakes. Implementations must be safe
// for concurrent use.
type ImageSource interface {
	PageImages(page int) ([]pdfimage.Image, error)
	Close() error
}

// Progress is reported after each page has been resolved. Pages that need
// OCR finish in parallel, so Page is not necessarily increasing; Done is.
type Progress struct {
	Done   int // pages finished so far (1..Total)
	Total  int
	Page   int              // 1-based page just finished
	Source model.PageSource // how the page's text was obtained
	OCR    bool             // true when OCR was attempted on this page
}

// Options configures a conversion. The zero value is usable.
type Options struct {
	OCR          OCRMode
	MinTextChars int    // default DefaultMinTextChars
	Lang         string // Tesseract language(s); empty means ocr.DefaultLang
	DPI          int    // resolution for rendering pages before OCR; default render.DefaultDPI
	Jobs         int    // pages OCR'd concurrently; default DefaultJobs()

	// TesseractPath is an explicit tesseract executable; empty means
	// auto-detect (see ocr.Find).
	TesseractPath string
	// Engine overrides Tesseract entirely. When nil, a Tesseract engine is
	// created lazily the first time a page needs OCR. Must be safe for
	// concurrent use when Jobs > 1.
	Engine ocr.Engine
	// OpenImages overrides how page images are obtained. The default renders
	// each page with PDFium and falls back to embedded images if rendering
	// is unavailable.
	OpenImages func(path string) (ImageSource, error)

	// OnProgress, if set, is called once per page as it completes (from the
	// calling goroutine only).
	OnProgress func(Progress)
	// Logf, if set, receives verbose diagnostics (may be called concurrently).
	Logf func(format string, args ...any)
}

func (o *Options) applyDefaults() {
	if o.OCR == "" {
		o.OCR = OCRAuto
	}
	if o.MinTextChars <= 0 {
		o.MinTextChars = DefaultMinTextChars
	}
	if o.DPI <= 0 {
		o.DPI = render.DefaultDPI
	}
	if o.Jobs <= 0 {
		o.Jobs = DefaultJobs()
	}
	if o.OnProgress == nil {
		o.OnProgress = func(Progress) {}
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
	}
	if o.OpenImages == nil {
		o.OpenImages = defaultOpenImages(o.DPI, o.Logf)
	}
}

// defaultOpenImages renders pages with PDFium; if that cannot be set up it
// falls back to extracting the images embedded in each page.
func defaultOpenImages(dpi int, logf func(string, ...any)) func(string) (ImageSource, error) {
	return func(path string) (ImageSource, error) {
		r, rerr := render.Open(path, dpi)
		if rerr == nil {
			logf("rendering pages at %d dpi for OCR", dpi)
			return r, nil
		}
		logf("page renderer unavailable (%v); falling back to embedded images", rerr)
		e, err := pdfimage.Open(path)
		if err != nil {
			return nil, fmt.Errorf("%w (renderer: %v)", err, rerr)
		}
		return e, nil
	}
}

// Report summarises a conversion.
type Report struct {
	Pages      int
	TextPages  int
	OCRPages   int
	EmptyPages int
	Warnings   []string
}

func (r *Report) warnf(format string, args ...any) {
	r.Warnings = append(r.Warnings, fmt.Sprintf(format, args...))
}

// Convert reads the PDF at in and writes a .docx to out. The output is
// written to a temporary file first and renamed into place on success.
func Convert(ctx context.Context, in, out string, opts Options) (Report, error) {
	if err := checkOutputDir(out); err != nil {
		return Report{}, err
	}
	doc, rep, err := BuildDocument(ctx, in, opts)
	if err != nil {
		return rep, err
	}
	if err := WriteFile(out, doc); err != nil {
		return rep, err
	}
	return rep, nil
}

// BuildDocument runs extraction and OCR and returns the document model
// without writing anything.
func BuildDocument(ctx context.Context, in string, opts Options) (*model.Document, Report, error) {
	opts.applyDefaults()
	var rep Report

	opts.Logf("reading text layer of %s", in)
	doc, warns, err := pdftext.Extract(in)
	if err != nil {
		return nil, rep, err
	}
	for _, w := range warns {
		rep.warnf("%s", w)
	}
	rep.Pages = len(doc.Pages)

	s := &session{opts: opts, in: in}
	defer s.close()

	done := 0
	finish := func(i int, ocrRan bool) {
		page := &doc.Pages[i]
		switch page.Source {
		case model.SourceText:
			rep.TextPages++
		case model.SourceOCR:
			rep.OCRPages++
		default:
			rep.EmptyPages++
		}
		done++
		opts.OnProgress(Progress{Done: done, Total: rep.Pages, Page: page.Number, Source: page.Source, OCR: ocrRan})
	}

	// Pass 1: settle pages that do not need OCR right away.
	var todo []int
	for i := range doc.Pages {
		if err := ctx.Err(); err != nil {
			return nil, rep, err
		}
		page := &doc.Pages[i]
		if s.needsOCR(page, &rep) {
			todo = append(todo, i)
			continue
		}
		finish(i, false)
	}

	// Pass 2: OCR the remaining pages, several at a time.
	if len(todo) > 0 {
		if err := s.ocrPages(ctx, doc, todo, &rep, finish); err != nil {
			return nil, rep, err
		}
	}
	if w := s.imagesOpenWarning(); w != "" {
		rep.warnf("%s", w)
	}
	if s.imagesFailedPages > 0 {
		rep.warnf("%d page(s) could not be OCR'd because page images were unavailable", s.imagesFailedPages)
	}
	return doc, rep, nil
}

// session holds lazily-created resources for one conversion.
type session struct {
	opts Options
	in   string

	imgOnce   sync.Once
	images    ImageSource
	imagesErr error // set once if the image source could not be opened

	engOnce sync.Once
	engine  ocr.Engine
	engErr  error

	imagesFailedPages int
}

func (s *session) close() {
	if s.images != nil {
		s.images.Close()
	}
}

// needsOCR applies the OCR policy to a page's text layer.
func (s *session) needsOCR(page *model.Page, rep *Report) bool {
	chars := page.TextChars()
	hasText := chars >= s.opts.MinTextChars
	switch s.opts.OCR {
	case OCROff:
		if chars == 0 {
			rep.warnf("page %d: no text layer and OCR is off", page.Number)
		}
		return false
	case OCRForce:
		return true
	default:
		return !hasText
	}
}

// pageResult is what one OCR worker produces for one page.
type pageResult struct {
	idx    int
	blocks []model.Block
	ocrRan bool
	warns  []string
	// imagesUnavailable marks pages skipped because the image source never
	// opened; they are counted once at the end instead of warned per page.
	imagesUnavailable bool
	err               error // hard error (engine missing): aborts the conversion
}

// ocrPages runs OCR for the given page indexes with opts.Jobs workers and
// applies the results in completion order.
func (s *session) ocrPages(ctx context.Context, doc *model.Document, todo []int, rep *Report, finish func(int, bool)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	results := make(chan pageResult)
	var wg sync.WaitGroup
	sem := make(chan struct{}, s.opts.Jobs)
	go func() {
		for _, idx := range todo {
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				// Emit a cancelled result so the receiver still sees every page.
				results <- pageResult{idx: idx, err: ctx.Err()}
				continue
			}
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				defer func() { <-sem }()
				results <- s.ocrPage(ctx, &doc.Pages[idx])
			}(idx)
		}
		wg.Wait()
		close(results)
	}()

	var firstErr error
	for r := range results {
		if r.err != nil {
			if firstErr == nil {
				firstErr = r.err
				cancel()
			}
			continue
		}
		if firstErr != nil {
			continue // draining
		}
		page := &doc.Pages[r.idx]
		if r.imagesUnavailable {
			s.imagesFailedPages++
		}
		for _, w := range r.warns {
			rep.warnf("%s", w)
		}
		if len(r.blocks) > 0 {
			page.Blocks = r.blocks
			page.Source = model.SourceOCR
		}
		finish(r.idx, r.ocrRan)
	}
	return firstErr
}

// ocrPage renders/extracts a page's images and recognises them. It never
// mutates the page; the caller applies the result.
func (s *session) ocrPage(ctx context.Context, page *model.Page) pageResult {
	res := pageResult{idx: page.Number - 1}
	hasText := page.TextChars() >= s.opts.MinTextChars

	imgs, err := s.pageImages(page.Number)
	if err != nil {
		if errors.Is(err, errImagesUnavailable) {
			res.imagesUnavailable = true
		} else {
			res.warns = append(res.warns, fmt.Sprintf("page %d: %v", page.Number, err))
		}
		return res
	}
	if len(imgs) == 0 {
		if !hasText {
			res.warns = append(res.warns, fmt.Sprintf("page %d: no text layer and no images to OCR", page.Number))
		}
		return res
	}

	eng, err := s.ensureEngine(ctx)
	if err != nil {
		res.err = fmt.Errorf("page %d needs OCR but %w; install Tesseract, pass -tesseract, or use -ocr off", page.Number, err)
		return res
	}

	s.opts.Logf("page %d: running %s on %d image(s)", page.Number, eng.Name(), len(imgs))
	res.ocrRan = true
	for i, img := range imgs {
		if ctx.Err() != nil {
			res.err = ctx.Err()
			return res
		}
		text, err := eng.Recognize(ctx, img.Data, img.Ext)
		if err != nil {
			if ctx.Err() != nil {
				res.err = ctx.Err()
				return res
			}
			res.warns = append(res.warns, fmt.Sprintf("page %d: image %d: OCR failed: %v", page.Number, i+1, err))
			continue
		}
		res.blocks = append(res.blocks, ocr.TextToBlocks(text)...)
	}
	if len(res.blocks) == 0 && !hasText {
		res.warns = append(res.warns, fmt.Sprintf("page %d: OCR produced no text", page.Number))
	}
	return res
}

var errImagesUnavailable = errors.New("page images unavailable")

// pageImages opens the image source on first use (once, even under
// concurrency). If opening fails, later calls fail fast with
// errImagesUnavailable and the failure is reported a single time.
func (s *session) pageImages(page int) ([]pdfimage.Image, error) {
	s.imgOnce.Do(func() {
		src, err := s.opts.OpenImages(s.in)
		if err != nil {
			s.imagesErr = fmt.Errorf("cannot get page images: %w; pages that need OCR will be left empty", err)
			return
		}
		s.images = src
	})
	if s.imagesErr != nil {
		return nil, errImagesUnavailable
	}
	return s.images.PageImages(page)
}

// imagesOpenWarning returns the one-time open failure, if any.
func (s *session) imagesOpenWarning() string {
	if s.imagesErr != nil {
		return s.imagesErr.Error()
	}
	return ""
}

func (s *session) ensureEngine(ctx context.Context) (ocr.Engine, error) {
	s.engOnce.Do(func() {
		if s.opts.Engine != nil {
			s.engine = s.opts.Engine
			return
		}
		path, err := ocr.Find(s.opts.TesseractPath)
		if err != nil {
			s.engErr = err
			return
		}
		t := &ocr.Tesseract{Path: path, Lang: s.opts.Lang}
		if s.opts.Jobs > 1 {
			t.Threads = 1 // one process per page; do not also multithread each
		}
		if err := t.Available(ctx); err != nil {
			s.engErr = fmt.Errorf("tesseract is not runnable: %w", err)
			return
		}
		s.opts.Logf("using %s (lang %s, %d parallel page(s))", path, orDefault(s.opts.Lang, ocr.DefaultLang), s.opts.Jobs)
		s.engine = t
	})
	return s.engine, s.engErr
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func checkOutputDir(out string) error {
	dir := filepath.Dir(out)
	st, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("output directory: %w", err)
	}
	if !st.IsDir() {
		return fmt.Errorf("output directory %s is not a directory", dir)
	}
	return nil
}

// WriteFile writes doc as .docx to out atomically: the data goes to
// out+".tmp" first and is renamed into place only if everything succeeded.
func WriteFile(out string, doc *model.Document) error {
	tmp := out + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	if err := docx.Write(f, doc); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close output: %w", err)
	}
	if err := os.Rename(tmp, out); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("replace output: %w", err)
	}
	return nil
}
