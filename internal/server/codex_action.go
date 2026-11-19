package server

import (
	"os/exec"
	"strings"

	"dv/internal/gateway"
)

// A Codex action runs its prompt through the Codex CLI itself, `codex exec`,
// as a command action runs: its output, and a notice when it is done. Codex
// models in a Claude Code session go through dv's gateway instead.

func codexInstalled() bool {
	_, err := exec.LookPath("codex")
	return err == nil
}

// codexActionModels are the models a Codex action can run on, as `codex exec
// -m` takes them.
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
// comes nearest to: `codex exec` asks nothing, so what it may do is set before
// it starts.
func codexSandbox(mode string) string {
	switch mode {
	case "danger-full-access", "bypassPermissions":
		return "danger-full-access"
	case "workspace-write", "acceptEdits", "auto", "autoAskEdits":
		return "workspace-write"
	}
	return "read-only"
}

func codexArgs(a actionRun) []string {
	args := []string{"codex", "exec", "--skip-git-repo-check", "-s", codexSandbox(a.Mode)}
	if a.Model != "" {
		args = append(args, "-m", strings.TrimPrefix(a.Model, gateway.ModelPrefix))
	}
	if a.Effort != "" {
		args = append(args, "-c", "model_reasoning_effort="+a.Effort)
	}
	return append(args, "--", a.Prompt)
}
