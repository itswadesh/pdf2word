package ocr

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"pdf2word/internal/model"
)

func whitePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	_ = color.White
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

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
					t.Errorf("block %q has kind %v, want paragraph", b.Text(), b.Kind)
				}
				texts = append(texts, b.Text())
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
	oldDirs, oldBundled := knownDirs, Bundled
	knownDirs = nil
	Bundled = nil
	t.Cleanup(func() { knownDirs, Bundled = oldDirs, oldBundled })
}

func TestFind_BundledBeatsPathButNotEnv(t *testing.T) {
	isolate(t)
	bundled := stubExe(t, t.TempDir(), "tesseract")
	Bundled = func() (string, error) { return bundled, nil }

	pathDir := t.TempDir()
	onPath := stubExe(t, pathDir, "tesseract")
	t.Setenv("PATH", pathDir)
	if got, err := Find(""); err != nil || got != bundled {
		t.Fatalf("Find(\"\") = %q, %v; want bundled %q over PATH %q", got, err, bundled, onPath)
	}

	envExe := stubExe(t, t.TempDir(), "tess-env")
	t.Setenv("TESSERACT_CMD", envExe)
	if got, err := Find(""); err != nil || got != envExe {
		t.Fatalf("Find(\"\") = %q, %v; TESSERACT_CMD must override the bundle", got, err)
	}
}

func TestFind_BundleFailureFallsThrough(t *testing.T) {
	isolate(t)
	Bundled = func() (string, error) { return "", errors.New("unpack failed") }
	pathDir := t.TempDir()
	onPath := stubExe(t, pathDir, "tesseract")
	t.Setenv("PATH", pathDir)
	if got, err := Find(""); err != nil || got != onPath {
		t.Fatalf("Find(\"\") = %q, %v; want PATH copy when the bundle fails", got, err)
	}
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

func TestTesseract_VersionBadBinary(t *testing.T) {
	tess := &Tesseract{Path: filepath.Join(t.TempDir(), "nope-tesseract")}
	if _, err := tess.Version(context.Background()); err == nil {
		t.Fatal("expected an error when the binary does not exist")
	}
	if err := tess.Available(context.Background()); err == nil {
		t.Fatal("Available must fail when the binary does not exist")
	}
}

func TestTesseract_VersionReal(t *testing.T) {
	p, err := Find("")
	if err != nil {
		t.Skipf("tesseract not installed: %v", err)
	}
	v, err := (&Tesseract{Path: p}).Version(context.Background())
	if err != nil || !strings.HasPrefix(strings.ToLower(v), "tesseract") {
		t.Fatalf("Version() = %q, %v", v, err)
	}
}

func TestParseTSV(t *testing.T) {
	tsv := "level\tpage_num\tblock_num\tpar_num\tline_num\tword_num\tleft\ttop\twidth\theight\tconf\ttext\n" +
		"1\t1\t0\t0\t0\t0\t0\t0\t2550\t3300\t-1\t\n" +
		"4\t1\t1\t1\t1\t0\t100\t200\t800\t40\t-1\t\n" +
		"5\t1\t1\t1\t1\t1\t100\t205\t150\t30\t96.5\tHello\n" +
		"5\t1\t1\t1\t1\t2\t270\t200\t200\t40\t91.0\tWorld\n" +
		"5\t1\t1\t1\t1\t3\t480\t210\t20\t20\t10.0\t \n" + // blank text: skipped
		"4\t1\t1\t1\t2\t0\t100\t260\t300\t44\t-1\t\n" +
		"5\t1\t1\t1\t2\t1\t100\t262\t300\t40\t88.2\tSecond\n"
	words := parseTSV(tsv)
	if len(words) != 3 {
		t.Fatalf("got %d words: %+v", len(words), words)
	}
	if w := words[0]; w.Text != "Hello" || w.Left != 100 || w.Top != 205 || w.Width != 150 || w.Height != 30 || w.LineTop != 200 || w.LineHeight != 40 || w.Line != 1 || w.Conf != 96.5 {
		t.Errorf("word 0 = %+v", w)
	}
	if w := words[2]; w.Text != "Second" || w.LineTop != 260 || w.LineHeight != 44 || w.Line != 2 {
		t.Errorf("word 2 = %+v", w)
	}
}

func TestTesseract_RecognizeWordsReal(t *testing.T) {
	p, err := Find("")
	if err != nil {
		t.Skipf("tesseract not installed: %v", err)
	}
	// A tiny image with no text still exercises the TSV path end to end.
	img := whitePNG(t, 120, 60)
	words, err := (&Tesseract{Path: p}).RecognizeWords(context.Background(), img, "png")
	if err != nil {
		t.Fatal(err)
	}
	if len(words) != 0 {
		t.Errorf("blank image produced words: %+v", words)
	}
	var _ WordEngine = (*Tesseract)(nil)
}

func TestTesseract_Name(t *testing.T) {
	if (&Tesseract{}).Name() != "tesseract" {
		t.Fatal("unexpected engine name")
	}
}
