package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// A command the reader runs from the message box with !, as in the terminal.
// Claude Code run headless would send it to the model as words, so dv runs it
// itself, for Codex too, and then sends the command and what it printed as a
// message, in the form Claude Code's terminal records one.

const (
	shellTimeout = 10 * time.Minute
	// shellKeep is how much of each stream the agent is sent: its start and,
	// mostly, its end, where a failure says what went wrong.
	shellKeep = 30000
)

var (
	bashInput  = regexp.MustCompile(`(?s)<bash-input>(.*?)</bash-input>`)
	bashStdout = regexp.MustCompile(`(?s)<bash-stdout>(.*?)</bash-stdout>`)
	bashStderr = regexp.MustCompile(`(?s)<bash-stderr>(.*?)</bash-stderr>`)
)

// Shell is a command running for a session, as the page is told of it.
type Shell struct {
	UUID    string `json:"uuid"` // the message it goes as
	Command string `json:"command"`
	Since   string `json:"since"`
	// Stopped and Error are why it did not reach the agent.
	Stopped bool   `json:"stopped,omitempty"`
	Error   string `json:"error,omitempty"`
}

type shellRun struct {
	Shell
	cancel context.CancelFunc
	done   chan struct{} // closed once it has gone, or failed to
}

func (r *shellRun) ended() bool { return r.Stopped || r.Error != "" }

// Shell runs command in the repository for session id, and sends it with
// what it printed as message uuid once it ends. Stopped, nothing is sent.
func (m *Manager) Shell(id, uuid, command string) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return errors.New("there is no command to run")
	}
	if _, elsewhere := m.running()[id]; elsewhere && !m.isCodex(id) {
		return errors.New("this session is open in a terminal; dv follows it, but only the terminal can talk to it")
	}
	m.mu.Lock()
	if r := m.shells[id]; r != nil && !r.ended() {
		m.mu.Unlock()
		return errors.New("a command is already running in this session")
	}
	ctx, cancel := context.WithTimeout(context.Background(), shellTimeout)
	r := &shellRun{
		Shell:  Shell{UUID: cmp.Or(uuid, newUUID()), Command: command, Since: time.Now().UTC().Format(time.RFC3339Nano)},
		cancel: cancel, done: make(chan struct{}),
	}
	m.shells[id] = r
	m.mu.Unlock()
	m.shellChanged(id)
	go m.runShell(ctx, id, r)
	return nil
}

func (m *Manager) runShell(ctx context.Context, id string, r *shellRun) {
	defer r.cancel()
	var stdout, stderr clipped
	cmd := shellCmd(r.Command)
	cmd.Dir, cmd.Stdout, cmd.Stderr = m.root, &stdout, &stderr
	ownGroup(cmd)
	err := cmd.Start()
	if err == nil {
		stop := context.AfterFunc(ctx, func() { killGroup(cmd) })
		cmd.Wait()
		stop()
	} else {
		fmt.Fprintln(&stderr, err)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		m.endShell(id, r, func() { r.Stopped = true })
		return
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		fmt.Fprintf(&stderr, "\ndv stopped the command after %s.\n", shellTimeout)
	}
	text := "<bash-input>" + r.Command + "</bash-input>\n<bash-stdout>" + stdout.String() + "</bash-stdout><bash-stderr>" + stderr.String() + "</bash-stderr>"
	if _, err = m.send(id, r.UUID, text, nil); err != nil {
		m.endShell(id, r, func() { r.Error = err.Error() })
		return
	}
	m.endShell(id, r, nil)
}

// endShell lets go of a command. One that did not reach the agent, as why
// sets, is kept for the page to put back in the box.
func (m *Manager) endShell(id string, r *shellRun, why func()) {
	m.mu.Lock()
	if why != nil {
		why()
	} else if m.shells[id] == r {
		delete(m.shells, id)
	}
	m.mu.Unlock()
	close(r.done)
	m.shellChanged(id)
}

// awaitShell holds a message until a command running before it has gone, so
// the agent reads the two in the order they were sent.
func (m *Manager) awaitShell(id string) {
	m.mu.Lock()
	r := m.shells[id]
	m.mu.Unlock()
	if r == nil {
		return
	}
	<-r.done
	m.mu.Lock()
	if m.shells[id] == r {
		delete(m.shells, id)
	}
	m.mu.Unlock()
}

// stopShell stops a command still running, reporting whether there was one.
func (m *Manager) stopShell(id string) bool {
	m.mu.Lock()
	r := m.shells[id]
	m.mu.Unlock()
	if r == nil || r.ended() {
		return false
	}
	r.cancel()
	return true
}

func (m *Manager) shellOf(id string) *Shell {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.shells[id]; r != nil {
		s := r.Shell
		return &s
	}
	return nil
}

func (m *Manager) shellChanged(id string) {
	if m.isCodex(id) {
		m.codex.thread(id).signal()
		return
	}
	m.signal(id, false)
}

// shellOutput is what a command run with ! printed, as one result.
func shellOutput(text string) *Result {
	out := strings.TrimRight(firstGroup(bashStdout, text), "\n")
	if e := strings.TrimRight(firstGroup(bashStderr, text), "\n"); e != "" {
		out = strings.TrimLeft(out+"\n"+e, "\n")
	}
	return &Result{Text: cut(out, 4*maxResult)}
}

// clipped keeps the start and the end of what a command prints, leaving out
// the middle of anything longer than shellKeep.
type clipped struct {
	head, tail []byte
	dropped    int
}

func (c *clipped) Write(p []byte) (int, error) {
	n := len(p)
	if room := shellKeep/3 - len(c.head); room > 0 {
		k := min(room, len(p))
		c.head, p = append(c.head, p[:k]...), p[k:]
	}
	c.tail = append(c.tail, p...)
	if over := len(c.tail) - shellKeep*2/3; over > 0 {
		c.tail = append(c.tail[:0], c.tail[over:]...)
		c.dropped += over
	}
	return n, nil
}

func (c *clipped) String() string {
	if c.dropped == 0 {
		return strings.ToValidUTF8(string(c.head)+string(c.tail), "")
	}
	return strings.ToValidUTF8(fmt.Sprintf("%s\n… %d bytes left out …\n%s", c.head, c.dropped, c.tail), "")
}
