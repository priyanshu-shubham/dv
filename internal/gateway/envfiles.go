package gateway

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// A folder's .env files are masked as they stand, unless the user turned
// that off: each value long enough to be a secret, named by its key.

// envSource is what the user's settings say of secrets they haven't masked:
// the folders dv has open, whether their .env files are masked, and whether
// what looks like a secret is asked about before it is sent.
var envSource atomic.Pointer[struct {
	dirs    func() []string
	on, ask func() bool
}]

// Watch has every gateway mask the .env files of the folders dirs gives
// while on says to, and ask about what looks like a secret while ask does.
func Watch(dirs func() []string, on, ask func() bool) {
	envSource.Store(&struct {
		dirs    func() []string
		on, ask func() bool
	}{dirs, on, ask})
}

// envMin is the shortest value taken for a secret: "true", "3000" and
// "localhost" are settings, not secrets.
const envMin = 8

type envSecret struct {
	name, value, from string
}

type envScan struct {
	mu    sync.Mutex
	at    time.Time
	stamp string
	found []envSecret
	from  map[string]string // by the name it is masked as
}

// refreshEnv reads the .env files again where they changed, at most every
// couple of seconds, and masks what they hold from then on.
func (g *Gateway) refreshEnv() {
	src := envSource.Load()
	g.env.mu.Lock()
	if time.Since(g.env.at) < 2*time.Second {
		g.env.mu.Unlock()
		return
	}
	g.env.at = time.Now()
	var files []string
	if src != nil && src.on() {
		for _, dir := range src.dirs() {
			files = append(files, envFiles(dir)...)
		}
	}
	var stamp strings.Builder
	for _, f := range files {
		if fi, err := os.Stat(f); err == nil {
			fmt.Fprintf(&stamp, "%s %d %d\n", f, fi.Size(), fi.ModTime().UnixNano())
		}
	}
	if stamp.String() == g.env.stamp {
		g.env.mu.Unlock()
		return
	}
	g.env.stamp = stamp.String()
	g.env.found = nil
	for _, f := range files {
		g.env.found = append(g.env.found, readEnv(f)...)
	}
	g.env.mu.Unlock()
	g.remask()
}

// envFiles are a folder's .env files, those that hold values rather than
// show what to fill in.
func envFiles(dir string) []string {
	matches, _ := filepath.Glob(filepath.Join(dir, ".env*"))
	return slices.DeleteFunc(matches, func(f string) bool {
		base := strings.ToLower(filepath.Base(f))
		for _, w := range []string{"example", "sample", "template", "dist", "defaults"} {
			if strings.Contains(base, w) {
				return true
			}
		}
		fi, err := os.Stat(f)
		return err != nil || fi.IsDir() || fi.Size() > 1<<20
	})
}

func readEnv(path string) []envSecret {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	from := filepath.Join(filepath.Base(filepath.Dir(path)), filepath.Base(path))
	var out []envSecret
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		key = strings.TrimSpace(key)
		if !ok || !NameRe.MatchString(key) {
			continue
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
			value = value[1 : len(value)-1]
		} else if i := strings.Index(value, " #"); i >= 0 {
			value = strings.TrimSpace(value[:i])
		}
		if len(value) >= envMin && !strings.ContainsAny(value, " \t") {
			out = append(out, envSecret{strings.ToUpper(key), value, from})
		}
	}
	return out
}

// remask masks the secrets the user saved and those the .env files hold: a
// saved one wins a name, and a value already masked isn't again.
func (g *Gateway) remask() {
	saved := map[string]string{}
	if p := g.saved.Load(); p != nil {
		saved = *p
	}
	values := make(map[string]string, len(saved))
	taken := map[string]bool{}
	for name, v := range saved {
		values[name] = v
		taken[v] = true
	}
	g.env.mu.Lock()
	defer g.env.mu.Unlock()
	from := map[string]string{}
	for _, s := range g.env.found {
		if taken[s.value] {
			continue
		}
		name := s.name
		for n := 2; values[name] != ""; n++ {
			name = fmt.Sprintf("%s_%d", s.name, n)
		}
		values[name], taken[s.value], from[name] = s.value, true, s.from
	}
	g.env.from = from
	g.mask.set(values)
}

// envFromOf is the .env file a masked secret was read from, "" for one saved.
func (g *Gateway) envFromOf(name string) string {
	g.env.mu.Lock()
	defer g.env.mu.Unlock()
	return g.env.from[name]
}
