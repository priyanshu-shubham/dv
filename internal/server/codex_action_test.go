package server

import (
	"strings"
	"testing"
)

func TestActionThread(t *testing.T) {
	s := &Server{}
	// Naming the conversation can finish before the prompt's own turn.
	if s.actionThread("t1", "title", false) {
		t.Error("a thread before the prompt's was taken for the action's")
	}
	if !s.actionThread("t1", "main", true) {
		t.Error("the prompt's turn was not the action's")
	}
	if !s.actionThread("t1", "main", false) {
		t.Error("a reply after the first was not the action's")
	}
	if s.actionThread("t1", "title", false) || s.actionThread("t2", "main", false) {
		t.Error("another thread, or another terminal's, was taken for the action's")
	}
}

func TestCodexLine(t *testing.T) {
	line := codexLine(actionRun{Prompt: "it's\nlong", Model: "gpt-x", Effort: "high", Mode: "workspace-write"})
	for _, want := range []string{"SSH_CONNECTION=", " codex -s workspace-write -m gpt-x -c 'model_reasoning_effort=high' -a never ", ` -- "$DV_PROMPT"` + "\r"} {
		if !strings.Contains(line, want) {
			t.Errorf("%q lacks %q", line, want)
		}
	}
	if strings.Contains(line, "long") {
		t.Errorf("the prompt is typed into the shell: %q", line)
	}
}
