//go:build windows

package tessbundle

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"pdf2word/internal/ocr"
	"pdf2word/internal/pdfimage"
)

func TestPath_UnpacksOnceAndRuns(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())

	exe, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(exe) != "tesseract.exe" || !fileExists(exe) {
		t.Fatalf("Path() = %q, want an existing tesseract.exe", exe)
	}
	if !strings.HasPrefix(exe, Dir()) {
		t.Errorf("exe %q is not under Dir() %q", exe, Dir())
	}
	for _, lang := range []string{"eng", "ori"} {
		if !fileExists(filepath.Join(Dir(), "tessdata", lang+".traineddata")) {
			t.Errorf("%s.traineddata missing from the unpacked bundle", lang)
		}
	}

	// A second call must reuse the unpacked copy, not rewrite it.
	stamp := filepath.Join(Dir(), stampName)
	before, _ := os.Stat(stamp)
	exe2, err := Path()
	if err != nil || exe2 != exe {
		t.Fatalf("second Path() = %q, %v", exe2, err)
	}
	after, _ := os.Stat(stamp)
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("bundle was unpacked again although it was already present")
	}

	out, err := exec.Command(exe, "--version").CombinedOutput()
	if err != nil || !strings.Contains(strings.ToLower(string(out)), "tesseract") {
		t.Fatalf("bundled tesseract --version failed: %v\n%s", err, out)
	}
}

func TestBundledTesseractRecognisesText(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	exe, err := Path()
	if err != nil {
		t.Fatal(err)
	}
	r, err := pdfimage.Open(filepath.Join("..", "..", "testdata", "scanned.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	imgs, err := r.PageImages(1)
	if err != nil || len(imgs) != 1 {
		t.Fatalf("fixture image: %v", err)
	}
	text, err := (&ocr.Tesseract{Path: exe}).Recognize(context.Background(), imgs[0].Data, imgs[0].Ext)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToLower(text), "scanned") || !strings.Contains(strings.ToLower(text), "lazy dog") {
		t.Errorf("unexpected OCR output: %q", text)
	}
}
