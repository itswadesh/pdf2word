package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
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
	Name        string // short name, for breadcrumbs
	Title       string
	Description string
	Tool        bool   // carries the converter
	Lang        string // OCR language the converter starts on here; "" means the server's
	Home        bool   // carries the site's structured data
	Article     bool   // a guide: described as an Article in structured data
	NoIndex     bool   // kept out of search results and the sitemap
}

// siteUpdated is when the pages last changed (the sitemap's lastmod and the
// articles' dateModified); articlesPublished is when the articles went up.
const (
	siteUpdated       = "2026-09-25"
	articlesPublished = "2026-09-25"
)

// notFoundPage is served, with status 404, for any address that is not a
// page. It is kept out of search results.
var notFoundPage = pageDef{
	Path: "/404", File: "404.html", Name: "Page not found", NoIndex: true,
	Title:       "Page Not Found - PDF to Word Converter",
	Description: "This address is not a page of the converter. Convert a PDF to Word from the home page, or read the guides on scanned PDFs, tables and how it works.",
}

// pageData is what the templates see: the page, its text escaped for HTML,
// and JSON-encoded copies for the structured data.
type pageData struct {
	pageDef
	TitleJSON, DescriptionJSON, NameJSON string
	Updated, Published                   string
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// sitePages lists the pages in the order the sitemap gives them.
var sitePages = []pageDef{
	{
		Path: "/", File: "home.html", Name: "PDF to Word", Tool: true, Home: true,
		Title:       "Free PDF to Word Converter - Convert PDF to DOCX Online",
		Description: "Drop in a PDF and get an editable Word DOCX back. Fonts, tables, images and page breaks stay put, scanned pages are read with OCR, and there is no sign-up.",
	},
	{
		Path: "/pdf-to-docx", File: "pdf-to-docx.html", Name: "PDF to DOCX", Tool: true,
		Title:       "PDF to DOCX Converter - Free, Keeps the Layout",
		Description: "Convert PDF to DOCX for free. The Word file keeps the fonts, tables, pictures, line breaks and page breaks of the PDF, so it has the same pages.",
	},
	{
		Path: "/scanned-pdf-to-word", File: "scanned-pdf-to-word.html", Name: "Scanned PDF to Word", Tool: true,
		Title:       "Scanned PDF to Word Converter with OCR - Free",
		Description: "Turn a scanned PDF or a photo of a page into an editable Word document. Pages are read with OCR in English or Odia and keep their layout. Free.",
	},
	{
		Path: "/odia-pdf-to-word", File: "odia-pdf-to-word.html", Name: "Odia PDF to Word", Tool: true, Lang: "ori",
		Title:       "Odia PDF to Word Converter - Free Odia OCR",
		Description: "Convert Odia (ଓଡ଼ିଆ) PDFs and scans to editable Word files. Odia text recognition is built in, pages mixing English and Odia work, and it is free.",
	},
	{
		Path: "/pdf-table-to-word", File: "pdf-table-to-word.html", Name: "PDF tables to Word", Tool: true,
		Title:       "PDF Table to Word Converter - Editable Tables, Free",
		Description: "Convert tables in a PDF into real Word tables you can type in. Ruled tables keep their rows, columns and cells, and the rest of the page keeps its layout.",
	},
	{
		Path: "/resume-pdf-to-word", File: "resume-pdf-to-word.html", Name: "Resume or CV to Word", Tool: true,
		Title:       "Resume PDF to Word Converter - Edit Your CV Free",
		Description: "Turn a resume or CV PDF into a Word document you can update. Fonts, headings and right-aligned dates stay in place, and nothing is added to the file.",
	},
	{
		Path: "/pdf-to-google-docs", File: "pdf-to-google-docs.html", Name: "PDF to Google Docs", Tool: true,
		Title:       "PDF to Google Docs - Convert and Edit for Free",
		Description: "Get a PDF into Google Docs with its layout intact: convert it to DOCX here, upload it to Google Drive and open it with Google Docs. Free, no sign-up.",
	},
	{
		Path: "/convert-multiple-pdf-to-word", File: "convert-multiple-pdf-to-word.html", Name: "Several PDFs at once", Tool: true,
		Title:       "Convert Multiple PDFs to Word at Once - Free",
		Description: "Choose or drop several PDFs and convert them to Word in one go. Each file gets its own progress bar and its own DOCX, and there is no daily limit.",
	},
	{
		Path: "/large-pdf-to-word", File: "large-pdf-to-word.html", Name: "Large PDFs", Tool: true,
		Title:       "Convert Large PDF Files to Word - Up to 100 MB",
		Description: "Convert long and large PDFs to Word: up to 100 MB a file, hundreds of pages, scanned or not. Every page comes across and the DOCX keeps the page count.",
	},
	{
		Path: "/pdf-to-word-on-phone", File: "pdf-to-word-on-phone.html", Name: "On your phone", Tool: true,
		Title:       "Convert PDF to Word on iPhone or Android - Free",
		Description: "Convert a PDF to an editable Word file on your phone, in the browser. Choose it from Files, watch each page being read, and open the DOCX in Word.",
	},
	{
		Path: "/how-it-works", File: "how-it-works.html", Name: "How it works", Article: true,
		Title:       "How the PDF to Word Converter Works",
		Description: "What happens to a PDF on its way to Word: how text, tables and pictures are read, when OCR is used, and how the DOCX keeps the pages of the original.",
	},
	{
		Path: "/pdf-to-word-challenges", File: "pdf-to-word-challenges.html", Name: "Why PDF to Word is hard", Article: true,
		Title:       "12 Challenges in Converting PDF to Word",
		Description: "Why converting PDF to Word is hard: no paragraphs, missing fonts, tables that are only lines, scans, reading order and more, and how each is handled.",
	},
	{
		Path: "/ai-in-pdf-to-word", File: "ai-in-pdf-to-word.html", Name: "AI and PDF to Word", Article: true,
		Title:       "The Role of AI in PDF to Word Conversion",
		Description: "Where AI helps turn a PDF into Word: reading scans, finding layout and tables, handwriting, and the risk of made-up text. And where this converter uses it.",
	},
	{
		Path: "/handwriting-pdf-to-word", File: "handwriting-pdf-to-word.html", Name: "Handwriting", Article: true,
		Title:       "Handwriting in a PDF to Word: How AI Reads It",
		Description: "How AI reads handwriting in a scanned PDF and turns it into Word text, what makes it harder than print, which tools do it, and what this converter can do.",
	},
	{
		Path: "/faq", File: "faq.html", Name: "Questions",
		Title:       "PDF to Word Converter FAQ - Questions and Answers",
		Description: "Answers about converting PDF to Word: is it free, will the DOCX look like the PDF, scanned pages and OCR, languages, file size limits and your files.",
	},
	{
		Path: "/privacy", File: "privacy.html", Name: "Privacy",
		Title:       "Privacy - What Happens to Your PDF Files",
		Description: "How long your PDFs and Word files are kept, who can see them, and what this converter never does: no account, no tracking and nothing added to files.",
	},
}

// builtPage is a page rendered once, at start-up, with its validator.
type builtPage struct {
	body []byte
	etag string
}

// buildPages renders every page, and the not-found page, with this
// deployment's public address and privacy copy filled in.
func buildPages(publicURL, privacy, filesAnswer string) (map[string]*builtPage, *builtPage, error) {
	out := make(map[string]*builtPage, len(sitePages))
	for _, p := range sitePages {
		b, err := buildPage(p, publicURL, privacy, filesAnswer)
		if err != nil {
			return nil, nil, err
		}
		out[p.Path] = b
	}
	notFound, err := buildPage(notFoundPage, publicURL, privacy, filesAnswer)
	if err != nil {
		return nil, nil, err
	}
	return out, notFound, nil
}

func buildPage(p pageDef, publicURL, privacy, filesAnswer string) (*builtPage, error) {
	t, err := template.ParseFS(static, "static/layout.html", "static/tool.html", "static/pages/"+p.File)
	if err != nil {
		return nil, fmt.Errorf("page %s: %w", p.Path, err)
	}
	data := pageData{
		pageDef:         p,
		TitleJSON:       jsonString(p.Title),
		DescriptionJSON: jsonString(p.Description),
		NameJSON:        jsonString(p.Name),
		Updated:         siteUpdated,
		Published:       articlesPublished,
	}
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
	return &builtPage{body: []byte(body), etag: fmt.Sprintf(`"%x"`, sha256.Sum256([]byte(body)))}, nil
}

// assets are the files the pages point at: the icons and the sharing image.
// They change only with a release, so browsers and CDNs may keep them.
var assetFiles = map[string]struct{ file, typ string }{
	"/og.png":               {"og.png", "image/png"},
	"/favicon.svg":          {"favicon.svg", "image/svg+xml"},
	"/favicon-48.png":       {"favicon-48.png", "image/png"},
	"/favicon.ico":          {"favicon-48.png", "image/png"}, // for clients that ask for it by name
	"/apple-touch-icon.png": {"apple-touch-icon.png", "image/png"},
}

const assetCacheControl = "public, max-age=604800"

type asset struct {
	body []byte
	typ  string
}

func loadAssets() (map[string]asset, error) {
	out := make(map[string]asset, len(assetFiles))
	for path, f := range assetFiles {
		b, err := static.ReadFile("static/assets/" + f.file)
		if err != nil {
			return nil, err
		}
		out[path] = asset{body: b, typ: f.typ}
	}
	return out, nil
}
