package server

import (
	"errors"
	"sync"
	"time"
)

// A page that loses the answer to a message it sent, on a poor connection,
// sends it again under the same uuid; the agent is to get it once.

const sentKept = 10 * time.Minute

type sending struct {
	done chan struct{}
	uuid string
	err  error
	at   time.Time
}

type sent struct {
	mu sync.Mutex
	by map[string]*sending // by session and uuid
}

// once runs send for a message the first time its uuid comes for the session,
// and gives a resend what that did, waiting for it if it is still going. A
// send that failed is forgotten, to be tried again. No uuid, no telling.
func (t *sent) once(session, uuid string, send func() (string, error)) (string, error) {
	if uuid == "" {
		return send()
	}
	key := session + " " + uuid
	t.mu.Lock()
	if t.by == nil {
		t.by = map[string]*sending{}
	}
	if p := t.by[key]; p != nil {
		t.mu.Unlock()
		<-p.done
		return p.uuid, p.err
	}
	now := time.Now()
	for k, p := range t.by {
		if now.Sub(p.at) > sentKept {
			delete(t.by, k)
		}
	}
	p := &sending{done: make(chan struct{}), at: now}
	t.by[key] = p
	t.mu.Unlock()

	// Closed come what may, or a resend waits on it for good.
	defer close(p.done)
	p.err = errors.New("the message could not be sent")
	defer func() {
		if p.err != nil {
			t.mu.Lock()
			delete(t.by, key)
			t.mu.Unlock()
		}
	}()
	p.uuid, p.err = send()
	return p.uuid, p.err
}
