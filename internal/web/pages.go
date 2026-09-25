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
