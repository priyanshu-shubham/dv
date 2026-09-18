// Package telegram sends dv's notices to the reader's Telegram, through a bot
// of their own, and takes what they answer and write there. Each session has
// a thread (a topic) of its own in the bot's chat: its notices go there, and
// what the reader writes there goes to it.
//
// Telegram hands a bot's messages to one listener at a time, so of the dvs
// running on a computer one sends for all of them: the first to find the bot
// set up and no other dv sending. The rest leave it be.
package telegram

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"dv/internal/notify"
	"dv/internal/store"
)

// Config is the bot and the chat it sends to. It holds the bot's token, so it
// is kept in a file of its own that only the user can read, never with the
// settings pages are sent, and the token in it sealed (secret.go).
type Config struct {
	Token  string `json:"-"`
	Sealed string `json:"sealed,omitempty"` // the token, as kept
	Bot    string `json:"bot"`              // its username
	Chat   int64  `json:"chat,omitempty"`   // the reader's, once connected
	Name   string `json:"name,omitempty"`   // theirs, to show
	Code   string `json:"code,omitempty"`   // what connects a chat, until one is
	Origin string `json:"origin,omitempty"`
	// Threads are the sessions' in the chat, by folder/session.
	Threads map[string]Thread `json:"threads,omitempty"`
}

// Thread is a session's topic in the chat.
type Thread struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Telegram is the provider. Every dv has one; only the one sending uses it.
type Telegram struct {
	center *notify.Center
	self   string // this dv's address, by which the others know it sends
	dir    string
	keyDir string
	api    botAPI
	jobs   chan func(context.Context)
	wake   chan struct{}
	start  time.Time
	flew   chan struct{} // a flight added
	// How often flights are looked at: typingEvery, less in tests.
	typingEvery time.Duration

	threading sync.Mutex // one thread made at a time, so a session gets one

	mu      sync.Mutex
	cfg     Config
	stamp   time.Time // cfg's file's, as read
	secret  []byte    // the key the token is sealed with, once read
	sending bool
	topics  *bool                  // whether the bot has them, once asked
	sent    map[string]sentMessage // by notice
	picks   map[string]pick        // what the buttons of a list stand for, by token
	asked   map[int64]pick         // questions a reply answers, by message
	flights []*flight
}

// New readies the provider of a dv at self, keeping its settings in dir and
// the key that seals its token in keyDir.
func New(center *notify.Center, self, dir, keyDir string) *Telegram {
	return &Telegram{
		center: center, self: self, dir: dir, keyDir: keyDir,
		api:  botAPI{"https://api.telegram.org", &http.Client{Timeout: callTimeout}},
		jobs: make(chan func(context.Context), 100), wake: make(chan struct{}, 1),
		start: time.Now(), flew: make(chan struct{}, 1), typingEvery: typingEvery,
		sent: map[string]sentMessage{}, picks: map[string]pick{}, asked: map[int64]pick{},
	}
}

// Run sends, if this dv is the one sending, until ctx ends; then lets go of
// the bot for another dv to take.
func (t *Telegram) Run(ctx context.Context) {
	go func() {
		for {
			select {
			case job := <-t.jobs:
				job(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()
	go t.fly(ctx)
	t.poll(ctx)
	t.letGo()
}

// Send tells of a notice in its session's thread.
func (t *Telegram) Send(n notify.Notice) {
	t.do(func(ctx context.Context) {
		cfg := t.config()
		if !t.isSending() || cfg.Chat == 0 {
			return
		}
		rows := keyboard(n, cfg, true)
		var m sentMessage
		err := t.inThread(ctx, cfg, n.Folder, n.Session, n.Where, n.Place, func(thread int64) (err error) {
			m.thread = thread
			m.id, err = t.postAll(ctx, cfg, thread, pages(n, thread != 0, true), rows)
			if unparsed(err) {
				m.id, err = t.postAll(ctx, cfg, thread, pages(n, thread != 0, false), rows)
			}
			return err
		})
		if err != nil {
			fmt.Fprintln(os.Stderr, "dv: could not send a notice to Telegram:", err)
			return
		}
		t.mu.Lock()
		t.sent[n.ID] = m
		t.mu.Unlock()
	})
}

// sentMessage is a notice's last message, which has its buttons.
type sentMessage struct{ id, thread int64 }

// Settle says how a notice ended on its message, which loses its answers. A
// turn's end has nothing to take back.
func (t *Telegram) Settle(n notify.Notice) {
	t.do(func(ctx context.Context) {
		t.mu.Lock()
		m := t.sent[n.ID]
		delete(t.sent, n.ID)
		t.mu.Unlock()
		if m.id == 0 || n.Outcome == "" {
			return
		}
		cfg := t.config()
		params := map[string]any{"chat_id": cfg.Chat, "message_id": m.id, "text": text(n, m.thread != 0, n.Outcome, true), "parse_mode": "HTML", "link_preview_options": noPreview}
		params["reply_markup"] = markup(keyboard(n, cfg, false))
		err := t.call(ctx, cfg, "editMessageText", params, nil)
		if unparsed(err) {
			params["text"] = text(n, m.thread != 0, n.Outcome, false)
			err = t.call(ctx, cfg, "editMessageText", params, nil)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "dv: could not update a notice in Telegram:", err)
		}
	})
}

func (t *Telegram) do(job func(context.Context)) {
	select {
	case t.jobs <- job:
	default: // Telegram is not keeping up; better a notice lost than dv held up
	}
}

// poll is the sending dv's: it takes the reader's taps and messages.
func (t *Telegram) poll(ctx context.Context) {
	var offset int64
	var token string
	// Pictures sent together come a message each, one after another: they
	// are gathered, to go to the session as one message once the next is not.
	var album *message
	for ctx.Err() == nil {
		cfg := t.config()
		if cfg.Token == "" || !t.claim(ctx) {
			t.setSending(false)
			t.sleep(ctx, 30*time.Second)
			continue
		}
		t.setSending(true)
		if cfg.Token != token {
			token, offset = cfg.Token, 0
			t.introduce(ctx, cfg)
		}
		wait := pollFor
		if album != nil {
			wait = 1
		}
		var updates []update
		err := t.api.call(ctx, cfg.Token, "getUpdates", map[string]any{
			"offset": offset, "timeout": wait, "allowed_updates": []string{"message", "callback_query"},
		}, &updates)
		if err != nil {
			if ctx.Err() == nil {
				t.sleep(ctx, 5*time.Second)
			}
			continue
		}
		for _, u := range updates {
			offset = u.ID + 1
			cfg := t.config() // a chat may have been readied to connect while this waited
			m := u.Message
			if album != nil && (m == nil || m.Album != album.Album) {
				t.said(ctx, cfg, album)
				album = nil
			}
			switch {
			case u.Callback != nil:
				t.tapped(ctx, cfg, u.Callback)
			case m == nil:
			case m.Album != "" && album != nil:
				album.more = append(album.more, m)
			case m.Album != "":
				album = m
			default:
				t.said(ctx, cfg, m)
			}
		}
		if len(updates) == 0 && album != nil {
			t.said(ctx, t.config(), album)
			album = nil
		}
	}
	// Telegram hears which updates were handled only with the next ask for
	// more; asked now, the dv after this one is not handed them again.
	if offset > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		t.api.call(ctx, token, "getUpdates", map[string]any{"offset": offset, "timeout": 0, "limit": 1}, nil)
		cancel()
	}
}

// introduce tells Telegram the bot's commands, for the menu its apps show,
// and learns whether the bot has topics, which sessions' threads are.
func (t *Telegram) introduce(ctx context.Context, cfg Config) {
	t.call(ctx, cfg, "setMyCommands", map[string]any{"commands": commands}, nil)
	t.hasTopics(ctx, cfg)
	t.menu(ctx, cfg)
}

// menu puts dv on the button beside where the reader writes, to open it in
// Telegram. That takes an https address, which a dv reached only on this
// computer has none of.
func (t *Telegram) menu(ctx context.Context, cfg Config) {
	link := linkTo(cfg.Origin, "/")
	if cfg.Chat == 0 || !strings.HasPrefix(link, "https://") {
		return
	}
	t.call(ctx, cfg, "setChatMenuButton", map[string]any{
		"chat_id": cfg.Chat, "menu_button": map[string]any{"type": "web_app", "text": "dv", "web_app": map[string]string{"url": link}},
	}, nil)
}

// hasTopics asks Telegram whether the bot has topics in private chats.
func (t *Telegram) hasTopics(ctx context.Context, cfg Config) bool {
	var me user
	if err := t.call(ctx, cfg, "getMe", map[string]any{}, &me); err != nil {
		return false
	}
	t.mu.Lock()
	t.topics = &me.Topics
	t.mu.Unlock()
	return me.Topics
}

func (t *Telegram) sleep(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-t.wake:
	case <-ctx.Done():
	}
}

// tapped acts on a button: a choice answering a notice, or a pick from a list
// the bot gave.
func (t *Telegram) tapped(ctx context.Context, cfg Config, q *callback) {
	if q.From.ID != cfg.Chat {
		t.call(ctx, cfg, "answerCallbackQuery", map[string]any{"callback_query_id": q.ID}, nil)
		return
	}
	if token, ok := strings.CutPrefix(q.Data, pickPrefix); ok {
		t.picked(ctx, cfg, q, token)
		return
	}
	id, n, _ := strings.Cut(q.Data, ":")
	i, _ := strconv.Atoi(n)
	choice, err := t.center.Answer(id, i, "Telegram")
	said := choice
	if err != nil {
		said = err.Error()
		// Left by a dv that has since restarted, it can only be answered in dv.
		if errors.Is(err, notify.ErrGone) && q.Message != nil {
			t.call(ctx, cfg, "editMessageReplyMarkup", map[string]any{"chat_id": cfg.Chat, "message_id": q.Message.ID, "reply_markup": markup(nil)}, nil)
		}
	}
	t.call(ctx, cfg, "answerCallbackQuery", map[string]any{"callback_query_id": q.ID, "text": said}, nil)
}

// Test sends a message to show that the reader can be reached, with a link
// to dv at origin, which links are to go to from now on.
func (t *Telegram) Test(ctx context.Context, origin string) error {
	cfg, err := t.change(func(c *Config) {
		if origin != "" {
			c.Origin = origin
		}
	})
	if err != nil {
		return err
	}
	if cfg.Chat == 0 {
		return errors.New("Connect a chat first")
	}
	t.menu(ctx, cfg)
	var rows [][]button
	if link := linkTo(cfg.Origin, "/"); link != "" {
		rows = [][]button{{{Text: "Open dv", URL: link}}}
	}
	_, err = t.post(ctx, cfg, 0, "dv on "+html.EscapeString(hostname())+" can reach you here.", rows)
	return err
}

// say sends plain words, in a thread or, with 0, the chat itself.
func (t *Telegram) say(ctx context.Context, cfg Config, thread int64, s string, rows [][]button) {
	t.post(ctx, cfg, thread, html.EscapeString(s), rows)
}

// postAll sends a notice's messages in turn, its buttons on the last, whose
// id it returns.
func (t *Telegram) postAll(ctx context.Context, cfg Config, thread int64, texts []string, rows [][]button) (int64, error) {
	var id int64
	for i, text := range texts {
		var last [][]button
		if i == len(texts)-1 {
			last = rows
		}
		var err error
		if id, err = t.post(ctx, cfg, thread, text, last); err != nil {
			return id, err
		}
	}
	return id, nil
}

// post sends a message to the reader, in a thread or the chat itself,
// returning its id. A link Telegram will not take - to an address on the
// reader's network - is left off.
func (t *Telegram) post(ctx context.Context, cfg Config, thread int64, text string, rows [][]button) (int64, error) {
	params := map[string]any{"chat_id": cfg.Chat, "text": text, "parse_mode": "HTML", "link_preview_options": noPreview}
	if thread != 0 {
		params["message_thread_id"] = thread
	}
	if len(rows) > 0 {
		params["reply_markup"] = markup(rows)
	}
	var m message
	err := t.call(ctx, cfg, "sendMessage", params, &m)
	if e := (*apiError)(nil); errors.As(err, &e) && strings.Contains(e.Description, "BUTTON_URL") {
		params["reply_markup"] = markup(withoutLinks(rows))
		err = t.call(ctx, cfg, "sendMessage", params, &m)
	}
	return m.ID, err
}

// call makes one call to the Bot API, waiting out Telegram's rate limit once.
func (t *Telegram) call(ctx context.Context, cfg Config, method string, params, out any) error {
	for try := 0; ; try++ {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		err := t.api.call(ctx, cfg.Token, method, params, out)
		cancel()
		var e *apiError
		if try > 0 || !errors.As(err, &e) || e.RetryAfter == 0 || e.RetryAfter > 30 {
			return err
		}
		select {
		case <-time.After(time.Duration(e.RetryAfter) * time.Second):
		case <-ctx.Done():
			return err
		}
	}
}

var noPreview = map[string]bool{"is_disabled": true}

// unparsed is Telegram refusing a message's HTML.
func unparsed(err error) bool {
	e := (*apiError)(nil)
	return errors.As(err, &e) && strings.Contains(e.Description, "can't parse entities")
}

func markup(rows [][]button) map[string]any {
	if rows == nil {
		rows = [][]button{}
	}
	return map[string]any{"inline_keyboard": rows}
}

func withoutLinks(rows [][]button) [][]button {
	var out [][]button
	for _, r := range rows {
		if r[0].URL == "" {
			out = append(out, r)
		}
	}
	return out
}

// A message holds this much of a body, well inside Telegram's 4096 characters
// with the rest of the message; a reply runs to this many messages at most.
const (
	pageChars = 3000
	maxPages  = 5
)

// pages is a notice as the messages it takes: a long reply goes over several.
// threaded is its going in its session's thread, which says whose it is.
func pages(n notify.Notice, threaded, rich bool) []string {
	parts := []string{clip(n.Body, pageChars)}
	if n.Kind == notify.Done && n.Format == notify.Markdown {
		if parts = split(n.Body, pageChars); len(parts) > maxPages {
			parts = parts[:maxPages]
			parts[maxPages-1] += "\n\n…"
		} else if len(parts) == 0 {
			parts = []string{""}
		}
	}
	out := make([]string, len(parts))
	for i, body := range parts {
		out[i] = page(n, body, i == 0, i == len(parts)-1, threaded, "", rich)
	}
	return out
}

// text is a notice in a single message, with how it ended once it has.
func text(n notify.Notice, threaded bool, outcome string, rich bool) string {
	return page(n, clip(n.Body, pageChars), true, true, threaded, outcome, rich)
}

// page is one of a notice's messages: its title on the first, and on the last
// the session it is from, unless in the session's thread, and how it ended
// once it has. A Markdown body is rendered when rich is set; plain, it is sent
// as written, for when Telegram will not take what was made of it.
func page(n notify.Notice, body string, first, last, threaded bool, outcome string, rich bool) string {
	var b strings.Builder
	if first {
		b.WriteString("<b>" + html.EscapeString(n.Title) + "</b>\n")
	}
	if body != "" {
		switch {
		case n.Format == notify.Code:
			b.WriteString("<pre>" + html.EscapeString(body) + "</pre>\n")
		case n.Format == notify.Markdown && rich:
			b.WriteString(markdownHTML(body) + "\n")
		default:
			b.WriteString(html.EscapeString(body) + "\n")
		}
	}
	if !last {
		return strings.TrimRight(b.String(), "\n")
	}
	if !threaded {
		b.WriteString("\n<i>" + html.EscapeString(from(n.Where, n.Place)) + "</i>")
	}
	switch {
	case outcome != "":
		b.WriteString("\n\n<b>" + html.EscapeString(outcome) + "</b>")
	case n.Kind == notify.Ask && len(n.Choices) == 0:
		b.WriteString("\n\nThis one is answered in dv.")
	}
	return strings.TrimSpace(b.String())
}

// from says whose a message is: "from session in dv folder (Fix the tests)".
func from(title, place string) string {
	s := "from session"
	if title == "" {
		s = "from a new session"
	}
	if place != "" {
		s += " in " + place + " folder"
	}
	if title != "" {
		s += " (" + title + ")"
	}
	return s
}

// keyboard is a notice's buttons: its choices, while it can be answered, and a
// link to its session.
func keyboard(n notify.Notice, cfg Config, open bool) [][]button {
	var rows [][]button
	if open {
		for i, c := range n.Choices {
			rows = append(rows, []button{{Text: c, Data: n.ID + ":" + strconv.Itoa(i)}})
		}
	}
	path := "/"
	if n.Folder != "" {
		path += n.Folder + "/"
	}
	if link := linkTo(cfg.Origin, path+"?session="+url.QueryEscape(n.Session)); link != "" {
		rows = append(rows, []button{{Text: "Open in dv", URL: link}})
	}
	return rows
}

// linkTo is a link to dv at origin, if Telegram's apps can follow it: not to
// this computer, which is not the phone's.
func linkTo(origin, path string) string {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return ""
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); host == "localhost" || ip != nil && ip.IsLoopback() {
		return ""
	}
	return strings.TrimSuffix(origin, "/") + path
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "this computer"
	}
	return h
}

// The files: the bot's settings, and which dv sends.
func (t *Telegram) configPath() string { return filepath.Join(t.dir, "telegram.json") }
func (t *Telegram) senderPath() string { return filepath.Join(t.dir, "telegram-sender.json") }

// config is the bot's settings, read again when another dv changed them.
func (t *Telegram) config() Config {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.load()
	return t.cfg
}

// Callers hold t.mu.
func (t *Telegram) load() {
	fi, err := os.Stat(t.configPath())
	if err != nil {
		t.cfg, t.stamp = Config{}, time.Time{}
		return
	}
	if fi.ModTime().Equal(t.stamp) {
		return
	}
	var cfg Config
	if b, err := os.ReadFile(t.configPath()); err == nil && json.Unmarshal(b, &cfg) == nil {
		// One that will not open - its key lost, or made on another computer -
		// leaves the bot to be set up again.
		if key, err := t.key(false); err == nil && cfg.Sealed != "" {
			cfg.Token, _ = unseal(key, cfg.Sealed)
		}
		t.cfg, t.stamp = cfg, fi.ModTime()
	}
}

// change rewrites the settings; a change with no token left removes them.
func (t *Telegram) change(edit func(*Config)) (Config, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.load()
	cfg := t.cfg
	edit(&cfg)
	if cfg.Token == "" {
		if err := os.Remove(t.configPath()); err != nil && !os.IsNotExist(err) {
			return t.cfg, err
		}
		t.cfg, t.stamp = Config{}, time.Time{}
		return t.cfg, nil
	}
	key, err := t.key(true)
	if err != nil {
		return t.cfg, fmt.Errorf("could not make the key the token is kept with: %w", err)
	}
	if cfg.Sealed, err = seal(key, cfg.Token); err != nil {
		return t.cfg, err
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	if err := writePrivate(t.configPath(), b, true); err != nil {
		return t.cfg, err
	}
	t.cfg, t.stamp = cfg, time.Time{}
	t.load()
	return cfg, nil
}

// writePrivate writes a file only its owner can read, whole or not at all. Not
// to replace one, it fails with fs.ErrExist if the file is there.
func writePrivate(path string, b []byte, replace bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if !replace {
		return os.Link(tmp.Name(), path)
	}
	return os.Rename(tmp.Name(), path)
}

func newCode() string {
	var b [8]byte
	rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func (t *Telegram) setSending(on bool) {
	t.mu.Lock()
	t.sending = on
	t.mu.Unlock()
}

func (t *Telegram) isSending() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sending
}

type sender struct {
	URL string `json:"url"`
}

// claim makes this dv the one sending, unless another is and still answers.
func (t *Telegram) claim(ctx context.Context) bool {
	var s sender
	if b, err := os.ReadFile(t.senderPath()); err == nil {
		json.Unmarshal(b, &s)
	}
	if s.URL == t.self {
		return true
	}
	if s.URL != "" && sends(ctx, s.URL) {
		return false
	}
	b, _ := json.Marshal(sender{t.self})
	return os.WriteFile(t.senderPath(), b, 0o644) == nil
}

// sender is the address of another dv that sends, "" for none.
func (t *Telegram) sender(ctx context.Context) string {
	var s sender
	if b, err := os.ReadFile(t.senderPath()); err == nil {
		json.Unmarshal(b, &s)
	}
	if s.URL == "" || s.URL == t.self || !sends(ctx, s.URL) {
		return ""
	}
	return s.URL
}

func (t *Telegram) letGo() {
	t.setSending(false)
	var s sender
	if b, err := os.ReadFile(t.senderPath()); err == nil && json.Unmarshal(b, &s) == nil && s.URL == t.self {
		os.Remove(t.senderPath())
	}
}

// sends reports whether the dv at url answers that it sends.
func sends(ctx context.Context, url string) bool {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/api/notify/telegram", nil)
	if err != nil {
		return false
	}
	store.MarkLocal(req)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var st struct {
		Sending bool `json:"sending"`
	}
	return resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&st) == nil && st.Sending
}
