package server

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"

	"dv/internal/store"
)

// RequireKey lets through only requests that carry key as the password of
// HTTP basic auth, which a browser asks the reader for once, any user name
// going; or the local token, from dv itself.
func RequireKey(h http.Handler, key, local string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, password, _ := r.BasicAuth()
		if same(password, key) || local != "" && same(r.Header.Get(store.LocalHeader), local) {
			h.ServeHTTP(w, r)
			return
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="dv", charset="UTF-8"`)
		http.Error(w, "dv asks for its key", http.StatusUnauthorized)
	})
}

// same compares in a time that says nothing of either, their lengths included.
func same(a, b string) bool {
	x, y := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(x[:], y[:]) == 1
}
