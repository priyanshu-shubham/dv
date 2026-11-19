package gateway

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
)

// anthropicRequest is what of a Messages request a Codex model is asked with.
type anthropicRequest struct {
	Model      string            `json:"model"`
	System     json.RawMessage   `json:"system"`
	Messages   []anthropicMsg    `json:"messages"`
	Tools      []json.RawMessage `json:"tools"`
	ToolChoice *struct {
		DisableParallelToolUse bool `json:"disable_parallel_tool_use"`
	} `json:"tool_choice"`
	Thinking *struct {
		Type string `json:"type"`
	} `json:"thinking"`
	OutputConfig *outputConfig `json:"output_config"`
	Stream       bool          `json:"stream"`
	Speed        string        `json:"speed"`
	Safeguards   []safeguard   `json:"safeguards"`
	Metadata     *struct {
		UserID string `json:"user_id"`
	} `json:"metadata"`
}

type outputConfig struct {
	Effort string `json:"effort"`
	Format *struct {
		Type   string          `json:"type"`
		Schema json.RawMessage `json:"schema"`
	} `json:"format"`
}

type anthropicMsg struct {
	Role         string          `json:"role"`
	Content      json.RawMessage `json:"content"`
	OutputConfig *outputConfig   `json:"output_config"`
}

type block struct {
	Type       string          `json:"type"`
	Text       string          `json:"text"`
	ID         string          `json:"id"`
	Name       string          `json:"name"`
	Input      json.RawMessage `json:"input"`
	ToolUseID  string          `json:"tool_use_id"`
	Content    json.RawMessage `json:"content"`
	IsError    bool            `json:"is_error"`
	Signature  string          `json:"signature"`
	ToolName   string          `json:"tool_name"`
	Definition json.RawMessage `json:"definition"`
	Title      string          `json:"title"`
	Source     *struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
	} `json:"source"`
	Tool *struct {
		Type       string          `json:"type"`
		Name       string          `json:"name"`
		Definition json.RawMessage `json:"definition"`
	} `json:"tool"`
}

type toolDef struct {
	Type         string          `json:"type"`
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	DeferLoading bool            `json:"defer_loading"`
}

// blocks reads a message's content, a string being one text block.
func blocks(raw json.RawMessage) []block {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return []block{{Type: "text", Text: s}}
	}
	var bs []block
	json.Unmarshal(raw, &bs)
	return bs
}

// A Codex reasoning item rides in a thinking block's signature, which Claude
// Code sends back as it came.
const reasoningSig = "dvcx1."

type reasoningItem struct {
	ID        string   `json:"i"`
	Encrypted string   `json:"e"`
	Summary   []string `json:"s,omitempty"`
}

func encodeReasoning(r reasoningItem) string {
	b, _ := json.Marshal(r)
	return reasoningSig + base64.RawURLEncoding.EncodeToString(b)
}

func decodeReasoning(sig string) (reasoningItem, bool) {
	var r reasoningItem
	rest, ok := strings.CutPrefix(sig, reasoningSig)
	if !ok {
		return r, false
	}
	b, err := base64.RawURLEncoding.DecodeString(rest)
	return r, err == nil && json.Unmarshal(b, &r) == nil && r.Encrypted != ""
}

var (
	functionName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
	notInName    = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
	// Claude Code's first system words, for Anthropic alone: its cch changes
	// every turn, which would leave no prefix the same to cache.
	billingLine = regexp.MustCompile(`^x-anthropic-billing-header:(\s*[A-Za-z_]+=[^;\s]*;)*\s*`)
)

// toolNames maps a tool's name to one OpenAI takes, and back.
type toolNames struct{ back map[string]string }

func (t *toolNames) out(name string) string {
	if functionName.MatchString(name) {
		return name
	}
	sum := sha256.Sum256([]byte(name))
	clean := notInName.ReplaceAllString(name, "_")
	short := clean[:min(len(clean), 50)] + "_" + hex.EncodeToString(sum[:])[:8]
	if t.back == nil {
		t.back = map[string]string{}
	}
	t.back[short] = name
	return short
}

func (t *toolNames) in(name string) string {
	if n, ok := t.back[name]; ok {
		return n
	}
	return name
}

// claudeEfforts are the levels Claude Code sends; a Codex model may take more.
var claudeEfforts = []string{"low", "medium", "high", "xhigh", "max"}

var effortOrder = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max", "ultra"}

// fitEffort is the model's level nearest the one asked for, a tie going up.
// Ultra is a level of Codex's picker, not of the API.
func fitEffort(want string, levels []string) string {
	levels = slices.DeleteFunc(slices.Clone(levels), func(l string) bool { return l == "ultra" })
	if want == "ultra" {
		want = "max"
	}
	if len(levels) == 0 || slices.Contains(levels, want) {
		return want
	}
	at := slices.Index(effortOrder, want)
	best, gap := "", len(effortOrder)
	for _, l := range levels {
		i := slices.Index(effortOrder, l)
		if i < 0 {
			continue
		}
		d := i - at
		if d < 0 {
			d = -d*2 + 1 // a tie goes up
		} else {
			d *= 2
		}
		if d < gap {
			best, gap = l, d
		}
	}
	return best
}

// lowestEffort is what a request without thinking runs at: none where the
// model has it. Claude Code's side requests, the auto mode classifier among
// them, wait a minute at most.
func lowestEffort(levels []string) string {
	for _, l := range effortOrder {
		if slices.Contains(levels, l) {
			return l
		}
	}
	return ""
}

func functionTool(d toolDef, names *toolNames) map[string]any {
	params := d.InputSchema
	if len(params) == 0 {
		params = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return map[string]any{"type": "function", "name": names.out(d.Name), "description": d.Description, "parameters": params, "strict": false}
}

func developerMessage(text string) map[string]any {
	return map[string]any{"type": "message", "role": "developer", "content": []map[string]any{{"type": "input_text", "text": text}}}
}

// codexRequest is req as the ChatGPT backend's Responses API takes it.
func codexRequest(req *anthropicRequest, slug, cacheKey string, levels []string) (map[string]any, *toolNames) {
	names := &toolNames{}
	// Tools leading the prompt stay as they were, so its cached prefix does:
	// one loaded later goes in an additional_tools item where it was loaded.
	var tools []map[string]any
	seen := map[string]bool{}
	deferred := map[string]toolDef{}
	webSearch := false
	for _, raw := range req.Tools {
		var d toolDef
		if json.Unmarshal(raw, &d) != nil {
			continue
		}
		if d.Type != "" && d.Type != "custom" {
			webSearch = webSearch || strings.HasPrefix(d.Type, "web_search")
			continue
		}
		switch {
		case d.Name == "":
		case d.DeferLoading:
			deferred[d.Name] = d
		case !seen[d.Name]:
			seen[d.Name] = true
			tools = append(tools, functionTool(d, names))
		}
	}

	input := []map[string]any{developerMessage(instructions(req.System, slug))}
	var loaded, userParts, assistantText []map[string]any
	load := func(d toolDef) {
		if d.Name != "" && !seen[d.Name] {
			seen[d.Name] = true
			loaded = append(loaded, functionTool(d, names))
		}
	}
	define := func(raw json.RawMessage) {
		var d toolDef
		if json.Unmarshal(raw, &d) == nil {
			load(d)
		}
	}
	loadNamed := func(name string) {
		if d, ok := deferred[name]; ok {
			load(d)
		}
	}
	flushLoaded := func() {
		if len(loaded) > 0 {
			input = append(input, map[string]any{"type": "additional_tools", "role": "developer", "tools": loaded})
			loaded = nil
		}
	}
	flushUser := func(role string) {
		if len(userParts) > 0 {
			input = append(input, map[string]any{"type": "message", "role": role, "content": userParts})
			userParts = nil
		}
	}
	flushAssistant := func() {
		if len(assistantText) > 0 {
			input = append(input, map[string]any{"type": "message", "role": "assistant", "content": assistantText})
			assistantText = nil
		}
	}
	effort := ""
	if oc := req.OutputConfig; oc != nil {
		effort = oc.Effort
	}
	for _, m := range req.Messages {
		role := m.Role
		if role == "system" {
			role = "developer"
			if oc := m.OutputConfig; oc != nil && oc.Effort != "" && (req.OutputConfig == nil || req.OutputConfig.Effort == "") {
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
					assistantText = append(assistantText, map[string]any{"type": "output_text", "text": b.Text})
				} else {
					userParts = append(userParts, map[string]any{"type": "input_text", "text": b.Text})
				}
			case "image", "document":
				if p := mediaPart(b); p != nil && role != "assistant" {
					userParts = append(userParts, p)
				}
			case "tool_addition":
				if t := b.Tool; t != nil && len(t.Definition) > 0 {
					define(t.Definition)
				} else if t != nil {
					loadNamed(t.Name)
				}
			case "tool_result":
				flushUser(role)
				input = append(input, map[string]any{"type": "function_call_output", "call_id": b.ToolUseID, "output": toolOutput(b, define, loadNamed)})
			case "tool_use":
				flushAssistant()
				args := string(b.Input)
				if args == "" || args == "null" {
					args = "{}"
				}
				input = append(input, map[string]any{"type": "function_call", "call_id": b.ID, "name": names.out(b.Name), "arguments": args})
			case "thinking":
				if r, ok := decodeReasoning(b.Signature); ok {
					flushAssistant()
					summary := []map[string]any{}
					for _, s := range r.Summary {
						summary = append(summary, map[string]any{"type": "summary_text", "text": s})
					}
					input = append(input, map[string]any{"type": "reasoning", "id": r.ID, "summary": summary, "encrypted_content": r.Encrypted})
				}
			}
		}
		flushLoaded()
		flushUser(role)
		flushAssistant()
	}
	input = paired(input)

	if webSearch {
		tools = append(tools, map[string]any{"type": "web_search"})
	}
	include := []string{"reasoning.encrypted_content"}
	if webSearch {
		include = append(include, "web_search_call.action.sources")
	}
	out := map[string]any{
		"model":               slug,
		"input":               input,
		"parallel_tool_calls": req.ToolChoice == nil || !req.ToolChoice.DisableParallelToolUse,
		"store":               false,
		"stream":              true,
		"include":             include,
		"prompt_cache_key":    cacheKey,
	}
	// Forced choices are refused here; Claude Code's prompts ask for tools anyway.
	if tools != nil {
		out["tools"], out["tool_choice"] = tools, "auto"
	}

	thinking := req.Thinking != nil && req.Thinking.Type != "disabled"
	reasoning := map[string]any{}
	switch {
	case !thinking:
		effort = lowestEffort(levels)
	case effort != "":
		effort = fitEffort(effort, levels)
	}
	if effort != "" {
		reasoning["effort"] = effort
	}
	// Summaries even where Claude Code asks for Claude's thinking omitted:
	// they are all of a Codex model's reasoning there is to show.
	if thinking {
		reasoning["summary"] = "auto"
	}
	if len(reasoning) > 0 {
		out["reasoning"] = reasoning
	}
	if effort == "none" {
		include = slices.DeleteFunc(include, func(s string) bool { return s == "reasoning.encrypted_content" })
		out["include"] = include
	}
	if oc := req.OutputConfig; oc != nil && oc.Format != nil && oc.Format.Type == "json_schema" {
		out["text"] = map[string]any{"format": map[string]any{"type": "json_schema", "name": "response", "schema": oc.Format.Schema, "strict": false}}
	}
	if req.Speed == "fast" {
		out["service_tier"] = "priority"
	}
	return out, names
}

// paired gives a call with no output one, and an output with no call words
// of its own: the backend refuses the whole conversation over either.
func paired(input []map[string]any) []map[string]any {
	calls, outputs := map[any]bool{}, map[any]bool{}
	for _, it := range input {
		switch it["type"] {
		case "function_call":
			calls[it["call_id"]] = true
		case "function_call_output":
			outputs[it["call_id"]] = true
		}
	}
	out := make([]map[string]any, 0, len(input))
	for _, it := range input {
		switch it["type"] {
		case "function_call_output":
			if !calls[it["call_id"]] {
				text, _ := json.Marshal(it["output"])
				out = append(out, map[string]any{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": "[An earlier tool result]: " + string(text)}}})
				continue
			}
		}
		out = append(out, it)
		if it["type"] == "function_call" && !outputs[it["call_id"]] {
			out = append(out, map[string]any{"type": "function_call_output", "call_id": it["call_id"], "output": "(no result: the call was not run)"})
		}
	}
	return out
}

// instructions are Claude Code's system prompt, which tells the model it is
// Claude; a line ahead of it says otherwise. It is the same on every request,
// so it stays in the cached prefix.
func instructions(system json.RawMessage, slug string) string {
	var parts []string
	for _, b := range blocks(system) {
		if b.Type != "text" {
			continue
		}
		if t := strings.TrimSpace(billingLine.ReplaceAllString(b.Text, "")); t != "" {
			parts = append(parts, t)
		}
	}
	head := "You are " + slug + ", an OpenAI model, working as the model behind Claude Code. What follows was written for Claude; where it names the model you are, it means you. Use the tools as described."
	return head + "\n\n" + strings.Join(parts, "\n\n")
}

func mediaPart(b block) map[string]any {
	s := b.Source
	if s == nil {
		return nil
	}
	switch {
	case b.Type == "image" && s.Type == "base64":
		return map[string]any{"type": "input_image", "image_url": "data:" + s.MediaType + ";base64," + s.Data}
	case b.Type == "image" && s.Type == "url":
		return map[string]any{"type": "input_image", "image_url": s.URL}
	case b.Type == "document" && s.Type == "base64":
		name := b.Title
		if name == "" {
			name = "document.pdf"
		}
		return map[string]any{"type": "input_file", "filename": name, "file_data": "data:" + s.MediaType + ";base64," + s.Data}
	case b.Type == "document" && s.Type == "text":
		return map[string]any{"type": "input_text", "text": s.Data}
	}
	return nil
}

// toolOutput is a tool result as a function call's output: its text, or
// with images or documents in it, its parts. A ToolSearch result loads the
// tools it names, by reference to a deferred one or with the definition.
func toolOutput(b block, define func(json.RawMessage), load func(string)) any {
	var text []string
	var parts []map[string]any
	loaded := func(name string) {
		t := "Tool loaded: " + name
		text = append(text, t)
		parts = append(parts, map[string]any{"type": "input_text", "text": t})
	}
	for _, c := range blocks(b.Content) {
		switch c.Type {
		case "text":
			text = append(text, c.Text)
			parts = append(parts, map[string]any{"type": "input_text", "text": c.Text})
		case "image", "document":
			if p := mediaPart(c); p != nil {
				parts = append(parts, p)
			}
		case "tool_reference":
			load(c.ToolName)
			loaded(c.ToolName)
		case "tool_definition":
			var d toolDef
			if json.Unmarshal(c.Definition, &d) == nil && d.Name != "" {
				define(c.Definition)
				loaded(d.Name)
			}
		}
	}
	if len(parts) > len(text) {
		return parts
	}
	return strings.Join(text, "\n")
}

// continuesTurn says the request answers the model's tool calls, rather than
// starting a turn with something the user said.
func continuesTurn(req *anthropicRequest) bool {
	if len(req.Messages) == 0 {
		return false
	}
	last := req.Messages[len(req.Messages)-1]
	if last.Role != "user" {
		return false
	}
	results := 0
	for _, b := range blocks(last.Content) {
		switch b.Type {
		case "tool_result":
			results++
		case "text":
			if !strings.HasPrefix(strings.TrimSpace(b.Text), "<system-reminder>") {
				return false
			}
		}
	}
	return results > 0
}

// withoutReasoning is a Responses request without the reasoning it replays,
// for when the backend can't read it: sealed for another ChatGPT sign-in.
func withoutReasoning(payload map[string]any) map[string]any {
	input, _ := payload["input"].([]map[string]any)
	payload["input"] = slices.DeleteFunc(slices.Clone(input), func(it map[string]any) bool { return it["type"] == "reasoning" })
	return payload
}

// stripCodexReasoning drops Codex's reasoning from a request going to
// Anthropic, which would refuse its signatures: a session moved from a Codex
// model to Claude.
func stripCodexReasoning(body []byte) []byte {
	var req map[string]json.RawMessage
	if json.Unmarshal(body, &req) != nil {
		return body
	}
	var msgs []map[string]json.RawMessage
	if json.Unmarshal(req["messages"], &msgs) != nil {
		return body
	}
	for _, m := range msgs {
		var bs []map[string]any
		if json.Unmarshal(m["content"], &bs) != nil {
			continue
		}
		kept := bs[:0]
		for _, b := range bs {
			if sig, _ := b["signature"].(string); b["type"] == "thinking" && strings.HasPrefix(sig, reasoningSig) {
				continue
			}
			kept = append(kept, b)
		}
		if len(kept) == 0 {
			kept = append(kept, map[string]any{"type": "text", "text": "(thought)"})
		}
		m["content"], _ = json.Marshal(kept)
	}
	req["messages"], _ = json.Marshal(msgs)
	out, err := json.Marshal(req)
	if err != nil {
		return body
	}
	return out
}
