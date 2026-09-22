package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// tokens are the service's access tokens, from the metadata server of the
// Cloud Run service it runs as: as the Chat app, and for Firestore.
type tokens struct {
	http   *http.Client
	scopes string

	mu     sync.Mutex
	token  string
	expiry time.Time
}

const metadata = "http://metadata.google.internal/computeMetadata/v1/"

func newTokens(scopes ...string) *tokens {
	return &tokens{http: &http.Client{Timeout: 10 * time.Second}, scopes: strings.Join(scopes, ",")}
}

func (t *tokens) get(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.token != "" && time.Until(t.expiry) > time.Minute {
		return t.token, nil
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := fromMetadata(ctx, t.http, "instance/service-accounts/default/token?scopes="+url.QueryEscape(t.scopes), &tok); err != nil {
		return "", fmt.Errorf("could not get an access token: %w", err)
	}
	t.token, t.expiry = tok.AccessToken, time.Now().Add(time.Duration(tok.ExpiresIn)*time.Second)
	return t.token, nil
}

// fromMetadata reads the metadata server: into out as JSON, or as text into
// a *string.
func fromMetadata(ctx context.Context, c *http.Client, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadata+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Metadata-Flavor", "Google")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("the metadata server answered %s", resp.Status)
	}
	if s, ok := out.(*string); ok {
		b, err := io.ReadAll(resp.Body)
		*s = strings.TrimSpace(string(b))
		return err
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// googleAPI makes a call to a Google REST API with the service's token.
func googleAPI(ctx context.Context, c *http.Client, tok *tokens, method, url string, in, out any) error {
	token, err := tok.get(ctx)
	if err != nil {
		return err
	}
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return &apiError{resp.StatusCode, strings.TrimSpace(string(b))}
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

type apiError struct {
	code int
	body string
}

func (e *apiError) Error() string { return fmt.Sprintf("Google answered %d: %s", e.code, e.body) }

// googleChat is the Chat API, called as the app: as the service account it
// runs as, which is the app's where it is of the app's project.
type googleChat struct {
	http *http.Client
	tok  *tokens
}

const chatBase = "https://chat.googleapis.com/v1/"

func newGoogleChat() *googleChat {
	return &googleChat{http: &http.Client{Timeout: 20 * time.Second}, tok: newTokens("https://www.googleapis.com/auth/chat.bot")}
}

func (c *googleChat) create(ctx context.Context, space, id string, m message) (message, error) {
	q := url.Values{"messageId": {id}}
	if m.Thread != nil {
		q.Set("messageReplyOption", "REPLY_MESSAGE_FALLBACK_TO_NEW_THREAD")
	}
	var made message
	err := c.call(ctx, http.MethodPost, chatBase+space+"/messages?"+q.Encode(), m, &made)
	return made, err
}

func (c *googleChat) patch(ctx context.Context, name, mask string, m message) error {
	return c.call(ctx, http.MethodPatch, chatBase+name+"?updateMask="+url.QueryEscape(mask), m, nil)
}

func (c *googleChat) get(ctx context.Context, name string) (m message, err error) {
	return m, c.call(ctx, http.MethodGet, chatBase+name, nil, &m)
}

func (c *googleChat) media(ctx context.Context, resource string) ([]byte, error) {
	token, err := c.tok.get(ctx)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, chatBase+"media/"+(&url.URL{Path: resource}).EscapedPath()+"?alt=media", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, &apiError{resp.StatusCode, strings.TrimSpace(string(b))}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxAttachBytes+1))
	if len(b) > maxAttachBytes {
		return nil, errors.New("the file is too large")
	}
	return b, err
}

// call tries again what Google Chat turns down for coming too fast: a space
// takes about a message a second.
func (c *googleChat) call(ctx context.Context, method, url string, in, out any) error {
	for try := 1; ; try++ {
		err := googleAPI(ctx, c.http, c.tok, method, url, in, out)
		var api *apiError
		if !errors.As(err, &api) || api.code != http.StatusTooManyRequests || try == 4 {
			return err
		}
		select {
		case <-time.After(time.Duration(try) * time.Second):
		case <-ctx.Done():
			return err
		}
	}
}

// firestore is the Store in Firestore, the project's default database. Each
// record is kept whole as JSON, with the fields it is looked up by beside it.
type firestore struct {
	http *http.Client
	tok  *tokens
	docs string // …/documents
}

func newFirestore(project string, tok *tokens) *firestore {
	return &firestore{
		http: &http.Client{Timeout: 15 * time.Second}, tok: tok,
		docs: "https://firestore.googleapis.com/v1/projects/" + project + "/databases/(default)/documents",
	}
}

type document struct {
	Fields map[string]value `json:"fields"`
}

type value struct {
	String string `json:"stringValue"`
}

func (f *firestore) put(ctx context.Context, collection, id string, record any, by map[string]string) error {
	b, err := json.Marshal(record)
	if err != nil {
		return err
	}
	doc := document{Fields: map[string]value{"json": {string(b)}}}
	for k, v := range by {
		doc.Fields[k] = value{v}
	}
	return googleAPI(ctx, f.http, f.tok, http.MethodPatch, f.docs+"/"+collection+"/"+url.PathEscape(id), doc, nil)
}

func (f *firestore) get(ctx context.Context, collection, id string, out any) error {
	var doc document
	if err := googleAPI(ctx, f.http, f.tok, http.MethodGet, f.docs+"/"+collection+"/"+url.PathEscape(id), nil, &doc); err != nil {
		return err
	}
	return json.Unmarshal([]byte(doc.Fields["json"].String), out)
}

func (f *firestore) delete(ctx context.Context, collection, id string) error {
	err := googleAPI(ctx, f.http, f.tok, http.MethodDelete, f.docs+"/"+collection+"/"+url.PathEscape(id), nil, nil)
	if errors.Is(err, errNotFound) {
		return nil
	}
	return err
}

// where is the records of a collection whose field is value.
func (f *firestore) where(ctx context.Context, collection, field, is string, each func(json []byte) error) error {
	query := map[string]any{"structuredQuery": map[string]any{
		"from":  []map[string]string{{"collectionId": collection}},
		"where": map[string]any{"fieldFilter": map[string]any{"field": map[string]string{"fieldPath": field}, "op": "EQUAL", "value": value{is}}},
	}}
	var found []struct {
		Document *document `json:"document"`
	}
	if err := googleAPI(ctx, f.http, f.tok, http.MethodPost, f.docs+":runQuery", query, &found); err != nil {
		return err
	}
	for _, r := range found {
		if r.Document != nil {
			if err := each([]byte(r.Document.Fields["json"].String)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (f *firestore) PutPairing(ctx context.Context, p Pairing) error {
	return f.put(ctx, "pairings", p.Code, p, map[string]string{"claim": p.Claim})
}

func (f *firestore) Pairing(ctx context.Context, code string) (p Pairing, err error) {
	return p, f.get(ctx, "pairings", code, &p)
}

func (f *firestore) PairingByClaim(ctx context.Context, claim string) (p Pairing, err error) {
	err = errNotFound
	f.where(ctx, "pairings", "claim", claim, func(b []byte) error {
		err = json.Unmarshal(b, &p)
		return err
	})
	return p, err
}

func (f *firestore) DeletePairing(ctx context.Context, code string) error {
	return f.delete(ctx, "pairings", code)
}

func (f *firestore) PutHub(ctx context.Context, h Hub) error {
	return f.put(ctx, "hubs", h.ID, h, map[string]string{"owner": h.Owner})
}

func (f *firestore) Hub(ctx context.Context, id string) (h Hub, err error) {
	return h, f.get(ctx, "hubs", id, &h)
}

func (f *firestore) HubsOf(ctx context.Context, owner string) ([]Hub, error) {
	var out []Hub
	err := f.where(ctx, "hubs", "owner", owner, func(b []byte) error {
		var h Hub
		if err := json.Unmarshal(b, &h); err != nil {
			return err
		}
		out = append(out, h)
		return nil
	})
	return out, err
}

func (f *firestore) DeleteHub(ctx context.Context, id string) error {
	return f.delete(ctx, "hubs", id)
}

func (f *firestore) PutRoute(ctx context.Context, r Route) error {
	return f.put(ctx, "routes", routeID(r.Thread, r.Owner), r, nil)
}

func (f *firestore) Route(ctx context.Context, thread, owner string) (r Route, err error) {
	return r, f.get(ctx, "routes", routeID(thread, owner), &r)
}

// routeID is a route's document's: a thread's name has slashes, which a
// document's id cannot.
func routeID(thread, owner string) string {
	sum := sha256.Sum256([]byte(thread + "|" + owner))
	return hex.EncodeToString(sum[:])
}
