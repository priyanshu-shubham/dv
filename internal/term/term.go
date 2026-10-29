// Package term runs shells in pseudo-terminals for the Terminal view. A shell
// is dv's, not a page's: pages attach and leave, and it runs on until it exits
// or is closed, keeping the end of what it printed for the next page to attach.
package term

import (
	"bytes"
	"cmp"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

const (
	keep       = 1 << 20 // bytes of output at least that a page attaching is given
	watchEvery = time.Second
	ticketLife = 30 * time.Second
	// How long a shell that exited has to finish printing; a job it left in
	// the background can hold the terminal open for good.
	drainFor = time.Second
)

// Info is a terminal as the page lists it.
type Info struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Named bool   `json:"named,omitempty"` // by the reader, rather than the shell's
	Dir   string `json:"dir"`
	// Busy is the shell running something in the foreground, Command, since
	// Since; or else at the prompt since then. Command can go untold.
	Busy    bool      `json:"busy,omitempty"`
	Command string    `json:"command,omitempty"`
	Since   time.Time `json:"since"`
	Output  time.Time `json:"output,omitzero"` // when it last printed
	Ended   bool      `json:"ended,omitempty"`
	Exit    int       `json:"exit,omitempty"`
	// Tmux is the session a tmux client in front shows, whose active pane
	// Command and Dir are then about.
	Tmux string `json:"tmux,omitempty"`
}

// Event is what a page attached is sent: output, or the shell ending or
// starting again.
type Event struct {
	Output  []byte
	Ended   bool
	Exit    int
	Started bool
}

// Manager holds a folder's terminals.
type Manager struct {
	root    string
	mu      sync.Mutex
	terms   []*Terminal
	tickets map[string]ticket
	closed  bool

	keepMu sync.Mutex
	kept   []keptTerm // as last written
}

type ticket struct {
	term    string
	expires time.Time
}

// Every folder's, for what a restart of dv would stop.
var managers = struct {
	sync.Mutex
	set map[*Manager]struct{}
}{set: map[*Manager]struct{}{}}

func New(root string) *Manager {
	m := &Manager{root: root, tickets: map[string]ticket{}}
	managers.Lock()
	managers.set[m] = struct{}{}
	managers.Unlock()
	return m
}

// Busy is how many terminals in the whole of dv are running a command that a
// restart would stop: tmux's sessions go on without their terminal.
func Busy() int {
	managers.Lock()
	all := make([]*Manager, 0, len(managers.set))
	for m := range managers.set {
		all = append(all, m)
	}
	managers.Unlock()
	n := 0
	for _, m := range all {
		m.mu.Lock()
		terms := slices.Clone(m.terms)
		m.mu.Unlock()
		for _, t := range terms {
			t.mu.Lock()
			if !t.ended && t.fg != t.pid && t.tmux == nil {
				n++
			}
			t.mu.Unlock()
		}
	}
	return n
}

// Available says whether dv can run a terminal here, and if not, why.
func Available() error { return supported }

// Start opens a terminal with a shell in the folder.
func (m *Manager) Start() (*Terminal, error) { return m.open(newID(), m.root) }

func (m *Manager) open(id, dir string) (*Terminal, error) {
	t := &Terminal{id: id, m: m, cols: 80, rows: 24, watchers: map[*Watcher]struct{}{}}
	if err := t.start(dir); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		t.kill()
		return nil, errors.New("this folder is closing")
	}
	m.terms = append(m.terms, t)
	return t, nil
}

func (m *Manager) Get(id string) *Terminal {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.terms {
		if t.id == id {
			return t
		}
	}
	return nil
}

// List is the terminals in the order they were opened.
func (m *Manager) List() []Info {
	m.mu.Lock()
	terms := slices.Clone(m.terms)
	m.mu.Unlock()
	out := make([]Info, 0, len(terms))
	for _, t := range terms {
		out = append(out, t.Info())
	}
	return out
}

// Running is how many terminals have a shell.
func (m *Manager) Running() int {
	m.mu.Lock()
	terms := slices.Clone(m.terms)
	m.mu.Unlock()
	n := 0
	for _, t := range terms {
		if !t.Info().Ended {
			n++
		}
	}
	return n
}

// Remove ends a terminal's shell and lets the terminal go.
func (m *Manager) Remove(id string) bool {
	m.mu.Lock()
	i := slices.IndexFunc(m.terms, func(t *Terminal) bool { return t.id == id })
	var t *Terminal
	if i >= 0 {
		t = m.terms[i]
		m.terms = slices.Delete(m.terms, i, i+1)
	}
	m.mu.Unlock()
	if t == nil {
		return false
	}
	t.close()
	m.keep()
	return true
}

// Close ends every shell, for a folder dv lets go.
func (m *Manager) Close() {
	m.mu.Lock()
	terms := m.terms
	m.terms, m.closed = nil, true
	m.mu.Unlock()
	managers.Lock()
	delete(managers.set, m)
	managers.Unlock()
	for _, t := range terms {
		t.close()
	}
}

// Ticket lets one page attach to terminal id shortly. It is how the page shows
// it is dv's own: a WebSocket is not kept to its origin as a fetch is, and a
// proxy in front of dv can rewrite the Host its Origin would be checked against.
func (m *Manager) Ticket(id string) string {
	tk := newID() + newID()
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for k, v := range m.tickets {
		if now.After(v.expires) {
			delete(m.tickets, k)
		}
	}
	m.tickets[tk] = ticket{id, now.Add(ticketLife)}
	return tk
}

// Redeem uses up a ticket, reporting whether it was one for terminal id.
func (m *Manager) Redeem(tk, id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, ok := m.tickets[tk]
	delete(m.tickets, tk)
	return ok && v.term == id && time.Now().Before(v.expires)
}

// Terminal is one shell in a pseudo-terminal, and once it has exited, what it
// left on screen, until the reader starts another in its place or closes it.
type Terminal struct {
	id string
	m  *Manager

	mu       sync.Mutex
	name     string
	shell    string
	pty      *os.File
	pid      int
	stop     chan struct{} // closed once the shell is reaped, after which pid is not its
	dir      string        // where the shell was, last looked
	out      []byte
	alt      bool // a full-screen program has the screen
	watchers map[*Watcher]struct{}
	fg       int // the foreground process group
	command  string
	tmux     *tmuxClient // the foreground's, if it is tmux
	since    time.Time
	output   time.Time
	ended    bool
	exit     int
	gone     bool // closed by the reader
	cols     int
	rows     int
}

func (t *Terminal) ID() string { return t.id }

func (t *Terminal) Info() Info {
	t.mu.Lock()
	defer t.mu.Unlock()
	info := Info{
		ID: t.id, Name: cmp.Or(t.name, filepath.Base(t.shell)), Named: t.name != "", Dir: t.dir,
		Busy: !t.ended && t.fg != t.pid, Command: t.command, Since: t.since, Output: t.output, Ended: t.ended, Exit: t.exit,
	}
	if c := t.tmux; c != nil && c.session != "" {
		info.Tmux, info.Command, info.Dir = c.session, c.command, c.dir
	}
	return info
}

// Rename names the terminal; "" gives it back the shell's name.
func (t *Terminal) Rename(name string) {
	t.mu.Lock()
	t.name = name
	t.mu.Unlock()
	t.m.keep()
}

// Restart starts a shell again in a terminal whose shell has exited, where
// that one last was.
func (t *Terminal) Restart() error {
	t.mu.Lock()
	if !t.ended {
		t.mu.Unlock()
		return errors.New("its shell is still running")
	}
	t.ended = false // so a second page asking at once is told so
	dir := t.dir
	t.mu.Unlock()
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		dir = t.m.root
	}
	if err := t.start(dir); err != nil {
		t.mu.Lock()
		t.ended = true
		t.mu.Unlock()
		return err
	}
	t.send(Event{Started: true})
	return nil
}

func (t *Terminal) start(dir string) error {
	if supported != nil {
		return supported
	}
	shell := userShell()
	cmd := shellCommand(shell)
	cmd.Dir = dir
	cmd.Env = shellEnv()
	t.mu.Lock()
	size := &pty.Winsize{Cols: uint16(t.cols), Rows: uint16(t.rows)}
	t.mu.Unlock()
	f, err := pty.StartWithSize(cmd, size)
	if err != nil {
		return err
	}
	stop := make(chan struct{})
	t.mu.Lock()
	if t.gone {
		t.mu.Unlock()
		f.Close()
		hangUp(cmd.Process.Pid)
		go cmd.Wait()
		return errors.New("the terminal was closed")
	}
	t.shell, t.pty, t.pid, t.stop, t.dir = shell, f, cmd.Process.Pid, stop, dir
	t.fg, t.command, t.since, t.tmux = cmd.Process.Pid, "", time.Now().UTC(), nil
	t.ended, t.exit, t.alt = false, 0, false
	t.mu.Unlock()

	read := make(chan struct{})
	go func() {
		defer close(read)
		t.read(f)
	}()
	go t.watch(f, cmd.Process.Pid, stop)
	go func() {
		err := cmd.Wait()
		close(stop)
		select {
		case <-read:
		case <-time.After(drainFor):
		}
		f.Close()
		<-read
		code := 0
		if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
			code = ee.ExitCode()
		}
		t.mu.Lock()
		t.ended, t.exit, t.command, t.since = true, code, "", time.Now().UTC()
		t.mu.Unlock()
		t.send(Event{Ended: true, Exit: code})
	}()
	return nil
}

func (t *Terminal) read(f *os.File) {
	buf := make([]byte, 32<<10)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			t.printed(bytes.Clone(buf[:n]))
		}
		if err != nil {
			return
		}
	}
}

var (
	altOn  = [][]byte{[]byte("\x1b[?1049h"), []byte("\x1b[?1047h"), []byte("\x1b[?47h")}
	altOff = [][]byte{[]byte("\x1b[?1049l"), []byte("\x1b[?1047l"), []byte("\x1b[?47l")}
)

func lastOf(p []byte, seqs [][]byte) int {
	i := -1
	for _, s := range seqs {
		i = max(i, bytes.LastIndex(p, s))
	}
	return i
}

func (t *Terminal) printed(p []byte) {
	t.mu.Lock()
	t.out = append(t.out, p...)
	// Let run to twice what is kept between trims, so a flood of output is not
	// copied over on every read.
	if len(t.out) > 2*keep {
		over := len(t.out) - keep
		// From a line's start, so the replay does not open mid-sequence.
		if nl := bytes.IndexByte(t.out[over:], '\n'); nl >= 0 && nl < 4096 {
			over += nl + 1
		}
		t.out = append(t.out[:0:0], t.out[over:]...)
	}
	if on, off := lastOf(p, altOn), lastOf(p, altOff); on != off {
		t.alt = on > off
	}
	t.output = time.Now().UTC()
	t.mu.Unlock()
	t.send(Event{Output: p})
}

// Write is the reader's typing.
func (t *Terminal) Write(p []byte) (int, error) {
	t.mu.Lock()
	f, ended := t.pty, t.ended
	t.mu.Unlock()
	if f == nil || ended {
		return 0, errors.New("its shell has exited")
	}
	return f.Write(p)
}

// Resize sets the terminal's size, the last page to ask having its way. With
// redraw, a full-screen program is asked to draw itself again even at the
// same size, for a page that has just attached and been given only the end of
// what it printed.
func (t *Terminal) Resize(cols, rows int, redraw bool) {
	if cols < 2 || rows < 1 || cols > 1000 || rows > 1000 {
		return
	}
	t.mu.Lock()
	same := cols == t.cols && rows == t.rows
	t.cols, t.rows = cols, rows
	f, ended, alt, fg := t.pty, t.ended, t.alt, t.fg
	t.mu.Unlock()
	if f == nil || ended {
		return
	}
	if !same {
		pty.Setsize(f, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	} else if redraw && alt {
		winched(fg)
	}
}

// Watcher follows a terminal for one page. C is closed when the terminal is,
// or when the page fell too far behind, which reattaching makes good.
type Watcher struct {
	C chan Event
	t *Terminal
}

// Attach follows the terminal, from what it has printed so far, given first.
func (t *Terminal) Attach() (*Watcher, []byte, Info) {
	w := &Watcher{C: make(chan Event, 1024), t: t}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.gone {
		close(w.C)
	} else {
		t.watchers[w] = struct{}{}
	}
	info := Info{Ended: t.ended, Exit: t.exit}
	return w, bytes.Clone(t.out), info
}

func (w *Watcher) Close() {
	w.t.mu.Lock()
	defer w.t.mu.Unlock()
	if _, ok := w.t.watchers[w]; ok {
		delete(w.t.watchers, w)
		close(w.C)
	}
}

func (t *Terminal) send(e Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for w := range t.watchers {
		select {
		case w.C <- e:
		default:
			delete(t.watchers, w)
			close(w.C)
		}
	}
}

// watch keeps track of what the shell is running and where it is; and with
// tmux in front, of the session it shows, which tmux can switch under it.
func (t *Terminal) watch(f *os.File, pid int, stop <-chan struct{}) {
	tick := time.NewTicker(watchEvery)
	defer tick.Stop()
	for n := 0; ; n++ {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		fg := foreground(f)
		dir := workingDir(pid)
		t.mu.Lock()
		changed := fg > 0 && fg != t.fg
		if dir != "" {
			t.dir = dir
		}
		c, looked := t.tmux, len(t.watchers) > 0
		t.mu.Unlock()
		if changed {
			var args []string
			if fg != pid {
				args = argsOf(fg)
			}
			c = tmuxOf(args)
			command := display(args)
			t.mu.Lock()
			if !t.ended {
				t.fg, t.command, t.since, t.tmux = fg, command, time.Now().UTC(), c
			}
			t.mu.Unlock()
			if c == nil {
				t.m.keep()
			}
		}
		// Asked each second while a page shows it, else every few.
		if c == nil || fg <= 0 || !(changed || looked || n%5 == 0) {
			continue
		}
		now := tmuxClient{flags: c.flags}
		if !now.ask(fg) {
			continue
		}
		t.mu.Lock()
		moved := t.tmux == c && now.session != c.session
		if t.tmux == c {
			c.session, c.command, c.dir = now.session, now.command, now.dir
		}
		t.mu.Unlock()
		if moved {
			t.m.keep()
		}
	}
}

// shellEnv is dv's environment for a shell in its terminal, less what says
// it runs in the terminal or tmux pane dv was started from: tmux would refuse
// to start, taking itself to be nested, and a profile's tmux hooks would act
// on dv's own pane. A profile that starts tmux unless $TMUX is set can leave
// dv's terminals be by their TERM_PROGRAM.
func shellEnv() []string {
	return append(without(os.Environ(), foreignEnv...), "TERM=xterm-256color", "COLORTERM=truecolor", "TERM_PROGRAM=dv")
}

var foreignEnv = []string{
	"TMUX", "TMUX_PANE", "STY", "WINDOW", "ZELLIJ", "ZELLIJ_SESSION_NAME", "ZELLIJ_PANE_ID",
	"TERM_PROGRAM_VERSION", "TERM_SESSION_ID", "ITERM_SESSION_ID", "KITTY_WINDOW_ID", "WEZTERM_PANE",
	"VTE_VERSION", "WINDOWID", "ALACRITTY_WINDOW_ID",
}

func without(env []string, keys ...string) []string {
	return slices.DeleteFunc(env, func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return slices.Contains(keys, k)
	})
}

func (t *Terminal) close() {
	t.mu.Lock()
	t.gone = true
	for w := range t.watchers {
		delete(t.watchers, w)
		close(w.C)
	}
	t.mu.Unlock()
	t.kill()
}

// kill hangs up on the shell, as closing a terminal window does, and makes
// sure of it shortly after.
func (t *Terminal) kill() {
	t.mu.Lock()
	f, pid, stop := t.pty, t.pid, t.stop
	t.mu.Unlock()
	if f == nil {
		return
	}
	f.Close()
	select {
	case <-stop:
		return
	default:
	}
	hangUp(pid)
	go func() {
		select {
		case <-stop:
		case <-time.After(3 * time.Second):
			killGroup(pid)
		}
	}()
}

// display writes a command as it would be typed, its program by name.
func display(args []string) string {
	if len(args) == 0 || args[0] == "" {
		return ""
	}
	args[0] = filepath.Base(args[0])
	s := strings.Join(args, " ")
	if len(s) > 300 {
		s = strings.ToValidUTF8(s[:300], "")
	}
	return s
}

func newID() string {
	b := make([]byte, 8)
	rand.Read(b)
	return hex.EncodeToString(b)
}
