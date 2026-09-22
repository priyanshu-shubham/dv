package notify

import (
	"fmt"
	"testing"
)

func TestParseWhere(t *testing.T) {
	places := []Place{{Slug: "notes", Name: "notes"}, {Slug: "dv-2", Name: "dv"}}
	for _, c := range []struct{ text, want, rest string }{
		{"fix the flaky test", "{ false false }", "fix the flaky test"},
		{"notes: jot this down", "{notes false false }", "jot this down"},
		{"DV: fix it", "{dv-2 false false }", "fix it"},
		{"wt: fix it", "{ true false }", "fix it"},
		{"notes wt: jot", "{notes true false }", "jot"},
		{"notes wt fix/login: jot", "{notes true false fix/login}", "jot"},
		{"wt notes: jot", "{notes true false }", "jot"},
		{"here: fix it", "{ false true }", "fix it"},
		{"notes:jot", "{notes false false }", "jot"},
		// Not all words it knows: the message's own.
		{"Note: remember the milk", "{ false false }", "Note: remember the milk"},
		{"https://example.com is down", "{ false false }", "https://example.com is down"},
		{"notes wt here: jot", "{ false false }", "notes wt here: jot"},
		{"notes dv: jot", "{ false false }", "notes dv: jot"},
		{"fix this\nnotes: no", "{ false false }", "fix this\nnotes: no"},
	} {
		w, rest := ParseWhere(c.text, places)
		slug := ""
		if w.Place != nil {
			slug = w.Place.Slug
		}
		if got := fmt.Sprintf("{%s %v %v %s}", slug, w.Worktree, w.Here, w.Branch); got != c.want || rest != c.rest {
			t.Errorf("%q: %s %q, want %s %q", c.text, got, rest, c.want, c.rest)
		}
	}
	// A task is a folder of its own, so it takes no other words.
	for _, c := range []struct {
		text string
		task bool
		rest string
	}{
		{"task: sum up this pdf", true, "sum up this pdf"},
		{"Task:sum it", true, "sum it"},
		{"notes task: jot", false, "notes task: jot"},
		{"task wt: jot", false, "task wt: jot"},
	} {
		if w, rest := ParseWhere(c.text, places); w.Task != c.task || rest != c.rest || c.task && w.Place != nil {
			t.Errorf("%q: %+v %q", c.text, w, rest)
		}
	}
}
