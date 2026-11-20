package gateway

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// GeminiRoute is how Gemini models are reached, as the user set it: with
// the Gemini API key, or through Vertex AI with their Google Cloud login.
type GeminiRoute struct {
	Vertex   bool
	Project  string // "" for gcloud's own
	Location string // global, us or eu
}

var routeSource atomic.Pointer[func() GeminiRoute]

// RouteGemini has the gateway read the user's choice of route from route.
func RouteGemini(route func() GeminiRoute) { routeSource.Store(&route) }

func currentRoute() GeminiRoute {
	if f := routeSource.Load(); f != nil {
		return (*f)()
	}
	return GeminiRoute{}
}

// geminiAccess is the route as it stands now, with what it needs found.
type geminiAccess struct {
	key               string
	vertex            bool
	project, location string
	err               error // why it can't be used
}

// id is what a model list is of: listed again when it changes.
func (a geminiAccess) id() string {
	if a.vertex {
		return "vertex:" + a.project + "/" + a.location
	}
	return "key:" + a.key
}

var errNoProject = errors.New("there is no Google Cloud project for Vertex AI: name one here, or set one with gcloud config set project")

// accessError is the route not there to use, which retrying won't change.
type accessError struct{ error }

func (e accessError) Unwrap() error { return e.error }

func (c *geminiUpstream) access() geminiAccess {
	r := GeminiRoute{}
	if c.route != nil {
		r = c.route()
	}
	if !r.Vertex {
		a := geminiAccess{key: c.key()}
		if a.key == "" {
			a.err = errNoGeminiKey
		}
		return a
	}
	a := geminiAccess{vertex: true, project: r.Project, location: cmp.Or(r.Location, "global")}
	if a.project == "" {
		a.project = c.login.defaultProject()
	}
	if a.project == "" {
		a.err = errNoProject
	}
	return a
}

// interactions is where a turn is posted. Vertex's multi-regions are hosts
// of their own; its single regions don't have the Interactions API.
func (c *geminiUpstream) interactions(a geminiAccess) string {
	if !a.vertex {
		return c.base + "/interactions"
	}
	host := "https://aiplatform.googleapis.com"
	if a.location != "global" {
		host = "https://aiplatform." + a.location + ".rep.googleapis.com"
	}
	host = cmp.Or(c.vertexHost, host)
	return host + "/v1/projects/" + url.PathEscape(a.project) + "/locations/" + url.PathEscape(a.location) + "/interactions"
}

func (c *geminiUpstream) authorize(req *http.Request, a geminiAccess) error {
	if !a.vertex {
		req.Header.Set("x-goog-api-key", a.key)
		return nil
	}
	token, err := c.login.token()
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	// A gcloud user login has no project of its own to count usage against,
	// which Model Garden's list refuses.
	req.Header.Set("x-goog-user-project", a.project)
	return nil
}

// googleLogin is the user's Google Cloud login, as Google's client libraries
// find it: gcloud auth application-default login, a service account's file
// named by GOOGLE_APPLICATION_CREDENTIALS, or the machine's own on Google Cloud.
type googleLogin struct {
	mu       sync.Mutex
	src      oauth2.TokenSource
	project  string
	err      error
	lookedAt time.Time
}

func (l *googleLogin) find() (oauth2.TokenSource, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.src == nil && time.Since(l.lookedAt) >= 10*time.Second {
		l.lookedAt = time.Now()
		// Not a request's context: the source refreshes with it long after.
		creds, err := google.FindDefaultCredentials(context.Background(), "https://www.googleapis.com/auth/cloud-platform")
		if err != nil {
			l.err = errors.New("there is no Google Cloud login for Vertex AI: run gcloud auth application-default login")
		} else {
			l.src, l.project, l.err = creds.TokenSource, creds.ProjectID, nil
		}
	}
	return l.src, l.err
}

func (l *googleLogin) token() (string, error) {
	src, err := l.find()
	if src == nil {
		return "", err
	}
	t, err := src.Token()
	if err != nil {
		l.mu.Lock()
		l.src = nil
		l.mu.Unlock()
		return "", fmt.Errorf("your Google Cloud login was refused, so run gcloud auth application-default login again: %w", err)
	}
	return t.AccessToken, nil
}

// defaultProject is the project Vertex AI is used in when none is named:
// the environment's, a service account's own, else the one gcloud is set to.
func (l *googleLogin) defaultProject() string {
	if p := cmp.Or(os.Getenv("GOOGLE_CLOUD_PROJECT"), os.Getenv("CLOUDSDK_CORE_PROJECT")); p != "" {
		return p
	}
	l.find()
	l.mu.Lock()
	p := l.project
	l.mu.Unlock()
	return cmp.Or(p, gcloudProject())
}

func gcloudProject() string {
	dir := os.Getenv("CLOUDSDK_CONFIG")
	if dir == "" {
		if runtime.GOOS == "windows" {
			dir = filepath.Join(os.Getenv("APPDATA"), "gcloud")
		} else if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, ".config", "gcloud")
		}
	}
	name := "default"
	if b, err := os.ReadFile(filepath.Join(dir, "active_config")); err == nil {
		name = cmp.Or(strings.TrimSpace(string(b)), name)
	}
	f, err := os.Open(filepath.Join(dir, "configurations", "config_"+name))
	if err != nil {
		return ""
	}
	defer f.Close()
	section := ""
	for sc := bufio.NewScanner(f); sc.Scan(); {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			section = strings.Trim(line, "[]")
		} else if k, v, ok := strings.Cut(line, "="); ok && section == "core" && strings.TrimSpace(k) == "project" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// vertexWindow is every Gemini model's on Vertex, which lists none.
const vertexWindow = 1048576

// fetchVertexModels lists the Gemini models Model Garden has, after asking
// the project's interactions - free - so a project Vertex AI can't be used
// in says so here, not in a session.
func (c *geminiUpstream) fetchVertexModels(ctx context.Context, a geminiAccess) ([]Model, error) {
	get := func(u string) ([]byte, error) {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err := c.authorize(req, a); err != nil {
			return nil, err
		}
		resp, err := c.client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		if err == nil && resp.StatusCode != http.StatusOK {
			_, msg := geminiFailure(b)
			err = fmt.Errorf("Vertex AI: %s", cmp.Or(msg, resp.Status))
		}
		return b, err
	}
	if _, err := get(c.interactions(a)); err != nil {
		return nil, err
	}
	var out []Model
	page := ""
	for {
		b, err := get(cmp.Or(c.vertexHost, "https://aiplatform.googleapis.com") + "/v1beta1/publishers/google/models?pageSize=300&pageToken=" + url.QueryEscape(page))
		if err != nil {
			return nil, err
		}
		var list struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"publisherModels"`
			Next string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(b, &list); err != nil {
			return nil, err
		}
		for _, m := range list.Models {
			slug := m.Name[strings.LastIndex(m.Name, "/")+1:]
			v := geminiVersion.FindStringSubmatch(slug)
			if v == nil || notForCode.MatchString(slug) {
				continue
			}
			// Vertex's Interactions API refuses 3.0 (gemini-3-flash-preview).
			if n, _ := strconv.ParseFloat(v[1], 64); n < 3.1 {
				continue
			}
			out = append(out, geminiModel(slug, geminiLabel(slug), vertexWindow))
		}
		if page = list.Next; page == "" {
			break
		}
	}
	geminiNewestFirst(out)
	return out, nil
}

// geminiLabel names a model as Google does where its list gives no names:
// gemini-3.1-pro-preview is Gemini 3.1 Pro Preview.
func geminiLabel(slug string) string {
	words := strings.Split(slug, "-")
	for i, w := range words {
		if w != "" {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}
