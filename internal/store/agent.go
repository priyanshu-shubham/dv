package store

import (
	"path/filepath"
	"slices"
	"sync"
)

const agentName = "agent.json"

// Sessions are the Claude Code sessions opened in dv's Agent view. A session
// running in a terminal has its prompts put to the page only while it is open
// here, so the set outlives the tab and is the same in every one. It also holds
// the rewinds not yet made real: a transcript only branches when the next
// message is written, and until then dv is all that knows where it goes.
type Sessions struct {
	mu   sync.Mutex
	file jsonFile
	doc  sessionsDoc
}

type sessionsDoc struct {
	Format  int               `json:"format"`
	Open    []string          `json:"open"` // most recently opened first
	Rewinds map[string]Rewind `json:"rewinds,omitempty"`
}

// Rewind is a session taken back to an earlier message.
type Rewind struct {
	At   string `json:"at"`             // the message the conversation now ends at
	Last string `json:"last,omitempty"` // the transcript's last entry when it was rewound
	// Sent is set once a message has gone after the rewind, so a later start
	// continues from what that message made rather than rewinding again.
	Sent bool `json:"sent,omitempty"`
}

// OpenSessions loads the set for a repository. Like Open, it creates nothing.
func OpenSessions(repoRoot string) (*Sessions, error) {
	s := &Sessions{file: jsonFile{path: filepath.Join(repoRoot, dirName, agentName)}, doc: sessionsDoc{Format: format}}
	if err := s.sync(); err != nil {
		return nil, err
	}
	return s, nil
}

// sync picks up a change made outside this process. Callers hold s.mu.
func (s *Sessions) sync() error {
	doc := sessionsDoc{Format: format}
	changed, err := s.file.load(&doc)
	if err == nil && changed {
		s.doc = doc
	}
	return err
}

// IDs lists the open sessions, most recently opened first.
func (s *Sessions) IDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sync()
	return slices.Clone(s.doc.Open)
}

// Has reports whether a session is open.
func (s *Sessions) Has(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sync()
	return slices.Contains(s.doc.Open, id)
}

// Rewound returns a session's rewind, if it has one.
func (s *Sessions) Rewound(id string) (Rewind, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sync()
	r, ok := s.doc.Rewinds[id]
	return r, ok
}

// SetRewound records a session's rewind, or with nil forgets it.
func (s *Sessions) SetRewound(id string, r *Rewind) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.sync(); err != nil {
		return err
	}
	if r == nil {
		if _, ok := s.doc.Rewinds[id]; !ok {
			return nil
		}
		delete(s.doc.Rewinds, id)
	} else {
		if s.doc.Rewinds == nil {
			s.doc.Rewinds = map[string]Rewind{}
		}
		s.doc.Rewinds[id] = *r
	}
	return s.file.save(s.doc)
}

// Set opens or closes a session, reporting whether that changed anything.
func (s *Sessions) Set(id string, open bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.sync(); err != nil {
		return false, err
	}
	i := slices.Index(s.doc.Open, id)
	switch {
	case open && i < 0:
		s.doc.Open = slices.Insert(s.doc.Open, 0, id)
	case !open && i >= 0:
		s.doc.Open = slices.Delete(s.doc.Open, i, i+1)
	default:
		return false, nil
	}
	return true, s.file.save(s.doc)
}
