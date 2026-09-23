// Command pdftrim writes a page range of a PDF to a new file using PDFium.
// It works on files that stricter parsers refuse to validate.
//
//	go run ./tools/pdftrim in.pdf out.pdf 1-25
package main

import (
	"fmt"
	"os"

	"github.com/klippa-app/go-pdfium/requests"

	"pdf2word/internal/pdfiumx"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: pdftrim in.pdf out.pdf PAGERANGE (e.g. 1-25 or 1,3,5-9)")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2], os.Args[3]); err != nil {
		fmt.Fprintln(os.Stderr, "pdftrim:", err)
		os.Exit(1)
	}
}

func run(in, out, pages string) error {
	d, err := pdfiumx.Open(in)
	if err != nil {
		return err
	}
	defer d.Close()
	d.Mu.Lock()
	defer d.Mu.Unlock()

	nd, err := d.Instance.FPDF_CreateNewDocument(&requests.FPDF_CreateNewDocument{})
	if err != nil {
		return err
	}
	defer d.Instance.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: nd.Document})
	if _, err := d.Instance.FPDF_ImportPages(&requests.FPDF_ImportPages{Destination: nd.Document, Source: d.Ref, PageRange: &pages}); err != nil {
		return fmt.Errorf("import pages %s: %w", pages, err)
	}
	f, err := os.Create(out)
	if err != nil {
		return err
	}
	if _, err := d.Instance.FPDF_SaveAsCopy(&requests.FPDF_SaveAsCopy{Document: nd.Document, FileWriter: f}); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
