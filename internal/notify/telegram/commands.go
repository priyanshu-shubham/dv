package telegram

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"slices"
	"strings"
	"time"
	"unicode"

	"dv/internal/notify"
)

// commands are the bot's, as its apps offer them in a menu.
var commands = []map[string]string{
	{"command": "sessions", "description": "The sessions open in dv, to open a thread for one"},
	{"command": "new", "description": "Start a session, picking where: /new [folder] what to do"},
	{"command": "stop", "description": "Stop the turn of this thread's session"},
	{"command": "last", "description": "What this thread's session said last"},
	{"command": "model", "description": "Pick the model of this thread's session"},
	{"command": "help", "description": "What this bot does"},
}

const help = `dv tells you here what its agents want and have done, each session in a thread of its own.

Write what a session is to do to start one, in the folder set in dv's Settings. Words before a colon say where instead: "notes: …" in the folder notes, "wt: …" in a new worktree, "notes wt fix/login: …" in one of notes on that branch, "here: …" in the folder itself.

In a session's thread, write to send it a message, pictures and all. /stop stops its turn, /last shows what it said last, and /model picks its model.

/sessions lists the sessions open in dv, to open a thread for one, and /new starts one, asking where.`

// via is what the agent is told its messages from here came through.
const via = "Telegram"

// Messages this much older than this dv were for one before it, and are
// dropped; newer ones came while dv restarted, and are still acted on.
const staleAfter = 10 * time.Minute

// said acts on a message: the one that connects the chat, a command, or words
// for the session whose thread they are in.
func (t *Telegram) said(ctx context.Context, cfg Config, m *message) {
	if code, ok := strings.CutPrefix(m.Text, "/start "); ok && cfg.Code != "" && code == cfg.Code && m.Chat.Type == "private" {
		t.connect(ctx, m)
		return
	}
	if m.Chat.ID != cfg.Chat || cfg.Chat == 0 || !m.sent() || time.Unix(m.Date, 0).Before(t.start.Add(-staleAfter)) {
		return
	}
	folder, session, isSession := t.sessionOf(m.Thread)
	here := func(s string) { t.say(ctx, cfg, m.Thread, s, nil) }
	command, args := parseCommand(m.Text)
	t.mu.Lock()
	var asked pick
	var answering bool
	if m.ReplyTo != nil {
		asked, answering = t.asked[m.ReplyTo.ID]
		delete(t.asked, m.ReplyTo.ID)
	}
	t.mu.Unlock()
	switch {
	case answering && command == "" && m.Text != "":
		// The branch for a new worktree, named by the reader.
		asked.branch, asked.fresh = strings.TrimSpace(m.Text), false
		t.inWorktree(ctx, cfg, m.Thread, asked)
	case command == "start" || command == "help":
		here(help)
	case command == "sessions":
		t.listSessions(ctx, cfg, m.Thread)
	case command == "new":
		if args != "" && isSession && !t.namesPlace(args) {
			// In a session's thread, a folder unnamed is the session's own.
			t.whereTo(ctx, cfg, m.Thread, pick{place: t.place(folder), text: args, message: m.ID})
			break
		}
		p := pick{text: args, message: m.ID}
		if !isSession {
			p.adopt = m.Thread
		}
		t.newSession(ctx, cfg, m.Thread, p)
	case !isSession && strings.HasPrefix(m.Text, "/"):
		here("That is for a session's thread. /sessions opens one for a session.")
	case !isSession:
		t.quickStart(ctx, cfg, m)
	case command == "stop":
		if err := t.center.Stop(folder, session); err != nil {
			here(err.Error())
		} else {
			t.react(ctx, cfg, m.ID, "👌")
		}
	case command == "last":
		t.showLast(ctx, cfg, m.Thread, folder, session)
	case command == "model":
		t.askModel(ctx, cfg, m.Thread, folder, session)
	default:
		// Any other command is the agent's: /compact, say.
		t.send(ctx, cfg, m, folder, session)
	}
}

// Most of a picture an agent takes: 5 MB, once in base64.
const maxPicture = 5 << 20 / 4 * 3

// send hands a message to its thread's session.
func (t *Telegram) send(ctx context.Context, cfg Config, m *message, folder, session string) {
	text, images, err := t.gather(ctx, cfg, m)
	if err != nil {
		t.say(ctx, cfg, m.Thread, err.Error(), nil)
		return
	}
	id, err := t.center.Send(folder, session, notify.Message{Text: text, Images: images, Via: via})
	if err != nil {
		t.say(ctx, cfg, m.Thread, err.Error(), nil)
		return
	}
	t.follow(&flight{folder: folder, session: session, id: id, message: m.ID, thread: m.Thread})
}

// gather is a message's words, and the pictures of it or of the album it
// starts, fetched from Telegram.
func (t *Telegram) gather(ctx context.Context, cfg Config, m *message) (string, []notify.Image, error) {
	var words []string
	var images []notify.Image
	for _, p := range append([]*message{m}, m.more...) {
		words = append(words, p.Text, p.Caption)
		id, kind, err := p.picture()
		if err == nil && id != "" {
			var data []byte
			if data, err = t.api.download(ctx, cfg.Token, id); err != nil {
				err = fmt.Errorf("Could not fetch the picture from Telegram: %w", err)
			}
			images = append(images, notify.Image{Type: kind, Data: data})
		}
		if err != nil {
			return "", nil, err
		}
	}
	return strings.Join(slices.DeleteFunc(words, func(s string) bool { return s == "" }), "\n\n"), images, nil
}

// picture is the file of the picture a message has, and its kind; none for
// words alone. What else it has, dv does not take.
func (m *message) picture() (id, kind string, err error) {
	switch d := m.Document; {
	case len(m.Photo) > 0:
		// Photos are JPEG, in their largest size an agent takes.
		for _, p := range slices.Backward(m.Photo) {
			if p.Size <= maxPicture {
				return p.FileID, "image/jpeg", nil
			}
		}
		return m.Photo[0].FileID, "image/jpeg", nil
	case d != nil && strings.HasPrefix(d.MimeType, "image/") && d.Size > maxPicture:
		return "", "", errors.New("That picture is too big to send as a file. Send it as a photo, which Telegram makes smaller.")
	case d != nil && strings.HasPrefix(d.MimeType, "image/"):
		return d.FileID, d.MimeType, nil
	case d != nil || m.other():
		return "", "", errors.New("dv takes words and pictures here: not voice notes, videos or other files.")
	}
	return "", "", nil
}

// parseCommand splits "/new@dv_bot fix it" into "new" and "fix it"; words
// that are no command give "".
func parseCommand(text string) (string, string) {
	if !strings.HasPrefix(text, "/") {
		return "", ""
	}
	word, args, _ := strings.Cut(text[1:], " ")
	word, _, _ = strings.Cut(word, "@")
	for _, c := range commands {
		if c["command"] == word {
			return word, strings.TrimSpace(args)
		}
	}
	if word == "start" {
		return word, ""
	}
	return "", ""
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
		t.say(ctx, cfg, 0, "Connected to dv on "+hostname()+".\n\n"+help, nil)
		t.menu(ctx, cfg)
	}
}

// react puts an emoji on a message, as all the answer it needs; "" takes it off.
func (t *Telegram) react(ctx context.Context, cfg Config, message int64, emoji string) {
	reaction := []map[string]string{}
	if emoji != "" {
		reaction = append(reaction, map[string]string{"type": "emoji", "emoji": emoji})
	}
	t.call(ctx, cfg, "setMessageReaction", map[string]any{"chat_id": cfg.Chat, "message_id": message, "reaction": reaction}, nil)
}

// pick is what a button stands for: a session to open a thread for; a step
// towards starting one with text - the folder, then here or in a new
// worktree, then the worktree's branch; or in a session's model picker.
type pick struct {
	session notify.Session
	place   notify.Place
	text    string
	images  []notify.Image
	// "folder", "here", "worktree", "branch"; "place", the folder of a session
	// whose message said the rest; "change", "model", "effort", "shut"
	step     string
	worktree bool // for "place": in a new worktree of it
	branch   string
	fresh    bool  // branch was made up, so is numbered on if taken
	adopt    int64 // the reader's thread the session takes as its own; 0 makes one
	message  int64 // the reader's that asked for it, to react on

	intro string // the picker's message's words, above the setup
	value string // the model or effort picked
	asNew bool   // picked for new sessions too
}

const pickPrefix = "p:"

// offer keeps what a button is to stand for, and returns its data.
func (t *Telegram) offer(p pick) string {
	var b [6]byte
	rand.Read(b[:])
	token := hex.EncodeToString(b[:])
	t.mu.Lock()
	if len(t.picks) > 500 {
		clear(t.picks) // lists that old are long gone from the screen
	}
	t.picks[token] = p
	t.mu.Unlock()
	return pickPrefix + token
}

func (t *Telegram) picked(ctx context.Context, cfg Config, q *callback, token string) {
	t.mu.Lock()
	p, ok := t.picks[token]
	t.mu.Unlock()
	said := ""
	var thread int64
	if q.Message != nil {
		thread = q.Message.Thread
	}
	switch {
	case !ok:
		said = "That is from before dv restarted; ask again."
	case p.step == "folder":
		t.whereTo(ctx, cfg, thread, p)
	case p.step == "place":
		t.begin(ctx, cfg, thread, p)
	case p.step == "here":
		t.startIn(ctx, cfg, thread, p.place, p)
	case p.step == "worktree":
		t.askBranch(ctx, cfg, thread, p)
	case p.step == "branch":
		t.inWorktree(ctx, cfg, thread, p)
	case p.step == "change" || p.step == "model" || p.step == "effort" || p.step == "shut":
		said = t.pickSetup(ctx, cfg, q, p)
	default:
		t.openThread(ctx, cfg, p.session)
	}
	t.call(ctx, cfg, "answerCallbackQuery", map[string]any{"callback_query_id": q.ID, "text": said}, nil)
}

// listSessions offers the sessions open in dv, the latest first.
func (t *Telegram) listSessions(ctx context.Context, cfg Config, thread int64) {
	sessions := t.center.Sessions()
	if len(sessions) == 0 {
		t.say(ctx, cfg, thread, "No sessions are open in dv. /new [folder] what to do starts one.", nil)
		return
	}
	var rows [][]button
	for _, s := range sessions[:min(len(sessions), 20)] {
		rows = append(rows, []button{{Text: state(s) + topicName(s.Title, s.Place), Data: t.offer(pick{session: s})}})
	}
	t.say(ctx, cfg, thread, "Open a thread for:", rows)
}

func state(s notify.Session) string {
	switch {
	case s.Asking:
		return "✋ "
	case s.Busy:
		return "⏳ "
	case s.Running == "terminal":
		return "🖥 "
	}
	return ""
}

// openThread starts a session's thread, if it has none, with what it said last.
func (t *Telegram) openThread(ctx context.Context, cfg Config, s notify.Session) {
	t.inThread(ctx, cfg, s.Folder, s.ID, s.Title, s.Place, func(thread int64) error {
		intro := "<b>" + html.EscapeString(topicName(s.Title, s.Place)) + "</b>\nWrite here to talk to it."
		if s.Running == "terminal" {
			intro = "<b>" + html.EscapeString(topicName(s.Title, s.Place)) + "</b>\nIt runs in a terminal: its news comes here, but only the terminal can write to it."
		}
		if thread == 0 {
			intro += "\n\n<i>This bot has no topics, so sessions have no threads: turn them on in @BotFather.</i>"
		}
		_, err := t.post(ctx, cfg, thread, intro, nil)
		if err == nil && thread != 0 {
			t.showLast(ctx, cfg, thread, s.Folder, s.ID)
		}
		return err
	})
}

// showLast sends what a session said last, in its thread.
func (t *Telegram) showLast(ctx context.Context, cfg Config, thread int64, folder, session string) {
	reply, err := t.center.Reply(folder, session)
	switch {
	case err != nil:
		t.say(ctx, cfg, thread, err.Error(), nil)
	case reply == "":
		t.say(ctx, cfg, thread, "Nothing said since your last message.", nil)
	default:
		n := notify.Notice{Kind: notify.Done, Title: "Last said", Body: reply, Format: notify.Markdown}
		if _, err := t.postAll(ctx, cfg, thread, pages(n, true, true), nil); unparsed(err) {
			t.postAll(ctx, cfg, thread, pages(n, true, false), nil)
		}
	}
}

// newSession starts one from "/new [folder] what to do", p.text: in the
// folder named first, the only one there is, or one the reader picks, then
// asking whether in a new worktree of it.
func (t *Telegram) newSession(ctx context.Context, cfg Config, thread int64, p pick) {
	places := t.center.Places()
	if len(places) == 0 {
		t.say(ctx, cfg, thread, "dv has no folder to start a session in: add one in the hub first.", nil)
		return
	}
	var in *notify.Place
	if first, rest, _ := strings.Cut(p.text, " "); first != "" {
		if at, ok := notify.Named(places, first); ok {
			in, p.text = &at, strings.TrimSpace(rest)
		}
	}
	if p.text == "" {
		t.say(ctx, cfg, thread, "Say what the session is to do: /new [folder] what to do", nil)
		return
	}
	if in == nil && len(places) == 1 {
		in = &places[0]
	}
	if in != nil {
		p.place = *in
		t.whereTo(ctx, cfg, thread, p)
		return
	}
	p.step = "folder"
	t.askFolder(ctx, cfg, thread, places, p, "Start it in which folder?")
}

// askFolder offers the folders for a session, each button going on with p
// in that folder.
func (t *Telegram) askFolder(ctx context.Context, cfg Config, thread int64, places []notify.Place, p pick, question string) {
	var rows [][]button
	for _, at := range places {
		p.place = at
		rows = append(rows, []button{{Text: at.Name, Data: t.offer(p)}})
	}
	t.say(ctx, cfg, thread, question, rows)
}

// quickStart begins a session with a message written outside any session's
// thread: where its first words say (notify.ParseWhere), or else where
// Settings do. A thread Telegram made for the message becomes the session's.
func (t *Telegram) quickStart(ctx context.Context, cfg Config, m *message) {
	text, images, err := t.gather(ctx, cfg, m)
	if err != nil {
		t.say(ctx, cfg, m.Thread, err.Error(), nil)
		return
	}
	places := t.center.Places()
	if len(places) == 0 {
		t.say(ctx, cfg, m.Thread, "dv has no folder to start a session in: add one in the hub first.", nil)
		return
	}
	where, text := notify.ParseWhere(text, places)
	if text == "" && len(images) == 0 {
		t.say(ctx, cfg, m.Thread, "Say what the session is to do, after the colon.", nil)
		return
	}
	d := t.center.Defaults()
	p := pick{
		text: text, images: images, adopt: m.Thread, message: m.ID, step: "place",
		worktree: where.Worktree || d.Worktree && !where.Here, branch: where.Branch,
	}
	i := slices.IndexFunc(places, func(at notify.Place) bool { return at.Slug == d.Folder })
	switch {
	case where.Place != nil:
		p.place = *where.Place
	case i >= 0:
		p.place = places[i]
	case len(places) == 1:
		p.place = places[0]
	default:
		t.askFolder(ctx, cfg, m.Thread, places, p, "Start it in which folder? A default folder in dv's Settings saves asking.")
		return
	}
	t.begin(ctx, cfg, m.Thread, p)
}

// begin starts a session as p says: in its folder, or in a new worktree of
// it, on the branch named or one named from what the session is to do.
func (t *Telegram) begin(ctx context.Context, cfg Config, thread int64, p pick) {
	if p.worktree && p.place.Git {
		if p.fresh = p.branch == ""; p.fresh {
			p.branch = branchFor(p.text)
		}
		t.inWorktree(ctx, cfg, thread, p)
		return
	}
	t.startIn(ctx, cfg, thread, p.place, p)
}

// namesPlace is whether /new's words start with a folder's name.
func (t *Telegram) namesPlace(args string) bool {
	first, _, _ := strings.Cut(args, " ")
	_, ok := notify.Named(t.center.Places(), first)
	return ok
}

// place is the folder at slug, as a session can be started in.
func (t *Telegram) place(slug string) notify.Place {
	for _, p := range t.center.Places() {
		if p.Slug == slug {
			return p
		}
	}
	return notify.Place{Slug: slug}
}

// whereTo asks whether a session is to start in the folder itself or in a new
// worktree of it, where a worktree can be made.
func (t *Telegram) whereTo(ctx context.Context, cfg Config, thread int64, p pick) {
	if !p.place.Git {
		t.startIn(ctx, cfg, thread, p.place, p)
		return
	}
	here, wt := p, p
	here.step, wt.step = "here", "worktree"
	t.say(ctx, cfg, thread, "Start it in "+p.place.Name+" itself, or in a new worktree of it?", [][]button{
		{{Text: "In " + p.place.Name, Data: t.offer(here)}},
		{{Text: "In a new worktree", Data: t.offer(wt)}},
	})
}

// askBranch asks for the new worktree's branch, offering one named from what
// the session is to do. A reply to the question names another.
func (t *Telegram) askBranch(ctx context.Context, cfg Config, thread int64, p pick) {
	suggested := p
	suggested.step, suggested.branch, suggested.fresh = "branch", branchFor(p.text), true
	id, err := t.post(ctx, cfg, thread, html.EscapeString("Name the new worktree's branch. It starts from what "+p.place.Name+
		" has checked out, without what is not committed there. Tap the name, or reply to this message with another."),
		[][]button{{{Text: suggested.branch, Data: t.offer(suggested)}}})
	if err == nil {
		t.mu.Lock()
		if len(t.asked) > 100 {
			clear(t.asked) // questions that old are not being answered
		}
		t.asked[id] = p
		t.mu.Unlock()
	}
}

// inWorktree makes the worktree and starts the session in it. Making it can
// take a while - its setup hook runs too - so it goes on by itself.
func (t *Telegram) inWorktree(ctx context.Context, cfg Config, thread int64, p pick) {
	t.say(ctx, cfg, thread, "Making the worktree "+p.branch+" of "+p.place.Name+"…", nil)
	go func() {
		made, err := t.center.Worktree(p.place.Slug, p.branch, p.fresh)
		if err != nil {
			t.say(ctx, cfg, thread, "Could not make the worktree: "+err.Error(), nil)
			return
		}
		t.startIn(ctx, cfg, thread, made, p)
	}()
}

// branchFor names a branch after what a session is to do: its first few
// words, or the time when there are none to use.
func branchFor(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	var words []string
	for _, w := range strings.FieldsFunc(strings.ToLower(line), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(words) == 5 || len(strings.Join(append(words, w), "-")) > 40 {
			break
		}
		words = append(words, w)
	}
	if len(words) == 0 {
		return "session-" + time.Now().Format("0102-1504")
	}
	return strings.Join(words, "-")
}

// startIn starts a session as p says, in the folder at: in the reader's
// thread p.adopt, or else a thread of its own named for what it is to do
// until it has a title.
func (t *Telegram) startIn(ctx context.Context, cfg Config, thread int64, at notify.Place, p pick) {
	id, err := t.center.Start(at.Slug, notify.Message{Text: p.text, Images: p.images, Via: via})
	if err != nil {
		t.say(ctx, cfg, thread, "Could not start it: "+err.Error(), nil)
		return
	}
	title := firstLine(p.text)
	intro := "<b>Started</b> in " + html.EscapeString(at.Name)
	switch {
	case at.Slug != p.place.Slug:
		intro += ", a new worktree of " + html.EscapeString(p.place.Name) + " on the branch <code>" + html.EscapeString(p.branch) + "</code>"
	case p.worktree:
		intro += " itself: it is no git repository, so it has no worktrees"
	}
	if p.adopt != 0 {
		t.adopt(ctx, cfg, p.adopt, at.Slug, id, title, at.Name)
		intro += "."
	} else {
		intro += ": " + html.EscapeString(clip(p.text, 500)) + "\n\nIts replies come here."
	}
	s, _ := t.center.Setup(at.Slug, id)
	setup := pick{session: notify.Session{Folder: at.Slug, ID: id}, intro: intro, asNew: true}
	var in int64
	err = t.inThread(ctx, cfg, at.Slug, id, title, at.Name, func(thread int64) error {
		in = thread
		_, err := t.post(ctx, cfg, thread, setupText(intro, s), t.setupRows(s, setup, false))
		return err
	})
	if err != nil {
		return
	}
	t.follow(&flight{folder: at.Slug, session: id, message: p.message, thread: in})
	if in != thread {
		t.say(ctx, cfg, thread, "Started, in its thread "+topicName(title, at.Name)+".", nil)
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return clip(line, 60)
}

var errNoTopics = errors.New("This bot has no topics, which dv puts each session in. Turn them on for it in @BotFather's Mini App: Open, pick the bot, then Bot Settings.")
