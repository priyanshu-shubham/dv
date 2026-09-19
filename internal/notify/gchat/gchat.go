// Package gchat is dv's conversation (package chat) in Google Chat, through a
// relay (dv-gchat-relay) that the hub connects out to: the relay hands the
// hub its owner's messages and taps, and puts what the hub sends in Google
// Chat. What goes between them is package relay's.
//
// The relay lets one dv of a hub's at a time take its events; of the dvs on
// a computer, that is the first to connect. The rest wait for it to stop.
package gchat

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"dv/internal/notify"
	"dv/internal/notify/chat"
	"dv/internal/relay"
	"dv/internal/secret"
)

// Config is the relay and the hub's token with it, kept like a bot's token:
// in a file of its own only the user can read, the token sealed.
type Config struct {
	Relay  string `json:"relay"` // its address
	Token  string `json:"-"`
	Sealed string `json:"sealed,omitempty"`
	Name   string `json:"name,omitempty"` // the hub's, as its owner sees it
	Owner  string `json:"owner,omitempty"`
	Email  string `json:"email,omitempty"`
	Origin string `json:"origin,omitempty"` // where links open dv
	// A pairing under way, until its code is sent in Google Chat.
	Code    string    `json:"code,omitempty"`
	Claim   string    `json:"claim,omitempty"`
	Expires time.Time `json:"expires,omitzero"`
	// Threads are the sessions' in Google Chat, by folder/session.
	Threads map[string]Thread `json:"threads,omitempty"`
}

type Thread struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// sealedAs binds a sealed token to what it is.
const sealedAs = "dv gchat relay token"

// GChat is the platform. Every dv has one; only the one connected uses it.
type GChat struct {
	conv   *chat.Conversation
	conn   string // this dv's, for the relay to tell it from another of the hub's
	dir    string
	keyDir string
	http   *http.Client
	wake   chan struct{}

	mu        sync.Mutex
	cfg       Config
	stamp     time.Time // cfg's file's, as read
	connected bool      // taking the hub's events
	elsewhere bool      // another dv of this computer's is
	lost      string    // why the relay turned the token down
}

// New readies the platform of a dv at self, keeping its settings in dir and
// the key that seals its token in keyDir.
func New(center *notify.Center, self, dir, keyDir string) *GChat {
	sum := sha256.Sum256([]byte(self))
	g := &GChat{
		conn: hex.EncodeToString(sum[:8]), dir: dir, keyDir: keyDir,
		http: &http.Client{Timeout: (relay.Wait + 15) * time.Second}, wake: make(chan struct{}, 1),
	}
	g.conv = chat.New(center, g)
	return g
}

func (g *GChat) Send(n notify.Notice)   { g.conv.Send(n) }
func (g *GChat) Settle(n notify.Notice) { g.conv.Settle(n) }

// Run takes the hub's events, or collects its token while it pairs, until ctx
// ends.
func (g *GChat) Run(ctx context.Context) {
	go g.conv.Run(ctx)
	var after int64
	for ctx.Err() == nil {
		cfg := g.config()
		switch {
		case cfg.Token == "" && cfg.Claim != "":
			g.collect(ctx, cfg)
			g.sleep(ctx, 2*time.Second)
			continue
		case cfg.Token == "" || g.isLost():
			g.setConnected(false, false)
			g.sleep(ctx, 30*time.Second)
			continue
		}
		// Not connected yet, it asks without waiting, to know it is at once.
		wait := relay.Wait
		if !g.Ready() {
			wait = 0
		}
		var got relay.Events
		q := url.Values{"after": {strconv.FormatInt(after, 10)}, "wait": {strconv.Itoa(wait)}, "conn": {g.conn}}
		err := g.call(ctx, cfg, http.MethodGet, relay.PathEvents+"?"+q.Encode(), nil, &got)
		var refused *refusal
		switch {
		case errors.As(err, &refused) && refused.code == http.StatusConflict:
			g.setConnected(false, true)
			g.sleep(ctx, 30*time.Second)
			continue
		case errors.As(err, &refused) && refused.code == http.StatusUnauthorized:
			g.mu.Lock()
			g.lost = refused.Error()
			g.mu.Unlock()
			continue
		case err != nil:
			g.setConnected(false, false)
			if ctx.Err() == nil {
				g.sleep(ctx, 5*time.Second)
			}
			continue
		}
		g.setConnected(true, false)
		for _, e := range got.Events {
			after = max(after, e.ID)
			g.handle(ctx, cfg, e)
		}
	}
}

// handle hands an event to the conversation.
func (g *GChat) handle(ctx context.Context, cfg Config, e relay.Event) {
	switch e.Kind {
	case "message":
		in := chat.In{Thread: e.Thread, Ref: e.Ref, ReplyTo: e.ReplyTo, Text: e.Text, Shared: e.Shared}
		for _, img := range e.Images {
			in.Images = append(in.Images, notify.Image{Type: img.Type, Data: img.Data})
		}
		g.conv.Said(ctx, in)
	case "tap":
		said, failed := g.conv.Tapped(ctx, chat.Tap{Data: e.Data, Thread: e.Thread, Ref: e.Ref})
		g.call(ctx, cfg, http.MethodPost, relay.PathAnswer, relay.Answer{Tap: e.Tap, Text: said, Failed: failed}, nil)
	}
}

// collect asks the relay for the token of a pairing under way, which it has
// once the code is sent in Google Chat.
func (g *GChat) collect(ctx context.Context, cfg Config) {
	if time.Now().After(cfg.Expires) {
		g.change(func(c *Config) { c.Code, c.Claim, c.Expires = "", "", time.Time{} })
		return
	}
	var paired relay.Paired
	err := g.call(ctx, cfg, http.MethodPost, relay.PathClaim, relay.Claim{Claim: cfg.Claim}, &paired)
	var refused *refusal
	switch {
	case errors.As(err, &refused) && refused.code == http.StatusGone:
		g.change(func(c *Config) { c.Code, c.Claim, c.Expires = "", "", time.Time{} })
	case err != nil || paired.Token == "":
		// Not sent yet, or the relay out of reach: asked again in a moment.
	default:
		g.change(func(c *Config) {
			c.Token, c.Name, c.Owner, c.Email = paired.Token, paired.Name, paired.Owner, paired.Email
			c.Code, c.Claim, c.Expires = "", "", time.Time{}
		})
		g.mu.Lock()
		g.lost = ""
		g.mu.Unlock()
	}
}

// The platform, as the conversation talks through it.

func (g *GChat) Via() string { return "Google Chat" }

func (g *GChat) Ready() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.connected
}

func (g *GChat) Origin() string { return g.config().Origin }

// A message's pages are well under Google Chat's 32,000 bytes, which the
// relay's rendering may lengthen a little.
const (
	pageBytes = 16000
	maxPages  = 3
)

func (g *GChat) Post(ctx context.Context, thread string, m chat.Out) (string, error) {
	pages := chat.Split(m.Text, pageBytes)
	if len(pages) > maxPages {
		pages = pages[:maxPages]
		pages[maxPages-1] += "\n\n…"
	} else if len(pages) == 0 {
		pages = []string{""}
	}
	var posted relay.Posted
	for i, page := range pages {
		one := m
		one.Text = page
		if i < len(pages)-1 {
			one.Buttons = nil
		}
		if err := g.call(ctx, g.config(), http.MethodPost, relay.PathPost, post(thread, one), &posted); err != nil {
			return posted.Ref, err
		}
	}
	return posted.Ref, nil
}

func (g *GChat) Edit(ctx context.Context, thread, ref string, m chat.Out) error {
	if len(m.Text) > pageBytes {
		m.Text = chat.Split(m.Text, pageBytes)[0]
	}
	return g.call(ctx, g.config(), http.MethodPost, relay.PathEdit, relay.Edit{Ref: ref, Post: post(thread, m)}, nil)
}

func (g *GChat) Unbutton(ctx context.Context, thread, ref string) error {
	return g.call(ctx, g.config(), http.MethodPost, relay.PathUnbutton, relay.Unbutton{Thread: thread, Ref: ref}, nil)
}

func (g *GChat) NewThread(ctx context.Context, name string) (string, error) {
	var th relay.Thread
	err := g.call(ctx, g.config(), http.MethodPost, relay.PathThread, relay.NewThread{Name: name}, &th)
	return th.Thread, err
}

// Rename does nothing: Google Chat's threads have no names.
func (g *GChat) Rename(ctx context.Context, thread, name string) error { return nil }

// Gone never is: the relay makes a thread afresh where one was deleted.
func (g *GChat) Gone(err error) bool { return false }

func (g *GChat) Thread(key string) (chat.Thread, bool) {
	th, ok := g.config().Threads[key]
	return chat.Thread(th), ok
}

func (g *GChat) SetThread(key string, th chat.Thread) {
	g.change(func(c *Config) {
		if c.Threads == nil {
			c.Threads = map[string]Thread{}
		}
		c.Threads[key] = Thread(th)
	})
}

func (g *GChat) DropThread(key string) {
	g.change(func(c *Config) { delete(c.Threads, key) })
}

func (g *GChat) ThreadOf(id string) (string, bool) {
	for k, th := range g.config().Threads {
		if th.ID == id {
			return k, true
		}
	}
	return "", false
}

func (g *GChat) Mark(ctx context.Context, thread, ref string, was, at notify.Progress) {
	g.call(ctx, g.config(), http.MethodPost, relay.PathMark, relay.Mark{Thread: thread, Ref: ref, Was: int(was), At: int(at)}, nil)
}

// Busy shows nothing: a Google Chat app has no way to say it is typing.
func (g *GChat) Busy(ctx context.Context, thread string) {}

// Ack answers in words, as a Google Chat app cannot react to a message.
func (g *GChat) Ack(ctx context.Context, thread, ref string) {
	g.Post(ctx, thread, chat.Out{Text: "Stopped.", Plain: true})
}

func post(thread string, m chat.Out) relay.Post {
	p := relay.Post{Thread: thread, Text: m.Text, Plain: m.Plain, Private: m.Private}
	for _, row := range m.Buttons {
		var r []relay.Button
		for _, b := range row {
			r = append(r, relay.Button(b))
		}
		p.Buttons = append(p.Buttons, r)
	}
	return p
}

// refusal is the relay turning a request down.
type refusal struct {
	code int
	why  string
}

func (r *refusal) Error() string { return r.why }

// call makes a request of the relay, with the hub's token once it has one.
func (g *GChat) call(ctx context.Context, cfg Config, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, cfg.Relay+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cfg.Token != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.Token)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return fmt.Errorf("could not reach the relay: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 || resp.StatusCode == http.StatusAccepted {
		var e relay.Error
		json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = "the relay answered " + resp.Status
		}
		return &refusal{resp.StatusCode, e.Error}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (g *GChat) sleep(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-g.wake:
	case <-ctx.Done():
	}
}

func (g *GChat) poke() {
	select {
	case g.wake <- struct{}{}:
	default:
	}
}

func (g *GChat) setConnected(on, elsewhere bool) {
	g.mu.Lock()
	g.connected, g.elsewhere = on, elsewhere
	g.mu.Unlock()
}

func (g *GChat) isLost() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.lost != ""
}

func (g *GChat) configPath() string { return filepath.Join(g.dir, "gchat.json") }

// config is the settings, read again when another dv changed them.
func (g *GChat) config() Config {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.load()
	return g.cfg
}

// Callers hold g.mu.
func (g *GChat) load() {
	fi, err := os.Stat(g.configPath())
	if err != nil {
		g.cfg, g.stamp = Config{}, time.Time{}
		return
	}
	if fi.ModTime().Equal(g.stamp) {
		return
	}
	var cfg Config
	if b, err := os.ReadFile(g.configPath()); err == nil && json.Unmarshal(b, &cfg) == nil {
		if key, err := secret.Key(g.keyDir, false); err == nil && cfg.Sealed != "" {
			cfg.Token, _ = secret.Open(key, cfg.Sealed, sealedAs)
		}
		g.cfg, g.stamp = cfg, fi.ModTime()
	}
}

// change rewrites the settings; with no relay left, removes them.
func (g *GChat) change(edit func(*Config)) (Config, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.load()
	cfg := g.cfg
	edit(&cfg)
	if cfg.Relay == "" {
		if err := os.Remove(g.configPath()); err != nil && !os.IsNotExist(err) {
			return g.cfg, err
		}
		g.cfg, g.stamp = Config{}, time.Time{}
		return g.cfg, nil
	}
	cfg.Sealed = ""
	if cfg.Token != "" {
		key, err := secret.Key(g.keyDir, true)
		if err != nil {
			return g.cfg, fmt.Errorf("could not make the key the token is kept with: %w", err)
		}
		if cfg.Sealed, err = secret.Seal(key, cfg.Token, sealedAs); err != nil {
			return g.cfg, err
		}
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	if err := secret.WritePrivate(g.configPath(), b, true); err != nil {
		return g.cfg, err
	}
	g.cfg, g.stamp = cfg, time.Time{}
	g.load()
	return cfg, nil
}
