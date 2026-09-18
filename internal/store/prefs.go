package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"sync"
)

const prefsName = "prefs.json"

// Prefs are the page's settings and unsent work kept on disk rather than in a
// browser, so a phone and a laptop open on the same dv see the same ones. The
// user's apply everywhere; a repository's sit beside its comments.
type Prefs struct {
	mu   sync.Mutex
	file jsonFile
	doc  prefsDoc
}

type prefsDoc struct {
	Format int                        `json:"format"`
	Prefs  map[string]json.RawMessage `json:"prefs"`
}

func emptyPrefs() prefsDoc { return prefsDoc{Format: format, Prefs: map[string]json.RawMessage{}} }

// ConfigDir is where dv keeps what belongs to the user rather than to one
// repository.
func ConfigDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "dv"), nil
}

// DataDir is where dv keeps what is to stay on this computer, apart from the
// settings, which get copied and synced: $XDG_DATA_HOME or ~/.local/share, and
// on Windows the AppData that does not roam.
func DataDir() (string, error) {
	if runtime.GOOS == "windows" {
		if dir := os.Getenv("LocalAppData"); dir != "" {
			return filepath.Join(dir, "dv"), nil
		}
		return "", errors.New("%LocalAppData% is not set")
	}
	if dir := os.Getenv("XDG_DATA_HOME"); filepath.IsAbs(dir) {
		return filepath.Join(dir, "dv"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "dv"), nil
}

// OpenUserPrefs loads the user's settings, shared by every dv they run.
func OpenUserPrefs() (*Prefs, error) {
	dir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	return openPrefs(filepath.Join(dir, prefsName))
}

// OpenRepoPrefs loads a repository's. Like Open, it creates nothing.
func OpenRepoPrefs(repoRoot string) (*Prefs, error) {
	return openPrefs(filepath.Join(repoRoot, dirName, prefsName))
}

func openPrefs(path string) (*Prefs, error) {
	p := &Prefs{file: jsonFile{path: path}, doc: emptyPrefs()}
	if err := p.sync(); err != nil {
		return nil, err
	}
	return p, nil
}

// sync picks up a change made outside this process. Callers hold p.mu.
func (p *Prefs) sync() error {
	doc := emptyPrefs()
	changed, err := p.file.load(&doc)
	if err != nil || !changed {
		return err
	}
	if doc.Prefs == nil {
		doc.Prefs = map[string]json.RawMessage{}
	}
	p.doc = doc
	return nil
}

// All is every value by key.
func (p *Prefs) All() map[string]json.RawMessage {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sync()
	return maps.Clone(p.doc.Prefs)
}

// Set stores one value, or deletes it given null. keep, when given, is asked
// about every other key, and what it turns down goes in the same write.
func (p *Prefs) Set(key string, value json.RawMessage, keep func(key string) bool) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.sync(); err != nil {
		return err
	}
	value, err := compact(value)
	if err != nil {
		return err
	}
	changed := false
	if string(value) == "null" {
		_, changed = p.doc.Prefs[key]
		delete(p.doc.Prefs, key)
	} else if old, _ := compact(p.doc.Prefs[key]); !bytes.Equal(old, value) {
		p.doc.Prefs[key] = value
		changed = true
	}
	if keep != nil {
		for k := range p.doc.Prefs {
			if k != key && !keep(k) {
				delete(p.doc.Prefs, k)
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	if len(p.doc.Prefs) == 0 {
		return p.file.remove()
	}
	return p.file.save(p.doc)
}

// compact is the one spelling of a value, since the file keeps it indented.
// Nothing at all reads as null.
func compact(raw json.RawMessage) (json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage("null"), nil
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Version moves whenever the values do, whoever changed them.
func (p *Prefs) Version() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sync()
	return p.file.stamp
}
