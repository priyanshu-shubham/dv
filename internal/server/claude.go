package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

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
// it changes, and what the open sessions are doing, each time that does. A
// stream rather than a poll, because the page has to hear of a request, or of
// Claude finishing, while its tab is hidden - the usual case, with the reader
// in the terminal - and polling stops then. Only sessions open in the Agent
// view, or run by dv, are told of: the rest are the terminal's business.
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
	// A terminal's busy and idle are only in its process record, so they are
	// looked at on a tick; each part goes out only when it moved.
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	sent := map[string]string{}
	send := func(key string, v any) bool {
		b, err := json.Marshal(map[string]any{key: v})
		if err != nil {
			return false
		}
		if sent[key] != string(b) {
			sent[key] = string(b)
			fmt.Fprintf(w, "data: %s\n\n", b)
			flusher.Flush()
		}
		return true
	}
	for {
		if !send("requests", s.Waiting()) || !send("sessions", s.agent.Activity()) {
			return
		}
		select {
		case <-changed:
		case <-tick.C:
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
	id := r.PathValue("id")
	if s.notices != nil {
		s.notices.Outcome(id, "Answered in dv")
	}
	if !s.permit.Answer(id, a) {
		if s.notices != nil {
			s.notices.Outcome(id, "")
		}
		writeErr(w, http.StatusConflict, fmt.Errorf("Claude is no longer waiting on this; it was answered in the terminal"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// hooks is whether Claude Code's hooks reach a dv, which terminal sessions'
// prompts need, and the settings file they go in.
type hooks struct {
	On   bool   `json:"on"`
	Path string `json:"path"`
}

func claudeHooks() (hooks, error) {
	path, err := permit.SettingsPath()
	if err != nil {
		return hooks{}, err
	}
	shown := path
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home+string(filepath.Separator)) {
		shown = "~" + path[len(home):]
	}
	exe, err := permit.Installed(path)
	if err != nil {
		return hooks{Path: shown}, err
	}
	_, err = os.Stat(exe)
	return hooks{On: exe != "" && err == nil, Path: shown}, nil
}

func (s *Server) handleClaudeHooks(w http.ResponseWriter, r *http.Request) {
	h, err := claudeHooks()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, h)
}

func (s *Server) handleClaudeSetHooks(w http.ResponseWriter, r *http.Request) {
	var req struct {
		On bool `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := setHooks(req.On); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.handleClaudeHooks(w, r)
}

func setHooks(on bool) error {
	path, err := permit.SettingsPath()
	if err != nil {
		return err
	}
	if !on {
		_, err = permit.Uninstall(path)
		return err
	}
	exe, err := permit.Exe()
	if err != nil {
		return err
	}
	_, err = permit.Install(path, exe)
	return err
}

// Guarded keeps other web pages away from what can let Claude run a command.
// A cross-origin POST can only carry JSON, or raw bytes as an upload does,
// after a preflight, which dv never grants, and a DNS rebinding attack arrives
// under a host name rather than the address dv is reached at.
func Guarded(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.Host)
		if err != nil {
			host = r.Host
		}
		if host != "localhost" && net.ParseIP(strings.Trim(host, "[]")) == nil {
			writeErr(w, http.StatusForbidden, fmt.Errorf("dv answers on an address or localhost, not %q", r.Host))
			return
		}
		if ct := r.Header.Get("Content-Type"); r.Method == http.MethodPost && !strings.HasPrefix(ct, "application/json") && ct != "application/octet-stream" {
			writeErr(w, http.StatusUnsupportedMediaType, fmt.Errorf("expected application/json"))
			return
		}
		h(w, r)
	}
}
