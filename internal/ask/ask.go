// Package ask runs the local `claude` CLI against a slice of the diff so the
// reviewer can ask what a change does without leaving the page. It shells out
// to the CLI rather than calling the API directly: the CLI already holds the
// user's credentials and model access, so there is no key to configure here.
package ask

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Model is one entry in the picker.
type Model struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Note  string `json:"note,omitempty"`
}

// Models are the choices offered in the UI. Sonnet 5 is the default: it is the
// right balance of speed and depth for "explain this hunk".
var Models = []Model{
	{ID: "claude-sonnet-5", Label: "Sonnet 5", Note: "default - fast and thorough"},
	{ID: "claude-opus-5", Label: "Opus 5", Note: "deepest reasoning"},
	{ID: "claude-haiku-4-5-20251001", Label: "Haiku 4.5", Note: "quickest"},
	{ID: "claude-fable-5", Label: "Fable 5", Note: "creative"},
}

const DefaultModel = "claude-sonnet-5"

// Valid reports whether id is one of the offered models. Anything else is
// refused rather than passed through to the CLI.
func Valid(id string) bool {
	for _, m := range Models {
		if m.ID == id {
			return true
		}
	}
	return false
}

// Available reports whether the CLI is installed, so the UI can hide the
// feature instead of failing at the first click.
func Available() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

const systemPrompt = `You are helping a developer read a code diff in a local review tool.

Answer the question about the diff you are given. Be concrete and brief: lead with the
answer, then the reasoning. Reference specific lines, identifiers and files. If the change
looks wrong or risky, say so plainly. If the diff alone does not settle the question, use
Read, Grep and Glob to look at the rest of the repository before answering.

Format as short markdown. No preamble, no restating the question, no summary of what you
are about to do.`

// Event is one item in the stream sent to the browser.
type Event struct {
	Type      string  `json:"type"` // delta | done | error
	Text      string  `json:"text,omitempty"`
	Cost      float64 `json:"cost,omitempty"`
	Duration  int     `json:"durationMs,omitempty"`
	Model     string  `json:"model,omitempty"`
	SessionID string  `json:"sessionId,omitempty"`
}

// Run streams an answer, calling emit for each event. It returns when the CLI
// exits or ctx is cancelled. A non-empty resume id continues an existing CLI
// session, which is what makes follow-up questions cheap: the diff and the
// earlier turns are already in that session's context.
func Run(ctx context.Context, dir, model, prompt, resume string, emit func(Event)) error {
	if !Valid(model) {
		model = DefaultModel
	}
	args := []string{
		"--print",
		"--model", model,
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--verbose",
		"--system-prompt", systemPrompt,
		// Read-only tools only: the reviewer asked a question, not for edits.
		"--allowedTools", "Read", "Grep", "Glob",
	}
	if resume != "" {
		args = append(args, "--resume", resume)
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(prompt)
	// Without this the CLI can outlive a cancelled request and keep burning
	// tokens for an answer nobody is reading any more.
	cmd.WaitDelay = 2 * time.Second

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("the `claude` CLI is not on PATH")
		}
		return err
	}

	var mu sync.Mutex
	streamed := false
	parse(stdout, &mu, &streamed, emit)

	waitErr := cmd.Wait()
	if waitErr != nil && ctx.Err() == nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = waitErr.Error()
		}
		return errors.New(msg)
	}
	return ctx.Err()
}

// parse reads the CLI's stream-json output. Text arrives twice — as partial
// deltas and again in the final result — so the result is only used when
// nothing streamed, which is what happens if a future CLI drops partials.
func parse(r io.Reader, mu *sync.Mutex, streamed *bool, emit func(Event)) {
	session := ""
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var ev struct {
			Type      string `json:"type"`
			SessionID string `json:"session_id"`
			Event     struct {
				Type  string `json:"type"`
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
			} `json:"event"`
			Subtype  string  `json:"subtype"`
			Result   string  `json:"result"`
			IsError  bool    `json:"is_error"`
			Cost     float64 `json:"total_cost_usd"`
			Duration int     `json:"duration_ms"`
		}
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		if ev.SessionID != "" {
			session = ev.SessionID
		}

		switch ev.Type {
		case "stream_event":
			if ev.Event.Type == "content_block_delta" && ev.Event.Delta.Text != "" {
				mu.Lock()
				*streamed = true
				mu.Unlock()
				emit(Event{Type: "delta", Text: ev.Event.Delta.Text})
			}
		case "result":
			mu.Lock()
			already := *streamed
			mu.Unlock()
			if ev.IsError {
				emit(Event{Type: "error", Text: firstNonEmpty(ev.Result, "the CLI reported an error")})
				return
			}
			if !already && ev.Result != "" {
				emit(Event{Type: "delta", Text: ev.Result})
			}
			emit(Event{Type: "done", Cost: ev.Cost, Duration: ev.Duration, SessionID: session})
			return
		}
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
