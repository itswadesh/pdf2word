package web

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"pdf2word/internal/convert"
)

// Every page of the site is served, stands on its own in search results,
// links only to pages that exist, and carries the converter only where it
// belongs.
func TestSitePages(t *testing.T) {
	s, err := New(Config{Base: convert.Options{Engine: &fakeEngine{}}, WorkDir: t.TempDir(), PublicURL: "https://pdf2word.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Close() })

	known := map[string]bool{}
	for _, p := range sitePages {
		known[p.Path] = true
	}
	for path := range assetFiles {
		known[path] = true
	}
	titles, descs := map[string]string{}, map[string]string{}
	internal := regexp.MustCompile(`href="(/[^"#]*)`)

	for _, p := range sitePages {
		resp, err := ts.Client().Get(ts.URL + p.Path)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("%s: status %d, type %q", p.Path, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "s-maxage") || resp.Header.Get("ETag") == "" {
			t.Errorf("%s: not cacheable (Cache-Control %q, ETag %q)", p.Path, cc, resp.Header.Get("ETag"))
		}
		resp.Body.Close()
		page := get(t, ts, p.Path)

		title := between(page, "<title>", "</title>")
		if n := utf8.RuneCountInString(title); n < 30 || n > 60 {
			t.Errorf("%s: title is %d characters, want 30-60: %q", p.Path, n, title)
		}
		if other, dup := titles[title]; dup {
			t.Errorf("%s and %s share the title %q", p.Path, other, title)
		}
		titles[title] = p.Path
		desc := attrOf(page, `<meta name="description" content="`)
		if n := utf8.RuneCountInString(desc); n < 120 || n > 160 {
			t.Errorf("%s: description is %d characters, want 120-160", p.Path, n)
		}
		if other, dup := descs[desc]; dup {
			t.Errorf("%s and %s share a description", p.Path, other)
		}
		descs[desc] = p.Path
		if n := strings.Count(page, "<h1>"); n != 1 {
			t.Errorf("%s: %d h1 elements, want 1", p.Path, n)
		}
		want := `<link rel="canonical" href="https://pdf2word.example.com` + p.Path + `">`
		if !strings.Contains(page, want) {
			t.Errorf("%s: missing %s", p.Path, want)
		}
		for _, leftover := range []string{"%PUBLIC_URL%", "%PRIVACY%", "%FILES_ANSWER%", "%ANALYTICS%", "{{"} {
			if strings.Contains(page, leftover) {
				t.Errorf("%s: %q left in the page", p.Path, leftover)
			}
		}
		hasTool := strings.Contains(page, `id="pick"`) && strings.Contains(page, "/api/convert") && strings.Contains(page, "<dialog")
		if hasTool != p.Tool {
			t.Errorf("%s: converter present = %v, want %v", p.Path, hasTool, p.Tool)
		}
		if p.Tool && !strings.Contains(page, `data-lang="`+p.Lang+`"`) {
			t.Errorf("%s: the converter does not start on %q", p.Path, p.Lang)
		}
		for _, m := range internal.FindAllStringSubmatch(page, -1) {
			if href := m[1]; !strings.HasPrefix(href, "/api/") && !known[href] {
				t.Errorf("%s links to %s, which is not a page", p.Path, href)
			}
		}
		// The page's own entries in the header and footer say so.
		if p.Path != "/" && !strings.Contains(page, `href="`+p.Path+`" aria-current="page"`) {
			t.Errorf("%s: its own link is not marked as the current page", p.Path)
		}
	}

	sitemap := get(t, ts, "/sitemap.xml")
	for _, p := range sitePages {
		if !strings.Contains(sitemap, "<loc>https://pdf2word.example.com"+p.Path+"</loc>") {
			t.Errorf("sitemap is missing %s", p.Path)
		}
	}
	resp, err := ts.Client().Get(ts.URL + "/no-such-page")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown path: status %d, want 404", resp.StatusCode)
	}
}
