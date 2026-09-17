package hub

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"dv/internal/server"
)

// activityTick is how often the stream looks at the folders, as often as a
// folder's own page looks at its sessions: busy and idle are only in a
// session's record, and a turn between two looks is never seen at all.
const activityTick = time.Second

// handleActivity streams what the sessions of every folder open here are
// doing, so a page can raise notices for the folders it is not on: the hub's
// own page for all of them, a folder's page for the others.
func (h *Hub) handleActivity(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	type folderLive struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
		server.Live
	}
	tick := time.NewTicker(activityTick)
	defer tick.Stop()
	sent := ""
	for {
		folders := []folderLive{}
		for _, f := range h.folders.List() {
			if rv := h.opened(f.Slug); rv != nil {
				folders = append(folders, folderLive{f.Slug, displayName(f), rv.srv.Live()})
			}
		}
		b, err := json.Marshal(map[string]any{"folders": folders})
		if err != nil {
			return
		}
		if string(b) != sent {
			sent = string(b)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
		select {
		case <-tick.C:
		case <-r.Context().Done():
			return
		}
	}
}
