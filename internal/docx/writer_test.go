package docx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"image"
	"image/color"
	"image/png"
	"io"
	"strings"
	"testing"

	"pdf2word/internal/model"
)

func render(t *testing.T, doc *model.Document) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := Write(&buf, doc); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return buf.Bytes()
}

func openZip(t *testing.T, data []byte) *zip.Reader {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("output is not a zip: %v", err)
	}
	return zr
}

func readPart(t *testing.T, zr *zip.Reader, name string) string {
	t.Helper()
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			b, err := io.ReadAll(rc)
			if err != nil {
				t.Fatal(err)
			}
			return string(b)
		}
	}
	t.Fatalf("part %q missing from package; have %v", name, partNames(zr))
	return ""
}

func hasPart(zr *zip.Reader, name string) bool {
	for _, f := range zr.File {
		if f.Name == name {
			return true
		}
	}
	return false
}

func partNames(zr *zip.Reader) []string {
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	return names
}

// para is a flattened view of one body-level <w:p> element.
type para struct {
	Style     string
	Text      string
	PageBreak bool
	Align     string
	Bold      bool // any run bold
	Italic    bool
	Sizes     []string
	Fonts     []string
	Tabs      int // <w:tab/> run elements
	Breaks    int // <w:br/> without type
	TabStops  []string
	Before    string
	IndLeft   string
	Drawing   bool
}

// paragraphs walks document.xml and flattens every body-level <w:p>
// (paragraphs inside tables are skipped).
func paragraphs(t *testing.T, docXML string) []para {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(docXML))
	var out []para
	var cur *para
	inTable := 0
	inPPr := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("document.xml is not well-formed: %v", err)
		}
		switch el := tok.(type) {
		case xml.StartElement:
			switch el.Name.Local {
			case "tbl":
				inTable++
			case "p":
				if inTable == 0 {
					out = append(out, para{})
					cur = &out[len(out)-1]
				}
			case "pPr":
				inPPr = cur != nil
			case "pStyle":
				if cur != nil {
					cur.Style = attr(el, "val")
				}
			case "jc":
				if cur != nil && inPPr {
					cur.Align = attr(el, "val")
				}
			case "spacing":
				if cur != nil && inPPr {
					cur.Before = attr(el, "before")
				}
			case "ind":
				if cur != nil && inPPr {
					cur.IndLeft = attr(el, "left")
				}
			case "tab":
				if cur != nil {
					if inPPr {
						cur.TabStops = append(cur.TabStops, attr(el, "val")+"@"+attr(el, "pos"))
					} else {
						cur.Tabs++
					}
				}
			case "br":
				if cur != nil {
					if attr(el, "type") == "page" {
						cur.PageBreak = true
					} else {
						cur.Breaks++
					}
				}
			case "b":
				if cur != nil {
					cur.Bold = true
				}
			case "i":
				if cur != nil {
					cur.Italic = true
				}
			case "sz":
				if cur != nil {
					cur.Sizes = append(cur.Sizes, attr(el, "val"))
				}
			case "rFonts":
				if cur != nil {
					cur.Fonts = append(cur.Fonts, attr(el, "ascii"))
				}
			case "drawing":
				if cur != nil {
					cur.Drawing = true
				}
			case "t":
				if cur != nil && inTable == 0 {
					var s string
					if err := dec.DecodeElement(&s, &el); err != nil {
						t.Fatal(err)
					}
					cur.Text += s
				}
			}
		case xml.EndElement:
			switch el.Name.Local {
			case "tbl":
				inTable--
			case "p":
				if inTable == 0 {
					cur = nil
				}
			case "pPr":
				inPPr = false
			}
		}
	}
	return out
}

func attr(el xml.StartElement, local string) string {
	for _, a := range el.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

func wellFormed(t *testing.T, name, body string) {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(body))
	for {
		_, err := dec.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("%s is not well-formed XML: %v", name, err)
		}
	}
}

func sampleDoc() *model.Document {
	return &model.Document{Pages: []model.Page{
		{Number: 1, Source: model.SourceText, Blocks: []model.Block{
			model.HeadingBlock(1, "Title"),
			model.Para("Body one"),
			model.HeadingBlock(2, "Sub"),
		}},
		{Number: 2, Source: model.SourceOCR, Blocks: []model.Block{
			model.Para("Second"),
		}},
	}}
}

func TestWrite_RequiredParts(t *testing.T) {
	zr := openZip(t, render(t, sampleDoc()))
	for _, name := range []string{
		"[Content_Types].xml",
		"_rels/.rels",
		"word/document.xml",
		"word/styles.xml",
		"word/_rels/document.xml.rels",
		"docProps/core.xml",
		"docProps/app.xml",
	} {
		readPart(t, zr, name)
	}
	ct := readPart(t, zr, "[Content_Types].xml")
	if !strings.Contains(ct, "wordprocessingml.document.main+xml") {
		t.Errorf("content types missing main document override:\n%s", ct)
	}
}

func TestWrite_ParagraphsStylesAndPageBreaks(t *testing.T) {
	zr := openZip(t, render(t, sampleDoc()))
	got := paragraphs(t, readPart(t, zr, "word/document.xml"))
	type want struct {
		style, text string
		brk         bool
	}
	wants := []want{{"Heading1", "Title", false}, {"", "Body one", false}, {"Heading2", "Sub", false}, {"", "", true}, {"", "Second", false}}
	if len(got) != len(wants) {
		t.Fatalf("got %d paragraphs %+v, want %d", len(got), got, len(wants))
	}
	for i, w := range wants {
		if got[i].Style != w.style || got[i].Text != w.text || got[i].PageBreak != w.brk {
			t.Errorf("paragraph %d = %+v, want %+v", i, got[i], w)
		}
	}
}

func TestWrite_PageBreakBetweenEveryPageEvenIfEmpty(t *testing.T) {
	doc := &model.Document{Pages: []model.Page{
		{Number: 1, Blocks: []model.Block{model.Para("a")}},
		{Number: 2}, // empty (e.g. scanned page with OCR off)
		{Number: 3, Blocks: []model.Block{model.Para("c")}},
	}}
	zr := openZip(t, render(t, doc))
	got := paragraphs(t, readPart(t, zr, "word/document.xml"))
	breaks := 0
	for _, p := range got {
		if p.PageBreak {
			breaks++
		}
	}
	if breaks != 2 {
		t.Fatalf("got %d page breaks, want 2: %+v", breaks, got)
	}
	if got[len(got)-1].PageBreak {
		t.Fatal("document must not end with a page break")
	}
}

func TestWrite_EscapesSpecialCharsAndDropsControlChars(t *testing.T) {
	doc := &model.Document{Pages: []model.Page{{Number: 1, Blocks: []model.Block{
		model.Para("A & B < C > \"D\" \x01end\ttab"),
	}}}}
	zr := openZip(t, render(t, doc))
	got := paragraphs(t, readPart(t, zr, "word/document.xml"))
	if len(got) != 1 || got[0].Text != "A & B < C > \"D\" end\ttab" {
		t.Fatalf("got %+v", got)
	}
}

func TestWrite_AllPartsAreWellFormedXML(t *testing.T) {
	zr := openZip(t, render(t, richDoc()))
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "word/media/") {
			continue
		}
		wellFormed(t, f.Name, readPart(t, zr, f.Name))
	}
}

func TestWrite_EmptyDocument(t *testing.T) {
	zr := openZip(t, render(t, &model.Document{}))
	body := readPart(t, zr, "word/document.xml")
	if !strings.Contains(body, "<w:sectPr") || !strings.Contains(body, `w:w="12240" w:h="15840"`) {
		t.Fatalf("empty document should default to Letter with section properties:\n%s", body)
	}
	if got := paragraphs(t, body); len(got) != 0 {
		t.Fatalf("empty document should have no paragraphs, got %+v", got)
	}
}

// --- layout features -------------------------------------------------------

func run(text string, bold, italic bool, size float64, font string) model.Run {
	return model.Run{Text: text, Bold: bold, Italic: italic, Size: size, Font: font}
}

func line(runs ...model.Run) model.Line {
	return model.Line{Segments: []model.Segment{{Runs: runs}}}
}

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			img.Set(x, y, color.RGBA{R: 200, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// richDoc exercises every layout feature on one landscape page.
func richDoc() *model.Document {
	setup := &model.PageSetup{Width: 842, Height: 595, MarginTop: 36, MarginRight: 36, MarginBottom: 36, MarginLeft: 36}
	return &model.Document{
		Setup: setup,
		Pages: []model.Page{{
			Number: 1, Source: model.SourceText, Width: 842, Height: 595,
			Blocks: []model.Block{
				{Kind: model.Heading, Level: 2, Align: model.AlignCenter, SpaceBefore: 12,
					Lines: []model.Line{line(run("Form No. 25", true, false, 14, "Arial"))}},
				{Kind: model.Paragraph, SpaceBefore: 6, Lines: []model.Line{{Segments: []model.Segment{
					{X: 0, Runs: []model.Run{run("Application No : 1", false, false, 9, "Times New Roman")}},
					{X: 640, FlushRight: true, Runs: []model.Run{run("Certificate No : 2", false, false, 9, "Times New Roman")}},
				}}}},
				{Kind: model.Paragraph, IndentLeft: 18, FirstIndent: -18, Lines: []model.Line{
					line(run("a) first line", false, true, 9, "")),
					line(run("second line", false, false, 9, "")),
				}},
				{Kind: model.Table, SpaceBefore: 8, Table: &model.TableData{
					ColWidths: []float64{60, 120}, Ruled: true,
					Rows: [][]model.Cell{
						{{Lines: []model.Line{line(run("Sl.", true, false, 9, ""))}, Align: model.AlignCenter}, {Lines: []model.Line{line(run("Village", true, false, 9, ""))}}},
						{{Lines: []model.Line{line(run("1", false, false, 7, ""))}, Align: model.AlignCenter}, {}},
					},
				}},
				{Kind: model.Image, SpaceBefore: 4, Image: &model.ImageData{Data: nil, Ext: "png", Width: 50, Height: 50, Align: model.AlignCenter}},
			},
		}},
	}
}

func TestWrite_RunFormattingAlignmentSpacingIndent(t *testing.T) {
	zr := openZip(t, render(t, richDoc()))
	got := paragraphs(t, readPart(t, zr, "word/document.xml"))
	if len(got) < 3 {
		t.Fatalf("paragraphs = %+v", got)
	}
	h := got[0]
	if h.Style != "Heading2" || h.Align != "center" || !h.Bold || h.Before != "240" || len(h.Sizes) == 0 || h.Sizes[0] != "28" || len(h.Fonts) == 0 || h.Fonts[0] != "Arial" {
		t.Errorf("heading = %+v; want centred bold 14pt Arial with 12pt before", h)
	}
	kv := got[1]
	if kv.Tabs != 1 || len(kv.TabStops) != 1 || kv.TabStops[0] != "right@15400" || kv.Text != "Application No : 1Certificate No : 2" {
		t.Errorf("key/value line = %+v; want one right tab stop at the content edge (770pt = 15400 twips)", kv)
	}
	list := got[2]
	if list.Breaks != 1 || !list.Italic || list.IndLeft != "360" {
		t.Errorf("list paragraph = %+v; want one line break, italic run, 18pt left indent", list)
	}
	if !strings.Contains(readPart(t, zr, "word/document.xml"), `w:hanging="360"`) {
		t.Error("hanging indent missing")
	}
}

func TestWrite_TableStructure(t *testing.T) {
	zr := openZip(t, render(t, richDoc()))
	body := readPart(t, zr, "word/document.xml")
	if strings.Count(body, "<w:tbl>") != 1 || strings.Count(body, "<w:tr>") != 2 || strings.Count(body, "<w:tc>") != 4 {
		t.Fatalf("table shape wrong: tbl=%d tr=%d tc=%d", strings.Count(body, "<w:tbl>"), strings.Count(body, "<w:tr>"), strings.Count(body, "<w:tc>"))
	}
	for _, want := range []string{
		`<w:tblW w:w="3600" w:type="dxa"/>`,
		`<w:gridCol w:w="1200"/>`, `<w:gridCol w:w="2400"/>`,
		`<w:insideV w:val="single"`, `<w:tblLayout w:type="fixed"/>`,
		`<w:tcW w:w="1200" w:type="dxa"/>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("table XML missing %s", want)
		}
	}
	// Cell paragraphs are not counted as body paragraphs, but their text exists.
	if !strings.Contains(body, ">Village<") || !strings.Contains(body, ">Sl.<") {
		t.Error("cell text missing")
	}
	// A table followed by the end of the body needs a trailing paragraph.
	end := &model.Document{Pages: []model.Page{{Number: 1, Blocks: []model.Block{richDoc().Pages[0].Blocks[3]}}}}
	endBody := readPart(t, openZip(t, render(t, end)), "word/document.xml")
	if !strings.Contains(endBody, "</w:tbl><w:p/><w:sectPr>") {
		t.Errorf("body ending with a table must get an empty paragraph before sectPr:\n%s", endBody[len(endBody)-200:])
	}
}

func TestWrite_InlineImage(t *testing.T) {
	doc := richDoc()
	doc.Pages[0].Blocks[4].Image.Data = tinyPNG(t)
	zr := openZip(t, render(t, doc))
	body := readPart(t, zr, "word/document.xml")
	got := paragraphs(t, body)
	last := got[len(got)-1]
	if !last.Drawing || last.Align != "center" {
		t.Errorf("image paragraph = %+v; want a centred drawing", last)
	}
	if !hasPart(zr, "word/media/image1.png") {
		t.Errorf("media part missing; parts = %v", partNames(zr))
	}
	rels := readPart(t, zr, "word/_rels/document.xml.rels")
	if !strings.Contains(rels, `Target="media/image1.png"`) || !strings.Contains(rels, "relationships/image") {
		t.Errorf("image relationship missing:\n%s", rels)
	}
	if !strings.Contains(readPart(t, zr, "[Content_Types].xml"), `Extension="png"`) {
		t.Error("png content type missing")
	}
	// 50pt = 635000 EMU
	if !strings.Contains(body, `cx="635000" cy="635000"`) || !strings.Contains(body, `r:embed="rId101"`) {
		t.Error("drawing extent or blip reference wrong")
	}
}

func TestWrite_ImageWithoutDataIsSkipped(t *testing.T) {
	zr := openZip(t, render(t, richDoc())) // image block has nil Data
	if hasPart(zr, "word/media/image1.png") || strings.Contains(readPart(t, zr, "word/document.xml"), "<w:drawing>") {
		t.Fatal("an image without data must not be written")
	}
}

func TestWrite_PageSetupFromDocument(t *testing.T) {
	zr := openZip(t, render(t, richDoc()))
	body := readPart(t, zr, "word/document.xml")
	for _, want := range []string{
		`<w:pgSz w:w="16840" w:h="11900" w:orient="landscape"/>`,
		`<w:pgMar w:top="720" w:right="720" w:bottom="720" w:left="720"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("sectPr missing %s in %s", want, body[strings.Index(body, "<w:sectPr>"):])
		}
	}
}

func TestWrite_PlainPagesKeepStyleSpacing(t *testing.T) {
	// Pages without layout information (OCR, fallback) must not get explicit
	// zero spacing, so the Normal style's paragraph spacing still applies.
	zr := openZip(t, render(t, sampleDoc()))
	got := paragraphs(t, readPart(t, zr, "word/document.xml"))
	if got[1].Before != "" {
		t.Fatalf("plain paragraph has explicit spacing %+v", got[1])
	}
}
