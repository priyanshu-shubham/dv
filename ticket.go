package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"dv/internal/gitx"
	"dv/internal/store"
)

const ticketUsage = `usage: dv ticket <verb> [args]

For the agents dv starts on a board ticket: reports to the board, for the
ticket and role dv started the session for. Any other session may only file:
a ticket into the Inbox of the repository it works in.

  show [ticket|plan]                 prints the ticket, the plan, open question and review
  write ticket|plan                  saves the document read from stdin
  status "a few words"               what the board shows the agent doing
  title "..."                        renames the ticket (scoper and planner)
  ready ["one line"]                 hands the stage's work on
  nothing "why"                      tells the reader the ticket needs no change, with the evidence
  split [--after ID] "title"         files a sub-ticket; its ticket comes on stdin
  suggest "title"                    offers the reader a follow-up; its request comes on stdin
  file "title"                       files a follow-up in the Inbox; its request comes on stdin
  guide                              the reviewer's guide to the branch for the reader, JSON on stdin
  mockup "name"                      keeps an HTML page of how the UI should look, from stdin
  pr "title"                         the builder's pull request title; its description on stdin
  ask "question"                     the builder asks the planner, the planner the scoper
  answer --source S "answer"         the planner answers the builder, the scoper the planner
  reply <ID> changed|answered "..."  answers one of the reader's comments on what you wrote

A scoper acts on one of its sub-tickets with --for ID: show, write ticket,
answer.
  finding "text"                     the reviewer records a finding
  verdict <ID> fixed|rejected "why"  the builder answers a finding
  recheck <ID> holds|fails "note"    the reviewer checks an answer
`

func ticket(args []string, stdin io.Reader) error {
	fl := flag.NewFlagSet("dv ticket", flag.ContinueOnError)
	fl.Usage = func() { fmt.Fprint(fl.Output(), ticketUsage) }
	after := fl.String("after", "", "split: the sub-ticket this one waits for")
	source := fl.String("source", "", `answer: what approved thing the answer follows from, or "you"`)
	part := fl.String("for", "", "the scoper's sub-ticket to act on, in place of its own ticket")
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
	if id == "" || role == "" {
		// Not naming what marks a ticket's agents, which an agent told it might set.
		if verb != "file" {
			return errors.New("this session isn't a board ticket's agent: of dv ticket, it can only file a ticket into the Inbox, with `dv ticket file`")
		}
		return fileTicket(root, rest, stdin)
	}
	if root == "" {
		return errors.New("this session isn't a board ticket's agent: of dv ticket, it can only file a ticket into the Inbox, with `dv ticket file`")
	}
	rep := map[string]any{"role": role, "verb": verb, "args": rest}
	if *part != "" {
		rep["by"], id = id, *part
	}
	switch verb {
	case "write", "split", "file", "suggest", "mockup", "pr", "guide":
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

// fileTicket files a ticket into the Inbox for a session that isn't a board
// ticket's agent: the board of the repository it works in, the main
// checkout's even from a worktree.
func fileTicket(root string, args []string, stdin io.Reader) error {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return errors.New(`give the ticket a title: dv ticket file "title", its request on stdin`)
	}
	body, err := io.ReadAll(stdin)
	if err != nil {
		return err
	}
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		repo, err := gitx.Open(cwd)
		if err != nil || !repo.IsGit() {
			return errors.New("dv ticket file files to a repository's board, and this folder isn't in one")
		}
		root = cmp.Or(repo.MainRoot(), repo.Root)
	}
	srv, ok := store.Announced(root)
	if !ok {
		return fmt.Errorf("no dv is serving %s", root)
	}
	b, _ := json.Marshal(map[string]string{"title": strings.TrimSpace(args[0]), "request": strings.TrimSpace(string(body))})
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(srv.URL, "/")+"/api/board", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	store.MarkLocal(req)
	res, err := (&http.Client{Timeout: time.Minute}).Do(req)
	if err != nil {
		return fmt.Errorf("could not reach dv: %w", err)
	}
	defer res.Body.Close()
	var out struct{ ID, Error string }
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		return fmt.Errorf("dv answered %s", res.Status)
	}
	if out.Error != "" {
		return errors.New(out.Error)
	}
	fmt.Printf("Filed %s in the Inbox, where it waits until the reader moves it on.\n", out.ID)
	return nil
}
