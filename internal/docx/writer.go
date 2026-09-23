// Package docx serialises a model.Document as a Word .docx file using only
// the standard library. It emits paragraphs with run formatting, alignment,
// indents, tab stops and line breaks, ruled tables, inline pictures and a
// page setup derived from the source PDF.
package docx

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"pdf2word/internal/model"
)

// now is a variable so tests can pin timestamps if they need to.
var now = func() time.Time { return time.Now().UTC() }

// Default page setup: US Letter, one-inch margins.
var defaultSetup = model.PageSetup{Width: 612, Height: 792, MarginTop: 72, MarginRight: 72, MarginBottom: 72, MarginLeft: 72}

type imagePart struct {
	name string // e.g. image1.png
	rid  string // e.g. rId2
	data []byte
}

// Write encodes doc as a .docx package and writes it to w.
func Write(w io.Writer, doc *model.Document) error {
	if doc == nil {
		doc = &model.Document{}
	}
	ts := now()
	stamp := ts.Format(time.RFC3339)

	dw := &docWriter{setup: defaultSetup}
	if doc.Setup != nil && doc.Setup.Width > 0 && doc.Setup.Height > 0 {
		dw.setup = *doc.Setup
	}
	body := dw.documentXML(doc)

	exts := map[string]bool{}
	var extList []string
	for _, im := range dw.images {
		ext := im.name[strings.LastIndexByte(im.name, '.')+1:]
		if !exts[ext] {
			exts[ext] = true
			extList = append(extList, ext)
		}
	}

	csFont := doc.ComplexScriptFont
	if csFont == "" {
		csFont = DefaultComplexScriptFont
	}
	parts := []struct{ name, body string }{
		{"[Content_Types].xml", contentTypesXML(extList)},
		{"_rels/.rels", rootRelsXML},
		{"word/document.xml", body},
		{"word/styles.xml", fmt.Sprintf(stylesXMLTemplate, escapeAttr(csFont))},
		{"word/_rels/document.xml.rels", documentRelsXML(dw.images)},
		{"docProps/core.xml", fmt.Sprintf(coreXMLTemplate, stamp, stamp)},
		{"docProps/app.xml", appXML},
	}

	zw := zip.NewWriter(w)
	for _, p := range parts {
		if err := addPart(zw, p.name, []byte(p.body), ts); err != nil {
			return err
		}
	}
	for _, im := range dw.images {
		if err := addPart(zw, "word/media/"+im.name, im.data, ts); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("docx: finalize package: %w", err)
	}
	return nil
}

func addPart(zw *zip.Writer, name string, data []byte, ts time.Time) error {
	f, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: ts})
	if err != nil {
		return fmt.Errorf("docx: create part %s: %w", name, err)
	}
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("docx: write part %s: %w", name, err)
	}
	return nil
}

// docWriter accumulates document.xml and the images it references.
type docWriter struct {
	sb     strings.Builder
	setup  model.PageSetup
	images []imagePart
	nextID int // drawing ids
}

const (
	nsW   = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	nsR   = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
	nsWP  = "http://schemas.openxmlformats.org/drawingml/2006/wordprocessingDrawing"
	nsA   = "http://schemas.openxmlformats.org/drawingml/2006/main"
	nsPic = "http://schemas.openxmlformats.org/drawingml/2006/picture"
)

const pageBreakXML = `<w:p><w:r><w:br w:type="page"/></w:r></w:p>`

func twips(pt float64) int { return int(math.Round(pt * 20)) }
func emu(pt float64) int64 { return int64(math.Round(pt * 12700)) }

// documentXML renders word/document.xml: every block becomes a paragraph,
// table or picture; consecutive pages are separated by a page break (even
// when a page is empty, so page numbering still lines up with the source).
func (dw *docWriter) documentXML(doc *model.Document) string {
	sb := &dw.sb
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	fmt.Fprintf(sb, `<w:document xmlns:w=%q xmlns:r=%q xmlns:wp=%q xmlns:a=%q xmlns:pic=%q><w:body>`, nsW, nsR, nsWP, nsA, nsPic)

	lastWasTable := false
	for i, page := range doc.Pages {
		if i > 0 {
			sb.WriteString(pageBreakXML)
			lastWasTable = false
		}
		layout := page.Width > 0
		for _, b := range page.Blocks {
			if b.Kind == model.Table && lastWasTable {
				sb.WriteString("<w:p/>") // two tables in a row would merge
			}
			dw.writeBlock(b, layout)
			lastWasTable = b.Kind == model.Table
		}
	}
	if lastWasTable {
		sb.WriteString("<w:p/>") // the body must not end with a table
	}
	dw.writeSectPr()
	sb.WriteString(`</w:body></w:document>`)
	return sb.String()
}

func (dw *docWriter) writeBlock(b model.Block, layout bool) {
	switch b.Kind {
	case model.Table:
		if b.Table != nil {
			dw.writeTable(b)
		}
	case model.Image:
		if b.Image != nil {
			dw.writeImage(b, layout)
		}
	default:
		dw.writeParagraph(b, layout)
	}
}

// paragraphProps writes <w:pPr> for a paragraph or heading. When layout is
// true the block came from a layout-aware extractor, so spacing is explicit
// (no style defaults) and positions are honoured.
func (dw *docWriter) paragraphProps(b model.Block, layout bool) {
	sb := &dw.sb
	var props strings.Builder
	if b.Kind == model.Heading {
		lvl := b.Level
		if lvl < 1 {
			lvl = 1
		}
		if lvl > 2 {
			lvl = 2
		}
		fmt.Fprintf(&props, `<w:pStyle w:val="Heading%d"/>`, lvl)
	}
	// Tab stops for multi-segment lines.
	if tabs := dw.tabStops(b); tabs != "" {
		props.WriteString(tabs)
	}
	if layout || b.SpaceBefore > 0 || b.Leading > 0 {
		before := twips(math.Max(0, b.SpaceBefore))
		if b.Leading > 0 {
			fmt.Fprintf(&props, `<w:spacing w:before="%d" w:after="0" w:line="%d" w:lineRule="exact"/>`, before, twips(b.Leading))
		} else {
			fmt.Fprintf(&props, `<w:spacing w:before="%d" w:after="0"/>`, before)
		}
	}
	if b.IndentLeft > 0.5 || math.Abs(b.FirstIndent) > 0.5 {
		props.WriteString(`<w:ind`)
		if b.IndentLeft > 0.5 {
			fmt.Fprintf(&props, ` w:left="%d"`, twips(b.IndentLeft))
		}
		if b.FirstIndent > 0.5 {
			fmt.Fprintf(&props, ` w:firstLine="%d"`, twips(b.FirstIndent))
		} else if b.FirstIndent < -0.5 {
			fmt.Fprintf(&props, ` w:hanging="%d"`, twips(-b.FirstIndent))
		}
		props.WriteString(`/>`)
	}
	if jc := jcValue(b.Align); jc != "" {
		fmt.Fprintf(&props, `<w:jc w:val="%s"/>`, jc)
	}
	if props.Len() > 0 {
		sb.WriteString("<w:pPr>")
		sb.WriteString(props.String())
		sb.WriteString("</w:pPr>")
	}
}

func jcValue(a model.Alignment) string {
	switch a {
	case model.AlignCenter:
		return "center"
	case model.AlignRight:
		return "right"
	case model.AlignJustify:
		return "both"
	}
	return ""
}

// segmentTab returns the tab stop a segment needs, if any: a centre tab at
// CenterX, a right tab at the content edge for FlushRight, or a left tab at
// X for every segment after the first.
type tabStop struct {
	pos  int
	kind string // left, center, right
}

func (dw *docWriter) segmentTab(j int, seg model.Segment) (tabStop, bool) {
	switch {
	case seg.CenterX > 0:
		return tabStop{pos: twips(seg.CenterX), kind: "center"}, true
	case seg.FlushRight:
		return tabStop{pos: twips(dw.setup.ContentWidth()), kind: "right"}, true
	case j > 0 && seg.X > 0:
		return tabStop{pos: twips(seg.X), kind: "left"}, true
	}
	return tabStop{}, false
}

// tabStops returns the <w:tabs> element for a block's segments.
func (dw *docWriter) tabStops(b model.Block) string {
	seen := map[tabStop]bool{}
	var stops []tabStop
	for _, ln := range b.Lines {
		for j, seg := range ln.Segments {
			s, ok := dw.segmentTab(j, seg)
			if !ok || s.pos <= 0 || seen[s] {
				continue
			}
			seen[s] = true
			stops = append(stops, s)
		}
	}
	if len(stops) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<w:tabs>")
	for _, s := range stops {
		fmt.Fprintf(&sb, `<w:tab w:val="%s" w:pos="%d"/>`, s.kind, s.pos)
	}
	sb.WriteString("</w:tabs>")
	return sb.String()
}

func (dw *docWriter) writeParagraph(b model.Block, layout bool) {
	sb := &dw.sb
	sb.WriteString("<w:p>")
	dw.paragraphProps(b, layout)
	for i, ln := range b.Lines {
		if i > 0 {
			sb.WriteString("<w:r><w:br/></w:r>")
		}
		for j, seg := range ln.Segments {
			if _, ok := dw.segmentTab(j, seg); ok || j > 0 {
				sb.WriteString("<w:r><w:tab/></w:r>")
			}
			for _, r := range seg.Runs {
				dw.writeRun(r)
			}
		}
	}
	sb.WriteString("</w:p>")
}

func (dw *docWriter) writeRun(r model.Run) {
	if r.Image != nil {
		if xml := dw.drawingXML(r.Image); xml != "" {
			dw.sb.WriteString("<w:r>" + xml + "</w:r>")
		}
		return
	}
	text := sanitize(r.Text)
	if text == "" {
		return
	}
	sb := &dw.sb
	sb.WriteString("<w:r>")
	var props strings.Builder
	if r.Font != "" {
		// The Latin font from the PDF; complex scripts keep the document's
		// complex-script font, which is the one with the right glyphs.
		f := escapeAttr(r.Font)
		fmt.Fprintf(&props, `<w:rFonts w:ascii="%s" w:hAnsi="%s"/>`, f, f)
	}
	if r.Bold {
		props.WriteString("<w:b/><w:bCs/>")
	}
	if r.Italic {
		props.WriteString("<w:i/><w:iCs/>")
	}
	if r.Size > 0 {
		hp := int(math.Round(r.Size * 2))
		if hp < 2 {
			hp = 2
		}
		fmt.Fprintf(&props, `<w:sz w:val="%d"/><w:szCs w:val="%d"/>`, hp, hp)
	}
	if props.Len() > 0 {
		sb.WriteString("<w:rPr>")
		sb.WriteString(props.String())
		sb.WriteString("</w:rPr>")
	}
	sb.WriteString(`<w:t xml:space="preserve">`)
	_ = xml.EscapeText(sb, []byte(text)) // never fails on a strings.Builder
	sb.WriteString(`</w:t></w:r>`)
}

func escapeAttr(s string) string {
	var sb strings.Builder
	_ = xml.EscapeText(&sb, []byte(s))
	return sb.String()
}

// writeTable emits a fixed-layout table with the model's column widths.
func (dw *docWriter) writeTable(b model.Block) {
	t := b.Table
	if len(t.Rows) == 0 || len(t.ColWidths) == 0 {
		return
	}
	sb := &dw.sb
	total := 0.0
	for _, w := range t.ColWidths {
		total += w
	}
	sb.WriteString("<w:tbl><w:tblPr>")
	fmt.Fprintf(sb, `<w:tblW w:w="%d" w:type="dxa"/>`, twips(total))
	if b.IndentLeft > 0.5 {
		fmt.Fprintf(sb, `<w:tblInd w:w="%d" w:type="dxa"/>`, twips(b.IndentLeft))
	}
	if t.Ruled {
		color := t.BorderColor
		if color == "" {
			color = "000000"
		}
		sb.WriteString("<w:tblBorders>")
		for _, side := range []string{"top", "left", "bottom", "right", "insideH", "insideV"} {
			fmt.Fprintf(sb, `<w:%s w:val="single" w:sz="4" w:space="0" w:color="%s"/>`, side, escapeAttr(color))
		}
		sb.WriteString("</w:tblBorders>")
	}
	sb.WriteString(`<w:tblLayout w:type="fixed"/>`)
	sb.WriteString(`<w:tblCellMar><w:left w:w="60" w:type="dxa"/><w:right w:w="60" w:type="dxa"/></w:tblCellMar>`)
	sb.WriteString("</w:tblPr><w:tblGrid>")
	for _, w := range t.ColWidths {
		fmt.Fprintf(sb, `<w:gridCol w:w="%d"/>`, twips(w))
	}
	sb.WriteString("</w:tblGrid>")
	ncols := len(t.ColWidths)
	for _, row := range t.Rows {
		sb.WriteString("<w:tr>")
		col := 0
		for _, cell := range row {
			if col >= ncols {
				break
			}
			span := cell.Span
			if span < 1 {
				span = 1
			}
			if col+span > ncols {
				span = ncols - col
			}
			width := 0.0
			for c := col; c < col+span; c++ {
				width += t.ColWidths[c]
			}
			sb.WriteString("<w:tc><w:tcPr>")
			fmt.Fprintf(sb, `<w:tcW w:w="%d" w:type="dxa"/>`, twips(width))
			if span > 1 {
				fmt.Fprintf(sb, `<w:gridSpan w:val="%d"/>`, span)
			}
			sb.WriteString("</w:tcPr>")
			dw.writeCellContent(cell)
			sb.WriteString("</w:tc>")
			col += span
		}
		// Pad short rows so every row covers the whole grid.
		for ; col < ncols; col++ {
			fmt.Fprintf(sb, `<w:tc><w:tcPr><w:tcW w:w="%d" w:type="dxa"/></w:tcPr><w:p><w:pPr><w:spacing w:before="0" w:after="0"/></w:pPr></w:p></w:tc>`, twips(t.ColWidths[col]))
		}
		sb.WriteString("</w:tr>")
	}
	sb.WriteString("</w:tbl>")
}

func (dw *docWriter) writeCellContent(cell model.Cell) {
	sb := &dw.sb
	if len(cell.Lines) == 0 {
		sb.WriteString(`<w:p><w:pPr><w:spacing w:before="0" w:after="0"/></w:pPr></w:p>`)
		return
	}
	for _, ln := range cell.Lines {
		sb.WriteString("<w:p><w:pPr>")
		sb.WriteString(`<w:spacing w:before="0" w:after="0"/>`)
		if jc := jcValue(cell.Align); jc != "" {
			fmt.Fprintf(sb, `<w:jc w:val="%s"/>`, jc)
		}
		sb.WriteString("</w:pPr>")
		for j, seg := range ln.Segments {
			if j > 0 {
				sb.WriteString("<w:r><w:tab/></w:r>")
			}
			for _, r := range seg.Runs {
				dw.writeRun(r)
			}
		}
		sb.WriteString("</w:p>")
	}
}

// writeImage emits an inline picture in its own paragraph.
func (dw *docWriter) writeImage(b model.Block, layout bool) {
	xml := dw.drawingXML(b.Image)
	if xml == "" {
		return
	}
	sb := &dw.sb
	sb.WriteString("<w:p>")
	dw.paragraphProps(model.Block{Align: b.Image.Align, SpaceBefore: b.SpaceBefore, IndentLeft: b.IndentLeft}, layout)
	sb.WriteString("<w:r>" + xml + "</w:r>")
	sb.WriteString("</w:p>")
}

// drawingXML registers the image as a media part and returns the
// <w:drawing> element for an inline picture (empty if the image is unusable).
func (dw *docWriter) drawingXML(im *model.ImageData) string {
	if im == nil || len(im.Data) == 0 || im.Width <= 0 || im.Height <= 0 {
		return ""
	}
	dw.nextID++
	id := dw.nextID
	ext := "png"
	if im.Ext == "jpg" || im.Ext == "jpeg" {
		ext = "jpeg"
	}
	part := imagePart{name: fmt.Sprintf("image%d.%s", id, ext), rid: fmt.Sprintf("rId%d", 100+id), data: im.Data}
	dw.images = append(dw.images, part)

	// Keep the picture inside the text area.
	w, h := im.Width, im.Height
	if cw := dw.setup.ContentWidth(); cw > 0 && w > cw {
		h = h * cw / w
		w = cw
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, `<w:drawing><wp:inline distT="0" distB="0" distL="0" distR="0"><wp:extent cx="%d" cy="%d"/>`, emu(w), emu(h))
	fmt.Fprintf(&sb, `<wp:effectExtent l="0" t="0" r="0" b="0"/><wp:docPr id="%d" name="Picture %d"/>`, id, id)
	sb.WriteString(`<wp:cNvGraphicFramePr><a:graphicFrameLocks noChangeAspect="1"/></wp:cNvGraphicFramePr>`)
	sb.WriteString(`<a:graphic><a:graphicData uri="http://schemas.openxmlformats.org/drawingml/2006/picture"><pic:pic>`)
	fmt.Fprintf(&sb, `<pic:nvPicPr><pic:cNvPr id="%d" name="Picture %d"/><pic:cNvPicPr/></pic:nvPicPr>`, id, id)
	fmt.Fprintf(&sb, `<pic:blipFill><a:blip r:embed="%s"/><a:stretch><a:fillRect/></a:stretch></pic:blipFill>`, part.rid)
	fmt.Fprintf(&sb, `<pic:spPr><a:xfrm><a:off x="0" y="0"/><a:ext cx="%d" cy="%d"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></pic:spPr>`, emu(w), emu(h))
	sb.WriteString(`</pic:pic></a:graphicData></a:graphic></wp:inline></w:drawing>`)
	return sb.String()
}

func (dw *docWriter) writeSectPr() {
	s := dw.setup
	sb := &dw.sb
	sb.WriteString("<w:sectPr>")
	orient := ""
	if s.Width > s.Height {
		orient = ` w:orient="landscape"`
	}
	fmt.Fprintf(sb, `<w:pgSz w:w="%d" w:h="%d"%s/>`, twips(s.Width), twips(s.Height), orient)
	fmt.Fprintf(sb, `<w:pgMar w:top="%d" w:right="%d" w:bottom="%d" w:left="%d" w:header="720" w:footer="720" w:gutter="0"/>`,
		twips(s.MarginTop), twips(s.MarginRight), twips(s.MarginBottom), twips(s.MarginLeft))
	sb.WriteString("</w:sectPr>")
}

// sanitize removes characters that are illegal in XML 1.0 and normalises
// line breaks to spaces (runs never contain line breaks by construction).
func sanitize(s string) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t':
			sb.WriteRune(r)
		case r == '\n' || r == '\r':
			sb.WriteByte(' ')
		case r < 0x20, r == 0xFFFE, r == 0xFFFF:
			// drop
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}
