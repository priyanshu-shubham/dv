package chat

import (
	"net"
	"net/url"
	"strconv"
	"strings"

	"dv/internal/notify"
)

// A prompt's body is cut to this much, for its buttons to stay in sight.
const askChars = 3000

// notice is a notice in Markdown: its title, its body, whose it is unless in
// its session's thread, and how it ended once it has.
func notice(n notify.Notice, threaded bool, outcome string) string {
	var b strings.Builder
	b.WriteString("**" + Escape(n.Title) + "**\n")
	body := n.Body
	if n.Kind == notify.Ask {
		body = clip(body, askChars)
	}
	switch {
	case body == "":
	case n.Format == notify.Code:
		b.WriteString(fenced(body) + "\n")
	case n.Format == notify.Markdown:
		b.WriteString(body + "\n")
	default:
		b.WriteString(Escape(body) + "\n")
	}
	if !threaded {
		b.WriteString("\n*" + Escape(from(n.Where, n.Place)) + "*")
	}
	switch {
	case outcome != "":
		b.WriteString("\n\n**" + Escape(outcome) + "**")
	case n.Kind == notify.Ask && len(n.Choices) == 0:
		b.WriteString("\n\nThis one is answered in dv.")
	}
	return strings.TrimSpace(b.String())
}

// from says whose a message is: "from session in dv folder (Fix the tests)".
func from(title, place string) string {
	s := "from session"
	if title == "" {
		s = "from a new session"
	}
	if place != "" {
		s += " in " + place + " folder"
	}
	if title != "" {
		s += " (" + title + ")"
	}
	return s
}

// keyboard is a notice's buttons: its choices, while it can be answered, and a
// link to its session.
func (c *Conversation) keyboard(n notify.Notice, open bool) [][]Button {
	var rows [][]Button
	if open {
		for i, choice := range n.Choices {
			rows = append(rows, []Button{{Text: choice, Data: n.ID + ":" + strconv.Itoa(i)}})
		}
	}
	path := "/"
	if n.Folder != "" {
		path += n.Folder + "/"
	}
	if link := LinkTo(c.p.Origin(), path+"?session="+url.QueryEscape(n.Session)); link != "" {
		rows = append(rows, []Button{{Text: "Open in dv", URL: link}})
	}
	return rows
}

// LinkTo is a link to dv at origin, if a phone can follow it: not to this
// computer, which is not the phone's.
func LinkTo(origin, path string) string {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" && u.Scheme != "https" || u.Host == "" {
		return ""
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); host == "localhost" || ip != nil && ip.IsLoopback() {
		return ""
	}
	return strings.TrimSuffix(origin, "/") + path
}

// Escape is words to show as they are in Markdown: its marks escaped.
func Escape(s string) string {
	return marks.Replace(s)
}

var marks = strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "`", "\\`", "~", `\~`, "[", `\[`, "]", `\]`)

// fenced is code as a Markdown block, fenced with what it does not hold.
func fenced(code string) string {
	fence := "```"
	if strings.Contains(code, fence) {
		fence = "~~~"
	}
	return fence + "\n" + code + "\n" + fence
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// Split cuts Markdown into parts of at most size bytes, between lines. A code
// block cut in two is closed at the end of one part and opened again at the
// start of the next.
func Split(md string, size int) []string {
	var parts []string
	var b strings.Builder
	open := "" // the line that opened the code block the part is in
	for _, line := range strings.Split(strings.TrimSpace(md), "\n") {
		if b.Len() > 0 && b.Len()+len(line)+5 > size {
			part := b.String()
			if open != "" {
				part += open[:3]
			}
			parts = append(parts, clip(strings.TrimSpace(part), size))
			b.Reset()
			if open != "" {
				b.WriteString(open + "\n")
			}
		}
		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			if open == "" {
				open = trimmed
			} else if strings.HasPrefix(trimmed, open[:3]) {
				open = ""
			}
		}
		b.WriteString(line + "\n")
	}
	if rest := strings.TrimSpace(b.String()); rest != "" {
		parts = append(parts, clip(rest, size))
	}
	return parts
}
