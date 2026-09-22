package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"dv/internal/gitx"
	"dv/internal/server"
	"dv/internal/store"
)

// ownerRepo is GitHub's shorthand for a repository.
var ownerRepo = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]*/[A-Za-z0-9._-]+$`)

// handleClone starts cloning: { source, into, name }, source a URL git takes or
// GitHub's owner/repo, into the folder to clone inside, and name the folder to
// clone as, "" for the one git would make.
func (h *Hub) handleClone(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Source string `json:"source"`
		Into   string `json:"into"`
		Name   string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	source := strings.TrimSpace(body.Source)
	if source == "" || strings.HasPrefix(source, "-") || strings.ContainsAny(source, " \t\n") {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("%q is not something git can clone", body.Source))
		return
	}
	url := source
	if ownerRepo.MatchString(source) {
		url = "https://github.com/" + strings.TrimSuffix(source, ".git") + ".git"
	}
	into, err := expand(body.Into)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if fi, err := os.Stat(into); err != nil || !fi.IsDir() {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("%s is not a folder", into))
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = repoName(url)
	}
	if err := plainName(name); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	dest := filepath.Join(into, name)
	if _, err := os.Lstat(dest); err == nil {
		writeErr(w, http.StatusConflict, fmt.Errorf("%s already exists; add it instead, or clone it as another name", server.HomeRelative(dest)))
		return
	}
	if h.busy(dest) != nil {
		writeErr(w, http.StatusConflict, fmt.Errorf("%s is already being cloned", server.HomeRelative(dest)))
		return
	}

	j := &job{Kind: "clone", Title: source, Path: dest, Place: server.HomeRelative(dest), Step: "Cloning"}
	writeJSON(w, http.StatusOK, h.start(j, func(ctx context.Context) (string, error) {
		cmd := exec.CommandContext(ctx, "git", "clone", "--progress", "--", url, dest)
		cmd.Env = gitx.QuietEnv()
		if err := h.run(j, cmd); err != nil {
			return "", err
		}
		if _, err := h.folders.Add(store.Folder{Path: dest}); err != nil {
			return "", fmt.Errorf("cloned, but could not add it: %w", err)
		}
		h.folders.SetCloneInto(into)
		return "", nil
	}))
}

// repoName is the folder git clone would make for url.
func repoName(url string) string {
	url = strings.TrimRight(url, "/")
	if i := strings.LastIndexAny(url, "/:"); i >= 0 {
		url = url[i+1:]
	}
	return strings.TrimSuffix(url, ".git")
}

// plainName is an error unless name is one folder's name, not a path.
func plainName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, "-") {
		return fmt.Errorf("%q cannot name a folder here", name)
	}
	return nil
}
