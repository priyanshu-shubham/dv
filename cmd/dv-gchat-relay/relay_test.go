package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"dv/internal/notify"
	"dv/internal/notify/gchat"
	"dv/internal/relay"
)

// fakeChat is Google Chat's API, keeping what the app posts.
type fakeChat struct {
	mu      sync.Mutex
	n       int
	posts   []posted
	patches []posted
	ids     map[string]string // the ids the app gave, by Chat's names
}

type posted struct {
	space, name, mask string
	m                 message
}

func (f *fakeChat) create(ctx context.Context, space, id string, m message) (message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.n++
	in := fmt.Sprintf("%s/threads/t%d", space, f.n)
	if m.Thread != nil && m.Thread.Name != "" {
		in = m.Thread.Name
	} else if m.Thread != nil {
		in = space + "/threads/" + m.Thread.ThreadKey
	}
	name := fmt.Sprintf("%s/messages/m%d", space, f.n)
	f.ids[name] = id
	f.posts = append(f.posts, posted{space: space, name: space + "/messages/" + id, m: m})
	return message{Name: name, Thread: &thread{Name: in}, ClientAssignedMessageID: id}, nil
}

func (f *fakeChat) patch(ctx context.Context, name, mask string, m message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.patches = append(f.patches, posted{name: name, mask: mask, m: m})
	return nil
}

func (f *fakeChat) get(ctx context.Context, name string) (message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return message{Name: name, ClientAssignedMessageID: f.ids[name]}, nil
}

func (f *fakeChat) media(ctx context.Context, resource string) ([]byte, error) {
	return []byte("picture " + resource), nil
}

// await takes the first message posted, or changed, that is wanted.
func (f *fakeChat) await(t *testing.T, changed bool, want func(posted) bool) posted {
	t.Helper()
	for until := time.Now().Add(3 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
		f.mu.Lock()
		list := &f.posts
		if changed {
			list = &f.patches
		}
		for i, p := range *list {
			if want(p) {
				*list = append((*list)[:i], (*list)[i+1:]...)
				f.mu.Unlock()
				return p
			}
		}
		f.mu.Unlock()
	}
	t.Fatal("no such message")
	return posted{}
}

// folder is a folder's sessions, keeping what was sent them.
type folder struct {
	mu      sync.Mutex
	said    []string
	started int
}

func (f *folder) List() []notify.Session { return nil }
func (f *folder) Send(s string, m notify.Message) (string, error) {
	said := s + ": " + m.Text + " (via " + m.Via + ")"
	if len(m.Images) > 0 {
		said += fmt.Sprintf(" with %s", m.Images[0].Data)
	}
	for _, file := range m.Files {
		said += fmt.Sprintf(" and %s: %s", file.Name, file.Data)
	}
	f.did(said)
	return "m1", nil
}
func (f *folder) Start(m notify.Message) (string, error) {
	f.did("started: " + m.Text + " (via " + m.Via + ")")
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started++
	return fmt.Sprintf("s%d", f.started), nil
}
func (f *folder) Progress(s, message string) notify.Progress { return notify.Finished }
func (f *folder) Stop(s string) error                        { return nil }
func (f *folder) Reply(s string) string                      { return "" }
func (f *folder) Setup(s string) notify.Setup                { return notify.Setup{} }
func (f *folder) Configure(string, *string, *string, bool) error {
	return nil
}
func (f *folder) did(s string) {
	f.mu.Lock()
	f.said = append(f.said, s)
	f.mu.Unlock()
}
func (f *folder) count(want string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, s := range f.said {
		if s == want {
			n++
		}
	}
	return n
}
func (f *folder) await(t *testing.T, want string) {
	t.Helper()
	for until := time.Now().Add(3 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
		if f.count(want) > 0 {
			return
		}
	}
	t.Fatalf("never %q: %q", want, f.said)
}

// chatSays sends the relay an event, as Google Chat does, and returns what
// the relay answers.
func chatSays(t *testing.T, srv *httptest.Server, e event) message {
	t.Helper()
	b, _ := json.Marshal(e)
	resp, err := http.Post(srv.URL+"/chat", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m message
	json.NewDecoder(resp.Body).Decode(&m)
	return m
}

// hubCalls calls the relay as a hub.
func hubCalls(t *testing.T, srv *httptest.Server, token, path string, in, out any) int {
	t.Helper()
	b, _ := json.Marshal(in)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(b))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func said(name, text, in string) *message {
	return &message{Name: name, Text: text, ArgumentText: text, Thread: &thread{Name: in}}
}

func TestRelay(t *testing.T) {
	defer func(d time.Duration) { deliverFor = d }(deliverFor)
	deliverFor = 300 * time.Millisecond
	chat := &fakeChat{ids: map[string]string{}}
	r := newRelay(newMemory(), chat, func(email string) bool { return strings.HasSuffix(email, "@example.com") }, nil)
	srv := httptest.NewServer(r.Handler())
	defer srv.Close()

	ada := user{Name: "users/1", DisplayName: "Ada", Email: "ada@example.com", Type: "HUMAN"}
	bob := user{Name: "users/2", DisplayName: "Bob", Email: "bob@example.com", Type: "HUMAN"}
	eve := user{Name: "users/3", DisplayName: "Eve", Email: "eve@elsewhere.com", Type: "HUMAN"}
	adaDM := space{Name: "spaces/dm1", SpaceType: "DIRECT_MESSAGE", SingleUserBotDm: true}
	bobDM := space{Name: "spaces/dm2", SpaceType: "DIRECT_MESSAGE", SingleUserBotDm: true}
	room := space{Name: "spaces/room", SpaceType: "SPACE"}

	// Ada's dv.
	center := notify.New(func() time.Duration { return 0 })
	defer center.Close()
	sessions := &folder{}
	alpha := center.Folder("alpha", "Alpha")
	alpha.Serve(sessions)
	g := gchat.New(center, "http://127.0.0.1:1", t.TempDir(), t.TempDir())
	center.Add(g)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Run(ctx)

	w := httptest.NewRecorder()
	g.HandleSetUp(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"relay": "`+srv.URL+`", "origin": "https://dv.example.com"}`)))
	var pairing struct{ Code string }
	json.Unmarshal(w.Body.Bytes(), &pairing)
	if w.Code != 200 || pairing.Code == "" {
		t.Fatalf("set up: %d %s", w.Code, w.Body)
	}

	// Only those allowed may pair, and only in their direct messages.
	if m := chatSays(t, srv, event{Type: "MESSAGE", User: eve, Space: adaDM, Message: said("spaces/dm3/messages/e1", "connect "+pairing.Code, "spaces/dm3/threads/e1")}); m.Text != "This relay is not open to you." {
		t.Fatalf("to a stranger: %+v", m)
	}
	if m := chatSays(t, srv, event{Type: "MESSAGE", User: ada, Space: room, Message: said("spaces/room/messages/c1", "connect "+pairing.Code, "spaces/room/threads/c1")}); !strings.Contains(m.Text, "direct message") || m.PrivateMessageViewer == nil {
		t.Fatalf("in a space: %+v", m)
	}
	if m := chatSays(t, srv, event{Type: "MESSAGE", User: ada, Space: adaDM, Message: said("spaces/dm1/messages/c2", "connect "+strings.ToLower(pairing.Code), "spaces/dm1/threads/c2")}); !strings.HasPrefix(m.Text, "Paired with dv on") {
		t.Fatalf("paired: %+v", m)
	}
	for until := time.Now().Add(5 * time.Second); !g.Ready(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(until) {
			t.Fatal("dv never connected")
		}
	}

	// A message in the direct messages starts a session, in its thread.
	if m := chatSays(t, srv, event{Type: "MESSAGE", User: ada, Space: adaDM, Message: said("spaces/dm1/messages/u1", "fix the tests", "spaces/dm1/threads/u1")}); m.Text != "" {
		t.Fatalf("answered: %+v", m)
	}
	sessions.await(t, "started: fix the tests (via Google Chat)")
	started := chat.await(t, false, func(p posted) bool { return strings.Contains(p.m.Text, "*Started* in Alpha.") })
	if started.space != "spaces/dm1" || started.m.Thread.Name != "spaces/dm1/threads/u1" || started.m.PrivateMessageViewer != nil {
		t.Fatalf("started: %+v", started)
	}

	// In its thread, words and pictures go to it, a message sent again once.
	written := event{Type: "MESSAGE", User: ada, Space: adaDM, Message: said("spaces/dm1/messages/u2", "and the lint", "spaces/dm1/threads/u1")}
	written.Message.ThreadReply = true
	chatSays(t, srv, written)
	chatSays(t, srv, written)
	pictured := event{Type: "MESSAGE", User: ada, Space: adaDM, Message: said("spaces/dm1/messages/u3", "this one", "spaces/dm1/threads/u1")}
	pictured.Message.Attachment = []attachment{{ContentType: "image/png"}}
	pictured.Message.Attachment[0].AttachmentDataRef.ResourceName = "abc"
	chatSays(t, srv, pictured)
	sessions.await(t, "s1: this one (via Google Chat) with picture abc")
	// Any other file goes as a file; one from Drive, with nothing to fetch, not at all.
	filed := event{Type: "MESSAGE", User: ada, Space: adaDM, Message: said("spaces/dm1/messages/u3b", "read these", "spaces/dm1/threads/u1")}
	filed.Message.Attachment = []attachment{{ContentName: "notes.pdf", ContentType: "application/pdf"}, {ContentName: "Plan", ContentType: "application/vnd.google-apps.document"}}
	filed.Message.Attachment[0].AttachmentDataRef.ResourceName = "pdf"
	chatSays(t, srv, filed)
	sessions.await(t, "s1: read these (via Google Chat) and notes.pdf: picture pdf")
	if n := sessions.count("s1: and the lint (via Google Chat)"); n != 1 {
		t.Fatalf("sent %d times", n)
	}

	// A prompt has its buttons on a card, and a tap on one answers it.
	alpha.Raise(notify.Notice{
		ID: "a1", Kind: notify.Ask, Session: "s1", Title: "Claude wants to run a command", Body: "npm test",
		Format: notify.Code, Choices: []string{"Yes", "No"}, Answer: func(int) error { return nil },
	})
	prompt := chat.await(t, false, func(p posted) bool { return len(p.m.CardsV2) > 0 })
	b, _ := json.Marshal(prompt.m.CardsV2)
	for _, want := range []string{`"function":"tap"`, `{"key":"data","value":"a1:0"}`, `"url":"https://dv.example.com/alpha/?session=s1"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("the prompt's card has no %s: %s", want, b)
		}
	}
	if prompt.m.Thread.Name != "spaces/dm1/threads/u1" || prompt.m.PrivateMessageViewer != nil {
		t.Fatalf("the prompt: %+v", prompt)
	}
	id := prompt.name[strings.LastIndex(prompt.name, "/")+1:]
	tap := func(u user, s space, data string) message {
		e := event{Type: "CARD_CLICKED", User: u, Space: s, Message: &message{Name: "spaces/dm1/messages/m9", Thread: &thread{Name: "spaces/dm1/threads/u1"}}}
		e.Common.InvokedFunction, e.Common.Parameters = tapFunction, map[string]string{"m": id, "data": data}
		return chatSays(t, srv, e)
	}
	if m := tap(bob, adaDM, "a1:0"); m.Text != "That is for someone else's dv." {
		t.Fatalf("Bob's tap: %+v", m)
	}
	if m := tap(ada, adaDM, "a1:1"); m.Text != "" {
		t.Fatalf("Ada's tap: %+v", m)
	}
	settled := chat.await(t, true, func(p posted) bool { return p.name == prompt.name })
	if b, _ := json.Marshal(settled.m.CardsV2); settled.mask != "text,cardsV2" || strings.Contains(string(b), "a1:0") {
		t.Fatalf("settled: %+v", settled)
	}
	// Tapped again, it says why nothing happened.
	if m := tap(ada, adaDM, "a1:0"); m.Text == "" || m.ActionResponse == nil {
		t.Fatalf("tapped again: %+v", m)
	}

	// In a space, a mention starts a session in its thread, and a prompt
	// there is for Ada alone.
	mention := event{Type: "MESSAGE", User: ada, Space: room, Message: &message{Name: "spaces/room/messages/r1", Text: "@dv fix the docs", ArgumentText: " fix the docs", Thread: &thread{Name: "spaces/room/threads/r1"}}}
	if m := chatSays(t, srv, mention); m.Text != "" {
		t.Fatalf("answered the mention: %+v", m)
	}
	sessions.await(t, "started: fix the docs (via Google Chat)")
	chat.await(t, false, func(p posted) bool {
		return p.space == "spaces/room" && p.m.Thread.Name == "spaces/room/threads/r1" && strings.Contains(p.m.Text, "*Started*")
	})
	alpha.Raise(notify.Notice{
		ID: "a2", Kind: notify.Ask, Session: "s2", Title: "Claude wants to edit", Choices: []string{"Yes", "No"}, Answer: func(int) error { return nil },
	})
	if p := chat.await(t, false, func(p posted) bool { return p.space == "spaces/room" && len(p.m.CardsV2) > 0 }); p.m.PrivateMessageViewer == nil || p.m.PrivateMessageViewer.Name != ada.Name {
		t.Fatalf("the prompt in the space: %+v", p)
	}
	// There, /new is turned down, and what is said to Ada is for her alone.
	mentioned := func(name, text string) event {
		return event{Type: "MESSAGE", User: ada, Space: room, Message: &message{Name: "spaces/room/messages/" + name, Text: "@dv " + text, ArgumentText: " " + text, Thread: &thread{Name: "spaces/room/threads/" + name}}}
	}
	chatSays(t, srv, mentioned("r2", "/new fix more"))
	chatSays(t, srv, mentioned("r3", "/sessions"))
	for _, want := range []string{"/new is for direct messages", "No sessions are open in dv"} {
		p := chat.await(t, false, func(p posted) bool { return strings.Contains(p.m.Text, want) })
		if p.space != "spaces/room" || p.m.PrivateMessageViewer == nil || p.m.PrivateMessageViewer.Name != ada.Name {
			t.Fatalf("%q: %+v", want, p)
		}
	}
	if n := sessions.count("started: fix more (via Google Chat)"); n != 0 {
		t.Fatal("/new started a session in a space")
	}

	// A second hub of Ada's is where her new sessions start, until /hub
	// picks the first again; a session's thread stays with its hub.
	var vm relay.Pairing
	hubCalls(t, srv, "", relay.PathPair, relay.PairRequest{Name: "vm"}, &vm)
	if m := chatSays(t, srv, event{Type: "MESSAGE", User: ada, Space: adaDM, Message: said("spaces/dm1/messages/v1", "connect "+vm.Code, "spaces/dm1/threads/v1")}); !strings.Contains(m.Text, "/hub switches") {
		t.Fatalf("paired again: %+v", m)
	}
	if code := hubCalls(t, srv, "", relay.PathClaim, relay.Claim{Claim: vm.Claim}, nil); code != 200 {
		t.Fatalf("the second claim: %d", code)
	}
	if m := chatSays(t, srv, event{Type: "MESSAGE", User: ada, Space: adaDM, Message: said("spaces/dm1/messages/v2", "fix the build", "spaces/dm1/threads/v2")}); m.Text != "Your dv on vm is not connected to the relay just now. /hub picks another of yours to start sessions on." {
		t.Fatalf("on the new hub: %+v", m)
	}
	chatSays(t, srv, event{Type: "MESSAGE", User: ada, Space: adaDM, Message: said("spaces/dm1/messages/u4", "and the docs", "spaces/dm1/threads/u1")})
	sessions.await(t, "s1: and the docs (via Google Chat)")
	list := chatSays(t, srv, event{Type: "MESSAGE", User: ada, Space: adaDM, Message: said("spaces/dm1/messages/v3", "/hub", "spaces/dm1/threads/v3")})
	b, _ = json.Marshal(list.CardsV2)
	host, _ := os.Hostname()
	if !strings.Contains(string(b), `"✓ vm · not connected"`) || !strings.Contains(string(b), `"text":"`+host+`"`) {
		t.Fatalf("the list: %s", b)
	}
	first := list.CardsV2[0].Card.Sections[0].Widgets[0].ButtonList.Buttons[0].OnClick.Action.Parameters[0].Value
	pick := event{Type: "CARD_CLICKED", User: ada, Space: adaDM, Message: &message{Name: "spaces/dm1/messages/m20"}}
	pick.Common.InvokedFunction, pick.Common.Parameters = hubFunction, map[string]string{"h": first}
	picked := chatSays(t, srv, pick)
	b, _ = json.Marshal(picked.CardsV2)
	if picked.ActionResponse == nil || picked.ActionResponse.Type != "UPDATE_MESSAGE" || !strings.Contains(string(b), `"✓ `+host+`"`) {
		t.Fatalf("picked: %+v %s", picked, b)
	}
	pick.User = bob
	if m := chatSays(t, srv, pick); m.Text != "That is for someone else's dv." {
		t.Fatalf("Bob's pick: %+v", m)
	}
	chatSays(t, srv, event{Type: "MESSAGE", User: ada, Space: adaDM, Message: said("spaces/dm1/messages/v4", "fix the build", "spaces/dm1/threads/v4")})
	sessions.await(t, "started: fix the build (via Google Chat)")

	// Bob has no dv yet.
	if m := chatSays(t, srv, event{Type: "MESSAGE", User: bob, Space: bobDM, Message: said("spaces/dm2/messages/b1", "hello", "spaces/dm2/threads/b1")}); m.Text != errNoHub.Error() {
		t.Fatalf("to Bob: %+v", m)
	}
	// Once he has, it may post only to his own threads and change only its own
	// messages.
	var bobs relay.Pairing
	hubCalls(t, srv, "", relay.PathPair, relay.PairRequest{Name: "bobs"}, &bobs)
	chatSays(t, srv, event{Type: "MESSAGE", User: bob, Space: bobDM, Message: said("spaces/dm2/messages/b2", "connect "+bobs.Code, "spaces/dm2/threads/b2")})
	var paired relay.Paired
	if code := hubCalls(t, srv, "", relay.PathClaim, relay.Claim{Claim: bobs.Claim}, &paired); code != 200 || paired.Owner != "Bob" {
		t.Fatalf("Bob's claim: %d %+v", code, paired)
	}
	if code := hubCalls(t, srv, paired.Token, relay.PathPost, relay.Post{Thread: "spaces/room/threads/r1", Text: "hi"}, nil); code != http.StatusForbidden {
		t.Fatalf("posted in Ada's thread: %d", code)
	}
	if code := hubCalls(t, srv, paired.Token, relay.PathEdit, relay.Edit{Ref: prompt.name, Post: relay.Post{Text: "hi"}}, nil); code != http.StatusBadRequest {
		t.Fatalf("changed Ada's message: %d", code)
	}
	if code := hubCalls(t, srv, paired.Token, relay.PathPost, relay.Post{Text: "hi"}, nil); code != 200 {
		t.Fatalf("posted in his own messages: %d", code)
	}
	chat.await(t, false, func(p posted) bool { return p.space == "spaces/dm2" && p.m.Text == "hi" })
	// His dv not connected, he is told.
	if m := chatSays(t, srv, event{Type: "MESSAGE", User: bob, Space: bobDM, Message: said("spaces/dm2/messages/b3", "hello", "spaces/dm2/threads/b3")}); m.Text != "Your dv on bobs is not connected to the relay just now." {
		t.Fatalf("to Bob: %+v", m)
	}

	// Removed from dv, the hub is forgotten, and new sessions start on the
	// one left.
	g.HandleRemove(httptest.NewRecorder(), httptest.NewRequest("DELETE", "/", nil))
	if m := chatSays(t, srv, event{Type: "MESSAGE", User: ada, Space: adaDM, Message: said("spaces/dm1/messages/u9", "hello", "spaces/dm1/threads/u9")}); m.Text != "Your dv on vm is not connected to the relay just now." {
		t.Fatalf("after removing: %+v", m)
	}
}

func TestChatText(t *testing.T) {
	for md, want := range map[string]string{
		"**Started** in Alpha: fix *it* now": "*Started* in Alpha: fix _it_ now",
		"see [the docs](https://x.dev/a)":    "see <https://x.dev/a|the docs>",
		"`**not bold**` and ~~gone~~":        "`**not bold**` and ~gone~",
		"## Heading **b**":                   "*Heading b*",
		"- one\n- two":                       "• one\n• two",
		"```go\nx := *p\n```":                "```\nx := *p\n```",
		"| a | b |\n| - | - |":               "```\n| a | b |\n| - | - |\n```",
		`a \*star\* and \_under\_`:           "a *star* and _under_",
	} {
		if got := chatText(md); got != want {
			t.Errorf("chatText(%q) = %q, want %q", md, got, want)
		}
	}
}

func TestOwns(t *testing.T) {
	h := Hub{ID: "0123456789abcdef0123"}
	id := messageID(h)
	if !owns(h, "spaces/AAA/messages/"+id) {
		t.Fatalf("does not own %s", id)
	}
	for _, ref := range []string{"spaces/AAA/messages/abc", "spaces/AAA/messages/client-ffffffffffff-00", "spaces/AAA/../messages/" + id} {
		if owns(h, ref) {
			t.Errorf("owns %s", ref)
		}
	}
}

func TestConnectCode(t *testing.T) {
	for text, want := range map[string]string{"connect ABCD-2345": "ABCD-2345", "Connect abcd2345": "ABCD-2345", "connect it": "", "connect ABCD-2345 now": ""} {
		if got, _ := connectCode(text); got != want {
			t.Errorf("connectCode(%q) = %q, want %q", text, got, want)
		}
	}
}
