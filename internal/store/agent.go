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
	Format    int                 `json:"format"`
	Open      []string            `json:"open"`                // most recently opened first
	Temporary []string            `json:"temporary,omitempty"` // left out of the list once closed
	Kept      []string            `json:"kept,omitempty"`      // never stopped for being idle, and started with dv
	Rewinds   map[string]Rewind   `json:"rewinds,omitempty"`
	Switches  map[string][]Switch `json:"switches,omitempty"`
	// Agents names the agent of a session that is not Claude Code's: "codex".
	Agents map[string]string `json:"agents,omitempty"`
	// Via names the chat app a session's last message came through, when it
	// was not dv's page: "Telegram".
	Via map[string]string `json:"via,omitempty"`
	// Picks are the model and effort picked for a Claude Code session, which
	// its transcript does not keep for a resume to start on.
	Picks map[string]Pick `json:"picks,omitempty"`
}

// Pick is a session's model and effort, "" for the user's settings'.
type Pick struct {
	Model  string `json:"model,omitempty"`
	Effort string `json:"effort,omitempty"`
}

// Switch is a change of permission mode made in dv, or of model. Claude Code
// records a mode only with the next message, and of a model only which one
// answered, so dv keeps where in the conversation it was.
type Switch struct {
	After string       `json:"after"`        // the transcript's last entry then
	To    string       `json:"to,omitempty"` // the mode
	Model *ModelSwitch `json:"model,omitempty"`
}

// ModelSwitch is a change of model: picked in dv, or made by Claude Code on its
// own, as when a model's safeguards decline a message and another answers it.
type ModelSwitch struct {
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
	// Why is Claude Code's notice of it, as its terminal shows it; "" for one
	// picked in dv.
	Why string `json:"why,omitempty"`
}

// maxSwitches is how many a session keeps, the latest.
const maxSwitches = 100

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

// TemporaryIDs lists the sessions marked temporary.
func (s *Sessions) TemporaryIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sync()
	return slices.Clone(s.doc.Temporary)
}

// SetTemporary marks a session temporary or not.
func (s *Sessions) SetTemporary(id string, on bool) error {
	return s.mark(&s.doc.Temporary, id, on)
}

// KeptIDs lists the sessions kept running.
func (s *Sessions) KeptIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sync()
	return slices.Clone(s.doc.Kept)
}

// SetKept marks a session kept running or not.
func (s *Sessions) SetKept(id string, on bool) error {
	return s.mark(&s.doc.Kept, id, on)
}

// mark puts id in the list or takes it out. The list is the doc's, read
// again once the file is synced.
func (s *Sessions) mark(list *[]string, id string, on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.sync(); err != nil {
		return err
	}
	i := slices.Index(*list, id)
	switch {
	case on && i < 0:
		*list = append(*list, id)
	case !on && i >= 0:
		*list = slices.Delete(*list, i, i+1)
	default:
		return nil
	}
	return s.file.save(s.doc)
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

// Switches lists a session's changes of mode, oldest first.
func (s *Sessions) Switches(id string) []Switch {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sync()
	return slices.Clone(s.doc.Switches[id])
}

// AddSwitch records a change of mode or model. A model's that repeats the
// last one's, as Claude Code can say twice, takes its place.
func (s *Sessions) AddSwitch(id string, sw Switch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.sync(); err != nil {
		return err
	}
	list := s.doc.Switches[id]
	if n := len(list); n > 0 && sw.Model != nil && list[n-1].Model != nil && list[n-1].After == sw.After &&
		list[n-1].Model.From == sw.Model.From && list[n-1].Model.To == sw.Model.To {
		list = list[:n-1]
	}
	list = append(slices.Clone(list), sw)
	if len(list) > maxSwitches {
		list = list[len(list)-maxSwitches:]
	}
	if s.doc.Switches == nil {
		s.doc.Switches = map[string][]Switch{}
	}
	s.doc.Switches[id] = list
	return s.file.save(s.doc)
}

// Agent is the agent a session was recorded as, "" for Claude Code.
func (s *Sessions) Agent(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sync()
	return s.doc.Agents[id]
}

// SetAgent records a session's agent.
func (s *Sessions) SetAgent(id, agent string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.sync(); err != nil {
		return err
	}
	if s.doc.Agents[id] == agent {
		return nil
	}
	if s.doc.Agents == nil {
		s.doc.Agents = map[string]string{}
	}
	s.doc.Agents[id] = agent
	return s.file.save(s.doc)
}

// Via is the chat app a session's last message came through, "" for dv's page.
func (s *Sessions) Via(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sync()
	return s.doc.Via[id]
}

func (s *Sessions) SetVia(id, via string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.sync(); err != nil {
		return err
	}
	if s.doc.Via[id] == via {
		return nil
	}
	if via == "" {
		delete(s.doc.Via, id)
	} else {
		if s.doc.Via == nil {
			s.doc.Via = map[string]string{}
		}
		s.doc.Via[id] = via
	}
	return s.file.save(s.doc)
}

// Picked is the model and effort a session was last given.
func (s *Sessions) Picked(id string) Pick {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sync()
	return s.doc.Picks[id]
}

func (s *Sessions) SetPicked(id string, p Pick) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.sync(); err != nil {
		return err
	}
	if s.doc.Picks[id] == p {
		return nil
	}
	if p == (Pick{}) {
		delete(s.doc.Picks, id)
	} else {
		if s.doc.Picks == nil {
			s.doc.Picks = map[string]Pick{}
		}
		s.doc.Picks[id] = p
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
