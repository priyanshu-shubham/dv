package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"dv/internal/gateway"
)

var errNoGateway = errors.New("dv's model gateway is not running, so it masks nothing")

// HandleSecrets lists the secrets masked in what sessions send a model.
func HandleSecrets(w http.ResponseWriter, r *http.Request) {
	gw := gateway.Active()
	if gw == nil {
		writeErr(w, http.StatusServiceUnavailable, errNoGateway)
		return
	}
	list, err := gw.Secrets()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// HandleAddSecret masks a secret from the next request on: { name, value }.
func HandleAddSecret(w http.ResponseWriter, r *http.Request) {
	gw := gateway.Active()
	if gw == nil {
		writeErr(w, http.StatusServiceUnavailable, errNoGateway)
		return
	}
	var req struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := gw.SetSecret(req.Name, req.Value); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	HandleSecrets(w, r)
}

// HandleScanSecrets is what in a message looks like a secret and isn't
// masked: { text } in, { found } out, for the page to ask before sending.
func HandleScanSecrets(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	found, names := []string{}, []string{}
	if gw := gateway.Active(); gw != nil {
		found = append(found, gw.Unmasked(req.Text)...)
		for _, v := range found {
			names = append(names, gateway.NameFor(req.Text, v))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"found": found, "names": names})
}

// HandleHeldSecrets lists the requests waiting on the user over what looks
// like a secret.
func HandleHeldSecrets(w http.ResponseWriter, r *http.Request) {
	held := []gateway.Held{}
	if gw := gateway.Active(); gw != nil {
		held = gw.HeldNow()
	}
	writeJSON(w, http.StatusOK, held)
}

// HandleAnswerHeld settles a held request: { names }, one per finding, ""
// to let that one go as it is.
func HandleAnswerHeld(w http.ResponseWriter, r *http.Request) {
	gw := gateway.Active()
	if gw == nil {
		writeErr(w, http.StatusServiceUnavailable, errNoGateway)
		return
	}
	var req struct {
		Names []string `json:"names"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := gw.Answer(r.PathValue("id"), req.Names); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// HandleAllowSecrets lets values go as they are from now on: { values }, what
// the user sent as written over the page's asking.
func HandleAllowSecrets(w http.ResponseWriter, r *http.Request) {
	gw := gateway.Active()
	if gw == nil {
		writeErr(w, http.StatusServiceUnavailable, errNoGateway)
		return
	}
	var req struct {
		Values []string `json:"values"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := gw.Allow(req.Values); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// HandleSecretValue is one secret's value, for a chip in the conversation
// the reader asked to see.
func HandleSecretValue(w http.ResponseWriter, r *http.Request) {
	gw := gateway.Active()
	if gw == nil {
		writeErr(w, http.StatusServiceUnavailable, errNoGateway)
		return
	}
	v, ok := gw.SecretValue(r.PathValue("name"))
	if !ok {
		writeErr(w, http.StatusNotFound, errors.New("no such secret"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"value": v})
}

// redactingAgent shows the Agent view each secret as its placeholder, as the
// model saw it: Claude Code's transcript has the value, put back in its
// replies, and a page is often in view of others.
func redactingAgent(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		shown := strings.Contains(r.URL.Path, "/api/agent/") || r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/api/claude/requests")
		if gw := gateway.Active(); gw != nil && shown {
			w = &redactingWriter{ResponseWriter: w, gw: gw}
		}
		h.ServeHTTP(w, r)
	})
}

type redactingWriter struct {
	http.ResponseWriter
	gw *gateway.Gateway
}

func (rw *redactingWriter) WriteHeader(status int) {
	rw.Header().Del("Content-Length")
	rw.ResponseWriter.WriteHeader(status)
}

func (rw *redactingWriter) Write(b []byte) (int, error) {
	if _, err := rw.ResponseWriter.Write(rw.gw.Redact(b)); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (rw *redactingWriter) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

// Flush is here for the streams that look for it themselves, not through
// http.ResponseController: the prompts' and a session's.
func (rw *redactingWriter) Flush() { http.NewResponseController(rw.ResponseWriter).Flush() }

// redacted is s with each secret as its placeholder, for words that leave
// the page: a notice, a chat app's message.
func redacted(s string) string {
	if gw := gateway.Active(); gw != nil {
		return gw.RedactText(s)
	}
	return s
}

func HandleRemoveSecret(w http.ResponseWriter, r *http.Request) {
	gw := gateway.Active()
	if gw == nil {
		writeErr(w, http.StatusServiceUnavailable, errNoGateway)
		return
	}
	if err := gw.DeleteSecret(r.PathValue("name")); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	HandleSecrets(w, r)
}
