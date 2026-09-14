# dv

A local diff reviewer. Run `dv` in a git repository and it prints a URL; the page
shows the diff, lets you comment on any line, and keeps those comments in a file
inside the repository that git never sees.

```
$ cd ~/code/my-project
$ dv

  my-project — reviewing /home/you/code/my-project
  http://127.0.0.1:41207
  comments → /home/you/code/my-project/.dv/comments.json

  ctrl-c to stop
```

## What it does

**Diff viewer.** Split or unified, syntax highlighted, with word-level
highlighting inside changed lines and expandable context up to the whole file.
An added or deleted file has only one side, so it takes the full width either way.
Collapsed context is a bar you can click anywhere on to reveal 20 more lines, with
expand/`all` controls pinned to both edges of the view. Drag the file list's
edge to make it wider or narrower; a double-click puts it back.

There is no comparison to choose before you start. dv shows whichever one has
something in it — your uncommitted work, else everything on the current branch
since it left the default one, else the last commit — and follows as the work
moves. So a dirty tree opens on your changes; commit them and the view follows
you to that commit; and on the trunk with a clean tree — where there is no
branch to compare against — you land on the last commit. The button names what
it picked. Picking one yourself (uncommitted, staged, last
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
support markdown, replies and resolve, and are listed together in the sidebar's
Comments tab. Everything lands
in `.dv/comments.json` at the repo root. On first run dv adds `/.dv/` to
`.git/info/exclude`, so the notes never show up in `git status` and cannot be
committed by accident — and your `.gitignore` stays untouched.

**Viewed.** *Viewed* on a file's header, or `v`, marks the file read and folds
it away; the foot of the file list counts what is left. A mark belongs to the
comparison it was made in, since having read a file in one diff says nothing
about another. Marks are kept in `.dv/viewed.json`, beside the comments, so they
hold across reloads, tabs and restarts. The page follows both files as they
change, whoever changes them: another tab, an agent, or a reset. *Reset* at the
foot of the file list or the Comments tab — or `dv reset` in a terminal —
deletes every comment and viewed mark to start the review over.

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

**Code navigation.** `Ctrl/Cmd+K` (or `Ctrl/Cmd+Shift+F`) searches the
repository in one list: definitions whose names fuzzy-match first, then every
line containing the text, grouped by file, with regex, case, whole word and
glob filters. Double-clicking an identifier in the diff jumps to where it is
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

**Code mode.** *Diff | Code* in the header (or `m`) switches to reading the
repository rather than the change. The tree becomes every file, marked the way
the diff's list is and VS Code's explorer does it — a changed file's name in its
status colour with `M`, `A`, `U`, `D` or `R` at the end of the row — plus a dot
on folders holding changes. The pane shows one file at a time, whole and
read-only, with a bar beside added and modified lines and a notch where lines
were deleted. The picker in the header now chooses what you read: the working
tree, or any branch or commit as committed, with a branch's changes since it
left the default branch marked. Nothing is checked out. Everything else works
as it does in the diff: comments on any line, go-to-definition (now in place,
with `Alt+←` / `Alt+→` and the header arrows to retrace), `n` / `p` through the
marked changes and `[` / `]` through the changed files. Switching keeps your
place: the line at the top of one view is put at the same height in the other.

**Whole files.** *File* on a file's header, or `f`, opens the whole file at the
line you are on — the one under the pointer, or the first one showing. It is
read from the side of the comparison the diff shows, so the line numbers agree
and a deleted file shows what was deleted; when that is not the working tree,
the viewer names the revision. `Ctrl/Cmd+P` opens any file in the repository by
a fuzzy match on its path, favouring matches in the file name (`srvhand` finds
`server/handlers.go`; spaces separate terms that must all match). Before you
type, it lists the files in the diff.

**Ask Claude.** Press `a`, or hit *Ask* on any file or line selection, and dv
sends that slice of the diff to the local `claude` CLI and streams the answer
beside the code. Sonnet 5 by default; the picker offers Opus 5, Haiku 4.5 and
Fable 5. The answer can be saved into the review as a comment. This needs the
`claude` CLI on your PATH; without it the panel just says so. Drag the panel's
edge to resize it; in a window too narrow to share, it slides over the diff
instead of squeezing it.

**Claude Code's prompts.** When Claude Code stops to ask permission — to edit
a file, run a command, fetch a page, use an MCP tool — the question pops up in
dv too. An edit shows as the diff it would make to the file as it stands:
highlighted, split or unified like the rest, with context to expand, and
double-clicking an identifier opens its definition over the request, with `Esc`
to come back, so you can read around a change before deciding. A command shows
as the command, a plan as the plan. The answers are the terminal's, in its
order — Yes, its "don't ask again" choices, No — and so are the keys: `↑` `↓`
and `Enter`, or the number; `Tab` to add a note, which goes with the answer you
pick (with Yes it reaches Claude beside the result; with No it is the reason,
and Claude carries on with it, where a bare No stops Claude); `Shift+Tab` to
allow all edits for the session. `Esc` puts the request away under the bell in
the header, which counts what is waiting and brings it back; turn off *Pop up
when Claude asks* and that is all a request does. The terminal asks at the same
time, and whichever you answer first wins; the other one goes away. dv only
sees what the terminal would ask about, so in accept-edits mode edits go
straight through, as they would anyway.

It works through Claude Code's hooks, which one command puts in place:

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

## Install

```
curl -fsSL https://raw.githubusercontent.com/priyanshu-shubham/dv/main/install.sh | sh
```

That fetches the latest release for your machine — Linux or macOS, x86-64 or
ARM — checks it against the release's checksums, and puts `dv` in
`~/.local/bin`. `DV_VERSION=v0.2.0` pins a release and `DV_INSTALL_DIR` picks
another directory. On Windows, take the zip from the
[releases page](https://github.com/priyanshu-shubham/dv/releases).

dv needs `git`. `rg` is used for text search when present, with a built-in
scanner as the fallback. If you use Claude Code, run `dv claude install` once.

### From source

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

## Keyboard

| key | action |
| --- | --- |
| `[` / `]` | previous / next file (also `Shift+P` / `Shift+N`) |
| `n` / `p` | next / previous change, into the next file when this one runs out |
| `f` | view the whole file at the line you are on |
| `m` | switch between Diff and Code |
| `c` | comment on the line under the cursor |
| `Esc` | dismiss the composer, step back a definition, or close an overlay |
| `Shift+Esc` | close an overlay outright, however deep the trail |
| `Alt+←` / `Alt+→` | back to the previous definition; in Code mode, back and forward through files |
| `a` | ask Claude about the line under the cursor |
| `v` | mark the current file viewed (in Diff) |
| `u` | toggle split / unified |
| `w` | toggle line wrapping |
| `r` | reload the diff (and look again at what automatic should show) |
| `Ctrl/Cmd+P` | go to file |
| `Ctrl/Cmd+K`, `Ctrl/Cmd+Shift+F` | search definitions and text |
| `↑` `↓` `Enter`, `1`–`9` | answer what Claude is asking; `Tab` adds a note, `Esc` leaves it under the bell |
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
internal/gitx        git plumbing, scope resolution, and the diff algorithm
internal/store       .dv/comments.json, .dv/viewed.json, .dv/server.json and the .git/info/exclude registration
internal/symindex    regex symbol index + ripgrep-backed text search
internal/server      JSON API, SSE ask and prompt endpoints, embedded UI assets
internal/ask         bridge to the `claude` CLI
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

MIT — see [LICENSE](LICENSE).
