package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"dv/internal/notify"
)

func unb64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := b64.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The example of RFC 8291, section 5 and appendix A.
func TestRFC8291Example(t *testing.T) {
	asPrivate, err := ecdh.P256().NewPrivateKey(unb64(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	asPublic := unb64(t, "BP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27mlmlMoZIIg Dll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A8")
	if !bytes.Equal(asPrivate.PublicKey().Bytes(), asPublic) {
		t.Fatal("the application server's keys do not match")
	}
	uaPublic := unb64(t, "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcx aOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4")
	auth := unb64(t, "BTBZMqHH6r4Tts7J_aSIgg")
	salt := unb64(t, "DGv6ra1nlYgDCS1FRnbzlw")

	got, err := seal([]byte("When I grow up, I want to be a watermelon"), uaPublic, auth, asPrivate, salt)
	if err != nil {
		t.Fatal(err)
	}
	want := `DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml
		mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT
		pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN`
	if b64.EncodeToString(got) != strings.Join(strings.Fields(want), "") {
		t.Fatalf("sealed as\n%s", b64.EncodeToString(got))
	}
}

// device is a browser subscribed: its keys, and what it reads of a message.
type device struct {
	key  *ecdh.PrivateKey
	auth []byte
}

func newDevice(t *testing.T) device {
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	rand.Read(auth)
	return device{key, auth}
}

func (d device) keys() (string, string) {
	return b64.EncodeToString(d.key.PublicKey().Bytes()), b64.EncodeToString(d.auth)
}

// read opens a message as RFC 8291 has the device do.
func (d device) read(t *testing.T, body []byte) []byte {
	t.Helper()
	if len(body) < 21 || len(body) < 21+int(body[20]) || binary.BigEndian.Uint32(body[16:]) != recordSize {
		t.Fatalf("no aes128gcm header: %x", body)
	}
	salt, asPublic := body[:16], body[21:21+int(body[20])]
	as, err := ecdh.P256().NewPublicKey(asPublic)
	if err != nil {
		t.Fatal(err)
	}
	shared, _ := d.key.ECDH(as)
	ikm, _ := hkdf.Key(sha256.New, shared, d.auth, "WebPush: info\x00"+string(d.key.PublicKey().Bytes())+string(asPublic), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, body[21+len(asPublic):], nil)
	if err != nil || len(plain) == 0 || plain[len(plain)-1] != 2 {
		t.Fatalf("could not open it: %v", err)
	}
	return plain[:len(plain)-1]
}

// verify checks a VAPID header against the key in it, returning its claims.
func verify(t *testing.T, header string) (claims map[string]any, key string) {
	t.Helper()
	jwt, key, ok := strings.Cut(strings.TrimPrefix(header, "vapid t="), ", k=")
	if !ok || !strings.HasPrefix(header, "vapid t=") {
		t.Fatalf("not a VAPID header: %q", header)
	}
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("not a JWT: %q", jwt)
	}
	if h := string(unb64(t, parts[0])); h != `{"typ":"JWT","alg":"ES256"}` {
		t.Fatalf("header %s", h)
	}
	pub, sig := unb64(t, key), unb64(t, parts[2])
	if len(pub) != 65 || pub[0] != 4 || len(sig) != 64 {
		t.Fatalf("key %d bytes, signature %d", len(pub), len(sig))
	}
	k := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(pub[1:33]), Y: new(big.Int).SetBytes(pub[33:])}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(k, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("the signature does not verify")
	}
	json.Unmarshal(unb64(t, parts[1]), &claims)
	return claims, key
}

func TestVAPID(t *testing.T) {
	dir, keyDir := t.TempDir(), t.TempDir()
	p := New("http://127.0.0.1:1", dir, keyDir)
	if _, err := p.key(false); err == nil {
		t.Fatal("a key before one was asked for")
	}
	v, err := p.key(true)
	if err != nil {
		t.Fatal(err)
	}
	header, err := p.authorization(v, "https://fcm.googleapis.com/fcm/send/abc:def")
	if err != nil {
		t.Fatal(err)
	}
	claims, key := verify(t, header)
	if key != v.public || claims["aud"] != "https://fcm.googleapis.com" || claims["sub"] != subject {
		t.Fatalf("claims %v, key %s", claims, key)
	}
	if exp := time.Unix(int64(claims["exp"].(float64)), 0); exp.Before(time.Now().Add(11*time.Hour)) || exp.After(time.Now().Add(tokenLife)) {
		t.Fatalf("runs out at %v", exp)
	}
	if again, _ := p.authorization(v, "https://fcm.googleapis.com/other"); again != header {
		t.Fatal("a push service's token was made again")
	}

	// Kept sealed, for the user alone, and the same key to another dv.
	b, _ := os.ReadFile(p.path())
	if fi, _ := os.Stat(p.path()); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("kept as %v", fi.Mode())
	}
	if !strings.Contains(string(b), `"key"`) {
		t.Fatalf("kept as\n%s", b)
	}
	if other, err := New("http://127.0.0.1:2", dir, keyDir).key(false); err != nil || other.public != v.public {
		t.Fatalf("another dv's key %v, %v", other, err)
	}

	// One that will not open here is made again, its devices lost with it.
	p.add(Device{Endpoint: "https://push.example/1", DV: p.self})
	elsewhere := New("http://127.0.0.1:1", dir, t.TempDir())
	if _, err := elsewhere.key(false); err == nil {
		t.Fatal("opened with another computer's key")
	}
	if fresh, err := elsewhere.key(true); err != nil || fresh.public == v.public || len(elsewhere.devices()) != 0 {
		t.Fatalf("made again: %v, %v, %d devices", fresh, err, len(elsewhere.devices()))
	}
}

func TestDevices(t *testing.T) {
	p := New("http://127.0.0.1:1", t.TempDir(), t.TempDir())
	other := New("http://127.0.0.1:2", p.dir, p.keyDir)
	p.add(Device{Endpoint: "https://push.example/a", Name: "phone", DV: p.self})
	p.add(Device{Endpoint: "https://push.example/b", Name: "laptop", DV: p.self})
	other.add(Device{Endpoint: "https://push.example/c", Name: "tablet", DV: other.self})
	if ds := p.devices(); len(ds) != 2 || len(other.devices()) != 1 {
		t.Fatalf("devices %v; the other dv's %v", ds, other.devices())
	}
	// Subscribed again, through the other dv, it is the other's.
	other.add(Device{Endpoint: "https://push.example/a", Name: "phone again", DV: other.self})
	if d, ok := other.device("https://push.example/a"); !ok || d.Name != "phone again" || len(p.devices()) != 1 {
		t.Fatalf("replaced: %v; this dv's %v", d, p.devices())
	}
	p.forget("https://push.example/b")
	if len(p.devices()) != 0 || len(other.devices()) != 2 {
		t.Fatalf("after forgetting: %v, %v", p.devices(), other.devices())
	}
}

func TestMessage(t *testing.T) {
	n := notify.Notice{
		ID: "n1", Kind: notify.Done, Folder: "alpha", Session: "s 1", Title: "Claude finished", Where: "Fix the tests",
		Body: "## Done\n\nThe **loop** in `main.go`\nwas off by one.\n\n```go\nfor i := 0; i <= n; i++ {\n```", Format: notify.Markdown,
	}
	m := messageOf(n)
	if m.URL != "/alpha/?session=s+1" || m.Body != "Fix the tests\nDone The loop in main.go was off by one. for i := 0; i <= n; i++ {" {
		t.Fatalf("%+v", m)
	}
	n.Folder, n.Body = "", strings.Repeat("long ", 1000)
	if m := messageOf(n); m.URL != "/?session=s+1" || len(m.Body) > 400 || !strings.HasSuffix(m.Body, "…") {
		t.Fatalf("%+v", m)
	}
	// A command action's opens on its output.
	cmd := notify.Notice{ID: "n2", Kind: notify.Done, Folder: "alpha", Action: "build", Title: "Build failed (exit 1)"}
	if m := messageOf(cmd); m.URL != "/alpha/?output=build" || m.Action != "build" {
		t.Fatalf("%+v", m)
	}
}

// pushed is a message as a push service took it.
type pushed struct {
	path   string
	header http.Header
	body   []byte
}

// fakeService is a push service: it answers each device's path with
// answers', 201 otherwise.
func fakeService(t *testing.T, answers map[string]int) (*httptest.Server, chan pushed) {
	got := make(chan pushed, 10)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- pushed{r.URL.Path, r.Header, b}
		if code := answers[r.URL.Path]; code != 0 {
			w.WriteHeader(code)
			return
		}
		w.WriteHeader(http.StatusCreated)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func await(t *testing.T, got chan pushed) pushed {
	t.Helper()
	select {
	case m := <-got:
		return m
	case <-time.After(3 * time.Second):
		t.Fatal("nothing pushed")
	}
	return pushed{}
}

func TestPush(t *testing.T) {
	srv, got := fakeService(t, map[string]int{"/gone": http.StatusGone})
	p := New("http://127.0.0.1:1", t.TempDir(), t.TempDir())
	p.http = srv.Client()
	v, _ := p.key(true)
	phone, old := newDevice(t), newDevice(t)
	for path, d := range map[string]device{"/phone": phone, "/gone": old} {
		ua, auth := d.keys()
		p.add(Device{Endpoint: srv.URL + path, P256dh: ua, Auth: auth, DV: p.self})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	p.Send(notify.Notice{ID: "a1", Kind: notify.Ask, Folder: "alpha", Session: "s1", Title: "Claude wants to run a command", Where: "Fix the tests", Body: "npm test"})
	for range 2 {
		m := await(t, got)
		if m.path == "/gone" {
			continue
		}
		for name, want := range map[string]string{"Content-Encoding": "aes128gcm", "Content-Type": "application/octet-stream", "TTL": "86400", "Urgency": "high"} {
			if m.header.Get(name) != want {
				t.Errorf("%s: %q", name, m.header.Get(name))
			}
		}
		if topic := m.header.Get("Topic"); len(topic) == 0 || len(topic) > 32 || strings.Trim(topic, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_") != "" {
			t.Errorf("Topic: %q", topic)
		}
		if claims, key := verify(t, m.header.Get("Authorization")); key != v.public || claims["aud"] != srv.URL {
			t.Errorf("signed for %v with %s", claims["aud"], key)
		}
		want := `{"id":"a1","kind":"ask","title":"Claude wants to run a command","body":"Fix the tests\nnpm test","url":"/alpha/?session=s1","folder":"alpha","session":"s1"}`
		if read := phone.read(t, m.body); string(read) != want {
			t.Errorf("the phone read\n%s", read)
		}
	}
	// The 410's subscription is dropped; the phone's kept.
	for until := time.Now().Add(3 * time.Second); len(p.devices()) != 1; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(until) {
			t.Fatalf("devices: %v", p.devices())
		}
	}
	if _, ok := p.device(srv.URL + "/phone"); !ok {
		t.Fatal("the phone was dropped")
	}

	// A turn's end keeps an hour, and wakes a phone asleep as well.
	p.Send(notify.Notice{ID: "d1", Kind: notify.Done, Session: "s1", Title: "Claude finished"})
	if m := await(t, got); m.header.Get("TTL") != "3600" || m.header.Get("Urgency") != "high" {
		t.Fatalf("TTL %s, Urgency %s", m.header.Get("TTL"), m.header.Get("Urgency"))
	}
}

func TestAPI(t *testing.T) {
	push, got := fakeService(t, nil)
	p := New("http://127.0.0.1:1", t.TempDir(), t.TempDir())
	p.http = push.Client()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/notify/push", p.HandleStatus)
	mux.HandleFunc("POST /api/notify/push", p.HandleSubscribe)
	mux.HandleFunc("DELETE /api/notify/push", p.HandleUnsubscribe)
	mux.HandleFunc("POST /api/notify/push/test", p.HandleTest)
	dv := httptest.NewServer(mux)
	defer dv.Close()
	call := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		req, _ := http.NewRequest(method, dv.URL+path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	endpoint := push.URL + "/phone"
	asked := "/api/notify/push?endpoint=" + url.QueryEscape(endpoint)
	code, st := call("GET", asked, "")
	if code != 200 || len(unb64(t, st["key"].(string))) != 65 || st["devices"] != 0.0 || st["subscribed"] != false {
		t.Fatalf("status: %d %v", code, st)
	}
	phone := newDevice(t)
	ua, auth := phone.keys()
	sub := func(endpoint, ua, auth string) string {
		b, _ := json.Marshal(map[string]any{"endpoint": endpoint, "keys": map[string]string{"p256dh": ua, "auth": auth}, "name": "Pixel"})
		return string(b)
	}
	for _, bad := range []string{sub("http://push.example/1", ua, auth), sub(endpoint, ua[:20], auth), sub(endpoint, ua, auth+"AAAA")} {
		if code, out := call("POST", "/api/notify/push", bad); code != 400 {
			t.Fatalf("took %s: %d %v", bad, code, out)
		}
	}
	if code, out := call("POST", "/api/notify/push", sub(endpoint, ua, auth)); code != 204 {
		t.Fatalf("subscribe: %d %v", code, out)
	}
	if code, st := call("GET", asked, ""); code != 200 || st["devices"] != 1.0 || st["subscribed"] != true {
		t.Fatalf("subscribed: %d %v", code, st)
	}

	if code, out := call("POST", "/api/notify/push/test", `{"endpoint":"`+endpoint+`"}`); code != 204 {
		t.Fatalf("test: %d %v", code, out)
	}
	want := `{"id":"dv-test","kind":"done","title":"dv","body":"Notifications reach this device.","url":"/"}`
	if read := phone.read(t, await(t, got).body); string(read) != want {
		t.Fatalf("the phone read\n%s", read)
	}
	if _, st := call("GET", asked, ""); st["last"] == nil || st["last"].(map[string]any)["title"] != "dv" || st["last"].(map[string]any)["error"] != nil {
		t.Fatalf("last sent: %v", st)
	}

	if code, _ := call("DELETE", "/api/notify/push", `{"endpoint":"`+endpoint+`"}`); code != 204 {
		t.Fatalf("unsubscribe: %d", code)
	}
	if _, st := call("GET", asked, ""); st["devices"] != 0.0 || st["subscribed"] != false {
		t.Fatalf("after: %v", st)
	}
	if code, _ := call("POST", "/api/notify/push/test", `{"endpoint":"`+endpoint+`"}`); code != 404 {
		t.Fatalf("test, unsubscribed: %d", code)
	}
}
