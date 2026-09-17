package gitx

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// side identifies where one half of a comparison reads its content from.
type side struct {
	rev      string // commit-ish; empty when worktree or index
	worktree bool
	index    bool
	empty    bool // the empty tree — used for a repository's root commit
}

func (s side) spec(path string) string {
	switch {
	case s.index:
		return ":" + path
	case s.rev != "":
		return s.rev + ":" + path
	}
	return ""
}

// Scope is one reviewable comparison. Kind is what the UI's picker sends; the
// remaining fields are the resolution, filled in by ResolveScope.
type Scope struct {
	Kind   string `json:"kind"`             // auto | working | staged | head | branch | custom
	Rev    string `json:"rev"`              // user-entered revspec for kind=custom
	Label  string `json:"label"`            // short human label, e.g. "main...HEAD"
	Desc   string `json:"desc"`             // one-line explanation shown under the picker
	Picked string `json:"picked,omitempty"` // for kind=auto, the concrete kind it settled on
	// Where each side is read from, to label a file shown off it; empty for
	// the working tree.
	OldAt string `json:"oldAt,omitempty"`
	NewAt string `json:"newAt,omitempty"`

	old, new         side
	includeUntracked bool
}

// emptyTree is git's well-known hash for a tree with no entries; diffing
// against it is how a root commit gets a sensible "everything is new" base.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// hasHead reports whether the repository has any commits. A freshly created
// repo has none, and every scope that names HEAD has to fall back to the empty
// tree instead of letting git fail with "ambiguous argument".
func (r *Repo) hasHead() bool {
	_, err := r.run("rev-parse", "--verify", "--quiet", "HEAD")
	return err == nil
}

// baseSide is the old side of a HEAD-relative comparison, or the empty tree in
// a repository that has not been committed to yet.
func (r *Repo) baseSide() side {
	if r.hasHead() {
		return side{rev: "HEAD"}
	}
	return side{rev: emptyTree, empty: true}
}

// ResolveScope turns a picker selection into concrete comparison sides.
func (r *Repo) ResolveScope(kind, rev string) (*Scope, error) {
	if !r.IsGit() {
		return folderScope(), nil
	}
	s, err := r.resolve(kind, rev)
	if err != nil {
		return nil, err
	}
	s.OldAt, s.NewAt = s.old.name(), s.new.name()
	return s, nil
}

func (r *Repo) resolve(kind, rev string) (*Scope, error) {
	s := &Scope{Kind: kind, Rev: rev}
	switch kind {
	case "auto":
		return r.resolveAuto()

	case "", "working":
		s.Kind = "working"
		s.old = r.baseSide()
		s.new = side{worktree: true}
		s.includeUntracked = true
		s.Label = "Uncommitted"
		s.Desc = "Working tree vs HEAD, including untracked files"
		if s.old.empty {
			s.Label = "Everything"
			s.Desc = "The repository has no commits yet, so every file is new"
		}

	case "staged":
		s.old = r.baseSide()
		s.new = side{index: true}
		s.Label = "Staged"
		s.Desc = "Index vs HEAD"

	case "head":
		if !r.hasHead() {
			return nil, fmt.Errorf("this repository has no commits yet")
		}
		if _, err := r.run("rev-parse", "--verify", "--quiet", "HEAD^"); err != nil {
			s.old = side{rev: emptyTree, empty: true}
		} else {
			s.old = side{rev: "HEAD^"}
		}
		s.new = side{rev: "HEAD"}
		s.Label = "Last commit"
		s.Desc = "HEAD vs its parent"

	case "branch":
		if !r.hasHead() {
			return nil, fmt.Errorf("this repository has no commits yet")
		}
		base := r.DefaultBranch()
		if base == "" {
			return nil, fmt.Errorf("no default branch found; pick a custom range instead")
		}
		mb, err := r.run("merge-base", base, "HEAD")
		if err != nil {
			return nil, fmt.Errorf("no merge base with %s: %w", base, err)
		}
		s.old = side{rev: strings.TrimSpace(mb)}
		s.new = side{worktree: true}
		s.includeUntracked = true
		s.Label = base + "...working tree"
		s.Desc = "Everything on this branch since it diverged from " + base + ", plus uncommitted work"

	case "custom":
		return r.resolveCustom(s)

	default:
		return nil, fmt.Errorf("unknown scope %q", kind)
	}
	return s, nil
}

// autoOrder is what "auto" tries, in order. The chain matches how a review
// actually goes: look at what you have not committed; with a clean tree, look at
// the branch you are working on; and on the trunk itself — where there is no
// branch to compare — look at what you last committed.
var autoOrder = []string{"working", "branch", "head"}

// resolveAuto picks the first comparison in autoOrder that is not empty. The
// probing costs one `git diff --name-status` per candidate, which is cheap
// enough to run on every load and keeps the view following the work.
func (r *Repo) resolveAuto() (*Scope, error) {
	for _, kind := range autoOrder {
		cand, err := r.ResolveScope(kind, "")
		if err != nil {
			continue // e.g. "branch" in a repo with no trunk, or no commits yet
		}
		files, err := r.Files(cand)
		if err != nil || len(files) == 0 {
			continue
		}
		cand.Kind = "auto"
		cand.Picked = kind
		return cand, nil
	}
	// Nothing anywhere: fall back to uncommitted so the UI has a sane empty
	// state to render rather than an error.
	s, err := r.ResolveScope("working", "")
	if err != nil {
		return nil, err
	}
	s.Kind = "auto"
	s.Picked = "working"
	return s, nil
}

// resolveCustom accepts the revspec forms people actually type: "a...b" (since
// the merge base), "a..b", a bare rev (that commit against its parent), and
// "rev.." / "rev" with an implied working-tree right-hand side.
func (r *Repo) resolveCustom(s *Scope) (*Scope, error) {
	spec := strings.TrimSpace(s.Rev)
	if spec == "" {
		return nil, fmt.Errorf("enter a revision or range")
	}
	verify := func(rev string) (string, error) {
		out, err := r.run("rev-parse", "--verify", "--quiet", rev+"^{commit}")
		if err != nil {
			return "", fmt.Errorf("unknown revision %q", rev)
		}
		return strings.TrimSpace(out), nil
	}

	switch {
	case strings.Contains(spec, "..."):
		l, rr, _ := strings.Cut(spec, "...")
		l, rr = strings.TrimSpace(l), strings.TrimSpace(rr)
		if l == "" {
			l = "HEAD"
		}
		mb, err := r.run("merge-base", l, orHead(rr))
		if err != nil {
			return nil, fmt.Errorf("no merge base between %s and %s", l, orHead(rr))
		}
		s.old = side{rev: strings.TrimSpace(mb)}
		if rr == "" {
			s.new = side{worktree: true}
			s.includeUntracked = true
			s.Label = l + "...working tree"
		} else {
			rev, err := verify(rr)
			if err != nil {
				return nil, err
			}
			s.new = side{rev: rev}
			s.Label = spec
		}
		s.Desc = "Changes since the merge base with " + l

	case strings.Contains(spec, ".."):
		l, rr, _ := strings.Cut(spec, "..")
		l, rr = strings.TrimSpace(l), strings.TrimSpace(rr)
		lrev, err := verify(orHead(l))
		if err != nil {
			return nil, err
		}
		s.old = side{rev: lrev}
		if rr == "" {
			s.new = side{worktree: true}
			s.includeUntracked = true
			s.Label = l + "..working tree"
		} else {
			rev, err := verify(rr)
			if err != nil {
				return nil, err
			}
			s.new = side{rev: rev}
			s.Label = spec
		}
		s.Desc = "Direct comparison of the two endpoints"

	default:
		rev, err := verify(spec)
		if err != nil {
			return nil, err
		}
		if _, err := r.run("rev-parse", "--verify", "--quiet", spec+"^"); err != nil {
			s.old = side{rev: emptyTree, empty: true}
		} else {
			s.old = side{rev: spec + "^"}
		}
		s.new = side{rev: rev}
		s.Label = spec
		s.Desc = "That commit against its parent"
	}
	return s, nil
}

func orHead(s string) string {
	if s == "" {
		return "HEAD"
	}
	return s
}

// diffArgs is the range portion of a `git diff` invocation for this scope.
func (s *Scope) diffArgs() []string {
	switch {
	case s.new.index:
		return []string{"--cached", s.old.rev}
	case s.new.worktree:
		return []string{s.old.rev}
	default:
		return []string{s.old.rev, s.new.rev}
	}
}

// FileEntry is one row in the changed-files sidebar. Contents are fetched
// separately so opening a 900-file diff stays instant.
type FileEntry struct {
	Path      string `json:"path"`
	OldPath   string `json:"oldPath,omitempty"`
	Status    string `json:"status"` // A | M | D | R | T
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	Binary    bool   `json:"binary"`
	Untracked bool   `json:"untracked,omitempty"`
	// Generated means machine-written: still listed, but its diff is not shown
	// until asked for. See generated.go for what counts.
	Generated bool `json:"generated,omitempty"`
	// Media is the content type of a file shown as itself; see MediaType.
	Media string `json:"media,omitempty"`
	// Rev identifies the content on both sides, so a client holding this
	// file's diff can tell from a fresh listing whether it has gone stale.
	Rev string `json:"rev"`

	oldOID, newOID string
}

// Files lists what changed in the scope, sorted by path.
func (r *Repo) Files(s *Scope) ([]FileEntry, error) {
	if !r.IsGit() {
		return []FileEntry{}, nil
	}
	args := append([]string{"diff", "--raw", "--no-abbrev", "-M", "-z"}, s.diffArgs()...)
	rawOut, err := r.run(args...)
	if err != nil {
		return nil, err
	}
	entries := parseRaw(rawOut)

	// numstat carries the line counts; "-" in either column means binary.
	args = append([]string{"diff", "--numstat", "-M", "-z"}, s.diffArgs()...)
	if numOut, err := r.run(args...); err == nil {
		applyNumstat(entries, numOut)
	}

	if s.includeUntracked {
		out, err := r.run("ls-files", "--others", "--exclude-standard", "-z")
		if err == nil {
			for _, p := range strings.Split(out, "\x00") {
				if p == "" {
					continue
				}
				e := &FileEntry{Path: p, Status: "A", Untracked: true}
				if b, ok, _ := r.worktreeFile(p); ok {
					if isBinary(b) {
						e.Binary = true
					} else {
						e.Additions = len(splitLines(b))
					}
				}
				entries = append(entries, e)
			}
		}
	}

	r.markGenerated(entries)

	out := make([]FileEntry, 0, len(entries))
	for _, e := range entries {
		e.Rev = r.rev(e)
		e.Media = MediaType(e.Path)
		out = append(out, *e)
	}
	sortFiles(out)
	return out, nil
}

// rev is the content identity behind FileEntry.Rev. An all-zero id is git
// saying "whatever is in the working tree", which it has not hashed, so the
// file's size and mtime stand in for it.
func (r *Repo) rev(e *FileEntry) string {
	if e.Status == "D" {
		return e.oldOID + ":-"
	}
	if strings.Trim(e.newOID, "0") != "" {
		return e.oldOID + ":" + e.newOID
	}
	fi, err := os.Stat(filepath.Join(r.Root, e.Path))
	if err != nil {
		return e.oldOID + ":-"
	}
	return fmt.Sprintf("%s:%d.%d", e.oldOID, fi.Size(), fi.ModTime().UnixNano())
}

// parseRaw reads `git diff --raw -z`: ":oldmode newmode oldid newid status",
// then the path, then a second path for renames and copies.
func parseRaw(z string) []*FileEntry {
	fields := strings.Split(z, "\x00")
	var entries []*FileEntry
	for i := 0; i < len(fields); i++ {
		meta := strings.Fields(strings.TrimPrefix(fields[i], ":"))
		if len(meta) < 5 {
			continue
		}
		e := &FileEntry{Status: meta[4][:1], oldOID: meta[2], newOID: meta[3]}
		switch e.Status {
		case "R", "C":
			if i+2 >= len(fields) {
				return entries
			}
			e.Status, e.OldPath, e.Path = "R", fields[i+1], fields[i+2]
			i += 2
		default:
			if i+1 >= len(fields) {
				return entries
			}
			e.Path = fields[i+1]
			i++
		}
		entries = append(entries, e)
	}
	return entries
}

func applyNumstat(entries []*FileEntry, z string) {
	byPath := make(map[string]*FileEntry, len(entries))
	for _, e := range entries {
		byPath[e.Path] = e
	}
	// numstat -z emits "adds\tdels\tpath\0", and for renames
	// "adds\tdels\t\0oldpath\0newpath\0".
	fields := strings.Split(z, "\x00")
	for i := 0; i < len(fields); i++ {
		rec := fields[i]
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		path := parts[2]
		if path == "" && i+2 < len(fields) {
			path = fields[i+2]
			i += 2
		}
		e := byPath[path]
		if e == nil {
			continue
		}
		if parts[0] == "-" || parts[1] == "-" {
			e.Binary = true
			continue
		}
		e.Additions, _ = strconv.Atoi(parts[0])
		e.Deletions, _ = strconv.Atoi(parts[1])
	}
}

func sortFiles(f []FileEntry) {
	for i := 1; i < len(f); i++ {
		for j := i; j > 0 && f[j].Path < f[j-1].Path; j-- {
			f[j], f[j-1] = f[j-1], f[j]
		}
	}
}

// maxFileBytes is the point past which a file is shown as a stat line only.
const maxFileBytes = 3 << 20

// FileDiff carries both complete sides plus their alignment, which is what lets
// the client expand context to the whole file without another round trip.
type FileDiff struct {
	FileEntry
	OldLines     []string `json:"oldLines"`
	NewLines     []string `json:"newLines"`
	Ops          []Op     `json:"ops"`
	TooLarge     bool     `json:"tooLarge,omitempty"`
	OldNoNewline bool     `json:"oldNoNewline,omitempty"`
	NewNoNewline bool     `json:"newNoNewline,omitempty"`
	Lang         string   `json:"lang"`
}

// Diff assembles the full comparison for one path.
func (r *Repo) Diff(s *Scope, entry FileEntry) (*FileDiff, error) {
	oldPath := entry.Path
	if entry.OldPath != "" {
		oldPath = entry.OldPath
	}

	var oldRaw, newRaw []byte
	if entry.Status != "A" {
		b, _, err := r.read(s.old, oldPath)
		if err != nil {
			return nil, err
		}
		oldRaw = b
	}
	if entry.Status != "D" {
		b, _, err := r.read(s.new, entry.Path)
		if err != nil {
			return nil, err
		}
		newRaw = b
	}
	return DiffContent(entry, oldRaw, newRaw), nil
}

// DiffContent compares two versions of a file already in hand: the sides of a
// comparison, or a file and an edit Claude has yet to make to it.
func DiffContent(entry FileEntry, oldRaw, newRaw []byte) *FileDiff {
	fd := &FileDiff{FileEntry: entry, Lang: LangFor(entry.Path), OldLines: []string{}, NewLines: []string{}}
	if isBinary(oldRaw) || isBinary(newRaw) {
		fd.Binary = true
		return fd
	}
	if len(oldRaw) > maxFileBytes || len(newRaw) > maxFileBytes {
		fd.TooLarge = true
		return fd
	}

	fd.OldLines = splitLines(oldRaw)
	fd.NewLines = splitLines(newRaw)
	fd.OldNoNewline = len(oldRaw) > 0 && !bytes.HasSuffix(oldRaw, []byte("\n"))
	fd.NewNoNewline = len(newRaw) > 0 && !bytes.HasSuffix(newRaw, []byte("\n"))
	fd.Ops = DiffLines(fd.OldLines, fd.NewLines)

	// Recount from the alignment: numstat's numbers come from git's own diff
	// and can disagree slightly with ours, which would make the sidebar lie.
	adds, dels := 0, 0
	for _, op := range fd.Ops {
		switch op.Kind {
		case OpInsert:
			adds += op.NewLen
		case OpDelete:
			dels += op.OldLen
		}
	}
	fd.Additions, fd.Deletions = adds, dels
	return fd
}

// read loads path from one side of a comparison, and whether it is there.
func (r *Repo) read(sd side, path string) ([]byte, bool, error) {
	switch {
	case sd.worktree:
		return r.worktreeFile(path)
	case sd.empty:
		return nil, false, nil
	case sd.index:
		b, err := r.runBytes("show", sd.spec(path))
		return b, err == nil, nil
	}
	return r.blob(sd.rev, path)
}

// name labels a side for the viewer. The working tree goes unlabelled: it is
// what a file is taken to mean.
func (sd side) name() string {
	switch {
	case sd.worktree, sd.empty:
		return ""
	case sd.index:
		return "index"
	case len(sd.rev) >= 40 && strings.Trim(sd.rev, "0123456789abcdef") == "":
		return sd.rev[:7] // a resolved merge base
	}
	return sd.rev
}

// FileAt reads a whole file for the source viewer. Without a scope it reads
// the working tree, so a jump to a definition lands on what is on disk; with
// one it reads that side of the comparison, so a file opened from the diff
// has the diff's line numbers. at names the side read, if not the working tree.
func (r *Repo) FileAt(path string, s *Scope, old bool) (lines []string, at string, err error) {
	sd := s.pick(old)
	raw, ok, err := r.read(sd, path)
	if err != nil {
		return nil, "", err
	}
	at = sd.name()
	if !ok {
		if at != "" {
			return nil, "", fmt.Errorf("no such file at %s: %s", at, path)
		}
		return nil, "", fmt.Errorf("no such file: %s", path)
	}
	if isBinary(raw) {
		return nil, "", fmt.Errorf("binary file")
	}
	if len(raw) > maxFileBytes {
		return nil, "", fmt.Errorf("file too large")
	}
	return splitLines(raw), at, nil
}

// pick is the side a file is read from: the working tree without a scope.
func (s *Scope) pick(old bool) side {
	switch {
	case s == nil:
		return side{worktree: true}
	case old:
		return s.old
	}
	return s.new
}

func splitLines(b []byte) []string {
	if len(b) == 0 {
		return []string{}
	}
	s := string(b)
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

func isBinary(b []byte) bool {
	n := len(b)
	if n > 8000 {
		n = 8000
	}
	return bytes.IndexByte(b[:n], 0) >= 0
}
