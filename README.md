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
Collapsed context is a bar you can click anywhere on to reveal 20 more lines, with
expand/`all` controls pinned to both edges of the view.

The scope defaults to **Auto**, which shows whichever comparison actually has
something in it: your uncommitted work, else everything on the current branch,
else the last commit. So a dirty tree opens on your changes; commit them and hit
`r` and the view follows you to that commit; and on the trunk with a clean tree —
where there is no branch to compare against — you land on the last commit. The
button says what it resolved to, tagged `auto`. The picker also pins any scope
explicitly: uncommitted, staged, last commit, this branch, or a revspec you type
(`main...HEAD`, `HEAD~3..`, `abc123`).

**Comments.** Select the code you want to talk about and the composer opens for
those lines — or drag down the line-number gutter, or hover a line and click `+`.
In split view the composer and the thread appear in the column you selected, so a
comment on the new side shows up on the right. Threads render inline in the diff,
support markdown, replies and resolve, and are listed together in the sidebar's
Comments tab. Everything lands
in `.dv/comments.json` at the repo root. On first run dv adds `/.dv/` to
`.git/info/exclude`, so the notes never show up in `git status` and cannot be
committed by accident — and your `.gitignore` stays untouched.

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

**Code navigation.** `Ctrl/Cmd+K` fuzzy-searches every definition in the
repository; double-clicking an identifier in the diff jumps to where it is
defined. Definition lookup is exact and whole-word — clicking `inflight` will
never offer you `MaxInflightLogChunks` — and comments and string literals are
excluded, so it lands on the line that declares a name rather than one that
mentions it. Results are ranked by distance from the file you are reading: same
file, then same directory, then the nearest shared path. Names the index does
not carry on its own (struct fields, parameters, locals) are found by a second
pass that keeps only declaration-shaped lines, and the picker says when that is
what you are looking at. Jumps stack, so following a call into a definition and
an identifier in *that* into another one leaves a trail: `Esc` or `Alt+←` steps
back to where you came from — at the line you were reading, not the top of the
file — and the `←` button in the header names the place it returns to.
`Shift+Esc` leaves the whole chain at once. `Ctrl/Cmd+Shift+F` is a repo-wide text search with
regex, case, whole word and glob filters. The symbol index is built in-process
from the files git tracks, so there is nothing to install and no daemon to run.

**Ask Claude.** Press `a`, or hit *Ask* on any file or line selection, and dv
sends that slice of the diff to the local `claude` CLI and streams the answer
beside the code. Sonnet 5 by default; the picker offers Opus 5, Haiku 4.5 and
Fable 5. The answer can be saved into the review as a comment. This needs the
`claude` CLI on your PATH; without it the panel just says so.

## Install

```
make install     # builds the UI bundle and the binary, installs to ~/.local/bin
```

Requires Go 1.24+ and Node (for the one-time asset bundle). `rg` is used for
text search when present, with a built-in scanner as the fallback.

The UI bundle in `internal/server/static` is generated, not checked in, so
`make build` is the one required step after cloning or after changing anything
under `web/frontend`. A binary built without it refuses to start rather than
serving a blank page.

## Flags

```
-C <dir>      repository to review (default: the current directory)
-port <n>     port to listen on (default: the first free one from 41100)
-host <addr>  address to bind (default: 127.0.0.1)
-no-open      don't open a browser
-version      print the version
```

## Keyboard

| key | action |
| --- | --- |
| `j` / `k` | next / previous file |
| `n` / `p` | next / previous change |
| `c` | comment on the line under the cursor |
| `Esc` | dismiss the composer, step back a definition, or close an overlay |
| `Shift+Esc` | close an overlay outright, however deep the trail |
| `Alt+←` | back to the previous definition |
| `a` | ask Claude about the line under the cursor |
| `v` | mark the current file viewed |
| `u` | toggle split / unified |
| `w` | toggle line wrapping |
| `r` | reload the diff (and re-resolve the auto scope) |
| `Ctrl/Cmd+K` | go to symbol |
| `Ctrl/Cmd+Shift+F` | search the repository |
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
main.go              CLI entry point: open the repo, start the server, print the URL
internal/gitx        git plumbing, scope resolution, and the diff algorithm
internal/store       .dv/comments.json and the .git/info/exclude registration
internal/symindex    regex symbol index + ripgrep-backed text search
internal/server      JSON API, SSE ask endpoint, embedded UI assets
internal/ask         bridge to the `claude` CLI
web/frontend         React UI, bundled by esbuild into internal/server/static
```

Diffs are computed in-process rather than parsed out of `git diff`, because the
UI wants both complete file sides in one payload — that is what makes expanding
context free and keeps syntax highlighting correct across hunk boundaries. The
algorithm is patience decomposition (split on lines unique to both sides) with a
traced Myers search on the leftover blocks.
