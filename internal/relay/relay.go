// Package relay is how a dv hub and a chat relay talk. dv-gchat-relay takes
// Google Chat's messages for the hubs they are for, and puts in Google Chat
// what the hubs send back. A hub connects out to the relay, over HTTPS, so it
// needs no address anyone can reach, and the relay keeps no message: it hands
// each one on as it comes.
//
// What goes between them is a chat's, not Google Chat's: messages in Markdown
// with buttons, threads, taps. The conversation - commands, starting sessions,
// prompts - is dv's (package chat), the same as in any app; how it looks in
// Google Chat is the relay's.
//
// A hub pairs once: it asks for a code, which its owner sends the app in Google
// Chat, and collects its token when they have. The token then goes with every
// request, as a bearer token.
package relay

import "time"

// The relay's API for hubs.
const (
	PathPair     = "/v1/pair"       // POST PairRequest: Pairing; open to anyone, and worth nothing until its code is sent in Chat
	PathClaim    = "/v1/pair/claim" // POST Claim: Paired, 202 until the code is sent, 410 once it has expired
	PathMe       = "/v1/me"         // GET: Me; DELETE forgets the hub
	PathEvents   = "/v1/events"     // GET ?after=id&wait=seconds&conn=id: Events, as they come
	PathPost     = "/v1/post"       // POST Post: Posted
	PathEdit     = "/v1/edit"       // POST Edit
	PathUnbutton = "/v1/unbutton"   // POST Unbutton
	PathThread   = "/v1/thread"     // POST NewThread: Thread
	PathMark     = "/v1/mark"       // POST Mark
	PathAnswer   = "/v1/answer"     // POST Answer
)

// Wait is the longest an events request is held for, in seconds, while none
// come; a hub asks again at once.
const Wait = 25

// PairRequest asks for a pairing code, for a hub called Name.
type PairRequest struct {
	Name string `json:"name"`
}

// Pairing is a code for the hub's owner to send the app in Google Chat, and
// Claim, which only the hub has, to collect its token with once they have.
type Pairing struct {
	Code    string    `json:"code"`
	Claim   string    `json:"claim"`
	Expires time.Time `json:"expires"`
}

type Claim struct {
	Claim string `json:"claim"`
}

// Paired is a hub's token, and whom it is for.
type Paired struct {
	Token string `json:"token"`
	Me
}

// Me is a paired hub: its name, and its owner, as Google Chat gives them.
type Me struct {
	Name  string `json:"name"`
	Owner string `json:"owner"`
	Email string `json:"email"`
}

type Events struct {
	Events []Event `json:"events"`
}

// Event is what the hub's owner did in Google Chat: wrote a message, or
// tapped a button, which Answer answers. IDs grow; a hub asks for those after
// the last it took.
type Event struct {
	ID   int64  `json:"id"`
	Kind string `json:"kind"` // "message" or "tap"
	// Where: a thread, "" for none, and the message written or tapped.
	Thread string `json:"thread,omitempty"`
	Ref    string `json:"ref,omitempty"`
	// A message's. Shared is written where others read it too: in a space,
	// not the owner's direct messages with the app.
	ReplyTo string  `json:"replyTo,omitempty"`
	Text    string  `json:"text,omitempty"`
	Images  []Image `json:"images,omitempty"`
	Shared  bool    `json:"shared,omitempty"`
	// A tap's: the button's data, and the tap, to answer.
	Data string `json:"data,omitempty"`
	Tap  string `json:"tap,omitempty"`
}

// Image is a picture, of a media Type such as "image/png".
type Image struct {
	Type string `json:"type"`
	Data []byte `json:"data"`
}

// Button is one under a message: a tap hands Data back, or it opens URL.
type Button struct {
	Text string `json:"text"`
	Data string `json:"data,omitempty"`
	URL  string `json:"url,omitempty"`
}

// Post is a message for a thread, "" the owner's chat with the app. Text is
// Markdown, or with Plain words as they are. A Private one is for the owner
// alone, where others read the thread.
type Post struct {
	Thread  string     `json:"thread,omitempty"`
	Text    string     `json:"text"`
	Plain   bool       `json:"plain,omitempty"`
	Buttons [][]Button `json:"buttons,omitempty"`
	Private bool       `json:"private,omitempty"`
}

// Posted is a message posted, by which to edit it.
type Posted struct {
	Ref string `json:"ref"`
}

type Edit struct {
	Ref string `json:"ref"`
	Post
}

type Unbutton struct {
	Thread string `json:"thread,omitempty"`
	Ref    string `json:"ref"`
}

// NewThread asks for a thread for a session, named Name where threads have
// names.
type NewThread struct {
	Name string `json:"name"`
}

type Thread struct {
	Thread string `json:"thread"`
}

// Mark is how far a session has got with a message its owner sent: At, as a
// notify.Progress, from Was, -1 for the first time.
type Mark struct {
	Thread string `json:"thread,omitempty"`
	Ref    string `json:"ref"`
	Was    int    `json:"was"`
	At     int    `json:"at"`
}

// Answer is a tap's, with a note for the owner, "" for none, and whether it
// is of the tap failing: all a chat without passing notes may show.
type Answer struct {
	Tap    string `json:"tap"`
	Text   string `json:"text,omitempty"`
	Failed bool   `json:"failed,omitempty"`
}

// Error is a request refused, and why.
type Error struct {
	Error string `json:"error"`
}
