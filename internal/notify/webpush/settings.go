package webpush

import (
	"crypto/ecdh"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// status is what a page subscribes with, and whether it has.
type status struct {
	Key        string `json:"key"`     // the VAPID public key, the browser's applicationServerKey
	Devices    int    `json:"devices"` // this dv's
	Subscribed *bool  `json:"subscribed,omitempty"`
}

// HandleStatus says the key, making it the first time, and how many devices
// this dv sends to; given ?endpoint=, whether that one is among them.
func (p *Push) HandleStatus(w http.ResponseWriter, r *http.Request) {
	v, err := p.key(true)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("could not make the key notifications are signed with: %w", err))
		return
	}
	st := status{Key: v.public, Devices: len(p.devices())}
	if endpoint := r.URL.Query().Get("endpoint"); endpoint != "" {
		_, ok := p.device(endpoint)
		st.Subscribed = &ok
	}
	writeJSON(w, http.StatusOK, st)
}

// HandleSubscribe keeps a browser's subscription, as PushSubscription.toJSON
// has it, with a name for the device: { endpoint, keys: { p256dh, auth }, name }.
func (p *Push) HandleSubscribe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	d := Device{
		Endpoint: req.Endpoint, P256dh: req.Keys.P256dh, Auth: req.Keys.Auth,
		Name: clip(strings.TrimSpace(req.Name), 80), Added: time.Now(), DV: p.self,
	}
	if err := d.check(); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := p.add(d); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// check is d as a browser subscribes: an https endpoint, a P-256 public key
// and a 16-byte auth secret.
func (d Device) check() error {
	if u, err := url.Parse(d.Endpoint); err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("A subscription's endpoint is an https address")
	}
	ua, err := decodeKey(d.P256dh)
	if err == nil {
		_, err = ecdh.P256().NewPublicKey(ua) // the uncompressed point alone: 65 bytes
	}
	if err != nil {
		return errors.New("A subscription's p256dh is a P-256 public key, 65 bytes in base64url")
	}
	if auth, err := decodeKey(d.Auth); err != nil || len(auth) != 16 {
		return errors.New("A subscription's auth is 16 bytes, in base64url")
	}
	return nil
}

// HandleUnsubscribe forgets a device: { endpoint }.
func (p *Push) HandleUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req); err != nil || req.Endpoint == "" {
		writeErr(w, http.StatusBadRequest, errors.New("expected { endpoint }"))
		return
	}
	if err := p.forget(req.Endpoint); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// HandleTest sends a notification to a device now: { endpoint }, which this
// dv sends to. What its push service says to it is the answer.
func (p *Push) HandleTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Endpoint string `json:"endpoint"`
	}
	json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&req)
	d, ok := p.device(req.Endpoint)
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("This device does not get dv's notifications yet: turn them on first"))
		return
	}
	m := message{ID: "dv-test", Kind: "done", Title: "dv", Body: "Notifications reach this device.", URL: "/"}
	if err := p.push(r.Context(), d, m); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
