package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"dv/internal/agent"
)

// choice is one entry in a picker.
type choice struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// The modes Shift+Tab cycles through in the terminal, in its order. Codex
// sessions take the same four, made of its sandbox and approval settings.
var agentModes = []choice{{"default", "Ask before edits"}, {"acceptEdits", "Accept edits"}, {"plan", "Plan"}, {"auto", "Auto"}}

var sessionID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// session reads the path's session id, or writes the error.
func session(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("id")
	if !sessionID.MatchString(id) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("%q is not a session id", id))
		return "", false
	}
	return id, true
}

func (s *Server) handleAgentSessions(w http.ResponseWriter, r *http.Request) {
	opts := s.agent.Options()
	// Asked with the sessions so the page hears of hooks put in from a terminal.
	hooks, _ := claudeHooks()
	out := map[string]any{
		"available": agent.Available(),
		"hooks":     hooks,
		"sessions":  s.agent.Sessions(),
		"models":    opts.Models,
		"modes":     agentModes,
		"mode":      opts.Mode,
		"usage":     s.agent.Usage(),
	}
	if agent.CodexAvailable() {
		c := s.agent.CodexOptions()
		out["codex"] = map[string]any{"available": true, "models": c.Models, "mode": c.Mode, "usage": s.agent.CodexUsage()}
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAgentCommands is apart from the session list, which is asked for every
// few seconds; the commands run to tens of kilobytes and seldom change.
func (s *Server) handleAgentCommands(w http.ResponseWriter, r *http.Request) {
	opts := s.agent.Options()
	if r.URL.Query().Get("agent") == "codex" {
		opts = s.agent.CodexOptions()
	}
	writeJSON(w, http.StatusOK, map[string]any{"commands": opts.Commands})
}

func (s *Server) handleAgentCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Agent string `json:"agent"`
		Ask   bool   `json:"ask"` // the Ask panel's, never listed
	}
	json.NewDecoder(r.Body).Decode(&req)
	if req.Agent != "" && req.Agent != "codex" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("dv runs Claude Code and Codex, not %q", req.Agent))
		return
	}
	create := s.agent.Create
	if req.Ask {
		create = s.agent.CreateAsk
	}
	id, err := create(req.Agent)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (s *Server) handleAgentOpen(w http.ResponseWriter, r *http.Request) {
	id, ok := session(w, r)
	if !ok {
		return
	}
	var req struct {
		Open bool `json:"open"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.agent.SetOpen(id, req.Open); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAgentStart(w http.ResponseWriter, r *http.Request) {
	id, ok := session(w, r)
	if !ok {
		return
	}
	if err := s.agent.Start(id); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAgentAsk(w http.ResponseWriter, r *http.Request) {
	id, ok := session(w, r)
	if !ok {
		return
	}
	var req struct {
		Ask bool `json:"ask"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.agent.SetAsk(id, req.Ask); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAgentTemporary(w http.ResponseWriter, r *http.Request) {
	id, ok := session(w, r)
	if !ok {
		return
	}
	var req struct {
		Temporary bool `json:"temporary"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.agent.SetTemporary(id, req.Temporary); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAgentKeep(w http.ResponseWriter, r *http.Request) {
	id, ok := session(w, r)
	if !ok {
		return
	}
	var req struct {
		Keep bool `json:"keep"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.agent.SetKept(id, req.Keep); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// StartKept starts the sessions kept running, once this dv is where Claude
// Code's prompts go.
func (s *Server) StartKept() { s.agent.StartKept() }

// handleAgentEvents streams a session to the page: the conversation as its
// transcript grows, and what the session is doing now.
func (s *Server) handleAgentEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := session(w, r)
	if !ok {
		return
	}
	sub, stop := s.agent.Follow(id, r.URL.Query().Get("from"))
	defer stop()
	streamUpdates(w, r, sub, func() agent.Update { return s.agent.Update(id, sub) })
}

// handleAgentSubagentEvents streams the conversation of an agent one of a
// session's calls started.
func (s *Server) handleAgentSubagentEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := session(w, r)
	if !ok {
		return
	}
	call := r.PathValue("tool")
	sub, stop := s.agent.FollowAgent(id, call)
	defer stop()
	streamUpdates(w, r, sub, func() agent.Update { return s.agent.AgentUpdate(id, call, sub) })
}

func streamUpdates(w http.ResponseWriter, r *http.Request, sub *agent.Sub, update func() agent.Update) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	for {
		select {
		case <-sub.Changed:
		case <-r.Context().Done():
			return
		}
		// A reply streams in many small pieces; one update for a few of them
		// is plenty for the eye.
		time.Sleep(40 * time.Millisecond)
		b, err := json.Marshal(update())
		if err != nil {
			return
		}
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
}

// taskTail is as much of a background call's output as is sent at once: the
// end, which is what is being watched.
const taskTail = 256 << 10

// handleAgentTaskOutput streams what a call left running in the background has
// written: the end of it so far, then what it adds as it goes.
func (s *Server) handleAgentTaskOutput(w http.ResponseWriter, r *http.Request) {
	id, ok := session(w, r)
	if !ok {
		return
	}
	path, err := s.agent.TaskOutput(id, r.PathValue("tool"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("streaming unsupported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	send := func(v any) {
		b, _ := json.Marshal(v)
		fmt.Fprintf(w, "data: %s\n\n", b)
		flusher.Flush()
	}
	at, gone := int64(-1), false
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		if fi, err := os.Stat(path); err != nil {
			// Claude Code clears a session's task files away in time.
			if !gone {
				send(map[string]bool{"gone": true})
				gone = true
			}
		} else if size := fi.Size(); size != at {
			reset := at < 0 || size < at
			from := at
			if reset || size-at > taskTail {
				from = max(0, size-taskTail)
			}
			text, end := readText(path, from, size)
			send(map[string]any{"text": text, "reset": reset || from != at, "cut": from > 0 && from != at})
			at, gone = end, false
		}
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
		}
	}
}

// readText reads a file from one byte to another, as whole characters: it
// starts after a partial one and stops before one not finished writing,
// reporting where it stopped.
func readText(path string, from, to int64) (string, int64) {
	f, err := os.Open(path)
	if err != nil {
		return "", from
	}
	defer f.Close()
	b := make([]byte, to-from)
	n, _ := f.ReadAt(b, from)
	b = b[:n]
	start := 0
	for from > 0 && start < len(b) && start < utf8.UTFMax && !utf8.RuneStart(b[start]) {
		start++
	}
	end := len(b)
	for i := len(b) - 1; i >= max(start, len(b)-utf8.UTFMax); i-- {
		if utf8.RuneStart(b[i]) {
			if !utf8.FullRune(b[i:]) {
				end = i
			}
			break
		}
	}
	return string(b[start:end]), from + int64(end)
}

func (s *Server) handleAgentSend(w http.ResponseWriter, r *http.Request) {
	id, ok := session(w, r)
	if !ok {
		return
	}
	var req struct {
		Text   string `json:"text"`
		UUID   string `json:"uuid"`
		Images []struct {
			MediaType string `json:"mediaType"`
			Data      []byte `json:"data"`
		} `json:"images"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Text) == "" && len(req.Images) == 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("the message is empty"))
		return
	}
	var images []agent.Image
	for _, img := range req.Images {
		image := agent.Image{MediaType: img.MediaType, Data: img.Data}
		if err := image.Check(); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		images = append(images, image)
	}
	if req.UUID != "" && !sessionID.MatchString(req.UUID) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("%q is not a uuid", req.UUID))
		return
	}
	uuid, err := s.sendVia(id, req.UUID, req.Text, images, "")
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "uuid": uuid})
}

// handleAgentShell runs a command typed after !, whose output then goes to the
// agent as the message uuid.
func (s *Server) handleAgentShell(w http.ResponseWriter, r *http.Request) {
	id, ok := session(w, r)
	if !ok {
		return
	}
	var req struct {
		Command string `json:"command"`
		UUID    string `json:"uuid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.UUID != "" && !sessionID.MatchString(req.UUID) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("%q is not a uuid", req.UUID))
		return
	}
	if err := s.agent.Shell(id, req.UUID, req.Command); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleAgentUnqueue takes back a message sent while Claude works that it has
// not taken up yet.
func (s *Server) handleAgentUnqueue(w http.ResponseWriter, r *http.Request) {
	id, ok := session(w, r)
	if !ok {
		return
	}
	cancelled, err := s.agent.Unqueue(id, r.PathValue("message"))
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"cancelled": cancelled})
}

func (s *Server) handleAgentInterrupt(w http.ResponseWriter, r *http.Request) {
	id, ok := session(w, r)
	if !ok {
		return
	}
	if err := s.agent.Interrupt(id); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAgentSettings(w http.ResponseWriter, r *http.Request) {
	id, ok := session(w, r)
	if !ok {
		return
	}
	var req struct {
		Model  *string `json:"model"`
		Mode   *string `json:"mode"`
		Effort *string `json:"effort"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	models := s.agent.OptionsFor(id).Models
	// Any model's effort levels, since the model may change in the same breath.
	efforts := map[string]bool{}
	for _, m := range models {
		for _, e := range m.Efforts {
			efforts[e] = true
		}
	}
	if req.Model != nil && !slices.ContainsFunc(models, func(m agent.Model) bool { return m.ID == *req.Model }) ||
		req.Mode != nil && !slices.ContainsFunc(agentModes, func(c choice) bool { return c.ID == *req.Mode }) ||
		req.Effort != nil && !efforts[*req.Effort] {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("that model, mode or effort is not one dv offers"))
		return
	}
	if err := s.agent.Configure(id, req.Model, req.Mode, req.Effort); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAgentRename(w http.ResponseWriter, r *http.Request) {
	id, ok := session(w, r)
	if !ok {
		return
	}
	var req struct {
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.agent.Rename(id, req.Title); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAgentPrompts(w http.ResponseWriter, r *http.Request) {
	id, ok := session(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"prompts": s.agent.Prompts(id)})
}

func (s *Server) handleAgentRewind(w http.ResponseWriter, r *http.Request) {
	id, ok := session(w, r)
	if !ok {
		return
	}
	var req struct {
		Prompt       string `json:"prompt"`
		Before       string `json:"before"`
		Conversation bool   `json:"conversation"`
		Code         bool   `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	next, err := s.agent.Rewind(id, req.Prompt, req.Before, req.Conversation, req.Code)
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": next})
}

func (s *Server) handleAgentEdit(w http.ResponseWriter, r *http.Request) {
	id, ok := session(w, r)
	if !ok {
		return
	}
	ed, err := s.agent.Edit(id, r.PathValue("tool"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, ed)
}

func (s *Server) handleAgentOutput(w http.ResponseWriter, r *http.Request) {
	id, ok := session(w, r)
	if !ok {
		return
	}
	out, err := s.agent.Output(id, r.PathValue("tool"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAgentImage(w http.ResponseWriter, r *http.Request) {
	id, ok := session(w, r)
	if !ok {
		return
	}
	var img *agent.Image
	var err error
	if message := r.PathValue("message"); message != "" {
		n, _ := strconv.Atoi(r.PathValue("n"))
		img, err = s.agent.PromptImage(id, message, n)
	} else {
		img, err = s.agent.Image(id, r.PathValue("tool"))
	}
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	w.Header().Set("Content-Type", img.MediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	// A result never changes once it is written.
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Write(img.Data)
}
