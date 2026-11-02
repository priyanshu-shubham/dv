package server

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dv/internal/agent"
)

func TestAMessageSentAgainIsSentOnce(t *testing.T) {
	var s sent
	var sends atomic.Int32
	release := make(chan struct{})
	send := func() (string, error) {
		sends.Add(1)
		<-release
		return "u1", nil
	}
	var wg sync.WaitGroup
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if uuid, err := s.once("s", "u1", send); uuid != "u1" || err != nil {
				t.Errorf("once: %q, %v", uuid, err)
			}
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	s.once("s", "u1", send)
	if n := sends.Load(); n != 1 {
		t.Fatalf("sent %d times", n)
	}
	// Another session's message of the same uuid is its own.
	s.once("other", "u1", send)
	if n := sends.Load(); n != 2 {
		t.Fatalf("sent %d times", n)
	}
}

// A long conversation goes end first, in pieces each going ahead of the last;
// an update to one goes in order. Either way the page puts back what was sent.
func TestALongUpdateGoesInPiecesThatAddUp(t *testing.T) {
	var items []agent.Item
	for i := range 400 {
		items = append(items, agent.Item{Key: fmt.Sprint(i), Kind: "text", Text: strings.Repeat("x", 500)})
	}
	for _, reset := range []bool{true, false} {
		u := agent.Update{Reset: reset, Items: items}
		b, _ := json.Marshal(u)
		ps := pieces(u, b)
		if len(ps) < 3 {
			t.Fatalf("reset %v: %d pieces", reset, len(ps))
		}
		var page []string
		for i, p := range ps {
			if len(p) > pieceSize+4096 {
				t.Errorf("piece %d is %d bytes", i, len(p))
			}
			var v agent.Update
			json.Unmarshal(p, &v)
			if v.More != (i < len(ps)-1) || v.Reset != (reset && i == 0) {
				t.Fatalf("reset %v, piece %d: more %v, reset %v", reset, i, v.More, v.Reset)
			}
			for _, it := range v.Items {
				page = append(page, it.Key)
			}
			before := []string{}
			for _, it := range v.Before {
				before = append(before, it.Key)
			}
			page = append(before, page...)
		}
		if reset {
			var first agent.Update
			json.Unmarshal(ps[0], &first)
			if len(ps[0]) > firstSize+4096 || first.Items[len(first.Items)-1].Key != "399" {
				t.Fatalf("the first piece is not the end: %d bytes", len(ps[0]))
			}
		}
		for i, k := range page {
			if k != fmt.Sprint(i) {
				t.Fatalf("reset %v: the page has %v", reset, page)
			}
		}
		if len(page) != len(items) {
			t.Fatalf("reset %v: %d of %d", reset, len(page), len(items))
		}
	}
}

func TestAMessageThatFailedIsTriedAgain(t *testing.T) {
	var s sent
	fail := errors.New("busy")
	if _, err := s.once("s", "u1", func() (string, error) { return "", fail }); err != fail {
		t.Fatal(err)
	}
	if uuid, err := s.once("s", "u1", func() (string, error) { return "u1", nil }); uuid != "u1" || err != nil {
		t.Fatalf("again: %q, %v", uuid, err)
	}
}

// A proxy or tunnel shows each event as it comes only if every one is
// flushed out of the gzip stream, not held until a block fills.
func TestAStreamIsGzippedAndFlushedEventByEvent(t *testing.T) {
	next := make(chan struct{})
	srv := httptest.NewServer(Compress(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for i := range 2 {
			fmt.Fprintf(w, "data: %d\n\n", i)
			w.(http.Flusher).Flush()
			<-next
		}
	})))
	defer srv.Close()
	defer close(next)
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Accept-Encoding", "gzip")
	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("not gzipped: %v", resp.Header)
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	lines := bufio.NewReader(zr)
	for i := range 2 {
		line, err := lines.ReadString('\n')
		if err != nil || line != fmt.Sprintf("data: %d\n", i) {
			t.Fatalf("event %d: %q, %v", i, line, err)
		}
		lines.ReadString('\n')
		next <- struct{}{}
	}
}

func TestCompressLeavesPicturesAndSocketsAlone(t *testing.T) {
	h := Compress(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", r.URL.Query().Get("type"))
		w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	for ct, want := range map[string]string{"application/json": "gzip", "image/svg+xml": "gzip", "image/png": "", "font/woff2": ""} {
		req := httptest.NewRequest("GET", "/?type="+ct, nil)
		req.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if got := w.Header().Get("Content-Encoding"); got != want {
			t.Errorf("%s: %q", ct, got)
		}
	}
	req := httptest.NewRequest("GET", "/?type=application/json", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Upgrade", "websocket")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Header().Get("Content-Encoding") != "" {
		t.Error("an upgrade was gzipped")
	}
}

func TestAStaticFileIsRevalidatedByItsTag(t *testing.T) {
	if !AssetsBuilt() {
		t.Skip("no UI bundle built")
	}
	h := Static()
	w := get(t, h, "/static/jetbrains-mono-latin-wght-normal.woff2")
	tag := w.Header().Get("ETag")
	if w.Code != http.StatusOK || tag == "" {
		t.Fatalf("%d, tag %q", w.Code, tag)
	}
	req := httptest.NewRequest("GET", "/static/jetbrains-mono-latin-wght-normal.woff2", nil)
	req.Header.Set("If-None-Match", tag)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != http.StatusNotModified {
		t.Fatalf("again: %d", w.Code)
	}
}
