import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { api } from "./api.js";
import { boot } from "./boot.js";
import { blockedNote, BranchText, branchRows, fetchNote, openWorktree, rowHint, rowTag, switchDone, useBranchChoices } from "./Branches.jsx";
import { cx, modKey, useCopy, useDismiss } from "./util.js";
import { HubMark } from "./Machine.jsx";
import {
  IconBack, IconBell, IconBolt, IconBranch, IconCheck, IconChevronDown, IconComment, IconCommit, IconDownload, IconPad, IconPin, IconPlus, IconSearch, IconSettings,
  IconUndo,
} from "./icons.jsx";

const PR_MS = 60000;

// usePR follows the branch's pull request, asked again each minute while the
// page is in view; GitHub is only asked when the server's answer is stale.
export function usePR(branch, remote) {
  const [pr, setPR] = useState(null);
  useEffect(() => {
    setPR(null);
    if (!branch || !remote) return;
    let live = true;
    const read = () => document.hidden || api.pr().then((d) => live && setPR(d.pr), () => {});
    read();
    const t = setInterval(read, PR_MS);
    return () => {
      live = false;
      clearInterval(t);
    };
  }, [branch, remote]);
  return pr;
}

const PR_STATE = { open: "Open", draft: "Draft", merged: "Merged", closed: "Closed" };
const PR_CHECKS = { passing: "checks passing", failing: "checks failing", pending: "checks running" };
const PR_REVIEW = { approved: "approved", changes: "changes requested", required: "review required" };

// PRChip is a branch's pull request, opening it on GitHub: its number, its
// state when not open, and a dot while its checks fail or run. A button, as
// a hub card is a link already.
export function PRChip({ pr }) {
  if (!pr) return null;
  const live = pr.state === "open" || pr.state === "draft";
  const about = [PR_STATE[pr.state], live && PR_CHECKS[pr.checks], live && PR_REVIEW[pr.review]].filter(Boolean).join(", ");
  return (
    <button
      className={cx("pr-chip", pr.state)}
      title={`#${pr.number} ${pr.title}\n${about}`}
      onClick={(e) => {
        e.preventDefault();
        e.stopPropagation();
        window.open(pr.url, "_blank", "noopener");
      }}
    >
      #{pr.number}
      {pr.state !== "open" && <span>{pr.state}</span>}
      {live && (pr.checks === "failing" || pr.checks === "pending") && <span className={cx("pr-checks", pr.checks)} />}
    </button>
  );
}

// BranchRow is the bar's branch and pull request at the top of the sidebar,
// for windows too narrow for the bar to have them.
export function BranchRow({ meta, pr, onFinish, onLog }) {
  if (!meta?.head?.branch && !meta?.head?.sha) return null;
  return (
    <div className="side-branch">
      <HeadRef meta={meta} onFinish={onFinish} onLog={onLog} />
      <PRChip pr={pr} />
    </div>
  );
}

const SWITCH_SHOWN = 12;

// HeadRef is the branch in the bar. It opens on pulling it, and main when on
// another, with a remote to pull from; and on switching to another branch,
// which the server allows only with nothing to lose, or making one at HEAD;
// the repository's other worktrees open their own pages. Any branch's commits
// can be read from it too. Outcomes show in the menu, as the diff follows the
// new HEAD on its own.
function HeadRef({ meta, onFinish, onLog }) {
  const [open, setOpen] = useState(false);
  const [state, setState] = useState(null); // { busy, doing } | { done } | { error }
  const [filter, setFilter] = useState("");
  const ref = useDismiss(open, () => setOpen(false));
  const { branch, sha, subject } = meta.head;
  const trunk = meta.defaultBranch && meta.defaultBranch !== branch && meta.branches?.includes(meta.defaultBranch) ? meta.defaultBranch : "";
  const pulls = meta.remote && (branch || trunk);
  const can = useBranchChoices(open, meta.head);

  const run = (doing, p, done) => {
    setState({ busy: true, doing });
    p.then((r) => setState({ done: done(r) }), (e) => setState({ error: e.message }));
  };
  const pull = (main) =>
    run("Fetching…", api.pull(main), ({ branch, pulled }) => (pulled ? `Pulled ${pulled} commit${pulled === 1 ? "" : "s"} into ${branch}.` : `${branch} is up to date.`));
  const switchTo = (row) => {
    if (row.off || state?.busy) return;
    if (row.worktree) return run(`Opening ${row.worktree.place}…`, openWorktree(row.worktree), () => "");
    run(row.create ? `Making ${row.branch}…` : `Switching to ${row.branch}…`, api.switchTo(row), () => (setFilter(""), switchDone(row) + "."));
  };
  const rows = branchRows(can, meta.branches, branch, filter, SWITCH_SHOWN);
  const note = blockedNote(can) || fetchNote(can);
  // Typing names a branch, so the pulls make way for the matches; Enter starts
  // on the first of those, as before there were arrows.
  const pulling = filter ? [] : [branch && pulls && { main: false, label: `Pull ${branch}` }, trunk && pulls && { main: true, label: `Update ${trunk}` }].filter(Boolean);
  const items = [...pulling.map((p) => ({ pull: p })), ...rows.map((row) => ({ row }))];
  const [sel, setSel] = useState(-1);
  const usable = (i) => items[i] && !state?.busy && !items[i].row?.off;
  const step = (from, by) => {
    for (let i = from + by; i >= 0 && i < items.length; i += by) if (usable(i)) return i;
    return -1;
  };
  const at = usable(sel) ? sel : step(pulling.length - 1, 1) >= 0 ? step(pulling.length - 1, 1) : step(-1, 1);
  const choose = (it) => (it.pull ? state?.busy || pull(it.pull.main) : switchTo(it.row));
  const onKey = (e) => {
    e.stopPropagation();
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const next = step(at, e.key === "ArrowDown" ? 1 : -1);
      if (next >= 0) setSel(next);
    } else if (e.key === "Enter" && at >= 0) choose(items[at]);
    else if (e.key === "Escape") setOpen(false);
  };
  const said = state?.busy ? state.doing : state?.done || state?.error || note;
  return (
    <span className="model-menu headref-menu" ref={ref}>
      <button className="headref" title={subject} aria-expanded={open} onClick={() => (setOpen((o) => !o), state?.busy || setState(null), setFilter(""), setSel(-1))}>
        <IconBranch size={13} />
        <span>{branch || sha}</span>
        <IconChevronDown size={11} className="headref-caret" />
      </button>
      {open && (
        <div className="model-list branch-menu">
          <label className="branch-find">
            <IconSearch size={13} />
            <input
              value={filter}
              placeholder="Find a branch, or name a new one"
              autoFocus
              spellCheck={false}
              onChange={(e) => (setFilter(e.target.value), setSel(-1))}
              onKeyDown={onKey}
            />
          </label>
          <div className="branch-list">
            {!filter && (
              <div className="branch-current">
                <IconBranch size={14} />
                <span className="branch-text">
                  <span className="branch-name">{branch || sha}</span>
                  {subject && (
                    <span className="branch-sub" title={subject}>
                      {subject}
                    </span>
                  )}
                </span>
                {onLog && (
                  <button className="mini branch-finish" title="Read this branch's commits" onClick={() => (setOpen(false), onLog(""))}>
                    Commits
                  </button>
                )}
                {onFinish ? (
                  <button className="mini branch-finish" title="Merge this worktree's branch, then delete the worktree" onClick={() => (setOpen(false), onFinish())}>
                    Finish…
                  </button>
                ) : (
                  !onLog && <IconCheck size={13} className="branch-tick" />
                )}
              </div>
            )}
            {items.map((it, i) => {
              if (it.pull)
                return (
                  <button key={it.pull.label} className={cx("branch-row", i === at && "on")} disabled={state?.busy} onMouseMove={() => setSel(i)} onClick={() => choose(it)}>
                    <IconDownload size={14} />
                    <span className="branch-name ui">{it.pull.label}</span>
                    <span className="branch-hint">fast-forward</span>
                  </button>
                );
              const r = it.row;
              const prev = items[i - 1]?.row;
              const sep = r.worktree ? !prev?.worktree && "Worktrees" : !r.create && (!prev || prev.worktree) && (filter ? "Matches" : "Switch to");
              return (
                <div key={(r.create ? "+" : r.worktree ? "@" : "") + (r.track || r.branch)} className="branch-group">
                  {sep && <div className="branch-sep">{sep}</div>}
                  <div className="branch-row-wrap">
                    <button
                      className={cx("branch-row", i === at && "on", r.create && "create")}
                      disabled={r.off || state?.busy}
                      title={rowHint(r)}
                      onMouseMove={() => setSel(i)}
                      onClick={() => choose(it)}
                    >
                      {r.create ? <IconPlus size={14} /> : <IconBranch size={14} />}
                      {r.create ? (
                        <span className="branch-name ui">
                          Create <b>{r.branch}</b>
                        </span>
                      ) : (
                        <BranchText row={r} can={can} />
                      )}
                      {rowTag(r) && <span className={cx("branch-hint", r.worktree && "place")}>{rowTag(r)}</span>}
                    </button>
                    {onLog && !r.create && (r.track || r.worktree?.branch || !r.worktree) && (
                      <button className="mini branch-log" title={`Read ${r.track || r.branch}'s commits`} onClick={() => (setOpen(false), onLog(r.track || r.branch))}>
                        Commits
                      </button>
                    )}
                  </div>
                </div>
              );
            })}
            {!rows.length && filter.trim() === branch && <div className="branch-empty">You are on {branch}.</div>}
            {!rows.length && !filter && <div className="branch-empty">No other branches. Type a name to make one.</div>}
          </div>
          {said && <div className={cx("branch-note", state?.error && "del", state?.done && "done", !state && can?.blocked && "warn")}>{said}</div>}
        </div>
      )}
    </span>
  );
}

// AUTO is the scope dv starts on: whichever comparison has something in it,
// followed as the work moves. Any other scope is a pin.
export const AUTO = { kind: "auto", rev: "" };

// The bar names the repository in its middle, and holds what is compared and
// the tools at its right; how the page is drawn is in Settings. Which mode the
// page is in is the sidebar's, at the top of the list that mode fills. A phone
// has its own app (phone/Phone.jsx) instead.
export default function Header({
  meta, folder, mode, scope, resolvedScope, onScope, onSearch, onSettings, waiting, arrived, onBell, bellOn,
  comments, commentsOn, onComments, padOn, onPad, settingsDot = {}, pr, onActions, onCloseTask, onFinishWorktree, onLog, onReviewPR, settings,
}) {
  const agent = mode === "agent";
  return (
    <header className="topbar">
      <div className="topbar-left">
        {boot.base ? (
          <a className="logo to-hub" href="/" title="Back to the hub (Alt+H)">
            <IconBack size={13} />
            <HubMark settings={settings} />
          </a>
        ) : (
          <span className="logo">
            <HubMark settings={settings} />
          </span>
        )}
      </div>

      <div className="topbar-title">
        <span className="repo" title={meta?.root}>
          {meta?.repo}
        </span>
        {/* A task's branch is only its folder's git, made to show a diff. */}
        {!onCloseTask && (meta?.head?.branch || meta?.head?.sha) && <HeadRef meta={meta} onFinish={onFinishWorktree} onLog={onLog} />}
        <PRChip pr={pr} />
        {onCloseTask && (
          <button className="mini" onClick={onCloseTask} title="Every session in this task stops, and its folder is deleted">
            Close task
          </button>
        )}
      </div>

      <div className="topbar-right">
      {!agent && !folder && <ScopePicker scope={scope} resolved={resolvedScope} meta={meta} pr={pr} mode={mode} onScope={onScope} onReviewPR={onReviewPR} />}

      {!agent && !folder && <span className="divider" />}

      {waiting > 0 && (
        <button
          className={cx("icon", "bell", bellOn && "on")}
          aria-pressed={bellOn}
          onClick={onBell}
          title={`Waiting on you: ${waiting === 1 ? "one request" : `${waiting} requests`}`}
        >
          {/* Keyed by arrivals, so the bell rings again for each new one. */}
          <span className="bell-glyph" key={arrived}>
            <IconBell size={16} />
          </span>
          <span className="bell-count">{waiting}</span>
        </button>
      )}
      <button
        className={cx("icon", "comments-toggle", commentsOn && "on")}
        aria-pressed={commentsOn}
        onClick={onComments}
        title={`Comments and notes${comments ? `: ${comments} open comment${comments === 1 ? "" : "s"}` : ""}`}
      >
        <IconComment size={16} />
        {comments > 0 && <span className="comments-count">{comments}</span>}
      </button>
      <button className={cx("icon", padOn && "on")} aria-pressed={padOn} onClick={onPad} title="Pad (Alt+P)">
        <IconPad size={16} />
      </button>
      <button className="icon" onClick={onActions} title="Actions (Alt+A)">
        <IconBolt size={16} />
      </button>
      <button className="icon" onClick={onSearch} title={`Search definitions and text (${modKey}+K)`}>
        <IconSearch size={16} />
      </button>
      <button className={cx("icon", settingsDot.dot)} onClick={onSettings} title={`Settings (,)${settingsDot.note || ""}`}>
        <IconSettings size={16} />
      </button>
      </div>
    </header>
  );
}

// ModeSwitch is Diff, Files, Agent and Terminal, at the top of the sidebar
// whose list each of them fills; a folder outside git has no Diff, and dv on
// Windows no Terminal. Files is Code mode, by its name in the code. Agent
// counts what waits to go with the next message.
export function ModeSwitch({ mode, modes, onMode, attached, uncommitted }) {
  const agent = mode === "agent";
  return (
    <div className="seg mode-switch" role="group" aria-label="Mode">
      {modes.includes("diff") && (
        <button
          className={cx(mode === "diff" && "on")}
          aria-pressed={mode === "diff"}
          onClick={() => onMode("diff")}
          title={`The changes (Shift+←/→ steps through the modes)${uncommitted ? ` - ${uncommitted} file${uncommitted === 1 ? "" : "s"} uncommitted` : ""}`}
        >
          Diff
          {uncommitted > 0 && ` (${uncommitted})`}
        </button>
      )}
      <button className={cx(mode === "code" && "on")} aria-pressed={mode === "code"} onClick={() => onMode("code")} title="Every file, one at a time (Shift+←/→ steps through the modes)">
        Files
      </button>
      <button
        className={cx(agent && "on")}
        aria-pressed={agent}
        onClick={() => onMode("agent")}
        title={`Claude Code sessions (Shift+←/→ steps through the modes)${attached ? ` - ${attached} added to your next message` : ""}`}
      >
        Agent
        {/* Keyed by the count, so each addition pulses. */}
        {attached > 0 && (
          <span className="attached-count" key={attached}>
            {attached}
          </span>
        )}
      </button>
      {modes.includes("terminal") && (
        <button
          className={cx(mode === "terminal" && "on")}
          aria-pressed={mode === "terminal"}
          onClick={() => onMode("terminal")}
          title="Shells in this folder (Ctrl+` from anywhere; Shift+←/→ steps through the modes)"
        >
          Terminal
        </button>
      )}
    </div>
  );
}

export const CONTEXT_LINES = [0, 3, 8, 20];

export const PRESETS = [
  { kind: "working", label: "Uncommitted", hint: () => "working tree vs HEAD" },
  { kind: "staged", label: "Staged", hint: () => "index vs HEAD" },
  { kind: "head", label: "Last commit", hint: () => "HEAD vs its parent" },
  { kind: "branch", label: "This branch", hint: (base) => (base ? `since it left ${base}` : "since the merge base") },
];

// prBase is the branch an open pull request merges into, which the server
// measures this branch from instead of the trunk.
export const prBase = (pr) => (pr?.state === "open" || pr?.state === "draft" ? pr.base || "" : "");

// readingLabel names what Code mode shows, which is the comparison's new side.
// Automatic reads as the working tree even when it settled on the last commit:
// it only does that with nothing uncommitted, so the files are the same.
function readingLabel(scope, resolved) {
  const at = resolved?.newAt || "";
  if (scope.kind === "auto" || !at) return "Working tree";
  if (at === "index") return "Staged";
  if (scope.kind === "custom") {
    const rev = scope.rev.trim();
    const right = rev.includes("...") ? rev.split("...")[1] : rev.includes("..") ? rev.split("..")[1] : rev;
    return right.trim() || "Working tree";
  }
  return at;
}

// ScopePicker chooses what the review compares. In the diff that is the
// comparison itself; in Code mode it is the version being read - the working
// tree, or any branch or commit, read-only.
function ScopePicker({ scope, resolved, meta, pr, mode, onScope, onReviewPR }) {
  const [open, setOpen] = useState(false);
  const [custom, setCustom] = useState(scope.kind === "custom" ? scope.rev : "");
  const [wouldShow, setWouldShow] = useState("");
  const ref = useRef(null);
  const auto = scope.kind === "auto";
  const code = mode === "code";
  const base = prBase(pr) || meta?.defaultBranch || "";

  // A click outside or Escape anywhere closes it. Escape is taken in the
  // capture phase so it closes only the menu, not whatever the page has open.
  useEffect(() => {
    if (!open) return;
    const onDown = (e) => {
      if (ref.current && !ref.current.contains(e.target)) setOpen(false);
    };
    const onKey = (e) => {
      if (e.key !== "Escape") return;
      e.stopPropagation();
      setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    window.addEventListener("keydown", onKey, true);
    return () => {
      document.removeEventListener("mousedown", onDown);
      window.removeEventListener("keydown", onKey, true);
    };
  }, [open]);

  // The branch's own commits, each to be shown alone.
  const [commits, setCommits] = useState(null); // { base, commits }
  useEffect(() => {
    if (!open || code) return;
    let live = true;
    api.commits().then((c) => live && setCommits(c), () => {});
    return () => {
      live = false;
    };
  }, [open, code]);

  // What automatic would show now, so the way back says where it leads.
  useEffect(() => {
    if (!open || auto) return;
    let live = true;
    api.diffList(AUTO).then((r) => live && setWouldShow(r.scope?.label || "")).catch(() => {});
    return () => {
      live = false;
    };
  }, [open, auto]);

  const pick = (s) => {
    onScope(s);
    setOpen(false);
  };

  const label = code
    ? readingLabel(scope, resolved)
    : resolved?.label || PRESETS.find((p) => p.kind === scope.kind)?.label || scope.rev || scope.kind;

  const worktree = auto || !resolved?.newAt;
  const against = resolved?.picked === "branch" || scope.kind === "branch" ? base : resolved?.oldAt;
  const branchRev = (b) => (base && b !== base ? `${base}...${b}` : b);
  const branches = (meta?.branches || []).slice(0, 8);
  const onBranch = scope.kind === "custom" && branches.some((b) => branchRev(b) === scope.rev);

  return (
    <div className="scope" ref={ref}>
      <button
        className="scope-button"
        onClick={() => setOpen((o) => !o)}
        aria-expanded={open}
        title={[resolved?.desc, auto ? "follows your work" : "pinned for this tab"].filter(Boolean).join(" - ")}
      >
        <span>{label}</span>
        {!auto && <IconPin size={12} className="pin" />}
        <IconChevronDown size={12} />
      </button>
      {open && (
        <div className="scope-menu">
          {!auto && !code && (
            <>
              <button className="scope-item back" onClick={() => pick(AUTO)}>
                <span className="scope-tick">
                  <IconUndo size={12} />
                </span>
                <span className="scope-label">Back to automatic</span>
                {wouldShow && <span className="scope-hint">would show {wouldShow}</span>}
              </button>
              <div className="scope-rule" />
            </>
          )}

          {code ? (
            <>
              <div className="scope-sep">Reading</div>
              <ScopeItem
                on={worktree}
                label="Working tree"
                sub={worktree && against ? `Changes marked against ${against}` : ""}
                hint="files on disk"
                onClick={() => pick(AUTO)}
              />
              {!worktree && !onBranch && (
                <ScopeItem on mono={resolved?.newAt !== "index"} label={label} hint="read-only" onClick={() => setOpen(false)} />
              )}
              {branches.map((b) => (
                <ScopeItem
                  key={b}
                  on={scope.kind === "custom" && scope.rev === branchRev(b)}
                  mono
                  label={b}
                  hint={branchRev(b) === b ? "as committed" : `since it left ${base}`}
                  onClick={() => pick({ kind: "custom", rev: branchRev(b) })}
                />
              ))}
            </>
          ) : (
            <>
              {PRESETS.map((p) => {
                const on = auto ? resolved?.picked === p.kind : scope.kind === p.kind;
                return (
                  <ScopeItem
                    key={p.kind}
                    on={on}
                    label={p.label}
                    // What the last commit is, which the bar no longer spells out.
                    sub={p.kind === "head" ? meta?.head?.subject : ""}
                    title={p.kind === "head" ? meta?.head?.subject : undefined}
                    hint={p.hint(base)}
                    onClick={() => pick({ kind: p.kind, rev: "" })}
                  />
                );
              })}
              {scope.kind === "pr" && <ScopeItem on label={resolved?.label || `#${scope.rev}`} hint="as GitHub has it" onClick={() => setOpen(false)} />}
              {onReviewPR && (
                <ScopeItem
                  label="A pull request…"
                  hint="as GitHub has it"
                  onClick={() => {
                    setOpen(false);
                    onReviewPR();
                  }}
                />
              )}
              {commits?.commits.length > 0 && (
                <>
                  <div className="scope-sep">{commits.base ? `Commits since ${commits.base}` : "Recent commits"}</div>
                  <div className="scope-commits">
                    {commits.commits.map((c) => (
                      <ScopeItem
                        key={c.sha}
                        className="scope-commit"
                        on={scope.kind === "custom" && scope.rev === c.sha}
                        label={c.subject}
                        sub={`${c.short} · ${c.author}`}
                        hint={c.when}
                        title={c.body ? `${c.subject}\n\n${c.body}` : c.subject}
                        onClick={() => pick({ kind: "custom", rev: c.sha })}
                      />
                    ))}
                  </div>
                </>
              )}
            </>
          )}

          <div className="scope-sep">{code ? "A commit or tag" : "Compare a range"}</div>
          <form
            className="scope-custom"
            onSubmit={(e) => {
              e.preventDefault();
              if (custom.trim()) pick({ kind: "custom", rev: custom.trim() });
            }}
          >
            <input
              value={custom}
              placeholder={code ? "v0.1.0, HEAD~3, abc123" : "main...HEAD, HEAD~3.., abc123"}
              onChange={(e) => setCustom(e.target.value)}
              onKeyDown={(e) => e.stopPropagation()}
            />
            <button className="primary" type="submit" disabled={!custom.trim()}>
              Go
            </button>
          </form>

          {!code && branches.length > 0 && (
            <>
              <div className="scope-sep">Since it left a branch</div>
              {branches.slice(0, 6).map((b) => (
                <button
                  key={b}
                  className="scope-item scope-branch"
                  onClick={() => pick({ kind: "custom", rev: `${b}...` })}
                  title={`Everything since the merge base with ${b}`}
                >
                  <span className="scope-tick">
                    <IconBranch size={12} />
                  </span>
                  <span className="scope-label mono">{b}</span>
                </button>
              ))}
            </>
          )}

          <div className="scope-desc">
            {code
              ? "Branches and commits open read-only, as committed. Nothing is checked out."
              : auto
                ? "Follows your work: uncommitted changes, else this branch, else the last commit. Picking one pins it for this tab."
                : "Pinned for this tab. A new dv session starts automatic again."}
          </div>
        </div>
      )}
    </div>
  );
}

// CommitNote heads the diff of one commit with what its message says. A long
// one starts cut short, two lines of subject and four of body, so the files
// stay in view; a click on it opens the rest and another closes it, unless
// the click ended a selection. Keyed by commit, it starts cut short again for
// the next.
export function CommitNote({ commit }) {
  const [open, setOpen] = useState(false);
  const [long, setLong] = useState(false);
  const [copied, copy] = useCopy(commit.sha);
  const ref = useRef(null);
  // Measured while cut short, and again as the pane's width moves the lines.
  useLayoutEffect(() => {
    if (open) return;
    const over = (el) => el.scrollHeight > el.clientHeight + 1;
    const measure = () => setLong([...ref.current.querySelectorAll(".commit-note-subject, .commit-note-body")].some(over));
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(ref.current);
    return () => ro.disconnect();
  }, [open, commit.sha]);
  const toggle = () => window.getSelection()?.toString() || setOpen((o) => !o);
  return (
    <div
      ref={ref}
      className={cx("commit-note", !open && "short", long && "long")}
      {...(long && {
        role: "button",
        tabIndex: 0,
        "aria-expanded": open,
        onClick: toggle,
        onKeyDown: (e) => e.key === "Enter" && e.target === e.currentTarget && toggle(),
      })}
    >
      <div className="commit-note-head">
        <IconCommit size={14} />
        <div className="commit-note-subject">{commit.subject}</div>
        <span className="commit-note-meta">
          {commit.author}, {commit.when}
        </span>
        <button className={cx("session-id", copied && "copied")} title={`Copy ${commit.sha}`} onClick={copy}>
          {copied ? "copied" : commit.short}
        </button>
        {long && <IconChevronDown size={12} className={cx("commit-note-caret", open && "open")} />}
      </div>
      {commit.body && <div className="commit-note-body">{commit.body}</div>}
    </div>
  );
}

function ScopeItem({ on, label, sub, hint, mono, title, className, onClick }) {
  return (
    <button className={cx("scope-item", on && "on", className)} title={title} onClick={onClick}>
      <span className="scope-tick">{on && <IconCheck size={12} />}</span>
      <span className={cx("scope-label", mono && "mono")}>
        {label}
        {sub && <span className="scope-sub">{sub}</span>}
      </span>
      {hint && <span className="scope-hint">{hint}</span>}
    </button>
  );
}
