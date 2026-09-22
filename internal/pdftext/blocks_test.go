package pdftext

import (
	"math/rand"
	"reflect"
	"testing"

	"pdf2word/internal/model"
)

// glyphs lays out s one glyph per rune starting at (x, y), advancing by half
// the font size per glyph, which is roughly Helvetica's average width.
func glyphs(x, y, size float64, s string) []Glyph {
	var gs []Glyph
	for _, r := range s {
		gs = append(gs, Glyph{X: x, Y: y, W: 0.5 * size, Size: size, Font: "Helvetica", S: string(r)})
		x += 0.5 * size
	}
	return gs
}

func concat(parts ...[]Glyph) []Glyph {
	var all []Glyph
	for _, p := range parts {
		all = append(all, p...)
	}
	return all
}

func texts(blocks []model.Block) []string {
	out := make([]string, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, b.Text)
	}
	return out
}

func TestBuildBlocks_EmptyInput(t *testing.T) {
	if got := BuildBlocks(nil); got != nil {
		t.Fatalf("expected nil, got %#v", got)
	}
}

func TestBuildBlocks_WordGapInsertsSpace(t *testing.T) {
	// "Hello" occupies x 0..25 at size 10; "World" starts 6pt later (> 0.25*10).
	in := concat(glyphs(0, 100, 10, "Hello"), glyphs(31, 100, 10, "World"))
	got := texts(BuildBlocks(in))
	want := []string{"Hello World"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildBlocks_TouchingGlyphsDoNotSplit(t *testing.T) {
	in := concat(glyphs(0, 100, 10, "Hel"), glyphs(15, 100, 10, "lo"))
	got := texts(BuildBlocks(in))
	want := []string{"Hello"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildBlocks_ExplicitSpaceGlyphs(t *testing.T) {
	in := glyphs(0, 100, 10, "Hello   World")
	got := texts(BuildBlocks(in))
	want := []string{"Hello World"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildBlocks_ZeroWidthGlyphsStillFormWords(t *testing.T) {
	// PDFs without /Widths give W=0; positions still advance.
	in := glyphs(0, 100, 10, "Hello World")
	for i := range in {
		in[i].W = 0
	}
	got := texts(BuildBlocks(in))
	want := []string{"Hello World"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildBlocks_LinesWithinLeadingMerge(t *testing.T) {
	in := concat(
		glyphs(72, 700, 11, "line one"),
		glyphs(72, 686, 11, "line two"),
	)
	got := texts(BuildBlocks(in))
	want := []string{"line one line two"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildBlocks_LargeGapStartsNewParagraph(t *testing.T) {
	in := concat(
		glyphs(72, 700, 11, "first para"),
		glyphs(72, 672, 11, "second para"),
	)
	got := texts(BuildBlocks(in))
	want := []string{"first para", "second para"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildBlocks_DehyphenatesLineBreaks(t *testing.T) {
	in := concat(
		glyphs(72, 700, 11, "part of the docu-"),
		glyphs(72, 686, 11, "ment used here"),
	)
	got := texts(BuildBlocks(in))
	want := []string{"part of the document used here"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestBuildBlocks_HeadingLevelsByFontSize(t *testing.T) {
	in := concat(
		glyphs(72, 720, 24, "Title"),
		glyphs(72, 690, 14, "Subtitle"),
		glyphs(72, 660, 11, "body text line one of the page"),
		glyphs(72, 646, 11, "body text line two of the page"),
		glyphs(72, 632, 11, "body text line three of page"),
	)
	got := BuildBlocks(in)
	if len(got) != 3 {
		t.Fatalf("expected 3 blocks, got %d: %q", len(got), texts(got))
	}
	if got[0].Kind != model.Heading || got[0].Level != 1 || got[0].Text != "Title" {
		t.Errorf("block 0 = %+v, want Heading level 1 'Title'", got[0])
	}
	if got[1].Kind != model.Heading || got[1].Level != 2 || got[1].Text != "Subtitle" {
		t.Errorf("block 1 = %+v, want Heading level 2 'Subtitle'", got[1])
	}
	if got[2].Kind != model.Paragraph || got[2].Level != 0 {
		t.Errorf("block 2 = %+v, want Paragraph", got[2])
	}
}

func TestBuildBlocks_LongTextIsNeverAHeading(t *testing.T) {
	long := ""
	for i := 0; i < 30; i++ {
		long += "big words "
	}
	in := concat(
		glyphs(72, 720, 20, long), // > 200 chars at a large size
		glyphs(72, 680, 11, "body text line one of the page"),
		glyphs(72, 666, 11, "body text line two of the page"),
		glyphs(72, 652, 11, "body text line three of page"),
		glyphs(72, 638, 11, "body text line four of the page"),
		glyphs(72, 624, 11, "body text line five of the page"),
		glyphs(72, 610, 11, "body text line six of the page"),
		glyphs(72, 596, 11, "body text line seven of the page"),
		glyphs(72, 582, 11, "body text line eight of the page"),
		glyphs(72, 568, 11, "body text line nine of the page"),
		glyphs(72, 554, 11, "body text line ten of the page"),
		glyphs(72, 540, 11, "body text line eleven of the page"),
	)
	got := BuildBlocks(in)
	if len(got) == 0 || got[0].Kind != model.Paragraph {
		t.Fatalf("expected first block to be a paragraph, got %+v", got)
	}
}

func TestBuildBlocks_OrderIndependent(t *testing.T) {
	ordered := concat(
		glyphs(72, 720, 24, "Title"),
		glyphs(72, 680, 11, "alpha beta gamma delta"),
		glyphs(72, 666, 11, "epsilon zeta eta theta"),
		glyphs(72, 638, 11, "new paragraph here"),
	)
	want := BuildBlocks(ordered)

	shuffled := append([]Glyph(nil), ordered...)
	rand.New(rand.NewSource(42)).Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	got := BuildBlocks(shuffled)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("shuffled input produced %q, ordered produced %q", texts(got), texts(want))
	}
	if want[0].Text != "Title" || want[1].Text != "alpha beta gamma delta epsilon zeta eta theta" || want[2].Text != "new paragraph here" {
		t.Fatalf("unexpected block texts: %q", texts(want))
	}
}
