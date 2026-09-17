// Package store persists a review inside the repository being reviewed: its
// comments (.dv/comments.json) and the files marked viewed (.dv/viewed.json).
// The directory is registered in .git/info/exclude rather than .gitignore so
// the notes never appear in git status and never end up in a commit — the
// repo's own ignore rules stay clean.
package store

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	dirName  = ".dv"
	fileName = "comments.json"
	format   = 1
)

// Comment is one message: the opening remark of a thread or a reply to it.
type Comment struct {
	ID        string     `json:"id"`
	Author    string     `json:"author"`
	Body      string     `json:"body"`
	CreatedAt time.Time  `json:"createdAt"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"` // pointer so an unedited comment omits the field
}

// Thread is a comment anchored to a place in the diff. Quote holds the lines it
// was attached to; when the file later changes underneath, that text is what
// lets a reader (or an agent) still find what was being discussed.
type Thread struct {
	ID        string   `json:"id"`
	File      string   `json:"file"`
	Side      string   `json:"side"`      // new | old | file
	StartLine int      `json:"startLine"` // 1-based; 0 for a file-level thread
	EndLine   int      `json:"endLine"`
	Quote     []string `json:"quote,omitempty"`
	Scope     string   `json:"scope,omitempty"`   // scope label when it was written
	BaseSHA   string   `json:"baseSha,omitempty"` // HEAD at the time, for provenance
	Resolved  bool     `json:"resolved"`
	// Origin is set on a thread written on one of Claude's edits in a session,
	// whose line numbers are the file's as that edit left it.
	Origin    *Origin   `json:"origin,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	Comments  []Comment `json:"comments"`
}

// Origin names the edit a thread was written on.
type Origin struct {
	Session string `json:"session"`
	Tool    string `json:"tool"` // the edit's tool_use id
}

// File is the on-disk document.
type File struct {
	Format  int       `json:"format"`
	Repo    string    `json:"repo,omitempty"`
	Threads []*Thread `json:"threads"`
}

// Store owns the comments file. Every mutation rewrites it.
type Store struct {
	mu   sync.Mutex
	file jsonFile
	name string
	doc  File
}

// Open loads (or initialises) the store for a repository. The .dv directory is
// not created here: opening a repo just to read a diff should leave nothing
// behind, so it appears the first time a comment is written.
func Open(repoRoot, repoName string) (*Store, error) {
	s := &Store{file: jsonFile{path: filepath.Join(repoRoot, dirName, fileName)}, name: repoName}
	s.doc = s.empty()
	if err := s.sync(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) empty() File {
	return File{Format: format, Repo: s.name, Threads: []*Thread{}}
}

// sync picks up a change made outside this process. Callers hold s.mu.
func (s *Store) sync() error {
	doc := s.empty()
	changed, err := s.file.load(&doc)
	if err != nil || !changed {
		return err
	}
	if doc.Threads == nil {
		doc.Threads = []*Thread{}
	}
	s.doc = doc
	return nil
}

// Path is the file comments live in, shown in the UI so it is obvious where the
// notes went.
func (s *Store) Path() string { return s.file.path }

// EnsureExcluded adds the store directory to .git/info/exclude when it is not
// already ignored. Returns whether the line was added. commonDir is the one a
// linked worktree shares with its main checkout: git reads info/exclude only
// from there, not from the worktree's own git dir.
func EnsureExcluded(commonDir string) (bool, error) {
	excl := filepath.Join(commonDir, "info", "exclude")
	if err := os.MkdirAll(filepath.Dir(excl), 0o755); err != nil {
		return false, err
	}
	b, err := os.ReadFile(excl)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	for _, line := range strings.Split(string(b), "\n") {
		switch strings.TrimSpace(line) {
		case "/" + dirName + "/", dirName + "/", "/" + dirName, dirName:
			return false, nil
		}
	}
	body := string(b)
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	body += "\n# dv review notes (local only)\n/" + dirName + "/\n"
	return true, os.WriteFile(excl, []byte(body), 0o644)
}

// save writes the document. Callers hold s.mu.
func (s *Store) save() error { return s.file.save(s.doc) }

// Version moves whenever the comments do, whoever changed them.
func (s *Store) Version() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sync()
	return s.file.stamp
}

// Reset deletes every thread, returning how many there were.
func (s *Store) Reset() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.sync(); err != nil {
		return 0, err
	}
	n := len(s.doc.Threads)
	s.doc = s.empty()
	return n, s.file.remove()
}

// Threads returns a copy sorted by file then line, so the UI's ordering does not
// depend on insertion order.
func (s *Store) Threads() []*Thread {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sync() // unreadable, it still has the last good copy to show
	out := make([]*Thread, len(s.doc.Threads))
	copy(out, s.doc.Threads)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		if out[i].StartLine != out[j].StartLine {
			return out[i].StartLine < out[j].StartLine
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// AddThread creates a thread with its opening comment.
func (s *Store) AddThread(t *Thread, body, author string) (*Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.sync(); err != nil {
		return nil, err
	}
	now := time.Now().UTC().Truncate(time.Second)
	t.ID = newID("t")
	t.CreatedAt, t.UpdatedAt = now, now
	t.Comments = []Comment{{ID: newID("c"), Author: author, Body: body, CreatedAt: now}}
	s.doc.Threads = append(s.doc.Threads, t)
	return t, s.save()
}

// AddReply appends to an existing thread and un-resolves it: a new remark on a
// settled thread means it is live again.
func (s *Store) AddReply(threadID, body, author string) (*Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.sync(); err != nil {
		return nil, err
	}
	t := s.find(threadID)
	if t == nil {
		return nil, fmt.Errorf("no such thread: %s", threadID)
	}
	now := time.Now().UTC().Truncate(time.Second)
	t.Comments = append(t.Comments, Comment{ID: newID("c"), Author: author, Body: body, CreatedAt: now})
	t.UpdatedAt = now
	t.Resolved = false
	return t, s.save()
}

// EditComment rewrites one message in place.
func (s *Store) EditComment(threadID, commentID, body string) (*Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.sync(); err != nil {
		return nil, err
	}
	t := s.find(threadID)
	if t == nil {
		return nil, fmt.Errorf("no such thread: %s", threadID)
	}
	now := time.Now().UTC().Truncate(time.Second)
	for i := range t.Comments {
		if t.Comments[i].ID == commentID {
			t.Comments[i].Body = body
			t.Comments[i].UpdatedAt = &now
			t.UpdatedAt = now
			return t, s.save()
		}
	}
	return nil, fmt.Errorf("no such comment: %s", commentID)
}

// DeleteComment removes one message, and the whole thread with it if that was
// the last one standing.
func (s *Store) DeleteComment(threadID, commentID string) (*Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.sync(); err != nil {
		return nil, err
	}
	t := s.find(threadID)
	if t == nil {
		return nil, fmt.Errorf("no such thread: %s", threadID)
	}
	kept := t.Comments[:0]
	for _, c := range t.Comments {
		if c.ID != commentID {
			kept = append(kept, c)
		}
	}
	t.Comments = kept
	if len(t.Comments) == 0 {
		s.removeLocked(threadID)
		return nil, s.save()
	}
	t.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	return t, s.save()
}

// SetResolved marks a thread settled or reopens it.
func (s *Store) SetResolved(threadID string, resolved bool) (*Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.sync(); err != nil {
		return nil, err
	}
	t := s.find(threadID)
	if t == nil {
		return nil, fmt.Errorf("no such thread: %s", threadID)
	}
	t.Resolved = resolved
	t.UpdatedAt = time.Now().UTC().Truncate(time.Second)
	return t, s.save()
}

// DeleteThread drops a thread and everything in it.
func (s *Store) DeleteThread(threadID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.sync(); err != nil {
		return err
	}
	if s.find(threadID) == nil {
		return fmt.Errorf("no such thread: %s", threadID)
	}
	s.removeLocked(threadID)
	return s.save()
}

func (s *Store) find(id string) *Thread {
	for _, t := range s.doc.Threads {
		if t.ID == id {
			return t
		}
	}
	return nil
}

func (s *Store) removeLocked(id string) {
	kept := s.doc.Threads[:0]
	for _, t := range s.doc.Threads {
		if t.ID != id {
			kept = append(kept, t)
		}
	}
	s.doc.Threads = kept
}

func newID(prefix string) string {
	var b [6]byte
	rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}
