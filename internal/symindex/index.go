// Package symindex gives dv its code navigation: a regex-derived index of
// definitions across the repository, and full-text search over the same corpus.
// It deliberately avoids ctags and language servers — nothing to install, no
// per-language configuration, and a cold build on a large repo takes well under
// a second. The trade-off is heuristic accuracy, which is the right one for
// "jump me to roughly where this is defined".
package symindex

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Symbol is one definition site.
type Symbol struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	File string `json:"file"`
	Line int    `json:"line"` // 1-based
	Text string `json:"text"` // the defining source line, trimmed
}

// Lister supplies the files to index; *gitx.Repo satisfies it.
type Lister interface {
	TrackedFiles() ([]string, error)
}

// Index holds the symbol table and rebuilds it on demand.
type Index struct {
	root   string
	lister Lister

	mu       sync.RWMutex
	syms     []Symbol
	files    int
	builtAt  time.Time
	building bool
	buildErr error
}

func New(root string, l Lister) *Index { return &Index{root: root, lister: l} }

// Status reports what the palette shows while a cold index is still filling in.
type Status struct {
	Symbols  int    `json:"symbols"`
	Files    int    `json:"files"`
	Building bool   `json:"building"`
	BuiltAt  string `json:"builtAt,omitempty"`
	Error    string `json:"error,omitempty"`
}

func (ix *Index) Status() Status {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	s := Status{Symbols: len(ix.syms), Files: ix.files, Building: ix.building}
	if !ix.builtAt.IsZero() {
		s.BuiltAt = ix.builtAt.Format(time.RFC3339)
	}
	if ix.buildErr != nil {
		s.Error = ix.buildErr.Error()
	}
	return s
}

// Build scans the repository. Concurrent calls collapse into the first one.
func (ix *Index) Build() {
	ix.mu.Lock()
	if ix.building {
		ix.mu.Unlock()
		return
	}
	ix.building = true
	ix.mu.Unlock()

	syms, files, err := ix.scan()

	ix.mu.Lock()
	ix.building = false
	ix.buildErr = err
	if err == nil {
		ix.syms, ix.files, ix.builtAt = syms, files, time.Now()
	}
	ix.mu.Unlock()
}

// BuildAsync kicks off a build without blocking the request that triggered it.
func (ix *Index) BuildAsync() { go ix.Build() }

// EnsureFresh rebuilds in the background when the index has gone stale, so a
// long-lived server keeps up with edits without watching the filesystem.
func (ix *Index) EnsureFresh(maxAge time.Duration) {
	ix.mu.RLock()
	stale := ix.builtAt.IsZero() || time.Since(ix.builtAt) > maxAge
	busy := ix.building
	ix.mu.RUnlock()
	if stale && !busy {
		ix.BuildAsync()
	}
}

const maxIndexedBytes = 2 << 20

func (ix *Index) scan() ([]Symbol, int, error) {
	paths, err := ix.lister.TrackedFiles()
	if err != nil {
		return nil, 0, err
	}

	type job struct{ path string }
	jobs := make(chan job, 256)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var all []Symbol
	scanned := 0

	workers := 8
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var local []Symbol
			n := 0
			for j := range jobs {
				l := langFor(j.path)
				if l == nil {
					continue
				}
				b, err := os.ReadFile(filepath.Join(ix.root, j.path))
				if err != nil || len(b) > maxIndexedBytes || isBinary(b) {
					continue
				}
				n++
				local = append(local, scanFile(j.path, b, l)...)
			}
			mu.Lock()
			all = append(all, local...)
			scanned += n
			mu.Unlock()
		}()
	}
	for _, p := range paths {
		if skipPath(p) {
			continue
		}
		jobs <- job{p}
	}
	close(jobs)
	wg.Wait()

	sort.Slice(all, func(i, j int) bool {
		if all[i].Name != all[j].Name {
			return all[i].Name < all[j].Name
		}
		if all[i].File != all[j].File {
			return all[i].File < all[j].File
		}
		return all[i].Line < all[j].Line
	})
	return all, scanned, nil
}

func scanFile(path string, content []byte, l *lang) []Symbol {
	var out []Symbol
	sc := bufio.NewScanner(bytes.NewReader(content))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	line := 0
	st := newScanState()
	for sc.Scan() {
		line++
		raw := sc.Text()
		// Rules run against the code on the line, never the prose beside it.
		// strip has to see every line even the ones we go on to skip, or its
		// block-comment state falls out of sync with the file.
		code := l.strip(raw, &st, false)
		if strings.TrimSpace(code) == "" || len(code) > 400 || l.isCont(raw) {
			continue
		}
		for _, r := range l.rules {
			m := r.re.FindStringSubmatch(code)
			if m == nil || r.group >= len(m) {
				continue
			}
			name := m[r.group]
			if name == "" || len(name) > 120 {
				continue
			}
			out = append(out, Symbol{Name: name, Kind: r.kind, File: path, Line: line, Text: strings.TrimSpace(raw)})
			break // first matching rule wins; rules are ordered most-specific first
		}
	}
	return out
}

var skipDirs = map[string]bool{
	"node_modules": true, "vendor": true, "dist": true, "build": true, ".next": true,
	"target": true, "__pycache__": true, ".venv": true, "venv": true, "coverage": true,
	".git": true, "third_party": true, "bower_components": true,
}

func skipPath(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if skipDirs[seg] {
			return true
		}
	}
	base := filepath.Base(p)
	return strings.HasSuffix(base, ".min.js") || strings.HasSuffix(base, ".min.css") ||
		strings.HasSuffix(base, ".lock") || base == "package-lock.json"
}

func isBinary(b []byte) bool {
	n := len(b)
	if n > 8000 {
		n = 8000
	}
	return bytes.IndexByte(b[:n], 0) >= 0
}

// Hit is a scored symbol plus the pattern positions that matched, so the client
// can bold the matched characters.
type Hit struct {
	Symbol
	Score   int    `json:"score"`
	Matches []int  `json:"matches"`
	Why     string `json:"why,omitempty"` // proximity note: "same file", "same directory"
}

// Query fuzzy-matches the pattern against symbol names, falling back to the
// file path when the name alone does not match - typing "server/handle" should
// still find handleFoo in internal/server. from is the file the reader is in,
// which breaks ties towards the definition they can actually reach.
func (ix *Index) Query(pattern, from string, limit int) []Hit {
	ix.mu.RLock()
	syms := ix.syms
	ix.mu.RUnlock()

	if strings.TrimSpace(pattern) == "" {
		return nil
	}
	var hits []Hit
	add := func(s Symbol, score int, idx []int) {
		prox, why := proximity(from, s.File)
		hits = append(hits, Hit{Symbol: s, Score: score + kindWeight(s.Kind) + prox, Matches: idx, Why: why})
	}
	for _, s := range syms {
		if score, idx, ok := fuzzyScore(pattern, s.Name); ok {
			add(s, score, idx)
			continue
		}
		// Path fallback scores lower so real name matches always sort first.
		if score, _, ok := fuzzyScore(pattern, s.File+":"+s.Name); ok {
			add(s, score/3-200, nil)
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		if len(hits[i].Name) != len(hits[j].Name) {
			return len(hits[i].Name) < len(hits[j].Name)
		}
		return hits[i].File < hits[j].File
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits
}
