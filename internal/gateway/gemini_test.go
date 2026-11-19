package gateway

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGeminiRequest(t *testing.T) {
	sig := encodeThought(thoughtStep{Signature: "EpoG", Summary: []string{"plan"}})
	body := `{"model":"gemini/gemini-4-neon","system":[{"type":"text","text":"x-anthropic-billing-header: cch=1;"},{"type":"text","text":"You are Claude Code."}],
	"thinking":{"type":"adaptive"},"output_config":{"effort":"max"},
	"tools":[{"name":"Read","description":"read","input_schema":{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{"file_path":{"type":"string"}}}},
		{"name":"mcp__later","defer_loading":true,"input_schema":{"type":"object"}},{"type":"web_search_20250305","name":"web_search"}],
	"messages":[
		{"role":"user","content":[{"type":"text","text":"read a.go"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBO"}}]},
		{"role":"assistant","content":[{"type":"thinking","thinking":"plan","signature":"` + sig + `"},{"type":"text","text":"Reading."},{"type":"tool_use","id":"c1","name":"Read","input":{"file_path":"a.go"}},{"type":"tool_use","id":"c2","name":"Read","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"c1","content":[{"type":"text","text":"package a"},{"type":"tool_reference","tool_name":"mcp__later"}]},{"type":"text","text":"thanks"}]}
	]}`
	var req anthropicRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	levels, _ := geminiLevels("gemini-4-neon")
	out, _ := geminiRequest(&req, "gemini-4-neon", levels)
	b, _ := json.Marshal(out)
	var got struct {
		System string `json:"system_instruction"`
		Store  bool
		Config map[string]string `json:"generation_config"`
		Tools  []map[string]any
		Input  []map[string]any
	}
	json.Unmarshal(b, &got)
	if strings.Contains(got.System, "billing") || !strings.Contains(got.System, "a Google model") || !strings.Contains(got.System, "You are Claude Code.") {
		t.Errorf("system %q", got.System)
	}
	if got.Store || got.Config["thinking_level"] != "xhigh" || got.Config["thinking_summaries"] != "auto" {
		t.Errorf("store %v config %v", got.Store, got.Config)
	}
	var tools []string
	for _, tl := range got.Tools {
		tools = append(tools, fmt.Sprint(tl["type"], ":", tl["name"]))
		if p, _ := tl["parameters"].(map[string]any); p != nil && p["$schema"] != nil {
			t.Errorf("$schema left in %v", p)
		}
	}
	if s := strings.Join(tools, " "); s != "function:Read function:mcp__later google_search:<nil>" {
		t.Errorf("tools %s", s)
	}
	var kinds []string
	for _, it := range got.Input {
		kinds = append(kinds, it["type"].(string))
	}
	if s := strings.Join(kinds, " "); s != "user_input thought model_output function_call function_call function_result function_result user_input" {
		t.Fatalf("input %s\n%s", s, b)
	}
	if th := got.Input[1]; th["signature"] != "EpoG" {
		t.Errorf("thought %v", th)
	}
	if img := got.Input[0]["content"].([]any)[1].(map[string]any); img["type"] != "image" || img["data"] != "iVBO" || img["mime_type"] != "image/png" {
		t.Errorf("image %v", img)
	}
	if r := got.Input[6]; r["call_id"] != "c1" || r["name"] != "Read" || !strings.Contains(r["result"].(string), "Tool loaded: mcp__later") {
		t.Errorf("result %v", r)
	}
	if r := got.Input[5]; r["call_id"] != "c2" || !strings.Contains(r["result"].(string), "not run") {
		t.Errorf("a call left without a result gets one: %v", r)
	}

	req.Thinking = nil
	out, _ = geminiRequest(&req, "gemini-3.6-flash", []string{"minimal", "low", "medium", "high"})
	if c := out["generation_config"].(map[string]any); c["thinking_level"] != "minimal" || c["thinking_summaries"] != "none" {
		t.Errorf("a request without thinking runs at the least: %v", c)
	}
}

func TestGeminiLevels(t *testing.T) {
	if l, d := geminiLevels("gemini-3.1-pro-preview-customtools"); strings.Join(l, ",") != "low,medium,high" || d != "high" {
		t.Errorf("a variant takes its model's: %v %q", l, d)
	}
	if l, _ := geminiLevels("gemini-9-new"); strings.Join(l, ",") != "low,medium,high" {
		t.Errorf("unknown %v", l)
	}
	if got := fitEffort("max", []string{"low", "medium", "high"}); got != "high" {
		t.Errorf("max fits %s", got)
	}
}

// geminiSSE is a stream as the Interactions API sends one.
func geminiSSE(evs ...map[string]any) string {
	var sse strings.Builder
	for _, ev := range evs {
		b, _ := json.Marshal(ev)
		sse.WriteString("event: " + ev["event_type"].(string) + "\ndata: " + string(b) + "\n\n")
	}
	return sse.String() + "event: done\ndata: [DONE]\n\n"
}

var geminiTurn = geminiSSE(
	map[string]any{"event_type": "interaction.created", "interaction": map[string]any{"id": "v1_1", "status": "in_progress"}},
	map[string]any{"event_type": "step.start", "index": 0, "step": map[string]any{"type": "thought", "signature": "", "summary": []any{map[string]any{"type": "text", "text": "**Plan** "}}}},
	map[string]any{"event_type": "step.delta", "index": 0, "delta": map[string]any{"type": "thought_summary", "content": map[string]any{"type": "text", "text": "read it"}}},
	map[string]any{"event_type": "step.delta", "index": 0, "delta": map[string]any{"type": "thought_signature", "signature": "EpoG"}},
	map[string]any{"event_type": "step.stop", "index": 0},
	map[string]any{"event_type": "step.start", "index": 1, "step": map[string]any{"type": "model_output", "content": []any{map[string]any{"type": "text", "text": "Hi "}}}},
	map[string]any{"event_type": "step.delta", "index": 1, "delta": map[string]any{"type": "text", "text": "there"}},
	map[string]any{"event_type": "step.stop", "index": 1},
	map[string]any{"event_type": "step.start", "index": 2, "step": map[string]any{"type": "function_call", "id": "un6k", "name": "Bash", "arguments": map[string]any{}}},
	map[string]any{"event_type": "step.delta", "index": 2, "delta": map[string]any{"type": "arguments_delta", "arguments": `{"command":`}},
	map[string]any{"event_type": "step.delta", "index": 2, "delta": map[string]any{"type": "arguments_delta", "arguments": `"ls"}`}},
	map[string]any{"event_type": "step.stop", "index": 2},
	map[string]any{"event_type": "interaction.completed", "interaction": map[string]any{"id": "v1_1", "status": "requires_action",
		"usage": map[string]any{"total_input_tokens": 1000, "total_cached_tokens": 900, "total_output_tokens": 20, "total_thought_tokens": 30}}},
)

func TestGeminiStream(t *testing.T) {
	col := &collector{}
	s := &geminiStream{model: "gemini/gemini-4-neon", names: &toolNames{}, out: col, steps: map[int]*geminiStep{}}
	if err := s.run(strings.NewReader(geminiTurn)); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(col.message())
	var got struct {
		ID         string
		StopReason string `json:"stop_reason"`
		Usage      map[string]int
		Content    []struct {
			Type, Text, Thinking, Signature, ID, Name string
			Input                                     map[string]string
		}
	}
	json.Unmarshal(b, &got)
	if got.ID != "msg_v1_1" || got.StopReason != "tool_use" || got.Usage["input_tokens"] != 100 || got.Usage["cache_read_input_tokens"] != 900 || got.Usage["output_tokens"] != 50 {
		t.Errorf("id %q stop %q usage %v", got.ID, got.StopReason, got.Usage)
	}
	if len(got.Content) != 3 {
		t.Fatalf("content %s", b)
	}
	if th, ok := decodeThought(got.Content[0].Signature); !ok || th.Signature != "EpoG" || got.Content[0].Thinking != "**Plan** read it" || th.Summary[0] != "**Plan** read it" {
		t.Errorf("thinking %+v", got.Content[0])
	}
	if got.Content[1].Text != "Hi there" {
		t.Errorf("text %+v", got.Content[1])
	}
	if c := got.Content[2]; c.Type != "tool_use" || c.ID != "un6k" || c.Name != "Bash" || c.Input["command"] != "ls" {
		t.Errorf("tool use %+v", c)
	}
}

func TestGeminiSearchAndCutShort(t *testing.T) {
	col := &collector{}
	s := &geminiStream{names: &toolNames{}, out: col, steps: map[int]*geminiStep{}}
	s.run(strings.NewReader(geminiSSE(
		map[string]any{"event_type": "step.start", "index": 0, "step": map[string]any{"type": "google_search_call", "id": "search_1", "arguments": map[string]any{"queries": []string{"dv release"}}}},
		map[string]any{"event_type": "step.stop", "index": 0},
		map[string]any{"event_type": "step.start", "index": 1, "step": map[string]any{"type": "google_search_result", "call_id": "search_1", "result": []any{map[string]any{"title": "Notes", "url": "https://x.example/n"}}}},
		map[string]any{"event_type": "step.stop", "index": 1},
		map[string]any{"event_type": "interaction.completed", "interaction": map[string]any{"status": "incomplete"}},
	)))
	msg := col.message()
	content := msg["content"].([]map[string]any)
	if len(content) != 2 || content[0]["type"] != "server_tool_use" || content[0]["input"].(map[string]any)["query"] != "dv release" || msg["stop_reason"] != "max_tokens" {
		t.Errorf("search %v", msg)
	}

	col = &collector{}
	s = &geminiStream{names: &toolNames{}, out: col, steps: map[int]*geminiStep{}}
	s.run(strings.NewReader(`event: error` + "\n" + `data: {"error":{"message":"The input token count (2000000) exceeds the maximum number of tokens allowed (1048576).","code":"invalid_request"},"event_type":"error"}` + "\n\n"))
	if col.failed == nil || !strings.HasPrefix(col.failed.message, "prompt is too long") {
		t.Errorf("an overflow is worded for Claude Code to compact: %+v", col.failed)
	}
}

// fakeGemini is the Interactions API, answering a turn with stream and a
// judge's question with answer.
func fakeGemini(t *testing.T, stream, answer string, seen *[]map[string]any) *geminiUpstream {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "k" {
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"code":"authentication","message":"API key not valid."}}`)
			return
		}
		if r.URL.Path == "/models" {
			io.WriteString(w, `{"models":[{"name":"models/gemini-2.5-pro","inputTokenLimit":1048576},{"name":"models/gemini-3.8-flash","displayName":"Gemini 3.8 Flash","inputTokenLimit":1048576},
				{"name":"models/gemini-4-neon","displayName":"Gemini 4 Neon","inputTokenLimit":1048576},{"name":"models/gemini-3.8-flash-tts"},{"name":"models/gemini-embedding-2"}]}`)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		*seen = append(*seen, body)
		if body["stream"] == true {
			io.WriteString(w, stream)
			return
		}
		b, _ := json.Marshal(map[string]any{"steps": []any{map[string]any{"type": "model_output", "content": []any{map[string]any{"type": "text", "text": answer}}}}})
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	g := newGeminiUpstream(func() string { return "k" })
	g.base, g.client = srv.URL, srv.Client()
	return g
}

func TestGeminiServe(t *testing.T) {
	var seen []map[string]any
	g := fakeGemini(t, geminiTurn, `{"verdicts":[{"id":"un6k","flagged":true,"reason":"lists files"}]}`, &seen)
	body := `{"model":"gemini/gemini-4-neon","stream":true,"messages":[{"role":"user","content":"list the files"}],"safeguards":[{"type":"dangerous_tool_use"}]}`
	rec := httptest.NewRecorder()
	g.serve(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)), []byte(body), &masker{})
	out := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(out, `"stop_reason":"tool_use"`) || !strings.Contains(out, `"outcome":"flagged"`) {
		t.Fatalf("%d %s", rec.Code, out)
	}
	if len(seen) != 2 || seen[0]["model"] != "gemini-4-neon" || seen[1]["response_format"] == nil {
		t.Errorf("requests %v", seen)
	}

	none := newGeminiUpstream(func() string { return "" })
	rec = httptest.NewRecorder()
	none.serve(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)), []byte(body), &masker{})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), GeminiKeyName) {
		t.Errorf("no key: %d %s", rec.Code, rec.Body)
	}

	bad := fakeGemini(t, "", "", &seen)
	bad.key = func() string { return "wrong" }
	rec = httptest.NewRecorder()
	bad.serve(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)), []byte(body), &masker{})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "API key not valid") || rec.Header().Get("x-should-retry") != "false" {
		t.Errorf("a refused key: %d %s", rec.Code, rec.Body)
	}
}

func TestGeminiModels(t *testing.T) {
	g := fakeGemini(t, "", "", new([]map[string]any))
	models, err := g.fetchModels(t.Context(), "k")
	if err != nil {
		t.Fatal(err)
	}
	var slugs []string
	for _, m := range models {
		slugs = append(slugs, m.Slug)
	}
	if s := strings.Join(slugs, " "); s != "gemini-4-neon gemini-3.8-flash" {
		t.Errorf("models %s", s)
	}
	if m := models[0]; m.Window != 1048576 || strings.Join(m.Efforts, ",") != "low,medium,high,xhigh" || m.Default != "high" || m.Label != "Gemini 4 Neon" {
		t.Errorf("neon %+v", m)
	}
	if _, err := g.fetchModels(t.Context(), "wrong"); err == nil || !strings.Contains(err.Error(), "API key not valid") {
		t.Errorf("a refused key says so: %v", err)
	}
}

// The cases Magpie met on Google's endpoints, by its notes.
func TestGeminiGuards(t *testing.T) {
	names := &toolNames{letter: true}
	out := names.out("1password__read")
	if !startsWord(out) || names.in(out) != "1password__read" || names.in("default_api:"+out) != "1password__read" {
		t.Errorf("a name starts with a letter, and comes back: %q", out)
	}

	schema := map[string]any{"type": "object", "x-mcp-header": "h", "properties": map[string]any{
		"grid":  map[string]any{"type": "array", "items": map[string]any{"type": "array", "items": true}},
		"never": false,
		"tok":   map[string]any{"type": "string", "x-mcp-header": "Authorization"},
	}}
	shapeSchema(schema)
	b, _ := json.Marshal(schema)
	if s := string(b); strings.Contains(s, "x-mcp") || strings.Contains(s, "never") || strings.Contains(s, "true") || !strings.Contains(s, `"items":{}`) {
		t.Errorf("shaped %s", s)
	}

	var req anthropicRequest
	json.Unmarshal([]byte(`{"messages":[
		{"role":"assistant","content":[{"type":"tool_use","id":"a","name":"Bash","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"a","content":"one"},{"type":"tool_result","tool_use_id":"a","content":"two"}]},
		{"role":"assistant","content":[{"type":"text","text":"Then"}]}]}`), &req)
	p, _ := geminiRequest(&req, "m", nil)
	var kinds []string
	for _, it := range p["input"].([]map[string]any) {
		kinds = append(kinds, it["type"].(string))
	}
	if s := strings.Join(kinds, " "); s != "user_input function_call function_result model_output user_input" {
		t.Errorf("opens and ends on the user's side, a call answered once: %s", s)
	}
}

func TestGeminiEmptyReplyFails(t *testing.T) {
	col := &collector{}
	s := &geminiStream{names: &toolNames{}, out: col, steps: map[int]*geminiStep{}}
	s.run(strings.NewReader(geminiSSE(
		map[string]any{"event_type": "step.start", "index": 0, "step": map[string]any{"type": "thought"}},
		map[string]any{"event_type": "step.stop", "index": 0},
		map[string]any{"event_type": "interaction.completed", "interaction": map[string]any{"status": "completed"}},
	)))
	if col.failed == nil || col.failed.noRetry {
		t.Errorf("a reply of only thinking is a failure to try again: %+v", col.failed)
	}
}

func TestGeminiSignatureRefused(t *testing.T) {
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		if strings.Contains(fmt.Sprint(body["input"]), "thought") {
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"code":"invalid_request","message":"Corrupted thought signature."}}`)
			return
		}
		io.WriteString(w, geminiTurn)
	}))
	defer srv.Close()
	g := newGeminiUpstream(func() string { return "k" })
	g.base, g.client = srv.URL, srv.Client()
	payload := map[string]any{"stream": true, "input": []map[string]any{userText("hi"), thoughtStep{Signature: "other"}.step(), userText("go on")}}
	resp, err := g.send(t.Context(), payload)
	if err != nil || resp.StatusCode != 200 || len(bodies) != 2 {
		t.Fatalf("%v %v, %d asked", resp, err, len(bodies))
	}
	resp.Body.Close()
}

func TestStripGatewayThinking(t *testing.T) {
	body := `{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"x","signature":"` + encodeThought(thoughtStep{Signature: "s"}) + `"},{"type":"text","text":"done"}]}]}`
	got := string(stripGatewayThinking([]byte(body)))
	if strings.Contains(got, thoughtSig) || !strings.Contains(got, `"done"`) {
		t.Errorf("stripped %s", got)
	}
}
