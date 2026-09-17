package store

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const hubName = "hub.json"

// Folder is one folder a hub offers, at /<Slug>/.
type Folder struct {
	Slug  string    `json:"slug"`
	Path  string    `json:"path"`
	Name  string    `json:"name,omitempty"` // what its card says, "" for the folder's own name
	Added time.Time `json:"added"`
	// WorktreeOf is the main checkout of the repository this is a linked
	// worktree of, whose card it is listed under.
	WorktreeOf string `json:"worktreeOf,omitempty"`
	// Setup runs in a worktree the hub makes of this repository, once git has
	// made it; Teardown before the hub deletes one. Each is a script for sh.
	Setup    string `json:"setup,omitempty"`
	Teardown string `json:"teardown,omitempty"`
}

// Folders is the hub's list, kept per user so any hub they start has it.
type Folders struct {
	mu   sync.Mutex
	file jsonFile
	doc  hubDoc
}

type hubDoc struct {
	Format  int      `json:"format"`
	Folders []Folder `json:"folders"`
	// A slug is a URL a tab may still have open, and the tab's browser storage
	// is keyed by it: once removed, it only ever comes back for the same path.
	Retired   map[string]string `json:"retired,omitempty"`
	CloneInto string            `json:"cloneInto,omitempty"` // where the last clone went
}

func emptyHub() hubDoc {
	return hubDoc{Format: format, Folders: []Folder{}, Retired: map[string]string{}}
}

// OpenFolders loads the user's hub list.
func OpenFolders() (*Folders, error) {
	dir, err := ConfigDir()
	if err != nil {
		return nil, err
	}
	f := &Folders{file: jsonFile{path: filepath.Join(dir, hubName)}, doc: emptyHub()}
	if err := f.sync(); err != nil {
		return nil, err
	}
	return f, nil
}

// sync picks up a change made outside this process. Callers hold f.mu.
func (f *Folders) sync() error {
	doc := emptyHub()
	changed, err := f.file.load(&doc)
	if err != nil || !changed {
		return err
	}
	if doc.Folders == nil {
		doc.Folders = []Folder{}
	}
	if doc.Retired == nil {
		doc.Retired = map[string]string{}
	}
	f.doc = doc
	return nil
}

// List is the folders in the order they were added.
func (f *Folders) List() []Folder {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sync()
	return slices.Clone(f.doc.Folders)
}

// Get finds a folder by its slug.
func (f *Folders) Get(slug string) (Folder, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sync()
	for _, d := range f.doc.Folders {
		if d.Slug == slug {
			return d, true
		}
	}
	return Folder{}, false
}

// Add puts d on the list, by its Path, or finds that path there already.
func (f *Folders) Add(d Folder) (Folder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.sync(); err != nil {
		return Folder{}, err
	}
	for _, have := range f.doc.Folders {
		if have.Path == d.Path {
			return have, nil
		}
	}
	d.Slug, d.Added = f.slugFor(d.Path), time.Now().UTC()
	f.doc.Folders = append(f.doc.Folders, d)
	delete(f.doc.Retired, d.Slug)
	return d, f.file.save(f.doc)
}

// Update changes a folder in place.
func (f *Folders) Update(slug string, change func(*Folder)) (Folder, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.sync(); err != nil {
		return Folder{}, err
	}
	i := slices.IndexFunc(f.doc.Folders, func(d Folder) bool { return d.Slug == slug })
	if i < 0 {
		return Folder{}, fmt.Errorf("no folder at /%s/", slug)
	}
	change(&f.doc.Folders[i])
	return f.doc.Folders[i], f.file.save(f.doc)
}

// ByPath finds a folder by where it is.
func (f *Folders) ByPath(path string) (Folder, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sync()
	i := slices.IndexFunc(f.doc.Folders, func(d Folder) bool { return d.Path == path })
	if i < 0 {
		return Folder{}, false
	}
	return f.doc.Folders[i], true
}

// Remove takes a folder off the list. The folder itself is left alone.
func (f *Folders) Remove(slug string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.sync(); err != nil {
		return err
	}
	i := slices.IndexFunc(f.doc.Folders, func(d Folder) bool { return d.Slug == slug })
	if i < 0 {
		return fmt.Errorf("no folder at /%s/", slug)
	}
	f.doc.Retired[slug] = f.doc.Folders[i].Path
	f.doc.Folders = slices.Delete(f.doc.Folders, i, i+1)
	return f.file.save(f.doc)
}

// CloneInto is the folder the last clone went into, "" before any.
func (f *Folders) CloneInto() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sync()
	return f.doc.CloneInto
}

func (f *Folders) SetCloneInto(dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.sync(); err != nil {
		return err
	}
	if f.doc.CloneInto == dir {
		return nil
	}
	f.doc.CloneInto = dir
	return f.file.save(f.doc)
}

// reservedSlugs are the hub's own paths.
var reservedSlugs = map[string]bool{"api": true, "static": true}

// slugFor names a folder in URLs: the slug it had before, else its base name,
// numbered past one already taken or retired for another path. Callers hold f.mu.
func (f *Folders) slugFor(path string) string {
	for s, p := range f.doc.Retired {
		if p == path {
			return s
		}
	}
	var b strings.Builder
	for _, r := range strings.ToLower(filepath.Base(path)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	base := strings.Trim(b.String(), "-.")
	if base == "" {
		base = "folder"
	}
	taken := func(s string) bool {
		if reservedSlugs[s] {
			return true
		}
		if p, ok := f.doc.Retired[s]; ok && p != path {
			return true
		}
		return slices.ContainsFunc(f.doc.Folders, func(d Folder) bool { return d.Slug == s })
	}
	slug := base
	for n := 2; taken(slug); n++ {
		slug = fmt.Sprintf("%s-%d", base, n)
	}
	return slug
}
