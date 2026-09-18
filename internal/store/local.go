package store

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// A dv that asks browsers for a key (-auth) takes the local token instead
// from dv's own requests: Claude Code's hooks, a dv asking whether another
// answers, a dv trying a new build. It is a secret of the user's on this
// computer, made by the first dv to ask for a key.
const (
	localName   = "local.key"
	LocalHeader = "X-Dv-Local"
)

func localPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, localName), nil
}

// LocalToken is the local token, made if there is none yet.
func LocalToken() (string, error) {
	path, err := localPath()
	if err != nil {
		return "", err
	}
	if b, err := os.ReadFile(path); err == nil {
		return strings.TrimSpace(string(b)), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	b := make([]byte, 32)
	rand.Read(b)
	token := hex.EncodeToString(b)
	// Written whole elsewhere and linked in, which fails if another dv made one
	// first: no dv reads a token half written, or has its own overwritten.
	tmp, err := os.CreateTemp(filepath.Dir(path), localName+".*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.WriteString(token)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if err := os.Link(tmp.Name(), path); errors.Is(err, fs.ErrExist) {
		b, err := os.ReadFile(path)
		return strings.TrimSpace(string(b)), err
	} else if err != nil {
		return "", err
	}
	return token, nil
}

// MarkLocal has one of dv's own requests carry the local token, if one has
// been made.
func MarkLocal(req *http.Request) {
	path, err := localPath()
	if err != nil {
		return
	}
	if b, err := os.ReadFile(path); err == nil {
		req.Header.Set(LocalHeader, strings.TrimSpace(string(b)))
	}
}
