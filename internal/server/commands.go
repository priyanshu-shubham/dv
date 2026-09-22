package server

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"dv/internal/notify"
)

// A command action runs with sh in the folder, as a worktree hook does, and
// says how it ended in a notice with the last of its output.
type ranCommand struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Started time.Time `json:"started"`
	stop    context.CancelFunc
	stopped bool
}

// commandLines is how much of a command's output its notice quotes.
const commandLines = 30

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
	run := &ranCommand{ID: a.ID, Name: a.Name, Started: time.Now(), stop: stop}
	s.commands[a.ID] = run
	s.actionsMu.Unlock()

	cmd := exec.CommandContext(ctx, sh, "-e", "-c", a.Command)
	cmd.Dir = s.repo.Root
	cmd.Env = append(os.Environ(), "DV_REPO="+s.repo.MainRoot(), "DV_WORKTREE="+s.repo.Root, "DV_BRANCH="+s.repo.Head().Branch)
	go func() {
		defer stop()
		tail, err := runTail(cmd)
		s.actionsMu.Lock()
		delete(s.commands, a.ID)
		stopped, closing := run.stopped, s.closing
		s.actionsMu.Unlock()
		// Stopped by the folder closing, whose notices are settled already.
		if !closing {
			s.notices.Raise(s.commandNotice(a, tail, err, stopped))
		}
	}()
	return nil
}

// runTail runs cmd, keeping the last lines of what it prints.
func runTail(cmd *exec.Cmd) (string, error) {
	out, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	cmd.Stderr = cmd.Stdout
	ownGroup(cmd)
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Start(); err != nil {
		return "", err
	}
	var tail []string
	sc := bufio.NewScanner(out)
	sc.Buffer(nil, 1<<20)
	for sc.Scan() {
		if tail = append(tail, sc.Text()); len(tail) > commandLines {
			tail = tail[1:]
		}
	}
	// A line too long to scan stops the scanner, not the command, which would
	// block once the pipe filled.
	if sc.Err() != nil {
		io.Copy(io.Discard, out)
	}
	return strings.TrimRight(strings.Join(tail, "\n"), "\n"), cmd.Wait()
}

func (s *Server) commandNotice(a actionRun, tail string, err error, stopped bool) notify.Notice {
	n := notify.Notice{ID: noticeID(), Kind: notify.Done, Title: a.Name + " is done", Where: firstLine(a.Command), Body: clip(tail), Format: notify.Code}
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

// handleCommands lists the command actions running: { running }.
func (s *Server) handleCommands(w http.ResponseWriter, r *http.Request) {
	running := []ranCommand{}
	s.actionsMu.Lock()
	for _, c := range s.commands {
		running = append(running, *c)
	}
	s.actionsMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"running": running})
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
