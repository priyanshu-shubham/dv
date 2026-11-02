package server

import (
	"bufio"
	"compress/gzip"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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
