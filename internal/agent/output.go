package agent

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"dv/internal/gitx"
)

// Output is the whole of a call's result, for its line in the conversation
// opened. Which fields are set depends on the tool.
type Output struct {
	Text   string  `json:"text,omitempty"` // as Claude was given it
	Read   *Read   `json:"read,omitempty"`
	Found  []Found `json:"found,omitempty"` // Grep and Glob
	Stdout string  `json:"stdout,omitempty"`
	Stderr string  `json:"stderr,omitempty"`
	Page   string  `json:"page,omitempty"` // WebFetch, as markdown
	Links  []Link  `json:"links,omitempty"`
}

// Read is the text a Read call was given, which the file may no longer hold.
type Read struct {
	Path    string `json:"path"`
	InRepo  bool   `json:"inRepo"`
	Lang    string `json:"lang,omitempty"`
	Start   int    `json:"start"`
	Content string `json:"content"`
}

// Found is a file a search turned up, or a line in one. A line with no path
// is in the one file the search was given.
type Found struct {
	Path   string `json:"path,omitempty"`
	InRepo bool   `json:"inRepo,omitempty"`
	Line   int    `json:"line,omitempty"`
	Text   string `json:"text,omitempty"`
}

type Link struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

var errNoResult = errors.New("the transcript has no result recorded for this call")

// resultLine finds the transcript line holding the result of tool call id.
func resultLine(path, id string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	needle := []byte(`"tool_use_id":"` + id + `"`)
	rd := bufio.NewReaderSize(f, 1<<16)
	for {
		line, err := rd.ReadBytes('\n')
		if bytes.Contains(line, needle) && bytes.Contains(line, []byte(`"toolUseResult"`)) {
			return line, nil
		}
		if err == io.EOF {
			return nil, errNoResult
		}
		if err != nil {
			return nil, err
		}
	}
}

type resultEntry struct {
	Cwd     string `json:"cwd"`
	Message struct {
		Content []block `json:"content"`
	} `json:"message"`
	Result toolUseResult `json:"toolUseResult"`
}

func outputFrom(line []byte, root, id string) (*Output, error) {
	var e resultEntry
	if err := json.Unmarshal(line, &e); err != nil {
		return nil, err
	}
	out := &Output{}
	for _, b := range e.Message.Content {
		if b.Type == "tool_result" && b.ToolUseID == id {
			out.Text = resultText(b)
		}
	}
	where := func(p string) (string, bool) {
		if !filepath.IsAbs(p) {
			p = filepath.Join(e.Cwd, p)
		}
		if rel, err := filepath.Rel(root, p); err == nil && filepath.IsLocal(rel) {
			return filepath.ToSlash(rel), true
		}
		return p, false
	}
	// Where a record says more than the text, the text is left out.
	switch r := &e.Result; {
	case r.File != nil && r.Type == "text":
		path, in := where(r.File.FilePath)
		out.Read = &Read{Path: path, InRepo: in, Lang: gitx.LangFor(path), Start: max(r.File.StartLine, 1), Content: r.File.Content}
		out.Text = ""
	case r.NumFiles != nil && r.Mode == "content":
		if r.Content != nil {
			out.Found, out.Text = matches(*r.Content, where), ""
		}
	case r.NumFiles != nil:
		for _, f := range r.Filenames {
			path, in := where(f)
			out.Found = append(out.Found, Found{Path: path, InRepo: in})
		}
		out.Text = ""
	case r.Stdout != nil && r.Background == "":
		out.Stdout, out.Stderr, out.Text = ansi.ReplaceAllString(*r.Stdout, ""), ansi.ReplaceAllString(r.Stderr, ""), ""
	case r.Code != 0:
		out.Page = r.Page
	case r.Query != nil && r.Results != nil:
		out.Links = searchLinks(r.Results)
	}
	return out, nil
}

var (
	ansi    = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]`)
	matched = regexp.MustCompile(`^(.*?):(\d+):(.*)$`)
	bare    = regexp.MustCompile(`^(\d+):(.*)$`)
)

// matches reads Grep's content output, path:line:text a line, or line:text
// when it searched one file.
func matches(content string, where func(string) (string, bool)) []Found {
	var out []Found
	for _, l := range strings.Split(strings.TrimRight(content, "\n"), "\n") {
		if m := bare.FindStringSubmatch(l); m != nil {
			n, _ := strconv.Atoi(m[1])
			out = append(out, Found{Line: n, Text: m[2]})
		} else if m := matched.FindStringSubmatch(l); m != nil {
			n, _ := strconv.Atoi(m[2])
			path, in := where(m[1])
			out = append(out, Found{Path: path, InRepo: in, Line: n, Text: m[3]})
		} else if l != "" && l != "--" {
			out = append(out, Found{Text: l})
		}
	}
	return out
}

// searchLinks pulls the pages out of WebSearch's results, which mix the
// searches' hits with text Claude wrote about them.
func searchLinks(raw json.RawMessage) []Link {
	var results []json.RawMessage
	json.Unmarshal(raw, &results)
	links := []Link{}
	for _, r := range results {
		var hit struct {
			Content []Link `json:"content"`
		}
		if json.Unmarshal(r, &hit) == nil {
			links = append(links, hit.Content...)
		}
	}
	return links
}

// Image is a picture a Read call was shown, as it was shown it.
type Image struct {
	MediaType string
	Data      []byte
}

// ImageTypes are the kinds of image both Claude and a page take.
var ImageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// Check is whether img can be sent with a message.
func (img Image) Check() error {
	if !ImageTypes[img.MediaType] {
		return fmt.Errorf("Claude takes PNG, JPEG, GIF and WebP images, not %s", img.MediaType)
	}
	// Claude's limit is on the image as sent, in base64.
	if base64.StdEncoding.EncodedLen(len(img.Data)) > 5<<20 {
		return errors.New("an image is over the 5 MB Claude takes")
	}
	return nil
}

// promptImage is the nth picture sent with a message, found by its uuid.
func promptImage(path, uuid string, n int) (*Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	needle := []byte(`"uuid":"` + uuid + `"`)
	rd := bufio.NewReaderSize(f, 1<<16)
	for {
		line, err := rd.ReadBytes('\n')
		if bytes.Contains(line, needle) {
			var e struct {
				UUID    string `json:"uuid"`
				Message struct {
					Content json.RawMessage `json:"content"`
				} `json:"message"`
				Attachment struct {
					Prompt json.RawMessage `json:"prompt"`
				} `json:"attachment"`
			}
			if json.Unmarshal(line, &e) == nil && e.UUID == uuid {
				var blocks []struct {
					Type   string `json:"type"`
					Source struct {
						MediaType string `json:"media_type"`
						Data      string `json:"data"`
					} `json:"source"`
				}
				if json.Unmarshal(e.Message.Content, &blocks) != nil {
					json.Unmarshal(e.Attachment.Prompt, &blocks)
				}
				for _, b := range blocks {
					if b.Type != "image" {
						continue
					}
					if n--; n >= 0 {
						continue
					}
					if !ImageTypes[b.Source.MediaType] {
						return nil, errors.New("not an image a page can show")
					}
					data, err := base64.StdEncoding.DecodeString(b.Source.Data)
					if err != nil {
						return nil, err
					}
					return &Image{MediaType: b.Source.MediaType, Data: data}, nil
				}
				return nil, errors.New("the message has no such image")
			}
		}
		if err == io.EOF {
			return nil, errors.New("no such message")
		}
		if err != nil {
			return nil, err
		}
	}
}

func imageFrom(line []byte) (*Image, error) {
	var e resultEntry
	if err := json.Unmarshal(line, &e); err != nil {
		return nil, err
	}
	f := e.Result.File
	if e.Result.Type != "image" || f == nil || !ImageTypes[f.MediaType] {
		return nil, errors.New("this call read no image")
	}
	data, err := base64.StdEncoding.DecodeString(f.Base64)
	if err != nil {
		return nil, err
	}
	return &Image{MediaType: f.MediaType, Data: data}, nil
}
