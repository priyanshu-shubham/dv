package notify

import (
	"strings"
	"unicode"
)

// Defaults are where a session begun from a chat app starts when its message
// does not say: the folder, by its slug ("" asks which), and whether in a new
// worktree of it.
type Defaults struct {
	Folder   string
	Worktree bool
}

// SetDefaults has the defaults read from defaults, as they are when asked.
func (c *Center) SetDefaults(defaults func() Defaults) {
	c.mu.Lock()
	c.defaults = defaults
	c.mu.Unlock()
}

func (c *Center) Defaults() Defaults {
	c.mu.Lock()
	d := c.defaults
	c.mu.Unlock()
	if d == nil {
		return Defaults{}
	}
	return d()
}

// Where is where a message says its session is to start, in words before a
// colon at its start: a folder's name, "wt" for a new worktree - the branch
// after it, if named - "here" for the folder itself, or "task" alone for a
// new one-off task. "notes wt fix/login:" is a new worktree of notes, on the
// branch fix/login.
type Where struct {
	Place    *Place
	Worktree bool
	Here     bool
	Branch   string
	Task     bool
}

// ParseWhere reads where text says to start, and returns what is left for
// the session. Words before a colon that are not all of those are the
// message's own - "Note: …" - and it is left whole.
func ParseWhere(text string, places []Place) (Where, string) {
	head, rest, ok := strings.Cut(text, ":")
	if !ok || strings.Contains(head, "\n") {
		return Where{}, text
	}
	words := strings.Fields(head)
	if len(words) == 0 || len(words) > 4 {
		return Where{}, text
	}
	var w Where
	for i := 0; i < len(words); i++ {
		word := words[i]
		switch lower := strings.ToLower(word); {
		case lower == "wt" || lower == "worktree":
			w.Worktree = true
			// The next word is the branch, unless it says something else.
			if i+1 < len(words) && !keyword(words[i+1]) && branchy(words[i+1]) {
				if _, folder := Named(places, words[i+1]); !folder {
					i++
					w.Branch = words[i]
				}
			}
		case lower == "here":
			w.Here = true
		case lower == "task":
			w.Task = true
		default:
			p, ok := Named(places, word)
			if !ok || w.Place != nil {
				return Where{}, text
			}
			w.Place = &p
		}
	}
	if w.Worktree && w.Here || w.Task && len(words) > 1 {
		return Where{}, text
	}
	return w, strings.TrimSpace(rest)
}

func keyword(word string) bool {
	switch strings.ToLower(word) {
	case "wt", "worktree", "here", "task":
		return true
	}
	return false
}

// Named is the folder a word names, by its slug or its name.
func Named(places []Place, word string) (Place, bool) {
	for _, p := range places {
		if strings.EqualFold(word, p.Slug) || strings.EqualFold(word, p.Name) {
			return p, true
		}
	}
	return Place{}, false
}

// branchy is whether a word could be a branch's name, as git takes one.
func branchy(word string) bool {
	return !strings.ContainsFunc(word, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || strings.ContainsRune("~^:?*[\\", r)
	})
}
