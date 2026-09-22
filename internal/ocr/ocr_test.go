package ocr

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"pdf2word/internal/model"
)

func TestTextToBlocks(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"blank line splits paragraphs", "Line one\nline two\n\nPara two\n", []string{"Line one line two", "Para two"}},
		{"windows line endings", "a\r\nb\r\n\r\nc", []string{"a b", "c"}},
		{"form feed separates", "first\n\f\nsecond", []string{"first", "second"}},
		{"internal whitespace collapses", "multi   space \t words\n", []string{"multi space words"}},
		{"hyphenated wrap joins", "hyphen-\nated word", []string{"hyphenated word"}},
		{"only whitespace", "   \n\n  \n", nil},
		{"empty", "", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TextToBlocks(tc.in)
			var texts []string
			for _, b := range got {
				if b.Kind != model.Paragraph {
					t.Errorf("block %q has kind %v, want paragraph", b.Text, b.Kind)
				}
				texts = append(texts, b.Text)
			}
			if !reflect.DeepEqual(texts, tc.want) {
				t.Fatalf("got %q, want %q", texts, tc.want)
			}
		})
	}
}

func stubExe(t *testing.T, dir, name string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// isolate removes every discovery source except the ones a test sets up.
func isolate(t *testing.T) {
	t.Helper()
	t.Setenv("TESSERACT_CMD", "")
	t.Setenv("PATH", t.TempDir())
	old := knownDirs
	knownDirs = nil
	t.Cleanup(func() { knownDirs = old })
}

func TestFind_ExplicitPath(t *testing.T) {
	isolate(t)
	exe := stubExe(t, t.TempDir(), "my-tess")
	got, err := Find(exe)
	if err != nil || got != exe {
		t.Fatalf("Find(%q) = %q, %v", exe, got, err)
	}
	if _, err := Find(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected an error for an explicit path that does not exist")
	}
}

func TestFind_EnvVar(t *testing.T) {
	isolate(t)
	exe := stubExe(t, t.TempDir(), "tess-env")
	t.Setenv("TESSERACT_CMD", exe)
	got, err := Find("")
	if err != nil || got != exe {
		t.Fatalf("Find(\"\") = %q, %v; want %q", got, err, exe)
	}
}

func TestFind_OnPath(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	exe := stubExe(t, dir, "tesseract")
	t.Setenv("PATH", dir)
	got, err := Find("")
	if err != nil || got != exe {
		t.Fatalf("Find(\"\") = %q, %v; want %q", got, err, exe)
	}
}

func TestFind_KnownDir(t *testing.T) {
	isolate(t)
	dir := t.TempDir()
	exe := stubExe(t, dir, "tesseract")
	knownDirs = []string{dir}
	got, err := Find("")
	if err != nil || got != exe {
		t.Fatalf("Find(\"\") = %q, %v; want %q", got, err, exe)
	}
}

func TestFind_NotFound(t *testing.T) {
	isolate(t)
	_, err := Find("")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestTesseract_Args(t *testing.T) {
	got := (&Tesseract{}).args("in.png")
	want := []string{"in.png", "stdout", "-l", "eng"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("default args = %q, want %q", got, want)
	}
	got = (&Tesseract{Lang: "eng+deu"}).args("in.tif")
	want = []string{"in.tif", "stdout", "-l", "eng+deu"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("custom args = %q, want %q", got, want)
	}
}

func TestTesseract_RecognizeBadBinary(t *testing.T) {
	tess := &Tesseract{Path: filepath.Join(t.TempDir(), "nope-tesseract")}
	_, err := tess.Recognize(context.Background(), []byte("not an image"), "png")
	if err == nil {
		t.Fatal("expected an error when the binary does not exist")
	}
	if !strings.Contains(err.Error(), "nope-tesseract") {
		t.Errorf("error should mention the binary path, got: %v", err)
	}
}

func TestTesseract_Name(t *testing.T) {
	if (&Tesseract{}).Name() != "tesseract" {
		t.Fatal("unexpected engine name")
	}
}
