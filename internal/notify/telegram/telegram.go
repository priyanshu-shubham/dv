// Package telegram is dv's conversation (package chat) in Telegram, through
// a bot of the reader's own. Each session has a thread - a topic - in the
// bot's chat: its notices go there, and what the reader writes there goes to
// it.
//
// Telegram hands a bot's messages to one listener at a time, so of the dvs
// running on a computer one sends for all of them: the first to find the bot
// set up and no other dv sending. The rest leave it be.
package telegram

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"dv/internal/agent"
	"dv/internal/notify"
	"dv/internal/notify/chat"
	"dv/internal/secret"
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

// Telegram is the platform. Every dv has one; only the one sending uses it.
type Telegram struct {
	conv   *chat.Conversation
	self   string // this dv's address, by which the others know it sends
	dir    string
	keyDir string
	api    botAPI
	wake   chan struct{}
	start  time.Time

	mu      sync.Mutex
	cfg     Config
	stamp   time.Time // cfg's file's, as read
	secret  []byte    // the key the token is sealed with, once read
	sending bool
	topics  *bool // whether the bot has them, once asked
}

// New readies the provider of a dv at self, keeping its settings in dir and
// the key that seals its token in keyDir.
func New(center *notify.Center, self, dir, keyDir string) *Telegram {
	t := &Telegram{
		self: self, dir: dir, keyDir: keyDir,
		api:  botAPI{"https://api.telegram.org", &http.Client{Timeout: callTimeout}},
		wake: make(chan struct{}, 1), start: time.Now(),
	}
	t.conv = chat.New(center, t)
	return t
}

// Run sends, if this dv is the one sending, until ctx ends; then lets go of
// the bot for another dv to take.
func (t *Telegram) Run(ctx context.Context) {
	go t.conv.Run(ctx)
	t.poll(ctx)
	t.letGo()
}

// Send and Settle are the notices', as a notify provider's.
func (t *Telegram) Send(n notify.Notice)   { t.conv.Send(n) }
func (t *Telegram) Settle(n notify.Notice) { t.conv.Settle(n) }

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

// Messages this much older than this dv were for one before it, and are
// dropped; newer ones came while dv restarted, and are still acted on.
const staleAfter = 10 * time.Minute

// said takes a message: the one that connects the chat, or one for the
// conversation, with its pictures, fetched from Telegram.
func (t *Telegram) said(ctx context.Context, cfg Config, m *message) {
	if code, ok := strings.CutPrefix(m.Text, "/start "); ok && cfg.Code != "" && code == cfg.Code && m.Chat.Type == "private" {
		t.connect(ctx, m)
		return
	}
	if m.Chat.ID != cfg.Chat || cfg.Chat == 0 || !m.sent() || time.Unix(m.Date, 0).Before(t.start.Add(-staleAfter)) {
		return
	}
	in, err := t.gather(ctx, cfg, m)
	if err != nil {
		t.post(ctx, cfg, m.Thread, html.EscapeString(err.Error()), nil)
		return
	}
	in.Thread, in.Ref = id(m.Thread), id(m.ID)
	if m.ReplyTo != nil {
		in.ReplyTo = id(m.ReplyTo.ID)
	}
	t.conv.Said(ctx, in)
}

// tapped takes a button: the reader's, for the conversation.
func (t *Telegram) tapped(ctx context.Context, cfg Config, q *callback) {
	answer := map[string]any{"callback_query_id": q.ID}
	if q.From.ID == cfg.Chat {
		tap := chat.Tap{Data: q.Data}
		if q.Message != nil {
			tap.Thread, tap.Ref = id(q.Message.Thread), id(q.Message.ID)
		}
		if said, _ := t.conv.Tapped(ctx, tap); said != "" {
			answer["text"] = said
		}
	}
	t.call(ctx, cfg, "answerCallbackQuery", answer, nil)
}

// connect makes the chat that sent the code the one dv sends to.
func (t *Telegram) connect(ctx context.Context, m *message) {
	name := ""
	if m.From != nil {
		name = m.From.FirstName
		if name == "" {
			name = "@" + m.From.Username
		}
	}
	cfg, err := t.change(func(c *Config) {
		c.Chat, c.Name, c.Code = m.Chat.ID, name, ""
	})
	if err == nil {
		t.post(ctx, cfg, 0, html.EscapeString("Connected to dv on "+hostname()+".\n\n"+chat.Help), nil)
		t.menu(ctx, cfg)
	}
}

// Most of a picture an agent takes: 5 MB, once in base64.
const maxPicture = 5 << 20 / 4 * 3

// gather is a message's words, and the pictures and files of it or of the
// album it starts, fetched from Telegram.
func (t *Telegram) gather(ctx context.Context, cfg Config, m *message) (chat.In, error) {
	var in chat.In
	var words []string
	for _, p := range append([]*message{m}, m.more...) {
		for _, w := range []string{p.Text, p.Caption} {
			if w != "" {
				words = append(words, w)
			}
		}
		a, err := p.attachment()
		if err != nil {
			return in, err
		}
		if a == nil {
			continue
		}
		data, err := t.api.download(ctx, cfg.Token, a.FileID)
		if err != nil {
			return in, fmt.Errorf("Could not fetch %s from Telegram: %w", cmp.Or(a.Name, "the picture"), err)
		}
		if a.picture {
			in.Images = append(in.Images, notify.Image{Type: a.MimeType, Data: data})
		} else {
			in.Files = append(in.Files, notify.File{Name: a.Name, Data: data})
		}
	}
	in.Text = strings.Join(words, "\n\n")
	return in, nil
}

// sent is what goes with a message's words: a picture an agent looks at as
// one, or any other file, saved for it to read.
type sent struct {
	document
	picture bool
}

// attachment is the picture or file a message has; none for words alone.
func (m *message) attachment() (*sent, error) {
	d := m.Document
	if d == nil {
		d = m.media()
	}
	switch {
	case len(m.Photo) > 0:
		// Photos are JPEG, in their largest size an agent takes.
		i := len(m.Photo) - 1
		for i > 0 && m.Photo[i].Size > maxPicture {
			i--
		}
		return &sent{document{FileID: m.Photo[i].FileID, MimeType: "image/jpeg"}, true}, nil
	case m.Sticker != nil:
		return nil, errors.New("dv takes words, pictures and files here, not stickers.")
	case d == nil:
		return nil, nil
	case d.Size > maxFetch:
		return nil, fmt.Errorf("%s is over the %d MB Telegram lets dv fetch.", cmp.Or(d.Name, "That file"), maxFetch>>20)
	case agent.ImageTypes[d.MimeType] && d.Size <= maxPicture:
		return &sent{*d, true}, nil
	}
	// A picture too big to send goes as a file, as it does from the page.
	f := *d
	f.Name = cmp.Or(f.Name, "file")
	return &sent{f, false}, nil
}

// introduce tells Telegram the bot's commands, for the menu its apps show,
// and learns whether the bot has topics, which sessions' threads are.
func (t *Telegram) introduce(ctx context.Context, cfg Config) {
	var commands []map[string]string
	for _, c := range chat.Commands {
		commands = append(commands, map[string]string{"command": c.Name, "description": c.Description})
	}
	t.call(ctx, cfg, "setMyCommands", map[string]any{"commands": commands}, nil)
	t.hasTopics(ctx, cfg)
	t.menu(ctx, cfg)
}

// menu puts dv on the button beside where the reader writes, to open it in
// Telegram. That takes an https address, which a dv reached only on this
// computer has none of.
func (t *Telegram) menu(ctx context.Context, cfg Config) {
	link := chat.LinkTo(cfg.Origin, "/")
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

var errNoTopics = errors.New("This bot has no topics, which dv puts each session in. Turn them on for it in @BotFather's Mini App: Open, pick the bot, then Bot Settings.")

func (t *Telegram) sleep(ctx context.Context, d time.Duration) {
	select {
	case <-time.After(d):
	case <-t.wake:
	case <-ctx.Done():
	}
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
	var rows [][]chat.Button
	if link := chat.LinkTo(cfg.Origin, "/"); link != "" {
		rows = [][]chat.Button{{{Text: "Open dv", URL: link}}}
	}
	_, err = t.post(ctx, cfg, 0, "dv on "+html.EscapeString(hostname())+" can reach you here.", rows)
	return err
}

// The platform, as the conversation talks through it.

func (t *Telegram) Via() string { return "Telegram" }

func (t *Telegram) Ready() bool { return t.isSending() && t.config().Chat != 0 }

func (t *Telegram) Origin() string { return t.config().Origin }

// A message holds this much of a body, well inside Telegram's 4096 characters
// with the rest of the message; a reply runs to this many messages at most.
const (
	pageChars = 3000
	maxPages  = 5
)

// Post sends a message, a long one as several, rendered from Markdown in the
// HTML Telegram takes - or as written, where Telegram will not take that.
func (t *Telegram) Post(ctx context.Context, thread string, m chat.Out) (string, error) {
	cfg := t.config()
	pages := chat.Split(m.Text, pageChars)
	if len(pages) > maxPages {
		pages = pages[:maxPages]
		pages[maxPages-1] += "\n\n…"
	} else if len(pages) == 0 {
		pages = []string{""}
	}
	var ref int64
	for i, page := range pages {
		var rows [][]chat.Button
		if i == len(pages)-1 {
			rows = m.Buttons
		}
		var err error
		ref, err = t.post(ctx, cfg, num(thread), render(page, m.Plain), rows)
		if unparsed(err) {
			ref, err = t.post(ctx, cfg, num(thread), html.EscapeString(page), rows)
		}
		if err != nil {
			return id(ref), err
		}
	}
	return id(ref), nil
}

func (t *Telegram) Edit(ctx context.Context, thread, ref string, m chat.Out) error {
	cfg := t.config()
	text := m.Text
	if len(text) > pageChars {
		text = chat.Split(text, pageChars)[0]
	}
	params := map[string]any{"chat_id": cfg.Chat, "message_id": num(ref), "text": render(text, m.Plain), "parse_mode": "HTML", "link_preview_options": noPreview}
	params["reply_markup"] = markup(m.Buttons)
	err := t.call(ctx, cfg, "editMessageText", params, nil)
	if unparsed(err) {
		params["text"] = html.EscapeString(text)
		err = t.call(ctx, cfg, "editMessageText", params, nil)
	}
	return err
}

func (t *Telegram) Unbutton(ctx context.Context, thread, ref string) error {
	cfg := t.config()
	return t.call(ctx, cfg, "editMessageReplyMarkup", map[string]any{"chat_id": cfg.Chat, "message_id": num(ref), "reply_markup": markup(nil)}, nil)
}

func (t *Telegram) NewThread(ctx context.Context, name string) (string, error) {
	cfg := t.config()
	var topic struct {
		ID int64 `json:"message_thread_id"`
	}
	err := t.call(ctx, cfg, "createForumTopic", map[string]any{"chat_id": cfg.Chat, "name": name}, &topic)
	return id(topic.ID), err
}

func (t *Telegram) Rename(ctx context.Context, thread, name string) error {
	cfg := t.config()
	return t.call(ctx, cfg, "editForumTopic", map[string]any{"chat_id": cfg.Chat, "message_thread_id": num(thread), "name": name}, nil)
}

func (t *Telegram) Gone(err error) bool {
	e := (*apiError)(nil)
	return errors.As(err, &e) && strings.Contains(e.Description, "thread not found")
}

func (t *Telegram) Thread(key string) (chat.Thread, bool) {
	th, ok := t.config().Threads[key]
	return chat.Thread{ID: id(th.ID), Name: th.Name}, ok
}

func (t *Telegram) SetThread(key string, th chat.Thread) {
	t.change(func(c *Config) {
		if c.Threads == nil {
			c.Threads = map[string]Thread{}
		}
		c.Threads[key] = Thread{num(th.ID), th.Name}
	})
}

func (t *Telegram) DropThread(key string) {
	t.change(func(c *Config) { delete(c.Threads, key) })
}

func (t *Telegram) ThreadOf(thread string) (string, bool) {
	for k, th := range t.config().Threads {
		if id(th.ID) == thread {
			return k, true
		}
	}
	return "", false
}

// A message the reader sent wears how far its session has got with it: 👀
// while it waits behind the step the agent is on, 👨‍💻 once taken up, nothing
// once the turn is over, its reply below it. Of the few emoji a bot may react
// with, none is ⏳ or ✅. While the agent works the thread shows the bot
// typing, which Telegram keeps up for 5 seconds.
func (t *Telegram) Mark(ctx context.Context, thread, ref string, was, at notify.Progress) {
	if face(was) != face(at) {
		t.react(ctx, ref, face(at))
	}
}

func (t *Telegram) Busy(ctx context.Context, thread string) {
	cfg := t.config()
	params := map[string]any{"chat_id": cfg.Chat, "action": "typing"}
	if thread != "" {
		params["message_thread_id"] = num(thread)
	}
	t.call(ctx, cfg, "sendChatAction", params, nil)
}

func (t *Telegram) Ack(ctx context.Context, thread, ref string) { t.react(ctx, ref, "👌") }

// face is the reaction for how far a session has got, "" for none.
func face(at notify.Progress) string {
	switch at {
	case notify.Queued:
		return "👀"
	case notify.Working, notify.Asking:
		return "👨‍💻"
	}
	return ""
}

// react puts an emoji on a message; "" takes it off.
func (t *Telegram) react(ctx context.Context, ref, emoji string) {
	cfg := t.config()
	reaction := []map[string]string{}
	if emoji != "" {
		reaction = append(reaction, map[string]string{"type": "emoji", "emoji": emoji})
	}
	t.call(ctx, cfg, "setMessageReaction", map[string]any{"chat_id": cfg.Chat, "message_id": num(ref), "reaction": reaction}, nil)
}

// id and num are Telegram's numbers as the conversation keeps them, "" for 0.
func id(n int64) string {
	if n == 0 {
		return ""
	}
	return strconv.FormatInt(n, 10)
}

func num(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

func render(text string, plain bool) string {
	if plain {
		return html.EscapeString(text)
	}
	return markdownHTML(text)
}

// post sends a message's HTML to the reader, in a thread or the chat itself,
// returning its id. A link Telegram will not take - to an address on the
// reader's network - is left off.
func (t *Telegram) post(ctx context.Context, cfg Config, thread int64, text string, rows [][]chat.Button) (int64, error) {
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

func markup(rows [][]chat.Button) map[string]any {
	keyboard := [][]button{}
	for _, r := range rows {
		var row []button
		for _, b := range r {
			row = append(row, button(b))
		}
		keyboard = append(keyboard, row)
	}
	return map[string]any{"inline_keyboard": keyboard}
}

func withoutLinks(rows [][]chat.Button) [][]chat.Button {
	var out [][]chat.Button
	for _, r := range rows {
		if r[0].URL == "" {
			out = append(out, r)
		}
	}
	return out
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
	if err := secret.WritePrivate(t.configPath(), b, true); err != nil {
		return t.cfg, err
	}
	t.cfg, t.stamp = cfg, time.Time{}
	t.load()
	return cfg, nil
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
