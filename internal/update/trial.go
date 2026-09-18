package update

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
)

// TrialEnv marks a dv started by Try, and TrialMark begins the line in which
// it says where it listens.
const (
	TrialEnv  = "DV_TRIAL"
	TrialMark = "dv trial listening on "
)

const trialWait = 20 * time.Second

// Try runs the dv now installed at this process's path, with its arguments,
// as a trial: it starts as it would for real, but on a port of its own and
// touching nothing it would share with this dv. Try returns once the trial has
// served its page and said its version - want, unless that is empty - and
// stops it; or says why it could not start.
func Try(ctx context.Context, want string) error {
	if pathErr != nil {
		return fmt.Errorf("Cannot tell where dv is installed: %w", pathErr)
	}
	ctx, cancel := context.WithTimeout(ctx, trialWait)
	defer cancel()
	cmd := exec.Command(Path, os.Args[1:]...)
	cmd.Env = append(os.Environ(), TrialEnv+"=1")
	var said tail
	cmd.Stderr = &said
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("The new dv would not run: %w", err)
	}
	found := make(chan string, 1)
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			if addr, ok := strings.CutPrefix(sc.Text(), TrialMark); ok {
				select {
				case found <- addr:
				default:
				}
			}
		}
	}()

	var addr string
	select {
	case addr = <-found:
	case <-ended:
		cmd.Wait()
		return fmt.Errorf("The new dv did not start: %s", said.last())
	case <-ctx.Done():
		stop(cmd, ended)
		return fmt.Errorf("The new dv did not start within %s", trialWait)
	}
	defer stop(cmd, ended)
	return serves(ctx, "http://"+addr, want)
}

// serves checks that a dv answers its page, and at the version wanted.
func serves(ctx context.Context, url, want string) error {
	for _, p := range []string{"/", "/api/restart"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+p, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("The new dv started but did not answer: %w", err)
		}
		var run struct {
			Version string `json:"version"`
		}
		if p == "/api/restart" {
			err = json.NewDecoder(resp.Body).Decode(&run)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK || err != nil {
			return fmt.Errorf("The new dv started, but %s answered %s", p, resp.Status)
		}
		if p == "/api/restart" && want != "" && run.Version != want {
			return fmt.Errorf("The new dv says it is %s, not %s", run.Version, want)
		}
	}
	return nil
}

// stop ends a trial as it would stop by itself, then outright if it lingers.
func stop(cmd *exec.Cmd, ended <-chan struct{}) {
	if cmd.Process.Signal(syscall.SIGTERM) != nil { // Windows has no SIGTERM to send
		cmd.Process.Kill()
	}
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		cmd.Process.Kill()
		<-ended
	}
	cmd.Wait()
}

// tail keeps the end of what a trial wrote to stderr, which says why it failed.
type tail struct {
	mu  sync.Mutex
	buf []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	if len(t.buf) > 2048 {
		t.buf = t.buf[len(t.buf)-2048:]
	}
	return len(p), nil
}

func (t *tail) last() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := strings.TrimSpace(string(t.buf))
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	if s == "" {
		return "it exited without saying why"
	}
	return s
}
