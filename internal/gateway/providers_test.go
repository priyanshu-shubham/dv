package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeProvider is an endpoint answering /models with models and each turn
// with the next of turns; seen gets each request's body and headers.
func fakeProvider(t *testing.T, cfg Provider, models string, turns []func(w http.ResponseWriter, body map[string]any), seen *[]map[string]any, heads *[]http.Header) *providerUpstream {
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			if models == "" {
				http.NotFound(w, r)
				return
			}
			io.WriteString(w, models)
			return
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		*seen = append(*seen, body)
		if heads != nil {
			*heads = append(*heads, r.Header.Clone())
		}
		if n >= len(turns) {
			t.Errorf("an unexpected request: %v", body)
			w.WriteHeader(500)
			return
		}
		n++
		turns[n-1](w, body)
	}))
	t.Cleanup(srv.Close)
	cfg.URL = srv.URL + cfg.URL
	return &providerUpstream{cfg: cfg, key: func(string) string { return "pk" }, client: srv.Client(), prices: &priceList{}, codex: func() []Model { return nil }, refused: map[string]bool{}}
}

func sse(w http.ResponseWriter, frames ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, f := range frames {
		io.WriteString(w, "data: "+f+"\n\n")
	}
}

func serveOnce(p *providerUpstream, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	p.serve(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)), []byte(body), &masker{})
	return rec
}

func waitModels(t *testing.T, p *providerUpstream) []Model {
	for range 100 {
		if m := p.knownModels(); m != nil {
			return m
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no models: %v", p.failed())
	return nil
}

func TestChatProvider(t *testing.T) {
	var seen []map[string]any
	turn := func(w http.ResponseWriter, body map[string]any) {
		sse(w,
			`{"id":"c1","choices":[{"delta":{"reasoning_content":"look first"}}]}`,
			`{"id":"c1","choices":[{"delta":{"content":"Listing."}}]}`,
			`{"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"Bash","arguments":"{\"comm"}}]}}]}`,
			`{"id":"c1","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"and\":\"ls\"}"}}]}}]}`,
			`{"id":"c1","choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","function":{"name":"mcp__1srv__x","arguments":""}}]}}]}`,
			`{"id":"c1","choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"id":"c1","choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_cache_hit_tokens":60}}`,
			`[DONE]`)
	}
	refuse := func(w http.ResponseWriter, body map[string]any) {
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"message":"Unrecognized request argument supplied: stream_options"}}`)
	}
	p := fakeProvider(t, Provider{ID: "ds", Name: "DeepSeek", API: APIChat, Key: "DS_KEY"},
		`{"data":[{"id":"deepseek-flash","context_window":1048576,"effort":{"supported_levels":["low","high","max"],"default_level":"high"}},{"id":"text-embedding-3"}]}`,
		[]func(http.ResponseWriter, map[string]any){refuse, turn, turn}, &seen, nil)

	models := waitModels(t, p)
	if len(models) != 1 || models[0].Window != 1048576 || strings.Join(models[0].Efforts, ",") != "low,high,max" {
		t.Fatalf("models %+v", models)
	}

	body := `{"model":"ds/deepseek-flash","stream":true,"max_tokens":32000,"thinking":{"type":"adaptive"},"output_config":{"effort":"medium"},
		"system":[{"type":"text","text":"x-anthropic-billing-header: cch=1;"},{"type":"text","text":"You are Claude Code."}],
		"tools":[{"name":"Bash","description":"run","input_schema":{"type":"object"}},{"name":"mcp__1srv__x","input_schema":{"type":"object"}}],
		"messages":[{"role":"user","content":"list"}],"safeguards":[{"type":"dangerous_tool_use"}]}`
	rec := serveOnce(p, body)
	out := rec.Body.String()
	for _, want := range []string{`"thinking":"look first"`, `"signature":"dvcc1.`, `"text":"Listing."`, `"name":"Bash"`, `"name":"mcp__1srv__x"`, `"stop_reason":"tool_use"`, `"cache_read_input_tokens":60`, `"input_tokens":40`, `"model":"ds/deepseek-flash"`} {
		if !strings.Contains(out, want) {
			t.Errorf("reply lacks %s:\n%s", want, out)
		}
	}
	if len(seen) != 3 {
		t.Fatalf("%d requests", len(seen))
	}
	if _, ok := seen[1]["stream_options"]; ok {
		t.Error("stream_options sent again after it was refused")
	}
	first := seen[1]
	msgs := first["messages"].([]any)
	if sys := msgs[0].(map[string]any)["content"].(string); strings.Contains(sys, "billing") || !strings.Contains(sys, "served by DeepSeek") {
		t.Errorf("system %q", sys)
	}
	if first["model"] != "deepseek-flash" || first["reasoning_effort"] != "high" || first["max_tokens"] != float64(32000) {
		t.Errorf("request %v", first)
	}
	tools := first["tools"].([]any)
	if name := tools[1].(map[string]any)["function"].(map[string]any)["name"].(string); !strings.HasPrefix(name, "mcp__1srv__x") {
		t.Errorf("tool name %s", name)
	}
	// The safeguard's judge went to the same endpoint: here, the third turn
	// came back unreadable, so the calls are left unjudged.
	if !strings.Contains(out, `"unavailable"`) {
		t.Errorf("no verdicts: %s", out)
	}

	// The next turn sends the reasoning back as reasoning_content.
	var sig string
	for _, line := range strings.Split(out, "\n") {
		if i := strings.Index(line, `"signature":"dvcc1.`); i >= 0 {
			sig = line[i+len(`"signature":"`):]
			sig = sig[:strings.Index(sig, `"`)]
		}
	}
	req := anthropicRequest{}
	json.Unmarshal([]byte(`{"model":"ds/deepseek-flash","messages":[{"role":"user","content":"list"},
		{"role":"assistant","content":[{"type":"thinking","thinking":"","signature":"`+sig+`"},{"type":"text","text":"Listing."},{"type":"tool_use","id":"call_1","name":"Bash","input":{"command":"ls"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":[{"type":"text","text":"a.go"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]}]}]}`), &req)
	payload, _ := chatRequest(&req, "deepseek-flash", "m", nil)
	b, _ := json.Marshal(payload["messages"])
	got := string(b)
	for _, want := range []string{`"reasoning_content":"look first"`, `"role":"tool","tool_call_id":"call_1"`, `"content":"a.go"`, `"image_url":{"url":"data:image/png;base64,AAAA"}`} {
		if !strings.Contains(got, want) {
			t.Errorf("messages lack %s: %s", want, got)
		}
	}
}

func TestChatMaxTokensRefused(t *testing.T) {
	var seen []map[string]any
	p := fakeProvider(t, Provider{ID: "v", Name: "vLLM", API: APIChat}, "", []func(http.ResponseWriter, map[string]any){
		func(w http.ResponseWriter, body map[string]any) {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"message":"Invalid max_tokens value, the valid range of max_tokens is [1, 8192]"}}`)
		},
		func(w http.ResponseWriter, body map[string]any) {
			sse(w, `{"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}`, `[DONE]`)
		},
	}, &seen, nil)
	rec := serveOnce(p, `{"model":"v/qwen","stream":false,"max_tokens":32000,"messages":[{"role":"user","content":"hi"}]}`)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"text":"hi"`) || !strings.Contains(rec.Body.String(), `"stop_reason":"end_turn"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if seen[1]["max_tokens"] != float64(8192) {
		t.Errorf("asked again with %v", seen[1]["max_tokens"])
	}
}

func TestAnthropicProvider(t *testing.T) {
	var seen []map[string]any
	var heads []http.Header
	stream := func(w http.ResponseWriter, body map[string]any) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, ev := range []string{
			`{"type":"message_start","message":{"id":"m1","type":"message","role":"assistant","model":"deepseek-flash","content":[],"usage":{"input_tokens":5,"output_tokens":0}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"ds-sig"}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu1","name":"Bash","input":{}}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"command\":\"rm -rf /\"}"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":9}}`,
			`{"type":"message_stop"}`,
		} {
			var d map[string]any
			json.Unmarshal([]byte(ev), &d)
			io.WriteString(w, "event: "+d["type"].(string)+"\ndata: "+ev+"\n\n")
		}
	}
	judge := func(w http.ResponseWriter, body map[string]any) {
		io.WriteString(w, `{"content":[{"type":"text","text":"Here: {\"verdicts\":[{\"id\":\"tu1\",\"flagged\":true,\"reason\":\"wipes the disk\"}]}"}]}`)
	}
	p := fakeProvider(t, Provider{ID: "lite", Name: "LiteLLM", API: APIAnthropic, Key: "LITE_KEY"}, `{"data":[{"id":"deepseek-flash"}]}`,
		[]func(http.ResponseWriter, map[string]any){stream, judge}, &seen, &heads)

	body := `{"model":"lite/deepseek-flash","stream":true,"max_tokens":1000,
		"system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2; cch=abc;"},{"type":"text","text":"You are Claude Code."}],
		"tools":[{"type":"custom","name":"Bash","input_schema":{"type":"object"}}],
		"messages":[{"role":"user","content":"clean up"},
			{"role":"assistant","content":[{"type":"thinking","thinking":"a","signature":"dvan1.lite.old-sig"},{"type":"thinking","thinking":"b","signature":"dvan1.other.x"},{"type":"thinking","thinking":"c","signature":"anthropic-sig"},{"type":"text","text":"ok"}]},
			{"role":"user","content":"go on"}],
		"safeguards":[{"type":"dangerous_tool_use"}]}`
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer claude-oauth-token")
	r.Header.Set("anthropic-beta", "some-beta")
	p.serve(rec, r, []byte(body), &masker{})
	out := rec.Body.String()
	for _, want := range []string{`"model":"lite/deepseek-flash"`, `"signature":"dvan1.lite.ds-sig"`, `"outcome":"flagged"`, `"stop_reason":"tool_use"`} {
		if !strings.Contains(out, want) {
			t.Errorf("reply lacks %s:\n%s", want, out)
		}
	}
	if len(seen) != 2 {
		t.Fatalf("%d requests", len(seen))
	}
	if h := heads[0]; h.Get("Authorization") != "Bearer pk" || h.Get("x-api-key") != "pk" || h.Get("anthropic-beta") != "some-beta" {
		t.Errorf("headers %v", h)
	}
	sent, _ := json.Marshal(seen[0])
	s := string(sent)
	if strings.Contains(s, "billing") || strings.Contains(s, "safeguards") || strings.Contains(s, `"custom"`) || seen[0]["model"] != "deepseek-flash" || seen[0]["stream"] != true {
		t.Errorf("request %s", s)
	}
	if !strings.Contains(s, `"signature":"old-sig"`) || strings.Contains(s, "dvan1.other") || strings.Contains(s, "anthropic-sig") {
		t.Errorf("thinking sent %s", s)
	}
}

func TestOpenAIProvider(t *testing.T) {
	var seen []map[string]any
	turn := func(w http.ResponseWriter, body map[string]any) {
		sse(w,
			`{"type":"response.created","response":{"id":"r1"}}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"message"}}`,
			`{"type":"response.output_text.delta","output_index":0,"delta":"hello"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"message"}}`,
			`{"type":"response.completed","response":{"usage":{"input_tokens":10,"output_tokens":2}}}`)
	}
	refuse := func(w http.ResponseWriter, body map[string]any) {
		w.WriteHeader(400)
		io.WriteString(w, `{"error":{"message":"Your organization must be verified to generate reasoning summaries.","type":"invalid_request_error"}}`)
	}
	cfg := openAI
	cfg.URL = ""
	p := fakeProvider(t, cfg, `{"data":[{"id":"gpt-6-luna"},{"id":"gpt-6-luna-2026-01-01"},{"id":"gpt-4.1"},{"id":"tts-2"},{"id":"whisper-1"}]}`,
		[]func(http.ResponseWriter, map[string]any){refuse, turn}, &seen, nil)
	models := waitModels(t, p)
	var slugs []string
	for _, m := range models {
		slugs = append(slugs, m.Slug)
	}
	if s := strings.Join(slugs, " "); s != "gpt-6-luna gpt-4.1" {
		t.Fatalf("models %s", s)
	}
	if models[1].Levels != nil || models[0].Levels == nil {
		t.Errorf("levels %+v", models)
	}

	body := `{"model":"openai/gpt-6-luna","stream":true,"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},
		"tools":[{"name":"ToolSearch","input_schema":{"type":"object"}},{"name":"Late","defer_loading":true,"input_schema":{"type":"object"}}],
		"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"ToolSearch","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":[{"type":"tool_reference","tool_name":"Late"}]}]}]}`
	rec := serveOnce(p, body)
	if !strings.Contains(rec.Body.String(), `"text":"hello"`) {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	b, _ := json.Marshal(seen[1])
	s := string(b)
	if strings.Contains(s, "additional_tools") || !strings.Contains(s, `"name":"Late"`) || strings.Contains(s, `"summary"`) || !strings.Contains(s, `"effort":"high"`) {
		t.Errorf("request %s", s)
	}

	plain := `{"model":"openai/gpt-4.1","stream":true,"thinking":{"type":"adaptive"},"messages":[{"role":"user","content":"hi"}]}`
	var req anthropicRequest
	json.Unmarshal([]byte(plain), &req)
	payload, _ := responsesRequest(&req, "gpt-4.1", "k", nil, p.responsesOpts("gpt-4.1", nil))
	if payload["reasoning"] != nil || payload["include"] != nil && len(payload["include"].([]string)) > 0 {
		t.Errorf("a model that doesn't reason was sent %v %v", payload["reasoning"], payload["include"])
	}
}

func TestKeyHelper(t *testing.T) {
	dir := t.TempDir()
	// Each run prints the next key: key1, key2, …
	helper := `n=$(cat ` + dir + `/n 2>/dev/null || echo 0); n=$((n+1)); echo $n > ` + dir + `/n; echo key$n`
	var seen []map[string]any
	var heads []http.Header
	ok := func(w http.ResponseWriter, body map[string]any) {
		sse(w, `{"choices":[{"delta":{"content":"hi"},"finish_reason":"stop"}]}`, `[DONE]`)
	}
	refused := func(w http.ResponseWriter, body map[string]any) {
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"message":"expired token"}}`)
	}
	p := fakeProvider(t, Provider{ID: "corp", Name: "Corp", API: APIChat, KeyHelper: helper, KeyTTL: 3600}, "",
		[]func(http.ResponseWriter, map[string]any){refused, ok, ok}, &seen, &heads)
	body := `{"model":"corp/m","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	for i := range 2 {
		if rec := serveOnce(p, body); !strings.Contains(rec.Body.String(), `"text":"hi"`) {
			t.Fatalf("turn %d: %d %s", i, rec.Code, rec.Body)
		}
	}
	var keys []string
	for _, h := range heads {
		keys = append(keys, h.Get("Authorization"))
	}
	// Refused, the key is made anew; kept, it is used again without a run.
	if got := strings.Join(keys, ","); got != "Bearer key1,Bearer key2,Bearer key2" {
		t.Errorf("keys sent %s", got)
	}

	prev := helperTimeout
	helperTimeout = 200 * time.Millisecond
	t.Cleanup(func() { helperTimeout = prev })
	slow := fakeProvider(t, Provider{ID: "slow", Name: "Slow", API: APIChat, KeyHelper: "sleep 5"}, "", nil, &seen, nil)
	start := time.Now()
	rec := serveOnce(slow, body)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "took over") || time.Since(start) > 3*time.Second {
		t.Errorf("a helper that hangs: %d %s after %s", rec.Code, rec.Body, time.Since(start))
	}
}

func TestProviderCheck(t *testing.T) {
	for _, bad := range []Provider{
		{ID: "openai", URL: "http://x", API: APIChat},
		{ID: "My Server", URL: "http://x", API: APIChat},
		{ID: "x", URL: "ftp://x", API: APIChat},
		{ID: "x", URL: "http://x", API: "grpc"},
	} {
		if bad.Check() == nil {
			t.Errorf("%+v passed", bad)
		}
	}
	if err := (Provider{ID: "litellm", URL: "http://localhost:4000", API: APIAnthropic}).Check(); err != nil {
		t.Error(err)
	}
}

func TestProviderRouting(t *testing.T) {
	t.Cleanup(func() { providerSource.Store(nil) })
	UseProviders(func() []Provider {
		return []Provider{{ID: "lite", Name: "LiteLLM", URL: "http://localhost:4000", API: APIAnthropic}, {ID: "codex", URL: "http://x", API: APIChat}}
	})
	g := &Gateway{codex: newCodexUpstream(), prices: &priceList{}}
	p, ok := g.provider("lite/deepseek/deepseek-chat")
	if !ok || p.cfg.Name != "LiteLLM" || p.rateKey("deepseek/deepseek-chat") != "*/deepseek-chat" {
		t.Fatalf("lite: %v %v", p, ok)
	}
	if again, _ := g.provider("lite/x"); again != p {
		t.Error("an unchanged provider was made anew, its models forgotten")
	}
	if _, ok := g.provider("codex/gpt-6"); ok {
		t.Error("a provider took dv's own codex/")
	}
	if _, ok := g.provider("openai/gpt-6"); ok && g.secretOrEnv(OpenAIKeyName) == "" {
		t.Error("OpenAI served without a key")
	}
	if key, ok := g.rateKey("openai/gpt-6-luna"); !ok || key != "openai/gpt-6-luna" {
		t.Errorf("rate key %s", key)
	}
}

func TestStripProviderThinking(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"a","signature":"dvcc1.eA"},{"type":"thinking","thinking":"b","signature":"dvan1.lite.s"},{"type":"thinking","thinking":"c","signature":"real"},{"type":"text","text":"ok"}]}]}`)
	got := string(stripGatewayThinking(body))
	if strings.Contains(got, "dvcc1") || strings.Contains(got, "dvan1") || !strings.Contains(got, `"real"`) {
		t.Errorf("%s", got)
	}
}
