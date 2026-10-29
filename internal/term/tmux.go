package term

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A tmux client is all of tmux that runs in a terminal: the session is in
// tmux's server, which outlives the terminal and dv with it. So a terminal
// showing one is not counted as work a restart would stop, and dv attaches to
// the session again when it next opens the folder.

type tmuxClient struct {
	flags   []string // -L or -S: the server the client talks to
	session string   // "" until tmux has said
	command string   // what runs in the session's active pane
	dir     string   // and where
}

// The tmux servers dv lists sessions from: the default one, and any a
// terminal's tmux was seen talking to.
var sockets = struct {
	sync.Mutex
	seen [][]string
}{seen: [][]string{nil}}

func sawSocket(flags []string) {
	sockets.Lock()
	defer sockets.Unlock()
	if !slices.ContainsFunc(sockets.seen, func(f []string) bool { return slices.Equal(f, flags) }) {
		sockets.seen = append(sockets.seen, flags)
	}
}

// socketFlags reports whether flags pick a tmux server, as -L name or -S path.
func socketFlags(flags []string) bool {
	return len(flags) == 0 || len(flags) == 2 && (flags[0] == "-L" || flags[0] == "-S") && flags[1] != ""
}

// Session is a tmux session that none of the folder's terminals shows.
type Session struct {
	Name     string    `json:"name"`
	Flags    []string  `json:"flags,omitempty"`
	Dir      string    `json:"dir"`
	Here     bool      `json:"here,omitempty"` // started in the folder
	Windows  int       `json:"windows"`
	Attached int       `json:"attached,omitempty"` // clients, all elsewhere
	Activity time.Time `json:"activity"`
}

// Sessions lists them, those started in the folder first, then the latest used.
func (m *Manager) Sessions() []Session {
	if supported != nil {
		return nil
	}
	shown := map[string]bool{}
	key := func(flags []string, session string) string {
		return strings.Join(append(slices.Clone(flags), session), "\x00")
	}
	m.mu.Lock()
	for _, t := range m.terms {
		t.mu.Lock()
		if c := t.tmux; c != nil && c.session != "" {
			shown[key(c.flags, c.session)] = true
		}
		t.mu.Unlock()
	}
	m.mu.Unlock()
	sockets.Lock()
	servers := slices.Clone(sockets.seen)
	sockets.Unlock()
	var out []Session
	for _, flags := range servers {
		b, err := tmux(flags, "list-sessions", "-F", "#{session_name}\t#{session_path}\t#{session_windows}\t#{session_attached}\t#{session_activity}")
		if err != nil {
			continue // no server running there
		}
		for line := range strings.Lines(string(b)) {
			f := strings.Split(strings.TrimRight(line, "\n"), "\t")
			if len(f) != 5 || shown[key(flags, f[0])] {
				continue
			}
			windows, _ := strconv.Atoi(f[2])
			attached, _ := strconv.Atoi(f[3])
			activity, _ := strconv.ParseInt(f[4], 10, 64)
			rel, err := filepath.Rel(m.root, f[1])
			out = append(out, Session{
				Name: f[0], Flags: flags, Dir: f[1], Here: err == nil && (rel == "." || filepath.IsLocal(rel)),
				Windows: windows, Attached: attached, Activity: time.Unix(activity, 0).UTC(),
			})
		}
	}
	slices.SortStableFunc(out, func(a, b Session) int {
		if a.Here != b.Here {
			if a.Here {
				return -1
			}
			return 1
		}
		return b.Activity.Compare(a.Activity)
	})
	return out
}

// Attach opens a terminal in the folder and attaches it to a tmux session.
func (m *Manager) Attach(session string, flags []string) (*Terminal, error) {
	if !socketFlags(flags) {
		return nil, fmt.Errorf("%q does not pick a tmux server", flags)
	}
	if _, err := tmux(flags, "has-session", "-t", "="+session); err != nil {
		return nil, fmt.Errorf("tmux has no session %q", session)
	}
	t, err := m.Start()
	if err != nil {
		return nil, err
	}
	go t.typeOnceReady(attachLine(session, flags))
	return t, nil
}

// attachLine is typed into a shell, as the reader would, so detaching leaves
// them at its prompt; the space keeps it out of most shells' history.
func attachLine(session string, flags []string) string {
	return " " + quote(append(append([]string{"tmux"}, flags...), "attach", "-t", "="+session)) + "\r"
}

// tmuxOf is the tmux client args would make, nil if they are not tmux.
func tmuxOf(args []string) *tmuxClient {
	if len(args) == 0 || filepath.Base(args[0]) != "tmux" {
		return nil
	}
	c := &tmuxClient{}
	for i := 1; i < len(args) && strings.HasPrefix(args[i], "-") && args[i] != "--"; i++ {
		switch a := args[i]; {
		case a == "-L" || a == "-S":
			if i+1 < len(args) {
				c.flags = append(c.flags, a, args[i+1])
				i++
			}
		case strings.HasPrefix(a, "-L") || strings.HasPrefix(a, "-S"):
			c.flags = append(c.flags, a[:2], a[2:])
		case a == "-c" || a == "-f" || a == "-T":
			i++
		}
	}
	if !socketFlags(c.flags) {
		c.flags = c.flags[len(c.flags)-2:] // the last given wins, as in tmux
	}
	sawSocket(c.flags)
	return c
}

func tmux(flags []string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "tmux", append(slices.Clone(flags), args...)...)
	cmd.Env = shellEnv() // as the terminal's tmux has it, to find the same server
	return cmd.Output()
}

// ask fills in what client pid shows, from its server, reporting whether it could.
func (c *tmuxClient) ask(pid int) bool {
	out, err := tmux(c.flags, "list-clients", "-F", "#{client_pid}\t#{session_name}\t#{pane_current_command}\t#{pane_current_path}\t#{pane_pid}")
	if err != nil {
		return false
	}
	for line := range strings.Lines(string(out)) {
		f := strings.Split(strings.TrimRight(line, "\n"), "\t")
		if len(f) == 5 && f[0] == strconv.Itoa(pid) {
			c.session, c.command, c.dir = f[1], f[2], f[3]
			// tmux names the program; its args are the process's own.
			shell, _ := strconv.Atoi(f[4])
			if fg := foregroundOf(shell); fg > 0 && fg != shell {
				c.command = cmp.Or(display(argsOf(fg)), c.command)
			}
			return true
		}
	}
	return false
}

// keptTerm is a terminal attached to a tmux session, as kept for dv to
// attach to it again.
type keptTerm struct {
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	Dir     string   `json:"dir"`
	Session string   `json:"session"`
	Flags   []string `json:"flags,omitempty"`
}

func (m *Manager) keptPath() string { return filepath.Join(m.root, ".dv", "terminals.json") }

// keep writes down which terminals show a tmux session, as it changes. Not
// once the folder is closing: its terminals going then is not the reader's doing.
func (m *Manager) keep() {
	m.keepMu.Lock()
	defer m.keepMu.Unlock()
	m.mu.Lock()
	closed, terms := m.closed, slices.Clone(m.terms)
	m.mu.Unlock()
	if closed {
		return
	}
	var kept []keptTerm
	for _, t := range terms {
		t.mu.Lock()
		if c := t.tmux; c != nil && c.session != "" && !t.ended {
			kept = append(kept, keptTerm{ID: t.id, Name: t.name, Dir: t.dir, Session: c.session, Flags: c.flags})
		}
		t.mu.Unlock()
	}
	if slices.EqualFunc(kept, m.kept, func(a, b keptTerm) bool {
		return a.ID == b.ID && a.Name == b.Name && a.Session == b.Session && slices.Equal(a.Flags, b.Flags)
	}) {
		return
	}
	m.kept = kept
	path := m.keptPath()
	if len(kept) == 0 {
		os.Remove(path)
		return
	}
	b, _ := json.MarshalIndent(kept, "", "  ")
	if os.MkdirAll(filepath.Dir(path), 0o755) == nil && os.WriteFile(path+".tmp", append(b, '\n'), 0o644) == nil {
		os.Rename(path+".tmp", path)
	}
}

// Restore opens a terminal for each tmux session one was showing when dv
// last had the folder open, attached to it again, so long as tmux still has it.
func (m *Manager) Restore() {
	if supported != nil {
		return
	}
	b, err := os.ReadFile(m.keptPath())
	if err != nil {
		return
	}
	var kept []keptTerm
	if json.Unmarshal(b, &kept) != nil {
		return
	}
	m.keepMu.Lock()
	m.kept = kept
	m.keepMu.Unlock()
	for _, k := range kept {
		if !socketFlags(k.Flags) {
			continue
		}
		if _, err := tmux(k.Flags, "has-session", "-t", "="+k.Session); err != nil {
			continue
		}
		sawSocket(k.Flags)
		dir := k.Dir
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			dir = m.root
		}
		t, err := m.open(k.ID, dir)
		if err != nil {
			continue
		}
		// Not Rename, which would write down that nothing shows a session yet.
		t.mu.Lock()
		t.name = k.Name
		t.mu.Unlock()
		go t.typeOnceReady(attachLine(k.Session, k.Flags))
	}
	// Once they have had time to attach, what is kept is what did: one that
	// could not is not tried again at every start.
	time.AfterFunc(15*time.Second, m.keep)
}

// typeOnceReady types line once the shell has started printing its prompt.
func (t *Terminal) typeOnceReady(line string) {
	for range 100 {
		t.mu.Lock()
		printed := !t.output.IsZero()
		t.mu.Unlock()
		if printed {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	t.Write([]byte(line))
}

// quote joins args as a POSIX shell would read them back; fish and zsh too,
// where a word starting with = is the path of the command named by the rest.
func quote(args []string) string {
	out := make([]string, len(args))
	for i, a := range args {
		if a != "" && strings.Trim(a, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_./:@%+,") == "" {
			out[i] = a
		} else {
			out[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(out, " ")
}
