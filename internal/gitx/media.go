package gitx

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// A media file is shown by the browser from its own URL, so the page never
// needs its bytes as lines: StampAt tells the page which version is there, and
// OpenAt serves it.

// StampAt identifies the version of path on one side of the scope without
// reading it - size and mtime on disk, the blob's id in git - so a page showing
// the file knows when to fetch it again. at is as FileAt has it.
func (r *Repo) StampAt(path string, s *Scope, old bool) (stamp, at string, err error) {
	sd := s.pick(old)
	at = sd.name()
	switch {
	case sd.worktree:
		fi, err := os.Stat(filepath.Join(r.Root, path))
		if err != nil || fi.IsDir() {
			return "", "", fmt.Errorf("no such file: %s", path)
		}
		return fmt.Sprintf("%d.%d", fi.Size(), fi.ModTime().UnixNano()), at, nil
	case !sd.empty:
		if out, err := r.run("rev-parse", "--verify", "--quiet", sd.spec(path)); err == nil {
			return strings.TrimSpace(out), at, nil
		}
	}
	return "", "", fmt.Errorf("no such file at %s: %s", at, path)
}

// OpenAt opens path on one side of the scope to be served as it is. On disk
// that is the file itself, so a long video is streamed and seeked in rather
// than read whole; from git it is the blob. The time is zero for a blob.
func (r *Repo) OpenAt(path string, s *Scope, old bool) (io.ReadSeekCloser, time.Time, error) {
	sd := s.pick(old)
	if sd.worktree {
		f, err := os.Open(filepath.Join(r.Root, path))
		if err != nil {
			return nil, time.Time{}, err
		}
		fi, err := f.Stat()
		if err != nil || fi.IsDir() {
			f.Close()
			return nil, time.Time{}, fmt.Errorf("no such file: %s", path)
		}
		return f, fi.ModTime(), nil
	}
	f, err := r.cachedBlob(sd.spec(path))
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("no such file at %s: %s", sd.name(), path)
	}
	return f, time.Time{}, nil
}

// MediaCache is where versions from git are kept to be served; a variable
// for tests.
var MediaCache = func() (string, error) {
	dir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "dv", "media"), nil
}

// mediaKeep is how long a version kept in the cache outlives its last use:
// long enough for a video's ranges and a reload, as each use renews it.
const mediaKeep = time.Hour

// cachedBlob opens the blob spec names from the cache, copying it there
// first. A video asks for itself a range at a time; each is served from the
// one copy, where reading the blob from git anew would read all of it each
// time, into memory.
func (r *Repo) cachedBlob(spec string) (*os.File, error) {
	if spec == "" {
		return nil, os.ErrNotExist
	}
	out, err := r.run("rev-parse", "--verify", "--quiet", spec)
	if err != nil {
		return nil, err
	}
	dir, err := MediaCache()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, strings.TrimSpace(out))
	if f, err := os.Open(path); err == nil {
		now := time.Now()
		os.Chtimes(path, now, now) // in use, so not cleared away
		return f, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	ClearMediaCache()
	tmp, err := os.CreateTemp(dir, ".copy-*")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name()) // nothing left behind once renamed
	cmd := exec.Command("git", "cat-file", "blob", spec)
	cmd.Dir = r.Root
	cmd.Stdout = tmp
	err = cmd.Run()
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return nil, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return nil, err
	}
	return os.Open(path)
}

// ClearMediaCache removes the versions not used for a while, and copies a dv
// stopped in the middle of. It runs as each copy is made and as dv starts,
// which catches what was left when none is made again.
func ClearMediaCache() {
	dir, err := MediaCache()
	if err != nil {
		return
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > mediaKeep {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
