package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"pdf2word/internal/convert"
	"pdf2word/internal/model"
)

type indicatorMode int

const (
	indicatorOff   indicatorMode = iota
	indicatorBar                 // single line redrawn in place (interactive terminal)
	indicatorLines               // one line per page (verbose or non-terminal)
)

const barWidth = 30

// indicator renders conversion progress on stderr.
//
// On an interactive terminal it draws a bar that is redrawn in place:
//
//	[############------------------]  40%  page 4/10  ocr
//
// With -v (where diagnostics are interleaved) it prints one line per page
// instead, so nothing gets overwritten. When stderr is not a terminal and -v
// is not set, it stays quiet apart from the final summary.
type indicator struct {
	w       io.Writer
	mode    indicatorMode
	lastLen int
}

func newIndicator(w io.Writer, enabled, verbose bool) *indicator {
	ind := &indicator{w: w}
	switch {
	case !enabled:
		ind.mode = indicatorOff
	case verbose:
		ind.mode = indicatorLines
	case isTerminal(w):
		ind.mode = indicatorBar
	default:
		ind.mode = indicatorOff
	}
	return ind
}

func (ind *indicator) start(input string) {
	switch ind.mode {
	case indicatorBar:
		ind.redraw(fmt.Sprintf("reading %s ...", input))
	case indicatorLines:
		fmt.Fprintf(ind.w, "reading %s ...\n", input)
	}
}

func (ind *indicator) update(p convert.Progress) {
	label := sourceLabel(p)
	switch ind.mode {
	case indicatorBar:
		filled := 0
		pct := 0
		if p.Total > 0 {
			filled = barWidth * p.Page / p.Total
			pct = 100 * p.Page / p.Total
		}
		bar := strings.Repeat("#", filled) + strings.Repeat("-", barWidth-filled)
		ind.redraw(fmt.Sprintf("[%s] %3d%%  page %d/%d  %s", bar, pct, p.Page, p.Total, label))
	case indicatorLines:
		fmt.Fprintf(ind.w, "page %d/%d: %s\n", p.Page, p.Total, label)
	}
}

// logf prints a diagnostic line without corrupting the bar.
func (ind *indicator) logf(format string, a ...any) {
	if ind.mode == indicatorBar && ind.lastLen > 0 {
		fmt.Fprint(ind.w, "\r", strings.Repeat(" ", ind.lastLen), "\r")
		ind.lastLen = 0
	}
	fmt.Fprintf(ind.w, format+"\n", a...)
}

func (ind *indicator) finish() {
	if ind.mode == indicatorBar && ind.lastLen > 0 {
		fmt.Fprint(ind.w, "\n")
		ind.lastLen = 0
	}
}

// redraw rewrites the current line, padding so a shorter line fully covers
// the previous one.
func (ind *indicator) redraw(line string) {
	pad := ind.lastLen - len(line)
	if pad < 0 {
		pad = 0
	}
	fmt.Fprint(ind.w, "\r", line, strings.Repeat(" ", pad))
	ind.lastLen = len(line)
}

func sourceLabel(p convert.Progress) string {
	switch {
	case p.Source == model.SourceOCR:
		return "ocr"
	case p.Source == model.SourceText:
		return "text"
	case p.OCR:
		return "ocr (no text found)"
	default:
		return "empty"
	}
}

// isTerminal reports whether w is an interactive character device.
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	st, err := f.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}
