package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	"dv/internal/relay"
)

// event is what Google Chat sends the app: an interaction event, of a Chat
// app that is not a Workspace add-on.
type event struct {
	Type    string   `json:"type"` // MESSAGE, CARD_CLICKED, ADDED_TO_SPACE, …
	Message *message `json:"message"`
	User    user     `json:"user"`
	Space   space    `json:"space"`
	Action  struct {
		ActionMethodName string  `json:"actionMethodName"`
		Parameters       []param `json:"parameters"`
	} `json:"action"`
	Common struct {
		InvokedFunction string            `json:"invokedFunction"`
		Parameters      map[string]string `json:"parameters"`
	} `json:"common"`
}

type space struct {
	Name            string `json:"name"`
	Type            string `json:"type"`      // DM or ROOM, as it was
	SpaceType       string `json:"spaceType"` // DIRECT_MESSAGE, GROUP_CHAT or SPACE
	SingleUserBotDm bool   `json:"singleUserBotDm"`
}

// dm is whether the space is someone's direct messages with the app.
func (s space) dm() bool {
	return s.SingleUserBotDm || s.SpaceType == "DIRECT_MESSAGE" || s.Type == "DM"
}

// handleChat takes what Google Chat sends: a message for the app, which goes
// to its writer's hub, or a tap on one of the app's buttons, which goes to
// the hub whose message it is. What the relay itself says comes back as the
// answer; hubs' words go through the API.
func (r *Relay) handleChat(w http.ResponseWriter, req *http.Request) {
	if r.verify != nil {
		if err := r.verify(req); err != nil {
			log.Printf("refused a request not from Google Chat: %v", err)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	var e event
	if err := json.NewDecoder(http.MaxBytesReader(w, req.Body, 1<<20)).Decode(&e); err != nil {
		http.Error(w, "that is no event", http.StatusBadRequest)
		return
	}
	if e.User.Type == "BOT" {
		reply(w, http.StatusOK, struct{}{})
		return
	}
	if !r.allowed(e.User.Email) {
		log.Printf("turned away %s <%s>", e.User.Name, e.User.Email)
		r.answer(w, e, "This relay is not open to you.")
		return
	}
	switch {
	case e.Type == "MESSAGE" || e.Type == "ADDED_TO_SPACE" && e.Message != nil:
		r.onMessage(w, req.Context(), e)
	case e.Type == "ADDED_TO_SPACE" && e.Space.dm():
		r.answer(w, e, "Hello! To have dv talk to you here, open Google Chat in dv's Settings, and send me the code it shows: connect ABCD-2345.")
	case e.Type == "ADDED_TO_SPACE":
		r.answer(w, e, "Mention me with what a session is to do, and your dv starts one in that thread. Each person's messages go to their own dv, and its questions to them alone.")
	case e.Type == "CARD_CLICKED":
		r.onTap(w, req.Context(), e)
	default:
		reply(w, http.StatusOK, struct{}{})
	}
}

// answer says something in reply to an event: to its writer alone, where
// others read the space.
func (r *Relay) answer(w http.ResponseWriter, e event, text string) {
	m := message{Text: text}
	if !e.Space.dm() && e.User.Name != "" {
		m.PrivateMessageViewer = &user{Name: e.User.Name}
	}
	if e.Type == "CARD_CLICKED" {
		m.ActionResponse = &actionResponse{Type: "NEW_MESSAGE"}
	}
	reply(w, http.StatusOK, m)
}

func (r *Relay) onMessage(w http.ResponseWriter, ctx context.Context, e event) {
	m := e.Message
	if m == nil || r.again(m.Name) {
		reply(w, http.StatusOK, struct{}{})
		return
	}
	text := words(m)
	if code, ok := connectCode(text); ok {
		r.answer(w, e, r.pair(ctx, e, code))
		return
	}
	if f := strings.Fields(text); len(f) > 0 && strings.EqualFold(f[0], "/hub") {
		r.showHubs(w, ctx, e)
		return
	}
	var in string
	if m.Thread != nil {
		in = m.Thread.Name
	}
	h, as, others, err := r.hubFor(ctx, e.User.Name, in)
	if err != nil {
		r.answer(w, e, err.Error())
		return
	}
	ev := relay.Event{Kind: "message", Thread: as, Ref: m.Name, Text: text, Images: r.images(ctx, m), Shared: !e.Space.dm()}
	if q := m.QuotedMessageMetadata; q != nil {
		ev.ReplyTo = r.refOf(ctx, q.Name)
	}
	if !r.deliver(ctx, h.ID, ev) {
		why := "Your dv on " + h.Name + " is not connected to the relay just now."
		if others {
			why += " /hub picks another of yours to start sessions on."
		}
		r.answer(w, e, why)
		return
	}
	reply(w, http.StatusOK, struct{}{})
}

// again is whether Google Chat sent a message before: it sends one again
// when it thinks the first went unanswered.
func (r *Relay) again(name string) bool {
	if name == "" {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.seen[name]; ok {
		return true
	}
	if len(r.seen) > 1000 {
		for k, at := range r.seen {
			if time.Since(at) > 10*time.Minute {
				delete(r.seen, k)
			}
		}
	}
	r.seen[name] = time.Now()
	return false
}

// words is what a message says to the app: without the mention of it that
// a space needs, but with a slash command's name.
func words(m *message) string {
	if m.SlashCommand == nil && m.ArgumentText != "" {
		return strings.TrimSpace(m.ArgumentText)
	}
	return strings.TrimSpace(m.Text)
}

// connectCode is the code in "connect ABCD-2345", as the relay gave it.
func connectCode(text string) (string, bool) {
	f := strings.Fields(text)
	if len(f) != 2 || !strings.EqualFold(f[0], "connect") {
		return "", false
	}
	code := strings.ToUpper(strings.ReplaceAll(f[1], "-", ""))
	if len(code) != 8 {
		return "", false
	}
	return code[:4] + "-" + code[4:], true
}

// pair confirms a hub's pairing: its code, sent in the direct messages
// where its owner hears from it.
func (r *Relay) pair(ctx context.Context, e event, code string) string {
	if !e.Space.dm() {
		return "Send me the code in a direct message: that is where your dv will talk to you."
	}
	p, err := r.store.Pairing(ctx, code)
	if err != nil || time.Now().After(p.Expires) || p.Owner != "" && p.Owner != e.User.Name {
		return "That is no code of mine, or it has expired: ask dv's Settings for another."
	}
	p.Owner, p.OwnerName, p.Email, p.Space = e.User.Name, e.User.DisplayName, e.User.Email, e.Space.Name
	if err := r.store.PutPairing(ctx, p); err != nil {
		log.Printf("could not keep a pairing: %v", err)
		return "Could not pair just now: send the code again."
	}
	if hubs, _ := r.store.HubsOf(ctx, e.User.Name); len(hubs) > 0 {
		return "Paired with dv on " + p.Name + ". New sessions now start there; /hub switches between your dvs."
	}
	return "Paired with dv on " + p.Name + ". It tells you here what its agents want and have done. Write what a session is to do to start one; /help says more."
}

var errNoHub = errors.New("You have no dv connected to the relay. In dv's Settings, open Google Chat, and send me here the code it shows.")

// hubFor is the hub an owner's message goes to, and its name for the thread
// it is in: the hub whose thread it is, or else the one the owner starts new
// sessions on, whose the thread then becomes. others is whether the owner
// has others to choose from instead.
func (r *Relay) hubFor(ctx context.Context, owner, in string) (h Hub, as string, others bool, err error) {
	if rt, ok := r.routeOf(ctx, in, owner); ok {
		if h, err := r.hub(ctx, rt.Hub); err == nil {
			return h, rt.As, false, nil
		}
	}
	hubs, err := r.hubsOf(ctx, owner)
	if err != nil {
		return Hub{}, "", false, err
	}
	h = chosen(hubs)
	r.route(ctx, h, in, in)
	return h, in, len(hubs) > 1, nil
}

// hubsOf is an owner's hubs, errNoHub for none.
func (r *Relay) hubsOf(ctx context.Context, owner string) ([]Hub, error) {
	hubs, err := r.store.HubsOf(ctx, owner)
	if err != nil {
		log.Printf("could not look up hubs: %v", err)
		return nil, errors.New("Could not look up your dv just now: try again.")
	}
	if len(hubs) == 0 {
		return nil, errNoHub
	}
	return hubs, nil
}

// chosen is the hub its owner's new sessions start on: the one last paired
// or picked with /hub.
func chosen(hubs []Hub) Hub {
	return slices.MaxFunc(hubs, func(a, b Hub) int { return a.chosen().Compare(b.chosen()) })
}

// hubFunction is what a tap on a hub in /hub's list invokes, with the hub's
// tag.
const hubFunction = "hub"

// showHubs lists the owner's hubs, the one new sessions start on ticked,
// for them to pick another.
func (r *Relay) showHubs(w http.ResponseWriter, ctx context.Context, e event) {
	hubs, err := r.hubsOf(ctx, e.User.Name)
	if err != nil {
		r.answer(w, e, err.Error())
		return
	}
	m := r.hubList(hubs, "")
	if !e.Space.dm() {
		m.PrivateMessageViewer = &user{Name: e.User.Name}
	}
	reply(w, http.StatusOK, m)
}

// pickHub makes the hub tapped in /hub's list the one new sessions start on,
// and ticks it there.
func (r *Relay) pickHub(w http.ResponseWriter, ctx context.Context, e event, t string) {
	hubs, err := r.hubsOf(ctx, e.User.Name)
	i := slices.IndexFunc(hubs, func(h Hub) bool { return tag(h) == t })
	if err != nil || i < 0 {
		r.answer(w, e, "That is for someone else's dv.")
		return
	}
	hubs[i].Chosen = time.Now()
	if err := r.putHub(ctx, hubs[i]); err != nil {
		log.Printf("could not keep a hub: %v", err)
		r.answer(w, e, "Could not change it just now: try again.")
		return
	}
	m := r.hubList(hubs, "New sessions start on dv on "+hubs[i].Name+". ")
	m.ActionResponse = &actionResponse{Type: "UPDATE_MESSAGE"}
	reply(w, http.StatusOK, m)
}

// hubList is the message listing hubs, each a button, oldest first.
func (r *Relay) hubList(hubs []Hub, said string) message {
	on := chosen(hubs)
	slices.SortFunc(hubs, func(a, b Hub) int { return a.Created.Compare(b.Created) })
	names := map[string]int{}
	for _, h := range hubs {
		names[h.Name]++
	}
	var c cardV2
	c.CardID = "hubs"
	var widgets []widget
	for _, h := range hubs {
		label := h.Name
		if names[h.Name] > 1 {
			label += " · paired " + h.Created.Format("2 Jan 15:04")
		}
		if !r.connected(h.ID) {
			label += " · not connected"
		}
		if h.ID == on.ID {
			label = "✓ " + label
		}
		var wd widget
		wd.ButtonList.Buttons = []button{{Text: label, OnClick: onClick{Action: &action{hubFunction, []param{{"h", tag(h)}}}}}}
		widgets = append(widgets, wd)
	}
	c.Card.Sections = []section{{Widgets: widgets}}
	return message{
		Text:    said + "New sessions start on the ticked dv; a session's thread stays with the dv it began on.",
		CardsV2: []cardV2{c},
	}
}

// connected is whether a dv takes the hub's events now.
func (r *Relay) connected(hub string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	l := r.live[hub]
	return l != nil && (l.waiting > 0 || time.Since(l.held) < holdFor)
}

// refOf is the name a hub knows a message of its by: the one the relay gave.
func (r *Relay) refOf(ctx context.Context, name string) string {
	m, err := r.chat.get(ctx, name)
	if err != nil || m.ClientAssignedMessageID == "" {
		return name
	}
	return spaceOf(name) + "/messages/" + m.ClientAssignedMessageID
}

// A message's pictures are handed on, so many of so large at most: they are
// held in memory on their way.
const (
	maxImages     = 5
	maxImageBytes = 10 << 20
)

func (r *Relay) images(ctx context.Context, m *message) []relay.Image {
	var out []relay.Image
	for _, a := range m.Attachment {
		if !strings.HasPrefix(a.ContentType, "image/") || a.AttachmentDataRef.ResourceName == "" || len(out) == maxImages {
			continue
		}
		b, err := r.chat.media(ctx, a.AttachmentDataRef.ResourceName)
		if err != nil {
			log.Printf("could not download a picture: %v", err)
			continue
		}
		out = append(out, relay.Image{Type: a.ContentType, Data: b})
	}
	return out
}

// onTap hands a tap to the hub whose message it was, once it is known to be
// its owner's, and waits for the hub to act on it. Only a failing says
// anything back: what it did shows on the hub's messages.
func (r *Relay) onTap(w http.ResponseWriter, ctx context.Context, e event) {
	fn, params := e.Common.InvokedFunction, e.Common.Parameters
	if fn == "" {
		fn, params = e.Action.ActionMethodName, map[string]string{}
		for _, p := range e.Action.Parameters {
			params[p.Key] = p.Value
		}
	}
	if fn == hubFunction {
		r.pickHub(w, ctx, e, params["h"])
		return
	}
	if fn != tapFunction || e.Message == nil {
		reply(w, http.StatusOK, struct{}{})
		return
	}
	id := params["m"]
	hubs, _ := r.store.HubsOf(ctx, e.User.Name)
	i := slices.IndexFunc(hubs, func(h Hub) bool { return tag(h) == tagOf(id) })
	if tagOf(id) == "" || i < 0 {
		r.answer(w, e, "That is for someone else's dv.")
		return
	}
	h := hubs[i]
	var in string
	if e.Message.Thread != nil {
		in = e.Message.Thread.Name
	}
	as := in
	if rt, ok := r.routeOf(ctx, in, h.Owner); ok && rt.Hub == h.ID {
		as = rt.As
	}
	ctx, cancel := context.WithTimeout(ctx, answerFor)
	defer cancel()
	tap := randomHex(8)
	answered := make(chan relay.Answer, 1)
	r.mu.Lock()
	r.taps[h.ID+"|"+tap] = answered
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.taps, h.ID+"|"+tap)
		r.mu.Unlock()
	}()
	ev := relay.Event{Kind: "tap", Thread: as, Ref: spaceOf(e.Message.Name) + "/messages/" + id, Data: params["data"], Tap: tap}
	if !r.deliver(ctx, h.ID, ev) {
		r.answer(w, e, "Your dv on "+h.Name+" is not connected to the relay just now.")
		return
	}
	select {
	case a := <-answered:
		if a.Failed && a.Text != "" {
			r.answer(w, e, a.Text)
			return
		}
	case <-ctx.Done():
	}
	reply(w, http.StatusOK, struct{}{})
}
