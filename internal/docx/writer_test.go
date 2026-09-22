package docx

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
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

func partNames(zr *zip.Reader) []string {
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	return names
}

// para is a flattened view of one <w:p> element.
type para struct {
	Style     string
	Text      string
	PageBreak bool
}

// paragraphs walks document.xml and flattens every <w:p>.
func paragraphs(t *testing.T, docXML string) []para {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(docXML))
	var out []para
	var cur *para
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
			case "p":
				out = append(out, para{})
				cur = &out[len(out)-1]
			case "pStyle":
				if cur != nil {
					cur.Style = attr(el, "val")
				}
			case "br":
				if cur != nil && attr(el, "type") == "page" {
					cur.PageBreak = true
				}
			case "t":
				if cur != nil {
					var s string
					if err := dec.DecodeElement(&s, &el); err != nil {
						t.Fatal(err)
					}
					cur.Text += s
				}
			}
		case xml.EndElement:
			if el.Name.Local == "p" {
				cur = nil
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

func sampleDoc() *model.Document {
	return &model.Document{Pages: []model.Page{
		{Number: 1, Source: model.SourceText, Blocks: []model.Block{
			{Kind: model.Heading, Level: 1, Text: "Title"},
			{Kind: model.Paragraph, Text: "Body one"},
			{Kind: model.Heading, Level: 2, Text: "Sub"},
		}},
		{Number: 2, Source: model.SourceOCR, Blocks: []model.Block{
			{Kind: model.Paragraph, Text: "Second"},
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
	want := []para{
		{Style: "Heading1", Text: "Title"},
		{Style: "", Text: "Body one"},
		{Style: "Heading2", Text: "Sub"},
		{PageBreak: true},
		{Style: "", Text: "Second"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d paragraphs %+v, want %d %+v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("paragraph %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestWrite_PageBreakBetweenEveryPageEvenIfEmpty(t *testing.T) {
	doc := &model.Document{Pages: []model.Page{
		{Number: 1, Blocks: []model.Block{{Text: "a"}}},
		{Number: 2}, // empty (e.g. scanned page with OCR off)
		{Number: 3, Blocks: []model.Block{{Text: "c"}}},
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
		{Text: "A & B < C > \"D\" \x01end\ttab"},
	}}}}
	zr := openZip(t, render(t, doc))
	got := paragraphs(t, readPart(t, zr, "word/document.xml"))
	if len(got) != 1 || got[0].Text != "A & B < C > \"D\" end\ttab" {
		t.Fatalf("got %+v", got)
	}
}

func TestWrite_AllPartsAreWellFormedXML(t *testing.T) {
	zr := openZip(t, render(t, sampleDoc()))
	for _, f := range zr.File {
		body := readPart(t, zr, f.Name)
		dec := xml.NewDecoder(strings.NewReader(body))
		for {
			_, err := dec.Token()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Errorf("%s is not well-formed XML: %v", f.Name, err)
				break
			}
		}
	}
}

func TestWrite_EmptyDocument(t *testing.T) {
	zr := openZip(t, render(t, &model.Document{}))
	body := readPart(t, zr, "word/document.xml")
	if !strings.Contains(body, "<w:sectPr") {
		t.Fatalf("empty document should still contain section properties:\n%s", body)
	}
	if got := paragraphs(t, body); len(got) != 0 {
		t.Fatalf("empty document should have no paragraphs, got %+v", got)
	}
}
