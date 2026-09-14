package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"dv/internal/permit"
)

// handleClaudeHook is where `dv claude hook` delivers Claude Code's hook
// events. A permission prompt is held here until the reader answers it.
func (s *Server) handleClaudeHook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	out, err := s.permit.Hook(r.Context(), body)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if out == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}

// handleClaudeRequests streams the waiting requests, the whole list each time
// it changes. A stream rather than a poll, because the page has to hear of a
// request while its tab is hidden - the usual case, with the reader in the
// terminal - and polling stops then.
func (s *Server) handleClaudeRequests(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	changed, stop := s.permit.Watch()
	defer stop()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	for {
		b, err := json.Marshal(map[string]any{"requests": s.permit.Waiting()})
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
		select {
		case <-changed:
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) handleClaudeAnswer(w http.ResponseWriter, r *http.Request) {
	var a permit.Answer
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !s.permit.Answer(r.PathValue("id"), a) {
		writeErr(w, http.StatusConflict, fmt.Errorf("Claude is no longer waiting on this; it was answered in the terminal"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// guarded keeps other web pages away from what can let Claude run a command.
// A cross-origin POST can only carry JSON after a preflight, which dv never
// grants, and a DNS rebinding attack arrives under a host name rather than
// the address dv is reached at.
func guarded(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "localhost" && net.ParseIP(strings.Trim(host, "[]")) == nil {
			writeErr(w, http.StatusForbidden, fmt.Errorf("dv answers on an address or localhost, not %q", r.Host))
			return
		}
		if r.Method == http.MethodPost && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			writeErr(w, http.StatusUnsupportedMediaType, fmt.Errorf("expected application/json"))
			return
		}
		h(w, r)
	}
}
