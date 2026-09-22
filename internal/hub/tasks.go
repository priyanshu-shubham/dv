package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"dv/internal/server"
	"dv/internal/store"
)

// tasksDir is where the folders of one-off tasks go: dv's own, so closing one
// can delete it whole without it ever being someone's work elsewhere.
func tasksDir() (string, error) {
	dir, err := store.DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "tasks"), nil
}

// handleNewTask makes a folder for a one-off task and puts it on the hub: {
// name }, what its card says, "" for the folder's own.
func (h *Hub) handleNewTask(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	json.NewDecoder(r.Body).Decode(&body)
	f, err := h.newTask(body.Name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// newTask makes a task's folder and lists it. It is a git repository, so the
// Diff view shows what was done in it.
func (h *Hub) newTask(name string) (store.Folder, error) {
	root, err := tasksDir()
	if err == nil {
		err = os.MkdirAll(root, 0o755)
	}
	if err != nil {
		return store.Folder{}, err
	}
	path, err := makeTaskDir(root, time.Now())
	if err != nil {
		return store.Folder{}, err
	}
	// Without git it is still a folder to work in, only with no diff.
	exec.Command("git", "init", "-q", path).Run()
	f, err := h.folders.Add(store.Folder{Path: path, Name: strings.TrimSpace(name), Task: true})
	if err != nil {
		os.RemoveAll(path)
	}
	return f, err
}

// makeTaskDir makes task-<month><day>-<hour><minute> in root, numbered on
// past one taken.
func makeTaskDir(root string, now time.Time) (string, error) {
	base := filepath.Join(root, "task-"+now.Format("0102-1504"))
	path := base
	for i := 2; ; i++ {
		err := os.Mkdir(path, 0o755)
		if err == nil {
			return path, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", err
		}
		path = base + "-" + strconv.Itoa(i)
	}
}

// handleCloseTask closes a task: its sessions stop, and its folder is deleted
// and taken off the hub.
func (h *Hub) handleCloseTask(w http.ResponseWriter, r *http.Request) {
	j, status, err := h.closeTask(r.PathValue("slug"))
	if err != nil {
		writeErr(w, status, err)
		return
	}
	h.mu.Lock()
	v := j.view()
	h.mu.Unlock()
	writeJSON(w, http.StatusOK, v)
}

// closeTask starts the job closing the task at slug, or says why it cannot,
// with the HTTP status for it.
func (h *Hub) closeTask(slug string) (*job, int, error) {
	f, ok := h.folders.Get(slug)
	if !ok || !f.Task {
		return nil, http.StatusNotFound, fmt.Errorf("no task at /%s/ in this hub", slug)
	}
	// hub.json is a file anyone can edit: only a folder of dv's own is deleted.
	root, err := tasksDir()
	if err != nil {
		return nil, http.StatusInternalServerError, err
	}
	if rel, err := filepath.Rel(root, f.Path); err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return nil, http.StatusBadRequest, fmt.Errorf("%s is not a task's folder, so it stays; take it off the hub instead", server.HomeRelative(f.Path))
	}
	if h.busy(f.Path) != nil {
		return nil, http.StatusConflict, fmt.Errorf("%s is busy; wait for it to finish", displayName(f))
	}
	h.forget(f.Path)
	j := &job{Kind: "discard", Title: displayName(f), Path: f.Path, Place: server.HomeRelative(f.Path), Slug: f.Slug, Step: "Deleting"}
	h.start(j, func(ctx context.Context) (string, error) {
		h.closeReview(f.Slug)
		if err := os.RemoveAll(f.Path); err != nil {
			return "", err
		}
		if _, listed := h.folders.Get(f.Slug); listed {
			return "", h.folders.Remove(f.Slug)
		}
		return "", nil
	})
	return j, 0, nil
}
