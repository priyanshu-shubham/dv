package gchat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"dv/internal/notify/chat"
	"dv/internal/relay"
)

// status is what the Settings page shows. It never has the token.
type status struct {
	Relay string `json:"relay,omitempty"`
	// Pairing: the code to send the app in Google Chat, until it is.
	Code    string    `json:"code,omitempty"`
	Expires time.Time `json:"expires,omitzero"`
	// Paired: the hub, and whom it is for.
	Name  string `json:"name,omitempty"`
	Owner string `json:"owner,omitempty"`
	Email string `json:"email,omitempty"`
	// Connected is this dv taking the hub's events; Elsewhere, another of
	// this computer's; Lost says why the relay turned the token down.
	Connected bool   `json:"connected"`
	Elsewhere bool   `json:"elsewhere,omitempty"`
	Lost      string `json:"lost,omitempty"`
	Sealed    bool   `json:"sealed,omitempty"` // a token is kept that will not open here
}

func (g *GChat) status() status {
	cfg := g.config()
	g.mu.Lock()
	defer g.mu.Unlock()
	return status{
		Relay: cfg.Relay, Code: cfg.Code, Expires: cfg.Expires,
		Name: cfg.Name, Owner: cfg.Owner, Email: cfg.Email,
		Connected: g.connected, Elsewhere: g.elsewhere, Lost: g.lost,
		Sealed: cfg.Token == "" && cfg.Sealed != "",
	}
}

func (g *GChat) HandleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, g.status())
}

// HandleSetUp pairs the hub with a relay: { relay, origin }, the relay's
// address and where the page asking is, for the messages' links. The relay
// gives a code, for the owner to send its app in Google Chat.
func (g *GChat) HandleSetUp(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Relay  string `json:"relay"`
		Origin string `json:"origin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	address := strings.TrimRight(strings.TrimSpace(req.Relay), "/")
	if u, err := url.Parse(address); err != nil || u.Host == "" || u.Scheme != "https" && !local(u.Hostname()) {
		writeErr(w, http.StatusBadRequest, errors.New("That is not a relay's address: it starts https://"))
		return
	}
	name, _ := os.Hostname()
	var p relay.Pairing
	if err := g.call(r.Context(), Config{Relay: address}, http.MethodPost, relay.PathPair, relay.PairRequest{Name: name}, &p); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	if _, err := g.change(func(c *Config) {
		*c = Config{Relay: address, Origin: req.Origin, Code: p.Code, Claim: p.Claim, Expires: p.Expires}
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	g.mu.Lock()
	g.lost = ""
	g.mu.Unlock()
	g.poke()
	writeJSON(w, http.StatusOK, g.status())
}

// local is an address on this computer, which a relay run to try it out may
// have, without https.
func local(host string) bool { return host == "localhost" || host == "127.0.0.1" || host == "::1" }

// HandleRemove forgets the relay, asking it to forget the hub too.
func (g *GChat) HandleRemove(w http.ResponseWriter, r *http.Request) {
	if cfg := g.config(); cfg.Token != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		g.call(ctx, cfg, http.MethodDelete, relay.PathMe, nil, nil)
		cancel()
	}
	if _, err := g.change(func(c *Config) { *c = Config{} }); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	g.setConnected(false, false)
	writeJSON(w, http.StatusOK, g.status())
}

// HandleTest sends a message to the owner: { origin }, where the page asking
// is, which links go to from now on.
func (g *GChat) HandleTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Origin string `json:"origin"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	cfg, err := g.change(func(c *Config) {
		if req.Origin != "" {
			c.Origin = req.Origin
		}
	})
	if err == nil && cfg.Token == "" {
		err = errors.New("Pair with a relay first")
	}
	if err == nil {
		var rows [][]chat.Button
		if link := chat.LinkTo(cfg.Origin, "/"); link != "" {
			rows = [][]chat.Button{{{Text: "Open dv", URL: link}}}
		}
		name, _ := os.Hostname()
		_, err = g.Post(r.Context(), "", chat.Out{Text: "dv on " + name + " can reach you here.", Plain: true, Buttons: rows})
	}
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, g.status())
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
