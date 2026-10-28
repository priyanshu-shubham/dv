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
	"maps"
	"os"
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

// Lister supplies the files to index; *gitx.Repo satisfies it. Version
// changes whenever any of them might have.
type Lister interface {
	TrackedFiles() ([]string, error)
	SkippedDirs() []string
	Version() (string, error)
}

// Index holds the symbol table and rebuilds it on demand.
type Index struct {
	root   string
	lister Lister

	mu       sync.RWMutex
	syms     []Symbol
	stamps   map[string]stamp // every file read, as it was when read
	version  string           // the Lister's Version the table is current with
	gen      int              // bumped as a build or drop replaces the table
	builtAt  time.Time
	building bool
	built    chan struct{} // closed as the build running ends
	buildErr error
	idle     *time.Timer // lets the table go once pages stop using it

	catching sync.Mutex
	caughtUp time.Time
}

// stamp is how a file is known to have changed since it was read.
type stamp struct {
	size int64
	mod  time.Time
}

func stampOf(fi os.FileInfo) stamp { return stamp{fi.Size(), fi.ModTime()} }

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
	s := Status{Symbols: len(ix.syms), Files: len(ix.stamps), Building: ix.building}
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

	// Taken first, so an edit during the scan reads as one after it.
	version, _ := ix.lister.Version()
	syms, stamps, err := ix.scan()

	ix.mu.Lock()
	ix.building = false
	ix.buildErr = err
	if err == nil {
		ix.syms, ix.stamps, ix.version, ix.builtAt = syms, stamps, version, time.Now()
		ix.gen++
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

// Ready waits for the table, built now if it has not been and otherwise
// caught up with the files changed since, for a question that needs it
// answered in full.
func (ix *Index) Ready() {
	ix.Use()
	ix.mu.RLock()
	built := !ix.builtAt.IsZero()
	ix.mu.RUnlock()
	if !built {
		ix.Build()
		return
	}
	ix.catchUp()
}

// recheckAfter spares the palette, which asks on every pause in typing, a
// git status each time.
const recheckAfter = time.Second

// catchUp rereads the files that changed since they were read, and drops
// those gone. Agents edit while the reader reads, and a line remembered from
// before an edit lands on whatever has since moved there. Files are only
// looked over once the Version says something moved.
func (ix *Index) catchUp() {
	ix.catching.Lock()
	defer ix.catching.Unlock()
	if time.Since(ix.caughtUp) < recheckAfter {
		return
	}
	ix.caughtUp = time.Now()
	ix.mu.RLock()
	known, gen, was := ix.stamps, ix.gen, ix.version
	ix.mu.RUnlock()
	version, err := ix.lister.Version()
	if err == nil && version == was {
		return
	}

	paths, err := ix.lister.TrackedFiles()
	if err != nil {
		return
	}
	listed := make(map[string]bool, len(paths))
	changed := map[string]stamp{}
	for _, p := range paths {
		if skipPath(p) || langFor(p) == nil {
			continue
		}
		fi, err := os.Stat(filepath.Join(ix.root, p))
		if err != nil {
			continue
		}
		listed[p] = true
		if st := stampOf(fi); known[p] != st {
			changed[p] = st
		}
	}
	gone := false
	for p := range known {
		gone = gone || !listed[p]
	}

	var fresh []Symbol
	for p := range changed {
		if b, err := gitx.ReadTextFile(filepath.Join(ix.root, p), maxIndexedBytes); err == nil {
			fresh = append(fresh, scanFile(p, b, langFor(p))...)
		}
	}

	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.gen != gen {
		return // a build or drop since has a table of its own
	}
	ix.version = version
	if len(changed) == 0 && !gone {
		return
	}
	kept := make([]Symbol, 0, len(ix.syms))
	for _, s := range ix.syms {
		if _, redone := changed[s.File]; listed[s.File] && !redone {
			kept = append(kept, s)
		}
	}
	stamps := make(map[string]stamp, len(listed))
	for p := range listed {
		if st, ok := changed[p]; ok {
			stamps[p] = st
		} else {
			stamps[p] = known[p]
		}
	}
	ix.syms, ix.stamps = merge(kept, sortSymbols(fresh)), stamps
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
	ix.syms, ix.stamps, ix.builtAt = nil, nil, time.Time{}
	ix.gen++
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

const maxIndexedBytes = 2 << 20

func (ix *Index) scan() ([]Symbol, map[string]stamp, error) {
	paths, err := ix.lister.TrackedFiles()
	if err != nil {
		return nil, nil, err
	}

	type job struct{ path string }
	jobs := make(chan job, 256)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var all []Symbol
	stamps := map[string]stamp{}

	workers := 8
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var local []Symbol
			seen := map[string]stamp{}
			for j := range jobs {
				l := langFor(j.path)
				if l == nil {
					continue
				}
				// Stamped before it is read: an edit landing in between then
				// shows as a change, and is read again.
				full := filepath.Join(ix.root, j.path)
				fi, err := os.Stat(full)
				if err != nil {
					continue
				}
				seen[j.path] = stampOf(fi)
				b, err := gitx.ReadTextFile(full, maxIndexedBytes)
				if err != nil {
					continue
				}
				local = append(local, scanFile(j.path, b, l)...)
			}
			mu.Lock()
			all = append(all, local...)
			maps.Copy(stamps, seen)
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
	return sortSymbols(all), stamps, nil
}

func before(a, b Symbol) bool {
	if a.Name != b.Name {
		return a.Name < b.Name
	}
	if a.File != b.File {
		return a.File < b.File
	}
	return a.Line < b.Line
}

func sortSymbols(all []Symbol) []Symbol {
	sort.Slice(all, func(i, j int) bool { return before(all[i], all[j]) })
	return all
}

// merge joins two sorted tables, which a few changed files' symbols join the
// rest's without sorting the whole table again.
func merge(a, b []Symbol) []Symbol {
	out := make([]Symbol, 0, len(a)+len(b))
	for len(a) > 0 && len(b) > 0 {
		if before(b[0], a[0]) {
			out, b = append(out, b[0]), b[1:]
		} else {
			out, a = append(out, a[0]), a[1:]
		}
	}
	return append(append(out, a...), b...)
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
