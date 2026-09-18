package notify

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// keepAlive is how often a quiet stream says something anyway: a proxy in
// front of dv drops a response that has been silent for long.
const keepAlive = 30 * time.Second

// HandleStream streams the notices open to a page, ?page=<id>, the whole list
// each time it changes. The page's reader counts as present while it is open.
func (c *Center) HandleStream(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("page")
	flusher, ok := w.(http.Flusher)
	if id == "" || len(id) > 64 || !ok {
		http.Error(w, "a page id is required", http.StatusBadRequest)
		return
	}
	changed, stop := c.Watch(id)
	defer stop()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	quiet := time.NewTicker(keepAlive)
	defer quiet.Stop()
	sent := ""
	for {
		b, err := json.Marshal(map[string]any{"notices": c.Notices()})
		if err != nil {
			return
		}
		if string(b) != sent {
			sent = string(b)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
		select {
		case <-changed:
		case <-quiet.C:
			fmt.Fprint(w, ": \n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

// HandlePresence takes a page's report: { page, focused, input, ago }, ago in
// milliseconds since the reader last used it, when input is set.
func (c *Center) HandlePresence(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Page    string `json:"page"`
		Focused bool   `json:"focused"`
		Input   bool   `json:"input"`
		Ago     int64  `json:"ago"`
	}
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil || p.Page == "" || len(p.Page) > 64 {
		http.Error(w, "expected { page, focused, input, ago }", http.StatusBadRequest)
		return
	}
	c.Report(p.Page, p.Focused, p.Input, time.Duration(max(0, p.Ago))*time.Millisecond)
	w.WriteHeader(http.StatusNoContent)
}
