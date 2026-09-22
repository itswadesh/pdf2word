package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"pdf2word/internal/convert"
	"pdf2word/internal/model"
)

func fixture(name string) string {
	return filepath.Join("..", "..", "testdata", name)
}

func TestRun_UsageErrors(t *testing.T) {
	cases := [][]string{
		{},                                     // no input
		{"a.pdf", "b.docx", "c"},               // too many positionals
		{"-ocr", "bogus", fixture("text.pdf")}, // bad mode
	}
	for _, args := range cases {
		var out, errb bytes.Buffer
		if code := run(args, &out, &errb); code != 2 {
			t.Errorf("run(%q) = %d, want 2; stderr=%s", args, code, errb.String())
		}
	}
}

func TestRun_Version(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{"-version"}, &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "pdf2word ") {
		t.Fatalf("code=%d stdout=%q", code, out.String())
	}
}

func TestRun_ConvertsTextPDF(t *testing.T) {
	out := filepath.Join(t.TempDir(), "result.docx")
	var stdout, stderr bytes.Buffer
	code := run([]string{"-no-progress", "-o", out, fixture("text.pdf")}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(out); err != nil {
		t.Fatalf("output missing: %v", err)
	}
	if !strings.Contains(stdout.String(), "converted 2 page(s) (2 text, 0 via OCR, 0 empty)") {
		t.Errorf("unexpected summary: %q", stdout.String())
	}
}

func TestRun_DefaultOutputNextToInput(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "sample.pdf")
	data, err := os.ReadFile(fixture("text.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(in, data, 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-no-progress", in}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d, stderr=%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "sample.docx")); err != nil {
		t.Fatalf("default output missing: %v", err)
	}
}

func TestRun_MissingInputIsConversionError(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"-no-progress", "-o", filepath.Join(t.TempDir(), "x.docx"), fixture("nope.pdf")}, &stdout, &stderr)
	if code != 1 || !strings.Contains(stderr.String(), "pdf2word:") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}

func TestIndicator_VerbosePrintsOneLinePerPage(t *testing.T) {
	var buf bytes.Buffer
	ind := newIndicator(&buf, true, true)
	ind.start("in.pdf")
	ind.update(convert.Progress{Done: 1, Page: 1, Total: 2, Source: model.SourceText})
	ind.update(convert.Progress{Done: 2, Page: 2, Total: 2, Source: model.SourceOCR, OCR: true})
	ind.finish()
	want := "reading in.pdf ...\npage 1/2: text\npage 2/2: ocr\n"
	if buf.String() != want {
		t.Fatalf("got %q, want %q", buf.String(), want)
	}
}

func TestIndicator_BarRedrawsInPlace(t *testing.T) {
	var buf bytes.Buffer
	ind := &indicator{w: &buf, mode: indicatorBar}
	ind.start("in.pdf")
	ind.update(convert.Progress{Done: 1, Page: 1, Total: 2, Source: model.SourceText})
	ind.update(convert.Progress{Done: 2, Page: 2, Total: 2, Source: model.SourceOCR, OCR: true})
	ind.finish()
	got := buf.String()
	if strings.Count(got, "\r") != 3 || !strings.HasSuffix(got, "\n") {
		t.Errorf("expected three carriage returns and a trailing newline, got %q", got)
	}
	if !strings.Contains(got, "[###############---------------]  50%  page 1/2  text") {
		t.Errorf("missing 50%% bar in %q", got)
	}
	if !strings.Contains(got, "[##############################] 100%  page 2/2  ocr") {
		t.Errorf("missing 100%% bar in %q", got)
	}
}

func TestIndicator_DisabledOrNonTerminalIsSilent(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		var buf bytes.Buffer
		ind := newIndicator(&buf, enabled, false) // bytes.Buffer is not a terminal
		ind.start("in.pdf")
		ind.update(convert.Progress{Done: 1, Page: 1, Total: 1, Source: model.SourceText})
		ind.finish()
		if buf.Len() != 0 {
			t.Errorf("enabled=%v: expected no output, got %q", enabled, buf.String())
		}
	}
}
