package web

import (
	"bytes"
	"encoding/json"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"pdf2word/internal/convert"
)

// ldBlocks returns every JSON-LD block on a page, parsed.
func ldBlocks(t *testing.T, path, page string) []map[string]any {
	t.Helper()
	var out []map[string]any
	const open = `<script type="application/ld+json">`
	for rest := page; ; {
		i := strings.Index(rest, open)
		if i < 0 {
			return out
		}
		rest = rest[i+len(open):]
		j := strings.Index(rest, "</script>")
		var v map[string]any
		if err := json.Unmarshal([]byte(rest[:j]), &v); err != nil {
			t.Fatalf("%s: structured data does not parse: %v\n%s", path, err, rest[:j])
		}
		out = append(out, v)
	}
}

// The things search engines and link previews look for: structured data
// that parses, a sharing image, icons they can fetch, a real not-found
// page, and one address per page.
func TestSEOElements(t *testing.T) {
	const site = "https://pdf2word.example.com"
	s, err := New(Config{Base: convert.Options{Engine: &fakeEngine{}}, WorkDir: t.TempDir(), PublicURL: site})
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { ts.Close(); s.Close() })
	noRedirect := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	for _, p := range sitePages {
		page := get(t, ts, p.Path)
		types := map[string]map[string]any{}
		for _, b := range ldBlocks(t, p.Path, page) {
			if b["@context"] != "https://schema.org" {
				t.Errorf("%s: @context = %v", p.Path, b["@context"])
			}
			types[b["@type"].(string)] = b
		}
		if p.Home {
			if types["WebApplication"] == nil || types["WebSite"] == nil {
				t.Errorf("home page structured data = %v, want WebApplication and WebSite", keys(types))
			}
		} else {
			bc := types["BreadcrumbList"]
			if bc == nil {
				t.Errorf("%s: no breadcrumbs", p.Path)
			} else {
				items := bc["itemListElement"].([]any)
				last := items[len(items)-1].(map[string]any)
				if len(items) != 2 || last["item"] != site+p.Path || last["name"] != p.Name {
					t.Errorf("%s: breadcrumbs = %v", p.Path, items)
				}
			}
		}
		if a := types["Article"]; (a != nil) != p.Article {
			t.Errorf("%s: Article present = %v, want %v", p.Path, a != nil, p.Article)
		} else if a != nil && (a["headline"] != p.Title || a["mainEntityOfPage"] != site+p.Path || a["dateModified"] == "") {
			t.Errorf("%s: Article = %v", p.Path, a)
		}
		for _, want := range []string{
			`<meta property="og:image" content="` + site + `/og.png">`,
			`<meta name="twitter:card" content="summary_large_image">`,
			`<link rel="icon" href="/favicon.svg" type="image/svg+xml">`,
			`<link rel="apple-touch-icon" href="/apple-touch-icon.png">`,
			`<meta name="robots" content="index,follow`,
		} {
			if !strings.Contains(page, want) {
				t.Errorf("%s: missing %s", p.Path, want)
			}
		}
		if strings.Contains(page, `rel="icon" href="data:`) {
			t.Errorf("%s: the favicon is a data: address search engines cannot fetch", p.Path)
		}
		// One address per page: the trailing-slash form moves permanently.
		if p.Path != "/" {
			resp, err := noRedirect.Get(ts.URL + p.Path + "/")
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusMovedPermanently || resp.Header.Get("Location") != p.Path {
				t.Errorf("%s/: status %d, Location %q; want 301 to %s", p.Path, resp.StatusCode, resp.Header.Get("Location"), p.Path)
			}
		}
	}

	// Assets: served with their type, cacheable, and the right size.
	for path, want := range map[string]struct {
		typ  string
		w, h int
	}{
		"/og.png":               {"image/png", 1200, 630},
		"/apple-touch-icon.png": {"image/png", 180, 180},
		"/favicon-48.png":       {"image/png", 48, 48},
		"/favicon.ico":          {"image/png", 48, 48},
		"/favicon.svg":          {"image/svg+xml", 0, 0},
	} {
		resp, err := ts.Client().Get(ts.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != want.typ || !strings.Contains(resp.Header.Get("Cache-Control"), "max-age=") {
			t.Errorf("%s: status %d, type %q, Cache-Control %q", path, resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Cache-Control"))
			continue
		}
		if want.w > 0 {
			cfg, err := png.DecodeConfig(bytes.NewReader(body))
			if err != nil || cfg.Width != want.w || cfg.Height != want.h {
				t.Errorf("%s: %dx%d (%v), want %dx%d", path, cfg.Width, cfg.Height, err, want.w, want.h)
			}
		} else if !bytes.Contains(body, []byte("<svg")) {
			t.Errorf("%s is not an SVG", path)
		}
	}

	// A real not-found page: status 404, kept out of search, pointing home.
	resp, err := ts.Client().Get(ts.URL + "/no-such-page")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	nf := string(body)
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(nf, `<meta name="robots" content="noindex,follow">`) ||
		strings.Contains(nf, `rel="canonical"`) || !strings.Contains(nf, `href="/"`) || strings.Count(nf, "<h1>") != 1 {
		t.Errorf("not-found page: status %d\n%s", resp.StatusCode, between(nf, "<head>", "<style>"))
	}
	if strings.Contains(get(t, ts, "/sitemap.xml"), "/404") {
		t.Error("the not-found page is in the sitemap")
	}
}

func keys(m map[string]map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
