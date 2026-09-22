// Package pdflayout extracts a PDF's text layer with its layout, using
// PDFium: characters with fonts and positions, table rulings drawn as paths,
// and placed images. It produces model pages with paragraphs, headings,
// alignment, tab-separated columns, tables and pictures.
package pdflayout

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/klippa-app/go-pdfium/enums"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/structs"

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

// Extract reads every page of the PDF at path.
func Extract(path string) (*model.Document, []Warning, error) {
	d, err := pdfiumx.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer d.Close()
	return extractDoc(d)
}

func extractDoc(d *pdfiumx.Doc) (*model.Document, []Warning, error) {
	d.Mu.Lock()
	defer d.Mu.Unlock()

	doc := &model.Document{}
	var warns []Warning
	var setup *model.PageSetup
	for n := 1; n <= d.Pages; n++ {
		page, margins, err := layoutPage(d, n)
		if err != nil {
			warns = append(warns, Warning{Page: n, Msg: err.Error()})
			page = model.Page{Number: n, Source: model.SourceEmpty}
		}
		doc.Pages = append(doc.Pages, page)
		if margins != nil {
			if setup == nil {
				s := *margins
				setup = &s
			} else {
				// Widest content wins: smallest margins over all pages.
				setup.MarginLeft = math.Min(setup.MarginLeft, margins.MarginLeft)
				setup.MarginRight = math.Min(setup.MarginRight, margins.MarginRight)
				setup.MarginTop = math.Min(setup.MarginTop, margins.MarginTop)
				setup.MarginBottom = math.Min(setup.MarginBottom, margins.MarginBottom)
			}
		}
	}
	doc.Setup = setup
	return doc, warns, nil
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

// layoutPage builds one page. The returned setup carries the page size and
// the margins implied by its content.
func layoutPage(d *pdfiumx.Doc, n int) (model.Page, *model.PageSetup, error) {
	inst := d.Instance
	page := model.Page{Number: n, Source: model.SourceEmpty}

	size, err := inst.FPDF_GetPageSizeByIndexF(&requests.FPDF_GetPageSizeByIndexF{Document: d.Ref, Index: n - 1})
	if err != nil {
		return page, nil, fmt.Errorf("page size: %w", err)
	}
	page.Width, page.Height = float64(size.Size.Width), float64(size.Size.Height)
	if page.Width <= 0 || page.Height <= 0 {
		return page, nil, fmt.Errorf("page has no size")
	}

	chars, err := readChars(d, n)
	if err != nil {
		return page, nil, err
	}
	rules, images, objWarns := readObjects(d, n, page.Width, page.Height, len(chars) > 0)

	lines := groupLines(chars)
	tables := detectTables(rules)
	lines = assignLines(lines, tables)

	// Content box from everything on the page.
	ct := content{left: math.Inf(1), right: math.Inf(-1), top: math.Inf(-1), bottom: math.Inf(1), pageW: page.Width}
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
		return page, nil, nil // nothing on the page
	}
	setup := &model.PageSetup{
		Width: page.Width, Height: page.Height,
		MarginLeft:   clamp(ct.left, minMargin, maxMargin),
		MarginRight:  clamp(page.Width-ct.right, minMargin, maxMargin),
		MarginTop:    clamp(page.Height-ct.top, minMargin, maxMargin),
		MarginBottom: clamp(ct.bottom, minMargin, maxMargin),
	}
	// Positions are expressed relative to the margins actually used.
	ct.left = setup.MarginLeft
	ct.right = page.Width - setup.MarginRight

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

	prevBottom := page.Height - setup.MarginTop
	for _, e := range elems {
		b := e.block
		b.SpaceBefore = clamp(prevBottom-e.top, 0, maxGap)
		page.Blocks = append(page.Blocks, b)
		prevBottom = math.Min(prevBottom, e.bottom)
	}
	if len(chars) > 0 || len(tables) > 0 {
		page.Source = model.SourceText
	}
	if len(objWarns) > 0 && page.Source != model.SourceText {
		return page, setup, fmt.Errorf("%s", strings.Join(objWarns, "; "))
	}
	return page, setup, nil
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
	case im.x1 >= ct.right-3:
		seg.FlushRight = true
	}
	return seg
}

// readChars fetches every character with its box and font.
func readChars(d *pdfiumx.Doc, n int) ([]char, error) {
	txt, err := d.Instance.GetPageTextStructured(&requests.GetPageTextStructured{
		Page:                   d.Page(n),
		Mode:                   requests.GetPageTextStructuredModeChars,
		CollectFontInformation: true,
	})
	if err != nil {
		return nil, fmt.Errorf("text: %w", err)
	}
	chars := make([]char, 0, len(txt.Chars))
	for _, c := range txt.Chars {
		if c.Text == "" {
			continue
		}
		if c.Text == "\x02" {
			// PDFium replaces a hyphen at a line end with this marker (it
			// assumes a hyphenated word). Keep it visible as a hyphen; the
			// paragraph joiner removes it when the word really continues.
			c.Text = "-"
		}
		box := c.PointPosition
		x0, x1 := math.Min(box.Left, box.Right), math.Max(box.Left, box.Right)
		y0, y1 := math.Min(box.Bottom, box.Top), math.Max(box.Bottom, box.Top)
		control := true
		for _, r := range c.Text {
			if !unicode.IsControl(r) {
				control = false
			}
		}
		if control || (x1-x0 <= 0 && y1-y0 <= 0) {
			continue // generated line breaks and zero-size marks
		}
		ch := char{text: c.Text, x0: x0, y0: y0, x1: x1, y1: y1}
		if c.FontInformation != nil {
			ch.size = c.FontInformation.Size
			ch.font = parseFont(c.FontInformation.Name, c.FontInformation.Weight, c.FontInformation.Flags)
		}
		if ch.size <= 0 {
			ch.size = math.Max(1, y1-y0)
		}
		chars = append(chars, ch)
	}
	return chars, nil
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
	case im.x1 >= ct.right-3 && im.x0 > ct.left+0.2*ct.width():
		b.Image.Align = model.AlignRight
	default:
		if ind := im.x0 - ct.left; ind > 1.5 {
			b.IndentLeft = ind
		}
	}
	return b
}

// readObjects walks the page objects (recursing into form XObjects) and
// collects rulings and images.
func readObjects(d *pdfiumx.Doc, n int, pageW, pageH float64, hasText bool) ([]rule, []placedImage, []string) {
	inst := d.Instance
	pg := d.Page(n)
	var rules []rule
	var images []placedImage
	var warns []string

	count, err := inst.FPDFPage_CountObjects(&requests.FPDFPage_CountObjects{Page: pg})
	if err != nil {
		return nil, nil, []string{"page objects: " + err.Error()}
	}
	var visit func(obj references.FPDF_PAGEOBJECT, depth int)
	visit = func(obj references.FPDF_PAGEOBJECT, depth int) {
		t, err := inst.FPDFPageObj_GetType(&requests.FPDFPageObj_GetType{PageObject: obj})
		if err != nil {
			return
		}
		switch t.Type {
		case enums.FPDF_PAGEOBJ_PATH:
			rules = append(rules, pathRules(d, obj)...)
		case enums.FPDF_PAGEOBJ_IMAGE:
			b, err := inst.FPDFPageObj_GetBounds(&requests.FPDFPageObj_GetBounds{PageObject: obj})
			if err != nil {
				return
			}
			x0, y0, x1, y1 := float64(b.Left), float64(b.Bottom), float64(b.Right), float64(b.Top)
			w, h := x1-x0, y1-y0
			if w < 8 || h < 8 {
				return
			}
			if hasText && w*h >= 0.9*pageW*pageH {
				return // full-page background behind real text
			}
			data, err := renderImageObject(d, pg, obj)
			if err != nil {
				warns = append(warns, "image: "+err.Error())
				return
			}
			images = append(images, placedImage{x0: x0, y0: y0, x1: x1, y1: y1, data: data})
		case enums.FPDF_PAGEOBJ_FORM:
			if depth > 4 {
				return
			}
			c, err := inst.FPDFFormObj_CountObjects(&requests.FPDFFormObj_CountObjects{PageObject: obj})
			if err != nil {
				return
			}
			for i := 0; i < c.Count; i++ {
				child, err := inst.FPDFFormObj_GetObject(&requests.FPDFFormObj_GetObject{PageObject: obj, Index: uint64(i)})
				if err == nil {
					visit(child.PageObject, depth+1)
				}
			}
		}
	}
	for i := 0; i < count.Count; i++ {
		o, err := inst.FPDFPage_GetObject(&requests.FPDFPage_GetObject{Page: pg, Index: i})
		if err != nil {
			continue
		}
		visit(o.PageObject, 0)
	}
	return rules, images, warns
}

// pathRules extracts straight horizontal/vertical strokes and thin filled
// rectangles from a path object. Invisible (white or transparent) shapes are
// ignored.
func pathRules(d *pdfiumx.Doc, obj references.FPDF_PAGEOBJECT) []rule {
	inst := d.Instance
	b, err := inst.FPDFPageObj_GetBounds(&requests.FPDFPageObj_GetBounds{PageObject: obj})
	if err != nil {
		return nil
	}
	x0, y0, x1, y1 := float64(b.Left), float64(b.Bottom), float64(b.Right), float64(b.Top)
	w, h := x1-x0, y1-y0

	dm, err := inst.FPDFPath_GetDrawMode(&requests.FPDFPath_GetDrawMode{PageObject: obj})
	if err != nil {
		return nil
	}
	col, visible := pathColor(d, obj, dm.Stroke, dm.FillMode != enums.FPDF_FILLMODE_NONE)
	if !visible {
		return nil
	}

	// Thin shape: treat the whole thing as one rule.
	if h <= ruleMaxThick && w >= ruleMinLength {
		return []rule{{vertical: false, pos: (y0 + y1) / 2, from: x0, to: x1, color: col}}
	}
	if w <= ruleMaxThick && h >= ruleMinLength {
		return []rule{{vertical: true, pos: (x0 + x1) / 2, from: y0, to: y1, color: col}}
	}

	// Otherwise look at the segments: axis-aligned strokes and rectangles.
	if !dm.Stroke {
		return nil
	}
	cnt, err := inst.FPDFPath_CountSegments(&requests.FPDFPath_CountSegments{PageObject: obj})
	if err != nil || cnt.Count < 2 || cnt.Count > 64 {
		return nil
	}
	var rules []rule
	var px, py float64
	have := false
	for i := 0; i < cnt.Count; i++ {
		seg, err := inst.FPDFPath_GetPathSegment(&requests.FPDFPath_GetPathSegment{PageObject: obj, Index: i})
		if err != nil {
			return rules
		}
		pt, err := inst.FPDFPathSegment_GetPoint(&requests.FPDFPathSegment_GetPoint{PathSegment: seg.PathSegment})
		if err != nil {
			return rules
		}
		st, err := inst.FPDFPathSegment_GetType(&requests.FPDFPathSegment_GetType{PathSegment: seg.PathSegment})
		if err != nil {
			return rules
		}
		x, y := float64(pt.X), float64(pt.Y)
		if st.Type == enums.FPDF_SEGMENT_LINETO && have {
			switch {
			case math.Abs(y-py) <= ruleTol && math.Abs(x-px) >= ruleMinLength:
				rules = append(rules, rule{pos: (y + py) / 2, from: math.Min(x, px), to: math.Max(x, px), color: col})
			case math.Abs(x-px) <= ruleTol && math.Abs(y-py) >= ruleMinLength:
				rules = append(rules, rule{vertical: true, pos: (x + px) / 2, from: math.Min(y, py), to: math.Max(y, py), color: col})
			}
		}
		px, py, have = x, y, true
	}
	return rules
}

// pathColor returns the drawing colour of a path as RRGGBB and whether the
// shape is visible at all (not white, not fully transparent).
func pathColor(d *pdfiumx.Doc, obj references.FPDF_PAGEOBJECT, stroke, fill bool) (string, bool) {
	var c structs.FPDF_COLOR
	got := false
	if stroke {
		if res, err := d.Instance.FPDFPageObj_GetStrokeColor(&requests.FPDFPageObj_GetStrokeColor{PageObject: obj}); err == nil {
			c, got = res.StrokeColor, true
		}
	}
	if !got && fill {
		if res, err := d.Instance.FPDFPageObj_GetFillColor(&requests.FPDFPageObj_GetFillColor{PageObject: obj}); err == nil {
			c, got = res.FillColor, true
		}
	}
	if !got {
		return "000000", true
	}
	if c.A == 0 || (c.R >= 250 && c.G >= 250 && c.B >= 250) {
		return "", false
	}
	return fmt.Sprintf("%02X%02X%02X", c.R&0xFF, c.G&0xFF, c.B&0xFF), true
}

// renderImageObject renders one image object to PNG bytes.
func renderImageObject(d *pdfiumx.Doc, pg requests.Page, obj references.FPDF_PAGEOBJECT) ([]byte, error) {
	inst := d.Instance
	bm, err := inst.FPDFImageObj_GetRenderedBitmap(&requests.FPDFImageObj_GetRenderedBitmap{Document: d.Ref, Page: pg, ImageObject: obj})
	if err != nil {
		return nil, err
	}
	defer inst.FPDFBitmap_Destroy(&requests.FPDFBitmap_Destroy{Bitmap: bm.Bitmap})

	wres, err := inst.FPDFBitmap_GetWidth(&requests.FPDFBitmap_GetWidth{Bitmap: bm.Bitmap})
	if err != nil {
		return nil, err
	}
	hres, err := inst.FPDFBitmap_GetHeight(&requests.FPDFBitmap_GetHeight{Bitmap: bm.Bitmap})
	if err != nil {
		return nil, err
	}
	sres, err := inst.FPDFBitmap_GetStride(&requests.FPDFBitmap_GetStride{Bitmap: bm.Bitmap})
	if err != nil {
		return nil, err
	}
	fres, err := inst.FPDFBitmap_GetFormat(&requests.FPDFBitmap_GetFormat{Bitmap: bm.Bitmap})
	if err != nil {
		return nil, err
	}
	bres, err := inst.FPDFBitmap_GetBuffer(&requests.FPDFBitmap_GetBuffer{Bitmap: bm.Bitmap})
	if err != nil {
		return nil, err
	}
	w, h, stride, buf := wres.Width, hres.Height, sres.Stride, bres.Buffer
	if w <= 0 || h <= 0 || len(buf) < stride*h {
		return nil, fmt.Errorf("bitmap %dx%d has %d bytes (stride %d)", w, h, len(buf), stride)
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		row := buf[y*stride:]
		for x := 0; x < w; x++ {
			var c color.NRGBA
			switch fres.Format {
			case enums.FPDF_BITMAP_FORMAT_GRAY:
				v := row[x]
				c = color.NRGBA{R: v, G: v, B: v, A: 255}
			case enums.FPDF_BITMAP_FORMAT_BGR:
				p := row[x*3:]
				c = color.NRGBA{R: p[2], G: p[1], B: p[0], A: 255}
			case enums.FPDF_BITMAP_FORMAT_BGRA:
				p := row[x*4:]
				c = color.NRGBA{R: p[2], G: p[1], B: p[0], A: p[3]}
			default: // BGRx
				p := row[x*4:]
				c = color.NRGBA{R: p[2], G: p[1], B: p[0], A: 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
