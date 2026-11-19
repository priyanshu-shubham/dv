package gateway

import (
	"cmp"
	"encoding/json"
	"io"
	"strings"
)

// geminiStream turns an Interactions stream into the Messages stream Claude
// Code reads, a step at a time.
type geminiStream struct {
	model string
	names *toolNames
	out   sink
	next  int
	steps map[int]*geminiStep // by the step's index
	tools bool
	said  bool // text, a call or a search, past thinking
	start bool
	ended bool
	calls []toolCall
	judge func([]toolCall) map[string]any
	// queries are the searches run since the last results were told.
	queries []string
}

type geminiStep struct {
	index    int // its block's
	kind     string
	id, name string
	args     strings.Builder
	given    json.RawMessage // arguments that came whole, with the step
	thought  thoughtStep
	queries  []string
	sources  []map[string]any
}

type geminiUsage struct {
	Input   int `json:"total_input_tokens"`
	Cached  int `json:"total_cached_tokens"`
	Output  int `json:"total_output_tokens"`
	Thought int `json:"total_thought_tokens"`
}

type geminiStepBody struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Signature string          `json:"signature"`
	Summary   []struct {
		Text string `json:"text"`
	} `json:"summary"`
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
	Result json.RawMessage `json:"result"`
}

type geminiDelta struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Signature string          `json:"signature"`
	Arguments json.RawMessage `json:"arguments"`
	Content   struct {
		Text string `json:"text"`
	} `json:"content"`
	Result json.RawMessage `json:"result"`
}

func (s *geminiStream) run(body io.Reader) error {
	err := readSSE(body, s.event)
	if !s.ended {
		return s.fail(geminiFailures.classify(0, "", "the reply ended before it was done"))
	}
	return err
}

func (s *geminiStream) event(ev event) error {
	var d struct {
		Type        string          `json:"event_type"`
		Index       int             `json:"index"`
		Step        geminiStepBody  `json:"step"`
		Delta       geminiDelta     `json:"delta"`
		Error       json.RawMessage `json:"error"`
		Interaction struct {
			ID     string       `json:"id"`
			Status string       `json:"status"`
			Usage  *geminiUsage `json:"usage"`
		} `json:"interaction"`
	}
	if json.Unmarshal(ev.data, &d) != nil {
		return nil // [DONE]
	}
	switch d.Type {
	case "interaction.created":
		return s.begin(d.Interaction.ID)
	case "step.start":
		return s.started(d.Index, d.Step)
	case "step.delta":
		return s.delta(d.Index, d.Delta)
	case "step.stop":
		return s.stopped(d.Index)
	case "interaction.completed":
		switch d.Interaction.Status {
		case "failed", "cancelled":
			return s.fail(geminiFailures.classify(0, d.Interaction.Status, "the model "+d.Interaction.Status))
		case "incomplete", "requires_continuation":
			return s.finish("max_tokens", d.Interaction.Usage)
		}
		// A reply of nothing, or of thinking alone, is Gemini failing:
		// ended as it stands, the agent's run would stop mid-task.
		if !s.said {
			return s.fail(geminiFailures.classify(0, "", "the model answered with nothing"))
		}
		if s.tools {
			return s.finish("tool_use", d.Interaction.Usage)
		}
		return s.finish("end_turn", d.Interaction.Usage)
	case "error":
		code, msg := geminiFailure([]byte(`{"error":` + string(d.Error) + `}`))
		return s.fail(geminiFailures.classify(0, code, cmp.Or(msg, "the model failed")))
	}
	return nil
}

func (s *geminiStream) begin(id string) error {
	if s.start {
		return nil
	}
	s.start = true
	return s.out.send(messageStart("msg_"+id, s.model))
}

// open starts a block for a step.
func (s *geminiStream) open(at int, st *geminiStep, block map[string]any) error {
	if err := s.begin(""); err != nil {
		return err
	}
	st.index = s.next
	s.next++
	s.steps[at] = st
	return s.out.send(newEvent("content_block_start", map[string]any{"type": "content_block_start", "index": st.index, "content_block": block}))
}

func (s *geminiStream) started(at int, step geminiStepBody) error {
	switch step.Type {
	case "thought":
		st := &geminiStep{kind: "thinking", thought: thoughtStep{Signature: step.Signature}}
		if err := s.open(at, st, map[string]any{"type": "thinking", "thinking": "", "signature": ""}); err != nil {
			return err
		}
		for _, p := range step.Summary {
			if err := s.summary(st, p.Text); err != nil {
				return err
			}
		}
	case "model_output":
		st := &geminiStep{kind: "text"}
		if err := s.open(at, st, map[string]any{"type": "text", "text": ""}); err != nil {
			return err
		}
		for _, p := range step.Content {
			if p.Text != "" {
				s.said = true
				if err := s.out.send(deltaEvent(st.index, "text_delta", p.Text)); err != nil {
					return err
				}
			}
		}
	case "function_call":
		s.tools, s.said = true, true
		name := s.names.in(step.Name)
		st := &geminiStep{kind: "tool_use", id: step.ID, name: name, given: step.Arguments}
		return s.open(at, st, map[string]any{"type": "tool_use", "id": step.ID, "name": name, "input": map[string]any{}})
	case "google_search_call":
		var args struct {
			Queries []string `json:"queries"`
			Query   string   `json:"query"`
		}
		json.Unmarshal(step.Arguments, &args)
		if args.Query != "" {
			args.Queries = append(args.Queries, args.Query)
		}
		s.steps[at] = &geminiStep{kind: "search_call", id: step.ID, queries: args.Queries}
	case "google_search_result":
		s.steps[at] = &geminiStep{kind: "search_result", id: step.ID, sources: searchSources(step.Result)}
	}
	return nil
}

func searchSources(raw json.RawMessage) []map[string]any {
	var items []struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	json.Unmarshal(raw, &items)
	var out []map[string]any
	for _, it := range items {
		if it.URL != "" {
			out = append(out, map[string]any{"type": "web_search_result", "url": it.URL, "title": cmp.Or(it.Title, it.URL)})
		}
	}
	return out
}

// summary adds to a thought's summary, as it streams: deltas of one part.
func (s *geminiStream) summary(st *geminiStep, text string) error {
	if text == "" {
		return nil
	}
	if len(st.thought.Summary) == 0 {
		st.thought.Summary = []string{""}
	}
	st.thought.Summary[0] += text
	return s.out.send(thinkingDelta(st.index, text))
}

func (s *geminiStream) delta(at int, d geminiDelta) error {
	st := s.steps[at]
	if st == nil {
		return nil
	}
	switch {
	case d.Type == "text" && st.kind == "text":
		if d.Text != "" {
			s.said = true
			return s.out.send(deltaEvent(st.index, "text_delta", d.Text))
		}
	case d.Type == "thought_summary" && st.kind == "thinking":
		return s.summary(st, d.Content.Text)
	case d.Type == "thought_signature" && st.kind == "thinking":
		st.thought.Signature += d.Signature
	case d.Type == "arguments_delta" && st.kind == "tool_use":
		var part string
		if json.Unmarshal(d.Arguments, &part) != nil {
			part = string(d.Arguments)
		}
		st.args.WriteString(part)
		return s.out.send(deltaEvent(st.index, "input_json_delta", part))
	case d.Type == "google_search_call" && st.kind == "search_call":
		var args struct {
			Queries []string `json:"queries"`
		}
		json.Unmarshal(d.Arguments, &args)
		st.queries = append(st.queries, args.Queries...)
	case d.Type == "google_search_result" && st.kind == "search_result":
		st.sources = append(st.sources, searchSources(d.Result)...)
	}
	return nil
}

func (s *geminiStream) stopped(at int) error {
	st := s.steps[at]
	if st == nil {
		return nil
	}
	delete(s.steps, at)
	switch st.kind {
	case "search_call":
		s.queries = append(s.queries, st.queries...)
		return nil
	case "search_result":
		query := strings.Join(s.queries, "; ")
		s.queries = nil
		if err := s.begin(""); err != nil {
			return err
		}
		if st.sources == nil {
			st.sources = []map[string]any{}
		}
		use := "srvtoolu_" + notInName.ReplaceAllString(cmp.Or(st.id, "search"), "_")
		s.said = true
		for _, ev := range searchEvents(s.next, use, query, st.sources) {
			if err := s.out.send(ev); err != nil {
				return err
			}
		}
		s.next += 2
		return nil
	case "thinking":
		if err := s.out.send(newEvent("content_block_delta", map[string]any{
			"type": "content_block_delta", "index": st.index,
			"delta": map[string]any{"type": "signature_delta", "signature": encodeThought(st.thought)},
		})); err != nil {
			return err
		}
	case "tool_use":
		args := st.args.String()
		if args == "" {
			args = "{}"
			if len(st.given) > 0 && string(st.given) != "null" {
				args = string(st.given)
			}
			if err := s.out.send(deltaEvent(st.index, "input_json_delta", args)); err != nil {
				return err
			}
		}
		s.calls = append(s.calls, toolCall{ID: st.id, Name: st.name, Input: args})
	}
	return s.out.send(newEvent("content_block_stop", map[string]any{"type": "content_block_stop", "index": st.index}))
}

func (s *geminiStream) finish(reason string, u *geminiUsage) error {
	if s.ended {
		return nil
	}
	if err := s.begin(""); err != nil {
		return err
	}
	for at, st := range s.steps {
		if st.kind != "search_call" && st.kind != "search_result" {
			if err := s.stopped(at); err != nil {
				return err
			}
		}
	}
	s.ended = true
	usage := map[string]any{"input_tokens": 0, "output_tokens": 0}
	if u != nil {
		usage = map[string]any{
			"input_tokens":            max(0, u.Input-u.Cached),
			"cache_read_input_tokens": u.Cached,
			"output_tokens":           u.Output + u.Thought,
		}
	}
	return messageEnd(s.out, reason, usage, s.judge, s.calls)
}

func (s *geminiStream) fail(e apiError) error {
	if s.ended {
		return nil
	}
	s.ended = true
	if r, ok := s.out.(refuser); ok && r.refuse(e) {
		return nil
	}
	return s.out.send(e.event())
}
