package gateway

import (
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"
)

// A Gemini thought step rides in a thinking block's signature, which Claude
// Code sends back as it came: the API asks for every one again, unchanged.
const thoughtSig = "dvgm1."

type thoughtStep struct {
	Signature string   `json:"s,omitempty"`
	Summary   []string `json:"t,omitempty"`
}

func encodeThought(t thoughtStep) string {
	b, _ := json.Marshal(t)
	return thoughtSig + base64.RawURLEncoding.EncodeToString(b)
}

func decodeThought(sig string) (thoughtStep, bool) {
	var t thoughtStep
	rest, ok := strings.CutPrefix(sig, thoughtSig)
	if !ok {
		return t, false
	}
	b, err := base64.RawURLEncoding.DecodeString(rest)
	return t, err == nil && json.Unmarshal(b, &t) == nil
}

func (t thoughtStep) step() map[string]any {
	summary := []map[string]any{}
	for _, s := range t.Summary {
		summary = append(summary, map[string]any{"type": "text", "text": s})
	}
	step := map[string]any{"type": "thought", "summary": summary}
	if t.Signature != "" {
		step["signature"] = t.Signature
	}
	return step
}

func geminiFunction(d toolDef, names *toolNames) map[string]any {
	var params map[string]any
	if json.Unmarshal(d.InputSchema, &params) != nil || len(params) == 0 {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	delete(params, "$schema")
	shapeSchema(params)
	return map[string]any{"type": "function", "name": names.out(d.Name), "description": d.Description, "parameters": params}
}

var (
	schemaMaps = []string{"properties", "patternProperties", "$defs", "definitions", "dependentSchemas"}
	schemaOne  = []string{"items", "contains", "propertyNames", "not", "if", "then", "else"}
	schemaList = []string{"anyOf", "oneOf", "allOf", "prefixItems"}
)

// shapeSchema takes out of s, in place, what Google's schemas can't hold
// and turn the whole request away over: a boolean schema (true is any
// value, {}; false none, left out) and an x- keyword, such as MCP's
// x-mcp-header.
func shapeSchema(s map[string]any) {
	shaped := func(v any) (any, bool) {
		switch v := v.(type) {
		case bool:
			return map[string]any{}, v
		case map[string]any:
			shapeSchema(v)
		}
		return v, true
	}
	for k, v := range s {
		switch {
		case strings.HasPrefix(k, "x-"):
			delete(s, k)
		case slices.Contains(schemaMaps, k):
			if m, ok := v.(map[string]any); ok {
				for name, sub := range m {
					if sub, keep := shaped(sub); keep {
						m[name] = sub
					} else {
						delete(m, name)
					}
				}
			}
		case slices.Contains(schemaOne, k):
			if sub, keep := shaped(v); keep {
				s[k] = sub
			} else {
				delete(s, k)
			}
		case slices.Contains(schemaList, k):
			if list, ok := v.([]any); ok {
				kept := list[:0]
				for _, sub := range list {
					if sub, keep := shaped(sub); keep {
						kept = append(kept, sub)
					}
				}
				s[k] = kept
			}
		}
	}
}

// geminiRequest is req as Gemini's Interactions API takes it, stateless.
func geminiRequest(req *anthropicRequest, slug string, levels []string) (map[string]any, *toolNames) {
	names := &toolNames{letter: true}
	// Tools loaded later go after those the prompt began with, so the
	// prefix cached before them stays the same.
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
			tools = append(tools, geminiFunction(d, names))
		}
	}
	load := func(d toolDef) {
		if d.Name != "" && !seen[d.Name] {
			seen[d.Name] = true
			tools = append(tools, geminiFunction(d, names))
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

	var input, userParts, modelParts []map[string]any
	called := map[string]string{} // a call's id, to its name as Gemini knows it
	flushUser := func() {
		if len(userParts) > 0 {
			input = append(input, map[string]any{"type": "user_input", "content": userParts})
			userParts = nil
		}
	}
	flushModel := func() {
		if len(modelParts) > 0 {
			input = append(input, map[string]any{"type": "model_output", "content": modelParts})
			modelParts = nil
		}
	}
	effort := ""
	if oc := req.OutputConfig; oc != nil {
		effort = oc.Effort
	}
	for _, m := range req.Messages {
		if oc := m.OutputConfig; m.Role == "system" && oc != nil && oc.Effort != "" && effort == "" {
			effort = oc.Effort
		}
		model := m.Role == "assistant"
		for _, b := range blocks(m.Content) {
			switch b.Type {
			case "text":
				if strings.TrimSpace(b.Text) == "" {
					continue
				}
				if model {
					flushUser()
					modelParts = append(modelParts, map[string]any{"type": "text", "text": b.Text})
				} else {
					flushModel()
					userParts = append(userParts, map[string]any{"type": "text", "text": b.Text})
				}
			case "image", "document":
				if p := geminiMedia(b); p != nil && !model {
					userParts = append(userParts, p)
				}
			case "tool_addition":
				if t := b.Tool; t != nil && len(t.Definition) > 0 {
					define(t.Definition)
				} else if t != nil {
					loadNamed(t.Name)
				}
			case "tool_result":
				flushUser()
				result := map[string]any{"type": "function_result", "call_id": b.ToolUseID, "result": geminiResult(b, define, loadNamed)}
				if n := called[b.ToolUseID]; n != "" {
					result["name"] = n
				}
				if b.IsError {
					result["is_error"] = true
				}
				input = append(input, result)
			case "tool_use":
				flushModel()
				args := b.Input
				if len(args) == 0 || string(args) == "null" {
					args = json.RawMessage("{}")
				}
				called[b.ID] = names.out(b.Name)
				input = append(input, map[string]any{"type": "function_call", "id": b.ID, "name": called[b.ID], "arguments": args})
			case "thinking":
				if t, ok := decodeThought(b.Signature); ok {
					flushModel()
					input = append(input, t.step())
				}
			}
		}
		flushUser()
		flushModel()
	}

	if webSearch {
		tools = append(tools, map[string]any{"type": "google_search"})
	}
	out := map[string]any{
		"model":              slug,
		"system_instruction": instructionsAs(req.System, slug+", a Google model"),
		"input":              userAround(geminiPaired(input)),
		"store":              false,
		"stream":             true,
	}
	if tools != nil {
		out["tools"] = tools
	}
	thinking := req.Thinking != nil && req.Thinking.Type != "disabled"
	switch {
	case !thinking:
		effort = lowestEffort(levels)
	case effort != "":
		effort = fitEffort(effort, levels)
	}
	config := map[string]any{"thinking_summaries": "none"}
	if thinking {
		config["thinking_summaries"] = "auto"
	}
	if effort != "" {
		config["thinking_level"] = effort
	}
	out["generation_config"] = config
	if oc := req.OutputConfig; oc != nil && oc.Format != nil && oc.Format.Type == "json_schema" {
		out["response_format"] = map[string]any{"type": "text", "mime_type": "application/json", "schema": oc.Format.Schema}
	}
	if req.Speed == "fast" {
		out["service_tier"] = "priority"
	}
	return out, names
}

// geminiPaired gives a call with no result one, and a result with no call
// words of its own, as paired does for Codex. A call answered twice keeps
// its first answer: Google turns away a turn whose answers outnumber its
// calls.
func geminiPaired(input []map[string]any) []map[string]any {
	calls, results := map[any]bool{}, map[any]bool{}
	for _, it := range input {
		switch it["type"] {
		case "function_call":
			calls[it["id"]] = true
		case "function_result":
			results[it["call_id"]] = true
		}
	}
	answered := map[any]bool{}
	out := make([]map[string]any, 0, len(input))
	for _, it := range input {
		if it["type"] == "function_result" {
			if answered[it["call_id"]] {
				continue
			}
			answered[it["call_id"]] = true
			if !calls[it["call_id"]] {
				text, _ := json.Marshal(it["result"])
				out = append(out, userText("[An earlier tool result]: "+string(text)))
				continue
			}
		}
		out = append(out, it)
		if it["type"] == "function_call" && !results[it["id"]] {
			out = append(out, map[string]any{"type": "function_result", "call_id": it["id"], "name": it["name"], "result": "(no result: the call was not run)"})
		}
	}
	return out
}

func userText(text string) map[string]any {
	return map[string]any{"type": "user_input", "content": []map[string]any{{"type": "text", "text": text}}}
}

// userAround has the conversation open and end on the user's side, as
// Gemini takes one: a compaction can leave it opening with the model's
// turn, and a prefill ends on it. The words are Gemini CLI's own.
func userAround(input []map[string]any) []map[string]any {
	if len(input) > 0 && input[0]["type"] != "user_input" {
		input = append([]map[string]any{userText("[Continuing from previous AI thoughts...]")}, input...)
	}
	if n := len(input); n > 0 && input[n-1]["type"] != "user_input" && input[n-1]["type"] != "function_result" {
		input = append(input, userText("Continue."))
	}
	return input
}

func geminiMedia(b block) map[string]any {
	s := b.Source
	if s == nil {
		return nil
	}
	switch {
	case s.Type == "base64":
		return map[string]any{"type": b.Type, "data": s.Data, "mime_type": s.MediaType}
	case s.Type == "url":
		return map[string]any{"type": b.Type, "uri": s.URL}
	case b.Type == "document" && s.Type == "text":
		return map[string]any{"type": "text", "text": s.Data}
	}
	return nil
}

// geminiResult is a tool result as a function's: its text, or with pictures
// in it, its parts. A ToolSearch result loads the tools it names.
func geminiResult(b block, define func(json.RawMessage), load func(string)) any {
	var text []string
	var parts []map[string]any
	add := func(t string) {
		text = append(text, t)
		parts = append(parts, map[string]any{"type": "text", "text": t})
	}
	pictures := false
	for _, c := range blocks(b.Content) {
		switch c.Type {
		case "text":
			add(c.Text)
		case "image":
			if p := geminiMedia(c); p != nil {
				parts = append(parts, p)
				pictures = true
			}
		case "document":
			// A function's result takes text and pictures only.
			if p := geminiMedia(c); p != nil && p["type"] == "text" {
				add(p["text"].(string))
			}
		case "tool_reference":
			load(c.ToolName)
			add("Tool loaded: " + c.ToolName)
		case "tool_definition":
			var d toolDef
			if json.Unmarshal(c.Definition, &d) == nil && d.Name != "" {
				define(c.Definition)
				add("Tool loaded: " + d.Name)
			}
		}
	}
	if pictures {
		return parts
	}
	if len(text) == 0 {
		return "(no output)"
	}
	return strings.Join(text, "\n")
}
