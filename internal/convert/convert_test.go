package convert

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"pdf2word/internal/model"
	"pdf2word/internal/ocr"
	"pdf2word/internal/pdfimage"
)

func fixture(name string) string {
	return filepath.Join("..", "..", "testdata", name)
}

type fakeEngine struct {
	text  string
	err   error
	calls int
}

func (f *fakeEngine) Name() string { return "fake" }
func (f *fakeEngine) Recognize(context.Context, []byte, string) (string, error) {
	f.calls++
	if f.err != nil {
		return "", f.err
	}
	return f.text, nil
}

type fakeImages struct {
	pages  map[int][]pdfimage.Image
	err    error
	closed bool
	opened int
}

func (f *fakeImages) PageImages(page int) ([]pdfimage.Image, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.pages[page], nil
}
func (f *fakeImages) Close() error { f.closed = true; return nil }

func opener(fi *fakeImages) func(string) (ImageSource, error) {
	return func(string) (ImageSource, error) {
		fi.opened++
		return fi, nil
	}
}

func oneImage() []pdfimage.Image {
	return []pdfimage.Image{{Data: []byte("fake-png"), Ext: "png", Width: 100, Height: 100}}
}

func pageText(p model.Page) string {
	var parts []string
	for _, b := range p.Blocks {
		parts = append(parts, b.Text())
	}
	return strings.Join(parts, "\n")
}

func TestParseOCRMode(t *testing.T) {
	for in, want := range map[string]OCRMode{"auto": OCRAuto, "OFF": OCROff, "Force": OCRForce} {
		got, err := ParseOCRMode(in)
		if err != nil || got != want {
			t.Errorf("ParseOCRMode(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParseOCRMode("bogus"); err == nil {
		t.Error("expected an error for an unknown mode")
	}
}

func TestBuildDocument_TextPDFNeverCallsOCR(t *testing.T) {
	eng := &fakeEngine{text: "should not be used"}
	fi := &fakeImages{pages: map[int][]pdfimage.Image{1: oneImage(), 2: oneImage()}}
	doc, rep, err := BuildDocument(context.Background(), fixture("text.pdf"), Options{Engine: eng, OpenImages: opener(fi)})
	if err != nil {
		t.Fatal(err)
	}
	if eng.calls != 0 {
		t.Errorf("engine was called %d times for a text PDF", eng.calls)
	}
	if rep.Pages != 2 || rep.TextPages != 2 || rep.OCRPages != 0 || rep.EmptyPages != 0 {
		t.Errorf("report = %+v", rep)
	}
	if doc.Pages[0].Source != model.SourceText || !strings.Contains(pageText(doc.Pages[0]), "Quarterly Report") {
		t.Errorf("page 1 = %+v", doc.Pages[0])
	}
}

func TestBuildDocument_ScannedPDFAutoUsesOCR(t *testing.T) {
	eng := &fakeEngine{text: "Hello from\nOCR\n\nSecond para\n"}
	fi := &fakeImages{pages: map[int][]pdfimage.Image{1: oneImage()}}
	doc, rep, err := BuildDocument(context.Background(), fixture("scanned.pdf"), Options{Engine: eng, OpenImages: opener(fi)})
	if err != nil {
		t.Fatal(err)
	}
	if eng.calls != 1 {
		t.Errorf("engine calls = %d, want 1", eng.calls)
	}
	p := doc.Pages[0]
	if p.Source != model.SourceOCR {
		t.Errorf("source = %v, want ocr", p.Source)
	}
	if got := pageText(p); got != "Hello from OCR\nSecond para" {
		t.Errorf("page text = %q", got)
	}
	if rep.OCRPages != 1 || rep.Pages != 1 || len(rep.Warnings) != 0 {
		t.Errorf("report = %+v", rep)
	}
	if !fi.closed {
		t.Error("image source was not closed")
	}
}

func TestBuildDocument_OCROffLeavesScannedPageEmpty(t *testing.T) {
	eng := &fakeEngine{text: "nope"}
	fi := &fakeImages{pages: map[int][]pdfimage.Image{1: oneImage()}}
	doc, rep, err := BuildDocument(context.Background(), fixture("scanned.pdf"), Options{OCR: OCROff, Engine: eng, OpenImages: opener(fi)})
	if err != nil {
		t.Fatal(err)
	}
	if eng.calls != 0 || fi.opened != 0 {
		t.Errorf("OCR off must not touch engine (%d calls) or images (%d opens)", eng.calls, fi.opened)
	}
	if doc.Pages[0].Source != model.SourceEmpty || rep.EmptyPages != 1 {
		t.Errorf("page = %+v report = %+v", doc.Pages[0], rep)
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "OCR is off") {
		t.Errorf("warnings = %q", rep.Warnings)
	}
}

func TestBuildDocument_ForcePrefersOCRWhereImagesExist(t *testing.T) {
	eng := &fakeEngine{text: "OCR TEXT"}
	fi := &fakeImages{pages: map[int][]pdfimage.Image{1: oneImage()}} // page 2 has no images
	doc, rep, err := BuildDocument(context.Background(), fixture("text.pdf"), Options{OCR: OCRForce, Engine: eng, OpenImages: opener(fi)})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Pages[0].Source != model.SourceOCR || pageText(doc.Pages[0]) != "OCR TEXT" {
		t.Errorf("page 1 = %+v", doc.Pages[0])
	}
	if doc.Pages[1].Source != model.SourceText || pageText(doc.Pages[1]) != "Second page content here." {
		t.Errorf("page 2 = %+v", doc.Pages[1])
	}
	if rep.OCRPages != 1 || rep.TextPages != 1 {
		t.Errorf("report = %+v", rep)
	}
}

func TestBuildDocument_NeedsOCRButNoTesseractFailsFast(t *testing.T) {
	t.Setenv("TESSERACT_CMD", "")
	fi := &fakeImages{pages: map[int][]pdfimage.Image{1: oneImage()}}
	_, _, err := BuildDocument(context.Background(), fixture("scanned.pdf"), Options{
		TesseractPath: filepath.Join(t.TempDir(), "nope-tesseract"),
		OpenImages:    opener(fi),
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !errors.Is(err, ocr.ErrNotFound) || !strings.Contains(err.Error(), "needs OCR") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestBuildDocument_NoImagesToOCRIsAWarning(t *testing.T) {
	eng := &fakeEngine{text: "unused"}
	fi := &fakeImages{pages: map[int][]pdfimage.Image{}}
	doc, rep, err := BuildDocument(context.Background(), fixture("scanned.pdf"), Options{Engine: eng, OpenImages: opener(fi)})
	if err != nil {
		t.Fatal(err)
	}
	if eng.calls != 0 || doc.Pages[0].Source != model.SourceEmpty {
		t.Errorf("calls = %d page = %+v", eng.calls, doc.Pages[0])
	}
	if len(rep.Warnings) != 1 || !strings.Contains(rep.Warnings[0], "no images") {
		t.Errorf("warnings = %q", rep.Warnings)
	}
}

func TestBuildDocument_EngineErrorBecomesWarning(t *testing.T) {
	eng := &fakeEngine{err: errors.New("boom")}
	fi := &fakeImages{pages: map[int][]pdfimage.Image{1: oneImage()}}
	doc, rep, err := BuildDocument(context.Background(), fixture("scanned.pdf"), Options{Engine: eng, OpenImages: opener(fi)})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Pages[0].Source != model.SourceEmpty || rep.EmptyPages != 1 {
		t.Errorf("page = %+v report = %+v", doc.Pages[0], rep)
	}
	joined := strings.Join(rep.Warnings, "|")
	if !strings.Contains(joined, "boom") {
		t.Errorf("warnings = %q", rep.Warnings)
	}
}

func TestBuildDocument_ImageSourceErrorBecomesWarning(t *testing.T) {
	eng := &fakeEngine{text: "unused"}
	fi := &fakeImages{err: errors.New("cannot read images")}
	_, rep, err := BuildDocument(context.Background(), fixture("scanned.pdf"), Options{Engine: eng, OpenImages: opener(fi)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(rep.Warnings, "|"), "cannot read images") {
		t.Errorf("warnings = %q", rep.Warnings)
	}
}

func TestBuildDocument_ReportsProgressPerPage(t *testing.T) {
	var got []Progress
	record := func(p Progress) { got = append(got, p) }

	_, _, err := BuildDocument(context.Background(), fixture("text.pdf"), Options{OnProgress: record, OpenImages: opener(&fakeImages{})})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d progress events, want 2: %+v", len(got), got)
	}
	for i, p := range got {
		if p.Done != i+1 || p.Page != i+1 || p.Total != 2 || p.Source != model.SourceText || p.OCR {
			t.Errorf("event %d = %+v", i, p)
		}
	}

	got = nil
	eng := &fakeEngine{text: "ocr"}
	fi := &fakeImages{pages: map[int][]pdfimage.Image{1: oneImage()}}
	if _, _, err := BuildDocument(context.Background(), fixture("scanned.pdf"), Options{OnProgress: record, Engine: eng, OpenImages: opener(fi)}); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (Progress{Done: 1, Total: 1, Page: 1, Source: model.SourceOCR, OCR: true}) {
		t.Errorf("scanned progress = %+v", got)
	}
}

// A 247-page file once produced 247 copies of the same "cannot open images"
// warning. The failure must be reported once, plus one summary line.
func TestBuildDocument_ImageOpenFailureIsReportedOnce(t *testing.T) {
	eng := &fakeEngine{text: "unused"}
	boom := func(string) (ImageSource, error) { return nil, errors.New("renderer exploded") }
	doc, rep, err := BuildDocument(context.Background(), fixture("text.pdf"), Options{OCR: OCRForce, Engine: eng, OpenImages: boom})
	if err != nil {
		t.Fatal(err)
	}
	if eng.calls != 0 {
		t.Errorf("engine should not run without images, ran %d times", eng.calls)
	}
	if len(rep.Warnings) != 2 {
		t.Fatalf("want exactly 2 warnings (failure + summary), got %d: %q", len(rep.Warnings), rep.Warnings)
	}
	if !strings.Contains(rep.Warnings[0], "renderer exploded") || !strings.Contains(rep.Warnings[1], "2 page(s)") {
		t.Errorf("warnings = %q", rep.Warnings)
	}
	// Text layer is still used when OCR cannot run.
	if doc.Pages[0].Source != model.SourceText || rep.TextPages != 2 {
		t.Errorf("pages should fall back to the text layer: %+v", rep)
	}
}

// Rendering a page that has a text layer but no embedded images, then
// OCR-ing the render, is the path that vector-outline PDFs rely on.
func TestBuildDocument_ForceOCRRendersPagesWithRealTesseract(t *testing.T) {
	if _, err := ocr.Find(""); err != nil {
		t.Skipf("tesseract not available: %v", err)
	}
	doc, rep, err := BuildDocument(context.Background(), fixture("text.pdf"), Options{OCR: OCRForce, DPI: 200})
	if err != nil {
		t.Fatal(err)
	}
	if rep.OCRPages != 2 || len(rep.Warnings) != 0 {
		t.Fatalf("report = %+v", rep)
	}
	text := strings.ToLower(pageText(doc.Pages[0]))
	if !strings.Contains(text, "quarterly report") || !strings.Contains(text, "first paragraph") {
		t.Errorf("OCR of rendered page 1 = %q", text)
	}
}

func TestBuildDocument_HonoursCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := BuildDocument(ctx, fixture("text.pdf"), Options{OpenImages: opener(&fakeImages{})}); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

// slowEngine returns the image payload as text after a delay and records how
// many Recognize calls overlapped.
type slowEngine struct {
	mu      sync.Mutex
	active  int
	maxSeen int
	delay   time.Duration
}

func (e *slowEngine) Name() string { return "slow" }
func (e *slowEngine) Recognize(_ context.Context, img []byte, _ string) (string, error) {
	e.mu.Lock()
	e.active++
	if e.active > e.maxSeen {
		e.maxSeen = e.active
	}
	e.mu.Unlock()
	time.Sleep(e.delay)
	e.mu.Lock()
	e.active--
	e.mu.Unlock()
	return string(img), nil
}

// pageTaggedImages returns one image per page whose bytes name the page, so
// results can be checked against the page they belong to.
type pageTaggedImages struct{}

func (pageTaggedImages) PageImages(page int) ([]pdfimage.Image, error) {
	return []pdfimage.Image{{Data: []byte(fmt.Sprintf("text of page %d", page)), Ext: "png", Width: 10, Height: 10}}, nil
}
func (pageTaggedImages) Close() error { return nil }

func TestBuildDocument_OCRRunsPagesInParallelAndKeepsOrder(t *testing.T) {
	eng := &slowEngine{delay: 120 * time.Millisecond}
	var events []Progress
	start := time.Now()
	doc, rep, err := BuildDocument(context.Background(), fixture("text.pdf"), Options{
		OCR:        OCRForce,
		Jobs:       2,
		Engine:     eng,
		OpenImages: func(string) (ImageSource, error) { return pageTaggedImages{}, nil },
		OnProgress: func(p Progress) { events = append(events, p) },
	})
	if err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if eng.maxSeen != 2 {
		t.Errorf("max concurrent OCR calls = %d, want 2", eng.maxSeen)
	}
	if elapsed > 220*time.Millisecond {
		t.Errorf("two 120ms pages took %v; they should overlap", elapsed)
	}
	for i, p := range doc.Pages {
		want := fmt.Sprintf("text of page %d", i+1)
		if p.Source != model.SourceOCR || pageText(p) != want {
			t.Errorf("page %d = %+v, want OCR text %q", i+1, p, want)
		}
	}
	if rep.OCRPages != 2 || len(events) != 2 {
		t.Fatalf("report = %+v events = %+v", rep, events)
	}
	for i, e := range events {
		if e.Done != i+1 || e.Total != 2 || !e.OCR {
			t.Errorf("event %d = %+v; Done must count up regardless of page order", i, e)
		}
	}
}

func TestBuildDocument_ParallelMissingEngineStillFailsFast(t *testing.T) {
	t.Setenv("TESSERACT_CMD", "")
	_, _, err := BuildDocument(context.Background(), fixture("text.pdf"), Options{
		OCR:           OCRForce,
		Jobs:          4,
		TesseractPath: filepath.Join(t.TempDir(), "nope-tesseract"),
		OpenImages:    func(string) (ImageSource, error) { return pageTaggedImages{}, nil },
	})
	if !errors.Is(err, ocr.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDefaultJobs(t *testing.T) {
	if n := DefaultJobs(); n < 1 || n > 8 {
		t.Fatalf("DefaultJobs() = %d, want 1..8", n)
	}
}

func TestConvert_WritesDocx(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.docx")
	rep, err := Convert(context.Background(), fixture("text.pdf"), out, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Pages != 2 {
		t.Errorf("report = %+v", rep)
	}
	if _, err := os.Stat(out + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("temporary file left behind: %v", err)
	}
	zr, err := zip.OpenReader(out)
	if err != nil {
		t.Fatalf("output is not a zip: %v", err)
	}
	defer zr.Close()
	found := false
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			found = true
		}
	}
	if !found {
		t.Error("word/document.xml missing from output")
	}
}

func TestConvert_MissingInput(t *testing.T) {
	if _, err := Convert(context.Background(), fixture("nope.pdf"), filepath.Join(t.TempDir(), "x.docx"), Options{}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestConvert_UnwritableOutputDir(t *testing.T) {
	out := filepath.Join(t.TempDir(), "missing-dir", "x.docx")
	if _, err := Convert(context.Background(), fixture("text.pdf"), out, Options{}); err == nil {
		t.Fatal("expected an error for a missing output directory")
	}
}

// TestConvert_ScannedWithRealTesseract exercises the full pipeline with the
// real image extractor and a real Tesseract binary. It is skipped when
// Tesseract is not installed.
func TestConvert_ScannedWithRealTesseract(t *testing.T) {
	if _, err := ocr.Find(""); err != nil {
		t.Skipf("tesseract not available: %v", err)
	}
	doc, rep, err := BuildDocument(context.Background(), fixture("scanned.pdf"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	p := doc.Pages[0]
	if p.Source != model.SourceOCR || rep.OCRPages != 1 {
		t.Fatalf("page source = %v, report = %+v, warnings = %q", p.Source, rep, rep.Warnings)
	}
	text := strings.ToLower(pageText(p))
	for _, want := range []string{"scanned", "quick brown fox", "lazy dog"} {
		if !strings.Contains(text, want) {
			t.Errorf("OCR text %q does not contain %q", text, want)
		}
	}
}
