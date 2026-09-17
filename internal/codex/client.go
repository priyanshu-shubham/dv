// Package codex talks to Codex through `codex app-server`, the JSON-RPC
// protocol Codex's own editors and apps use: threads to start and resume,
// turns to run in them, and the notifications and approval requests they send
// back. One app-server serves every folder a dv has open; it starts when
// something asks for it and stops once nothing has used it for a while.
package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Handler hears what the app-server says about one thread.
type Handler interface {
	// Notify is a notification about the thread, in the order they came.
	Notify(method string, params json.RawMessage)
	// Request is the app-server asking something of the thread's client, as an
	// approval. It is called apart and may wait on the reader; its result is
	// the response. id is what serverRequest/resolved names it by.
	Request(id, method string, params json.RawMessage) (any, error)
	// Exited says the app-server stopped, taking the thread's turn with it.
	Exited()
}

// Error is an error the app-server answered a call with.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

// Client is a running `codex app-server`, started as it is needed.
type Client struct {
	mu       sync.Mutex
	cmd      *exec.Cmd
	stdin    io.WriteCloser
	exited   chan struct{}
	stopping bool
	ready    chan struct{} // closed once initialized; nil before a start
	startErr error
	next     int
	pending  map[string]chan reply
	handlers map[string]Handler
	kept     map[string]bool // threads a turn can still run in, which hold it up
	// Threads this app-server loaded, by when they were let go: zero while kept.
	ours     map[string]time.Time
	lastUsed time.Time
	stderr   *tailBuffer

	wmu sync.Mutex
}

type reply struct {
	result json.RawMessage
	err    error
}

var (
	shared     *Client
	sharedOnce sync.Once
)

// Shared is the app-server every folder in this dv uses.
func Shared() *Client {
	sharedOnce.Do(func() {
		shared = &Client{handlers: map[string]Handler{}, kept: map[string]bool{}, ours: map[string]time.Time{}}
		go shared.reap()
	})
	return shared
}

// Available reports whether there is a Codex to run.
func Available() bool {
	_, err := exec.LookPath("codex")
	return err == nil
}

// ClientVersion is what dv tells the app-server it is.
var ClientVersion = "dev"

// idleFor is how long the app-server runs with no thread kept and nothing
// asked of it. It takes a few hundred megabytes, so it does not linger.
const idleFor = 3 * time.Minute

// Handle routes a thread's notifications and requests to h, or with nil stops.
func (c *Client) Handle(thread string, h Handler) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if h == nil {
		delete(c.handlers, thread)
		if c.kept[thread] {
			delete(c.kept, thread)
			c.ours[thread] = time.Now()
		}
	} else {
		c.handlers[thread] = h
	}
}

// Keep holds the app-server up while a thread is loaded in it, or lets it go.
func (c *Client) Keep(thread string, on bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if on {
		c.kept[thread] = true
		c.ours[thread] = time.Time{}
	} else if c.kept[thread] {
		delete(c.kept, thread)
		c.ours[thread] = time.Now()
	}
	c.lastUsed = time.Now()
}

// Running reports whether the app-server is up, which a loaded thread needs.
func (c *Client) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cmd != nil && !c.stopping
}

// Call makes a request and decodes its result into out, starting the
// app-server if it is not running.
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	ready, err := c.start()
	if err != nil {
		return err
	}
	select {
	case <-ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	c.mu.Lock()
	if c.startErr != nil {
		err := c.startErr
		c.mu.Unlock()
		return err
	}
	c.mu.Unlock()
	raw, err := c.call(ctx, method, params)
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.cmd == nil {
		c.mu.Unlock()
		return nil, errors.New("Codex stopped")
	}
	c.next++
	id := strconv.Itoa(c.next)
	ch := make(chan reply, 1)
	c.pending[id] = ch
	c.lastUsed = time.Now()
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	msg := map[string]any{"id": c.next, "method": method}
	if params != nil {
		msg["params"] = params
	}
	if err := c.write(msg); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		return r.result, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// start runs the app-server if it is not running, returning a channel closed
// once it has been initialized.
func (c *Client) start() (chan struct{}, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastUsed = time.Now()
	if c.cmd != nil && !c.stopping {
		return c.ready, nil
	}
	if c.cmd != nil {
		// Still going from a stop; a new one starts once it has.
		exited := c.exited
		c.mu.Unlock()
		<-exited
		c.mu.Lock()
		if c.cmd != nil {
			return c.ready, nil
		}
	}
	cmd := exec.Command("codex", "app-server")
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	ownGroup(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	c.stderr = &tailBuffer{max: 4096}
	cmd.Stderr = c.stderr
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, errors.New("the `codex` CLI is not on PATH")
		}
		return nil, err
	}
	c.cmd, c.stdin, c.exited, c.stopping, c.startErr = cmd, stdin, make(chan struct{}), false, nil
	c.pending = map[string]chan reply{}
	c.ready = make(chan struct{})
	read := make(chan struct{})
	go c.read(stdout, read)
	go c.wait(cmd, read)
	go c.initialize(c.ready)
	return c.ready, nil
}

func (c *Client) initialize(ready chan struct{}) {
	defer close(ready)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := c.call(ctx, "initialize", map[string]any{
		"clientInfo": map[string]any{"name": "dv", "title": "dv", "version": ClientVersion},
		// Queued messages, settings between turns and plan mode are experimental.
		"capabilities": map[string]any{"experimentalApi": true},
	})
	if err == nil {
		err = c.write(map[string]any{"method": "initialized"})
	}
	if err != nil {
		c.mu.Lock()
		c.startErr = fmt.Errorf("Codex would not start: %w", err)
		c.mu.Unlock()
		// The next call starts another rather than asking this one again.
		go c.Stop()
	}
}

func (c *Client) wait(cmd *exec.Cmd, read chan struct{}) {
	<-read
	err := cmd.Wait()
	c.mu.Lock()
	why := errors.New("Codex stopped")
	if msg := strings.TrimSpace(c.stderr.String()); err != nil && !c.stopping && msg != "" {
		why = fmt.Errorf("Codex stopped: %s", msg)
	}
	for _, ch := range c.pending {
		deliver(ch, reply{err: why})
	}
	handlers := make([]Handler, 0, len(c.handlers))
	for _, h := range c.handlers {
		handlers = append(handlers, h)
	}
	c.kept, c.ours = map[string]bool{}, map[string]time.Time{}
	c.cmd, c.stdin = nil, nil
	close(c.exited)
	c.mu.Unlock()
	for _, h := range handlers {
		h.Exited()
	}
}

// Stop ends the app-server, with anything running in it.
func (c *Client) Stop() {
	c.mu.Lock()
	cmd, stdin, exited := c.cmd, c.stdin, c.exited
	if cmd == nil {
		c.mu.Unlock()
		return
	}
	c.stopping = true
	c.mu.Unlock()
	stdin.Close()
	select {
	case <-exited:
	case <-time.After(3 * time.Second):
		killGroup(cmd)
		<-exited
	}
}

func (c *Client) reap() {
	for range time.Tick(30 * time.Second) {
		c.mu.Lock()
		idle := c.cmd != nil && len(c.kept) == 0 && len(c.pending) == 0 && time.Since(c.lastUsed) > idleFor
		c.mu.Unlock()
		if idle {
			c.Stop()
		}
	}
}

func (c *Client) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.mu.Lock()
	stdin := c.stdin
	c.mu.Unlock()
	if stdin == nil {
		return errors.New("Codex is not running")
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = stdin.Write(append(b, '\n'))
	return err
}

type message struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
}

func (c *Client) read(stdout io.Reader, done chan struct{}) {
	defer close(done)
	rd := bufio.NewReaderSize(stdout, 1<<16)
	for {
		line, err := rd.ReadBytes('\n')
		if len(line) > 1 {
			c.handle(line)
		}
		if err != nil {
			return
		}
	}
}

func (c *Client) handle(line []byte) {
	var m message
	if json.Unmarshal(line, &m) != nil {
		return
	}
	switch {
	case m.Method == "" && m.ID != nil:
		c.mu.Lock()
		ch := c.pending[strings.Trim(string(m.ID), `"`)]
		c.mu.Unlock()
		if ch != nil {
			r := reply{result: m.Result}
			if m.Error != nil {
				r.err = m.Error
			}
			deliver(ch, r)
		}
	case m.ID != nil:
		go c.serve(m)
	default:
		if h := c.handler(m.Params); h != nil {
			h.Notify(m.Method, m.Params)
		}
	}
}

// serve answers a request from the app-server through its thread's handler.
func (c *Client) serve(m message) {
	var result any
	var err error
	if h := c.handler(m.Params); h != nil {
		result, err = h.Request(strings.Trim(string(m.ID), `"`), m.Method, m.Params)
	} else {
		err = fmt.Errorf("dv does not handle %s", m.Method)
	}
	out := map[string]any{"id": m.ID}
	if err != nil {
		out["error"] = map[string]any{"code": -32603, "message": err.Error()}
	} else {
		out["result"] = result
	}
	c.write(out)
}

func (c *Client) handler(params json.RawMessage) Handler {
	var p struct {
		ThreadID string `json:"threadId"`
	}
	json.Unmarshal(params, &p)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.handlers[p.ThreadID]
}

func deliver(ch chan reply, r reply) {
	select {
	case ch <- r:
	default:
	}
}

// tailBuffer keeps the last bytes written to it: the end of stderr is where a
// failing app-server says why.
type tailBuffer struct {
	mu  sync.Mutex
	max int
	b   []byte
}

func (t *tailBuffer) Write(b []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, b...)
	if len(t.b) > t.max {
		t.b = t.b[len(t.b)-t.max:]
	}
	return len(b), nil
}

func (t *tailBuffer) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}
