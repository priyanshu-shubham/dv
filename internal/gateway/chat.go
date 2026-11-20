package gateway

import (
	"bytes"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

// A Chat Completions model's reasoning rides in a thinking block's signature
// as well as its text, so it goes back as reasoning_content - which DeepSeek
// refuses a conversation with tools without - even where Claude Code kept
// only the signature.
const chatSig = "dvcc1."

func encodeChatReasoning(text string) string {
	return chatSig + base64.RawURLEncoding.EncodeToString([]byte(text))
}

func decodeChatReasoning(b block) (string, bool) {
	rest, ok := strings.CutPrefix(b.Signature, chatSig)
	if !ok {
		return "", false
	}
	if raw, err := base64.RawURLEncoding.DecodeString(rest); err == nil && len(raw) > 0 {
		return string(raw), true
	}
	return b.Thinking, true
}

// chatRequest is req as a Chat Completions endpoint takes it.
func chatRequest(req *anthropicRequest, slug, who string, levels []string) (map[string]any, *toolNames) {
	names := &toolNames{letter: true}
	var tools []map[string]any
	seen := map[string]bool{}
	deferred := map[string]toolDef{}
	add := func(d toolDef) {
		if d.Name == "" || seen[d.Name] {
			return
		}
		seen[d.Name] = true
		params := d.InputSchema
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": names.out(d.Name), "description": d.Description, "parameters": params}})
	}
	for _, raw := range req.Tools {
		var d toolDef
		if json.Unmarshal(raw, &d) != nil || d.Type != "" && d.Type != "custom" {
			continue
		}
		if d.DeferLoading {
			deferred[d.Name] = d
		} else {
			add(d)
		}
	}
	define := func(raw json.RawMessage) {
		var d toolDef
		if json.Unmarshal(raw, &d) == nil {
			add(d)
		}
	}
	loadNamed := func(name string) {
		if d, ok := deferred[name]; ok {
			add(d)
		}
	}

	msgs := []map[string]any{{"role": "system", "content": instructionsAs(req.System, who)}}
	var parts []map[string]any // what the user said, pending
	var pictures []map[string]any
	var assistant map[string]any
	flushUser := func(role string) {
		if len(parts) == 0 {
			return
		}
		if role == "developer" {
			role = "system"
		}
		msgs = append(msgs, map[string]any{"role": role, "content": chatContent(parts)})
		parts = nil
	}
	flushAssistant := func() {
		if assistant != nil {
			msgs = append(msgs, assistant)
			assistant = nil
		}
	}
	// Pictures a tool returned follow its result as the user's: a tool's
	// message takes only text.
	flushPictures := func() {
		if len(pictures) > 0 {
			msgs = append(msgs, map[string]any{"role": "user", "content": append([]map[string]any{{"type": "text", "text": "(The pictures the tool results above returned.)"}}, pictures...)})
			pictures = nil
		}
	}
	turn := func() map[string]any {
		if assistant == nil {
			assistant = map[string]any{"role": "assistant", "content": ""}
		}
		return assistant
	}
	effort := ""
	if oc := req.OutputConfig; oc != nil {
		effort = oc.Effort
	}
	for _, m := range req.Messages {
		role := m.Role
		if role == "system" {
			role = "developer"
			if oc := m.OutputConfig; oc != nil && oc.Effort != "" && effort == "" {
				effort = oc.Effort
			}
		}
		for _, b := range blocks(m.Content) {
			switch b.Type {
			case "text":
				if strings.TrimSpace(b.Text) == "" {
					continue
				}
				if role == "assistant" {
					a := turn()
					a["content"] = joinText(a["content"].(string), b.Text)
				} else {
					flushPictures()
					parts = append(parts, map[string]any{"type": "text", "text": b.Text})
				}
			case "image", "document":
				if role != "assistant" {
					flushPictures()
					parts = append(parts, chatMedia(b))
				}
			case "thinking":
				if text, ok := decodeChatReasoning(b); ok && role == "assistant" {
					a := turn()
					prev, _ := a["reasoning_content"].(string)
					a["reasoning_content"] = joinText(prev, text)
				}
			case "tool_use":
				a := turn()
				args := string(b.Input)
				if args == "" || args == "null" {
					args = "{}"
				}
				calls, _ := a["tool_calls"].([]map[string]any)
				a["tool_calls"] = append(calls, map[string]any{"id": b.ID, "type": "function", "function": map[string]any{"name": names.out(b.Name), "arguments": args}})
			case "tool_addition":
				if t := b.Tool; t != nil && len(t.Definition) > 0 {
					define(t.Definition)
				} else if t != nil {
					loadNamed(t.Name)
				}
			case "tool_result":
				flushUser(role)
				text, pics := chatToolOutput(b, define, loadNamed)
				msgs = append(msgs, map[string]any{"role": "tool", "tool_call_id": b.ToolUseID, "content": text})
				pictures = append(pictures, pics...)
			}
		}
		flushUser(role)
		flushAssistant()
	}
	flushPictures()
	msgs = chatPaired(msgs)

	out := map[string]any{
		"model":          slug,
		"messages":       msgs,
		"stream":         true,
		"stream_options": map[string]any{"include_usage": true},
	}
	if tools != nil {
		out["tools"], out["tool_choice"] = tools, "auto"
		if req.ToolChoice != nil && req.ToolChoice.DisableParallelToolUse {
			out["parallel_tool_calls"] = false
		}
	}
	if req.MaxTokens > 0 {
		out["max_tokens"] = req.MaxTokens
	}
	if req.Thinking != nil && req.Thinking.Type != "disabled" && effort != "" && len(levels) > 0 {
		out["reasoning_effort"] = fitEffort(effort, levels)
	}
	if oc := req.OutputConfig; oc != nil && oc.Format != nil && oc.Format.Type == "json_schema" {
		out["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "response", "schema": oc.Format.Schema, "strict": false}}
	}
	return out, names
}

func joinText(a, b string) string {
	if a == "" {
		return b
	}
	return a + "\n\n" + b
}

// chatContent is a message's parts, as a plain string where they are all
// text: some servers take nothing else.
func chatContent(parts []map[string]any) any {
	var text []string
	for _, p := range parts {
		if p["type"] != "text" {
			return parts
		}
		text = append(text, p["text"].(string))
	}
	return strings.Join(text, "\n\n")
}

func chatMedia(b block) map[string]any {
	s := b.Source
	switch {
	case s == nil:
	case b.Type == "image" && s.Type == "base64":
		return map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + s.MediaType + ";base64," + s.Data}}
	case b.Type == "image" && s.Type == "url":
		return map[string]any{"type": "image_url", "image_url": map[string]any{"url": s.URL}}
	case b.Type == "document" && s.Type == "text":
		return map[string]any{"type": "text", "text": s.Data}
	}
	return map[string]any{"type": "text", "text": "(A " + cmp.Or(b.Title, "document") + " was attached here, which this API can't be sent.)"}
}

// chatToolOutput is a tool result's text, and the pictures in it apart.
func chatToolOutput(b block, define func(json.RawMessage), load func(string)) (string, []map[string]any) {
	var text []string
	var pics []map[string]any
	for _, c := range blocks(b.Content) {
		switch c.Type {
		case "text":
			text = append(text, c.Text)
		case "image":
			pics = append(pics, chatMedia(c))
		case "document":
			if p := chatMedia(c); p["text"] != nil {
				text = append(text, p["text"].(string))
			}
		case "tool_reference":
			load(c.ToolName)
			text = append(text, "Tool loaded: "+c.ToolName)
		case "tool_definition":
			var d toolDef
			if json.Unmarshal(c.Definition, &d) == nil && d.Name != "" {
				define(c.Definition)
				text = append(text, "Tool loaded: "+d.Name)
			}
		}
	}
	out := strings.Join(text, "\n")
	if b.IsError {
		out = "Error: " + out
	}
	if out == "" && len(pics) > 0 {
		out = "(pictures, below)"
	}
	return out, pics
}

// chatPaired gives a tool call with no result one, and makes a result with
// no call words of the user's: either is refused.
func chatPaired(msgs []map[string]any) []map[string]any {
	calls, results := map[any]bool{}, map[any]bool{}
	for _, m := range msgs {
		if tc, ok := m["tool_calls"].([]map[string]any); ok {
			for _, c := range tc {
				calls[c["id"]] = true
			}
		}
		if m["role"] == "tool" {
			results[m["tool_call_id"]] = true
		}
	}
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		if m["role"] == "tool" && !calls[m["tool_call_id"]] {
			out = append(out, map[string]any{"role": "user", "content": "[An earlier tool result]: " + m["content"].(string)})
			continue
		}
		out = append(out, m)
		if tc, ok := m["tool_calls"].([]map[string]any); ok {
			for _, c := range tc {
				if !results[c["id"]] {
					out = append(out, map[string]any{"role": "tool", "tool_call_id": c["id"], "content": "(no result: the call was not run)"})
				}
			}
		}
	}
	return out
}

// serveChat answers a Messages request through a Chat Completions endpoint.
func (p *providerUpstream) serveChat(w http.ResponseWriter, r *http.Request, body []byte, m *masker) {
	var req anthropicRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	slug := strings.TrimPrefix(req.Model, p.prefix())
	levels := p.levels(slug)
	payload, names := chatRequest(&req, slug, slug+", served by "+p.cfg.Name, levels)
	if strings.HasSuffix(r.URL.Path, "/count_tokens") {
		json.NewEncoder(w).Encode(map[string]int{"input_tokens": estimateTokens(payload)})
		return
	}
	resp, b, err := p.postChat(r.Context(), payload)
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
	s := &chatStream{model: req.Model, names: names, out: out.out, calls: map[int]*chatCall{}, who: p.failures()}
	for _, sg := range req.Safeguards {
		if sg.Type == "dangerous_tool_use" {
			s.judge = func(calls []toolCall) map[string]any {
				return judged(r.Context(), sg, &req, calls, func(ctx context.Context, ask string) (string, error) {
					return p.chatVerdicts(ctx, slug, ask)
				})
			}
		}
	}
	s.run(resp.Body)
	out.close()
}

func (p *providerUpstream) postChat(ctx context.Context, payload map[string]any) (*http.Response, []byte, error) {
	p.withoutRefused(payload)
	refreshed := false
	for try := 0; ; try++ {
		data, _ := json.Marshal(payload)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.cfg.URL+"/chat/completions", bytes.NewReader(data))
		p.authorize(req.Header)
		req.Header.Set("Content-Type", "application/json")
		if payload["stream"] == true {
			req.Header.Set("Accept", "text/event-stream")
		}
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
		// Some servers answer a parameter they don't know with 422.
		bad := resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnprocessableEntity
		if !bad || try >= 4 || !p.easeChat(payload, b) {
			return resp, b, nil
		}
	}
}

var number = regexp.MustCompile(`\d+`)

// easeChat takes out of payload what a 400 said was wrong with it, false
// where there was nothing to take. Too many tokens asked for is asked again
// at the most the message names, where it names one.
func (p *providerUpstream) easeChat(payload map[string]any, b []byte) bool {
	_, msg := providerFailure(b)
	if msg == "" {
		msg = string(b)
	}
	low := strings.ToLower(msg)
	if want, ok := payload["max_tokens"].(int); ok && strings.Contains(low, "max_tokens") && !tooLong.MatchString(msg) {
		most := 0
		for _, n := range number.FindAllString(msg, -1) {
			if v, _ := strconv.Atoi(n); v >= 1024 && v < want && v > most {
				most = v
			}
		}
		if most > 0 {
			payload["max_tokens"] = most
		} else {
			delete(payload, "max_tokens")
		}
		return true
	}
	return p.dropRefused(payload, msg, "reasoning_effort", "stream_options", "parallel_tool_calls", "response_format", "tool_choice")
}

// chatVerdicts has the model judge auto mode's calls, its answer held to the
// verdicts' schema where the endpoint takes one.
func (p *providerUpstream) chatVerdicts(ctx context.Context, slug, ask string) (string, error) {
	payload := map[string]any{
		"model": slug,
		"messages": []map[string]any{
			{"role": "system", "content": judgeInstructions + "\n\n" + verdictShape},
			{"role": "user", "content": ask},
		},
		"response_format": map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "verdicts", "strict": true, "schema": verdictSchema}},
	}
	resp, b, err := p.postChat(ctx, payload)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, msg := providerFailure(b)
		return "", fmt.Errorf("%s: %s", resp.Status, msg)
	}
	var done struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&done); err != nil {
		return "", err
	}
	if len(done.Choices) == 0 {
		return "", errors.New("it gave no answer")
	}
	return jsonIn(done.Choices[0].Message.Content), nil
}

// verdictShape tells a model held to no schema how to answer.
const verdictShape = `Answer with JSON alone: {"verdicts":[{"id":"<the action's id>","flagged":true|false,"reason":"<why>"}]}`

// jsonIn is the JSON object in a model's answer, without the fence or words
// around it a model held to no schema may put there.
func jsonIn(text string) string {
	i, j := strings.Index(text, "{"), strings.LastIndex(text, "}")
	if i < 0 || j < i {
		return text
	}
	return text[i : j+1]
}
