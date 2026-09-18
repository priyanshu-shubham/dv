package telegram

import (
	"context"
	"slices"
	"time"

	"dv/internal/notify"
)

// A message sent to a session wears how far the session has got with it: 👀
// while it waits behind the step the agent is on, 👨‍💻 once taken up, nothing
// once the turn is over, its reply below it. Of the few emoji a bot may react
// with, none is ⏳ or ✅. While the agent works, not waiting on the reader, the
// thread shows the bot typing, which Telegram keeps up for 5 seconds.
const typingEvery = 4 * time.Second

// flight is a message sent to a session, or a session started, followed
// until its turn is over.
type flight struct {
	folder, session string
	id              string // the message's in the session; "" for the turn going on
	message         int64  // in Telegram, 0 for none to react on
	thread          int64
	face            string // the reaction on message
}

// follow has a message's reaction, and its thread, keep up with its session.
func (t *Telegram) follow(f *flight) {
	t.mu.Lock()
	t.flights = append(t.flights, f)
	t.mu.Unlock()
	select {
	case t.flew <- struct{}{}:
	default:
	}
}

// fly looks at the flights at once when one is added, then every
// typingEvery until none is left.
func (t *Telegram) fly(ctx context.Context) {
	for {
		t.mu.Lock()
		idle := len(t.flights) == 0
		t.mu.Unlock()
		var every <-chan time.Time
		if !idle {
			every = time.After(t.typingEvery)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.flew:
		case <-every:
		}
		t.look(ctx)
	}
}

func (t *Telegram) look(ctx context.Context) {
	cfg := t.config()
	t.mu.Lock()
	flights := slices.Clone(t.flights)
	t.mu.Unlock()
	typing := map[int64]bool{}
	var landed []*flight
	for _, f := range flights {
		at := t.center.Progress(f.folder, f.session, f.id)
		if f.message != 0 && face(at) != f.face {
			f.face = face(at)
			t.react(ctx, cfg, f.message, f.face)
		}
		switch at {
		case notify.Working:
			typing[f.thread] = true
		case notify.Finished:
			landed = append(landed, f)
		}
	}
	t.mu.Lock()
	t.flights = slices.DeleteFunc(t.flights, func(f *flight) bool { return slices.Contains(landed, f) })
	t.mu.Unlock()
	for thread := range typing {
		params := map[string]any{"chat_id": cfg.Chat, "action": "typing"}
		if thread != 0 {
			params["message_thread_id"] = thread
		}
		t.call(ctx, cfg, "sendChatAction", params, nil)
	}
}

// face is the reaction for how far a session has got, "" for none.
func face(at notify.Progress) string {
	switch at {
	case notify.Queued:
		return "👀"
	case notify.Working, notify.Asking:
		return "👨‍💻"
	}
	return ""
}
