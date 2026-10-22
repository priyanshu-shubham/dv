package server

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"dv/internal/agent"
	"dv/internal/store"
)

func TestAddedModels(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	user, err := store.OpenUserPrefs()
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{user: user}
	post := func(body string) int {
		w := httptest.NewRecorder()
		s.handleAgentModels(w, httptest.NewRequest("POST", "/api/agent/models", strings.NewReader(body)))
		return w.Code
	}
	for _, body := range []string{
		`{"model":"claude-opus-4-1","add":true}`,
		`{"model":" my-proxy ","add":true}`,
		`{"model":"opus","add":true}`,
		`{"agent":"codex","model":"gpt-5.1-codex-max","add":true}`,
		`{"model":"claude-opus-4-1","add":true}`,
	} {
		if c := post(body); c != http.StatusOK {
			t.Fatalf("%s: %d", body, c)
		}
	}
	for _, body := range []string{`{"model":"two words","add":true}`, `{"model":"","add":true}`, `{"agent":"gemini","model":"x","add":true}`} {
		if c := post(body); c != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, c)
		}
	}

	offered := []agent.Model{
		{ID: "", Model: "claude-opus-5-5[1m]", Label: "Opus 5.5", Efforts: []string{"low", "high", "max"}},
		{ID: "opus", Model: "claude-opus-5-5"},
		{ID: "sonnet", Model: "claude-sonnet-5", Efforts: []string{"low", "high"}},
	}
	got := s.withAdded("", offered)
	ids := []string{}
	for _, m := range got {
		ids = append(ids, m.ID)
	}
	// Opus is offered already; adding it again moves it nowhere.
	if want := []string{"", "opus", "sonnet", "my-proxy", "claude-opus-4-1"}; !slices.Equal(ids, want) {
		t.Fatalf("listed %q, want %q", ids, want)
	}
	if m := got[4]; !m.Added || m.Model != "claude-opus-4-1" || !slices.Equal(m.Efforts, offered[0].Efforts) {
		t.Errorf("an added Opus is %+v, want Opus's efforts", m)
	}
	if m := got[3]; !m.Added || m.Efforts != nil {
		t.Errorf("a model of no offered kind is %+v, want no efforts", m)
	}
	if got := s.withAdded("", nil); got != nil {
		t.Errorf("before the agent has answered, listed %v", got)
	}
	if got := s.withAdded("codex", []agent.Model{{ID: ""}}); len(got) != 2 || got[1].ID != "gpt-5.1-codex-max" {
		t.Errorf("Codex's list is %v", got)
	}

	post(`{"model":"my-proxy"}`)
	post(`{"model":"claude-opus-4-1"}`)
	post(`{"model":"opus"}`)
	if got := s.withAdded("", offered); len(got) != len(offered) {
		t.Errorf("after taking them off, listed %v", got)
	}
	post(`{"agent":"codex","model":"gpt-5.1-codex-max"}`)
	if _, ok := user.All()["addedModels"]; ok {
		t.Error("an empty list is still kept")
	}
}
