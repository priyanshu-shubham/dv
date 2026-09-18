package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"dv/internal/agent"
	"dv/internal/notify"
	"dv/internal/permit"
)

// A turn counts as over once idle has held this long: a terminal's record can
// read idle for a moment between steps.
const settleFor = 1500 * time.Millisecond

// bodyChars bounds what a notice quotes: a reply, a plan or a tool's input can
// run long.
const bodyChars = 4000

// raiseNotices tells the reader, through the center, of the requests waiting
// on them and of turns that end, until ctx does.
func (s *Server) raiseNotices(ctx context.Context) {
	changed, stop := s.permit.Watch()
	defer stop()
	// A terminal's busy and idle are only in its process record, so they are
	// looked at on a tick.
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	asked := map[string]bool{}
	var busy map[string]bool       // by session, as last seen; nil before the first look
	idle := map[string]time.Time{} // sessions gone idle from busy, since when
	done := map[string]string{}    // by session, its turn's notice
	for {
		waiting := s.Waiting()
		still := map[string]bool{}
		for _, r := range waiting {
			still[r.ID] = true
			if !asked[r.ID] {
				asked[r.ID] = true
				s.notices.Raise(s.askNotice(r))
			}
		}
		for id := range asked {
			if !still[id] {
				delete(asked, id)
				s.notices.Settle(id, "No longer waiting")
			}
		}

		// A folder first looked at only has its sessions noted: nothing ended yet.
		was := busy
		busy = map[string]bool{}
		sessions := map[string]agent.Activity{}
		for _, a := range s.agent.Activity() {
			busy[a.ID], sessions[a.ID] = a.Busy, a
			if a.Busy {
				delete(idle, a.ID)
				if id := done[a.ID]; id != "" {
					s.notices.Settle(id, "")
					delete(done, a.ID)
				}
			} else if was[a.ID] {
				idle[a.ID] = time.Now()
			}
		}
		for id, since := range idle {
			a, ok := sessions[id]
			if ok && time.Since(since) < settleFor {
				continue
			}
			delete(idle, id)
			// Waiting on the reader is not the end of a turn.
			if !ok || asking(waiting, id) {
				continue
			}
			if old := done[id]; old != "" {
				s.notices.Settle(old, "")
			}
			n := s.doneNotice(a)
			done[id] = n.ID
			s.notices.Raise(n)
		}

		select {
		case <-changed:
		case <-tick.C:
		case <-ctx.Done():
			return
		}
	}
}

func asking(waiting []Asked, session string) bool {
	for _, r := range waiting {
		if r.Session == session {
			return true
		}
	}
	return false
}

func agentName(via string) string {
	if via == "codex" {
		return "Codex"
	}
	return "Claude"
}

func (s *Server) doneNotice(a agent.Activity) notify.Notice {
	var b [12]byte
	rand.Read(b[:])
	n := notify.Notice{
		ID: hex.EncodeToString(b[:]), Kind: notify.Done, Session: a.ID, Agent: a.Agent,
		Title: agentName(a.Agent) + " finished", Where: a.Title, Body: a.Last,
	}
	if reply := s.agent.Reply(a.ID); reply != "" {
		n.Body, n.Format = clip(reply), notify.Markdown
	}
	return n
}

func (s *Server) askNotice(r Asked) notify.Notice {
	n := notify.Notice{
		ID: r.ID, Kind: notify.Ask, Session: r.Session, Agent: r.Via, Request: r.ID,
		Title: agentName(r.Via) + " wants to " + r.Headline, Where: r.Title,
	}
	n.Body, n.Format = askBody(r.Request)
	n.Body = clip(n.Body)
	n.Choices, n.Answer = s.choices(r.Request)
	return n
}

func clip(s string) string {
	if len(s) <= bodyChars {
		return s
	}
	return strings.ToValidUTF8(s[:bodyChars], "") + "…"
}

// askBody is what a request is about, in a line or a block: the command, the
// file, the plan, the question.
func askBody(r *permit.Request) (string, string) {
	var in struct {
		Command   string `json:"command"`
		FilePath  string `json:"file_path"`
		Notebook  string `json:"notebook_path"`
		URL       string `json:"url"`
		Query     string `json:"query"`
		Plan      string `json:"plan"`
		Questions []struct {
			Question string `json:"question"`
		} `json:"questions"`
	}
	json.Unmarshal(r.Input, &in)
	switch r.Tool {
	case "Bash", "PowerShell":
		return in.Command, notify.Code
	case "Edit", "Write", "NotebookEdit":
		if len(r.Previews) == 0 {
			return in.FilePath + in.Notebook, ""
		}
		lines := make([]string, len(r.Previews))
		for i, p := range r.Previews {
			lines[i] = p.Path
			if p.Diff != nil {
				lines[i] += fmt.Sprintf("  +%d −%d", p.Diff.Additions, p.Diff.Deletions)
			}
		}
		return strings.Join(lines, "\n"), ""
	case "WebFetch":
		return in.URL, ""
	case "WebSearch":
		return in.Query, ""
	case "ExitPlanMode":
		return in.Plan, notify.Markdown
	case "AskUserQuestion":
		qs := make([]string, len(in.Questions))
		for i, q := range in.Questions {
			qs[i] = q.Question
		}
		return strings.Join(qs, "\n"), ""
	}
	return string(r.Input), notify.Code
}

// choices are the answers a request can take away from the page: its options,
// or a lone question's, picked from. A question taking several answers, or
// one of several questions, is answered in dv.
func (s *Server) choices(r *permit.Request) ([]string, func(int) error) {
	if r.Tool != "AskUserQuestion" {
		labels := make([]string, len(r.Options))
		for i, o := range r.Options {
			labels[i] = o.Label
		}
		return labels, func(i int) error {
			o := r.Options[i]
			return s.answer(r.ID, permit.Answer{Allow: o.Allow, Suggestion: o.Suggestion})
		}
	}
	var in struct {
		Questions []struct {
			Question    string `json:"question"`
			MultiSelect bool   `json:"multiSelect"`
			Options     []struct {
				Label   string `json:"label"`
				Preview string `json:"preview"`
			} `json:"options"`
		} `json:"questions"`
	}
	if json.Unmarshal(r.Input, &in) != nil || len(in.Questions) != 1 || in.Questions[0].MultiSelect {
		return nil, nil
	}
	q := in.Questions[0]
	labels := make([]string, len(q.Options))
	for i, o := range q.Options {
		labels[i] = o.Label
	}
	return labels, func(i int) error {
		a := permit.Answer{Allow: true, Answers: map[string]string{q.Question: labels[i]}}
		// The drawing of the option picked goes with it, as the page sends it.
		if p := q.Options[i].Preview; p != "" {
			b, _ := json.Marshal(map[string]string{"preview": p})
			a.Annotations = map[string]json.RawMessage{q.Question: b}
		}
		return s.answer(r.ID, a)
	}
}

func (s *Server) answer(id string, a permit.Answer) error {
	if !s.permit.Answer(id, a) {
		return notify.ErrGone
	}
	return nil
}
