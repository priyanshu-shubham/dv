package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// agyCall is a tool call agy makes, put together from the updates that tell
// of it, and drawn as the call of Claude Code's it comes nearest to.
type agyCall struct {
	id, title, kind, status string
	input                   map[string]any
	diffs                   []agyDiff
	text                    []string
	output                  json.RawMessage
	mcp                     bool
	paths                   []string

	written, done bool
	tool          string
	// The file an edit changes, as it stood before.
	before  *string
	snapped bool
}

type agyDiff struct {
	path     string
	old, new *string
}

func (c *agyCall) merge(u *agyUpdate) {
	if u.Title != nil && *u.Title != "" {
		c.title = *u.Title
	}
	if u.ToolKind != nil && *u.ToolKind != "" {
		c.kind = *u.ToolKind
	}
	if u.Status != nil && *u.Status != "" {
		c.status = *u.Status
	}
	var in map[string]any
	if json.Unmarshal(u.RawInput, &in) == nil {
		for k, v := range in {
			c.input[k] = v
		}
	}
	if len(u.RawOutput) > 0 && string(u.RawOutput) != "null" {
		c.output = u.RawOutput
	}
	for _, l := range u.Locations {
		if l.Path != "" && !slices.Contains(c.paths, l.Path) {
			c.paths = append(c.paths, l.Path)
		}
	}
	var meta struct {
		MCP bool `json:"is_mcp_tool_call"`
	}
	if json.Unmarshal(u.Meta, &meta) == nil && meta.MCP {
		c.mcp = true
	}
	var content []struct {
		Type    string  `json:"type"`
		Path    string  `json:"path"`
		OldText *string `json:"oldText"`
		NewText *string `json:"newText"`
		Content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if len(u.Content) == 0 || json.Unmarshal(u.Content, &content) != nil {
		return
	}
	var diffs []agyDiff
	var text []string
	for _, b := range content {
		switch b.Type {
		case "diff":
			diffs = append(diffs, agyDiff{b.Path, b.OldText, b.NewText})
		case "content":
			if b.Content.Type == "text" && b.Content.Text != "" {
				text = append(text, b.Content.Text)
			}
		}
	}
	if len(diffs) > 0 {
		c.diffs = diffs
	}
	if len(text) > 0 {
		c.text = text
	}
}

// name is agy's tool, out of the title it gives the call: "Run edit_file?",
// "Running run_command", or the bare name.
func (c *agyCall) name() string {
	t := strings.TrimSpace(c.title)
	t = strings.TrimSuffix(t, "?")
	for _, p := range []string{"Running ", "Run "} {
		if rest, ok := strings.CutPrefix(t, p); ok && !strings.Contains(rest, " ") {
			return rest
		}
	}
	return t
}

// str is the first of the input's keys that holds a string; agy names an
// argument in more than one way across its versions.
func (c *agyCall) str(keys ...string) string {
	for _, k := range keys {
		if s, ok := c.input[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func (c *agyCall) num(keys ...string) int {
	for _, k := range keys {
		if f, ok := c.input[k].(float64); ok {
			return int(f)
		}
	}
	return 0
}

var (
	commandKeys = []string{"CommandLine", "command_line", "commandLine", "command"}
	fileKeys    = []string{"TargetFile", "target_file", "file_path", "AbsolutePath", "absolute_path", "path"}
)

func (c *agyCall) isEdit() bool {
	switch c.kind {
	case "edit", "delete", "move":
		return true
	}
	switch c.name() {
	case "write_to_file", "replace_file_content", "multi_replace_file_content", "edit_file":
		return true
	}
	return len(c.diffs) > 0
}

// path is the file the call acts on.
func (c *agyCall) path() string {
	if len(c.diffs) > 0 && c.diffs[0].path != "" {
		return c.diffs[0].path
	}
	if p := c.str(fileKeys...); p != "" {
		return p
	}
	if len(c.paths) > 0 {
		return c.paths[0]
	}
	return ""
}

// snapshot keeps the file an edit is about to change, the first time its path
// is known, for the diff once it has.
func (c *agyCall) snapshot() {
	if c.snapped || !c.isEdit() || c.status == "completed" || c.status == "failed" {
		return
	}
	path := c.path()
	if path == "" || !filepath.IsAbs(path) {
		return
	}
	c.snapped = true
	if fi, err := os.Stat(path); err == nil && !fi.IsDir() && fi.Size() < maxEditFile {
		if b, err := os.ReadFile(path); err == nil {
			s := string(b)
			c.before = &s
		}
	}
}

// maxEditFile is the largest file whose edit dv keeps a whole diff of.
const maxEditFile = 2 << 20

// claude is the call as Claude Code would have made it: the tool the page
// draws, with the arguments it reads.
func (c *agyCall) claude() (string, map[string]any) {
	name := c.name()
	switch {
	case c.kind == "execute" || c.str(commandKeys...) != "":
		in := map[string]any{"command": c.str(commandKeys...)}
		if in["command"] == "" {
			in["command"] = c.title
		}
		return "Bash", in

	case c.isEdit():
		path := c.path()
		if len(c.diffs) == 1 && c.diffs[0].old == nil && c.diffs[0].new != nil {
			return "Write", map[string]any{"file_path": path, "content": *c.diffs[0].new}
		}
		if len(c.diffs) > 0 && c.diffs[0].old != nil && c.diffs[0].new != nil {
			return "Edit", map[string]any{"file_path": path, "old_string": *c.diffs[0].old, "new_string": *c.diffs[0].new}
		}
		if s := c.str("CodeContent", "code_content"); s != "" {
			return "Write", map[string]any{"file_path": path, "content": s}
		}
		in := map[string]any{"file_path": path}
		if old := c.str("TargetContent", "target_content"); old != "" {
			in["old_string"], in["new_string"] = old, c.str("ReplacementContent", "replacement_content")
		}
		return "Edit", in

	case c.kind == "read" || name == "view_file" || name == "client_view_file":
		in := map[string]any{"file_path": c.path()}
		if s := c.num("start_line", "StartLine"); s > 0 {
			in["offset"] = s
			if e := c.num("end_line", "EndLine"); e >= s {
				in["limit"] = e - s + 1
			}
		}
		return "Read", in

	case name == "list_dir":
		return "LS", map[string]any{"path": c.str("DirectoryPath", "directory_path", "path")}

	case name == "grep_search":
		return "Grep", map[string]any{"pattern": c.str("Query", "query", "Pattern", "pattern"), "path": c.str("SearchPath", "search_path", "path")}

	case name == "find_by_name":
		return "Glob", map[string]any{"pattern": c.str("Pattern", "pattern", "Query", "query"), "path": c.str("SearchDirectory", "search_directory", "path")}

	case c.kind == "search" || name == "search_web" || name == "Web search":
		return "WebSearch", map[string]any{"query": c.str("query", "Query")}

	case c.kind == "fetch" || name == "read_url_content":
		return "WebFetch", map[string]any{"url": c.str("Url", "url", "URL")}

	case !c.mcp && (strings.HasSuffix(name, "_subagent")):
		in := map[string]any{"description": c.str("Role", "role"), "prompt": c.str("Prompt", "prompt"), "subagent_type": c.str("TypeName", "type_name")}
		if list, ok := c.input["Subagents"].([]any); ok && len(list) > 0 {
			if s, ok := list[0].(map[string]any); ok {
				in["description"], in["prompt"], in["subagent_type"] = s["Role"], s["Prompt"], s["TypeName"]
			}
		}
		return "Agent", in
	}
	if name == "" {
		name = "Antigravity"
	}
	in := map[string]any{}
	for k, v := range c.input {
		in[k] = v
	}
	if c.title != "" && c.title != name {
		in["title"] = c.title
	}
	return name, in
}

type agyResult struct {
	text   string
	failed bool
	record map[string]any
}

// agyExit finds a command's exit code in the header agy puts over its output:
// "Created At: …\nCompleted At: …\nCommand exited with code 0.\n\nCommand output:\n…".
var agyExit = regexp.MustCompile(`Command exited with code (-?\d+)`)

// result is what the call came back with, and the record of it that lets
// the page show more than its text: an edit's diff, a command's output.
func (c *agyCall) result() *agyResult {
	r := &agyResult{failed: c.status == "failed", text: strings.Join(c.text, "\n")}
	var out map[string]any
	var outText string
	if json.Unmarshal(c.output, &outText) != nil {
		json.Unmarshal(c.output, &out)
	}
	if r.text == "" {
		for _, k := range []string{"combinedOutput", "formatted_output", "output", "error", "message"} {
			if s, ok := out[k].(string); ok && s != "" {
				r.text = s
				break
			}
		}
	}
	if r.text == "" && outText != "Tool execution" {
		r.text = outText
	}
	switch c.tool {
	case "Bash":
		code := 0
		for _, k := range []string{"exit_code", "exitCode"} {
			if f, ok := out[k].(float64); ok {
				code = int(f)
				break
			}
		}
		if m := agyExit.FindStringSubmatch(r.text); m != nil {
			fmt.Sscan(m[1], &code)
		}
		if _, output, ok := strings.Cut(r.text, "Command output:\n"); ok {
			r.text = output
		}
		r.text = strings.TrimRight(strings.ReplaceAll(r.text, "\r\n", "\n"), "\n")
		if code != 0 {
			r.failed = true
			r.text = strings.TrimSpace(fmt.Sprintf("Exit code %d\n%s", code, r.text))
		}
		stdout := r.text
		r.record = map[string]any{"stdout": stdout, "stderr": "", "interrupted": false}
	case "Edit", "Write":
		if r.failed {
			break
		}
		path := c.path()
		after := readSmall(path)
		before := c.before
		// Not caught before agy changed it: the diff agy sent, undone.
		if after != nil && (before == nil && c.hadFile() || before != nil && *before == *after) {
			if d := c.diffFor(path); d != nil && d.old != nil && d.new != nil && strings.Contains(*after, *d.new) {
				s := strings.Replace(*after, *d.new, *d.old, 1)
				before = &s
			}
		}
		r.record = editRecord(path, before, after)
		if r.text == "" {
			r.text = "The file " + path + " has been updated."
		}
	}
	if r.text == "" {
		if r.failed {
			r.text = "Failed"
		} else {
			r.text = "Done"
		}
	}
	return r
}

// hadFile reports whether the edit was to a file there already, as one with
// old text is.
func (c *agyCall) hadFile() bool {
	d := c.diffFor(c.path())
	return d != nil && d.old != nil
}

func (c *agyCall) diffFor(path string) *agyDiff {
	for i := range c.diffs {
		if c.diffs[i].path == path || c.diffs[i].path == "" {
			return &c.diffs[i]
		}
	}
	return nil
}

func readSmall(path string) *string {
	if path == "" {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() || fi.Size() >= maxEditFile {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	s := string(b)
	return &s
}
