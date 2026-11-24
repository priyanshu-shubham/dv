package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Antigravity's usage limits, read as T3 Code reads them: agy keeps nothing to
// ask over ACP, so dv takes the access token agy keeps in its file and asks
// the endpoint the Antigravity app asks, as that app. This is the user's own
// choice, made knowing Google may count it as a third party using their login.

// agyQuotaHosts are asked in this order, and the first to name a bucket wins:
// a personal account is metered on the daily host. Tests point them elsewhere.
var agyQuotaHosts = []string{"https://daily-cloudcode-pa.googleapis.com", "https://cloudcode-pa.googleapis.com"}

func agyUserAgent() string {
	return fmt.Sprintf("antigravity/hub/2.24.0 (aidev_client; os_type=%s; arch=%s; cl=963137146)", runtime.GOOS, runtime.GOARCH)
}

type agyToken struct {
	Token struct {
		AccessToken string    `json:"access_token"`
		Expiry      time.Time `json:"expiry"`
	} `json:"token"`
	Method string `json:"auth_method"`
	Email  string `json:"id_token_email"`
}

// agyTokenPath is where agy keeps its Google sign-in, when it keeps it in a
// file rather than the system keyring.
func agyTokenPath() string {
	home := os.Getenv("GEMINI_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		home = filepath.Join(h, ".gemini")
	}
	return filepath.Join(home, "antigravity-cli", "antigravity-oauth-token")
}

func readAgyToken() (agyToken, error) {
	var t agyToken
	b, err := os.ReadFile(agyTokenPath())
	if err != nil {
		return t, err
	}
	if err := json.Unmarshal(b, &t); err != nil || t.Token.AccessToken == "" {
		return t, errors.New("agy's sign-in file holds no token")
	}
	return t, nil
}

// AgyAccount is who agy is signed in as, "" when unknown.
func AgyAccount() string {
	t, _ := readAgyToken()
	return t.Email
}

var agyLimits struct {
	sync.Mutex
	usage  *Usage
	next   time.Time
	asking bool
}

// AgyUsage is how much of the Antigravity plan's limits is used, nil while
// unknown. Once a minute old it is asked again, apart.
func AgyUsage() *Usage {
	u := &agyLimits
	u.Lock()
	defer u.Unlock()
	if agyOff() || u.asking || time.Now().Before(u.next) {
		return u.usage
	}
	if !AgyAvailable() {
		u.usage, u.next = nil, time.Now().Add(optionsFor)
		return nil
	}
	u.asking = true
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		usage, err := readAgyLimits(ctx)
		u.Lock()
		defer u.Unlock()
		u.asking = false
		u.next = time.Now().Add(time.Minute)
		if err == nil {
			u.usage = usage
		}
	}()
	return u.usage
}

// errAgyTokenOld is a token agy has not renewed lately. agy renews it as it
// runs; dv does not start agy to, as each start unpacks it, and keeps the
// limits last heard until agy runs again, in a session or a terminal.
var errAgyTokenOld = errors.New("agy's sign-in has expired until agy runs again")

func readAgyLimits(ctx context.Context) (*Usage, error) {
	t, err := readAgyToken()
	if err != nil {
		return nil, err
	}
	if time.Until(t.Token.Expiry) < time.Minute {
		return nil, errAgyTokenOld
	}
	hosts := agyQuotaHosts
	if t.Method == "oauth-business" {
		hosts = []string{hosts[1], hosts[0]}
	}
	type reply struct {
		buckets []agyBucket
		err     error
	}
	replies := make([]reply, len(hosts))
	var wg sync.WaitGroup
	for i, host := range hosts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := askAgyQuota(ctx, host, t.Token.AccessToken)
			replies[i] = reply{b, err}
		}()
	}
	wg.Wait()
	now := time.Now()
	seen := map[string]bool{}
	var all []agyBucket
	var firstErr error
	for _, r := range replies {
		firstErr = errors.Join(firstErr, r.err)
		for _, b := range r.buckets {
			if !seen[b.ID] {
				seen[b.ID] = true
				all = append(all, b)
			}
		}
	}
	if len(all) == 0 {
		return nil, cmpErr(firstErr, errors.New("Antigravity gave no quota"))
	}
	return agyUsageFrom(all, now), nil
}

func cmpErr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

func cmpStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

type agyBucket struct {
	ID          string   `json:"bucketId"`
	Window      string   `json:"window"`
	Remaining   *float64 `json:"remainingFraction"`
	ResetTime   string   `json:"resetTime"`
	Description *string  `json:"description"`
	Disabled    bool     `json:"disabled"`
	group       string
}

func askAgyQuota(ctx context.Context, host, token string) ([]agyBucket, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/v1internal:retrieveUserQuotaSummary", bytes.NewReader([]byte("{}")))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", agyUserAgent())
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", host, resp.Status)
	}
	var r struct {
		Groups []struct {
			Name    string      `json:"displayName"`
			Buckets []agyBucket `json:"buckets"`
		} `json:"groups"`
		Buckets []agyBucket `json:"buckets"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, err
	}
	out := r.Buckets
	for _, g := range r.Groups {
		for _, b := range g.Buckets {
			b.group = strings.TrimSpace(g.Name)
			out = append(out, b)
		}
	}
	return out, nil
}

var agyWindows = map[string]int64{"5h": 5 * 60, "weekly": 7 * 24 * 60}

// agyUsageFrom lays the buckets out as a plan's limits: Gemini's five-hour
// and weekly ones as the plan's own, the rest - the Claude and GPT models'
// shared allowance, Gemini's metered-apart models - each under its name.
func agyUsageFrom(buckets []agyBucket, now time.Time) *Usage {
	u := &Usage{}
	for _, b := range buckets {
		if b.Disabled || b.Remaining == nil || math.IsNaN(*b.Remaining) {
			continue
		}
		window := agyWindows[b.Window]
		l := &Limit{Percent: (1 - math.Max(0, math.Min(1, *b.Remaining))) * 100, Window: window, ResetsAt: b.ResetTime}
		// Untouched, a bucket reports a whole window from now, which is no reset.
		if reset, err := time.Parse(time.RFC3339, b.ResetTime); err == nil && *b.Remaining == 1 && b.Description == nil && window > 0 &&
			reset.Sub(now.Add(time.Duration(window)*time.Minute)).Abs() <= time.Minute {
			l.ResetsAt = ""
		}
		family := ""
		switch {
		case strings.HasPrefix(b.ID, "gemini-argon-"):
			family = cmpStr(b.group, "Gemini Argon")
		case strings.HasPrefix(b.ID, "gemini-carbon-"):
			family = cmpStr(b.group, "Gemini Carbon")
		case strings.HasPrefix(b.ID, "gemini-"):
		case strings.HasPrefix(b.ID, "3p-"):
			family = "Claude/GPT"
		default:
			family = cmpStr(b.group, b.ID)
		}
		switch {
		case family == "" && b.Window == "5h" && u.Session == nil:
			u.Session = l
		case family == "" && b.Window == "weekly" && u.Week == nil:
			u.Week = l
		case *b.Remaining >= 1:
			// An allowance apart that hasn't been touched is left off, as most go unused.
		default:
			span := map[string]string{"5h": " 5h", "weekly": " 7d"}[b.Window]
			l.Model = cmpStr(family, "Gemini") + span
			u.Models = append(u.Models, l)
		}
	}
	if u.Session == nil && u.Week == nil && len(u.Models) == 0 {
		return nil
	}
	return u
}
