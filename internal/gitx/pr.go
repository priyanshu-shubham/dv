package gitx

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// PR is the pull request a branch is the head of, as GitHub's CLI finds it.
type PR struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
	State  string `json:"state"`            // open, draft, merged or closed
	Checks string `json:"checks,omitempty"` // passing, failing or pending; "" for none
	Review string `json:"review,omitempty"` // approved, changes or required
}

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
	pr := askGH(r.Root, branch)
	prMu.Lock()
	prCache[key] = prAnswer{pr, time.Now()}
	prMu.Unlock()
	return pr
}

func askGH(dir, branch string) *PR {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", branch, "--json", "number,title,url,state,isDraft,reviewDecision,statusCheckRollup")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parsePR(out)
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
		StatusCheckRollup []struct{ Status, Conclusion, State string }
	}
	if json.Unmarshal(out, &v) != nil || v.Number == 0 {
		return nil
	}
	pr := &PR{Number: v.Number, Title: v.Title, URL: v.URL, State: strings.ToLower(v.State)}
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
