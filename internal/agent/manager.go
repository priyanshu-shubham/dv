package agent

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"maps"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"dv/internal/codex"
	"dv/internal/permit"
	"dv/internal/store"
)

// Manager is the Agent view's side of the server: the sessions it lists, the
// ones dv runs, and the transcripts open pages are following. Codex's sessions
// are its codex side's; the rest of it is Claude Code's.
type Manager struct {
	root   string
	broker *permit.Broker
	saved  *store.Sessions // which are open, and rewinds still to be made real
	codex  *codexSide
	// Suggests says whether Claude sessions guess the next message after a
	// turn, a setting a session takes up the next time it starts. Set once,
	// before the first session starts.
	Suggests func() bool

	mu      sync.Mutex
	procs   map[string]*proc
	follows map[string]*follow
	rows    map[string]cachedRow // by transcript path
	agents  map[string]string    // sessions whose agent has been worked out
	shells  map[string]*shellRun // commands run with !, by session
	live    map[string]running
	liveAt  time.Time
	opts    Options
	optsAt  time.Time
	probing bool
	// When Claude Code's settings files last changed, as last looked.
	settingsAt, settingsLooked time.Time

	usage      *Usage
	usageAt    time.Time
	usageAsked bool
}

type cachedRow struct {
	size int64
	mod  time.Time
	row  Session
	ok   bool
}

func New(root string, broker *permit.Broker, saved *store.Sessions) *Manager {
	m := &Manager{
		root: root, broker: broker, saved: saved, codex: newCodexSide(root, broker, saved),
		procs: map[string]*proc{}, follows: map[string]*follow{}, rows: map[string]cachedRow{}, agents: map[string]string{},
		shells: map[string]*shellRun{},
	}
	// A session started here but not yet sent to has no transcript, so all
	// that says it exists is its place among the open ones.
	if ids := saved.IDs(); len(ids) > 0 {
		files := transcriptFiles(root)
		for _, id := range ids {
			if saved.Agent(id) == "codex" {
				m.codex.thread(id)
			} else if _, ok := files[id]; !ok {
				m.procs[id] = m.newProc(id, root, false)
			}
		}
	}
	broker.SetOwned(m.runs)
	go m.reap()
	return m
}

// Available reports whether there is a Claude Code to run.
func Available() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

// CodexAvailable reports whether there is a Codex to run.
func CodexAvailable() bool { return codex.Available() }

// isCodex reports whether a session is Codex's rather than Claude Code's.
func (m *Manager) isCodex(id string) bool {
	m.mu.Lock()
	agent, known := m.agents[id]
	_, claude := m.procs[id]
	m.mu.Unlock()
	if known {
		return agent == "codex"
	}
	switch {
	case claude:
		return false
	case m.saved.Agent(id) != "":
		agent = m.saved.Agent(id)
	default:
		if _, ok := transcriptFiles(m.root)[id]; !ok {
			mine, sure := m.codex.has(id)
			if !sure {
				return mine
			}
			if mine {
				agent = "codex"
			}
		}
	}
	m.mu.Lock()
	m.agents[id] = agent
	m.mu.Unlock()
	return agent == "codex"
}

// Visible reports whether a session's permission prompts belong in the page:
// it is open in the Agent view, or dv runs it.
func (m *Manager) Visible(session string) bool {
	return m.runs(session) || m.saved.Has(session)
}

func (m *Manager) runs(session string) bool {
	if m.codex.running(session) {
		return true
	}
	m.mu.Lock()
	p := m.procs[session]
	m.mu.Unlock()
	if p == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cmd != nil
}

// Sessions lists the conversations started in the repository: those open in
// dv first, then the rest newest first.
func (m *Manager) Sessions() []Session {
	files := transcriptFiles(m.root)
	live := m.running()
	opts := m.Options()
	opened := map[string]int{} // 1 for the latest
	for i, id := range m.saved.IDs() {
		opened[id] = i + 1
	}
	temporary, kept := m.saved.TemporaryIDs(), m.saved.KeptIDs()
	m.mu.Lock()
	procs := make(map[string]*proc, len(m.procs))
	for id, p := range m.procs {
		procs[id] = p
	}
	m.mu.Unlock()

	out := []Session{}
	for id, path := range files {
		row, ok := m.row(path)
		if !ok || !within(row.Cwd, m.root) {
			continue
		}
		row.ID, row.Open = id, opened[id] > 0
		row.Running, row.Busy = where(procs[id], live[id])
		row.Temporary, row.Kept = slices.Contains(temporary, id), slices.Contains(kept, id)
		if row.Temporary && !row.Open && row.Running == "" {
			delete(procs, id)
			continue
		}
		row.Status = statusOf(procs[id])
		// Rewound, the file ends on the branch left behind until the next message.
		if r, ok := m.saved.Rewound(id); ok && !r.Sent {
			row.Last, row.LastBy = "", ""
		}
		if c := windowFor(row.model, opts); row.used > 0 && c.Max > 0 {
			if p := procs[id]; p != nil {
				p.mu.Lock()
				if p.cmd != nil && p.context != nil {
					c = *p.context
				}
				p.mu.Unlock()
			}
			c.Used = row.used
			row.Context = &c
		}
		out = append(out, row)
		delete(procs, id)
	}
	// Started here, with nothing said yet.
	for id, p := range procs {
		p.mu.Lock()
		row := Session{ID: id, Title: p.title, Cwd: p.cwd, Updated: p.lastUsed, Open: opened[id] > 0, Temporary: slices.Contains(temporary, id), Kept: slices.Contains(kept, id)}
		p.mu.Unlock()
		row.Running, row.Busy = where(p, running{})
		row.Status = statusOf(p)
		out = append(out, row)
	}
	for _, row := range m.codex.sessions() {
		row.Open, row.Temporary, row.Kept = opened[row.ID] > 0, slices.Contains(temporary, row.ID), slices.Contains(kept, row.ID)
		if row.Temporary && !row.Open && row.Running == "" {
			continue
		}
		m.mu.Lock()
		m.agents[row.ID] = "codex"
		m.mu.Unlock()
		out = append(out, row)
	}
	// Open sessions keep the order they were opened in, or two at work would
	// trade places each time one wrote.
	slices.SortFunc(out, func(a, b Session) int {
		if ra, rb := opened[a.ID], opened[b.ID]; ra > 0 || rb > 0 {
			return cmp.Compare(cmp.Or(ra, math.MaxInt), cmp.Or(rb, math.MaxInt))
		}
		// By id where they were written at the same moment: the files come in
		// no order, so two alike would trade places on every listing.
		return cmp.Or(b.Updated.Compare(a.Updated), cmp.Compare(a.ID, b.ID))
	})
	asks := m.saved.AskIDs()
	return slices.DeleteFunc(out, func(s Session) bool { return slices.Contains(asks, s.ID) })
}

// Activity is what a session open in dv is doing, for word of it to reach the
// reader wherever in the page, or out of it, they are.
type Activity struct {
	ID      string    `json:"id"`
	Agent   string    `json:"agent,omitempty"` // "codex"; "" is Claude Code
	Title   string    `json:"title,omitempty"`
	Running string    `json:"running,omitempty"`
	Busy    bool      `json:"busy,omitempty"`
	Last    string    `json:"last,omitempty"` // Claude's, when it spoke last
	Updated time.Time `json:"updated"`
}

// Activity lists the sessions open in dv or run by it, newest first as the
// session list has them.
func (m *Manager) Activity() []Activity {
	live := m.running()
	m.mu.Lock()
	procs := maps.Clone(m.procs)
	m.mu.Unlock()
	ids := m.saved.IDs()
	for id := range procs {
		if m.runs(id) && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	files := transcriptFiles(m.root)
	out := m.codex.activity(ids)
	for _, id := range ids {
		if m.saved.Agent(id) == "codex" {
			continue
		}
		a := Activity{ID: id}
		a.Running, a.Busy = where(procs[id], live[id])
		if path := files[id]; path != "" {
			if row, ok := m.row(path); ok {
				a.Title, a.Updated = cmp.Or(row.Title, row.Prompt), row.Updated
				if row.LastBy != "you" {
					a.Last = row.Last
				}
			}
		}
		if p := procs[id]; a.Title == "" && p != nil {
			p.mu.Lock()
			a.Title = p.title
			p.mu.Unlock()
		}
		out = append(out, a)
	}
	slices.SortStableFunc(out, func(a, b Activity) int {
		return cmp.Or(b.Updated.Compare(a.Updated), cmp.Compare(a.ID, b.ID))
	})
	// An ask is followed in its panel alone: no notices, no chat, no count at work.
	asks := m.saved.AskIDs()
	return slices.DeleteFunc(out, func(a Activity) bool { return slices.Contains(asks, a.ID) })
}

// Title is what the session list calls a session, "" for one nothing has been
// said in.
func (m *Manager) Title(id string) string {
	if m.isCodex(id) {
		return m.codex.title(id)
	}
	path := transcriptFiles(m.root)[id]
	if path == "" {
		return ""
	}
	row, _ := m.row(path)
	return cmp.Or(row.Title, row.Prompt)
}

// Reply is what the agent said last in a session, whole and as written - the
// Markdown of it - where Activity has the start of it on one line.
func (m *Manager) Reply(id string) string {
	if m.isCodex(id) {
		return m.codex.reply(id)
	}
	path := transcriptFiles(m.root)[id]
	if path == "" {
		return ""
	}
	return lastReply(path)
}

// where says who has a session open: dv, a terminal, or nobody.
func where(p *proc, r running) (string, bool) {
	if p != nil {
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.cmd != nil {
			return "dv", p.busy
		}
	}
	if r.SessionID != "" {
		return "terminal", r.Status == "busy"
	}
	return "", false
}

// statusOf is what a process dv runs last said it is doing, when that is more
// than working: "compacting". A terminal does not say.
func statusOf(p *proc) string {
	if p == nil {
		return ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd == nil || p.status != "compacting" {
		return ""
	}
	return p.status
}

func (m *Manager) row(path string) (Session, bool) {
	fi, err := os.Stat(path)
	if err != nil {
		return Session{}, false
	}
	m.mu.Lock()
	c, hit := m.rows[path]
	m.mu.Unlock()
	if hit && c.size == fi.Size() && c.mod.Equal(fi.ModTime()) {
		return c.row, c.ok
	}
	row, ok := summarize(path)
	m.mu.Lock()
	m.rows[path] = cachedRow{fi.Size(), fi.ModTime(), row, ok}
	m.mu.Unlock()
	return row, ok
}

// running is runningSessions less often: every page listing and following
// asks, and the answer is a directory of files to read.
func (m *Manager) running() map[string]running {
	m.mu.Lock()
	defer m.mu.Unlock()
	if time.Since(m.liveAt) > time.Second {
		m.live, m.liveAt = runningSessions(), time.Now()
		// dv's own processes list themselves too; those are dv's, not a terminal's.
		for id, p := range m.procs {
			p.mu.Lock()
			if p.cmd != nil && p.cmd.Process != nil && m.live[id].PID == p.cmd.Process.Pid {
				delete(m.live, id)
			}
			p.mu.Unlock()
		}
	}
	return m.live
}

// Create starts a new session in the repository with agent, "codex" or Claude
// Code's "". Nothing runs until the first message is sent.
func (m *Manager) Create(agent string) (string, error) { return m.create(agent, false) }

// CreateAsk is Create for the Ask panel: marked before anything is told of
// it, so it is never listed, and never open, being the panel's alone.
func (m *Manager) CreateAsk(agent string) (string, error) { return m.create(agent, true) }

func (m *Manager) create(agent string, ask bool) (string, error) {
	var id string
	if agent == "codex" {
		var err error
		if id, err = m.codex.create(); err != nil {
			return "", err
		}
	} else {
		id = newUUID()
	}
	m.mu.Lock()
	if agent == "codex" {
		m.agents[id] = "codex"
	} else {
		m.procs[id] = m.newProc(id, m.root, false)
	}
	m.mu.Unlock()
	if ask {
		if err := m.saved.SetAsk(id, true); err != nil {
			return "", err
		}
	} else if _, err := m.saved.Set(id, true); err != nil {
		return "", err
	}
	m.broker.Notify()
	return id, nil
}

// SetAsk marks a session as the Ask panel's, left out of the list and of
// what is told of sessions at work; off, it is an ordinary session again.
func (m *Manager) SetAsk(id string, on bool) error {
	if err := m.saved.SetAsk(id, on); err != nil {
		return err
	}
	m.broker.Notify()
	return nil
}

// SetTemporary marks a session to leave the list once it is closed. Its
// transcript stays, for the terminal's --resume. Marking one opens it: a past
// session, closed already, would leave the list while it is being looked at.
func (m *Manager) SetTemporary(id string, on bool) error {
	if on {
		if _, err := m.saved.Set(id, true); err != nil {
			return err
		}
	}
	if err := m.saved.SetTemporary(id, on); err != nil {
		return err
	}
	m.broker.Notify()
	return nil
}

// SetKept keeps a session running: it is not stopped for being idle, and
// starts with dv. Keeping one opens and starts it.
func (m *Manager) SetKept(id string, on bool) error {
	if on {
		if _, err := m.saved.Set(id, true); err != nil {
			return err
		}
	}
	if err := m.saved.SetKept(id, on); err != nil {
		return err
	}
	if on {
		return m.Start(id)
	}
	m.broker.Notify()
	return nil
}

// StartKept starts the sessions kept running, as dv starts. One a terminal
// has open is the terminal's, and is left to it.
func (m *Manager) StartKept() {
	for _, id := range m.saved.KeptIDs() {
		m.Start(id)
	}
}

// SetOpen opens a session in the Agent view or closes it. Closing one dv runs
// stops it, and it is kept running no longer; the session stays on disk.
func (m *Manager) SetOpen(id string, open bool) error {
	if _, err := m.saved.Set(id, open); err != nil {
		return err
	}
	if !open {
		if err := m.saved.SetKept(id, false); err != nil {
			return err
		}
	}
	if !open && m.isCodex(id) {
		m.codex.thread(id).release()
	} else if !open {
		m.mu.Lock()
		p := m.procs[id]
		delete(m.procs, id)
		m.mu.Unlock()
		if p != nil {
			p.stop()
		}
	}
	m.broker.Notify()
	return nil
}

// Start runs a session ahead of a message, as looking at one does: stopped
// while idle, it has no process for other sessions to send to. Codex has no
// such messages to wait for.
func (m *Manager) Start(id string) error {
	if m.isCodex(id) {
		return nil
	}
	p, err := m.procFor(id)
	if err != nil {
		return err
	}
	p.mu.Lock()
	err = p.startLocked()
	// Idle from the look, so it is not stopped again straight away.
	p.lastUsed = time.Now()
	p.mu.Unlock()
	m.broker.Notify()
	return err
}

func (m *Manager) newProc(id, cwd string, resume bool) *proc {
	p := &proc{id: id, cwd: cwd, broker: m.broker, resume: resume, lastUsed: time.Now()}
	// A resume starts on the user's settings, not what the session last ran with.
	pick := m.saved.Picked(id)
	p.model, p.effort = pick.Model, pick.Effort
	if r, ok := m.saved.Rewound(id); ok && !r.Sent {
		p.resumeAt = r.At
	}
	p.changed = func() { m.signal(id, false) }
	p.wrote = func() { m.signal(id, true) }
	p.named = func() string { return agentName(transcriptFiles(m.root)[id]) }
	p.suggests = func() bool { return m.Suggests != nil && m.Suggests() }
	p.picked = func(model, effort string) {
		p.mu.Lock()
		p.model, p.effort = model, effort
		p.mu.Unlock()
		m.saved.SetPicked(id, store.Pick{Model: model, Effort: effort})
	}
	p.fellBack = func(at string, sw store.ModelSwitch) { m.markModel(id, at, sw) }
	p.ended = func() {
		// A turn spends from the plan; the next look should show it.
		m.mu.Lock()
		m.usageAt = time.Time{}
		m.mu.Unlock()
	}
	p.sent = func() {
		// The page keeps showing the rewound conversation until the file
		// moves on; a start after this must not rewind it again.
		if r, ok := m.saved.Rewound(id); ok && !r.Sent {
			if r.Last == "" {
				m.saved.SetRewound(id, nil)
			} else {
				r.Sent = true
				m.saved.SetRewound(id, &r)
			}
		}
	}
	return p
}

// procFor is the process for a session, made ready to start: a session only
// on disk is resumed, and one a terminal has open is refused.
func (m *Manager) procFor(id string) (*proc, error) {
	m.mu.Lock()
	p := m.procs[id]
	m.mu.Unlock()
	if p != nil {
		return p, nil
	}
	path := transcriptFiles(m.root)[id]
	row, ok := m.row(path)
	if !ok || !within(row.Cwd, m.root) {
		return nil, fmt.Errorf("no session %s in this repository", id)
	}
	if _, elsewhere := m.running()[id]; elsewhere {
		return nil, errors.New("this session is open in a terminal; dv follows it, but only the terminal can talk to it")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if p = m.procs[id]; p == nil {
		// Resumed where it was started: Claude Code finds a transcript by the
		// working directory's folder.
		p = m.newProc(id, row.Cwd, true)
		// In the mode the page shows it in: the transcript's last, or a switch since.
		if f := m.follows[id]; f != nil {
			f.mu.Lock()
			last := f.t.Last()
			f.mu.Unlock()
			leaf, switches := m.leaf(id, last), m.saved.Switches(id)
			f.mu.Lock()
			p.mode = f.t.Mode(leaf, switches)
			f.mu.Unlock()
		}
		m.procs[id] = p
	}
	return p, nil
}

// Send gives a session a message, starting it if it is not running, and
// returns the uuid the message is written under: message, or a new one.
func (m *Manager) Send(id, message, text string, images []Image) (string, error) {
	m.awaitShell(id)
	return m.send(id, message, text, images)
}

func (m *Manager) send(id, message, text string, images []Image) (string, error) {
	// Claude Code would go on in a new session, and nothing more would come to this one.
	if f := strings.Fields(text); len(f) > 0 && f[0] == "/clear" {
		return "", errors.New("/clear would move Claude Code to a session dv is not showing. + starts a new one.")
	}
	// Open, for its prompts to reach the page; the Ask panel's is never open,
	// but dv runs it, which is as good while it has anything to ask.
	open := func() error {
		if slices.Contains(m.saved.AskIDs(), id) {
			return nil
		}
		_, err := m.saved.Set(id, true)
		return err
	}
	if m.isCodex(id) {
		if err := open(); err != nil {
			return "", err
		}
		m.saved.SetAgent(id, "codex")
		return m.codex.thread(id).send(cmp.Or(message, newUUID()), text, images)
	}
	p, err := m.procFor(id)
	if err != nil {
		return "", err
	}
	if err := open(); err != nil {
		return "", err
	}
	return p.send(cmp.Or(message, newUUID()), text, images)
}

// Progress is how far a session has got with a message sent to it: queued
// behind the step the agent is on, or taken up while it is still busy.
func (m *Manager) Progress(id, message string) (queued, busy bool) {
	if m.isCodex(id) {
		return m.codex.thread(id).progress(message)
	}
	m.mu.Lock()
	p := m.procs[id]
	m.mu.Unlock()
	if p == nil {
		return false, false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.ContainsFunc(p.queued, func(q Queued) bool { return q.UUID == message }), p.busy
}

// AgentOf is who runs a session: "codex", or "" for Claude Code.
func (m *Manager) AgentOf(id string) string {
	if m.isCodex(id) {
		return "codex"
	}
	return ""
}

// Picked is the model and effort asked for a session, "" for the agent's own.
func (m *Manager) Picked(id string) (model, effort string) {
	if m.isCodex(id) {
		return m.codex.thread(id).picked()
	}
	m.mu.Lock()
	p := m.procs[id]
	m.mu.Unlock()
	if p == nil {
		return "", ""
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.model, p.effort
}

// Unqueue takes back a message still waiting for the step Claude is on, as Up
// does in the terminal. It reports false once a turn has taken the message up.
func (m *Manager) Unqueue(id, message string) (bool, error) {
	if m.isCodex(id) {
		return m.codex.thread(id).unqueue(message), nil
	}
	m.mu.Lock()
	p := m.procs[id]
	m.mu.Unlock()
	if p == nil {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	body, err := p.request(ctx, map[string]any{"subtype": "cancel_async_message", "message_uuid": message})
	if err != nil {
		return false, err
	}
	var r struct {
		Cancelled bool `json:"cancelled"`
	}
	json.Unmarshal(body, &r)
	if r.Cancelled {
		p.unqueue(message)
	}
	return r.Cancelled, nil
}

// Interrupt stops what the session is doing, as Esc does in the terminal.
func (m *Manager) Interrupt(id string) error {
	// Esc first stops a command run with !, which the agent has not been sent.
	if m.stopShell(id) {
		return nil
	}
	if m.isCodex(id) {
		return m.codex.thread(id).interrupt()
	}
	m.mu.Lock()
	p := m.procs[id]
	m.mu.Unlock()
	if p == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := p.request(ctx, map[string]any{"subtype": "interrupt"})
	return err
}

// Configure sets the model, permission mode or effort, for the process running
// now and the next one started.
func (m *Manager) Configure(id string, model, mode, effort *string) error {
	if m.isCodex(id) {
		return m.codex.thread(id).configure(model, mode, effort)
	}
	p, err := m.procFor(id)
	if err != nil {
		return err
	}
	opts := m.Options()
	p.mu.Lock()
	if model != nil {
		p.model = *model
		// An effort the new model does not take would fail its next start.
		if i := slices.IndexFunc(opts.Models, func(o Model) bool { return o.ID == *model }); i >= 0 && !slices.Contains(opts.Models[i].Efforts, p.effort) {
			p.effort = ""
		}
	}
	switched := mode != nil && *mode != p.mode
	if mode != nil {
		p.mode = *mode
	}
	if effort != nil {
		p.effort = *effort
	}
	live := p.cmd != nil
	pick := store.Pick{Model: p.model, Effort: p.effort}
	p.mu.Unlock()
	if err := m.saved.SetPicked(id, pick); err != nil {
		return err
	}
	if switched {
		m.markSwitch(id, *mode)
	}
	if model != nil {
		m.markModel(id, "", store.ModelSwitch{To: *model})
	}
	if live {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if model != nil {
			// Claude Code's "default" is its recommendation, not the model the
			// user's settings start on, which is what "" means here.
			if _, err := p.request(ctx, map[string]any{"subtype": "set_model", "model": cmp.Or(*model, m.Options().Model, "default")}); err != nil {
				return err
			}
		}
		if mode != nil {
			if _, err := p.request(ctx, map[string]any{"subtype": "set_permission_mode", "mode": *mode}); err != nil {
				return err
			}
		}
		if effort != nil {
			settings := map[string]any{"effortLevel": *effort}
			if _, err := p.request(ctx, map[string]any{"subtype": "apply_flag_settings", "settings": settings}); err != nil {
				return err
			}
		}
		// A model has its own effort, so either change can move it.
		go p.refresh()
	}
	p.changed()
	return nil
}

// markSwitch keeps where in the conversation the mode was changed, which the
// transcript says nothing of until the next message. Before the first there is
// nowhere to mark: the session starts in the mode.
func (m *Manager) markSwitch(id, to string) {
	m.mark(id, "", store.Switch{To: to})
}

// markModel keeps a change of model where it was made: after the message
// at names, as a refused one, or where the conversation has got to.
func (m *Manager) markModel(id, at string, sw store.ModelSwitch) {
	m.mark(id, at, store.Switch{Model: &sw})
}

func (m *Manager) mark(id, at string, sw store.Switch) {
	m.mu.Lock()
	f := m.follows[id]
	m.mu.Unlock()
	if f == nil {
		return
	}
	if at == "" {
		f.mu.Lock()
		last := f.t.Last()
		f.mu.Unlock()
		if last == "" {
			return
		}
		at = cmp.Or(m.leaf(id, last), last)
	}
	sw.After = at
	if m.saved.AddSwitch(id, sw) != nil {
		return
	}
	f.mu.Lock()
	f.version++
	f.mu.Unlock()
}

// Rename titles a session, as /rename does in the terminal. A terminal that
// has the session open keeps its own title, so it is left to rename it.
func (m *Manager) Rename(id, title string) error {
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		return errors.New("a session needs a name")
	}
	if m.isCodex(id) {
		return m.codex.thread(id).rename(title)
	}
	if _, elsewhere := m.running()[id]; elsewhere {
		return errors.New("this session is open in a terminal; rename it there with /rename")
	}
	m.mu.Lock()
	p := m.procs[id]
	m.mu.Unlock()
	if p != nil {
		p.mu.Lock()
		live := p.cmd != nil
		p.title = title
		p.mu.Unlock()
		if live {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := p.request(ctx, map[string]any{"subtype": "rename_session", "title": title, "source": "host", "session_id": id})
			return err
		}
	}
	path := transcriptFiles(m.root)[id]
	if path == "" {
		if p != nil {
			return nil // it is named as it starts
		}
		return fmt.Errorf("no session %s in this repository", id)
	}
	return writeTitle(path, id, title)
}

// writeTitle names a session nothing is running, as Claude Code's /rename
// does: its title, and the name other sessions send to.
func writeTitle(path, id, title string) error {
	var lines []byte
	for _, v := range []map[string]string{
		{"type": "custom-title", "customTitle": title, "sessionId": id},
		{"type": "agent-name", "agentName": title, "sessionId": id},
	} {
		b, _ := json.Marshal(v)
		lines = append(append(lines, b...), '\n')
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(lines)
	return err
}

// agentName is the name a transcript last gave its session, "" for none.
func agentName(path string) string {
	f, err := os.Open(path)
	if path == "" || err != nil {
		return ""
	}
	defer f.Close()
	name := ""
	rd := bufio.NewReaderSize(f, 1<<16)
	for {
		line, err := rd.ReadBytes('\n')
		if bytes.Contains(line, []byte(`"type":"agent-name"`)) {
			var r struct {
				Name string `json:"agentName"`
			}
			if json.Unmarshal(line, &r) == nil && r.Name != "" {
				name = r.Name
			}
		}
		if err != nil {
			return name
		}
	}
}

// Rewind takes a session back to just before one of its prompts, as the
// terminal's double Esc does: the conversation, the code Claude changed since,
// or both. before is the assistant message the prompt followed. Rewinding past
// the first prompt leaves nothing to keep, so that starts a new session, whose
// id is returned.
func (m *Manager) Rewind(id, prompt, before string, conversation, code bool) (string, error) {
	if m.isCodex(id) {
		switch {
		case code:
			return "", errors.New("Codex keeps no copies of the files it changes, so dv cannot restore the code")
		case !conversation:
			return id, nil
		case before == "":
			return m.Create("codex")
		}
		return id, m.codex.thread(id).rewind(prompt)
	}
	p, err := m.procFor(id)
	if err != nil {
		return "", err
	}
	if code {
		p.mu.Lock()
		err := p.startLocked()
		p.mu.Unlock()
		if err != nil {
			return "", err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		body, err := p.request(ctx, map[string]any{"subtype": "rewind_files", "user_message_id": prompt})
		if err != nil {
			return "", err
		}
		var r struct {
			CanRewind bool   `json:"canRewind"`
			Error     string `json:"error"`
		}
		json.Unmarshal(body, &r)
		if !r.CanRewind {
			return "", fmt.Errorf("Claude Code cannot restore the code from there: %s", cmp.Or(r.Error, "it kept no copy of the files"))
		}
	}
	if !conversation {
		return id, nil
	}
	if before == "" {
		return m.Create("")
	}
	p.stop()
	p.mu.Lock()
	p.resume, p.resumeAt = true, before
	p.mu.Unlock()
	r := &store.Rewind{At: before}
	m.mu.Lock()
	f := m.follows[id]
	m.mu.Unlock()
	if f != nil {
		f.mu.Lock()
		r.Last = f.t.Last()
		f.version++
		f.mu.Unlock()
	}
	if err := m.saved.SetRewound(id, r); err != nil {
		return "", err
	}
	m.signal(id, false)
	return id, nil
}

// Prompts are what was said in a session back to its start - messages and
// commands - to pick one to rewind to; a page shows only from the latest
// compaction. Rewound to before one, Claude Code goes on from the conversation
// as it was then, whole.
func (m *Manager) Prompts(id string) []Item {
	prompts := []Item{}
	for _, it := range m.items(id) {
		if (it.Kind == "prompt" || it.Kind == "command") && it.UUID != "" {
			prompts = append(prompts, it)
		}
	}
	return prompts
}

// Failed is whether the session's last turn ended on an error or an
// interruption rather than on what the agent did.
func (m *Manager) Failed(id string) bool { return failed(m.items(id)) }

func failed(items []Item) bool {
	for _, it := range slices.Backward(items) {
		switch it.Kind {
		case "note":
			if it.Error || it.Text == "Interrupted" {
				return true
			}
		case "text", "tool", "prompt", "command", "shell":
			return false
		}
	}
	return false
}

// items is the whole of the conversation the session is on.
func (m *Manager) items(id string) []Item {
	if m.isCodex(id) {
		return m.codex.thread(id).items()
	}
	m.mu.Lock()
	f := m.follows[id]
	m.mu.Unlock()
	if f != nil {
		m.readWhole(f)
		f.mu.Lock()
		last := f.t.Last()
		f.mu.Unlock()
		leaf := m.leaf(id, last)
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.t.Items(leaf)
	}
	path := transcriptFiles(m.root)[id]
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	t := newTranscript(m.root)
	t.Feed(data)
	return t.Items(m.leaf(id, t.Last()))
}

// Edit is the diff a tool call in a session made.
func (m *Manager) Edit(id, tool string) (*EditDiff, error) {
	if m.isCodex(id) {
		return m.codex.edit(id, tool)
	}
	line, err := m.resultLine(id, tool)
	return editOf(line, err, m.root)
}

// Output is the whole result of one of a session's tool calls.
func (m *Manager) Output(id, tool string) (*Output, error) {
	if m.isCodex(id) {
		return m.codex.output(id, tool)
	}
	line, err := m.resultLine(id, tool)
	if err != nil {
		return nil, err
	}
	return outputFrom(line, m.root, tool)
}

// Image is the picture one of a session's Read calls was shown.
func (m *Manager) Image(id, tool string) (*Image, error) {
	if m.isCodex(id) {
		return m.codex.image(id, tool)
	}
	line, err := m.resultLine(id, tool)
	if err != nil {
		return nil, err
	}
	return imageFrom(line)
}

// PromptImage is the nth picture sent with one of a session's messages.
func (m *Manager) PromptImage(id, message string, n int) (*Image, error) {
	if m.isCodex(id) {
		return m.codex.promptImage(id, message, n)
	}
	path, err := m.transcript(id)
	if err != nil {
		return nil, err
	}
	return promptImage(path, message, n)
}

// resultLine is the line a call's result is recorded on: in the session's
// transcript or, for a call one of its agents made, in that agent's.
func (m *Manager) resultLine(id, tool string) ([]byte, error) {
	path, err := m.transcript(id)
	if err != nil {
		return nil, err
	}
	line, err := resultLine(path, tool)
	if errors.Is(err, errNoResult) {
		for _, sub := range subagentFiles(path, id) {
			if l, e := resultLine(sub, tool); e == nil {
				return l, nil
			}
		}
	}
	return line, err
}

// TaskOutput is the file a call left running in the background writes its
// output to, as its result says; Claude Code keeps these under a tasks folder.
func (m *Manager) TaskOutput(id, tool string) (string, error) {
	if m.isCodex(id) {
		return "", errNoTask
	}
	line, err := m.resultLine(id, tool)
	if err != nil {
		return "", err
	}
	var r resultEntry
	if json.Unmarshal(line, &r) != nil || r.Result.Background == "" {
		return "", errNoTask
	}
	for _, b := range r.Message.Content {
		if b.ToolUseID != tool {
			continue
		}
		path := firstGroup(taskOutput, resultText(b))
		if filepath.Base(path) == r.Result.Background+".output" && filepath.Base(filepath.Dir(path)) == "tasks" {
			return path, nil
		}
	}
	return "", errNoTask
}

var (
	errNoTask  = errors.New("this call left nothing running in the background")
	taskOutput = regexp.MustCompile(`Output is being written to: (\S+\.output)`)
)

func (m *Manager) transcript(id string) (string, error) {
	if path := transcriptFiles(m.root)[id]; path != "" {
		return path, nil
	}
	return "", fmt.Errorf("no session %s in this repository", id)
}

// Close stops every session dv runs, as dv exits.
func (m *Manager) Close() {
	m.mu.Lock()
	procs := slices.Collect(maps.Values(m.procs))
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range procs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.stop()
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		m.codex.close()
	}()
	wg.Wait()
}

// idleFor is how long a session dv runs may sit unused before it is stopped.
// It resumes on the next message, so all that goes is a process.
const idleFor = 30 * time.Minute

func (m *Manager) reap() {
	for range time.Tick(time.Minute) {
		kept := m.saved.KeptIDs()
		m.mu.Lock()
		var idle []*proc
		for id, p := range m.procs {
			p.mu.Lock()
			if p.cmd != nil && !p.busy && len(p.asks) == 0 && time.Since(p.lastUsed) > idleFor && m.follows[id] == nil && !slices.Contains(kept, id) {
				idle = append(idle, p)
			}
			p.mu.Unlock()
		}
		m.mu.Unlock()
		for _, p := range idle {
			p.stop()
		}
		m.codex.reap(idleFor, kept)
	}
}

// CodexOptions are what a Codex session starts with and can pick from.
func (m *Manager) CodexOptions() Options { return m.codex.options() }

// CodexUsage is how much of the Codex account's limits is used.
func (m *Manager) CodexUsage() *Usage {
	if !codex.Available() {
		return nil
	}
	return m.codex.usageNow()
}

// OptionsFor is what the session's own agent offers.
func (m *Manager) OptionsFor(id string) Options {
	if m.isCodex(id) {
		return m.codex.options()
	}
	return m.Options()
}

// Live is what a session is doing, beyond what its transcript says.
type Live struct {
	Agent   string `json:"agent,omitempty"`   // "codex"; "" is Claude Code
	Found   bool   `json:"found"`             // the transcript exists yet
	Running string `json:"running,omitempty"` // dv | terminal
	Busy    bool   `json:"busy,omitempty"`
	Model   string `json:"model"` // asked for; "" is the user's default
	Using   string `json:"using,omitempty"`
	// The model the transcript was last answered on, which a resumed session
	// goes on with unless another is asked for.
	LastModel   string   `json:"lastModel,omitempty"`
	LastEffort  string   `json:"lastEffort,omitempty"`
	Mode        string   `json:"mode,omitempty"`
	Effort      string   `json:"effort,omitempty"` // asked for
	EffortUsing string   `json:"effortUsing,omitempty"`
	Context     *Context `json:"context,omitempty"`
	Queued      []Queued `json:"queued,omitempty"` // sent while busy, not yet taken up
	Status      string   `json:"status,omitempty"`
	Error       string   `json:"error,omitempty"`
	Cost        float64  `json:"cost,omitempty"`
	Blocks      []Block  `json:"blocks,omitempty"`
	// Since is when the turn going on began, where that is known.
	Since string `json:"since,omitempty"`
	Shell *Shell `json:"shell,omitempty"` // a command run with ! that has not reached the agent
	// Pending is a model, mode or effort picked during a turn, which Codex
	// takes from the next one.
	Pending bool `json:"pending,omitempty"`
	// Suggestion is the next message Claude Code guessed after the last turn.
	Suggestion string `json:"suggestion,omitempty"`
}

// Update is what a page following a session is sent: the items that are new
// or changed since its last update - all of them, when Reset - and the live
// state.
type Update struct {
	Reset   bool     `json:"reset,omitempty"`
	Items   []Item   `json:"items,omitempty"`
	Earlier *Earlier `json:"earlier,omitempty"`
	Live    Live     `json:"live"`
}

// Earlier is the conversation above where a page shows a session from.
type Earlier struct {
	Messages int `json:"messages,omitempty"` // said in the part Next shows, when known
	// The from that shows it back to the compaction before: its key, "all", or
	// before:<key> while what came before has not been read.
	Next string `json:"next"`
}

// Sub is one page following a session.
type Sub struct {
	Changed chan struct{}
	version int
	keys    []string
	hashes  map[string]uint64
	// Where the conversation is shown from: a compaction's key, or "all";
	// until the first update, "" for the latest compaction then.
	from    string
	earlier *Earlier
}

type follow struct {
	id   string
	call string // for an agent's transcript, the session's call that started it
	key  string // in Manager.follows: the session's id, and the call's
	poke chan struct{}
	quit chan struct{}
	subs map[*Sub]bool

	mu      sync.Mutex
	path    string
	t       *Transcript
	version int // moves when the conversation might read differently
	status  string
	whole   bool // read from the start, not from the latest compaction
}

// readWhole reads what a transcript read from its latest compaction left out.
// It is read again apart, the pages following it going on with what they have
// until it is in.
func (m *Manager) readWhole(f *follow) {
	f.mu.Lock()
	path, partial := f.path, f.t.Start > 0
	f.whole = true
	f.mu.Unlock()
	if !partial {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	t := f.transcript(m.root)
	t.Feed(data)
	f.mu.Lock()
	f.t = t
	f.version++
	f.mu.Unlock()
	m.read(f) // written since
	m.signal(f.key, false)
}

func (f *follow) transcript(root string) *Transcript {
	t := newTranscript(root)
	t.sidechain = f.call != ""
	return t
}

// Follow starts following a session for a page. Its Changed channel says when
// to ask for an Update; the func stops following.
//
// from is where the page shows the conversation from: a compaction's key, "all",
// or "" for the latest compaction. What came before a compaction is most of a
// long session, and what Claude no longer has in front of it.
func (m *Manager) Follow(id, from string) (*Sub, func()) {
	if m.isCodex(id) {
		s := &Sub{Changed: make(chan struct{}, 1), version: -1}
		return s, m.codex.thread(id).follow(s, from == "all")
	}
	// Anywhere but the latest compaction needs what came before it read.
	s, stop := m.startFollow(id, "", from != "")
	s.from = from
	return s, stop
}

// FollowAgent follows the transcript of an agent a session started, by the
// call that started it, for AgentUpdate. It is shown whole.
func (m *Manager) FollowAgent(id, call string) (*Sub, func()) {
	if m.isCodex(id) {
		s := &Sub{Changed: make(chan struct{}, 1), version: -1}
		if t := m.codex.agentThread(id, call); t != nil {
			return s, t.follow(s, true)
		}
		return s, func() {}
	}
	return m.startFollow(id, call, true)
}

func (m *Manager) startFollow(id, call string, whole bool) (*Sub, func()) {
	s := &Sub{Changed: make(chan struct{}, 1), version: -1}
	key := strings.TrimSuffix(id+" "+call, " ")
	m.mu.Lock()
	f := m.follows[key]
	existing := f != nil
	if !existing {
		f = &follow{id: id, call: call, key: key, poke: make(chan struct{}, 1), quit: make(chan struct{}), subs: map[*Sub]bool{}, whole: whole}
		f.t = f.transcript(m.root)
		m.follows[key] = f
		go m.run(f)
	} else {
		s.Changed <- struct{}{}
	}
	f.subs[s] = true
	m.mu.Unlock()
	if existing && whole {
		m.readWhole(f)
	}
	return s, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		delete(f.subs, s)
		if len(f.subs) == 0 && m.follows[key] == f {
			delete(m.follows, key)
			close(f.quit)
		}
	}
}

// signal tells the pages following a session that it changed; wrote means the
// transcript did, so it is read now rather than at the next tick.
func (m *Manager) signal(id string, wrote bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f := m.follows[id]
	if f == nil {
		return
	}
	if wrote {
		select {
		case f.poke <- struct{}{}:
		default:
		}
		return
	}
	for s := range f.subs {
		select {
		case s.Changed <- struct{}{}:
		default:
		}
	}
}

func (m *Manager) run(f *follow) {
	// Read before the first update, or a page opens on an empty conversation.
	m.read(f)
	m.signal(f.key, false)
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for n := 0; ; n++ {
		select {
		case <-f.quit:
			return
		case <-tick.C:
		case <-f.poke:
		}
		read := m.read(f)
		// A terminal's busy and idle, which only its process record has.
		status := ""
		if n%5 == 0 && f.call == "" {
			r := m.running()[f.id]
			status = r.Status + r.Kind
		}
		f.mu.Lock()
		moved := n%5 == 0 && status != f.status
		if moved {
			f.status = status
		}
		f.mu.Unlock()
		if read || moved {
			m.signal(f.key, false)
		}
	}
}

// read takes in whatever the transcript gained, reporting whether it did.
func (m *Manager) read(f *follow) bool {
	f.mu.Lock()
	path := f.path
	f.mu.Unlock()
	if path == "" {
		path = transcriptFiles(m.root)[f.id]
		// An agent's appears a moment after the call that starts it.
		if path != "" && f.call != "" {
			path = subagentFiles(path, f.id)[f.call]
		}
		if path == "" {
			return false
		}
	}
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.path = path
	if fi.Size() < f.t.Offset {
		f.t = f.transcript(m.root) // rewritten from the start
	}
	if fi.Size() == f.t.Offset {
		return false
	}
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer file.Close()
	data, err := io.ReadAll(io.NewSectionReader(file, f.t.Offset, fi.Size()-f.t.Offset))
	if err != nil {
		return false
	}
	if f.t.Offset == 0 && !f.whole {
		if start, mode := tail(data); start > 0 {
			f.t.Start, f.t.Offset, f.t.mode = int64(start), int64(start), mode
			data = data[start:]
		}
	}
	if f.t.Feed(data) {
		f.version++
	}
	return true
}

// AgentUpdate is what has changed in an agent's transcript since the page
// following it with FollowAgent last asked. Whether the agent is still at work
// is its call's to say, in the session.
func (m *Manager) AgentUpdate(id, call string, s *Sub) Update {
	if m.isCodex(id) {
		if t := m.codex.agentThread(id, call); t != nil {
			return t.update(s)
		}
		return Update{}
	}
	m.mu.Lock()
	f := m.follows[id+" "+call]
	m.mu.Unlock()
	var u Update
	if f == nil {
		return u
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	u.Live.Found = f.path != ""
	if s.version != f.version {
		s.version = f.version
		u.Reset, u.Items = s.diff(f.t.Items(""))
	}
	_, u.Live.LastModel = f.t.Context("")
	u.Live.LastEffort = f.t.Effort("")
	return u
}

// ran is when the process a session runs in started, zero when none does.
func ran(p *proc) time.Time {
	if p == nil {
		return time.Time{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd == nil {
		return time.Time{}
	}
	return p.ran
}

// ended marks work left running in the background by a process that has since
// gone: it stopped with that process, and nothing will ever say how it went,
// so the page would keep it on "running in the background" for good.
func ended(items []Item, ran time.Time) {
	if ran.IsZero() {
		return
	}
	for i, it := range items {
		if it.Task != nil || it.Result == nil || it.Result.Detail == nil || it.Result.Detail.Background == "" {
			continue
		}
		if at, err := time.Parse(time.RFC3339, it.At); err == nil && at.Before(ran) {
			items[i].Task = &Task{Status: "stopped"}
		}
	}
}

// Update is what has changed in a session since the page following it last
// asked.
func (m *Manager) Update(id string, s *Sub) Update {
	u := m.update(id, s)
	u.Live.Shell = m.shellOf(id)
	return u
}

func (m *Manager) update(id string, s *Sub) Update {
	if m.isCodex(id) {
		return m.codex.thread(id).update(s)
	}
	m.mu.Lock()
	f := m.follows[id]
	p := m.procs[id]
	m.mu.Unlock()
	var u Update
	if f == nil {
		return u
	}
	f.mu.Lock()
	last := f.t.Last()
	f.mu.Unlock()
	leaf := m.leaf(id, last)
	switches := m.saved.Switches(id)
	// Rewound to before where the file was read from.
	f.mu.Lock()
	before := leaf != "" && f.t.Start > 0 && !f.t.Has(leaf)
	f.mu.Unlock()
	if before {
		m.readWhole(f)
	}
	r := m.running()[id]
	opts := m.Options()

	f.mu.Lock()
	u.Live.Found = f.path != ""
	if s.version != f.version {
		s.version = f.version
		items := s.shown(f.t.Items(leaf, switches...), f.t.Start > 0)
		ended(items, ran(p))
		u.Reset, u.Items = s.diff(items)
	}
	u.Earlier = s.earlier
	used, model := f.t.Context(leaf)
	u.Live.LastModel = model
	if c := windowFor(model, opts); used > 0 && c.Max > 0 {
		c.Used = used
		u.Live.Context = &c
	}
	if p != nil {
		p.mu.Lock()
		u.Live.Model, u.Live.Using, u.Live.Mode, u.Live.Queued = p.model, p.using, p.mode, slices.Clone(p.queued)
		u.Live.Effort, u.Live.EffortUsing = p.effort, p.effortUsing
		if p.cmd != nil && p.context != nil {
			// Its window is exact; the transcript keeps up with what is used
			// within a turn, where the process is only asked after one.
			c := *p.context
			if used > 0 {
				c.Used = used
			}
			u.Live.Context = &c
		}
		u.Live.Status, u.Live.Error, u.Live.Cost, u.Live.Suggestion = p.status, p.err, p.cost, p.suggestion
		if p.busy && !p.since.IsZero() {
			u.Live.Since = p.since.UTC().Format(time.RFC3339Nano)
		}
		// Blocks reach the file in order, so one written means those before it
		// are too, even from before where a long transcript is read from.
		for i, b := range slices.Backward(p.blocks) {
			if f.t.Written(b.msg) > b.index {
				p.blocks = slices.Delete(p.blocks, 0, i+1)
				break
			}
		}
		for _, b := range p.blocks {
			if f.t.Written(b.msg) <= b.index {
				u.Live.Blocks = append(u.Live.Blocks, *b)
			}
		}
		p.mu.Unlock()
	}
	// Until Claude Code runs it and says, the mode the transcript last recorded,
	// or one switched to since.
	if u.Live.Mode == "" {
		u.Live.Mode = f.t.Mode(leaf, switches)
	}
	f.mu.Unlock()
	u.Live.Running, u.Live.Busy = where(p, r)
	return u
}

// leaf is where a rewound conversation ends, until the transcript's last
// entry moves on from what it was at the rewind; "" is wherever the file ends.
func (m *Manager) leaf(id, last string) string {
	r, ok := m.saved.Rewound(id)
	switch {
	case !ok || last == "":
		return ""
	case r.Last == "":
		r.Last = last
		m.saved.SetRewound(id, &r)
	case r.Last != last:
		m.saved.SetRewound(id, nil)
		return ""
	}
	return r.At
}

// shown is the conversation from where the page shows it, noting what is above.
// The place is settled on the first time: a compaction made while the page
// looks on does not take away what it is showing. partial is a transcript read
// from its latest compaction, where how much came before is not known.
func (s *Sub) shown(items []Item, partial bool) []Item {
	// Rewound to before it, the compaction it was shown from is gone.
	if s.from == "" || s.from != "all" && !strings.HasPrefix(s.from, "before:") && cutAt(items, s.from) < 0 {
		s.from = lastCompaction(items)
	}
	if key, ok := strings.CutPrefix(s.from, "before:"); ok {
		s.from = lastCompaction(items[:max(cutAt(items, key), 0)])
	}
	i := max(cutAt(items, s.from), 0)
	s.earlier = nil
	switch {
	case partial:
		s.earlier = &Earlier{Next: "before:" + s.from}
	case i > 0:
		s.earlier = &Earlier{Next: lastCompaction(items[:i])}
		for _, it := range items[max(cutAt(items, s.earlier.Next), 0):i] {
			if it.Kind == "prompt" {
				s.earlier.Messages++
			}
		}
	}
	return items[i:]
}

func lastCompaction(items []Item) string {
	for _, it := range slices.Backward(items) {
		if it.Kind == "compact" {
			return it.Key
		}
	}
	return "all"
}

// cutAt is where a conversation shown from a compaction starts - there, or at
// the /compact that made it - or -1 when it holds no such compaction.
func cutAt(items []Item, key string) int {
	i := slices.IndexFunc(items, func(it Item) bool { return it.Key == key })
	if i > 0 && items[i-1].Kind == "command" && strings.HasPrefix(items[i-1].Text, "/compact") {
		i--
	}
	return i
}

// diff is items less what the page already has, or everything when what it
// has is no longer the start of the conversation.
func (s *Sub) diff(items []Item) (bool, []Item) {
	keys := make([]string, len(items))
	hashes := make(map[string]uint64, len(items))
	for i, it := range items {
		keys[i] = it.Key
		h := fnv.New64a()
		json.NewEncoder(h).Encode(it)
		hashes[it.Key] = h.Sum64()
	}
	prefix := s.hashes != nil && len(s.keys) <= len(keys) && slices.Equal(s.keys, keys[:len(s.keys)])
	old := s.hashes
	s.keys, s.hashes = keys, hashes
	if !prefix {
		return true, items
	}
	var changed []Item
	for _, it := range items {
		if old[it.Key] != hashes[it.Key] {
			changed = append(changed, it)
		}
	}
	return false, changed
}

func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}
