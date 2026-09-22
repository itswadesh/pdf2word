// Package docx serialises a model.Document as a Word .docx file using only
// the standard library. The output is a minimal but valid OOXML package:
// paragraphs, two heading styles and page breaks between source pages.
package docx

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"pdf2word/internal/model"
)

// now is a variable so tests can pin timestamps if they need to.
var now = func() time.Time { return time.Now().UTC() }

// Write encodes doc as a .docx package and writes it to w.
func Write(w io.Writer, doc *model.Document) error {
	if doc == nil {
		doc = &model.Document{}
	}
	ts := now()
	stamp := ts.Format(time.RFC3339)

	parts := []struct{ name, body string }{
		{"[Content_Types].xml", contentTypesXML},
		{"_rels/.rels", rootRelsXML},
		{"word/document.xml", documentXML(doc)},
		{"word/styles.xml", stylesXML},
		{"word/_rels/document.xml.rels", documentRelsXML},
		{"docProps/core.xml", fmt.Sprintf(coreXMLTemplate, stamp, stamp)},
		{"docProps/app.xml", appXML},
	}

	zw := zip.NewWriter(w)
	for _, p := range parts {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: p.name, Method: zip.Deflate, Modified: ts})
		if err != nil {
			return fmt.Errorf("docx: create part %s: %w", p.name, err)
		}
		if _, err := io.WriteString(f, p.body); err != nil {
			return fmt.Errorf("docx: write part %s: %w", p.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("docx: finalize package: %w", err)
	}
	return nil
}

const pageBreakXML = `<w:p><w:r><w:br w:type="page"/></w:r></w:p>`

// documentXML renders word/document.xml: every block becomes a paragraph and
// consecutive pages are separated by a page break (even when a page is
// empty, so page numbering still lines up with the source PDF).
func documentXML(doc *model.Document) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	sb.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for i, page := range doc.Pages {
		if i > 0 {
			sb.WriteString(pageBreakXML)
		}
		for _, b := range page.Blocks {
			writeParagraph(&sb, b)
		}
	}
	sb.WriteString(sectPrXML)
	sb.WriteString(`</w:body></w:document>`)
	return sb.String()
}

func writeParagraph(sb *strings.Builder, b model.Block) {
	sb.WriteString("<w:p>")
	if b.Kind == model.Heading {
		lvl := b.Level
		if lvl < 1 {
			lvl = 1
		}
		if lvl > 2 {
			lvl = 2
		}
		fmt.Fprintf(sb, `<w:pPr><w:pStyle w:val="Heading%d"/></w:pPr>`, lvl)
	}
	if text := sanitize(b.Text); text != "" {
		sb.WriteString(`<w:r><w:t xml:space="preserve">`)
		// EscapeText never fails on a strings.Builder.
		_ = xml.EscapeText(sb, []byte(text))
		sb.WriteString(`</w:t></w:r>`)
	}
	sb.WriteString("</w:p>")
}

// sanitize removes characters that are illegal in XML 1.0 and normalises
// line breaks to spaces (blocks are single paragraphs by construction).
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
