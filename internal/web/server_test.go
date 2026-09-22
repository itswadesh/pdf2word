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

func waitForJob(t *testing.T, ts *httptest.Server, id string) JobView {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := ts.Client().Get(ts.URL + "/api/jobs/" + id)
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
	resp := upload(t, ts, "notes.txt", []byte("hello there"), nil)
	if resp.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status %d, want 415", resp.StatusCode)
	}
	var e map[string]string
	decode(t, resp, &e)
	if !strings.Contains(e["error"], "not a PDF") {
		t.Errorf("error = %q", e["error"])
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
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", resp.StatusCode)
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

// Each browser gets a cookie on first visit and only lists its own jobs, so
// people sharing one server do not see each other's files.
func TestJobsAreScopedPerBrowser(t *testing.T) {
	_, ts := newTestServer(t)
	data, _ := os.ReadFile(fixture("text.pdf"))

	// First visit sets the cookie.
	resp, err := ts.Client().Get(ts.URL + "/")
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
		t.Fatalf("index did not set an HttpOnly %s cookie: %+v", clientCookie, resp.Cookies())
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
	uploadAs(nil, "anonymous.pdf")

	if got := listAs(cookie); len(got) != 1 || got[0] != "mine.pdf" {
		t.Errorf("my list = %v, want [mine.pdf]", got)
	}
	if got := listAs(other); len(got) != 1 || got[0] != "theirs.pdf" {
		t.Errorf("their list = %v, want [theirs.pdf]", got)
	}
	if got := listAs(nil); len(got) != 1 || got[0] != "anonymous.pdf" {
		t.Errorf("cookie-less list = %v, want [anonymous.pdf]", got)
	}
	// Direct access by id keeps working for everyone (ids are unguessable).
	waitForJob(t, ts, mine.ID)
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
