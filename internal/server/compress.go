package server

import (
	"net/http"
	"strings"

	"github.com/klauspost/compress/gzhttp"
)

// Compress gzips what dv sends where the browser takes it: the bundles, the
// API's JSON and the event streams, whose updates repeat much of the one
// before and so cost little more than what changed. A WebSocket is left to
// its own compression, and pictures, video, fonts and PDFs are already
// compressed. Gzip rather than zstd, which fewer proxies and tunnels in front
// of dv know what to do with.
func Compress(h http.Handler) http.Handler {
	wrap, err := gzhttp.NewWrapper(gzhttp.EnableZstd(false), gzhttp.ContentTypeFilter(compressible))
	if err != nil {
		panic(err)
	}
	gz := wrap(h)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Upgrade") != "" {
			h.ServeHTTP(w, r)
			return
		}
		gz.ServeHTTP(w, r)
	})
}

func compressible(ct string) bool {
	ct = strings.ToLower(ct)
	switch {
	case strings.HasPrefix(ct, "image/svg"):
		return true
	case strings.HasPrefix(ct, "image/"), strings.HasPrefix(ct, "font/woff"), strings.HasPrefix(ct, "application/pdf"):
		return false
	}
	return gzhttp.DefaultContentTypeFilter(ct)
}
