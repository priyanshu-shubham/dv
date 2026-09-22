package main

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"dv/internal/relay"
)

// chatAPI is Google Chat's REST API, as the app calls it.
type chatAPI interface {
	// create posts m in space, as the message id: a custom one, "client-…".
	create(ctx context.Context, space, id string, m message) (message, error)
	patch(ctx context.Context, name, mask string, m message) error
	get(ctx context.Context, name string) (message, error)
	media(ctx context.Context, resource string) ([]byte, error)
}

// message is Google Chat's, as far as the relay reads and writes one.
type message struct {
	Name                    string          `json:"name,omitempty"`
	Text                    string          `json:"text,omitempty"`
	ArgumentText            string          `json:"argumentText,omitempty"`
	Thread                  *thread         `json:"thread,omitempty"`
	ThreadReply             bool            `json:"threadReply,omitempty"`
	CardsV2                 []cardV2        `json:"cardsV2,omitempty"`
	PrivateMessageViewer    *user           `json:"privateMessageViewer,omitempty"`
	Attachment              []attachment    `json:"attachment,omitempty"`
	SlashCommand            *struct{}       `json:"slashCommand,omitempty"`
	QuotedMessageMetadata   *quoted         `json:"quotedMessageMetadata,omitempty"`
	ClientAssignedMessageID string          `json:"clientAssignedMessageId,omitempty"`
	ActionResponse          *actionResponse `json:"actionResponse,omitempty"`
}

type thread struct {
	Name      string `json:"name,omitempty"`
	ThreadKey string `json:"threadKey,omitempty"`
}

type user struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName,omitempty"`
	Email       string `json:"email,omitempty"`
	Type        string `json:"type,omitempty"`
}

type attachment struct {
	ContentName       string `json:"contentName"`
	ContentType       string `json:"contentType"`
	AttachmentDataRef struct {
		ResourceName string `json:"resourceName"`
	} `json:"attachmentDataRef"`
}

type quoted struct {
	Name string `json:"name"`
}

type actionResponse struct {
	Type string `json:"type"`
}

type cardV2 struct {
	CardID string `json:"cardId"`
	Card   struct {
		Sections []section `json:"sections"`
	} `json:"card"`
}

type section struct {
	Widgets []widget `json:"widgets"`
}

type widget struct {
	ButtonList struct {
		Buttons []button `json:"buttons"`
	} `json:"buttonList"`
}

type button struct {
	Text    string  `json:"text"`
	OnClick onClick `json:"onClick"`
}

type onClick struct {
	Action   *action   `json:"action,omitempty"`
	OpenLink *openLink `json:"openLink,omitempty"`
}

type action struct {
	Function   string  `json:"function"`
	Parameters []param `json:"parameters"`
}

type openLink struct {
	URL string `json:"url"`
}

type param struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// tapFunction is what a button's tap invokes. Its parameters are the tapped
// message's id, which says whose it is, and the button's data.
const tapFunction = "tap"

// tag stands for a hub in its messages' ids, and in its threads' keys.
func tag(h Hub) string { return h.ID[:12] }

// messageID is a message's id with its hub's tag in it: "client-" is
// Google Chat's mark of an id the app gave.
func messageID(h Hub) string { return "client-" + tag(h) + "-" + randomHex(8) }

// tagOf is the hub's tag in a message's id or name.
func tagOf(name string) string {
	id, ok := strings.CutPrefix(name[strings.LastIndex(name, "/")+1:], "client-")
	if !ok {
		return ""
	}
	t, _, _ := strings.Cut(id, "-")
	return t
}

// owns is whether a message is one the relay posted for the hub.
func owns(h Hub, ref string) bool {
	return messageName.MatchString(ref) && tagOf(ref) == tag(h)
}

var messageName = regexp.MustCompile(`^spaces/[\w-]+/messages/client-[0-9a-f]+-[0-9a-f]+$`)

var errNotHubs = errors.New("not the hub's thread")

// post puts a hub's message in Google Chat, returning the name it has, to
// change it by, and the thread it went in.
func (r *Relay) post(ctx context.Context, h Hub, p relay.Post) (ref, in string, err error) {
	space, th, err := r.where(ctx, h, p.Thread)
	if err != nil {
		return "", "", err
	}
	id := messageID(h)
	ref = space + "/messages/" + id
	m := render(ref, p)
	m.Thread = th
	if p.Private && space != h.Space {
		m.PrivateMessageViewer = &user{Name: h.Owner}
	}
	made, err := r.chat.create(ctx, space, id, m)
	if err != nil {
		return "", "", err
	}
	if made.Thread != nil {
		in = made.Thread.Name
	}
	return ref, in, nil
}

// where is the space and thread a hub's thread is: "" is the owner's direct
// messages with the app, a key there the relay gave, and any other a thread
// the owner wrote to the hub in.
func (r *Relay) where(ctx context.Context, h Hub, id string) (string, *thread, error) {
	if id == "" {
		return h.Space, nil, nil
	}
	if key, ok := strings.CutPrefix(id, keyPrefix); ok {
		return h.Space, &thread{ThreadKey: tag(h) + "-" + clip(key, 64)}, nil
	}
	if rt, ok := r.routeOf(ctx, id, h.Owner); !ok || rt.Hub != h.ID {
		return "", nil, errNotHubs
	}
	return spaceOf(id), &thread{Name: id}, nil
}

// spaceOf is the space a thread or message is in: spaces/….
func spaceOf(name string) string {
	parts := strings.SplitN(name, "/", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

// render is a hub's message as Google Chat takes it: its words in Chat's
// marks, and its buttons on a card. A tap on one names the message, ref.
func render(ref string, p relay.Post) message {
	m := message{Text: p.Text}
	if !p.Plain {
		m.Text = chatText(p.Text)
	}
	id := ref[strings.LastIndex(ref, "/")+1:]
	var widgets []widget
	for _, row := range p.Buttons {
		var w widget
		for _, b := range row {
			bt := button{Text: b.Text}
			if b.URL != "" {
				bt.OnClick.OpenLink = &openLink{b.URL}
			} else {
				bt.OnClick.Action = &action{tapFunction, []param{{"m", id}, {"data", b.Data}}}
			}
			w.ButtonList.Buttons = append(w.ButtonList.Buttons, bt)
		}
		if len(w.ButtonList.Buttons) > 0 && len(widgets) < 100 {
			widgets = append(widgets, w)
		}
	}
	if len(widgets) > 0 {
		var c cardV2
		c.CardID = "buttons"
		c.Card.Sections = []section{{Widgets: widgets}}
		m.CardsV2 = []cardV2{c}
	}
	return m
}

// chatText renders Markdown in Google Chat's own marks: *bold*, _italics_,
// ~strikethrough~, code and links. Headings go bold, list marks become
// bullets, and a table keeps its columns in a code block. Chat has no way to
// escape a mark, so an escaped one is the character itself.
func chatText(md string) string {
	var b strings.Builder
	lines := strings.Split(md, "\n")
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~"):
			fence := trimmed[:3]
			b.WriteString("```\n")
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), fence); i++ {
				b.WriteString(lines[i] + "\n")
			}
			b.WriteString("```\n")
		case strings.HasPrefix(trimmed, "|"):
			b.WriteString("```\n")
			for ; i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "|"); i++ {
				b.WriteString(strings.TrimSpace(lines[i]) + "\n")
			}
			i--
			b.WriteString("```\n")
		case heading.MatchString(trimmed):
			text := strings.ReplaceAll(heading.FindStringSubmatch(trimmed)[1], "**", "")
			b.WriteString("*" + inline(text) + "*\n")
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
	heading  = regexp.MustCompile(`^#{1,6}\s+(.*?)\s*#*$`)
	rule     = regexp.MustCompile(`^(-\s*){3,}$|^(\*\s*){3,}$|^(_\s*){3,}$`)
	bullet   = regexp.MustCompile(`^(\s*)[-*+]\s+(.*)$`)
	codeSpan = regexp.MustCompile("`[^`]+`")
	link     = regexp.MustCompile(`\[([^\]]+)\]\((https?://[^)\s]+)\)`)
	bold     = regexp.MustCompile(`\*\*([^*]+)\*\*`)
	italic   = regexp.MustCompile(`(^|[^\w*])\*([^*\s](?:[^*]*[^*\s])?)\*([^\w*]|$)`)
	struck   = regexp.MustCompile(`~~([^~]+)~~`)
	escaped  = regexp.MustCompile("\\\\([!-/:-@\\[-`{-~])")
)

// inline renders a line's spans, leaving code as it is.
func inline(s string) string {
	var b strings.Builder
	at := 0
	for _, m := range codeSpan.FindAllStringIndex(s, -1) {
		b.WriteString(marks(s[at:m[0]]))
		b.WriteString(s[m[0]:m[1]])
		at = m[1]
	}
	b.WriteString(marks(s[at:]))
	return b.String()
}

// marks renders text with no code in it. Italics go first, as Chat's bold is
// Markdown's italics.
func marks(s string) string {
	var held []string
	s = escaped.ReplaceAllStringFunc(s, func(e string) string {
		held = append(held, e[1:])
		return "\x00"
	})
	s = link.ReplaceAllString(s, "<$2|$1>")
	s = italic.ReplaceAllString(s, "${1}_${2}_$3")
	s = italic.ReplaceAllString(s, "${1}_${2}_$3")
	s = bold.ReplaceAllString(s, "*$1*")
	s = struck.ReplaceAllString(s, "~$1~")
	for _, c := range held {
		s = strings.Replace(s, "\x00", c, 1)
	}
	return s
}
