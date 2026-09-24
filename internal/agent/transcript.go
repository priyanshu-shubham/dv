package agent

import (
	"bytes"
	"cmp"
	"encoding/json"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"dv/internal/permit"
	"dv/internal/store"
)

// Item is one thing in a conversation as the page draws it.
type Item struct {
	Key  string `json:"key"`
	Kind string `json:"kind"` // prompt | command | output | text | thinking | tool | note | compact | mode | model | peer
	At   string `json:"at,omitempty"`
	Text string `json:"text,omitempty"`

	// On a prompt: the transcript entry it is, and the assistant message before
	// it, which resuming at rewinds the conversation to just before the prompt.
	UUID   string `json:"uuid,omitempty"`
	Before string `json:"before,omitempty"`
	Images int    `json:"images,omitempty"` // fetched one by one, by the prompt's uuid
	// Answer marks a prompt that is only the reader's words on an answer to a
	// call, which Codex takes as a message of its own: "yes" or "no".
	Answer string `json:"answer,omitempty"`

	Tool   string          `json:"tool,omitempty"`
	ToolID string          `json:"toolId,omitempty"`
	Input  json.RawMessage `json:"input,omitempty"`
	Result *Result         `json:"result,omitempty"`
	// Task is how a call left running in the background ended, once it has.
	Task *Task `json:"task,omitempty"`
	// Dropped is a call its turn ended without a result for, as when the
	// process running it was stopped mid-call: not running, however busy.
	Dropped bool `json:"dropped,omitempty"`

	Error bool `json:"error,omitempty"` // a note about something that failed, or a command's error

	// Turn marks what began a turn: a message or command sent while idle, or a
	// background task ending. One sent while Claude worked joins its turn.
	Turn bool `json:"turn,omitempty"`
	// Took is, on a turn's end ("worked"), how long it went on, in milliseconds.
	Took int64 `json:"took,omitempty"`

	// On a compaction, whose Text is Claude's summary of what came before.
	Compacted *Compacted `json:"compacted,omitempty"`

	// On a change of mode, whose Text is the new one; on a peer's message, the
	// session that sent it. On a change of model, the models, and as Text
	// Claude Code's notice of why, when dv caught it.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

type Compacted struct {
	Auto   bool `json:"auto,omitempty"`
	Before int  `json:"before,omitempty"` // tokens
	After  int  `json:"after,omitempty"`
}

// Result is what a tool call came back with.
type Result struct {
	Text    string `json:"text"`
	IsError bool   `json:"isError,omitempty"`
	// Edited is set when the call changed a file. The diff itself is fetched
	// on its own, when its card comes into view.
	Edited *Edited `json:"edited,omitempty"`
	Detail *Detail `json:"detail,omitempty"`
	// Took is how long the call ran, in milliseconds. Claude Code records no
	// such thing, so for Claude it runs from the call to its result, and takes
	// in any wait for the reader to allow it.
	Took int64 `json:"took,omitempty"`
	// Said is what the reader wrote with their answer to the call, which went
	// to the agent beside it; Yes says the answer allowed it.
	Said string `json:"said,omitempty"`
	Yes  bool   `json:"yes,omitempty"`
}

// Detail is what a result records beyond its text, for the one line that
// stands for the call; the rest is fetched when the line is opened. Which
// fields are set depends on the tool.
type Detail struct {
	Kind        string `json:"kind,omitempty"`  // Read: text, image, pdf or notebook; Grep: its mode
	Start       int    `json:"start,omitempty"` // Read: the first line read
	Lines       int    `json:"lines,omitempty"` // read, matched or printed
	Total       int    `json:"total,omitempty"` // Read: the file's lines
	Files       *int   `json:"files,omitempty"` // Grep, Glob
	More        bool   `json:"more,omitempty"`  // Glob found more than it listed
	Width       int    `json:"width,omitempty"` // an image, as Claude was shown it
	Height      int    `json:"height,omitempty"`
	Interrupted bool   `json:"interrupted,omitempty"`
	Status      string `json:"status,omitempty"` // WebFetch: "200 OK"
	Bytes       int    `json:"bytes,omitempty"`
	Results     *int   `json:"results,omitempty"` // WebSearch
	// Background names the task a call left running, whose result so far
	// only says it started.
	Background string `json:"background,omitempty"`
}

// Task is the end of a call's background work.
type Task struct {
	Status  string `json:"status"` // completed | failed | killed | stopped
	Summary string `json:"summary,omitempty"`
}

type Edited struct {
	Path    string `json:"path"` // relative to the repository when inside it
	InRepo  bool   `json:"inRepo"`
	Adds    int    `json:"adds"`
	Dels    int    `json:"dels"`
	Created bool   `json:"created,omitempty"`
}

// Past these a tool's output and the text in its arguments are cut: the rows
// that show them are summaries, and a long session carries thousands.
const (
	maxResult = 4000
	maxInput  = 4000
)

// entry is one line of a transcript, reduced to what the page draws from it.
type entry struct {
	uuid, parent string
	seq          int // its place in the file
	assistant    bool
	msg          string // the API message an assistant entry is one block of
	items        []Item
	// On an assistant entry, the context its request filled, and the model
	// and effort it was asked of.
	tokens int
	model  string
	effort string
	mode   string // the permission mode last recorded when it was written
}

// Transcript is a session's file as read so far. Entries form a tree - a
// rewind branches it - and the conversation is the path from the last entry
// back to the first.
type Transcript struct {
	root    string
	entries map[string]*entry
	// Tool results by tool_use id, whichever branch they are on: calls made
	// together are chained one after another, but each result hangs off its
	// own call, so all but one are off the path the conversation takes.
	results map[string]*Result
	tasks   map[string]*Task  // by the tool_use id that started them
	started map[string]string // background task id -> tool_use id
	calls   map[string]string // when each call still without a result was made
	// Compaction boundaries by the entry the conversation had reached. Claude
	// Code carries on from that entry in a chain that goes around the boundary,
	// so it is placed by where it falls in the file.
	compacts map[string][]*entry
	fed      int
	last     string
	// Assistant entries by the API message they are blocks of. Calls made
	// together are one message: the conversation can go through its first
	// block alone, when that call's result was written last.
	msgs map[string][]*entry
	// Claude Code records the permission mode on each prompt and, now and
	// then, on a line of its own, a little after it changes.
	mode string
	// An agent's own transcript is all sidechain, which in a session's is
	// the agents' part, left out.
	sidechain bool
	rest      []byte // a last line not yet finished
	Offset    int64  // bytes fed so far
	// Where in the file it was read from: a long one is read from its latest
	// compaction, what came before only when asked for.
	Start int64
}

// tailFrom is how long a transcript is before it is read from its latest
// compaction: shorter, it takes no time to read whole.
const tailFrom = 4 << 20

// tailBack is how many lines before the latest compaction are read with it. The
// conversation after one goes on from the last entry before its boundary.
const tailBack = 50

// tail is where to start reading a transcript for its latest compaction, and
// what Claude Code last recorded the permission mode as before there. 0 is the
// start.
func tail(data []byte) (int, string) {
	if len(data) < tailFrom {
		return 0, ""
	}
	end := len(data)
	for {
		i := bytes.LastIndex(data[:end], []byte(`"subtype":"compact_boundary"`))
		if i < 0 {
			return 0, ""
		}
		start := bytes.LastIndexByte(data[:i], '\n') + 1
		stop := len(data)
		if j := bytes.IndexByte(data[i:], '\n'); j >= 0 {
			stop = i + j
		}
		end = start
		if !bytes.Contains(data[start:stop], []byte(`"type":"system"`)) {
			continue // written inside something else, as a tool's output
		}
		for n := 0; n < tailBack && start > 0; n++ {
			start = bytes.LastIndexByte(data[:start-1], '\n') + 1
		}
		mode := ""
		if j := bytes.LastIndex(data[:start], []byte(`"permissionMode":"`)); j >= 0 {
			rest := data[j+len(`"permissionMode":"`):]
			if k := bytes.IndexByte(rest, '"'); k > 0 {
				mode = string(rest[:k])
			}
		}
		return start, mode
	}
}

func newTranscript(root string) *Transcript {
	return &Transcript{
		root: root, entries: map[string]*entry{}, results: map[string]*Result{},
		tasks: map[string]*Task{}, started: map[string]string{}, calls: map[string]string{}, compacts: map[string][]*entry{}, msgs: map[string][]*entry{},
	}
}

// Feed takes the next bytes of the file, reporting whether they added to it.
func (t *Transcript) Feed(data []byte) bool {
	t.Offset += int64(len(data))
	data = append(t.rest, data...)
	added := false
	for {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		if e := t.parse(data[:i]); e != nil {
			t.fed++
			e.seq = t.fed
			t.entries[e.uuid] = e
			t.last = e.uuid
			if e.msg != "" {
				t.msgs[e.msg] = append(t.msgs[e.msg], e)
			}
			added = true
		}
		data = data[i+1:]
	}
	t.rest = append([]byte(nil), data...)
	return added
}

// Written is how many blocks of an API message have reached the file.
func (t *Transcript) Written(msg string) int { return len(t.msgs[msg]) }

// Last is the entry the conversation currently ends at.
func (t *Transcript) Last() string { return t.last }

// Has reports whether the transcript holds an entry.
func (t *Transcript) Has(uuid string) bool { return t.entries[uuid] != nil }

// Context is how many tokens the conversation ending at leaf fills - what its
// last request to the model held - and the model that answered it. A reply
// with no usage, as an error Claude Code writes itself, is passed over.
func (t *Transcript) Context(leaf string) (int, string) {
	used := 0
	chain := t.chain(leaf)
	for i, e := range chain {
		// A compaction since leaves what it kept, until a reply says otherwise.
		for _, b := range t.boundaries(chain, i) {
			used = cmp.Or(used, b.items[0].Compacted.After)
		}
		if e.tokens > 0 {
			return cmp.Or(used, e.tokens), e.model
		}
	}
	return used, ""
}

// Effort is what the last reply in the conversation ending at leaf was asked
// to think at, where Claude Code recorded it.
func (t *Transcript) Effort(leaf string) string {
	for _, e := range t.chain(leaf) {
		if e.effort != "" {
			return e.effort
		}
	}
	return ""
}

// chain is the conversation ending at leaf, or at the last entry when leaf is
// not one of them, from its end back.
func (t *Transcript) chain(leaf string) []*entry {
	if t.entries[leaf] == nil {
		leaf = t.last
	}
	var chain []*entry
	for id, seen := leaf, map[string]bool{}; id != "" && !seen[id]; {
		e := t.entries[id]
		if e == nil {
			break
		}
		seen[id] = true
		chain = append(chain, e)
		id = e.parent
	}
	return chain
}

// boundaries are the compactions between chain[i] and the entry after it,
// newest first: chain[i] itself when it is one, and those the chain went
// around.
func (t *Transcript) boundaries(chain []*entry, i int) []*entry {
	var got []*entry
	e := chain[i]
	if i > 0 {
		for _, b := range slices.Backward(t.compacts[e.uuid]) {
			if b.seq < chain[i-1].seq {
				got = append(got, b)
			}
		}
	}
	if len(e.items) > 0 && e.items[0].Compacted != nil {
		got = append(got, e)
	}
	return got
}

// Items is the conversation ending at leaf, or at the last entry when leaf is
// not one of them, with the changes of mode made in dv where they were made.
func (t *Transcript) Items(leaf string, switches ...store.Switch) []Item {
	chain := t.chain(leaf)
	items := []Item{}
	before := ""
	// recorded is the mode the transcript last wrote, which its entries go on
	// carrying past a switch; shown is the one the page last said.
	recorded, shown := "", ""
	after := map[string][]store.Switch{}
	for _, sw := range switches {
		after[sw.After] = append(after[sw.After], sw)
	}
	drawn := map[string]bool{} // messages
	// The model the last reply came from, and whether a move off it since is
	// accounted for: by /model, a pick in dv or Claude Code's own notice.
	answering, explained := "", false
	for i := len(chain) - 1; i >= 0; i-- {
		e := chain[i]
		if e.assistant && e.model != "" && e.model != "<synthetic>" {
			if answering != "" && e.model != answering && !explained {
				items = append(items, Item{Key: "model:" + e.uuid, Kind: "model", From: answering, To: e.model})
			}
			answering, explained = e.model, false
		}
		// Where the conversation first shows a new mode; the first it shows is
		// the one it began in.
		if e.mode != "" && e.mode != recorded {
			if recorded != "" && e.mode != shown {
				items = append(items, Item{Key: "mode:" + e.uuid, Kind: "mode", Text: e.mode, From: shown})
			}
			recorded, shown = e.mode, e.mode
		}
		group := []*entry{e}
		if e.msg != "" {
			group = nil
			if !drawn[e.msg] {
				drawn[e.msg], group = true, t.msgs[e.msg]
			}
		}
		for _, g := range group {
			for _, it := range g.items {
				if it.Compacted != nil {
					continue // placed with the ones gone around
				}
				switch it.Kind {
				case "prompt", "command":
					it.Before = before
					if it.Kind == "command" && (it.Text == "/model" || strings.HasPrefix(it.Text, "/model ")) {
						explained = true
					}
				case "tool":
					it.Result, it.Task = t.results[it.ToolID], t.tasks[it.ToolID]
				}
				items = append(items, it)
			}
		}
		for _, b := range slices.Backward(t.boundaries(chain, i)) {
			items = append(items, b.items[0])
		}
		for n, sw := range after[e.uuid] {
			if sw.Model != nil {
				explained = true
				if sw.Model.Why != "" {
					items = append(items, Item{Key: "fallback:" + e.uuid + ":" + strconv.Itoa(n), Kind: "model", Text: sw.Model.Why, From: sw.Model.From, To: sw.Model.To})
				}
				continue
			}
			if sw.To != shown {
				items = append(items, Item{Key: "switch:" + e.uuid + ":" + strconv.Itoa(n), Kind: "mode", Text: sw.To, From: shown})
				shown = sw.To
			}
		}
		if e.assistant {
			before = e.uuid
		}
	}
	ended := false
	for i := len(items) - 1; i >= 0; i-- {
		switch it := &items[i]; {
		case it.Turn || it.Kind == "worked":
			ended = true
		case ended && it.Kind == "tool" && it.Result == nil:
			it.Dropped = true
		}
	}
	return compactedBy(items)
}

// Mode is the mode the conversation ending at leaf is left in: the last the
// transcript recorded, or a switch made in dv since.
func (t *Transcript) Mode(leaf string, switches []store.Switch) string {
	after := map[string]string{}
	for _, sw := range switches {
		if sw.Model == nil {
			after[sw.After] = sw.To
		}
	}
	recorded, mode := "", t.mode
	for _, e := range slices.Backward(t.chain(leaf)) {
		if e.mode != "" && e.mode != recorded {
			recorded, mode = e.mode, e.mode
		}
		if to, ok := after[e.uuid]; ok {
			mode = to
		}
	}
	return mode
}

// compactedBy puts a /compact before the compaction it made, which Claude Code
// writes first, and leaves out its word that it compacted: the rule says so.
func compactedBy(items []Item) []Item {
	for i := 0; i < len(items); i++ {
		name, _, _ := strings.Cut(items[i].Text, " ")
		if items[i].Kind != "command" || name != "/compact" {
			continue
		}
		j := i
		for j > 0 && items[j-1].Kind == "compact" {
			j--
		}
		if j == i {
			continue
		}
		cmd := items[i]
		copy(items[j+1:i+1], items[j:i])
		items[j] = cmd
		if i+1 < len(items) && items[i+1].Kind == "output" && strings.HasPrefix(items[i+1].Text, "Compacted") {
			items = slices.Delete(items, i+1, i+2)
		}
	}
	return items
}

type rawEntry struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	UUID      string          `json:"uuid"`
	Parent    string          `json:"parentUuid"`
	Logical   string          `json:"logicalParentUuid"`
	Sidechain bool            `json:"isSidechain"`
	Meta      bool            `json:"isMeta"`
	Compact   bool            `json:"isCompactSummary"`
	APIError  bool            `json:"isApiErrorMessage"`
	Timestamp string          `json:"timestamp"`
	Effort    string          `json:"effort"` // an assistant line's
	Content   json.RawMessage `json:"content"` // a system line's
	Origin    struct {
		Kind string `json:"kind"`
	} `json:"origin"`
	Message struct {
		ID      string          `json:"id"`
		Content json.RawMessage `json:"content"`
		Model   string          `json:"model"`
		Usage   struct {
			Input       int `json:"input_tokens"`
			CacheCreate int `json:"cache_creation_input_tokens"`
			CacheRead   int `json:"cache_read_input_tokens"`
			Output      int `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
	ToolUseResult json.RawMessage `json:"toolUseResult"`
	DurationMs    int64           `json:"durationMs"` // a turn_duration line's
	Compaction    struct {
		Trigger string `json:"trigger"`
		Before  int    `json:"preTokens"`
		After   int    `json:"postTokens"`
	} `json:"compactMetadata"`
	Attachment struct {
		Type        string          `json:"type"`
		Prompt      json.RawMessage `json:"prompt"`
		CommandMode string          `json:"commandMode"`
		Content     json.RawMessage `json:"content"`
		ToolUseID   string          `json:"toolUseID"`
	} `json:"attachment"`

	Cwd         string `json:"cwd"`
	GitBranch   string `json:"gitBranch"`
	AITitle     string `json:"aiTitle"`
	CustomTitle string `json:"customTitle"`
	LastPrompt  string `json:"lastPrompt"`

	PermissionMode string `json:"permissionMode"`
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	Thinking  string          `json:"thinking"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

func (t *Transcript) parse(line []byte) *entry {
	var r rawEntry
	if json.Unmarshal(line, &r) != nil || r.Sidechain != t.sidechain {
		return nil
	}
	t.mode = cmp.Or(r.PermissionMode, t.mode)
	if r.UUID == "" {
		return nil
	}
	e := &entry{uuid: r.UUID, parent: r.Parent, mode: t.mode}
	// A compaction starts the chain again; the conversation before it still
	// reads as part of this one.
	if e.parent == "" {
		e.parent = r.Logical
	}
	note := func(text string, failed bool) {
		e.items = append(e.items, Item{Key: r.UUID, Kind: "note", At: r.Timestamp, Text: text, Error: failed})
	}

	switch r.Type {
	case "assistant":
		e.assistant, e.msg = true, r.Message.ID
		u := r.Message.Usage
		e.tokens, e.model, e.effort = u.Input+u.CacheCreate+u.CacheRead+u.Output, r.Message.Model, r.Effort
		var blocks []block
		json.Unmarshal(r.Message.Content, &blocks)
		for i, b := range blocks {
			key := r.UUID + ":" + strconv.Itoa(i)
			switch b.Type {
			case "text":
				if r.APIError {
					note(b.Text, true)
				} else if strings.TrimSpace(b.Text) != "" {
					e.items = append(e.items, Item{Key: key, Kind: "text", At: r.Timestamp, Text: b.Text})
				}
			case "thinking":
				if strings.TrimSpace(b.Thinking) != "" {
					e.items = append(e.items, Item{Key: key, Kind: "thinking", At: r.Timestamp, Text: b.Thinking})
				}
			case "tool_use", "server_tool_use":
				e.items = append(e.items, Item{Key: b.ID, Kind: "tool", At: r.Timestamp, Tool: b.Name, ToolID: b.ID, Input: trimInput(b.Input)})
				t.calls[b.ID] = r.Timestamp
			}
		}

	case "user":
		text, images, blocks := contentOf(r.Message.Content)
		// A result's record can hold a whole file, and is read once.
		var ed *toolUseResult
		for _, b := range blocks {
			if b.Type == "tool_result" {
				if ed == nil {
					ed = &toolUseResult{}
					if json.Unmarshal(r.ToolUseResult, ed) != nil {
						ed = &toolUseResult{}
					}
				}
				res := t.result(b, ed)
				if at, ok := t.calls[b.ToolUseID]; ok {
					delete(t.calls, b.ToolUseID)
					res.Took = between(at, r.Timestamp)
				}
				t.results[b.ToolUseID] = res
				t.track(b.ToolUseID, ed)
				images = 0 // what a result showed Claude, not something said
			}
		}
		if r.Compact {
			if b := t.entries[r.Parent]; b != nil && len(b.items) > 0 && b.items[0].Compacted != nil {
				b.items[0].Text = summary(text)
			} else {
				note("Earlier messages were summarised to make room", false)
			}
			break
		}
		t.finished(text)
		// Another session's message comes in as a meta line when this one is idle.
		if text == "" && images == 0 || r.Meta && r.Origin.Kind != "peer" {
			break
		}
		t.prompt(e, r, text, images, note, true)

	case "attachment":
		// A message sent while Claude was busy joins the turn as an attachment.
		if r.Attachment.Type == "queued_command" {
			text, images, _ := contentOf(r.Attachment.Prompt)
			t.finished(text)
			if text != "" || images > 0 {
				t.prompt(e, r, text, images, note, false)
			}
		}
		// A note with a yes reaches Claude from a hook, after the call's result.
		if res := t.results[r.Attachment.ToolUseID]; res != nil && r.Attachment.Type == "hook_additional_context" {
			var told []string
			json.Unmarshal(r.Attachment.Content, &told)
			for _, s := range told {
				if w, yes, ok := permit.Said(s); ok && yes {
					// A copy: items already handed out hold the old one.
					said := *res
					said.Said, said.Yes = w, true
					t.results[r.Attachment.ToolUseID] = &said
				}
			}
		}

	case "system":
		// Written by the terminal at the end of a turn; a session run headless has none.
		if r.Subtype == "turn_duration" && r.DurationMs > 0 {
			e.items = append(e.items, Item{Key: r.UUID, Kind: "worked", At: r.Timestamp, Took: r.DurationMs})
		}
		if r.Subtype == "local_command" {
			var text string
			json.Unmarshal(r.Content, &text)
			output(e, r, text)
		}
		if r.Subtype == "compact_boundary" {
			c := r.Compaction
			e.items = append(e.items, Item{Key: r.UUID, Kind: "compact", At: r.Timestamp, Compacted: &Compacted{Auto: c.Trigger == "auto", Before: c.Before, After: c.After}})
			// Its logical parent may be written after it, on a chain that leads
			// back to it.
			if t.last != "" {
				e.parent = t.last
			}
			t.compacts[e.parent] = append(t.compacts[e.parent], e)
		}
	}
	return e
}

var (
	commandName = regexp.MustCompile(`<command-name>([^<]*)</command-name>`)
	commandArgs = regexp.MustCompile(`(?s)<command-args>(.*?)</command-args>`)
	taskSummary = regexp.MustCompile(`(?s)<summary>(.*?)</summary>`)
	peerMessage = regexp.MustCompile(`(?s)<cross-session-message\b([^>]*)>(.*?)(?:</cross-session-message>|$)`)
	peerName    = regexp.MustCompile(`from-name="([^"]*)"`)
	taskTool    = regexp.MustCompile(`<tool-use-id>([^<]*)</tool-use-id>`)
	taskID      = regexp.MustCompile(`<task-id>([^<]*)</task-id>`)
	taskStatus  = regexp.MustCompile(`<status>([^<]*)</status>`)
	localOutput = regexp.MustCompile(`(?s)<local-command-std(?:out|err)>(.*?)</local-command-std(?:out|err)>`)
)

// summary is a compaction's summary without what Claude Code tells Claude
// around it.
func summary(text string) string {
	if _, s, ok := strings.Cut(text, "\nSummary:\n"); ok {
		text = s
	}
	for _, tail := range []string{"\nIf you need specific details from before compaction", "\nContinue the conversation from where it left off"} {
		if i := strings.LastIndex(text, tail); i >= 0 {
			text = text[:i]
		}
	}
	return strings.TrimSpace(text)
}

// track notes a call that left work running in the background, and a TaskStop
// that ended some.
func (t *Transcript) track(toolID string, r *toolUseResult) {
	background := r.Background
	if r.Async && background == "" {
		background = r.AgentID
	}
	if background != "" {
		t.started[background] = toolID
	}
	if id := t.started[r.Stopped]; id != "" && t.tasks[id] == nil {
		t.tasks[id] = &Task{Status: "stopped"}
	}
}

// finished reads the notification Claude Code sends when background work ends.
func (t *Transcript) finished(text string) {
	if !strings.HasPrefix(text, "<task-notification>") {
		return
	}
	id := firstGroup(taskTool, text)
	if id == "" {
		id = t.started[firstGroup(taskID, text)]
	}
	if id != "" {
		t.tasks[id] = &Task{Status: firstGroup(taskStatus, text), Summary: strings.TrimSpace(firstGroup(taskSummary, text))}
	}
}

// contentOf is a message's content, a string or blocks: its text, and how many
// images came with it.
func contentOf(raw json.RawMessage) (text string, images int, blocks []block) {
	if isString(raw) {
		json.Unmarshal(raw, &text)
		return text, 0, nil
	}
	json.Unmarshal(raw, &blocks)
	var parts []string
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, b.Text)
		case "image":
			images++
		}
	}
	return strings.Join(parts, "\n"), images, blocks
}

// prompt files what someone - or something standing in for them - said.
// turn is whether it begins one, rather than joining the one going on.
func (t *Transcript) prompt(e *entry, r rawEntry, text string, images int, note func(string, bool), turn bool) {
	switch {
	case strings.HasPrefix(text, "[Request interrupted"):
		note("Interrupted", false)
	case strings.HasPrefix(text, "<local-command-caveat>"):
	// A command that has Claude do something leads with its message.
	case strings.HasPrefix(text, "<command-name>") || strings.HasPrefix(text, "<command-message>"):
		name := strings.TrimSpace(firstGroup(commandName, text))
		if args := strings.TrimSpace(firstGroup(commandArgs, text)); args != "" {
			name += " " + args
		}
		e.items = append(e.items, Item{Key: r.UUID, Kind: "command", At: r.Timestamp, Text: name, UUID: r.UUID, Turn: turn})
	case strings.HasPrefix(text, "<local-command-std"):
		output(e, r, text)
	// A command run with !. The terminal writes what it printed on a line of its
	// own after it; dv sends the two as one message.
	case strings.HasPrefix(text, "<bash-input>"):
		it := Item{Key: r.UUID, Kind: "shell", At: r.Timestamp, Text: strings.TrimSpace(firstGroup(bashInput, text)), UUID: r.UUID, Turn: turn}
		if bashStdout.MatchString(text) || bashStderr.MatchString(text) {
			it.Result = shellOutput(text)
		}
		e.items = append(e.items, it)
	case strings.HasPrefix(text, "<bash-stdout>") || strings.HasPrefix(text, "<bash-stderr>"):
		if p := t.entries[r.Parent]; p != nil && len(p.items) > 0 && p.items[len(p.items)-1].Kind == "shell" {
			p.items[len(p.items)-1].Result = shellOutput(text)
		}
	case r.Origin.Kind == "task-notification" || strings.HasPrefix(text, "<task-notification>"):
		note(strings.TrimSpace(firstGroup(taskSummary, text)), false)
		e.items[len(e.items)-1].Turn = turn
	// Sent by another session with SendMessage. Busy, this one gets it as a
	// queued message, which has only the tag to say so.
	case r.Origin.Kind == "peer" || strings.HasPrefix(text, "<cross-session-message"):
		m := peerMessage.FindStringSubmatch(text)
		if m == nil {
			break
		}
		e.items = append(e.items, Item{Key: r.UUID, Kind: "peer", At: r.Timestamp, From: firstGroup(peerName, m[1]), Text: strings.TrimSpace(m[2]), Turn: turn})
	default:
		e.items = append(e.items, Item{Key: r.UUID, Kind: "prompt", At: r.Timestamp, Text: text, Images: images, UUID: r.UUID, Turn: turn})
	}
}

// output files what a command printed. Claude Code writes it as a line of its
// own, lately a system line and before that a user one.
func output(e *entry, r rawEntry, text string) {
	out := strings.TrimSpace(ansi.ReplaceAllString(firstGroup(localOutput, text), ""))
	if out == "" {
		return
	}
	e.items = append(e.items, Item{Key: r.UUID, Kind: "output", At: r.Timestamp, Text: out, Error: strings.HasPrefix(text, "<local-command-stderr>")})
}

func (t *Transcript) result(b block, ed *toolUseResult) *Result {
	text := resultText(b)
	res := &Result{IsError: b.IsError, Text: cut(text, maxResult)}
	if w, yes, ok := permit.Said(text); ok && !yes && b.IsError {
		res.Said = w
	}
	res.Detail = ed.detail()
	if ed.FilePath == "" || ed.Patch == nil && ed.Type != "create" {
		return res
	}
	e := &Edited{Path: ed.FilePath}
	if rel, err := filepath.Rel(t.root, ed.FilePath); err == nil && filepath.IsLocal(rel) {
		e.Path, e.InRepo = filepath.ToSlash(rel), true
	}
	if ed.Type == "create" && ed.Content != nil {
		e.Created = true
		e.Adds = strings.Count(strings.TrimSuffix(*ed.Content, "\n"), "\n") + 1
	}
	for _, h := range ed.Patch {
		for _, l := range h.Lines {
			switch {
			case strings.HasPrefix(l, "+"):
				e.Adds++
			case strings.HasPrefix(l, "-"):
				e.Dels++
			}
		}
	}
	res.Edited = e
	return res
}

// toolUseResult is the record Claude Code keeps of a call's result, in the
// shapes of the tools dv shows more of than their text.
type toolUseResult struct {
	Type     string  `json:"type"` // Write: create | update; Read: text | image | pdf | notebook
	FilePath string  `json:"filePath"`
	Patch    []hunk  `json:"structuredPatch"`
	Content  *string `json:"content"`

	File *struct {
		FilePath   string `json:"filePath"`
		Content    string `json:"content"`
		StartLine  int    `json:"startLine"`
		NumLines   int    `json:"numLines"`
		TotalLines int    `json:"totalLines"`
		Base64     string `json:"base64"`
		MediaType  string `json:"type"`
		Dimensions *struct {
			Width  int `json:"displayWidth"`
			Height int `json:"displayHeight"`
		} `json:"dimensions"`
	} `json:"file"`

	Mode      string          `json:"mode"`
	NumFiles  *int            `json:"numFiles"`
	NumLines  int             `json:"numLines"`
	Filenames []string        `json:"filenames"`
	Truncated bool            `json:"truncated"`
	Stdout    *string         `json:"stdout"`
	Stderr    string          `json:"stderr"`
	Interrupt bool            `json:"interrupted"`
	Code      int             `json:"code"`
	CodeText  string          `json:"codeText"`
	Bytes     int             `json:"bytes"`
	URL       string          `json:"url"`
	Page      string          `json:"result"`
	Query     *string         `json:"query"`
	Results   json.RawMessage `json:"results"`

	Background string `json:"backgroundTaskId"`
	Async      bool   `json:"isAsync"`
	AgentID    string `json:"agentId"`
	Stopped    string `json:"task_id"` // TaskStop's
}

// A failed call's record is only its error as a string, which the result's
// own text already says.
func (r *toolUseResult) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return nil
	}
	type record toolUseResult
	return json.Unmarshal(b, (*record)(r))
}

// detail tells the tools apart by what their records hold, since a result
// line does not name its tool.
func (r *toolUseResult) detail() *Detail {
	switch {
	case r.File != nil && r.Type != "":
		d := &Detail{Kind: r.Type, Start: r.File.StartLine, Lines: r.File.NumLines, Total: r.File.TotalLines}
		if r.File.Dimensions != nil {
			d.Width, d.Height = r.File.Dimensions.Width, r.File.Dimensions.Height
		}
		return d
	case r.NumFiles != nil:
		return &Detail{Kind: r.Mode, Files: r.NumFiles, Lines: r.NumLines, More: r.Truncated}
	case r.Stdout != nil:
		return &Detail{Lines: lineCount(*r.Stdout) + lineCount(r.Stderr), Interrupted: r.Interrupt, Background: r.Background}
	case r.Code != 0:
		return &Detail{Status: strings.TrimSpace(strconv.Itoa(r.Code) + " " + r.CodeText), Bytes: r.Bytes}
	case r.Query != nil && r.Results != nil:
		n := len(searchLinks(r.Results))
		return &Detail{Results: &n}
	case r.Async && r.AgentID != "":
		return &Detail{Background: r.AgentID}
	}
	return nil
}

func lineCount(s string) int {
	s = strings.TrimRight(s, "\n")
	if strings.TrimSpace(s) == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

// resultText is the text of a tool_result, which is a string or text blocks.
func resultText(b block) string {
	var text string
	if isString(b.Content) {
		json.Unmarshal(b.Content, &text)
		return text
	}
	var parts []block
	json.Unmarshal(b.Content, &parts)
	var s []string
	for _, p := range parts {
		if p.Type == "text" {
			s = append(s, p.Text)
		}
	}
	return strings.Join(s, "\n")
}

// isString tells content that is a string from content that is blocks without
// reading it as one to find out it is the other: it can be a file long.
func isString(raw json.RawMessage) bool {
	s := bytes.TrimLeft(raw, " \t\r\n")
	return len(s) > 0 && s[0] == '"'
}

// trimInput cuts long strings in a tool call's arguments: the rows only show
// the start of a command or a file's content.
func trimInput(raw json.RawMessage) json.RawMessage {
	if len(raw) <= maxInput {
		return raw
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	for k, v := range m {
		if s, ok := v.(string); ok {
			m[k] = cut(s, maxInput)
		}
	}
	out, _ := json.Marshal(m)
	return out
}

// between is the milliseconds from one of the transcript's timestamps to
// another, 0 when either does not read.
func between(from, to string) int64 {
	a, err1 := time.Parse(time.RFC3339Nano, from)
	b, err2 := time.Parse(time.RFC3339Nano, to)
	if err1 != nil || err2 != nil || b.Before(a) {
		return 0
	}
	return b.Sub(a).Milliseconds()
}

// cut shortens s to about n bytes, on a rune boundary, saying so.
func cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "\n…"
}

func firstGroup(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}
