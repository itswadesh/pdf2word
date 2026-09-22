// Command genfixtures writes the PDF fixtures used by the test suite:
//
//	testdata/text.pdf     – two pages with a real text layer (heading + paragraphs)
//	testdata/scanned.pdf  – one page containing only a PNG of rendered text
//
// Run from the repository root:
//
//	go run ./tools/genfixtures
package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

func main() {
	outDir := "testdata"
	if len(os.Args) > 1 {
		outDir = os.Args[1]
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "text.pdf"), buildTextPDF(), 0o644); err != nil {
		log.Fatal(err)
	}
	scanned, err := buildScannedPDF()
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "scanned.pdf"), scanned, 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Println("wrote", filepath.Join(outDir, "text.pdf"), "and", filepath.Join(outDir, "scanned.pdf"))
}

// helveticaWidths holds the standard Helvetica AFM advance widths for
// character codes 32..126 (in 1/1000 em), so the text layer carries real
// glyph widths like a production PDF would.
var helveticaWidths = []int{
	278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278, // 32-47
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556, // 48-63
	1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778, // 64-79
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556, // 80-95
	333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556, // 96-111
	556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584, // 112-126
}

// buildTextPDF hand-assembles a small PDF 1.4 file with two pages of
// Helvetica text. Coordinates are chosen so the layout rules in the design
// spec (line gap 14pt within a paragraph, 28pt between paragraphs, a 24pt
// heading, and a hyphenated line break) are all exercised.
func buildTextPDF() []byte {
	var b bytes.Buffer
	var offsets []int
	b.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	add := func(body string) {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	stream := func(content string) string {
		return fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content)
	}

	var widths bytes.Buffer
	for i, w := range helveticaWidths {
		if i > 0 {
			widths.WriteByte(' ')
		}
		fmt.Fprintf(&widths, "%d", w)
	}
	fontDict := fmt.Sprintf("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding /FirstChar 32 /LastChar 126 /Widths [%s] >>", widths.String())

	page1 := "BT\n" +
		"/F1 24 Tf\n72 720 Td\n(Quarterly Report) Tj\n" +
		"/F1 11 Tf\n0 -40 Td\n(This is the first paragraph of the docu-) Tj\n" +
		"0 -14 Td\n(ment used to test the converter. It has) Tj\n" +
		"0 -14 Td\n(three lines of text.) Tj\n" +
		"0 -28 Td\n(The second paragraph starts after a larger) Tj\n" +
		"0 -14 Td\n(vertical gap and also spans several) Tj\n" +
		"0 -14 Td\n(lines on the page.) Tj\n" +
		"ET\n"
	page2 := "BT\n/F1 11 Tf\n72 720 Td\n(Second page content here.) Tj\nET\n"

	add("<< /Type /Catalog /Pages 2 0 R >>")
	add("<< /Type /Pages /Kids [4 0 R 6 0 R] /Count 2 >>")
	add(fontDict)
	add("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents 5 0 R >>")
	add(stream(page1))
	add("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents 7 0 R >>")
	add(stream(page2))

	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return b.Bytes()
}

// buildScannedPDF renders a few lines of text into a 200-dpi Letter-sized
// PNG and wraps it in a single-page PDF with no text layer, imitating a
// scanner's output.
func buildScannedPDF() ([]byte, error) {
	const w, h = 1650, 2200
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)

	ttf, err := opentype.Parse(goregular.TTF)
	if err != nil {
		return nil, fmt.Errorf("parse font: %w", err)
	}
	face, err := opentype.NewFace(ttf, &opentype.FaceOptions{Size: 72, DPI: 72, Hinting: font.HintingFull})
	if err != nil {
		return nil, fmt.Errorf("font face: %w", err)
	}
	defer face.Close()

	d := &font.Drawer{Dst: img, Src: image.NewUniform(color.Black), Face: face}
	lines := []string{
		"SCANNED PAGE",
		"The quick brown fox",
		"jumps over the lazy dog.",
	}
	y := 300
	for _, line := range lines {
		d.Dot = fixed.P(150, y)
		d.DrawString(line)
		y += 120
	}

	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		return nil, fmt.Errorf("encode png: %w", err)
	}

	imp := pdfcpu.DefaultImportConfig()
	var out bytes.Buffer
	if err := api.ImportImages(nil, &out, []io.Reader{bytes.NewReader(pngBuf.Bytes())}, imp, nil); err != nil {
		return nil, fmt.Errorf("import image into pdf: %w", err)
	}
	return out.Bytes(), nil
}
