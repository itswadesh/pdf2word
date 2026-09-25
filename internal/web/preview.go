package web

import (
	"bytes"
	"errors"
	"fmt"
	"image/jpeg"
	"os"
	"path/filepath"
	"sync"

	"github.com/klippa-app/go-pdfium/requests"

	"pdf2word/internal/pdfiumx"
)

// previewSize is the box a page is fitted into.
type previewSize struct {
	name          string // as asked for in ?size=, and part of the cache file name
	width, height int
}

var (
	// The card's thumbnail: twice the 150x194 it is drawn at, so it stays
	// sharp on a high-density screen.
	sizeCard = previewSize{"", 300, 388}
	// The modal's page, drawn up to about 560x720, at one and a half times.
	sizeLarge = previewSize{"large", 840, 1088}
)

var (
	errPreviewGone = errors.New("the conversion has finished")
	errNoSuchPage  = errors.New("no such page")
)

// previewer renders thumbnails of the page a conversion is working on, for
// the scanner on the page. It keeps at most one document open across the
// whole server, so previews can never hold more than one PDFium instance
// and starve a conversion of one.
type previewer struct {
	mu    sync.Mutex
	jobID string
	doc   *pdfiumx.Doc
}

// thumbnail returns page n of the job's PDF as a JPEG fitted to size. A
// page is rendered once per size and kept in the job's directory, which
// the retention sweep deletes with everything else.
func (p *previewer) thumbnail(j *job, n int, size previewSize) ([]byte, error) {
	name := fmt.Sprintf("preview-%d.jpg", n)
	if size.name != "" {
		name = fmt.Sprintf("preview-%d-%s.jpg", n, size.name)
	}
	cached := filepath.Join(j.dir, name)
	if b, err := os.ReadFile(cached); err == nil {
		return b, nil
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	// Checked under the lock that forget takes, so a document is never
	// opened for a job whose conversion has already let go of it.
	if j.currentState().terminal() {
		return nil, errPreviewGone
	}
	if p.jobID != j.id {
		p.closeLocked()
		doc, err := pdfiumx.Open(j.input)
		if err != nil {
			return nil, err
		}
		p.doc, p.jobID = doc, j.id
	}
	if n < 1 || n > p.doc.Pages {
		return nil, errNoSuchPage
	}
	b, err := renderThumbnail(p.doc, n, size)
	if err != nil {
		return nil, err
	}
	os.WriteFile(cached, b, 0o600) // a cache: failing to keep it costs a re-render, nothing more
	return b, nil
}

func renderThumbnail(doc *pdfiumx.Doc, n int, size previewSize) ([]byte, error) {
	doc.Mu.Lock()
	defer doc.Mu.Unlock()
	res, err := doc.Instance.RenderPageInPixels(&requests.RenderPageInPixels{
		Page:   doc.Page(n),
		Width:  size.width,
		Height: size.height,
	})
	if err != nil {
		return nil, fmt.Errorf("page %d: render: %w", n, err)
	}
	defer res.Cleanup() // the pixel buffer lives in WebAssembly memory
	if res.Result.RenderedImage == nil {
		return nil, errors.New("pdfium returned no image")
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, res.Result.RenderedImage, &jpeg.Options{Quality: 72}); err != nil {
		return nil, fmt.Errorf("page %d: encode jpeg: %w", n, err)
	}
	return buf.Bytes(), nil
}

// forget closes the open document if it belongs to this job.
func (p *previewer) forget(jobID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.jobID == jobID {
		p.closeLocked()
	}
}

func (p *previewer) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closeLocked()
}

func (p *previewer) closeLocked() {
	if p.doc != nil {
		p.doc.Close()
	}
	p.doc, p.jobID = nil, ""
}
