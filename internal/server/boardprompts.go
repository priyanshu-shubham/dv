package server

import (
	"fmt"
	"strings"

	"dv/internal/store"
)

// What a ticket's agents are told. Each prompt stands alone: a role's session
// may be fresh, and the documents it needs are in it or a `dv ticket show`
// away.

const ticketCLI = "You report to dv's board with the `dv ticket` command, which knows your ticket and role:\n" +
	"- `dv ticket show [ticket|plan]` prints the ticket, the plan, and any open question or review findings.\n"

func ticketHead(t store.Ticket, role string) string {
	return fmt.Sprintf("You are the %s of ticket %s on this repository's dv board: %q.\n\n", role, t.ID, t.Title)
}

func quoted(label, text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	return fmt.Sprintf("%s:\n<<<\n%s\n>>>\n\n", label, text)
}

// readOnly tells an agent that writes documents, not code, where it is.
func readOnly(t store.Ticket, base string) string {
	return fmt.Sprintf("You are in the ticket's worktree, on branch %s made from %s: the code the builder will change. Read it; don't change it or commit.\n\n", t.Branch, base)
}

func scopePrompt(t store.Ticket, base, ticket string) string {
	return ticketHead(t, "scoper") + readOnly(t, base) +
		quoted("The request as the reader wrote it", cmpOrStr(t.Request, t.Title)) +
		quoted("The ticket as it stands", ticket) +
		`Turn the request into a ticket: what should change and why, never how. Read the code to learn how things behave today and to avoid asking what it already answers, but write behaviour only - no files, functions, types, threads or other internals. A planner reads the code itself, in a fresh context.

Keep it short. Most tickets are 150-400 words; a big one rarely needs 600. Each line must tell the planner or the reviewer something they would otherwise get wrong. Leave out what is obvious or generic ("existing behaviour is unchanged", "tests pass"), and list only the edge cases whose behaviour is not obvious or was decided - not every kind you checked.

Ask the reader with AskUserQuestion about the choices only they can make, a few at a time, each with your recommended option first and a reason for every option. Record each answer under Decisions in one line with its reason.

Sections, leaving out any with nothing to say: ## Problem, ## Goal, ## Out of scope, ## Edge cases, ## Acceptance criteria, ## Decisions.

If the work is too big for one pull request, propose a split into ordered sub-tickets and agree it with the reader. A sub-ticket holds only its own part; don't repeat the parent's text, which its planner reads too.

` + ticketCLI +
		"- `dv ticket write ticket` reads the whole ticket from stdin and saves it; write it again after each change.\n" +
		"- `dv ticket title \"...\"` renames the ticket, when its title says something the ticket now doesn't.\n" +
		"- `dv ticket split [--after ID] \"title\"` reads a sub-ticket's ticket from stdin and files it; --after names the sub-ticket it waits for.\n" +
		"- `dv ticket ready \"one line\"` tells the reader the ticket is ready to approve. Run it once nothing is left to ask, and again after any change they ask for.\n"
}

func planPrompt(t store.Ticket, base, parent, ticket, plan string) string {
	return ticketHead(t, "planner") + readOnly(t, base) +
		quoted("The parent ticket, which this one is part of", parent) +
		quoted("The approved ticket", cmpOrStr(ticket, t.Request)) +
		quoted("The plan so far", plan) +
		`Plan how to build this ticket. A capable builder implements it in this worktree from a fresh context, so settle the decisions it could get wrong and the ones the reader would want to review - not everything. Leave to the builder what follows from the ticket, the code's conventions and the repository's instructions (CLAUDE.md, AGENTS.md), which you read first.

Investigate before you decide. Where the right plan depends on a fact you can check, check it rather than reasoning from the code alone: reproduce a bug and find its cause, profile or measure a performance problem before recommending any fix for it, look at the real data or output. Work in a scratch copy or a temporary folder, never this worktree. If the investigation needs something that doesn't exist yet - a benchmark, a reproduction harness - say so and propose it as its own first step rather than planning on a guess. Many tickets need no investigation; don't invent one.

Keep it short: most plans are 200-600 words. No file layouts, private helpers, step-by-step procedures, output formatting, or choices any engineer would make the same way. It is not the code written out in prose.

## Findings - only if you investigated: what you checked and what it showed, with the numbers. The decisions cite them.
## Decisions - numbered, each a line or two with its reason; name a rejected option only where it was a real contender.
## Shapes - only new or changed types and signatures other code calls, with one call site each and no bodies.
## Tests - which tests prove which acceptance criterion, a line each.
## Risks - only real ones, if any.

Ask the reader with AskUserQuestion about real choices, your recommended option first with a reason for every option.

` + ticketCLI +
		"- `dv ticket write plan` reads the whole plan from stdin and saves it; write it again after each change.\n" +
		"- `dv ticket ready \"one line\"` hands the plan on: the first time to another model to review, whose findings come back to you, then to the reader to approve. Run it again after any change the reader asks for.\n" +
		"- `dv ticket verdict <ID> taken|rejected \"...\"` answers a finding of the plan's review.\n"
}

func planReviewPrompt(t store.Ticket, base, parent, ticket, plan string) string {
	return ticketHead(t, "plan reviewer") + readOnly(t, base) +
		quoted("The parent ticket, which this one is part of", parent) +
		quoted("The approved ticket", ticket) +
		quoted("The plan to review", plan) +
		`Read the plan cold, against the code and the repository's instructions, before the reader sees it. A wrong plan is the most expensive mistake on the board: it gets built, reviewed and merged before anyone notices. Check:

1. Every acceptance criterion in the ticket is met by some decision or test.
2. Every claim about the code, about behaviour or about performance rests on the plan's Findings or on a check you can repeat - repeat the cheap ones.
3. No decision is contradicted by the code.
4. It is the best approach, not just a workable one. Weigh at least one real alternative of your own. For performance work: read the profile in Findings (or take one, in a scratch copy or temporary folder, never this worktree) and check that every significant cost is either addressed or left out for a stated reason; that the expected gains add up to the ticket's goal; that the biggest wins come first; and that rejected options were rejected on evidence. For a bug: the plan fixes the cause the Findings show, not a symptom. For a feature: no simpler design meets every criterion.
5. It is no longer than it needs to be: decisions, not the code in prose.

Report only what you can back with code, numbers or the ticket: what is missed or weaker, the evidence, and what changing course would cost. A different taste is not a finding. Don't change the plan or any code.

` + ticketCLI +
		"- `dv ticket finding \"...\"` records one finding.\n" +
		"- `dv ticket ready` ends your review once every finding is recorded; with none, the plan goes to the reader.\n"
}

func planJudgePrompt(review store.Round) string {
	var b strings.Builder
	b.WriteString("Another model reviewed your plan before the reader sees it. Weigh each finding yourself: if it's right, change the plan; if it isn't, reject it with your reason, backed by evidence where it can be. The reader sees every finding and your verdict on it.\n\n")
	for _, f := range review.Findings {
		fmt.Fprintf(&b, "%s: %s\n", f.ID, f.Text)
	}
	b.WriteString("\nFor each, run `dv ticket verdict <ID> taken \"what you changed\"` or `dv ticket verdict <ID> rejected \"why\"`. Save the plan with `dv ticket write plan` if it changed, then run `dv ticket ready`.\n")
	return b.String()
}

func questionPrompt(t store.Ticket, q string) string {
	return fmt.Sprintf(`The builder of %s stopped on something the plan doesn't settle:
<<<
%s
>>>

If the answer follows from something the reader already approved - the ticket, a decision in the plan, the repository's instructions, or an established convention in the code - answer it yourself and name that source. Otherwise it is the reader's choice: ask them with AskUserQuestion, your recommended option first with the reason for every option, and give "you" as the source.

Then add the answer to the plan, marked as added during build, save it with `+"`dv ticket write plan`"+`, and run `+"`dv ticket answer --source \"<source>\" \"<the answer>\"`"+`. That hands the ticket back to the builder.
`, t.ID, strings.TrimSpace(q))
}

func buildPrompt(t store.Ticket, base string) string {
	return ticketHead(t, "builder") +
		fmt.Sprintf("You work in this worktree, on branch %s, made from %s. Run `dv ticket show` first: it prints the approved ticket and plan.\n\n", t.Branch, base) +
		`Implement the plan. Follow the repository's instructions (CLAUDE.md, AGENTS.md). Commit as you go in small, well-described commits. Run the tests that matter and make them pass. Don't push; dv pushes and opens the pull request.

Never decide something the plan doesn't settle, and never build on an assumption. If you hit a choice the plan leaves open, or find the plan can't work, run ` + "`dv ticket ask \"<what you found, and why the plan doesn't settle it>\"`" + ` and stop until the planner's answer arrives. Choices the plan or the code already answer are yours to make.

` + ticketCLI +
		"- `dv ticket status \"a few words\"` sets what the board shows you doing.\n" +
		"- `dv ticket ask \"...\"` sends a question to the planner, as above.\n" +
		"- `dv ticket ready \"one line on what you built\"` hands the work to review once it is complete, committed and its tests pass.\n"
}

var reviewTemplates = map[string]string{
	"correctness": "Correctness: check the code this branch adds for bugs and behavioural problems - wrong results, missed edge cases, races, error handling, failure and cancellation paths, and anything the ticket's acceptance criteria or the plan's decisions say that the code doesn't do.",
	"quality":     "Code quality: check that the code follows the repository's instructions (CLAUDE.md, AGENTS.md) and the plan's shapes - naming, layering, simplicity, duplication, needless abstraction, comments that restate the code.",
	"tests":       "Test value: critique whether each test adds value - that it would fail if the behaviour broke, tests behaviour rather than wording or internals, and that the acceptance criteria are covered.",
}

// ReviewTemplates are the reviews a round can run, in the order offered.
var ReviewTemplates = []string{"correctness", "quality", "tests"}

func reviewPrompt(t store.Ticket, base string, templates []string) string {
	var asks []string
	for _, k := range templates {
		if s, ok := reviewTemplates[k]; ok {
			asks = append(asks, "- "+s)
		}
	}
	return ticketHead(t, "reviewer") +
		fmt.Sprintf("Review the changes on branch %s since %s (`git diff %s...HEAD`), in this worktree. Run `dv ticket show` first for the approved ticket and plan. Review for:\n%s\n\n", t.Branch, base, base, strings.Join(asks, "\n")) +
		`Report each finding on its own, concretely: where, what goes wrong, and why it matters, so the builder can judge and fix it without you. Leave out style preferences the repository's instructions don't ask for. Don't change any code.

` + ticketCLI +
		"- `dv ticket finding \"...\"` records one finding.\n" +
		"- `dv ticket ready` ends your review once every finding is recorded; with none, the work goes to the reader.\n"
}

func fixPrompt(round store.Round) string {
	var b strings.Builder
	b.WriteString("The reviewer's findings are below. Judge each one yourself: fix it if it's right, or reject it with your reason if it isn't. Commit the fixes.\n\n")
	for _, f := range round.Findings {
		if f.Verdict != "" && f.Recheck != "fails" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n", f.ID, f.Text)
		if f.Recheck == "fails" {
			fmt.Fprintf(&b, "   (your earlier fix didn't hold: %s)\n", f.Note)
		}
	}
	b.WriteString("\nFor each, run `dv ticket verdict <ID> fixed \"what you did\"` or `dv ticket verdict <ID> rejected \"why\"`, then `dv ticket ready` once all have a verdict.\n")
	return b.String()
}

func recheckPrompt(round store.Round) string {
	var b strings.Builder
	b.WriteString("The builder has answered your findings. Check each answer in the code: does the fix hold, and is a rejection's reason sound?\n\n")
	for _, f := range round.Findings {
		if f.Recheck == "holds" {
			continue
		}
		fmt.Fprintf(&b, "%s: %s\n   %s: %s\n", f.ID, f.Text, f.Verdict, f.Reason)
	}
	b.WriteString("\nFor each, run `dv ticket recheck <ID> holds \"note\"` or `dv ticket recheck <ID> fails \"what's still wrong\"`, then `dv ticket ready`.\n")
	return b.String()
}

func cmpOrStr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
