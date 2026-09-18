package agent

import (
	"bytes"
	"cmp"
	"encoding/json"
	"io"
	"os"
	"slices"
	"strings"
	"time"
)

// Session is one row of the Agent view's list.
type Session struct {
	ID      string    `json:"id"`
	Agent   string    `json:"agent,omitempty"` // "codex"; "" is Claude Code
	Title   string    `json:"title"`
	Prompt  string    `json:"prompt"` // the last thing asked, which names a session with no title
	Cwd     string    `json:"cwd"`
	Branch  string    `json:"branch,omitempty"`
	Updated time.Time `json:"updated"`
	// Where it runs now: "dv", "terminal", or "" when nothing has it open.
	Running string `json:"running,omitempty"`
	Busy    bool   `json:"busy,omitempty"`
	Status  string `json:"status,omitempty"` // "compacting", when dv runs it and it is
	Open    bool   `json:"open"`
	// Temporary leaves the list once nothing has it open.
	Temporary bool `json:"temporary,omitempty"`
	// The last thing said in it, by "you" or "claude" ("agent" for Codex).
	Last   string `json:"last,omitempty"`
	LastBy string `json:"lastBy,omitempty"`
	// How full its context is, as of the last reply the end of the file has.
	Context *Context `json:"context,omitempty"`

	used  int
	model string
}

// lastWords is how much of the last thing said a row keeps.
const lastWords = 240

// A transcript can run to tens of megabytes, and listing reads every one, so
// only its two ends are read: the start has where it ran and the first prompt,
// the end the latest title and prompt.
const summaryEnds = 128 << 10

// summarize reads a transcript's list row, or reports that it is not a
// conversation - one with nothing said in it yet, say.
func summarize(path string) (Session, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Session{}, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Session{}, false
	}
	s := Session{Updated: fi.ModTime()}

	head := make([]byte, min(fi.Size(), summaryEnds))
	if _, err := io.ReadFull(f, head); err != nil {
		return Session{}, false
	}
	tail := head
	if fi.Size() > summaryEnds {
		tail = make([]byte, summaryEnds)
		if _, err := f.ReadAt(tail, fi.Size()-summaryEnds); err != nil {
			return Session{}, false
		}
		// Cut back to whole lines at both edges.
		head = head[:bytes.LastIndexByte(head, '\n')+1]
		tail = tail[bytes.IndexByte(tail, '\n')+1:]
	}

	first := ""
	eachLine(head, func(r *rawEntry) bool {
		if s.Cwd == "" && r.Cwd != "" {
			s.Cwd, s.Branch = r.Cwd, r.GitBranch
		}
		if first == "" {
			first = promptOf(r)
		}
		return s.Cwd == "" || first == ""
	})
	custom, ai := "", ""
	said := func(text, by string) {
		// What went with a message from dv is not what was said.
		text, _, _ = strings.Cut(text, "<dv-context>")
		if text = strings.Join(strings.Fields(text), " "); text != "" {
			s.Last, s.LastBy = cut(text, lastWords), by
		}
	}
	eachLine(tail, func(r *rawEntry) bool {
		if r.CustomTitle != "" {
			custom = r.CustomTitle
		}
		if r.AITitle != "" {
			ai = r.AITitle
		}
		if r.LastPrompt != "" {
			s.Prompt = r.LastPrompt
		}
		if r.GitBranch != "" {
			s.Branch = r.GitBranch
		}
		switch {
		case r.Sidechain:
		case r.Type == "assistant" && !r.APIError:
			if u := r.Message.Usage; u.Input+u.CacheCreate+u.CacheRead+u.Output > 0 {
				s.used, s.model = u.Input+u.CacheCreate+u.CacheRead+u.Output, r.Message.Model
			}
			var blocks []block
			json.Unmarshal(r.Message.Content, &blocks)
			for _, b := range blocks {
				if b.Type == "text" {
					said(b.Text, "claude")
				}
			}
		case r.Type == "system" && r.Subtype == "compact_boundary":
			s.used = r.Compaction.After
		default:
			said(promptOf(r), "you")
		}
		return true
	})
	// A title someone gave the session holds; the AI's is redone as it goes on.
	s.Title = cmp.Or(custom, ai)
	if s.Prompt == "" {
		s.Prompt = first
	}
	if s.Cwd == "" || s.Prompt == "" && s.Title == "" {
		return Session{}, false
	}
	// Claude Code keeps the prompt with what dv sent along with it, which names nothing.
	s.Prompt, _, _ = strings.Cut(s.Prompt, "<dv-context>")
	s.Prompt = strings.TrimSpace(s.Prompt)
	return s, true
}

// lastReply is what Claude said last in a transcript, whole and as written:
// its text since its last tool call, "" if the reader has spoken since.
func lastReply(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	tail := make([]byte, min(fi.Size(), summaryEnds))
	if _, err := f.ReadAt(tail, fi.Size()-int64(len(tail))); err != nil {
		return ""
	}
	var said []string
	eachLine(tail, func(r *rawEntry) bool {
		switch {
		case r.Sidechain:
		case r.Type == "assistant" && !r.APIError:
			var blocks []block
			json.Unmarshal(r.Message.Content, &blocks)
			for _, b := range blocks {
				switch b.Type {
				case "text":
					said = append(said, strings.TrimSpace(b.Text))
				case "tool_use":
					said = nil
				}
			}
		case promptOf(r) != "":
			said = nil
		}
		return true
	})
	return strings.Join(said, "\n\n")
}

// eachLine decodes the lines that can matter to a summary, until fn says stop.
func eachLine(b []byte, fn func(*rawEntry) bool) {
	for len(b) > 0 {
		line := b
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			line, b = b[:i], b[i+1:]
		} else {
			b = nil
		}
		var r rawEntry
		if json.Unmarshal(line, &r) != nil {
			continue
		}
		if !fn(&r) {
			return
		}
	}
}

// promptOf is what a line says someone asked, or "".
func promptOf(r *rawEntry) string {
	var text string
	var blocks []block
	switch {
	case r.Type == "user" && !r.Meta && !r.Compact && r.Origin.Kind != "task-notification":
		text, _, blocks = contentOf(r.Message.Content)
	case r.Type == "attachment" && r.Attachment.Type == "queued_command":
		text, _, _ = contentOf(r.Attachment.Prompt)
	}
	if slices.ContainsFunc(blocks, func(b block) bool { return b.Type == "tool_result" }) {
		return ""
	}
	if strings.HasPrefix(text, "<bash-input>") {
		return "!" + strings.TrimSpace(firstGroup(bashInput, text))
	}
	if strings.HasPrefix(text, "<") || strings.HasPrefix(text, "[Request interrupted") {
		return ""
	}
	return strings.TrimSpace(text)
}
