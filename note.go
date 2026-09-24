package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"dv/internal/gitx"
	"dv/internal/store"
)

const noteUsage = `usage: dv note [flags] [message]

Adds a note to this repository's list, the same as adding one in dv's page:
something found that is not for now - a bug elsewhere, a follow-up, a
decision to keep. Every worktree of the repository shares the list.

  [message]  the note; its first line is the title, the rest Markdown under
             it. Leave out, or use -, to read stdin

examples:
  dv note "Retry in relay drops the last message"
  dv note -p high -l relay,flaky "Retry in relay drops the last message"
  dv note -l docs <<'EOF'
  README's install steps skip the Node requirement

  make build needs Node for the UI bundle.
  EOF

flags:
`

func note(dir string, args []string, stdin io.Reader) error {
	fl := flag.NewFlagSet("dv note", flag.ContinueOnError)
	fl.Usage = func() {
		fmt.Fprint(fl.Output(), noteUsage)
		fl.PrintDefaults()
	}
	author := fl.String("author", defaultAuthor(), "the name shown on the note")
	priority := fl.String("p", "", "priority: high, medium or low")
	labels := fl.String("l", "", "labels, separated by commas")
	fl.StringVar(&dir, "C", dir, "repository directory")
	if err := fl.Parse(args); err != nil {
		return helpIsNoError(err)
	}
	// Flags may come after the message's first word too.
	words := fl.Args()
	if len(words) > 0 {
		if err := fl.Parse(words[1:]); err != nil {
			return helpIsNoError(err)
		}
		words = append([]string{words[0]}, fl.Args()...)
	}
	msg := strings.Join(words, " ")
	if msg == "" || msg == "-" {
		if f, ok := stdin.(*os.File); ok && msg == "" {
			if fi, err := f.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
				fl.Usage()
				return fmt.Errorf("give the note, or pipe it in")
			}
		}
		b, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		msg = string(b)
	}
	title, body, _ := strings.Cut(strings.TrimSpace(msg), "\n")

	repo, err := gitx.Open(dir)
	if err != nil {
		return err
	}
	notes, err := store.OpenNotes(repo.Root, repo.CommonDir)
	if err != nil {
		return err
	}
	n, err := notes.Add(store.Note{
		Title:    title,
		Body:     body,
		Priority: strings.ToLower(strings.TrimSpace(*priority)),
		Labels:   strings.Split(*labels, ","),
	}, *author)
	if err != nil {
		return err
	}
	fmt.Println("dv: noted " + n.Title)
	return nil
}
