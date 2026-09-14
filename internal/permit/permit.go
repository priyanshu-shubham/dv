// Package permit puts Claude Code's permission prompts in the page. For each
// prompt it is about to show, Claude Code runs `dv claude hook`, which hands
// the request to the dv serving that repository and waits for the reader. The
// terminal shows the same prompt meanwhile and whichever is answered first
// wins: a no there kills the hook, and a yes is heard when the tool reports
// that it ran.
package permit

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"
)

// Request is one prompt waiting on the reader.
type Request struct {
	ID      string          `json:"id"`
	Session string          `json:"session"`
	Agent   string          `json:"agent,omitempty"` // set when a subagent is asking
	Tool    string          `json:"tool"`
	Input   json.RawMessage `json:"input"`
	// Suggestions are the terminal's "Yes, and don't ask again" options, in the
	// form a hook hands back to apply one.
	Suggestions []json.RawMessage `json:"suggestions,omitempty"`
	At          time.Time         `json:"at"`
	Preview     *Preview          `json:"preview,omitempty"`
}

// Answer is the reader's decision on a request.
type Answer struct {
	Allow bool   `json:"allow"`
	Note  string `json:"note"`
	// Suggestion indexes the suggestion to apply along with an allow.
	Suggestion *int `json:"suggestion"`
}

// hookInput is the part of a hook's stdin dv reads.
type hookInput struct {
	Event       string            `json:"hook_event_name"`
	Session     string            `json:"session_id"`
	AgentID     string            `json:"agent_id"` // empty on the main thread
	Agent       string            `json:"agent_type"`
	Tool        string            `json:"tool_name"`
	Input       json.RawMessage   `json:"tool_input"`
	Suggestions []json.RawMessage `json:"permission_suggestions"`
}

// Broker holds the requests waiting on the reader.
type Broker struct {
	root string

	mu      sync.Mutex
	waiting []*waiter
	notes   []*note
	watch   map[chan struct{}]bool
}

type waiter struct {
	req    *Request
	thread string // the agent asking within the session; "" for the main one
	// answer carries the reader's decision, or nil once the terminal has answered.
	answer chan *Answer
}

// note is what the reader wrote when allowing a call. Claude can only be told
// beside the call's result, so it waits for Claude Code to report the call ran.
type note struct {
	session, tool string
	input         json.RawMessage
	text          string
	until         time.Time
}

// noteTTL bounds that wait. A call that never ran - a deny rule outranks any
// hook's allow - must not leave its note for a later one that looks the same.
const noteTTL = 10 * time.Minute

func New(root string) *Broker {
	return &Broker{root: root, watch: map[chan struct{}]bool{}}
}

// Hook serves one run of `dv claude hook`, returning what it should print.
// Nothing leaves Claude Code carrying on as though dv were not there.
func (b *Broker) Hook(ctx context.Context, body []byte) ([]byte, error) {
	var in hookInput
	if err := json.Unmarshal(body, &in); err != nil {
		return nil, err
	}
	switch in.Event {
	case "PermissionRequest":
		// A question for the user wants answers, which only the terminal takes.
		if in.Tool == "AskUserQuestion" {
			return nil, nil
		}
		return b.ask(ctx, &in), nil
	case "PostToolUse", "PostToolUseFailure":
		return b.ran(&in), nil
	}
	return nil, nil
}

func (b *Broker) ask(ctx context.Context, in *hookInput) []byte {
	w := &waiter{
		req: &Request{
			ID: newID(), Session: in.Session, Agent: in.Agent, Tool: in.Tool, Input: in.Input,
			Suggestions: in.Suggestions, At: time.Now(), Preview: preview(b.root, in.Tool, in.Input),
		},
		thread: in.AgentID,
		answer: make(chan *Answer, 1),
	}
	// Claude Code asks one thing at a time per agent, so an older request from
	// this one was answered in the terminal - by a yes on a call still running,
	// or one whose report dv never matched. Left, it would wait out the timeout.
	for {
		old := b.remove(func(x *waiter) bool { return x.req.Session == in.Session && x.thread == in.AgentID })
		if old == nil {
			break
		}
		old.answer <- nil
	}
	b.mu.Lock()
	b.waiting = append(b.waiting, w)
	b.mu.Unlock()
	b.changed()

	select {
	case a := <-w.answer:
		if a == nil {
			return nil
		}
		return b.decide(in, w.req, a)
	case <-ctx.Done():
		// The hook is gone: the terminal said no, or Claude Code stopped waiting.
		if b.remove(func(x *waiter) bool { return x == w }) != nil {
			b.changed()
		}
		return nil
	}
}

func (b *Broker) decide(in *hookInput, req *Request, a *Answer) []byte {
	text := strings.TrimSpace(a.Note)
	d := map[string]any{"behavior": "allow"}
	switch {
	case a.Allow:
		if i := a.Suggestion; i != nil && *i >= 0 && *i < len(req.Suggestions) {
			d["updatedPermissions"] = []json.RawMessage{req.Suggestions[*i]}
		}
		// Held before the allow goes out, so it is there when the call reports back.
		if text != "" {
			b.mu.Lock()
			b.notes = append(b.notes, &note{in.Session, in.Tool, in.Input, text, time.Now().Add(noteTTL)})
			b.mu.Unlock()
		}
	case text != "":
		d = map[string]any{"behavior": "deny", "message": "The user declined this in dv and said: " + text}
	default:
		// A bare no stops Claude, as it does in the terminal, rather than leaving
		// it to guess at another way round.
		d = map[string]any{"behavior": "deny", "message": "The user declined this in dv.", "interrupt": true}
	}
	return output("PermissionRequest", "decision", d)
}

// ran hears that a tool call finished. A request still waiting on it was
// answered in the terminal; a note left for it goes to Claude now.
func (b *Broker) ran(in *hookInput) []byte {
	same := func(session, tool string, input json.RawMessage) bool {
		return session == in.Session && tool == in.Tool && sameCall(input, in.Input)
	}
	if w := b.remove(func(w *waiter) bool { return same(w.req.Session, w.req.Tool, w.req.Input) }); w != nil {
		w.answer <- nil
		b.changed()
	}

	b.mu.Lock()
	text, now := "", time.Now()
	kept := b.notes[:0]
	for _, n := range b.notes {
		switch {
		case now.After(n.until):
		case text == "" && same(n.session, n.tool, n.input):
			text = n.text
		default:
			kept = append(kept, n)
		}
	}
	b.notes = kept
	b.mu.Unlock()
	if text == "" {
		return nil
	}
	return output(in.Event, "additionalContext", "The user allowed this "+in.Tool+" call in dv, with a note: "+text)
}

// Answer settles request id with the reader's decision. False means it is no
// longer waiting: answered in the terminal, or given up on.
func (b *Broker) Answer(id string, a Answer) bool {
	w := b.remove(func(w *waiter) bool { return w.req.ID == id })
	if w == nil {
		return false
	}
	w.answer <- &a
	b.changed()
	return true
}

// Waiting lists the requests in the order they arrived.
func (b *Broker) Waiting() []*Request {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*Request, len(b.waiting))
	for i, w := range b.waiting {
		out[i] = w.req
	}
	return out
}

// Watch returns a channel signalled whenever the list changes, and a func to
// stop. Signals coalesce, so a slow reader gets the latest list, not each step.
func (b *Broker) Watch() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	b.watch[ch] = true
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		delete(b.watch, ch)
		b.mu.Unlock()
	}
}

func (b *Broker) changed() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.watch {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (b *Broker) remove(match func(*waiter) bool) *waiter {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, w := range b.waiting {
		if match(w) {
			b.waiting = slices.Delete(b.waiting, i, i+1)
			return w
		}
	}
	return nil
}

func output(event, key string, v any) []byte {
	out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": event, key: v}})
	return out
}

// sameCall reports whether two tool inputs are the same call. Different hook
// events need not spell one alike - an option left unset can arrive as false
// or not at all - so fields at their zero value are not compared.
func sameCall(a, b json.RawMessage) bool {
	return reflect.DeepEqual(significant(a), significant(b))
}

func significant(raw json.RawMessage) map[string]any {
	var m map[string]any
	json.Unmarshal(raw, &m)
	for k, v := range m {
		switch v {
		case nil, false, "", 0.0:
			delete(m, k)
		}
	}
	return m
}

// Random rather than counted, so a page on another origin cannot guess one to answer.
func newID() string {
	var b [12]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
