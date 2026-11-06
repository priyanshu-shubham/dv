package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dv/internal/permit"
	"dv/internal/store"
)

func TestATitleIsTakenOutOfItsWrapping(t *testing.T) {
	for in, want := range map[string]string{
		"Go map iteration order":                            "Go map iteration order",
		"\"Title: Go map iteration order\"":                 "Go map iteration order",
		"**Sidebar collapse state.**\n\nIt names the work.": "Sidebar collapse state",
		strings.Repeat("word ", 20):                         "",
	} {
		if got := cleanTitle(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); !ok(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("timed out waiting for " + what)
		}
	}
}

// A long turn pushes a title out of the end summarize reads; earlier still has
// it, reading the file through once and then only what was added.
func TestATitleFarBackIsStillFound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	output := map[string]any{"type": "user", "uuid": "r", "message": map[string]any{"role": "user",
		"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t", "content": strings.Repeat("x", 100<<10)}}}}
	quoted := user("u2", "a1", `what is {"type":"ai-title","aiTitle":"Not this"}?`)
	head := line(t, user("u1", "", "make x two")) +
		line(t, map[string]any{"type": "last-prompt", "lastPrompt": "make x two"}) +
		line(t, map[string]any{"type": "ai-title", "aiTitle": "Set x"}) +
		line(t, map[string]any{"type": "last-prompt", "lastPrompt": "make x two"}) +
		line(t, quoted) +
		line(t, map[string]any{"type": "last-prompt", "lastPrompt": "now three"})
	os.WriteFile(path, []byte(head+line(t, output)+line(t, output)), 0o644)
	if err := writeTitle(path, "s", "x's value"); err != nil {
		t.Fatal(err)
	}
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(line(t, output) + line(t, output) + `{"type":"ai-title","aiTi`)
	f.Close()

	if s, _ := summarize(path); s.Title != "" {
		t.Fatalf("the end alone has a title: %q", s.Title)
	}
	var e earlier
	e.readOn(path)
	if e.custom != "x's value" || e.ai != "Set x" || e.prompts != 2 {
		t.Fatalf("read through: %+v", e)
	}
	fi, _ := os.Stat(path)
	if e.to >= fi.Size() {
		t.Fatal("read a line still being written")
	}
	f, _ = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`tle":"Set x to three"}` + "\n")
	f.Close()
	e.readOn(path)
	if fi, _ := os.Stat(path); e.ai != "Set x to three" || e.to != fi.Size() {
		t.Fatalf("read on: %+v", e)
	}
}

// A session nobody named is titled from its conversation once it has a reply,
// and once more when it has moved well on; a name given it always wins.
func TestASessionIsTitledFromItsConversation(t *testing.T) {
	root, cfg := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	dir := filepath.Join(cfg, "projects", folderName(root))
	os.MkdirAll(dir, 0o755)
	at := func(e map[string]any) map[string]any { e["cwd"] = root; return e }
	write := func(id string, lines ...map[string]any) string {
		var b strings.Builder
		for _, l := range lines {
			b.WriteString(line(t, at(l)))
		}
		path := filepath.Join(dir, id+".jsonl")
		os.WriteFile(path, []byte(b.String()), 0o644)
		return path
	}
	prompt := func(s string) map[string]any { return map[string]any{"type": "last-prompt", "lastPrompt": s} }
	answered := write("s1", user("u1", "", "why do Go maps iterate randomly?"), prompt("why do Go maps iterate randomly?"),
		user("u2", "u1", "and in sorted order?"), prompt("and in sorted order?"),
		assistant("a1", "u2", "m1", text("Sort the keys first.")))
	write("s2", user("u1", "", "hello"), prompt("hello"))
	write("s3", user("u1", "", "scratch"), prompt("scratch"), assistant("a1", "u1", "m1", text("Sure.")))

	asked := make(chan string, 10)
	askTitle = func(gist string) (string, error) { asked <- gist; return "Go map iteration order", nil }
	t.Cleanup(func() { askTitle = claudeTitle })
	saved, _ := store.OpenSessions(root)
	saved.SetTemporary("s3", true)
	saved.Set("s3", true)
	m := New(root, permit.New(root), saved)
	on := true
	m.AutoTitles = func() bool { return on }
	title := func(id string) string {
		for _, s := range m.Sessions() {
			if s.ID == id {
				return s.Title
			}
		}
		return "?"
	}
	again := func() {
		m.mu.Lock()
		m.titled = map[string]time.Time{}
		m.mu.Unlock()
	}
	// The listing once the file has been read through to n prompts.
	readTo := func(n int) {
		again()
		waitFor(t, "the file read on", func() bool {
			m.Sessions()
			m.mu.Lock()
			defer m.mu.Unlock()
			e := m.scans[answered]
			return e != nil && e.prompts == n
		})
		m.Sessions()
	}

	waitFor(t, "a title", func() bool { return title("s1") != "" })
	gist := <-asked
	if !strings.Contains(gist, "why do Go maps iterate randomly?") || !strings.Contains(gist, "and in sorted order?") || !strings.Contains(gist, "Sort the keys first.") {
		t.Fatalf("titled from %q", gist)
	}
	for _, s := range m.Sessions() {
		if s.ID == "s1" && (s.Title != "Go map iteration order" || s.Prompt != "why do Go maps iterate randomly?") {
			t.Fatalf("row %+v", s)
		}
	}
	if m.Title("s1") != "Go map iteration order" || m.Title("s2") != "hello" {
		t.Fatalf("named away from the list %q, %q", m.Title("s1"), m.Title("s2"))
	}

	// Not again until it has moved well on, and then once.
	f, _ := os.OpenFile(answered, os.O_APPEND|os.O_WRONLY, 0)
	for _, p := range []string{"three", "four", "five", "six"} {
		f.WriteString(line(t, prompt(p)))
	}
	f.Close()
	readTo(6)
	time.Sleep(100 * time.Millisecond)
	if len(asked) > 0 {
		t.Fatal("titled again after 6 prompts")
	}
	f, _ = os.OpenFile(answered, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(line(t, prompt("seven")) + line(t, prompt("eight")))
	f.Close()
	readTo(8)
	waitFor(t, "a title made again", func() bool { return saved.Titles()["s1"].Redone })
	<-asked
	f, _ = os.OpenFile(answered, os.O_APPEND|os.O_WRONLY, 0)
	for i := range 30 {
		f.WriteString(line(t, prompt(strings.Repeat("more", i+1))))
	}
	f.Close()
	readTo(38)
	time.Sleep(100 * time.Millisecond)

	// One with no reply, or temporary, is never titled; a name given one wins.
	if err := writeTitle(answered, "s1", "maps"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the rename", func() bool { return title("s1") == "maps" })
	if len(asked) > 0 {
		t.Fatalf("asked for a title of %q", <-asked)
	}
	if title("s2") != "" || title("s3") != "" {
		t.Fatalf("titled: %q, %q", title("s2"), title("s3"))
	}

	// Off, nothing is asked for.
	on = false
	saved.SetTitle("s1", store.Title{})
	write("s4", user("u1", "", "off"), prompt("off"), assistant("a1", "u1", "m1", text("Off.")))
	waitFor(t, "s4 read through", func() bool {
		m.Sessions()
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.scans[filepath.Join(dir, "s4.jsonl")] != nil
	})
	m.Sessions()
	time.Sleep(100 * time.Millisecond)
	if len(asked) > 0 {
		t.Fatal("asked with titles off")
	}
}
