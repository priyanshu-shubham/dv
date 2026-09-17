package hub

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"dv/internal/server"
)

// maxDirs keeps a folder with thousands inside from making one huge answer.
const maxDirs = 2000

type dirEntry struct {
	Name string `json:"name"`
	Git  bool   `json:"git,omitempty"`
}

// handleDirs lists the folders inside one, for picking a folder from a page
// that may be on a phone, with no file dialog onto this machine: ?path=, the
// home folder when empty. Hidden folders are left out.
func (h *Hub) handleDirs(w http.ResponseWriter, r *http.Request) {
	path, err := expand(r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	dirs := []dirEntry{}
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(path, name)
		if !e.IsDir() {
			// A link to a folder is followed, as a shell's cd would.
			if e.Type()&os.ModeSymlink == 0 {
				continue
			}
			if fi, err := os.Stat(full); err != nil || !fi.IsDir() {
				continue
			}
		}
		dirs = append(dirs, dirEntry{Name: name, Git: isRepo(full)})
	}
	slices.SortFunc(dirs, func(a, b dirEntry) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	more := max(0, len(dirs)-maxDirs)
	parent, parentPlace := filepath.Dir(path), ""
	if parent == path {
		parent = ""
	} else {
		parentPlace = server.HomeRelative(parent)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":        path,
		"place":       server.HomeRelative(path),
		"parent":      parent,
		"parentPlace": parentPlace,
		"git":         isRepo(path),
		"dirs":        dirs[:len(dirs)-more],
		"more":        more,
	})
}

// handleMakeDir makes a folder from the picker: { in, name }. One already
// there is not an error, as the listing can leave it out: hidden, past
// maxDirs, or named in another case on a filesystem that ignores case.
func (h *Hub) handleMakeDir(w http.ResponseWriter, r *http.Request) {
	var body struct {
		In   string `json:"in"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	in, err := expand(body.In)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	name := strings.TrimSpace(body.Name)
	if err := plainName(name); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	path := filepath.Join(in, name)
	if err := os.Mkdir(path, 0o755); err != nil {
		if fi, serr := os.Stat(path); !errors.Is(err, fs.ErrExist) || serr != nil || !fi.IsDir() {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"path": path, "place": server.HomeRelative(path)})
}

func isRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// expand reads a path typed in the page: ~ is the home folder, and so is
// where a relative path starts, since the hub's own working folder means
// nothing to someone on a phone.
func expand(p string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	p = strings.TrimSpace(p)
	switch {
	case p == "" || p == "~":
		return home, nil
	case strings.HasPrefix(p, "~/"):
		p = filepath.Join(home, p[2:])
	case !filepath.IsAbs(p):
		p = filepath.Join(home, p)
	}
	return filepath.Clean(p), nil
}
