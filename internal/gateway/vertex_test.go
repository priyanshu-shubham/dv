package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

func TestVertex(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("x-goog-api-key") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		paths = append(paths, r.Method+" "+r.URL.Path)
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1beta1/publishers/google/models"):
			io.WriteString(w, `{"publisherModels":[{"name":"publishers/google/models/gemini-4-neon"},{"name":"publishers/google/models/gemini-3.8-flash-tts"},{"name":"publishers/google/models/gemini-3-flash-preview"},{"name":"publishers/google/models/gemini-3.1-pro-preview"},{"name":"publishers/google/models/imagen-5"}]}`)
		case r.Method == http.MethodGet:
			io.WriteString(w, `{}`)
		default:
			io.WriteString(w, geminiTurn)
		}
	}))
	defer srv.Close()

	route := GeminiRoute{Vertex: true, Project: "proj"}
	g := newGeminiUpstream(func() string { return "key unused on Vertex" })
	g.client, g.vertexHost, g.route = srv.Client(), srv.URL, func() GeminiRoute { return route }
	g.login = &googleLogin{src: oauth2.StaticTokenSource(&oauth2.Token{AccessToken: "tok"})}

	models, err := g.fetchVertexModels(t.Context(), g.access())
	if err != nil {
		t.Fatal(err)
	}
	var slugs []string
	for _, m := range models {
		slugs = append(slugs, m.Slug+"="+m.Label)
	}
	if want := []string{"gemini-4-neon=Gemini 4 Neon", "gemini-3.1-pro-preview=Gemini 3.1 Pro Preview"}; !slices.Equal(slugs, want) {
		t.Errorf("models %v, want %v", slugs, want)
	}

	body := `{"model":"gemini/gemini-4-neon","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	g.serve(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)), []byte(body), &masker{})
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "message_stop") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	if last := paths[len(paths)-1]; last != "POST /v1/projects/proj/locations/global/interactions" {
		t.Errorf("posted to %s", last)
	}

	route.Location = "eu"
	if u := (&geminiUpstream{}).interactions(g.access()); u != "https://aiplatform.eu.rep.googleapis.com/v1/projects/proj/locations/eu/interactions" {
		t.Errorf("eu: %s", u)
	}

	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("CLOUDSDK_CORE_PROJECT", "")
	t.Setenv("CLOUDSDK_CONFIG", t.TempDir())
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/nonexistent")
	route.Project = ""
	rec = httptest.NewRecorder()
	g.serve(rec, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body)), []byte(body), &masker{})
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "project") {
		t.Errorf("no project: %d %s", rec.Code, rec.Body)
	}
}
