package agent

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"dv/internal/gitx"
	"dv/internal/store"
)

// An Antigravity session keeps its conversation in agy's own store, in a form
// dv cannot read, so dv writes one beside it as it goes: a transcript in
// Claude Code's shape, under dv's data folder rather than Claude Code's, where
// the terminal's --resume would offer it. Everything that reads a Claude
// session reads it the same.

// agyEntrypoint marks a transcript as Antigravity's. It begins "sdk" as a
// headless Claude Code's does, which the reading takes the same way.
const agyEntrypoint = "sdk-agy"

// agyProjects is where Antigravity sessions' transcripts are kept, in folders
// named as Claude Code names its own.
func agyProjects() (string, error) {
	dir, err := store.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "agy", "projects"), nil
}

func agyTranscriptPath(cwd, id string) (string, error) {
	base, err := agyProjects()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, folderName(cwd), id+".jsonl"), nil
}

// isAgyTranscript reports whether a transcript is one dv writes for agy.
func isAgyTranscript(path string) bool {
	base, err := agyProjects()
	return err == nil && strings.HasPrefix(path, base+string(filepath.Separator))
}

// agyLog appends to an Antigravity session's transcript. Each line hangs off
// the one before, so the conversation is the file in order.
type agyLog struct {
	path, id, cwd string
	wrote         func()

	mu   sync.Mutex
	last string
	mode string // the permission mode put on each prompt
	// held is a line outside the conversation waiting for the first one in
	// it: until something is said, there is to be no transcript.
	held map[string]any
}

func openAgyLog(cwd, id string, wrote func()) (*agyLog, error) {
	path, err := agyTranscriptPath(cwd, id)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	l := &agyLog{path: path, id: id, cwd: cwd, wrote: wrote}
	if b, err := os.ReadFile(path); err == nil {
		l.last = lastUUID(b)
	}
	return l, nil
}

// lastUUID is the uuid of the last line that has one.
func lastUUID(b []byte) string {
	for len(b) > 0 {
		b = bytes.TrimRight(b, "\n")
		i := bytes.LastIndexByte(b, '\n')
		var r struct {
			UUID string `json:"uuid"`
		}
		if json.Unmarshal(b[i+1:], &r) == nil && r.UUID != "" {
			return r.UUID
		}
		b = b[:max(i, 0)]
	}
	return ""
}

func (l *agyLog) append(line map[string]any) error {
	l.mu.Lock()
	uuid, _ := line["uuid"].(string)
	if uuid == "" {
		uuid = newUUID()
	}
	line["uuid"] = uuid
	if l.last != "" {
		line["parentUuid"] = l.last
	} else {
		line["parentUuid"] = nil
	}
	line["timestamp"] = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	line["sessionId"] = l.id
	line["cwd"] = l.cwd
	line["entrypoint"] = agyEntrypoint
	line["isSidechain"] = false
	b, err := json.Marshal(line)
	if err == nil && l.held != nil {
		meta, _ := json.Marshal(l.held)
		b = append(append(meta, '\n'), b...)
	}
	if err == nil {
		var f *os.File
		if f, err = os.OpenFile(l.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			_, err = f.Write(append(b, '\n'))
			f.Close()
		}
	}
	if err == nil {
		l.last, l.held = uuid, nil
	}
	l.mu.Unlock()
	if err == nil && l.wrote != nil {
		l.wrote()
	}
	return err
}

func (l *agyLog) setMode(mode string) {
	l.mu.Lock()
	l.mode = mode
	l.mu.Unlock()
}

// appendMeta writes a line outside the conversation, as Claude Code writes a
// session's title: it has no uuid, and the next line hangs off the one before.
// Before the conversation has begun, it waits for its first line.
func (l *agyLog) appendMeta(line map[string]any) error {
	b, err := json.Marshal(line)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.last == "" {
		l.held = line
		return nil
	}
	f, err := os.OpenFile(l.path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(b, '\n'))
	return err
}

// agyUsage is what a reply's request filled of the context, as far as agy
// says: it reports how much is used, not how it splits.
type agyUsage struct{ input, cached, output int }

// prompt writes a message the user sent, under the uuid the page knows it by.
func (l *agyLog) prompt(uuid, text string, images []Image) error {
	var content []map[string]any
	for _, img := range images {
		content = append(content, map[string]any{"type": "image", "source": map[string]any{
			"type": "base64", "media_type": img.MediaType, "data": base64.StdEncoding.EncodeToString(img.Data),
		}})
	}
	if text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	l.mu.Lock()
	mode := l.mode
	l.mu.Unlock()
	line := map[string]any{"type": "user", "uuid": uuid, "message": map[string]any{"role": "user", "content": content}}
	if mode != "" {
		line["permissionMode"] = mode
	}
	return l.append(line)
}

// assistant writes one block of a reply. Blocks of one reply share msg, as an
// API message's do, so the page drops what it streamed once they are written.
func (l *agyLog) assistant(msg, model string, block map[string]any, usage *agyUsage) error {
	m := map[string]any{"id": msg, "type": "message", "role": "assistant", "model": model, "content": []any{block}}
	if usage != nil {
		m["usage"] = map[string]int{
			"input_tokens": usage.input, "cache_read_input_tokens": usage.cached, "output_tokens": usage.output,
		}
	}
	return l.append(map[string]any{"type": "assistant", "message": m})
}

// result writes what a call came back with, and the record that lets the page
// show more of it than its text.
func (l *agyLog) result(call, text string, failed bool, record map[string]any) error {
	line := map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]any{
		"type": "tool_result", "tool_use_id": call, "content": text, "is_error": failed,
	}}}}
	if record != nil {
		line["toolUseResult"] = record
	}
	return l.append(line)
}

// note writes something dv has to say in the conversation, as an API error
// is: a reply the page draws as a note.
func (l *agyLog) note(text string) error {
	return l.append(map[string]any{
		"type": "assistant", "isApiErrorMessage": true,
		"message": map[string]any{"id": newUUID(), "type": "message", "role": "assistant", "model": "<synthetic>", "content": []any{map[string]any{"type": "text", "text": text}}},
	})
}

// editRecord is Claude Code's record of an edit, from the file before and
// after: enough for the page to count the change and draw its diff.
func editRecord(path string, before, after *string) map[string]any {
	if after == nil {
		return nil
	}
	if before == nil {
		return map[string]any{"type": "create", "filePath": path, "content": *after, "structuredPatch": []hunk{}}
	}
	return map[string]any{
		"type": "update", "filePath": path, "originalFile": *before, "content": *after,
		"structuredPatch": patchOf(*before, *after),
	}
}

// patchOf is the change from a to b as unified-diff hunks with three lines of
// context, as Claude Code records an edit.
func patchOf(a, b string) []hunk {
	const context = 3
	old, new := splitKeep(a), splitKeep(b)
	ops := gitx.DiffLines(old, new)
	var hunks []hunk
	var h *hunk
	for i, op := range ops {
		if op.Kind == gitx.OpEqual {
			if h == nil {
				continue
			}
			n := min(op.OldLen, context)
			last := i == len(ops)-1
			if !last && op.OldLen > 2*context {
				for k := 0; k < n; k++ {
					h.Lines = append(h.Lines, " "+old[op.OldStart+k])
				}
				h.OldLines += n
				h.NewLines += n
				hunks = append(hunks, *h)
				h = nil
				continue
			}
			if last {
				op.OldLen = n
			}
			for k := 0; k < op.OldLen; k++ {
				h.Lines = append(h.Lines, " "+old[op.OldStart+k])
			}
			h.OldLines += op.OldLen
			h.NewLines += op.OldLen
			continue
		}
		if h == nil {
			lead := 0
			if i > 0 && ops[i-1].Kind == gitx.OpEqual {
				lead = min(ops[i-1].OldLen, context)
			}
			h = &hunk{OldStart: op.OldStart - lead + 1, NewStart: op.NewStart - lead + 1, OldLines: lead, NewLines: lead}
			for k := op.OldStart - lead; k < op.OldStart; k++ {
				h.Lines = append(h.Lines, " "+old[k])
			}
		}
		switch op.Kind {
		case gitx.OpDelete:
			for k := 0; k < op.OldLen; k++ {
				h.Lines = append(h.Lines, "-"+old[op.OldStart+k])
			}
			h.OldLines += op.OldLen
		case gitx.OpInsert:
			for k := 0; k < op.NewLen; k++ {
				h.Lines = append(h.Lines, "+"+new[op.NewStart+k])
			}
			h.NewLines += op.NewLen
		}
	}
	if h != nil {
		hunks = append(hunks, *h)
	}
	if hunks == nil {
		hunks = []hunk{}
	}
	return hunks
}

func splitKeep(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
