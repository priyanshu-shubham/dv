package store

import (
	"path/filepath"
	"slices"
	"sync"
)

const viewedName = "viewed.json"

// Viewed remembers which files were marked viewed, per comparison: "I've read
// this" is a claim about one diff. It is kept beside the comments rather than
// in the browser so it outlives the tab, is the same in every one, and
// `dv reset` can clear it.
type Viewed struct {
	mu   sync.Mutex
	file jsonFile
	doc  viewedDoc
}

type viewedDoc struct {
	Format int                 `json:"format"`
	Scopes map[string][]string `json:"scopes"` // comparison label -> sorted paths
}

func emptyViewed() viewedDoc { return viewedDoc{Format: format, Scopes: map[string][]string{}} }

// OpenViewed loads the marks for a repository. Like Open, it creates nothing.
func OpenViewed(repoRoot string) (*Viewed, error) {
	v := &Viewed{file: jsonFile{path: filepath.Join(repoRoot, dirName, viewedName)}, doc: emptyViewed()}
	if err := v.sync(); err != nil {
		return nil, err
	}
	return v, nil
}

// sync picks up a change made outside this process. Callers hold v.mu.
func (v *Viewed) sync() error {
	doc := emptyViewed()
	changed, err := v.file.load(&doc)
	if err != nil || !changed {
		return err
	}
	if doc.Scopes == nil {
		doc.Scopes = map[string][]string{}
	}
	for _, paths := range doc.Scopes {
		slices.Sort(paths) // Mark searches them; a hand edit need not be in order
	}
	v.doc = doc
	return nil
}

// Paths lists the files marked viewed in one comparison.
func (v *Viewed) Paths(scope string) []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.sync()
	return append([]string{}, v.doc.Scopes[scope]...)
}

// Mark sets whether files count as viewed in one comparison.
func (v *Viewed) Mark(scope string, paths []string, on bool) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.sync(); err != nil {
		return err
	}
	marked := v.doc.Scopes[scope]
	changed := false
	for _, p := range paths {
		i, found := slices.BinarySearch(marked, p)
		switch {
		case on && !found:
			marked = slices.Insert(marked, i, p)
		case !on && found:
			marked = slices.Delete(marked, i, i+1)
		default:
			continue
		}
		changed = true
	}
	if !changed {
		return nil
	}
	if len(marked) == 0 {
		delete(v.doc.Scopes, scope)
	} else {
		v.doc.Scopes[scope] = marked
	}
	return v.file.save(v.doc)
}

// Count is how many marks there are across every comparison.
func (v *Viewed) Count() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.sync()
	return v.countLocked()
}

func (v *Viewed) countLocked() int {
	n := 0
	for _, paths := range v.doc.Scopes {
		n += len(paths)
	}
	return n
}

// Version moves whenever the marks do, whoever changed them.
func (v *Viewed) Version() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.sync()
	return v.file.stamp
}

// Reset forgets every mark, returning how many there were.
func (v *Viewed) Reset() (int, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if err := v.sync(); err != nil {
		return 0, err
	}
	n := v.countLocked()
	v.doc = emptyViewed()
	return n, v.file.remove()
}
