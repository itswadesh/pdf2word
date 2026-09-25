package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pdf2word/internal/convert"
	"pdf2word/internal/pdfimage"
)

func fixture(name string) string {
	return filepath.Join("..", "..", "testdata", name)
}

// fakeEngine lets the scanned fixture convert without Tesseract.
type fakeEngine struct{ text string }

func (f *fakeEngine) Name() string { return "fake-ocr" }
func (f *fakeEngine) Recognize(context.Context, []byte, string) (string, error) {
	return f.text, nil
}

type fakeImages struct{}

func (fakeImages) PageImages(int) ([]pdfimage.Image, error) {
	return []pdfimage.Image{{Data: []byte("x"), Ext: "png", Width: 100, Height: 100}}, nil
}
func (fakeImages) Close() error { return nil }

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	s, err := New(Config{
		Base: convert.Options{
			Engine:     &fakeEngine{text: "Recognised by fake OCR"},
			OpenImages: func(string) (convert.ImageSource, error) { return fakeImages{}, nil },
		},
		WorkDir: t.TempDir(),
		Version: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Close() })
	return s, ts
}

func upload(t *testing.T, ts *httptest.Server, filename string, content []byte, fields map[string]string) *http.Response {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	fw.Write(content)
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/convert", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func decode(t *testing.T, resp *http.Response, v any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

// waitForJob polls until the job reaches a terminal state. Pass the owning
// browser's cookie when the job was uploaded with one: jobs are only
// visible to the browser that created them.
func waitForJob(t *testing.T, ts *httptest.Server, id string, as ...*http.Cookie) JobView {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/jobs/"+id, nil)
		for _, c := range as {
			if c != nil {
				req.AddCookie(c)
			}
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var v JobView
		decode(t, resp, &v)
		if v.State.terminal() {
			return v
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("job did not finish in time")
	return JobView{}
}

func TestIndexServed(t *testing.T) {
	s, ts := newTestServer(t)
	resp, err := ts.Client().Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(string(body), "<html") || !strings.Contains(string(body), "/api/convert") {
		t.Fatalf("status %d, body %q", resp.StatusCode, string(body[:min(200, len(body))]))
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type = %q", ct)
	}
	if !s.PageOpened() {
		t.Error("PageOpened should be true after GET /")
	}
}

// The page is the product's shop window: it has to carry the title,
// description, one keyword-bearing h1, headed sections and the
// WebApplication data that search engines read.
func TestIndexSEOHead(t *testing.T) {
	_, ts := newTestServer(t)
	resp, err := ts.Client().Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	page := string(body)

	title := between(page, "<title>", "</title>")
	if !strings.Contains(strings.ToLower(title), "free pdf to word converter") {
		t.Errorf("title = %q, want the primary keyword in it", title)
	}
	if n := len(title); n < 30 || n > 60 {
		t.Errorf("title is %d characters; search results show about 30-60", n)
	}
	desc := attrOf(page, `<meta name="description" content="`)
	if n := len(desc); n < 120 || n > 160 {
		t.Errorf("meta description is %d characters (%q); want 120-160", n, desc)
	}
	if strings.EqualFold(desc, title) {
		t.Error("the description must not restate the title")
	}
	if n := strings.Count(page, "<h1>"); n != 1 {
		t.Errorf("%d h1 elements, want exactly 1", n)
	}
	h1 := between(page, "<h1>", "</h1>")
	if !strings.Contains(strings.ToLower(h1), "pdf to word converter") {
		t.Errorf("h1 = %q, want the keyword in it", h1)
	}
	if n := strings.Count(page, "<h2>"); n < 4 {
		t.Errorf("%d h2 sections; the page needs supporting content", n)
	}
	for _, want := range []string{
		`<link rel="canonical"`, `property="og:title"`, `property="og:description"`,
		`name="twitter:card"`, `name="robots"`, `"@type": "WebApplication"`,
		`"price": "0"`, `"isAccessibleForFree": true`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("head is missing %s", want)
		}
	}
	// Ratings must come from real reviews, so the page claims none.
	if strings.Contains(page, "aggregateRating") {
		t.Error("aggregateRating must not be declared without real ratings behind it")
	}
	// The structured data has to parse, or search engines drop it silently.
	var ld map[string]any
	if err := json.Unmarshal([]byte(between(page, `<script type="application/ld+json">`, "</script>")), &ld); err != nil {
		t.Fatalf("JSON-LD does not parse: %v", err)
	}
	if ld["@context"] != "https://schema.org" || ld["name"] == "" {
		t.Errorf("JSON-LD = %+v", ld)
	}
	if offer, ok := ld["offers"].(map[string]any); !ok || offer["price"] != "0" {
		t.Errorf("offers = %+v, want a free price", ld["offers"])
	}
	if feats, ok := ld["featureList"].([]any); !ok || len(feats) < 3 {
		t.Errorf("featureList = %+v", ld["featureList"])
	}
	if strings.Contains(page, "%PUBLIC_URL%") {
		t.Error("the public-URL placeholder was left in the page")
	}
	for _, kw := range []string{"PDF to DOCX", "scanned", "OCR", "free"} {
		if !strings.Contains(strings.ToLower(page), strings.ToLower(kw)) {
			t.Errorf("page never mentions %q", kw)
		}
	}
}

func TestPublicURLAndCrawlerFiles(t *testing.T) {
	// Without a public address: relative canonical, no sitemap.
	_, ts := newTestServer(t)
	page := get(t, ts, "/")
	if !strings.Contains(page, `<link rel="canonical" href="/">`) {
		t.Errorf("canonical without a public URL should be relative:\n%s", between(page, "<link rel=\"canonical\"", ">"))
	}
	robots := get(t, ts, "/robots.txt")
	if !strings.Contains(robots, "Disallow: /api/") || strings.Contains(robots, "Sitemap:") {
		t.Errorf("robots.txt = %q", robots)
	}
	resp, err := ts.Client().Get(ts.URL + "/sitemap.xml")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("sitemap without a public URL: status %d, want 404", resp.StatusCode)
	}

	// With one (trailing slash trimmed): absolute canonical, og:url and sitemap.
	s, err := New(Config{Base: convert.Options{Engine: &fakeEngine{}}, WorkDir: t.TempDir(), PublicURL: "https://pdf2word.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	pub := httptest.NewServer(s.Handler())
	t.Cleanup(func() { pub.Close(); s.Close() })

	page = get(t, pub, "/")
	for _, want := range []string{
		`<link rel="canonical" href="https://pdf2word.example.com/">`,
		`<meta property="og:url" content="https://pdf2word.example.com/">`,
		`"url": "https://pdf2word.example.com/"`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("page missing %s", want)
		}
	}
	if !strings.Contains(get(t, pub, "/robots.txt"), "Sitemap: https://pdf2word.example.com/sitemap.xml") {
		t.Error("robots.txt should point at the sitemap")
	}
	sitemap := get(t, pub, "/sitemap.xml")
	if !strings.Contains(sitemap, "<loc>https://pdf2word.example.com/</loc>") || !strings.Contains(sitemap, "<urlset") {
		t.Errorf("sitemap.xml = %q", sitemap)
	}
}

func get(t *testing.T, ts *httptest.Server, path string) string {
	t.Helper()
	resp, err := ts.Client().Get(ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func between(s, open, close string) string {
	i := strings.Index(s, open)
	if i < 0 {
		return ""
	}
	rest := s[i+len(open):]
	j := strings.Index(rest, close)
	if j < 0 {
		return ""
	}
	return rest[:j]
}

func attrOf(page, prefix string) string { return between(page, prefix, `"`) }

// The page promises where the file goes. A server reachable from the
// network must not repeat the self-hosted claim that the PDF never leaves
// your machine, because for every visitor but the operator it is false.
func TestPrivacyCopyMatchesTheDeployment(t *testing.T) {
	local, ts := newTestServer(t)
	page := get(t, ts, "/")
	if strings.Contains(page, "%PRIVACY%") || strings.Contains(page, "%FILES_ANSWER%") {
		t.Fatal("copy placeholders were left in the page")
	}
	if !strings.Contains(page, "runs on your own computer") {
		t.Error("a loopback-only server should say the files stay put")
	}
	if !local.cfg.AllowRemote && strings.Contains(page, "uploaded to this server") {
		t.Error("a local server must not use the hosted wording")
	}

	s, err := New(Config{Base: convert.Options{Engine: &fakeEngine{}}, WorkDir: t.TempDir(), AllowRemote: true})
	if err != nil {
		t.Fatal(err)
	}
	pub := httptest.NewServer(s.Handler())
	t.Cleanup(func() { pub.Close(); s.Close() })
	hosted := get(t, pub, "/")
	for _, want := range []string{
		"uploaded to this server",
		"deleted an hour after the conversion finishes whether or not anyone else visits",
		"Only the browser that uploaded a file can see or download the result",
		"run this same program yourself",
	} {
		if !strings.Contains(hosted, want) {
			t.Errorf("hosted page missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"runs on the computer or server you point it at",
		"your PDFs are not handed to anyone else",
		"runs on your own computer",
	} {
		if strings.Contains(hosted, forbidden) {
			t.Errorf("hosted page still claims %q, which is false for its visitors", forbidden)
		}
	}
}

// "deleted an hour after conversion" has to be the clock's job, not the
// next visitor's.
func TestFinishedJobsAreDeletedOnATimer(t *testing.T) {
	old := pruneInterval
	pruneInterval = 10 * time.Millisecond
	t.Cleanup(func() { pruneInterval = old })

	s, err := New(Config{Base: convert.Options{Engine: &fakeEngine{}}, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	j, err := s.jobs.create("old.pdf", convert.OCRAuto, "eng", "someone")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(j.input, []byte("%PDF-1.4"), 0o600); err != nil {
		t.Fatal(err)
	}
	j.finish(convert.Report{Pages: 1}, nil)
	j.mu.Lock()
	j.finished = time.Now().Add(-2 * retention) // finished long ago
	j.mu.Unlock()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, still := s.jobs.get(j.id); !still {
			if _, err := os.Stat(j.dir); !os.IsNotExist(err) {
				t.Fatalf("job forgotten but its directory survives: %v", err)
			}
			return // swept without any upload happening
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("an expired job was never swept; retention still waits for the next upload")
}

// "Only the browser that uploaded a file can see or download it" has to be
// enforced, not just printed.
func TestOneBrowserCannotReachAnothersJob(t *testing.T) {
	_, ts := newTestServer(t)
	data, _ := os.ReadFile(fixture("text.pdf"))

	mine := &http.Cookie{Name: clientCookie, Value: "browser-one"}
	theirs := &http.Cookie{Name: clientCookie, Value: "browser-two"}

	do := func(method, path string, c *http.Cookie) *http.Response {
		req, _ := http.NewRequest(method, ts.URL+path, nil)
		if c != nil {
			req.AddCookie(c)
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "private.pdf")
	fw.Write(data)
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/convert", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(mine)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var created JobView
	decode(t, resp, &created)
	if created.ID == "" {
		t.Fatal("upload did not create a job")
	}
	waitForJob(t, ts, created.ID, mine) // finishes; the owner can still poll it

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/jobs/" + created.ID},
		{http.MethodGet, "/api/jobs/" + created.ID + "/download"},
		{http.MethodPost, "/api/jobs/" + created.ID + "/cancel"},
	} {
		r := do(tc.method, tc.path, theirs)
		r.Body.Close()
		if r.StatusCode != http.StatusNotFound {
			t.Errorf("another browser got %d from %s %s; want 404", r.StatusCode, tc.method, tc.path)
		}
	}
	r := do(http.MethodGet, "/api/jobs/"+created.ID, mine)
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Errorf("the owner was locked out of its own job: %d", r.StatusCode)
	}
}

func TestInfo(t *testing.T) {
	_, ts := newTestServer(t)
	resp, err := ts.Client().Get(ts.URL + "/api/info")
	if err != nil {
		t.Fatal(err)
	}
	var info InfoView
	decode(t, resp, &info)
	if info.Version != "test" || !info.OCR.Available {
		t.Fatalf("info = %+v", info)
	}
	// The page starts its language picker on the server's default.
	if info.OCR.DefaultLang != "eng" {
		t.Errorf("defaultLang = %q, want eng when none is configured", info.OCR.DefaultLang)
	}

	s, err := New(Config{Base: convert.Options{Engine: &fakeEngine{}, Lang: "ori"}, WorkDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	odia := httptest.NewServer(s.Handler())
	t.Cleanup(func() { odia.Close(); s.Close() })
	resp, err = odia.Client().Get(odia.URL + "/api/info")
	if err != nil {
		t.Fatal(err)
	}
	decode(t, resp, &info)
	if info.OCR.DefaultLang != "ori" {
		t.Errorf("defaultLang = %q, want the configured ori", info.OCR.DefaultLang)
	}
}

// The language reaches Tesseract's command line, so only well-formed
// codes are accepted.
func TestLanguageField(t *testing.T) {
	_, ts := newTestServer(t)
	data, _ := os.ReadFile(fixture("text.pdf"))
	for lang, want := range map[string]int{
		"eng":           http.StatusAccepted,
		"eng+ori":       http.StatusAccepted,
		"chi_sim":       http.StatusAccepted,
		"eng; rm -rf /": http.StatusBadRequest,
		"--psm 0":       http.StatusBadRequest,
		"eng+":          http.StatusBadRequest,
		"../../etc/eng": http.StatusBadRequest,
	} {
		resp := upload(t, ts, "a.pdf", data, map[string]string{"lang": lang})
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Errorf("lang %q: status %d, want %d", lang, resp.StatusCode, want)
		}
	}
}

func TestConvertTextPDFEndToEnd(t *testing.T) {
	_, ts := newTestServer(t)
	data, err := os.ReadFile(fixture("text.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	resp := upload(t, ts, "My Report.pdf", data, map[string]string{"ocr": "auto", "lang": "eng"})
	if resp.StatusCode != http.StatusAccepted {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, b)
	}
	var created JobView
	decode(t, resp, &created)
	if created.ID == "" || created.Filename != "My Report.pdf" || created.Size != int64(len(data)) {
		t.Fatalf("created = %+v", created)
	}

	final := waitForJob(t, ts, created.ID)
	if final.State != StateDone || final.Report == nil || final.Report.Pages != 2 || final.Report.TextPages != 2 {
		t.Fatalf("final = %+v", final)
	}
	if final.Done != 2 || final.Total != 2 || final.Download == "" {
		t.Errorf("progress not complete: %+v", final)
	}

	dl, err := ts.Client().Get(ts.URL + final.Download)
	if err != nil {
		t.Fatal(err)
	}
	defer dl.Body.Close()
	if dl.StatusCode != 200 {
		t.Fatalf("download status %d", dl.StatusCode)
	}
	if cd := dl.Header.Get("Content-Disposition"); !strings.Contains(cd, `filename="My Report.docx"`) {
		t.Errorf("content-disposition = %q", cd)
	}
	docx, _ := io.ReadAll(dl.Body)
	zr, err := zip.NewReader(bytes.NewReader(docx), int64(len(docx)))
	if err != nil {
		t.Fatalf("download is not a docx/zip: %v", err)
	}
	found := false
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			found = true
		}
	}
	if !found {
		t.Error("word/document.xml missing from download")
	}
}

func TestConvertScannedUsesOCRAndReportsStage(t *testing.T) {
	_, ts := newTestServer(t)
	data, _ := os.ReadFile(fixture("scanned.pdf"))
	resp := upload(t, ts, "scan.pdf", data, nil)
	var created JobView
	decode(t, resp, &created)
	final := waitForJob(t, ts, created.ID)
	if final.State != StateDone || final.Report.OCRPages != 1 || final.Stage != "ocr" {
		t.Fatalf("final = %+v", final)
	}
}

func TestRejectsNonPDF(t *testing.T) {
	_, ts := newTestServer(t)
	resp := upload(t, ts, "notes.pdf", []byte("hello there"), nil)
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status %d, want 415", resp.StatusCode)
	}
	var e map[string]string
	decode(t, resp, &e)
	if !strings.Contains(e["error"], "not a PDF") {
		t.Errorf("error = %q, want the signature check to name the file as not a PDF", e["error"])
	}
	// A rejected upload must not linger as a job on the page.
	list, err := ts.Client().Get(ts.URL + "/api/jobs")
	if err != nil {
		t.Fatal(err)
	}
	var jobs []JobView
	decode(t, list, &jobs)
	if len(jobs) != 0 {
		t.Errorf("rejected upload left %d job(s): %+v", len(jobs), jobs)
	}
}

func TestRejectsMissingFile(t *testing.T) {
	_, ts := newTestServer(t)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	mw.WriteField("ocr", "auto")
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/convert", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
}

func TestRejectsBadOCRMode(t *testing.T) {
	_, ts := newTestServer(t)
	data, _ := os.ReadFile(fixture("text.pdf"))
	resp := upload(t, ts, "a.pdf", data, map[string]string{"ocr": "sometimes"})
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", resp.StatusCode)
	}
}

func TestUploadLimit(t *testing.T) {
	s, err := New(Config{Base: convert.Options{Engine: &fakeEngine{}}, WorkDir: t.TempDir(), MaxUploadBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	defer s.Close()
	data, _ := os.ReadFile(fixture("scanned.pdf")) // ~54 KB
	resp := upload(t, ts, "big.pdf", data, nil)
	var body map[string]string
	decode(t, resp, &body)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", resp.StatusCode)
	}
	if !strings.Contains(body["error"], "1 KB") {
		t.Errorf("error %q should name the limit", body["error"])
	}
	if n := len(s.jobs.list()); n != 0 {
		t.Errorf("%d job(s) left behind by the rejected upload", n)
	}
}

// The default limit is 100 MB and the page learns it from /api/info so it
// can refuse bigger files before uploading them.
func TestUploadLimitDefaultAndInfo(t *testing.T) {
	s, ts := newTestServer(t)
	if s.cfg.MaxUploadBytes != 100<<20 {
		t.Fatalf("default MaxUploadBytes = %d, want 100 MiB", s.cfg.MaxUploadBytes)
	}
	resp, err := ts.Client().Get(ts.URL + "/api/info")
	if err != nil {
		t.Fatal(err)
	}
	var info InfoView
	decode(t, resp, &info)
	if info.MaxUploadBytes != 100<<20 {
		t.Fatalf("info.maxUploadBytes = %d", info.MaxUploadBytes)
	}
}

// Only files named *.pdf are accepted, whatever their content; the case of
// the extension does not matter.
func TestUploadRequiresPDFExtension(t *testing.T) {
	_, ts := newTestServer(t)
	data, _ := os.ReadFile(fixture("text.pdf"))
	for _, name := range []string{"report.txt", "report.docx", "report.pdf.exe", "report"} {
		resp := upload(t, ts, name, data, nil)
		var body map[string]string
		decode(t, resp, &body)
		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Errorf("%s: status %d, want 415", name, resp.StatusCode)
		}
		if !strings.Contains(body["error"], ".pdf") {
			t.Errorf("%s: error %q should mention .pdf", name, body["error"])
		}
	}
	resp := upload(t, ts, "REPORT.PDF", data, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Errorf("REPORT.PDF: status %d, want 202", resp.StatusCode)
	}
}

func TestUnknownJob(t *testing.T) {
	_, ts := newTestServer(t)
	for _, p := range []string{"/api/jobs/nope", "/api/jobs/nope/download"} {
		resp, err := ts.Client().Get(ts.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, resp.StatusCode)
		}
	}
}

func TestRefusesNonLoopbackHostAndForeignOrigin(t *testing.T) {
	_, ts := newTestServer(t)

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/info", nil)
	req.Host = "evil.example.com"
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign Host: status %d, want 403", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/ping", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	resp, err = ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign Origin: status %d, want 403", resp.StatusCode)
	}

	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/ping", nil)
	req.Header.Set("Origin", ts.URL)
	resp, err = ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("same Origin: status %d, want 200", resp.StatusCode)
	}
}

func TestAllowRemoteAcceptsForeignHost(t *testing.T) {
	s, err := New(Config{Base: convert.Options{Engine: &fakeEngine{}}, WorkDir: t.TempDir(), AllowRemote: true})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	defer s.Close()

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/info", nil)
	req.Host = "192.168.1.50:8080"
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("remote Host with AllowRemote: status %d, want 200", resp.StatusCode)
	}

	// Cross-site POSTs are still refused.
	req, _ = http.NewRequest(http.MethodPost, ts.URL+"/api/ping", nil)
	req.Header.Set("Origin", "http://evil.example.com")
	resp, err = ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign Origin: status %d, want 403", resp.StatusCode)
	}
}

// The page has to be cacheable by a CDN, which means no per-visitor
// Set-Cookie on it and a validator so repeat visits are cheap.
func TestPageIsCacheableAndRevalidates(t *testing.T) {
	_, ts := newTestServer(t)
	resp, err := ts.Client().Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	cc := resp.Header.Get("Cache-Control")
	if strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q; no-store keeps it out of every CDN and out of the back/forward cache", cc)
	}
	if !strings.Contains(cc, "s-maxage") {
		t.Errorf("Cache-Control = %q; want a shared-cache lifetime", cc)
	}
	tag := resp.Header.Get("ETag")
	if tag == "" {
		t.Fatal("no ETag, so every revalidation re-sends the whole page")
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/", nil)
	req.Header.Set("If-None-Match", tag)
	again, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(again.Body)
	again.Body.Close()
	if again.StatusCode != http.StatusNotModified {
		t.Errorf("revalidation returned %d, want 304", again.StatusCode)
	}
	if len(body) != 0 {
		t.Errorf("304 carried %d bytes of body", len(body))
	}

	// API responses stay private.
	api, err := ts.Client().Get(ts.URL + "/api/jobs")
	if err != nil {
		t.Fatal(err)
	}
	api.Body.Close()
	if !strings.Contains(api.Header.Get("Cache-Control"), "no-store") {
		t.Errorf("API Cache-Control = %q, want no-store", api.Header.Get("Cache-Control"))
	}
}

// Each browser gets a cookie on first visit and only lists its own jobs, so
// people sharing one server do not see each other's files.
func TestJobsAreScopedPerBrowser(t *testing.T) {
	_, ts := newTestServer(t)
	data, _ := os.ReadFile(fixture("text.pdf"))

	// The first API call mints the cookie; the page itself is cacheable and
	// must not carry a per-visitor Set-Cookie.
	page, err := ts.Client().Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	page.Body.Close()
	for _, c := range page.Cookies() {
		if c.Name == clientCookie {
			t.Fatalf("the cacheable page set a per-visitor cookie: %+v", c)
		}
	}
	resp, err := ts.Client().Get(ts.URL + "/api/jobs")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == clientCookie {
			cookie = c
		}
	}
	if cookie == nil || cookie.Value == "" || !cookie.HttpOnly {
		t.Fatalf("the first API call did not set an HttpOnly %s cookie: %+v", clientCookie, resp.Cookies())
	}

	uploadAs := func(c *http.Cookie, name string) JobView {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		fw, _ := mw.CreateFormFile("file", name)
		fw.Write(data)
		mw.Close()
		req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/convert", &body)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		if c != nil {
			req.AddCookie(c)
		}
		r, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var v JobView
		decode(t, r, &v)
		return v
	}
	listAs := func(c *http.Cookie) []string {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/jobs", nil)
		if c != nil {
			req.AddCookie(c)
		}
		r, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var views []JobView
		decode(t, r, &views)
		var names []string
		for _, v := range views {
			names = append(names, v.Filename)
		}
		return names
	}

	other := &http.Cookie{Name: clientCookie, Value: "someone-else"}
	mine := uploadAs(cookie, "mine.pdf")
	uploadAs(other, "theirs.pdf")
	anon := uploadAs(nil, "anonymous.pdf")

	if got := listAs(cookie); len(got) != 1 || got[0] != "mine.pdf" {
		t.Errorf("my list = %v, want [mine.pdf]", got)
	}
	if got := listAs(other); len(got) != 1 || got[0] != "theirs.pdf" {
		t.Errorf("their list = %v, want [theirs.pdf]", got)
	}
	// An upload that arrives without a session is given one, so it belongs
	// to that browser rather than to a shared bucket every stranger can
	// list. A caller that keeps no cookies reaches it by its id instead.
	if got := listAs(nil); len(got) != 0 {
		t.Errorf("cookie-less list = %v, want nothing shared between strangers", got)
	}
	if v := waitForJob(t, ts, anon.ID); v.Filename != "anonymous.pdf" {
		t.Errorf("a cookie-less client could not follow its own job by id: %+v", v)
	}
	// The owner can still reach its own job by id.
	waitForJob(t, ts, mine.ID, cookie)
}

func TestIdleTracking(t *testing.T) {
	s, ts := newTestServer(t)
	time.Sleep(30 * time.Millisecond)
	if s.IdleFor() < 20*time.Millisecond {
		t.Fatalf("IdleFor = %v, expected to grow without requests", s.IdleFor())
	}
	resp, _ := ts.Client().Post(ts.URL+"/api/ping", "", nil)
	resp.Body.Close()
	if s.IdleFor() > 20*time.Millisecond {
		t.Fatalf("IdleFor = %v after a ping, expected ~0", s.IdleFor())
	}
	if s.Busy() {
		t.Error("server should not be busy with no jobs")
	}
}

func TestLoopbackHost(t *testing.T) {
	for host, want := range map[string]bool{
		"127.0.0.1:8080": true, "localhost:8080": true, "[::1]:8080": true, "LOCALHOST": true,
		"192.168.1.5:8080": false, "example.com": false, "127.0.0.1.nip.io:80": false,
	} {
		if got := loopbackHost(host); got != want {
			t.Errorf("loopbackHost(%q) = %v, want %v", host, got, want)
		}
	}
}
