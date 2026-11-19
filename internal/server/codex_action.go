package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os/exec"
	"strings"

	"dv/internal/gateway"
	"dv/internal/notify"
	"dv/internal/permit"
	"dv/internal/term"
)

// A Codex action runs its prompt through the Codex CLI itself: interactive, in
// a terminal of the Terminal view, or with `codex exec` as a command action
// runs. Codex models in a Claude Code session go through dv's gateway instead.

func codexInstalled() bool {
	_, err := exec.LookPath("codex")
	return err == nil
}

// codexActionModels are the models a Codex action can run on, as `codex -m`
// takes them.
func codexActionModels() []map[string]any {
	out := []map[string]any{}
	gw := gateway.Active()
	if gw == nil {
		return out
	}
	for _, m := range gw.CodexModels() {
		out = append(out, map[string]any{"id": m.Slug, "label": m.Label, "efforts": m.Efforts})
	}
	return out
}

// codexSandbox is the sandbox an action asks for, or that its permission mode
// comes nearest to.
func codexSandbox(mode string) string {
	switch mode {
	case "danger-full-access", "bypassPermissions":
		return "danger-full-access"
	case "workspace-write", "acceptEdits", "auto", "autoAskEdits":
		return "workspace-write"
	}
	return "read-only"
}

func codexOptions(a actionRun) []string {
	args := []string{"-s", codexSandbox(a.Mode)}
	if a.Model != "" {
		args = append(args, "-m", strings.TrimPrefix(a.Model, gateway.ModelPrefix))
	}
	if a.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+a.Effort)
	}
	return args
}

func codexArgs(a actionRun) []string {
	args := append([]string{"codex", "exec", "--skip-git-repo-check"}, codexOptions(a)...)
	return append(args, "--", a.Prompt)
}

// codexLine is what a Codex action types into its terminal. An approval would
// wait there unseen, so the sandbox alone bounds what it does; and each reply
// it finishes is passed to dv by `dv codex notify`, for a notice. The prompt
// comes from $DV_PROMPT, as quoting one many lines long into a shell would
// show as a screenful of continuation prompts. Codex copies to the computer's
// own clipboard unless it takes the session for a remote one, then through
// the terminal (OSC 52), which is the only way a copy reaches the browser.
func codexLine(a actionRun) string {
	args := append([]string{"env", "SSH_CONNECTION=127.0.0.1 0 127.0.0.1 0", "SSH_CLIENT=127.0.0.1 0 0", "codex"}, codexOptions(a)...)
	args = append(args, "-a", "never")
	if exe, err := permit.Exe(); err == nil {
		// JSON's strings are TOML's too.
		argv, _ := json.Marshal([]string{exe, "codex", "notify"})
		args = append(args, "-c", "notify="+string(argv))
	}
	return " " + term.Quote(args) + ` -- "$DV_PROMPT"` + "\r"
}

func (s *Server) runCodexTerminal(a actionRun) (*term.Terminal, error) {
	return s.terms.Run(a.Name, codexLine(a), "DV_PROMPT="+a.Prompt)
}

// handleCodexNotify is a reply Codex finished in one of the folder's
// terminals, from `dv codex notify`: {terminal, thread, first, message}, first
// being the turn the action's prompt began.
func (s *Server) handleCodexNotify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Terminal string `json:"terminal"`
		Thread   string `json:"thread"`
		First    bool   `json:"first"`
		Message  string `json:"message"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	t := s.terms.Get(req.Terminal)
	if t == nil {
		writeErr(w, http.StatusNotFound, errors.New("that terminal is closed"))
		return
	}
	if s.actionThread(req.Terminal, req.Thread, req.First) && s.notices != nil {
		s.notices.Raise(notify.Notice{
			ID: noticeID(), Kind: notify.Done, Terminal: t.ID(), Title: "Codex is done", Where: t.Info().Name,
			Body: clip(strings.TrimSpace(req.Message)), Format: notify.Markdown,
		})
	}
	w.WriteHeader(http.StatusNoContent)
}

// actionThread is whether a turn Codex finished in terminal is on the thread
// the action's prompt began, which first marks.
func (s *Server) actionThread(terminal, thread string, first bool) bool {
	s.actionsMu.Lock()
	defer s.actionsMu.Unlock()
	if first {
		if s.threads == nil {
			s.threads = map[string]string{}
		}
		s.threads[terminal] = thread
	}
	return thread != "" && s.threads[terminal] == thread
}
