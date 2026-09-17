// Package agent follows and runs Claude Code sessions for the Agent view.
//
// What a session looks like comes from its transcript, the JSONL file Claude
// Code appends to as the conversation goes, whoever is running it: dv reads the
// file as it grows. A session dv runs itself is a `claude -p` process speaking
// stream-json, which adds what the file does not carry - the reply as it
// streams, and the permission prompts to answer.
package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"dv/internal/permit"
)

// maxFolder is where Claude Code stops spelling a working directory out in its
// folder name and adds a hash dv cannot reproduce. Such folders are matched on
// the part it can, and the transcript's own cwd decides.
const maxFolder = 200

// folderName is the folder Claude Code files a working directory's transcripts
// under: every UTF-16 unit that is not an ASCII letter or digit becomes a dash.
func folderName(dir string) string {
	var b strings.Builder
	for _, r := range dir {
		switch {
		case r < 128 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'):
			b.WriteRune(r)
		case r > 0xFFFF:
			b.WriteString(strings.Repeat("-", len(utf16.Encode([]rune{r}))))
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// transcriptFiles lists the transcripts that may belong to sessions started in
// root or a folder under it. A folder name is not unique - /a/b-c and /a/b/c
// share one - so the caller checks each file's cwd.
func transcriptFiles(root string) map[string]string {
	cfg, err := permit.ConfigDir()
	if err != nil {
		return nil
	}
	base := filepath.Join(cfg, "projects")
	dirs, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	name := folderName(root)
	out := map[string]string{}
	for _, d := range dirs {
		n := d.Name()
		mine := n == name || strings.HasPrefix(n, name+"-") ||
			len(name) > maxFolder && strings.HasPrefix(n, name[:maxFolder])
		if !d.IsDir() || !mine {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(base, n))
		for _, f := range files {
			if id, ok := strings.CutSuffix(f.Name(), ".jsonl"); ok && !f.IsDir() {
				out[id] = filepath.Join(base, n, f.Name())
			}
		}
	}
	return out
}

// subagentFiles are the transcripts of the agents a session started, by the id
// of the call that started each, which a meta file beside each one names.
func subagentFiles(path, id string) map[string]string {
	metas, _ := filepath.Glob(filepath.Join(filepath.Dir(path), id, "subagents", "agent-*.meta.json"))
	out := map[string]string{}
	for _, meta := range metas {
		var m struct {
			Call string `json:"toolUseId"`
		}
		if b, err := os.ReadFile(meta); err == nil && json.Unmarshal(b, &m) == nil && m.Call != "" {
			out[m.Call] = strings.TrimSuffix(meta, ".meta.json") + ".jsonl"
		}
	}
	return out
}

// within reports whether dir is root or somewhere under it.
func within(dir, root string) bool {
	return dir == root || strings.HasPrefix(dir, root+string(filepath.Separator))
}

// running is a Claude Code process as it describes itself in sessions/<pid>.json.
type running struct {
	PID        int    `json:"pid"`
	SessionID  string `json:"sessionId"`
	Kind       string `json:"kind"`
	Entrypoint string `json:"entrypoint"`
	Status     string `json:"status"` // busy | idle
}

// runningSessions are the sessions some live Claude Code process has open, by
// session id. A process that died without cleaning up leaves its file behind,
// so each one's pid is checked.
func runningSessions() map[string]running {
	cfg, err := permit.ConfigDir()
	if err != nil {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(cfg, "sessions", "*.json"))
	out := map[string]running{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		var r running
		if err != nil || json.Unmarshal(b, &r) != nil || r.SessionID == "" || !alive(r.PID) {
			continue
		}
		out[r.SessionID] = r
	}
	return out
}
