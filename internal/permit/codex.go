package permit

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"dv/internal/store"
)

// RunCodexNotify is `dv codex notify`, Codex's notify program in a terminal a
// Codex action opened: it passes the reply Codex finished on to the dv that
// terminal is in. Codex adds the event as the last argument, and its own
// environment carries the terminal's DV_TERMINAL and DV_PROMPT.
func RunCodexNotify(event string) {
	var in struct {
		Type    string   `json:"type"`
		Thread  string   `json:"thread-id"`
		Cwd     string   `json:"cwd"`
		Input   []string `json:"input-messages"`
		Message string   `json:"last-assistant-message"`
	}
	terminal := os.Getenv("DV_TERMINAL")
	if json.Unmarshal([]byte(event), &in) != nil || in.Type != "agent-turn-complete" || terminal == "" {
		return
	}
	srv, ok := store.FindServer(in.Cwd)
	if !ok {
		return
	}
	// Codex also finishes turns of its own on other threads, such as naming the
	// conversation; the action's thread is the one its prompt began.
	first := len(in.Input) == 1 && strings.TrimSpace(in.Input[0]) == strings.TrimSpace(os.Getenv("DV_PROMPT"))
	body, _ := json.Marshal(map[string]any{"terminal": terminal, "thread": in.Thread, "first": first, "message": in.Message})
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/codex/notify", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	store.MarkLocal(req)
	client := http.Client{Timeout: 5 * time.Second}
	if resp, err := client.Do(req); err == nil {
		resp.Body.Close()
	}
}
