package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"dv/internal/store"
)

const ticketUsage = `usage: dv ticket <verb> [args]

For the agents dv starts on a board ticket: reports to the board. dv sets
DV_ROOT, DV_TICKET and DV_ROLE in those sessions, so it knows which ticket and
role it speaks for.

  show [ticket|plan]                 prints the ticket, the plan, open question and review
  write ticket|plan                  saves the document read from stdin
  status "a few words"               what the board shows the agent doing
  title "..."                        renames the ticket (scoper and planner)
  ready ["one line"]                 hands the stage's work on
  split [--after ID] "title"         files a sub-ticket; its ticket comes on stdin
  ask "question"                     the builder asks the planner
  answer --source S "answer"         the planner answers the builder
  finding "text"                     the reviewer records a finding
  verdict <ID> fixed|rejected "why"  the builder answers a finding
  recheck <ID> holds|fails "note"    the reviewer checks an answer
`

func ticket(args []string, stdin io.Reader) error {
	fl := flag.NewFlagSet("dv ticket", flag.ContinueOnError)
	fl.Usage = func() { fmt.Fprint(fl.Output(), ticketUsage) }
	after := fl.String("after", "", "split: the sub-ticket this one waits for")
	source := fl.String("source", "", `answer: what approved thing the answer follows from, or "you"`)
	if err := fl.Parse(args); err != nil {
		return helpIsNoError(err)
	}
	words := fl.Args()
	if len(words) == 0 {
		fl.Usage()
		return errors.New("name a verb")
	}
	// Flags may come after the verb too.
	if err := fl.Parse(words[1:]); err != nil {
		return helpIsNoError(err)
	}
	verb, rest := words[0], fl.Args()

	root, id, role := os.Getenv("DV_ROOT"), os.Getenv("DV_TICKET"), os.Getenv("DV_ROLE")
	if root == "" || id == "" || role == "" {
		return errors.New("dv ticket is for the agents of a board ticket, which dv starts with DV_ROOT, DV_TICKET and DV_ROLE set")
	}
	rep := map[string]any{"role": role, "verb": verb, "args": rest}
	switch verb {
	case "write", "split":
		body, err := io.ReadAll(stdin)
		if err != nil {
			return err
		}
		rep["body"] = string(body)
		rep["flag"] = *after
	case "answer":
		rep["flag"] = *source
	}
	srv, ok := store.Announced(root)
	if !ok {
		return fmt.Errorf("no dv is serving %s", root)
	}
	b, _ := json.Marshal(rep)
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(srv.URL, "/")+"/api/board/"+id+"/agent", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	store.MarkLocal(req)
	res, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return fmt.Errorf("could not reach dv: %w", err)
	}
	defer res.Body.Close()
	var out struct{ Out, Error string }
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return fmt.Errorf("dv answered %s", res.Status)
	}
	if out.Error != "" {
		return errors.New(out.Error)
	}
	fmt.Print(out.Out)
	if !strings.HasSuffix(out.Out, "\n") {
		fmt.Println()
	}
	return nil
}
