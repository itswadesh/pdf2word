package pdflayout

import (
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"pdf2word/internal/model"
)

// char is one positioned character in PDF user space (origin bottom-left,
// y grows upwards).
type char struct {
	text           string
	x0, y0, x1, y1 float64
	size           float64
	font           fontInfo
	group          int // OCR paragraph id (0 = unknown)
}

func (c char) width() float64  { return c.x1 - c.x0 }
func (c char) height() float64 { return c.y1 - c.y0 }
func (c char) isSpace() bool {
	for _, r := range c.text {
		if !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// segment is a horizontally contiguous piece of a line.
type segment struct {
	x0, x1 float64
	runs   []model.Run
}

func (s segment) text() string {
	var sb strings.Builder
	for _, r := range s.runs {
		sb.WriteString(r.Text)
	}
	return sb.String()
}

// textLine is one visual line of characters.
type textLine struct {
	chars    []char
	x0, y0   float64
	x1, y1   float64
	size     float64 // dominant font size
	bold     bool    // every non-space char bold
	group    int     // dominant OCR paragraph id (0 = unknown)
	segments []segment
}

func (l textLine) center() float64 { return (l.x0 + l.x1) / 2 }
func (l textLine) width() float64  { return l.x1 - l.x0 }

// Layout thresholds (multiples of the font size unless noted).
const (
	segmentGapFactor = 1.0  // horizontal gap that separates columns on a line
	ocrSegmentGap    = 1.6  // same for OCR words, whose spacing is noisier
	wordGapFactor    = 0.25 // gap that separates words when no space char exists
	paragraphGap     = 1.6  // vertical gap that separates paragraphs
	ocrParagraphGap  = 2.2  // same, within one OCR-detected paragraph (1.5/2.0 line spacing)
	sizeChangeRatio  = 0.15
	heading1Ratio    = 1.6
	heading2Ratio    = 1.25
	maxHeadingChars  = 200
	fullLineRatio    = 0.6 // a line this wide (of the content width) is "full"
	shortLineRatio   = 0.7 // a non-final line narrower than this gets a hard break
)

// groupLines clusters characters into lines by vertical overlap and sorts
// each line by x. Lines are returned top-to-bottom. gapFactor is the
// horizontal gap (in font sizes) that splits a line into columns.
func groupLines(chars []char, gapFactor float64) []textLine {
	if len(chars) == 0 {
		return nil
	}
	sorted := append([]char(nil), chars...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].y0 != sorted[j].y0 {
			return sorted[i].y0 > sorted[j].y0 // higher on the page first
		}
		return sorted[i].x0 < sorted[j].x0
	})

	var lines []textLine
	for _, c := range sorted {
		placed := false
		// Try the most recent lines first: overlap vertically by > 50 % of
		// the smaller height, or share a baseline within half the size.
		for i := len(lines) - 1; i >= 0 && i >= len(lines)-3; i-- {
			ln := &lines[i]
			overlap := math.Min(ln.y1, c.y1) - math.Max(ln.y0, c.y0)
			minH := math.Min(ln.y1-ln.y0, c.height())
			if (minH > 0 && overlap > 0.5*minH) || math.Abs(ln.y0-c.y0) <= 0.5*math.Max(ln.size, c.size) {
				ln.chars = append(ln.chars, c)
				ln.y0 = math.Min(ln.y0, c.y0)
				ln.y1 = math.Max(ln.y1, c.y1)
				placed = true
				break
			}
		}
		if !placed {
			lines = append(lines, textLine{chars: []char{c}, y0: c.y0, y1: c.y1, size: c.size})
		}
	}
	for i := range lines {
		finishLine(&lines[i], gapFactor)
	}
	sort.SliceStable(lines, func(i, j int) bool { return lines[i].y1 > lines[j].y1 })
	return lines
}

// finishLine sorts a line's chars, computes its extent, dominant size, and
// splits it into segments and runs.
func finishLine(ln *textLine, gapFactor float64) {
	sort.SliceStable(ln.chars, func(i, j int) bool { return ln.chars[i].x0 < ln.chars[j].x0 })
	sizes := map[float64]int{}
	ln.bold = true
	first := true
	for _, c := range ln.chars {
		if c.isSpace() {
			continue
		}
		sizes[math.Round(c.size*10)/10]++
		if !c.font.Bold {
			ln.bold = false
		}
		if first || c.x0 < ln.x0 {
			ln.x0 = c.x0
		}
		if first || c.x1 > ln.x1 {
			ln.x1 = c.x1
		}
		first = false
	}
	if first { // only spaces
		ln.x0, ln.x1 = ln.chars[0].x0, ln.chars[len(ln.chars)-1].x1
		ln.bold = false
	}
	ln.size = modeSize(sizes)
	if ln.size == 0 {
		ln.size = ln.chars[0].size
	}
	groups := map[int]int{}
	for _, c := range ln.chars {
		if c.group != 0 {
			groups[c.group]++
		}
	}
	best := 0
	for g, n := range groups {
		if n > best {
			best, ln.group = n, g
		}
	}
	ln.segments = splitSegments(ln.chars, ln.size, gapFactor)
}

func modeSize(counts map[float64]int) float64 {
	best, bestN := 0.0, -1
	for s, n := range counts {
		if n > bestN || (n == bestN && s > best) {
			best, bestN = s, n
		}
	}
	return best
}

// splitSegments breaks a sorted line into segments at wide gaps and each
// segment into runs at formatting changes. Spaces are normalised: runs of
// whitespace collapse, and a space is inserted where glyphs are apart but no
// space character exists.
func splitSegments(chars []char, size, gapFactor float64) []segment {
	if gapFactor <= 0 {
		gapFactor = segmentGapFactor
	}
	var segs []segment
	var cur *segment
	var prev *char
	pendingSpace := false

	flushRun := func(seg *segment, r *model.Run) {
		if r != nil && r.Text != "" {
			seg.runs = append(seg.runs, *r)
		}
	}
	var run *model.Run
	sameStyle := func(r *model.Run, c char) bool {
		return r != nil && r.Bold == c.font.Bold && r.Italic == c.font.Italic &&
			math.Abs(r.Size-c.size) < 0.05 && r.Font == c.font.Family
	}

	for i := range chars {
		c := &chars[i]
		if c.isSpace() {
			pendingSpace = true
			prev = c
			continue
		}
		gap := 0.0
		if prev != nil {
			gap = c.x0 - prev.x1
		}
		refSize := size
		if c.size > 0 {
			refSize = math.Max(size, c.size)
		}
		if cur == nil || gap > gapFactor*refSize {
			// New segment (also the first one).
			if cur != nil {
				flushRun(cur, run)
				segs = append(segs, *cur)
			}
			cur = &segment{x0: c.x0, x1: c.x1}
			run = nil
			pendingSpace = false
		} else if gap > wordGapFactor*refSize {
			pendingSpace = true
		}
		if !sameStyle(run, *c) {
			flushRun(cur, run)
			run = &model.Run{Bold: c.font.Bold, Italic: c.font.Italic, Size: c.size, Font: c.font.Family}
			if pendingSpace && len(cur.runs) > 0 {
				// Attach the separating space to the previous run.
				last := &cur.runs[len(cur.runs)-1]
				if !strings.HasSuffix(last.Text, " ") {
					last.Text += " "
				}
			}
			pendingSpace = false
		} else if pendingSpace {
			if !strings.HasSuffix(run.Text, " ") {
				run.Text += " "
			}
			pendingSpace = false
		}
		run.Text += c.text
		if c.x1 > cur.x1 {
			cur.x1 = c.x1
		}
		prev = c
	}
	if cur != nil {
		flushRun(cur, run)
		segs = append(segs, *cur)
	}
	// Only the last run of a segment may lose its trailing space; inner runs
	// keep the space that separates them from the next run.
	for i := range segs {
		if n := len(segs[i].runs); n > 0 {
			segs[i].runs[n-1].Text = strings.TrimRight(segs[i].runs[n-1].Text, " ")
		}
	}
	return segs
}

var listMarker = regexp.MustCompile(`^(\(?\d{1,3}[.)]|\(?[a-zA-Z][.)]|\(?[ivxlcIVXLC]{1,6}[.)]|\d+(\.\d+)+\.?|[•·▪◦\-–—*])(\s|$)`)

// looksLikeListItem reports whether a line starts with a list or clause
// marker such as "a)", "(1)", "3.", "2.1" or a bullet.
func looksLikeListItem(s string) bool {
	return listMarker.MatchString(strings.TrimSpace(s))
}

// content is the text area of a page in PDF coordinates. tol is the
// geometric tolerance for edge comparisons (larger for OCR boxes).
type content struct {
	left, right float64
	top, bottom float64
	pageW       float64
	tol         float64
}

func (c content) width() float64  { return c.right - c.left }
func (c content) center() float64 { return (c.left + c.right) / 2 }

// paragraph is a group of lines that flow together.
type paragraph struct {
	lines  []textLine
	breaks []bool // breaks[i]: hard break after line i
}

func (p paragraph) x0() float64 {
	x := math.Inf(1)
	for _, l := range p.lines {
		x = math.Min(x, l.x0)
	}
	return x
}
func (p paragraph) x1() float64 {
	x := math.Inf(-1)
	for _, l := range p.lines {
		x = math.Max(x, l.x1)
	}
	return x
}
func (p paragraph) top() float64    { return p.lines[0].y1 }
func (p paragraph) bottom() float64 { return p.lines[len(p.lines)-1].y0 }

// baseline estimates a line's baseline from its box (the bottom includes
// the descender, roughly a fifth of the size).
func baseline(l textLine) float64 { return l.y0 + 0.2*l.size }

// size returns the paragraph's largest dominant line size.
func (p paragraph) size() float64 {
	s := 0.0
	for _, l := range p.lines {
		s = math.Max(s, l.size)
	}
	return s
}

// leading is the baseline-to-baseline distance: the median of the measured
// gaps for wrapped paragraphs, 1.2 x size for single lines. It never drops
// below 1.15 x size so exact spacing in Word does not clip glyphs.
func (p paragraph) leading() float64 {
	size := p.size()
	if len(p.lines) < 2 {
		return 1.2 * size
	}
	var deltas []float64
	for i := 1; i < len(p.lines); i++ {
		if d := baseline(p.lines[i-1]) - baseline(p.lines[i]); d > 0 {
			deltas = append(deltas, d)
		}
	}
	if len(deltas) == 0 {
		return 1.2 * size
	}
	sort.Float64s(deltas)
	return math.Max(deltas[len(deltas)/2], 1.15*size)
}

// wordBox returns the vertical extent the paragraph will occupy in Word when
// laid out with exact leading: the first line box starts 0.8 leading above
// its baseline and the last ends 0.2 leading below.
func (p paragraph) wordBox() (top, bottom float64) {
	l := p.leading()
	return baseline(p.lines[0]) + 0.8*l, baseline(p.lines[len(p.lines)-1]) - 0.2*l
}

// groupParagraphs merges consecutive lines that read as one wrapped
// paragraph. Lines with several segments (columns) stay on their own.
func groupParagraphs(lines []textLine, ct content) []paragraph {
	var paras []paragraph
	for _, ln := range lines {
		if len(paras) > 0 && canMerge(&paras[len(paras)-1], ln, ct) {
			p := &paras[len(paras)-1]
			p.lines = append(p.lines, ln)
			p.breaks = append(p.breaks, false)
			continue
		}
		paras = append(paras, paragraph{lines: []textLine{ln}, breaks: []bool{false}})
	}
	// Hard breaks: a non-final line clearly shorter than the widest one.
	for pi := range paras {
		p := &paras[pi]
		widest := 0.0
		for _, l := range p.lines {
			widest = math.Max(widest, l.x1)
		}
		for i := 0; i < len(p.lines)-1; i++ {
			if p.lines[i].x1 < p.x0()+shortLineRatio*(widest-p.x0()) {
				p.breaks[i] = true
			}
		}
	}
	return paras
}

func canMerge(p *paragraph, ln textLine, ct content) bool {
	prev := p.lines[len(p.lines)-1]
	if len(prev.segments) != 1 || len(ln.segments) != 1 {
		return false
	}
	// Baseline-to-baseline distance: wrapped prose sits at ~1.2 x size,
	// paragraph gaps are clearly larger. When the OCR engine grouped both
	// lines into one paragraph, trust it up to double spacing.
	delta := prev.y0 - ln.y0
	ref := math.Max(prev.size, ln.size)
	gapLimit := paragraphGap
	if prev.group != 0 && ln.group != 0 {
		if prev.group != ln.group {
			return false
		}
		gapLimit = ocrParagraphGap
	}
	if delta <= 0.3*ref || delta > gapLimit*ref {
		return false
	}
	if math.Abs(ln.size-prev.size) > sizeChangeRatio*prev.size {
		return false
	}
	if prev.bold != ln.bold {
		return false
	}
	if looksLikeListItem(ln.segments[0].text()) {
		return false
	}
	// Left edges must agree, except that a paragraph's first line may be
	// indented (or hang) relative to the rest.
	if math.Abs(ln.x0-prev.x0) > 1.5 {
		if len(p.lines) != 1 || math.Abs(ln.x0-prev.x0) > 36 {
			return false
		}
	}
	// The previous line must be "full": wrapped prose fills its measure. A
	// short line followed by a long one is a label, list item or heading.
	full := prev.width() >= fullLineRatio*ct.width() || prev.x1 >= 0.85*ln.x1
	return full
}

// alignment infers the paragraph's alignment from its line geometry.
func alignment(p paragraph, ct content) model.Alignment {
	if ct.width() <= 0 {
		return model.AlignLeft
	}
	tol := 0.02 * ct.pageW
	edge := ct.tol
	if edge <= 0 {
		edge = tolText
	}
	centered, rightAligned, justified := true, true, 0
	minLeft, maxLeft := math.Inf(1), math.Inf(-1)
	for i, l := range p.lines {
		if math.Abs(l.center()-ct.center()) > tol || l.width() > 0.85*ct.width() {
			centered = false
		}
		if l.x1 < ct.right-edge {
			rightAligned = false
		}
		minLeft = math.Min(minLeft, l.x0)
		maxLeft = math.Max(maxLeft, l.x0)
		if i < len(p.lines)-1 && math.Abs(l.x0-ct.left) <= edge && l.x1 >= ct.right-edge {
			justified++
		}
	}
	switch {
	case centered && (len(p.lines) > 1 || p.lines[0].x0 > ct.left+0.1*ct.width()):
		return model.AlignCenter
	case rightAligned && (maxLeft-minLeft > edge || (len(p.lines) == 1 && p.lines[0].x0 > ct.left+0.2*ct.width())):
		return model.AlignRight
	case justified >= 2:
		return model.AlignJustify
	}
	return model.AlignLeft
}

// toBlock converts a paragraph into a model block positioned in the content
// box. bodySize is the page's dominant font size (for heading detection).
func toBlock(p paragraph, ct content, bodySize float64) model.Block {
	b := model.Block{Kind: model.Paragraph, Align: alignment(p, ct), Leading: p.leading()}
	px0 := p.x0()
	if b.Align == model.AlignLeft || b.Align == model.AlignJustify {
		if ind := px0 - ct.left; ind > 1.5 {
			b.IndentLeft = ind
		}
		if len(p.lines) > 1 {
			if fi := p.lines[0].x0 - px0; math.Abs(fi) > 1.5 {
				b.FirstIndent = fi
			}
		}
	}

	var cur model.Line
	started := false
	textLen := 0
	for i, l := range p.lines {
		for _, s := range l.segments {
			textLen += len(s.text())
		}
		if !started {
			cur = model.Line{}
			for _, s := range l.segments {
				cur.Segments = append(cur.Segments, toSegment(s, l, ct))
			}
			started = true
		} else {
			// Wrapped continuation: fold into the same logical line.
			cur = appendWrapped(cur, l, ct)
		}
		if i == len(p.lines)-1 || p.breaks[i] {
			b.Lines = append(b.Lines, cur)
			started = false
		}
	}

	size := p.lines[0].size
	if bodySize > 0 && textLen <= maxHeadingChars && len(p.lines) <= 3 {
		switch ratio := size / bodySize; {
		case ratio >= heading1Ratio:
			b.Kind, b.Level = model.Heading, 1
		case ratio >= heading2Ratio:
			b.Kind, b.Level = model.Heading, 2
		}
	}
	return b
}

// toSegment converts an extracted segment into a model segment positioned
// relative to the content box.
func toSegment(s segment, l textLine, ct content) model.Segment {
	seg := model.Segment{X: math.Max(0, s.x0-ct.left), Runs: append([]model.Run(nil), s.runs...)}
	if len(l.segments) > 1 {
		center := (s.x0 + s.x1) / 2
		switch {
		case s.x1 >= ct.right-ct.tol-1 && s.x0 > ct.left+0.3*ct.width():
			seg.FlushRight = true
		case math.Abs(center-ct.center()) <= 0.02*ct.pageW && s.x0 > ct.left+0.1*ct.width():
			seg.CenterX = center - ct.left
		}
	}
	return seg
}

// appendWrapped folds the next wrapped line into the current logical line:
// its first segment's runs join the last segment (with a space, or by
// de-hyphenating "docu-" + "ment"); any further segments are kept as columns.
func appendWrapped(cur model.Line, next textLine, ct content) model.Line {
	if len(next.segments) == 0 {
		return cur
	}
	if len(cur.Segments) == 0 {
		for _, s := range next.segments {
			cur.Segments = append(cur.Segments, toSegment(s, next, ct))
		}
		return cur
	}
	target := &cur.Segments[len(cur.Segments)-1]
	nextRuns := append([]model.Run(nil), next.segments[0].runs...)
	if n := len(target.Runs); n > 0 && len(nextRuns) > 0 {
		lastRun := &target.Runs[n-1]
		first := nextRuns[0]
		if strings.HasSuffix(lastRun.Text, "-") && startsLower(first.Text) {
			lastRun.Text = strings.TrimSuffix(lastRun.Text, "-")
		} else if !strings.HasSuffix(lastRun.Text, " ") {
			lastRun.Text += " "
		}
		if lastRun.Bold == first.Bold && lastRun.Italic == first.Italic && lastRun.Font == first.Font && math.Abs(lastRun.Size-first.Size) < 0.05 {
			lastRun.Text += first.Text
			nextRuns = nextRuns[1:]
		}
	}
	target.Runs = append(target.Runs, nextRuns...)
	for _, s := range next.segments[1:] {
		cur.Segments = append(cur.Segments, toSegment(s, next, ct))
	}
	return cur
}

func startsLower(s string) bool {
	for _, r := range s {
		return unicode.IsLower(r)
	}
	return false
}
