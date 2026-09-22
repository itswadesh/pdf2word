// Package render rasterises PDF pages with PDFium compiled to WebAssembly
// (via go-pdfium and wazero), so no cgo or external renderer is needed.
//
// It exists because a "scanned" page is not always an embedded bitmap: some
// print drivers (Microsoft Print To PDF among them) emit text as vector
// outlines. Rendering the whole page and OCR-ing the result works for every
// variant: bitmaps, outlines, JBIG2/JPX images, rotated or tiled scans.
package render

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"

	"github.com/klippa-app/go-pdfium/requests"

	"pdf2word/internal/pdfimage"
	"pdf2word/internal/pdfiumx"
)

// DefaultDPI is a good trade-off between OCR accuracy and speed.
const DefaultDPI = 300

// Renderer holds one open document. It is safe for concurrent use; renders
// are serialised because a PDFium instance is single-threaded.
type Renderer struct {
	doc *pdfiumx.Doc
	dpi int
}

// Open loads the PDF at path for rendering at the given DPI (0 = DefaultDPI).
// Call Close when done.
func Open(path string, dpi int) (*Renderer, error) {
	if dpi <= 0 {
		dpi = DefaultDPI
	}
	doc, err := pdfiumx.Open(path)
	if err != nil {
		return nil, err
	}
	return &Renderer{doc: doc, dpi: dpi}, nil
}

// PageCount returns the number of pages in the document.
func (r *Renderer) PageCount() int { return r.doc.Pages }

// DPI returns the rendering resolution.
func (r *Renderer) DPI() int { return r.dpi }

// PageImages renders the 1-based page to a single grayscale PNG. It satisfies
// convert.ImageSource so a rendered page can be OCR'd like an embedded scan.
func (r *Renderer) PageImages(page int) ([]pdfimage.Image, error) {
	if page < 1 || page > r.doc.Pages {
		return nil, fmt.Errorf("page %d out of range (1-%d)", page, r.doc.Pages)
	}
	r.doc.Mu.Lock()
	defer r.doc.Mu.Unlock()

	res, err := r.doc.Instance.RenderPageInDPI(&requests.RenderPageInDPI{
		DPI:         r.dpi,
		Page:        r.doc.Page(page),
		ImageFormat: requests.RenderImageFormatGrayscale,
	})
	if err != nil {
		return nil, fmt.Errorf("page %d: render: %w", page, err)
	}
	defer res.Cleanup() // the pixel buffer lives in WebAssembly memory

	if res.Result.RenderedImage == nil {
		return nil, errors.New("pdfium returned no image")
	}
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestSpeed}
	if err := enc.Encode(&buf, res.Result.RenderedImage); err != nil {
		return nil, fmt.Errorf("page %d: encode png: %w", page, err)
	}
	return []pdfimage.Image{{Data: buf.Bytes(), Ext: "png", Width: res.Result.Width, Height: res.Result.Height}}, nil
}

// Close releases the document, the PDFium instance and the file.
func (r *Renderer) Close() error { return r.doc.Close() }
