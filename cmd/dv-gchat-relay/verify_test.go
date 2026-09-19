package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestVerify(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	b64 := base64.RawURLEncoding.EncodeToString
	certs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "k1", "n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	defer certs.Close()
	const endpoint = "https://relay.example.com/chat"
	v := newVerifier(endpoint)
	v.certs = certs.URL

	sign := func(kid string, claims map[string]any) *http.Request {
		head, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": kid})
		body, _ := json.Marshal(claims)
		signed := b64(head) + "." + b64(body)
		sum := sha256.Sum256([]byte(signed))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
		req := httptest.NewRequest("POST", "/chat", nil)
		req.Header.Set("Authorization", "Bearer "+signed+"."+b64(sig))
		return req
	}
	now := time.Now().Unix()
	good := func() map[string]any {
		return map[string]any{
			"iss": "https://accounts.google.com", "aud": endpoint, "exp": now + 3600, "iat": now,
			"email": chatAccount, "email_verified": true,
		}
	}
	if err := v.check(sign("k1", good())); err != nil {
		t.Fatalf("Google Chat's request: %v", err)
	}
	for name, change := range map[string]func(map[string]any){
		"for another app":     func(c map[string]any) { c["aud"] = "https://other.example.com/chat" },
		"of another account":  func(c map[string]any) { c["email"] = "someone@example.com" },
		"expired":             func(c map[string]any) { c["exp"] = now - 3600 },
		"from someone else":   func(c map[string]any) { c["iss"] = "https://evil.example.com" },
		"with email unproven": func(c map[string]any) { c["email_verified"] = false },
	} {
		c := good()
		change(c)
		if v.check(sign("k1", c)) == nil {
			t.Errorf("took a request %s", name)
		}
	}
	if v.check(sign("k2", good())) == nil {
		t.Error("took a request signed by an unknown key")
	}
	forged := sign("k1", good())
	forged.Header.Set("Authorization", forged.Header.Get("Authorization")[:len(forged.Header.Get("Authorization"))-4]+"AAAA")
	if v.check(forged) == nil {
		t.Error("took a forged request")
	}
	if v.check(httptest.NewRequest("POST", "/chat", nil)) == nil {
		t.Error("took a request with no token")
	}
}
