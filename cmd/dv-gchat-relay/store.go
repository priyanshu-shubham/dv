package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

// What the relay keeps: who has hubs, and which thread is whose. No message
// is kept; they are handed on as they come.

// Hub is a dv paired with the relay, by the hash of its token.
type Hub struct {
	ID        string    `json:"id"`    // hex SHA-256 of its token
	Name      string    `json:"name"`  // as its owner sees it: the computer's
	Owner     string    `json:"owner"` // users/…, as Google Chat names the user
	OwnerName string    `json:"ownerName"`
	Email     string    `json:"email"`
	Space     string    `json:"space"` // the owner's direct messages with the app
	Created   time.Time `json:"created"`
	Seen      time.Time `json:"seen"`
	// Chosen is when its owner made it the hub their new sessions start on.
	Chosen time.Time `json:"chosen,omitzero"`
}

// chosen is when the hub was made its owner's default: when it was paired,
// if not since.
func (h Hub) chosen() time.Time {
	if h.Chosen.IsZero() {
		return h.Created
	}
	return h.Chosen
}

// Pairing is a hub on its way to being paired: asked for by the hub, then
// confirmed by the one who sends its code in Google Chat.
type Pairing struct {
	Code    string    `json:"code"`
	Claim   string    `json:"claim"` // hex SHA-256 of what the hub collects its token with
	Name    string    `json:"name"`
	Expires time.Time `json:"expires"`
	// Once confirmed: by whom, and where they talk to the app.
	Owner     string `json:"owner,omitempty"`
	OwnerName string `json:"ownerName,omitempty"`
	Email     string `json:"email,omitempty"`
	Space     string `json:"space,omitempty"`
}

// Route is a thread of a hub's, for one owner: Google Chat's thread, and the
// hub's name for it, where the hub made it before Google Chat had one.
type Route struct {
	Thread string `json:"thread"` // spaces/…/threads/…
	Owner  string `json:"owner"`
	Hub    string `json:"hub"`
	As     string `json:"as"` // the hub's id for the thread
}

var errNotFound = errors.New("not found")

// Store keeps hubs, pairings and routes: Firestore's, or memory's to try the
// relay out.
type Store interface {
	PutPairing(ctx context.Context, p Pairing) error
	Pairing(ctx context.Context, code string) (Pairing, error)
	PairingByClaim(ctx context.Context, claim string) (Pairing, error)
	DeletePairing(ctx context.Context, code string) error

	PutHub(ctx context.Context, h Hub) error
	Hub(ctx context.Context, id string) (Hub, error)
	HubsOf(ctx context.Context, owner string) ([]Hub, error)
	DeleteHub(ctx context.Context, id string) error

	PutRoute(ctx context.Context, r Route) error
	Route(ctx context.Context, thread, owner string) (Route, error)
}

// memory is a Store that forgets on stopping.
type memory struct {
	mu       sync.Mutex
	pairings map[string]Pairing
	hubs     map[string]Hub
	routes   map[string]Route
}

func newMemory() *memory {
	return &memory{pairings: map[string]Pairing{}, hubs: map[string]Hub{}, routes: map[string]Route{}}
}

func (m *memory) PutPairing(ctx context.Context, p Pairing) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pairings[p.Code] = p
	return nil
}

func (m *memory) Pairing(ctx context.Context, code string) (Pairing, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.pairings[code]
	if !ok {
		return p, errNotFound
	}
	return p, nil
}

func (m *memory) PairingByClaim(ctx context.Context, claim string) (Pairing, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.pairings {
		if p.Claim == claim {
			return p, nil
		}
	}
	return Pairing{}, errNotFound
}

func (m *memory) DeletePairing(ctx context.Context, code string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.pairings, code)
	return nil
}

func (m *memory) PutHub(ctx context.Context, h Hub) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.hubs[h.ID] = h
	return nil
}

func (m *memory) Hub(ctx context.Context, id string) (Hub, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.hubs[id]
	if !ok {
		return h, errNotFound
	}
	return h, nil
}

func (m *memory) HubsOf(ctx context.Context, owner string) ([]Hub, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Hub
	for _, h := range m.hubs {
		if h.Owner == owner {
			out = append(out, h)
		}
	}
	return out, nil
}

func (m *memory) DeleteHub(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.hubs, id)
	return nil
}

func (m *memory) PutRoute(ctx context.Context, r Route) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.routes[r.Thread+"|"+r.Owner] = r
	return nil
}

func (m *memory) Route(ctx context.Context, thread, owner string) (Route, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.routes[thread+"|"+owner]
	if !ok {
		return r, errNotFound
	}
	return r, nil
}
