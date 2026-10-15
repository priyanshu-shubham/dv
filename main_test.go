package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"dv/internal/gitx"
	"dv/internal/notify"
	"dv/internal/notify/webpush"
	"dv/internal/store"
)

func TestHomePortIsStableAndSpread(t *testing.T) {
	a := homePort("/home/you/code/one")
	if a != homePort("/home/you/code/one") {
		t.Fatal("the same root hashed to two ports")
	}
	if a < basePort || a >= basePort+portSpan {
		t.Fatalf("port %d outside %d..%d", a, basePort, basePort+portSpan-1)
	}
	seen := map[int]bool{}
	for i := range 20 {
		seen[homePort(fmt.Sprintf("/home/you/code/repo%d", i))] = true
	}
	if len(seen) < 18 {
		t.Fatalf("20 roots landed on only %d ports", len(seen))
	}
}

func TestListenMovesPastABusyHomePort(t *testing.T) {
	root := "/home/you/code/busy"
	home := homePort(root)
	held, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(home)))
	if err != nil {
		t.Skipf("home port %d is not free here: %v", home, err)
	}
	defer held.Close()

	ln, err := listen("127.0.0.1", 0, root)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := ln.Addr().(*net.TCPAddr).Port
	if got == home || got < basePort || got >= basePort+portSpan {
		t.Fatalf("got port %d; want a free one in range other than the busy %d", got, home)
	}
}

func TestResetDeletesTheReview(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	repo, err := gitx.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := store.Open(repo.Root, repo.Name())
	vw, _ := store.OpenViewed(repo.Root)
	st.AddThread(&store.Thread{File: "a.go", Side: "new", StartLine: 1, EndLine: 1}, "note", "me")
	vw.Mark("Uncommitted", []string{"a.go"}, true)

	// Nobody at a terminal to ask, so it must not delete without -y.
	r, w, _ := os.Pipe()
	defer r.Close()
	w.Close()
	stdin := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = stdin }()
	if err := reset(root, nil); err == nil || !strings.Contains(err.Error(), "-y") {
		t.Fatalf("reset without a terminal or -y: %v", err)
	}
	if len(st.Threads()) != 1 || vw.Count() != 1 {
		t.Fatal("reset deleted without being told to")
	}

	if err := reset(root, []string{"-y"}); err != nil {
		t.Fatal(err)
	}
	if len(st.Threads()) != 0 || vw.Count() != 0 {
		t.Fatalf("after reset -y: %d threads, %d marks", len(st.Threads()), vw.Count())
	}
}

// What installs dv as an app is served at any address it is reached at; the
// devices it pushes to only at its own.
func TestAppFilesAreOpenAndPushIsGuarded(t *testing.T) {
	notices := notify.New(func() time.Duration { return time.Minute })
	defer notices.Close()
	push := webpush.New("http://127.0.0.1:1", t.TempDir(), t.TempDir())
	h := withNotices(http.NotFoundHandler(), notices, chatApps{push: push})
	for path, want := range map[string]int{
		"/manifest.webmanifest": 200, "/sw.js": 200, "/icon/192.png": 200, "/icon/badge.png": 200, "/api/notify/push": 403,
	} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "http://dv.example.com"+path, nil))
		if w.Code != want {
			t.Errorf("%s: %d, not %d", path, w.Code, want)
		}
	}
}
