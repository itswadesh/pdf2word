// Package convert orchestrates the PDF → DOCX pipeline: read the text layer,
// decide per page whether OCR is needed, run it, and write the result.
package convert

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"pdf2word/internal/docx"
	"pdf2word/internal/model"
	"pdf2word/internal/ocr"
	"pdf2word/internal/pdfimage"
	"pdf2word/internal/pdftext"
)

// OCRMode controls when OCR is used.
type OCRMode string

const (
	// OCRAuto runs OCR only on pages that have (almost) no text layer.
	OCRAuto OCRMode = "auto"
	// OCROff never runs OCR.
	OCROff OCRMode = "off"
	// OCRForce runs OCR on every page that has images, preferring the OCR
	// result over the text layer.
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

// ImageSource yields the images of a page (1-based). pdfimage.Reader
// satisfies it; tests supply fakes.
type ImageSource interface {
	PageImages(page int) ([]pdfimage.Image, error)
	Close() error
}

// Progress is reported after each page has been resolved.
type Progress struct {
	Page   int // 1-based page just finished
	Total  int
	Source model.PageSource // how the page's text was obtained
	OCR    bool             // true when OCR was attempted on this page
}

// Options configures a conversion. The zero value is usable.
type Options struct {
	OCR          OCRMode
	MinTextChars int    // default DefaultMinTextChars
	Lang         string // Tesseract language(s); empty means ocr.DefaultLang

	// TesseractPath is an explicit tesseract executable; empty means
	// auto-detect (see ocr.Find).
	TesseractPath string
	// Engine overrides Tesseract entirely. When nil, a Tesseract engine is
	// created lazily the first time a page needs OCR.
	Engine ocr.Engine
	// OpenImages overrides how page images are obtained (default pdfimage).
	OpenImages func(path string) (ImageSource, error)

	// OnProgress, if set, is called once per page as it completes.
	OnProgress func(Progress)
	// Logf, if set, receives verbose diagnostics.
	Logf func(format string, args ...any)
}

func (o *Options) applyDefaults() {
	if o.OCR == "" {
		o.OCR = OCRAuto
	}
	if o.MinTextChars <= 0 {
		o.MinTextChars = DefaultMinTextChars
	}
	if o.OpenImages == nil {
		o.OpenImages = func(path string) (ImageSource, error) {
			r, err := pdfimage.Open(path)
			if err != nil {
				return nil, err
			}
			return r, nil
		}
	}
	if o.OnProgress == nil {
		o.OnProgress = func(Progress) {}
	}
	if o.Logf == nil {
		o.Logf = func(string, ...any) {}
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

	s := &session{opts: opts, in: in, rep: &rep}
	defer s.close()

	for i := range doc.Pages {
		if err := ctx.Err(); err != nil {
			return nil, rep, err
		}
		page := &doc.Pages[i]
		ocrRan, err := s.processPage(ctx, page)
		if err != nil {
			return nil, rep, err
		}
		switch page.Source {
		case model.SourceText:
			rep.TextPages++
		case model.SourceOCR:
			rep.OCRPages++
		default:
			rep.EmptyPages++
		}
		opts.OnProgress(Progress{Page: page.Number, Total: rep.Pages, Source: page.Source, OCR: ocrRan})
	}
	return doc, rep, nil
}

// session holds lazily-created resources for one conversion.
type session struct {
	opts Options
	in   string
	rep  *Report

	images    ImageSource
	imagesErr error
	engine    ocr.Engine
}

func (s *session) close() {
	if s.images != nil {
		s.images.Close()
	}
}

// processPage applies the OCR policy to one page, mutating it in place.
// It reports whether OCR was attempted. Only a missing/unusable OCR engine is
// a hard error; everything else becomes a warning.
func (s *session) processPage(ctx context.Context, page *model.Page) (bool, error) {
	chars := page.TextChars()
	hasText := chars >= s.opts.MinTextChars

	switch s.opts.OCR {
	case OCROff:
		if chars == 0 {
			s.rep.warnf("page %d: no text layer and OCR is off", page.Number)
		}
		return false, nil
	case OCRAuto:
		if hasText {
			return false, nil
		}
	case OCRForce:
		// always try
	}

	imgs, err := s.pageImages(page.Number)
	if err != nil {
		s.rep.warnf("page %d: %v", page.Number, err)
		return false, nil
	}
	if len(imgs) == 0 {
		if !hasText {
			s.rep.warnf("page %d: no text layer and no images to OCR", page.Number)
		}
		return false, nil
	}

	eng, err := s.ensureEngine(ctx)
	if err != nil {
		return false, fmt.Errorf("page %d needs OCR but %w; install Tesseract, pass -tesseract, or use -ocr off", page.Number, err)
	}

	s.opts.Logf("page %d: running %s on %d image(s)", page.Number, eng.Name(), len(imgs))
	var blocks []model.Block
	for i, img := range imgs {
		text, err := eng.Recognize(ctx, img.Data, img.Ext)
		if err != nil {
			s.rep.warnf("page %d: image %d: OCR failed: %v", page.Number, i+1, err)
			continue
		}
		blocks = append(blocks, ocr.TextToBlocks(text)...)
	}
	if len(blocks) == 0 {
		if !hasText {
			s.rep.warnf("page %d: OCR produced no text", page.Number)
		}
		return true, nil
	}
	page.Blocks = blocks
	page.Source = model.SourceOCR
	return true, nil
}

func (s *session) pageImages(page int) ([]pdfimage.Image, error) {
	if s.images == nil && s.imagesErr == nil {
		src, err := s.opts.OpenImages(s.in)
		if err != nil {
			s.imagesErr = fmt.Errorf("open images: %w", err)
		} else {
			s.images = src
		}
	}
	if s.imagesErr != nil {
		return nil, s.imagesErr
	}
	return s.images.PageImages(page)
}

func (s *session) ensureEngine(ctx context.Context) (ocr.Engine, error) {
	if s.engine != nil {
		return s.engine, nil
	}
	if s.opts.Engine != nil {
		s.engine = s.opts.Engine
		return s.engine, nil
	}
	path, err := ocr.Find(s.opts.TesseractPath)
	if err != nil {
		return nil, err
	}
	t := &ocr.Tesseract{Path: path, Lang: s.opts.Lang}
	if err := t.Available(ctx); err != nil {
		return nil, fmt.Errorf("tesseract is not runnable: %w", err)
	}
	s.opts.Logf("using %s (lang %s)", path, orDefault(s.opts.Lang, ocr.DefaultLang))
	s.engine = t
	return t, nil
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
