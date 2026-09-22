package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"dv/internal/agent"
	"dv/internal/notify"
	"dv/internal/permit"
)

// talk is the folder's sessions as the reader reaches them from a provider,
// a chat app on their phone.
type talk struct{ s *Server }

func (t talk) List() []notify.Session {
	asking := map[string]bool{}
	for _, r := range t.s.Waiting() {
		asking[r.Session] = true
	}
	var out []notify.Session
	for _, a := range t.s.agent.Activity() {
		out = append(out, notify.Session{
			ID: a.ID, Title: a.Title, Agent: a.Agent, Running: a.Running,
			Busy: a.Busy, Asking: asking[a.ID], Updated: a.Updated,
		})
	}
	return out
}

func (t talk) Send(session string, m notify.Message) (string, error) {
	var with []agent.Image
	for _, img := range m.Images {
		image := agent.Image{MediaType: img.Type, Data: img.Data}
		if err := image.Check(); err != nil {
			return "", err
		}
		with = append(with, image)
	}
	text := m.Text
	for _, f := range m.Files {
		name, err := t.s.save(f.Name, bytes.NewReader(f.Data))
		if err != nil {
			return "", fmt.Errorf("Could not save %s: %w", f.Name, err)
		}
		text = withContext(text, `<file path="`+attrEscaper.Replace(name)+`" />`)
	}
	return t.s.sendVia(session, "", text, with, m.Via)
}

// attrEscaper escapes as the page does, which reads the context back.
var attrEscaper = strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;")

func (t talk) Progress(session, message string) notify.Progress {
	switch queued, busy := t.s.agent.Progress(session, message); {
	case queued:
		return notify.Queued
	case busy && slices.ContainsFunc(t.s.permit.Waiting(), func(r *permit.Request) bool { return r.Session == session }):
		return notify.Asking
	case busy:
		return notify.Working
	}
	return notify.Finished
}

// newModel is the page's pref of that name: the model and effort last picked
// for a new session, by agent.
type newModel map[string]struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

// Start begins a session as the page does: with the agent last picked for a
// new one, on the model and effort last picked for that agent's, where it
// still offers them. It starts in the mode the setting chatMode asks of one
// begun from a chat app, or else the agent's own.
func (t talk) Start(m notify.Message) (string, error) {
	prefs := t.s.user.All()
	var picked string
	json.Unmarshal(prefs["newAgent"], &picked)
	with := ""
	if agent.CodexAvailable() && (picked == "codex" || !agent.Available()) {
		with = "codex"
	}
	id, err := t.s.agent.Create(with)
	if err != nil {
		return "", err
	}
	var last newModel
	json.Unmarshal(prefs["newModel"], &last)
	want := last[with]
	var model, mode, effort *string
	models := t.s.agent.OptionsFor(id).Models
	if i := slices.IndexFunc(models, func(m agent.Model) bool { return m.ID == want.Model }); i >= 0 {
		if want.Model != "" {
			model = &want.Model
		}
		if want.Effort != "" && slices.Contains(models[i].Efforts, want.Effort) {
			effort = &want.Effort
		}
	}
	var settings struct {
		ChatMode string `json:"chatMode"`
	}
	json.Unmarshal(prefs["settings"], &settings)
	if slices.ContainsFunc(agentModes, func(c choice) bool { return c.ID == settings.ChatMode }) {
		mode = &settings.ChatMode
	}
	if model != nil || mode != nil || effort != nil {
		if err := t.s.agent.Configure(id, model, mode, effort); err != nil {
			return "", err
		}
	}
	_, err = t.Send(id, m)
	return id, err
}

func (t talk) Stop(session string) error { return t.s.agent.Interrupt(session) }

func (t talk) Reply(session string) string { return t.s.agent.Reply(session) }

func (t talk) Setup(session string) notify.Setup {
	s := notify.Setup{Agent: t.s.agent.AgentOf(session)}
	s.Model, s.Effort = t.s.agent.Picked(session)
	for _, m := range t.s.agent.OptionsFor(session).Models {
		s.Models = append(s.Models, notify.Model{ID: m.ID, Label: m.Label, Effort: m.Effort, Efforts: m.Efforts})
	}
	return s
}

func (t talk) Configure(session string, model, effort *string, asNew bool) error {
	if err := t.s.agent.Configure(session, model, nil, effort); err != nil {
		return err
	}
	if !asNew {
		return nil
	}
	var last newModel
	json.Unmarshal(t.s.user.All()["newModel"], &last)
	if last == nil {
		last = newModel{}
	}
	kind := t.s.agent.AgentOf(session)
	pick := last[kind]
	pick.Model, pick.Effort = t.s.agent.Picked(session)
	last[kind] = pick
	b, _ := json.Marshal(last)
	return t.s.user.Set("newModel", b, nil)
}
