// Package pdfimage extracts the images embedded in PDF pages so they can be
// handed to an OCR engine.
package pdfimage

import (
	"bytes"
	"fmt"
	"image"
	"io"
	"os"
	"sort"

	// Register decoders so image.DecodeConfig can read dimensions from the
	// formats pdfcpu emits (png, jpg, tif).
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/tiff"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

// minImageSide is the smallest width/height (in pixels) worth OCR-ing.
// Anything smaller is almost certainly a rule, bullet or icon.
const minImageSide = 50

// Image is one embedded raster image, already encoded in a file format that
// Tesseract can read directly. Width and Height are 0 when unknown.
type Image struct {
	Data   []byte
	Ext    string // "png", "jpg" or "tif"
	Width  int
	Height int
}

// Reader holds a parsed PDF so that images can be pulled from many pages
// without re-parsing the file each time.
type Reader struct {
	f   *os.File
	ctx *model.Context
}

// Open parses the PDF at path once. Call Close when done.
func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed
	conf.Cmd = model.EXTRACTIMAGES
	ctx, err := api.ReadValidateAndOptimize(f, conf)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("open pdf for image extraction: %w", err)
	}
	return &Reader{f: f, ctx: ctx}, nil
}

// Close releases the underlying file.
func (r *Reader) Close() error { return r.f.Close() }

// PageCount returns the number of pages in the PDF.
func (r *Reader) PageCount() int { return r.ctx.PageCount }

// PageImages returns the usable images on the given 1-based page, in a
// stable order. Pages without images return an empty slice and no error.
func (r *Reader) PageImages(page int) ([]Image, error) {
	if page < 1 || page > r.ctx.PageCount {
		return nil, fmt.Errorf("page %d out of range (1-%d)", page, r.ctx.PageCount)
	}
	found, err := pdfcpu.ExtractPageImages(r.ctx, page, false)
	if err != nil {
		return nil, fmt.Errorf("page %d: extract images: %w", page, err)
	}

	objNrs := make([]int, 0, len(found))
	for nr := range found {
		objNrs = append(objNrs, nr)
	}
	sort.Ints(objNrs)

	out := make([]Image, 0, len(found))
	for _, nr := range objNrs {
		im := found[nr]
		if im.Reader == nil {
			continue
		}
		data, err := io.ReadAll(im)
		if err != nil {
			return nil, fmt.Errorf("page %d: read image object %d: %w", page, nr, err)
		}
		if len(data) == 0 {
			continue
		}
		img := Image{Data: data, Ext: im.FileType, Width: im.Width, Height: im.Height}
		if img.Width == 0 || img.Height == 0 {
			// pdfcpu does not always fill these in; read them from the header.
			img.Width, img.Height = sniffSize(data)
		}
		if !usable(img) {
			continue
		}
		out = append(out, img)
	}
	return out, nil
}

// sniffSize decodes only the image header. It returns 0, 0 when the format
// is not recognised.
func sniffSize(data []byte) (w, h int) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

// usable reports whether an image is large enough to plausibly contain text.
// Images of unknown size are kept, since we cannot rule them out.
func usable(img Image) bool {
	if img.Width == 0 || img.Height == 0 {
		return true
	}
	return img.Width >= minImageSide && img.Height >= minImageSide
}
