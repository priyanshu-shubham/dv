package gchat

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"dv/internal/notify"
	"dv/internal/relay"
)

// fakeRelay is a relay as a hub sees it: it pairs once its code is "sent",
// hands out the events queued, and keeps what the hub posts.
type fakeRelay struct {
	mu     sync.Mutex
	sent   bool // the code sent in Chat
	events chan relay.Event
	calls  []string // path and body
	conns  map[string]bool
}

func (f *fakeRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	reply := func(code int, v any) {
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	b, _ := json.Marshal(body)
	f.mu.Lock()
	f.calls = append(f.calls, r.URL.Path+" "+string(b))
	sent := f.sent
	f.mu.Unlock()
	if r.URL.Path != relay.PathPair && r.URL.Path != relay.PathClaim && r.Header.Get("Authorization") != "Bearer tok" {
		reply(http.StatusUnauthorized, relay.Error{Error: "This hub is not paired"})
		return
	}
	switch r.URL.Path {
	case relay.PathPair:
		reply(http.StatusOK, relay.Pairing{Code: "ABC-123", Claim: "claim", Expires: time.Now().Add(time.Minute)})
	case relay.PathClaim:
		if !sent {
			reply(http.StatusAccepted, relay.Error{Error: "Not sent yet"})
			return
		}
		reply(http.StatusOK, relay.Paired{Token: "tok", Me: relay.Me{Name: "laptop", Owner: "Ada", Email: "ada@example.com"}})
	case relay.PathEvents:
		conn := r.URL.Query().Get("conn")
		f.mu.Lock()
		f.conns[conn] = true
		taken := len(f.conns) > 1 && conn != "first"
		f.mu.Unlock()
		if taken {
			reply(http.StatusConflict, relay.Error{Error: "Another dv of this hub's is connected"})
			return
		}
		select {
		case e := <-f.events:
			reply(http.StatusOK, relay.Events{Events: []relay.Event{e}})
		case <-time.After(100 * time.Millisecond):
			reply(http.StatusOK, relay.Events{})
		case <-r.Context().Done():
		}
	case relay.PathPost:
		reply(http.StatusOK, relay.Posted{Ref: "spaces/dm/messages/9"})
	case relay.PathThread:
		reply(http.StatusOK, relay.Thread{Thread: "spaces/dm/threads/new"})
	default:
		reply(http.StatusOK, map[string]any{})
	}
}

func (f *fakeRelay) await(t *testing.T, prefix string) string {
	t.Helper()
	for until := time.Now().Add(3 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
		f.mu.Lock()
		for i, c := range f.calls {
			if strings.HasPrefix(c, prefix) {
				f.calls = append(f.calls[:i], f.calls[i+1:]...)
				f.mu.Unlock()
				return c
			}
		}
		f.mu.Unlock()
	}
	t.Fatalf("no %s", prefix)
	return ""
}

// folder is a folder's sessions, keeping the messages sent them.
type folder struct {
	mu   sync.Mutex
	said []string
}

func (f *folder) List() []notify.Session { return nil }
func (f *folder) Send(s string, m notify.Message) (string, error) {
	f.did(s + ": " + m.Text + " (via " + m.Via + ")")
	return "m1", nil
}
func (f *folder) Start(m notify.Message) (string, error) {
	f.did("started: " + m.Text + " (via " + m.Via + ")")
	return "s2", nil
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
func (f *folder) await(t *testing.T, want string) {
	t.Helper()
	for until := time.Now().Add(3 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
		f.mu.Lock()
		for _, s := range f.said {
			if s == want {
				f.mu.Unlock()
				return
			}
		}
		f.mu.Unlock()
	}
	t.Fatalf("never %q", want)
}

func TestPairAndTalk(t *testing.T) {
	fake := &fakeRelay{events: make(chan relay.Event, 4), conns: map[string]bool{}}
	server := httptest.NewServer(fake)
	defer server.Close()
	center := notify.New(func() time.Duration { return 0 })
	defer center.Close()
	sessions := &folder{}
	center.Folder("alpha", "Alpha").Serve(sessions)
	dir, keyDir := t.TempDir(), t.TempDir()
	g := New(center, "http://127.0.0.1:1", dir, keyDir)
	g.conn = "first"
	center.Add(g)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go g.Run(ctx)

	// Pairing: a code for the owner to send, then the token, once they have.
	w := httptest.NewRecorder()
	g.HandleSetUp(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"relay": "http://`+strings.TrimPrefix(server.URL, "http://")+`/", "origin": "https://dv.example.com"}`)))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"code":"ABC-123"`) {
		t.Fatalf("set up: %d %s", w.Code, w.Body)
	}
	fake.await(t, relay.PathPair)
	fake.await(t, relay.PathClaim)
	fake.mu.Lock()
	fake.sent = true
	fake.mu.Unlock()
	for until := time.Now().Add(5 * time.Second); !g.Ready(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(until) {
			t.Fatalf("never connected: %+v", g.status())
		}
	}
	if st := g.status(); st.Owner != "Ada" || st.Code != "" {
		t.Fatalf("paired: %+v", st)
	}
	kept, _ := os.ReadFile(g.configPath())
	if strings.Contains(string(kept), `"tok"`) || !strings.Contains(string(kept), `"sealed"`) {
		t.Fatalf("the token is kept as it is:\n%s", kept)
	}
	if fi, _ := os.Stat(g.configPath()); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("kept as %v", fi.Mode())
	}

	// A message outside a session's thread starts one, in the thread it came in.
	fake.events <- relay.Event{ID: 1, Kind: "message", Thread: "spaces/dm/threads/t1", Ref: "spaces/dm/messages/1", Text: "fix the tests"}
	sessions.await(t, "started: fix the tests (via Google Chat)")
	if c := fake.await(t, relay.PathPost); !strings.Contains(c, `"thread":"spaces/dm/threads/t1"`) || !strings.Contains(c, `**Started** in Alpha.`) {
		t.Fatalf("started: %s", c)
	}
	// Written in its thread, words go to it.
	fake.events <- relay.Event{ID: 2, Kind: "message", Thread: "spaces/dm/threads/t1", Ref: "spaces/dm/messages/2", Text: "and the lint"}
	sessions.await(t, "s2: and the lint (via Google Chat)")

	// A notice goes to its session's thread, a prompt for the owner alone.
	center.Folder("alpha", "Alpha").Raise(notify.Notice{
		ID: "a1", Kind: notify.Ask, Session: "s2", Title: "Claude wants to run a command", Body: "npm test",
		Format: notify.Code, Choices: []string{"Yes", "No"}, Answer: func(int) error { return nil },
	})
	c := fake.await(t, relay.PathPost)
	for _, want := range []string{`"thread":"spaces/dm/threads/t1"`, `"private":true`, `"data":"a1:0"`, `"url":"https://dv.example.com/alpha/?session=s2"`} {
		if !strings.Contains(c, want) {
			t.Fatalf("the prompt has no %s: %s", want, c)
		}
	}
	// A tap is answered with what it did.
	fake.events <- relay.Event{ID: 3, Kind: "tap", Thread: "spaces/dm/threads/t1", Ref: "spaces/dm/messages/9", Data: "a1:1", Tap: "tap-1"}
	if c := fake.await(t, relay.PathAnswer); !strings.Contains(c, `"tap":"tap-1"`) || !strings.Contains(c, `"text":"No"`) {
		t.Fatalf("the answer: %s", c)
	}

	// Another dv of the computer's waits while this one is connected.
	other := New(center, "http://127.0.0.1:2", dir, keyDir)
	go other.Run(ctx)
	for until := time.Now().Add(3 * time.Second); !other.status().Elsewhere; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(until) {
			t.Fatalf("the other dv: %+v", other.status())
		}
	}

	w = httptest.NewRecorder()
	g.HandleRemove(w, httptest.NewRequest("DELETE", "/", nil))
	fake.await(t, "/v1/me")
	if _, err := os.Stat(g.configPath()); !os.IsNotExist(err) {
		t.Fatalf("still kept: %v", err)
	}
}
