package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dv/internal/permit"
	"dv/internal/store"
)

// fakeAgy stands in for `agy acp`: the test binary run again, by a script
// in agy's place, speaking enough of ACP to run a session's turns.
func TestFakeAgy(t *testing.T) {
	if os.Getenv("DV_FAKE_AGY") == "" {
		t.Skip("run by the Antigravity tests in agy's place")
	}
	runFakeAgy()
	os.Exit(0)
}

func runFakeAgy() {
	log, _ := os.OpenFile(os.Getenv("DV_FAKE_AGY_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	var wmu sync.Mutex
	send := func(v any) {
		b, _ := json.Marshal(v)
		wmu.Lock()
		os.Stdout.Write(append(b, '\n'))
		wmu.Unlock()
	}
	model, thinking, mode := "gemini-3.8-flash", "high", "default"
	options := func() []any {
		return []any{
			map[string]any{"id": "model", "name": "Model", "category": "model", "type": "select", "currentValue": model, "options": []any{
				map[string]any{"value": "gemini-3.8-flash", "name": "Gemini 3.8 Flash"},
				map[string]any{"value": "gemini-4-neon", "name": "Gemini 4 Neon"},
			}},
			map[string]any{"id": "thinking_level", "name": "Thinking", "category": "thought_level", "type": "select", "currentValue": thinking, "options": []any{
				map[string]any{"value": "low", "name": "Low"}, map[string]any{"value": "high", "name": "High"},
			}},
			map[string]any{"id": "mode", "name": "Mode", "category": "mode", "type": "select", "currentValue": mode, "options": []any{
				map[string]any{"value": "default", "name": "Default"}, map[string]any{"value": "accept-edits", "name": "Accept edits"},
				map[string]any{"value": "auto", "name": "Auto"}, map[string]any{"value": "plan", "name": "Plan"},
			}},
		}
	}
	update := func(u map[string]any) {
		send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "agy-1", "update": u}})
	}
	var amu sync.Mutex
	answers := map[string]chan json.RawMessage{}
	next := 100
	ask := func(params any) json.RawMessage {
		amu.Lock()
		next++
		id := fmt.Sprint(next)
		ch := make(chan json.RawMessage, 1)
		answers[id] = ch
		amu.Unlock()
		send(map[string]any{"jsonrpc": "2.0", "id": next, "method": "session/request_permission", "params": params})
		return <-ch
	}
	cwd, _ := os.Getwd()

	rd := bufio.NewReaderSize(os.Stdin, 1<<20)
	for {
		line, err := rd.ReadBytes('\n')
		if err != nil {
			return
		}
		var m struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
		}
		json.Unmarshal(line, &m)
		if m.Method == "" {
			amu.Lock()
			ch := answers[string(m.ID)]
			amu.Unlock()
			if ch != nil {
				ch <- m.Result
			}
			continue
		}
		fmt.Fprintln(log, m.Method, string(m.Params))
		reply := func(result any) {
			send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
		}
		switch m.Method {
		case "initialize":
			reply(map[string]any{"protocolVersion": 1, "agentInfo": map[string]any{"name": "Antigravity ACP Server", "version": "1.5.12"},
				"agentCapabilities": map[string]any{"loadSession": true, "promptCapabilities": map[string]any{"image": true}, "sessionCapabilities": map[string]any{"resume": map[string]any{}}}})
		case "authenticate":
			if os.Getenv("DV_FAKE_AGY_SIGNED_OUT") != "" {
				send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32603, "message": "No browser is available here (no graphical display is available to open a browser). Run `agy acp --login` in a terminal on this machine, then retry; or use a Gemini API key."}})
				continue
			}
			reply(map[string]any{})
		case "session/new":
			reply(map[string]any{"sessionId": "agy-1", "configOptions": options()})
		case "session/resume":
			var p struct{ SessionID string }
			json.Unmarshal(m.Params, &p)
			if p.SessionID != "agy-1" {
				send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32603, "message": "conversation not found"}})
				continue
			}
			reply(map[string]any{"configOptions": options()})
		case "session/set_config_option":
			var p struct{ ConfigID, Value string }
			json.Unmarshal(m.Params, &p)
			switch p.ConfigID {
			case "model":
				model = p.Value
			case "thinking_level":
				thinking = p.Value
			case "mode":
				mode = p.Value
			}
			reply(map[string]any{"configOptions": options()})
		case "session/prompt":
			var p struct {
				Prompt []struct{ Text string } `json:"prompt"`
			}
			json.Unmarshal(m.Params, &p)
			text := ""
			if len(p.Prompt) > 0 {
				text = p.Prompt[0].Text
			}
			go func(id json.RawMessage) {
				defer send(map[string]any{"jsonrpc": "2.0", "id": id, "result": map[string]any{"stopReason": "end_turn"}})
				if text == "question" {
					a := ask(map[string]any{"sessionId": "agy-1", "toolCall": map[string]any{"toolCallId": "interaction_1", "title": "Which one?"},
						"options": []any{map[string]any{"optionId": "1", "name": "Red", "kind": "allow_once"}, map[string]any{"optionId": "2", "name": "Blue", "kind": "allow_once"}}})
					update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Picked " + string(a)}})
					return
				}
				update(map[string]any{"sessionUpdate": "agent_thought_chunk", "content": map[string]any{"type": "text", "text": "Thinking it over"}})
				update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Hello "}})
				update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "there"}})
				update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "cmd-1", "title": "Running run_command", "kind": "execute", "status": "in_progress",
					"rawInput": map[string]any{"CommandLine": "echo hi", "Cwd": cwd}})
				update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "cmd-1", "status": "completed",
					"content": []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": "hi"}}}, "rawOutput": map[string]any{"exit_code": 0}})
				file := filepath.Join(cwd, "x.txt")
				a := ask(map[string]any{"sessionId": "agy-1", "toolCall": map[string]any{"toolCallId": "edit-1", "title": "Run edit_file?", "kind": "edit", "status": "pending",
					"content":   []any{map[string]any{"type": "diff", "path": file, "oldText": "two", "newText": "TWO"}},
					"locations": []any{map[string]any{"path": file}}},
					"options": []any{map[string]any{"optionId": "always", "name": "Allow always", "kind": "allow_always"},
						map[string]any{"optionId": "once", "name": "Allow", "kind": "allow_once"}, map[string]any{"optionId": "no", "name": "Deny", "kind": "reject_once"}}})
				if strings.Contains(string(a), `"once"`) {
					os.WriteFile(file, []byte("one\nTWO\nthree\n"), 0o644)
					update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "edit-1", "title": "Running edit_file", "status": "in_progress", "rawInput": map[string]any{"file_path": file}})
					update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "edit-1", "status": "completed", "rawOutput": "Tool execution"})
				} else {
					update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "edit-1", "status": "failed", "rawOutput": map[string]any{"error": "denied"}})
				}
				update(map[string]any{"sessionUpdate": "plan", "entries": []any{map[string]any{"content": "Say hello", "status": "completed", "priority": "high"}}})
				update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "Done."}})
				update(map[string]any{"sessionUpdate": "usage_update", "used": 9000, "size": 1048576})
			}(m.ID)
		case "session/cancel":
		default:
			send(map[string]any{"jsonrpc": "2.0", "id": m.ID, "error": map[string]any{"code": -32601, "message": "no " + m.Method}})
		}
	}
}

// useFakeAgy puts the fake in agy's place, and dv's data in a folder of the
// test's; it returns the log of what the fake was asked.
func useFakeAgy(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "asked.log")
	script := filepath.Join(dir, "agy")
	exe, _ := os.Executable()
	os.WriteFile(script, []byte(fmt.Sprintf("#!/bin/sh\nDV_FAKE_AGY=1 DV_FAKE_AGY_LOG=%q exec %q -test.run='^TestFakeAgy$'\n", logPath, exe)), 0o755)
	agyBinary = script
	t.Cleanup(func() { agyBinary = "" })
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(dir, "claude"))
	agyState.Lock()
	agyState.loaded, agyState.cat, agyState.probing, agyState.signIn, agyState.err = true, agyCatalog{Checked: time.Now()}, false, false, ""
	agyState.Unlock()
	return logPath
}

// answerAll answers what the session asks: yes to calls, the first choice to
// questions.
func answerAll(t *testing.T, b *permit.Broker) func() {
	stop := make(chan struct{})
	go func() {
		ch, done := b.Watch()
		defer done()
		for {
			for _, r := range b.Waiting() {
				a := permit.Answer{Allow: true}
				if r.Tool == "AskUserQuestion" {
					a.Answers = map[string]string{"Which one?": "Blue"}
				}
				b.Answer(r.ID, a)
			}
			select {
			case <-ch:
			case <-stop:
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()
	return func() { close(stop) }
}

func waitIdle(t *testing.T, m *Manager, id string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		m.mu.Lock()
		p := m.procs[id]
		m.mu.Unlock()
		p.mu.Lock()
		busy, err := p.busy, p.err
		p.mu.Unlock()
		if err != "" {
			t.Fatalf("the session failed: %s", err)
		}
		if !busy {
			return
		}
	}
	t.Fatal("the turn never ended")
}

func TestAnAntigravitySessionRunsAndResumes(t *testing.T) {
	asked := useFakeAgy(t)
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "x.txt"), []byte("one\ntwo\nthree\n"), 0o644)
	saved, _ := store.OpenSessions(root)
	broker := permit.New(root)
	defer answerAll(t, broker)()
	m := New(root, broker, saved)
	defer m.Close()

	id, _ := m.Create()
	model := "agy/gemini-4-neon"
	if err := m.Configure(id, &model, nil, nil); err != nil {
		t.Fatal(err)
	}
	if saved.Agent(id) != "agy" {
		t.Fatal("not recorded as agy's")
	}
	if _, err := m.Send(id, "", "do it", nil); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m, id)

	items := m.items(id)
	var kinds []string
	var bash, edit *Item
	for i, it := range items {
		kinds = append(kinds, it.Kind+":"+it.Tool+it.Text)
		switch it.Tool {
		case "Bash":
			bash = &items[i]
		case "Edit":
			edit = &items[i]
		}
	}
	got := strings.Join(kinds, " | ")
	for _, want := range []string{"prompt:do it", "thinking:Thinking it over", "text:Hello there", "tool:Bash", "tool:Edit", "tool:TodoWrite", "text:Done."} {
		if !strings.Contains(got, want) {
			t.Fatalf("no %q in %s", want, got)
		}
	}
	if bash == nil || bash.Result == nil || bash.Result.Text != "hi" {
		t.Fatalf("the command: %+v", bash)
	}
	if edit == nil || edit.Result == nil || edit.Result.Edited == nil || edit.Result.Edited.Adds != 1 || edit.Result.Edited.Dels != 1 {
		t.Fatalf("the edit: %+v", edit)
	}
	if d, err := m.Edit(id, edit.ToolID); err != nil || d.Diff == nil || len(d.Diff.NewLines) != 3 {
		t.Fatalf("the edit's diff: %+v %v", d, err)
	}

	rows := m.Sessions()
	if len(rows) != 1 || rows[0].Agent != "agy" || rows[0].Prompt != "do it" {
		t.Fatalf("the row: %+v", rows)
	}
	if err := m.switchAgent(id, m.procs[id], false); err == nil {
		t.Fatal("an Antigravity session moved to Claude Code")
	}

	// dv restarts: the session resumes agy's conversation by the id it kept.
	m.Close()
	m2 := New(root, broker, saved)
	defer m2.Close()
	if _, err := m2.Send(id, "", "question", nil); err != nil {
		t.Fatal(err)
	}
	waitIdle(t, m2, id)
	b, _ := os.ReadFile(asked)
	if !strings.Contains(string(b), `session/resume {"cwd"`) || !strings.Contains(string(b), `"sessionId":"agy-1"`) {
		t.Fatalf("not resumed:\n%s", b)
	}
	if strings.Count(string(b), "session/new") != 1 {
		t.Fatalf("a new session on resume:\n%s", b)
	}
	last := m2.items(id)
	var reply Item
	for _, it := range last {
		if it.Kind == "text" {
			reply = it
		}
	}
	if !strings.Contains(reply.Text, "Picked") || !strings.Contains(reply.Text, `"2"`) {
		t.Fatalf("the question's answer: %+v", reply)
	}
	if last[len(last)-1].Kind != "worked" {
		t.Fatalf("the turn's end: %+v", last[len(last)-1])
	}
	if !strings.Contains(string(b), `"value":"gemini-4-neon"`) {
		t.Fatalf("the model was not set:\n%s", b)
	}
}

func TestPatchOfCountsTheChange(t *testing.T) {
	h := patchOf("a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n", "a\nb\nC\nd\ne\nf\ng\nh\nI\nj\n")
	if len(h) != 1 {
		t.Fatalf("hunks: %+v", h)
	}
	adds, dels := 0, 0
	for _, l := range h[0].Lines {
		switch l[0] {
		case '+':
			adds++
		case '-':
			dels++
		}
	}
	if adds != 2 || dels != 2 || h[0].OldStart != 1 {
		t.Fatalf("hunk: %+v", h[0])
	}
	far := patchOf(strings.Repeat("x\n", 20)+"a\n", "A\n"+strings.Repeat("x\n", 20)+"b\n")
	if len(far) != 2 {
		t.Fatalf("two changes far apart: %+v", far)
	}
}

func TestAgyCallsAreDrawnAsClaudeCodesTools(t *testing.T) {
	call := func(title, kind string, in map[string]any) (string, map[string]any) {
		c := &agyCall{title: title, kind: kind, input: in}
		return c.claude()
	}
	if tool, in := call("Run run_command?", "execute", map[string]any{"CommandLine": "ls"}); tool != "Bash" || in["command"] != "ls" {
		t.Fatal(tool, in)
	}
	if tool, in := call("Running view_file", "read", map[string]any{"file_path": "/a", "start_line": 3.0, "end_line": 5.0}); tool != "Read" || in["offset"] != 3 || in["limit"] != 3 {
		t.Fatal(tool, in)
	}
	if tool, in := call("Run write_to_file?", "edit", map[string]any{"TargetFile": "/a", "CodeContent": "x"}); tool != "Write" || in["content"] != "x" {
		t.Fatal(tool, in)
	}
	if tool, in := call("Running invoke_subagent", "other", map[string]any{"TypeName": "research", "Role": "Researcher", "Prompt": "look"}); tool != "Agent" || in["description"] != "Researcher" {
		t.Fatal(tool, in)
	}
	if tool, _ := call("Run schedule", "other", map[string]any{"DurationSeconds": 30.0}); tool != "schedule" {
		t.Fatal(tool)
	}
}

// As agy 1.5.14 reports a command: its output under a header of its own.
func TestAgyCommandOutputLosesItsHeader(t *testing.T) {
	run := func(text string) *agyResult {
		c := &agyCall{status: "completed", tool: "Bash", text: []string{text}}
		return c.result()
	}
	r := run("Created At: 2026-11-22T13:30:07+05:30\nCompleted At: 2026-11-22T13:30:36+05:30\nCommand exited with code 0.\n\nCommand output:\ndv-agy-check\r\n")
	if r.text != "dv-agy-check" || r.failed {
		t.Fatalf("%+v", r)
	}
	r = run("Created At: x\nCompleted At: y\nCommand exited with code 2.\n\nCommand output:\nno such file\r\n")
	if r.text != "Exit code 2\nno such file" || !r.failed {
		t.Fatalf("%+v", r)
	}
}

func TestAgyRefusesAnOldCLI(t *testing.T) {
	for v, old := range map[string]bool{"1.5.12": false, "agy_acp_server_1.1.1": true, "1.4.9": true, "2.0.0": false} {
		if agyTooOld(v) != old {
			t.Errorf("%s: old %v", v, !old)
		}
	}
}

func TestAgyModelsAreHeardFromAProbe(t *testing.T) {
	useFakeAgy(t)
	AgyStatus(true)
	deadline := time.Now().Add(20 * time.Second)
	for AgyStatus(false).Probing && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if st := AgyStatus(false); st.Models != 2 || st.Version != "1.5.12" || st.Error != "" || st.SignIn {
		t.Fatalf("status: %+v", st)
	}
	models := AgyModels()
	if len(models) != 2 || models[1].ID != "agy/gemini-4-neon" || models[1].Source != "Antigravity" || len(models[1].Efforts) != 2 || models[1].Effort != "high" {
		t.Fatalf("models: %+v", models)
	}
	AgyOff = func() bool { return true }
	defer func() { AgyOff = nil }()
	if AgyModels() != nil {
		t.Fatal("listed while switched off")
	}
}

func TestAnAgyWithNoSignInSaysSo(t *testing.T) {
	useFakeAgy(t)
	t.Setenv("DV_FAKE_AGY_SIGNED_OUT", "1")
	root := t.TempDir()
	saved, _ := store.OpenSessions(root)
	m := New(root, permit.New(root), saved)
	defer m.Close()
	id, _ := m.Create()
	model := "agy/gemini-3.8-flash"
	m.Configure(id, &model, nil, nil)
	if _, err := m.Send(id, "", "hi", nil); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
		p := m.procs[id]
		p.mu.Lock()
		e, busy := p.err, p.busy
		p.mu.Unlock()
		if e != "" && !busy {
			if !strings.Contains(e, "isn't signed in") {
				t.Fatalf("the error: %s", e)
			}
			return
		}
	}
	t.Fatal("never said it isn't signed in")
}

// Looking at a new session starts agy, which opens its session; nothing is
// said yet, so there is no transcript, and the session can still move.
func TestAnAgySessionLookedAtIsNotStarted(t *testing.T) {
	useFakeAgy(t)
	root := t.TempDir()
	saved, _ := store.OpenSessions(root)
	m := New(root, permit.New(root), saved)
	defer m.Close()
	id, _ := m.Create()
	model := "agy/gemini-3.8-flash"
	m.Configure(id, &model, nil, nil)
	if err := m.Start(id); err != nil {
		t.Fatal(err)
	}
	<-m.procs[id].agy.ready
	if path := transcriptFiles(root)[id]; path != "" {
		t.Fatalf("a transcript with nothing said: %s", path)
	}
	claude := "claude-opus-5"
	if err := m.Configure(id, &claude, nil, nil); err != nil {
		t.Fatalf("a session with nothing said kept from Claude Code: %v", err)
	}
	if saved.Agent(id) != "" || m.procs[id].agyKind {
		t.Fatal("still agy's")
	}
}
