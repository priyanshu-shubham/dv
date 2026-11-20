package gateway

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// An Anthropic-compatible endpoint's thinking signature is its own, which
// Anthropic, or another endpoint, would refuse: it goes to Claude Code as
// compatSig, the provider's id, then the signature, and back to that
// provider alone as it came.
const compatSig = "dvan1."

func (p *providerUpstream) wrapSig(sig string) string { return compatSig + p.cfg.ID + "." + sig }

// anthropicBody is a Messages request as the endpoint is sent it: its model
// as the endpoint names it, streamed, with no safeguards - the gateway
// judges auto mode's calls itself - and no thinking but the endpoint's own.
func (p *providerUpstream) anthropicBody(body []byte, slug string) []byte {
	mine := p.wrapSig("")
	body = keepThinking(body, func(sig string) (string, bool) {
		rest, ok := strings.CutPrefix(sig, mine)
		return rest, ok
	})
	var req map[string]json.RawMessage
	if json.Unmarshal(body, &req) != nil {
		return body
	}
	req["model"], _ = json.Marshal(slug)
	req["stream"] = json.RawMessage("true")
	delete(req, "safeguards")
	// Claude Code's per-turn billing line would leave no prefix the same to cache.
	var system []map[string]any
	if json.Unmarshal(req["system"], &system) == nil && len(system) > 0 {
		if t, _ := system[0]["text"].(string); billingLine.MatchString(t) {
			if rest := strings.TrimSpace(billingLine.ReplaceAllString(t, "")); rest == "" {
				system = system[1:]
			} else {
				system[0]["text"] = rest
			}
			req["system"], _ = json.Marshal(system)
		}
	}
	// DeepSeek turns a client tool typed "custom" away.
	var tools []map[string]any
	if json.Unmarshal(req["tools"], &tools) == nil {
		for _, t := range tools {
			if t["type"] == "custom" {
				delete(t, "type")
			}
		}
		req["tools"], _ = json.Marshal(tools)
	}
	out, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return out
}

// postAnthropic posts body to path, once more with a helper's new key where
// the endpoint refused the one it had.
func (p *providerUpstream) postAnthropic(ctx context.Context, path string, body []byte, from http.Header) (*http.Response, error) {
	resp, err := p.postAnthropicOnce(ctx, path, body, from)
	if err == nil && resp.StatusCode == http.StatusUnauthorized && p.refreshKey(resp.Request.Header) {
		resp.Body.Close()
		return p.postAnthropicOnce(ctx, path, body, from)
	}
	return resp, err
}

func (p *providerUpstream) postAnthropicOnce(ctx context.Context, path string, body []byte, from http.Header) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.URL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	if from != nil {
		if v := from.Get("anthropic-version"); v != "" {
			req.Header.Set("anthropic-version", v)
		}
		if v := from.Get("anthropic-beta"); v != "" {
			req.Header.Set("anthropic-beta", v)
		}
	}
	p.authorize(req.Header)
	return p.client.Do(req)
}

// serveAnthropic passes a Messages request on to an endpoint that speaks
// Anthropic's API, with the provider's key, never Claude Code's own login.
func (p *providerUpstream) serveAnthropic(w http.ResponseWriter, r *http.Request, body []byte, m *masker) {
	var req anthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	slug := strings.TrimPrefix(req.Model, p.prefix())
	out := p.anthropicBody(body, slug)

	if strings.HasSuffix(r.URL.Path, "/count_tokens") {
		var cr map[string]json.RawMessage
		json.Unmarshal(out, &cr)
		delete(cr, "stream")
		b, _ := json.Marshal(cr)
		if resp, err := p.postAnthropic(r.Context(), "/v1/messages/count_tokens", b, r.Header); err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				w.Header().Set("Content-Type", "application/json")
				io.Copy(w, resp.Body)
				return
			}
		}
		json.NewEncoder(w).Encode(map[string]int{"input_tokens": len(rawData.ReplaceAll(dataURL.ReplaceAll(b, nil), nil)) / 4})
		return
	}

	resp, err := p.postAnthropic(r.Context(), "/v1/messages", out, r.Header)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			apiError{http.StatusBadGateway, "api_error", p.cfg.Name + ": " + err.Error(), false}.write(w)
		}
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		p.relayError(w, resp, b)
		return
	}
	reply := newReply(w, req.Stream, m)
	s := &compatStream{p: p, model: req.Model, out: reply.out, blocks: map[int]*compatBlock{}}
	for _, sg := range req.Safeguards {
		if sg.Type == "dangerous_tool_use" {
			s.judge = func(calls []toolCall) map[string]any {
				return judged(r.Context(), sg, &req, calls, func(ctx context.Context, ask string) (string, error) {
					return p.anthropicVerdicts(ctx, slug, ask)
				})
			}
		}
	}
	s.run(resp.Body)
	reply.close()
}

// compatStream passes an Anthropic-compatible stream on, its model named as
// Claude Code asked for it and its thinking signed as the provider's.
type compatStream struct {
	p      *providerUpstream
	model  string
	out    sink
	blocks map[int]*compatBlock
	calls  []toolCall
	ended  bool
	judge  func([]toolCall) map[string]any
}

type compatBlock struct {
	kind, id, name string
	signed         bool
	args           strings.Builder
}

func (s *compatStream) run(body io.Reader) {
	readSSE(body, s.event)
	if !s.ended {
		s.fail(s.p.failures().classify(0, "", "the reply ended before it was done"))
	}
}

func (s *compatStream) event(ev event) error {
	var d map[string]any
	if json.Unmarshal(ev.data, &d) != nil {
		return nil
	}
	kind, _ := d["type"].(string)
	if _, ok := d["error"]; ok && (kind == "" || kind == "error") {
		code, msg := providerFailure(ev.data)
		return s.fail(s.p.failures().classify(0, code, msg))
	}
	index := -1
	if f, ok := d["index"].(float64); ok {
		index = int(f)
	}
	switch kind {
	case "ping":
		return nil
	case "message_start":
		if msg, ok := d["message"].(map[string]any); ok {
			msg["model"] = s.model
		}
	case "content_block_start":
		cb, _ := d["content_block"].(map[string]any)
		b := &compatBlock{}
		b.kind, _ = cb["type"].(string)
		b.id, _ = cb["id"].(string)
		b.name, _ = cb["name"].(string)
		if sig, _ := cb["signature"].(string); b.kind == "thinking" && sig != "" {
			cb["signature"], b.signed = s.p.wrapSig(sig), true
		}
		s.blocks[index] = b
	case "content_block_delta":
		delta, _ := d["delta"].(map[string]any)
		b := s.blocks[index]
		switch delta["type"] {
		case "signature_delta":
			sig, _ := delta["signature"].(string)
			delta["signature"] = s.p.wrapSig(sig)
			if b != nil {
				b.signed = true
			}
		case "input_json_delta":
			if b != nil {
				part, _ := delta["partial_json"].(string)
				b.args.WriteString(part)
			}
		}
	case "content_block_stop":
		b := s.blocks[index]
		delete(s.blocks, index)
		switch {
		case b == nil:
		case b.kind == "thinking" && !b.signed:
			// Unsigned, it would go back to no one.
			if err := s.out.send(newEvent("content_block_delta", map[string]any{"type": "content_block_delta", "index": index,
				"delta": map[string]any{"type": "signature_delta", "signature": s.p.wrapSig("")}})); err != nil {
				return err
			}
		case b.kind == "tool_use":
			s.calls = append(s.calls, toolCall{ID: b.id, Name: b.name, Input: cmp.Or(b.args.String(), "{}")})
		}
	case "message_delta":
		if s.judge != nil {
			delta, _ := d["delta"].(map[string]any)
			if delta == nil {
				delta = map[string]any{}
				d["delta"] = delta
			}
			delta["safeguard_results"] = []any{map[string]any{
				"type":   "dangerous_tool_use",
				"status": map[string]any{"type": "available", "tool_uses": s.judge(s.calls)},
			}}
		}
	case "message_stop":
		s.ended = true
	}
	return s.out.send(newEvent(cmp.Or(ev.name, kind), d))
}

func (s *compatStream) fail(e apiError) error {
	if s.ended {
		return nil
	}
	s.ended = true
	if r, ok := s.out.(refuser); ok && r.refuse(e) {
		return nil
	}
	return s.out.send(e.event())
}

func (p *providerUpstream) anthropicVerdicts(ctx context.Context, slug, ask string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"model":      slug,
		"max_tokens": 4096,
		"system":     judgeInstructions + "\n\n" + verdictShape,
		"messages":   []map[string]any{{"role": "user", "content": ask}},
		"thinking":   map[string]any{"type": "disabled"},
	})
	resp, err := p.postAnthropic(ctx, "/v1/messages", body, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		_, msg := providerFailure(b)
		return "", fmt.Errorf("%s: %s", resp.Status, msg)
	}
	var done struct {
		Content []block `json:"content"`
	}
	if err := json.Unmarshal(b, &done); err != nil {
		return "", err
	}
	var text strings.Builder
	for _, c := range done.Content {
		if c.Type == "text" {
			text.WriteString(c.Text)
		}
	}
	return jsonIn(text.String()), nil
}
