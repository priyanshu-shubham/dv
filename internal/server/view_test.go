package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A file anywhere is served to dv's own pages, by its absolute path or one
// from home, and to no other site, which could otherwise run a page as dv.
func TestView(t *testing.T) {
	dir := t.TempDir()
	page := filepath.Join(dir, "pages", "chart.html")
	if err := os.MkdirAll(filepath.Dir(page), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(page, []byte("<h1>chart</h1>"), 0o644); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/view/{path...}", Guarded((&Server{}).handleView))
	url := func(p string) string { return "/api/view/" + strings.TrimPrefix(filepath.ToSlash(p), "/") }
	for _, c := range []struct {
		name, site, path string
		want             int
	}{
		{"from dv's page", "same-origin", url(page), 200},
		{"typed in", "none", url(page), 200},
		{"from a browser that does not say", "", url(page), 200},
		{"from another site", "cross-site", url(page), 403},
		{"from another port", "same-site", url(page), 403},
		{"a folder", "", url(dir), 404},
		{"a file not there", "", url(page + ".gone"), 404},
	} {
		r := httptest.NewRequest("GET", c.path, nil)
		r.Host = "localhost:4100"
		if c.site != "" {
			r.Header.Set("Sec-Fetch-Site", c.site)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != c.want {
			t.Errorf("%s: %d %s", c.name, w.Code, w.Body)
		} else if c.want == 200 && (!strings.HasPrefix(w.Header().Get("Content-Type"), "text/html") || w.Body.String() != "<h1>chart</h1>") {
			t.Errorf("%s: %s %q", c.name, w.Header().Get("Content-Type"), w.Body)
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	if got, _ := viewPath("~/pages/chart.html"); got != filepath.Join(home, "pages", "chart.html") {
		t.Errorf("from home: %s", got)
	}
}
