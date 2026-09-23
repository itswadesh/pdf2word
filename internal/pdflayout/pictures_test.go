package pdflayout

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"pdf2word/internal/model"
)

// Real typesetters set text at "1 pt" and scale it with the text matrix,
// and place words glyph by glyph without space characters. The sizes must
// come out as drawn and the words must stay apart.
func TestExtract_ScaledTextFixture(t *testing.T) {
	doc, warns, err := Extract(fixture("scaled.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if len(warns) != 0 {
		t.Errorf("warnings: %v", warns)
	}
	blocks := doc.Pages[0].Blocks
	t.Logf("blocks:\n%s", describe(blocks))

	var heading, body, apart *model.Block
	for i := range blocks {
		b := &blocks[i]
		switch {
		case strings.HasPrefix(b.Text(), "Scaled Heading"):
			heading = b
		case strings.HasPrefix(b.Text(), "The body of this page"):
			body = b
		case strings.HasPrefix(b.Text(), "Words"):
			apart = b
		}
	}
	if heading == nil || body == nil || apart == nil {
		t.Fatalf("blocks missing: heading=%v body=%v apart=%v", heading != nil, body != nil, apart != nil)
	}
	if heading.Kind != model.Heading || !near(heading.Lines[0].Segments[0].Runs[0].Size, 18, 0.6) {
		t.Errorf("heading = kind %v size %.2f, want heading at 18 pt", heading.Kind, heading.Lines[0].Segments[0].Runs[0].Size)
	}
	if sz := body.Lines[0].Segments[0].Runs[0].Size; !near(sz, 11, 0.6) {
		t.Errorf("body size = %.2f, want 11 pt (Tf 1 scaled by the matrix)", sz)
	}
	if got := body.Text(); !strings.HasSuffix(got, "page layout programs do.") || strings.Count(got, " ") < 15 {
		t.Errorf("body text = %q", got)
	}
	if got := apart.Text(); got != "Words placed apart without spaces" {
		t.Errorf("glyph-positioned words = %q, want them separated by spaces", got)
	}
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for i := range img.Pix {
		img.Pix[i] = 0x80
	}
	img.Set(0, 0, color.RGBA{200, 30, 30, 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A page-sized image is the scan of a text page only when OCR finds text on
// it. Otherwise it is a picture and belongs in the document.
func TestAssembleOCR_PicturePages(t *testing.T) {
	const w, h = 612.0, 792.0
	pngData := tinyPNG(t)
	fullPage := placedImage{x0: 20, y0: 20, x1: w - 20, y1: h - 20, data: pngData}   // 94 % of the page
	twoThirds := placedImage{x0: 72, y0: 300, x1: w - 72, y1: h - 72, data: pngData} // 45 % of the page area... see below
	twoThirds.y0 = 150                                                               // 468 x 642 = 62 %
	word := func(text string, x0, top, size float64) Word {
		return Word{Text: text, X0: x0, X1: x0 + 5*size, Y1: top, Y0: top - 1.2*size, Size: size}
	}
	manyWords := func(n int) []Word {
		var ws []Word
		for i := 0; i < n; i++ {
			ws = append(ws, word("word", 72+float64(i%8)*60, h-100-float64(i/8)*14, 11))
		}
		return ws
	}
	countKind := func(blocks []model.Block, k model.BlockKind) int {
		n := 0
		for _, b := range blocks {
			if b.Kind == k {
				n++
			}
		}
		return n
	}

	isPicture := func() bool { return true }
	isScan := func() bool { return false }
	notAsked := func() bool { t.Error("the picture test must not run when the word count decides"); return false }

	// 1. Full-page picture, no words at all: the picture is the page.
	page, _ := AssembleOCR(1, w, h, nil, &PageAssets{Width: w, Height: h, images: []placedImage{fullPage}}, Options{}, nil, notAsked)
	if countKind(page.Blocks, model.Image) != 1 || len(page.Blocks) != 1 {
		t.Errorf("picture-only page: %s", describe(page.Blocks))
	}
	if page.Blocks[0].Image == nil || !near(page.Blocks[0].Image.Width, w-40, 0.5) {
		t.Errorf("picture block = %+v", page.Blocks[0].Image)
	}

	// 2. Full-page picture with a handful of words (a cover): picture kept,
	// the words dropped.
	cover := []Word{word("The", 200, 500, 30), word("Novel", 300, 500, 30), word("Author", 220, 300, 18)}
	page, _ = AssembleOCR(1, w, h, cover, &PageAssets{Width: w, Height: h, images: []placedImage{fullPage}}, Options{}, nil, isPicture)
	if countKind(page.Blocks, model.Image) != 1 || page.TextChars() != 0 {
		t.Errorf("cover page: %s", describe(page.Blocks))
	}

	// 3. The same few words on a mostly-white page: a short scanned note.
	// The words stay, the scan goes.
	page, _ = AssembleOCR(1, w, h, cover, &PageAssets{Width: w, Height: h, images: []placedImage{fullPage}}, Options{}, nil, isScan)
	if countKind(page.Blocks, model.Image) != 0 || page.TextChars() == 0 {
		t.Errorf("short scanned note: %s", describe(page.Blocks))
	}

	// 4. Full-page image with a page of text on it: a scan, whatever the
	// tones say. Text only.
	page, _ = AssembleOCR(1, w, h, manyWords(60), &PageAssets{Width: w, Height: h, images: []placedImage{fullPage}}, Options{}, nil, notAsked)
	if countKind(page.Blocks, model.Image) != 0 || page.TextChars() == 0 {
		t.Errorf("scanned page: %s", describe(page.Blocks))
	}

	// 5. A picture on two thirds of the page with a short caption below it:
	// both kept.
	page, _ = AssembleOCR(1, w, h, []Word{word("Figure", 72, 120, 10), word("one", 130, 120, 10)},
		&PageAssets{Width: w, Height: h, images: []placedImage{twoThirds}}, Options{}, nil, isPicture)
	if countKind(page.Blocks, model.Image) != 1 || page.TextChars() == 0 {
		t.Errorf("illustration with caption: %s", describe(page.Blocks))
	}

	// 6. Words read inside the picture (a logo's lettering) belong to the
	// picture and are not repeated as text.
	page, _ = AssembleOCR(1, w, h, []Word{word("PUFFIN", 250, 400, 14), word("Figure", 72, 120, 10)},
		&PageAssets{Width: w, Height: h, images: []placedImage{twoThirds}}, Options{}, nil, isPicture)
	if countKind(page.Blocks, model.Image) != 1 || page.TextChars() != len("Figure") {
		t.Errorf("lettering inside the picture should be dropped, caption kept: %s", describe(page.Blocks))
	}
}

// Text on paper is near-white and near-black; pictures have mid-tones.
func TestLooksLikePicture(t *testing.T) {
	encode := func(img image.Image) []byte {
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	// A "scan": white page with black text-like bars.
	scan := image.NewGray(image.Rect(0, 0, 200, 260))
	for i := range scan.Pix {
		scan.Pix[i] = 0xFF
	}
	for row := 20; row < 240; row += 12 {
		for y := row; y < row+6; y++ {
			for x := 20; x < 180; x++ {
				if (x/3)%2 == 0 {
					scan.SetGray(x, y, color.Gray{0x10})
				}
			}
		}
	}
	if LooksLikePicture(encode(scan)) {
		t.Error("a black-on-white text scan was taken for a picture")
	}
	// A "cover": a gradient.
	cover := image.NewGray(image.Rect(0, 0, 200, 260))
	for y := 0; y < 260; y++ {
		for x := 0; x < 200; x++ {
			cover.SetGray(x, y, color.Gray{uint8(40 + (x+y)*180/460)})
		}
	}
	if !LooksLikePicture(encode(cover)) {
		t.Error("a shaded picture was taken for a text scan")
	}
	if LooksLikePicture([]byte("not an image")) {
		t.Error("undecodable data must count as text")
	}
}
