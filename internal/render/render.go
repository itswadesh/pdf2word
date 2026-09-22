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
	"os"
	"sync"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"

	"pdf2word/internal/pdfimage"
)

// DefaultDPI is a good trade-off between OCR accuracy and speed.
const DefaultDPI = 300

var (
	poolOnce sync.Once
	pool     pdfium.Pool
	poolErr  error
)

// getPool compiles the embedded PDFium WebAssembly module once per process.
func getPool() (pdfium.Pool, error) {
	poolOnce.Do(func() {
		// A few instances so that several documents can be open at once
		// (each instance has its own WebAssembly memory).
		pool, poolErr = webassembly.Init(webassembly.Config{MinIdle: 1, MaxIdle: 4, MaxTotal: 4})
		if poolErr != nil {
			poolErr = fmt.Errorf("initialise pdfium: %w", poolErr)
		}
	})
	return pool, poolErr
}

// Renderer holds one open document. It is safe for concurrent use; renders
// are serialised because a PDFium instance is single-threaded.
type Renderer struct {
	mu    sync.Mutex
	f     *os.File
	inst  pdfium.Pdfium
	doc   references.FPDF_DOCUMENT
	pages int
	dpi   int
}

// Open loads the PDF at path for rendering at the given DPI (0 = DefaultDPI).
// Call Close when done.
func Open(path string, dpi int) (*Renderer, error) {
	if dpi <= 0 {
		dpi = DefaultDPI
	}
	p, err := getPool()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	inst, err := p.GetInstance(30 * time.Second)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("pdfium instance: %w", err)
	}
	doc, err := inst.OpenDocument(&requests.OpenDocument{FileReader: f, FileReaderSize: st.Size()})
	if err != nil {
		inst.Close()
		f.Close()
		return nil, fmt.Errorf("pdfium open: %w", err)
	}
	count, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
		inst.Close()
		f.Close()
		return nil, fmt.Errorf("pdfium page count: %w", err)
	}
	return &Renderer{f: f, inst: inst, doc: doc.Document, pages: count.PageCount, dpi: dpi}, nil
}

// PageCount returns the number of pages in the document.
func (r *Renderer) PageCount() int { return r.pages }

// DPI returns the rendering resolution.
func (r *Renderer) DPI() int { return r.dpi }

// PageImages renders the 1-based page to a single grayscale PNG. It satisfies
// convert.ImageSource so a rendered page can be OCR'd like an embedded scan.
func (r *Renderer) PageImages(page int) ([]pdfimage.Image, error) {
	if page < 1 || page > r.pages {
		return nil, fmt.Errorf("page %d out of range (1-%d)", page, r.pages)
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	res, err := r.inst.RenderPageInDPI(&requests.RenderPageInDPI{
		DPI:         r.dpi,
		Page:        requests.Page{ByIndex: &requests.PageByIndex{Document: r.doc, Index: page - 1}},
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
func (r *Renderer) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var errs []error
	if _, err := r.inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: r.doc}); err != nil {
		errs = append(errs, err)
	}
	if err := r.inst.Close(); err != nil {
		errs = append(errs, err)
	}
	if err := r.f.Close(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
