package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"dv/internal/secret"
)

// What a session is about to send that looks like a secret, and isn't
// masked, waits for the user to say: mask it, or let it go as it is. Left
// unanswered, it is masked, the safe way, before Claude Code gives up.

// holdFor is how long a request waits on the user.
const holdFor = 5 * time.Minute

// Held is a request waiting on the user, as the page is shown it: a glimpse
// of each finding, a name to mask it by, and where it is sent from. The
// values themselves are asked for one at a time (HeldValue).
type Held struct {
	ID      string    `json:"id"`
	Session string    `json:"session,omitempty"`
	Cwd     string    `json:"cwd,omitempty"`
	At      time.Time `json:"at"`
	Found   []Finding `json:"found"`
	// Prompted is put to the user in its session, as a permission prompt is,
	// rather than in a dialog of its own.
	Prompted bool `json:"prompted,omitempty"`
	values   []string
	answer   chan []string // a name for each finding, "" to let it go
	unprompt context.CancelFunc
}

// Asker puts a held request to the open folder its session is in, as a
// permission prompt, and says false where none has it. answer hears the
// user; ctx ends once the request is settled some other way.
type Asker func(ctx context.Context, h Held, answer func(names []string, quiet bool, label string)) bool

var asker atomic.Pointer[Asker]

// SetAsker has held requests put to the user through a.
func SetAsker(a Asker) { asker.Store(&a) }

type Finding struct {
	Preview string `json:"preview"`
	Name    string `json:"name"`
	// Where it is in the request - "What Read gave back" - and what of:
	// the file, the command.
	Where  string `json:"where,omitempty"`
	Detail string `json:"detail,omitempty"`
	// The rest of its line, either side.
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
}

type holds struct {
	mu      sync.Mutex
	waiting []*Held
	allowed map[string]Exempt // by the sha-256 of a value the user let go as it is
	loaded  bool
	quiet   map[string]Exempt // by the id of a session the user said not to ask about
}

// Exempt is a value let go as it is, or a session not asked about, as
// Settings lists them to undo. A value is kept by its hash and a glimpse.
type Exempt struct {
	Key     string    `json:"key"`
	Preview string    `json:"preview,omitempty"`
	Label   string    `json:"label,omitempty"` // a session's title and folder
	At      time.Time `json:"at,omitzero"`
}

// StopAsking stops holding what the session of held request id sends.
func (g *Gateway) StopAsking(id, label string) error {
	g.held.mu.Lock()
	i := slices.IndexFunc(g.held.waiting, func(h *Held) bool { return h.ID == id })
	session := ""
	if i >= 0 {
		session = g.held.waiting[i].Session
	}
	g.held.mu.Unlock()
	if session == "" {
		return nil
	}
	return g.SetAsking(session, false, label)
}

// Asking is whether what a session sends is held over what looks like a secret.
func (g *Gateway) Asking(session string) bool {
	g.held.mu.Lock()
	defer g.held.mu.Unlock()
	g.loadAllowedLocked()
	_, quiet := g.held.quiet[session]
	return !quiet
}

// SetAsking has a session's requests held over what looks like a secret, or
// not, from now on.
func (g *Gateway) SetAsking(session string, on bool, label string) error {
	g.held.mu.Lock()
	defer g.held.mu.Unlock()
	g.loadAllowedLocked()
	if on {
		delete(g.held.quiet, session)
	} else {
		g.held.quiet[session] = Exempt{Key: session, Label: label, At: time.Now()}
	}
	return writeExempts(g.quietPath(), g.held.quiet)
}

// Exemptions are the values let go as they are and the sessions not asked
// about, the latest first.
func (g *Gateway) Exemptions() (allowed, unasked []Exempt) {
	g.held.mu.Lock()
	defer g.held.mu.Unlock()
	g.loadAllowedLocked()
	return newestFirst(g.held.allowed), newestFirst(g.held.quiet)
}

// Forget has a value let go as it is asked about again, by its hash.
func (g *Gateway) Forget(key string) error {
	g.held.mu.Lock()
	defer g.held.mu.Unlock()
	g.loadAllowedLocked()
	delete(g.held.allowed, key)
	return g.saveAllowedLocked()
}

func newestFirst(set map[string]Exempt) []Exempt {
	out := make([]Exempt, 0, len(set))
	out = slices.AppendSeq(out, maps.Values(set))
	slices.SortFunc(out, func(a, b Exempt) int {
		if c := b.At.Compare(a.At); c != 0 {
			return c
		}
		return strings.Compare(a.Key, b.Key)
	})
	return out
}

func (g *Gateway) quietPath() string { return filepath.Join(filepath.Dir(g.path), "unasked-sessions.json") }

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

// HeldValue is one of a held request's findings in full, for the user to
// see before they say.
func (g *Gateway) HeldValue(id string, i int) (string, bool) {
	g.held.mu.Lock()
	defer g.held.mu.Unlock()
	for _, h := range g.held.waiting {
		if h.ID == id && i >= 0 && i < len(h.values) {
			return h.values[i], true
		}
	}
	return "", false
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
	if h.unprompt != nil {
		h.unprompt()
	}
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
		d := digest(v)
		g.held.allowed[d] = Exempt{Key: d, Preview: preview(v), At: time.Now()}
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
	g.held.allowed = readExempts(g.allowedPath())
	g.held.quiet = readExempts(g.quietPath())
}

func (g *Gateway) saveAllowedLocked() error {
	return writeExempts(g.allowedPath(), g.held.allowed)
}

func readExempts(path string) map[string]Exempt {
	set := map[string]Exempt{}
	b, err := os.ReadFile(path)
	if err != nil {
		return set
	}
	var list []Exempt
	if json.Unmarshal(b, &list) != nil {
		// Through v0.39, the hashes alone.
		list = nil
		var keys []string
		json.Unmarshal(b, &keys)
		for _, k := range keys {
			list = append(list, Exempt{Key: k})
		}
	}
	for _, e := range list {
		set[e.Key] = e
	}
	return set
}

func writeExempts(path string, set map[string]Exempt) error {
	b, _ := json.Marshal(newestFirst(set))
	return secret.WritePrivate(path, b, true)
}

// hold waits, where body holds what looks like a secret the user hasn't
// masked or let go, for them to say which; unanswered, those are masked.
func (g *Gateway) hold(ctx context.Context, body []byte, session string) {
	src := envSource.Load()
	if src == nil || src.ask == nil || !src.ask() {
		return
	}
	if !g.Asking(session) {
		return
	}
	text := sentText(body)
	cwd, parts := requestParts(body)
	// What the model wrote itself is nothing new to it: only what comes in
	// from the user's side is asked about.
	parts = slices.DeleteFunc(parts, func(p part) bool { return p.model })
	// Each part apart: a lock file read in one doesn't hide a key in another,
	// as the many tokens of a long conversation taken together would.
	var found []string
	for _, p := range parts {
		for _, v := range g.Unmasked(p.text) {
			if !slices.Contains(found, v) {
				found = append(found, v)
			}
		}
	}
	g.held.mu.Lock()
	// One already asked about - for a request since given up, as on an
	// interruption, or another still waiting - is waited on, not asked again.
	var asked []*Held
	for _, h := range g.held.waiting {
		n := len(found)
		found = slices.DeleteFunc(found, func(v string) bool { return slices.Contains(h.values, v) })
		if len(found) < n {
			asked = append(asked, h)
		}
	}
	if len(found) > 0 {
		asked = append(asked, g.askLocked(session, cwd, text, parts, found))
	}
	g.held.mu.Unlock()
	for _, h := range asked {
		select {
		case <-h.answer:
		case <-ctx.Done():
			return
		}
	}
}

// askLocked puts a question to the user about found, which stands until it
// is answered or, after holdFor, masked: a request given up meanwhile leaves
// it for the next to wait on.
func (g *Gateway) askLocked(session, cwd, text string, parts []part, found []string) *Held {
	h := &Held{ID: newHeldID(), Session: session, Cwd: cwd, At: time.Now(), values: found, answer: make(chan []string)}
	taken := map[string]bool{}
	for _, v := range found {
		base := nameFor(text, v)
		name := base
		for n := 2; taken[name] || g.hasSecret(name); n++ {
			name = fmt.Sprintf("%s_%d", base, n)
		}
		taken[name] = true
		where, detail, before, after := locate(parts, v)
		h.Found = append(h.Found, Finding{preview(v), name, where, detail, before, after})
	}
	g.held.waiting = append(g.held.waiting, h)
	time.AfterFunc(holdFor, func() { g.Answer(h.ID, nil) })
	if a := asker.Load(); a != nil && session != "" {
		ctx, cancel := context.WithCancel(context.Background())
		id := h.ID
		h.Prompted = (*a)(ctx, *h, func(names []string, quiet bool, label string) {
			if quiet {
				g.StopAsking(id, label)
			}
			g.Answer(id, names)
		})
		if h.Prompted {
			h.unprompt = cancel
		} else {
			cancel()
		}
	}
	return h
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
