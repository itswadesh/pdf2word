package pdftext

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/ledongthuc/pdf"

	"pdf2word/internal/model"
)

// Warning describes a non-fatal problem encountered on one page.
type Warning struct {
	Page int
	Msg  string
}

func (w Warning) String() string { return fmt.Sprintf("page %d: %s", w.Page, w.Msg) }

// Extract reads the text layer of every page in the PDF at path.
//
// Pages whose content cannot be parsed are returned with Source ==
// model.SourceEmpty and a Warning, so one bad page does not abort the file.
// Encrypted files return an error mentioning "encrypted".
func Extract(path string) (*model.Document, []Warning, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}

	r, err := openReader(f, st.Size())
	if err != nil {
		return nil, nil, err
	}

	n := r.NumPage()
	doc := &model.Document{Pages: make([]model.Page, 0, n)}
	var warns []Warning
	for i := 1; i <= n; i++ {
		page := model.Page{Number: i, Source: model.SourceEmpty}
		blocks, perr := extractPage(r, i)
		if perr != nil {
			warns = append(warns, Warning{Page: i, Msg: perr.Error()})
		}
		if len(blocks) > 0 {
			page.Blocks = blocks
			page.Source = model.SourceText
		}
		doc.Pages = append(doc.Pages, page)
	}
	return doc, warns, nil
}

// openReader wraps pdf.NewReader, converting panics and encryption failures
// into ordinary errors.
func openReader(f *os.File, size int64) (r *pdf.Reader, err error) {
	defer func() {
		if p := recover(); p != nil {
			r, err = nil, fmt.Errorf("open pdf: %v", p)
		}
	}()
	r, err = pdf.NewReader(f, size)
	if err != nil {
		msg := strings.ToLower(err.Error())
		if errors.Is(err, pdf.ErrInvalidPassword) || strings.Contains(msg, "encrypt") || strings.Contains(msg, "password") {
			return nil, fmt.Errorf("open pdf: file is encrypted (not supported): %w", err)
		}
		return nil, fmt.Errorf("open pdf: %w", err)
	}
	if r.NumPage() == 0 {
		return nil, errors.New("open pdf: file has no pages")
	}
	return r, nil
}

// extractPage converts one page's glyphs into blocks, recovering from any
// panic raised by the PDF library on malformed content.
func extractPage(r *pdf.Reader, num int) (blocks []model.Block, err error) {
	defer func() {
		if p := recover(); p != nil {
			blocks, err = nil, fmt.Errorf("text extraction failed: %v", p)
		}
	}()
	p := r.Page(num)
	if p.V.IsNull() {
		return nil, errors.New("page object missing")
	}
	content := p.Content()
	glyphs := make([]Glyph, 0, len(content.Text))
	for _, t := range content.Text {
		glyphs = append(glyphs, Glyph{X: t.X, Y: t.Y, W: t.W, Size: t.FontSize, Font: t.Font, S: t.S})
	}
	return BuildBlocks(glyphs), nil
}
