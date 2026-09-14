package permit

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"dv/internal/gitx"
)

// Preview is what a file tool would do, worked out against the file as it is
// on disk when the request arrives.
type Preview struct {
	Path   string         `json:"path"` // relative to the repository when inside it
	InRepo bool           `json:"inRepo"`
	Diff   *gitx.FileDiff `json:"diff"`
	// Problem is why Claude Code would refuse the edit as it stands. The diff is
	// then between Claude's two strings, since there is no place in the file for it.
	Problem string `json:"problem,omitempty"`
}

func preview(root, tool string, raw json.RawMessage) *Preview {
	if tool != "Edit" && tool != "Write" {
		return nil
	}
	var in struct {
		FilePath   string `json:"file_path"`
		Content    string `json:"content"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if json.Unmarshal(raw, &in) != nil || in.FilePath == "" {
		return nil
	}
	p := &Preview{Path: in.FilePath}
	if rel, err := filepath.Rel(root, in.FilePath); err == nil && rel != ".." && !strings.HasPrefix(rel, "../") {
		p.Path, p.InRepo = filepath.ToSlash(rel), true
	}

	cur, err := os.ReadFile(in.FilePath)
	exists := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		p.Problem = err.Error()
		return p
	}
	entry := gitx.FileEntry{Path: p.Path, Status: "M"}
	if !exists {
		entry.Status = "A"
	}
	next := []byte(in.Content)
	if tool == "Edit" {
		next, p.Problem = applyEdit(cur, exists, in.OldString, in.NewString, in.ReplaceAll)
		if p.Problem != "" {
			p.Diff = gitx.DiffContent(gitx.FileEntry{Path: p.Path, Status: "M"}, []byte(in.OldString), []byte(in.NewString))
			return p
		}
	}
	p.Diff = gitx.DiffContent(entry, cur, next)
	return p
}

// applyEdit makes an Edit the way Claude Code does, or says why it would not.
func applyEdit(cur []byte, exists bool, old, repl string, all bool) ([]byte, string) {
	if old == "" {
		// An empty old_string is how Edit creates a file.
		if len(cur) > 0 {
			return nil, "old_string is empty, which only creates a file, and this one already has content"
		}
		return []byte(repl), ""
	}
	if !exists {
		return nil, "the file does not exist"
	}
	text := string(cur)
	if !strings.Contains(text, old) {
		old = unquoted(text, old)
	}
	switch n := strings.Count(text, old); {
	case old == "" || n == 0:
		return nil, "old_string is not in the file as it is on disk"
	case all:
		return []byte(strings.ReplaceAll(text, old, repl)), ""
	case n > 1:
		return nil, fmt.Sprintf("old_string is in the file %d times and replace_all is off", n)
	}
	return []byte(strings.Replace(text, old, repl, 1)), ""
}

// unquoted finds old in text when the two differ only in curly against
// straight quotes, which Claude Code forgives, and returns text's spelling.
func unquoted(text, old string) string {
	no := straight(old)
	before, _, ok := strings.Cut(straight(text), no)
	if !ok {
		return ""
	}
	// straight swaps runes one for one, so rune offsets carry across.
	start := utf8.RuneCountInString(before)
	return string([]rune(text)[start : start+utf8.RuneCountInString(no)])
}

var straight = strings.NewReplacer("‘", "'", "’", "'", "“", `"`, "”", `"`).Replace
