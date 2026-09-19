package chat

import (
	"context"
	"slices"
	"time"

	"dv/internal/notify"
)

// flight is a message sent to a session, or a session started, followed
// until its turn is over: how far the session has got shows on the message,
// and the thread shows it at work while it is, not waiting on the reader.
type flight struct {
	folder, session string
	id              string // the message's in the session; "" for the turn going on
	ref             string // the reader's message, "" for none to show it on
	thread          string
	at              notify.Progress // as last shown
}

// follow has a message, and its thread, keep up with its session.
func (c *Conversation) follow(f *flight) {
	f.at = Unmarked
	c.mu.Lock()
	c.flights = append(c.flights, f)
	c.mu.Unlock()
	select {
	case c.flew <- struct{}{}:
	default:
	}
}

// fly looks at the flights at once when one is added, then every Every until
// none is left.
func (c *Conversation) fly(ctx context.Context) {
	for {
		c.mu.Lock()
		idle := len(c.flights) == 0
		c.mu.Unlock()
		var every <-chan time.Time
		if !idle {
			every = time.After(c.Every)
		}
		select {
		case <-ctx.Done():
			return
		case <-c.flew:
		case <-every:
		}
		c.look(ctx)
	}
}

func (c *Conversation) look(ctx context.Context) {
	c.mu.Lock()
	flights := slices.Clone(c.flights)
	c.mu.Unlock()
	busy := map[string]bool{}
	var landed []*flight
	for _, f := range flights {
		at := c.center.Progress(f.folder, f.session, f.id)
		if f.ref != "" && at != f.at && (f.at != Unmarked || at != notify.Finished) {
			c.p.Mark(ctx, f.thread, f.ref, f.at, at)
		}
		f.at = at
		switch at {
		case notify.Working:
			busy[f.thread] = true
		case notify.Finished:
			landed = append(landed, f)
		}
	}
	c.mu.Lock()
	c.flights = slices.DeleteFunc(c.flights, func(f *flight) bool { return slices.Contains(landed, f) })
	c.mu.Unlock()
	for thread := range busy {
		c.p.Busy(ctx, thread)
	}
}
