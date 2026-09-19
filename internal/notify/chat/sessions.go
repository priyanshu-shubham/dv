package chat

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"slices"
	"strings"
	"time"
	"unicode"

	"dv/internal/notify"
)

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
	fresh    bool   // branch was made up, so is numbered on if taken
	adopt    string // the reader's thread the session takes as its own; "" makes one
	message  string // the reader's that asked for it, to show progress on

	intro string // the picker's message's words, above the setup
	value string // the model or effort picked
	asNew bool   // picked for new sessions too

	question, label string // the question it answers, and its button's words
}

// answer is a button of a question, going on with p.
type answer struct {
	label string
	p     pick
}

// ask puts a question to the reader, a button for each answer. It takes one:
// the first tap, or reply, spends it.
func (c *Conversation) ask(ctx context.Context, thread, question string, answers []answer) (string, error) {
	var rows [][]Button
	for _, a := range answers {
		a.p.question, a.p.label = question, a.label
		rows = append(rows, []Button{{Text: a.label, Data: c.offer(a.p)}})
	}
	return c.p.Post(ctx, thread, Out{Text: question, Plain: true, Buttons: rows, Private: true})
}

// answered spends the question on the message ref, which then shows its
// answer instead of its buttons, and reports whether it was not spent
// already. One on a message the app gave no ref for is never spent.
func (c *Conversation) answered(ctx context.Context, thread, ref, question, answer string) bool {
	if ref == "" {
		return true
	}
	c.mu.Lock()
	spent := c.spent[ref]
	if !spent {
		if len(c.spent) > 500 {
			clear(c.spent) // as the picks those questions offered are
		}
		c.spent[ref] = true
	}
	c.mu.Unlock()
	if spent {
		return false
	}
	c.p.Edit(ctx, thread, ref, Out{Text: question + "\n✓ " + answer, Plain: true, Private: true})
	return true
}

const pickPrefix = "p:"

// offer keeps what a button is to stand for, and returns its data.
func (c *Conversation) offer(p pick) string {
	var b [6]byte
	rand.Read(b[:])
	token := hex.EncodeToString(b[:])
	c.mu.Lock()
	if len(c.picks) > 500 {
		clear(c.picks) // lists that old are long gone from the screen
	}
	c.picks[token] = p
	c.mu.Unlock()
	return pickPrefix + token
}

func (c *Conversation) picked(ctx context.Context, t Tap, token string) string {
	c.mu.Lock()
	p, ok := c.picks[token]
	c.mu.Unlock()
	once := p.step == "folder" || p.step == "place" || p.step == "here" || p.step == "worktree" || p.step == "branch"
	switch {
	case !ok:
		return "That is from before dv restarted; ask again."
	case once && !c.answered(ctx, t.Thread, t.Ref, p.question, p.label):
		return "That is answered already."
	case p.step == "folder":
		c.whereTo(ctx, t.Thread, p)
	case p.step == "place":
		c.begin(ctx, t.Thread, p)
	case p.step == "here":
		c.startIn(ctx, t.Thread, p.place, p)
	case p.step == "worktree":
		c.askBranch(ctx, t.Thread, p)
	case p.step == "branch":
		c.inWorktree(ctx, t.Thread, p)
	case p.step == "change" || p.step == "model" || p.step == "effort" || p.step == "shut":
		return c.pickSetup(ctx, t, p)
	default:
		c.openThread(ctx, p.session)
	}
	return ""
}

// listSessions offers the sessions open in dv, the latest first.
func (c *Conversation) listSessions(ctx context.Context, thread string) {
	sessions := c.center.Sessions()
	if len(sessions) == 0 {
		c.tell(ctx, thread, "No sessions are open in dv. Write what one is to do to start it.", nil)
		return
	}
	var rows [][]Button
	for _, s := range sessions[:min(len(sessions), 20)] {
		rows = append(rows, []Button{{Text: state(s) + topicName(s.Title, s.Place), Data: c.offer(pick{session: s})}})
	}
	c.tell(ctx, thread, "Open a thread for:", rows)
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
func (c *Conversation) openThread(ctx context.Context, s notify.Session) {
	c.inThread(ctx, s.Folder, s.ID, s.Title, s.Place, func(thread string) error {
		intro := "**" + Escape(topicName(s.Title, s.Place)) + "**\nWrite here to talk to it."
		if s.Running == "terminal" {
			intro = "**" + Escape(topicName(s.Title, s.Place)) + "**\nIt runs in a terminal: its news comes here, but only the terminal can write to it."
		}
		_, err := c.p.Post(ctx, thread, Out{Text: intro})
		if err == nil && thread != "" {
			c.showLast(ctx, thread, s.Folder, s.ID)
		}
		return err
	})
}

// showLast sends what a session said last, in its thread.
func (c *Conversation) showLast(ctx context.Context, thread, folder, session string) {
	reply, err := c.center.Reply(folder, session)
	switch {
	case err != nil:
		c.say(ctx, thread, err.Error(), nil)
	case reply == "":
		c.say(ctx, thread, "Nothing said since your last message.", nil)
	default:
		n := notify.Notice{Kind: notify.Done, Title: "Last said", Body: reply, Format: notify.Markdown}
		c.p.Post(ctx, thread, Out{Text: notice(n, true, "")})
	}
}

// newSession starts one from "/new [folder] what to do", p.text: in the
// folder named first, the only one there is, or one the reader picks, then
// asking whether in a new worktree of it.
func (c *Conversation) newSession(ctx context.Context, thread string, p pick) {
	places := c.center.Places()
	if len(places) == 0 {
		c.say(ctx, thread, "dv has no folder to start a session in: add one in the hub first.", nil)
		return
	}
	var in *notify.Place
	if first, rest, _ := strings.Cut(p.text, " "); first != "" {
		if at, ok := notify.Named(places, first); ok {
			in, p.text = &at, strings.TrimSpace(rest)
		}
	}
	if p.text == "" {
		c.say(ctx, thread, "Say what the session is to do: /new [folder] what to do", nil)
		return
	}
	if in == nil && len(places) == 1 {
		in = &places[0]
	}
	if in != nil {
		p.place = *in
		c.whereTo(ctx, thread, p)
		return
	}
	p.step = "folder"
	c.askFolder(ctx, thread, places, p, "Start it in which folder?")
}

// askFolder offers the folders for a session, each button going on with p
// in that folder.
func (c *Conversation) askFolder(ctx context.Context, thread string, places []notify.Place, p pick, question string) {
	var answers []answer
	for _, at := range places {
		p.place = at
		answers = append(answers, answer{at.Name, p})
	}
	c.ask(ctx, thread, question, answers)
}

// quickStart begins a session with a message written outside any session's
// thread: where its first words say (notify.ParseWhere), or else where
// Settings do. A thread the app made for the message becomes the session's.
func (c *Conversation) quickStart(ctx context.Context, in In) {
	places := c.center.Places()
	if len(places) == 0 {
		c.tell(ctx, in.Thread, "dv has no folder to start a session in: add one in the hub first.", nil)
		return
	}
	where, text := notify.ParseWhere(in.Text, places)
	if text == "" && len(in.Images) == 0 {
		c.tell(ctx, in.Thread, "Say what the session is to do, after the colon.", nil)
		return
	}
	d := c.center.Defaults()
	p := pick{
		text: text, images: in.Images, adopt: in.Thread, message: in.Ref, step: "place",
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
		c.askFolder(ctx, in.Thread, places, p, "Start it in which folder? A default folder in dv's Settings saves asking.")
		return
	}
	c.begin(ctx, in.Thread, p)
}

// begin starts a session as p says: in its folder, or in a new worktree of
// it, on the branch named or one named from what the session is to do.
func (c *Conversation) begin(ctx context.Context, thread string, p pick) {
	if p.worktree && p.place.Git {
		if p.fresh = p.branch == ""; p.fresh {
			p.branch = branchFor(p.text)
		}
		c.inWorktree(ctx, thread, p)
		return
	}
	c.startIn(ctx, thread, p.place, p)
}

// namesPlace is whether /new's words start with a folder's name.
func (c *Conversation) namesPlace(args string) bool {
	first, _, _ := strings.Cut(args, " ")
	_, ok := notify.Named(c.center.Places(), first)
	return ok
}

// place is the folder at slug, as a session can be started in.
func (c *Conversation) place(slug string) notify.Place {
	for _, p := range c.center.Places() {
		if p.Slug == slug {
			return p
		}
	}
	return notify.Place{Slug: slug}
}

// whereTo asks whether a session is to start in the folder itself or in a new
// worktree of it, where a worktree can be made.
func (c *Conversation) whereTo(ctx context.Context, thread string, p pick) {
	if !p.place.Git {
		c.startIn(ctx, thread, p.place, p)
		return
	}
	here, wt := p, p
	here.step, wt.step = "here", "worktree"
	c.ask(ctx, thread, "Start it in "+p.place.Name+" itself, or in a new worktree of it?", []answer{
		{"In " + p.place.Name, here},
		{"In a new worktree", wt},
	})
}

// askBranch asks for the new worktree's branch, offering one named from what
// the session is to do. A reply to the question names another.
func (c *Conversation) askBranch(ctx context.Context, thread string, p pick) {
	suggested := p
	suggested.step, suggested.branch, suggested.fresh = "branch", branchFor(p.text), true
	question := "Name the new worktree's branch. It starts from what " + p.place.Name +
		" has checked out, without what is not committed there. Tap the name, or reply to this message with another."
	ref, err := c.ask(ctx, thread, question, []answer{{suggested.branch, suggested}})
	if err == nil {
		p.question = question
		c.mu.Lock()
		if len(c.asked) > 100 {
			clear(c.asked) // questions that old are not being answered
		}
		c.asked[ref] = p
		c.mu.Unlock()
	}
}

// inWorktree makes the worktree and starts the session in it. Making it can
// take a while - its setup hook runs too - so it goes on by itself.
func (c *Conversation) inWorktree(ctx context.Context, thread string, p pick) {
	c.say(ctx, thread, "Making the worktree "+p.branch+" of "+p.place.Name+"…", nil)
	go func() {
		made, err := c.center.Worktree(p.place.Slug, p.branch, p.fresh)
		if err != nil {
			c.say(ctx, thread, "Could not make the worktree: "+err.Error(), nil)
			return
		}
		c.startIn(ctx, thread, made, p)
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
func (c *Conversation) startIn(ctx context.Context, thread string, at notify.Place, p pick) {
	id, err := c.center.Start(at.Slug, notify.Message{Text: p.text, Images: p.images, Via: c.p.Via()})
	if err != nil {
		c.say(ctx, thread, "Could not start it: "+err.Error(), nil)
		return
	}
	title := firstLine(p.text)
	intro := "**Started** in " + Escape(at.Name)
	switch {
	case at.Slug != p.place.Slug:
		intro += ", a new worktree of " + Escape(p.place.Name) + " on the branch `" + p.branch + "`"
	case p.worktree:
		intro += " itself: it is no git repository, so it has no worktrees"
	}
	if p.adopt != "" {
		c.adopt(ctx, p.adopt, at.Slug, id, title, at.Name)
		intro += "."
	} else {
		intro += ": " + Escape(clip(p.text, 500)) + "\n\nIts replies come here."
	}
	s, _ := c.center.Setup(at.Slug, id)
	setup := pick{session: notify.Session{Folder: at.Slug, ID: id}, intro: intro, asNew: true}
	var in string
	err = c.inThread(ctx, at.Slug, id, title, at.Name, func(thread string) error {
		in = thread
		_, err := c.p.Post(ctx, thread, Out{Text: setupText(intro, s), Buttons: c.setupRows(s, setup, false)})
		return err
	})
	if err != nil {
		return
	}
	c.follow(&flight{folder: at.Slug, session: id, ref: p.message, thread: in})
	if in != thread {
		c.say(ctx, thread, "Started, in its thread "+topicName(title, at.Name)+".", nil)
	}
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return clip(line, 60)
}
