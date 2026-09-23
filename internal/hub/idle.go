package hub

import (
	"time"

	"dv/internal/store"
)

// A folder is opened when it is asked for - its page, a session started in it
// from elsewhere - and let go once nothing has gone on in it for idleAfter: no
// page on it, no session running, no question waiting. There is nothing for the
// reader to open or close.
const (
	idleAfter = time.Hour
	usedEvery = time.Minute // how often a folder's use is written down
)

// sweepIdle lets idle folders go, looking every minute, until the hub closes.
func (h *Hub) sweepIdle() {
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case now := <-tick.C:
			h.sweep(now)
		case <-h.stop:
			return
		}
	}
}

func (h *Hub) sweep(now time.Time) {
	h.mu.Lock()
	var quiet []string
	for slug, rv := range h.open {
		if rv.requests == 0 && now.Sub(rv.asked) >= h.idleAfter {
			quiet = append(quiet, slug)
		}
	}
	h.mu.Unlock()
	for _, slug := range quiet {
		rv := h.opened(slug)
		if rv == nil {
			continue
		}
		if rv.srv.InUse() {
			h.used(slug) // agents at work are the folder in use, page or not
			continue
		}
		h.closeReview(slug)
	}
}

// StartKept opens the folders with sessions kept running, and starts them, as
// the hub starts. Running, they keep their folder open.
func (h *Hub) StartKept() {
	for _, f := range h.folders.List() {
		saved, err := store.OpenSessions(f.Path)
		if err != nil || len(saved.KeptIDs()) == 0 {
			continue
		}
		if rv, err := h.review(f.Slug); err == nil {
			rv.srv.StartKept()
		}
	}
}

// asking marks a request to a folder's review under way, until the func it
// returns is called: a page open on it keeps it asking.
func (h *Hub) asking(slug string, rv *review) func() {
	h.mu.Lock()
	rv.requests++
	rv.asked = time.Now()
	h.mu.Unlock()
	h.used(slug)
	return func() {
		h.mu.Lock()
		rv.requests--
		rv.asked = time.Now()
		h.mu.Unlock()
	}
}

// used writes down that the folder is in use now, at most every usedEvery.
func (h *Hub) used(slug string) {
	now := time.Now()
	h.mu.Lock()
	fresh := now.Sub(h.usedAt[slug]) < usedEvery
	if !fresh {
		h.usedAt[slug] = now
	}
	h.mu.Unlock()
	if !fresh {
		h.folders.Update(slug, func(f *store.Folder) { f.Used = now.UTC() })
	}
}
