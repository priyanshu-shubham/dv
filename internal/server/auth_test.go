package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"dv/internal/store"
)

func TestRequireKey(t *testing.T) {
	h := RequireKey(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}), "open sesame", "local")
	for _, c := range []struct {
		name, user, password, local string
		want                        int
	}{
		{"nothing", "", "", "", 401},
		{"a wrong key", "dv", "open", "", 401},
		{"the key", "anyone", "open sesame", "", 200},
		{"dv's own", "", "", "local", 200},
		{"a wrong token", "", "", "loca", 401},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		if c.user != "" || c.password != "" {
			r.SetBasicAuth(c.user, c.password)
		}
		if c.local != "" {
			r.Header.Set(store.LocalHeader, c.local)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != c.want || c.want == 401 && w.Header().Get("WWW-Authenticate") == "" {
			t.Errorf("%s: %d %v", c.name, w.Code, w.Header())
		}
	}
	// No token made, none is let through for want of one.
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	RequireKey(h, "key", "").ServeHTTP(w, r)
	if w.Code != 401 {
		t.Errorf("an empty token: %d", w.Code)
	}
}
