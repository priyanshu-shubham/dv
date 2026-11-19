package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"dv/internal/secret"
)

// What a session is about to send that looks like a secret, and isn't
// masked, waits for the user to say: mask it, or let it go as it is. Left
// unanswered, it is masked, the safe way, before Claude Code gives up.

// holdFor is how long a request waits on the user.
const holdFor = 5 * time.Minute

// Held is a request waiting on the user, as the page is shown it: a glimpse
// of each finding, never its value, and a name to mask it by.
type Held struct {
	ID      string    `json:"id"`
	Session string    `json:"session,omitempty"`
	At      time.Time `json:"at"`
	Found   []Finding `json:"found"`
	values  []string
	answer  chan []string // a name for each finding, "" to let it go
}

type Finding struct {
	Preview string `json:"preview"`
	Name    string `json:"name"`
}

type holds struct {
	mu      sync.Mutex
	waiting []*Held
	allowed map[string]bool // sha-256 of values the user let go as they are
	loaded  bool
}

// HeldNow are the requests waiting on the user, oldest first.
func (g *Gateway) HeldNow() []Held {
	g.held.mu.Lock()
	defer g.held.mu.Unlock()
	out := []Held{}
	for _, h := range g.held.waiting {
		out = append(out, *h)
	}
	return out
}

// Answer says what to do with a held request's findings: a name for each to
// mask it by, or "" to let that one go as it is.
func (g *Gateway) Answer(id string, names []string) error {
	g.held.mu.Lock()
	i := slices.IndexFunc(g.held.waiting, func(h *Held) bool { return h.ID == id })
	if i < 0 {
		g.held.mu.Unlock()
		return fmt.Errorf("nothing is waiting as %s", id)
	}
	h := g.held.waiting[i]
	g.held.waiting = slices.Delete(g.held.waiting, i, i+1)
	g.held.mu.Unlock()
	if len(names) != len(h.values) {
		names = make([]string, len(h.values))
		for i, f := range h.Found {
			names[i] = f.Name
		}
	}
	if err := g.settle(h.values, names); err != nil {
		return err
	}
	close(h.answer)
	return nil
}

// Allow lets values go as they are from now on, as the user said to.
func (g *Gateway) Allow(values []string) error {
	return g.settle(values, make([]string, len(values)))
}

func (g *Gateway) settle(values, names []string) error {
	var allow []string
	for i, v := range values {
		if names[i] == "" {
			allow = append(allow, v)
			continue
		}
		if err := g.SetSecret(names[i], v); err != nil {
			return err
		}
	}
	if len(allow) == 0 {
		return nil
	}
	g.held.mu.Lock()
	defer g.held.mu.Unlock()
	g.loadAllowedLocked()
	for _, v := range allow {
		g.held.allowed[digest(v)] = true
	}
	return g.saveAllowedLocked()
}

func digest(v string) string {
	sum := sha256.Sum256([]byte(v))
	return hex.EncodeToString(sum[:])
}

func (g *Gateway) allowedPath() string { return filepath.Join(filepath.Dir(g.path), "allowed-secrets.json") }

func (g *Gateway) loadAllowedLocked() {
	if g.held.loaded {
		return
	}
	g.held.loaded = true
	g.held.allowed = map[string]bool{}
	var list []string
	if b, err := os.ReadFile(g.allowedPath()); err == nil && json.Unmarshal(b, &list) == nil {
		for _, d := range list {
			g.held.allowed[d] = true
		}
	}
}

func (g *Gateway) saveAllowedLocked() error {
	list := make([]string, 0, len(g.held.allowed))
	for d := range g.held.allowed {
		list = append(list, d)
	}
	slices.Sort(list)
	b, _ := json.Marshal(list)
	return secret.WritePrivate(g.allowedPath(), b, true)
}

// hold waits, where body holds what looks like a secret the user hasn't
// masked or let go, for them to say which; unanswered, those are masked.
func (g *Gateway) hold(ctx context.Context, body []byte, session string) {
	src := envSource.Load()
	if src == nil || src.ask == nil || !src.ask() {
		return
	}
	text := sentText(body)
	found := g.Unmasked(text)
	g.held.mu.Lock()
	// One already asked about, in a request still waiting, is that one's to settle.
	for _, h := range g.held.waiting {
		found = slices.DeleteFunc(found, func(v string) bool { return slices.Contains(h.values, v) })
	}
	if len(found) == 0 {
		g.held.mu.Unlock()
		return
	}
	h := &Held{ID: newHeldID(), Session: session, At: time.Now(), values: found, answer: make(chan []string)}
	taken := map[string]bool{}
	for _, v := range found {
		base := nameFor(text, v)
		name := base
		for n := 2; taken[name] || g.hasSecret(name); n++ {
			name = fmt.Sprintf("%s_%d", base, n)
		}
		taken[name] = true
		h.Found = append(h.Found, Finding{preview(v), name})
	}
	g.held.waiting = append(g.held.waiting, h)
	g.held.mu.Unlock()

	select {
	case <-h.answer:
	case <-time.After(holdFor):
		g.Answer(h.ID, nil)
	case <-ctx.Done():
		g.held.mu.Lock()
		g.held.waiting = slices.DeleteFunc(g.held.waiting, func(x *Held) bool { return x == h })
		g.held.mu.Unlock()
	}
}

func (g *Gateway) hasSecret(name string) bool {
	_, ok := g.SecretValue(name)
	return ok
}

func newHeldID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// sentText is the words a Messages request sends: the system prompt, what
// was said, tool calls and their results - not pictures, documents or the
// signatures and sealed reasoning, whose base64 reads as random.
func sentText(body []byte) string {
	var req struct {
		System   json.RawMessage `json:"system"`
		Messages []struct {
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &req) != nil {
		return ""
	}
	var b strings.Builder
	var walk func(raw json.RawMessage)
	walk = func(raw json.RawMessage) {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			b.WriteString(s)
			b.WriteByte('\n')
			return
		}
		var blocks []struct {
			Type    string          `json:"type"`
			Text    string          `json:"text"`
			Input   json.RawMessage `json:"input"`
			Content json.RawMessage `json:"content"`
		}
		json.Unmarshal(raw, &blocks)
		for _, bl := range blocks {
			switch bl.Type {
			case "text":
				b.WriteString(bl.Text)
				b.WriteByte('\n')
			case "tool_use":
				b.Write(bl.Input)
				b.WriteByte('\n')
			case "tool_result":
				walk(bl.Content)
			}
		}
	}
	walk(req.System)
	for _, m := range req.Messages {
		walk(m.Content)
	}
	return b.String()
}

// keyBefore is a setting's key just before its value: API_KEY=, "token": or
// webhook_secret: as a config file, a .env file or JSON has it.
var keyBefore = regexp.MustCompile(`([A-Za-z_][A-Za-z0-9_.-]{1,60})["']?\s*[:=]\s*["']?$`)

// NameFor names a secret by the key it is set under where text has one,
// else by its shape.
func NameFor(text, v string) string { return nameFor(text, v) }

func nameFor(text, v string) string {
	if i := strings.Index(text, v); i > 0 {
		if m := keyBefore.FindStringSubmatch(text[max(0, i-80):i]); m != nil {
			name := strings.ToUpper(notName.ReplaceAllString(m[1], "_"))
			if NameRe.MatchString(name) {
				return name
			}
		}
	}
	return guessName(v)
}

var notName = regexp.MustCompile(`[^A-Za-z0-9_]`)

// guessName names a secret by the shape of its value, where that says whose it is.
func guessName(v string) string {
	for _, s := range []struct{ prefix, name string }{
		{"sk-ant-", "ANTHROPIC_API_KEY"}, {"sk-", "OPENAI_API_KEY"},
		{"ghp_", "GITHUB_TOKEN"}, {"gho_", "GITHUB_TOKEN"}, {"ghs_", "GITHUB_TOKEN"}, {"github_pat_", "GITHUB_TOKEN"},
		{"glpat-", "GITLAB_TOKEN"}, {"xox", "SLACK_TOKEN"}, {"AKIA", "AWS_ACCESS_KEY_ID"}, {"ASIA", "AWS_ACCESS_KEY_ID"},
		{"AIza", "GOOGLE_API_KEY"}, {"npm_", "NPM_TOKEN"}, {"eyJ", "JWT"}, {"-----BEGIN", "PRIVATE_KEY"},
	} {
		if strings.HasPrefix(v, s.prefix) {
			return s.name
		}
	}
	return "SECRET"
}
