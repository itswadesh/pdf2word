// pdf2word serves a page that converts every page of a PDF into one .docx,
// with a Word page break where each PDF page ended. Odia (or any script)
// survives because everything stays UTF-8 and the document.xml asks Word for
// a font with Indic glyphs.
//
// Formatting survives because OCR runs in hOCR mode instead of plain text:
// tesseract's layout analysis hands back paragraphs, line boxes and x_size,
// which become Word paragraphs, alignment, first-line indents and font sizes.
// Bold/italic detection died with the legacy engine in tesseract 4, so glyph
// height is the only weight signal left — big text becomes a bold heading.
//
// No Go dependencies: poppler + tesseract do the work, archive/zip writes
// the .docx (a docx is a zip with three XML parts).
package main

import (
	"archive/zip"
	"bytes"
	"embed"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

//go:embed index.html
var page []byte

// Embedded at its real path, so http.FS serves it as /static/banner.png.
//
//go:embed static
var static embed.FS

var (
	addr     = flag.String("addr", "127.0.0.1:8080", "listen address")
	lang     = flag.String("lang", "ori", "tesseract language code (ori = Odia)")
	dpi      = flag.String("dpi", "300", "render DPI before OCR")
	font     = flag.String("font", "Nirmala UI", "font Word uses for the text")
	basePt   = flag.Int("pt", 12, "point size body text maps to; everything else scales off it")
	headings = flag.Bool("headings", true, "second OCR pass to recover text the layout analyser filed as a picture (headings in coloured boxes); doubles OCR time")
	maxSize  = flag.Int64("max", 300<<20, "max upload bytes")
)

func main() {
	flag.Parse()
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(page)
	})
	http.Handle("/static/", http.FileServer(http.FS(static)))
	http.HandleFunc("/convert", convert)
	log.Printf("http://%s  (lang=%s dpi=%s)", *addr, *lang, *dpi)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

func convert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST a pdf", http.StatusMethodNotAllowed)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, *maxSize)
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		http.Error(w, "upload too large or malformed", http.StatusBadRequest)
		return
	}
	defer r.MultipartForm.RemoveAll()

	src, hdr, err := r.FormFile("pdf")
	if err != nil {
		http.Error(w, "no file in field 'pdf'", http.StatusBadRequest)
		return
	}
	defer src.Close()

	dir, err := os.MkdirTemp("", "pdf2word")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(dir)

	pdf := filepath.Join(dir, "in.pdf")
	f, err := os.Create(pdf)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_, err = io.Copy(f, src)
	f.Close()
	if err != nil {
		http.Error(w, "read failed: "+err.Error(), http.StatusBadRequest)
		return
	}
	if head, _ := os.ReadFile(pdf); !bytes.HasPrefix(head, []byte("%PDF-")) {
		http.Error(w, "not a PDF", http.StatusBadRequest)
		return
	}

	paras, how, err := pdfDoc(pdf, dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if len(paras) == 0 {
		http.Error(w, "no text came out — try a higher -dpi, or check the -lang traineddata", http.StatusUnprocessableEntity)
		return
	}
	doc, err := docx(paras, *font)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	name := strings.TrimSuffix(filepath.Base(hdr.Filename), filepath.Ext(hdr.Filename)) + ".docx"
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.wordprocessingml.document")
	w.Header().Set("Content-Disposition", `attachment; filename="converted.docx"; filename*=UTF-8''`+url.PathEscape(name))
	w.Header().Set("X-Extract-Method", how)
	w.Write(doc)
}

// pdfDoc prefers the embedded text layer and falls back to OCR, which is the
// path a scanned book actually takes. Every page of the PDF ends up in the
// same paragraph list, the first paragraph of each page carrying a page break.
func pdfDoc(pdf, dir string) ([]para, string, error) {
	if bin, err := tool("pdftotext"); err == nil {
		out := filepath.Join(dir, "all.txt")
		if run(bin, "-enc", "UTF-8", pdf, out) == nil {
			if b, _ := os.ReadFile(out); len(bytes.TrimSpace(b)) > 20 {
				return textParas(string(b)), "text layer", nil
			}
		}
	}

	ppm, err := tool("pdftoppm")
	if err != nil {
		return nil, "", fmt.Errorf("pdftoppm not found — put poppler's bin on PATH or set POPPLER_BIN")
	}
	tess, err := tool("tesseract")
	if err != nil {
		return nil, "", fmt.Errorf("scanned page needs OCR but tesseract was not found — install it plus the %q traineddata, or set TESSERACT", *lang)
	}
	if err := run(ppm, "-r", *dpi, "-png", pdf, filepath.Join(dir, "page")); err != nil {
		return nil, "", err
	}
	// pdftoppm zero-pads every number to the width of the last page, so plain
	// lexical order is page order.
	pngs, err := filepath.Glob(filepath.Join(dir, "page-*.png"))
	if err != nil || len(pngs) == 0 {
		return nil, "", fmt.Errorf("pdftoppm rendered no pages")
	}
	sort.Strings(pngs)

	how := "OCR (" + *lang + " @" + *dpi + "dpi, " + strconv.Itoa(len(pngs)) + " pages)"
	var out []para
	// ponytail: pages OCR'd one at a time — tesseract already threads inside a
	// page, so a worker pool only pays off on a many-core box. Add one if a book
	// takes too long.
	for i, png := range pngs {
		base := strings.TrimSuffix(png, ".png")
		ocr := func(suffix, psm string) ([]byte, error) {
			if err := run(tess, png, base+suffix, "-l", *lang, "--psm", psm, "hocr"); err != nil {
				return nil, err
			}
			return os.ReadFile(base + suffix + ".hocr")
		}

		b, err := ocr("-auto", "1") // full page segmentation: paragraphs and reading order
		if err != nil {
			return nil, "", err
		}
		pars, photos := parseHOCR(b)

		if *headings {
			// Stylised headings sit inside coloured boxes that segmentation files as
			// ocr_photo and never reads. Sparse mode reads text anywhere, so take just
			// the lines that land inside those boxes and leave the rest — sparse mode
			// has no paragraph structure worth keeping.
			if b, err := ocr("-sparse", "11"); err == nil {
				sparse, _ := parseHOCR(b)
				for _, p := range sparse {
					for _, l := range p {
						if letters(l.text) >= 3 && insideAny(l.bbox, photos) {
							pars = append(pars, []pline{l})
						}
					}
				}
				if i == 0 {
					how += " + sparse pass"
				}
			}
		}
		page := layout(pars, *basePt)
		if i > 0 && len(page) > 0 {
			page[0].brk = true
		}
		out = append(out, page...)
		os.Remove(png) // a 300dpi book is gigabytes of PNG; drop each once it is read
	}
	return out, how, nil
}

// tool resolves an executable: $POPPLER_BIN/$TESSERACT override, then a
// poppler-*/ folder sitting next to the project, then PATH.
func tool(name string) (string, error) {
	if name == "tesseract" {
		if p := os.Getenv("TESSERACT"); p != "" {
			return p, nil
		}
	} else if p := os.Getenv("POPPLER_BIN"); p != "" {
		return exec.LookPath(filepath.Join(p, name))
	}
	for _, pat := range []string{"poppler-*/Library/bin", "../poppler-*/Library/bin", "poppler-*/bin", "../poppler-*/bin"} {
		dirs, _ := filepath.Glob(pat)
		for _, d := range dirs {
			if p, err := exec.LookPath(filepath.Join(d, name)); err == nil {
				return p, nil
			}
		}
	}
	return exec.LookPath(name)
}

func run(name string, args ...string) error {
	var errb bytes.Buffer
	cmd := exec.Command(name, args...)
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %v: %s", filepath.Base(name), err, strings.TrimSpace(errb.String()))
	}
	return nil
}

// --- hOCR ------------------------------------------------------------------

type box struct{ x0, y0, x1, y1 int }

func (b box) w() int { return b.x1 - b.x0 }

// pline is one OCR'd line: its text, where it sat on the page, and x_size —
// tesseract's estimate of the glyph height, which stands in for font size.
type pline struct {
	text  string
	bbox  box
	xsize float64
}

// hOCR calls a line by what it thinks the line is for; all of these hold text.
var lineClass = map[string]bool{
	"ocr_line": true, "ocr_caption": true, "ocr_textfloat": true,
	"ocr_header": true, "ocr_footer": true,
}

// parseHOCR walks tesseract's hOCR and returns paragraphs of lines plus the
// regions it decided were pictures. It is a token walk rather than an unmarshal
// because everything interesting lives in a class attribute, not in the nesting.
func parseHOCR(b []byte) (pars [][]pline, photos []box) {
	d := xml.NewDecoder(bytes.NewReader(b))
	d.Strict = false
	d.AutoClose = xml.HTMLAutoClose
	d.Entity = xml.HTMLEntity

	var cur []pline
	inWord := false
	flush := func() {
		if len(cur) > 0 {
			pars = append(pars, cur)
			cur = nil
		}
	}

	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			var class, title string
			for _, a := range t.Attr {
				switch a.Name.Local {
				case "class":
					class = a.Value
				case "title":
					title = a.Value
				}
			}
			v := titleVals(title)
			switch {
			case class == "ocr_photo":
				photos = append(photos, boxOf(v))
			case class == "ocr_par":
				flush()
			case lineClass[class]:
				cur = append(cur, pline{bbox: boxOf(v), xsize: first(v["x_size"])})
			case class == "ocrx_word":
				inWord = len(cur) > 0
			}
		case xml.CharData:
			if inWord {
				cur[len(cur)-1].text += strings.TrimSpace(string(t))
			}
		case xml.EndElement:
			if inWord && t.Name.Local == "span" {
				inWord = false
				cur[len(cur)-1].text += " "
			}
		}
	}
	flush()
	return pars, photos
}

// titleVals splits an hOCR title attribute ("bbox 1 2 3 4; x_size 12.5") into
// its numeric properties, dropping the ones that are not numbers ("image ...").
func titleVals(title string) map[string][]float64 {
	m := map[string][]float64{}
	for _, part := range strings.Split(title, ";") {
		f := strings.Fields(part)
		if len(f) < 2 {
			continue
		}
		v := make([]float64, 0, len(f)-1)
		for _, s := range f[1:] {
			n, err := strconv.ParseFloat(s, 64)
			if err != nil {
				v = nil
				break
			}
			v = append(v, n)
		}
		if v != nil {
			m[f[0]] = v
		}
	}
	return m
}

func boxOf(v map[string][]float64) box {
	b := v["bbox"]
	if len(b) < 4 {
		return box{}
	}
	return box{int(b[0]), int(b[1]), int(b[2]), int(b[3])}
}

func first(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	return v[0]
}

func insideAny(b box, boxes []box) bool {
	cx, cy := (b.x0+b.x1)/2, (b.y0+b.y1)/2
	for _, p := range boxes {
		if cx >= p.x0 && cx <= p.x1 && cy >= p.y0 && cy <= p.y1 {
			return true
		}
	}
	return false
}

func letters(s string) (n int) {
	for _, r := range s {
		if unicode.IsLetter(r) {
			n++
		}
	}
	return n
}

// --- layout ----------------------------------------------------------------

// para is one Word paragraph and the formatting it asks for.
type para struct {
	text   string
	sz     int // half-points, what w:sz wants
	bold   bool
	center bool
	indent bool
	brk    bool // starts a new PDF page, so Word gets a page break here
	top    int
}

// layout turns positioned OCR lines into Word paragraphs. Sizes are relative:
// the median line on the page is body text at basePt, everything else scales
// off it, so the result is independent of the DPI the page was rendered at.
func layout(pars [][]pline, basePt int) []para {
	var sizes []float64
	col := box{x0: math.MaxInt32}
	for _, p := range pars {
		for _, l := range p {
			if l.xsize > 0 {
				sizes = append(sizes, l.xsize)
			}
			col.x0 = min(col.x0, l.bbox.x0)
			col.x1 = max(col.x1, l.bbox.x1)
		}
	}
	body := median(sizes)
	if body == 0 || col.w() <= 0 {
		return nil
	}

	var out []para
	for _, p := range pars {
		b, ls := p[0].bbox, make([]float64, 0, len(p))
		var text strings.Builder
		for _, l := range p {
			b.x0, b.y0 = min(b.x0, l.bbox.x0), min(b.y0, l.bbox.y0)
			b.x1, b.y1 = max(b.x1, l.bbox.x1), max(b.y1, l.bbox.y1)
			ls = append(ls, l.xsize)
			text.WriteString(l.text)
		}
		s := clean(text.String())
		if s == "" {
			continue
		}
		ratio := median(ls) / body
		left, right := b.x0-col.x0, col.x1-b.x1
		out = append(out, para{
			text: s,
			sz:   2 * clamp(int(math.Round(ratio*float64(basePt))), 6, 48),
			bold: ratio >= 1.25,
			// Centred means pulled in from both margins by about the same amount.
			center: left > col.w()/20 && abs(left-right) < col.w()/16,
			indent: p[0].bbox.x0-b.x0 > col.w()/40,
			top:    b.y0,
		})
	}
	// Reading order by vertical position. ponytail: single-column assumption —
	// a true two-column page would interleave; sort by column then y if that lands.
	sort.SliceStable(out, func(i, j int) bool { return out[i].top < out[j].top })
	return out
}

// textParas handles PDFs that already carry text: a blank line ends a
// paragraph, so lines rejoin into something Word can reflow, and pdftotext's
// form feed ends a page.
func textParas(s string) []para {
	var out []para
	for i, pg := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\f") {
		start := len(out)
		for _, blk := range strings.Split(pg, "\n\n") {
			var text strings.Builder
			for _, line := range strings.Split(blk, "\n") {
				if l := clean(line); l != "" {
					text.WriteString(l + " ")
				}
			}
			if t := strings.TrimSpace(text.String()); t != "" {
				out = append(out, para{text: t, sz: 2 * *basePt})
			}
		}
		if i > 0 && len(out) > start {
			out[start].brk = true
		}
	}
	return out
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}

func clamp(v, lo, hi int) int { return min(max(v, lo), hi) }

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// --- docx ------------------------------------------------------------------

// docx builds a minimal WordprocessingML package. Every run carries the
// complex-script twins (szCs, bCs, rFonts w:cs) because Odia is a complex
// script and Word ignores the plain ascii properties for it.
func docx(paras []para, fontName string) ([]byte, error) {
	var fb bytes.Buffer
	xml.EscapeText(&fb, []byte(fontName))
	f := fb.String()

	rPr := func(p para) string {
		sz := strconv.Itoa(p.sz)
		s := `<w:rPr><w:rFonts w:ascii="` + f + `" w:hAnsi="` + f + `" w:cs="` + f + `"/>`
		if p.bold {
			s += `<w:b/><w:bCs/>`
		}
		return s + `<w:sz w:val="` + sz + `"/><w:szCs w:val="` + sz + `"/><w:lang w:bidi="or-IN"/></w:rPr>`
	}

	var body bytes.Buffer
	for _, p := range paras {
		body.WriteString(`<w:p><w:pPr>`)
		if p.indent {
			body.WriteString(`<w:ind w:firstLine="420"/>`)
		}
		if p.center {
			body.WriteString(`<w:jc w:val="center"/>`)
		}
		body.WriteString(rPr(p) + `</w:pPr>`)
		if p.brk {
			body.WriteString(`<w:r><w:br w:type="page"/></w:r>`)
		}
		body.WriteString(`<w:r>` + rPr(p) + `<w:t xml:space="preserve">`)
		xml.EscapeText(&body, []byte(p.text))
		body.WriteString(`</w:t></w:r></w:p>`)
	}

	parts := [][2]string{
		{"[Content_Types].xml", xmlHead + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
			`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
			`<Default Extension="xml" ContentType="application/xml"/>` +
			`<Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`},
		{"_rels/.rels", xmlHead + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`},
		{"word/document.xml", xmlHead + `<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` +
			body.String() + `<w:sectPr/></w:body></w:document>`},
	}

	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for _, p := range parts {
		w, err := z.Create(p[0])
		if err != nil {
			return nil, err
		}
		if _, err := io.WriteString(w, p[1]); err != nil {
			return nil, err
		}
	}
	if err := z.Close(); err != nil { // must finish before Bytes(): Close writes the central directory
		return nil, err
	}
	return buf.Bytes(), nil
}

const xmlHead = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`

// clean drops the control characters XML 1.0 forbids — pdftotext's page-break
// form feed among them, which Word rejects outright.
func clean(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case r < 0x20, r == 0x7f, r == 0xfffe, r == 0xffff:
			return -1
		}
		return r
	}, s))
}
