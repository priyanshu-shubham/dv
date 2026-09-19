package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"dv/internal/relay"
)

// Relay is the service: hubs connect to it, and Google Chat sends it what
// their owners write. It runs as one instance, which holds each hub's events
// on their way, in memory only.
type Relay struct {
	store Store
	chat  chatAPI
	// allowed is who may use the relay: Google Chat users' emails.
	allowed func(email string) bool
	// verify checks that a request came from Google Chat.
	verify func(r *http.Request) error

	mu     sync.Mutex
	live   map[string]*live             // by hub
	taps   map[string]chan relay.Answer // answers awaited, by hub|tap
	known  map[string]Hub               // hubs by id, as last read
	routes map[string]Route             // by thread|owner, as last read or kept
	seen   map[string]time.Time         // Chat's messages taken, as it may send one again
	asked  []time.Time                  // when codes were asked for, this last hour
}

func newRelay(store Store, chat chatAPI, allowed func(string) bool, verify func(*http.Request) error) *Relay {
	return &Relay{
		store: store, chat: chat, allowed: allowed, verify: verify,
		live: map[string]*live{}, taps: map[string]chan relay.Answer{}, known: map[string]Hub{},
		routes: map[string]Route{}, seen: map[string]time.Time{},
	}
}

// How long: a code is good for, a hub's dv keeps its hold on its events
// without asking for more, and the owner's message and tap wait for the hub.
// Google Chat waits 30 seconds for an answer.
const (
	pairFor      = 10 * time.Minute
	holdFor      = time.Minute
	seenEvery    = 5 * time.Minute
	pairsPerHour = 30 // codes the relay gives at most
)

var (
	deliverFor = 10 * time.Second
	answerFor  = 20 * time.Second
)

func (r *Relay) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+relay.PathPair, r.handlePair)
	mux.HandleFunc("POST "+relay.PathClaim, r.handleClaim)
	mux.HandleFunc("GET "+relay.PathMe, r.paired(r.handleMe))
	mux.HandleFunc("DELETE "+relay.PathMe, r.paired(r.handleForget))
	mux.HandleFunc("GET "+relay.PathEvents, r.paired(r.handleEvents))
	mux.HandleFunc("POST "+relay.PathPost, r.paired(r.handlePost))
	mux.HandleFunc("POST "+relay.PathEdit, r.paired(r.handleEdit))
	mux.HandleFunc("POST "+relay.PathUnbutton, r.paired(r.handleUnbutton))
	mux.HandleFunc("POST "+relay.PathThread, r.paired(r.handleThread))
	mux.HandleFunc("POST "+relay.PathMark, r.paired(func(w http.ResponseWriter, _ *http.Request, _ Hub) { reply(w, http.StatusOK, struct{}{}) }))
	mux.HandleFunc("POST "+relay.PathAnswer, r.paired(r.handleAnswer))
	mux.HandleFunc("POST /chat", r.handleChat)
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("dv-gchat-relay\n")) })
	return mux
}

// paired has a handler take only requests with a hub's token.
func (r *Relay) paired(h func(http.ResponseWriter, *http.Request, Hub)) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		token, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			refuse(w, http.StatusUnauthorized, "This hub is not paired with the relay")
			return
		}
		hub, err := r.hub(req.Context(), hash(token))
		if err != nil {
			refuse(w, http.StatusUnauthorized, "The relay does not know this hub any more: pair it again")
			return
		}
		if time.Since(hub.Seen) > seenEvery {
			hub.Seen = time.Now()
			r.putHub(req.Context(), hub)
		}
		h(w, req, hub)
	}
}

// putHub keeps a hub, and the relay's copy of it, which is written back.
func (r *Relay) putHub(ctx context.Context, h Hub) error {
	r.mu.Lock()
	r.known[h.ID] = h
	r.mu.Unlock()
	return r.store.PutHub(ctx, h)
}

// hub is a paired hub, read once.
func (r *Relay) hub(ctx context.Context, id string) (Hub, error) {
	r.mu.Lock()
	h, ok := r.known[id]
	r.mu.Unlock()
	if ok {
		return h, nil
	}
	h, err := r.store.Hub(ctx, id)
	if err != nil {
		return h, err
	}
	r.mu.Lock()
	r.known[id] = h
	r.mu.Unlock()
	return h, nil
}

func (r *Relay) handlePair(w http.ResponseWriter, req *http.Request) {
	if !r.mayPair() {
		refuse(w, http.StatusTooManyRequests, "Too many codes asked for: try again in an hour")
		return
	}
	var in relay.PairRequest
	if json.NewDecoder(http.MaxBytesReader(w, req.Body, 4<<10)).Decode(&in) != nil {
		refuse(w, http.StatusBadRequest, "Say the hub's name")
		return
	}
	claim := randomHex(32)
	p := Pairing{Code: newCode(), Claim: hash(claim), Name: clip(strings.TrimSpace(in.Name), 60), Expires: time.Now().Add(pairFor)}
	if p.Name == "" {
		p.Name = "dv"
	}
	if err := r.store.PutPairing(req.Context(), p); err != nil {
		refuse(w, http.StatusInternalServerError, "Could not keep the code")
		return
	}
	reply(w, http.StatusOK, relay.Pairing{Code: p.Code, Claim: claim, Expires: p.Expires})
}

// mayPair keeps anyone from asking for codes all day: they are worth nothing
// unless sent in Chat, but each is kept. The limit is the relay's, as an
// address behind Cloud Run's front end can be made up.
func (r *Relay) mayPair() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	hour := time.Now().Add(-time.Hour)
	r.asked = slices.DeleteFunc(r.asked, func(t time.Time) bool { return t.Before(hour) })
	if len(r.asked) >= pairsPerHour {
		return false
	}
	r.asked = append(r.asked, time.Now())
	return true
}

func (r *Relay) handleClaim(w http.ResponseWriter, req *http.Request) {
	var in relay.Claim
	json.NewDecoder(http.MaxBytesReader(w, req.Body, 4<<10)).Decode(&in)
	p, err := r.store.PairingByClaim(req.Context(), hash(in.Claim))
	switch {
	case in.Claim == "" || err != nil:
		refuse(w, http.StatusGone, "That pairing is over: ask for another code")
		return
	case time.Now().After(p.Expires):
		r.store.DeletePairing(req.Context(), p.Code)
		refuse(w, http.StatusGone, "The code expired before it was sent: ask for another")
		return
	case p.Owner == "":
		refuse(w, http.StatusAccepted, "Not sent in Google Chat yet")
		return
	}
	token := randomHex(32)
	now := time.Now()
	// The newest hub is where its owner's new sessions start.
	h := Hub{
		ID: hash(token), Name: p.Name, Owner: p.Owner, OwnerName: p.OwnerName, Email: p.Email, Space: p.Space,
		Created: now, Seen: now, Chosen: now,
	}
	if err := r.store.PutHub(req.Context(), h); err != nil {
		refuse(w, http.StatusInternalServerError, "Could not keep the hub")
		return
	}
	r.store.DeletePairing(req.Context(), p.Code)
	reply(w, http.StatusOK, relay.Paired{Token: token, Me: me(h)})
}

func me(h Hub) relay.Me { return relay.Me{Name: h.Name, Owner: h.OwnerName, Email: h.Email} }

func (r *Relay) handleMe(w http.ResponseWriter, req *http.Request, h Hub) {
	reply(w, http.StatusOK, me(h))
}

func (r *Relay) handleForget(w http.ResponseWriter, req *http.Request, h Hub) {
	if err := r.store.DeleteHub(req.Context(), h.ID); err != nil {
		refuse(w, http.StatusInternalServerError, "Could not forget the hub")
		return
	}
	r.mu.Lock()
	delete(r.known, h.ID)
	delete(r.live, h.ID)
	r.mu.Unlock()
	reply(w, http.StatusOK, struct{}{})
}

// live is a hub's events on their way, and which of its dvs takes them.
type live struct {
	next    int64
	queue   []*queued
	wake    chan struct{} // closed as an event is queued
	conn    string
	held    time.Time // when conn last asked
	waiting int       // requests waiting
}

type queued struct {
	event relay.Event
	taken chan struct{}
}

// liveOf is a hub's. Callers hold r.mu.
func (r *Relay) liveOf(hub string) *live {
	l := r.live[hub]
	if l == nil {
		l = &live{wake: make(chan struct{})}
		r.live[hub] = l
	}
	return l
}

// handleEvents hands a hub what its owner did, as it comes. One dv of the
// hub's takes them; another is turned away while it keeps asking.
func (r *Relay) handleEvents(w http.ResponseWriter, req *http.Request, h Hub) {
	q := req.URL.Query()
	conn := q.Get("conn")
	wait, _ := strconv.Atoi(q.Get("wait"))
	wait = min(max(wait, 0), relay.Wait)
	deadline := time.After(time.Duration(wait) * time.Second)
	for {
		r.mu.Lock()
		l := r.liveOf(h.ID)
		if l.conn != conn && l.conn != "" && (l.waiting > 0 || time.Since(l.held) < holdFor) {
			r.mu.Unlock()
			refuse(w, http.StatusConflict, "Another dv of this hub's is connected")
			return
		}
		l.conn, l.held = conn, time.Now()
		if len(l.queue) > 0 {
			var events []relay.Event
			for _, e := range l.queue {
				events = append(events, e.event)
				close(e.taken)
			}
			l.queue = nil
			r.mu.Unlock()
			reply(w, http.StatusOK, relay.Events{Events: events})
			return
		}
		wake := l.wake
		l.waiting++
		r.mu.Unlock()
		var done bool
		select {
		case <-wake:
		case <-deadline:
			done = true
		case <-req.Context().Done():
			done = true
		}
		r.mu.Lock()
		l.waiting--
		l.held = time.Now()
		r.mu.Unlock()
		if done {
			reply(w, http.StatusOK, relay.Events{Events: []relay.Event{}})
			return
		}
	}
}

// deliver hands an event to the hub's dv, reporting whether it took it
// within deliverFor. One not taken is dropped: nothing waits for a dv not
// there.
func (r *Relay) deliver(ctx context.Context, hub string, e relay.Event) bool {
	r.mu.Lock()
	l := r.liveOf(hub)
	l.next++
	e.ID = l.next
	q := &queued{event: e, taken: make(chan struct{})}
	l.queue = append(l.queue, q)
	close(l.wake)
	l.wake = make(chan struct{})
	r.mu.Unlock()
	select {
	case <-q.taken:
		return true
	case <-time.After(deliverFor):
	case <-ctx.Done():
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case <-q.taken:
		return true
	default:
		l.queue = slices.DeleteFunc(l.queue, func(x *queued) bool { return x == q })
		return false
	}
}

func (r *Relay) handlePost(w http.ResponseWriter, req *http.Request, h Hub) {
	var p relay.Post
	if json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<20)).Decode(&p) != nil {
		refuse(w, http.StatusBadRequest, "That is not a message")
		return
	}
	ref, thread, err := r.post(req.Context(), h, p)
	switch {
	case errors.Is(err, errNotHubs):
		refuse(w, http.StatusForbidden, "That thread is not this hub's")
		return
	case err != nil:
		refuse(w, http.StatusBadGateway, "Google Chat did not take the message: "+err.Error())
		return
	}
	r.route(req.Context(), h, thread, p.Thread)
	reply(w, http.StatusOK, relay.Posted{Ref: ref})
}

// route keeps which hub a thread is, and the hub's name for it, so that what
// its owner writes there goes to that hub.
func (r *Relay) route(ctx context.Context, h Hub, thread, as string) {
	if thread == "" {
		return
	}
	if as == "" {
		as = thread
	}
	rt := Route{Thread: thread, Owner: h.Owner, Hub: h.ID, As: as}
	k := thread + "|" + h.Owner
	r.mu.Lock()
	kept := r.routes[k] == rt
	if !kept {
		if len(r.routes) > 10000 {
			clear(r.routes)
		}
		r.routes[k] = rt
	}
	r.mu.Unlock()
	if !kept {
		if err := r.store.PutRoute(ctx, rt); err != nil {
			log.Printf("could not keep a route: %v", err)
		}
	}
}

// routeOf is the hub an owner's thread is, if one is.
func (r *Relay) routeOf(ctx context.Context, thread, owner string) (Route, bool) {
	if thread == "" {
		return Route{}, false
	}
	k := thread + "|" + owner
	r.mu.Lock()
	rt, ok := r.routes[k]
	r.mu.Unlock()
	if ok {
		return rt, true
	}
	rt, err := r.store.Route(ctx, thread, owner)
	if err != nil {
		return rt, false
	}
	r.mu.Lock()
	r.routes[k] = rt
	r.mu.Unlock()
	return rt, true
}

func (r *Relay) handleEdit(w http.ResponseWriter, req *http.Request, h Hub) {
	var e relay.Edit
	if json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<20)).Decode(&e) != nil || !owns(h, e.Ref) {
		refuse(w, http.StatusBadRequest, "That is not a message of this hub's")
		return
	}
	if err := r.chat.patch(req.Context(), e.Ref, "text,cardsV2", render(e.Ref, e.Post)); err != nil {
		refuse(w, http.StatusBadGateway, "Google Chat did not take the change: "+err.Error())
		return
	}
	reply(w, http.StatusOK, struct{}{})
}

func (r *Relay) handleUnbutton(w http.ResponseWriter, req *http.Request, h Hub) {
	var u relay.Unbutton
	if json.NewDecoder(http.MaxBytesReader(w, req.Body, 4<<10)).Decode(&u) != nil || !owns(h, u.Ref) {
		refuse(w, http.StatusBadRequest, "That is not a message of this hub's")
		return
	}
	if err := r.chat.patch(req.Context(), u.Ref, "cardsV2", message{}); err != nil {
		refuse(w, http.StatusBadGateway, "Google Chat did not take the change: "+err.Error())
		return
	}
	reply(w, http.StatusOK, struct{}{})
}

// handleThread gives a thread for a session: a key, which Google Chat makes
// a thread of with the first message posted to it.
func (r *Relay) handleThread(w http.ResponseWriter, req *http.Request, h Hub) {
	reply(w, http.StatusOK, relay.Thread{Thread: keyPrefix + randomHex(8)})
}

const keyPrefix = "key:"

func (r *Relay) handleAnswer(w http.ResponseWriter, req *http.Request, h Hub) {
	var a relay.Answer
	json.NewDecoder(http.MaxBytesReader(w, req.Body, 4<<10)).Decode(&a)
	r.mu.Lock()
	ch := r.taps[h.ID+"|"+a.Tap]
	r.mu.Unlock()
	if ch != nil {
		select {
		case ch <- a:
		default:
		}
	}
	reply(w, http.StatusOK, struct{}{})
}

func reply(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func refuse(w http.ResponseWriter, code int, why string) {
	reply(w, code, relay.Error{Error: why})
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// newCode is a pairing code to type: eight letters and digits that do not
// look alike, as ABCD-2345.
func newCode() string {
	const letters = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 8)
	rand.Read(b)
	for i := range b {
		b[i] = letters[int(b[i])%len(letters)]
	}
	return string(b[:4]) + "-" + string(b[4:])
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}
