// Package pdfiumx owns the process-wide PDFium (WebAssembly) pool and opens
// documents for the packages that render pages or read their layout.
package pdfiumx

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/references"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
)

var (
	poolOnce sync.Once
	pool     pdfium.Pool
	poolErr  error
)

// Pool compiles the embedded PDFium WebAssembly module once per process.
func Pool() (pdfium.Pool, error) {
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

// Doc is an open PDF bound to one PDFium instance. PDFium instances are
// single-threaded: callers must serialise use of Instance (Mu helps).
type Doc struct {
	Mu       sync.Mutex
	Instance pdfium.Pdfium
	Ref      references.FPDF_DOCUMENT
	Pages    int

	f *os.File
}

// Open loads the PDF at path. Call Close when done.
func Open(path string) (*Doc, error) {
	p, err := Pool()
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
	return &Doc{Instance: inst, Ref: doc.Document, Pages: count.PageCount, f: f}, nil
}

// Page returns the request selector for a 1-based page number.
func (d *Doc) Page(n int) requests.Page {
	return requests.Page{ByIndex: &requests.PageByIndex{Document: d.Ref, Index: n - 1}}
}

// Close releases the document, the PDFium instance and the file.
func (d *Doc) Close() error {
	d.Mu.Lock()
	defer d.Mu.Unlock()
	var errs []error
	if _, err := d.Instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: d.Ref}); err != nil {
		errs = append(errs, err)
	}
	if err := d.Instance.Close(); err != nil {
		errs = append(errs, err)
	}
	if err := d.f.Close(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
