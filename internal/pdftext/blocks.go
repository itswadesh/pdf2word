// Package pdftext extracts the text layer of a PDF and rebuilds it into
// lines, paragraphs and headings.
package pdftext

import (
	"math"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"pdf2word/internal/model"
)

// Glyph is a single positioned character from a PDF content stream.
// Coordinates are in PDF points with the origin at the bottom-left corner.
type Glyph struct {
	X, Y float64 // baseline start position
	W    float64 // advance width (0 when the PDF provides no widths)
	Size float64 // font size in points
	Font string
	S    string // the character (UTF-8)
}

// Tunable layout thresholds. All are multiples of the font size.
const (
	lineTolerance   = 0.5  // glyphs within this vertical distance share a line
	wordGapFactor   = 0.25 // horizontal gap that separates two words
	paragraphGap    = 1.6  // vertical gap that separates two paragraphs
	sizeChangeRatio = 0.15 // relative size change that separates paragraphs
	heading1Ratio   = 1.6  // block size / body size for Heading 1
	heading2Ratio   = 1.25 // block size / body size for Heading 2
	maxHeadingChars = 200  // longer blocks are never headings
	assumedGlyphW   = 0.5  // fallback advance width when W is missing
)

type line struct {
	y     float64
	size  float64 // dominant font size on the line
	text  string
	count int // non-space glyph count
}

type paragraph struct {
	text  string
	sizes map[float64]int // font size -> non-space glyph count
}

func (p *paragraph) dominantSize() float64 {
	return modeSize(p.sizes)
}

// BuildBlocks turns a page's glyphs into ordered paragraphs and headings.
// It tolerates glyphs in any order and PDFs that omit glyph widths.
func BuildBlocks(glyphs []Glyph) []model.Block {
	gs := normalize(glyphs)
	if len(gs) == 0 {
		return nil
	}
	body := bodySize(gs)
	lines := groupLines(gs)
	paras := groupParagraphs(lines)

	var blocks []model.Block
	for _, p := range paras {
		text := strings.TrimSpace(p.text)
		if text == "" {
			continue
		}
		blocks = append(blocks, classify(text, p.dominantSize(), body))
	}
	return blocks
}

// normalize drops empty glyphs, fills in missing sizes and widths, and sorts
// top-to-bottom then left-to-right.
func normalize(glyphs []Glyph) []Glyph {
	gs := make([]Glyph, 0, len(glyphs))
	for _, g := range glyphs {
		if g.S == "" {
			continue
		}
		if g.Size <= 0 {
			g.Size = 1
		}
		if g.W <= 0 {
			g.W = assumedGlyphW * g.Size
		}
		gs = append(gs, g)
	}
	sort.SliceStable(gs, func(i, j int) bool {
		if gs[i].Y != gs[j].Y {
			return gs[i].Y > gs[j].Y
		}
		return gs[i].X < gs[j].X
	})
	return gs
}

func roundSize(s float64) float64 { return math.Round(s*10) / 10 }

func isSpace(s string) bool {
	for _, r := range s {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// bodySize returns the most common font size among non-space glyphs.
func bodySize(gs []Glyph) float64 {
	counts := map[float64]int{}
	for _, g := range gs {
		if !isSpace(g.S) {
			counts[roundSize(g.Size)]++
		}
	}
	return modeSize(counts)
}

// modeSize returns the size with the highest count; ties go to the larger size.
func modeSize(counts map[float64]int) float64 {
	best, bestN := 0.0, -1
	for s, n := range counts {
		if n > bestN || (n == bestN && s > best) {
			best, bestN = s, n
		}
	}
	return best
}

// groupLines splits sorted glyphs into lines and renders each line's text,
// inserting spaces where glyphs are far enough apart.
func groupLines(gs []Glyph) []line {
	var groups [][]Glyph
	for _, g := range gs {
		if n := len(groups); n > 0 {
			cur := groups[n-1]
			ref := cur[0]
			if math.Abs(g.Y-ref.Y) <= lineTolerance*math.Max(ref.Size, g.Size) {
				groups[n-1] = append(cur, g)
				continue
			}
		}
		groups = append(groups, []Glyph{g})
	}

	lines := make([]line, 0, len(groups))
	for _, grp := range groups {
		sort.SliceStable(grp, func(i, j int) bool { return grp[i].X < grp[j].X })
		lines = append(lines, renderLine(grp))
	}
	return lines
}

func renderLine(grp []Glyph) line {
	var sb strings.Builder
	sizes := map[float64]int{}
	pendingSpace := false
	var prev *Glyph
	count := 0
	for i := range grp {
		g := &grp[i]
		if isSpace(g.S) {
			pendingSpace = true
			prev = g
			continue
		}
		if prev != nil {
			gap := g.X - (prev.X + prev.W)
			if gap > wordGapFactor*math.Max(prev.Size, g.Size) {
				pendingSpace = true
			}
		}
		if pendingSpace && sb.Len() > 0 {
			sb.WriteByte(' ')
		}
		pendingSpace = false
		sb.WriteString(g.S)
		sizes[roundSize(g.Size)]++
		count++
		prev = g
	}
	size := modeSize(sizes)
	if size == 0 {
		size = roundSize(grp[0].Size)
	}
	return line{y: grp[0].Y, size: size, text: sb.String(), count: count}
}

// groupParagraphs merges consecutive lines that are close together and share
// a font size. Hyphenated line breaks are joined back into one word.
func groupParagraphs(lines []line) []paragraph {
	var paras []paragraph
	for i, ln := range lines {
		if ln.text == "" {
			continue
		}
		startNew := len(paras) == 0
		if !startNew {
			prev := lines[i-1]
			gap := prev.y - ln.y
			ref := math.Max(prev.size, ln.size)
			sizeChanged := math.Abs(ln.size-prev.size) > sizeChangeRatio*prev.size
			startNew = gap < 0 || gap > paragraphGap*ref || sizeChanged
		}
		if startNew {
			paras = append(paras, paragraph{text: ln.text, sizes: map[float64]int{ln.size: ln.count}})
			continue
		}
		p := &paras[len(paras)-1]
		p.sizes[ln.size] += ln.count
		if strings.HasSuffix(p.text, "-") && startsLower(ln.text) {
			p.text = strings.TrimSuffix(p.text, "-") + ln.text
		} else {
			p.text += " " + ln.text
		}
	}
	return paras
}

func startsLower(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsLower(r)
}

func classify(text string, size, body float64) model.Block {
	if body > 0 && utf8.RuneCountInString(text) <= maxHeadingChars {
		switch ratio := size / body; {
		case ratio >= heading1Ratio:
			return model.HeadingBlock(1, text)
		case ratio >= heading2Ratio:
			return model.HeadingBlock(2, text)
		}
	}
	return model.Para(text)
}
