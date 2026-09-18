// Package notify tells the reader what the agents want and have done, wherever
// the reader is: in dv's open pages at once, and through providers - a chat
// app on their phone - once they have gone a while without using dv. It knows
// nothing of Claude Code or of any app: folders raise notices and settle them,
// and providers carry them.
package notify

import (
	"errors"
	"slices"
	"sync"
	"time"
)

// The kinds of notice.
const (
	Ask  = "ask"  // an agent waits on the reader
	Done = "done" // an agent finished its turn
)

// The formats of a notice's body, besides plain text.
const (
	Code     = "code"
	Markdown = "markdown"
)

// A Notice is one thing to tell the reader.
type Notice struct {
	ID      string    `json:"id"`
	Kind    string    `json:"kind"`
	Folder  string    `json:"folder,omitempty"` // its slug in a hub
	Place   string    `json:"place"`            // the folder's name
	Session string    `json:"session"`
	Agent   string    `json:"agent,omitempty"`   // "codex"; "" is Claude Code
	Request string    `json:"request,omitempty"` // an Ask's, which its folder's page answers in a window of its own
	Title   string    `json:"title"`             // "Claude wants to run a command"
	Where   string    `json:"where"`             // the session's title
	Body    string    `json:"body,omitempty"`    // the command or question, or what the agent said last
	Format  string    `json:"format,omitempty"`  // Body's: Code, Markdown, or plain text
	Choices []string  `json:"choices,omitempty"` // answers a provider can offer
	At      time.Time `json:"at"`
	// Loud asks the pages for a desktop notification: nobody was at one as it
	// came, or it has been sent on since.
	Loud bool `json:"loud,omitempty"`

	// Answer answers with Choices[i].
	Answer func(i int) error `json:"-"`
	// Outcome is how it was settled, for a provider to say.
	Outcome string `json:"-"`

	sent bool
}

// A Provider carries notices away from dv's pages. Both calls return at once,
// doing their work in the background, in the order they were made.
type Provider interface {
	Send(n Notice)
	Settle(n Notice) // n.Outcome says how
}

// A page stops counting as on screen when it has not said so for this long:
// one open while its device sleeps may not have been told.
const pageStale = 90 * time.Second

// Center holds the notices open, and follows the reader through their pages
// to decide when to send them on.
type Center struct {
	after func() time.Duration // how long the reader may go without using dv before notices are sent on
	now   func() time.Time
	stop  chan struct{}

	defaults func() Defaults // guarded by mu

	mu        sync.Mutex
	notices   []*Notice
	outcomes  map[string]string // by notice, how it is being settled
	pages     map[string]*page
	input     time.Time // the reader's last, in any page
	providers []Provider
	watch     map[chan struct{}]bool
	folders   map[string]*Folder
	talking   map[string]bool // sessions the reader wrote to from a provider, by key, until their turn ends
	hub       Hub
}

type page struct {
	focused bool
	heard   time.Time
	streams int
}

// New starts a center. after is read each time it is needed, so a change to
// the reader's setting takes at once.
func New(after func() time.Duration) *Center {
	c := newCenter(after, time.Now)
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				c.step()
			case <-c.stop:
				return
			}
		}
	}()
	return c
}

func newCenter(after func() time.Duration, now func() time.Time) *Center {
	return &Center{
		after: after, now: now, stop: make(chan struct{}),
		outcomes: map[string]string{}, pages: map[string]*page{}, watch: map[chan struct{}]bool{},
		folders: map[string]*Folder{}, talking: map[string]bool{},
	}
}

func (c *Center) Close() { close(c.stop) }

// Add sends notices to p from now on.
func (c *Center) Add(p Provider) {
	c.mu.Lock()
	c.providers = append(c.providers, p)
	c.mu.Unlock()
}

// A Folder raises the notices of one folder's sessions, and lets providers
// talk to them.
type Folder struct {
	c          *Center
	slug, name string
	sessions   Sessions
}

// Folder is where the notices of the folder at slug come from; "" for a dv
// serving one folder.
func (c *Center) Folder(slug, name string) *Folder {
	f := &Folder{c: c, slug: slug, name: name}
	c.mu.Lock()
	c.folders[slug] = f
	c.mu.Unlock()
	return f
}

// Raise tells of n, unless it is open already.
func (f *Folder) Raise(n Notice) {
	c := f.c
	c.mu.Lock()
	if c.find(n.ID) >= 0 {
		c.mu.Unlock()
		return
	}
	now := c.now()
	n.Folder, n.Place, n.At = f.slug, f.name, now
	n.Loud = !c.watched(now)
	c.notices = append(c.notices, &n)
	c.changed()
	c.mu.Unlock()
	c.step()
}

// Settle ends a notice - answered, withdrawn or seen - saying how, unless it
// was said already by Outcome.
func (f *Folder) Settle(id, outcome string) { f.c.settle(id, outcome) }

// Outcome says how a notice is about to be settled, by an answer being given;
// "" takes that back, the answer having failed.
func (f *Folder) Outcome(id, outcome string) {
	f.c.mu.Lock()
	if outcome == "" {
		delete(f.c.outcomes, id)
	} else {
		f.c.outcomes[id] = outcome
	}
	f.c.mu.Unlock()
}

// Close settles the folder's notices, as its sessions stop with it.
func (f *Folder) Close() {
	f.c.mu.Lock()
	if f.c.folders[f.slug] == f {
		delete(f.c.folders, f.slug)
	}
	var ids []string
	for _, n := range f.c.notices {
		if n.Folder == f.slug {
			ids = append(ids, n.ID)
		}
	}
	f.c.mu.Unlock()
	for _, id := range ids {
		f.c.settle(id, "Closed in dv")
	}
}

func (c *Center) settle(id, outcome string) {
	c.mu.Lock()
	if o, ok := c.outcomes[id]; ok {
		outcome = o
		delete(c.outcomes, id)
	}
	i := c.find(id)
	if i < 0 {
		c.mu.Unlock()
		return
	}
	n := c.notices[i]
	c.notices = slices.Delete(c.notices, i, i+1)
	n.Outcome = outcome
	c.changed()
	providers := c.providers
	c.mu.Unlock()
	if n.sent {
		for _, p := range providers {
			p.Settle(*n)
		}
	}
}

// ErrGone is an answer to a notice no longer open, worded for the reader.
var ErrGone = errors.New("It is no longer waiting")

// Answer answers notice id with its choice i, for a provider the reader
// answered in, which by names. It returns the choice.
func (c *Center) Answer(id string, i int, by string) (string, error) {
	c.mu.Lock()
	var n *Notice
	if at := c.find(id); at >= 0 {
		n = c.notices[at]
	}
	if n == nil || n.Answer == nil || i < 0 || i >= len(n.Choices) {
		c.mu.Unlock()
		return "", ErrGone
	}
	choice := n.Choices[i]
	c.outcomes[id] = "Answered from " + by + ": " + choice
	c.mu.Unlock()
	if err := n.Answer(i); err != nil {
		c.mu.Lock()
		delete(c.outcomes, id)
		c.mu.Unlock()
		return "", err
	}
	c.settle(id, "")
	return choice, nil
}

// Report is what a page says of its reader: whether it has them, and, when
// input is set, that they used it ago.
func (c *Center) Report(id string, focused, input bool, ago time.Duration) {
	c.mu.Lock()
	now := c.now()
	p := c.page(id)
	p.focused, p.heard = focused, now
	var seen []string
	if at := now.Add(-ago); input && at.After(c.input) {
		c.input = at
		// A turn's end is told for the reader to see, which using dv after it
		// counts as: it was on the page they were at.
		for _, n := range c.notices {
			if n.Kind == Done && n.At.Before(at) {
				seen = append(seen, n.ID)
			}
		}
	}
	c.mu.Unlock()
	for _, id := range seen {
		c.settle(id, "")
	}
}

// Watch follows the notices for page id, whose reader counts as present while
// it is open. The channel is signalled when the notices change.
func (c *Center) Watch(id string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	c.mu.Lock()
	c.watch[ch] = true
	c.page(id).streams++
	c.mu.Unlock()
	return ch, func() {
		c.mu.Lock()
		delete(c.watch, ch)
		if p := c.pages[id]; p != nil {
			if p.streams--; p.streams <= 0 {
				delete(c.pages, id)
			}
		}
		c.mu.Unlock()
	}
}

// Notices lists those open, oldest first.
func (c *Center) Notices() []Notice {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Notice, len(c.notices))
	for i, n := range c.notices {
		out[i] = *n
	}
	return out
}

// step sends on each notice the reader has had long enough to see in a page:
// after without using dv, counted from the notice or from their last use of
// dv, whichever came later. A session the reader is talking to from a
// provider has its notices sent at once, until its turn ends.
func (c *Center) step() {
	after := c.after()
	c.mu.Lock()
	now := c.now()
	var send []Notice
	for _, n := range c.notices {
		from := n.At
		if c.input.After(from) {
			from = c.input
		}
		talking := c.talking[key(n.Folder, n.Session)]
		if !n.sent && (talking || now.Sub(from) >= after) {
			n.sent, n.Loud = true, true
			send = append(send, *n)
			if talking && n.Kind == Done {
				delete(c.talking, key(n.Folder, n.Session))
			}
		}
	}
	if len(send) > 0 {
		c.changed()
	}
	for id, p := range c.pages {
		if p.streams == 0 && now.Sub(p.heard) > pageStale {
			delete(c.pages, id) // reported, but never followed
		}
	}
	providers := c.providers
	c.mu.Unlock()
	for _, n := range send {
		for _, p := range providers {
			p.Send(n)
		}
	}
}

// watched is whether the reader has a page in front of them. Callers hold c.mu.
func (c *Center) watched(now time.Time) bool {
	for _, p := range c.pages {
		if p.focused && p.streams > 0 && now.Sub(p.heard) < pageStale {
			return true
		}
	}
	return false
}

// Callers hold c.mu.
func (c *Center) page(id string) *page {
	p := c.pages[id]
	if p == nil {
		p = &page{}
		c.pages[id] = p
	}
	return p
}

// Callers hold c.mu.
func (c *Center) find(id string) int {
	return slices.IndexFunc(c.notices, func(n *Notice) bool { return n.ID == id })
}

// Callers hold c.mu.
func (c *Center) changed() {
	for ch := range c.watch {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
