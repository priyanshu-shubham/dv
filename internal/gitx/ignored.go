package gitx

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// The explorer lists what git ignores too. A folder ignored as a whole is one
// entry, its path ending in "/", listed a level at a time as it is opened:
// node_modules alone can hold more files than the rest of the repository.

// Ignored lists the ignored files and folders on the scope's new side, and
// what each folder in open holds. Only the working tree has any.
func (r *Repo) Ignored(s *Scope, open []string) ([]string, error) {
	if !s.new.worktree {
		return nil, nil
	}
	var top []string
	var err error
	if r.IsGit() {
		top, err = r.ignoredByGit()
	} else {
		err = r.walkFolder(func(string, fs.DirEntry) {}, func(dir string) { top = append(top, dir+"/") })
	}
	if err != nil {
		return nil, err
	}
	all := top
	for _, dir := range open {
		if !insideIgnored(top, dir) {
			continue
		}
		if entries, err := r.ignoredDir(dir); err == nil {
			all = append(all, entries...)
		}
	}
	return all, nil
}

// ignoredByGit asks for matching mode, in which a folder that matches a pattern
// is listed alone, while ignored files in folders that do not are listed one
// by one.
func (r *Repo) ignoredByGit() ([]string, error) {
	out, err := r.run("--no-optional-locks", "status", "--porcelain", "-z", "--ignored=matching", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var paths []string
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if len(f) < 4 {
			continue
		}
		switch {
		case strings.ContainsAny(f[:2], "RC"):
			i++ // where a rename or copy came from follows it
		case f[:3] == "!! " && f[3:] != ".dv/":
			paths = append(paths, f[3:])
		}
	}
	return paths, nil
}

// insideIgnored says whether dir, with no trailing slash, is or is in one of
// the ignored folders, which keeps a listing to what the explorer offers.
func insideIgnored(top []string, dir string) bool {
	if dir == "" || path.Clean(dir) != dir || !filepath.IsLocal(filepath.FromSlash(dir)) {
		return false
	}
	for _, t := range top {
		if strings.HasSuffix(t, "/") && strings.HasPrefix(dir+"/", t) {
			return true
		}
	}
	return false
}

// ignoredDir lists a folder's files and folders, one level down. A link to a
// folder is listed as a folder: pnpm's node_modules is made of them.
func (r *Repo) ignoredDir(dir string) ([]string, error) {
	full := filepath.Join(r.Root, filepath.FromSlash(dir))
	entries, err := os.ReadDir(full)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, e := range entries {
		p := dir + "/" + e.Name()
		switch {
		case e.IsDir():
			p += "/"
		case e.Type()&fs.ModeSymlink != 0:
			fi, err := os.Stat(filepath.Join(full, e.Name()))
			if err != nil {
				continue
			}
			if fi.IsDir() {
				p += "/"
			}
		case !e.Type().IsRegular():
			continue
		}
		paths = append(paths, p)
		if len(paths) >= maxFolderFiles {
			break
		}
	}
	return paths, nil
}
