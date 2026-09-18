package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// status is what the Settings page shows. It never has the token.
type status struct {
	Bot     string `json:"bot,omitempty"`
	Chat    string `json:"chat,omitempty"`    // who it sends to, once connected
	Connect string `json:"connect,omitempty"` // the link that connects a chat, until one is
	Origin  string `json:"origin,omitempty"`  // where its links open dv
	Sending bool   `json:"sending"`           // this dv sends for the computer
	Sender  string `json:"sender,omitempty"`  // or the one that does
	Lost    bool   `json:"lost,omitempty"`    // a token is kept, but will not open on this computer
	// NoTopics is the bot having had its topics turned off since it was set up.
	NoTopics string `json:"noTopics,omitempty"`
}

func (t *Telegram) status(ctx context.Context) status {
	cfg := t.config()
	if cfg.Token == "" {
		return status{Lost: cfg.Sealed != ""}
	}
	st := status{Bot: cfg.Bot, Chat: cfg.Name, Origin: cfg.Origin, Sending: t.isSending()}
	if cfg.Chat == 0 && cfg.Code != "" {
		st.Connect = "https://t.me/" + cfg.Bot + "?start=" + cfg.Code
	}
	if !st.Sending {
		st.Sender = t.sender(ctx)
	}
	// Asked again while they are off, so turning them on shows at once.
	t.mu.Lock()
	off := t.topics != nil && !*t.topics
	t.mu.Unlock()
	if st.Sending && off && !t.hasTopics(ctx, cfg) {
		st.NoTopics = errNoTopics.Error()
	}
	return st
}

// HandleStatus says how Telegram is set up; other dvs ask it too, to learn
// whether this one sends.
func (t *Telegram) HandleStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, t.status(r.Context()))
}

// HandleSetUp takes a bot's token, { token, origin }, checking it with
// Telegram, and readies a chat to connect to it: a new bot starts afresh.
// origin is where the page asking is, for the messages' links.
func (t *Telegram) HandleSetUp(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token  string `json:"token"`
		Origin string `json:"origin"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" || strings.ContainsAny(token, "/?#% ") {
		writeErr(w, http.StatusBadRequest, errors.New("That is not a bot token: BotFather's look like 123456:ABC-DEF…"))
		return
	}
	var me user
	if err := t.api.call(r.Context(), token, "getMe", map[string]any{}, &me); err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("Telegram did not take that token: "+strings.TrimPrefix(err.Error(), "Telegram said: ")))
		return
	}
	if !me.Topics {
		writeErr(w, http.StatusBadRequest, errNoTopics)
		return
	}
	if _, err := t.change(func(c *Config) {
		*c = Config{Token: token, Bot: me.Username, Code: newCode(), Origin: req.Origin}
	}); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	select {
	case t.wake <- struct{}{}:
	default:
	}
	writeJSON(w, http.StatusOK, t.status(r.Context()))
}

// HandleRemove forgets the bot.
func (t *Telegram) HandleRemove(w http.ResponseWriter, r *http.Request) {
	if _, err := t.change(func(c *Config) { *c = Config{} }); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, t.status(r.Context()))
}

// HandleTest sends a message to the chat connected: { origin }, where the
// page asking is, which links go to from now on.
func (t *Telegram) HandleTest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Origin string `json:"origin"`
	}
	json.NewDecoder(r.Body).Decode(&req)
	if err := t.Test(r.Context(), req.Origin); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, t.status(r.Context()))
}

// HandlePicture makes a picture the bot's: { photo }, a JPEG, which the page
// draws from the tab's icon so that each computer's bot looks like its tabs.
func (t *Telegram) HandlePicture(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Photo []byte `json:"photo"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !bytes.HasPrefix(req.Photo, []byte{0xFF, 0xD8, 0xFF}) {
		writeErr(w, http.StatusBadRequest, errors.New("Telegram takes a bot's picture as a JPEG, and this is not one"))
		return
	}
	cfg := t.config()
	if cfg.Token == "" {
		writeErr(w, http.StatusBadRequest, errors.New("Set up the bot first"))
		return
	}
	// Telegram takes it as a file attached under the name the field points to.
	photo := `{"type":"static","photo":"attach://picture"}`
	if err := t.api.upload(r.Context(), cfg.Token, "setMyProfilePhoto", map[string]string{"photo": photo}, "picture", req.Photo); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, t.status(r.Context()))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}
