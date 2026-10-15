package server

import (
	"encoding/json"
	"fmt"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func appMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /manifest.webmanifest", HandleManifest)
	mux.HandleFunc("GET /icon/{name}", HandleIcon)
	mux.HandleFunc("GET /sw.js", HandleWorker)
	return mux
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
	return w
}

func TestManifestAndIcons(t *testing.T) {
	mux := appMux()
	w := get(t, mux, "/manifest.webmanifest")
	var m struct {
		ID, Name, StartURL, Scope, Display string
		Icons                              []struct{ Src, Sizes, Type, Purpose string }
	}
	if w.Header().Get("Content-Type") != "application/manifest+json" || json.Unmarshal(w.Body.Bytes(), &m) != nil {
		t.Fatalf("manifest: %s\n%s", w.Header().Get("Content-Type"), w.Body)
	}
	if m.Name != "dv" || m.Display != "standalone" || len(m.Icons) != 3 {
		t.Fatalf("manifest: %+v", m)
	}
	icons := map[string]string{"/icon/badge.png": "96x96"}
	for _, i := range m.Icons {
		icons[i.Src] = i.Sizes
	}
	for src, sizes := range icons {
		w := get(t, mux, src)
		img, err := png.Decode(w.Body)
		if w.Code != 200 || w.Header().Get("Content-Type") != "image/png" || err != nil {
			t.Fatalf("%s: %d %v", src, w.Code, err)
		}
		if b := img.Bounds(); fmt.Sprintf("%dx%d", b.Dx(), b.Dy()) != sizes {
			t.Errorf("%s is %v, not %s", src, b, sizes)
		}
	}
	if w := get(t, mux, "/icon/huge.png"); w.Code != 404 {
		t.Errorf("an unknown icon: %d", w.Code)
	}
}

func TestIconsLookLikeTheTabs(t *testing.T) {
	at := func(name string, x, y int) color.NRGBA {
		img, _ := png.Decode(strings.NewReader(string(icons()[name])))
		return color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
	}
	red := color.NRGBA{0xf8, 0x51, 0x49, 0xff}
	// The red line's middle is (9, 16) of the tab's 32.
	for _, c := range []struct {
		name string
		x, y int
		want color.NRGBA
	}{
		{"192.png", 54, 96, red},
		{"192.png", 0, 0, color.NRGBA{}},                         // outside the rounded corner
		{"512.png", 256, 4, color.NRGBA{0x3e, 0x3e, 0x3e, 0xff}}, // the frame's edge
		{"maskable-512.png", 0, 0, color.NRGBA{0x14, 0x14, 0x14, 0xff}},
		{"maskable-512.png", 256 - 7*512*6/10/28, 256, red},
		{"badge.png", 27, 48, color.NRGBA{0xff, 0xff, 0xff, 0xff}},
		{"badge.png", 40, 48, color.NRGBA{}}, // between the line and the divider
	} {
		if got := at(c.name, c.x, c.y); got != c.want {
			t.Errorf("%s at (%d, %d) is %v, not %v", c.name, c.x, c.y, got, c.want)
		}
	}
}

func TestWorker(t *testing.T) {
	w := get(t, appMux(), "/sw.js")
	body := w.Body.String()
	if w.Header().Get("Content-Type") != "application/javascript" || w.Header().Get("Cache-Control") != "no-cache" {
		t.Fatalf("served as %v", w.Header())
	}
	// Pages always come from dv: a fetch handler could serve one from a cache.
	if !strings.Contains(body, `"push"`) || !strings.Contains(body, `"notificationclick"`) || strings.Contains(body, `"fetch"`) {
		t.Fatalf("sw.js:\n%s", body)
	}
}
