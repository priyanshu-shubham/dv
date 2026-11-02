package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"time"

	"dv/internal/agent"
)

// Ping is what a quiet stream says every KeepAlive. An event rather than a
// comment, which a page cannot see: one that hears nothing for a few of these
// takes its connection for dead - as a phone's is after it changes network,
// without either end being told - and opens another.
const Ping = "event: ping\ndata: {}\n\n"

// A conversation stream that ended is remembered a while by what it had sent,
// for the one that takes its place to go on from. Its last few updates are,
// as the page may not have had the last ones before the connection died.
const (
	parkedFor   = 10 * time.Minute
	parkedItems = 500_000 // in all the remembered updates, past which the oldest go
	snapsKept   = 8
	// How long a stream named by one taking its place is waited on to end.
	handOver = 2 * time.Second
)

// snap is what the page has once it has update n.
type snap struct {
	n    int
	seen agent.Seen
}

type parked struct {
	what  string
	snaps []snap
	at    time.Time
}

type liveStream struct {
	cancel context.CancelFunc
	done   chan struct{}
}

type resumes struct {
	mu   sync.Mutex
	by   map[string]parked      // by stream, once ended
	live map[string]*liveStream // by stream, while it runs
}

// start registers a stream of what as running; cancel ends it, should a page
// come back on another connection before this one is found dead. end parks
// what it sent, and is to be called as it ends.
func (rs *resumes) start(cancel context.CancelFunc) (stream string, end func(snaps []snap, what string)) {
	var b [8]byte
	rand.Read(b[:])
	stream = hex.EncodeToString(b[:])
	ls := &liveStream{cancel, make(chan struct{})}
	rs.mu.Lock()
	if rs.live == nil {
		rs.live = map[string]*liveStream{}
	}
	rs.live[stream] = ls
	rs.mu.Unlock()
	return stream, func(snaps []snap, what string) {
		rs.mu.Lock()
		delete(rs.live, stream)
		if len(snaps) > 0 {
			rs.parkLocked(stream, parked{what, snaps, time.Now()})
		}
		rs.mu.Unlock()
		close(ls.done)
	}
}

func (rs *resumes) parkLocked(stream string, p parked) {
	if rs.by == nil {
		rs.by = map[string]parked{}
	}
	rs.by[stream] = p
	size := func(p parked) (n int) {
		for _, s := range p.snaps {
			n += s.seen.Len()
		}
		return n
	}
	total := 0
	for k, p := range rs.by {
		if time.Since(p.at) > parkedFor {
			delete(rs.by, k)
		} else {
			total += size(p)
		}
	}
	for total > parkedItems && len(rs.by) > 1 {
		var oldest string
		for k, p := range rs.by {
			if k != stream && (oldest == "" || p.at.Before(rs.by[oldest].at)) {
				oldest = k
			}
		}
		total -= size(rs.by[oldest])
		delete(rs.by, oldest)
	}
}

// resume has sub go on from the stream last names, <stream>:<n>, if that was
// of the same conversation and remembers what the page had at n. Otherwise
// the page is sent the conversation afresh. A stream named that is still
// running is ended first: the page has left it.
func (rs *resumes) resume(what, last string, sub *agent.Sub) {
	stream, nth, ok := strings.Cut(last, ":")
	n, err := strconv.Atoi(nth)
	if !ok || err != nil {
		return
	}
	rs.mu.Lock()
	ls := rs.live[stream]
	rs.mu.Unlock()
	if ls != nil {
		ls.cancel()
		select {
		case <-ls.done:
		case <-time.After(handOver):
		}
	}
	rs.mu.Lock()
	p, found := rs.by[stream]
	delete(rs.by, stream)
	rs.mu.Unlock()
	if !found || p.what != what || time.Since(p.at) > parkedFor {
		return
	}
	for _, s := range p.snaps {
		if s.n == n {
			sub.Resume(s.seen)
			return
		}
	}
}

// pieceSize is about as much as an update is sent in at once: the page hears
// nothing of an event until it is all in, and on a slow link takes a long
// silence for a dead connection. The end of a conversation sent whole goes in
// a smaller first piece, as what is to show first.
const (
	pieceSize = 64 << 10
	firstSize = 32 << 10
)

// pieces is an update, b, as events of about pieceSize. A conversation sent
// whole goes end first, each later piece going ahead of the last, as the
// page shows its end; any other update in order, each piece adding to the one
// before.
func pieces(u agent.Update, b []byte) [][]byte {
	if len(b) <= firstSize || len(u.Items) < 2 {
		return [][]byte{b}
	}
	sizes := make([]int, len(u.Items))
	for i, it := range u.Items {
		j, err := json.Marshal(it)
		if err != nil {
			return [][]byte{b}
		}
		sizes[i] = len(j)
	}
	// Spans of items, [from, to), in the order they are sent.
	type span struct{ from, to int }
	var spans []span
	if u.Reset {
		limit := firstSize
		for to := len(u.Items); to > 0; {
			from, size := to-1, sizes[to-1]
			for from > 0 && size+sizes[from-1] <= limit {
				from--
				size += sizes[from]
			}
			spans = append(spans, span{from, to})
			to, limit = from, pieceSize
		}
	} else {
		for from := 0; from < len(u.Items); {
			to, size := from+1, sizes[from]
			for to < len(u.Items) && size+sizes[to] <= pieceSize {
				size += sizes[to]
				to++
			}
			spans = append(spans, span{from, to})
			from = to
		}
	}
	if len(spans) == 1 {
		return [][]byte{b}
	}
	out := make([][]byte, 0, len(spans))
	for i, s := range spans {
		v := u
		v.Items, v.More = u.Items[s.from:s.to], i < len(spans)-1
		if i > 0 {
			v.Reset, v.Keep = false, nil
			if u.Reset {
				v.Items, v.Before = nil, u.Items[s.from:s.to]
			}
		}
		p, err := json.Marshal(v)
		if err != nil {
			return [][]byte{b}
		}
		out = append(out, p)
	}
	return out
}
