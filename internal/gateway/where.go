package gateway

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

// part is one piece of a Messages request's text, named for the reader by
// what it is: a file Read gave back, a command the model ran, a message.
type part struct {
	where, detail, text string
	model               bool // the model's own words or calls
}

var workingDir = regexp.MustCompile(`(?m)Primary working directory: (\S.*)$`)

// requestParts are a request's pieces of text in the order sent, and the
// folder Claude Code says it works in.
func requestParts(body []byte) (cwd string, parts []part) {
	var req struct {
		System   json.RawMessage `json:"system"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &req) != nil {
		return "", nil
	}
	if sys := textOf(req.System); sys != "" {
		parts = append(parts, part{"Claude Code's system prompt", "", sys, false})
		if m := workingDir.FindStringSubmatch(sys); m != nil {
			cwd = strings.TrimSpace(m[1])
		}
	}
	calls := map[string]part{}
	for _, m := range req.Messages {
		var s string
		if json.Unmarshal(m.Content, &s) == nil {
			parts = append(parts, part{said(m.Role), "", s, m.Role == "assistant"})
			continue
		}
		var blocks []struct {
			Type    string          `json:"type"`
			Text    string          `json:"text"`
			ID      string          `json:"id"`
			Name    string          `json:"name"`
			Input   json.RawMessage `json:"input"`
			UseID   string          `json:"tool_use_id"`
			Content json.RawMessage `json:"content"`
		}
		json.Unmarshal(m.Content, &blocks)
		for _, b := range blocks {
			switch b.Type {
			case "text":
				parts = append(parts, part{said(m.Role), "", b.Text, m.Role == "assistant"})
			case "tool_use":
				call := part{"The model's " + b.Name + " call", inputGist(b.Input), string(b.Input), true}
				calls[b.ID] = call
				parts = append(parts, call)
			case "tool_result":
				call, ok := calls[b.UseID]
				name := strings.TrimPrefix(call.where, "The model's ")
				name = strings.TrimSuffix(name, " call")
				if !ok {
					name = "A tool"
				}
				parts = append(parts, part{"What " + name + " gave back", call.detail, textOf(b.Content), false})
			}
		}
	}
	return cwd, parts
}

func said(role string) string {
	if role == "assistant" {
		return "The model's reply"
	}
	return "A message to the model"
}

// textOf is a system prompt's or a tool result's text: a string, or blocks.
func textOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	json.Unmarshal(raw, &blocks)
	var b strings.Builder
	for _, bl := range blocks {
		if bl.Type == "text" {
			b.WriteString(bl.Text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// inputGist is what a tool call is about, as its input names it: the file,
// the command, the search.
func inputGist(input json.RawMessage) string {
	var in map[string]any
	json.Unmarshal(input, &in)
	for _, k := range []string{"file_path", "notebook_path", "command", "path", "pattern", "url", "query", "description"} {
		if s, ok := in[k].(string); ok && s != "" {
			return clipRunes(oneLine(s), 160, false)
		}
	}
	return ""
}

// locate is where in a request v is sent - the latest place, which is what
// is new in it - and the line around it.
func locate(parts []part, v string) (where, detail, before, after string) {
	for i := len(parts) - 1; i >= 0; i-- {
		p := parts[i]
		at := strings.Index(p.text, v)
		if at < 0 {
			continue
		}
		// From the last tab: Read numbers each line before one.
		start := strings.LastIndexAny(p.text[:at], "\n\t") + 1
		end := len(p.text)
		if n := strings.IndexByte(p.text[at+len(v):], '\n'); n >= 0 {
			end = at + len(v) + n
		}
		return p.where, p.detail, clipRunes(p.text[start:at], 28, true), clipRunes(p.text[at+len(v):end], 40, false)
	}
	return "", "", "", ""
}

func oneLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i] + " …"
	}
	return s
}

// clipRunes shortens s to n runes, keeping its end where tail.
func clipRunes(s string, n int, tail bool) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	if tail {
		return "…" + string(r[len(r)-n:])
	}
	return string(r[:n]) + "…"
}
