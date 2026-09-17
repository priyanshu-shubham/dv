package hub

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"dv/internal/gitx"
	"dv/internal/server"
	"dv/internal/store"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func repo(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(path, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(path, "sub", "a.txt"), []byte("a\n"), 0o644)
	git(t, path, "init", "-q", "-b", "main")
	git(t, path, "add", ".")
	git(t, path, "commit", "-qm", "first")
}

type client struct {
	t   *testing.T
	url string
}

// do sends a request and decodes a JSON answer into out, if given.
func (c client) do(method, path string, body string, out any) *http.Response {
	c.t.Helper()
	req, _ := http.NewRequest(method, c.url+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if out != nil {
		// Decoded afresh: a field the answer leaves out must not keep the last one's.
		reflect.ValueOf(out).Elem().SetZero()
		if err := json.Unmarshal(b, out); err != nil {
			c.t.Fatalf("%s %s: %v\n%s", method, path, err, b)
		}
	}
	resp.Body = io.NopCloser(strings.NewReader(string(b)))
	return resp
}

type folder struct {
	Slug       string         `json:"slug"`
	Path       string         `json:"path"`
	Name       string         `json:"name"`
	WorktreeOf string         `json:"worktreeOf"`
	Open       *server.Status `json:"open"`
	Elsewhere  string         `json:"elsewhere"`
	Remote     *gitx.Remote   `json:"remote"`
	Status     *gitx.Status   `json:"status"`
}

type listing struct {
	Folders []folder `json:"folders"`
	Jobs    []job    `json:"jobs"`
}

func (l listing) folder(t *testing.T, path string) folder {
	t.Helper()
	for _, f := range l.Folders {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("%s is not listed: %+v", path, l.Folders)
	return folder{}
}

// settle waits for every job to finish or fail.
func (c client) settle() listing {
	c.t.Helper()
	var got listing
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		c.do("GET", "/api/hub/folders", "", &got)
		busy := false
		for _, j := range got.Jobs {
			busy = busy || j.Error == ""
		}
		if !busy {
			return got
		}
		if time.Now().After(deadline) {
			c.t.Fatalf("jobs did not finish: %+v", got.Jobs)
		}
	}
}

func TestHub(t *testing.T) {
	home, _ := filepath.EvalSymlinks(t.TempDir())
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join(home, ".claude"))
	alpha, beta := filepath.Join(home, "code", "alpha"), filepath.Join(home, "code", "beta")
	repo(t, alpha)
	repo(t, beta)

	user, err := store.OpenUserPrefs()
	if err != nil {
		t.Fatal(err)
	}
	list, err := store.OpenFolders()
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(nil)
	h := New("http://"+ts.Listener.Addr().String(), user, list)
	ts.Config.Handler = h.Handler()
	ts.Start()
	defer ts.Close()
	defer h.Close()
	c := client{t, ts.URL}

	// A folder inside a repository adds the repository.
	var added store.Folder
	if r := c.do("POST", "/api/hub/folders", `{"path": "~/code/alpha/sub"}`, &added); r.StatusCode != 200 || added.Path != alpha || added.Slug != "alpha" {
		t.Fatalf("add: %d %+v", r.StatusCode, added)
	}

	if r := c.do("GET", "/alpha", "", nil); r.StatusCode != http.StatusFound || r.Header.Get("Location") != "/alpha/" {
		t.Fatalf("/alpha: %d to %q", r.StatusCode, r.Header.Get("Location"))
	}
	r := c.do("GET", "/alpha/", "", nil)
	page, _ := io.ReadAll(r.Body)
	if r.StatusCode != 200 || !strings.Contains(string(page), `"base":"/alpha"`) {
		t.Fatalf("/alpha/: %d\n%s", r.StatusCode, page)
	}
	var ping struct{ Root string }
	c.do("GET", "/alpha/api/ping", "", &ping)
	if ping.Root != alpha {
		t.Fatalf("ping answered %q", ping.Root)
	}
	if s, ok := store.Announced(alpha); !ok || s.URL != ts.URL+"/alpha" {
		t.Fatalf("alpha announced as %+v", s)
	}

	// Beta is open in a dv of its own, which keeps it.
	own, err := server.Open(beta, user)
	if err != nil {
		t.Fatal(err)
	}
	defer own.Close()
	lone := httptest.NewServer(own.Handler(""))
	defer lone.Close()
	unannounce, _ := store.Announce(beta, lone.URL)
	c.do("POST", "/api/hub/folders", `{"path": "`+beta+`"}`, &added)
	if r := c.do("GET", "/beta/", "", nil); r.StatusCode != http.StatusFound || !strings.Contains(r.Header.Get("Location"), "from=beta") {
		t.Fatalf("/beta/ while open elsewhere: %d to %q", r.StatusCode, r.Header.Get("Location"))
	}
	var got listing
	c.do("GET", "/api/hub/folders", "", &got)
	if got.folder(t, alpha).Open == nil || got.folder(t, beta).Elsewhere != lone.URL {
		t.Fatalf("folders: %+v", got.Folders)
	}
	// Once that dv stops, its record is only a record.
	lone.Close()
	c.do("GET", "/api/hub/folders", "", &got)
	if got.folder(t, beta).Elsewhere != "" {
		t.Fatalf("a stopped dv still counts: %+v", got.folder(t, beta))
	}
	unannounce()

	// Cloning runs on its own, as the name asked for, then lists the folder.
	bare := filepath.Join(home, "remote", "gamma.git")
	git(t, home, "clone", "-q", "--bare", alpha, bare)
	var started job
	if r := c.do("POST", "/api/hub/clones", `{"source": "`+bare+`", "into": "~/code", "name": "delta"}`, &started); r.StatusCode != 200 {
		t.Fatalf("clone: %d %+v", r.StatusCode, started)
	}
	delta := filepath.Join(home, "code", "delta")
	if started.Path != delta {
		t.Fatalf("cloning into %q, want %q", started.Path, delta)
	}
	if got = c.settle(); len(got.Jobs) != 0 {
		t.Fatalf("clone failed: %+v", got.Jobs)
	}
	got.folder(t, delta)
	if r := c.do("POST", "/api/hub/clones", `{"source": "`+bare+`", "into": "~/code", "name": "delta"}`, &started); r.StatusCode != http.StatusConflict {
		t.Fatalf("cloning over an existing folder: %d", r.StatusCode)
	}
	if r := c.do("POST", "/api/hub/clones", `{"source": "`+bare+`", "into": "~/code", "name": "../out"}`, &started); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("cloning as a path: %d", r.StatusCode)
	}

	// A failed clone says why and stays until dismissed.
	c.do("POST", "/api/hub/clones", `{"source": "`+filepath.Join(home, "nothing.git")+`", "into": "~/code"}`, &started)
	if got = c.settle(); len(got.Jobs) != 1 || got.Jobs[0].Error == "" {
		t.Fatalf("clone of nothing: %+v", got.Jobs)
	}
	c.do("DELETE", "/api/hub/jobs/"+started.ID, "", nil)
	if c.do("GET", "/api/hub/folders", "", &got); len(got.Jobs) != 0 {
		t.Fatal("dismissed clone still listed")
	}

	// The remote and git's status come with details, and a name sticks.
	git(t, alpha, "remote", "add", "origin", "git@github.com:you/alpha.git")
	os.WriteFile(filepath.Join(alpha, "sub", "a.txt"), []byte("changed\n"), 0o644)
	os.WriteFile(filepath.Join(alpha, "new.txt"), nil, 0o644)
	c.do("GET", "/api/hub/folders?details=1", "", &got)
	if f := got.folder(t, alpha); f.Remote == nil || f.Remote.Label != "github.com/you/alpha" || f.Remote.Web != "https://github.com/you/alpha" {
		t.Fatalf("alpha's remote: %+v", f.Remote)
	}
	if s := got.folder(t, alpha).Status; s == nil || s.Branch != "main" || s.Unstaged != 1 || s.Untracked != 1 || s.Staged != 0 {
		t.Fatalf("alpha's status: %+v", s)
	}
	git(t, alpha, "checkout", "-q", "--", "sub/a.txt")
	os.Remove(filepath.Join(alpha, "new.txt"))
	c.do("PATCH", "/api/hub/folders/alpha", `{"name": "Alpha app"}`, nil)
	if c.do("GET", "/api/hub/folders", "", &got); got.folder(t, alpha).Name != "Alpha app" {
		t.Fatalf("renamed: %+v", got.folder(t, alpha))
	}

	// A worktree, beside the repository, set up by the hook.
	setup := `echo "$DV_REPO $DV_BRANCH" > made-by-hook; test -z "$FAIL"`
	tornDown := filepath.Join(home, "torn-down")
	body, _ := json.Marshal(map[string]string{"setup": setup, "teardown": `touch "` + tornDown + `"; test ! -e keep`})
	c.do("PATCH", "/api/hub/folders/alpha", string(body), nil)
	if r := c.do("POST", "/api/hub/folders/alpha/worktrees", `{"branch": "fix/login"}`, &started); r.StatusCode != 200 {
		t.Fatalf("worktree: %d %+v", r.StatusCode, started)
	}
	wt := filepath.Join(home, "code", "alpha-fix-login")
	if got = c.settle(); len(got.Jobs) != 0 {
		t.Fatalf("worktree failed: %+v", got.Jobs)
	}
	f := got.folder(t, wt)
	if f.WorktreeOf != alpha {
		t.Fatalf("worktree listed as %+v", f)
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "made-by-hook")); string(b) != alpha+" fix/login\n" {
		t.Fatalf("the setup hook wrote %q", b)
	}
	// Its review's notes are ignored where git looks, which a worktree shares.
	c.do("GET", "/"+f.Slug+"/", "", nil)
	os.WriteFile(filepath.Join(wt, "made-by-hook"), nil, 0o644)
	if out, _ := exec.Command("git", "-C", wt, "status", "--porcelain", "--untracked-files=all").Output(); strings.Contains(string(out), ".dv") {
		t.Fatalf("the worktree's .dv shows in git status:\n%s", out)
	}
	os.Remove(filepath.Join(wt, "made-by-hook"))

	// A failing setup hook keeps the worktree, and runs again when asked.
	t.Setenv("FAIL", "1")
	c.do("POST", "/api/hub/folders/"+f.Slug+"/setup", "{}", &started)
	if got = c.settle(); len(got.Jobs) != 1 || got.Jobs[0].Retry != "setup" || !strings.Contains(got.Jobs[0].Error, "setup hook failed") {
		t.Fatalf("failed setup: %+v", got.Jobs)
	}
	os.Unsetenv("FAIL")
	c.do("POST", "/api/hub/folders/"+f.Slug+"/setup", "{}", &started)
	if got = c.settle(); len(got.Jobs) != 0 {
		t.Fatalf("setup again: %+v", got.Jobs)
	}

	// Uncommitted changes keep it, before the teardown runs or the review closes.
	c.do("POST", "/api/hub/folders/"+f.Slug+"/delete", "{}", &started)
	if got = c.settle(); len(got.Jobs) != 1 || got.Jobs[0].Retry != "force" || !strings.Contains(got.Jobs[0].Error, "?? made-by-hook") {
		t.Fatalf("deleting with changes: %+v", got.Jobs)
	}
	if _, err := os.Stat(tornDown); err == nil {
		t.Fatal("the teardown ran for a worktree that stays")
	}
	if got.folder(t, wt).Open == nil {
		t.Fatal("refusing to delete closed the review")
	}

	// A teardown that fails keeps it; skipped, git removes it and the branch stays.
	os.WriteFile(filepath.Join(wt, "keep"), nil, 0o644)
	git(t, wt, "add", "keep", "made-by-hook")
	git(t, wt, "commit", "-qm", "keep")
	c.do("POST", "/api/hub/folders/"+f.Slug+"/delete", "{}", &started)
	if got = c.settle(); len(got.Jobs) != 1 || got.Jobs[0].Retry != "remove" {
		t.Fatalf("failed teardown: %+v", got.Jobs)
	}
	if _, err := os.Stat(tornDown); err != nil {
		t.Fatal("the teardown did not run")
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatal("a failed teardown deleted the worktree")
	}
	c.do("POST", "/api/hub/folders/"+f.Slug+"/delete", `{"skipTeardown": true}`, &started)
	if got = c.settle(); len(got.Jobs) != 0 {
		t.Fatalf("delete: %+v", got.Jobs)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatal("the worktree is still there")
	}
	for _, f := range got.Folders {
		if f.Path == wt {
			t.Fatal("a deleted worktree is still listed")
		}
	}
	if out, _ := exec.Command("git", "-C", alpha, "branch", "--list", "fix/login").Output(); !strings.Contains(string(out), "fix/login") {
		t.Fatal("deleting the worktree took its branch")
	}

	// Forced, one with uncommitted changes goes, and the changes with it.
	c.do("POST", "/api/hub/folders/alpha/worktrees", `{"branch": "spike"}`, &started)
	if got = c.settle(); len(got.Jobs) != 0 {
		t.Fatalf("worktree spike: %+v", got.Jobs)
	}
	spike := filepath.Join(home, "code", "alpha-spike")
	os.WriteFile(filepath.Join(spike, "sub", "a.txt"), []byte("changed\n"), 0o644)
	c.do("POST", "/api/hub/folders/"+got.folder(t, spike).Slug+"/delete", `{"force": true}`, &started)
	if got = c.settle(); len(got.Jobs) != 0 {
		t.Fatalf("forced delete: %+v", got.Jobs)
	}
	if _, err := os.Stat(spike); !os.IsNotExist(err) {
		t.Fatal("a forced delete left the worktree")
	}

	var dirs struct {
		Place string
		Dirs  []dirEntry
	}
	c.do("GET", "/api/hub/dirs?path=~/code", "", &dirs)
	if dirs.Place != "~/code" || len(dirs.Dirs) != 3 || !dirs.Dirs[0].Git {
		t.Fatalf("dirs: %+v", dirs)
	}
	var made struct{ Place string }
	if r := c.do("POST", "/api/hub/dirs", `{"in": "~/code", "name": "fresh"}`, &made); r.StatusCode != 200 || made.Place != "~/code/fresh" {
		t.Fatalf("new folder: %d %+v", r.StatusCode, made)
	}
	if r := c.do("POST", "/api/hub/dirs", `{"in": "~/code", "name": "fresh"}`, nil); r.StatusCode != 200 {
		t.Fatalf("making a folder that is there: %d", r.StatusCode)
	}
	os.WriteFile(filepath.Join(home, "code", "note"), nil, 0o644)
	if r := c.do("POST", "/api/hub/dirs", `{"in": "~/code", "name": "note"}`, nil); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("a folder named as a file: %d", r.StatusCode)
	}
	if r := c.do("POST", "/api/hub/dirs", `{"in": "~/code", "name": "../out"}`, nil); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("a name reaching outside: %d", r.StatusCode)
	}

	// Removing closes the review, and takes down what it announced.
	c.do("DELETE", "/api/hub/folders/alpha", "", nil)
	if _, ok := store.Announced(alpha); ok {
		t.Fatal("alpha's record outlived its removal")
	}
	if r := c.do("GET", "/alpha/api/ping", "", nil); r.StatusCode != http.StatusNotFound {
		t.Fatalf("removed folder still answers: %d", r.StatusCode)
	}
}
