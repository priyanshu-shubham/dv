package agent

import (
	"context"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"dv/internal/gateway"
)

// Auto mode has each action checked by Anthropic's API, which answers on the
// reply; a Codex model's reply has no answer. From Claude Code 2.1.322, one
// that can't send telemetry takes that as "ask about everything" unless
// CLAUDE_CODE_AUTO_MODE_SERVER=0 has it check locally instead, on the
// session's model. Before 2.1.322 the same setting turns auto mode off, and
// a Claude model is better checked by Anthropic, so it is set only for a
// Codex model on a Claude Code that needs it.

var claudeVersion = sync.OnceValue(func() []int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "claude", "--version").Output()
	if err != nil {
		return nil
	}
	m := regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`).FindStringSubmatch(string(out))
	if m == nil {
		return nil
	}
	var v []int
	for _, s := range m[1:] {
		n, _ := strconv.Atoi(s)
		v = append(v, n)
	}
	return v
})

// checksLocally says whether a Claude Code on model should check auto
// mode's actions itself.
func checksLocally(model string) bool {
	if os.Getenv("CLAUDE_CODE_AUTO_MODE_SERVER") != "" || gateway.Active() == nil || !strings.HasPrefix(model, gateway.ModelPrefix) {
		return false
	}
	v := claudeVersion()
	return v != nil && slices.Compare(v, []int{2, 1, 322}) >= 0
}

// restartForCheck stops a process started for the other kind of check than
// model needs, while it is idle; the next message starts it again, as an
// idle one is.
func (p *proc) restartForCheck(model string) {
	p.mu.Lock()
	stale := p.cmd != nil && !p.busy && checksLocally(model) != p.localCheck
	p.mu.Unlock()
	if stale {
		go p.stop()
	}
}
