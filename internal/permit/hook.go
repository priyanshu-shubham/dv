package permit

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"dv/internal/store"
)

// RunHook is `dv claude hook`, the command Claude Code runs on its hook
// events. It passes the event to the dv serving the session's repository and
// prints the reply. Anything going wrong - no dv there, dv stopped - prints
// nothing, and Claude Code carries on as it would without dv.
func RunHook(stdin io.Reader, stdout io.Writer) {
	body, err := io.ReadAll(stdin)
	if err != nil {
		return
	}
	var in struct {
		Event string `json:"hook_event_name"`
		Cwd   string `json:"cwd"`
		Input struct {
			FilePath string `json:"file_path"`
		} `json:"tool_input"`
	}
	if json.Unmarshal(body, &in) != nil {
		return
	}
	var client http.Client
	switch in.Event {
	case "PermissionRequest":
		// Unbounded: the reader takes the time they take, and Claude Code's own
		// hook timeout is the backstop.
	case "PostToolUse", "PostToolUseFailure":
		client.Timeout = 5 * time.Second
	default:
		return
	}

	srv, ok := store.FindServer(in.Cwd)
	if !ok && in.Input.FilePath != "" {
		srv, ok = store.FindServer(filepath.Dir(in.Input.FilePath))
	}
	if !ok {
		return
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/claude/hook", bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	store.MarkLocal(req)
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	// Read whole before writing any: half a reply is a parse error to Claude Code.
	out, err := io.ReadAll(resp.Body)
	if err == nil && resp.StatusCode == http.StatusOK {
		stdout.Write(out)
	}
}
