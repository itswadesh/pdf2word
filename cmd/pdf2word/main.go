// Command pdf2word converts a PDF file to a Word (.docx) document, using the
// PDF's text layer where it exists and Tesseract OCR for scanned pages.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"pdf2word/internal/convert"
	"pdf2word/internal/render"
)

// version is overridable at build time:
//
//	go build -ldflags "-X main.version=1.0.0" ./cmd/pdf2word
var version = "dev"

const usageText = `pdf2word converts a PDF to a Word (.docx) document.

Usage:
  pdf2word [flags] input.pdf [output.docx]

Pages with a text layer are converted directly. Pages that are only images
(scans) are run through Tesseract OCR, which must be installed separately
(https://github.com/tesseract-ocr/tesseract). Progress is shown on stderr.

Exit codes: 0 success, 1 conversion failed, 2 bad usage.

Flags:
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pdf2word", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		outFlag     = fs.String("o", "", "output .docx path (default: input name with .docx)")
		ocrFlag     = fs.String("ocr", "auto", "OCR mode: auto, off or force")
		lang        = fs.String("lang", "eng", "Tesseract language(s), e.g. eng or eng+deu")
		tess        = fs.String("tesseract", "", "path to the tesseract executable (default: auto-detect)")
		minText     = fs.Int("min-text", convert.DefaultMinTextChars, "text-layer characters below which a page counts as scanned")
		dpi         = fs.Int("dpi", render.DefaultDPI, "resolution used to render pages before OCR")
		verbose     = fs.Bool("v", false, "verbose: one progress line per page plus diagnostics")
		noProgress  = fs.Bool("no-progress", false, "disable the progress indicator")
		showVersion = fs.Bool("version", false, "print version and exit")
	)
	fs.Usage = func() {
		fmt.Fprint(stderr, usageText)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "pdf2word %s\n", version)
		return 0
	}

	rest := fs.Args()
	if len(rest) < 1 || len(rest) > 2 {
		fs.Usage()
		return 2
	}
	in := rest[0]
	out := *outFlag
	if out == "" && len(rest) == 2 {
		out = rest[1]
	}
	if out == "" {
		out = strings.TrimSuffix(in, filepath.Ext(in)) + ".docx"
	}
	if sameFile(in, out) {
		fmt.Fprintf(stderr, "pdf2word: output %s would overwrite the input\n", out)
		return 2
	}
	mode, err := convert.ParseOCRMode(*ocrFlag)
	if err != nil {
		fmt.Fprintf(stderr, "pdf2word: %v\n", err)
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	ind := newIndicator(stderr, !*noProgress, *verbose)
	opts := convert.Options{
		OCR:           mode,
		MinTextChars:  *minText,
		DPI:           *dpi,
		Lang:          *lang,
		TesseractPath: *tess,
		OnProgress:    ind.update,
	}
	if *verbose {
		opts.Logf = func(format string, a ...any) { ind.logf(format, a...) }
	}

	start := time.Now()
	ind.start(in)
	rep, err := convert.Convert(ctx, in, out, opts)
	ind.finish()
	if err != nil {
		fmt.Fprintf(stderr, "pdf2word: %v\n", err)
		return 1
	}
	for _, w := range rep.Warnings {
		fmt.Fprintf(stderr, "warning: %s\n", w)
	}
	fmt.Fprintf(stdout, "converted %d page(s) (%d text, %d via OCR, %d empty) in %s -> %s\n",
		rep.Pages, rep.TextPages, rep.OCRPages, rep.EmptyPages, time.Since(start).Round(time.Millisecond), out)
	return 0
}

// sameFile reports whether in and out refer to the same existing file.
func sameFile(in, out string) bool {
	si, err := os.Stat(in)
	if err != nil {
		return false
	}
	so, err := os.Stat(out)
	if err != nil {
		return false
	}
	return os.SameFile(si, so)
}
