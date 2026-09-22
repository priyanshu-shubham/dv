package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"dv/internal/agent"
)

// An action is what the reader keeps in dv to run in a click: a prompt, sent
// to the session they are in or to a new one set up as the action says, or a
// command. The page keeps the actions, among its settings, and sends the one
// run.
type actionRun struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Kind    string `json:"kind"` // "command" runs Command; otherwise Prompt is sent
	Command string `json:"command"`
	Prompt  string `json:"prompt"`
	Session string `json:"session"` // the session to send it to; "" starts one
	// A new session's.
	Agent     string `json:"agent"` // "codex", or "" for Claude Code
	Model     string `json:"model"`
	Effort    string `json:"effort"`
	Mode      string `json:"mode"`
	Temporary bool   `json:"temporary"`
	Close     bool   `json:"close"` // once its turn is done, unless it failed
}

// ranAction is what is kept of an action until its session's turn is done,
// for the notice of it and what comes after.
type ranAction struct {
	name  string
	close bool
}

// handleRunAction starts an action's command, or sends its prompt: { session },
// the one it went to.
func (s *Server) handleRunAction(w http.ResponseWriter, r *http.Request) {
	var a actionRun
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if a.Kind == "command" {
		if a.Command = strings.TrimSpace(a.Command); a.Command == "" {
			writeErr(w, http.StatusBadRequest, errors.New("the action has no command"))
		} else if err := s.runCommand(a); err != nil {
			writeErr(w, http.StatusConflict, err)
		} else {
			writeJSON(w, http.StatusOK, map[string]string{})
		}
		return
	}
	if a.Prompt = strings.TrimSpace(a.Prompt); a.Prompt == "" {
		writeErr(w, http.StatusBadRequest, errors.New("the action has no prompt"))
		return
	}
	id := a.Session
	if id != "" && !sessionID.MatchString(id) {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("%q is not a session id", id))
		return
	}
	if id == "" {
		var err error
		if id, err = s.startFor(a); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	s.actionsMu.Lock()
	if s.actions == nil {
		s.actions = map[string]ranAction{}
	}
	s.actions[id] = ranAction{name: a.Name, close: a.Close && a.Session == ""}
	s.actionsMu.Unlock()
	if _, err := s.sendVia(id, "", a.Prompt, nil, ""); err != nil {
		s.takeAction(id)
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"session": id})
}

// startFor makes the session an action runs in, as it says: a model, effort
// or mode that is not on offer is left to the agent's own.
func (s *Server) startFor(a actionRun) (string, error) {
	switch {
	case a.Agent == "codex" && !agent.CodexAvailable():
		return "", errors.New("Codex is not installed here")
	case a.Agent == "" && !agent.Available():
		return "", errors.New("Claude Code is not installed here")
	case a.Agent != "" && a.Agent != "codex":
		return "", fmt.Errorf("dv runs Claude Code and Codex, not %q", a.Agent)
	}
	id, err := s.agent.Create(a.Agent)
	if err != nil {
		return "", err
	}
	var mode *string
	model, effort := s.picks(id, a.Model, a.Effort)
	if slices.ContainsFunc(agentModes, func(c choice) bool { return c.ID == a.Mode }) {
		mode = &a.Mode
	}
	if model != nil || mode != nil || effort != nil {
		if err := s.agent.Configure(id, model, mode, effort); err != nil {
			return "", err
		}
	}
	if a.Temporary {
		if err := s.agent.SetTemporary(id, true); err != nil {
			return "", err
		}
	}
	return id, nil
}

// picks is the model and effort a new session takes of those asked for, nil
// for one not on offer. What Claude Code offers is only known once it has
// been asked, which a server just started has not; until then the picks,
// made from that list in the page, are trusted.
func (s *Server) picks(session, model, effort string) (*string, *string) {
	models := s.agent.OptionsFor(session).Models
	known := len(models) > 0
	i := slices.IndexFunc(models, func(m agent.Model) bool { return m.ID == model })
	var m, e *string
	if model != "" && (i >= 0 || !known) {
		m = &model
	}
	if effort != "" && (i >= 0 && slices.Contains(models[i].Efforts, effort) || !known) {
		e = &effort
	}
	return m, e
}

// takeAction is the action a session's turn was for, which it forgets.
func (s *Server) takeAction(session string) (ranAction, bool) {
	s.actionsMu.Lock()
	defer s.actionsMu.Unlock()
	a, ok := s.actions[session]
	delete(s.actions, session)
	return a, ok
}
