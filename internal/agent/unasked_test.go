package agent

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnaskedGlobs(t *testing.T) {
	root := filepath.FromSlash("/work/repo")
	at := func(rel string) string { return filepath.Join(root, filepath.FromSlash(rel)) }
	for _, c := range []struct {
		glob, path string
		want       bool
	}{
		{"*.md", "README.md", true},
		{"*.md", "docs/guide/intro.MD", true},
		{"*.md", "main.go", false},
		{"docs/", "docs/a/b.txt", true},
		{"docs", "web/docs/x.js", true},
		{"/docs", "web/docs/x.js", false},
		{"./docs/**", "docs/a/b.txt", true},
		{"src/**/*.gen.go", "src/a/b/c.gen.go", true},
		{"src/**/*.gen.go", "src/c.gen.go", true},
		{"src/*.go", "src/a/b.go", false},
		{"**/*_test.go", "internal/x_test.go", true},
		{"?.txt", "a.txt", true},
		{"?.txt", "ab.txt", false},
		{"ünï*.md", "ünïcode.md", true},
		{"  ", "a.md", false},
	} {
		if got := unasked([]string{c.glob}, root, at(c.path)); got != c.want {
			t.Errorf("%q on %q: %v, want %v", c.glob, c.path, got, c.want)
		}
	}
	// Outside the folder, nothing a glob says counts.
	if unasked([]string{"*.md", "**"}, root, filepath.FromSlash("/work/other/a.md")) {
		t.Error("a file outside the folder went unasked")
	}
	if unasked([]string{"**"}, root, "") {
		t.Error("a call with no path went unasked")
	}
}

type sink struct{ bytes.Buffer }

func (*sink) Close() error { return nil }

func TestAutoAskEditsLeavesUnaskedEditsToAutoMode(t *testing.T) {
	root := filepath.FromSlash("/work/repo")
	out := &sink{}
	p := &proc{mode: AutoAskEdits, stdin: out}
	p.unasked = func(path string) bool { return unasked([]string{"docs/"}, root, path) }
	answer := func(file string) string {
		t.Helper()
		out.Reset()
		in, _ := json.Marshal(map[string]any{
			"subtype": "hook_callback", "callback_id": "edit",
			"input": map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Write", "tool_input": map[string]string{"file_path": filepath.Join(root, file)}},
		})
		p.control("r1", in)
		return out.String()
	}
	if got := answer("main.go"); !strings.Contains(got, `"permissionDecision":"ask"`) {
		t.Fatalf("an edit to main.go was not put to the user: %s", got)
	}
	if got := answer(filepath.FromSlash("docs/a.md")); strings.Contains(got, "permissionDecision") {
		t.Fatalf("an edit under docs/ was put to the user: %s", got)
	}
}
