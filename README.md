# dv

A local diff reviewer. Run `dv` in a git repository and it prints a URL; the page
shows the diff, lets you comment on any line, and keeps those comments in a file
inside the repository that git never sees. Run it in a folder outside git and
it opens the same page on the folder's files, without the diff.

It works with Claude Code too. Start or resume sessions beside the diff, and
read each edit Claude makes as a diff you can comment on: the comments go to
Claude with your next message. Claude's permission prompts come up in the page,
whether dv runs the session or your terminal does.

```
$ cd ~/code/my-project
$ dv

  my-project — reviewing /home/you/code/my-project
  http://127.0.0.1:41207
  comments → /home/you/code/my-project/.dv/comments.json

  ctrl-c to stop
```

## Install

```
curl -fsSL https://raw.githubusercontent.com/priyanshu-shubham/dv/main/install.sh | sh
```

That fetches the latest release for your machine — Linux or macOS, x86-64 or
ARM — checks it against the release's checksums, and puts `dv` in
`~/.local/bin`. `DV_VERSION=v0.2.0` pins a release and `DV_INSTALL_DIR` picks
another directory. On Windows, take the zip from the
[releases page](https://github.com/priyanshu-shubham/dv/releases).

dv needs `git` for the diff. `rg` is used for text search when present, with a
built-in scanner as the fallback. If you use Claude Code, run
`dv claude install` once. To build it yourself, see [From source](#from-source).

## What it does

**Diff viewer.** Split or unified, syntax highlighted, with word-level
highlighting inside changed lines and expandable context up to the whole file.
A changed line is told by its shade and its line numbers, without a `+` or `-`.
An added or deleted file has only one side, so it takes the full width either
way, with its code lined up with the other files'.
Collapsed context is a bar you can click anywhere on to reveal 20 more lines, with
expand/`all` controls pinned to both edges of the view. Drag the file list's
edge to make it wider or narrower; a double-click puts it back.

There is no comparison to choose before you start. dv shows whichever one has
something in it — your uncommitted work, else everything on the current branch
since it left the default one, else the last commit — and follows as the work
moves. So a dirty tree opens on your changes; commit them and the view follows
you to that commit; and on the trunk with a clean tree — where there is no
branch to compare against — you land on the last commit. The button at the right
of the header names what it picked, and its menu shows the last commit's message.
Picking one yourself (uncommitted, staged, last
commit, this branch, or a revspec you type: `main...HEAD`, `HEAD~3..`, `abc123`)
pins it for that tab, marked with a pin; *Back to automatic* at the top of the
menu says what following would show now, and a new session starts automatic.

**Live.** The page keeps up with the repository as it changes — an agent
editing files, a formatter, a commit — without being reloaded, and without
losing your place. New files appear, removed ones go, and a file whose content
changed is refetched and swapped in where it stands: the line you were reading
stays at the same height on screen, context you expanded stays expanded, and a
file you are writing a comment on waits until you finish. It costs one
`git status` every second and a half (less often in a repository where that is
slow, and not at all while the tab is hidden). `r` still reloads everything.

**Comments.** Select the code you want to talk about and the composer opens for
those lines — or drag down the line-number gutter, or hover a line and click `+`.
In split view the composer and the thread appear in the column you selected, so a
comment on the new side shows up on the right. Threads render inline in the diff —
a line that carries one is never folded away, whatever the context setting — and
support markdown, replies and resolve. The comment button in the header, which
counts the open ones, lists them all in a panel on the right, in any mode; drag
its edge to size it. Everything lands
in `.dv/comments.json` at the repo root. On first run dv adds `/.dv/` to
`.git/info/exclude`, so the notes never show up in `git status` and cannot be
committed by accident — and your `.gitignore` stays untouched.

**Viewed.** *Viewed* on a file's header, or `v`, marks the file read and folds
it away; the foot of the file list counts what is left. A mark belongs to the
comparison it was made in, since having read a file in one diff says nothing
about another. Marks are kept in `.dv/viewed.json`, beside the comments, so they
hold across reloads, tabs and restarts. The page follows both files as they
change, whoever changes them: another tab, an agent, or a reset. *Reset* at the
foot of the file list — or `dv reset` in a terminal — deletes every comment and
viewed mark to start the review over.

**Generated files are skipped.** A regenerated lock file or protobuf stub
between two real changes is noise that hides them, so dv lists those files but
does not render the diff — it says so, with the line count, and a *Show it
anyway* button next to it. The body is not even fetched until you ask, so a
branch that rewrites `package-lock.json` costs nothing to open. A file counts
as generated if its path says so (lock files, `vendor/`, `node_modules/`,
`*.pb.go`, `*.min.js`, `__snapshots__/` and friends), if it carries a
`generated … do not edit` banner in its first lines — which is how `mockgen`,
`stringer`, `sqlc` and `protoc` output is caught regardless of its name — or if
`.gitattributes` marks it `linguist-generated`, which overrules the guesses.
*Hide generated files*, in the panel behind the funnel above the file list,
hides them altogether — from the list, the diff and file stepping — and stays
that way until you turn it off.

**Include and exclude.** The same panel narrows the review to the paths you
care about, with *files to include* and *files to exclude* as
comma-separated globs: `src/, *.go`, `**/*_test.go, docs/`. A pattern without a
slash matches a file or folder name anywhere, one with a slash is anchored at
the repository root, and a folder takes everything inside it. Like hiding
generated files, it applies to the list, the diff and file stepping, and it is
remembered. While either is on the funnel lights up, and a note above the list
says how many files are hidden and why. *Show all* pauses the filters rather
than clearing them: *Resume*, or editing one, turns them back on as they were.

The box above the list, in Diff and Files, only narrows the list, and takes a
piece of a path (`handlers`) or a glob read the same way (`*.go`,
`internal/**/*.ts`). Several go comma-separated, and one starting with `!`
hides what it matches: `*.go, !*_test.go`.

**Find in page.** `Ctrl/Cmd+F` opens dv's own find bar rather than the
browser's, which could only see the rows near the screen: the diff loads files
as you reach them and draws only what is close by. dv's searches every row the
diff shows at the current context, loading the files it has not got yet, and
the path in every file's header, so the count covers the whole review.
`Enter` / `Shift+Enter` (or `F3`) step through the matches, bringing each into
view. As on screen, folded context is left out, and a file marked viewed or a
hidden generated file is found by its header alone. In Files mode it searches
the open file's lines, as an editor's find does; `Ctrl/Cmd+P` is the way to a
file by name. It starts from the selection, like search, and has the same case,
whole word and regex toggles.

**Code navigation.** `Ctrl/Cmd+K` (or `Ctrl/Cmd+Shift+F`) searches the
repository in one list: definitions whose names fuzzy-match first, then every
line containing the text, grouped by file, with regex, case, whole word and
glob filters. With text selected on one line, it opens searching for that,
even once the selection has opened a composer, which closes again if nothing
was typed in it. Double-clicking an identifier in the diff jumps to where it is
defined; when it has several definitions, or none, the same search opens with
the candidates on top and the word's uses below. Definition lookup is exact and whole-word — clicking `inflight` will
never offer you `MaxInflightLogChunks` — and comments and string literals are
excluded, so it lands on the line that declares a name rather than one that
mentions it. Results are ranked by distance from the file you are reading: same
file, then same directory, then the nearest shared path. Names the index does
not carry on its own (struct fields, parameters, locals) are found by a second
pass that keeps only declaration-shaped lines, and the search says when that is
what you are looking at. Jumps stack, so following a call into a definition and
an identifier in *that* into another one leaves a trail: `Esc` or `Alt+←` steps
back to where you came from — at the line you were reading, not the top of the
file — and the `←` button in the header names the place it returns to.
`Shift+Esc` leaves the whole chain at once. The symbol index is built in-process
from the files git tracks, so there is nothing to install and no daemon to run.
It knows the definitions of Go, JavaScript and TypeScript, Python, Rust, Ruby,
Java, Kotlin, Scala, C#, C, C++, Objective-C, PHP, shell, SQL, Protocol Buffers
(messages, enums and their values, services, rpcs), Terraform and HCL, CSS,
Elixir, Lua and Swift, plus Makefile targets and Markdown headings.

**Files mode.** *Files*, at the top of the sidebar (or `Shift+→`), switches to reading the
repository rather than the change. The tree becomes every file, marked the way
the diff's list is — `M`, `A`, `U`, `D` or `R` in its colour at the end of a
changed file's row — plus a dot
on folders holding changes. The pane shows one file at a time, whole and
read-only, with a bar beside added and modified lines and a notch where lines
were deleted. The picker in the header now chooses what you read: the working
tree, or any branch or commit as committed, with a branch's changes since it
left the default branch marked. Nothing is checked out. Everything else works
as it does in the diff: comments on any line, go-to-definition (now in place,
with `Alt+←` / `Alt+→` and the header arrows to retrace), `n` / `p` through the
marked changes and `[` / `]` through the changed files. Switching keeps your
place: the line at the top of one view is put at the same height in the other.

**Markdown.** A Markdown file has *Preview* on its header, which shows it
rendered, as a repository host would: tables, task lists, highlighted code
blocks, front matter as a block of YAML, and the HTML a README leans on (centred
logos, `<details>`) with scripts, styles and event handlers stripped out. Images
and links written relative to the file resolve in the repository, and a link to
another file opens it in dv. In Files mode the choice holds for every Markdown
file and toggling keeps your place; `Ctrl/Cmd+F` over a preview is the
browser's own find, since the whole page is drawn. The same button is on a
Markdown file's card in the diff, on Claude's edit to one in the Agent view, and
on an edit Claude asks permission for, showing the file as the change leaves it.
An edit whose transcript kept only the changed lines has no preview: those alone
would not render. An SVG has *Preview* too, which draws it (before and after, in
the diff).

**Images, video, sound and PDFs.** These show as themselves wherever a file
does: in Files mode, in the window *File* and `Ctrl/Cmd+P` open, and in the
diff, where a changed one is shown before and after, side by side. An image
sits on a checkerboard so its transparent edges show, with its size under it; a
video or sound plays, with its length, and seeks without being read whole. A PDF
opens in the browser's own viewer, with a link to open it in a tab of its own,
which is the way to one on a phone. What is shown follows the file as it
changes. PNG, JPEG, GIF, WebP, AVIF, BMP and ICO; MP4, WebM, MOV and Ogg video;
MP3, WAV, Ogg, Opus, M4A and FLAC - as far as the browser can play them.

**Outside git.** In a folder that is not a git repository there is no diff, so
the sidebar offers Files and Agent and the page opens on the files. The rest
works as in a repository: comments, search and go-to-definition, find, Markdown
previews, the Agent view, Claude Code's prompts, and live updates as files
change. The listing leaves out `node_modules`, `.venv`, `__pycache__`, `.cache`
and version control's directories, and stops at 50,000 files. The notes go in
`.dv/` in the folder; with no `.git/info/exclude` to add it to, it is left for
you to ignore.

**Whole files.** *File* on a file's header, or `f`, opens the whole file at the
line you are on — the one under the pointer, or the first one showing. It is
read from the side of the comparison the diff shows, so the line numbers agree
and a deleted file shows what was deleted; when that is not the working tree,
the viewer names the revision. Select code in it, or drag its line numbers, to
comment as anywhere else. `Ctrl/Cmd+P` opens any file in the repository by
a fuzzy match on its path, favouring matches in the file name (`srvhand` finds
`server/handlers.go`; spaces separate terms that must all match). Before you
type, it lists the files in the diff.

**Settings.** The sliders button at the right of the header, or `,`, opens
dv's settings, kept in the browser: the theme and the colours code is
highlighted in (GitHub, One, Solarized, Tomorrow, or Monokai, which is dark
only), split or unified, the context around each change, line wrapping, desktop
notifications, and whether a session opened by adding to it starts temporary.

**Agent.** *Agent* at the top of the sidebar (or `Shift+→`), lists the repository's Claude
Code sessions and lets you work in them next to the diff: start one, pick up an
old one, send messages, stop it, change its model or permission mode
(`Shift+Tab`, as in the terminal). dv runs these through the `claude` CLI, which
has to be on your PATH. The conversation reads like the terminal's, except that
an edit Claude made is a diff card, highlighted and split or unified like the
rest. You can comment on those cards as on any diff. The comment goes into the
review and onto your next message, so Claude sees what you said about its edit.
To give Claude something to look at, press `a` on a line, or use *Add* on a
file or selection, in Diff, Files or the file window a definition opens in. It
turns into a chip on a session's message box, to go with the next message you
send: the first open session's, or the one picked in the menu beside the button,
which stays picked (pointing at *Add* names it). The round button after it adds
to a new session instead and takes you there. The session's card says how much
is waiting there, and the header counts it all. dv keeps a list of the sessions
you have open in `.dv/agent.json`.

Select text in a conversation - something Claude said, a command, a tool's
output - and a small bar over it offers *Reply*, which quotes it in this
session's message box, and *New session*, which starts one with it quoted there.
A selection that starts or ends partway into a word takes the whole word.

With no session picked, or after `+`, the view asks what you want to do, with a
few places to start. No session exists until you send the first message. The
message box is a card: `+` adds images (so does pasting or dropping them in),
then the model with its effort in one menu, the permission mode, and the send
button, which stops Claude while it works and the box is empty. These start at
what your Claude Code settings say. Your messages sit to the right, with any
images you sent.

A `/` at the start of the box suggests the slash commands Claude Code takes
there - its own that work outside the terminal, and your commands and skills -
with what each does; `↑` / `↓` pick one and `Tab` or `Enter` fills it in. Once
the box holds a command, it is written in the code font with the command named
under it. A command is sent on its own: images and anything added stay in the
box for the message after it. What it prints shows under it in a card - where
the terminal would open a panel, such as `/context` or `/autocompact`, Claude
Code prints the same as text. `/clear` starts a new session, as `+` does; a few
commands that mean nothing outside a terminal (`/color`, `/fast`) are left out.

A message sent while Claude is working shows as queued until Claude takes it
in; `Esc`, or `↑` in the empty box, takes it back to edit. For five seconds
after you send, `Esc` takes that message back as well: Claude stops, the
conversation (and any file it got as far as changing) goes back to before it,
and the message returns to the box. Otherwise `↑` and `↓` in the empty box step
through what you said, as in the terminal. `Esc Esc` is the terminal's rewind:
pick one of your messages or commands, then restore the conversation, the code, or both.
The list goes back to the session's start, past compactions the page is not
showing: rewound to before one, the conversation comes back as it was then.
The message comes back into the box with its images and chips.

The other tool calls are one line each, saying what they found: the lines a Read
got, a search's matches, a command's exit. Click one for the rest: the lines
read, highlighted, with their numbers opening the file; a search's matches,
grouped by file; a command's output; a fetched page or a web search's links; an
image Claude looked at. Calls made one after another fold, once done, into one
line saying what they did together — *Read 3 files, ran 2 commands* — which
opens on each; edits stay diff cards. A command or agent sent to the background
stays running until Claude Code reports it finished, failed or stopped. While
Claude works,
an orb under the conversation moves with the kind of work, beside what it is
doing. The view follows the conversation as it grows; scrolled up, it keeps
your place while diffs load above you, and *Latest* takes you back down. Once
your message is scrolled out of sight, the header names it, and `[` / `]` step
between your messages. Each session keeps its place as you switch between them
(`Shift+↑` / `Shift+↓` for the open ones). If dv stops, the view says it lost
the connection and picks up again when dv is back.

Each session in the list is a small card: its name, where it runs, the last
thing said in it, the start of its ID (click it to copy the whole ID, for
`claude --resume`), and how full its context is. The filter above the list
matches names and IDs. Double-click the name to rename it (the terminal's
`/rename`). Open sessions keep the order you opened them in, so two at work do
not trade places. Closing a session, with `×` on its card or in its
header, stops the Claude Code dv runs for it and moves it to Recent, from where
it carries on when you next send to it. For a session running in a terminal,
closing only stops following it: the terminal carries on, and goes back to being
the only place it asks for permission. A session marked *Temporary* (the dashed
circle by the permission mode) leaves the list once closed instead; its
transcript stays for `claude --resume`. A card whose session is waiting on you
has a pulsing amber dot and says so.

The session's header has how full its context is, with a tick where Claude Code
compacts it; near there it says how close. Click it to compact now (the
terminal's `/compact`). A change to the compaction window in
your settings shows within seconds. While Claude Code compacts, the orb says so
and the session's card reads *compacting*; a terminal does not tell dv, so there
it says so once the context is past the compaction point. A compaction is a rule across the
conversation, with the tokens before and after, and clicking it shows the
summary Claude carries on from. A session opens at its latest compaction -
what Claude still has in front of it - and *Show the conversation before this*
steps back a compaction at a time, keeping your place. A long session's file is
only read from there until you do, so it opens in a fraction of the time.
A change of permission mode is a rule too ("Entered plan mode", "Switched to
Auto"); Claude Code
records it a little after the change, so the rule can come a step or two late.
On a Claude plan, the foot of the session list shows how much of the five-hour
and weekly limits you have used.

What Claude has left at work - agents it started, and commands it sent to the
background - is pinned over the message box while it runs. Click one to see it
in place of the conversation: an agent's own conversation, followed as it goes,
or a command's output as it comes. `Esc` or the arrow in the header goes back to
the conversation. An agent's call also opens on its conversation from its line
(*Conversation*, on pointing at it), after it has finished too.

**Notices.** A session not on screen that asks permission, or finishes its
turn, says so in a notice at the top right, wherever in dv you are: *Review*
opens the request, *Open session* goes to it. With desktop notifications on in
Settings, the same come as desktop notifications while dv's tab is in the
background, and the tab's title says when Claude has finished.

A session that is open in a terminal can be read in dv but not written to,
because two writers would fork its transcript and the terminal never reloads it.
dv follows it as it grows and shows its permission prompts.

**Claude Code's prompts.** When a session open in the Agent view stops to ask
permission — to edit a file, run a command, fetch a page, use an MCP tool — the
question comes up in dv: in the conversation when that session is on screen, and
otherwise as a notice whose *Review* opens it in a window over the page.
Sessions you have not opened in dv stay the
terminal's business. An edit shows as the diff it would make to the file as it
stands: highlighted, split or unified like the rest, with context to expand, and
double-clicking an identifier opens its definition over the request, with `Esc`
to come back, so you can read around a change before deciding. Comment on its
lines as on any diff, and the comments go with your answer: with No they are the
reason, and Claude revises the edit; with Yes they reach Claude as a note. A
command shows as the command, a plan as the plan. The answers are the
terminal's, in its order — Yes, its "don't ask again" choices, No — and so are
the keys: `↑` `↓` and `Enter`, or the number; `Tab` to add a note, which goes
with the answer you pick (with Yes it reaches Claude beside the result; with No
it is the reason, and Claude carries on with it, where a bare No stops Claude);
`Shift+Tab` to allow all edits for the session. `Esc` puts the request away
under the bell in the header, which counts what is waiting and brings it back. For a
terminal session, the terminal asks at the same time, and whichever you answer
first wins; the other one goes away. dv only sees what the terminal would ask
about, so in accept-edits mode edits go straight through, as they would anyway.

When Claude asks you questions, you answer them in dv too, as in the terminal:
one at a time under a tab each, with `←` `→` between them. Picking an option
moves on; where several can be picked, the *Submit* row does. Claude's drawing
of an option shows beside the list, *Something else* takes your own answer, and
`Tab` goes to a note on the answer, which Claude reads with it. The last tab
sums up your answers, with rows to send them or cancel. Half-answered questions
keep their place if you put them away or reload.

For terminal sessions this works through Claude Code's hooks, which one command
puts in place:

```
dv claude install
```

That adds three hooks to `~/.claude/settings.json` (or `$CLAUDE_CONFIG_DIR`),
beside any you have, and running it again after moving dv updates them rather
than adding more. Each runs `dv claude hook`, which looks for a dv open on the
session's repository and does nothing when there is none, so Claude Code
behaves as it always has wherever dv is not open. `PermissionRequest` is the
question; the two `PostToolUse` hooks are how dv hears that the terminal
answered first, and how a Yes's note reaches Claude.

## From source

```
make install     # builds the UI bundle and the binary, installs to ~/.local/bin
```

Requires Go 1.24+ and Node (for the one-time asset bundle).

The UI bundle in `internal/server/static` is generated, not checked in, so
`make build` is the one required step after cloning or after changing anything
under `web/frontend`. A binary built without it refuses to start rather than
serving a blank page.

### Releasing

Push a version tag — `git tag v0.2.0 && git push origin v0.2.0`. The release
workflow runs the tests, builds every platform with GoReleaser and publishes the
archives and `checksums.txt` as a GitHub release, which is what `install.sh`
downloads. `goreleaser release --snapshot --clean` builds the same archives
into `dist/` without publishing anything.

## Flags

```
-C <dir>      repository to review (default: the current directory)
-port <n>     port to listen on (default: one derived from the repository's path,
              in 41100-41999, so each repository keeps its own URL)
-host <addr>  address to bind (default: 127.0.0.1)
-no-open      don't open a browser
-version      print the version
```

`dv claude install` puts dv's hooks in Claude Code's settings; see *Claude
Code's prompts* above. `dv claude hook` is what those hooks run.

`dv reset` deletes the review — every comment and viewed mark — after asking.
`-y` skips the question, and is needed when stdin is not a terminal. A dv
already running on the repository picks the change up, and so do its pages.

## On a phone

Under 760px wide, dv is one column. The menu button at the top left slides the
sidebar in over the page, and the comments button does the same for the
comments from the right; one is open at a time, and picking a file, a session
or a comment puts it away. The diff is unified, with one line number and long
lines wrapped (Settings turns that off, and a phone keeps its own
setting; unwrapped, drag a line sideways to read the rest). Tap a line number to comment on the line; a selection comments on the
lines it covers once you let its handles rest; a double tap on a name goes to
its definition. The buttons that show on pointing at a line show always, and
the on-screen keyboard shrinks the page rather than covering the message box.
A session's header has its context as a percent, and near compacting, how much
is left. Android's keyboard and Paste put only text in the message box, so `+`
there offers *Paste image*, which reads what was copied; browsers allow that only
over https or on localhost, and elsewhere `+` picks a photo or file.

## Keyboard

| key | action |
| --- | --- |
| `[` / `]` | previous / next file (also `Shift+P` / `Shift+N`) |
| `n` / `p` | next / previous change, into the next file when this one runs out |
| `f` | view the whole file at the line you are on |
| `Shift+←` / `Shift+→` | previous / next mode: Diff, Files, Agent (in the Agent view too, while the message box is empty) |
| `c` | comment on the line under the cursor |
| `Esc` | dismiss a composer with nothing typed in it, step back a definition, or close an overlay |
| `Shift+Esc` | close an overlay outright, however deep the trail |
| `Alt+←` / `Alt+→` | back to the previous definition; in Files mode, back and forward through files |
| `a` | add the line under the cursor, or the file, to your next message to Claude |
| `Esc` `Esc` | in the Agent view, rewind the session to before one of your messages |
| `Esc` | in the Agent view, take back a queued message or one sent in the last five seconds; otherwise stop Claude |
| `↑` / `↓` | in the Agent view's empty message box, bring back a queued message, or step through what you said |
| `[` / `]` | in the Agent view, your previous / next message |
| `Shift+Tab` | in the Agent view's message box, change the permission mode |
| `Shift+↑` / `Shift+↓` | in the Agent view, the previous / next open session (from the message box, when it is empty) |
| `v` | mark the current file viewed (in Diff) |
| `u` | toggle split / unified |
| `w` | toggle line wrapping |
| `r` | reload the diff (and look again at what automatic should show) |
| `Ctrl/Cmd+P` | go to file |
| `Ctrl/Cmd+F` | find in the page, all of it, not just the rows drawn so far; `Enter` / `Shift+Enter` or `F3` step |
| `Ctrl/Cmd+K`, `Ctrl/Cmd+Shift+F` | search definitions and text, starting from the selection |
| `↑` `↓` `Enter`, `1`–`9` | answer what Claude is asking; `Tab` adds a note, `Esc` leaves it under the bell |
| `←` / `→` | between Claude's questions, when it asks several |
| `,` | settings |
| `?` | show all shortcuts |

## The comments file

```json
{
  "format": 1,
  "repo": "my-project",
  "threads": [
    {
      "id": "t_9f2c...",
      "file": "internal/server/handlers.go",
      "side": "new",
      "startLine": 88,
      "endLine": 91,
      "quote": ["\tif err != nil {", "\t\treturn err", "\t}"],
      "scope": "Uncommitted",
      "baseSha": "fa4ae86",
      "resolved": false,
      "comments": [{ "id": "c_1a...", "author": "you", "body": "this swallows the cause" }]
    }
  ]
}
```

`quote` holds the lines the thread was attached to, so a comment is still
findable after the code beneath it moves — which is what makes the file useful
to hand to an agent along with "address these".

## Layout

```
main.go              CLI entry point: open the repo, start the server, print the URL; dv reset, dv claude
internal/gitx        git plumbing, scope resolution, the diff algorithm, and folders outside git
internal/store       .dv/comments.json, .dv/viewed.json, .dv/agent.json, .dv/server.json and the .git/info/exclude registration
internal/symindex    regex symbol index + ripgrep-backed text search
internal/server      JSON API, SSE session and prompt endpoints, embedded UI assets
internal/agent       Claude Code sessions: transcripts, the headless `claude` processes dv runs, rewinds
internal/permit      Claude Code's permission prompts: the hook and its install, the requests waiting, edit previews
web/frontend         React UI, bundled by esbuild into internal/server/static
install.sh           the curl | sh installer, reading the GitHub releases
.goreleaser.yaml     release builds, run by .github/workflows/release.yml on a v* tag
```

Diffs are computed in-process rather than parsed out of `git diff`, because the
UI wants both complete file sides in one payload — that is what makes expanding
context free and keeps syntax highlighting correct across hunk boundaries. The
algorithm is patience decomposition (split on lines unique to both sides) with a
traced Myers search on the leftover blocks.

## License

MIT — see [LICENSE](LICENSE). The UI ships JetBrains Mono, under the SIL Open
Font License 1.1.
