package telegram

import (
	"cmp"
	"context"
	"html"
	"slices"
	"strings"

	"dv/internal/notify"
)

// A session's model shows on the message that starts it and on /model's,
// with a button that opens its models and efforts there. Picked on the
// message that started the session, they are where new sessions start too,
// as picking them for a new session in the page is.

// setupText is a message's words, intro then the session's setup.
func setupText(intro string, s notify.Setup) string {
	parts := []string{"Claude"}
	if s.Agent == "codex" {
		parts[0] = "Codex"
	}
	if m := modelOf(s); m.Label != "" {
		parts = append(parts, m.Label)
	}
	if e := effortOf(s); e != "" {
		parts = append(parts, e+" effort")
	}
	line := "On <b>" + html.EscapeString(strings.Join(parts, " · ")) + "</b>"
	if intro == "" {
		return line
	}
	return intro + "\n\n" + line
}

// modelOf is the model a session runs on, as offered, or known only by its ID.
func modelOf(s notify.Setup) notify.Model {
	if i := slices.IndexFunc(s.Models, func(m notify.Model) bool { return m.ID == s.Model }); i >= 0 {
		return s.Models[i]
	}
	return notify.Model{ID: s.Model, Label: s.Model}
}

func effortOf(s notify.Setup) string { return cmp.Or(s.Effort, modelOf(s).Effort) }

// setupRows are the button that opens the picker, or the picker open: a row
// a model, the efforts of the one picked, and Done. p is the session's.
func (t *Telegram) setupRows(s notify.Setup, p pick, open bool) [][]button {
	if len(s.Models) == 0 {
		return nil
	}
	if !open {
		p.step = "change"
		return [][]button{{{Text: "Change model", Data: t.offer(p)}}}
	}
	on := modelOf(s)
	var rows [][]button
	for _, m := range s.Models {
		p.step, p.value = "model", m.ID
		rows = append(rows, []button{{Text: ticked(m.ID == on.ID) + m.Label, Data: t.offer(p)}})
	}
	var efforts []button
	for _, e := range on.Efforts {
		p.step, p.value = "effort", e
		efforts = append(efforts, button{Text: ticked(e == effortOf(s)) + e, Data: t.offer(p)})
	}
	for row := range slices.Chunk(efforts, 3) {
		rows = append(rows, row)
	}
	p.step = "shut"
	return append(rows, []button{{Text: "Done", Data: t.offer(p)}})
}

func ticked(on bool) string {
	if on {
		return "✓ "
	}
	return ""
}

// pickSetup acts on a button of the picker, on the message it is on, and
// returns what to tell the reader of it.
func (t *Telegram) pickSetup(ctx context.Context, cfg Config, q *callback, p pick) string {
	folder, session := p.session.Folder, p.session.ID
	var err error
	switch p.step {
	case "model":
		err = t.center.Configure(folder, session, &p.value, nil, p.asNew)
	case "effort":
		err = t.center.Configure(folder, session, nil, &p.value, p.asNew)
	}
	if err != nil {
		return err.Error()
	}
	s, err := t.center.Setup(folder, session)
	if err != nil {
		return err.Error()
	}
	if q.Message != nil {
		// A tap on what is picked already changes nothing, which Telegram
		// turns down, and that is all.
		t.call(ctx, cfg, "editMessageText", map[string]any{
			"chat_id": cfg.Chat, "message_id": q.Message.ID, "text": setupText(p.intro, s),
			"parse_mode": "HTML", "link_preview_options": noPreview, "reply_markup": markup(t.setupRows(s, p, p.step != "shut")),
		}, nil)
	}
	return ""
}

// askModel shows a session's setup with the picker open, for /model.
func (t *Telegram) askModel(ctx context.Context, cfg Config, thread int64, folder, session string) {
	s, err := t.center.Setup(folder, session)
	switch {
	case err != nil:
		t.say(ctx, cfg, thread, err.Error(), nil)
	case len(s.Models) == 0:
		t.say(ctx, cfg, thread, "dv does not know yet which models the agent offers: ask again in a moment.", nil)
	default:
		t.post(ctx, cfg, thread, setupText("", s), t.setupRows(s, pick{session: notify.Session{Folder: folder, ID: session}}, true))
	}
}
