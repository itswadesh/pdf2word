package pdflayout

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"unicode"

	"github.com/klippa-app/go-pdfium/enums"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/structs"

	"pdf2word/internal/pdfiumx"
)

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
	if w < ruleMinPiece && h < ruleMinPiece {
		return nil // dots and specks
	}

	dm, err := inst.FPDFPath_GetDrawMode(&requests.FPDFPath_GetDrawMode{PageObject: obj})
	if err != nil {
		return nil
	}
	col, visible := pathColor(d, obj, dm.Stroke, dm.FillMode != enums.FPDF_FILLMODE_NONE)
	if !visible {
		return nil
	}

	// Thin shape: treat the whole thing as one rule. Filled pieces may be
	// short (cell borders are often drawn per cell, or even per glyph run);
	// detectTables chains collinear pieces and discards what stays short.
	minLen := ruleMinLength
	if !dm.Stroke {
		minLen = ruleMinPiece
	}
	if h <= ruleMaxThick && w >= minLen {
		return []rule{{vertical: false, pos: (y0 + y1) / 2, from: x0, to: x1, color: col, stroke: dm.Stroke}}
	}
	if w <= ruleMaxThick && h >= minLen {
		return []rule{{vertical: true, pos: (x0 + x1) / 2, from: y0, to: y1, color: col, stroke: dm.Stroke}}
	}

	// Otherwise look at the segments. Their points are in the path's own
	// space; the object matrix maps them onto the page.
	cnt, err := inst.FPDFPath_CountSegments(&requests.FPDFPath_CountSegments{PageObject: obj})
	if err != nil || cnt.Count < 2 || cnt.Count > 128 {
		return nil
	}
	ma, mb, mc, md, me, mf := 1.0, 0.0, 0.0, 1.0, 0.0, 0.0
	if m, err := inst.FPDFPageObj_GetMatrix(&requests.FPDFPageObj_GetMatrix{PageObject: obj}); err == nil {
		ma, mb, mc, md, me, mf = float64(m.Matrix.A), float64(m.Matrix.B), float64(m.Matrix.C), float64(m.Matrix.D), float64(m.Matrix.E), float64(m.Matrix.F)
	}
	pts := make([]pathPoint, 0, cnt.Count)
	for i := 0; i < cnt.Count; i++ {
		seg, err := inst.FPDFPath_GetPathSegment(&requests.FPDFPath_GetPathSegment{PageObject: obj, Index: i})
		if err != nil {
			return nil
		}
		pt, err := inst.FPDFPathSegment_GetPoint(&requests.FPDFPathSegment_GetPoint{PathSegment: seg.PathSegment})
		if err != nil {
			return nil
		}
		st, err := inst.FPDFPathSegment_GetType(&requests.FPDFPathSegment_GetType{PathSegment: seg.PathSegment})
		if err != nil {
			return nil
		}
		x, y := float64(pt.X), float64(pt.Y)
		pts = append(pts, pathPoint{x: ma*x + mc*y + me, y: mb*x + md*y + mf, kind: st.Type})
	}

	var rules []rule
	if dm.Stroke {
		// Axis-aligned stroked segments are rulings.
		for i := 1; i < len(pts); i++ {
			p, q := pts[i-1], pts[i]
			if q.kind != enums.FPDF_SEGMENT_LINETO {
				continue
			}
			switch {
			case math.Abs(q.y-p.y) <= ruleTol && math.Abs(q.x-p.x) >= ruleMinLength:
				rules = append(rules, rule{pos: (q.y + p.y) / 2, from: math.Min(q.x, p.x), to: math.Max(q.x, p.x), color: col, stroke: true})
			case math.Abs(q.x-p.x) <= ruleTol && math.Abs(q.y-p.y) >= ruleMinLength:
				rules = append(rules, rule{vertical: true, pos: (q.x + p.x) / 2, from: math.Min(q.y, p.y), to: math.Max(q.y, p.y), color: col, stroke: true})
			}
		}
		return rules
	}

	// Filled path made of several axis-aligned rectangles: a table "frame"
	// (outer box plus cell boxes filled even-odd, as Word's PDF output does).
	// Every rectangle edge is then a ruling. A single filled rectangle is a
	// background, not a border.
	rects := rectSubpaths(pts)
	if len(rects) < 2 {
		return nil
	}
	for _, r := range rects {
		if r.x1-r.x0 >= ruleMinPiece {
			rules = append(rules,
				rule{pos: r.y0, from: r.x0, to: r.x1, color: col},
				rule{pos: r.y1, from: r.x0, to: r.x1, color: col})
		}
		if r.y1-r.y0 >= ruleMinPiece {
			rules = append(rules,
				rule{vertical: true, pos: r.x0, from: r.y0, to: r.y1, color: col},
				rule{vertical: true, pos: r.x1, from: r.y0, to: r.y1, color: col})
		}
	}
	return rules
}

type pathPoint struct {
	x, y float64
	kind enums.FPDF_SEGMENT
}

type rect struct{ x0, y0, x1, y1 float64 }

// rectSubpaths splits a path at MoveTo segments and returns the bounding
// boxes of the sub-paths that are axis-aligned rectangles (4 corners, with
// or without a closing point, straight segments only).
func rectSubpaths(pts []pathPoint) []rect {
	var rects []rect
	var cur []pathPoint
	curved := false
	flush := func() {
		if !curved {
			if r, ok := asRect(cur); ok {
				rects = append(rects, r)
			}
		}
		cur, curved = cur[:0], false
	}
	for _, p := range pts {
		if p.kind == enums.FPDF_SEGMENT_MOVETO && len(cur) > 0 {
			flush()
		}
		if p.kind == enums.FPDF_SEGMENT_BEZIERTO {
			curved = true
		}
		cur = append(cur, p)
	}
	if len(cur) > 0 {
		flush()
	}
	return rects
}

func asRect(pts []pathPoint) (rect, bool) {
	const eps = 0.15
	// Drop a closing point that repeats the first one.
	if n := len(pts); n == 5 && math.Abs(pts[4].x-pts[0].x) < eps && math.Abs(pts[4].y-pts[0].y) < eps {
		pts = pts[:4]
	}
	if len(pts) != 4 {
		return rect{}, false
	}
	r := rect{x0: math.Inf(1), y0: math.Inf(1), x1: math.Inf(-1), y1: math.Inf(-1)}
	for _, p := range pts {
		r.x0, r.x1 = math.Min(r.x0, p.x), math.Max(r.x1, p.x)
		r.y0, r.y1 = math.Min(r.y0, p.y), math.Max(r.y1, p.y)
	}
	if r.x1-r.x0 < eps || r.y1-r.y0 < eps {
		return rect{}, false
	}
	// Every point must sit on a corner of the bounding box, and consecutive
	// points must share an x or a y (axis-aligned edges).
	for i, p := range pts {
		onX := math.Abs(p.x-r.x0) < eps || math.Abs(p.x-r.x1) < eps
		onY := math.Abs(p.y-r.y0) < eps || math.Abs(p.y-r.y1) < eps
		if !onX || !onY {
			return rect{}, false
		}
		next := pts[(i+1)%4]
		if math.Abs(p.x-next.x) > eps && math.Abs(p.y-next.y) > eps {
			return rect{}, false
		}
	}
	return r, true
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
