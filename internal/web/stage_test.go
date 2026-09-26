package web

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"pdf2word/internal/convert"
	"strings"
	"testing"
	"time"
)

// uploadAs posts a file with form fields, as the browser holding c.
func uploadAs(t *testing.T, ts *httptest.Server, c *http.Cookie, name string, data []byte, fields map[string]string) *http.Response {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write(data)
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/convert", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if c != nil {
		req.AddCookie(c)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func postForm(t *testing.T, ts *httptest.Server, path string, c *http.Cookie, form url.Values) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c != nil {
		req.AddCookie(c)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// A file uploaded with hold=1 waits for its uploader: it can be previewed,
// it is not in the list of conversions and does not keep the server busy,
// and it converts, in the language chosen then, only when started.
func TestStagedUploadWaitsToBeStarted(t *testing.T) {
	s, ts := newTestServer(t)
	data, _ := os.ReadFile(fixture("text.pdf"))
	mine := &http.Cookie{Name: clientCookie, Value: "browser-one"}
	theirs := &http.Cookie{Name: clientCookie, Value: "browser-two"}

	var staged JobView
	decode(t, uploadAs(t, ts, mine, "report.pdf", data, map[string]string{"hold": "1"}), &staged)
	if staged.State != StateStaged || staged.Pages < 1 {
		t.Fatalf("held upload = %+v; want state staged with a page count", staged)
	}
	if s.Busy() {
		t.Error("a staged file counts as work in progress")
	}
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/jobs", nil)
	req.AddCookie(mine)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var list []JobView
	decode(t, resp, &list)
	if len(list) != 0 {
		t.Errorf("the list of conversions shows a file nobody started: %+v", list)
	}
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/jobs/"+staged.ID+"/pages/1?size=large", nil)
	req.AddCookie(mine)
	if resp, err = ts.Client().Do(req); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("preview of a staged file: %v %v", err, resp.Status)
	}
	resp.Body.Close()

	start := "/api/jobs/" + staged.ID + "/start"
	for _, tc := range []struct {
		as   *http.Cookie
		lang string
		want int
	}{
		{theirs, "eng", http.StatusNotFound},
		{mine, "eng; rm -rf /", http.StatusBadRequest},
	} {
		r := postForm(t, ts, start, tc.as, url.Values{"lang": {tc.lang}})
		r.Body.Close()
		if r.StatusCode != tc.want {
			t.Errorf("start as %s with lang %q: status %d, want %d", tc.as.Value, tc.lang, r.StatusCode, tc.want)
		}
	}

	var started JobView
	decode(t, postForm(t, ts, start, mine, url.Values{"lang": {"ori"}}), &started)
	if started.State != StateQueued && started.State != StateRunning && !started.State.terminal() {
		t.Fatalf("started job = %+v", started)
	}
	j, _ := s.jobs.get(staged.ID)
	if j.lang != "ori" {
		t.Errorf("job language = %q, want the ori chosen at start", j.lang)
	}
	r := postForm(t, ts, start, mine, nil)
	r.Body.Close()
	if r.StatusCode != http.StatusConflict {
		t.Errorf("starting twice: status %d, want 409", r.StatusCode)
	}
	if v := waitForJob(t, ts, staged.ID, mine); v.State != StateDone {
		t.Errorf("staged file ended %s: %s", v.State, v.Message)
	}
}

// "Read every page as a scan" is chosen at start, like the language: a text
// PDF started with ocr=force is read with OCR, and a mode the converter does
// not know is refused without starting the file.
func TestStartCanReadEveryPageAsAScan(t *testing.T) {
	s, ts := newTestServer(t)
	data, _ := os.ReadFile(fixture("text.pdf"))
	mine := &http.Cookie{Name: clientCookie, Value: "browser-one"}

	var staged JobView
	decode(t, uploadAs(t, ts, mine, "legacy-font.pdf", data, map[string]string{"hold": "1"}), &staged)
	start := "/api/jobs/" + staged.ID + "/start"

	r := postForm(t, ts, start, mine, url.Values{"ocr": {"always; rm -rf /"}})
	r.Body.Close()
	if r.StatusCode != http.StatusBadRequest {
		t.Fatalf("start with an unknown OCR mode: status %d, want 400", r.StatusCode)
	}
	if j, _ := s.jobs.get(staged.ID); j.currentState() != StateStaged {
		t.Fatalf("a refused start left the file %s, want it still staged", j.currentState())
	}

	var started JobView
	decode(t, postForm(t, ts, start, mine, url.Values{"lang": {"eng"}, "ocr": {"force"}}), &started)
	if j, _ := s.jobs.get(staged.ID); j.ocr != convert.OCRForce {
		t.Errorf("job OCR mode = %q, want force", j.ocr)
	}
	v := waitForJob(t, ts, staged.ID, mine)
	if v.State != StateDone || v.Report.OCRPages != v.Report.Pages || v.Report.TextPages != 0 {
		t.Errorf("forced start ended %s with %+v; want every page read with OCR", v.State, v.Report)
	}
}

// Closing the dialog cancels a staged file, which deletes it outright.
func TestCancellingAStagedFileDeletesIt(t *testing.T) {
	s, ts := newTestServer(t)
	data, _ := os.ReadFile(fixture("text.pdf"))
	mine := &http.Cookie{Name: clientCookie, Value: "browser-one"}

	var staged JobView
	decode(t, uploadAs(t, ts, mine, "report.pdf", data, map[string]string{"hold": "1"}), &staged)
	j, _ := s.jobs.get(staged.ID)
	dir := j.dir

	var v JobView
	decode(t, postForm(t, ts, "/api/jobs/"+staged.ID+"/cancel", mine, nil), &v)
	if v.State != StateCancelled {
		t.Errorf("cancel answered %s", v.State)
	}
	if _, ok := s.jobs.get(staged.ID); ok {
		t.Error("the cancelled file is still a job")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("the cancelled file's directory is still there: %v", err)
	}
	s.preview.mu.Lock()
	open := s.preview.doc != nil
	s.preview.mu.Unlock()
	if open {
		t.Error("the preview still holds the cancelled file open")
	}
}

// A file PDFium cannot open is refused when it is staged, not after the
// visitor has pressed Convert and waited.
func TestStagingRefusesAnUnreadablePDF(t *testing.T) {
	s, ts := newTestServer(t)
	resp := uploadAs(t, ts, nil, "broken.pdf", []byte("%PDF-1.4\nthis is not really a PDF\n"), map[string]string{"hold": "1"})
	var body map[string]string
	decode(t, resp, &body)
	if resp.StatusCode != http.StatusUnprocessableEntity || !strings.Contains(body["error"], "broken.pdf") {
		t.Errorf("status %d, error %q; want 422 naming the file", resp.StatusCode, body["error"])
	}
	if n := len(s.jobs.list()); n != 0 {
		t.Errorf("%d jobs left behind by a refused file", n)
	}
}

// A staged file nobody starts is deleted by the same sweep as finished ones.
func TestForgottenStagedFilesArePruned(t *testing.T) {
	s, ts := newTestServer(t)
	data, _ := os.ReadFile(fixture("text.pdf"))
	var staged JobView
	decode(t, uploadAs(t, ts, nil, "report.pdf", data, map[string]string{"hold": "1"}), &staged)
	j, _ := s.jobs.get(staged.ID)
	j.mu.Lock()
	j.created = time.Now().Add(-2 * retention)
	j.mu.Unlock()

	s.jobs.prune(retention, s.preview.forget)
	if _, ok := s.jobs.get(staged.ID); ok {
		t.Error("a staged file older than the retention period was kept")
	}
	if _, err := os.Stat(j.dir); !os.IsNotExist(err) {
		t.Errorf("its directory is still there: %v", err)
	}
}
