package gateway

import (
	"bytes"
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"sync"
)

// A secret leaves this computer as its placeholder, and a reply's placeholder
// comes back as the secret, so a command the model writes still runs with it.
const placeholderOpen = "[[dv-secret:"

var (
	placeholderRe = regexp.MustCompile(`\[\[dv-secret:([A-Za-z0-9_]{1,64})\]\]`)
	NameRe        = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
)

// MinSecret is the shortest value masked: a shorter one would turn up in
// ordinary text.
const MinSecret = 6

func Placeholder(name string) string { return placeholderOpen + name + "]]" }

type masker struct {
	mu sync.RWMutex
	// Longest first, so a secret holding another is masked whole.
	values []maskValue
	byName map[string]string
}

type maskValue struct {
	name  string
	forms []string // as written in JSON: raw, and escaped where that differs
}

func (m *masker) set(secrets map[string]string) {
	values := make([]maskValue, 0, len(secrets))
	for name, v := range secrets {
		forms := []string{v}
		if e := jsonInner(v); e != v {
			forms = append(forms, e)
		}
		values = append(values, maskValue{name, forms})
	}
	slices.SortFunc(values, func(a, b maskValue) int { return len(b.forms[0]) - len(a.forms[0]) })
	m.mu.Lock()
	m.values, m.byName = values, secrets
	m.mu.Unlock()
}

func (m *masker) active() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.values) > 0
}

// mask replaces every secret in a request body. A placeholder is the same on
// every request, so a conversation's prompt stays the cached one.
func (m *masker) mask(body []byte) []byte {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, v := range m.values {
		for _, f := range v.forms {
			if bytes.Contains(body, []byte(f)) {
				body = bytes.ReplaceAll(body, []byte(f), []byte(Placeholder(v.name)))
			}
		}
	}
	return body
}

// unmask puts the secrets back in s; in JSON, as they are written inside a
// string. A name with no secret is left as it is.
func (m *masker) unmask(s string, inJSON bool) string {
	if !strings.Contains(s, placeholderOpen) {
		return s
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return placeholderRe.ReplaceAllStringFunc(s, func(p string) string {
		v, ok := m.byName[p[len(placeholderOpen):len(p)-2]]
		if !ok {
			return p
		}
		if inJSON {
			return jsonInner(v)
		}
		return v
	})
}

// heldFrom is where a placeholder may have begun in s that the next piece of
// a stream would finish: what is from there on waits for it.
func heldFrom(s string) int {
	const longest = len(placeholderOpen) + 64 + 2
	start := max(0, len(s)-longest)
	for i := start; i < len(s); i++ {
		if s[i] == '[' && couldOpen(s[i:]) {
			return i
		}
	}
	return len(s)
}

func couldOpen(tail string) bool {
	if len(tail) <= len(placeholderOpen) {
		return strings.HasPrefix(placeholderOpen, tail)
	}
	if !strings.HasPrefix(tail, placeholderOpen) {
		return false
	}
	rest := strings.TrimRight(tail[len(placeholderOpen):], "]")
	if len(tail)-len(placeholderOpen)-len(rest) > 1 {
		return false // closed, and so not in the secrets
	}
	return rest == "" || NameRe.MatchString(rest)
}

// jsonInner is s as it is written between a JSON string's quotes.
func jsonInner(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(s)
	out := b.Bytes()
	return string(out[1 : len(out)-2])
}

// unmasking is a stream's events with the secrets put back in what the model
// writes. A placeholder split across deltas is held until it is whole.
type unmasking struct {
	m    *masker
	next sink
	held map[int]*heldDelta
}

type heldDelta struct {
	kind string
	text string
}

func (u *unmasking) refuse(e apiError) bool {
	r, ok := u.next.(refuser)
	return ok && r.refuse(e)
}

func (u *unmasking) send(ev event) error {
	switch ev.name {
	case "content_block_delta":
		var d struct {
			Index int `json:"index"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		if json.Unmarshal(ev.data, &d) != nil {
			break
		}
		var text string
		inJSON := false
		switch d.Delta.Type {
		case "text_delta":
			text = d.Delta.Text
		case "input_json_delta":
			text, inJSON = d.Delta.PartialJSON, true
		default:
			return u.next.send(ev)
		}
		h := u.held[d.Index]
		if h == nil {
			h = &heldDelta{kind: d.Delta.Type}
			u.held[d.Index] = h
		}
		s := u.m.unmask(h.text+text, inJSON)
		cut := heldFrom(s)
		h.text = s[cut:]
		if cut == 0 {
			return nil
		}
		return u.next.send(deltaEvent(d.Index, d.Delta.Type, s[:cut]))
	case "content_block_stop":
		var d struct {
			Index int `json:"index"`
		}
		json.Unmarshal(ev.data, &d)
		if h := u.held[d.Index]; h != nil {
			delete(u.held, d.Index)
			if h.text != "" {
				if err := u.next.send(deltaEvent(d.Index, h.kind, h.text)); err != nil {
					return err
				}
			}
		}
	}
	return u.next.send(ev)
}

func deltaEvent(index int, kind, text string) event {
	field := "text"
	if kind == "input_json_delta" {
		field = "partial_json"
	}
	return newEvent("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]any{"type": kind, field: text},
	})
}

// unmaskMessage puts the secrets back in a whole reply's text and tool calls.
func (m *masker) unmaskMessage(body []byte) []byte {
	var msg map[string]json.RawMessage
	if json.Unmarshal(body, &msg) != nil {
		return body
	}
	var content []map[string]any
	if json.Unmarshal(msg["content"], &content) != nil {
		return body
	}
	for _, b := range content {
		switch b["type"] {
		case "text":
			if s, ok := b["text"].(string); ok {
				b["text"] = m.unmask(s, false)
			}
		case "tool_use":
			b["input"] = m.unmaskValue(b["input"])
		}
	}
	msg["content"], _ = json.Marshal(content)
	out, err := json.Marshal(msg)
	if err != nil {
		return body
	}
	return out
}

func (m *masker) unmaskValue(v any) any {
	switch v := v.(type) {
	case string:
		return m.unmask(v, false)
	case map[string]any:
		for k, x := range v {
			v[k] = m.unmaskValue(x)
		}
	case []any:
		for i, x := range v {
			v[i] = m.unmaskValue(x)
		}
	}
	return v
}
