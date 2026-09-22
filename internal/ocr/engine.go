// Package ocr recognises text in page images. The only engine shipped is a
// wrapper around the Tesseract command-line tool, but the Engine interface
// lets callers substitute another implementation (or a fake in tests).
package ocr

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"pdf2word/internal/model"
)

// Engine turns an encoded image into plain text.
type Engine interface {
	// Name identifies the engine in logs and reports.
	Name() string
	// Recognize runs OCR over img, whose encoding is given by ext
	// ("png", "jpg" or "tif"), and returns the recognised text with
	// paragraphs separated by blank lines.
	Recognize(ctx context.Context, img []byte, ext string) (string, error)
}

// TextToBlocks converts raw OCR output into paragraphs. Blank lines and form
// feeds separate paragraphs; wrapped lines inside a paragraph are joined with
// a space; a hyphen at a line end followed by a lowercase letter is treated
// as a soft hyphen and removed. Runs of whitespace collapse to one space.
func TextToBlocks(text string) []model.Block {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	text = strings.ReplaceAll(text, "\f", "\n\n")

	var blocks []model.Block
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			blocks = append(blocks, model.Block{Kind: model.Paragraph, Text: cur.String()})
			cur.Reset()
		}
	}

	for _, raw := range strings.Split(text, "\n") {
		line := strings.Join(strings.Fields(raw), " ")
		if line == "" {
			flush()
			continue
		}
		if cur.Len() == 0 {
			cur.WriteString(line)
			continue
		}
		prev := cur.String()
		if strings.HasSuffix(prev, "-") && startsLower(line) {
			cur.Reset()
			cur.WriteString(strings.TrimSuffix(prev, "-"))
		} else {
			cur.WriteByte(' ')
		}
		cur.WriteString(line)
	}
	flush()
	return blocks
}

func startsLower(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	return unicode.IsLower(r)
}
