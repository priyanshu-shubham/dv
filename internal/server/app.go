package server

import (
	"bytes"
	"cmp"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"regexp"
	"strings"
	"sync"

	"dv/internal/store"
)

// What a browser installs dv as an app from: the manifest, its icons, and the
// service worker that shows the notices pushed to the device. None holds
// anything of the reader's, so none is Guarded; each is the process's, a
// hub's at its root, as the app is the whole hub.

//go:embed sw.js
var serviceWorker []byte

const manifest = `{"id":"/","name":%[1]s,"short_name":%[1]s,"start_url":"/","scope":"/","display":"standalone","background_color":"#131314","theme_color":"#131314","icons":[` +
	`{"src":"/icon/192.png%[2]s","sizes":"192x192","type":"image/png","purpose":"any"},` +
	`{"src":"/icon/512.png%[2]s","sizes":"512x512","type":"image/png","purpose":"any"},` +
	`{"src":"/icon/maskable-512.png%[2]s","sizes":"512x512","type":"image/png","purpose":"maskable"}]}`

// Manifest serves the manifest, the app named and marked as the tab is: the
// settings tabName, tabIcon and accent, as appearance.js's tabMark reads them.
// A phone takes them as it installs; changing them later is up to the phone.
func Manifest(user *store.Prefs) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var s struct {
			TabName  string `json:"tabName"`
			TabColor string `json:"tabColor"`
			TabIcon  string `json:"tabIcon"`
			Accent   string `json:"accent"`
		}
		var theme string
		if user != nil {
			all := user.All()
			json.Unmarshal(all["settings"], &s)
			json.Unmarshal(all["theme"], &theme)
		}
		name, _ := json.Marshal(cmp.Or(appName(s.TabName), "dv"))
		style, c := tabMark(theme, s.Accent, s.TabColor, s.TabIcon)
		// In the address, as icons are cached for a day and kept by their URL.
		mark := ""
		switch style {
		case "ring":
			mark = "?ring=" + hexOf(c)
		case "filled":
			mark = "?fill=" + hexOf(c)
		}
		w.Header().Set("Content-Type", "application/manifest+json")
		w.Header().Set("Cache-Control", "no-cache")
		fmt.Fprintf(w, manifest, name, mark)
	}
}

// nameMax and appName are appearance.js's NAME_MAX and cleanName: letters,
// digits, . _ and -, and single spaces, so a name sits in the bar as a word.
const nameMax = 20

var notInName = regexp.MustCompile(`[^\p{L}\p{N} ._-]`)

func appName(s string) string {
	r := []rune(strings.Join(strings.Fields(notInName.ReplaceAllString(s, "")), " "))
	return strings.TrimSpace(string(r[:min(len(r), nameMax)]))
}

// accentOf and tabMark are appearance.js's: the picked accent, else the tab's
// colour by name from before there was one; the tab's style and colour.
func accentOf(accent, tabColor string) (color.RGBA, bool) {
	if c, ok := hexColor(accent); ok {
		return c, true
	}
	c, ok := ringColors[tabColor]
	return c, ok
}

func tabMark(theme, accent, tabColor, tabIcon string) (string, color.RGBA) {
	style := tabIcon
	if style == "" && tabColor != "" {
		style = "ring"
	}
	c, ok := accentOf(accent, tabColor)
	if !ok {
		c = cmp.Or(themeAccents[theme], themeAccents["dark"])
	}
	return style, c
}

// PageAppearance is appearance.js's appearanceOf as the attributes of <html>,
// so a page's first frame is drawn in Settings → Appearance: theme is the
// "theme" pref, settings the "settings" one. Only words and checked values
// reach it.
func PageAppearance(theme string, settings json.RawMessage) string {
	var s struct {
		CodeColors   string   `json:"codeColors"`
		Density      string   `json:"density"`
		Corners      string   `json:"corners"`
		Changes      string   `json:"changes"`
		Glow         string   `json:"glow"`
		Glass        bool     `json:"glass"`
		Motion       *bool    `json:"motion"`
		UIFont       string   `json:"uiFont"`
		CodeFont     string   `json:"codeFont"`
		CodeSize     int      `json:"codeSize"`
		Accent       string   `json:"accent"`
		TabColor     string   `json:"tabColor"`
		TabIcon      string   `json:"tabIcon"`
		GlassBlur    *float64 `json:"glassBlur"`
		GlassOpacity *float64 `json:"glassOpacity"`
	}
	json.Unmarshal(settings, &s)
	var b strings.Builder
	attr := func(name, v string) {
		if attrWord.MatchString(v) {
			fmt.Fprintf(&b, ` data-%s="%s"`, name, v)
		}
	}
	attr("theme", theme)
	attr("code", cmp.Or(s.CodeColors, "github"))
	attr("density", s.Density)
	attr("corners", s.Corners)
	attr("changes", s.Changes)
	attr("glow", s.Glow)
	if s.Glass {
		attr("glass", "on")
	}
	if s.Motion != nil && !*s.Motion {
		attr("motion", "off")
	}
	attr("ui-font", s.UIFont)
	attr("code-font", s.CodeFont)
	if s.CodeSize >= 11 && s.CodeSize <= 15 && s.CodeSize != 12 {
		attr("code-size", fmt.Sprint(s.CodeSize))
	}
	_, mark := tabMark(theme, s.Accent, s.TabColor, s.TabIcon)
	style := "--tab-mark:#" + hexOf(mark)
	if c, ok := accentOf(s.Accent, s.TabColor); ok {
		attr("accent", "on")
		style += ";--pick:#" + hexOf(c)
	}
	if s.Glass {
		blur, opacity := 16.0, 60.0
		if s.GlassBlur != nil {
			blur = *s.GlassBlur
		}
		if s.GlassOpacity != nil {
			opacity = *s.GlassOpacity
		}
		style += fmt.Sprintf(";--blur:%gpx;--glass-a:%g", min(max(blur, 0), 40), min(max(opacity, 25), 90)/100)
	}
	fmt.Fprintf(&b, ` style="%s"`, style)
	return b.String()
}

var attrWord = regexp.MustCompile(`^[a-z0-9]+$`)

// themeAccents are appearance.js's THEMES' accents: the tab's colour when it
// is marked but no accent is picked.
var themeAccents = map[string]color.RGBA{
	"dark":     {0x5b, 0x9d, 0xff, 0xff},
	"graphite": {0xa7, 0x8b, 0xfa, 0xff},
	"obsidian": {0x2d, 0xd4, 0xbf, 0xff},
	"ember":    {0xf2, 0xa6, 0x5a, 0xff},
	"light":    {0x09, 0x69, 0xda, 0xff},
}

// hexColor reads "#rrggbb" or "rrggbb".
func hexColor(s string) (color.RGBA, bool) {
	s = strings.TrimPrefix(s, "#")
	var c color.RGBA
	if len(s) != 6 {
		return c, false
	}
	if _, err := fmt.Sscanf(s, "%02x%02x%02x", &c.R, &c.G, &c.B); err != nil {
		return c, false
	}
	c.A = 0xff
	return c, true
}

func hexOf(c color.RGBA) string { return fmt.Sprintf("%02x%02x%02x", c.R, c.G, c.B) }

// markColor is an icon address's colour: by hex, or by the name an app
// installed before the accent was given.
func markColor(v string) (color.RGBA, bool) {
	if c, ok := ringColors[v]; ok {
		return c, true
	}
	return hexColor(v)
}

// HandleWorker serves the service worker, revalidated each time so a new dv's
// replaces it.
func HandleWorker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(worker())
}

// worker is the service worker with its notifications' pictures in it. As
// addresses they would be fetched as each shows, past whatever guards dv: a
// login Cloudflare Access may have let lapse by the time a phone is woken,
// which leaves Android's own generic icon in their place.
var worker = sync.OnceValue(func() []byte {
	png := func(name string) string { return "data:image/png;base64," + base64.StdEncoding.EncodeToString(icons()[name]) }
	return fmt.Appendf(nil, "const ICON = %q;\nconst BADGE = %q;\n\n%s", png("192.png"), png("badge.png"), serviceWorker)
})

// HandleIcon serves /icon/{name}: 192.png, 512.png, maskable-512.png, and
// badge.png, the white glyph Android shows in the status bar, by its alpha.
func HandleIcon(w http.ResponseWriter, r *http.Request) {
	set := icons()
	q := r.URL.Query()
	if c, ok := markColor(q.Get("ring")); ok {
		set = markedIcons(c, false)
	} else if c, ok := markColor(q.Get("fill")); ok {
		set = markedIcons(c, true)
	}
	b, ok := set[r.PathValue("name")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(b)
}

// The icons are the tab's (index.html's SVG, in its 32×32), drawn again.
var (
	frameEdge = color.RGBA{0x3e, 0x3e, 0x3e, 0xff}
	frameFill = color.RGBA{0x14, 0x14, 0x14, 0xff}
	white     = color.RGBA{0xff, 0xff, 0xff, 0xff}
)

// frame is the rounded square: the SVG's 2-wide stroke, centred on its rect
// (1, 1, 31, 31, rx 7), is the band between these two.
var frame = []shape{
	{0, 0, 32, 32, 8, frameEdge},
	{2, 2, 30, 30, 6, frameFill},
}

// glyph is the divider and the six lines either side of it.
var glyph = []shape{
	{14, 2, 18, 30, 0, color.RGBA{0x2c, 0x2c, 0x2c, 0xff}},
	{6, 8, 12, 12, 0, color.RGBA{0x6a, 0x6a, 0x6a, 0xff}},
	{20, 8, 26, 12, 0, color.RGBA{0x6a, 0x6a, 0x6a, 0xff}},
	{6, 14, 12, 18, 0, color.RGBA{0xf8, 0x51, 0x49, 0xff}},
	{20, 14, 26, 18, 0, color.RGBA{0x3f, 0xb9, 0x50, 0xff}},
	{6, 20, 12, 24, 0, color.RGBA{0x6a, 0x6a, 0x6a, 0xff}},
	{20, 20, 26, 24, 0, color.RGBA{0x6a, 0x6a, 0x6a, 0xff}},
}

var icons = sync.OnceValue(func() map[string][]byte {
	whole := append(append([]shape{}, frame...), glyph...)
	blank := make([]shape, len(glyph))
	for i, s := range glyph {
		s.c = white
		blank[i] = s
	}
	// A launcher's mask may cut all but a circle 80% across: the glyph, 28
	// high, keeps to the middle 60%.
	maskScale := 512 * 0.6 / 28
	return map[string][]byte{
		"192.png":          encode(draw(192, 6, 0, color.RGBA{}, whole)),
		"512.png":          encode(draw(512, 16, 0, color.RGBA{}, whole)),
		"maskable-512.png": encode(draw(512, maskScale, 256-16*maskScale, frameFill, glyph)),
		"badge.png":        encode(draw(96, 3, 0, color.RGBA{}, blank)),
	}
})

// ringColors are favicon.js's TAB_COLORS, the tab's colours by name before
// it took the accent.
var ringColors = map[string]color.RGBA{
	"blue":   {0x2f, 0x81, 0xf7, 0xff},
	"purple": {0x89, 0x57, 0xe5, 0xff},
	"pink":   {0xdb, 0x61, 0xa2, 0xff},
	"orange": {0xe0, 0x82, 0x3d, 0xff},
	"yellow": {0xd4, 0xa7, 0x2c, 0xff},
	"teal":   {0x1f, 0x9e, 0x9e, 0xff},
}

// The icons drawn in a colour, kept by colour and style. Any colour can be
// asked for, so only a few are kept.
var (
	markedMu sync.Mutex
	marked   = map[markKey]map[string][]byte{}
)

type markKey struct {
	c      color.RGBA
	filled bool
}

// The tile's greys, on a filled tile c darkened by these, as favicon.js's.
const dividerShade, lineShade = 0.2, 0.32

// markedIcons are the app's icons marked in c as the tab's is. Ringed: the
// SVG's 3.5-wide stroke over the frame, and on the maskable one a circle
// inside what a launcher keeps. Filled: the tile itself c, its glyph's greys
// darkened from it.
func markedIcons(c color.RGBA, filled bool) map[string][]byte {
	key := markKey{c, filled}
	markedMu.Lock()
	set, ok := marked[key]
	markedMu.Unlock()
	if ok {
		return set
	}
	maskScale := 512 * 0.6 / 28
	var whole, mask []shape
	maskBg := frameFill
	if filled {
		g := append([]shape{}, glyph...)
		g[0].c = shade(c, dividerShade)
		for i := range g {
			if g[i].c == glyph[1].c {
				g[i].c = shade(c, lineShade)
			}
		}
		whole = append([]shape{{0, 0, 32, 32, 8, c}}, g...)
		mask, maskBg = g, c
	} else {
		inner := append([]shape{}, glyph...)
		inner[0].y0, inner[0].y1 = 3.5, 28.5
		whole = append([]shape{{0, 0, 32, 32, 8.75, c}, {3.5, 3.5, 28.5, 28.5, 5.25, frameFill}}, inner...)
		const R, band = 18.6, 1.4
		mask = append([]shape{{16 - R, 16 - R, 16 + R, 16 + R, R, c}, {16 - R + band, 16 - R + band, 16 + R - band, 16 + R - band, R - band, frameFill}}, glyph...)
	}
	set = map[string][]byte{
		"192.png":          encode(draw(192, 6, 0, color.RGBA{}, whole)),
		"512.png":          encode(draw(512, 16, 0, color.RGBA{}, whole)),
		"maskable-512.png": encode(draw(512, maskScale, 256-16*maskScale, maskBg, mask)),
		"badge.png":        icons()["badge.png"],
	}
	markedMu.Lock()
	if len(marked) >= 8 {
		clear(marked)
	}
	marked[key] = set
	markedMu.Unlock()
	return set
}

func shade(c color.RGBA, k float64) color.RGBA {
	f := func(v uint8) uint8 { return uint8(math.Round(float64(v) * (1 - k))) }
	return color.RGBA{f(c.R), f(c.G), f(c.B), 0xff}
}

// A shape is a rectangle, its corners rounded by r, of one opaque colour.
type shape struct {
	x0, y0, x1, y1, r float64
	c                 color.RGBA
}

func (s shape) has(x, y float64) bool {
	if x < s.x0 || x >= s.x1 || y < s.y0 || y >= s.y1 {
		return false
	}
	// Near a corner, the point is inside only within r of the corner's centre.
	dx := x - min(max(x, s.x0+s.r), s.x1-s.r)
	dy := y - min(max(y, s.y0+s.r), s.y1-s.r)
	return dx*dx+dy*dy <= s.r*s.r
}

// draw paints shapes, later over earlier, on size×size pixels over bg; a
// shape's (x, y) is at (x, y)*scale + shift. Each pixel is the mean of a 4×4
// of samples, for smooth edges.
func draw(size int, scale, shift float64, bg color.RGBA, shapes []shape) *image.RGBA {
	const n = 4
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for py := range size {
		for px := range size {
			var r, g, b, a int
			for sy := range n {
				for sx := range n {
					x := (float64(px) + (float64(sx)+0.5)/n - shift) / scale
					y := (float64(py) + (float64(sy)+0.5)/n - shift) / scale
					c := bg
					for i := len(shapes) - 1; i >= 0; i-- {
						if shapes[i].has(x, y) {
							c = shapes[i].c
							break
						}
					}
					r, g, b, a = r+int(c.R), g+int(c.G), b+int(c.B), a+int(c.A)
				}
			}
			mean := func(v int) uint8 { return uint8((v + n*n/2) / (n * n)) }
			img.SetRGBA(px, py, color.RGBA{mean(r), mean(g), mean(b), mean(a)})
		}
	}
	return img
}

func encode(img image.Image) []byte {
	var buf bytes.Buffer
	(&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buf, img)
	return buf.Bytes()
}
