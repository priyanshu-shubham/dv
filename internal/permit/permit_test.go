package permit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dv/internal/store"
)

// event is a hook's stdin, shaped as Claude Code sends it.
func event(name, tool, input string) []byte {
	return []byte(`{"hook_event_name":"` + name + `","session_id":"s1","cwd":"/x","tool_name":"` + tool +
		`","tool_input":` + input + `,"permission_suggestions":[{"type":"setMode","mode":"acceptEdits","destination":"session"}]}`)
}

// asking starts a permission prompt and returns what its hook will print.
func asking(t *testing.T, b *Broker, ctx context.Context, tool, input string) (*Request, <-chan []byte) {
	t.Helper()
	changed, stop := b.Watch()
	defer stop()
	out := make(chan []byte, 1)
	go func() {
		o, err := b.Hook(ctx, event("PermissionRequest", tool, input))
		if err != nil {
			t.Error(err)
		}
		out <- o
	}()
	select {
	case <-changed:
	case <-time.After(2 * time.Second):
		t.Fatal("the request never showed up")
	}
	w := b.Waiting()
	if len(w) != 1 {
		t.Fatalf("%d requests waiting, want 1", len(w))
	}
	return w[0], out
}

func decision(t *testing.T, out <-chan []byte) map[string]any {
	t.Helper()
	select {
	case o := <-out:
		if o == nil {
			return nil
		}
		var v struct {
			H struct {
				Decision map[string]any `json:"decision"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(o, &v); err != nil {
			t.Fatalf("hook printed %q: %v", o, err)
		}
		return v.H.Decision
	case <-time.After(2 * time.Second):
		t.Fatal("the hook is still waiting")
	}
	return nil
}

func TestAllowWithANoteReachesClaudeOnceTheCallRuns(t *testing.T) {
	b := New(t.TempDir())
	input := `{"command":"make test","description":"Run the tests"}`
	req, out := asking(t, b, context.Background(), "Bash", input)

	one := 0
	if !b.Answer(req.ID, Answer{Allow: true, Note: "and then lint", Suggestion: &one}) {
		t.Fatal("the answer found nothing waiting")
	}
	d := decision(t, out)
	if d["behavior"] != "allow" || d["updatedPermissions"] == nil {
		t.Fatalf("decision %v; want an allow applying the suggestion", d)
	}

	ran, _ := b.Hook(context.Background(), event("PostToolUse", "Bash", input))
	if !bytes.Contains(ran, []byte("and then lint")) || !bytes.Contains(ran, []byte("additionalContext")) {
		t.Fatalf("after the call ran: %s", ran)
	}
	if again, _ := b.Hook(context.Background(), event("PostToolUse", "Bash", input)); again != nil {
		t.Fatalf("the note went twice: %s", again)
	}
}

func TestDenyInterruptsUnlessItSaysWhy(t *testing.T) {
	b := New(t.TempDir())
	req, out := asking(t, b, context.Background(), "Bash", `{"command":"rm -rf build"}`)
	b.Answer(req.ID, Answer{})
	if d := decision(t, out); d["behavior"] != "deny" || d["interrupt"] != true {
		t.Fatalf("bare deny: %v", d)
	}

	req, out = asking(t, b, context.Background(), "Bash", `{"command":"rm -rf build"}`)
	b.Answer(req.ID, Answer{Note: "use make clean"})
	d := decision(t, out)
	if d["behavior"] != "deny" || d["interrupt"] != nil || !strings.Contains(d["message"].(string), "use make clean") {
		t.Fatalf("deny with a reason: %v", d)
	}
}

func TestWhatWasWrittenWithAnAnswerReadsBack(t *testing.T) {
	for _, c := range []struct {
		told, words string
		yes, ok     bool
	}{
		{Declined("use make clean"), "use make clean", false, true},
		{Allowed("Bash", "and then lint"), "and then lint", true, true},
		{Allowed("", "in dv, with a note: too"), "in dv, with a note: too", true, true},
		{"The user declined this in dv.", "", false, false},
	} {
		if w, yes, ok := Said(c.told); w != c.words || yes != c.yes || ok != c.ok {
			t.Errorf("Said(%q) = %q, %v, %v", c.told, w, yes, ok)
		}
	}
}

// A yes in the terminal is only heard when the call reports back, and the
// report need not spell the input the way the prompt did.
func TestTheTerminalAnsweringFirstReleasesTheHook(t *testing.T) {
	b := New(t.TempDir())
	req, out := asking(t, b, context.Background(), "Edit",
		`{"file_path":"/x/a.go","old_string":"a","new_string":"b","replace_all":false}`)
	b.Hook(context.Background(), event("PostToolUse", "Edit", `{"file_path":"/x/a.go","old_string":"a","new_string":"b"}`))
	if d := decision(t, out); d != nil {
		t.Fatalf("hook printed %v after the terminal answered", d)
	}
	if len(b.Waiting()) != 0 || b.Answer(req.ID, Answer{Allow: true}) {
		t.Fatal("the request is still waiting")
	}
}

func TestAQuestionFromTheTerminalIsAnsweredInDV(t *testing.T) {
	b := New(t.TempDir())
	input := `{"questions":[{"question":"Which color?","header":"Color","multiSelect":false,"options":[{"label":"Red"},{"label":"Blue"}]}]}`
	req, out := asking(t, b, context.Background(), "AskUserQuestion", input)
	b.Answer(req.ID, Answer{
		Allow:       true,
		Answers:     map[string]string{"Which color?": "Blue"},
		Annotations: map[string]json.RawMessage{"Which color?": json.RawMessage(`{"notes":"a darker navy"}`)},
	})
	d := decision(t, out)
	got, _ := json.Marshal(d["updatedInput"])
	for _, want := range []string{`"answers":{"Which color?":"Blue"}`, `"annotations":{"Which color?":{"notes":"a darker navy"}}`, `"questions":[`} {
		if d["behavior"] != "allow" || !strings.Contains(string(got), want) {
			t.Fatalf("decision %v lacks %s", d, want)
		}
	}

	// Answered in the terminal, the call reports with its answers in it.
	req, out = asking(t, b, context.Background(), "AskUserQuestion", input)
	answered := strings.TrimSuffix(input, "}") + `,"answers":{"Which color?":"Red"},"annotations":{}}`
	b.Hook(context.Background(), event("PostToolUse", "AskUserQuestion", answered))
	if d := decision(t, out); d != nil || b.Answer(req.ID, Answer{Allow: true}) {
		t.Fatalf("the question is still waiting after the terminal answered: %v", d)
	}
}

// Claude Code asks one thing at a time per agent, so a new prompt means the
// last one from that agent was answered, however dv missed hearing it.
func TestANewPromptRetiresTheLastFromTheSameAgent(t *testing.T) {
	b := New(t.TempDir())
	_, first := asking(t, b, context.Background(), "Bash", `{"command":"sleep 600"}`)

	sub := make(chan []byte, 1)
	go func() {
		o, _ := b.Hook(context.Background(), []byte(`{"hook_event_name":"PermissionRequest","session_id":"s1","agent_id":"a7","tool_name":"Bash","tool_input":{"command":"ls"}}`))
		sub <- o
	}()
	for len(b.Waiting()) < 2 {
		time.Sleep(time.Millisecond)
	}

	go b.Hook(context.Background(), event("PermissionRequest", "Bash", `{"command":"make"}`))
	if d := decision(t, first); d != nil {
		t.Fatalf("the superseded hook printed %v", d)
	}
	for len(b.Waiting()) < 2 {
		time.Sleep(time.Millisecond)
	}
	for _, r := range b.Waiting() {
		if strings.Contains(string(r.Input), "sleep") {
			t.Fatal("the superseded request is still listed")
		}
	}
	select {
	case o := <-sub:
		t.Fatalf("a subagent's prompt was retired by the main thread's: %s", o)
	default:
	}
}

func TestAHookThatStopsTakesItsRequestAway(t *testing.T) {
	b := New(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	_, out := asking(t, b, ctx, "Bash", `{"command":"ls"}`)
	cancel()
	if d := decision(t, out); d != nil {
		t.Fatalf("a cancelled hook printed %v", d)
	}
	if len(b.Waiting()) != 0 {
		t.Fatal("the request outlived its hook")
	}
}

func TestPreviewIsTheFileAfterTheEdit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pkg", "a.go")
	os.MkdirAll(filepath.Dir(path), 0o755)
	os.WriteFile(path, []byte("package a\n\nfunc F() int { return 1 }\n"), 0o644)

	in, _ := json.Marshal(map[string]any{"file_path": path, "old_string": "return 1", "new_string": "return 2"})
	p := preview(root, "Edit", in)
	if p == nil || p.Problem != "" || !p.InRepo || p.Path != "pkg/a.go" {
		t.Fatalf("preview %+v", p)
	}
	if got := p.Diff.NewLines[2]; got != "func F() int { return 2 }" || p.Diff.OldLines[2] != "func F() int { return 1 }" {
		t.Fatalf("new line 3 is %q", got)
	}

	out := filepath.Join(t.TempDir(), "new.txt")
	in, _ = json.Marshal(map[string]any{"file_path": out, "content": "hi\n"})
	if p := preview(root, "Write", in); p.InRepo || p.Path != out || p.Diff.Status != "A" || len(p.Diff.NewLines) != 1 {
		t.Fatalf("a new file outside the repo: %+v", p)
	}
}

func TestAPlanIsPreviewedForEitherAgent(t *testing.T) {
	b := New(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	claude := &Request{Tool: "ExitPlanMode", Input: json.RawMessage(`{"plan":"# Plan\n\n1. Do it\n","planFilePath":"/home/me/.claude/plans/x.md"}`)}
	codex := &Request{Tool: "ExitPlanMode", Via: "codex", Input: json.RawMessage(`{"plan":"Do it"}`)}
	b.Put(ctx, claude)
	b.Put(ctx, codex)
	if ps := claude.Previews; len(ps) != 1 || ps[0].Path != "/home/me/.claude/plans/x.md" || len(ps[0].Diff.NewLines) != 3 {
		t.Errorf("Claude's plan: %+v", ps)
	}
	if ps := codex.Previews; len(ps) != 1 || ps[0].Path != "plan.md" || ps[0].Diff.NewLines[0] != "Do it" {
		t.Errorf("Codex's plan: %+v", ps)
	}
}

func TestApplyEditRefusesWhatClaudeCodeWould(t *testing.T) {
	file := []byte("x := 1\ny := 1\nsay(“hi”)\n")
	cases := []struct {
		name, old, repl string
		all             bool
		want, problem   string
	}{
		{"once", "x := 1", "x := 2", false, "x := 2\ny := 1\nsay(“hi”)\n", ""},
		{"ambiguous", ":= 1", ":= 2", false, "", "2 times"},
		{"all", ":= 1", ":= 2", true, "x := 2\ny := 2\nsay(“hi”)\n", ""},
		{"missing", "z := 1", "z := 2", false, "", "not in the file"},
		{"straight for curly", `say("hi")`, `say("bye")`, false, "x := 1\ny := 1\nsay(\"bye\")\n", ""},
		{"create over content", "", "new", false, "", "already has content"},
	}
	for _, c := range cases {
		got, problem := applyEdit(file, true, c.old, c.repl, c.all)
		if c.problem != "" {
			if !strings.Contains(problem, c.problem) {
				t.Errorf("%s: problem %q, want one mentioning %q", c.name, problem, c.problem)
			}
			continue
		}
		if problem != "" || string(got) != c.want {
			t.Errorf("%s: got %q (%s), want %q", c.name, got, problem, c.want)
		}
	}
}

func TestRunHookFindsTheRepositorysDV(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, ".git"), 0o755)
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{"ok":1}`))
	}))
	defer srv.Close()

	in := []byte(`{"hook_event_name":"PermissionRequest","cwd":"` + filepath.Join(root, "sub") + `","tool_name":"Bash"}`)
	var out bytes.Buffer
	RunHook(bytes.NewReader(in), &out)
	if out.Len() != 0 || got != nil {
		t.Fatalf("with no dv announced it printed %q", out.String())
	}

	unannounce, err := store.Announce(root, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	RunHook(bytes.NewReader(in), &out)
	if out.String() != `{"ok":1}` || !bytes.Equal(got, in) {
		t.Fatalf("printed %q after sending %q", out.String(), got)
	}

	unannounce()
	if _, ok := store.FindServer(root); ok {
		t.Fatal("still announced after unannouncing")
	}
}
