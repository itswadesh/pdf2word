package pdfimage

import (
	"bytes"
	"path/filepath"
	"testing"
)

func fixture(name string) string {
	return filepath.Join("..", "..", "testdata", name)
}

func TestPageImages_ScannedPage(t *testing.T) {
	imgs, err := PageImages(fixture("scanned.pdf"), 1)
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
	imgs, err := PageImages(fixture("text.pdf"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(imgs) != 0 {
		t.Fatalf("got %d images, want 0", len(imgs))
	}
}

func TestPageImages_PageOutOfRange(t *testing.T) {
	if _, err := PageImages(fixture("text.pdf"), 5); err == nil {
		t.Fatal("expected an error for page 5 of a 2-page file")
	}
}

func TestPageImages_MissingFile(t *testing.T) {
	if _, err := PageImages(fixture("nope.pdf"), 1); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestPageCount(t *testing.T) {
	n, err := PageCount(fixture("text.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("page count = %d, want 2", n)
	}
}
