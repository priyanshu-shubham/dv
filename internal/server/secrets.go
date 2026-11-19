package server

import (
	"encoding/json"
	"errors"
	"net/http"

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
