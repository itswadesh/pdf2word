// Package pdflayout extracts a PDF's text layer with its layout, using
// PDFium: characters with fonts and positions, table rulings drawn as paths,
// and placed images. It produces model pages with paragraphs, headings,
// alignment, tab-separated columns, tables and pictures.
//
// Pages without a text layer keep their rulings and images as PageAssets so
// that OCR output (word boxes) can be laid out the same way via AssembleOCR.
package pdflayout

import (
	"fmt"
	"math"
	"sort"

	"github.com/klippa-app/go-pdfium/requests"

	"pdf2word/internal/model"
	"pdf2word/internal/pdfiumx"
)

// Warning describes a non-fatal problem encountered on one page.
type Warning struct {
	Page int
	Msg  string
}

func (w Warning) String() string { return fmt.Sprintf("page %d: %s", w.Page, w.Msg) }

// Margin limits for the derived page setup, in points.
const (
	minMargin = 21.6 // 0.3 in
	maxMargin = 90.0 // 1.25 in
	maxGap    = 400  // pt: largest vertical gap reproduced between blocks
)

// Geometry tolerances in points: PDFium positions are exact, OCR boxes are not.
const (
	tolText = 2.0
	tolOCR  = 5.0
)

// PageAssets holds what a page offers besides its text layer: size, table
// rulings and images. Kept for pages that need OCR.
type PageAssets struct {
	Width, Height float64
	rules         []rule
	images        []placedImage
}

// Result is the outcome of ExtractAll.
type Result struct {
	Doc      *model.Document
	Warnings []Warning
	// Assets by 1-based page number, for pages that have no text layer.
	Assets map[int]*PageAssets
}

// Extract reads every page of the PDF at path.
func Extract(path string) (*model.Document, []Warning, error) {
	res, err := ExtractAll(path)
	if err != nil {
		return nil, nil, err
	}
	return res.Doc, res.Warnings, nil
}

// ExtractAll reads every page and also returns the assets of text-less pages.
func ExtractAll(path string) (*Result, error) {
	d, err := pdfiumx.Open(path)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	return extractDoc(d)
}

func extractDoc(d *pdfiumx.Doc) (*Result, error) {
	d.Mu.Lock()
	defer d.Mu.Unlock()

	res := &Result{Doc: &model.Document{}, Assets: map[int]*PageAssets{}}
	var setup *model.PageSetup
	for n := 1; n <= d.Pages; n++ {
		page, margins, assets, err := layoutPage(d, n)
		if err != nil {
			res.Warnings = append(res.Warnings, Warning{Page: n, Msg: err.Error()})
			page = model.Page{Number: n, Source: model.SourceEmpty}
		}
		res.Doc.Pages = append(res.Doc.Pages, page)
		if assets != nil {
			res.Assets[n] = assets
		}
		setup = mergeSetup(setup, margins)
	}
	res.Doc.Setup = setup
	return res, nil
}

// MergeSetup combines page setups: the first page's size, and the smallest
// margins seen (widest content wins).
func MergeSetup(a, b *model.PageSetup) *model.PageSetup { return mergeSetup(a, b) }

func mergeSetup(a, b *model.PageSetup) *model.PageSetup {
	if b == nil {
		return a
	}
	if a == nil {
		c := *b
		return &c
	}
	a.MarginLeft = math.Min(a.MarginLeft, b.MarginLeft)
	a.MarginRight = math.Min(a.MarginRight, b.MarginRight)
	a.MarginTop = math.Min(a.MarginTop, b.MarginTop)
	a.MarginBottom = math.Min(a.MarginBottom, b.MarginBottom)
	return a
}

func pageSize(d *pdfiumx.Doc, n int) (float64, float64, error) {
	size, err := d.Instance.FPDF_GetPageSizeByIndexF(&requests.FPDF_GetPageSizeByIndexF{Document: d.Ref, Index: n - 1})
	if err != nil {
		return 0, 0, fmt.Errorf("page size: %w", err)
	}
	w, h := float64(size.Size.Width), float64(size.Size.Height)
	if w <= 0 || h <= 0 {
		return 0, 0, fmt.Errorf("page has no size")
	}
	return w, h, nil
}

// layoutPage builds one page from its text layer. For a page without text
// it returns the page's assets so OCR can be laid out later.
func layoutPage(d *pdfiumx.Doc, n int) (model.Page, *model.PageSetup, *PageAssets, error) {
	page := model.Page{Number: n, Source: model.SourceEmpty}
	w, h, err := pageSize(d, n)
	if err != nil {
		return page, nil, nil, err
	}
	page.Width, page.Height = w, h

	chars, err := readChars(d, n)
	if err != nil {
		return page, nil, nil, err
	}
	rules, images, objWarns := readObjects(d, n, w, h, len(chars) > 0)
	if len(chars) == 0 {
		// Nothing to lay out yet; keep what OCR will need.
		assets := &PageAssets{Width: w, Height: h, rules: rules, images: images}
		var werr error
		if len(objWarns) > 0 {
			werr = fmt.Errorf("%v", objWarns)
		}
		return page, nil, assets, werr
	}
	p, setup := assemble(n, w, h, chars, rules, images, tolText, false)
	return p, setup, nil, nil
}

// Word is OCR output in PDF points (origin bottom-left). Y0/Y1 should span
// the text line, not the glyphs of the individual word. Group, when not 0,
// identifies the paragraph the OCR engine assigned the word to; lines from
// different groups are never merged into one paragraph.
type Word struct {
	Text           string
	X0, Y0, X1, Y1 float64
	Size           float64 // font size estimate in points
	Bold, Italic   bool
	Font           string
	Group          int
}

// AssembleOCR lays out OCR words on a page, using the page's rulings and
// images (assets may be nil). Full-page scan images are not embedded.
func AssembleOCR(number int, width, height float64, words []Word, assets *PageAssets) (model.Page, *model.PageSetup) {
	chars := make([]char, 0, len(words))
	for _, w := range words {
		if w.Text == "" {
			continue
		}
		c := char{text: w.Text, x0: w.X0, y0: w.Y0, x1: w.X1, y1: w.Y1, size: w.Size, font: fontInfo{Family: w.Font, Bold: w.Bold, Italic: w.Italic}, group: w.Group}
		if c.size <= 0 {
			c.size = math.Max(1, (w.Y1-w.Y0)*0.8)
		}
		chars = append(chars, c)
	}
	var rules []rule
	var images []placedImage
	if assets != nil {
		rules = assets.rules
		for _, im := range assets.images {
			if (im.x1-im.x0)*(im.y1-im.y0) >= 0.5*width*height {
				continue // the scan itself
			}
			images = append(images, im)
		}
		if assets.Width > 0 && assets.Height > 0 {
			width, height = assets.Width, assets.Height
		}
	}
	page, setup := assemble(number, width, height, chars, rules, images, tolOCR, true)
	if len(page.Blocks) > 0 {
		page.Source = model.SourceOCR
	}
	return page, setup
}

// element is anything placed in the page flow. top/bottom are the edges Word
// will lay out (for text: first baseline + 0.8 leading, last baseline - 0.2
// leading), used to reproduce vertical spacing.
type element struct {
	top, bottom float64
	x0, x1      float64
	block       model.Block
	image       *placedImage // set for single images (may be grouped)
}

// assemble is the shared page builder: it groups characters into lines and
// paragraphs, detects tables from rulings, places images, derives margins
// and vertical spacing, and returns the page with its setup.
func assemble(number int, w, h float64, chars []char, rules []rule, images []placedImage, tol float64, ocr bool) (model.Page, *model.PageSetup) {
	page := model.Page{Number: number, Source: model.SourceEmpty, Width: w, Height: h}

	gap := segmentGapFactor
	if ocr {
		gap = ocrSegmentGap
	}
	lines := groupLines(chars, gap)
	tables := detectTables(rules)
	lines = assignLines(lines, tables)

	// Content box from everything on the page.
	ct := content{left: math.Inf(1), right: math.Inf(-1), top: math.Inf(-1), bottom: math.Inf(1), pageW: w, tol: tol}
	extend := func(x0, y0, x1, y1 float64) {
		ct.left = math.Min(ct.left, x0)
		ct.right = math.Max(ct.right, x1)
		ct.top = math.Max(ct.top, y1)
		ct.bottom = math.Min(ct.bottom, y0)
	}
	for _, l := range lines {
		extend(l.x0, l.y0, l.x1, l.y1)
	}
	for _, t := range tables {
		extend(t.x0, t.y0, t.x1, t.y1)
	}
	for _, im := range images {
		extend(im.x0, im.y0, im.x1, im.y1)
	}
	if math.IsInf(ct.left, 1) {
		return page, nil // nothing on the page
	}
	setup := &model.PageSetup{
		Width: w, Height: h,
		MarginLeft:   clamp(ct.left, minMargin, maxMargin),
		MarginRight:  clamp(w-ct.right, minMargin, maxMargin),
		MarginTop:    clamp(h-ct.top, minMargin, maxMargin),
		MarginBottom: clamp(ct.bottom, minMargin, maxMargin),
	}
	// Positions are expressed relative to the margins actually used.
	ct.left = setup.MarginLeft
	ct.right = w - setup.MarginRight

	bodySize := dominantSize(chars)
	var elems []element
	for _, p := range groupParagraphs(lines, ct) {
		top, bottom := p.wordBox()
		elems = append(elems, element{top: top, bottom: bottom, x0: p.x0(), x1: p.x1(), block: toBlock(p, ct, bodySize)})
	}
	for _, t := range tables {
		elems = append(elems, element{top: t.y1, bottom: t.y0, x0: t.x0, x1: t.x1, block: tableBlock(t, ct)})
	}
	for i := range images {
		im := &images[i]
		elems = append(elems, element{top: im.y1, bottom: im.y0, x0: im.x0, x1: im.x1, block: im.block(ct), image: im})
	}
	sort.SliceStable(elems, func(i, j int) bool {
		if math.Abs(elems[i].top-elems[j].top) > 1 {
			return elems[i].top > elems[j].top
		}
		return elems[i].x0 < elems[j].x0
	})
	elems = groupImageBands(elems, ct)

	prevBottom := h - setup.MarginTop
	for _, e := range elems {
		b := e.block
		b.SpaceBefore = clamp(prevBottom-e.top, 0, maxGap)
		page.Blocks = append(page.Blocks, b)
		prevBottom = math.Min(prevBottom, e.bottom)
	}
	if len(chars) > 0 || len(tables) > 0 {
		page.Source = model.SourceText
	}
	return page, setup
}

func clamp(v, lo, hi float64) float64 { return math.Max(lo, math.Min(hi, v)) }

// groupImageBands joins images that share a horizontal band (logo left,
// QR code right, ...) into one paragraph whose segments carry the pictures
// at their positions, so they stay on one line in Word.
func groupImageBands(elems []element, ct content) []element {
	var out []element
	for _, e := range elems {
		if e.image != nil && len(out) > 0 {
			last := &out[len(out)-1]
			if last.image != nil || last.block.Kind == model.Paragraph && isImageLine(last.block) {
				overlap := math.Min(last.top, e.top) - math.Max(last.bottom, e.bottom)
				minH := math.Min(last.top-last.bottom, e.top-e.bottom)
				if minH > 0 && overlap >= 0.5*minH {
					mergeImageInto(last, e, ct)
					continue
				}
			}
		}
		out = append(out, e)
	}
	return out
}

func isImageLine(b model.Block) bool {
	if len(b.Lines) != 1 {
		return false
	}
	for _, s := range b.Lines[0].Segments {
		for _, r := range s.Runs {
			if r.Image == nil {
				return false
			}
		}
	}
	return len(b.Lines[0].Segments) > 0
}

// mergeImageInto turns last into an image line (if it is still a single
// image block) and adds e's image as another segment, ordered by x.
func mergeImageInto(last *element, e element, ct content) {
	if last.image != nil {
		img := last.block.Image
		last.block = model.Block{Kind: model.Paragraph, Lines: []model.Line{{Segments: []model.Segment{imageSegment(*last.image, img, ct)}}}}
		last.image = nil
	}
	seg := imageSegment(*e.image, e.block.Image, ct)
	segs := append(last.block.Lines[0].Segments, seg)
	sort.SliceStable(segs, func(i, j int) bool { return segs[i].X < segs[j].X })
	last.block.Lines[0].Segments = segs
	last.top = math.Max(last.top, e.top)
	last.bottom = math.Min(last.bottom, e.bottom)
	last.x0 = math.Min(last.x0, e.x0)
	last.x1 = math.Max(last.x1, e.x1)
}

func imageSegment(im placedImage, data *model.ImageData, ct content) model.Segment {
	seg := model.Segment{X: math.Max(0, im.x0-ct.left), Runs: []model.Run{{Image: data}}}
	center := (im.x0 + im.x1) / 2
	switch {
	case math.Abs(center-ct.center()) <= 0.02*ct.pageW:
		seg.CenterX = center - ct.left
	case im.x1 >= ct.right-ct.tol-1:
		seg.FlushRight = true
	}
	return seg
}

func dominantSize(chars []char) float64 {
	counts := map[float64]int{}
	for _, c := range chars {
		if !c.isSpace() {
			counts[math.Round(c.size*10)/10]++
		}
	}
	return modeSize(counts)
}

// placedImage is an image object with its page placement.
type placedImage struct {
	x0, y0, x1, y1 float64
	data           []byte
}

func (im placedImage) block(ct content) model.Block {
	w, h := im.x1-im.x0, im.y1-im.y0
	b := model.Block{Kind: model.Image, Image: &model.ImageData{Data: im.data, Ext: "png", Width: w, Height: h}}
	center := (im.x0 + im.x1) / 2
	switch {
	case math.Abs(center-ct.center()) <= 0.02*ct.pageW:
		b.Image.Align = model.AlignCenter
	case im.x1 >= ct.right-ct.tol-1 && im.x0 > ct.left+0.2*ct.width():
		b.Image.Align = model.AlignRight
	default:
		if ind := im.x0 - ct.left; ind > 1.5 {
			b.IndentLeft = ind
		}
	}
	return b
}
