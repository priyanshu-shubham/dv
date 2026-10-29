//go:build !windows

package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"dv/internal/store"
)

func TestTerminalOverWebSocket(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("PS1", "$ ")
	user, err := store.OpenUserPrefs()
	if err != nil {
		t.Fatal(err)
	}
	s, err := Open(t.TempDir(), user, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ts := httptest.NewServer(s.Handler(""))
	defer ts.Close()

	call := func(method, path string, out any) int {
		t.Helper()
		req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
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
	var made struct{ ID string }
	if call("POST", "/api/terminals", &made); made.ID == "" {
		t.Fatal("no terminal made")
	}
	if !s.InUse() {
		t.Fatal("a folder with a shell open is not in use")
	}

	ws := "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/terminals/" + made.ID + "/attach?ticket="
	if _, resp, err := websocket.DefaultDialer.Dial(ws+"forged", nil); err == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("attached without a ticket: %v", err)
	}
	attach := func() *websocket.Conn {
		t.Helper()
		var tk struct{ Ticket string }
		call("POST", "/api/terminals/"+made.ID+"/ticket", &tk)
		c, _, err := websocket.DefaultDialer.Dial(ws+tk.Ticket, nil)
		if err != nil {
			t.Fatal(err)
		}
		c.WriteMessage(websocket.TextMessage, []byte(`{"cols":100,"rows":30}`))
		return c
	}
	// until reads until a message has want in it, or a status says so.
	until := func(c *websocket.Conn, want string) string {
		t.Helper()
		var seen strings.Builder
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		for {
			_, p, err := c.ReadMessage()
			if err != nil {
				t.Fatalf("no %q in %q: %v", want, seen.String(), err)
			}
			seen.Write(p)
			if strings.Contains(seen.String(), want) {
				return seen.String()
			}
		}
	}

	c := attach()
	c.WriteMessage(websocket.BinaryMessage, []byte("stty size; echo one-$((40+2))\n"))
	until(c, "30 100")
	until(c, "one-42")
	c.Close()

	// Another page is given what was printed before it came.
	c = attach()
	defer c.Close()
	until(c, "one-42")

	c.WriteMessage(websocket.BinaryMessage, []byte("sleep 3\n"))
	busy := func() map[string]any {
		var list struct{ Terminals []map[string]any }
		call("GET", "/api/terminals", &list)
		return list.Terminals[0]
	}
	deadline := time.Now().Add(4 * time.Second)
	for busy()["command"] != "sleep 3" {
		if time.Now().After(deadline) {
			t.Fatalf("sleep not seen running: %v", busy())
		}
		time.Sleep(200 * time.Millisecond)
	}

	c.WriteMessage(websocket.BinaryMessage, []byte("exit 7\n"))
	until(c, `"exit":7`)
	if s.InUse() {
		t.Fatal("in use with its shell gone")
	}
	if code := call("POST", "/api/terminals/"+made.ID+"/start", nil); code != http.StatusOK {
		t.Fatalf("start again: %d", code)
	}
	until(c, `"started":true`)

	call("DELETE", "/api/terminals/"+made.ID, nil)
	c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		if _, _, err := c.ReadMessage(); err != nil {
			if !websocket.IsCloseError(err, closedCode) {
				t.Fatalf("closed with %v", err)
			}
			break
		}
	}
}
