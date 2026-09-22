package notify

import (
	"cmp"
	"errors"
	"slices"
	"time"
)

// Sessions is what a folder lets the reader do with its sessions from a
// provider: see them, write to them, start one, stop one, pick its model.
type Sessions interface {
	List() []Session
	// Send writes to a session, returning the id the message goes by.
	Send(session string, m Message) (string, error)
	Progress(session, message string) Progress
	// Start begins a session with its first message, on the model last picked
	// for a new one.
	Start(m Message) (string, error)
	Stop(session string) error
	// Reply is what the agent said last, "" if nothing since the reader did.
	Reply(session string) string
	Setup(session string) Setup
	// Configure picks a session's model or effort, nil leaving it be. asNew
	// has new sessions start on them too.
	Configure(session string, model, effort *string, asNew bool) error
}

// Progress is how far a session has got with a message sent to it.
type Progress int

const (
	Queued   Progress = iota // waiting for the step the agent is on
	Working                  // taken up, in the turn going on
	Asking                   // that turn waiting on the reader
	Finished                 // that turn over
)

// Setup is what a session runs on, and what else it could.
type Setup struct {
	Agent  string // "codex"; "" is Claude Code
	Model  string // an ID of Models'; "" is the agent's own
	Effort string // "" is the model's own
	Models []Model
}

// Model is one an agent offers, running at Effort unless another is picked.
type Model struct {
	ID, Label, Effort string
	Efforts           []string
}

// Session is one a folder has open, as a provider lists it.
type Session struct {
	ID      string
	Folder  string // its slug in a hub
	Place   string // the folder's name
	Title   string
	Agent   string // "codex"; "" is Claude Code
	Running string // "dv", "terminal", or "" when nothing runs it
	Busy    bool
	Asking  bool // waiting on the reader
	Updated time.Time
}

// Message is what the reader writes to a session from a provider.
type Message struct {
	Text   string
	Images []Image
	Files  []File // saved into the session's folder, for the agent to read there
	Via    string // the chat app, as the agent is told it: "Telegram"
}

// Image is a picture sent to a session, of a media Type such as "image/png".
type Image struct {
	Type string
	Data []byte
}

// File is any other file sent to a session, by the name it was sent with.
type File struct {
	Name string
	Data []byte
}

// Place is a folder a session can be started in. Git marks one a worktree can
// be made of; Task one of a hub's one-off tasks, deleted when closed.
type Place struct {
	Slug, Name string
	Git, Task  bool
}

// A Hub serves many folders, opening one when asked, and makes worktrees:
// what lets the reader start a session from elsewhere in any of its folders,
// or in a new worktree of one.
type Hub interface {
	Places() []Place
	Open(slug string) error
	// Worktree makes a worktree of the repository slug is in, on a new branch
	// from what slug has checked out, and returns once it is set up, as the
	// folder it is. fresh numbers a branch name taken on, as for one made up.
	Worktree(slug, branch string, fresh bool) (Place, error)
	// NewTask makes a folder for a one-off task; CloseTask deletes one, its
	// sessions stopped, and returns once it is gone.
	NewTask() (Place, error)
	CloseTask(slug string) error
}

// errNoHub is what a dv serving one folder says to what only a hub does.
func errNoHub(what string) error {
	return errors.New(what + " by a dv hub, and this dv is not one")
}

// Tasks is whether one-off tasks can be made here, which a hub does.
func (c *Center) Tasks() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hub != nil
}

// NewTask makes a folder for a one-off task, as the hub does.
func (c *Center) NewTask() (Place, error) {
	c.mu.Lock()
	h := c.hub
	c.mu.Unlock()
	if h == nil {
		return Place{}, errNoHub("Tasks are made")
	}
	return h.NewTask()
}

// CloseTask deletes a task's folder, as the hub does.
func (c *Center) CloseTask(slug string) error {
	c.mu.Lock()
	h := c.hub
	c.mu.Unlock()
	if h == nil {
		return errNoHub("Tasks are closed")
	}
	return h.CloseTask(slug)
}

// SetHub has sessions started in any of h's folders, opened for them.
func (c *Center) SetHub(h Hub) {
	c.mu.Lock()
	c.hub = h
	c.mu.Unlock()
}

// Worktree makes a worktree for a session, as the hub does.
func (c *Center) Worktree(folder, branch string, fresh bool) (Place, error) {
	c.mu.Lock()
	h := c.hub
	c.mu.Unlock()
	if h == nil {
		return Place{}, errNoHub("Worktrees are made")
	}
	return h.Worktree(folder, branch, fresh)
}

// Serve lets providers talk to the folder's sessions through s.
func (f *Folder) Serve(s Sessions) {
	f.c.mu.Lock()
	f.sessions = s
	f.c.mu.Unlock()
}

// ErrNoFolder is a folder that is not open in dv, or no longer.
var ErrNoFolder = errors.New("That folder is not open in dv")

// Places are the folders sessions can be started in: a hub's, or those open.
func (c *Center) Places() []Place {
	c.mu.Lock()
	h := c.hub
	c.mu.Unlock()
	if h != nil {
		out := h.Places()
		slices.SortFunc(out, func(a, b Place) int { return cmp.Compare(a.Name, b.Name) })
		return out
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []Place
	for _, f := range c.folders {
		if f.sessions != nil {
			out = append(out, Place{Slug: f.slug, Name: f.name})
		}
	}
	slices.SortFunc(out, func(a, b Place) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// Sessions lists the open folders' sessions, the latest first.
func (c *Center) Sessions() []Session {
	var out []Session
	for _, f := range c.served() {
		for _, s := range f.sessions.List() {
			s.Folder, s.Place = f.slug, f.name
			out = append(out, s)
		}
	}
	slices.SortStableFunc(out, func(a, b Session) int { return b.Updated.Compare(a.Updated) })
	return out
}

// Send writes to a session, whose notices then come to the reader at once
// until its turn ends: they are waiting on it.
func (c *Center) Send(folder, session string, m Message) (string, error) {
	s, err := c.sessionsOf(folder)
	if err != nil {
		return "", err
	}
	id, err := s.Send(session, m)
	if err != nil {
		return "", err
	}
	c.talk(folder, session)
	return id, nil
}

// Progress is how far a session has got with a message Send gave it. A folder
// let go since has nothing going on.
func (c *Center) Progress(folder, session, message string) Progress {
	var s Sessions
	c.mu.Lock()
	if f := c.folders[folder]; f != nil {
		s = f.sessions
	}
	c.mu.Unlock()
	if s == nil {
		return Finished
	}
	return s.Progress(session, message)
}

func (c *Center) Setup(folder, session string) (Setup, error) {
	s, err := c.sessionsOf(folder)
	if err != nil {
		return Setup{}, err
	}
	return s.Setup(session), nil
}

func (c *Center) Configure(folder, session string, model, effort *string, asNew bool) error {
	s, err := c.sessionsOf(folder)
	if err != nil {
		return err
	}
	return s.Configure(session, model, effort, asNew)
}

// Start begins a session in folder with text, which is then talked to as by
// Send.
func (c *Center) Start(folder string, m Message) (string, error) {
	s, err := c.sessionsOf(folder)
	if err != nil {
		return "", err
	}
	id, err := s.Start(m)
	if err == nil {
		c.talk(folder, id)
	}
	return id, err
}

// Stop ends the turn a session is taking.
func (c *Center) Stop(folder, session string) error {
	s, err := c.sessionsOf(folder)
	if err != nil {
		return err
	}
	return s.Stop(session)
}

// Reply is what the agent said last in a session.
func (c *Center) Reply(folder, session string) (string, error) {
	s, err := c.sessionsOf(folder)
	if err != nil {
		return "", err
	}
	return s.Reply(session), nil
}

func (c *Center) talk(folder, session string) {
	c.mu.Lock()
	c.talking[key(folder, session)] = true
	c.mu.Unlock()
}

// sessionsOf is the folder's sessions. A hub's folder not open - let go while
// nothing went on in it - is opened for them.
func (c *Center) sessionsOf(folder string) (Sessions, error) {
	c.mu.Lock()
	f, h := c.folders[folder], c.hub
	c.mu.Unlock()
	if f != nil && f.sessions != nil {
		return f.sessions, nil
	}
	if h == nil {
		return nil, ErrNoFolder
	}
	if err := h.Open(folder); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if f := c.folders[folder]; f != nil && f.sessions != nil {
		return f.sessions, nil
	}
	return nil, ErrNoFolder
}

func (c *Center) served() []*Folder {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*Folder
	for _, f := range c.folders {
		if f.sessions != nil {
			out = append(out, f)
		}
	}
	return out
}

func key(folder, session string) string { return folder + "/" + session }
