package pdflayout

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"pdf2word/internal/model"
	"pdf2word/internal/pdfiumx"
)

// TestDebugLines is a diagnostic, not a check: with PDF2WORD_DEBUG_PDF set
// to a file (and PDF2WORD_DEBUG_PAGE to a 1-based page) it prints, for every
// kept line, where Word should place it (top = distance from the page top
// of its line box), the width it had in the PDF, its natural width in the
// Word font, and the fitting applied. Run with -run TestDebugLines -v.
func TestDebugLines(t *testing.T) {
	path := os.Getenv("PDF2WORD_DEBUG_PDF")
	if path == "" {
		t.Skip("set PDF2WORD_DEBUG_PDF (and PDF2WORD_DEBUG_PAGE) to use")
	}
	pageNo, _ := strconv.Atoi(os.Getenv("PDF2WORD_DEBUG_PAGE"))
	if pageNo < 1 {
		pageNo = 1
	}
	doc, _, err := Extract(path)
	if err != nil {
		t.Fatal(err)
	}
	p := doc.Pages[pageNo-1]
	setup := doc.Setup
	if p.Setup != nil {
		setup = p.Setup
	}
	fmt.Printf("page %d: %.1fx%.1f, setup %+v (own=%v), content %.1f x %.1f\n", pageNo, p.Width, p.Height, setup, p.Setup != nil, setup.ContentWidth(), setup.ContentHeight())
	y := setup.MarginTop
	for bi, b := range p.Blocks {
		y += b.SpaceBefore
		if b.Kind == model.Table || b.Kind == model.Image {
			fmt.Printf("block %d: %v at top=%.1f\n", bi, b.Kind, y)
			if b.Kind == model.Image && b.Image != nil {
				y += b.Image.Height
			}
			continue
		}
		fmt.Printf("block %d: %v align=%v indL=%.1f indR=%.1f first=%.1f before=%.1f leading=%.1f\n", bi, b.Kind, b.Align, b.IndentLeft, b.IndentRight, b.FirstIndent, b.SpaceBefore, b.Leading)
		for li, l := range b.Lines {
			var sb strings.Builder
			natural, glyphs := 0.0, 0
			spacing, scale := 0.0, 0.0
			runs := 0
			for _, s := range l.Segments {
				for _, r := range s.Runs {
					sb.WriteString(r.Text)
					fam := r.Font
					if fam == "" {
						fam = defaultBodyFont
					}
					w, _ := measureText(fam, r.Bold, r.Italic, r.Size, r.Text)
					natural += w
					glyphs += utf8.RuneCountInString(r.Text)
					spacing, scale = r.Spacing, r.Scale
					runs++
				}
			}
			avail := setup.ContentWidth() - b.IndentLeft - b.IndentRight
			if li == 0 {
				avail -= b.FirstIndent
			}
			text := sb.String()
			if utf8.RuneCountInString(text) > 34 {
				text = string([]rune(text)[:34]) + "…"
			}
			fmt.Printf("  L%d top=%6.1f natural=%.1f fitted=%.1f avail=%.1f sp=%.2f sc=%.2f runs=%d %q\n", li, y, natural, natural*scaleOr1(scale)+spacing*float64(glyphs), avail, spacing, scale, runs, text)
			y += b.Leading
		}
	}
	fmt.Printf("end of content at top=%.1f; page content ends at %.1f\n", y, setup.MarginTop+setup.ContentHeight())
}

func scaleOr1(s float64) float64 {
	if s <= 0 {
		return 1
	}
	return s
}

// TestDebugPDFLines prints the page's real text lines (top of ink from the
// page top, height, text) straight from the PDF, to set against the
// positions TestDebugLines predicts for Word.
func TestDebugPDFLines(t *testing.T) {
	path := os.Getenv("PDF2WORD_DEBUG_PDF")
	if path == "" {
		t.Skip("set PDF2WORD_DEBUG_PDF (and PDF2WORD_DEBUG_PAGE) to use")
	}
	pageNo, _ := strconv.Atoi(os.Getenv("PDF2WORD_DEBUG_PAGE"))
	if pageNo < 1 {
		pageNo = 1
	}
	d, err := pdfiumx.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	d.Mu.Lock()
	defer d.Mu.Unlock()
	raw, err := readPage(d, pageNo)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range groupLines(raw.chars, segmentGapFactor) {
		var sb strings.Builder
		for _, s := range l.segments {
			sb.WriteString(s.text())
		}
		text := sb.String()
		if utf8.RuneCountInString(text) > 30 {
			text = string([]rune(text)[:30])
		}
		fmt.Printf("  pdf inkTop=%6.1f h=%4.1f size=%4.1f base=%6.1f %q\n", raw.h-l.y1, l.y1-l.y0, l.size, raw.h-baseline(l), text)
	}
}
