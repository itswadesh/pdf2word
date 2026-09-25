package web

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"html"
	"strings"
	"text/template"
)

// pageDef is one page of the site. Every page shares the layout (head,
// header, footer); tool pages also carry the converter, its dialog and its
// script. Titles and descriptions are sized for search results.
type pageDef struct {
	Path        string
	File        string // under static/pages
	Title       string
	Description string
	Tool        bool   // carries the converter
	Lang        string // OCR language the converter starts on here; "" means the server's
	Home        bool   // carries the site's structured data
}

// sitePages lists the pages in the order the sitemap gives them.
var sitePages = []pageDef{
	{
		Path: "/", File: "home.html", Tool: true, Home: true,
		Title:       "Free PDF to Word Converter - Convert PDF to DOCX Online",
		Description: "Drop in a PDF and get an editable Word DOCX back. Fonts, tables, images and page breaks stay put, scanned pages are read with OCR, and there is no sign-up.",
	},
	{
		Path: "/pdf-to-docx", File: "pdf-to-docx.html", Tool: true,
		Title:       "PDF to DOCX Converter - Free, Keeps the Layout",
		Description: "Convert PDF to DOCX for free. The Word file keeps the fonts, tables, pictures, line breaks and page breaks of the PDF, so it has the same pages.",
	},
	{
		Path: "/scanned-pdf-to-word", File: "scanned-pdf-to-word.html", Tool: true,
		Title:       "Scanned PDF to Word Converter with OCR - Free",
		Description: "Turn a scanned PDF or a photo of a page into an editable Word document. Pages are read with OCR in English or Odia and keep their layout. Free.",
	},
	{
		Path: "/odia-pdf-to-word", File: "odia-pdf-to-word.html", Tool: true, Lang: "ori",
		Title:       "Odia PDF to Word Converter - Free Odia OCR",
		Description: "Convert Odia (ଓଡ଼ିଆ) PDFs and scans to editable Word files. Odia text recognition is built in, pages mixing English and Odia work, and it is free.",
	},
	{
		Path: "/pdf-table-to-word", File: "pdf-table-to-word.html", Tool: true,
		Title:       "PDF Table to Word Converter - Editable Tables, Free",
		Description: "Convert tables in a PDF into real Word tables you can type in. Ruled tables keep their rows, columns and cells, and the rest of the page keeps its layout.",
	},
	{
		Path: "/resume-pdf-to-word", File: "resume-pdf-to-word.html", Tool: true,
		Title:       "Resume PDF to Word Converter - Edit Your CV Free",
		Description: "Turn a resume or CV PDF into a Word document you can update. Fonts, headings and right-aligned dates stay in place, and nothing is added to the file.",
	},
	{
		Path: "/pdf-to-google-docs", File: "pdf-to-google-docs.html", Tool: true,
		Title:       "PDF to Google Docs - Convert and Edit for Free",
		Description: "Get a PDF into Google Docs with its layout intact: convert it to DOCX here, upload it to Google Drive and open it with Google Docs. Free, no sign-up.",
	},
	{
		Path: "/convert-multiple-pdf-to-word", File: "convert-multiple-pdf-to-word.html", Tool: true,
		Title:       "Convert Multiple PDFs to Word at Once - Free",
		Description: "Choose or drop several PDFs and convert them to Word in one go. Each file gets its own progress bar and its own DOCX, and there is no daily limit.",
	},
	{
		Path: "/large-pdf-to-word", File: "large-pdf-to-word.html", Tool: true,
		Title:       "Convert Large PDF Files to Word - Up to 100 MB",
		Description: "Convert long and large PDFs to Word: up to 100 MB a file, hundreds of pages, scanned or not. Every page comes across and the DOCX keeps the page count.",
	},
	{
		Path: "/pdf-to-word-on-phone", File: "pdf-to-word-on-phone.html", Tool: true,
		Title:       "Convert PDF to Word on iPhone or Android - Free",
		Description: "Convert a PDF to an editable Word file on your phone, in the browser. Choose it from Files, watch each page being read, and open the DOCX in Word.",
	},
	{
		Path: "/how-it-works", File: "how-it-works.html",
		Title:       "How the PDF to Word Converter Works",
		Description: "What happens to a PDF on its way to Word: how text, tables and pictures are read, when OCR is used, and how the DOCX keeps the pages of the original.",
	},
	{
		Path: "/faq", File: "faq.html",
		Title:       "PDF to Word Converter FAQ - Questions and Answers",
		Description: "Answers about converting PDF to Word: is it free, will the DOCX look like the PDF, scanned pages and OCR, languages, file size limits and your files.",
	},
	{
		Path: "/privacy", File: "privacy.html",
		Title:       "Privacy - What Happens to Your PDF Files",
		Description: "How long your PDFs and Word files are kept, who can see them, and what this converter never does: no account, no tracking and nothing added to files.",
	},
}

// builtPage is a page rendered once, at start-up, with its validator.
type builtPage struct {
	body []byte
	etag string
}

// buildPages renders every page with this deployment's public address and
// privacy copy filled in.
func buildPages(publicURL, privacy, filesAnswer string) (map[string]*builtPage, error) {
	out := make(map[string]*builtPage, len(sitePages))
	for _, p := range sitePages {
		t, err := template.ParseFS(static, "static/layout.html", "static/tool.html", "static/pages/"+p.File)
		if err != nil {
			return nil, fmt.Errorf("page %s: %w", p.Path, err)
		}
		data := p
		data.Title = html.EscapeString(p.Title)
		data.Description = html.EscapeString(p.Description)
		var buf bytes.Buffer
		if err := t.ExecuteTemplate(&buf, "layout", data); err != nil {
			return nil, fmt.Errorf("page %s: %w", p.Path, err)
		}
		body := buf.String()
		for from, to := range map[string]string{
			"%PUBLIC_URL%":   publicURL,
			"%PRIVACY%":      privacy,
			"%FILES_ANSWER%": filesAnswer,
		} {
			body = strings.ReplaceAll(body, from, to)
		}
		out[p.Path] = &builtPage{body: []byte(body), etag: fmt.Sprintf(`"%x"`, sha256.Sum256([]byte(body)))}
	}
	return out, nil
}
