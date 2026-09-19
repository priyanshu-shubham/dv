package main

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// verifier checks that a request came from Google Chat, by the token it
// carries, which Google signs. Its audience is what the app's settings say:
// the endpoint's URL, when it is an ID token of Chat's service account, or
// the project's number, when that account signs it itself.
type verifier struct {
	audience  string
	byProject bool
	certs     string // where the keys are
	http      *http.Client

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

const (
	chatAccount = "chat@system.gserviceaccount.com"
	googleCerts = "https://www.googleapis.com/oauth2/v3/certs"
	chatCerts   = "https://www.googleapis.com/service_accounts/v1/metadata/x509/" + chatAccount
)

func newVerifier(audience string) *verifier {
	v := &verifier{audience: audience, certs: googleCerts, http: &http.Client{Timeout: 10 * time.Second}, keys: map[string]*rsa.PublicKey{}}
	if strings.Trim(audience, "0123456789") == "" {
		v.byProject, v.certs = true, chatCerts
	}
	return v
}

type claims struct {
	Iss           string   `json:"iss"`
	Aud           audience `json:"aud"`
	Exp           int64    `json:"exp"`
	Iat           int64    `json:"iat"`
	Email         string   `json:"email"`
	EmailVerified any      `json:"email_verified"` // true, or "true"
}

// audience is a token's: one, or several.
type audience []string

func (a *audience) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*a = audience{one}
		return nil
	}
	return json.Unmarshal(b, (*[]string)(a))
}

// skew is how far apart Google's clock and this one may be.
const skew = 5 * time.Minute

func (v *verifier) check(req *http.Request) error {
	token, ok := strings.CutPrefix(req.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return errors.New("no token")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return errors.New("not a JWT")
	}
	var head struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := decodePart(parts[0], &head); err != nil {
		return err
	}
	if head.Alg != "RS256" {
		return fmt.Errorf("signed with %q", head.Alg)
	}
	key, err := v.key(req.Context(), head.Kid)
	if err != nil {
		return err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		return errors.New("not signed by Google")
	}
	var c claims
	if err := decodePart(parts[1], &c); err != nil {
		return err
	}
	now := time.Now()
	switch {
	case now.After(time.Unix(c.Exp, 0).Add(skew)):
		return errors.New("expired")
	case time.Unix(c.Iat, 0).After(now.Add(skew)):
		return errors.New("issued in the future")
	case !strings.Contains(" "+strings.Join(c.Aud, " ")+" ", " "+v.audience+" "):
		return fmt.Errorf("for %v", c.Aud)
	case v.byProject && c.Iss != chatAccount:
		return fmt.Errorf("issued by %q", c.Iss)
	case !v.byProject && c.Iss != "https://accounts.google.com" && c.Iss != "accounts.google.com":
		return fmt.Errorf("issued by %q", c.Iss)
	case !v.byProject && (c.Email != chatAccount || c.EmailVerified != true && c.EmailVerified != "true"):
		return fmt.Errorf("of %q", c.Email)
	}
	return nil
}

func decodePart(part string, out any) error {
	b, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}

// key is Google's public key kid, fetched again when one comes that is not
// known, at most once a minute, or when they are a day old.
func (v *verifier) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if k, ok := v.keys[kid]; ok && time.Since(v.fetched) < 24*time.Hour {
		return k, nil
	}
	if time.Since(v.fetched) < time.Minute {
		return nil, fmt.Errorf("no key %q", kid)
	}
	v.fetched = time.Now()
	keys, err := v.fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not fetch Google's keys: %w", err)
	}
	v.keys = keys
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, fmt.Errorf("no key %q", kid)
}

// fetch reads Google's keys: a JSON Web Key Set, or certificates by id.
func (v *verifier) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.certs, nil)
	if err != nil {
		return nil, err
	}
	resp, err := v.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Google answered %s", resp.Status)
	}
	keys := map[string]*rsa.PublicKey{}
	if !v.byProject {
		var set struct {
			Keys []struct {
				Kid string `json:"kid"`
				N   string `json:"n"`
				E   string `json:"e"`
			} `json:"keys"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
			return nil, err
		}
		for _, k := range set.Keys {
			n, err1 := base64.RawURLEncoding.DecodeString(k.N)
			e, err2 := base64.RawURLEncoding.DecodeString(k.E)
			if err1 != nil || err2 != nil {
				continue
			}
			keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		}
		return keys, nil
	}
	var certs map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&certs); err != nil {
		return nil, err
	}
	for kid, p := range certs {
		block, _ := pem.Decode([]byte(p))
		if block == nil {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		if k, ok := cert.PublicKey.(*rsa.PublicKey); ok {
			keys[kid] = k
		}
	}
	return keys, nil
}
