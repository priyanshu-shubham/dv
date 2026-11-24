package gitx

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// OwnChanges narrows a comparison along a branch to the branch's own work.
// Where it crosses a merge of another line into the branch - the trunk merged
// in to keep up - the old side is taken as it would be with that merge made
// too, so what the merge brought doesn't read as the branch's changes. Where
// that can't be worked out, the comparison is left direct.
func (r *Repo) OwnChanges(s *Scope) {
	if s == nil || s.old.rev == "" || s.old.empty || s.old.index || s.new.index {
		return
	}
	end := s.new.rev
	if s.new.worktree {
		end = "HEAD"
	}
	// The newest merge on the way holds what the ones before it brought in.
	merge, err := r.run("rev-list", "--first-parent", "--merges", "-1", s.old.rev+".."+end)
	if merge = strings.TrimSpace(merge); err != nil || merge == "" {
		return
	}
	theirs, err := r.run("rev-parse", "--verify", "--quiet", merge+"^2")
	if err != nil {
		return
	}
	theirs = strings.TrimSpace(theirs)
	base, err := r.run("merge-base", s.old.rev, theirs)
	if base = strings.TrimSpace(base); err != nil || base == theirs {
		return
	}
	tree, err := r.mergedTree(base, s.old.rev, theirs)
	if err != nil {
		return
	}
	s.old = side{rev: tree, as: s.old.name()}
	s.Desc = "The branch's own changes: what it merged in is taken as already there"
}

// mergedTrees holds the trees made, as the same three commits always merge
// to the same tree and a ticket's changes are read again every few seconds.
var mergedTrees sync.Map // base ours theirs -> tree

// mergedTree is the tree of ours and theirs merged from base, made in an
// index of its own so neither the checkout nor its index is touched. A file
// the merge can't settle keeps ours.
func (r *Repo) mergedTree(base, ours, theirs string) (string, error) {
	key := base + " " + ours + " " + theirs
	if t, ok := mergedTrees.Load(key); ok {
		return t.(string), nil
	}
	f, err := os.CreateTemp("", "dv-index-")
	if err != nil {
		return "", err
	}
	index := f.Name()
	f.Close()
	os.Remove(index) // read-tree wants to make it
	defer os.Remove(index)
	git := func(stdin string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = r.Root
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+index)
		cmd.Stdin = strings.NewReader(stdin)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			return out.String(), fmt.Errorf("git %s: %w: %s", args[0], err, errb.String())
		}
		return out.String(), nil
	}
	if _, err := git("", "read-tree", "-i", "-m", "--aggressive", base, ours, theirs); err != nil {
		return "", err
	}
	unmerged, err := git("", "ls-files", "-u", "-z")
	if err != nil {
		return "", err
	}
	// mode sha stage\tpath, a line a stage; settled here a file at a time.
	type entry struct{ mode, sha string }
	stages := map[string]*[4]entry{}
	var order []string
	for _, line := range strings.Split(unmerged, "\x00") {
		meta, path, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		fields := strings.Fields(meta)
		if len(fields) != 3 || len(fields[2]) != 1 {
			continue
		}
		if stages[path] == nil {
			stages[path] = &[4]entry{}
			order = append(order, path)
		}
		stages[path][fields[2][0]-'0'] = entry{fields[0], fields[1]}
	}
	var info strings.Builder
	for _, path := range order {
		st := stages[path]
		keep := st[2]
		if st[1].sha != "" && st[2].sha != "" && st[3].sha != "" {
			if sha, ok := r.mergeBlobs(st[1].sha, st[2].sha, st[3].sha); ok {
				keep.sha = sha
			}
		}
		// Clearing the path's stages, then ours or the merge in its place.
		fmt.Fprintf(&info, "0 %s\t%s\n", strings.Repeat("0", 40), path)
		if keep.sha != "" {
			fmt.Fprintf(&info, "%s %s 0\t%s\n", keep.mode, keep.sha, path)
		}
	}
	if info.Len() > 0 {
		if _, err := git(info.String(), "update-index", "--index-info"); err != nil {
			return "", err
		}
	}
	tree, err := git("", "write-tree")
	if err != nil {
		return "", err
	}
	tree = strings.TrimSpace(tree)
	mergedTrees.Store(key, tree)
	return tree, nil
}

// mergeBlobs merges one file's three versions, and stores the result: false
// where they conflict.
func (r *Repo) mergeBlobs(base, ours, theirs string) (string, bool) {
	dir, err := os.MkdirTemp("", "dv-merge-")
	if err != nil {
		return "", false
	}
	defer os.RemoveAll(dir)
	var paths [3]string
	for i, sha := range []string{ours, base, theirs} {
		b, err := r.run("cat-file", "blob", sha)
		if err != nil {
			return "", false
		}
		paths[i] = fmt.Sprintf("%s/%d", dir, i)
		if os.WriteFile(paths[i], []byte(b), 0o600) != nil {
			return "", false
		}
	}
	merged, err := gitOutput(r.Root, "merge-file", "-p", "--quiet", paths[0], paths[1], paths[2])
	if err != nil {
		return "", false // conflicts, or a binary file
	}
	sha, err := r.runInput(merged, "hash-object", "-w", "--stdin")
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(sha), true
}
