package pdftext

import (
	"os"
	"path/filepath"
	"testing"

	"pdf2word/internal/model"
)

func fixture(name string) string {
	return filepath.Join("..", "..", "testdata", name)
}

func TestExtract_TextPDF(t *testing.T) {
	doc, warns, err := Extract(fixture("text.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Errorf("unexpected warnings: %v", warns)
	}
	if len(doc.Pages) != 2 {
		t.Fatalf("pages = %d, want 2", len(doc.Pages))
	}

	p1 := doc.Pages[0]
	if p1.Number != 1 || p1.Source != model.SourceText {
		t.Errorf("page 1 = number %d source %v, want 1/text", p1.Number, p1.Source)
	}
	if len(p1.Blocks) != 3 {
		t.Fatalf("page 1 has %d blocks, want 3: %+v", len(p1.Blocks), p1.Blocks)
	}
	if b := p1.Blocks[0]; b.Kind != model.Heading || b.Level != 1 || b.Text != "Quarterly Report" {
		t.Errorf("block 0 = %+v, want Heading 1 'Quarterly Report'", b)
	}
	wantPara1 := "This is the first paragraph of the document used to test the converter. It has three lines of text."
	if b := p1.Blocks[1]; b.Kind != model.Paragraph || b.Text != wantPara1 {
		t.Errorf("block 1 = %+v\nwant paragraph %q", b, wantPara1)
	}
	wantPara2 := "The second paragraph starts after a larger vertical gap and also spans several lines on the page."
	if b := p1.Blocks[2]; b.Kind != model.Paragraph || b.Text != wantPara2 {
		t.Errorf("block 2 = %+v\nwant paragraph %q", b, wantPara2)
	}

	p2 := doc.Pages[1]
	if p2.Number != 2 || p2.Source != model.SourceText {
		t.Errorf("page 2 = number %d source %v, want 2/text", p2.Number, p2.Source)
	}
	if len(p2.Blocks) != 1 || p2.Blocks[0].Text != "Second page content here." {
		t.Errorf("page 2 blocks = %+v, want one paragraph 'Second page content here.'", p2.Blocks)
	}
}

func TestExtract_ScannedPDFHasNoText(t *testing.T) {
	doc, _, err := Extract(fixture("scanned.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Pages) != 1 {
		t.Fatalf("pages = %d, want 1", len(doc.Pages))
	}
	p := doc.Pages[0]
	if p.Source != model.SourceEmpty || p.TextChars() != 0 || len(p.Blocks) != 0 {
		t.Errorf("scanned page = %+v, want empty", p)
	}
}

func TestExtract_MissingFile(t *testing.T) {
	if _, _, err := Extract(fixture("does-not-exist.pdf")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestExtract_NotAPDF(t *testing.T) {
	p := filepath.Join(t.TempDir(), "junk.pdf")
	if err := os.WriteFile(p, []byte("this is not a pdf"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Extract(p); err == nil {
		t.Fatal("expected an error for a non-PDF file")
	}
}
