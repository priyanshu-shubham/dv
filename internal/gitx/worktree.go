package gitx

import (
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// Remote is where a repository came from, as a hub's card shows it: Label is
// host/owner/repo, and Web the page a browser opens for it, when there is one.
type Remote struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Label string `json:"label"`
	Web   string `json:"web,omitempty"`
}

// Origin is the remote named origin, else the first there is; nil for none.
func (r *Repo) Origin() *Remote {
	if !r.IsGit() {
		return nil
	}
	out, err := r.run("config", "--get-regexp", `^remote\..*\.url$`)
	if err != nil {
		return nil
	}
	var found *Remote
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		key, u, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".url")
		if found == nil || name == "origin" {
			label, web := describeRemote(u)
			found = &Remote{Name: name, URL: u, Label: label, Web: web}
		}
		if name == "origin" {
			break
		}
	}
	return found
}

// describeRemote shortens a remote's URL - https, ssh, or scp-like
// git@host:owner/repo - to host/owner/repo, with the https page it most likely
// has. A path on disk stays as it is, with no page.
func describeRemote(raw string) (label, web string) {
	var host, path string
	if u, err := url.Parse(raw); err == nil && u.Host != "" && (u.Scheme == "https" || u.Scheme == "http" || u.Scheme == "ssh" || u.Scheme == "git") {
		host, path = u.Hostname(), u.Path
	} else if at, rest, ok := strings.Cut(raw, ":"); ok && !strings.Contains(raw, "://") && !strings.Contains(at, "/") && len(at) > 1 {
		host, path = at[strings.LastIndex(at, "@")+1:], rest
	} else {
		return raw, ""
	}
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	label = host + "/" + path
	if strings.Contains(host, ".") && strings.Count(path, "/") >= 1 {
		web = "https://" + label
	}
	return label, web
}

// Status is where a checkout stands, as a hub's card shows it.
type Status struct {
	Branch    string `json:"branch"`             // the short SHA when detached
	Upstream  string `json:"upstream,omitempty"` // what the branch tracks
	Ahead     int    `json:"ahead,omitempty"`
	Behind    int    `json:"behind,omitempty"`
	Staged    int    `json:"staged,omitempty"`
	Unstaged  int    `json:"unstaged,omitempty"`
	Untracked int    `json:"untracked,omitempty"`
	Conflicts int    `json:"conflicts,omitempty"`
}

// Status is one `git status`, for everything a card says about the checkout.
func (r *Repo) Status() (*Status, error) {
	out, err := r.run("--no-optional-locks", "status", "--porcelain=v2", "--branch", "--untracked-files=normal")
	if err != nil {
		return nil, err
	}
	var s Status
	var oid string
	for _, l := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(l, "# branch.oid "):
			oid = strings.TrimPrefix(l, "# branch.oid ")
		case strings.HasPrefix(l, "# branch.head "):
			if head := strings.TrimPrefix(l, "# branch.head "); head != "(detached)" {
				s.Branch = head
			}
		case strings.HasPrefix(l, "# branch.upstream "):
			s.Upstream = strings.TrimPrefix(l, "# branch.upstream ")
		case strings.HasPrefix(l, "# branch.ab "):
			fmt.Sscanf(strings.TrimPrefix(l, "# branch.ab "), "+%d -%d", &s.Ahead, &s.Behind)
		case strings.HasPrefix(l, "? "):
			s.Untracked++
		case strings.HasPrefix(l, "u "):
			s.Conflicts++
		case strings.HasPrefix(l, "1 "), strings.HasPrefix(l, "2 "):
			// "1 XY ...": X is the index against HEAD, Y the tree against the index.
			if len(l) > 3 && l[2] != '.' {
				s.Staged++
			}
			if len(l) > 3 && l[3] != '.' {
				s.Unstaged++
			}
		}
	}
	if s.Branch == "" && len(oid) >= 7 && oid != "(initial)" {
		s.Branch = oid[:7]
	}
	return &s, nil
}

// Uncommitted lists what `git status --short` would: changed files and
// untracked ones, not ignored ones. It is what stops `git worktree remove`.
func (r *Repo) Uncommitted() ([]string, error) {
	out, err := r.run("--no-optional-locks", "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines, nil
}

// ValidBranch is an error when git would not take name for a branch.
func (r *Repo) ValidBranch(name string) error {
	if _, err := r.run("check-ref-format", "--branch", name); err != nil || strings.HasPrefix(name, "-") {
		return fmt.Errorf("%q is not a name git takes for a branch", name)
	}
	return nil
}

// HasBranch is whether a local branch is called name.
func (r *Repo) HasBranch(name string) bool {
	_, err := r.run("rev-parse", "--verify", "--quiet", "refs/heads/"+name)
	return err == nil
}

// HasRemoteBranch is whether a remote has a branch called name, which `git
// worktree add` checks out as a new branch tracking it.
func (r *Repo) HasRemoteBranch(name string) bool {
	return slices.Contains(r.RemoteBranches(), name)
}

// RemoteBranches names the remotes' branches as a local one tracking them would
// be named: origin/fix/login as fix/login.
func (r *Repo) RemoteBranches() []string {
	out, err := r.run("for-each-ref", "--format=%(refname:lstrip=3)", "refs/remotes")
	if err != nil {
		return nil
	}
	var names []string
	for _, n := range strings.Fields(out) {
		if n != "HEAD" && !slices.Contains(names, n) {
			names = append(names, n)
		}
	}
	return names
}
