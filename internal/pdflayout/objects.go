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
	if w < ruleMinLength && h < ruleMinLength {
		return nil // glyph outlines and dots: not rulings
	}

	dm, err := inst.FPDFPath_GetDrawMode(&requests.FPDFPath_GetDrawMode{PageObject: obj})
	if err != nil {
		return nil
	}
	col, visible := pathColor(d, obj, dm.Stroke, dm.FillMode != enums.FPDF_FILLMODE_NONE)
	if !visible {
		return nil
	}

	// Thin shape: treat the whole thing as one rule. Filled shapes need to be
	// longer than a glyph, because text drawn as outlines ("l", "I", "-")
	// produces thin filled paths too.
	minLen := ruleMinLength
	if !dm.Stroke {
		minLen = ruleMinFillLength
	}
	if h <= ruleMaxThick && w >= minLen {
		return []rule{{vertical: false, pos: (y0 + y1) / 2, from: x0, to: x1, color: col}}
	}
	if w <= ruleMaxThick && h >= minLen {
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
