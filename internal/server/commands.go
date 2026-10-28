package server

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"dv/internal/notify"
)

// A command action runs with sh in the folder, as a worktree hook does, and
// says how it ended in a notice with the last of its output. The whole of it
// is kept, until the action runs again, for the page to show.
type ranCommand struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Started time.Time `json:"started"`
	stop    context.CancelFunc
	stopped bool
	command string
	out     *output
	ended   string // the notice's title, once it has
}

// commandLines is how much of a command's output its notice quotes.
const commandLines = 30

// output keeps the last outputBytes of what a command prints.
type output struct {
	mu  sync.Mutex
	buf []byte
	cut bool
}

const outputBytes = 1 << 20

func (o *output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.buf = append(o.buf, p...)
	// Trimmed once twice the size kept, not on every write once full.
	if len(o.buf) > 2*outputBytes {
		o.buf = append([]byte(nil), o.buf[len(o.buf)-outputBytes:]...)
		o.cut = true
	}
	return len(p), nil
}

// String is what is kept, from its first whole line if its start was let go.
func (o *output) String() (string, bool) {
	o.mu.Lock()
	b, cut := o.buf, o.cut
	if len(b) > outputBytes {
		b, cut = b[len(b)-outputBytes:], true
	}
	s := string(b)
	o.mu.Unlock()
	if _, rest, ok := strings.Cut(s, "\n"); ok && cut {
		s = rest
	}
	return strings.ToValidUTF8(s, "�"), cut
}

// lastLines is the end of s, n lines of it.
func lastLines(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	i := len(s)
	for range n {
		if i = strings.LastIndexByte(s[:i], '\n'); i < 0 {
			return s
		}
	}
	return s[i+1:]
}

// runCommand starts a's command, one run of an action at a time.
func (s *Server) runCommand(a actionRun) error {
	if a.ID == "" {
		return errors.New("the action has no id")
	}
	sh, err := exec.LookPath("sh")
	if err != nil {
		return errors.New("commands run with sh, which is not on PATH")
	}
	s.actionsMu.Lock()
	if s.commands == nil {
		s.commands = map[string]*ranCommand{}
	}
	if s.commands[a.ID] != nil {
		s.actionsMu.Unlock()
		return fmt.Errorf("%s is running already", a.Name)
	}
	ctx, stop := context.WithCancel(context.Background())
	run := &ranCommand{ID: a.ID, Name: a.Name, Started: time.Now(), stop: stop, command: a.Command, out: &output{}}
	s.commands[a.ID] = run
	if s.ran == nil {
		s.ran = map[string]*ranCommand{}
	}
	s.ran[a.ID] = run
	s.actionsMu.Unlock()

	cmd := exec.CommandContext(ctx, sh, "-e", "-c", a.Command)
	cmd.Dir = s.repo.Root
	cmd.Env = append(os.Environ(), "DV_REPO="+s.repo.MainRoot(), "DV_WORKTREE="+s.repo.Root, "DV_BRANCH="+s.repo.Head().Branch)
	cmd.Stdout, cmd.Stderr = run.out, run.out
	ownGroup(cmd)
	// What it started can hold its output open after it ends.
	cmd.WaitDelay = 10 * time.Second
	go func() {
		defer stop()
		err := cmd.Run()
		text, _ := run.out.String()
		s.actionsMu.Lock()
		delete(s.commands, a.ID)
		n := s.commandNotice(a, lastLines(text, commandLines), err, run.stopped)
		run.ended = n.Title
		closing := s.closing
		s.actionsMu.Unlock()
		// Stopped by the folder closing, whose notices are settled already.
		if !closing {
			s.notices.Raise(n)
		}
	}()
	return nil
}

func (s *Server) commandNotice(a actionRun, tail string, err error, stopped bool) notify.Notice {
	n := notify.Notice{ID: noticeID(), Kind: notify.Done, Action: a.ID, Title: a.Name + " is done", Where: firstLine(a.Command), Body: clip(tail), Format: notify.Code}
	var exit *exec.ExitError
	switch {
	case stopped:
		n.Title = a.Name + " was stopped"
	case errors.As(err, &exit):
		n.Title = fmt.Sprintf("%s failed (exit %d)", a.Name, exit.ExitCode())
	case err != nil:
		n.Title, n.Body, n.Format = a.Name+" failed", err.Error(), ""
	}
	return n
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// handleCommands lists the command actions running, and the ones whose
// output is kept: { running, ran }.
func (s *Server) handleCommands(w http.ResponseWriter, r *http.Request) {
	running, ran := []ranCommand{}, []string{}
	s.actionsMu.Lock()
	for _, c := range s.commands {
		running = append(running, *c)
	}
	for id := range s.ran {
		ran = append(ran, id)
	}
	s.actionsMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"running": running, "ran": ran})
}

// handleStopCommand stops an action's command, and what it started.
func (s *Server) handleStopCommand(w http.ResponseWriter, r *http.Request) {
	s.actionsMu.Lock()
	c := s.commands[r.PathValue("id")]
	if c != nil {
		c.stopped = true
		c.stop()
	}
	s.actionsMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"stopped": c != nil})
}

// handleCommandOutput is what an action's command printed in its latest run,
// as much as is kept: { name, command, title, running, output, cut }.
func (s *Server) handleCommandOutput(w http.ResponseWriter, r *http.Request) {
	s.actionsMu.Lock()
	c := s.ran[r.PathValue("id")]
	var title string
	if c != nil {
		title = cmp.Or(c.ended, c.Name+" is running")
	}
	running := c != nil && c.ended == ""
	s.actionsMu.Unlock()
	if c == nil {
		writeErr(w, http.StatusNotFound, errors.New("it has not run since dv started"))
		return
	}
	text, cut := c.out.String()
	writeJSON(w, http.StatusOK, map[string]any{
		"name": c.Name, "command": c.command, "title": title, "running": running, "output": text, "cut": cut,
	})
}

// stopCommands stops every command running, as the folder closes.
func (s *Server) stopCommands() {
	s.actionsMu.Lock()
	defer s.actionsMu.Unlock()
	s.closing = true
	for _, c := range s.commands {
		c.stopped = true
		c.stop()
	}
}

func (s *Server) commandsRunning() bool {
	s.actionsMu.Lock()
	defer s.actionsMu.Unlock()
	return len(s.commands) > 0
}
