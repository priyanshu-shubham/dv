package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// sniffLen is how much of a file git reads to tell binary from text: a NUL
// byte within it means binary.
const sniffLen = 8000

// ErrBinary and ErrTooLarge are why a file's text was not read.
var (
	ErrBinary   = errors.New("binary file")
	ErrTooLarge = errors.New("file too large")
)

// ReadText reads the text of a file size bytes long from rd. It reads no more
// than the start of one that turns out binary, or longer than limit, so a
// large video or archive costs a few kilobytes rather than its whole length.
func ReadText(rd io.Reader, size, limit int64) ([]byte, error) {
	buf := bytes.NewBuffer(make([]byte, 0, min(size, sniffLen)))
	if _, err := io.CopyN(buf, rd, sniffLen); err != nil && err != io.EOF {
		return nil, err
	}
	if bytes.IndexByte(buf.Bytes(), 0) >= 0 {
		return nil, ErrBinary
	}
	// A file that grew since its size was taken is held to the limit all the same.
	if size > limit || int64(buf.Len()) > limit {
		return nil, ErrTooLarge
	}
	if more := int(size) - buf.Len(); more > 0 {
		buf.Grow(more + 1)
	}
	if _, err := buf.ReadFrom(io.LimitReader(rd, limit+1-int64(buf.Len()))); err != nil {
		return nil, err
	}
	if int64(buf.Len()) > limit {
		return nil, ErrTooLarge
	}
	return buf.Bytes(), nil
}

// ReadTextFile is ReadText for a file on disk.
func ReadTextFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.IsDir() {
		return nil, fmt.Errorf("%s is a folder", path)
	}
	return ReadText(f, fi.Size(), limit)
}

// readText reads path on one side of a comparison as ReadText does, and
// whether it is there.
func (r *Repo) readText(sd side, path string, limit int64) ([]byte, bool, error) {
	switch {
	case sd.empty:
		return nil, false, nil
	case sd.worktree:
		b, err := ReadTextFile(filepath.Join(r.Root, path), limit)
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return b, true, err
	}
	spec := sd.spec(path)
	if spec == "" {
		return nil, false, nil
	}
	// The size first, which also says whether it is there at all.
	out, err := r.run("cat-file", "-s", spec)
	if err != nil {
		return nil, false, nil
	}
	size, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return nil, false, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	// Cancelled once read, which stops git writing out the rest of a blob not wanted.
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "cat-file", "blob", spec)
	cmd.Dir = r.Root
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	if err := cmd.Start(); err != nil {
		return nil, false, err
	}
	b, err := ReadText(stdout, size, limit)
	cancel()
	cmd.Wait()
	return b, true, err
}
