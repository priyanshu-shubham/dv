package gateway

import (
	"bytes"
	"cmp"
	"crypto/rand"
	"encoding/json"
	"io"
	"strings"
)

// chatStream turns a Chat Completions stream into the Messages stream Claude
// Code reads. Reasoning, text and each tool call are blocks of their own,
// one open at a time, as the chunks bring them.
type chatStream struct {
	model string
	names *toolNames
	out   sink
	who   failures

	next      int
	open      string // the kind of the block open: thinking, text, tool_use; "" for none
	reasoning strings.Builder
	calls     map[int]*chatCall // by the call's index
	callAt    int
	done      []toolCall
	start     bool
	ended     bool
	finish    string
	usage     *chatUsage
	judge     func([]toolCall) map[string]any
}

type chatCall struct {
	index    int
	id, name string
	args     strings.Builder
	started  bool
}

type chatUsage struct {
	Prompt     int `json:"prompt_tokens"`
	Completion int `json:"completion_tokens"`
	Details    *struct {
		Cached int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
	CacheHit int `json:"prompt_cache_hit_tokens"` // DeepSeek's
}

func (s *chatStream) run(body io.Reader) error {
	err := readSSE(body, s.event)
	if !s.ended {
		if s.finish != "" || s.start {
			return s.end()
		}
		return s.fail(s.who.classify(0, "", "the reply ended before it was done"))
	}
	return err
}

func (s *chatStream) event(ev event) error {
	data := bytes.TrimSpace(ev.data)
	if bytes.Equal(data, []byte("[DONE]")) {
		return s.end()
	}
	var d struct {
		ID      string          `json:"id"`
		Error   json.RawMessage `json:"error"`
		Usage   *chatUsage      `json:"usage"`
		Choices []struct {
			Delta struct {
				Content          *string `json:"content"`
				ReasoningContent *string `json:"reasoning_content"`
				Reasoning        *string `json:"reasoning"`
				ToolCalls        []struct {
					Index    *int   `json:"index"`
					ID       string `json:"id"`
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"delta"`
			Finish *string `json:"finish_reason"`
		} `json:"choices"`
	}
	if json.Unmarshal(data, &d) != nil {
		return nil
	}
	if len(d.Error) > 0 && string(d.Error) != "null" {
		code, msg := providerFailure(data)
		return s.fail(s.who.classify(0, code, cmp.Or(msg, string(d.Error))))
	}
	if err := s.begin(d.ID); err != nil {
		return err
	}
	if d.Usage != nil {
		s.usage = d.Usage
	}
	for _, c := range d.Choices {
		delta := c.Delta
		if r := cmp.Or(delta.ReasoningContent, delta.Reasoning); r != nil && *r != "" {
			if err := s.to("thinking"); err != nil {
				return err
			}
			s.reasoning.WriteString(*r)
			if err := s.out.send(thinkingDelta(s.next-1, *r)); err != nil {
				return err
			}
		}
		if delta.Content != nil && *delta.Content != "" {
			if err := s.to("text"); err != nil {
				return err
			}
			if err := s.out.send(deltaEvent(s.next-1, "text_delta", *delta.Content)); err != nil {
				return err
			}
		}
		for _, tc := range delta.ToolCalls {
			at := s.callAt
			if tc.Index != nil {
				at = *tc.Index
			} else if c := s.calls[at]; c != nil && tc.ID != "" && c.id != "" && c.id != tc.ID {
				at++ // a server that numbers no calls starts the next by its id
			}
			call := s.calls[at]
			if call == nil {
				call = &chatCall{}
				s.calls[at] = call
			}
			if tc.ID != "" {
				call.id = tc.ID
			}
			if tc.Function.Name != "" {
				call.name = s.names.in(tc.Function.Name)
			}
			call.args.WriteString(tc.Function.Arguments)
			if err := s.toCall(at, call, tc.Function.Arguments); err != nil {
				return err
			}
		}
		if c.Finish != nil && *c.Finish != "" {
			s.finish = *c.Finish
		}
	}
	return nil
}

func (s *chatStream) begin(id string) error {
	if s.start {
		return nil
	}
	s.start = true
	return s.out.send(messageStart(id, s.model))
}

// to has a block of kind open, closing another.
func (s *chatStream) to(kind string) error {
	if s.open == kind {
		return nil
	}
	if err := s.closeOpen(); err != nil {
		return err
	}
	s.open = kind
	start := map[string]any{"type": "text", "text": ""}
	if kind == "thinking" {
		start = map[string]any{"type": "thinking", "thinking": "", "signature": ""}
	}
	s.next++
	return s.out.send(newEvent("content_block_start", map[string]any{"type": "content_block_start", "index": s.next - 1, "content_block": start}))
}

// toCall streams a call's arguments, opening its block once its name has
// come; a call's arguments held till then go out with it.
func (s *chatStream) toCall(at int, call *chatCall, args string) error {
	if !call.started {
		if call.name == "" {
			return nil
		}
		if err := s.closeOpen(); err != nil {
			return err
		}
		call.started, call.index, s.open, s.callAt = true, s.next, "tool_use", at
		s.next++
		if call.id == "" {
			call.id = "call_" + rand.Text()
		}
		if err := s.out.send(newEvent("content_block_start", map[string]any{"type": "content_block_start", "index": call.index,
			"content_block": map[string]any{"type": "tool_use", "id": call.id, "name": call.name, "input": map[string]any{}}})); err != nil {
			return err
		}
		args = call.args.String()
	}
	if args == "" {
		return nil
	}
	return s.out.send(deltaEvent(call.index, "input_json_delta", args))
}

func (s *chatStream) closeOpen() error {
	switch s.open {
	case "":
		return nil
	case "thinking":
		if err := s.out.send(newEvent("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": s.next - 1,
			"delta": map[string]any{"type": "signature_delta", "signature": encodeChatReasoning(s.reasoning.String())},
		})); err != nil {
			return err
		}
		s.reasoning.Reset()
	case "tool_use":
		call := s.calls[s.callAt]
		args := strings.TrimSpace(call.args.String())
		if args == "" {
			args = "{}"
			if err := s.out.send(deltaEvent(call.index, "input_json_delta", args)); err != nil {
				return err
			}
		}
		s.done = append(s.done, toolCall{ID: call.id, Name: call.name, Input: args})
		delete(s.calls, s.callAt)
		s.callAt++
		s.open = ""
		return s.out.send(newEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": call.index}))
	}
	at := s.next - 1
	s.open = ""
	return s.out.send(newEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": at}))
}

func (s *chatStream) end() error {
	if s.ended {
		return nil
	}
	if err := s.begin(""); err != nil {
		return err
	}
	if err := s.closeOpen(); err != nil {
		return err
	}
	s.ended = true
	reason := "end_turn"
	switch {
	case len(s.done) > 0:
		reason = "tool_use"
	case s.finish == "length":
		reason = "max_tokens"
	case s.finish == "content_filter":
		reason = "refusal"
	}
	usage := map[string]any{"input_tokens": 0, "output_tokens": 0}
	if u := s.usage; u != nil {
		cached := u.CacheHit
		if u.Details != nil {
			cached = max(cached, u.Details.Cached)
		}
		usage = map[string]any{"input_tokens": max(0, u.Prompt-cached), "cache_read_input_tokens": cached, "output_tokens": u.Completion}
	}
	return messageEnd(s.out, reason, usage, s.judge, s.done)
}

func (s *chatStream) fail(e apiError) error {
	if s.ended {
		return nil
	}
	s.ended = true
	if r, ok := s.out.(refuser); ok && r.refuse(e) {
		return nil
	}
	return s.out.send(e.event())
}
