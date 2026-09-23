package pdflayout

import (
	"bytes"
	"fmt"
	"image/png"
	"math"
	"path/filepath"
	"strings"
	"testing"

	"pdf2word/internal/model"
)

func fixture(name string) string {
	return filepath.Join("..", "..", "testdata", name)
}

func TestParseFont(t *testing.T) {
	cases := []struct {
		name   string
		weight int
		flags  int
		want   fontInfo
	}{
		{"Helvetica-Bold", 0, 0x20, fontInfo{"Arial", true, false, "Arial"}},
		{"Times-Roman", 0, 0x20, fontInfo{"Times New Roman", false, false, "Times New Roman"}},
		{"Times-BoldItalic", 0, 0, fontInfo{"Times New Roman", true, true, "Times New Roman"}},
		// Typefaces the reader will not have are replaced by their stand-in;
		// Windows and Office fonts keep their names.
		{"GQUSAY+BalooBhaina2-Regular", 400, 0x80020, fontInfo{"Times New Roman", false, false, "Times New Roman"}},
		{"ABCDEF+Calibri", 700, 0, fontInfo{"Calibri", true, false, "Arial"}},
		{"Arial,Italic", 0, 0, fontInfo{"Arial", false, true, "Arial"}},
		{"CourierNewPSMT", 0, 0, fontInfo{"Courier New", false, false, "Courier New"}},
		{"Verdana", 400, 1 << 6, fontInfo{"Verdana", false, true, "Arial"}},
		{"FournierMT-ItalicOsF", 310, 0x80044, fontInfo{"Times New Roman", false, true, "Times New Roman"}},
		{"AAAAAA+LiberationSerif", 435, 0x80004, fontInfo{"Times New Roman", false, false, "Times New Roman"}},
		{"LiberationSans-Bold", 700, 0x20, fontInfo{"Arial", true, false, "Arial"}},
		{"Consolas", 400, 0x1, fontInfo{"Consolas", false, false, "Courier New"}},
		{"SegoeUI-Semibold", 600, 0x20, fontInfo{"Segoe UI", true, false, "Arial"}},
		{"Garamond-Italic", 400, 0x60, fontInfo{"Garamond", false, true, "Times New Roman"}},
	}
	for _, tc := range cases {
		if got := parseFont(tc.name, tc.weight, tc.flags); got != tc.want {
			t.Errorf("parseFont(%q, %d, %#x) = %+v, want %+v", tc.name, tc.weight, tc.flags, got, tc.want)
		}
	}
}

func describe(blocks []model.Block) string {
	var sb strings.Builder
	for i, b := range blocks {
		fmt.Fprintf(&sb, "%d: %s align=%s ind=%.1f before=%.1f", i, b.Kind, b.Align, b.IndentLeft, b.SpaceBefore)
		switch b.Kind {
		case model.Table:
			fmt.Fprintf(&sb, " cols=%v rows=%d", b.Table.ColWidths, len(b.Table.Rows))
		case model.Image:
			fmt.Fprintf(&sb, " %.0fx%.0f align=%s", b.Image.Width, b.Image.Height, b.Image.Align)
		default:
			for _, l := range b.Lines {
				sb.WriteString(" |")
				for _, s := range l.Segments {
					fmt.Fprintf(&sb, " [x=%.0f fr=%v", s.X, s.FlushRight)
					for _, r := range s.Runs {
						fmt.Fprintf(&sb, " %q(%s%s%.0f %s)", r.Text, map[bool]string{true: "B", false: ""}[r.Bold], map[bool]string{true: "I", false: ""}[r.Italic], r.Size, r.Font)
					}
					sb.WriteString("]")
				}
			}
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestExtract_LayoutFixture(t *testing.T) {
	// Reflow: this test checks how wrapped lines join; line keeping is
	// covered in lines_test.go.
	doc, warns, err := ExtractWith(fixture("layout.pdf"), Options{Reflow: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings: %v", warns)
	}
	if len(doc.Pages) != 1 {
		t.Fatalf("pages = %d", len(doc.Pages))
	}
	p := doc.Pages[0]
	if p.Source != model.SourceText || !near(p.Width, 842, 0.5) || !near(p.Height, 595, 0.5) {
		t.Fatalf("page = source %v %.0fx%.0f", p.Source, p.Width, p.Height)
	}
	if doc.Setup == nil || !near(doc.Setup.MarginLeft, 36, 2) || !near(doc.Setup.MarginRight, 36, 2) || doc.Setup.Width != 842 {
		t.Errorf("setup = %+v, want landscape with ~36pt side margins", doc.Setup)
	}
	blocks := p.Blocks
	t.Logf("blocks:\n%s", describe(blocks))

	// Expected order: image line, title, subtitle, key/value, body, list a,
	// list b, table, merged-header table, footer.
	if len(blocks) != 10 {
		t.Fatalf("got %d blocks, want 10", len(blocks))
	}

	// Two images on one band become one line: centre tab + right tab.
	imgs := blocks[0]
	if imgs.Kind != model.Paragraph || len(imgs.Lines) != 1 || len(imgs.Lines[0].Segments) != 2 {
		t.Fatalf("block 0 should be a line with two pictures: %+v", imgs)
	}
	s0, s1 := imgs.Lines[0].Segments[0], imgs.Lines[0].Segments[1]
	if s0.CenterX <= 0 || len(s0.Runs) != 1 || s0.Runs[0].Image == nil || !near(s0.Runs[0].Image.Width, 60, 1) {
		t.Errorf("first picture segment = %+v, want centred 60pt image", s0)
	} else if im, err := png.Decode(bytes.NewReader(s0.Runs[0].Image.Data)); err != nil || im.Bounds().Dx() < 30 {
		t.Errorf("image data not a decodable PNG: %v", err)
	}
	if !s1.FlushRight || len(s1.Runs) != 1 || s1.Runs[0].Image == nil {
		t.Errorf("second picture segment = %+v, want flush right image", s1)
	}
	if imgs.SpaceBefore > 6 {
		t.Errorf("first block sits at the top margin; SpaceBefore = %.1f", imgs.SpaceBefore)
	}

	title := blocks[1]
	if title.Kind != model.Heading || title.Align != model.AlignCenter || title.Text() != "Form No. 25" {
		t.Errorf("title = %+v", title)
	}
	if r := title.Lines[0].Segments[0].Runs[0]; !r.Bold || !near(r.Size, 14, 0.1) || r.Font != "Arial" {
		t.Errorf("title run = %+v, want bold 14pt Arial", r)
	}
	if !near(title.Leading, 1.2*14, 0.5) {
		t.Errorf("single-line heading leading = %.1f, want 16.8", title.Leading)
	}

	if sub := blocks[2]; sub.Align != model.AlignCenter || sub.Text() != "Nil Certificate Of Encumbrance On Property" {
		t.Errorf("subtitle = %+v", sub)
	}

	kv := blocks[3]
	if len(kv.Lines) != 1 || len(kv.Lines[0].Segments) != 2 {
		t.Fatalf("key/value block = %+v", kv)
	}
	if s := kv.Lines[0].Segments; s[0].Text() != "Application No : 2026039031285" || s[1].Text() != "Certificate No : EC0392026027049" || !s[1].FlushRight || s[0].X > 1 {
		t.Errorf("key/value segments = %+v", s)
	}

	body := blocks[4]
	if len(body.Lines) != 1 || !strings.HasPrefix(body.Text(), "Having applied to me") || !strings.HasSuffix(body.Text(), "for the said property.") || body.Align != model.AlignLeft {
		t.Errorf("body paragraph should be one wrapped line: %q align=%v (lines=%d)", body.Text(), body.Align, len(body.Lines))
	}
	if !strings.Contains(body.Text(), "respect of the undermentioned") {
		t.Errorf("wrapped lines must be joined with a space: %q", body.Text())
	}
	if !near(body.Leading, 12, 0.6) {
		t.Errorf("body leading = %.2f, want the measured 12pt baseline distance", body.Leading)
	}

	if a, b := blocks[5], blocks[6]; !strings.HasPrefix(a.Text(), "a) ") || !strings.HasPrefix(b.Text(), "b) ") {
		t.Errorf("list items should be separate paragraphs: %q / %q", a.Text(), b.Text())
	}

	tbl := blocks[7]
	if tbl.Kind != model.Table {
		t.Fatalf("block 7 = %v, want table", tbl.Kind)
	}
	if len(tbl.Table.ColWidths) != 3 || len(tbl.Table.Rows) != 2 || !near(tbl.Table.ColWidths[0], 100, 2) {
		t.Errorf("table shape = cols %v rows %d", tbl.Table.ColWidths, len(tbl.Table.Rows))
	} else {
		hdr := tbl.Table.Rows[0]
		if hdr[0].Text() != "Sl. No." || hdr[1].Text() != "Village Name" || hdr[2].Text() != "Area" {
			t.Errorf("header row = %q %q %q", hdr[0].Text(), hdr[1].Text(), hdr[2].Text())
		}
		if !hdr[1].Lines[0].Segments[0].Runs[0].Bold {
			t.Error("header cell should be bold")
		}
		row := tbl.Table.Rows[1]
		if row[0].Text() != "1" || row[1].Text() != "Bhanapur - 42" || row[2].Text() != "0.0186 Hectare" {
			t.Errorf("data row = %q %q %q", row[0].Text(), row[1].Text(), row[2].Text())
		}
	}
	if !tbl.Table.Ruled || tbl.Table.BorderColor != "000000" {
		t.Errorf("table should be ruled in black, got ruled=%v color=%q", tbl.Table.Ruled, tbl.Table.BorderColor)
	}

	merged := blocks[8]
	if merged.Kind != model.Table || len(merged.Table.Rows) != 2 {
		t.Fatalf("block 8 = %v with %d rows, want a 2-row table", merged.Kind, len(merged.Table.Rows))
	}
	if hdr := merged.Table.Rows[0]; len(hdr) != 1 || hdr[0].Span != 3 || hdr[0].Text() != "Merged header" {
		t.Errorf("merged header row = %+v, want one cell spanning 3 columns", hdr)
	}
	if data := merged.Table.Rows[1]; len(data) != 3 || data[0].Text() != "1" || data[1].Text() != "Two" || data[2].Text() != "Three" {
		t.Errorf("data row = %+v", data)
	}
	if c := merged.Table.BorderColor; c != "808080" && c != "7F7F7F" {
		t.Errorf("grey rulings should give a grey border, got %q", c)
	}

	footer := blocks[9]
	if len(footer.Lines) != 1 || len(footer.Lines[0].Segments) != 2 || footer.Lines[0].Segments[1].Text() != "Page 1 of 1" || !footer.Lines[0].Segments[1].FlushRight {
		t.Errorf("footer = %+v", footer)
	}

	// Table text must not also appear in the flow.
	for i, b := range blocks {
		if b.Kind != model.Table && strings.Contains(b.Text(), "Bhanapur") {
			t.Errorf("table text leaked into block %d: %q", i, b.Text())
		}
	}
	// Spacing: the table sits below the list with a visible gap.
	if tbl.SpaceBefore <= 0 {
		t.Errorf("table SpaceBefore = %.1f, want > 0", tbl.SpaceBefore)
	}
}

func TestExtract_PlainTextFixtureStillWorks(t *testing.T) {
	doc, _, err := ExtractWith(fixture("text.pdf"), Options{Reflow: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Pages) != 2 {
		t.Fatalf("pages = %d", len(doc.Pages))
	}
	p1 := doc.Pages[0]
	t.Logf("page 1:\n%s", describe(p1.Blocks))
	if len(p1.Blocks) != 3 || p1.Blocks[0].Kind != model.Heading || p1.Blocks[0].Text() != "Quarterly Report" {
		t.Fatalf("page 1 blocks = %s", describe(p1.Blocks))
	}
	want := "This is the first paragraph of the document used to test the converter. It has three lines of text."
	if got := p1.Blocks[1].Text(); got != want {
		t.Errorf("paragraph 1 = %q\nwant %q", got, want)
	}
	if doc.Pages[1].Blocks[0].Text() != "Second page content here." {
		t.Errorf("page 2 = %s", describe(doc.Pages[1].Blocks))
	}
	if doc.Setup == nil || doc.Setup.Width != 612 || doc.Setup.Height != 792 {
		t.Errorf("setup = %+v", doc.Setup)
	}
}

func TestExtract_ScannedPageIsEmpty(t *testing.T) {
	doc, _, err := Extract(fixture("scanned.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	p := doc.Pages[0]
	// The full-page scan image is not embedded (there is no text) but the
	// page carries no text either.
	if p.TextChars() != 0 {
		t.Errorf("scanned page has text: %s", describe(p.Blocks))
	}
}

func TestAssembleOCR_SyntheticWords(t *testing.T) {
	// Letter page; words in points, y up. A centred heading, a justified
	// two-line paragraph and a right-aligned footer.
	mk := func(text string, x0, x1, top, size float64) Word {
		return Word{Text: text, X0: x0, X1: x1, Y1: top, Y0: top - 1.2*size, Size: size}
	}
	words := []Word{
		mk("BID", 260, 290, 700, 14), mk("INVITATION", 296, 352, 700, 14), // centre ≈ 306 = 612/2
		mk("The", 72, 92, 650, 11), mk("quick", 96, 126, 650, 11), mk("brown", 130, 540, 650, 11),
		mk("fox", 72, 92, 636, 11), mk("jumps", 96, 130, 636, 11), mk("over", 134, 540, 636, 11),
		mk("end.", 72, 100, 622, 11),
		mk("30", 528, 540, 60, 10),
	}
	page, setup := AssembleOCR(30, 612, 792, words, nil, Options{Reflow: true}, nil, nil)
	t.Logf("blocks:\n%s", describe(page.Blocks))
	if page.Source != model.SourceOCR || page.Number != 30 || page.Width != 612 {
		t.Fatalf("page = %+v", page)
	}
	if setup == nil || !near(setup.MarginLeft, 72, 1) || !near(setup.MarginRight, 72-measureSlack, 1) {
		t.Errorf("setup = %+v, want 72pt side margins", setup)
	}
	if len(page.Blocks) != 3 {
		t.Fatalf("got %d blocks, want 3", len(page.Blocks))
	}
	if h := page.Blocks[0]; h.Kind != model.Heading || h.Align != model.AlignCenter || h.Text() != "BID INVITATION" {
		t.Errorf("heading = %+v", h)
	}
	if p := page.Blocks[1]; p.Align != model.AlignJustify || p.Text() != "The quick brown fox jumps over end." || !near(p.Leading, 14, 0.5) {
		t.Errorf("paragraph = align %v %q leading %.1f", p.Align, p.Text(), p.Leading)
	}
	if f := page.Blocks[2]; f.Align != model.AlignRight || f.Text() != "30" {
		t.Errorf("footer = %+v", f)
	}
}

func TestExtract_MissingFile(t *testing.T) {
	if _, _, err := Extract(fixture("nope.pdf")); err == nil {
		t.Fatal("expected an error")
	}
}
