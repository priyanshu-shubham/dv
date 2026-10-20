package server

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"sync"
)

// What a browser installs dv as an app from: the manifest, its icons, and the
// service worker that shows the notices pushed to the device. None holds
// anything of the reader's, so none is Guarded; each is the process's, a
// hub's at its root, as the app is the whole hub.

//go:embed sw.js
var serviceWorker []byte

const manifest = `{"id":"/","name":"dv","short_name":"dv","start_url":"/","scope":"/","display":"standalone","background_color":"#131314","theme_color":"#131314","icons":[` +
	`{"src":"/icon/192.png","sizes":"192x192","type":"image/png","purpose":"any"},` +
	`{"src":"/icon/512.png","sizes":"512x512","type":"image/png","purpose":"any"},` +
	`{"src":"/icon/maskable-512.png","sizes":"512x512","type":"image/png","purpose":"maskable"}]}`

func HandleManifest(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "no-cache")
	io.WriteString(w, manifest)
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
	b, ok := icons()[r.PathValue("name")]
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
