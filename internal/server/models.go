package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"dv/internal/agent"
)

// addedModels is the user pref of that name: the models the user put on the
// picker's list, by agent, each as /model takes it.
type addedModels map[string][]string

// modelVersion is what is left off a model to name its kind: claude-opus-4-1 is an Opus.
var modelVersion = regexp.MustCompile(`-\d.*$`)

// withAdded is what a session of kind's can be put on: what its agent offers,
// then the models the user added. An added one takes the effort levels of the
// offered model of its kind, as the page does for a session moved to it with
// /model. Until the agent has said what it offers, nothing is added either.
func (s *Server) withAdded(kind string, offered []agent.Model) []agent.Model {
	if len(offered) == 0 {
		return offered
	}
	var added addedModels
	json.Unmarshal(s.user.All()["addedModels"], &added)
	// Clipped, so appending never writes into the agent's own list.
	out := slices.Clip(offered)
	for _, id := range added[kind] {
		if slices.ContainsFunc(out, func(m agent.Model) bool { return m.ID == id || m.Model == id }) {
			continue
		}
		m := agent.Model{ID: id, Model: id, Label: id, Added: true}
		if i := slices.IndexFunc(offered, func(o agent.Model) bool {
			return o.Model != "" && modelVersion.ReplaceAllString(o.Model, "") == modelVersion.ReplaceAllString(id, "")
		}); i >= 0 {
			m.Efforts = offered[i].Efforts
		}
		out = append(out, m)
	}
	return out
}

func (s *Server) modelsFor(session string) []agent.Model {
	return s.withAdded(s.agent.AgentOf(session), s.agent.OptionsFor(session).Models)
}

// handleAgentModels puts a model on an agent's list, or takes one the user put
// there off it: { agent, model, add }.
func (s *Server) handleAgentModels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Agent string `json:"agent"`
		Model string `json:"model"`
		Add   bool   `json:"add"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	id := strings.TrimSpace(req.Model)
	if req.Agent != "" && req.Agent != "codex" || id == "" || len(id) > 200 || strings.ContainsFunc(id, unicode.IsSpace) {
		writeErr(w, http.StatusBadRequest, errors.New("a model is named in one word, as /model takes it"))
		return
	}
	var added addedModels
	json.Unmarshal(s.user.All()["addedModels"], &added)
	if added == nil {
		added = addedModels{}
	}
	list := slices.DeleteFunc(added[req.Agent], func(m string) bool { return m == id })
	if req.Add {
		list = append(list, id)
	}
	added[req.Agent] = list
	if len(list) == 0 {
		delete(added, req.Agent)
	}
	b := []byte("null")
	if len(added) > 0 {
		b, _ = json.Marshal(added)
	}
	if err := s.user.Set("addedModels", b, nil); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
