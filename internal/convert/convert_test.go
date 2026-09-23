package convert

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"math"
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
	var got, reading []Progress
	record := func(p Progress) {
		if p.Phase == PhaseReading {
			reading = append(reading, p)
			return
		}
		got = append(got, p)
	}

	_, _, err := BuildDocument(context.Background(), fixture("text.pdf"), Options{OnProgress: record, OpenImages: opener(&fakeImages{})})
	if err != nil {
		t.Fatal(err)
	}
	if len(reading) == 0 || reading[len(reading)-1].Done != 2 || reading[len(reading)-1].Total != 2 {
		t.Fatalf("reading phase should report both pages: %+v", reading)
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

func TestBuildDocument_ParallelProgressStaysInPhase(t *testing.T) {
	var events []Progress
	_, _, err := BuildDocument(context.Background(), fixture("text.pdf"), Options{
		OCR: OCRForce, Jobs: 2, Engine: &slowEngine{delay: 20 * time.Millisecond},
		OpenImages: func(string) (ImageSource, error) { return pageTaggedImages{}, nil },
		OnProgress: func(p Progress) { events = append(events, p) },
	})
	if err != nil {
		t.Fatal(err)
	}
	sawReading := false
	for i, e := range events {
		if e.Phase == PhaseReading {
			sawReading = true
			continue
		}
		if sawReading && i > 0 && events[i-1].Phase == PhaseReading && e.Done != 1 {
			t.Errorf("resolution phase must restart its count at 1, got %+v", e)
		}
	}
	if !sawReading {
		t.Error("no reading-phase events")
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
		OnProgress: func(p Progress) {
			if p.Phase == "" {
				events = append(events, p)
			}
		},
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

// wordEngine returns fixed word boxes in a 100x100 px image: a centred title
// line, two body lines, and a page number at the bottom right.
type wordEngine struct{ calls int }

func (e *wordEngine) Name() string { return "fake-words" }
func (e *wordEngine) Recognize(context.Context, []byte, string) (string, error) {
	return "should not be used", nil
}
func (e *wordEngine) RecognizeWords(context.Context, []byte, string) ([]ocr.Word, error) {
	e.calls++
	w := func(text string, left, top, width, height, line int) ocr.Word {
		return ocr.Word{Text: text, Left: left, Top: top, Width: width, Height: height, LineTop: top, LineHeight: height, Block: 1, Par: 1, Line: line, Conf: 90}
	}
	return []ocr.Word{
		w("TITLE", 42, 10, 16, 5, 1), // centred: 42..58 of 100
		w("body", 10, 30, 10, 3, 2), w("text", 22, 30, 10, 3, 2), w("line", 34, 30, 10, 3, 2),
		w("second", 10, 34, 14, 3, 3), w("line", 26, 34, 10, 3, 3),
		w("42", 84, 90, 6, 3, 4), // bottom right
	}, nil
}

func TestBuildDocument_OCRWordsAreLaidOut(t *testing.T) {
	eng := &wordEngine{}
	// A 100x130 px "render" of the Letter-sized text fixture (scale ~6.1 pt/px);
	// OCR is forced so the word boxes are used on a normal page size.
	img := []pdfimage.Image{{Data: []byte("fake-png"), Ext: "png", Width: 100, Height: 130}}
	fi := &fakeImages{pages: map[int][]pdfimage.Image{1: img, 2: img}}
	doc, rep, err := BuildDocument(context.Background(), fixture("text.pdf"), Options{OCR: OCRForce, Engine: eng, OpenImages: opener(fi)})
	if err != nil {
		t.Fatal(err)
	}
	p := doc.Pages[0]
	if eng.calls != 2 || p.Source != model.SourceOCR || rep.OCRPages != 2 {
		t.Fatalf("calls=%d source=%v report=%+v", eng.calls, p.Source, rep)
	}
	if p.Width <= 0 || p.Height <= 0 {
		t.Fatalf("OCR page should carry the page size, got %.0fx%.0f", p.Width, p.Height)
	}
	if len(p.Blocks) != 3 {
		t.Fatalf("blocks = %d, want title, paragraph, page number:\n%s", len(p.Blocks), describeBlocks(p.Blocks))
	}
	if p.Blocks[0].Text() != "TITLE" || p.Blocks[0].Align != model.AlignCenter {
		t.Errorf("block 0 = %q align %v, want centred TITLE", p.Blocks[0].Text(), p.Blocks[0].Align)
	}
	if got := p.Blocks[1].Text(); got != "body text line second line" || p.Blocks[1].Leading <= 0 {
		t.Errorf("block 1 = %q leading %.1f; want the two lines joined with measured leading", got, p.Blocks[1].Leading)
	}
	if p.Blocks[2].Text() != "42" || p.Blocks[2].Align != model.AlignRight {
		t.Errorf("block 2 = %q align %v, want right-aligned page number", p.Blocks[2].Text(), p.Blocks[2].Align)
	}
	if doc.Setup == nil || doc.Setup.Width != p.Width {
		t.Errorf("document setup should come from the OCR'd page: %+v", doc.Setup)
	}
}

func TestOCRWords_FiltersNoiseAndSnapsSizes(t *testing.T) {
	mk := func(text string, left, top, w, h, lineH int, conf float64) ocr.Word {
		return ocr.Word{Text: text, Left: left, Top: top, Width: w, Height: h, LineTop: top, LineHeight: lineH, Block: 1, Par: 1, Line: top, Conf: conf}
	}
	in := []ocr.Word{
		mk("Body", 100, 100, 80, 40, 44, 95),
		mk("text", 200, 100, 80, 36, 44, 95),
		mk("here", 100, 160, 80, 42, 46, 95),        // 46 vs median 44: same size
		mk("BIG", 100, 300, 120, 60, 62, 95),        // clearly larger: own size
		mk("cccceeeecc", 400, 160, 300, 30, 46, 80), // dotted leader junk
		mk("......", 700, 160, 100, 10, 46, 80),     // dots
		mk("x", 900, 160, 20, 30, 46, 10),           // low confidence
		mk("|", 50, 50, 6, 3000, 3100, 95),          // page border: line box far too tall
	}
	got := ocrWords(in, 2550, 3300, 612, 792)
	var texts []string
	for _, w := range got {
		texts = append(texts, w.Text)
	}
	if strings.Join(texts, " ") != "Body text here BIG" {
		t.Fatalf("kept words = %q", texts)
	}
	scale := 612.0 / 2550
	// Median over words: 44,44,46,46,46,46 (junk words still count for the
	// median before they are dropped) -> 46.
	if got[0].Size != got[2].Size || !near(got[0].Size, 46*scale*ocrFontFactor, 0.01) {
		t.Errorf("body sizes should snap to the median line height: %.2f vs %.2f", got[0].Size, got[2].Size)
	}
	if !near(got[3].Size, 62*scale*ocrFontFactor, 0.01) {
		t.Errorf("BIG should keep its own size, got %.2f", got[3].Size)
	}
	if !near(got[0].X0, 100*scale, 0.01) || !near(got[0].Y1, 792-100*scale, 0.01) || !near(got[0].Y0, 792-144*scale, 0.01) {
		t.Errorf("geometry wrong: %+v", got[0])
	}
}

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestSparseAdditions(t *testing.T) {
	mk := func(text string, left, top, w, h int, conf float64) ocr.Word {
		return ocr.Word{Text: text, Left: left, Top: top, Width: w, Height: h, LineTop: top, LineHeight: h, Conf: conf}
	}
	found := []ocr.Word{mk("Body", 100, 100, 80, 40, 95), mk("text", 200, 100, 80, 40, 95)}
	sparse := []ocr.Word{
		mk("Body", 102, 101, 78, 39, 90),     // same word again: overlaps
		mk("HEADING", 400, 500, 300, 60, 88), // new: inside a picture region
		mk("x", 800, 500, 20, 30, 90),        // too short
		mk("maybe", 900, 500, 100, 30, 40),   // too uncertain
	}
	got := sparseAdditions(found, sparse)
	if len(got) != 1 || got[0].Text != "HEADING" || got[0].Block < 900000 {
		t.Fatalf("sparse additions = %+v", got)
	}
}

// sparseEngine answers the normal pass with body words and the sparse pass
// with one extra heading.
type sparseEngine struct{ wordEngine }

func (e *sparseEngine) RecognizeWordsSparse(context.Context, []byte, string) ([]ocr.Word, error) {
	return []ocr.Word{{Text: "BOXED", Left: 40, Top: 60, Width: 20, Height: 5, LineTop: 60, LineHeight: 5, Block: 7, Par: 1, Line: 1, Conf: 91}}, nil
}

func TestBuildDocument_SparsePassAddsMissedText(t *testing.T) {
	img := []pdfimage.Image{{Data: []byte("fake-png"), Ext: "png", Width: 100, Height: 130}}
	fi := &fakeImages{pages: map[int][]pdfimage.Image{1: img, 2: img}}
	without, _, err := BuildDocument(context.Background(), fixture("text.pdf"), Options{OCR: OCRForce, Engine: &sparseEngine{}, OpenImages: opener(fi)})
	if err != nil {
		t.Fatal(err)
	}
	with, _, err := BuildDocument(context.Background(), fixture("text.pdf"), Options{OCR: OCRForce, SparsePass: true, Engine: &sparseEngine{}, OpenImages: opener(fi)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(pageText(without.Pages[0]), "BOXED") {
		t.Fatal("sparse words must not appear without SparsePass")
	}
	if !strings.Contains(pageText(with.Pages[0]), "BOXED") {
		t.Fatalf("sparse pass text missing:\n%s", describeBlocks(with.Pages[0].Blocks))
	}
}

func describeBlocks(bs []model.Block) string {
	var sb strings.Builder
	for i, b := range bs {
		fmt.Fprintf(&sb, "%d: %s align=%v %q\n", i, b.Kind, b.Align, b.Text())
	}
	return sb.String()
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
