package agent

import (
	"fmt"
	"strings"
	"testing"
)

func TestAShellCommandReadsBackAsOne(t *testing.T) {
	// dv sends a command and what it printed as one message; the terminal
	// writes the output on a line of its own.
	dv := user("u1", "", "<bash-input>go test ./...</bash-input>\n<bash-stdout>ok  \tdv\n</bash-stdout><bash-stderr>warning\n</bash-stderr>")
	terminal := user("u2", "a1", "<bash-input> ls</bash-input>")
	printed := user("u3", "u2", "<bash-stdout>a.txt\n</bash-stdout><bash-stderr></bash-stderr>")
	tr := newTranscript("/repo")
	tr.Feed([]byte(line(t, dv) + line(t, assistant("a1", "u1", "m1", text("Tests pass."))) + line(t, terminal) + line(t, printed)))

	var shells []Item
	for _, it := range tr.Items("") {
		if it.Kind == "shell" {
			shells = append(shells, it)
		}
	}
	if len(shells) != 2 {
		t.Fatalf("shells: %+v", shells)
	}
	if s := shells[0]; s.Text != "go test ./..." || !s.Turn || s.UUID != "u1" || s.Result == nil || s.Result.Text != "ok  \tdv\nwarning" {
		t.Errorf("dv's: %+v %+v", s, s.Result)
	}
	if s := shells[1]; s.Text != "ls" || s.Result == nil || s.Result.Text != "a.txt" {
		t.Errorf("the terminal's: %+v %+v", s, s.Result)
	}
}

func TestClippedKeepsTheStartAndTheEnd(t *testing.T) {
	var c clipped
	for i := range 5000 {
		fmt.Fprintf(&c, "line %05d\n", i)
	}
	s := c.String()
	if !strings.HasPrefix(s, "line 00000\n") || !strings.HasSuffix(s, "line 04999\n") || !strings.Contains(s, "bytes left out") || len(s) > shellKeep+100 {
		t.Errorf("clipped to %d bytes: %.60q … %.60q", len(s), s, s[len(s)-60:])
	}
}
