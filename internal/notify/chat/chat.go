// Package chat is dv in a chat app. The reader is told there what agents ask
// and have done, each session in a thread of its own, and can answer them,
// write to a session, start one, stop it and pick its model.
//
// An app - Telegram, Google Chat through a relay - is a Platform the
// Conversation talks through. How messages look, how threads are made and
// kept, and how a session's progress shows are the app's; the rest is the
// same in every one. Messages are written in Markdown, which each app renders
// its own way.
package chat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"dv/internal/notify"
)

// Out is a message to post.
type Out struct {
	// Text is Markdown; with Plain, words to show as they are.
	Text    string
	Plain   bool
	Buttons [][]Button
	// Private is for the session's owner alone, where others read the thread.
	Private bool
}

// Button is one under a message: a tap hands Data back, or it opens URL.
type Button struct {
	Text string `json:"text"`
	Data string `json:"data,omitempty"`
	URL  string `json:"url,omitempty"`
}

// In is a message the reader wrote.
type In struct {
	Thread  string // "" in the chat itself
	Ref     string // the app's for the message, to show progress on
	ReplyTo string // the message it answers, where the app says
	Text    string
	Images  []notify.Image
	// Shared is written where others read it too, as a Google Chat space.
	Shared bool
}

// Tap is a button the reader tapped, on the message Ref in Thread.
type Tap struct {
	Data   string
	Thread string
	Ref    string
}

// Thread is a session's in the chat: the app's id for it, and the name it was
// last given.
type Thread struct {
	ID   string
	Name string
}

// Platform is a chat app, as a Conversation talks through it.
type Platform interface {
	// Via is the app's name, as the agent and the reader are told it.
	Via() string
	// Ready is whether the app is set up, and this dv the one talking through it.
	Ready() bool
	// Origin is where links to dv go: "" for nowhere the reader can follow.
	Origin() string

	// Post sends a message in a thread, "" being the chat itself. A long one
	// may go as several; the ref returned is the last's, which has the buttons.
	Post(ctx context.Context, thread string, m Out) (string, error)
	// Edit changes a message Post sent, and Unbutton takes its buttons away.
	Edit(ctx context.Context, thread, ref string, m Out) error
	Unbutton(ctx context.Context, thread, ref string) error

	// NewThread starts a thread named name and returns its id: "" in an app
	// without threads, where everything goes in the chat itself. Rename names
	// one afresh, where threads have names; Gone is whether err says one was
	// deleted.
	NewThread(ctx context.Context, name string) (string, error)
	Rename(ctx context.Context, thread, name string) error
	Gone(err error) bool
	// The sessions' threads, kept with the app's settings by key: Thread and
	// SetThread by the session's, ThreadOf by the thread's id.
	Thread(key string) (Thread, bool)
	SetThread(key string, t Thread)
	DropThread(key string)
	ThreadOf(id string) (key string, ok bool)

	// Mark shows how far a session has got with a message the reader sent,
	// when that changes from was - Unmarked the first time. Busy shows, every
	// few seconds, that a thread's session is at work on one. Ack shows that
	// a message was acted on, and needs no other answer.
	Mark(ctx context.Context, thread, ref string, was, at notify.Progress)
	Busy(ctx context.Context, thread string)
	Ack(ctx context.Context, thread, ref string)
}

// Unmarked is a message Mark has not been called for yet.
const Unmarked notify.Progress = -1

// Conversation is dv's side of a chat, through one app.
type Conversation struct {
	center *notify.Center
	p      Platform
	jobs   chan func(context.Context)
	flew   chan struct{} // a flight added
	// Every is how often flights are looked at: a Busy lasts a few seconds.
	Every time.Duration

	threading sync.Mutex // one thread made at a time, so a session gets one

	mu      sync.Mutex
	sent    map[string]sent // notices' messages, by notice
	picks   map[string]pick // what the buttons of a list stand for, by token
	asked   map[string]pick // questions a reply answers, by message
	spent   map[string]bool // questions answered, by message
	flights []*flight
}

// sent is a notice's last message, which has its buttons.
type sent struct{ ref, thread string }

func New(center *notify.Center, p Platform) *Conversation {
	return &Conversation{
		center: center, p: p, jobs: make(chan func(context.Context), 100), flew: make(chan struct{}, 1),
		Every: 4 * time.Second, sent: map[string]sent{}, picks: map[string]pick{}, asked: map[string]pick{},
		spent: map[string]bool{},
	}
}

// Run sends notices, in turn, and shows sessions' progress, until ctx ends.
func (c *Conversation) Run(ctx context.Context) {
	go c.fly(ctx)
	for {
		select {
		case job := <-c.jobs:
			job(ctx)
		case <-ctx.Done():
			return
		}
	}
}

func (c *Conversation) do(job func(context.Context)) {
	select {
	case c.jobs <- job:
	default: // the app is not keeping up; better a notice lost than dv held up
	}
}

// Send tells of a notice in its session's thread.
func (c *Conversation) Send(n notify.Notice) {
	c.do(func(ctx context.Context) {
		if !c.p.Ready() {
			return
		}
		var m sent
		err := c.inThread(ctx, n.Folder, n.Session, n.Where, n.Place, func(thread string) (err error) {
			m.thread = thread
			m.ref, err = c.p.Post(ctx, thread, Out{Text: notice(n, thread != "", ""), Buttons: c.keyboard(n, true), Private: n.Kind == notify.Ask})
			return err
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "dv: could not send a notice to %s: %v\n", c.p.Via(), err)
			return
		}
		c.mu.Lock()
		c.sent[n.ID] = m
		c.mu.Unlock()
	})
}

// Settle says how a notice ended on its message, which loses its answers. A
// turn's end has nothing to take back.
func (c *Conversation) Settle(n notify.Notice) {
	c.do(func(ctx context.Context) {
		c.mu.Lock()
		m := c.sent[n.ID]
		delete(c.sent, n.ID)
		c.mu.Unlock()
		if m.ref == "" || n.Outcome == "" {
			return
		}
		out := Out{Text: notice(n, m.thread != "", n.Outcome), Buttons: c.keyboard(n, false), Private: n.Kind == notify.Ask}
		if err := c.p.Edit(ctx, m.thread, m.ref, out); err != nil {
			fmt.Fprintf(os.Stderr, "dv: could not update a notice in %s: %v\n", c.p.Via(), err)
		}
	})
}

// Tapped acts on a button: a choice answering a notice, or a pick from a list
// the conversation gave. It returns what to tell the reader of it, and
// whether that is of its failing.
func (c *Conversation) Tapped(ctx context.Context, t Tap) (string, bool) {
	if token, ok := strings.CutPrefix(t.Data, pickPrefix); ok {
		said := c.picked(ctx, t, token)
		return said, said != ""
	}
	id, n, _ := strings.Cut(t.Data, ":")
	i, _ := strconv.Atoi(n)
	choice, err := c.center.Answer(id, i, c.p.Via())
	if err != nil {
		// Left by a dv that has since restarted, it can only be answered in dv.
		if errors.Is(err, notify.ErrGone) && t.Ref != "" {
			c.p.Unbutton(ctx, t.Thread, t.Ref)
		}
		return err.Error(), true
	}
	return choice, false
}

// Commands are the conversation's, as an app may offer them in a menu.
var Commands = []struct{ Name, Description string }{
	{"sessions", "The sessions open in dv, to open a thread for one"},
	{"new", "Start a session, picking where: /new [folder] what to do"},
	{"stop", "Stop the turn of this thread's session"},
	{"last", "What this thread's session said last"},
	{"model", "Pick the model of this thread's session"},
	{"help", "What this bot does"},
}

// Help is what the conversation is, told on connecting and asked for.
const Help = `dv tells you here what its agents want and have done, each session in a thread of its own.

Write what a session is to do to start one, in the folder set in dv's Settings. Words before a colon say where instead: "notes: …" in the folder notes, "wt: …" in a new worktree, "notes wt fix/login: …" in one of notes on that branch, "here: …" in the folder itself.

In a session's thread, write to send it a message, pictures and all. /stop stops its turn, /last shows what it said last, and /model picks its model.

/sessions lists the sessions open in dv, to open a thread for one, and /new starts one, asking where.`

// Said acts on a message: a command, words for the session whose thread they
// are in, or the start of a new session.
func (c *Conversation) Said(ctx context.Context, in In) {
	folder, session, isSession := c.sessionOf(in.Thread)
	here := func(s string) { c.tell(ctx, in.Thread, s, nil) }
	command, args := parseCommand(in.Text)
	c.mu.Lock()
	var asked pick
	var answering bool
	if in.ReplyTo != "" {
		asked, answering = c.asked[in.ReplyTo]
		delete(c.asked, in.ReplyTo)
	}
	c.mu.Unlock()
	switch {
	case answering && command == "" && in.Text != "":
		// The branch for a new worktree, named by the reader.
		branch := strings.TrimSpace(in.Text)
		if !c.answered(ctx, in.Thread, in.ReplyTo, asked.question, branch) {
			here("That is answered already.")
			break
		}
		asked.branch, asked.fresh = branch, false
		c.inWorktree(ctx, in.Thread, asked)
	case command == "start" || command == "help":
		here(Help)
	case command == "sessions":
		c.listSessions(ctx, in.Thread)
	case command == "new" && in.Shared:
		// Where others read along, a session starts from what it is to do alone,
		// without asking where in front of them.
		here("Here, mention me with what the session is to do, and it starts in this thread. /new is for direct messages.")
	case command == "new":
		if args != "" && isSession && !c.namesPlace(args) {
			// In a session's thread, a folder unnamed is the session's own.
			c.whereTo(ctx, in.Thread, pick{place: c.place(folder), text: args, message: in.Ref})
			break
		}
		p := pick{text: args, message: in.Ref}
		if !isSession {
			p.adopt = in.Thread
		}
		c.newSession(ctx, in.Thread, p)
	case !isSession && strings.HasPrefix(in.Text, "/"):
		here("That is for a session's thread. /sessions opens one for a session.")
	case !isSession:
		c.quickStart(ctx, in)
	case command == "stop":
		if err := c.center.Stop(folder, session); err != nil {
			here(err.Error())
		} else {
			c.p.Ack(ctx, in.Thread, in.Ref)
		}
	case command == "last":
		c.showLast(ctx, in.Thread, folder, session)
	case command == "model":
		c.askModel(ctx, in.Thread, folder, session)
	default:
		// Any other command is the agent's: /compact, say.
		id, err := c.center.Send(folder, session, notify.Message{Text: in.Text, Images: in.Images, Via: c.p.Via()})
		if err != nil {
			here(err.Error())
			return
		}
		c.follow(&flight{folder: folder, session: session, id: id, ref: in.Ref, thread: in.Thread})
	}
}

// parseCommand splits "/new@dv_bot fix it" into "new" and "fix it"; words
// that are no command give "".
func parseCommand(text string) (string, string) {
	if !strings.HasPrefix(text, "/") {
		return "", ""
	}
	word, args, _ := strings.Cut(text[1:], " ")
	word, _, _ = strings.Cut(word, "@")
	for _, c := range Commands {
		if c.Name == word {
			return word, strings.TrimSpace(args)
		}
	}
	if word == "start" {
		return word, ""
	}
	return "", ""
}

// say sends plain words, in a thread or, with "", the chat itself.
func (c *Conversation) say(ctx context.Context, thread, s string, rows [][]Button) {
	c.p.Post(ctx, thread, Out{Text: s, Plain: true, Buttons: rows})
}

// tell is say for the reader alone, where others read the thread: a note, a
// list or a question only they can act on.
func (c *Conversation) tell(ctx context.Context, thread, s string, rows [][]Button) {
	c.p.Post(ctx, thread, Out{Text: s, Plain: true, Buttons: rows, Private: true})
}
