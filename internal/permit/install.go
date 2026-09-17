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

// Exe is the dv binary for hooks to run: this one, unless it will not be there.
func Exe() (string, error) {
	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return "", err
	}
	// `go run` builds into the temp dir and deletes the binary on exit, which
	// would leave every tool call failing a hook.
	if strings.HasPrefix(exe, os.TempDir()+string(filepath.Separator)) {
		return "", fmt.Errorf("this dv is a temporary build (%s); install it and run that one", exe)
	}
	return exe, nil
}

// Install puts hooks running exe into the settings file at path, in place of
// any an earlier install left there. The rest of the file is kept as it was,
// down to the order of its keys. It reports whether the file changed.
func Install(path, exe string) (bool, error) {
	s, err := readSettings(path)
	if err != nil {
		return false, err
	}
	for _, ev := range hookEvents {
		groups, err := s.groups(ev.name)
		if err != nil {
			return false, err
		}
		groups = slices.DeleteFunc(groups, isOurs)
		mine := encode(group{Hooks: []handler{{Type: "command", Command: exe, Args: hookArgs, Timeout: ev.timeout}}})
		s.hooks.set(ev.name, encode(append(groups, mine)))
	}
	return s.write()
}

// Uninstall takes dv's hooks out of the settings file at path, on any event,
// and an event or hooks object they leave empty. It reports whether the file
// changed.
func Uninstall(path string) (bool, error) {
	s, err := readSettings(path)
	if err != nil {
		return false, err
	}
	removed := false
	for _, m := range slices.Clone(s.hooks) {
		groups, err := s.groups(m.key)
		if err != nil {
			continue
		}
		kept := slices.DeleteFunc(slices.Clone(groups), isOurs)
		switch {
		case len(kept) == len(groups):
			continue
		case len(kept) == 0:
			s.hooks.remove(m.key)
		default:
			s.hooks.set(m.key, encode(kept))
		}
		removed = true
	}
	if !removed {
		return false, nil
	}
	if len(s.hooks) == 0 {
		s.top.remove("hooks")
	}
	return s.write()
}

// Installed returns the dv binary the hooks in the settings file at path run,
// or "" when it has none of dv's.
func Installed(path string) (string, error) {
	s, err := readSettings(path)
	if err != nil {
		return "", err
	}
	for _, ev := range hookEvents {
		groups, err := s.groups(ev.name)
		if err != nil {
			return "", err
		}
		for _, g := range groups {
			if exe := dvRuns(g); exe != "" {
				return exe, nil
			}
		}
	}
	return "", nil
}

// Heal points dv's hooks at exe when the binary they run is gone, as after dv
// is moved. It returns that binary when it tried. A dv that is still there
// keeps them, or two builds of dv would keep taking them from each other.
func Heal(path, exe string) (string, error) {
	old, err := Installed(path)
	if err != nil || old == "" || old == exe || !filepath.IsAbs(old) {
		return "", err
	}
	if _, err := os.Stat(old); !errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	_, err = Install(path, exe)
	return old, err
}

// isOurs reports whether a hook group is one Install wrote.
func isOurs(raw json.RawMessage) bool { return dvRuns(raw) != "" }

// dvRuns returns the dv binary a hook group runs, if there is nothing in it but
// dv serving its hook.
func dvRuns(raw json.RawMessage) string {
	var g group
	if json.Unmarshal(raw, &g) != nil || len(g.Hooks) == 0 {
		return ""
	}
	var exe string
	for _, h := range g.Hooks {
		exe = h.Command
		if len(h.Args) == 0 {
			// A shell line, as written by hand.
			cmd := strings.Fields(h.Command)
			if len(cmd) != 3 || !slices.Equal(cmd[1:], hookArgs) {
				return ""
			}
			exe = cmd[0]
		} else if !slices.Equal(h.Args, hookArgs) {
			return ""
		}
		if strings.TrimSuffix(filepath.Base(exe), ".exe") != "dv" {
			return ""
		}
	}
	return exe
}

// settings is a Claude Code settings file, read to change its hooks.
type settings struct {
	path       string
	orig       []byte
	top, hooks fields
}

func readSettings(path string) (*settings, error) {
	// A settings file linked in from dotfiles stays a link.
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real
	}
	orig, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	src := orig
	if len(bytes.TrimSpace(src)) == 0 {
		src = []byte("{}")
	}
	top, err := objectFields(src)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	hooks, err := objectFields(top.get("hooks"))
	if err != nil {
		return nil, fmt.Errorf("%s: hooks: %w", path, err)
	}
	return &settings{path, orig, top, hooks}, nil
}

func (s *settings) groups(event string) ([]json.RawMessage, error) {
	var groups []json.RawMessage
	if raw := s.hooks.get(event); raw != nil {
		if err := json.Unmarshal(raw, &groups); err != nil {
			return nil, fmt.Errorf("%s: hooks.%s: %w", s.path, event, err)
		}
	}
	return groups, nil
}

// write saves the file with its hooks as they now are, unless that changes
// nothing but layout. It reports whether it wrote.
func (s *settings) write() (bool, error) {
	if len(s.hooks) > 0 {
		s.top.set("hooks", s.hooks.marshal())
	}
	var out bytes.Buffer
	if err := json.Indent(&out, s.top.marshal(), "", "  "); err != nil {
		return false, err
	}
	out.WriteByte('\n')
	if same(s.orig, out.Bytes()) {
		return false, nil
	}
	return true, writeFile(s.path, out.Bytes())
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

func (f *fields) remove(key string) {
	*f = slices.DeleteFunc(*f, func(m member) bool { return m.key == key })
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
