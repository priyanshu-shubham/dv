package gateway

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
)

// TestClaudeThroughChatProvider runs the real claude CLI on a model of an
// OpenAI-compatible endpoint (a fake here), through a tool call and back:
// DV_CLAUDE=1 to run it. Anthropic's own address is the fake too, so nothing
// leaves the machine.
func TestClaudeThroughChatProvider(t *testing.T) {
	if os.Getenv("DV_CLAUDE") == "" {
		t.Skip("DV_CLAUDE=1 runs the claude CLI")
	}
	var mu sync.Mutex
	var turns []map[string]any
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/models":
			io.WriteString(w, `{"data":[{"id":"fake-coder","context_window":64000}]}`)
		case r.URL.Path == "/v1/chat/completions":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			turns = append(turns, body)
			mu.Unlock()
			if body["stream"] != true {
				io.WriteString(w, `{"choices":[{"message":{"content":"{\"verdicts\":[]}"}}]}`)
				return
			}
			b, _ := json.Marshal(body["messages"])
			if strings.Contains(string(b), `"role":"tool"`) {
				sse(w, `{"choices":[{"delta":{"content":"The command said so."},"finish_reason":"stop"}]}`, `[DONE]`)
				return
			}
			if _, ok := body["tools"]; !ok {
				sse(w, `{"choices":[{"delta":{"content":"A title"},"finish_reason":"stop"}]}`, `[DONE]`)
				return
			}
			sse(w,
				`{"choices":[{"delta":{"reasoning_content":"I should run it."}}]}`,
				`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_a","function":{"name":"Bash","arguments":"{\"command\":\"echo dv-tool-ran\",\"description\":\"Say it\"}"}}]},"finish_reason":"tool_calls"}]}`,
				`{"choices":[],"usage":{"prompt_tokens":1000,"completion_tokens":30}}`,
				`[DONE]`)
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"type":"error","error":{"type":"not_found_error","message":"fake"}}`)
		}
	}))
	t.Cleanup(fake.Close)

	g, err := New(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	g.anthropic, _ = url.Parse(fake.URL)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: g}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	prev := active.Load()
	t.Cleanup(func() { active.Store(prev); providerSource.Store(nil) })
	UseProviders(func() []Provider {
		return []Provider{{ID: "local", Name: "Local", URL: fake.URL + "/v1", API: APIChat}}
	})
	g.Serve(ln.Addr())
	waitModels(t, g.providers()[len(g.providers())-1])

	dir := t.TempDir()
	cmd := exec.Command("claude", "-p", "--model", "local/fake-coder", "--dangerously-skip-permissions", "--output-format", "json", "Run echo dv-tool-ran and tell me what it said")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "CLAUDE_CONFIG_DIR="+t.TempDir(), "ANTHROPIC_API_KEY=dummy", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1")
	cmd.Env = append(cmd.Env, Env("local/fake-coder")...)
	out, err := cmd.CombinedOutput()
	t.Logf("claude: %s", out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "The command said so.") {
		t.Errorf("the reply didn't come back")
	}
	mu.Lock()
	defer mu.Unlock()
	var sawResult, sawReasoning bool
	for _, turn := range turns {
		b, _ := json.Marshal(turn["messages"])
		sawResult = sawResult || strings.Contains(string(b), `"content":"dv-tool-ran","role":"tool","tool_call_id":"call_a"`)
		sawReasoning = sawReasoning || strings.Contains(string(b), `"reasoning_content":"I should run it."`)
	}
	if !sawResult || !sawReasoning {
		t.Errorf("the tool's result went back: %v, the reasoning: %v", sawResult, sawReasoning)
	}
}
