// Package acp speaks the Agent Client Protocol to an agent run as a child
// process: JSON-RPC 2.0, one message per line on its stdin and stdout. It
// reads loosely, leaving what each message carries to its caller as raw
// JSON, since an agent may add to the schema.
package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
)

// Error is one the agent answered a request with.
type Error struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *Error) Error() string {
	if len(e.Data) > 0 && string(e.Data) != "null" {
		var s string
		if json.Unmarshal(e.Data, &s) == nil && s != "" {
			return e.Message + ": " + s
		}
		var d struct {
			Message string `json:"message"`
			Details string `json:"details"`
		}
		if json.Unmarshal(e.Data, &d) == nil && (d.Message != "" || d.Details != "") {
			return e.Message + ": " + d.Message + d.Details
		}
	}
	return e.Message
}

// Handler answers what the agent sends of its own accord. Request's answer
// is the result, or an *Error; Notify gets what needs none.
type Handler interface {
	Request(ctx context.Context, method string, params json.RawMessage) (any, error)
	Notify(method string, params json.RawMessage)
}

// Strays is a Handler that hears the lines an agent prints on stdout outside
// the protocol, as a sign-in link.
type Strays interface {
	Stray(line []byte)
}

// ErrClosed is what a request gets once the agent has gone.
var ErrClosed = errors.New("the agent stopped")

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

type answer struct {
	result json.RawMessage
	err    error
}

// Conn is one agent's end of the line.
type Conn struct {
	w       io.Writer
	handler Handler

	wmu sync.Mutex

	mu      sync.Mutex
	next    int
	pending map[string]chan answer
	asked   map[string]context.CancelFunc // the agent's requests still being answered
	done    chan struct{}
	err     error
}

// New starts reading r, the agent's stdout; w is its stdin. Notifications are
// handed over in the order they come, before the answer to any request that
// follows them, so a request's caller has heard all that came before it.
func New(w io.Writer, r io.Reader, h Handler) *Conn {
	c := &Conn{w: w, handler: h, pending: map[string]chan answer{}, asked: map[string]context.CancelFunc{}, done: make(chan struct{})}
	go c.read(r)
	return c
}

// Done is closed once the agent's output ends.
func (c *Conn) Done() <-chan struct{} { return c.done }

func (c *Conn) read(r io.Reader) {
	rd := bufio.NewReaderSize(r, 1<<16)
	var err error
	for {
		var line []byte
		line, err = rd.ReadBytes('\n')
		if len(line) > 1 {
			c.handle(line)
		}
		if err != nil {
			break
		}
	}
	c.mu.Lock()
	c.err = ErrClosed
	for id, ch := range c.pending {
		ch <- answer{err: ErrClosed}
		delete(c.pending, id)
	}
	for _, cancel := range c.asked {
		cancel()
	}
	c.mu.Unlock()
	close(c.done)
}

func (c *Conn) handle(line []byte) {
	var m message
	if json.Unmarshal(line, &m) != nil {
		if s, ok := c.handler.(Strays); ok {
			s.Stray(line)
		}
		return
	}
	switch {
	case m.Method != "" && len(m.ID) > 0:
		ctx, cancel := context.WithCancel(context.Background())
		key := string(m.ID)
		c.mu.Lock()
		c.asked[key] = cancel
		c.mu.Unlock()
		// Answered apart, as an answer may wait on the user.
		go func() {
			defer func() {
				cancel()
				c.mu.Lock()
				delete(c.asked, key)
				c.mu.Unlock()
			}()
			result, err := c.handler.Request(ctx, m.Method, m.Params)
			reply := map[string]any{"jsonrpc": "2.0", "id": m.ID}
			var rpc *Error
			switch {
			case errors.As(err, &rpc):
				reply["error"] = rpc
			case err != nil:
				reply["error"] = &Error{Code: -32603, Message: err.Error()}
			default:
				if result == nil {
					result = map[string]any{}
				}
				reply["result"] = result
			}
			c.write(reply)
		}()
	case m.Method != "":
		if m.Method == "$/cancel_request" {
			var p struct {
				ID json.RawMessage `json:"requestId"`
			}
			json.Unmarshal(m.Params, &p)
			c.mu.Lock()
			if cancel := c.asked[string(p.ID)]; cancel != nil {
				cancel()
			}
			c.mu.Unlock()
			return
		}
		c.handler.Notify(m.Method, m.Params)
	case len(m.ID) > 0:
		c.mu.Lock()
		ch := c.pending[string(m.ID)]
		delete(c.pending, string(m.ID))
		c.mu.Unlock()
		if ch != nil {
			if m.Error != nil {
				ch <- answer{err: m.Error}
			} else {
				ch <- answer{result: m.Result}
			}
		}
	}
}

func (c *Conn) write(v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_, err = c.w.Write(append(b, '\n'))
	return err
}

// Call sends a request and waits for its answer, decoded into out unless out
// is nil. A ctx that ends first leaves the agent to answer into nothing.
func (c *Conn) Call(ctx context.Context, method string, params, out any) error {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return c.err
	}
	c.next++
	id := strconv.Itoa(c.next)
	ch := make(chan answer, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	if err := c.write(map[string]any{"jsonrpc": "2.0", "id": c.next, "method": method, "params": params}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return err
	}
	select {
	case a := <-ch:
		if a.err != nil {
			return a.err
		}
		if out != nil && len(a.result) > 0 {
			if err := json.Unmarshal(a.result, out); err != nil {
				return fmt.Errorf("%s: %w", method, err)
			}
		}
		return nil
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return ctx.Err()
	}
}

// Notify sends what needs no answer.
func (c *Conn) Notify(method string, params any) error {
	return c.write(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}
