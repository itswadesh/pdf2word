package web

import (
	"bytes"
	"image/jpeg"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"testing"
)

// The scanner on the page shows the page being converted. The thumbnail
// is the uploader's alone, is only drawn while the conversion is going,
// and must not keep the document open once it is over.
func TestPagePreview(t *testing.T) {
	s, ts := newTestServer(t)
	data, _ := os.ReadFile(fixture("text.pdf"))
	mine := &http.Cookie{Name: clientCookie, Value: "browser-one"}
	theirs := &http.Cookie{Name: clientCookie, Value: "browser-two"}

	get := func(path string, c *http.Cookie) *http.Response {
		req, _ := http.NewRequest(http.MethodGet, ts.URL+path, nil)
		req.AddCookie(c)
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// Hold the single conversion slot so the job stays queued, which is
	// "in progress" as far as the preview is concerned.
	s.sem <- struct{}{}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "report.pdf")
	fw.Write(data)
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, ts.URL+"/api/convert", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(mine)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var job JobView
	decode(t, resp, &job)
	base := "/api/jobs/" + job.ID + "/pages/"

	r := get(base+"1", mine)
	img, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusOK || r.Header.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("owner: status %d, type %q", r.StatusCode, r.Header.Get("Content-Type"))
	}
	if cc := r.Header.Get("Cache-Control"); cc != "private, max-age=600" {
		t.Errorf("Cache-Control = %q; a shared cache must not keep someone's page", cc)
	}
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(img))
	if err != nil {
		t.Fatalf("not a JPEG: %v", err)
	}
	if cfg.Width > previewWidth || cfg.Height > previewHeight || (cfg.Width != previewWidth && cfg.Height != previewHeight) {
		t.Errorf("thumbnail is %dx%d; want it to fill a %dx%d box", cfg.Width, cfg.Height, previewWidth, previewHeight)
	}

	for _, tc := range []struct {
		path string
		as   *http.Cookie
	}{
		{base + "1", theirs},
		{base + "0", mine},
		{base + "999", mine},
		{base + "one", mine},
	} {
		r := get(tc.path, tc.as)
		r.Body.Close()
		if r.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s as %s: status %d, want 404", tc.path, tc.as.Value, r.StatusCode)
		}
	}

	<-s.sem // let it convert
	waitForJob(t, ts, job.ID, mine)
	s.preview.mu.Lock()
	open := s.preview.doc != nil
	s.preview.mu.Unlock()
	if open {
		t.Error("the preview document is still open after the conversion finished")
	}
	// A page already drawn is still served from the job's directory; a new
	// one is not drawn for a finished conversion.
	r = get(base+"1", mine)
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Errorf("cached page after finishing: status %d, want 200", r.StatusCode)
	}
	s.preview.mu.Lock()
	open = s.preview.doc != nil
	s.preview.mu.Unlock()
	if open {
		t.Error("serving a cached page reopened the document")
	}
}
