package telegram

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"dv/internal/notify"
	"dv/internal/notify/chat"
)

// fakeBot is the Bot API, as far as dv uses it: it hands out the updates
// queued, and keeps each call made.
type fakeBot struct {
	updates chan update
	mu      sync.Mutex
	calls   []call
	topics  bool
	gone    map[float64]bool // threads the reader deleted
}

type call struct {
	Method string
	Params map[string]any
	ID     int64 // a message sent's
}

func (f *fakeBot) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if file, ok := strings.CutPrefix(r.URL.Path, "/file/bot123:abc/"); ok {
		fmt.Fprint(w, "bytes of "+file)
		return
	}
	token, method, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/bot"), "/")
	var params map[string]any
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		r.ParseMultipartForm(1 << 20)
		file, _, err := r.FormFile("picture")
		var got []byte
		if err == nil {
			got, _ = io.ReadAll(file)
		}
		params = map[string]any{"photo": r.FormValue("photo"), "picture": string(got)}
	} else {
		json.NewDecoder(r.Body).Decode(&params)
	}
	reply := func(v any) { json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": v}) }
	refuse := func(s string) { json.NewEncoder(w).Encode(map[string]any{"ok": false, "description": s}) }
	if token != "123:abc" {
		w.WriteHeader(http.StatusUnauthorized)
		refuse("Unauthorized")
		return
	}
	if method == "getUpdates" {
		select {
		case u := <-f.updates:
			reply([]update{u})
		case <-time.After(200 * time.Millisecond):
			reply([]update{})
		case <-r.Context().Done():
		}
		return
	}
	thread, _ := params["message_thread_id"].(float64)
	f.mu.Lock()
	n := len(f.calls) + 1
	f.calls = append(f.calls, call{method, params, int64(100 + n)})
	topics, gone := f.topics, f.gone[thread]
	f.mu.Unlock()
	switch {
	case method == "getMe":
		reply(map[string]any{"id": 1, "username": "dv_test_bot", "has_topics_enabled": topics})
	case method == "sendMessage" && gone:
		refuse("Bad Request: message thread not found")
	case method == "sendMessage":
		reply(map[string]any{"message_id": 100 + n, "date": time.Now().Unix()})
	case method == "createForumTopic":
		reply(map[string]any{"message_thread_id": 1000 + n, "name": params["name"]})
	case method == "getFile":
		reply(map[string]any{"file_path": "photos/" + params["file_id"].(string)})
	default:
		reply(true)
	}
}

// await is the first call to method not yet awaited. Calls from the poll and
// from the queue of notices come in either order.
func (f *fakeBot) await(t *testing.T, method string, seen map[int]bool) call {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		for i, c := range f.calls {
			if c.Method == method && !seen[i] {
				seen[i] = true
				f.mu.Unlock()
				return c
			}
		}
		f.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no %s", method)
	return call{}
}

// shown is a message as the reader sees it: its text, and its buttons.
func shown(c call) string {
	var b strings.Builder
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	e.Encode(c.Params["reply_markup"])
	s, _ := c.Params["text"].(string)
	return s + "\n" + b.String()
}

func do(t *testing.T, h http.HandlerFunc, body string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	h(w, httptest.NewRequest("POST", "/", strings.NewReader(body)))
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	if w.Code != 200 {
		t.Fatalf("%d: %v", w.Code, out)
	}
	return out
}

// fakeFolder is a folder's sessions, keeping what was done to them.
type fakeFolder struct {
	mu            sync.Mutex
	done          []string
	at            notify.Progress // every message's
	model, effort string
	started       int
}

func (f *fakeFolder) List() []notify.Session {
	return []notify.Session{{ID: "s1", Title: "Fix <the> tests", Running: "dv", Busy: true, Updated: time.Now()}}
}
func (f *fakeFolder) Send(s string, m notify.Message) (string, error) {
	text := m.Text
	for _, img := range m.Images {
		text += fmt.Sprintf(" [%s %s]", img.Type, img.Data)
	}
	for _, f := range m.Files {
		text += fmt.Sprintf(" {%s %s}", f.Name, f.Data)
	}
	if m.Via != "Telegram" {
		text += " (via " + m.Via + ")"
	}
	f.did(s + ": " + text)
	return "m1", nil
}
func (f *fakeFolder) Start(m notify.Message) (string, error) {
	f.mu.Lock()
	f.started++
	id := fmt.Sprintf("s%d", f.started+1)
	f.mu.Unlock()
	f.did("started: " + m.Text)
	return id, nil
}
func (f *fakeFolder) Stop(s string) error   { f.did(s + " stopped"); return nil }
func (f *fakeFolder) Reply(s string) string { return "All **done**." }

func (f *fakeFolder) Progress(s, message string) notify.Progress {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.at
}

func (f *fakeFolder) Setup(s string) notify.Setup {
	f.mu.Lock()
	defer f.mu.Unlock()
	return notify.Setup{Model: f.model, Effort: f.effort, Models: []notify.Model{
		{ID: "", Label: "Opus 5", Effort: "high", Efforts: []string{"low", "medium", "high", "max"}},
		{ID: "sonnet", Label: "Sonnet", Efforts: []string{"low", "high"}},
	}}
}

func (f *fakeFolder) Configure(s string, model, effort *string, asNew bool) error {
	f.mu.Lock()
	if model != nil {
		f.model, f.effort = *model, ""
	}
	if effort != nil {
		f.effort = *effort
	}
	did := fmt.Sprintf("%s on %q %q, new too: %v", s, f.model, f.effort, asNew)
	f.mu.Unlock()
	f.did(did)
	return nil
}

func (f *fakeFolder) set(at notify.Progress) {
	f.mu.Lock()
	f.at = at
	f.mu.Unlock()
}

func (f *fakeFolder) did(s string) {
	f.mu.Lock()
	f.done = append(f.done, s)
	f.mu.Unlock()
}

func (f *fakeFolder) await(t *testing.T, want string) {
	t.Helper()
	for until := time.Now().Add(3 * time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
		f.mu.Lock()
		ok := slices.Contains(f.done, want)
		f.mu.Unlock()
		if ok {
			return
		}
	}
	t.Fatalf("never %q; did %q", want, f.done)
}

func TestTelegram(t *testing.T) {
	bot := &fakeBot{updates: make(chan update, 4), gone: map[float64]bool{}}
	api := httptest.NewServer(bot)
	defer api.Close()
	center := notify.New(func() time.Duration { return 0 })
	defer center.Close()
	dir, keyDir := t.TempDir(), t.TempDir()
	tg := New(center, "http://127.0.0.1:1", dir, keyDir)
	tg.api.base = api.URL
	center.Add(tg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tg.Run(ctx)
	seen := map[int]bool{}
	folder := center.Folder("alpha", "Alpha")
	sessions := &fakeFolder{}
	folder.Serve(sessions)
	msg := func(id int64, thread int64, text string) {
		m := &message{ID: id, Thread: thread, Text: text, Date: time.Now().Unix(), From: &user{ID: 42}}
		m.Chat.ID, m.Chat.Type = 42, "private"
		bot.updates <- update{ID: id, Message: m}
	}

	// Setting up: a bot without topics is turned away.
	w := httptest.NewRecorder()
	tg.HandleSetUp(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"token": "123:abc"}`)))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "no topics") || tg.config().Token != "" {
		t.Fatalf("a bot without topics: %d %s", w.Code, w.Body)
	}
	bot.mu.Lock()
	bot.topics = true
	bot.mu.Unlock()
	st := do(t, tg.HandleSetUp, `{"token": " 123:abc ", "origin": "https://dv.example.com"}`)
	link, _ := st["connect"].(string)
	code, ok := strings.CutPrefix(link, "https://t.me/dv_test_bot?start=")
	if !ok || code == "" || strings.Contains(link+st["bot"].(string), "abc") {
		t.Fatalf("set up: %v", st)
	}
	for _, path := range []string{tg.configPath(), tg.keyPath()} {
		if fi, err := os.Stat(path); err != nil || runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
			t.Fatalf("%s: %v, %v", path, fi.Mode(), err)
		}
	}
	if kept, _ := os.ReadFile(tg.configPath()); strings.Contains(string(kept), "123:abc") || !strings.Contains(string(kept), `"sealed"`) {
		t.Fatalf("the token is kept as it is:\n%s", kept)
	}
	if again := New(center, "", dir, keyDir); again.config().Token != "123:abc" {
		t.Fatal("the token did not open with this computer's key")
	}
	elsewhere := New(center, "", dir, t.TempDir())
	if st := elsewhere.status(context.Background()); elsewhere.config().Token != "" || !st.Lost {
		t.Fatalf("with another computer's key: %+v", st)
	}
	w = httptest.NewRecorder()
	tg.HandleSetUp(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"token": "999:nope"}`)))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "Unauthorized") || tg.config().Token != "123:abc" {
		t.Fatalf("a token Telegram refuses: %d %s", w.Code, w.Body)
	}
	if c := bot.await(t, "setMyCommands", seen); len(c.Params["commands"].([]any)) != len(chat.Commands) {
		t.Fatalf("commands: %v", c.Params)
	}

	stranger := &message{Text: "/start nope", From: &user{ID: 7, FirstName: "Eve"}, Date: time.Now().Unix()}
	stranger.Chat.ID, stranger.Chat.Type = 7, "private"
	bot.updates <- update{ID: 1, Message: stranger}
	reader := &message{Text: "/start " + code, From: &user{ID: 42, FirstName: "Ada"}, Date: time.Now().Unix()}
	reader.Chat.ID, reader.Chat.Type = 42, "private"
	bot.updates <- update{ID: 2, Message: reader}
	if c := bot.await(t, "sendMessage", seen); c.Params["chat_id"] != 42.0 || !strings.HasPrefix(c.Params["text"].(string), "Connected") {
		t.Fatalf("on connecting: %v", c.Params)
	}
	if cfg := tg.config(); cfg.Chat != 42 || cfg.Name != "Ada" || cfg.Code != "" {
		t.Fatalf("connected as %+v", cfg)
	}
	if c := bot.await(t, "setChatMenuButton", seen); !strings.Contains(fmt.Sprint(c.Params), "url:https://dv.example.com/") {
		t.Fatalf("the menu button: %v", c.Params)
	}

	// A notice goes in its session's thread, made for it.
	answered := make(chan int, 1)
	folder.Raise(notify.Notice{
		ID: "a1", Kind: notify.Ask, Session: "s1", Title: "Claude wants to run a command", Where: "Fix <the> tests",
		Body: "npm test", Format: notify.Code, Choices: []string{"Yes", "No"},
		Answer: func(i int) error { answered <- i; return nil },
	})
	made := bot.await(t, "createForumTopic", seen)
	if made.Params["name"] != "Fix <the> tests · Alpha" {
		t.Fatalf("the thread: %v", made.Params)
	}
	sent := bot.await(t, "sendMessage", seen)
	thread := sent.Params["message_thread_id"].(float64)
	for _, want := range []string{`<b>Claude wants to run a command</b>`, `<pre>npm test</pre>`, `"callback_data":"a1:1"`, `"url":"https://dv.example.com/alpha/?session=s1"`} {
		if !strings.Contains(shown(sent), want) {
			t.Fatalf("the notice has no %s: %s", want, shown(sent))
		}
	}
	if strings.Contains(shown(sent), "from session") {
		t.Fatalf("in its own thread, the notice says whose it is: %s", shown(sent))
	}

	bot.updates <- update{ID: 3, Callback: &callback{ID: "q0", From: user{ID: 7}, Data: "a1:0"}}
	bot.updates <- update{ID: 4, Callback: &callback{ID: "q1", From: user{ID: 42}, Data: "a1:1"}}
	if i := <-answered; i != 1 {
		t.Fatalf("answered %d", i)
	}
	if c := bot.await(t, "answerCallbackQuery", seen); c.Params["callback_query_id"] != "q0" || c.Params["text"] != nil {
		t.Fatalf("a stranger's tap: %v", c.Params)
	}
	edit := bot.await(t, "editMessageText", seen)
	if s := shown(edit); !strings.Contains(s, "Answered from Telegram: No") || strings.Contains(s, "callback_data") || !strings.Contains(s, "Open in dv") {
		t.Fatalf("the notice once answered: %s", s)
	}
	if c := bot.await(t, "answerCallbackQuery", seen); c.Params["text"] != "No" {
		t.Fatalf("the tap answered: %v", c.Params)
	}
	// Tapped again, it is gone: its buttons go too.
	bot.updates <- update{ID: 5, Callback: &callback{ID: "q2", From: user{ID: 42}, Data: "a1:0", Message: &message{ID: 101}}}
	bot.await(t, "editMessageReplyMarkup", seen)
	if c := bot.await(t, "answerCallbackQuery", seen); c.Params["text"] != notify.ErrGone.Error() {
		t.Fatalf("a tap on one gone: %v", c.Params)
	}

	// Written in the thread, words go to its session, and so does any command
	// not the bot's; /stop and /last are the session's.
	msg(6, int64(thread), "run the linter")
	sessions.await(t, "s1: run the linter")
	bot.await(t, "setMessageReaction", seen)
	msg(7, int64(thread), "/compact")
	sessions.await(t, "s1: /compact")
	bot.await(t, "setMessageReaction", seen)
	msg(8, int64(thread), "/stop")
	sessions.await(t, "s1 stopped")
	bot.await(t, "setMessageReaction", seen)
	msg(9, int64(thread), "/last@dv_test_bot")
	if c := bot.await(t, "sendMessage", seen); c.Params["message_thread_id"] != thread || !strings.Contains(shown(c), "All <b>done</b>.") {
		t.Fatalf("/last: %v", c.Params)
	}

	// Pictures go with their words, those of an album as one message; a
	// photo in its largest size an agent takes.
	picture := func(id int64, album, caption string, photo []photoSize, doc *document) {
		m := &message{ID: id, Thread: int64(thread), Caption: caption, Album: album, Photo: photo, Document: doc, Date: time.Now().Unix()}
		m.Chat.ID = 42
		bot.updates <- update{ID: id, Message: m}
	}
	picture(20, "", "what is this?", []photoSize{{"small", 10}, {"large", 900}, {"huge", maxPicture + 1}}, nil)
	sessions.await(t, "s1: what is this? [image/jpeg bytes of photos/large]")
	bot.await(t, "setMessageReaction", seen)
	picture(21, "g1", "", []photoSize{{"p1", 10}}, nil)
	picture(22, "g1", "compare these", nil, &document{FileID: "p2", MimeType: "image/png", Size: 10})
	sessions.await(t, "s1: compare these [image/jpeg bytes of photos/p1] [image/png bytes of photos/p2]")
	if c := bot.await(t, "setMessageReaction", seen); c.Params["message_id"] != 21.0 {
		t.Fatalf("the album's reaction: %v", c.Params)
	}
	// Anything else goes as a file: a picture too big to send as one, too.
	picture(23, "", "", nil, &document{FileID: "big", MimeType: "image/png", Size: maxPicture + 1, Name: "shot.png"})
	sessions.await(t, "s1:  {shot.png bytes of photos/big}")
	bot.await(t, "setMessageReaction", seen)
	picture(24, "", "read this", nil, &document{FileID: "notes", MimeType: "application/pdf", Size: 10, Name: "notes.pdf"})
	sessions.await(t, "s1: read this {notes.pdf bytes of photos/notes}")
	bot.await(t, "setMessageReaction", seen)
	voice := &message{ID: 25, Thread: int64(thread), Voice: &document{FileID: "v1", MimeType: "audio/ogg", Size: 10}, Date: time.Now().Unix()}
	voice.Chat.ID = 42
	bot.updates <- update{ID: 25, Message: voice}
	sessions.await(t, "s1:  {voice.ogg bytes of photos/v1}")
	bot.await(t, "setMessageReaction", seen)
	picture(26, "", "", nil, &document{FileID: "huge", MimeType: "application/zip", Size: maxFetch + 1, Name: "all.zip"})
	if c := bot.await(t, "sendMessage", seen); !strings.HasPrefix(shown(c), "all.zip is over the 20 MB") {
		t.Fatalf("a file too big to fetch: %v", c.Params)
	}
	// Telegram's own note of a topic made, which has no words, is no message
	// to answer; words outside a session's thread are.
	msg(10, 777, "")
	msg(10, 0, "hello")
	sessions.await(t, "started: hello")
	if c := bot.await(t, "createForumTopic", seen); c.Params["name"] != "hello · Alpha" {
		t.Fatalf("words outside a thread start a session, in a thread of its own: %v", c.Params)
	}
	if c := bot.await(t, "sendMessage", seen); !strings.HasPrefix(c.Params["text"].(string), "<b>Started</b> in Alpha: hello") {
		t.Fatalf("in its thread: %v", c.Params)
	}
	if c := bot.await(t, "sendMessage", seen); c.Params["text"] != "Started, in its thread hello · Alpha." {
		t.Fatalf("where it was asked for: %v", c.Params)
	}

	// /sessions offers them; picked, one's thread has what it said last.
	msg(11, 0, "/sessions")
	list := bot.await(t, "sendMessage", seen)
	if !strings.Contains(shown(list), "⏳ Fix <the> tests · Alpha") {
		t.Fatalf("/sessions: %s", shown(list))
	}
	data := list.Params["reply_markup"].(map[string]any)["inline_keyboard"].([]any)[0].([]any)[0].(map[string]any)["callback_data"].(string)
	bot.updates <- update{ID: 12, Callback: &callback{ID: "q3", From: user{ID: 42}, Data: data}}
	if c := bot.await(t, "sendMessage", seen); c.Params["message_thread_id"] != thread || !strings.Contains(shown(c), "Write here to talk to it") {
		t.Fatalf("the picked session's thread: %v", c.Params)
	}
	if c := bot.await(t, "sendMessage", seen); !strings.Contains(shown(c), "All <b>done</b>.") {
		t.Fatalf("what it said last: %v", c.Params)
	}

	// /new starts one in the folder named, in a thread of its own.
	msg(13, 0, "/new alpha do the thing")
	sessions.await(t, "started: do the thing")
	if c := bot.await(t, "createForumTopic", seen); c.Params["name"] != "do the thing · Alpha" {
		t.Fatalf("the new session's thread: %v", c.Params)
	}
	if c := bot.await(t, "sendMessage", seen); !strings.HasPrefix(c.Params["text"].(string), "<b>Started</b>") || c.Params["message_thread_id"] == nil {
		t.Fatalf("in the new thread: %v", c.Params)
	}
	if c := bot.await(t, "sendMessage", seen); c.Params["message_thread_id"] != nil || !strings.Contains(c.Params["text"].(string), "do the thing · Alpha") {
		t.Fatalf("where /new was said: %v", c.Params)
	}

	// A long reply goes over several messages, with no title in its thread and
	// the link on the last; a new title renames the thread.
	long := strings.Repeat("Some words about it.\n\n", 400)
	folder.Raise(notify.Notice{ID: "d1", Kind: notify.Done, Session: "s1", Title: "Claude finished", Where: "Tests fixed", Body: long, Format: notify.Markdown})
	if c := bot.await(t, "editForumTopic", seen); c.Params["name"] != "Tests fixed · Alpha" || c.Params["message_thread_id"] != thread {
		t.Fatalf("renamed: %v", c.Params)
	}
	var parts []call
	for range 3 {
		parts = append(parts, bot.await(t, "sendMessage", seen))
	}
	if !strings.HasPrefix(shown(parts[0]), "Some words") || strings.Contains(shown(parts[0]), "Open in dv") || !strings.Contains(shown(parts[2]), "Open in dv") {
		t.Fatalf("the reply's parts: %v", parts)
	}

	// A thread the reader deleted is made again.
	bot.mu.Lock()
	bot.gone[thread] = true
	bot.mu.Unlock()
	folder.Raise(notify.Notice{ID: "d2", Kind: notify.Done, Session: "s1", Title: "Claude finished", Where: "Tests fixed", Body: "Again."})
	bot.await(t, "sendMessage", seen)
	bot.await(t, "createForumTopic", seen)
	if c := bot.await(t, "sendMessage", seen); c.Params["message_thread_id"] == thread || !strings.Contains(shown(c), "Again.") {
		t.Fatalf("after the thread was deleted: %v", c.Params)
	}
	// Only the latest reply keeps the link.
	if c := bot.await(t, "editMessageReplyMarkup", seen); c.Params["message_id"] != float64(parts[2].ID) {
		t.Fatalf("the reply before, still linked: %v", c.Params)
	}

	do(t, tg.HandleTest, `{"origin": "http://127.0.0.1:41000"}`)
	test := bot.await(t, "sendMessage", seen)
	if _, linked := test.Params["reply_markup"]; linked || !strings.Contains(test.Params["text"].(string), "can reach you here") {
		t.Fatalf("the test, from this computer's own address: %v", test.Params)
	}

	jpeg := base64.StdEncoding.EncodeToString([]byte("\xff\xd8\xff\xe0 a picture"))
	do(t, tg.HandlePicture, `{"photo": "`+jpeg+`"}`)
	if c := bot.await(t, "setMyProfilePhoto", seen); c.Params["photo"] != `{"type":"static","photo":"attach://picture"}` || c.Params["picture"] != "\xff\xd8\xff\xe0 a picture" {
		t.Fatalf("the picture: %q", c.Params)
	}
	w = httptest.NewRecorder()
	tg.HandlePicture(w, httptest.NewRequest("POST", "/", strings.NewReader(`{"photo": "`+base64.StdEncoding.EncodeToString([]byte("<svg/>"))+`"}`)))
	if w.Code != 400 {
		t.Fatalf("a picture not a JPEG: %d %s", w.Code, w.Body)
	}

	do(t, tg.HandleRemove, `{}`)
	if st := do(t, tg.HandleStatus, ``); len(st) != 1 || st["sending"] == nil {
		t.Fatalf("after removing: %v", st)
	}
}

// fakeHub makes worktrees at once, and opens the folders asked for.
type fakeHub struct {
	center *notify.Center
	folder *fakeFolder
	made   chan string
}

func (h *fakeHub) Places() []notify.Place {
	return []notify.Place{{Slug: "alpha", Name: "Alpha", Git: true}, {Slug: "notes", Name: "notes"}}
}

func (h *fakeHub) Open(slug string) error {
	h.center.Folder(slug, slug).Serve(h.folder)
	return nil
}

func (h *fakeHub) Worktree(slug, branch string, fresh bool) (notify.Place, error) {
	h.made <- fmt.Sprintf("%s %s %v", slug, branch, fresh)
	return notify.Place{Slug: "alpha-" + branch, Name: "alpha-" + branch, Git: true}, nil
}

// connected is a provider already set up and connected, sending, to chat 42.
func connected(t *testing.T) (*Telegram, *fakeBot, *notify.Center, func(id, thread int64, text string, reply *message)) {
	bot := &fakeBot{updates: make(chan update, 4), gone: map[float64]bool{}, topics: true}
	api := httptest.NewServer(bot)
	t.Cleanup(api.Close)
	center := notify.New(func() time.Duration { return 0 })
	t.Cleanup(center.Close)
	tg := New(center, "http://127.0.0.1:1", t.TempDir(), t.TempDir())
	tg.api.base = api.URL
	tg.conv.Every = 20 * time.Millisecond
	tg.change(func(c *Config) { *c = Config{Token: "123:abc", Bot: "dv_test_bot", Chat: 42} })
	center.Add(tg)
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{})
	go func() {
		tg.Run(ctx)
		close(ran)
	}()
	t.Cleanup(func() {
		cancel()
		<-ran // done with its folder, which goes next
	})
	msg := func(id, thread int64, text string, reply *message) {
		m := &message{ID: id, Thread: thread, Text: text, Date: time.Now().Unix(), From: &user{ID: 42}, ReplyTo: reply}
		m.Chat.ID, m.Chat.Type = 42, "private"
		bot.updates <- update{ID: id, Message: m}
	}
	return tg, bot, center, msg
}

func TestNewInAWorktree(t *testing.T) {
	_, bot, center, msg := connected(t)
	sessions := &fakeFolder{}
	hub := &fakeHub{center: center, folder: sessions, made: make(chan string, 1)}
	center.SetHub(hub)
	seen := map[int]bool{}
	tap := func(id int64, c call, row int) {
		data := c.Params["reply_markup"].(map[string]any)["inline_keyboard"].([]any)[row].([]any)[0].(map[string]any)["callback_data"].(string)
		bot.updates <- update{ID: id, Callback: &callback{ID: "q", From: user{ID: 42}, Data: data}}
	}

	// A folder that is no repository takes no question.
	msg(1, 0, "/new notes jot this down", nil)
	sessions.await(t, "started: jot this down")
	if c := bot.await(t, "createForumTopic", seen); c.Params["name"] != "jot this down · notes" {
		t.Fatalf("its thread: %v", c.Params)
	}

	msg(2, 0, "/new Alpha Fix the login bug, and its tests!", nil)
	where := bot.await(t, "sendMessage", seen)
	for where.Params["text"] != "Start it in Alpha itself, or in a new worktree of it?" {
		where = bot.await(t, "sendMessage", seen)
	}
	tap(3, where, 1)
	ask := bot.await(t, "sendMessage", seen)
	if !strings.Contains(shown(ask), `"text":"fix-the-login-bug-and"`) {
		t.Fatalf("the branch offered: %s", shown(ask))
	}
	tap(4, ask, 0)
	if got := <-hub.made; got != "alpha fix-the-login-bug-and true" {
		t.Fatalf("made %q", got)
	}
	sessions.await(t, "started: Fix the login bug, and its tests!")
	if c := bot.await(t, "createForumTopic", seen); c.Params["name"] != "Fix the login bug, and its tests! · alpha-fix-the-login-bug-and" {
		t.Fatalf("its thread: %v", c.Params)
	}

	// A name of the reader's own, in reply to the question.
	msg(5, 0, "/new alpha tidy up", nil)
	where = bot.await(t, "sendMessage", seen)
	for where.Params["text"] != "Start it in Alpha itself, or in a new worktree of it?" {
		where = bot.await(t, "sendMessage", seen)
	}
	tap(6, where, 1)
	ask = bot.await(t, "sendMessage", seen)
	for !strings.HasPrefix(ask.Params["text"].(string), "Name the new worktree") {
		ask = bot.await(t, "sendMessage", seen)
	}
	msg(7, 0, "chore/tidy", &message{ID: ask.ID})
	if got := <-hub.made; got != "alpha chore/tidy false" {
		t.Fatalf("made %q", got)
	}
	// Started once its thread is kept, which is written to the test's folder.
	for c := bot.await(t, "sendMessage", seen); !strings.HasPrefix(c.Params["text"].(string), "Started, in its thread"); c = bot.await(t, "sendMessage", seen) {
	}
}

// Words outside a session's thread start one, where their first words say or
// else where Settings do, in the thread Telegram made for them.
func TestQuickStart(t *testing.T) {
	_, bot, center, msg := connected(t)
	sessions := &fakeFolder{}
	hub := &fakeHub{center: center, folder: sessions, made: make(chan string, 1)}
	center.SetHub(hub)
	var mu sync.Mutex
	var defaults notify.Defaults
	center.SetDefaults(func() notify.Defaults {
		mu.Lock()
		defer mu.Unlock()
		return defaults
	})
	set := func(d notify.Defaults) {
		mu.Lock()
		defaults = d
		mu.Unlock()
	}
	seen := map[int]bool{}
	started := func() string {
		t.Helper()
		for {
			if c := bot.await(t, "sendMessage", seen); strings.HasPrefix(c.Params["text"].(string), "<b>Started</b>") {
				return fmt.Sprint(c.Params["message_thread_id"], " ", c.Params["text"])
			}
		}
	}

	// No folder set, it asks which.
	msg(1, 900, "fix the tests", nil)
	ask := bot.await(t, "sendMessage", seen)
	if !strings.HasPrefix(ask.Params["text"].(string), "Start it in which folder?") || ask.Params["message_thread_id"] != 900.0 {
		t.Fatalf("with no folder set: %v", ask.Params)
	}
	data := ask.Params["reply_markup"].(map[string]any)["inline_keyboard"].([]any)[0].([]any)[0].(map[string]any)["callback_data"].(string)
	bot.updates <- update{ID: 2, Callback: &callback{ID: "q", From: user{ID: 42}, Data: data, Message: &message{ID: ask.ID, Thread: 900}}}
	sessions.await(t, "started: fix the tests")
	// The question keeps its answer, and takes no other.
	if c := bot.await(t, "editMessageText", seen); c.Params["message_id"] != float64(ask.ID) || !strings.HasSuffix(shown(c), "\n✓ Alpha\n"+`{"inline_keyboard":[]}`+"\n") {
		t.Fatalf("the question answered: %q", shown(c))
	}
	bot.await(t, "answerCallbackQuery", seen)
	bot.updates <- update{ID: 20, Callback: &callback{ID: "q", From: user{ID: 42}, Data: data, Message: &message{ID: ask.ID, Thread: 900}}}
	if c := bot.await(t, "answerCallbackQuery", seen); c.Params["text"] != "That is answered already." {
		t.Fatalf("tapped again: %v", c.Params)
	}
	sessions.mu.Lock()
	if n := len(slices.DeleteFunc(slices.Clone(sessions.done), func(s string) bool { return s != "started: fix the tests" })); n != 1 {
		t.Fatalf("started %d times", n)
	}
	sessions.mu.Unlock()
	if c := bot.await(t, "editForumTopic", seen); c.Params["message_thread_id"] != 900.0 || c.Params["name"] != "fix the tests · Alpha" {
		t.Fatalf("the reader's thread, named for the session: %v", c.Params)
	}
	if got := started(); !strings.HasPrefix(got, "900 <b>Started</b> in Alpha.\n") {
		t.Fatalf("started: %q", got)
	}

	set(notify.Defaults{Folder: "alpha", Worktree: true})
	msg(3, 901, "Fix the login", nil)
	if got := <-hub.made; got != "alpha fix-the-login true" {
		t.Fatalf("made %q", got)
	}
	if got := started(); !strings.HasPrefix(got, "901 <b>Started</b> in alpha-fix-the-login, a new worktree of Alpha on the branch <code>fix-the-login</code>.") {
		t.Fatalf("started: %q", got)
	}

	// Words before a colon say otherwise.
	msg(4, 902, "notes: jot this down", nil)
	sessions.await(t, "started: jot this down")
	if got := started(); !strings.HasPrefix(got, "902 <b>Started</b> in notes itself: it is no git repository") {
		t.Fatalf("started: %q", got)
	}
	msg(5, 903, "alpha here: tidy up", nil)
	sessions.await(t, "started: tidy up")
	if got := started(); !strings.HasPrefix(got, "903 <b>Started</b> in Alpha.\n") {
		t.Fatalf("started: %q", got)
	}
	msg(6, 904, "wt fix/login: do it", nil)
	if got := <-hub.made; got != "alpha fix/login false" {
		t.Fatalf("made %q", got)
	}
	started()
	msg(7, 905, "Note: the build is red", nil)
	if got := <-hub.made; got != "alpha note-the-build-is-red true" {
		t.Fatalf("made %q", got)
	}
	sessions.await(t, "started: Note: the build is red")
	started()

	msg(8, 906, "/compact", nil)
	if c := bot.await(t, "sendMessage", seen); !strings.HasPrefix(c.Params["text"].(string), "That is for a session&#39;s thread") {
		t.Fatalf("a command outside a session's thread: %v", c.Params)
	}
	bot.mu.Lock()
	defer bot.mu.Unlock()
	for _, c := range bot.calls {
		if c.Method == "createForumTopic" {
			t.Fatalf("made a thread where the reader's was taken: %v", c.Params)
		}
	}
}

func TestProgressAndModels(t *testing.T) {
	tg, bot, center, msg := connected(t)
	sessions := &fakeFolder{}
	center.Folder("alpha", "Alpha").Serve(sessions)
	tg.change(func(c *Config) {
		c.Threads = map[string]Thread{"alpha/s1": {ID: 500, Name: "Fix · Alpha"}}
	})
	seen := map[int]bool{}
	reaction := func() string { return fmt.Sprint(bot.await(t, "setMessageReaction", seen).Params["reaction"]) }
	count := func(method string) (n int) {
		bot.mu.Lock()
		defer bot.mu.Unlock()
		for _, c := range bot.calls {
			if c.Method == method {
				n++
			}
		}
		return n
	}
	taps := int64(100)
	press := func(c call, label string) {
		t.Helper()
		id := c.ID
		if v, ok := c.Params["message_id"].(float64); ok {
			id = int64(v)
		}
		for _, row := range c.Params["reply_markup"].(map[string]any)["inline_keyboard"].([]any) {
			for _, b := range row.([]any) {
				if b := b.(map[string]any); strings.TrimPrefix(b["text"].(string), "✓ ") == label {
					taps++
					bot.updates <- update{ID: taps, Callback: &callback{ID: "q", From: user{ID: 42}, Data: b["callback_data"].(string), Message: &message{ID: id}}}
					return
				}
			}
		}
		t.Fatalf("no %q on %s", label, shown(c))
	}

	// 👀 while a message waits, 👨‍💻 once taken up, the bot typing in the
	// thread meanwhile but for while the agent asks; none once it is done.
	msg(1, 500, "run the linter", nil)
	sessions.await(t, "s1: run the linter")
	if r := reaction(); !strings.Contains(r, "👀") {
		t.Fatalf("queued: %s", r)
	}
	sessions.set(notify.Working)
	if r := reaction(); !strings.Contains(r, "👨‍💻") {
		t.Fatalf("taken up: %s", r)
	}
	if c := bot.await(t, "sendChatAction", seen); c.Params["message_thread_id"] != 500.0 || c.Params["action"] != "typing" {
		t.Fatalf("typing: %v", c.Params)
	}
	sessions.set(notify.Asking)
	time.Sleep(60 * time.Millisecond)
	typed := count("sendChatAction")
	time.Sleep(100 * time.Millisecond)
	if count("sendChatAction") != typed || count("setMessageReaction") != 2 {
		t.Fatal("typing, or reacting again, while the agent asks")
	}
	sessions.set(notify.Finished)
	if r := reaction(); r != "[]" {
		t.Fatalf("done: %s", r)
	}

	// /model has the session's, the picker open: a tap picks for it alone.
	msg(2, 500, "/model", nil)
	c := bot.await(t, "sendMessage", seen)
	if s := shown(c); !strings.Contains(s, "On <b>Claude · Opus 5 · high effort</b>") || !strings.Contains(s, "✓ Opus 5") || !strings.Contains(s, "✓ high") {
		t.Fatalf("/model: %s", s)
	}
	press(c, "Sonnet")
	sessions.await(t, `s1 on "sonnet" "", new too: false`)
	c = bot.await(t, "editMessageText", seen)
	if s := shown(c); !strings.Contains(s, "On <b>Claude · Sonnet</b>") || !strings.Contains(s, "✓ Sonnet") || strings.Contains(s, "medium") {
		t.Fatalf("Sonnet picked: %s", s)
	}
	press(c, "high")
	sessions.await(t, `s1 on "sonnet" "high", new too: false`)
	press(bot.await(t, "editMessageText", seen), "Done")
	if s := shown(bot.await(t, "editMessageText", seen)); !strings.Contains(s, "Sonnet · high effort") || !strings.Contains(s, "Change model") || strings.Contains(s, "✓") {
		t.Fatalf("the picker shut: %s", s)
	}

	// The message a session starts with has its model, picked there for the
	// sessions after it too.
	msg(3, 0, "/new alpha do the thing", nil)
	sessions.await(t, "started: do the thing")
	started := bot.await(t, "sendMessage", seen)
	if s := shown(started); !strings.HasPrefix(s, "<b>Started</b>") || !strings.Contains(s, "On <b>Claude · Sonnet · high effort</b>") {
		t.Fatalf("started: %s", s)
	}
	press(started, "Change model")
	opened := bot.await(t, "editMessageText", seen)
	if s := shown(opened); !strings.HasPrefix(s, "<b>Started</b>") || !strings.Contains(s, "✓ Sonnet") {
		t.Fatalf("its picker: %s", s)
	}
	press(opened, "Opus 5")
	sessions.await(t, `s2 on "" "", new too: true`)
}
