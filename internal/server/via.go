package server

import (
	"strings"

	"dv/internal/agent"
)

// sendVia gives a session a message written in via: a chat app, or "" for
// dv's page. Where that changes from the last message's, the agent is told
// where its replies are read now - briefly in a chat, in full here - which
// holds until it changes again. A slash command is left as it is, for the
// agent to take it as one.
func (s *Server) sendVia(id, uuid, text string, images []agent.Image, via string) (string, error) {
	note := !strings.HasPrefix(strings.TrimSpace(text), "/") && s.saved.Via(id) != via
	if note {
		text = withContext(text, viaNote(via))
	}
	uuid, err := s.agent.Send(id, uuid, text, images)
	if err == nil && note {
		s.saved.SetVia(id, via)
	}
	return uuid, err
}

func viaNote(via string) string {
	if via == "" {
		return `<via app="dv">The user is back in dv, where your replies are read in full: answer at your usual length.</via>`
	}
	return `<via app="` + via + `">The user sent this from ` + via + ` and reads your reply there, likely on a phone: keep it short and to the point.</via>`
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
