// Package webpush sends notices to the reader's devices as notifications,
// through the push service of each device's browser - Google's, Mozilla's,
// Apple's - with no page of dv's open: to a phone dv is installed on, say.
// Each message is sealed for its device (RFC 8291) and signed for dv (VAPID,
// RFC 8292); the push service carrying it reads neither.
//
// Every dv sends its own notices, to the devices that subscribed through it.
package webpush

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"dv/internal/notify"
)

// Push is the provider. Settle has nothing to do: a page takes down the
// notifications of notices settled.
type Push struct {
	self   string // this dv's address
	dir    string
	keyDir string
	http   *http.Client
	queue  chan notify.Notice

	mu     sync.Mutex
	sealed string // the key, as last read
	vapid  *vapid // and opened
	tokens map[string]token
	last   map[string]sent // by endpoint
}

// sent is how the last message to a device went, for its Settings to say: a
// notification that never shows was either not sent or held by the device.
type sent struct {
	At    time.Time `json:"at"`
	Title string    `json:"title"`
	Error string    `json:"error,omitempty"`
}

// New readies the provider of a dv at self, keeping its devices in dir and
// the key that seals its own in keyDir.
func New(self, dir, keyDir string) *Push {
	return &Push{
		self: self, dir: dir, keyDir: keyDir,
		http:  &http.Client{Timeout: 15 * time.Second},
		queue: make(chan notify.Notice, 100), tokens: map[string]token{}, last: map[string]sent{},
	}
}

// Run sends the notices, in turn, until ctx ends.
func (p *Push) Run(ctx context.Context) {
	for {
		select {
		case n := <-p.queue:
			p.send(ctx, n)
		case <-ctx.Done():
			return
		}
	}
}

func (p *Push) Send(n notify.Notice) {
	select {
	case p.queue <- n:
	default: // the push services not keeping up; better a notice lost than dv held up
	}
}

func (p *Push) Settle(n notify.Notice) {}

// Eager: a device's notifications are what the reader looks for once dv is
// out of sight, and its service worker holds one back while a page is in front.
func (p *Push) Eager() {}

var _ notify.Eager = (*Push)(nil)

func (p *Push) send(ctx context.Context, n notify.Notice) {
	m := messageOf(n)
	for _, d := range p.devices() {
		if err := p.push(ctx, d, m); err != nil && ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "dv: could not send a notice to %s: %v\n", cmp.Or(d.Name, "a device"), err)
		}
	}
}

// message is what a device's notification is made from.
type message struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	URL     string `json:"url"`
	Folder  string `json:"folder,omitempty"`
	Session string `json:"session,omitempty"`
	Action  string `json:"action,omitempty"`
}

// A message is held well inside the 4 KB a push service carries, as a
// notification shows only its first lines anyway.
const (
	titleChars = 120
	lineChars  = 240
)

// messageOf is a notice as a notification: its session's title over a line
// of what it says, opening the session, or a command's output, in its
// folder's page.
func messageOf(n notify.Notice) message {
	path := "/"
	if n.Folder != "" {
		path += n.Folder + "/"
	}
	if n.Session != "" {
		path += "?session=" + url.QueryEscape(n.Session)
	} else if n.Action != "" {
		path += "?output=" + url.QueryEscape(n.Action)
	}
	body := clip(n.Where, titleChars)
	if line := excerpt(n.Body, n.Format); line != "" && body != "" {
		body += "\n" + line
	} else if line != "" {
		body = line
	}
	return message{ID: n.ID, Kind: n.Kind, Title: clip(n.Title, titleChars), Body: body, URL: path, Folder: n.Folder, Session: n.Session, Action: n.Action}
}

// excerpt is the start of a body, on one line; Markdown's marks are dropped,
// a notification showing them as they are.
func excerpt(body, format string) string {
	var words []string
	for line := range strings.Lines(body) {
		if format == notify.Markdown {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				continue
			}
			line = unmark.Replace(strings.TrimLeft(line, "#> \t"))
		}
		words = append(words, strings.Fields(line)...)
		if len(words) > lineChars/2 {
			break
		}
	}
	return clip(strings.Join(words, " "), lineChars)
}

var unmark = strings.NewReplacer("**", "", "__", "", "`", "")

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:n-1])) + "…"
}

// errGone is a push service having dropped a device's subscription: the
// browser's site data cleared, or notifications turned off for dv.
var errGone = errors.New("The device's push service no longer has its subscription; turn notifications on there again")

// push sends m to d now. A subscription its push service has dropped is
// forgotten.
func (p *Push) push(ctx context.Context, d Device, m message) (err error) {
	defer func() {
		if ctx.Err() != nil {
			return
		}
		s := sent{At: time.Now(), Title: m.Title}
		if err != nil {
			s.Error = err.Error()
		}
		p.mu.Lock()
		p.last[d.Endpoint] = s
		p.mu.Unlock()
	}()
	v, err := p.key(false)
	if err != nil {
		return err
	}
	ua, err1 := decodeKey(d.P256dh)
	auth, err2 := decodeKey(d.Auth)
	if err1 != nil || err2 != nil {
		return errors.New("the device's keys are not base64url")
	}
	plaintext, _ := json.Marshal(m)
	body, err := encrypt(plaintext, ua, auth)
	if err != nil {
		return err
	}
	signed, err := p.authorization(v, d.Endpoint)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	// Each wakes the device: at normal urgency a phone asleep is told when it
	// next wakes, which is when the reader would have looked anyway. What waits
	// on the reader keeps a day; a turn's end is old news in an hour.
	ttl := time.Hour
	if m.Kind == notify.Ask {
		ttl = 24 * time.Hour
	}
	sum := sha256.Sum256([]byte(m.ID))
	req.Header.Set("Authorization", signed)
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("TTL", strconv.Itoa(int(ttl.Seconds())))
	req.Header.Set("Urgency", "high")
	req.Header.Set("Topic", b64.EncodeToString(sum[:16]))
	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		p.forget(d.Endpoint)
		return errGone
	case resp.StatusCode >= 300:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 500))
		said := "the push service answered " + resp.Status
		if why := strings.TrimSpace(string(b)); why != "" {
			said += ": " + why
		}
		return errors.New(said)
	}
	return nil
}

// decodeKey takes a key in base64url, as browsers give them, padded or not.
func decodeKey(s string) ([]byte, error) {
	return b64.DecodeString(strings.TrimRight(s, "="))
}
