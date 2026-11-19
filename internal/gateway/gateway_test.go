package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

type events []event

func (e *events) send(ev event) error { *e = append(*e, ev); return nil }

func testMasker() *masker {
	m := &masker{}
	m.set(map[string]string{"GH": "ghp_abcdef123456", "QUOTED": `pa"ss\word`})
	return m
}

func TestMaskRequest(t *testing.T) {
	m := testMasker()
	body, _ := json.Marshal(map[string]any{"messages": []any{"token ghp_abcdef123456 and " + `pa"ss\word`}})
	got := string(m.mask(body))
	if strings.Contains(got, "ghp_abcdef") || strings.Contains(got, "word") {
		t.Fatalf("a secret went out: %s", got)
	}
	if !strings.Contains(got, Placeholder("GH")) || !strings.Contains(got, Placeholder("QUOTED")) {
		t.Fatalf("no placeholders: %s", got)
	}
}

func TestHeldFrom(t *testing.T) {
	for s, want := range map[string]int{
		"plain":                        5,
		"a [":                          2,
		"a [[dv-sec":                   2,
		"a [[dv-secret:GH":             2,
		"a [[dv-secret:GH]":            2,
		"a [[dv-secret:GH]]":           18,
		"list[0] and [x]":              15,
		"a [[dv-secret:bad name":       22,
		"x [[dv-secret:GH]] [[dv-secr": 19,
	} {
		if got := heldFrom(s); got != want {
			t.Errorf("heldFrom(%q) = %d, want %d", s, got, want)
		}
	}
}

func TestUnmaskStream(t *testing.T) {
	m := testMasker()
	var out events
	u := &unmasking{m: m, next: &out, held: map[int]*heldDelta{}}
	send := func(kind, text string) { u.send(deltaEvent(0, kind, text)) }
	send("text_delta", "use [[dv-se")
	send("text_delta", "cret:GH]] now [[dv-secret:")
	u.send(newEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}))
	send("input_json_delta", `{"pw":"[[dv-secret:QUO`)
	send("input_json_delta", `TED]]"}`)
	u.send(newEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0}))

	var text, args strings.Builder
	for _, ev := range out {
		var d struct {
			Delta struct {
				Type, Text, PartialJSON string
			}
		}
		json.Unmarshal(ev.data, &d)
		var raw map[string]map[string]string
		json.Unmarshal(ev.data, &raw)
		text.WriteString(raw["delta"]["text"])
		args.WriteString(raw["delta"]["partial_json"])
	}
	if got := text.String(); got != "use ghp_abcdef123456 now [[dv-secret:" {
		t.Errorf("text %q", got)
	}
	var input map[string]string
	if err := json.Unmarshal([]byte(args.String()), &input); err != nil || input["pw"] != `pa"ss\word` {
		t.Errorf("tool input %q: %v", args.String(), err)
	}
}

func TestCodexRequest(t *testing.T) {
	sig := encodeReasoning(reasoningItem{ID: "rs_1", Encrypted: "enc", Summary: []string{"looked"}})
	raw := `{
		"model": "codex/gpt-6-sol",
		"system": [{"type":"text","text":"You are Claude Code."}],
		"tools": [
			{"name":"Read","description":"read","input_schema":{"type":"object"}},
			{"name":"Later","input_schema":{"type":"object"},"defer_loading":true},
			{"type":"web_search_20250305","name":"web_search"}
		],
		"messages": [
			{"role":"user","content":"fix it"},
			{"role":"system","content":[{"type":"tool_addition","tool":{"type":"tool_definition","definition":{"name":"mcp__a.b__c","input_schema":{"type":"object"}}}}]},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"","signature":"` + sig + `"},
				{"type":"thinking","thinking":"claude's","signature":"EqQB"},
				{"type":"text","text":"Reading."},
				{"type":"tool_use","id":"call_1","name":"mcp__a.b__c","input":{"p":"x"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"ok"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAA"}}]},
				{"type":"text","text":"<system-reminder>x</system-reminder>"}
			]}
		],
		"thinking": {"type":"adaptive","display":"omitted"},
		"output_config": {"effort":"high"},
		"stream": true
	}`
	var req anthropicRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatal(err)
	}
	out, names := codexRequest(&req, "gpt-6-sol", "key", []string{"low", "medium", "high", "xhigh"})
	b, _ := json.Marshal(out)
	var got struct {
		Model     string
		Reasoning map[string]string
		Tools     []struct{ Type, Name string }
		Input     []map[string]any
	}
	json.Unmarshal(b, &got)
	if got.Model != "gpt-6-sol" || got.Reasoning["effort"] != "high" || got.Reasoning["summary"] != "auto" {
		t.Errorf("model %q reasoning %v", got.Model, got.Reasoning)
	}
	if len(got.Tools) != 2 || got.Tools[0].Name != "Read" || got.Tools[1].Type != "web_search" {
		t.Errorf("tools %+v", got.Tools)
	}
	var kinds []string
	for _, it := range got.Input {
		k, _ := it["type"].(string)
		if r, _ := it["role"].(string); r != "" {
			k += ":" + r
		}
		kinds = append(kinds, k)
	}
	want := "message:developer message:user additional_tools:developer reasoning message:assistant function_call function_call_output message:user"
	if strings.Join(kinds, " ") != want {
		t.Errorf("input %v, want %s", kinds, want)
	}
	loaded, _ := got.Input[2]["tools"].([]any)
	if len(loaded) != 1 || names.in(loaded[0].(map[string]any)["name"].(string)) != "mcp__a.b__c" {
		t.Errorf("loaded mid-conversation: %v", got.Input[2])
	}
	if got.Input[3]["encrypted_content"] != "enc" {
		t.Errorf("reasoning %v", got.Input[3])
	}
	if _, parts := got.Input[6]["output"].([]any); !parts {
		t.Errorf("a result with an image is parts: %v", got.Input[6]["output"])
	}
	if !continuesTurn(&req) {
		t.Error("tool results with a reminder continue the turn")
	}
}

func TestCodexStream(t *testing.T) {
	lines := []map[string]any{
		{"type": "response.created", "response": map[string]any{"id": "resp_1"}},
		{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"type": "reasoning"}},
		{"type": "response.reasoning_summary_text.delta", "output_index": 0, "delta": "thinking"},
		{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"type": "reasoning", "id": "rs_1", "encrypted_content": "enc", "summary": []any{map[string]any{"text": "thinking"}}}},
		{"type": "response.output_item.added", "output_index": 1, "item": map[string]any{"type": "message"}},
		{"type": "response.output_text.delta", "output_index": 1, "delta": "Hi "},
		{"type": "response.output_text.delta", "output_index": 1, "delta": "there"},
		{"type": "response.output_item.done", "output_index": 1, "item": map[string]any{"type": "message"}},
		{"type": "response.output_item.added", "output_index": 2, "item": map[string]any{"type": "function_call", "call_id": "call_9", "name": "Read"}},
		{"type": "response.function_call_arguments.delta", "output_index": 2, "delta": `{"file_path":`},
		{"type": "response.function_call_arguments.delta", "output_index": 2, "delta": `"a.go"}`},
		{"type": "response.output_item.done", "output_index": 2, "item": map[string]any{"type": "function_call"}},
		{"type": "response.completed", "response": map[string]any{"usage": map[string]any{"input_tokens": 1000, "output_tokens": 20, "input_tokens_details": map[string]any{"cached_tokens": 900}}}},
	}
	var sse strings.Builder
	for _, l := range lines {
		b, _ := json.Marshal(l)
		sse.WriteString("event: " + l["type"].(string) + "\ndata: " + string(b) + "\n\n")
	}
	col := &collector{}
	s := &codexStream{model: "codex/gpt-6-sol", names: &toolNames{}, out: col, blocks: map[int]*openBlock{}}
	if err := s.run(strings.NewReader(sse.String())); err != nil {
		t.Fatal(err)
	}
	msg := col.message()
	b, _ := json.Marshal(msg)
	var got struct {
		Model      string
		StopReason string `json:"stop_reason"`
		Usage      map[string]int
		Content    []struct {
			Type, Text, Thinking, Signature, ID, Name string
			Input                                     map[string]string
		}
	}
	json.Unmarshal(b, &got)
	if got.StopReason != "tool_use" || got.Usage["input_tokens"] != 100 || got.Usage["cache_read_input_tokens"] != 900 || got.Usage["output_tokens"] != 20 {
		t.Errorf("stop %q usage %v", got.StopReason, got.Usage)
	}
	if len(got.Content) != 3 {
		t.Fatalf("content %s", b)
	}
	if r, ok := decodeReasoning(got.Content[0].Signature); !ok || r.ID != "rs_1" || r.Encrypted != "enc" || got.Content[0].Thinking != "thinking" {
		t.Errorf("thinking %+v", got.Content[0])
	}
	if got.Content[1].Text != "Hi there" {
		t.Errorf("text %+v", got.Content[1])
	}
	if c := got.Content[2]; c.Type != "tool_use" || c.ID != "call_9" || c.Input["file_path"] != "a.go" {
		t.Errorf("tool use %+v", c)
	}
}

func TestEffortAndBillingLine(t *testing.T) {
	levels := []string{"none", "low", "medium", "high"}
	for want, cases := range map[string][][2]string{
		"high": {{"max", "adaptive"}, {"xhigh", "adaptive"}},
		"low":  {{"low", "adaptive"}},
		"none": {{"high", "disabled"}, {"high", ""}},
	} {
		for _, c := range cases {
			req := anthropicRequest{OutputConfig: &outputConfig{Effort: c[0]}}
			if c[1] != "" {
				req.Thinking = &struct {
					Type string `json:"type"`
				}{c[1]}
			}
			out, _ := codexRequest(&req, "m", "k", levels)
			if got := out["reasoning"].(map[string]any)["effort"]; got != want {
				t.Errorf("effort %s with thinking %q: %v, want %s", c[0], c[1], got, want)
			}
		}
	}
	sys := json.RawMessage(`[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1; cch=abc123;"},{"type":"text","text":"You are Claude Code."}]`)
	if got := instructions(sys, "m"); strings.Contains(got, "billing") || !strings.Contains(got, "You are Claude Code.") {
		t.Errorf("instructions %q", got)
	}
}

func TestPaired(t *testing.T) {
	in := []map[string]any{
		{"type": "function_call", "call_id": "a"},
		{"type": "function_call_output", "call_id": "b", "output": "x"},
	}
	var kinds []string
	for _, it := range paired(in) {
		kinds = append(kinds, it["type"].(string))
	}
	if strings.Join(kinds, " ") != "function_call function_call_output message" {
		t.Errorf("paired %v", kinds)
	}
}

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		status     int
		code, msg  string
		wantStatus int
		wantKind   string
	}{
		{0, "context_length_exceeded", "Your input exceeds the context window", 400, "invalid_request_error"},
		{0, "server_is_overloaded", "busy", 529, "overloaded_error"},
		{429, "usage_limit_reached", "limit", 429, "rate_limit_error"},
		{502, "", "response protection is unavailable", 400, "invalid_request_error"},
		{500, "", "oops", 500, "api_error"},
	} {
		e := classify(c.status, c.code, c.msg)
		if e.status != c.wantStatus || e.kind != c.wantKind {
			t.Errorf("classify(%d, %q, %q) = %d %s", c.status, c.code, c.msg, e.status, e.kind)
		}
	}
	if !strings.HasPrefix(classify(0, "context_length_exceeded", "x").message, "prompt is too long") {
		t.Error("an overflow is worded for Claude Code to compact")
	}
}

func TestCodexStore(t *testing.T) {
	home := t.TempDir()
	os.WriteFile(filepath.Join(home, "config.toml"), []byte("model = \"x\"\ncli_auth_credentials_store = \"keyring\"\n\n[features]\nsecret_auth_storage = true\n[other]\ncli_auth_credentials_store = \"file\"\n"), 0o600)
	if s := readCodexStore(home); s.mode != "keyring" || !s.encrypted {
		t.Errorf("store %+v", s)
	}
	c := &codexUpstream{home: home}
	if _, err := c.loginBytes(); err == nil || !strings.Contains(err.Error(), "encrypted") {
		t.Errorf("an encrypted store: %v", err)
	}
	os.WriteFile(filepath.Join(home, "config.toml"), []byte(""), 0o600)
	if _, err := c.loginBytes(); err != errNotSignedIn {
		t.Errorf("no auth.json: %v", err)
	}
}

func TestEnvFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env.local"), []byte("# keys\nexport API_KEY=\"sk-live-0123456789\"\nPORT=3000\nDEBUG=true\nDB_URL=postgres://u:pw@db/app # main\nSHARED=saved-value-1234\n"+
		"ENV=development\nHOST=localhost\nREGION=eu-central\nLEVEL=Information\nTOKEN=qXbRtzKmWpLv\n"), 0o600)
	os.WriteFile(filepath.Join(dir, ".env.example"), []byte("API_KEY=put-your-key-here\n"), 0o600)
	g := &Gateway{}
	saved := map[string]string{"MINE": "saved-value-1234"}
	g.saved.Store(&saved)
	Watch(func() []string { return []string{dir} }, func() bool { return true }, nil)
	defer Watch(func() []string { return nil }, func() bool { return false }, nil)
	g.refreshEnv()
	got := map[string]string{}
	for name, v := range g.mask.byName {
		got[name] = v
	}
	want := map[string]string{"MINE": "saved-value-1234", "API_KEY": "sk-live-0123456789", "DB_URL": "postgres://u:pw@db/app", "TOKEN": "qXbRtzKmWpLv"}
	if len(got) != len(want) {
		t.Fatalf("masked %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if from := g.envFromOf("API_KEY"); from != filepath.Join(filepath.Base(dir), ".env.local") {
		t.Errorf("from %q", from)
	}
}

func TestUnmasked(t *testing.T) {
	g := &Gateway{}
	saved := map[string]string{"KNOWN": "sk-known-0123456789abcdefghij"}
	g.saved.Store(&saved)
	g.remask()
	text := "deploy with sk-live-9f8e7d6c5b4a39281706aBcD and ghp_abcdefghijklmnopqrstuvwxyz0123456789 " +
		"but not sk-known-0123456789abcdefghij, commit 9c9bb1b0a1f2e3d4c5b6a7980f1e2d3c4b5a6978, " +
		"id 3f2b8c1e-4a5d-4e6f-9a8b-7c6d5e4f3a2b, path /home/user/projects/some/deep/folder/file.go, " +
		"or the word internationalizationsandlocalizations, but yes x9Kq2LmZ7vRt4WbN8cHy3JpF6sDg1QeA"
	got := g.Unmasked(text)
	want := []string{"sk-live-9f8e7d6c5b4a39281706aBcD", "ghp_abcdefghijklmnopqrstuvwxyz0123456789", "x9Kq2LmZ7vRt4WbN8cHy3JpF6sDg1QeA"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("found %q\nwant %q", got, want)
	}
}

func TestHold(t *testing.T) {
	dir := t.TempDir()
	g := &Gateway{path: filepath.Join(dir, "secrets.json"), keyDir: dir}
	Watch(func() []string { return nil }, func() bool { return false }, func() bool { return true })
	defer Watch(func() []string { return nil }, func() bool { return false }, nil)
	body := []byte(`{"system":[{"type":"text","text":"Primary working directory: /w/app\n"}],"messages":[` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/w/app/.env.local"}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"PORT=3000\nTOKEN=x9Kq2LmZ7vRt4WbN8cHy3JpF6sDg1QeA # prod\n"},{"type":"image","source":{"data":"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk"}}]}]}`)
	done := make(chan struct{})
	go func() { g.hold(t.Context(), body, "s"); close(done) }()
	var held []Held
	for i := 0; i < 100 && len(held) == 0; i++ {
		time.Sleep(10 * time.Millisecond)
		held = g.HeldNow()
	}
	if len(held) != 1 || len(held[0].Found) != 1 || held[0].Found[0].Preview != "x9Kq••••1QeA" || held[0].Found[0].Name != "TOKEN" {
		t.Fatalf("held %+v", held)
	}
	if f := held[0].Found[0]; held[0].Cwd != "/w/app" || f.Where != "What Read gave back" || f.Detail != "/w/app/.env.local" || f.Before != "TOKEN=" || f.After != " # prod" {
		t.Fatalf("where %q: %+v", held[0].Cwd, f)
	}
	if v, ok := g.HeldValue(held[0].ID, 0); !ok || v != "x9Kq2LmZ7vRt4WbN8cHy3JpF6sDg1QeA" {
		t.Fatalf("value %q", v)
	}
	if err := g.Answer(held[0].ID, []string{""}); err != nil {
		t.Fatal(err)
	}
	<-done
	// Let go once, it goes from then on without asking.
	if found := g.Unmasked(sentText(body)); len(found) != 0 {
		t.Errorf("asked again about %v", found)
	}

	other := []byte(`{"messages":[{"role":"user","content":"KEY=Zq81mXv0Lp4TnR7wYc2BsD9fHj6KgUe3"}]}`)
	if err := g.SetAsking("s", false, "Fix the build · app"); err != nil {
		t.Fatal(err)
	}
	g.hold(t.Context(), other, "s")
	if len(g.HeldNow()) != 0 || g.Asking("s") || !g.Asking("t") {
		t.Fatal("a session told not to ask was held")
	}

	// Both lists outlast dv, for Settings to undo.
	again := &Gateway{path: g.path, keyDir: dir}
	allowed, unasked := again.Exemptions()
	if len(allowed) != 1 || allowed[0].Preview != "x9Kq••••1QeA" || len(unasked) != 1 || unasked[0].Label != "Fix the build · app" {
		t.Fatalf("allowed %+v, unasked %+v", allowed, unasked)
	}
	if err := again.Forget(allowed[0].Key); err != nil {
		t.Fatal(err)
	}
	if found := again.Unmasked(sentText(body)); len(found) != 1 {
		t.Errorf("forgotten, yet not asked about: %v", found)
	}
}

// A long conversation has many tokens that read as random; a key read in one
// of its tool results is still asked about.
func TestHoldInALongConversation(t *testing.T) {
	dir := t.TempDir()
	g := &Gateway{path: filepath.Join(dir, "secrets.json"), keyDir: dir}
	Watch(func() []string { return nil }, func() bool { return false }, func() bool { return true })
	defer Watch(func() []string { return nil }, func() bool { return false }, nil)
	var earlier []string
	for i := range 8 {
		earlier = append(earlier, fmt.Sprintf(`{"role":"user","content":"id %d: Qm7xZ%dpLr4TbW9nKc2VhY6sJd8GfA3eUq"}`, i, i))
	}
	body := []byte(`{"messages":[` + strings.Join(earlier, ",") +
		`,{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"/w/.env.test"}}]}` +
		`,{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"DEPLOY_TOKEN=xtL9jGhycTAar2mar3nGJPYVDTitJYpLw7Yf # staging"}]}]}`)
	go g.hold(t.Context(), body, "s")
	var held []Held
	for i := 0; i < 100 && len(held) == 0; i++ {
		time.Sleep(10 * time.Millisecond)
		held = g.HeldNow()
	}
	if len(held) != 1 || !slices.ContainsFunc(held[0].Found, func(f Finding) bool { return f.Name == "DEPLOY_TOKEN" }) {
		t.Fatalf("held %+v", held)
	}
	g.Answer(held[0].ID, nil)
}

// A request given up while asked about - an interruption - leaves its question
// up, and the next carrying the same value waits on it rather than asking again.
func TestGivenUpIsAskedOnce(t *testing.T) {
	dir := t.TempDir()
	g := &Gateway{path: filepath.Join(dir, "secrets.json"), keyDir: dir}
	Watch(func() []string { return nil }, func() bool { return false }, func() bool { return true })
	defer Watch(func() []string { return nil }, func() bool { return false }, nil)
	body := []byte(`{"messages":[{"role":"user","content":"TOKEN=x9Kq2LmZ7vRt4WbN8cHy3JpF6sDg1QeA"}]}`)
	ctx, cancel := context.WithCancel(t.Context())
	first := make(chan struct{})
	go func() { g.hold(ctx, body, "s"); close(first) }()
	for i := 0; i < 100 && len(g.HeldNow()) == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-first
	again := make(chan struct{})
	go func() { g.hold(t.Context(), body, "s"); close(again) }()
	time.Sleep(50 * time.Millisecond)
	held := g.HeldNow()
	if len(held) != 1 {
		t.Fatalf("asked %d times", len(held))
	}
	select {
	case <-again:
		t.Fatal("the retry went without waiting")
	default:
	}
	g.Answer(held[0].ID, []string{""})
	<-again
}

// A session an open folder has is asked in it, as its permission prompts are;
// its answer settles the request, and a request settled otherwise takes the
// prompt down.
func TestHeldIsPrompted(t *testing.T) {
	dir := t.TempDir()
	g := &Gateway{path: filepath.Join(dir, "secrets.json"), keyDir: dir}
	Watch(func() []string { return nil }, func() bool { return false }, func() bool { return true })
	defer Watch(func() []string { return nil }, func() bool { return false }, nil)
	type put struct {
		ctx    context.Context
		h      Held
		answer func([]string, bool, string)
	}
	puts := make(chan put, 2)
	SetAsker(func(ctx context.Context, h Held, answer func([]string, bool, string)) bool {
		if h.Session != "mine" {
			return false
		}
		puts <- put{ctx, h, answer}
		return true
	})
	defer asker.Store(nil)

	body := []byte(`{"messages":[{"role":"user","content":"TOKEN=x9Kq2LmZ7vRt4WbN8cHy3JpF6sDg1QeA"}]}`)
	done := make(chan struct{})
	go func() { g.hold(t.Context(), body, "mine"); close(done) }()
	p := <-puts
	if held := g.HeldNow(); len(held) != 1 || !held[0].Prompted || p.h.Found[0].Name != "TOKEN" {
		t.Fatalf("held %+v", held)
	}
	p.answer([]string{"MY_TOKEN"}, false, "")
	<-done
	if v, ok := g.SecretValue("MY_TOKEN"); !ok || v != "x9Kq2LmZ7vRt4WbN8cHy3JpF6sDg1QeA" {
		t.Fatalf("masked as %q, %v", v, ok)
	}
	if p.ctx.Err() == nil {
		t.Error("the prompt is still up once answered")
	}

	other := []byte(`{"messages":[{"role":"user","content":"KEY=Zq81mXv0Lp4TnR7wYc2BsD9fHj6KgUe3"}]}`)
	go g.hold(t.Context(), other, "a terminal's")
	for i := 0; i < 100 && len(g.HeldNow()) == 0; i++ {
		time.Sleep(10 * time.Millisecond)
	}
	if held := g.HeldNow(); len(held) != 1 || held[0].Prompted {
		t.Fatalf("one no folder has is the dialog's: %+v", held)
	}
	g.Answer(g.HeldNow()[0].ID, nil)
}

func TestModelsOwnWordsAreNotAsked(t *testing.T) {
	dir := t.TempDir()
	g := &Gateway{path: filepath.Join(dir, "secrets.json"), keyDir: dir}
	Watch(func() []string { return nil }, func() bool { return false }, func() bool { return true })
	defer Watch(func() []string { return nil }, func() bool { return false }, nil)
	body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"text","text":"try KEY=Zq81mXv0Lp4TnR7wYc2BsD9fHj6KgUe3"},` +
		`{"type":"tool_use","id":"t1","name":"Edit","input":{"new_string":"x9Kq2LmZ7vRt4WbN8cHy3JpF6sDg1QeA"}}]}]}`)
	g.hold(t.Context(), body, "s")
	if len(g.HeldNow()) != 0 {
		t.Fatalf("asked about the model's own: %+v", g.HeldNow())
	}
}

func TestNotRandom(t *testing.T) {
	for _, s := range []string{
		"npm/_logs/2026-11-19T14_29_58_022Z-debug-0",
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk",
	} {
		if random(s) {
			t.Errorf("%s taken for a key", s)
		}
	}
	if !random("xtL9jGhycTAar2mar3nGJPYVDTitJYpLw7Yf") {
		t.Error("a key not taken for one")
	}
}

func TestNoExemptsIsAnEmptyList(t *testing.T) {
	dir := t.TempDir()
	g := &Gateway{path: filepath.Join(dir, "secrets.json"), keyDir: dir}
	allowed, unasked := g.Exemptions()
	b, _ := json.Marshal(map[string]any{"allowed": allowed, "unasked": unasked})
	if string(b) != `{"allowed":[],"unasked":[]}` {
		t.Fatalf("%s", b)
	}
}

func TestExemptsOfOld(t *testing.T) {
	path := filepath.Join(t.TempDir(), "allowed-secrets.json")
	os.WriteFile(path, []byte(`["abc","def"]`), 0o600)
	if set := readExempts(path); len(set) != 2 || set["abc"].Key != "abc" {
		t.Fatalf("%+v", set)
	}
}

func TestStripCodexReasoning(t *testing.T) {
	sig := encodeReasoning(reasoningItem{ID: "rs_1", Encrypted: "enc"})
	body := `{"model":"claude-opus-5-5","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"` + sig + `"},{"type":"text","text":"done"}]}]}`
	got := string(stripCodexReasoning([]byte(body)))
	if strings.Contains(got, reasoningSig) || !strings.Contains(got, `"done"`) {
		t.Fatalf("got %s", got)
	}
}
