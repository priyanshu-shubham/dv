package webpush

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"time"
)

// VAPID (RFC 8292) is how a push service knows a message comes from whoever
// the device subscribed with: dv signs each with a key of its own, whose
// public half the page handed the browser as it subscribed.

// subject is whom a push service would contact about dv's messages.
const subject = "https://github.com/priyanshu-shubham/dv"

// A token runs this long; push services refuse one past 24 hours.
const tokenLife = 12 * time.Hour

// vapid is the key, opened.
type vapid struct {
	key    *ecdsa.PrivateKey
	public string // its point, uncompressed, in base64url: the browser's applicationServerKey
}

func openVapid(der []byte) (*vapid, error) {
	k, err := x509.ParsePKCS8PrivateKey(der)
	key, ok := k.(*ecdsa.PrivateKey)
	if err != nil || !ok || key.Curve.Params().Name != "P-256" {
		return nil, errors.New("not a P-256 key")
	}
	pub, err := key.PublicKey.ECDH()
	if err != nil {
		return nil, err
	}
	return &vapid{key, b64.EncodeToString(pub.Bytes())}, nil
}

// A token is a push service's, until it runs out or the key changes.
type token struct {
	jwt   string
	until time.Time
	by    *vapid
}

// authorization is the header that signs a message for endpoint's push
// service, its token made afresh an hour before the last runs out.
func (p *Push) authorization(v *vapid, endpoint string) (string, error) {
	u, err := url.Parse(endpoint)
	if err != nil {
		return "", err
	}
	audience := u.Scheme + "://" + u.Host
	p.mu.Lock()
	defer p.mu.Unlock()
	t, ok := p.tokens[audience]
	if now := time.Now(); !ok || t.by != v || now.After(t.until.Add(-time.Hour)) {
		t = token{until: now.Add(tokenLife), by: v}
		if t.jwt, err = sign(v.key, audience, t.until); err != nil {
			return "", err
		}
		p.tokens[audience] = t
	}
	return "vapid t=" + t.jwt + ", k=" + v.public, nil
}

// sign makes a JWT, ES256: its signature r and s, 32 bytes each.
func sign(key *ecdsa.PrivateKey, audience string, exp time.Time) (string, error) {
	claims, err := json.Marshal(map[string]any{"aud": audience, "exp": exp.Unix(), "sub": subject})
	if err != nil {
		return "", err
	}
	signed := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`)) + "." + b64.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signed))
	r, s, err := ecdsa.Sign(rand.Reader, key, sum[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signed + "." + b64.EncodeToString(sig), nil
}

// b64 is base64url without padding, as JWTs and browsers' keys are.
var b64 = base64.RawURLEncoding
