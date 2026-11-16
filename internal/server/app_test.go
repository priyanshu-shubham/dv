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

	"dv/internal/store"
)

func appMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /manifest.webmanifest", Manifest(nil))
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

func TestManifestTakesTabName(t *testing.T) {
	user, err := store.OpenRepoPrefs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	user.Set("settings", json.RawMessage(`{"tabName":" work \"pc\"  büro "}`), nil)
	w := get(t, Manifest(user), "/manifest.webmanifest")
	var m struct {
		Name      string
		ShortName string `json:"short_name"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil || m.Name != `work pc büro` || m.ShortName != m.Name {
		t.Fatalf("manifest: %+v %v\n%s", m, err, w.Body)
	}
}

func TestAppName(t *testing.T) {
	for in, want := range map[string]string{
		"":                               "",
		"  home  ":                       "home",
		"home<script>":                   "homescript",
		"a very long name for a dv here": "a very long name for",
		"x\t\ty":                         "xy",
		"--":                             "--",
	} {
		if got := appName(in); got != want {
			t.Errorf("appName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestManifestRingsIconsAsTheTab(t *testing.T) {
	user, err := store.OpenRepoPrefs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	user.Set("settings", json.RawMessage(`{"tabColor":"teal"}`), nil)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /manifest.webmanifest", Manifest(user))
	mux.HandleFunc("GET /icon/{name}", HandleIcon)
	var m struct{ Icons []struct{ Src string } }
	json.Unmarshal(get(t, mux, "/manifest.webmanifest").Body.Bytes(), &m)
	teal := color.NRGBA{0x1f, 0x9e, 0x9e, 0xff}
	for _, c := range []struct {
		src  string
		x, y int
	}{{m.Icons[0].Src, 96, 4}, {m.Icons[2].Src, 256, 256 - 200}} {
		img, err := png.Decode(get(t, mux, c.src).Body)
		if err != nil {
			t.Fatal(c.src, err)
		}
		if got := color.NRGBAModel.Convert(img.At(c.x, c.y)); got != teal {
			t.Errorf("%s at (%d, %d) is %v, not the ring", c.src, c.x, c.y, got)
		}
	}
}

// The accent marks the app's icon as it does the tab's: a tile of it when
// filled, and with no accent picked, the theme's own.
func TestManifestMarksIconsWithTheAccent(t *testing.T) {
	for _, c := range []struct {
		settings, theme, src string
		x, y                 int
		want                 color.NRGBA
	}{
		{`{"accent":"#F2A65A","tabIcon":"filled"}`, `"dark"`, "/icon/192.png?fill=f2a65a", 96, 4, color.NRGBA{0xf2, 0xa6, 0x5a, 0xff}},
		{`{"tabIcon":"ring"}`, `"graphite"`, "/icon/192.png?ring=a78bfa", 96, 4, color.NRGBA{0xa7, 0x8b, 0xfa, 0xff}},
		{`{"accent":"#2dd4bf","tabIcon":"filled"}`, `"dark"`, "/icon/maskable-512.png?fill=2dd4bf", 4, 4, color.NRGBA{0x2d, 0xd4, 0xbf, 0xff}},
	} {
		user, err := store.OpenRepoPrefs(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		user.Set("settings", json.RawMessage(c.settings), nil)
		user.Set("theme", json.RawMessage(c.theme), nil)
		mux := http.NewServeMux()
		mux.HandleFunc("GET /manifest.webmanifest", Manifest(user))
		mux.HandleFunc("GET /icon/{name}", HandleIcon)
		var m struct{ Icons []struct{ Src string } }
		json.Unmarshal(get(t, mux, "/manifest.webmanifest").Body.Bytes(), &m)
		if !strings.Contains(c.src, m.Icons[0].Src[len("/icon/192.png"):]) {
			t.Errorf("%s: icons at %q, not %q", c.settings, m.Icons[0].Src, c.src)
		}
		img, err := png.Decode(get(t, mux, c.src).Body)
		if err != nil {
			t.Fatal(c.src, err)
		}
		if got := color.NRGBAModel.Convert(img.At(c.x, c.y)); got != c.want {
			t.Errorf("%s at (%d, %d) is %v, not %v", c.src, c.x, c.y, got, c.want)
		}
	}
}

// The page is in its theme and Appearance's measures from its first frame,
// and a value that isn't a plain word never reaches the tag.
func TestPageStartsInItsAppearance(t *testing.T) {
	w := httptest.NewRecorder()
	WritePage(w, Boot{Prefs: map[string]map[string]json.RawMessage{"user": {
		"theme":    json.RawMessage(`"ember"`),
		"settings": json.RawMessage(`{"density":"compact","corners":"round","glass":true,"glow":"x\" onload=\"y"}`),
	}}})
	html := w.Body.String()
	want := `<html lang="en" data-theme="ember" data-code="github" data-density="compact" data-corners="round" data-glass="on" style="--tab-mark:#f2a65a;--blur:16px;--glass-a:0.6">`
	if !strings.Contains(html, want) {
		t.Fatalf("no %s in\n%.300s", want, html)
	}
}

// A picked accent is --pick, and the tab's colour is it too; settings from
// before the accent give the tab's colour by name for both.
func TestPageAppearanceAccent(t *testing.T) {
	for _, c := range []struct{ settings, want string }{
		{`{"accent":"#F472B6","codeSize":13,"uiFont":"geist"}`, ` data-theme="dark" data-code="github" data-ui-font="geist" data-code-size="13" data-accent="on" style="--tab-mark:#f472b6;--pick:#f472b6"`},
		{`{"tabColor":"teal"}`, ` data-theme="dark" data-code="github" data-accent="on" style="--tab-mark:#1f9e9e;--pick:#1f9e9e"`},
		{`{"accent":"red;x:y","codeSize":99}`, ` data-theme="dark" data-code="github" style="--tab-mark:#5b9dff"`},
	} {
		if got := PageAppearance("dark", json.RawMessage(c.settings)); got != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.settings, got, c.want)
		}
	}
}

// An app installed before the accent asks for its ring by name.
func TestIconRingByOldName(t *testing.T) {
	img, err := png.Decode(get(t, appMux(), "/icon/192.png?ring=teal").Body)
	if err != nil {
		t.Fatal(err)
	}
	if got := color.NRGBAModel.Convert(img.At(96, 4)); got != (color.NRGBA{0x1f, 0x9e, 0x9e, 0xff}) {
		t.Errorf("ring is %v", got)
	}
	if w := get(t, appMux(), "/icon/192.png?ring=nothex"); w.Code != 200 {
		t.Errorf("a bad colour gave %d, not the plain icon", w.Code)
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
	// Its pictures are in it, as nothing past a login can be fetched for them.
	for _, name := range []string{"ICON", "BADGE"} {
		if !strings.Contains(body, "const "+name+` = "data:image/png;base64,`) {
			t.Fatalf("no %s in sw.js", name)
		}
	}
}
