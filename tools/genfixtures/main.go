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
	"strings"

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
	if err := os.WriteFile(filepath.Join(outDir, "badannot.pdf"), buildBadAnnotPDF(), 0o644); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "layout.pdf"), buildLayoutPDF(), 0o644); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "scaled.pdf"), buildScaledPDF(), 0o644); err != nil {
		log.Fatal(err)
	}
	fmt.Println("wrote text.pdf, scanned.pdf, badannot.pdf, layout.pdf and scaled.pdf in", outDir)
}

// buildScaledPDF imitates two habits of real typesetting software that the
// simple fixtures do not have: text set at "1 pt" (Tf 1) and scaled up with
// the text matrix, and words positioned glyph by glyph with no space
// characters between them. A converter that trusts Tf produces 1 pt text,
// and one that only copies characters glues the words together.
func buildScaledPDF() []byte {
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

	var c strings.Builder
	// Heading: Tf 1, matrix scale 18.
	c.WriteString("BT /F1 1 Tf 18 0 0 18 72 700 Tm (Scaled Heading) Tj ET\n")
	// Body: Tf 1, matrix scale 11, three lines of a paragraph.
	c.WriteString("BT /F1 1 Tf 11 0 0 11 72 660 Tm (The body of this page is set at one point and) Tj ET\n")
	c.WriteString("BT /F1 1 Tf 11 0 0 11 72 646 Tm (scaled eleven times by the text matrix, as many) Tj ET\n")
	c.WriteString("BT /F1 1 Tf 11 0 0 11 72 632 Tm (page layout programs do.) Tj ET\n")
	// A line whose words are placed one by one, 0.3 em apart, without any
	// space characters, at a plain 11 pt.
	x := 72.0
	for _, w := range []string{"Words", "placed", "apart", "without", "spaces"} {
		fmt.Fprintf(&c, "BT /F1 11 Tf %.2f 600 Td (%s) Tj ET\n", x, w)
		x += helveticaWidth(w, 11) + 0.3*11
	}

	// Page 2 is an outlier: a caption 20 pt from the left and top edges,
	// well outside the 72 pt margins of page 1. It must get its own page
	// setup rather than pull the document's margins in.
	page2 := "BT /F1 9 Tf 20 772 Td (Edge caption on an outlier page) Tj ET\n" +
		"BT /F1 11 Tf 72 700 Td (Ordinary text on the same page.) Tj ET\n"

	add("<< /Type /Catalog /Pages 2 0 R >>")
	add("<< /Type /Pages /Kids [4 0 R 6 0 R] /Count 2 >>")
	add(fontDict)
	add("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /Font << /F1 3 0 R >> >> /Contents 5 0 R >>")
	add(stream(c.String()))
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

// helveticaWidth returns the advance of s in points at the given size using
// the Helvetica metrics above (good enough to right-align and centre text).
func helveticaWidth(s string, size float64) float64 {
	w := 0.0
	for _, r := range s {
		if r >= 32 && r <= 126 {
			w += float64(helveticaWidths[r-32])
		} else {
			w += 556
		}
	}
	return w / 1000 * size
}

// buildLayoutPDF imitates a government form (landscape A4): a centred bold
// title, an image, a key/value line with the value flush right, a wrapped
// body paragraph, a short-line list, a ruled 3x2 table drawn with stroked
// lines, and a footer with items at both edges. It drives the layout tests.
func buildLayoutPDF() []byte {
	const pageW, pageH = 842.0, 595.0
	const left, right = 36.0, 806.0
	var b bytes.Buffer
	var offsets []int
	b.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")
	add := func(body string) {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}
	var widths bytes.Buffer
	for i, w := range helveticaWidths {
		if i > 0 {
			widths.WriteByte(' ')
		}
		fmt.Fprintf(&widths, "%d", w)
	}
	fontDict := func(base string) string {
		return fmt.Sprintf("<< /Type /Font /Subtype /Type1 /BaseFont /%s /Encoding /WinAnsiEncoding /FirstChar 32 /LastChar 126 /Widths [%s] >>", base, widths.String())
	}

	// 60x60 gray image with a black square in the middle.
	const iw, ih = 60, 60
	pix := make([]byte, iw*ih)
	for y := 0; y < ih; y++ {
		for x := 0; x < iw; x++ {
			v := byte(255)
			if x > 15 && x < 45 && y > 15 && y < 45 {
				v = 0
			}
			pix[y*iw+x] = v
		}
	}
	imgObj := fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 8 /Length %d >>\nstream\n%s\nendstream", iw, ih, len(pix), pix)

	var c strings.Builder
	pdfEscape := strings.NewReplacer(`\`, `\\`, "(", `\(`, ")", `\)`)
	text := func(font string, size, x, y float64, s string) {
		fmt.Fprintf(&c, "BT /%s %.1f Tf %.2f %.2f Td (%s) Tj ET\n", font, size, x, y, pdfEscape.Replace(s))
	}
	centered := func(font string, size, y float64, s string) {
		text(font, size, (pageW-helveticaWidth(s, size))/2, y, s)
	}
	flushRight := func(font string, size, y float64, s string) {
		text(font, size, right-helveticaWidth(s, size), y, s)
	}
	hline := func(x1, x2, y float64) { fmt.Fprintf(&c, "%.2f %.2f m %.2f %.2f l S\n", x1, y, x2, y) }
	vline := func(x, y1, y2 float64) { fmt.Fprintf(&c, "%.2f %.2f m %.2f %.2f l S\n", x, y1, x, y2) }

	// Image top centre with a second one flush right on the same band (like
	// a logo and a QR code), title below.
	fmt.Fprintf(&c, "q 60 0 0 60 %.2f 520 cm /Im1 Do Q\n", (pageW-60)/2)
	fmt.Fprintf(&c, "q 60 0 0 60 %.2f 510 cm /Im1 Do Q\n", right-60)
	centered("F2", 14, 495, "Form No. 25")
	centered("F1", 12, 475, "Nil Certificate Of Encumbrance On Property")
	// Key/value line: left label, value flush right.
	text("F1", 9, left, 450, "Application No : 2026039031285")
	flushRight("F1", 9, 450, "Certificate No : EC0392026027049")
	// Wrapped body paragraph (two long lines, left aligned).
	text("F1", 9, left, 425, "Having applied to me for a certificate giving particulars of registered acts and encumbrances, if any in respect of the")
	text("F1", 9, left, 413, "undermentioned property, I hereby certify that a search has been made in the books and indexes for the said property.")
	// Short-line list: each line ends well before the right edge.
	text("F1", 9, left, 390, "a) The applicant has not undertaken the search himself.")
	text("F1", 9, left, 378, "b) The department will not be responsible for errors.")
	// Ruled table 3 columns x 2 rows: x 36..336, y 300..340 (rows at 320).
	fmt.Fprintf(&c, "0.5 w\n")
	for _, x := range []float64{36, 136, 236, 336} {
		vline(x, 300, 340)
	}
	for _, y := range []float64{340, 320, 300} {
		hline(36, 136, y)
		hline(136, 236, y)
		hline(236, 336, y)
	}
	text("F2", 9, 42, 327, "Sl. No.")
	text("F2", 9, 142, 327, "Village Name")
	text("F2", 9, 242, 327, "Area")
	text("F1", 9, 42, 307, "1")
	text("F1", 9, 142, 307, "Bhanapur - 42")
	text("F1", 9, 242, 307, "0.0186 Hectare")
	// Second table, grey rulings, header row spanning all three columns
	// (no interior verticals in the top row): y 240..280, rows at 260.
	fmt.Fprintf(&c, "0.5 0.5 0.5 RG\n")
	vline(36, 240, 280)
	vline(336, 240, 280)
	vline(136, 240, 260)
	vline(236, 240, 260)
	for _, y := range []float64{280, 260, 240} {
		hline(36, 336, y)
	}
	text("F2", 9, 42, 267, "Merged header")
	text("F1", 9, 42, 247, "1")
	text("F1", 9, 142, 247, "Two")
	text("F1", 9, 242, 247, "Three")
	fmt.Fprintf(&c, "0 0 0 RG\n")
	// Footer: left and right items on one line.
	text("F1", 9, left, 40, "Regn. Office: KATAKA")
	flushRight("F1", 9, 40, "Page 1 of 1")
	content := c.String()

	add("<< /Type /Catalog /Pages 2 0 R >>")
	add("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	add(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.0f %.0f] /Resources << /Font << /F1 4 0 R /F2 5 0 R >> /XObject << /Im1 6 0 R >> >> /Contents 7 0 R >>", pageW, pageH))
	add(fontDict("Helvetica"))
	add(fontDict("Helvetica-Bold"))
	add(imgObj)
	add(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))

	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return b.Bytes()
}

// buildBadAnnotPDF reproduces a real-world file from "Microsoft: Print To
// PDF": a page whose only content is an image, plus a /Redact annotation
// whose /OC entry is an array instead of a dictionary. Strict validators
// reject the whole file because of that annotation; we must still be able to
// pull the page image out for OCR.
func buildBadAnnotPDF() []byte {
	var b bytes.Buffer
	var offsets []int
	b.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	add := func(body string) {
		offsets = append(offsets, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}

	// 60x60 8-bit grayscale image: white with a black bar across the middle.
	const w, h = 60, 60
	pix := make([]byte, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := byte(255)
			if y > 25 && y < 35 {
				v = 0
			}
			pix[y*w+x] = v
		}
	}
	imgObj := fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height %d /ColorSpace /DeviceGray /BitsPerComponent 8 /Length %d >>\nstream\n%s\nendstream", w, h, len(pix), pix)
	content := "q 300 0 0 300 150 400 cm /Im1 Do Q\n"

	add("<< /Type /Catalog /Pages 2 0 R >>")
	add("<< /Type /Pages /Kids [3 0 R] /Count 1 >>")
	add("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << /XObject << /Im1 4 0 R >> >> /Contents 5 0 R /Annots [6 0 R] >>")
	add(imgObj)
	add(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
	// The offending annotation: /OC must be a dict (OCG/OCMD) but is an array here.
	add("<< /Type /Annot /Subtype /Redact /Rect [100 100 200 200] /F 4 /OC [1 0 0] >>")

	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offsets)+1)
	for _, o := range offsets {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offsets)+1, xref)
	return b.Bytes()
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
