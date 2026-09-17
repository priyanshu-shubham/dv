package gitx

import (
	"bytes"
	"fmt"
	"io"
	"os"
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
	b, ok, err := r.read(sd, path)
	if err != nil {
		return nil, time.Time{}, err
	}
	if !ok {
		return nil, time.Time{}, fmt.Errorf("no such file at %s: %s", sd.name(), path)
	}
	return nopCloser{bytes.NewReader(b)}, time.Time{}, nil
}

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }
