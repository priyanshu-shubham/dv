package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"dv/internal/agent"
	"dv/internal/gateway"
)

// addedModels is the user pref of that name: the models the user put on the
// picker's list, each as /model takes it, under "" as when dv ran more than
// one agent.
type addedModels map[string][]string

// modelVersion is what is left off a model to name its kind: claude-opus-4-1 is an Opus.
var modelVersion = regexp.MustCompile(`-\d.*$`)

// withAdded is what a session can be put on: what Claude Code offers, the
// models the user added, then those dv's gateway serves, and Antigravity's,
// which agy runs in a session of its own. An added one takes the
// effort levels of the offered model of its kind, as the page does for a
// session moved to it with /model. Until Claude Code has said what it
// offers, nothing is added either.
func (s *Server) withAdded(offered []agent.Model) []agent.Model {
	if len(offered) == 0 {
		return offered
	}
	var added addedModels
	json.Unmarshal(s.user.All()["addedModels"], &added)
	// Clipped, so appending never writes into the agent's own list.
	out := slices.Clip(offered)
	for _, id := range added[""] {
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
	out = append(out, gatewayModels(out)...)
	for _, m := range agent.AgyModels() {
		if !slices.ContainsFunc(out, func(l agent.Model) bool { return l.ID == m.ID }) {
			out = append(out, m)
		}
	}
	return out
}

// gatewayModels are the models the user's Codex sign-in, Gemini route and
// providers offer a Claude Code session, through dv's gateway; one already
// on the list isn't put on twice.
func gatewayModels(listed []agent.Model) []agent.Model {
	gw := gateway.Active()
	if gw == nil {
		return nil
	}
	var out []agent.Model
	add := func(prefix, source, through string, models []gateway.Model) {
		for _, m := range models {
			id := prefix + m.Slug
			if slices.ContainsFunc(listed, func(l agent.Model) bool { return l.ID == id }) {
				continue
			}
			desc := through
			if m.Window > 0 {
				desc += fmt.Sprintf(" · %dk context", m.Window/1000)
			}
			out = append(out, agent.Model{ID: id, Model: id, Label: m.Label, Description: desc, Efforts: m.Efforts, Effort: m.Default, Source: source})
		}
	}
	gemini := "Through your Gemini API key"
	if gw.Gemini().Vertex {
		gemini = "Through Vertex AI"
	}
	add(gateway.ModelPrefix, "Codex", "Through your Codex sign-in", gw.CodexModels())
	add(gateway.GeminiPrefix, "Gemini", gemini, gw.GeminiModels())
	for _, p := range gw.ProviderModels() {
		through := "Through " + p.Name
		if p.Prefix == gateway.OpenAIPrefix {
			through = "Through your OpenAI API key"
		}
		add(p.Prefix, p.Name, through, p.Models)
	}
	return out
}

func (s *Server) modelsFor(session string) []agent.Model {
	return s.withAdded(s.agent.Options().Models)
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
	if req.Agent != "" || id == "" || len(id) > 200 || strings.ContainsFunc(id, unicode.IsSpace) {
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
