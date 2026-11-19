package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// refreshThroughCodex has Codex refresh its own sign-in, with a
// `codex app-server` started for the one call.
func refreshThroughCodex(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "codex", "app-server")
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return errors.New("the `codex` CLI is not on PATH")
		}
		return err
	}
	defer func() {
		stdin.Close()
		done := make(chan struct{})
		go func() { cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
			<-done
		}
	}()

	enc := json.NewEncoder(stdin)
	lines := bufio.NewScanner(stdout)
	lines.Buffer(make([]byte, 64*1024), 16*1024*1024)
	call := func(id int, method string, params any) error {
		if err := enc.Encode(map[string]any{"id": id, "method": method, "params": params}); err != nil {
			return err
		}
		for lines.Scan() {
			var m struct {
				ID    *int `json:"id"`
				Error *struct {
					Message string `json:"message"`
				} `json:"error"`
				Method string `json:"method"`
			}
			if json.Unmarshal(lines.Bytes(), &m) != nil || m.ID == nil || *m.ID != id || m.Method != "" {
				continue
			}
			if m.Error != nil {
				return fmt.Errorf("%s: %s", method, m.Error.Message)
			}
			return nil
		}
		if err := lines.Err(); err != nil {
			return err
		}
		return fmt.Errorf("codex app-server ended before answering %s", method)
	}
	if err := call(1, "initialize", map[string]any{"clientInfo": map[string]any{"name": "dv", "title": "dv", "version": "1"}}); err != nil {
		return err
	}
	if err := enc.Encode(map[string]any{"method": "initialized"}); err != nil {
		return err
	}
	return call(2, "account/read", map[string]any{"refreshToken": true})
}
