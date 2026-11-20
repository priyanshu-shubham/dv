package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

// helperTimeout is how long a key helper may take before it is stopped.
var helperTimeout = 30 * time.Second

// defaultKeyTTL is how long a helper's key is used where no TTL is set.
const defaultKeyTTL = 5 * time.Minute

// keyHelper runs a provider's key helper, one run at a time, and keeps the
// key it printed for the provider's TTL.
type keyHelper struct {
	mu    sync.Mutex
	cmd   string // the command value is from
	value string
	at    time.Time
	err   error
}

// key is the helper's key: the one kept while it is fresh, or a new one where
// refused, the key the endpoint turned away, is still the one kept - another
// request may have had it made anew already. A failed run is tried again
// only after a while, not on every request.
func (h *keyHelper) key(cfg Provider, refused string) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ttl := defaultKeyTTL
	if cfg.KeyTTL > 0 {
		ttl = time.Duration(cfg.KeyTTL) * time.Second
	}
	if h.cmd == cfg.KeyHelper {
		since := time.Since(h.at)
		switch {
		case refused != "" && refused != h.value && h.err == nil:
			return h.value, nil
		case refused == "" && h.err == nil && since < ttl:
			return h.value, nil
		case refused == "" && h.err != nil && since < 10*time.Second:
			return "", h.err
		}
	}
	h.cmd = cfg.KeyHelper
	h.value, h.err = runHelper(cfg.KeyHelper)
	h.at = time.Now()
	return h.value, h.err
}

func runHelper(command string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), helperTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	// A command the shell started can hold its output open after the shell
	// is stopped; past this, it is no longer waited for.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", fmt.Errorf("the key helper took over %s, so it was stopped", helperTimeout)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = "…" + msg[len(msg)-300:]
		}
		if msg != "" {
			return "", fmt.Errorf("the key helper failed: %v: %s", err, msg)
		}
		return "", fmt.Errorf("the key helper failed: %v", err)
	}
	key := strings.TrimSpace(string(out))
	if key == "" {
		return "", errors.New("the key helper printed no key")
	}
	return key, nil
}
