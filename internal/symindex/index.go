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
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"dv/internal/gitx"
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
	SkippedDirs() []string
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
	built    chan struct{} // closed as the build running ends
	buildErr error
	idle     *time.Timer // lets the table go once pages stop using it
}

// idleAfter is how long the table is kept with no page using it. It is built
// only when first looked in, so a folder open only for its agents, or never
// searched, does without it.
const idleAfter = 15 * time.Minute

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

// Build scans the repository. A call while one runs waits for that one.
func (ix *Index) Build() {
	ix.mu.Lock()
	if ix.building {
		running := ix.built
		ix.mu.Unlock()
		<-running
		return
	}
	ix.building, ix.built = true, make(chan struct{})
	ix.mu.Unlock()

	syms, files, err := ix.scan()

	ix.mu.Lock()
	ix.building = false
	ix.buildErr = err
	if err == nil {
		ix.syms, ix.files, ix.builtAt = syms, files, time.Now()
	}
	close(ix.built)
	ix.mu.Unlock()
}

// Use says a page is using the index, which starts building it if it has not
// been, and keeps it for idleAfter more.
func (ix *Index) Use() {
	ix.mu.Lock()
	cold := ix.builtAt.IsZero() && !ix.building
	if ix.idle == nil {
		ix.idle = time.AfterFunc(idleAfter, ix.drop)
	} else {
		ix.idle.Reset(idleAfter)
	}
	ix.mu.Unlock()
	if cold {
		ix.BuildAsync()
	}
}

// Ready waits for the table, built now if it has not been, for a question
// that needs it answered in full.
func (ix *Index) Ready() {
	ix.Use()
	ix.mu.RLock()
	built := !ix.builtAt.IsZero()
	ix.mu.RUnlock()
	if !built {
		ix.Build()
	}
}

// drop lets the table go, and hands its memory back to the system now rather
// than over the next minutes.
func (ix *Index) drop() {
	ix.mu.Lock()
	if ix.building {
		ix.idle.Reset(idleAfter)
		ix.mu.Unlock()
		return
	}
	ix.syms, ix.files, ix.builtAt = nil, 0, time.Time{}
	ix.mu.Unlock()
	debug.FreeOSMemory()
}

// Close lets the table go with the folder.
func (ix *Index) Close() {
	ix.mu.Lock()
	if ix.idle != nil {
		ix.idle.Stop()
	}
	ix.syms = nil
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
				b, err := gitx.ReadTextFile(filepath.Join(ix.root, j.path), maxIndexedBytes)
				if err != nil {
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

// skipPath drops files that are noise to search even when tracked. Folders are
// left to .gitignore: a fixed list of names like build/ also hid source.
func skipPath(p string) bool {
	base := filepath.Base(p)
	return strings.HasSuffix(base, ".min.js") || strings.HasSuffix(base, ".min.css") ||
		strings.HasSuffix(base, ".lock") || base == "package-lock.json"
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
