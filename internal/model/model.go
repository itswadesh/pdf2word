// Package model defines the intermediate document representation shared by
// the extraction, OCR and DOCX-writing stages.
package model

import "unicode"

// BlockKind classifies a block of text.
type BlockKind int

const (
	// Paragraph is ordinary body text.
	Paragraph BlockKind = iota
	// Heading is a title or section heading; Block.Level gives its level.
	Heading
)

func (k BlockKind) String() string {
	switch k {
	case Paragraph:
		return "paragraph"
	case Heading:
		return "heading"
	}
	return "unknown"
}

// PageSource records where a page's text came from.
type PageSource int

const (
	// SourceEmpty means no text could be obtained for the page.
	SourceEmpty PageSource = iota
	// SourceText means the text came from the PDF's own text layer.
	SourceText
	// SourceOCR means the text was recognised from page images.
	SourceOCR
)

func (s PageSource) String() string {
	switch s {
	case SourceEmpty:
		return "empty"
	case SourceText:
		return "text"
	case SourceOCR:
		return "ocr"
	}
	return "unknown"
}

// Block is one paragraph or heading of plain text.
type Block struct {
	Kind  BlockKind
	Level int    // heading level (1 = top); 0 for paragraphs
	Text  string // plain text without a trailing newline
}

// Page is an ordered list of blocks belonging to one PDF page.
type Page struct {
	Number int // 1-based page number
	Source PageSource
	Blocks []Block
}

// TextChars returns the number of non-whitespace runes on the page.
func (p Page) TextChars() int {
	n := 0
	for _, b := range p.Blocks {
		for _, r := range b.Text {
			if !unicode.IsSpace(r) {
				n++
			}
		}
	}
	return n
}

// Document is the whole converted file.
type Document struct {
	Pages []Page
}
