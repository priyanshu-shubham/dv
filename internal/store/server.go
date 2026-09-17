package store

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// serverName records where the repository's dv is listening, for `dv claude
// hook` - which Claude Code runs from wherever the session is - to find it.
const serverName = "server.json"

type Server struct {
	URL string `json:"url"`
	PID int    `json:"pid"`
}

// Announce records this process as the dv serving root. The func it returns
// takes the record down again, unless another dv has since announced itself.
func Announce(root, url string) (func(), error) {
	f := &jsonFile{path: filepath.Join(root, dirName, serverName)}
	me := Server{URL: url, PID: os.Getpid()}
	if err := f.save(me); err != nil {
		return nil, err
	}
	return func() {
		if s, ok := readServer(f.path); ok && s == me {
			os.Remove(f.path)
		}
	}, nil
}

// FindServer returns the dv announced for the repository or folder holding
// dir. The walk stops at the first directory with a .git in it, so a nested
// repository never reaches its parent's dv.
func FindServer(dir string) (Server, bool) {
	for dir != "" {
		// A dv on a folder outside git announces itself where it runs.
		if s, ok := readServer(filepath.Join(dir, dirName, serverName)); ok {
			return s, true
		}
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return Server{}, false
		}
		up := filepath.Dir(dir)
		if up == dir {
			break
		}
		dir = up
	}
	return Server{}, false
}

func readServer(path string) (Server, bool) {
	var s Server
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &s) != nil || s.URL == "" {
		return Server{}, false
	}
	return s, true
}
