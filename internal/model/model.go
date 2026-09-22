// Package model defines the intermediate document representation shared by
// the extraction, OCR and DOCX-writing stages.
//
// Version 2 carries layout: runs with fonts, alignment, indents, tab-separated
// segments, tables and images. Plain producers (OCR, the fallback text
// extractor) use Para/HeadingBlock, which build single-run paragraphs.
package model

import (
	"strings"
	"unicode"
)

// BlockKind classifies a block.
type BlockKind int

const (
	// Paragraph is ordinary body text.
	Paragraph BlockKind = iota
	// Heading is a title or section heading; Block.Level gives its level.
	Heading
	// Table holds Block.Table.
	Table
	// Image holds Block.Image.
	Image
)

func (k BlockKind) String() string {
	switch k {
	case Paragraph:
		return "paragraph"
	case Heading:
		return "heading"
	case Table:
		return "table"
	case Image:
		return "image"
	}
	return "unknown"
}

// Alignment is horizontal paragraph or cell alignment.
type Alignment int

const (
	AlignLeft Alignment = iota
	AlignCenter
	AlignRight
	AlignJustify
)

func (a Alignment) String() string {
	switch a {
	case AlignCenter:
		return "center"
	case AlignRight:
		return "right"
	case AlignJustify:
		return "justify"
	}
	return "left"
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

// Run is a span of text with one set of character formatting, or an inline
// picture when Image is set (Text is then ignored).
type Run struct {
	Text   string
	Bold   bool
	Italic bool
	Size   float64 // points; 0 = inherit the document default
	Font   string  // family name; "" = document default
	Image  *ImageData
}

// Segment is a horizontally positioned piece of a line. A line with several
// segments is rendered with tab stops so columns stay aligned.
type Segment struct {
	X          float64 // left edge in points from the page's left margin
	CenterX    float64 // > 0: centred on this position (centre tab stop)
	FlushRight bool    // ends at the right margin: rendered with a right tab
	Runs       []Run
}

// Text joins the segment's runs.
func (s Segment) Text() string {
	var sb strings.Builder
	for _, r := range s.Runs {
		sb.WriteString(r.Text)
	}
	return sb.String()
}

// Line is one visual line. Consecutive lines in a block are separated by
// hard line breaks.
type Line struct {
	Segments []Segment
}

// Text joins the line's segments with single spaces.
func (l Line) Text() string {
	parts := make([]string, 0, len(l.Segments))
	for _, s := range l.Segments {
		parts = append(parts, s.Text())
	}
	return strings.Join(parts, " ")
}

// Cell is one table cell. Span > 1 makes it cover that many grid columns;
// a row's cells then number fewer than the grid's columns.
type Cell struct {
	Lines []Line
	Align Alignment
	Span  int
}

// Text joins the cell's lines with newlines.
func (c Cell) Text() string {
	parts := make([]string, 0, len(c.Lines))
	for _, l := range c.Lines {
		parts = append(parts, l.Text())
	}
	return strings.Join(parts, "\n")
}

// TableData is a grid of cells with fixed column widths.
type TableData struct {
	ColWidths   []float64 // points; len == number of grid columns
	Rows        [][]Cell  // each row's spans add up to len(ColWidths)
	Ruled       bool      // draw borders
	BorderColor string    // hex RRGGBB; "" = black
}

// ImageData is a raster image placed in the flow.
type ImageData struct {
	Data   []byte
	Ext    string  // "png" or "jpg"
	Width  float64 // display size in points
	Height float64
	Align  Alignment
}

// Block is one flow element: a paragraph, heading, table or image.
type Block struct {
	Kind        BlockKind
	Level       int       // heading level (1 = top); 0 otherwise
	Align       Alignment // paragraphs, headings, images
	IndentLeft  float64   // points
	FirstIndent float64   // points, relative to IndentLeft (may be negative)
	SpaceBefore float64   // points of vertical space above the block
	Leading     float64   // points from baseline to baseline; 0 = Word default

	Lines []Line     // Paragraph and Heading content
	Table *TableData // Kind == Table
	Image *ImageData // Kind == Image
}

// Para builds a plain single-run paragraph.
func Para(text string) Block {
	return Block{Kind: Paragraph, Lines: []Line{{Segments: []Segment{{Runs: []Run{{Text: text}}}}}}}
}

// HeadingBlock builds a plain single-run heading.
func HeadingBlock(level int, text string) Block {
	b := Para(text)
	b.Kind = Heading
	b.Level = level
	return b
}

// Text returns the block's plain text: lines joined by newlines, tables by
// rows of tab-separated cells. Images have no text.
func (b Block) Text() string {
	switch b.Kind {
	case Table:
		if b.Table == nil {
			return ""
		}
		var rows []string
		for _, row := range b.Table.Rows {
			var cells []string
			for _, c := range row {
				cells = append(cells, c.Text())
			}
			rows = append(rows, strings.Join(cells, "\t"))
		}
		return strings.Join(rows, "\n")
	case Image:
		return ""
	}
	parts := make([]string, 0, len(b.Lines))
	for _, l := range b.Lines {
		parts = append(parts, l.Text())
	}
	return strings.Join(parts, "\n")
}

// Page is an ordered list of blocks belonging to one PDF page.
type Page struct {
	Number int // 1-based page number
	Source PageSource
	Width  float64 // points; 0 = unknown
	Height float64
	Blocks []Block
}

// TextChars returns the number of non-whitespace runes on the page.
func (p Page) TextChars() int {
	n := 0
	for _, b := range p.Blocks {
		for _, r := range b.Text() {
			if !unicode.IsSpace(r) {
				n++
			}
		}
	}
	return n
}

// PageSetup is the section layout derived from the source pages.
type PageSetup struct {
	Width, Height                                    float64 // points
	MarginTop, MarginRight, MarginBottom, MarginLeft float64 // points
}

// ContentWidth is the width available to text.
func (s PageSetup) ContentWidth() float64 { return s.Width - s.MarginLeft - s.MarginRight }

// Document is the whole converted file.
type Document struct {
	Pages []Page
	// Setup, when set, drives the Word page size and margins; otherwise
	// US Letter with one-inch margins is used.
	Setup *PageSetup
}
