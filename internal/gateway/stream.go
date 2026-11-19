package gateway

import (
	"cmp"
	"encoding/json"
	"io"
	"strings"
)

// codexStream turns a Responses stream into the Messages stream Claude Code
// reads, block by block.
type codexStream struct {
	model  string
	names  *toolNames
	out    sink
	next   int
	blocks map[int]*openBlock // by the output item's index
	tools  bool
	start  bool
	ended  bool
	calls  []toolCall
	// judge, where auto mode asked for it, has the reply's calls judged for
	// its last message_delta (safeguard.go).
	judge func([]toolCall) map[string]any
}

type openBlock struct {
	index    int
	kind     string
	id, name string
	args     strings.Builder // a function call's arguments, as streamed
	summary  []string
}

type codexUsage struct {
	Input  int `json:"input_tokens"`
	Output int `json:"output_tokens"`
	Detail struct {
		Cached int `json:"cached_tokens"`
		Write  int `json:"cache_write_tokens"`
	} `json:"input_tokens_details"`
}

type codexError struct {
	Code    string `json:"code"`
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (s *codexStream) run(body io.Reader) error {
	err := readSSE(body, s.event)
	if !s.ended {
		return s.fail(classify(0, "", "the reply ended before it was done"))
	}
	return err
}

func (s *codexStream) event(ev event) error {
	var d struct {
		Type        string          `json:"type"`
		OutputIndex int             `json:"output_index"`
		Delta       string          `json:"delta"`
		Item        json.RawMessage `json:"item"`
		Response    struct {
			ID                string      `json:"id"`
			Usage             *codexUsage `json:"usage"`
			IncompleteDetails *struct {
				Reason string `json:"reason"`
			} `json:"incomplete_details"`
			Error *codexError `json:"error"`
		} `json:"response"`
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(ev.data, &d) != nil {
		return nil
	}
	switch d.Type {
	case "response.created":
		return s.begin(d.Response.ID)
	case "response.output_item.added":
		return s.added(d.OutputIndex, d.Item)
	case "response.reasoning_summary_part.added":
		if b := s.blocks[d.OutputIndex]; b != nil && b.kind == "thinking" {
			b.summary = append(b.summary, "")
			if len(b.summary) > 1 {
				return s.out.send(thinkingDelta(b.index, "\n\n"))
			}
		}
	case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		if b := s.blocks[d.OutputIndex]; b != nil && b.kind == "thinking" {
			if d.Type == "response.reasoning_summary_text.delta" {
				if len(b.summary) == 0 {
					b.summary = append(b.summary, "")
				}
				b.summary[len(b.summary)-1] += d.Delta
			}
			return s.out.send(thinkingDelta(b.index, d.Delta))
		}
	case "response.output_text.delta", "response.refusal.delta":
		if b := s.blocks[d.OutputIndex]; b != nil && b.kind == "text" {
			return s.out.send(deltaEvent(b.index, "text_delta", d.Delta))
		}
	case "response.function_call_arguments.delta":
		if b := s.blocks[d.OutputIndex]; b != nil && b.kind == "tool_use" {
			b.args.WriteString(d.Delta)
			return s.out.send(deltaEvent(b.index, "input_json_delta", d.Delta))
		}
	case "response.output_item.done":
		return s.done(d.OutputIndex, d.Item)
	case "response.completed", "response.incomplete":
		reason := "end_turn"
		if s.tools {
			reason = "tool_use"
		}
		if inc := d.Response.IncompleteDetails; inc != nil && inc.Reason != "" {
			reason = "max_tokens"
			if inc.Reason == "content_filter" {
				reason = "refusal"
			}
		}
		return s.finish(reason, d.Response.Usage)
	case "response.failed":
		e := cmp.Or(d.Response.Error, &codexError{})
		return s.fail(classify(0, cmp.Or(e.Code, e.Type), cmp.Or(e.Message, "the model failed")))
	case "error":
		var e codexError
		if json.Unmarshal(d.Error, &e) != nil {
			json.Unmarshal(d.Error, &e.Message)
		}
		return s.fail(classify(0, cmp.Or(e.Code, d.Code, e.Type), cmp.Or(e.Message, d.Message)))
	}
	return nil
}

func (s *codexStream) begin(id string) error {
	if s.start {
		return nil
	}
	s.start = true
	return s.out.send(newEvent("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": "msg_" + id, "type": "message", "role": "assistant", "model": s.model,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": 0, "output_tokens": 0},
		},
	}))
}

func (s *codexStream) added(at int, raw json.RawMessage) error {
	var item struct {
		Type   string `json:"type"`
		CallID string `json:"call_id"`
		Name   string `json:"name"`
	}
	json.Unmarshal(raw, &item)
	var start map[string]any
	kind := ""
	switch item.Type {
	case "reasoning":
		kind, start = "thinking", map[string]any{"type": "thinking", "thinking": "", "signature": ""}
	case "message":
		kind, start = "text", map[string]any{"type": "text", "text": ""}
	case "function_call":
		s.tools = true
		kind, start = "tool_use", map[string]any{"type": "tool_use", "id": item.CallID, "name": s.names.in(item.Name), "input": map[string]any{}}
	default:
		return nil
	}
	if err := s.begin(""); err != nil {
		return err
	}
	b := &openBlock{index: s.next, kind: kind, id: item.CallID, name: s.names.in(item.Name)}
	s.next++
	s.blocks[at] = b
	return s.out.send(newEvent("content_block_start", map[string]any{"type": "content_block_start", "index": b.index, "content_block": start}))
}

func (s *codexStream) done(at int, raw json.RawMessage) error {
	var item struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		Encrypted string `json:"encrypted_content"`
		Arguments string `json:"arguments"`
		Summary   []struct {
			Text string `json:"text"`
		} `json:"summary"`
		Action *struct {
			Query   string `json:"query"`
			Sources []struct {
				URL   string `json:"url"`
				Title string `json:"title"`
			} `json:"sources"`
		} `json:"action"`
	}
	json.Unmarshal(raw, &item)
	if item.Type == "web_search_call" && item.Action != nil && item.Action.Query != "" {
		return s.search(item.ID, item.Action.Query, item.Action.Sources)
	}
	b := s.blocks[at]
	if b == nil {
		return nil
	}
	delete(s.blocks, at)
	switch b.kind {
	case "thinking":
		r := reasoningItem{ID: item.ID, Encrypted: item.Encrypted}
		for _, p := range item.Summary {
			r.Summary = append(r.Summary, p.Text)
		}
		if err := s.out.send(newEvent("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": b.index,
			"delta": map[string]any{"type": "signature_delta", "signature": encodeReasoning(r)},
		})); err != nil {
			return err
		}
	case "tool_use":
		// The done item's arguments are whole where a delta went missing.
		streamed := b.args.String()
		args := streamed
		if rest, ok := strings.CutPrefix(item.Arguments, streamed); ok && rest != "" {
			args = item.Arguments
			if err := s.out.send(deltaEvent(b.index, "input_json_delta", rest)); err != nil {
				return err
			}
		} else if streamed == "" {
			args = "{}"
			if err := s.out.send(deltaEvent(b.index, "input_json_delta", "{}")); err != nil {
				return err
			}
		}
		s.calls = append(s.calls, toolCall{ID: b.id, Name: b.name, Input: args})
	}
	return s.out.send(newEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": b.index}))
}

// search tells a web search the model ran as Anthropic's own server tool
// tells one, which Claude Code's WebSearch takes its links from.
func (s *codexStream) search(id, query string, sources []struct {
	URL   string `json:"url"`
	Title string `json:"title"`
}) error {
	if err := s.begin(""); err != nil {
		return err
	}
	use := "srvtoolu_" + strings.TrimPrefix(id, "ws_")
	q, _ := json.Marshal(map[string]string{"query": query})
	results := []map[string]any{}
	for _, src := range sources {
		if src.URL != "" {
			results = append(results, map[string]any{"type": "web_search_result", "url": src.URL, "title": cmp.Or(src.Title, src.URL)})
		}
	}
	evs := []event{
		newEvent("content_block_start", map[string]any{"type": "content_block_start", "index": s.next,
			"content_block": map[string]any{"type": "server_tool_use", "id": use, "name": "web_search", "input": map[string]any{}}}),
		deltaEvent(s.next, "input_json_delta", string(q)),
		newEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.next}),
		newEvent("content_block_start", map[string]any{"type": "content_block_start", "index": s.next + 1,
			"content_block": map[string]any{"type": "web_search_tool_result", "tool_use_id": use, "content": results}}),
		newEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.next + 1}),
	}
	s.next += 2
	for _, ev := range evs {
		if err := s.out.send(ev); err != nil {
			return err
		}
	}
	return nil
}

func (s *codexStream) finish(reason string, u *codexUsage) error {
	if s.ended {
		return nil
	}
	if err := s.begin(""); err != nil {
		return err
	}
	for at := range s.blocks {
		if err := s.done(at, nil); err != nil {
			return err
		}
	}
	s.ended = true
	usage := map[string]any{"input_tokens": 0, "output_tokens": 0}
	if u != nil {
		usage = map[string]any{
			"input_tokens":                max(0, u.Input-u.Detail.Cached-u.Detail.Write),
			"cache_read_input_tokens":     u.Detail.Cached,
			"cache_creation_input_tokens": u.Detail.Write,
			"output_tokens":               u.Output,
		}
	}
	delta := map[string]any{"stop_reason": reason, "stop_sequence": nil}
	if s.judge != nil {
		delta["safeguard_results"] = []any{map[string]any{
			"type":   "dangerous_tool_use",
			"status": map[string]any{"type": "available", "tool_uses": s.judge(s.calls)},
		}}
	}
	if err := s.out.send(newEvent("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": delta,
		"usage": usage,
	})); err != nil {
		return err
	}
	return s.out.send(newEvent("message_stop", map[string]any{"type": "message_stop"}))
}

func (s *codexStream) fail(e apiError) error {
	if s.ended {
		return nil
	}
	s.ended = true
	if r, ok := s.out.(refuser); ok && r.refuse(e) {
		return nil
	}
	return s.out.send(e.event())
}

func errorEvent(kind, msg string) event {
	return newEvent("error", map[string]any{"type": "error", "error": map[string]any{"type": kind, "message": msg}})
}

func thinkingDelta(index int, text string) event {
	return newEvent("content_block_delta", map[string]any{
		"type": "content_block_delta", "index": index,
		"delta": map[string]any{"type": "thinking_delta", "thinking": text},
	})
}
