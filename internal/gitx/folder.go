package gitx

import (
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
)

// A plain folder is a Repo with no git behind it: the files on disk, and
// nothing to compare them with. Every scope reads the folder as it stands and
// none of them has changes, so the rest of dv needs no second code path.

// folderSkip is what a folder's listing leaves out: version control's
// directories and dv's own, and folderIgnored, the dependency and cache trees
// a .gitignore would have kept out of a repository's. Those are still shown,
// as ignored.
var folderSkip = map[string]bool{".git": true, ".hg": true, ".svn": true, ".dv": true}

var folderIgnored = map[string]bool{
	"node_modules": true, "__pycache__": true, ".venv": true, ".cache": true, ".next": true, ".tox": true,
}

// SkippedDirs names the folders TrackedFiles leaves out, so a tool walking the
// disk itself, like ripgrep, need not go through node_modules only to have its
// results thrown away. A repository has none: ripgrep reads its .gitignore.
func (r *Repo) SkippedDirs() []string {
	if r.IsGit() {
		return nil
	}
	var out []string
	for d := range folderSkip {
		out = append(out, d)
	}
	for d := range folderIgnored {
		out = append(out, d)
	}
	return out
}

// maxFolderFiles bounds a listing, which is walked on every poll, so dv opened
// on a home directory stays usable.
const maxFolderFiles = 50000

func folderScope() *Scope {
	return &Scope{
		Kind:  "working",
		Label: "Files",
		Desc:  "A folder outside git, read from disk",
		old:   side{empty: true},
		new:   side{worktree: true},
	}
}

// walkFolder calls fn for each file, by slash-separated path, in lexical order,
// and ignored, if given, for each folder in folderIgnored it does not go into.
func (r *Repo) walkFolder(fn func(path string, d fs.DirEntry), ignored func(dir string)) error {
	n := 0
	return filepath.WalkDir(r.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == r.Root {
				return err
			}
			return nil // an unreadable directory is left out
		}
		if d.IsDir() {
			if p == r.Root {
				return nil
			}
			if folderIgnored[d.Name()] && ignored != nil {
				if rel, err := filepath.Rel(r.Root, p); err == nil {
					ignored(filepath.ToSlash(rel))
				}
			}
			if folderSkip[d.Name()] || folderIgnored[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		// A link to a directory is not walked into, and cannot be opened as a file.
		if d.Type()&fs.ModeSymlink != 0 {
			if fi, err := os.Stat(p); err != nil || fi.IsDir() {
				return nil
			}
		} else if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(r.Root, p)
		if err != nil {
			return nil
		}
		fn(filepath.ToSlash(rel), d)
		if n++; n >= maxFolderFiles {
			return filepath.SkipAll
		}
		return nil
	})
}

func (r *Repo) folderFiles() ([]string, error) {
	var files []string
	err := r.walkFolder(func(path string, _ fs.DirEntry) { files = append(files, path) }, nil)
	return files, err
}

// folderVersion is Version for a folder: every file's path, size and mtime,
// which is what git status would have noticed.
func (r *Repo) folderVersion() (string, error) {
	h := fnv.New64a()
	err := r.walkFolder(func(path string, d fs.DirEntry) {
		fmt.Fprintf(h, "%s\x00", path)
		if fi, err := d.Info(); err == nil {
			fmt.Fprintf(h, "%d.%d\x00", fi.Size(), fi.ModTime().UnixNano())
		}
	}, nil)
	if err != nil {
		return "", err
	}
	return strconv.FormatUint(h.Sum64(), 36), nil
}
