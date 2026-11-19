// Package gateway is what the Claude Code sessions dv starts reach the model
// through. It masks the user's secrets on the way out and puts them back in
// the reply, sends a Claude model's requests on to Anthropic as they came,
// and answers a Codex model's (codex/…) through the user's ChatGPT sign-in.
package gateway

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"dv/internal/secret"
)

// Prefix is where the gateway is served on dv's own address, ahead of the
// page's handlers: no key is asked there, the path's token being its own.
const Prefix = "/gw/"

type Gateway struct {
	token     string
	anthropic *url.URL
	client    *http.Client
	mask      masker
	codex     *codexUpstream

	keyDir, path string
	mu           sync.Mutex
	base         atomic.Pointer[string]
}

var active atomic.Pointer[Gateway]

// New makes the process's gateway, with the secrets kept in configDir sealed
// by the key in keyDir.
func New(configDir, keyDir string) (*Gateway, error) {
	key, err := secret.Key(keyDir, true)
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte("dv gateway"))
	upstream := "https://api.anthropic.com"
	// A gateway of the user's own stays the one Claude models go through.
	if u := os.Getenv("ANTHROPIC_BASE_URL"); u != "" {
		upstream = u
	}
	au, err := url.Parse(strings.TrimRight(upstream, "/"))
	if err != nil {
		return nil, err
	}
	g := &Gateway{
		token:     hex.EncodeToString(mac.Sum(nil))[:32],
		anthropic: au,
		client:    &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()},
		codex:     newCodexUpstream(),
		keyDir:    keyDir,
		path:      filepath.Join(configDir, "secrets.json"),
	}
	if err := g.load(); err != nil {
		return nil, err
	}
	return g, nil
}

// Serve has the Claude Code dv starts from now on reach the model through g,
// at the dv listening on addr.
func (g *Gateway) Serve(addr net.Addr) {
	host, port := "127.0.0.1", ""
	if t, ok := addr.(*net.TCPAddr); ok {
		port = strconv.Itoa(t.Port)
		if !t.IP.IsUnspecified() {
			host = t.IP.String()
		}
	}
	base := "http://" + net.JoinHostPort(host, port) + Prefix + g.token
	g.base.Store(&base)
	active.Store(g)
}

// Env is what a Claude Code dv starts runs with to go through the gateway;
// nothing before one is serving.
func Env() []string {
	g := active.Load()
	if g == nil {
		return nil
	}
	return []string{"ANTHROPIC_BASE_URL=" + *g.base.Load()}
}

// Active is the gateway serving, nil if none is.
func Active() *Gateway { return active.Load() }

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rest, ok := strings.CutPrefix(r.URL.Path, Prefix+g.token)
	if !ok || (rest != "" && rest[0] != '/') {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost || (rest != "/v1/messages" && rest != "/v1/messages/count_tokens") {
		g.forward(w, r, rest, nil)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 512<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	body = g.mask.mask(body)
	var head struct {
		Model string `json:"model"`
	}
	json.Unmarshal(body, &head)
	if strings.HasPrefix(head.Model, ModelPrefix) {
		g.codex.serve(w, r, body, &g.mask)
		return
	}
	if bytes.Contains(body, []byte(reasoningSig)) {
		body = stripCodexReasoning(body)
	}
	g.forward(w, r, rest, body)
}

var hopHeaders = []string{"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade"}

// forward sends a request on to Anthropic as it came, body aside when given,
// and its reply back with the secrets put in again.
func (g *Gateway) forward(w http.ResponseWriter, r *http.Request, path string, body []byte) {
	u := *g.anthropic
	u.Path += path
	u.RawQuery = r.URL.RawQuery
	var rd io.Reader = r.Body
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, u.String(), rd)
	if err != nil {
		writeError(w, http.StatusBadGateway, "api_error", err.Error())
		return
	}
	req.Header = r.Header.Clone()
	for _, h := range hopHeaders {
		req.Header.Del(h)
	}
	// Unset, the transport asks for gzip and opens it, so the reply can be read.
	req.Header.Del("Accept-Encoding")
	if body != nil {
		req.ContentLength = int64(len(body))
	} else {
		req.ContentLength = r.ContentLength
	}
	resp, err := g.client.Do(req)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			writeError(w, http.StatusBadGateway, "api_error", "dv could not reach "+g.anthropic.Host+": "+err.Error())
		}
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	for _, h := range append(hopHeaders, "Content-Length", "Content-Encoding") {
		w.Header().Del(h)
	}
	ctype := resp.Header.Get("Content-Type")
	masking := g.mask.active() && strings.HasPrefix(path, "/v1/messages")
	switch {
	case masking && strings.HasPrefix(ctype, "text/event-stream"):
		w.WriteHeader(resp.StatusCode)
		out := &unmasking{m: &g.mask, next: &sseWriter{w: w, rc: http.NewResponseController(w)}, held: map[int]*heldDelta{}}
		readSSE(resp.Body, out.send)
	case masking && strings.HasPrefix(ctype, "application/json") && resp.StatusCode == http.StatusOK:
		b, err := io.ReadAll(resp.Body)
		if err != nil {
			return
		}
		w.WriteHeader(resp.StatusCode)
		w.Write(g.mask.unmaskMessage(b))
	default:
		w.WriteHeader(resp.StatusCode)
		copyFlushing(w, resp.Body)
	}
}

func copyFlushing(w http.ResponseWriter, r io.Reader) {
	rc := http.NewResponseController(w)
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			rc.Flush()
		}
		if err != nil {
			return
		}
	}
}

// CodexModels are the models the user's Codex sign-in offers, none without
// one or before the list has come.
func (g *Gateway) CodexModels() []CodexModel { return g.codex.knownModels() }

// CodexWindow is the context window of a Codex model, 0 for another.
func (g *Gateway) CodexWindow(model string) int {
	slug, ok := strings.CutPrefix(model, ModelPrefix)
	if !ok {
		return 0
	}
	for _, m := range g.CodexModels() {
		if m.Slug == slug {
			return m.Window
		}
	}
	return 0
}
