package server

import (
	"encoding/json"
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
	if n := ended("bad"); n.Title != "bad failed (exit 3)" || n.Body != "oops" || n.Action != "bad" {
		t.Fatalf("bad: %q %q %q", n.Title, n.Body, n.Action)
	}

	// The notice quotes the end; the page is given all of it.
	run("long", "seq 100")
	if n := ended("long"); !strings.HasPrefix(n.Body, "71\n") || !strings.HasSuffix(n.Body, "\n100") {
		t.Fatalf("long: %q", n.Body)
	}
	output := func(id string) (got struct {
		Title   string
		Running bool
		Output  string
	}) {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/actions/"+id+"/output", nil)
		r.SetPathValue("id", id)
		w := httptest.NewRecorder()
		s.handleCommandOutput(w, r)
		json.Unmarshal(w.Body.Bytes(), &got)
		return got
	}
	if got := output("long"); got.Title != "long is done" || got.Running || !strings.HasPrefix(got.Output, "1\n2\n") || !strings.HasSuffix(got.Output, "\n100\n") {
		t.Fatalf("long's output: %+v", got)
	}

	run("slow", "echo started; sleep 30")
	for range 100 {
		if output("slow").Output != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := output("slow"); got.Title != "slow is running" || !got.Running || got.Output != "started\n" {
		t.Fatalf("slow's output while it runs: %+v", got)
	}
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

func TestCommandOutputKeepsItsEnd(t *testing.T) {
	var o output
	line := strings.Repeat("x", 99) + "\n"
	for range 2*outputBytes/len(line) + 10 {
		o.Write([]byte(line))
	}
	o.Write([]byte("last\n"))
	got, cut := o.String()
	if !cut || len(got) > outputBytes || !strings.HasPrefix(got, line) || !strings.HasSuffix(got, line+"last\n") {
		t.Fatalf("cut %v, %d bytes, starting %q", cut, len(got), got[:min(len(got), 20)])
	}
	if got := lastLines("a\nb\nc\n", 2); got != "b\nc" {
		t.Fatalf("lastLines: %q", got)
	}
	if got := lastLines("a", 2); got != "a" {
		t.Fatalf("lastLines of one: %q", got)
	}
}
