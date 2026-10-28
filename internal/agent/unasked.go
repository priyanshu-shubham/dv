package agent

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

// unasked says whether an edit to path, an absolute one, is to a file globs
// leave out of AutoAskEdits. They read as the page's file filter does, from
// the folder's root: a pattern without a slash names a file or folder at any
// depth, one with a slash is anchored at the root, ** spans folders, and a
// folder takes in everything inside it. A file outside the folder is asked
// about whatever the globs say.
func unasked(globs []string, root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || path == "" || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	rel = filepath.ToSlash(rel)
	for _, g := range globs {
		if re := globRegexp(g); re != nil && re.MatchString(rel) {
			return true
		}
	}
	return false
}

func globRegexp(glob string) *regexp.Regexp {
	glob = strings.TrimSpace(glob)
	if glob == "" {
		return nil
	}
	var b strings.Builder
	b.WriteString("(?i)")
	// A trailing slash only says "folder"; any other slash anchors it.
	if strings.Contains(strings.TrimSuffix(glob, "/"), "/") {
		b.WriteString("^")
	} else {
		b.WriteString("(?:^|/)")
	}
	rest, dotted := strings.CutPrefix(glob, "./")
	if !dotted {
		rest = strings.TrimPrefix(rest, "/")
	}
	rest = strings.TrimSuffix(rest, "/")
	for rest != "" {
		switch {
		case strings.HasPrefix(rest, "**/"):
			b.WriteString("(?:.*/)?")
			rest = rest[3:]
		case strings.HasPrefix(rest, "**"):
			b.WriteString(".*")
			rest = rest[2:]
		case rest[0] == '*':
			b.WriteString("[^/]*")
			rest = rest[1:]
		case rest[0] == '?':
			b.WriteString("[^/]")
			rest = rest[1:]
		default:
			_, n := utf8.DecodeRuneInString(rest)
			b.WriteString(regexp.QuoteMeta(rest[:n]))
			rest = rest[n:]
		}
	}
	b.WriteString("(?:/|$)")
	re, _ := regexp.Compile(b.String())
	return re
}

// editedPath is the file an edit tool's call is for.
func editedPath(input json.RawMessage) string {
	var in struct {
		File     string `json:"file_path"`
		Notebook string `json:"notebook_path"`
	}
	json.Unmarshal(input, &in)
	if in.File != "" {
		return in.File
	}
	return in.Notebook
}
