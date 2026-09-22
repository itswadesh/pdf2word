// Command pdf2word converts PDF files to Word (.docx) documents, using the
// PDF's text layer where it exists and Tesseract OCR for scanned pages.
//
// Run it with no arguments (or double-click it) to open the drag-and-drop
// page in your browser. Pass a file to convert from the command line.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"pdf2word/internal/convert"
	"pdf2word/internal/render"
	"pdf2word/internal/web"
)

// version is overridable at build time:
//
//	go build -ldflags "-X main.version=1.0.0" ./cmd/pdf2word
var version = "dev"

const usageText = `pdf2word converts PDF files to Word (.docx) documents.

Usage:
  pdf2word                          open the drag-and-drop page in your browser
  pdf2word [flags] input.pdf [output.docx]
                                    convert one file from the command line

Pages with a text layer are converted directly. Pages without one (scans,
print-to-PDF outlines) are rendered and read with Tesseract OCR, which must be
installed separately (https://github.com/tesseract-ocr/tesseract).

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
		jobs        = fs.Int("jobs", convert.DefaultJobs(), "pages to OCR at the same time")
		verbose     = fs.Bool("v", false, "verbose: one progress line per page plus diagnostics")
		noProgress  = fs.Bool("no-progress", false, "disable the progress indicator (command line)")
		addr        = fs.String("addr", "127.0.0.1:0", "address for the browser page; 0 picks a free port, 0.0.0.0:PORT shares it on the network")
		noBrowser   = fs.Bool("no-browser", false, "do not open the browser automatically")
		noAutoExit  = fs.Bool("no-auto-exit", false, "keep running after the browser page is closed (always on when shared on the network)")
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

	base := convert.Options{
		MinTextChars:  *minText,
		DPI:           *dpi,
		Jobs:          *jobs,
		Lang:          *lang,
		TesseractPath: *tess,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	rest := fs.Args()
	if len(rest) == 0 {
		remote := listensRemotely(*addr)
		return serve(ctx, serveOptions{
			addr:        *addr,
			openBrowser: !*noBrowser,
			autoExit:    !*noAutoExit && !remote,
			remote:      remote,
			base:        base,
			verbose:     *verbose,
		}, stdout, stderr)
	}
	if len(rest) > 2 {
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

	ind := newIndicator(stderr, !*noProgress, *verbose)
	opts := base
	opts.OCR = mode
	opts.OnProgress = ind.update
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

type serveOptions struct {
	addr        string
	openBrowser bool
	autoExit    bool
	remote      bool // listening on a network interface: accept any Host
	base        convert.Options
	verbose     bool
	// ready, if set, is called with the page URL once the server listens
	// (used by tests).
	ready func(url string)
}

const (
	// idleGrace is how long the page may be silent before the app assumes
	// the browser tab was closed. The page polls every few seconds.
	idleGrace = 45 * time.Second
	// neverOpenedGrace bounds how long we wait for a browser that never shows up.
	neverOpenedGrace = 5 * time.Minute
)

// serve runs the browser UI until ctx is cancelled or the page goes away.
func serve(ctx context.Context, so serveOptions, stdout, stderr io.Writer) int {
	logf := func(string, ...any) {}
	if so.verbose {
		logf = func(format string, a ...any) { fmt.Fprintf(stderr, format+"\n", a...) }
	}
	srv, err := web.New(web.Config{Base: so.base, Version: version, Logf: logf, AllowRemote: so.remote})
	if err != nil {
		fmt.Fprintf(stderr, "pdf2word: %v\n", err)
		return 1
	}
	defer srv.Close()

	ln, err := net.Listen("tcp", so.addr)
	if err != nil {
		fmt.Fprintf(stderr, "pdf2word: cannot listen on %s: %v\n", so.addr, err)
		return 1
	}
	port := ln.Addr().(*net.TCPAddr).Port
	url := fmt.Sprintf("http://%s/", ln.Addr().String())
	if so.remote || strings.HasPrefix(url, "http://[::]") || strings.HasPrefix(url, "http://0.0.0.0") {
		url = fmt.Sprintf("http://127.0.0.1:%d/", port)
	}

	httpSrv := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()

	fmt.Fprintf(stdout, "pdf2word %s\n\nOpen %s in your browser", version, url)
	if so.openBrowser {
		if err := openBrowser(url); err != nil {
			fmt.Fprintf(stdout, " (could not open it automatically: %v)", err)
		} else {
			fmt.Fprint(stdout, " (it should open by itself)")
		}
	}
	fmt.Fprintln(stdout, ".")
	if so.remote {
		fmt.Fprintln(stdout, "Other computers on your network can use:")
		for _, u := range lanURLs(port) {
			fmt.Fprintf(stdout, "  %s\n", u)
		}
		fmt.Fprintln(stdout, "Anyone who can reach these addresses can convert files; there is no login.")
	}
	if so.autoExit {
		fmt.Fprintln(stdout, "Drop PDF files on the page. This window closes on its own after the page is closed.")
	} else {
		fmt.Fprintln(stdout, "Drop PDF files on the page. Press Ctrl+C here to quit.")
	}
	if so.ready != nil {
		so.ready(url)
	}

	started := time.Now()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	code := 0
loop:
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(stdout, "\nstopping")
			break loop
		case err := <-errc:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				fmt.Fprintf(stderr, "pdf2word: server stopped: %v\n", err)
				code = 1
			}
			break loop
		case <-tick.C:
			if !so.autoExit || srv.Busy() {
				continue
			}
			if (srv.PageOpened() && srv.IdleFor() > idleGrace) || (!srv.PageOpened() && time.Since(started) > neverOpenedGrace) {
				fmt.Fprintln(stdout, "browser page closed; exiting")
				break loop
			}
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpSrv.Shutdown(shutdownCtx)
	return code
}

// listensRemotely reports whether addr binds a network interface rather than
// the loopback address only.
func listensRemotely(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	switch strings.ToLower(host) {
	case "", "0.0.0.0", "::":
		return true
	case "localhost":
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

// lanURLs lists http URLs for this machine's IPv4 addresses on the network.
func lanURLs(port int) []string {
	var urls []string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return urls
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipn.IP.To4()
		if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		urls = append(urls, fmt.Sprintf("http://%s:%d/", ip, port))
	}
	return urls
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
