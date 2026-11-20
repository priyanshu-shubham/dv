package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
)

// serveResponses answers a Messages request through a Responses API: OpenAI's
// own with the user's key, or a proxy's that speaks it.
func (p *providerUpstream) serveResponses(w http.ResponseWriter, r *http.Request, body []byte, m *masker) {
	var req anthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	slug := strings.TrimPrefix(req.Model, p.prefix())
	levels := p.levels(slug)
	opts := p.responsesOpts(slug, levels)
	key := cacheKey(sessionOf(r, &req), r.Header.Get("X-Claude-Code-Agent-Id"), instructionsAs(req.System, opts.who))
	payload, names := responsesRequest(&req, slug, key, levels, opts)
	if strings.HasSuffix(r.URL.Path, "/count_tokens") {
		json.NewEncoder(w).Encode(map[string]int{"input_tokens": estimateTokens(payload)})
		return
	}
	resp, b, err := p.postResponses(r.Context(), payload)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			apiError{http.StatusBadGateway, "api_error", p.cfg.Name + ": " + err.Error(), false}.write(w)
		}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		p.relayError(w, resp, b)
		return
	}
	out := newReply(w, req.Stream, m)
	s := &codexStream{model: req.Model, names: names, out: out.out, blocks: map[int]*openBlock{}}
	for _, sg := range req.Safeguards {
		if sg.Type == "dangerous_tool_use" {
			s.judge = func(calls []toolCall) map[string]any {
				return judged(r.Context(), sg, &req, calls, func(ctx context.Context, ask string) (string, error) {
					return p.responsesVerdicts(ctx, slug, levels, ask)
				})
			}
		}
	}
	s.run(resp.Body)
	out.close()
}

func (p *providerUpstream) responsesOpts(slug string, levels []string) responsesOpts {
	if p.cfg.ID != openAI.ID {
		return responsesOpts{who: slug + ", served by " + p.cfg.Name}
	}
	listed := slices.ContainsFunc(p.knownModels(), func(m Model) bool { return m.Slug == slug })
	return responsesOpts{who: slug + ", an OpenAI model", noReasoning: listed && len(levels) == 0}
}

// postResponses posts a turn, again without what the endpoint turned away:
// a parameter it doesn't take, reasoning sealed for another account. A
// refusal's body comes back read, with the response.
func (p *providerUpstream) postResponses(ctx context.Context, payload map[string]any) (*http.Response, []byte, error) {
	p.withoutRefused(payload)
	refreshed := false
	for try := 0; ; try++ {
		data, _ := json.Marshal(payload)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.URL+"/responses", bytes.NewReader(data))
		p.authorize(req.Header)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		resp, err := p.client.Do(req)
		if err != nil {
			return nil, nil, err
		}
		if resp.StatusCode == http.StatusOK {
			return resp, nil, nil
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(b))
		if resp.StatusCode == http.StatusUnauthorized && !refreshed && p.refreshKey(req.Header) {
			refreshed = true
			continue
		}
		if resp.StatusCode != http.StatusBadRequest || try >= 4 || !p.easeResponses(payload, b) {
			return resp, b, nil
		}
	}
}

// easeResponses takes out of payload what a 400 said was wrong with it,
// false where there was nothing to take.
func (p *providerUpstream) easeResponses(payload map[string]any, b []byte) bool {
	_, msg := providerFailure(b)
	low := strings.ToLower(msg)
	input, _ := payload["input"].([]map[string]any)
	switch {
	case foreignReasoning.MatchString(msg) && slices.ContainsFunc(input, func(it map[string]any) bool { return it["type"] == "reasoning" }):
		withoutReasoning(payload)
		return true
	case strings.Contains(low, "summar") && payload["reasoning"] != nil:
		// Summaries need a verified organization.
		r, _ := payload["reasoning"].(map[string]any)
		if _, ok := r["summary"]; ok {
			delete(r, "summary")
			return true
		}
	case strings.Contains(low, "encrypted") && payload["include"] != nil:
		delete(payload, "include")
		withoutReasoning(payload)
		return true
	case strings.Contains(low, "web_search"):
		tools, _ := payload["tools"].([]map[string]any)
		if i := slices.IndexFunc(tools, func(t map[string]any) bool { return t["type"] == "web_search" }); i >= 0 {
			payload["tools"] = slices.Delete(tools, i, i+1)
			return true
		}
	}
	return p.dropRefused(payload, msg, "reasoning", "prompt_cache_key", "service_tier", "parallel_tool_calls", "include")
}

func (p *providerUpstream) responsesVerdicts(ctx context.Context, slug string, levels []string, ask string) (string, error) {
	payload := map[string]any{
		"model": slug,
		"input": []map[string]any{developerMessage(judgeInstructions), {"type": "message", "role": "user", "content": []map[string]any{{"type": "input_text", "text": ask}}}},
		"store": false, "stream": true,
		"text": map[string]any{"format": map[string]any{"type": "json_schema", "name": "verdicts", "strict": true, "schema": verdictSchema}},
	}
	if effort := lowestEffort(levels); effort != "" {
		payload["reasoning"] = map[string]any{"effort": effort}
	}
	resp, b, err := p.postResponses(ctx, payload)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, msg := providerFailure(b)
		return "", fmt.Errorf("%s: %s", resp.Status, msg)
	}
	var text strings.Builder
	readSSE(resp.Body, func(ev event) error {
		var d struct {
			Type  string `json:"type"`
			Delta string `json:"delta"`
		}
		if json.Unmarshal(ev.data, &d) == nil && d.Type == "response.output_text.delta" {
			text.WriteString(d.Delta)
		}
		return nil
	})
	return text.String(), nil
}
