//go:build !windows

package term

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestQuote(t *testing.T) {
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"tmux", "-L", "dv", "attach", "-t", "=work"}, "tmux -L dv attach -t '=work'"}, // zsh would look for a command "work"
		{[]string{"-t", "=my work"}, "-t '=my work'"},
		{[]string{"-t", "=it's"}, `-t '=it'\''s'`},
		{[]string{"-S", "/tmp/a b"}, "-S '/tmp/a b'"},
	} {
		if got := quote(c.args); got != c.want {
			t.Errorf("quote(%q) = %s, want %s", c.args, got, c.want)
		}
	}
}

// The attach line is typed into the reader's own shell, so each is tried that
// is here; zsh without a profile of its own.
func TestTmuxOutlivesDv(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("no tmux")
	}
	for _, sh := range []string{"sh", "zsh"} {
		path, err := exec.LookPath(sh)
		if err != nil {
			continue
		}
		t.Run(sh, func(t *testing.T) {
			t.Setenv("SHELL", path)
			// Not the reader's profile, nor zsh's set-up for a user without one.
			zdot := t.TempDir()
			os.WriteFile(filepath.Join(zdot, ".zshrc"), nil, 0o644)
			t.Setenv("ZDOTDIR", zdot)
			outlives(t, "dvtest"+sh+strconv.Itoa(os.Getpid()))
		})
	}
}

func outlives(t *testing.T, sock string) {
	// A server of its own, never the reader's.
	t.Cleanup(func() { exec.Command("tmux", "-L", sock, "kill-server").Run() })
	t.Setenv("TMUX", "/tmp/elsewhere,1,0") // dv started inside tmux
	root := t.TempDir()

	until := func(what string, ok func() bool) {
		t.Helper()
		for end := time.Now().Add(10 * time.Second); !ok(); time.Sleep(100 * time.Millisecond) {
			if time.Now().After(end) {
				t.Fatalf("no %s", what)
			}
		}
	}
	m := New(root)
	first, err := m.Start()
	if err != nil {
		t.Fatal(err)
	}
	// The shell is not in dv's tmux, and says it is dv's for a profile to see.
	w, _, _ := first.Attach()
	first.Write([]byte("echo \"in [$TMUX] of [$TERM_PROGRAM]\"\n"))
	var out strings.Builder
	until("a shell of dv's own", func() bool {
		for {
			select {
			case e := <-w.C:
				out.Write(e.Output)
			default:
				return strings.Contains(out.String(), "in [] of [dv]")
			}
		}
	})
	w.Close()
	first.Rename("first")
	first.Write([]byte("tmux -L " + sock + " new -s work -c " + filepath.Join(root) + "\n"))
	until("tmux session seen", func() bool { return m.List()[0].Tmux == "work" })
	// What runs in its pane, by its whole command line, not tmux's name for it.
	first.Write([]byte("sleep 31\n"))
	until("the pane's command", func() bool { return m.List()[0].Command == "sleep 31" })
	if n := Busy(); n != 0 {
		t.Fatalf("a restart would stop %d, with only tmux running", n)
	}
	if b, _ := os.ReadFile(m.keptPath()); !strings.Contains(string(b), `"work"`) {
		t.Fatalf("not kept: %s", b)
	}

	m.Close() // as dv restarting does
	if err := exec.Command("tmux", "-L", sock, "has-session", "-t", "=work").Run(); err != nil {
		t.Fatal("the session went with the terminal")
	}

	again := New(root)
	defer again.Close()
	again.Restore()
	list := again.List()
	if len(list) != 1 || list[0].ID != first.ID() || list[0].Name != "first" {
		t.Fatalf("restored %+v", list)
	}
	until("attached again", func() bool { return again.List()[0].Tmux == "work" })

	// Closed by the reader, it is not brought back, but is offered to open.
	again.Remove(first.ID())
	if _, err := os.Stat(again.keptPath()); !os.IsNotExist(err) {
		t.Fatal("still kept after being closed")
	}
	offered := func() *Session {
		for _, s := range again.Sessions() {
			if s.Name == "work" && slices.Equal(s.Flags, []string{"-L", sock}) {
				return &s
			}
		}
		return nil
	}
	until("session offered", func() bool { return offered() != nil })
	if s := offered(); !s.Here || s.Windows != 1 {
		t.Fatalf("offered %+v", s)
	}
	if _, err := again.Attach("work", []string{"-c", "rm -rf ~"}); err == nil {
		t.Fatal("attached by flags that are no server's")
	}
	if _, err := again.Attach("work", []string{"-L", sock}); err != nil {
		t.Fatal(err)
	}
	until("attached from the list", func() bool { return len(again.List()) == 1 && again.List()[0].Tmux == "work" && offered() == nil })
}
