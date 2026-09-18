// Package update finds dv's newest release and installs it over the running
// binary, as install.sh does: this machine's archive, checked against the
// release's checksums, put in place by a rename.
package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const repo = "https://github.com/priyanshu-shubham/dv"

// A check is kept this long; a failed one only briefly, so a machine that was
// offline hears of a release soon after it is back.
const (
	checkEvery = 6 * time.Hour
	retryAfter = 10 * time.Minute
)

var (
	mu      sync.Mutex
	checked time.Time
	latest  string
	lastErr error
)

// Latest is the newest release's version, as "0.8.2". GitHub answers
// releases/latest with a redirect to its tag, which is all that is read: no
// API, so no rate limit.
func Latest(ctx context.Context) (string, error) {
	mu.Lock()
	defer mu.Unlock()
	keep := checkEvery
	if lastErr != nil {
		keep = retryAfter
	}
	if !checked.IsZero() && time.Since(checked) < keep {
		return latest, lastErr
	}
	latest, lastErr = ask(ctx)
	checked = time.Now()
	return latest, lastErr
}

func ask(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, repo+"/releases/latest", nil)
	if err != nil {
		return "", err
	}
	c := http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	tag := path.Base(resp.Header.Get("Location"))
	if resp.StatusCode/100 != 3 || !strings.HasPrefix(tag, "v") {
		return "", fmt.Errorf("GitHub did not say which release is the latest (%s)", resp.Status)
	}
	return strings.TrimPrefix(tag, "v"), nil
}

// Newer reports whether version a is after b, both as "0.8.2".
func Newer(a, b string) bool {
	x, y := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(x) || i < len(y); i++ {
		p, q := part(x, i), part(y, i)
		if p < 0 || q < 0 {
			return false
		}
		if p != q {
			return p > q
		}
	}
	return false
}

func part(s []string, i int) int {
	if i >= len(s) {
		return 0
	}
	n, err := strconv.Atoi(s[i])
	if err != nil {
		return -1
	}
	return n
}

var installing sync.Mutex

// Install puts release version in place of the binary running now, which
// takes it on its next start.
func Install(ctx context.Context, version string) error {
	installing.Lock()
	defer installing.Unlock()

	exe, err := os.Executable()
	if err == nil {
		exe, err = filepath.EvalSymlinks(exe)
	}
	if err != nil {
		return fmt.Errorf("cannot tell where dv is installed: %w", err)
	}
	name, ext := "dv", ".tar.gz"
	if runtime.GOOS == "windows" {
		name, ext = "dv.exe", ".zip"
	}
	asset := "dv_" + runtime.GOOS + "_" + runtime.GOARCH + ext
	base := repo + "/releases/download/v" + version + "/"

	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	archive, err := fetch(ctx, base+asset)
	if err != nil {
		return err
	}
	sums, err := fetch(ctx, base+"checksums.txt")
	if err != nil {
		return err
	}
	if !matches(sums, asset, archive) {
		return fmt.Errorf("%s does not match its checksum; nothing was installed", asset)
	}
	bin, err := extract(archive, ext, name)
	if err != nil {
		return err
	}
	return replace(exe, bin)
}

func fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("could not download %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func matches(sums []byte, asset string, archive []byte) bool {
	got := sha256.Sum256(archive)
	for _, line := range strings.Split(string(sums), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[1] == asset {
			return f[0] == hex.EncodeToString(got[:])
		}
	}
	return false
}

func extract(archive []byte, ext, name string) ([]byte, error) {
	if ext == ".zip" {
		zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
		if err != nil {
			return nil, err
		}
		for _, f := range zr.File {
			if path.Clean(f.Name) == name {
				rc, err := f.Open()
				if err != nil {
					return nil, err
				}
				defer rc.Close()
				return io.ReadAll(rc)
			}
		}
	} else {
		gz, err := gzip.NewReader(bytes.NewReader(archive))
		if err != nil {
			return nil, err
		}
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return nil, err
			}
			if path.Clean(h.Name) == name {
				return io.ReadAll(tr)
			}
		}
	}
	return nil, fmt.Errorf("the release's archive has no %s in it", name)
}

// replace writes the new binary beside the old one and renames it over it, so
// no moment has half a binary at the path. Windows will not rename over a
// program that is running, but will move it aside.
func replace(exe string, bin []byte) error {
	dir := filepath.Dir(exe)
	tmp, err := os.CreateTemp(dir, ".dv.new-*")
	if err != nil {
		return fmt.Errorf("dv cannot write to %s, where it is installed: %w", dir, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bin); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	if runtime.GOOS == "windows" {
		old := exe + ".old"
		os.Remove(old) // the one moved aside last time, no longer running
		if err := os.Rename(exe, old); err != nil {
			return err
		}
		if err := os.Rename(tmp.Name(), exe); err != nil {
			os.Rename(old, exe)
			return err
		}
		return nil
	}
	return os.Rename(tmp.Name(), exe)
}
