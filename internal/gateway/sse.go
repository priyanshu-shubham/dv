package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"time"
)

type event struct {
	name string
	data []byte
}

func newEvent(name string, v any) event {
	data, _ := json.Marshal(v)
	return event{name, data}
}

// A sink takes a reply's events in Anthropic's shape, as they come.
type sink interface {
	send(event) error
}

type sseWriter struct {
	w  io.Writer
	rc *http.ResponseController
}

func (s *sseWriter) send(ev event) error {
	var b bytes.Buffer
	if ev.name != "" {
		b.WriteString("event: " + ev.name + "\n")
	}
	b.WriteString("data: ")
	b.Write(ev.data)
	b.WriteString("\n\n")
	if _, err := s.w.Write(b.Bytes()); err != nil {
		return err
	}
	if s.rc != nil {
		s.rc.Flush()
	}
	return nil
}

// A refuser answers a failure with an HTTP error while it still can.
type refuser interface {
	refuse(apiError) bool
}

// heldStream is a reply's stream, held until the model says something: a
// failure before then goes out as an HTTP error, which Claude Code retries
// or compacts by, where one inside a stream it can only show. Held too long,
// or quiet once going, it is kept alive with pings.
type heldStream struct {
	w       http.ResponseWriter
	mu      sync.Mutex
	sse     *sseWriter
	held    []event
	started bool
	failed  *apiError
	last    time.Time
	since   time.Time
	stop    chan struct{}
}

const (
	holdAtMost = 20 * time.Second
	pingAfter  = 30 * time.Second
)

func newHeldStream(w http.ResponseWriter) *heldStream {
	h := &heldStream{w: w, since: time.Now(), stop: make(chan struct{})}
	go h.keepAlive()
	return h
}

func (h *heldStream) send(ev event) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.started {
		h.held = append(h.held, ev)
		if ev.name == "content_block_delta" || ev.name == "message_stop" || ev.name == "error" {
			return h.startLocked()
		}
		return nil
	}
	h.last = time.Now()
	return h.sse.send(ev)
}

func (h *heldStream) refuse(e apiError) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.started {
		return false
	}
	h.failed = &e
	return true
}

func (h *heldStream) startLocked() error {
	h.started, h.last = true, time.Now()
	h.w.Header().Set("Content-Type", "text/event-stream")
	h.w.Header().Set("Cache-Control", "no-cache")
	h.w.WriteHeader(http.StatusOK)
	h.sse = &sseWriter{w: h.w, rc: http.NewResponseController(h.w)}
	for _, ev := range h.held {
		if err := h.sse.send(ev); err != nil {
			return err
		}
	}
	h.held = nil
	return nil
}

func (h *heldStream) keepAlive() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-h.stop:
			return
		case <-t.C:
		}
		h.mu.Lock()
		switch {
		case h.failed != nil:
		case !h.started && time.Since(h.since) > holdAtMost:
			h.startLocked()
		case h.started && time.Since(h.last) > pingAfter:
			h.last = time.Now()
			h.sse.send(newEvent("ping", map[string]string{"type": "ping"}))
		}
		h.mu.Unlock()
	}
}

// close ends the reply: as an HTTP error where it failed before saying
// anything, else with whatever was still held.
func (h *heldStream) close() {
	close(h.stop)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.failed != nil {
		h.failed.write(h.w)
		return
	}
	if !h.started {
		h.startLocked()
	}
}

// readSSE calls fn with each event of r, until r ends or fn fails.
func readSSE(r io.Reader, fn func(event) error) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 64*1024*1024)
	var ev event
	var data [][]byte
	for sc.Scan() {
		line := sc.Bytes()
		switch {
		case len(line) == 0:
			if len(data) > 0 || ev.name != "" {
				ev.data = bytes.Join(data, []byte("\n"))
				if err := fn(ev); err != nil {
					return err
				}
			}
			ev, data = event{}, nil
		case bytes.HasPrefix(line, []byte("event:")):
			ev.name = string(bytes.TrimSpace(line[len("event:"):]))
		case bytes.HasPrefix(line, []byte("data:")):
			data = append(data, bytes.Clone(bytes.TrimPrefix(line[len("data:"):], []byte(" "))))
		}
	}
	if len(data) > 0 {
		ev.data = bytes.Join(data, []byte("\n"))
		if err := fn(ev); err != nil {
			return err
		}
	}
	return sc.Err()
}

// collector builds the whole message a stream of events makes, for a client
// that asked for no stream.
type collector struct {
	msg     map[string]any
	content []map[string]any
	partial map[int]*bytes.Buffer
	failed  *apiError
}

func (c *collector) refuse(e apiError) bool {
	c.failed = &e
	return true
}

func (c *collector) send(ev event) error {
	var d map[string]any
	if json.Unmarshal(ev.data, &d) != nil {
		return nil
	}
	index := func() int { f, _ := d["index"].(float64); return int(f) }
	switch d["type"] {
	case "message_start":
		c.msg, _ = d["message"].(map[string]any)
	case "content_block_start":
		b, _ := d["content_block"].(map[string]any)
		for len(c.content) <= index() {
			c.content = append(c.content, nil)
		}
		c.content[index()] = b
	case "content_block_delta":
		i := index()
		if i >= len(c.content) || c.content[i] == nil {
			return nil
		}
		b, delta := c.content[i], d["delta"].(map[string]any)
		switch delta["type"] {
		case "text_delta":
			b["text"] = b["text"].(string) + delta["text"].(string)
		case "thinking_delta":
			b["thinking"] = b["thinking"].(string) + delta["thinking"].(string)
		case "signature_delta":
			b["signature"] = delta["signature"]
		case "input_json_delta":
			if c.partial == nil {
				c.partial = map[int]*bytes.Buffer{}
			}
			if c.partial[i] == nil {
				c.partial[i] = &bytes.Buffer{}
			}
			c.partial[i].WriteString(delta["partial_json"].(string))
		}
	case "content_block_stop":
		if p := c.partial[index()]; p != nil && p.Len() > 0 {
			var input any
			if json.Unmarshal(p.Bytes(), &input) == nil {
				c.content[index()]["input"] = input
			}
		}
	case "message_delta":
		if c.msg == nil {
			return nil
		}
		if delta, ok := d["delta"].(map[string]any); ok {
			for k, v := range delta {
				c.msg[k] = v
			}
		}
		if u, ok := d["usage"]; ok {
			c.msg["usage"] = u
		}
	}
	return nil
}

func (c *collector) message() map[string]any {
	if c.msg == nil {
		return nil
	}
	c.msg["content"] = c.content
	return c.msg
}
