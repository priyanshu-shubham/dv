package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"dv/internal/gitx"
	"dv/internal/store"
)

const commentUsage = `usage: dv comment [flags] <file>[:line[-end]] [message]

Adds a comment to this repository's review, the same as leaving one in dv's
page. Useful for an agent to report what it found while reviewing.

  <file>       path from the repository root, from here, or absolute
  :line[-end]  the line or lines it is about; leave out for the whole file
  [message]    the comment, in Markdown; leave out, or use -, to read stdin

Line numbers are the file's as it is now. For a line the change removed,
pass -old and use its number in the last commit.

examples:
  dv comment internal/app.go:42 "This can be nil when the cache is cold."
  dv comment app.go:10-14 "Duplicates parseArgs above."
  dv comment -old app.go:7 "This check is gone; was that meant?"
  dv comment README.md < note.md

flags:
`

var commentLoc = regexp.MustCompile(`^(.*?)(?::(\d+)(?:-(\d+))?)?$`)

// comment writes to the comment store itself, as reset does: a running dv
// reads the file again once it has moved, and its pages follow.
func comment(dir string, args []string, stdin io.Reader) error {
	fl := flag.NewFlagSet("dv comment", flag.ContinueOnError)
	fl.Usage = func() {
		fmt.Fprint(fl.Output(), commentUsage)
		fl.PrintDefaults()
	}
	author := fl.String("author", defaultAuthor(), "the name shown on the comment")
	old := fl.Bool("old", false, "the line is one the change removed, numbered as in the last commit")
	fl.StringVar(&dir, "C", dir, "repository directory")
	if err := fl.Parse(args); err != nil {
		return helpIsNoError(err)
	}
	// Flags may come after the file too; the body is all that is left.
	loc := fl.Arg(0)
	if loc == "" {
		fl.Usage()
		return fmt.Errorf("comment needs a file")
	}
	if err := fl.Parse(fl.Args()[1:]); err != nil {
		return helpIsNoError(err)
	}
	body := strings.Join(fl.Args(), " ")
	if body == "" || body == "-" {
		if f, ok := stdin.(*os.File); ok && body == "" {
			if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
				return fmt.Errorf("give the comment after the file, or pipe it in")
			}
		}
		b, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		body = string(b)
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return fmt.Errorf("the comment is empty")
	}

	m := commentLoc.FindStringSubmatch(loc)
	start, _ := strconv.Atoi(m[2])
	end, _ := strconv.Atoi(m[3])
	if end == 0 {
		end = start
	}
	if end < start {
		return fmt.Errorf("%s ends before it starts", loc)
	}

	repo, err := gitx.Open(dir)
	if err != nil {
		return err
	}
	if *old && !repo.IsGit() {
		return fmt.Errorf("-old needs a git repository")
	}
	scope, err := repo.ResolveScope("working", "")
	if err != nil {
		return err
	}
	path, err := commentPath(repo, scope, dir, m[1], *old)
	if err != nil {
		return err
	}
	side := map[bool]string{true: "old", false: "new"}[*old]
	var quote []string
	if start > 0 {
		lines, at, err := repo.FileAt(path, scope, *old)
		if err != nil {
			return err
		}
		if end > len(lines) {
			if at != "" {
				at = " at " + at
			}
			return fmt.Errorf("%s has only %d lines%s", path, len(lines), at)
		}
		quote = lines[start-1 : end]
	}

	if repo.IsGit() {
		store.EnsureExcluded(repo.CommonDir)
	}
	st, err := store.Open(repo.Root, repo.Name())
	if err != nil {
		return err
	}
	_, err = st.AddThread(&store.Thread{
		File:      path,
		Side:      side,
		StartLine: start,
		EndLine:   end,
		Quote:     quote,
		Scope:     scope.Label,
		BaseSHA:   repo.Head().SHA,
	}, body, *author)
	if err != nil {
		return err
	}
	where := path
	if start > 0 {
		where += ":" + strconv.Itoa(start)
		if end > start {
			where += "-" + strconv.Itoa(end)
		}
	}
	fmt.Println("dv: commented on " + where)
	return nil
}

// commentPath is the file as the store names it, from the root. An agent
// working in a folder below may write it from either, so both are tried.
func commentPath(repo *gitx.Repo, scope *gitx.Scope, dir, p string, old bool) (string, error) {
	if p == "" {
		return "", fmt.Errorf("comment needs a file")
	}
	here, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	tries := []string{p}
	if !filepath.IsAbs(p) {
		tries = []string{filepath.Join(here, p), filepath.Join(repo.Root, p)}
	}
	var first string
	for _, abs := range tries {
		rel, err := filepath.Rel(repo.Root, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		rel = filepath.ToSlash(rel)
		if first == "" {
			first = rel
		}
		if _, _, err := repo.StampAt(rel, scope, old); err == nil {
			return rel, nil
		}
	}
	if first == "" {
		return "", fmt.Errorf("%s is outside %s", p, repo.Root)
	}
	if !old && repo.IsGit() {
		if _, _, err := repo.StampAt(first, scope, true); err == nil {
			return "", fmt.Errorf("%s is deleted; pass -old to comment on it as it was", first)
		}
	}
	return "", fmt.Errorf("no such file: %s", first)
}

// defaultAuthor names the agent running the command, as their sessions show
// in the environment, and otherwise the reader, as the page does.
func defaultAuthor() string {
	switch {
	case os.Getenv("CLAUDECODE") != "":
		return "Claude"
	case os.Getenv("CODEX_THREAD_ID") != "":
		return "Codex"
	}
	return "you"
}

// helpIsNoError keeps -h from ending in an error; any other has been printed
// with the usage already.
func helpIsNoError(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	return err
}
