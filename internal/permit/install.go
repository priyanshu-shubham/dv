package permit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// hookEvents are the events `dv claude hook` serves. A prompt may wait on the
// reader for as long as a day; a report that a call ran is answered at once.
var hookEvents = []struct {
	name    string
	timeout int
}{
	{"PermissionRequest", 86400},
	{"PostToolUse", 0},
	{"PostToolUseFailure", 0},
}

var hookArgs = []string{"claude", "hook"}

// handler is one hook as Claude Code's settings spell it. Exec form - args
// rather than a shell line - so a path with a space in it needs no quoting.
type handler struct {
	Type    string   `json:"type"`
	Command string   `json:"command"`
	Args    []string `json:"args,omitempty"`
	Timeout int      `json:"timeout,omitempty"`
}

type group struct {
	Hooks []handler `json:"hooks"`
}

// ConfigDir is where Claude Code keeps its settings and sessions.
func ConfigDir() (string, error) {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}

// SettingsPath is the user's Claude Code settings file.
func SettingsPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "settings.json"), nil
}

// Install puts hooks running exe into the settings file at path, in place of
// any an earlier install left there. The rest of the file is kept as it was,
// down to the order of its keys. It reports whether the file changed.
func Install(path, exe string) (bool, error) {
	// A settings file linked in from dotfiles stays a link.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	orig, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return false, err
	}
	src := orig
	if len(bytes.TrimSpace(src)) == 0 {
		src = []byte("{}")
	}
	top, err := objectFields(src)
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	hooks, err := objectFields(top.get("hooks"))
	if err != nil {
		return false, fmt.Errorf("%s: hooks: %w", path, err)
	}

	for _, ev := range hookEvents {
		var groups []json.RawMessage
		if raw := hooks.get(ev.name); raw != nil {
			if err := json.Unmarshal(raw, &groups); err != nil {
				return false, fmt.Errorf("%s: hooks.%s: %w", path, ev.name, err)
			}
		}
		groups = slices.DeleteFunc(groups, isOurs)
		mine := encode(group{Hooks: []handler{{Type: "command", Command: exe, Args: hookArgs, Timeout: ev.timeout}}})
		hooks.set(ev.name, encode(append(groups, mine)))
	}
	top.set("hooks", hooks.marshal())

	var out bytes.Buffer
	if err := json.Indent(&out, top.marshal(), "", "  "); err != nil {
		return false, err
	}
	out.WriteByte('\n')
	if same(orig, out.Bytes()) {
		return false, nil
	}
	return true, writeFile(path, out.Bytes())
}

// isOurs reports whether a hook group is one Install wrote: nothing in it but
// dv serving its hook.
func isOurs(raw json.RawMessage) bool {
	var g group
	if json.Unmarshal(raw, &g) != nil || len(g.Hooks) == 0 {
		return false
	}
	for _, h := range g.Hooks {
		cmd := strings.Fields(h.Command)
		ok := len(cmd) == 1 && slices.Equal(h.Args, hookArgs) ||
			len(cmd) == 3 && len(h.Args) == 0 && slices.Equal(cmd[1:], hookArgs)
		if !ok || filepath.Base(cmd[0]) != "dv" {
			return false
		}
	}
	return true
}

// encode is json.Marshal without its HTML escaping, which would turn every &&
// in someone's hook commands into &&.
func encode(v any) json.RawMessage {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return bytes.TrimRight(b.Bytes(), "\n")
}

// same compares two JSON documents as text less its layout, so a file that
// only differs in indentation is not rewritten.
func same(a, b []byte) bool {
	var x, y bytes.Buffer
	return json.Compact(&x, a) == nil && json.Compact(&y, b) == nil && bytes.Equal(x.Bytes(), y.Bytes())
}

func writeFile(path string, b []byte) error {
	mode := fs.FileMode(0o644)
	if fi, err := os.Stat(path); err == nil {
		mode = fi.Mode().Perm()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".dv-tmp"
	if err := os.WriteFile(tmp, b, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// fields is a JSON object's members in the order the file has them. Going
// through a map would sort them, and rewrite the whole of someone's settings
// to add three hooks.
type fields []member

type member struct {
	key string
	val json.RawMessage
}

func objectFields(raw json.RawMessage) (fields, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("not a JSON object")
	}
	var f fields
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, err
		}
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, err
		}
		f = append(f, member{t.(string), v})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	return f, nil
}

func (f fields) get(key string) json.RawMessage {
	for _, m := range f {
		if m.key == key {
			return m.val
		}
	}
	return nil
}

func (f *fields) set(key string, v json.RawMessage) {
	for i := range *f {
		if (*f)[i].key == key {
			(*f)[i].val = v
			return
		}
	}
	*f = append(*f, member{key, v})
}

func (f fields) marshal() json.RawMessage {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, m := range f {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(encode(m.key))
		b.WriteByte(':')
		b.Write(m.val)
	}
	b.WriteByte('}')
	return b.Bytes()
}
