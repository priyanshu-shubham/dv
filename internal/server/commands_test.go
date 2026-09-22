package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"dv/internal/notify"
	"dv/internal/store"
)

func TestCommandActionsSayHowTheyEnded(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	user, err := store.OpenUserPrefs()
	if err != nil {
		t.Fatal(err)
	}
	center := notify.New(func() time.Duration { return time.Hour })
	defer center.Close()
	s, err := Open(t.TempDir(), user, center.Folder("", "folder"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	run := func(id, command string) error {
		return s.runCommand(actionRun{ID: id, Name: id, Kind: "command", Command: command})
	}
	ended := func(id string) notify.Notice {
		t.Helper()
		for range 100 {
			for _, n := range center.Notices() {
				if strings.HasPrefix(n.Title, id+" ") {
					return n
				}
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("no notice of %s", id)
		return notify.Notice{}
	}

	if err := run("ok", `echo one; echo "$DV_WORKTREE"`); err != nil {
		t.Fatal(err)
	}
	if n := ended("ok"); n.Title != "ok is done" || n.Body != "one\n"+s.repo.Root || n.Session != "" {
		t.Fatalf("ok: %q %q", n.Title, n.Body)
	}
	run("bad", "echo oops; exit 3; echo never")
	if n := ended("bad"); n.Title != "bad failed (exit 3)" || n.Body != "oops" {
		t.Fatalf("bad: %q %q", n.Title, n.Body)
	}

	run("slow", "sleep 30")
	if err := run("slow", "sleep 30"); err == nil {
		t.Fatal("ran an action twice at once")
	}
	if !s.InUse() {
		t.Fatal("a command running is not the folder in use")
	}
	r := httptest.NewRequest("POST", "/api/actions/slow/stop", nil)
	r.SetPathValue("id", "slow")
	s.handleStopCommand(httptest.NewRecorder(), r)
	if n := ended("slow"); n.Title != "slow was stopped" {
		t.Fatalf("slow: %q", n.Title)
	}
}
