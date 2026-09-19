package telegram

import (
	"html"
	"regexp"
	"strings"
)

// markdownHTML renders Markdown, as the agents write it, in the HTML Telegram
// takes: bold, italics, strikethrough, code, links and quotes. Headings go
// bold, list marks become bullets, and a table keeps its columns in a
// fixed-width block. Whatever is not understood shows as written. A reply cut
// short can end inside a code block, which is closed.
func markdownHTML(md string) string {
	var b strings.Builder
	lines := strings.Split(md, "\n")
	// run gathers the lines from i on that start with prefix.
	run := func(i int, prefix string) []string {
		var out []string
		for ; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), prefix); i++ {
			out = append(out, strings.TrimSpace(lines[i]))
		}
		return out
	}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~"):
			fence, lang := trimmed[:3], strings.TrimSpace(trimmed[3:])
			var code []string
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), fence); i++ {
				code = append(code, lines[i])
			}
			body := html.EscapeString(strings.Join(code, "\n"))
			if word.MatchString(lang) {
				b.WriteString(`<pre><code class="language-` + lang + `">` + body + "</code></pre>\n")
			} else {
				b.WriteString("<pre>" + body + "</pre>\n")
			}
		case strings.HasPrefix(trimmed, "|"):
			rows := run(i, "|")
			i += len(rows) - 1
			b.WriteString("<pre>" + html.EscapeString(strings.Join(rows, "\n")) + "</pre>\n")
		case strings.HasPrefix(trimmed, ">"):
			quote := run(i, ">")
			i += len(quote) - 1
			for j, q := range quote {
				quote[j] = inline(strings.TrimSpace(strings.TrimPrefix(q, ">")))
			}
			b.WriteString("<blockquote>" + strings.Join(quote, "\n") + "</blockquote>\n")
		case heading.MatchString(trimmed):
			text := strings.ReplaceAll(heading.FindStringSubmatch(trimmed)[1], "**", "") // bold already
			b.WriteString("<b>" + inline(text) + "</b>\n")
		case rule.MatchString(trimmed):
			b.WriteString("⎯⎯⎯\n")
		default:
			if m := bullet.FindStringSubmatch(line); m != nil {
				line = m[1] + "• " + m[2]
			}
			b.WriteString(inline(line) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

var (
	word    = regexp.MustCompile(`^[\w+#-]+$`)
	heading = regexp.MustCompile(`^#{1,6}\s+(.*?)\s*#*$`)
	rule    = regexp.MustCompile(`^(-\s*){3,}$|^(\*\s*){3,}$|^(_\s*){3,}$`)
	bullet  = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)

	codeSpan = regexp.MustCompile("`([^`]+)`")
	link     = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)\s]+)\)`)
	bold     = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	italic   = regexp.MustCompile(`(^|[^\w*])\*([^*\s](?:[^*]*[^*\s])?)\*([^\w*]|$)`)
	struck   = regexp.MustCompile(`~~([^~]+)~~`)
)

// inline renders a line's spans. Code is taken out first, so nothing in it
// reads as a mark.
func inline(s string) string {
	var b strings.Builder
	at := 0
	for _, m := range codeSpan.FindAllStringSubmatchIndex(s, -1) {
		b.WriteString(marks(s[at:m[0]]))
		b.WriteString("<code>" + html.EscapeString(s[m[2]:m[3]]) + "</code>")
		at = m[1]
	}
	b.WriteString(marks(s[at:]))
	return b.String()
}

// marks renders text with no code in it. The marks read the same escaped.
// Underscores are left alone: in plain text they are mostly names, __init__.
// A mark escaped with a backslash is the character itself.
func marks(s string) string {
	var held []string
	s = escaped.ReplaceAllStringFunc(s, func(e string) string {
		held = append(held, e[1:])
		return "\x00"
	})
	s = html.EscapeString(s)
	s = link.ReplaceAllString(s, `<a href="$2">$1</a>`)
	s = bold.ReplaceAllString(s, "<b>$1</b>")
	// Twice, as neighbours share the character between them: *a* *b*.
	s = italic.ReplaceAllString(s, "$1<i>$2</i>$3")
	s = italic.ReplaceAllString(s, "$1<i>$2</i>$3")
	s = struck.ReplaceAllString(s, "<s>$1</s>")
	for _, c := range held {
		s = strings.Replace(s, "\x00", html.EscapeString(c), 1)
	}
	return s
}

var escaped = regexp.MustCompile("\\\\[!-/:-@\\[-`{-~]")
