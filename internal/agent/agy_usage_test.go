package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeAgyToken(t *testing.T, expiry time.Time) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GEMINI_HOME", home)
	os.MkdirAll(filepath.Join(home, "antigravity-cli"), 0o700)
	b, _ := json.Marshal(map[string]any{
		"token":       map[string]any{"access_token": "tok-1", "refresh_token": "r", "expiry": expiry.Format(time.RFC3339Nano)},
		"auth_method": "oauth-personal", "id_token_email": "me@example.com",
	})
	os.WriteFile(filepath.Join(home, "antigravity-cli", "antigravity-oauth-token"), b, 0o600)
}

func TestAgyLimitsAreAskedAsTheAntigravityApp(t *testing.T) {
	writeAgyToken(t, time.Now().Add(time.Hour))
	now := time.Now().UTC()
	weekOn := now.Add(7 * 24 * time.Hour).Format(time.RFC3339)
	fiveOn := now.Add(2 * time.Hour).Format(time.RFC3339)
	var asked []string
	daily := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		asked = append(asked, r.URL.Path+" "+r.Header.Get("Authorization")+" "+r.Header.Get("User-Agent")+" "+string(body))
		json.NewEncoder(w).Encode(map[string]any{"groups": []any{
			map[string]any{"displayName": "Gemini", "buckets": []any{
				map[string]any{"bucketId": "gemini-5h", "window": "5h", "remainingFraction": 0.75, "resetTime": fiveOn, "description": "used"},
				map[string]any{"bucketId": "gemini-weekly", "window": "weekly", "remainingFraction": 1.0, "resetTime": weekOn},
			}},
			map[string]any{"displayName": "Other models", "buckets": []any{
				map[string]any{"bucketId": "3p-5h", "window": "5h", "remainingFraction": 0.5, "resetTime": fiveOn, "description": "used"},
				map[string]any{"bucketId": "3p-weekly", "window": "weekly", "remainingFraction": 1.0, "resetTime": weekOn},
			}},
		}})
	}))
	defer daily.Close()
	prod := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer prod.Close()
	was := agyQuotaHosts
	agyQuotaHosts = []string{daily.URL, prod.URL}
	defer func() { agyQuotaHosts = was }()

	u, err := readAgyLimits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(asked) != 1 || !strings.HasPrefix(asked[0], "/v1internal:retrieveUserQuotaSummary Bearer tok-1 antigravity/hub/") || !strings.HasSuffix(asked[0], " {}") {
		t.Fatalf("asked: %q", asked)
	}
	if u.Session == nil || u.Session.Percent != 25 || u.Session.ResetsAt != fiveOn || u.Session.Window != 300 {
		t.Fatalf("five hours: %+v", u.Session)
	}
	if u.Week == nil || u.Week.Percent != 0 || u.Week.ResetsAt != "" {
		t.Fatalf("an untouched week has no reset: %+v", u.Week)
	}
	if len(u.Models) != 1 || u.Models[0].Model != "Claude/GPT 5h" || u.Models[0].Percent != 50 {
		t.Fatalf("the shared allowance: %+v", u.Models)
	}
	if AgyAccount() != "me@example.com" {
		t.Fatal(AgyAccount())
	}
}

func TestAnExpiredAgyTokenIsNotSent(t *testing.T) {
	writeAgyToken(t, time.Now().Add(-time.Minute))
	sent := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { sent = true }))
	defer srv.Close()
	was := agyQuotaHosts
	agyQuotaHosts = []string{srv.URL, srv.URL}
	defer func() { agyQuotaHosts = was }()
	if _, err := readAgyLimits(context.Background()); err != errAgyTokenOld || sent {
		t.Fatalf("err %v, sent %v", err, sent)
	}
}
