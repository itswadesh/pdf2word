package render

import (
	"bytes"
	"image/png"
	"path/filepath"
	"testing"
)

func fixture(name string) string {
	return filepath.Join("..", "..", "testdata", name)
}

func open(t *testing.T, name string, dpi int) *Renderer {
	t.Helper()
	r, err := Open(fixture(name), dpi)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

// darkFraction decodes a PNG and returns the share of pixels darker than mid-grey.
func darkFraction(t *testing.T, data []byte) float64 {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("output is not a PNG: %v", err)
	}
	b := img.Bounds()
	dark, total := 0, 0
	for y := b.Min.Y; y < b.Max.Y; y += 4 {
		for x := b.Min.X; x < b.Max.X; x += 4 {
			r, g, bl, _ := img.At(x, y).RGBA()
			if (r+g+bl)/3 < 0x8000 {
				dark++
			}
			total++
		}
	}
	return float64(dark) / float64(total)
}

func TestOpen_TextPDF(t *testing.T) {
	r := open(t, "text.pdf", 100)
	if r.PageCount() != 2 {
		t.Fatalf("page count = %d, want 2", r.PageCount())
	}
	imgs, err := r.PageImages(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 1 || imgs[0].Ext != "png" {
		t.Fatalf("got %d images (%+v), want one png", len(imgs), imgs)
	}
	// 612x792 pt at 100 dpi -> 850x1100 px
	if imgs[0].Width != 850 || imgs[0].Height != 1100 {
		t.Errorf("size = %dx%d, want 850x1100", imgs[0].Width, imgs[0].Height)
	}
	frac := darkFraction(t, imgs[0].Data)
	if frac < 0.002 || frac > 0.2 {
		t.Errorf("page 1 dark pixel fraction = %.4f; expected some text, mostly white", frac)
	}
}

func TestOpen_DefaultDPI(t *testing.T) {
	r := open(t, "text.pdf", 0)
	if r.DPI() != DefaultDPI {
		t.Fatalf("dpi = %d, want %d", r.DPI(), DefaultDPI)
	}
}

func TestPageImages_OutOfRange(t *testing.T) {
	r := open(t, "text.pdf", 72)
	if _, err := r.PageImages(3); err == nil {
		t.Fatal("expected an error for page 3 of a 2-page file")
	}
	if _, err := r.PageImages(0); err == nil {
		t.Fatal("expected an error for page 0")
	}
}

func TestOpen_ScannedAndMalformedFiles(t *testing.T) {
	for _, name := range []string{"scanned.pdf", "badannot.pdf"} {
		t.Run(name, func(t *testing.T) {
			r := open(t, name, 72)
			if r.PageCount() != 1 {
				t.Errorf("page count = %d, want 1", r.PageCount())
			}
			imgs, err := r.PageImages(1)
			if err != nil {
				t.Fatal(err)
			}
			if frac := darkFraction(t, imgs[0].Data); frac < 0.001 {
				t.Errorf("rendered page is blank (dark fraction %.4f)", frac)
			}
		})
	}
}

func TestOpen_SeveralDocumentsAtOnce(t *testing.T) {
	a := open(t, "text.pdf", 72)
	b := open(t, "scanned.pdf", 72)
	if _, err := a.PageImages(1); err != nil {
		t.Fatal(err)
	}
	if _, err := b.PageImages(1); err != nil {
		t.Fatal(err)
	}
}

func TestOpen_MissingFile(t *testing.T) {
	if _, err := Open(fixture("nope.pdf"), 72); err == nil {
		t.Fatal("expected an error")
	}
}

func TestOpen_NotAPDF(t *testing.T) {
	if _, err := Open(fixture("../go.mod"), 72); err == nil {
		t.Fatal("expected an error for a non-PDF file")
	}
}
