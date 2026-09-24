package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const notesName = "notes.json"

// ErrNoNote and NoteError are the caller's to fix, as a file that failed is not.
var ErrNoNote = errors.New("no such note")

type NoteError string

func (e NoteError) Error() string { return string(e) }

// Note is something to come back to in a repository, kept apart from any one
// review, branch or worktree: the reader's, or an agent's through `dv note`.
type Note struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Body      string    `json:"body,omitempty"`
	Status    string    `json:"status"`             // open | doing | done
	Priority  string    `json:"priority,omitempty"` // high | medium | low, "" for none
	Labels    []string  `json:"labels,omitempty"`
	Author    string    `json:"author"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// NotePatch changes the fields that are set.
type NotePatch struct {
	Title    *string   `json:"title"`
	Body     *string   `json:"body"`
	Status   *string   `json:"status"`
	Priority *string   `json:"priority"`
	Labels   *[]string `json:"labels"`
}

type notesDoc struct {
	Format int     `json:"format"`
	Notes  []*Note `json:"notes"`
}

func emptyNotes() notesDoc { return notesDoc{Format: format, Notes: []*Note{}} }

// Notes owns a repository's notes file.
type Notes struct {
	mu   sync.Mutex
	file jsonFile
	doc  notesDoc
}

// OpenNotes loads the notes of the repository at root. In git they are kept in
// commonDir, which all its worktrees share and git never tracks; a plain
// folder keeps them in .dv. Like Open, it creates nothing.
func OpenNotes(root, commonDir string) (*Notes, error) {
	path := filepath.Join(root, dirName, notesName)
	if commonDir != "" {
		path = filepath.Join(commonDir, "dv", notesName)
	}
	n := &Notes{file: jsonFile{path: path}, doc: emptyNotes()}
	if err := n.sync(); err != nil {
		return nil, err
	}
	return n, nil
}

// sync picks up a change made outside this process. Callers hold n.mu.
func (n *Notes) sync() error {
	doc := emptyNotes()
	changed, err := n.file.load(&doc)
	if err != nil || !changed {
		return err
	}
	if doc.Notes == nil {
		doc.Notes = []*Note{}
	}
	n.doc = doc
	return nil
}

func (n *Notes) Path() string { return n.file.path }

// Version moves whenever the notes do, whoever changed them.
func (n *Notes) Version() string {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sync()
	return n.file.stamp
}

// List is every note, oldest first; the page sorts them.
func (n *Notes) List() []Note {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.sync()
	out := make([]Note, len(n.doc.Notes))
	for i, x := range n.doc.Notes {
		out[i] = *x
		out[i].Labels = slices.Clone(x.Labels)
	}
	return out
}

// Add files a note. Its ID, author and times are set here, but for a creation
// time given, as a deleted note put back has; its status is open unless given.
func (n *Notes) Add(note Note, author string) (Note, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.sync(); err != nil {
		return Note{}, err
	}
	note.Title = strings.TrimSpace(note.Title)
	note.Body = strings.TrimSpace(note.Body)
	if note.Status == "" {
		note.Status = "open"
	}
	if err := checkNote(&note); err != nil {
		return Note{}, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	note.ID = newID("n")
	note.Author = author
	note.Labels = n.spell(note.Labels)
	note.UpdatedAt = now
	if note.CreatedAt.IsZero() || note.CreatedAt.After(now) {
		note.CreatedAt = now
	}
	n.doc.Notes = append(n.doc.Notes, &note)
	return note, n.file.save(n.doc)
}

// Update changes a note by the fields p sets.
func (n *Notes) Update(id string, p NotePatch) (Note, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.sync(); err != nil {
		return Note{}, err
	}
	i := slices.IndexFunc(n.doc.Notes, func(x *Note) bool { return x.ID == id })
	if i < 0 {
		return Note{}, fmt.Errorf("%w: %s", ErrNoNote, id)
	}
	note := *n.doc.Notes[i]
	if p.Title != nil {
		note.Title = strings.TrimSpace(*p.Title)
	}
	if p.Body != nil {
		note.Body = strings.TrimSpace(*p.Body)
	}
	if p.Status != nil {
		note.Status = *p.Status
	}
	if p.Priority != nil {
		note.Priority = *p.Priority
	}
	if p.Labels != nil {
		note.Labels = n.spell(*p.Labels)
	}
	if err := checkNote(&note); err != nil {
		return Note{}, err
	}
	note.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	n.doc.Notes[i] = &note
	return note, n.file.save(n.doc)
}

func (n *Notes) Delete(id string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if err := n.sync(); err != nil {
		return err
	}
	i := slices.IndexFunc(n.doc.Notes, func(x *Note) bool { return x.ID == id })
	if i < 0 {
		return fmt.Errorf("%w: %s", ErrNoNote, id)
	}
	n.doc.Notes = slices.Delete(n.doc.Notes, i, i+1)
	return n.file.save(n.doc)
}

// Priorities are the ones a note can have, highest first, "" for none.
var Priorities = []string{"high", "medium", "low", ""}

var statuses = []string{"open", "doing", "done"}

func checkNote(note *Note) error {
	switch {
	case note.Title == "":
		return NoteError("a note needs a title")
	case !slices.Contains(statuses, note.Status):
		return NoteError(fmt.Sprintf("status %q is not one of %s", note.Status, strings.Join(statuses, ", ")))
	case !slices.Contains(Priorities, note.Priority):
		return NoteError(fmt.Sprintf("priority %q is not one of high, medium, low", note.Priority))
	}
	return nil
}

// spell tidies labels and writes each as the notes already have it, so
// "Relay" and "relay" stay one label. Callers hold n.mu.
func (n *Notes) spell(labels []string) []string {
	known := map[string]string{}
	for _, x := range n.doc.Notes {
		for _, l := range x.Labels {
			known[strings.ToLower(l)] = l
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, l := range labels {
		l = strings.Join(strings.Fields(l), " ")
		key := strings.ToLower(l)
		if l == "" || seen[key] {
			continue
		}
		seen[key] = true
		if k, ok := known[key]; ok {
			l = k
		}
		out = append(out, l)
	}
	return out
}
