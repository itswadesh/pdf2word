package pdflayout

import (
	"strings"
	"testing"

	"pdf2word/internal/model"
)

// By default each line of the PDF stays a line in Word, hyphen included, so
// the page breaks where the original did. With Reflow the lines are joined
// into flowing text and the hyphenated word is repaired.
func TestExtract_KeepsLineBreaksUnlessReflow(t *testing.T) {
	doc, _, err := Extract(fixture("text.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	var first *model.Block
	for i := range doc.Pages[0].Blocks {
		if b := &doc.Pages[0].Blocks[i]; strings.HasPrefix(b.Text(), "This is the first") {
			first = b
			break
		}
	}
	if first == nil {
		t.Fatalf("first paragraph not found:\n%s", describe(doc.Pages[0].Blocks))
	}
	if len(first.Lines) != 3 {
		t.Fatalf("kept lines = %d, want 3:\n%s", len(first.Lines), describe([]model.Block{*first}))
	}
	want := []string{"This is the first paragraph of the docu-", "ment used to test the converter. It has", "three lines of text."}
	for i, l := range first.Lines {
		got := strings.TrimSpace(lineText(l))
		if got != want[i] {
			t.Errorf("line %d = %q, want %q", i, got, want[i])
		}
	}

	flowed, _, err := ExtractWith(fixture("text.pdf"), Options{Reflow: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range flowed.Pages[0].Blocks {
		if strings.HasPrefix(b.Text(), "This is the first") {
			if len(b.Lines) != 1 || !strings.Contains(b.Text(), "document used") {
				t.Errorf("reflowed paragraph = %d line(s) %q", len(b.Lines), b.Text())
			}
		}
	}
}

func lineText(l model.Line) string {
	var sb strings.Builder
	for _, s := range l.Segments {
		for _, r := range s.Runs {
			sb.WriteString(r.Text)
		}
	}
	return sb.String()
}

func TestChooseSetup(t *testing.T) {
	mk := func(l, r, tp, b float64) *model.PageSetup {
		return &model.PageSetup{Width: 612, Height: 792, MarginLeft: l, MarginRight: r, MarginTop: tp, MarginBottom: b}
	}
	if ChooseSetup(nil) != nil {
		t.Error("no setups must give nil")
	}
	// Typical book: most pages 72/72/80/90 (bottom varies with short pages),
	// one cover page with content to the edges.
	got := ChooseSetup([]*model.PageSetup{mk(72, 72, 80, 90), mk(72, 72, 80, 88), mk(74, 72, 80, 200), mk(0, 0, 0, 0), mk(72, 72, 80, 92)})
	if got == nil || !near(got.MarginLeft, 72, 0.1) || !near(got.MarginRight, 72, 0.1) || !near(got.MarginTop, 80, 0.1) || !near(got.MarginBottom, 88, 0.1) {
		t.Errorf("ChooseSetup = %+v, want 72/72/80/88 (cover ignored, smallest typical bottom)", got)
	}
	// A single page is its own typical.
	if got := ChooseSetup([]*model.PageSetup{mk(20, 30, 40, 50)}); !near(got.MarginLeft, 20, 0.1) || !near(got.MarginBottom, 50, 0.1) {
		t.Errorf("single setup = %+v", got)
	}
}

// A page whose content lies outside the document's margins keeps its own
// setup (a section of its own in Word); ordinary pages share the document's.
func TestExtract_OutlierPageGetsOwnSetup(t *testing.T) {
	doc, _, err := Extract(fixture("scaled.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Pages) != 2 || doc.Setup == nil {
		t.Fatalf("pages=%d setup=%v", len(doc.Pages), doc.Setup)
	}
	if !near(doc.Setup.MarginLeft, 72, 1) {
		t.Errorf("document left margin = %.1f, want 72 (the outlier page must not pull it in)", doc.Setup.MarginLeft)
	}
	if doc.Pages[0].Setup != nil {
		t.Errorf("page 1 should share the document setup, got %+v", doc.Pages[0].Setup)
	}
	p2 := doc.Pages[1]
	if p2.Setup == nil || !near(p2.Setup.MarginLeft, 20, 1) || p2.Setup.MarginTop > 21 {
		t.Fatalf("page 2 setup = %+v, want its own with ~20pt left margin", p2.Setup)
	}
	// Positions on page 2 are relative to its own margins: the ordinary
	// line at x=72 is indented 52 pt from the 20 pt margin.
	for _, b := range p2.Blocks {
		if strings.HasPrefix(b.Text(), "Ordinary") && !near(b.IndentLeft, 52, 1.5) {
			t.Errorf("ordinary line indent = %.1f, want 52", b.IndentLeft)
		}
	}
}
