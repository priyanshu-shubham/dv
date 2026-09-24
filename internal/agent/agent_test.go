package agent

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"dv/internal/permit"
	"dv/internal/store"
)

// line builds one transcript line from a map, the way Claude Code writes them.
func line(t *testing.T, v map[string]any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func user(uuid, parent, text string) map[string]any {
	return map[string]any{"type": "user", "uuid": uuid, "parentUuid": parent, "cwd": "/repo", "gitBranch": "main",
		"origin": map[string]any{"kind": "human"}, "message": map[string]any{"role": "user", "content": text}}
}

func assistant(uuid, parent, msg string, block map[string]any) map[string]any {
	return map[string]any{"type": "assistant", "uuid": uuid, "parentUuid": parent,
		"message": map[string]any{"id": msg, "role": "assistant", "content": []any{block}}}
}

func text(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

// conversation is a session with an edit in it, rewound once from the terminal:
// the second prompt was taken back and asked differently.
func conversation(t *testing.T) string {
	edit := map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Edit",
		"input": map[string]any{"file_path": "/repo/a.go", "old_string": "x := 1", "new_string": "x := 2"}}
	result := map[string]any{"type": "user", "uuid": "u2", "parentUuid": "a2",
		"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "updated"}}},
		"toolUseResult": map[string]any{"filePath": "/repo/a.go", "originalFile": "package a\nx := 1\n",
			"structuredPatch": []any{map[string]any{"oldStart": 1, "oldLines": 2, "newStart": 1, "newLines": 2, "lines": []string{" package a", "-x := 1", "+x := 2"}}}}}
	lines := []string{
		line(t, map[string]any{"type": "permission-mode", "permissionMode": "default"}),
		line(t, user("u1", "", "make x two")),
		line(t, assistant("a1", "u1", "msg_1", map[string]any{"type": "thinking", "thinking": "easy"})),
		line(t, assistant("a2", "a1", "msg_1", edit)),
		line(t, result),
		line(t, assistant("a3", "u2", "msg_2", text("Done."))),
		line(t, user("u3", "a3", "now three")),
		line(t, assistant("a4", "u3", "msg_3", text("Three."))),
		line(t, map[string]any{"type": "ai-title", "aiTitle": "Set x"}),
		line(t, user("u4", "a3", "now four")),
		line(t, map[string]any{"type": "attachment", "uuid": "q1", "parentUuid": "u4",
			"attachment": map[string]any{"type": "queued_command", "prompt": "and five", "commandMode": "prompt"}}),
		line(t, assistant("a5", "q1", "msg_4", text("Four, then five."))),
		line(t, map[string]any{"type": "ai-title", "aiTitle": "Set x to four"}),
		line(t, map[string]any{"type": "last-prompt", "lastPrompt": "now four"}),
	}
	return strings.Join(lines, "")
}

func kinds(items []Item) []string {
	var out []string
	for _, it := range items {
		s := it.Kind
		if it.Text != "" {
			s += ":" + it.Text
		}
		out = append(out, s)
	}
	return out
}

func answer(uuid, parent, msg, model, s string) map[string]any {
	a := assistant(uuid, parent, msg, text(s))
	a["message"].(map[string]any)["model"] = model
	return a
}

// A reply from another model than the last says so: why, when dv caught
// Claude Code's notice; which, when only the transcript tells; nothing, when
// /model or a pick in dv moved it.
func TestAModelNobodyPickedIsShown(t *testing.T) {
	models := func(tr *Transcript, switches ...store.Switch) []string {
		var out []string
		for _, it := range tr.Items("", switches...) {
			if it.Kind == "model" {
				out = append(out, it.From+">"+it.To+":"+it.Text)
			}
		}
		return out
	}
	tr := newTranscript("/repo")
	tr.Feed([]byte(line(t, user("u1", "", "hi")) +
		line(t, answer("a1", "u1", "m1", "claude-opus-5-5", "Hello.")) +
		line(t, user("u2", "a1", "and this")) +
		line(t, answer("a2", "u2", "m2", "claude-opus-4-5", "This."))))
	if got := models(tr); !slices.Equal(got, []string{"claude-opus-5-5>claude-opus-4-5:"}) {
		t.Fatalf("seen in the transcript alone: %q", got)
	}
	why := "Opus 5.5's safeguards flagged this session. Opus 4.5 is answering instead."
	caught := store.Switch{After: "u2", Model: &store.ModelSwitch{From: "claude-opus-5-5", To: "claude-opus-4-5", Why: why}}
	if got := models(tr, caught); !slices.Equal(got, []string{"claude-opus-5-5>claude-opus-4-5:" + why}) {
		t.Fatalf("with the notice: %q", got)
	}
	if got := models(tr, store.Switch{After: "a1", Model: &store.ModelSwitch{To: "opus"}}); got != nil {
		t.Fatalf("picked in dv: %q", got)
	}
	if mode := tr.Mode("", []store.Switch{caught}); mode != "" {
		t.Fatalf("a change of model made the mode %q", mode)
	}

	byCommand := newTranscript("/repo")
	byCommand.Feed([]byte(line(t, user("u1", "", "hi")) +
		line(t, answer("a1", "u1", "m1", "claude-opus-5-5", "Hello.")) +
		line(t, user("c1", "a1", "<command-name>/model</command-name>\n<command-message>model</command-message>\n<command-args>sonnet</command-args>")) +
		line(t, user("u2", "c1", "and this")) +
		line(t, answer("a2", "u2", "m2", "claude-sonnet-5", "This."))))
	if got := models(byCommand); got != nil {
		t.Fatalf("after /model: %q", got)
	}
}

// Claude Code's notice of a fallback reaches the page's record of it, placed
// after the message refused.
func TestAFallbackNoticeIsKept(t *testing.T) {
	var at string
	var got store.ModelSwitch
	p := &proc{changed: func() {}, fellBack: func(a string, sw store.ModelSwitch) { at, got = a, sw }}
	p.handle([]byte(`{"type":"system","subtype":"model_refusal_fallback","content":"Opus 5.5's safeguards flagged this session.","original_model":"claude-opus-5-5","fallback_model":"claude-opus-4-5","refused_user_message_uuid":"u2","scope":"session"}`))
	if at != "u2" || got.From != "claude-opus-5-5" || got.To != "claude-opus-4-5" || !strings.Contains(got.Why, "safeguards") || p.using != "claude-opus-4-5" {
		t.Fatalf("kept %+v after %q, using %q", got, at, p.using)
	}
	p.handle([]byte(`{"type":"system","subtype":"model_refusal_no_fallback","content":"","original_model":"claude-opus-5-5"}`))
	if got.To != "" || got.Why == "" {
		t.Fatalf("a refusal with no fallback: %+v", got)
	}
}

func TestTheConversationIsTheBranchTheFileEndsOn(t *testing.T) {
	tr := newTranscript("/repo")
	tr.Feed([]byte(conversation(t)))
	items := tr.Items("")
	want := []string{"prompt:make x two", "thinking:easy", "tool", "text:Done.", "prompt:now four", "prompt:and five", "text:Four, then five."}
	if got := kinds(items); !slices.Equal(got, want) {
		t.Fatalf("items\n got %q\nwant %q", got, want)
	}
	tool := items[2]
	if tool.Result == nil || tool.Result.Text != "updated" || tool.Result.Edited == nil {
		t.Fatalf("tool result %+v", tool.Result)
	}
	if e := tool.Result.Edited; e.Path != "a.go" || !e.InRepo || e.Adds != 1 || e.Dels != 1 {
		t.Fatalf("edited %+v", e)
	}
	// Rewinding to before "now four" resumes at the reply before it.
	if items[4].UUID != "u4" || items[4].Before != "a3" || items[0].Before != "" {
		t.Fatalf("prompt anchors: %+v / %+v", items[4], items[0])
	}
	if got := kinds(tr.Items("a4")); got[len(got)-1] != "text:Three." {
		t.Fatalf("the abandoned branch reads %q", got)
	}
	if tr.Written("msg_1") != 2 || tr.Written("msg_9") != 0 {
		t.Fatal("blocks written per message miscounted")
	}
}

// Calls made together are chained, and each result hangs off its own call, so
// the first call's result is off the path from the last entry.
func TestCallsMadeTogetherAllGetTheirResults(t *testing.T) {
	call := func(id, path string) map[string]any {
		return map[string]any{"type": "tool_use", "id": id, "name": "Read", "input": map[string]any{"file_path": path}}
	}
	result := func(uuid, parent, id string) map[string]any {
		return map[string]any{"type": "user", "uuid": uuid, "parentUuid": parent,
			"message":       map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": "read " + id}}},
			"toolUseResult": map[string]any{"type": "text", "file": map[string]any{"filePath": "/repo/" + id, "content": "x", "numLines": 1, "startLine": 1, "totalLines": 9}}}
	}
	tr := newTranscript("/repo")
	tr.Feed([]byte(line(t, user("u1", "", "read both")) +
		line(t, assistant("a1", "u1", "msg_1", call("t1", "/repo/a"))) +
		line(t, assistant("a2", "a1", "msg_1", call("t2", "/repo/b"))) +
		line(t, result("r1", "a1", "t1")) +
		line(t, result("r2", "a2", "t2")) +
		line(t, assistant("a3", "r2", "msg_2", text("Both read.")))))
	for _, it := range tr.Items("") {
		if it.Kind == "tool" && (it.Result == nil || it.Result.Detail == nil || it.Result.Detail.Total != 9) {
			t.Fatalf("%s has result %+v", it.ToolID, it.Result)
		}
	}

	// Written the other way round, the conversation goes on from the first
	// call, around the second - which is still one message with it. Nor does
	// the second go missing while it waits for its result.
	tr = newTranscript("/repo")
	want := []string{"prompt:read both", "tool", "tool"}
	for _, l := range []string{
		line(t, user("u1", "", "read both")),
		line(t, assistant("a1", "u1", "msg_1", call("t1", "/repo/a"))),
		line(t, assistant("a2", "a1", "msg_1", call("t2", "/repo/b"))),
		line(t, result("r2", "a2", "t2")),
		line(t, result("r1", "a1", "t1")),
	} {
		tr.Feed([]byte(l))
		if got := kinds(tr.Items("")); tr.Written("msg_1") == 2 && !slices.Equal(got, want) {
			t.Fatalf("items %q", got)
		}
	}
	tr.Feed([]byte(line(t, assistant("a3", "r1", "msg_2", text("Both read.")))))
	if got := kinds(tr.Items("")); !slices.Equal(got, append(want, "text:Both read.")) {
		t.Fatalf("items %q", got)
	}
}

// A call whose process was stopped before its result has none ever, so once
// the next turn begins it is no longer running, however busy the session.
func TestACallCutOffByItsTurnsEndIsDropped(t *testing.T) {
	bash := func(id string) map[string]any {
		return map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": "restart"}}
	}
	dropped := func(tr *Transcript) map[string]bool {
		out := map[string]bool{}
		for _, it := range tr.Items("") {
			if it.Kind == "tool" {
				out[it.ToolID] = it.Dropped
			}
		}
		return out
	}
	tr := newTranscript("/repo")
	tr.Feed([]byte(line(t, user("u1", "", "restart it")) + line(t, assistant("a1", "u1", "msg_1", bash("t1")))))
	if got := dropped(tr); got["t1"] {
		t.Fatal("a call still out in the turn it began in is dropped")
	}
	tr.Feed([]byte(line(t, user("u2", "a1", "Continue from where you left off.")) + line(t, assistant("a2", "u2", "msg_2", bash("t2")))))
	if got := dropped(tr); !got["t1"] || got["t2"] {
		t.Fatalf("dropped %v, want t1 alone", got)
	}
}

func TestATurnFailsOnAnErrorOrAnInterruption(t *testing.T) {
	reply := Item{Kind: "text", Text: "Posted."}
	for name, c := range map[string]struct {
		items []Item
		want  bool
	}{
		"a reply":                 {[]Item{{Kind: "prompt"}, reply, {Kind: "worked"}}, false},
		"an API error":            {[]Item{{Kind: "prompt"}, reply, {Kind: "note", Text: "API Error: 529", Error: true}}, true},
		"interrupted":             {[]Item{{Kind: "prompt"}, {Kind: "tool"}, {Kind: "note", Text: "Interrupted"}}, true},
		"an old error, then done": {[]Item{{Kind: "note", Error: true}, {Kind: "prompt"}, reply}, false},
		"a plain note at the end": {[]Item{{Kind: "prompt"}, reply, {Kind: "note", Text: "Compacted"}}, false},
	} {
		if got := failed(c.items); got != c.want {
			t.Errorf("%s: failed %v, want %v", name, got, c.want)
		}
	}
}

func resultFor(id string, text any, record map[string]any) map[string]any {
	return map[string]any{"type": "user", "uuid": "r-" + id, "parentUuid": "a-" + id, "cwd": "/repo/sub",
		"message":       map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": text}}},
		"toolUseResult": record}
}

func TestResultsSayWhatTheirToolsFound(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG fake"))
	records := map[string]map[string]any{
		"read":  {"type": "text", "file": map[string]any{"filePath": "/repo/a.go", "content": "b\nc", "startLine": 2, "numLines": 2, "totalLines": 9}},
		"image": {"type": "image", "file": map[string]any{"base64": png, "type": "image/png", "dimensions": map[string]any{"displayWidth": 640, "displayHeight": 400}}},
		"grep":  {"mode": "content", "numFiles": 0, "numLines": 2, "content": "lib/x.go:3:func X()\n/elsewhere/y.go:7:func Y()"},
		"glob":  {"filenames": []string{"/repo/a.go", "b.go"}, "numFiles": 2, "truncated": true},
		"bash":  {"stdout": "one\ntwo\n", "stderr": "\x1b[31mwarn\x1b[0m", "interrupted": false},
		"bg":    {"stdout": "", "stderr": "", "backgroundTaskId": "task9"},
		"fetch": {"code": 200, "codeText": "OK", "bytes": 1234, "result": "# Page", "url": "https://x"},
	}
	var lines strings.Builder
	for id, rec := range records {
		lines.WriteString(line(t, resultFor(id, "text of "+id, rec)))
	}
	tr := newTranscript("/repo")
	tr.Feed([]byte(lines.String()))
	d := func(id string) Detail { return *tr.results[id].Detail }
	if r := d("read"); r.Kind != "text" || r.Start != 2 || r.Lines != 2 || r.Total != 9 {
		t.Errorf("read %+v", r)
	}
	if r := d("image"); r.Kind != "image" || r.Width != 640 || r.Height != 400 {
		t.Errorf("image %+v", r)
	}
	if r := d("grep"); r.Kind != "content" || r.Lines != 2 || *r.Files != 0 {
		t.Errorf("grep %+v", r)
	}
	if r := d("glob"); *r.Files != 2 || !r.More {
		t.Errorf("glob %+v", r)
	}
	if r := d("bash"); r.Lines != 3 || r.Background != "" {
		t.Errorf("bash %+v", r)
	}
	if r := d("bg"); r.Background != "task9" {
		t.Errorf("background %+v", r)
	}
	if r := d("fetch"); r.Status != "200 OK" || r.Bytes != 1234 {
		t.Errorf("fetch %+v", r)
	}

	path := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(path, []byte(lines.String()), 0o644)
	out := func(id string) *Output {
		l, err := resultLine(path, id)
		if err != nil {
			t.Fatal(err)
		}
		o, err := outputFrom(l, "/repo", id)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	if o := out("read"); o.Read == nil || o.Read.Path != "a.go" || !o.Read.InRepo || o.Read.Lang != "go" || o.Read.Start != 2 || o.Text != "" {
		t.Errorf("read output %+v", o)
	}
	// Grep's paths are the session's working directory's.
	want := []Found{{Path: "sub/lib/x.go", InRepo: true, Line: 3, Text: "func X()"}, {Path: "/elsewhere/y.go", Line: 7, Text: "func Y()"}}
	if o := out("grep"); !slices.Equal(o.Found, want) {
		t.Errorf("grep output %+v", o.Found)
	}
	if o := out("glob"); len(o.Found) != 2 || o.Found[0].Path != "a.go" || o.Found[1].Path != "sub/b.go" {
		t.Errorf("glob output %+v", o.Found)
	}
	if o := out("bash"); o.Stdout != "one\ntwo\n" || o.Stderr != "warn" {
		t.Errorf("bash output %+v", o)
	}
	l, _ := resultLine(path, "image")
	if img, err := imageFrom(l); err != nil || img.MediaType != "image/png" || string(img.Data) != "\x89PNG fake" {
		t.Errorf("image %v %v", img, err)
	}
	if l, _ := resultLine(path, "read"); l != nil {
		if _, err := imageFrom(l); err == nil {
			t.Error("a text read served as an image")
		}
	}
}

// A no's words come back in the call's result; a yes's in a hook's attachment
// after it.
func TestWhatWasWrittenWithAnAnswerStaysWithItsCall(t *testing.T) {
	bash := func(id string) map[string]any {
		return map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": "make"}}
	}
	result := func(id, parent, text string, failed bool) map[string]any {
		return map[string]any{"type": "user", "uuid": "r-" + id, "parentUuid": parent,
			"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": text, "is_error": failed}}}}
	}
	tr := newTranscript("/repo")
	tr.Feed([]byte(line(t, user("u1", "", "build it")) +
		line(t, assistant("a1", "u1", "msg_1", bash("t1"))) +
		line(t, result("t1", "a1", permit.Declined("not yet"), true)) +
		line(t, assistant("a2", "r-t1", "msg_2", bash("t2"))) +
		line(t, result("t2", "a2", "built", false))))
	said := func(items []Item) map[string]Result {
		out := map[string]Result{}
		for _, it := range items {
			if it.Kind == "tool" {
				out[it.ToolID] = *it.Result
			}
		}
		return out
	}
	before := tr.Items("")
	tr.Feed([]byte(line(t, map[string]any{"type": "attachment", "uuid": "h1", "parentUuid": "r-t2",
		"attachment": map[string]any{"type": "hook_additional_context", "toolUseID": "t2", "content": []string{permit.Allowed("Bash", "then lint")}}})))
	got := said(tr.Items(""))
	if r := got["t1"]; r.Said != "not yet" || r.Yes {
		t.Errorf("no = %+v", r)
	}
	if r := got["t2"]; r.Said != "then lint" || !r.Yes || r.Text != "built" {
		t.Errorf("yes = %+v", r)
	}
	if r := said(before)["t2"]; r.Said != "" {
		t.Errorf("items handed out before the note changed under their holder: %+v", r)
	}
}

func TestAFailedCallsOutputIsItsText(t *testing.T) {
	said := "The user declined this in dv and said: not yet"
	r := resultFor("no", said, nil)
	r["toolUseResult"] = "Error: " + said
	path := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(path, []byte(line(t, r)), 0o644)
	l, err := resultLine(path, "no")
	if err != nil {
		t.Fatal(err)
	}
	if o, err := outputFrom(l, "/repo", "no"); err != nil || o.Text != said {
		t.Errorf("output %+v, %v", o, err)
	}
}

// Another session's message shows whether it came while this one was idle, as
// a meta line, or busy, as a queued message.
func TestPeerMessages(t *testing.T) {
	msg := func(body string) string {
		return "<cross-session-message from=\"uds:/run/user/1000/cc-socks/9.sock\" from-name=\"dv-3e\" from-mode=\"prompting\">\n" + body + "\n</cross-session-message>"
	}
	tr := newTranscript("/repo")
	tr.Feed([]byte(line(t, user("u1", "", "go")) +
		line(t, assistant("a1", "u1", "m1", text("ok"))) +
		line(t, map[string]any{"type": "user", "uuid": "p1", "parentUuid": "a1", "isMeta": true,
			"origin":  map[string]any{"kind": "peer", "name": "dv-3e"},
			"message": map[string]any{"role": "user", "content": "Another Claude session sent a message:\n" + msg("Chunk 1 is done.")}}) +
		line(t, map[string]any{"type": "attachment", "uuid": "p2", "parentUuid": "p1",
			"attachment": map[string]any{"type": "queued_command", "prompt": msg("And chunk 2.")}})))
	var got []Item
	for _, it := range tr.Items("") {
		if it.Kind == "peer" {
			got = append(got, it)
		}
		if it.Kind == "prompt" && it.Text != "go" {
			t.Errorf("a peer's message shows as a prompt: %q", it.Text)
		}
	}
	if len(got) != 2 || got[0].From != "dv-3e" || got[0].Text != "Chunk 1 is done." || !got[0].Turn || got[1].Text != "And chunk 2." || got[1].Turn {
		t.Fatalf("peer messages = %+v", got)
	}
}

// A background command's result only says it started; it ends when Claude
// Code's notification says so, or when Claude stops it.
func TestBackgroundWorkRunsUntilItsNotification(t *testing.T) {
	bash := func(id string) map[string]any {
		return map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": "sleep 9", "run_in_background": true}}
	}
	started := func(uuid, parent, id, task string) map[string]any {
		return map[string]any{"type": "user", "uuid": uuid, "parentUuid": parent,
			"message":       map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": id, "content": "running in background"}}},
			"toolUseResult": map[string]any{"stdout": "", "stderr": "", "backgroundTaskId": task}}
	}
	tr := newTranscript("/repo")
	tr.Feed([]byte(line(t, user("u1", "", "go")) +
		line(t, assistant("a1", "u1", "m1", bash("t1"))) +
		line(t, started("r1", "a1", "t1", "bgA")) +
		line(t, assistant("a2", "r1", "m2", bash("t2"))) +
		line(t, started("r2", "a2", "t2", "bgB"))))
	task := func(id string) *Task {
		for _, it := range tr.Items("") {
			if it.ToolID == id {
				return it.Task
			}
		}
		return nil
	}
	if task("t1") != nil || task("t2") != nil {
		t.Fatal("a background command ended as it started")
	}
	tr.Feed([]byte(line(t, map[string]any{"type": "attachment", "uuid": "q1", "parentUuid": "r2",
		"attachment": map[string]any{"type": "queued_command", "commandMode": "task-notification",
			"prompt": "<task-notification>\n<task-id>bgA</task-id>\n<tool-use-id>t1</tool-use-id>\n<status>completed</status>\n<summary>Background command \"sleep\" completed (exit code 0)</summary>\n</task-notification>"}}) +
		line(t, assistant("a3", "q1", "m3", map[string]any{"type": "tool_use", "id": "t3", "name": "TaskStop", "input": map[string]any{"task_id": "bgB"}})) +
		line(t, map[string]any{"type": "user", "uuid": "r3", "parentUuid": "a3",
			"message":       map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "t3", "content": "stopped"}}},
			"toolUseResult": map[string]any{"message": "Successfully stopped task: bgB", "task_id": "bgB"}})))
	if tk := task("t1"); tk == nil || tk.Status != "completed" || !strings.Contains(tk.Summary, "exit code 0") {
		t.Errorf("completed task %+v", tk)
	}
	if tk := task("t2"); tk == nil || tk.Status != "stopped" {
		t.Errorf("stopped task %+v", tk)
	}
}

// Work a process left running goes with it: dv restarting, or stopping a
// session it had not heard from, means no notification is ever written.
func TestBackgroundWorkOfAGoneProcessIsOver(t *testing.T) {
	at := func(s string) string { return s }
	items := []Item{
		{ToolID: "t1", At: at("2026-09-21T01:00:00Z"), Result: &Result{Detail: &Detail{Background: "bgA"}}},
		{ToolID: "t2", At: at("2026-09-21T03:00:00Z"), Result: &Result{Detail: &Detail{Background: "bgB"}}},
		{ToolID: "t3", At: at("2026-09-21T01:00:00Z"), Result: &Result{Detail: &Detail{Background: "bgC"}}, Task: &Task{Status: "completed"}},
		{ToolID: "t4", At: at("2026-09-21T01:00:00Z"), Result: &Result{Text: "done"}},
	}
	ended(items, time.Date(2026, 9, 21, 2, 0, 0, 0, time.UTC))
	if tk := items[0].Task; tk == nil || tk.Status != "stopped" {
		t.Errorf("work from before the process started: %+v", tk)
	}
	if items[1].Task != nil {
		t.Errorf("work this process started: %+v", items[1].Task)
	}
	if items[2].Task.Status != "completed" {
		t.Error("an end already known was written over")
	}
	if items[3].Task != nil {
		t.Error("a call that left nothing running was marked")
	}
	// With no process, nothing is claimed either way.
	items[0].Task = nil
	ended(items, time.Time{})
	if items[0].Task != nil {
		t.Error("marked with no process running")
	}
}

// The file is read as it grows, which can split a line anywhere.
func TestFeedingInPiecesReadsTheSame(t *testing.T) {
	data := []byte(conversation(t))
	whole := newTranscript("/repo")
	whole.Feed(data)
	pieces := newTranscript("/repo")
	for i := 0; i < len(data); i += 37 {
		pieces.Feed(data[i:min(i+37, len(data))])
	}
	a, _ := json.Marshal(whole.Items(""))
	b, _ := json.Marshal(pieces.Items(""))
	if string(a) != string(b) || pieces.Offset != int64(len(data)) {
		t.Fatal("reading in pieces differs from reading it whole")
	}
}

func TestCompactionKeepsWhatCameBefore(t *testing.T) {
	tr := newTranscript("/repo")
	tr.Feed([]byte(line(t, user("u1", "", "first")) +
		line(t, assistant("a1", "u1", "m1", text("one"))) +
		line(t, map[string]any{"type": "system", "subtype": "compact_boundary", "uuid": "c1", "logicalParentUuid": "a1"}) +
		line(t, map[string]any{"type": "user", "uuid": "s1", "parentUuid": "c1", "isCompactSummary": true, "message": map[string]any{"content": "summary"}}) +
		line(t, user("u2", "s1", "<command-name>/model</command-name>\n<command-args>opus</command-args>")) +
		line(t, user("u3", "u2", "[Request interrupted by user]")) +
		line(t, map[string]any{"type": "user", "uuid": "u4", "parentUuid": "u3", "isMeta": true, "message": map[string]any{"content": "hidden"}})))
	want := []string{"prompt:first", "text:one", "compact:summary", "command:/model opus", "note:Interrupted"}
	if got := kinds(tr.Items("")); !slices.Equal(got, want) {
		t.Fatalf("items\n got %q\nwant %q", got, want)
	}
}

// A /compact is written after the compaction it made, with a line of its own
// saying it compacted.
func TestACompactReadsBeforeItsCompaction(t *testing.T) {
	tr := newTranscript("/repo")
	tr.Feed([]byte(line(t, user("u1", "", "first")) +
		line(t, assistant("a1", "u1", "m1", text("one"))) +
		line(t, map[string]any{"type": "system", "subtype": "compact_boundary", "uuid": "c1", "parentUuid": nil, "logicalParentUuid": "a1",
			"compactMetadata": map[string]any{"trigger": "manual", "preTokens": 143_000, "postTokens": 10_000}}) +
		line(t, map[string]any{"type": "user", "uuid": "s1", "parentUuid": "c1", "isCompactSummary": true, "message": map[string]any{"content": "Continued.\n\nSummary:\nWe did one."}}) +
		line(t, map[string]any{"type": "user", "uuid": "k1", "parentUuid": "s1", "isMeta": true, "message": map[string]any{"content": "<local-command-caveat>Caveat</local-command-caveat>"}}) +
		line(t, user("u2", "k1", "<command-name>/compact</command-name>\n<command-message>compact</command-message>\n<command-args></command-args>")) +
		line(t, user("u3", "u2", "<local-command-stdout>Compacted </local-command-stdout>")) +
		line(t, user("u4", "u3", "<command-message>hello</command-message>\n<command-name>/hello</command-name>\n<command-args>there</command-args>")) +
		line(t, user("u5", "u4", "<command-name>/context</command-name>\n<command-message>context</command-message>\n<command-args></command-args>")) +
		line(t, map[string]any{"type": "system", "subtype": "local_command", "uuid": "o1", "parentUuid": "u5", "content": "<local-command-stdout>## Context Usage\n\x1b[1m22k\x1b[22m</local-command-stdout>"})))
	want := []string{"prompt:first", "text:one", "command:/compact", "compact:We did one.", "command:/hello there", "command:/context", "output:## Context Usage\n22k"}
	if got := kinds(tr.Items("")); !slices.Equal(got, want) {
		t.Fatalf("items\n got %q\nwant %q", got, want)
	}
}

// A page shows a session from its latest compaction, and steps back a
// compaction at a time; one made while it looks on takes nothing away.
func TestAPageShowsASessionFromItsLatestCompaction(t *testing.T) {
	c := func(key string) Item { return Item{Key: key, Kind: "compact"} }
	p := func(key string) Item { return Item{Key: key, Kind: "prompt", Text: key} }
	items := []Item{p("u1"), c("c1"), p("u2"), p("u3"), {Key: "x", Kind: "command", Text: "/compact"}, c("c2"), p("u4")}
	keys := func(items []Item) (out []string) {
		for _, it := range items {
			out = append(out, it.Key)
		}
		return out
	}

	s := &Sub{}
	if got := keys(s.shown(items, false)); !slices.Equal(got, []string{"x", "c2", "u4"}) {
		t.Fatalf("shown %q", got)
	}
	if e := s.earlier; e == nil || e.Messages != 2 || e.Next != "c1" {
		t.Fatalf("earlier %+v", e)
	}
	later := append(slices.Clone(items), c("c3"), p("u5"))
	if got := keys(s.shown(later, false)); got[0] != "x" || len(got) != 5 {
		t.Fatalf("a compaction since moved the start: %q", got)
	}

	s = &Sub{from: "c1"}
	if got := keys(s.shown(items, false)); got[0] != "c1" || s.earlier.Next != "all" || s.earlier.Messages != 1 {
		t.Fatalf("one back: %q %+v", got, s.earlier)
	}
	s = &Sub{from: "all"}
	if got := s.shown(items, false); len(got) != len(items) || s.earlier != nil {
		t.Fatalf("all: %d items, earlier %+v", len(got), s.earlier)
	}
	s = &Sub{}
	if got := s.shown([]Item{p("u1")}, false); len(got) != 1 || s.earlier != nil {
		t.Fatal("a session never compacted is shown whole")
	}

	// Read from its latest compaction, what is before is there but not counted,
	// and asking for it names the compaction it is before.
	s = &Sub{}
	if got := keys(s.shown(items[3:], true)); got[0] != "x" || s.earlier == nil || s.earlier.Next != "before:c2" || s.earlier.Messages != 0 {
		t.Fatalf("partial: %q %+v", got, s.earlier)
	}
	s = &Sub{from: "before:c2"}
	if got := keys(s.shown(items, false)); got[0] != "c1" || s.earlier.Next != "all" {
		t.Fatalf("before:c2: %q %+v", got, s.earlier)
	}

	// Rewound to before the compaction it was shown from, it shows from the one before.
	s = &Sub{}
	s.shown(items, false)
	if got := keys(s.shown(items[:4], false)); got[0] != "c1" {
		t.Fatalf("rewound: %q", got)
	}
}

// A long transcript is read from shortly before its latest compaction, and
// reads from there as it does whole: the conversation after a compaction goes
// on from the entry before its boundary.
func TestALongTranscriptIsReadFromItsLatestCompaction(t *testing.T) {
	var b strings.Builder
	parent := ""
	for i := 0; b.Len() < tailFrom+(1<<20); i++ {
		u, a := fmt.Sprintf("u%d", i), fmt.Sprintf("a%d", i)
		b.WriteString(line(t, user(u, parent, "early "+strings.Repeat("x", 4000))))
		b.WriteString(line(t, assistant(a, u, "m"+a, text("reply"))))
		if i == 3 {
			// Recorded long before the tail, and not since.
			b.WriteString(line(t, map[string]any{"type": "permission-mode", "permissionMode": "plan"}))
		}
		parent = a
	}
	b.WriteString(line(t, map[string]any{"type": "system", "subtype": "compact_boundary", "uuid": "c1", "parentUuid": nil, "logicalParentUuid": "t1",
		"compactMetadata": map[string]any{"trigger": "auto", "preTokens": 568_000, "postTokens": 14_000}}))
	b.WriteString(line(t, map[string]any{"type": "attachment", "uuid": "x1", "parentUuid": parent, "attachment": map[string]any{"type": "date"}}))
	b.WriteString(line(t, map[string]any{"type": "user", "uuid": "s1", "parentUuid": "c1", "isCompactSummary": true, "message": map[string]any{"content": "Continued.\n\nSummary:\nEarly work."}}))
	b.WriteString(line(t, map[string]any{"type": "attachment", "uuid": "t1", "parentUuid": "s1", "attachment": map[string]any{"type": "date"}}))
	b.WriteString(line(t, user("u-late", "x1", "later "+`"subtype":"compact_boundary"`)))
	b.WriteString(line(t, assistant("a-late", "u-late", "m-late", text("done"))))
	data := []byte(b.String())

	start, mode := tail(data)
	if start == 0 || mode != "plan" {
		t.Fatalf("tail at %d, mode %q", start, mode)
	}
	whole, part := newTranscript("/repo"), newTranscript("/repo")
	whole.Feed(data)
	part.Start, part.Offset = int64(start), int64(start)
	part.Feed(data[start:])

	sw, sp := &Sub{}, &Sub{}
	a, _ := json.Marshal(sw.shown(whole.Items(""), false))
	c, _ := json.Marshal(sp.shown(part.Items(""), true))
	if string(a) != string(c) {
		t.Fatalf("from the compaction\nwhole %s\n part %s", a, c)
	}
	if got := kinds(sp.shown(part.Items(""), true)); !slices.Equal(got, []string{"compact:Early work.", "prompt:later \"subtype\":\"compact_boundary\"", "text:done"}) {
		t.Fatalf("shown %q", got)
	}
	if sw.earlier == nil || sw.earlier.Messages == 0 || sp.earlier == nil || sp.earlier.Next != "before:c1" {
		t.Fatalf("earlier: whole %+v, part %+v", sw.earlier, sp.earlier)
	}
	if u, _ := part.Context(""); u == 0 {
		t.Fatal("no context read from the tail")
	}
}

// Claude Code now writes a compaction as two chains at once: the boundary, the
// summary and what it kept, whose logical parent comes after the boundary and
// leads back to it; and the next turn's context, hung off where the
// conversation was. The file alternates between them.
func TestACompactionReadsTheSameWhicheverChainIsWritten(t *testing.T) {
	used := func(m map[string]any, n int) map[string]any {
		m["message"].(map[string]any)["usage"] = map[string]any{"input_tokens": n}
		return m
	}
	attachment := func(uuid, parent string) map[string]any {
		return map[string]any{"type": "attachment", "uuid": uuid, "parentUuid": parent, "attachment": map[string]any{"type": "date"}}
	}
	lines := []string{
		line(t, user("u1", "", "first")),
		line(t, used(assistant("a1", "u1", "m1", text("one")), 560_000)),
		line(t, assistant("a2", "a1", "m1", text("kept"))),
		line(t, map[string]any{"type": "system", "subtype": "compact_boundary", "uuid": "c1", "parentUuid": nil, "logicalParentUuid": "t1",
			"compactMetadata": map[string]any{"trigger": "auto", "preTokens": 568_000, "postTokens": 14_000}}),
		line(t, attachment("x1", "a2")),
		line(t, map[string]any{"type": "user", "uuid": "s1", "parentUuid": "c1", "isCompactSummary": true, "message": map[string]any{"content": "This session is being continued from a previous conversation that ran out of context.\n\nSummary:\nWe did one.\n\nIf you need specific details from before compaction, read the transcript.\nContinue the conversation from where it left off."}}),
		line(t, attachment("x2", "x1")),
		line(t, attachment("t1", "s1")),
		line(t, attachment("f1", "t1")),
		line(t, user("u2", "x2", "second")),
		line(t, used(assistant("a3", "u2", "m2", text("two")), 15_000)),
	}
	tr, s := newTranscript("/repo"), &Sub{}
	for i, l := range lines {
		tr.Feed([]byte(l))
		reset, _ := s.diff(tr.Items(""))
		if i > 0 && reset {
			t.Fatalf("line %d starts the conversation over: %q", i, kinds(tr.Items("")))
		}
		if used, _ := tr.Context(""); i >= 3 && i < 10 && used != 14_000 {
			t.Fatalf("line %d: context %d, want what the compaction left", i, used)
		}
	}
	want := []string{"prompt:first", "text:one", "text:kept", "compact:We did one.", "prompt:second", "text:two"}
	items := tr.Items("")
	if got := kinds(items); !slices.Equal(got, want) {
		t.Fatalf("items\n got %q\nwant %q", got, want)
	}
	if c := items[3].Compacted; c == nil || !c.Auto || c.Before != 568_000 || c.After != 14_000 {
		t.Fatalf("compaction %+v", c)
	}
	if used, _ := tr.Context(""); used != 15_000 {
		t.Fatalf("context %d after the next reply", used)
	}
}

func TestLastReply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	call := map[string]any{"type": "tool_use", "id": "toolu_9", "name": "Bash", "input": map[string]any{"command": "go test"}}
	result := map[string]any{"type": "user", "uuid": "u2", "parentUuid": "a2",
		"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_9", "content": "ok"}}}}
	said := line(t, user("u1", "", "run the tests")) +
		line(t, assistant("a1", "u1", "msg_1", text("Running them."))) +
		line(t, assistant("a2", "a1", "msg_1", call)) +
		line(t, result) +
		line(t, assistant("a3", "u2", "msg_2", text("## Done\n\nAll   **pass**.\n"))) +
		line(t, assistant("a4", "a3", "msg_2", text("Nothing else to do.")))
	os.WriteFile(path, []byte(said), 0o644)
	if got := lastReply(path); got != "## Done\n\nAll   **pass**.\n\nNothing else to do." {
		t.Fatalf("reply %q", got)
	}
	os.WriteFile(path, []byte(said+line(t, user("u3", "a4", "and lint?"))), 0o644)
	if got := lastReply(path); got != "" {
		t.Fatalf("reply %q after the reader spoke", got)
	}
}

func TestSummaryTakesTheLatestTitleAndPrompt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(path, []byte(conversation(t)), 0o644)
	s, ok := summarize(path)
	if !ok || s.Title != "Set x to four" || s.Prompt != "now four" || s.Cwd != "/repo" || s.Branch != "main" {
		t.Fatalf("summary %+v, %v", s, ok)
	}
	if s.Last != "Four, then five." || s.LastBy != "claude" {
		t.Fatalf("last said %q by %q", s.Last, s.LastBy)
	}

	// The last word is yours, without what dv sent along with it; the context is the last reply's.
	reply := assistant("a6", "a5", "msg_5", text("Five."))
	reply["message"].(map[string]any)["usage"] = map[string]any{"input_tokens": 1200, "cache_read_input_tokens": 30000, "output_tokens": 40}
	reply["message"].(map[string]any)["model"] = "claude-opus-5"
	os.WriteFile(path, []byte(conversation(t)+line(t, reply)+line(t, user("u5", "a6", "and   six\n<dv-context>\n<file path=\"a.go\" />\n</dv-context>"))), 0o644)
	if s, _ := summarize(path); s.Last != "and six" || s.LastBy != "you" || s.used != 31240 || s.model != "claude-opus-5" {
		t.Fatalf("summary %+v", s)
	}

	// Untitled, a session is named by its prompt, which Claude Code keeps with the context flattened in.
	asked := line(t, user("u1", "", "what does this do?\n\n<dv-context>\n<file path=\"a.go\" />\n</dv-context>"))
	os.WriteFile(path, []byte(asked+line(t, map[string]any{"type": "last-prompt", "lastPrompt": "what does this do?  <dv-context> <file path=\"a.go\" /> </dv-context>"})), 0o644)
	if s, ok := summarize(path); !ok || s.Prompt != "what does this do?" {
		t.Fatalf("prompt %q, listed %v", s.Prompt, ok)
	}
	// Only added code said, it is still a conversation, just unnamed.
	os.WriteFile(path, []byte(line(t, user("u1", "", "\n\n<dv-context>\n<file path=\"a.go\" />\n</dv-context>"))), 0o644)
	if s, ok := summarize(path); !ok || s.Prompt != "" {
		t.Fatalf("prompt %q, listed %v", s.Prompt, ok)
	}

	os.WriteFile(path, []byte(line(t, map[string]any{"type": "permission-mode", "permissionMode": "default"})), 0o644)
	if _, ok := summarize(path); ok {
		t.Fatal("a transcript with nothing said in it was listed")
	}
}

func TestEditIsTheWholeFileWhenItWasKept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(path, []byte(conversation(t)), 0o644)
	ed, err := findEdit(path, "/repo", "toolu_1")
	if err != nil {
		t.Fatal(err)
	}
	if ed.Partial || ed.Path != "a.go" || !slices.Equal(ed.Diff.NewLines, []string{"package a", "x := 2"}) || ed.Diff.Additions != 1 {
		t.Fatalf("edit %+v / %+v", ed, ed.Diff)
	}
	if _, err := findEdit(path, "/repo", "toolu_9"); err != errNoEdit {
		t.Fatalf("a call with no edit: %v", err)
	}
}

func TestEditFromHunksAloneKeepsTheirLineNumbers(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"filePath": "/repo/b.go", "structuredPatch": []any{
		map[string]any{"oldStart": 10, "oldLines": 3, "newStart": 10, "newLines": 3, "lines": []string{" a", "-b", "+B", " c"}},
		map[string]any{"oldStart": 40, "oldLines": 2, "newStart": 40, "newLines": 3, "lines": []string{" d", "+e", " f"}},
	}})
	ed, err := editFrom(raw, "/repo")
	if err != nil || !ed.Partial {
		t.Fatalf("%+v, %v", ed, err)
	}
	fd := ed.Diff
	if fd.OldLines[10] != "b" || fd.NewLines[10] != "B" || fd.NewLines[40] != "e" || fd.Additions != 2 || fd.Deletions != 1 {
		t.Fatalf("lines landed wrong: old %d new %d", len(fd.OldLines), len(fd.NewLines))
	}
	// Nine unknown lines, then the hunk's own line of context.
	if fd.Ops[0].OldLen != 10 || fd.Ops[0].Kind != 0 {
		t.Fatalf("the run before the first change: %+v", fd.Ops[0])
	}
}

func TestApplyHunksRefusesAFileThatMovedOn(t *testing.T) {
	patch := []hunk{{OldStart: 2, OldLines: 1, NewStart: 2, NewLines: 1, Lines: []string{"-two", "+2"}}}
	if got, ok := applyHunks("one\ntwo\nthree\n", patch); !ok || got != "one\n2\nthree\n" {
		t.Fatalf("got %q %v", got, ok)
	}
	if _, ok := applyHunks("one\nTWO\nthree\n", patch); ok {
		t.Fatal("applied to a file it does not match")
	}
}

func TestFolderNameIsClaudeCodes(t *testing.T) {
	if got := folderName("/home/me/my_repo.v2"); got != "-home-me-my-repo-v2" {
		t.Fatalf("got %q", got)
	}
	if got := folderName("/x/é😀"); got != "-x----" {
		t.Fatalf("non-ASCII: %q", got)
	}
}

// A page is sent what is new at the end, or everything when the start changed.
func TestUpdatesAreTheTailUnlessTheStartMoved(t *testing.T) {
	tr := newTranscript("/repo")
	tr.Feed([]byte(conversation(t)))
	s := &Sub{}
	if reset, items := s.diff(tr.Items("a3")); !reset || len(items) != 4 {
		t.Fatalf("first update: reset %v, %d items", reset, len(items))
	}
	if reset, items := s.diff(tr.Items("")); reset || len(items) != 3 || items[0].Text != "now four" {
		t.Fatalf("growing: reset %v, %q", reset, kinds(items))
	}
	if reset, _ := s.diff(tr.Items("a4")); !reset {
		t.Fatal("switching branch was sent as a tail")
	}
}

func TestProcArgsResumeAtARewind(t *testing.T) {
	p := &proc{id: "s1", resume: true, resumeAt: "a3", mode: "plan"}
	got := strings.Join(p.args(), " ")
	if !strings.Contains(got, "--resume s1 --resume-session-at a3") || !strings.Contains(got, "--permission-mode plan") || strings.Contains(got, "--model") {
		t.Fatalf("args %s", got)
	}
	p = &proc{id: "s2"}
	if got := strings.Join(p.args(), " "); !strings.Contains(got, "--session-id s2") || strings.Contains(got, "--resume") {
		t.Fatalf("new session args %s", got)
	}
}

// A headless resume forgets the session's name, so dv passes the one its
// transcript last gave it; a rename while stopped leaves it there too.
func TestAResumeKeepsTheSessionsName(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s1.jsonl")
	os.WriteFile(path, []byte(line(t, user("u1", "", "go"))+`{"type":"agent-name","agentName":"old","sessionId":"s1"}`+"\n"), 0o600)
	if got := agentName(path); got != "old" {
		t.Fatalf("agentName = %q", got)
	}
	p := &proc{id: "s1", resume: true, named: func() string { return agentName(path) }}
	if got := strings.Join(p.args(), " "); !strings.Contains(got, "--name old") {
		t.Fatalf("resume args %s", got)
	}
	if err := writeTitle(path, "s1", "new name"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(p.args(), " "); !strings.Contains(got, "--name new name") {
		t.Fatalf("after a rename %s", got)
	}
	if got := agentName(filepath.Join(t.TempDir(), "none.jsonl")); got != "" {
		t.Fatalf("no transcript: %q", got)
	}
}

// Claude Code's "default" is the model it recommends, which the user's settings
// may have swapped for another.
func TestTheListLeadsWithTheModelTheSettingsStartOn(t *testing.T) {
	var init initResponse
	json.Unmarshal([]byte(`{"current_permission_mode":"acceptEdits","models":[
		{"value":"default","resolvedModel":"claude-opus-5[1m]","displayName":"Default (recommended)","description":"Opus 5 with 1M context · Best for everyday, complex tasks"},
		{"value":"opus[1m]","resolvedModel":"claude-opus-5[1m]","displayName":"Opus (1M context)","description":"Opus 5 with 1M context · Best for everyday, complex tasks"},
		{"value":"claude-fable-5-1[1m]","resolvedModel":"claude-fable-5-1","displayName":"Fable","description":"Fable 5.1 · Most capable","supportedEffortLevels":["low","high","max"]},
		{"value":"haiku","resolvedModel":"claude-haiku-4-5-20251001","displayName":"Haiku","description":"Haiku 4.5 · Fastest"}]}`), &init)
	ids := func(o Options) string {
		var s []string
		for _, m := range o.Models {
			s = append(s, m.ID+"="+m.Label)
		}
		return strings.Join(s, ", ")
	}
	settings := func(model string) *settingsResponse {
		var s settingsResponse
		json.Unmarshal([]byte(`{"effective":{"effortLevel":"xhigh","modelSettings":{"claude-fable-5-1":{"effortLevel":"high"}}},"applied":{"model":"`+model+`"}}`), &s)
		return &s
	}

	o := offer(&init, settings("claude-opus-5[1m]"))
	if got := ids(o); got != "=Opus 5 with 1M context, claude-fable-5-1[1m]=Fable 5.1, haiku=Haiku 4.5" || o.Mode != "acceptEdits" {
		t.Fatalf("settings on the recommendation: %s, mode %q", got, o.Mode)
	}
	// Fable takes its own effort setting; Haiku takes none.
	if f, h := o.Models[1], o.Models[2]; f.Effort != "high" || len(f.Efforts) != 3 || h.Effort != "" || h.Efforts != nil {
		t.Fatalf("efforts: fable %+v, haiku %+v", f, h)
	}
	o = offer(&init, settings("claude-haiku-4-5-20251001"))
	if got := ids(o); got != "=Haiku 4.5, opus[1m]=Opus 5 with 1M context, claude-fable-5-1[1m]=Fable 5.1" || o.Model != "claude-haiku-4-5-20251001" {
		t.Fatalf("settings on haiku: %s", got)
	}
	// Recommended but offered nowhere else, so it stays.
	init.Models = slices.Delete(init.Models, 1, 2)
	if got := ids(offer(&init, settings("claude-haiku-4-5-20251001"))); got != "=Haiku 4.5, default=Opus 5 with 1M context, claude-fable-5-1[1m]=Fable 5.1" {
		t.Fatalf("recommendation alone: %s", got)
	}
	if o := offer(&init, &settingsResponse{}); o.Model != "claude-opus-5[1m]" || o.Models[0].Label != "Opus 5 with 1M context" {
		t.Fatalf("without settings to go on: %+v", o)
	}
}

func TestUsageIsThePlansWindows(t *testing.T) {
	u := usageFrom(json.RawMessage(`{"rate_limits_available":true,"rate_limits":{"five_hour":{"utilization":4,"resets_at":"2026-09-16T22:00:00Z"},"seven_day":{"utilization":6.5,"resets_at":"2026-09-23T11:00:00Z"},"seven_day_opus":null}}`))
	if u == nil || u.Session.Percent != 4 || u.Week.Percent != 6.5 || u.Week.ResetsAt != "2026-09-23T11:00:00Z" {
		t.Fatalf("usage %+v", u)
	}
	if usageFrom(json.RawMessage(`{"rate_limits_available":false,"rate_limits":null}`)) != nil {
		t.Fatal("an account without plan limits has usage")
	}
	if c := contextOf(json.RawMessage(`{"totalTokens":17124,"maxTokens":600000,"autoCompactThreshold":567000,"isAutoCompactEnabled":true}`)); c != (Context{17124, 600000, 567000}) {
		t.Fatalf("context %+v", c)
	}
	if c := contextOf(json.RawMessage(`{"totalTokens":17124,"maxTokens":600000,"autoCompactThreshold":567000,"isAutoCompactEnabled":false}`)); c.Compact != 0 {
		t.Fatalf("compacts with auto-compact off: %+v", c)
	}
	o := Options{Model: "claude-opus-5[1m]", Context: Context{Max: 600_000, Compact: 567_000}}
	if c := windowFor("claude-opus-5", o); c != (Context{0, 600_000, 567_000}) {
		t.Fatalf("the settings' model: %+v", c)
	}
	if c := windowFor("claude-haiku-4-5", o); c != (Context{0, 200_000, 167_000}) {
		t.Fatalf("another model: %+v", c)
	}
	small := Options{Model: "claude-opus-5[1m]", Context: Context{Max: 120_000, Compact: 87_000}}
	if c := windowFor("claude-haiku-4-5", small); c != (Context{0, 120_000, 87_000}) {
		t.Fatalf("a window set below every model's: %+v", c)
	}
	// Not a guess before Claude Code has been asked; after, when it would not say.
	if c := windowFor("claude-opus-5", Options{}); c.Max != 0 {
		t.Fatalf("before asking: %+v", c)
	}
	if c := windowFor("claude-opus-5", Options{Models: fallbackModels}); c.Max != 200_000 {
		t.Fatalf("not told: %+v", c)
	}
}

func TestANewSessionOutlivesARestartBeforeItsFirstMessage(t *testing.T) {
	root, cfg := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	dir := filepath.Join(cfg, "projects", folderName(root))
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "said.jsonl"), []byte(`{"type":"user","uuid":"u1","cwd":"`+root+`","message":{"role":"user","content":"hi"}}`+"\n"), 0o644)
	saved, _ := store.OpenSessions(root)
	saved.Set("said", true)
	saved.Set("unsaid", true)

	m := New(root, permit.New(root), saved)
	p, err := m.procFor("unsaid")
	if err != nil || p.resume {
		t.Fatalf("the new session after a restart: %v, resume %v", err, p != nil && p.resume)
	}
	if m.procs["said"] != nil {
		t.Fatal("a session with a transcript was made new again")
	}
}

// The effort picked for a session is what it resumes with after dv restarts:
// Claude Code's transcript keeps no record of it.
func TestAPickedEffortOutlivesARestart(t *testing.T) {
	root, cfg := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	t.Setenv("PATH", "")
	dir := filepath.Join(cfg, "projects", folderName(root))
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(`{"type":"user","uuid":"u1","cwd":"`+root+`","message":{"role":"user","content":"hi"}}`+"\n"), 0o644)
	saved, _ := store.OpenSessions(root)
	saved.Set("s", true)

	high := "high"
	if err := New(root, permit.New(root), saved).Configure("s", nil, nil, &high); err != nil {
		t.Fatal(err)
	}
	again, _ := store.OpenSessions(root)
	p, err := New(root, permit.New(root), again).procFor("s")
	if err != nil {
		t.Fatal(err)
	}
	if p.effort != "high" || !slices.Contains(p.args(), "high") {
		t.Fatalf("resumed at effort %q, with %q", p.effort, p.args())
	}
}

// Two sessions written at the same moment keep an order of their own, rather
// than trading places on every listing: the files come in no order.
func TestSessionsWrittenTogetherKeepTheirOrder(t *testing.T) {
	root, cfg := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	t.Setenv("PATH", "")
	dir := filepath.Join(cfg, "projects", folderName(root))
	os.MkdirAll(dir, 0o755)
	same := time.Now().Add(-time.Hour)
	for _, id := range []string{"cc", "aa", "bb"} {
		path := filepath.Join(dir, id+".jsonl")
		os.WriteFile(path, []byte(`{"type":"user","uuid":"u1","cwd":"`+root+`","message":{"role":"user","content":"hi"}}`+"\n"), 0o644)
		os.Chtimes(path, same, same)
	}
	saved, _ := store.OpenSessions(root)
	m := New(root, permit.New(root), saved)
	listed := func() []string {
		var ids []string
		for _, s := range m.Sessions() {
			ids = append(ids, s.ID)
		}
		return ids
	}
	want := []string{"aa", "bb", "cc"}
	for i := 0; i < 8; i++ {
		if got := listed(); !slices.Equal(got, want) {
			t.Fatalf("listing %d: %v", i, got)
		}
	}
}

func TestATemporarySessionLeavesTheListOnceClosed(t *testing.T) {
	root, cfg := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	t.Setenv("PATH", "") // no claude to ask for the options
	dir := filepath.Join(cfg, "projects", folderName(root))
	os.MkdirAll(dir, 0o755)
	for _, id := range []string{"kept", "temp"} {
		os.WriteFile(filepath.Join(dir, id+".jsonl"), []byte(`{"type":"user","uuid":"u1","cwd":"`+root+`","message":{"role":"user","content":"hi"}}`+"\n"), 0o644)
	}
	saved, _ := store.OpenSessions(root)
	m := New(root, permit.New(root), saved)
	// Marked while closed, as a past session is from the page.
	m.SetTemporary("temp", true)
	listed := func() (ids []string) {
		for _, s := range m.Sessions() {
			ids = append(ids, s.ID)
		}
		slices.Sort(ids)
		return ids
	}
	if got := listed(); !slices.Equal(got, []string{"kept", "temp"}) {
		t.Fatalf("while open %v", got)
	}
	m.SetOpen("temp", false)
	if got := listed(); !slices.Equal(got, []string{"kept"}) {
		t.Fatalf("once closed %v", got)
	}
}

func TestAKeptSessionIsKeptUntilClosed(t *testing.T) {
	root, cfg := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	t.Setenv("PATH", "") // no claude to start
	dir := filepath.Join(cfg, "projects", folderName(root))
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "s1.jsonl"), []byte(`{"type":"user","uuid":"u1","cwd":"`+root+`","message":{"role":"user","content":"hi"}}`+"\n"), 0o644)
	saved, _ := store.OpenSessions(root)
	m := New(root, permit.New(root), saved)
	kept := func() bool {
		for _, s := range m.Sessions() {
			if s.ID == "s1" {
				return s.Kept && s.Open
			}
		}
		return false
	}
	// Starting it fails here, but it stays kept, to start with dv.
	if err := m.SetKept("s1", true); err == nil {
		t.Fatal("started with no claude")
	}
	if !kept() {
		t.Fatal("not kept and open once kept")
	}
	m.SetOpen("s1", false)
	if kept() || len(saved.KeptIDs()) != 0 {
		t.Fatal("still kept once closed")
	}
}

func TestASessionKeepsItsRecordedMode(t *testing.T) {
	root, cfg := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	dir := filepath.Join(cfg, "projects", folderName(root))
	os.MkdirAll(dir, 0o755)
	first := user("u1", "", "plan it")
	first["cwd"] = root
	os.WriteFile(filepath.Join(dir, "s1.jsonl"), []byte(line(t, first)+line(t, map[string]any{"type": "permission-mode", "permissionMode": "plan"})), 0o644)
	saved, _ := store.OpenSessions(root)
	m := New(root, permit.New(root), saved)
	sub, stop := m.Follow("s1", "")
	defer stop()
	<-sub.Changed
	if u := m.Update("s1", sub); u.Live.Mode != "plan" {
		t.Fatalf("shown in %q", u.Live.Mode)
	}
	if p, err := m.procFor("s1"); err != nil || p.mode != "plan" {
		t.Fatalf("resumed in %q, %v", p.mode, err)
	}
}

// An agent's conversation is its own file, found by the call that started it;
// a call it made is looked up there, and a call left running in the background
// is followed in the file its result names.
func TestAnAgentIsFollowedInItsOwnTranscript(t *testing.T) {
	root, cfg := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", cfg)
	dir := filepath.Join(cfg, "projects", folderName(root))
	agents := filepath.Join(dir, "s1", "subagents")
	os.MkdirAll(agents, 0o755)
	output := filepath.Join(t.TempDir(), "tasks", "b1.output")
	started := map[string]any{"type": "user", "uuid": "u2", "parentUuid": "a1",
		"message":       map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_bg", "content": "Command running in background with ID: b1. Output is being written to: " + output + ". You will be notified."}}},
		"toolUseResult": map[string]any{"stdout": "", "stderr": "", "backgroundTaskId": "b1"}}
	session := line(t, user("u1", "", "look into it")) +
		line(t, assistant("a1", "u1", "msg_1", map[string]any{"type": "tool_use", "id": "toolu_agent", "name": "Agent", "input": map[string]any{"description": "Look"}})) +
		line(t, started)
	os.WriteFile(filepath.Join(dir, "s1.jsonl"), []byte(strings.ReplaceAll(session, `"cwd":"/repo"`, `"cwd":"`+root+`"`)), 0o644)

	side := func(v map[string]any) map[string]any { v["isSidechain"] = true; return v }
	answer := side(assistant("x3", "x2", "msg_y", text("It is one line.")))
	answer["effort"] = "high"
	read := map[string]any{"type": "user", "uuid": "x2", "parentUuid": "x1", "isSidechain": true,
		"message":       map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_read", "content": "package a"}}},
		"toolUseResult": map[string]any{"type": "text", "file": map[string]any{"filePath": root + "/a.go", "content": "package a", "numLines": 1, "startLine": 1, "totalLines": 1}}}
	os.WriteFile(filepath.Join(agents, "agent-x.meta.json"), []byte(`{"agentType":"Explore","toolUseId":"toolu_agent"}`), 0o644)
	os.WriteFile(filepath.Join(agents, "agent-x.jsonl"), []byte(line(t, side(user("x0", "", "Look at a.go")))+
		line(t, side(assistant("x1", "x0", "msg_x", map[string]any{"type": "tool_use", "id": "toolu_read", "name": "Read", "input": map[string]any{"file_path": root + "/a.go"}})))+
		line(t, read)+line(t, answer)), 0o644)

	saved, _ := store.OpenSessions(root)
	m := New(root, permit.New(root), saved)
	sub, stop := m.FollowAgent("s1", "toolu_agent")
	defer stop()
	<-sub.Changed
	u := m.AgentUpdate("s1", "toolu_agent", sub)
	if want := []string{"prompt:Look at a.go", "tool", "text:It is one line."}; !u.Live.Found || !slices.Equal(kinds(u.Items), want) {
		t.Fatalf("agent items %q, found %v", kinds(u.Items), u.Live.Found)
	}
	if u.Live.LastEffort != "high" {
		t.Fatalf("agent effort %q", u.Live.LastEffort)
	}
	if out, err := m.Output("s1", "toolu_read"); err != nil || out.Read == nil {
		t.Fatalf("the agent's call's output %+v, %v", out, err)
	}
	if path, err := m.TaskOutput("s1", "toolu_bg"); err != nil || path != output {
		t.Fatalf("task output %q, %v", path, err)
	}
	if _, err := m.TaskOutput("s1", "toolu_read"); err == nil {
		t.Fatal("a call that finished has no task output")
	}
}

// A turn's result can arrive after the next message went, so once Claude Code
// reports its state, only idle ends being busy.
func TestBusyUntilClaudeCodeSaysIdle(t *testing.T) {
	noop := func() {}
	p := &proc{changed: noop, wrote: noop, ended: noop, busy: true}
	p.handle([]byte(`{"type":"result","subtype":"success"}`))
	if p.busy {
		t.Fatal("without states, a result ends the turn")
	}
	p.busy = true
	p.handle([]byte(`{"type":"system","subtype":"session_state_changed","state":"running"}`))
	p.handle([]byte(`{"type":"result","subtype":"success"}`))
	if !p.busy {
		t.Fatal("a result ended a turn Claude Code still reports running")
	}
	// A turn longer than the reaper waits is idle from its end, not its start.
	p.lastUsed = time.Now().Add(-time.Hour)
	p.handle([]byte(`{"type":"system","subtype":"session_state_changed","state":"idle"}`))
	if p.busy {
		t.Fatal("still busy after idle")
	}
	if time.Since(p.lastUsed) > time.Minute {
		t.Fatal("idle since the turn began")
	}
}

// An API error comes as a message, which the transcript keeps, and again as
// the turn's result; it is shown once. Any other failed turn is the session's.
func TestAnAPIErrorIsNotSaidTwice(t *testing.T) {
	noop := func() {}
	p := &proc{changed: noop, wrote: noop, ended: noop, busy: true}
	p.handle([]byte(`{"type":"assistant","is_api_error_message":true,"error":"authentication_failed","message":{"content":[{"type":"text","text":"Not logged in · Please run /login"}]}}`))
	p.handle([]byte(`{"type":"result","subtype":"success","is_error":true,"result":"Not logged in · Please run /login"}`))
	if p.err != "" {
		t.Fatalf("error %q, which the transcript shows already", p.err)
	}
	p.handle([]byte(`{"type":"result","subtype":"error_max_turns","is_error":true,"errors":["Reached the maximum number of turns"]}`))
	if p.err == "" {
		t.Fatal("a failed turn left no error")
	}
}

// Compacting makes a request of its own, which must not read as the turn going
// on; what ends it is Claude Code saying so, or the compaction landing.
func TestCompactingUntilItIsDone(t *testing.T) {
	noop := func() {}
	p := &proc{changed: noop, wrote: noop, ended: noop, busy: true}
	p.handle([]byte(`{"type":"system","subtype":"status","status":"compacting"}`))
	p.handle([]byte(`{"type":"system","subtype":"status","status":"requesting"}`))
	if p.status != "compacting" {
		t.Fatalf("status %q while compacting", p.status)
	}
	p.handle([]byte(`{"type":"system","subtype":"compact_boundary"}`))
	if p.status != "" {
		t.Fatalf("status %q after the compaction", p.status)
	}
	p.handle([]byte(`{"type":"system","subtype":"status","status":"compacting"}`))
	p.handle([]byte(`{"type":"system","subtype":"status","status":null}`))
	if p.status != "" {
		t.Fatalf("status %q after Claude Code said it was done", p.status)
	}
}

// A queued message stays queued until Claude Code says a turn took it up, or
// it was taken back.
func TestAQueuedMessageGoesWhenATurnTakesItUp(t *testing.T) {
	noop := func() {}
	p := &proc{changed: noop, wrote: noop, ended: noop, busy: true, queued: []Queued{{UUID: "m1", Text: "one"}, {UUID: "m2", Text: "two"}}}
	p.handle([]byte(`{"type":"command_lifecycle","command_uuid":"m1","state":"queued"}`))
	if len(p.queued) != 2 {
		t.Fatalf("queued %+v", p.queued)
	}
	p.handle([]byte(`{"type":"command_lifecycle","command_uuid":"m1","state":"started"}`))
	p.handle([]byte(`{"type":"command_lifecycle","command_uuid":"m9","state":"cancelled"}`))
	if len(p.queued) != 1 || p.queued[0].UUID != "m2" {
		t.Fatalf("queued %+v", p.queued)
	}
}

// A change of permission mode is marked where the conversation first shows it,
// whether a prompt or a line of its own records it; the same mode recorded
// again, or the one it began in, is not.
func TestAModeChangeIsMarkedWhereItShows(t *testing.T) {
	planned := user("u2", "a1", "go on")
	planned["permissionMode"] = "plan"
	transcript := line(t, map[string]any{"type": "permission-mode", "permissionMode": "default"}) +
		line(t, user("u1", "", "look around")) +
		line(t, map[string]any{"type": "permission-mode", "permissionMode": "default"}) +
		line(t, assistant("a1", "u1", "msg_1", text("Looked."))) +
		line(t, planned) +
		line(t, assistant("a2", "u2", "msg_2", text("Planning."))) +
		line(t, map[string]any{"type": "permission-mode", "permissionMode": "acceptEdits"}) +
		line(t, assistant("a3", "a2", "msg_3", text("Editing.")))
	tr := newTranscript("/repo")
	tr.Feed([]byte(transcript))
	items := tr.Items("")
	want := []string{"prompt:look around", "text:Looked.", "mode:plan", "prompt:go on", "text:Planning.", "mode:acceptEdits", "text:Editing."}
	if got := kinds(items); !slices.Equal(got, want) {
		t.Fatalf("items\n got %q\nwant %q", got, want)
	}
	if items[2].From != "default" || items[5].From != "plan" {
		t.Fatalf("changed from %q, then %q", items[2].From, items[5].From)
	}
}

// A mode switched to in dv is marked where it was made, mid-turn, and not again
// when the next message records it; the entries between go on carrying the old one.
func TestASwitchIsMarkedWhereItWasMade(t *testing.T) {
	auto := user("u2", "a2", "go on")
	auto["permissionMode"] = "auto"
	first := user("u1", "", "look around")
	first["permissionMode"] = "default"
	tr := newTranscript("/repo")
	tr.Feed([]byte(line(t, first) + line(t, assistant("a1", "u1", "msg_1", text("Looking.")))))
	switches := []store.Switch{{After: "a1", To: "auto"}}
	if got := tr.Mode("", switches); got != "auto" {
		t.Fatalf("mode %q before the next message; want auto", got)
	}
	tr.Feed([]byte(line(t, assistant("a2", "a1", "msg_2", text("Looked."))) + line(t, auto)))
	items := tr.Items("", switches...)
	want := []string{"prompt:look around", "text:Looking.", "mode:auto", "text:Looked.", "prompt:go on"}
	if got := kinds(items); !slices.Equal(got, want) {
		t.Fatalf("items\n got %q\nwant %q", got, want)
	}
	if items[2].From != "default" {
		t.Fatalf("switched from %q", items[2].From)
	}
}

// The stream says the mode only as a turn starts: Claude entering plan mode,
// and a plan approved into another, are taken as they happen.
func TestTheModeFollowsPlansAsTheyHappen(t *testing.T) {
	noop := func() {}
	p := &proc{changed: noop, wrote: noop, ended: noop, mode: "default"}
	p.handle([]byte(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_1","name":"EnterPlanMode","input":{}}]}}`))
	p.handle([]byte(`{"type":"user","message":{"content":"a prompt, not blocks"}}`))
	if p.mode != "default" {
		t.Fatalf("mode %q before the call returned", p.mode)
	}
	p.handle([]byte(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"Entered plan mode."}]}}`))
	if p.mode != "plan" {
		t.Fatalf("mode %q after entering plan mode", p.mode)
	}
	d := map[string]any{"behavior": "allow", "updatedPermissions": []json.RawMessage{json.RawMessage(`{"type":"setMode","mode":"acceptEdits","destination":"session"}`)}}
	if got := modeSet(d); got != "acceptEdits" {
		t.Fatalf("approving went to %q", got)
	}
	if got := modeSet(map[string]any{"behavior": "allow"}); got != "" {
		t.Fatalf("a plain allow went to %q", got)
	}
}

// Pictures sent with a message are counted on its prompt and fetched by its
// uuid; those a tool's result carries are not something said.
func TestImagesGoWithTheirPrompt(t *testing.T) {
	png := base64.StdEncoding.EncodeToString([]byte("\x89PNG fake"))
	image := map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": png}}
	withImage := user("u1", "", "")
	withImage["message"] = map[string]any{"role": "user", "content": []any{image, text("what is this?")}}
	onlyImage := user("u2", "a1", "")
	onlyImage["message"] = map[string]any{"role": "user", "content": []any{image}}
	result := map[string]any{"type": "user", "uuid": "u3", "parentUuid": "a2",
		"message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": "toolu_1", "content": "shot"}, image}}}
	queued := map[string]any{"type": "attachment", "uuid": "q1", "parentUuid": "u3",
		"attachment": map[string]any{"type": "queued_command", "prompt": []any{image, image, text("and these")}, "commandMode": "prompt"}}
	transcript := line(t, withImage) + line(t, assistant("a1", "u1", "msg_1", text("A cat."))) + line(t, onlyImage) +
		line(t, assistant("a2", "u2", "msg_2", map[string]any{"type": "tool_use", "id": "toolu_1", "name": "Bash", "input": map[string]any{}})) +
		line(t, result) + line(t, queued)

	tr := newTranscript("/repo")
	tr.Feed([]byte(transcript))
	var got []string
	for _, it := range tr.Items("") {
		if it.Kind == "prompt" {
			got = append(got, fmt.Sprintf("%s:%q:%d", it.UUID, it.Text, it.Images))
		}
	}
	if want := []string{`u1:"what is this?":1`, `u2:"":1`, `q1:"and these":2`}; !slices.Equal(got, want) {
		t.Fatalf("prompts\n got %q\nwant %q", got, want)
	}

	path := filepath.Join(t.TempDir(), "s.jsonl")
	os.WriteFile(path, []byte(transcript), 0o644)
	img, err := promptImage(path, "q1", 1)
	if err != nil || img.MediaType != "image/png" || string(img.Data) != "\x89PNG fake" {
		t.Fatalf("image %+v, %v", img, err)
	}
	if _, err := promptImage(path, "q1", 2); err == nil {
		t.Fatal("a third image of two")
	}
}

// The transcript only branches when the next message is written, so a rewind
// has to be remembered until then - through a restart of dv - and then not
// applied a second time.
func TestARewindOutlivesARestartUntilAMessageGoes(t *testing.T) {
	root := t.TempDir()
	saved, _ := store.OpenSessions(root)
	saved.SetRewound("s1", &store.Rewind{At: "a3", Last: "a5"})

	m := New(root, permit.New(root), saved)
	p := m.newProc("s1", root, true)
	if p.resumeAt != "a3" || m.leaf("s1", "a5") != "a3" {
		t.Fatalf("after a restart: resumes at %q, shows %q", p.resumeAt, m.leaf("s1", "a5"))
	}
	p.sent()
	if again := m.newProc("s1", root, true); again.resumeAt != "" {
		t.Fatalf("a start after the message rewinds again, to %q", again.resumeAt)
	}
	if m.leaf("s1", "a5") != "a3" {
		t.Fatal("the rewound conversation went before the file moved on")
	}
	if m.leaf("s1", "u9") != "" {
		t.Fatal("the rewind held after the file moved on")
	}
	if _, ok := saved.Rewound("s1"); ok {
		t.Fatal("the rewind was not forgotten")
	}
}
