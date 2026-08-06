package main

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

func TestDocx(t *testing.T) {
	// Odia text, an XML metacharacter, and pdftotext's page-break form feed.
	b, err := docx([]para{
		{text: "ଓଡ଼ିଆ ଭାଷା & ସାହିତ୍ୟ", sz: 48, bold: true, center: true},
		{text: clean("line\x0ctwo"), sz: 24, indent: true, brk: true},
	}, "Nirmala UI")
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, f := range z.File {
		r, _ := f.Open()
		d, _ := io.ReadAll(r)
		r.Close()
		got[f.Name] = string(d)
	}
	for _, want := range []string{"[Content_Types].xml", "_rels/.rels", "word/document.xml"} {
		if _, ok := got[want]; !ok {
			t.Fatalf("missing part %s", want)
		}
	}
	doc := got["word/document.xml"]
	if !strings.Contains(doc, "ଓଡ଼ିଆ ଭାଷା &amp; ସାହିତ୍ୟ") {
		t.Error("Odia text not present or ampersand not escaped")
	}
	if strings.ContainsRune(doc, '\x0c') || strings.Contains(doc, "&#xC;") {
		t.Error("form feed leaked into XML; Word will reject the file")
	}
	if n := strings.Count(doc, "<w:p>"); n != 2 {
		t.Errorf("got %d paragraphs, want 2", n)
	}
	if !strings.Contains(doc, `w:cs="Nirmala UI"`) {
		t.Error("complex-script font missing; Odia would render as boxes")
	}
	// Complex scripts ignore w:b and w:sz — only the Cs twins reach Odia glyphs.
	for _, want := range []string{`<w:jc w:val="center"/>`, `<w:ind w:firstLine="420"/>`,
		`<w:b/><w:bCs/>`, `<w:sz w:val="48"/><w:szCs w:val="48"/>`, `<w:br w:type="page"/>`} {
		if !strings.Contains(doc, want) {
			t.Errorf("formatting missing: %s", want)
		}
	}
}

const sampleHOCR = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE html PUBLIC "-//W3C//DTD XHTML 1.0 Transitional//EN" "x.dtd">
<html xmlns="http://www.w3.org/1999/xhtml"><body>
 <div class='ocr_page' id='page_1' title='image "p.png"; bbox 0 0 1200 1000'>
  <div class='ocr_photo' id='block_1_1' title="bbox 0 600 900 900"></div>
  <div class='ocr_carea' title="bbox 100 100 1000 320">
   <p class='ocr_par' title="bbox 100 100 1000 320">
    <span class='ocr_line' title="bbox 200 100 1000 200; x_size 100">
     <span class='ocrx_word' title='bbox 200 100 500 200; x_wconf 96'>ଓଡ଼ିଆ</span>
     <span class='ocrx_word' title='bbox 520 100 1000 200; x_wconf 96'>ଭାଷା</span>
    </span>
    <span class='ocr_line' title="bbox 100 220 1000 320; x_size 100">
     <span class='ocrx_word' title='bbox 100 220 1000 320; x_wconf 96'>&amp;ସାହିତ୍ୟ</span>
    </span>
   </p>
  </div>
  <div class='ocr_carea' title="bbox 450 400 650 500">
   <p class='ocr_par' title="bbox 450 400 650 500">
    <span class='ocr_caption' title="bbox 450 400 650 500; x_size 200">
     <span class='ocrx_word' title='bbox 450 400 650 500; x_wconf 90'>ପାଠରୁ</span>
    </span>
   </p>
  </div>
 </div>
</body></html>`

func TestParseHOCRLayout(t *testing.T) {
	pars, photos := parseHOCR([]byte(sampleHOCR))
	if len(pars) != 2 {
		t.Fatalf("got %d paragraphs, want 2", len(pars))
	}
	if len(pars[0]) != 2 {
		t.Errorf("first paragraph has %d lines, want 2", len(pars[0]))
	}
	if len(photos) != 1 || photos[0] != (box{0, 600, 900, 900}) {
		t.Errorf("photo regions = %v, want one box{0 600 900 900}", photos)
	}
	// The picture region is where stylised headings hide; the sparse pass keys off it.
	if !insideAny(box{100, 700, 400, 800}, photos) || insideAny(box{100, 100, 400, 200}, photos) {
		t.Error("insideAny misjudged a line against the photo region")
	}

	got := layout(pars, 12)
	if len(got) != 2 {
		t.Fatalf("got %d paragraphs, want 2", len(got))
	}

	body := got[0]
	if body.text != "ଓଡ଼ିଆ ଭାଷା &ସାହିତ୍ୟ" {
		t.Errorf("body text = %q; lines should rejoin into one reflowable paragraph", body.text)
	}
	if body.sz != 24 || body.bold {
		t.Errorf("body sz=%d bold=%v, want 24 and not bold (it is the median line)", body.sz, body.bold)
	}
	if body.center {
		t.Error("full-width paragraph reported as centred")
	}
	if !body.indent {
		t.Error("first line starts 100px in; first-line indent was dropped")
	}

	head := got[1]
	if !head.center || !head.bold || head.sz != 48 {
		t.Errorf("heading center=%v bold=%v sz=%d, want true true 48", head.center, head.bold, head.sz)
	}
	if head.indent {
		t.Error("centred single line should not also be indented")
	}
}

func TestTextParas(t *testing.T) {
	// pdftotext puts a form feed at the end of every page.
	got := textParas("one\ntwo\n\nthree\x0c\nfour\n")
	if len(got) != 3 {
		t.Fatalf("got %d paragraphs, want 3 (blank line ends a paragraph)", len(got))
	}
	if got[0].text != "one two" {
		t.Errorf("got %q, want %q; wrapped lines must rejoin", got[0].text, "one two")
	}
	if got[1].text != "three" || got[1].brk {
		t.Errorf("got %q brk=%v, want %q and no break mid-page", got[1].text, got[1].brk, "three")
	}
	if got[2].text != "four" || !got[2].brk {
		t.Errorf("got %q brk=%v, want %q starting a new Word page", got[2].text, got[2].brk, "four")
	}
}
