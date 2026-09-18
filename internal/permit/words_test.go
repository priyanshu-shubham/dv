package permit

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestWords(t *testing.T) {
	sug := func(s ...string) (out []json.RawMessage) {
		for _, x := range s {
			out = append(out, json.RawMessage(x))
		}
		return out
	}
	long := `{"type":"addRules","behavior":"allow","destination":"localSettings","rules":[{"toolName":"Bash","ruleContent":"npm test:*"},{"toolName":"Bash","ruleContent":"for f in *.go; do gofmt -l \"$f\" && echo checked this one file; done\nexit 0"}]}`
	for _, c := range []struct {
		req      Request
		headline string
		labels   []string
	}{
		{Request{Tool: "Bash", Input: json.RawMessage(`{"command":"npm test\nnpm run lint"}`), Suggestions: sug(long, `{"type":"mystery"}`)},
			"run a command",
			[]string{"Yes", "Yes, and don't ask again for npm test:*, for f in *.go; do gofmt -l \"$f\" && echo checked this on…", "No"}},
		{Request{Tool: "Edit", Input: json.RawMessage(`{"file_path":"/r/src/a.go"}`), Previews: []*Preview{{Path: "src/a.go"}}, Suggestions: PlanModes[:1]},
			"edit a.go",
			[]string{"Yes", "Yes, allow all edits this session", "No"}},
		{Request{Tool: "ExitPlanMode", Input: json.RawMessage(`{"plan":"# Plan"}`), Suggestions: PlanModes},
			"start on this plan",
			[]string{"Yes, and accept edits", "Yes, and ask before edits", "No"}},
		{Request{Tool: "mcp__github__create_issue", Input: json.RawMessage(`{}`), Suggestions: sug(`{"type":"addDirectories","directories":["/tmp"],"destination":"session"}`)},
			"use github: create_issue",
			[]string{"Yes", "Yes, and always allow access to /tmp", "No"}},
		{Request{Tool: "Edit", Input: json.RawMessage(`{"files":["a","b"]}`)}, "edit 2 files", []string{"Yes", "No"}},
	} {
		if got := c.req.headline(); got != c.headline {
			t.Errorf("%s: headline %q, want %q", c.req.Tool, got, c.headline)
		}
		opts := c.req.options()
		var labels []string
		for _, o := range opts {
			labels = append(labels, o.Label)
		}
		if !reflect.DeepEqual(labels, c.labels) {
			t.Errorf("%s: options %q, want %q", c.req.Tool, labels, c.labels)
		}
	}

	// A suggestion is known by its place among them, the unworded ones counted.
	r := Request{Tool: "Bash", Suggestions: sug(`{"type":"mystery"}`, long)}
	if o := r.options()[1]; o.Suggestion == nil || *o.Suggestion != 1 || o.Where != ".claude/settings.local.json" || !o.Allow {
		t.Errorf("the rule's option: %+v", o)
	}
}
