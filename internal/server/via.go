package server

import (
	"slices"
	"strings"

	"dv/internal/agent"
)

// sendVia gives a session a message written in via: a chat app, or "" for
// dv's page. Where that changes from the last message's, the agent is told
// where its replies are read now - briefly in a chat or the Ask panel, in full
// here - which holds until it changes again. Being put in or out of
// AutoAskEdits is told the same way. A slash command is left as it is, for
// the agent to take it as one.
func (s *Server) sendVia(id, uuid, text string, images []agent.Image, via string) (string, error) {
	if via == "" && slices.Contains(s.saved.AskIDs(), id) {
		via = askPanel
	}
	slash := strings.HasPrefix(strings.TrimSpace(text), "/")
	note := !slash && s.saved.Via(id) != via
	if note {
		text = withContext(text, viaNote(via))
	}
	asking := s.agent.AsksEdits(id)
	tell := !slash && asking != s.saved.ToldEdits(id)
	if tell {
		text = withContext(text, editsNote(asking))
	}
	uuid, err := s.agent.Send(id, uuid, text, images)
	if err == nil && note {
		s.saved.SetVia(id, via)
	}
	if err == nil && tell {
		s.saved.SetToldEdits(id, asking)
	}
	return uuid, err
}

// askPanel is the via of the Ask panel: dv's page, but a narrow column where a
// long reply is hard to read.
const askPanel = "ask"

func viaNote(via string) string {
	switch via {
	case "":
		return `<via app="dv">The user is back in dv, where your replies are read in full: answer at your usual length.</via>`
	case askPanel:
		// app="dv", for the page not to tag each message in the panel as sent from it.
		return `<via app="dv">The user is asking from dv's Ask panel, a narrow column beside the code: answer concisely unless they ask for more detail.</via>`
	}
	return `<via app="` + via + `">The user sent this from ` + via + ` and reads your reply there, likely on a phone: keep it short and to the point.</via>`
}

// editsNote tells the agent AutoAskEdits began or ended. The rules stand
// against auto mode's own advice to edit through the shell, where no hook
// can put the change to the user.
func editsNote(asking bool) string {
	if !asking {
		return `<mode ended="` + agent.AutoAskEdits + `">dv's edit rules are lifted.</mode>`
	}
	return `<mode name="` + agent.AutoAskEdits + `">Until dv says otherwise, the user reviews each edit as a diff: make small edits, one per message, only with Edit, Write or NotebookEdit, never the shell, whatever auto mode suggests. Don't make a rejected edit another way.</mode>`
}

// withContext adds part to the block of context the page ends a message with,
// which the agent reads and the page shows as chips, starting one if need be.
func withContext(text, part string) string {
	const end = "</dv-context>"
	body := strings.TrimRight(text, " \t\n")
	if strings.HasSuffix(body, end) {
		return strings.TrimSuffix(body, end) + part + "\n" + end
	}
	return body + "\n\n<dv-context>\n" + part + "\n" + end
}
