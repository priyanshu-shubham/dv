package gitx

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// PR is a pull request, as GitHub's CLI finds it.
type PR struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	State  string `json:"state"`            // open, draft, merged or closed
	Checks string `json:"checks,omitempty"` // passing, failing or pending; "" for none
	Review string `json:"review,omitempty"` // approved, changes or required
	Base   string `json:"base,omitempty"`   // the branch it would merge into
	// The base's tip as GitHub has it, which the trunk here may be ahead of
	// or behind.
	BaseSHA string `json:"baseSha,omitempty"`
	Head    string `json:"head,omitempty"`
	Author  string `json:"author,omitempty"`
	// Where its branch is checked out: this folder, or another worktree.
	Here      bool   `json:"here,omitempty"`
	Elsewhere string `json:"elsewhere,omitempty"`

	headRepo string // owner/name, for a fork's
	fork     bool
}

const (
	prFields   = "number,title,url,state,isDraft,reviewDecision,statusCheckRollup,baseRefName,baseRefOid,headRefName"
	headFields = "isCrossRepository,headRepositoryOwner,headRepository"
)

// Asked of GitHub each time a page reads its git details, the answer is kept
// a while; one of none too, so a branch with no PR costs one call a spell.
const prFresh = 3 * time.Minute

type prAnswer struct {
	pr *PR
	at time.Time
}

var (
	prMu    sync.Mutex
	prCache = map[string]prAnswer{}
)

// PullRequest is the current branch's pull request, nil for none: none too on
// a detached HEAD, the trunk, a remote that is not a hosted one, or with no
// `gh` signed in to ask.
func (r *Repo) PullRequest() *PR {
	branch := r.Head().Branch
	if branch == "" || branch == r.DefaultBranch() {
		return nil
	}
	if o := r.Origin(); o == nil || o.Web == "" {
		return nil
	}
	key := r.Root + "\x00" + branch
	prMu.Lock()
	a, ok := prCache[key]
	prMu.Unlock()
	if ok && time.Since(a.at) < prFresh {
		return a.pr
	}
	var pr *PR
	// Unnamed, gh goes by what the branch tracks, which finds a fork's pull
	// request `gh pr checkout` made a branch for; by name it would not.
	if out, err := runGH(r.Root, 15*time.Second, "pr", "view", "--json", prFields); err == nil {
		pr = parsePR(out)
	}
	// The branch's diff is measured from the base's tip, so it has to be here.
	if pr != nil && pr.BaseSHA != "" && !r.hasCommit(pr.BaseSHA) {
		gitIn(r.Root, time.Minute, "fetch", "--quiet", "--no-tags", "--", r.remoteFor(pr.URL), "refs/heads/"+pr.Base)
	}
	prMu.Lock()
	prCache[key] = prAnswer{pr, time.Now()}
	prMu.Unlock()
	return pr
}

// knownPR is the current branch's open pull request as last heard, however
// long ago, without asking GitHub.
func (r *Repo) knownPR() *PR {
	branch := r.Head().Branch
	if branch == "" {
		return nil
	}
	prMu.Lock()
	pr := prCache[r.Root+"\x00"+branch].pr
	prMu.Unlock()
	if pr == nil || pr.State != "open" && pr.State != "draft" {
		return nil
	}
	return pr
}

// PullRequests lists the repository's open pull requests, the latest first,
// each with where its branch is checked out.
func (r *Repo) PullRequests() ([]*PR, error) {
	out, err := runGH(r.Root, 30*time.Second, "pr", "list", "--limit", "50", "--json",
		"number,title,url,state,isDraft,baseRefName,headRefName,author,"+headFields)
	if err != nil {
		return nil, err
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, err
	}
	prs := []*PR{}
	for _, one := range raw {
		if pr := parsePR(one); pr != nil {
			prs = append(prs, pr)
		}
	}
	r.locate(prs)
	return prs, nil
}

// prRef is where a reviewed pull request's head or base is kept: dv's own
// refs, so reading one checks nothing out and leaves every branch as it is.
func prRef(n int, which string) string { return "refs/dv/pr/" + strconv.Itoa(n) + "/" + which }

// FetchPR fetches pull request arg - a number, a URL or a branch - to be
// read: its head, and its base's tip, which the "pr" scope measures it from.
func (r *Repo) FetchPR(arg string) (*PR, error) {
	arg = strings.TrimPrefix(strings.TrimSpace(arg), "#")
	if arg == "" || strings.HasPrefix(arg, "-") {
		return nil, errors.New("name a pull request")
	}
	out, err := runGH(r.Root, 30*time.Second, "pr", "view", arg, "--json", prFields+","+headFields)
	if err != nil {
		return nil, err
	}
	pr := parsePR(out)
	if pr == nil {
		return nil, fmt.Errorf("no pull request %s", arg)
	}
	n := strconv.Itoa(pr.Number)
	// A fork's branch is not on the base repository, but GitHub keeps every
	// pull request's head there as refs/pull/<n>/head.
	if _, err := gitIn(r.Root, 2*time.Minute, "fetch", "--quiet", "--no-tags", "--", r.remoteFor(pr.URL),
		"+refs/pull/"+n+"/head:"+prRef(pr.Number, "head"), "+refs/heads/"+pr.Base+":"+prRef(pr.Number, "base")); err != nil {
		return nil, fmt.Errorf("could not fetch #%s: %w", n, err)
	}
	r.locate([]*PR{pr})
	return pr, nil
}

// remoteNamed is the remote of the repository a pull request is on, by its
// page; "" when none here is.
func (r *Repo) remoteNamed(prURL string) string {
	repo, _, _ := strings.Cut(prURL, "/pull/")
	for _, x := range r.remotes() {
		if repo != "" && strings.EqualFold(x.Web, repo) {
			return x.Name
		}
	}
	return ""
}

// remoteFor is what to fetch a pull request from: its repository's remote,
// else the repository's URL.
func (r *Repo) remoteFor(prURL string) string {
	if name := r.remoteNamed(prURL); name != "" {
		return name
	}
	repo, _, _ := strings.Cut(prURL, "/pull/")
	return repo + ".git"
}

func (r *Repo) remotes() []Remote {
	out, _ := r.run("config", "--get-regexp", `^remote\..*\.url$`)
	var rs []Remote
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if key, u, ok := strings.Cut(line, " "); ok {
			label, web := describeRemote(u)
			rs = append(rs, Remote{Name: strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".url"), URL: u, Label: label, Web: web})
		}
	}
	return rs
}

// locate fills in where each pull request's branch is checked out. A local
// branch is taken for one when it tracks it - by its pull ref, as `gh pr
// checkout` leaves a fork's, or its branch on the repository it comes from -
// or, for one not from a fork, is just named for it.
func (r *Repo) locate(prs []*PR) {
	at := r.checkouts()
	if len(at) == 0 {
		return
	}
	cfg, _ := r.run("config", "--get-regexp", `^branch\..*\.(merge|remote)$`)
	merge, remote := map[string]string{}, map[string]string{}
	for _, line := range strings.Split(cfg, "\n") {
		key, v, _ := strings.Cut(line, " ")
		if b, ok := strings.CutSuffix(strings.TrimPrefix(key, "branch."), ".merge"); ok {
			merge[b] = v
		} else if b, ok := strings.CutSuffix(strings.TrimPrefix(key, "branch."), ".remote"); ok {
			remote[b] = v
		}
	}
	urls := map[string]string{}
	for _, x := range r.remotes() {
		urls[x.Name] = x.URL
	}
	headRepo := func(pr *PR) string {
		if pr.fork {
			return pr.headRepo
		}
		repo, _, _ := strings.Cut(pr.URL, "/pull/")
		if u, err := url.Parse(repo); err == nil {
			return strings.Trim(u.Path, "/")
		}
		return ""
	}
	for _, pr := range prs {
		for ref, dir := range at {
			b := strings.TrimPrefix(ref, "refs/heads/")
			// A branch's remote is a remote's name, or a URL as gh leaves for a fork.
			label, _ := describeRemote(cmp.Or(urls[remote[b]], remote[b]))
			ok := merge[b] == "refs/pull/"+strconv.Itoa(pr.Number)+"/head" ||
				merge[b] == "refs/heads/"+pr.Head && strings.HasSuffix(strings.ToLower(label), "/"+strings.ToLower(headRepo(pr))) ||
				!pr.fork && b == pr.Head && merge[b] == ""
			if !ok {
				continue
			}
			if dir == r.Root {
				pr.Here, pr.Elsewhere = true, ""
				break
			}
			pr.Elsewhere = dir
		}
	}
}

func (r *Repo) hasCommit(sha string) bool {
	_, err := r.run("cat-file", "-e", sha+"^{commit}")
	return err == nil
}

// runGH runs GitHub's CLI in dir, failing with what it said.
func runGH(dir string, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return nil, errors.New("GitHub's CLI, gh, is not installed")
	case ctx.Err() != nil:
		return nil, errors.New("GitHub took too long to answer")
	case err != nil:
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, err
	}
	return out, nil
}

var failedCheck = map[string]bool{"FAILURE": true, "ERROR": true, "TIMED_OUT": true, "CANCELLED": true, "ACTION_REQUIRED": true, "STARTUP_FAILURE": true}

func parsePR(out []byte) *PR {
	var v struct {
		Number         int
		Title          string
		URL            string
		State          string
		IsDraft        bool
		ReviewDecision string
		// A CheckRun has a status and conclusion, a commit status a state.
		StatusCheckRollup   []struct{ Status, Conclusion, State string }
		BaseRefName         string
		BaseRefOid          string
		HeadRefName         string
		Author              struct{ Login string }
		IsCrossRepository   bool
		HeadRepositoryOwner struct{ Login string }
		HeadRepository      struct{ Name string }
	}
	if json.Unmarshal(out, &v) != nil || v.Number == 0 {
		return nil
	}
	pr := &PR{
		Number: v.Number, Title: v.Title, URL: v.URL, State: strings.ToLower(v.State),
		Base: v.BaseRefName, BaseSHA: v.BaseRefOid, Head: v.HeadRefName, Author: v.Author.Login,
		fork: v.IsCrossRepository,
	}
	if v.IsCrossRepository && v.HeadRepositoryOwner.Login != "" {
		pr.headRepo = v.HeadRepositoryOwner.Login + "/" + v.HeadRepository.Name
	}
	if pr.State == "open" && v.IsDraft {
		pr.State = "draft"
	}
	pr.Review = map[string]string{"APPROVED": "approved", "CHANGES_REQUESTED": "changes", "REVIEW_REQUIRED": "required"}[v.ReviewDecision]
	for _, c := range v.StatusCheckRollup {
		switch {
		case failedCheck[c.Conclusion] || failedCheck[c.State]:
			pr.Checks = "failing"
		case pr.Checks == "failing":
		case c.State == "PENDING" || c.State == "EXPECTED" || c.Status != "" && c.Status != "COMPLETED":
			pr.Checks = "pending"
		case pr.Checks == "":
			pr.Checks = "passing"
		}
	}
	return pr
}
