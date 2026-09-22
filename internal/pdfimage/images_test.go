package pdfimage

import (
	"bytes"
	"path/filepath"
	"strconv"
	"testing"
)

func fixture(name string) string {
	return filepath.Join("..", "..", "testdata", name)
}

func openFixture(t *testing.T, name string) *Reader {
	t.Helper()
	r, err := Open(fixture(name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestPageImages_ScannedPage(t *testing.T) {
	r := openFixture(t, "scanned.pdf")
	if r.PageCount() != 1 {
		t.Fatalf("page count = %d, want 1", r.PageCount())
	}
	imgs, err := r.PageImages(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 1 {
		t.Fatalf("got %d images, want 1", len(imgs))
	}
	img := imgs[0]
	if img.Ext != "png" {
		t.Errorf("ext = %q, want png", img.Ext)
	}
	if img.Width != 1650 || img.Height != 2200 {
		t.Errorf("size = %dx%d, want 1650x2200", img.Width, img.Height)
	}
	if !bytes.HasPrefix(img.Data, []byte("\x89PNG\r\n\x1a\n")) {
		t.Errorf("data does not start with the PNG signature: % x", img.Data[:8])
	}
}

func TestPageImages_TextPageHasNone(t *testing.T) {
	r := openFixture(t, "text.pdf")
	if r.PageCount() != 2 {
		t.Fatalf("page count = %d, want 2", r.PageCount())
	}
	imgs, err := r.PageImages(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 0 {
		t.Fatalf("got %d images, want 0", len(imgs))
	}
}

func TestPageImages_PageOutOfRange(t *testing.T) {
	r := openFixture(t, "text.pdf")
	if _, err := r.PageImages(5); err == nil {
		t.Fatal("expected an error for page 5 of a 2-page file")
	}
	if _, err := r.PageImages(0); err == nil {
		t.Fatal("expected an error for page 0")
	}
}

// A malformed /Redact annotation (/OC given as an array) must not prevent
// image extraction; strict validation rejected a real 247-page file this way.
func TestOpen_ToleratesInvalidAnnotation(t *testing.T) {
	r := openFixture(t, "badannot.pdf")
	if r.PageCount() != 1 {
		t.Fatalf("page count = %d, want 1", r.PageCount())
	}
	imgs, err := r.PageImages(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 1 || imgs[0].Width != 60 || imgs[0].Height != 60 {
		t.Fatalf("got %d images %+v, want one 60x60 image", len(imgs), summarize(imgs))
	}
}

func summarize(imgs []Image) []string {
	var out []string
	for _, im := range imgs {
		out = append(out, im.Ext+" "+itoa(im.Width)+"x"+itoa(im.Height))
	}
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }

func TestOpen_MissingFile(t *testing.T) {
	if _, err := Open(fixture("nope.pdf")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestUsable(t *testing.T) {
	cases := []struct {
		img  Image
		want bool
	}{
		{Image{Width: 49, Height: 500}, false},
		{Image{Width: 500, Height: 49}, false},
		{Image{Width: 50, Height: 50}, true},
		{Image{Width: 1650, Height: 2200}, true},
		{Image{}, true}, // unknown size: keep it
	}
	for _, tc := range cases {
		if got := usable(tc.img); got != tc.want {
			t.Errorf("usable(%dx%d) = %v, want %v", tc.img.Width, tc.img.Height, got, tc.want)
		}
	}
}

func TestSniffSize(t *testing.T) {
	r := openFixture(t, "scanned.pdf")
	imgs, err := r.PageImages(1)
	if err != nil || len(imgs) != 1 {
		t.Fatalf("PageImages = %d images, %v", len(imgs), err)
	}
	if w, h := sniffSize(imgs[0].Data); w != 1650 || h != 2200 {
		t.Errorf("sniffSize = %dx%d, want 1650x2200", w, h)
	}
	if w, h := sniffSize([]byte("garbage")); w != 0 || h != 0 {
		t.Errorf("sniffSize(garbage) = %dx%d, want 0x0", w, h)
	}
}
