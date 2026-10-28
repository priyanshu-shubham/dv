package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"dv/internal/notify"
	"dv/internal/store"
)

// NotifyAfter reads how long the reader may go without using dv before a
// notice is sent on to them: the setting notifyAfter, in seconds.
func NotifyAfter(user *store.Prefs) func() time.Duration {
	return func() time.Duration {
		var s struct {
			NotifyAfter *int `json:"notifyAfter"`
		}
		json.Unmarshal(user.All()["settings"], &s)
		if s.NotifyAfter == nil {
			return time.Minute
		}
		return time.Duration(*s.NotifyAfter) * time.Second
	}
}

// PromptSuggestions reads whether Claude sessions guess the next message after
// each turn: the setting promptSuggestions, on unless turned off.
func PromptSuggestions(user *store.Prefs) func() bool {
	return func() bool {
		if user == nil {
			return false
		}
		var s struct {
			On *bool `json:"promptSuggestions"`
		}
		json.Unmarshal(user.All()["settings"], &s)
		return s.On == nil || *s.On
	}
}

// AgentInstructions reads what dv adds to a session's own instructions as it
// starts: how to show files in a reply, unless the setting filesInReplies is off.
func AgentInstructions(user *store.Prefs) func() string {
	return func() string {
		if user == nil {
			return ""
		}
		var s struct {
			On *bool `json:"filesInReplies"`
		}
		json.Unmarshal(user.All()["settings"], &s)
		if s.On != nil && !*s.On {
			return ""
		}
		return filesNote
	}
}

const filesNote = "You are running in dv, a web page where the user reads your replies. A line of its own reading ![title](path) shows that file in your reply: a picture, video, sound, PDF, or an HTML page, which runs in place with its scripts and can load files beside it by relative path. When something would come across better interactively than as text - a chart of real data, something to explore with controls, a UI mock-up - write it as an HTML page and show it this way. Unless the page belongs in the project, put it under .dv/pages/ in your working directory, which git ignores. Paths are absolute or relative to your working directory. A reply read in a chat app shows only its text."

// CloseIdleAfter reads how long an open session may go without a message
// before it moves to Recent: the setting closeIdleAfter, in seconds, a day
// unless set and never at 0.
func CloseIdleAfter(user *store.Prefs) func() time.Duration {
	return func() time.Duration {
		if user == nil {
			return 0
		}
		var s struct {
			After *int `json:"closeIdleAfter"`
		}
		json.Unmarshal(user.All()["settings"], &s)
		if s.After == nil {
			return 24 * time.Hour
		}
		return time.Duration(*s.After) * time.Second
	}
}

// UnaskedEdits reads the globs of the files Auto, ask before edits does not ask
// about: the pref unaskedEdits, one a line.
func UnaskedEdits(user *store.Prefs) func() []string {
	return func() []string {
		if user == nil {
			return nil
		}
		var text string
		json.Unmarshal(user.All()["unaskedEdits"], &text)
		return strings.Split(text, "\n")
	}
}

// ChatDefaults reads where a session begun from a chat app starts when its
// message does not say: the settings chatFolder and chatWorktree.
func ChatDefaults(user *store.Prefs) func() notify.Defaults {
	return func() notify.Defaults {
		var s struct {
			ChatFolder   string `json:"chatFolder"`
			ChatWorktree bool   `json:"chatWorktree"`
		}
		json.Unmarshal(user.All()["settings"], &s)
		return notify.Defaults{Folder: s.ChatFolder, Worktree: s.ChatWorktree}
	}
}

func (s *Server) allPrefs() map[string]map[string]json.RawMessage {
	return map[string]map[string]json.RawMessage{"user": s.user.All(), "repo": s.prefs.All()}
}

func (s *Server) prefsVersion() string { return s.user.Version() + "/" + s.prefs.Version() }

func (s *Server) handlePrefs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.allPrefs())
}

// handleSetPref stores one value: { where: "user" or "repo", key, value }, where
// a null value deletes the key.
func (s *Server) handleSetPref(w http.ResponseWriter, r *http.Request) {
	setPref(w, r, func(where, key string, value json.RawMessage) error {
		switch where {
		case "user":
			return s.user.Set(key, value, nil)
		case "repo":
			return s.prefs.Set(key, value, s.stillAsked)
		}
		return fmt.Errorf("where is user or repo, not %q", where)
	})
}

// UserPrefs serves /api/prefs for a page with no repository, the hub's: only
// the user's settings.
func UserPrefs(user *store.Prefs) (get, set http.HandlerFunc) {
	get = func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"user": user.All()})
	}
	set = func(w http.ResponseWriter, r *http.Request) {
		setPref(w, r, func(where, key string, value json.RawMessage) error {
			if where != "user" {
				return fmt.Errorf("the hub keeps only the user's settings, not %q", where)
			}
			return user.Set(key, value, nil)
		})
	}
	return get, set
}

func setPref(w http.ResponseWriter, r *http.Request, set func(where, key string, value json.RawMessage) error) {
	var body struct {
		Where string          `json:"where"`
		Key   string          `json:"key"`
		Value json.RawMessage `json:"value"`
	}
	// A draft, or code added to a message, can run long; not this long.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if body.Key == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("key is required"))
		return
	}
	if err := set(body.Where, body.Key, body.Value); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// stillAsked lets go of half-answered questions from requests that are gone,
// answered in a terminal or ended with their session.
func (s *Server) stillAsked(key string) bool {
	id, ok := strings.CutPrefix(key, "ask:")
	if !ok {
		return true
	}
	for _, r := range s.permit.Waiting() {
		if r.ID == id {
			return true
		}
	}
	return false
}
