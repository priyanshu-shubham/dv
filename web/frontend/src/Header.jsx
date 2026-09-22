import { useEffect, useRef, useState } from "react";
import { api } from "./api.js";
import { boot } from "./boot.js";
import { cx, modKey, useDismiss } from "./util.js";
import {
  IconBack, IconBell, IconBranch, IconCheck, IconChevronDown, IconComment, IconFile, IconKeyboard, IconMenu, IconPin, IconPlus, IconSearch, IconSettings, IconUndo,
} from "./icons.jsx";

// NewSession is the phone's +: a session here, or - where the hub can make
// one - in a new worktree, which it asks between.
function NewSession({ onNew, onNewWorktree, canStart }) {
  const [open, setOpen] = useState(false);
  const ref = useDismiss(open, () => setOpen(false));
  const pick = (fn) => () => (setOpen(false), fn());
  return (
    <div className="model-menu phone-only" ref={ref}>
      <button className="icon" onClick={onNewWorktree ? () => setOpen((o) => !o) : onNew} disabled={!canStart} title="New session (Alt+N)">
        <IconPlus size={15} />
      </button>
      {open && (
        <div className="model-list">
          <button onClick={pick(onNew)}>
            <span className="model-name">New session here</span>
          </button>
          <button onClick={pick(onNewWorktree)}>
            <span className="model-name">New session in a worktree…</span>
          </button>
        </div>
      )}
    </div>
  );
}

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
export function BranchRow({ meta, pr }) {
  if (!meta?.head?.branch && !meta?.head?.sha) return null;
  return (
    <div className="side-branch">
      <HeadRef meta={meta} />
      <PRChip pr={pr} />
    </div>
  );
}

// HeadRef is the branch in the bar; with a remote to pull from it opens on
// pulling it, and main when on another. The outcome shows in the menu, as
// the diff follows the new HEAD on its own.
function HeadRef({ meta }) {
  const [open, setOpen] = useState(false);
  const [state, setState] = useState(null); // { busy } | { done } | { error }
  const ref = useDismiss(open, () => setOpen(false));
  const { branch, sha, subject } = meta.head;
  const trunk = meta.defaultBranch && meta.defaultBranch !== branch && meta.branches?.includes(meta.defaultBranch) ? meta.defaultBranch : "";
  const label = (
    <>
      <IconBranch size={13} />
      <span>{branch || sha}</span>
    </>
  );
  if (!meta.remote || (!branch && !trunk)) {
    return (
      <span className="headref" title={subject}>
        {label}
      </span>
    );
  }
  const pull = (main) => {
    setState({ busy: true });
    api.pull(main).then(
      ({ branch, pulled }) => setState({ done: pulled ? `Pulled ${pulled} commit${pulled === 1 ? "" : "s"} into ${branch}.` : `${branch} is up to date.` }),
      (e) => setState({ error: e.message }),
    );
  };
  return (
    <span className="model-menu headref-menu" ref={ref}>
      <button className="headref" title={subject} onClick={() => (setOpen((o) => !o), state?.busy || setState(null))}>
        {label}
      </button>
      {open && (
        <div className="model-list">
          {branch && (
            <button disabled={state?.busy} onClick={() => pull(false)}>
              <span className="model-name">Pull {branch}</span>
            </button>
          )}
          {trunk && (
            <button disabled={state?.busy} onClick={() => pull(true)}>
              <span className="model-name">Update {trunk}</span>
            </button>
          )}
          <div className={cx("model-note", "headref-note", state?.error && "del")}>
            {state?.busy ? "Fetching…" : state?.done || state?.error || "Only when it fast-forwards."}
          </div>
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
// page is in is the sidebar's, at the top of the list that mode fills. On a
// phone, what is marked wide-only gives way and what is marked phone-only comes
// in: the menu button the sidebar is behind, and buttons for what is otherwise
// in the sidebar or on a key. The menu button is at the edge the sidebar
// slides in from.
export default function Header({
  meta, folder, mode, scope, resolvedScope, onScope, onSearch, onOpenFile, onHelp, onSettings, waiting, arrived, onBell, bellOn,
  comments, commentsOn, onComments, sideOn, onSide, sideRight, onNewSession, onNewWorktree, canStart, update, pr,
}) {
  const agent = mode === "agent";
  const menu = (
    <button className={cx("icon", "phone-only", sideOn && "on")} aria-pressed={sideOn} onClick={onSide} title="Files and sessions">
      <IconMenu size={15} className={cx("menu-glyph", sideOn && "open")} />
    </button>
  );
  return (
    <header className="topbar">
      <div className="topbar-left">
        {!sideRight && menu}
        {/* A phone has no title bar, so the folder's name stands in for dv's, which
            is the one thing there that says where you are. */}
        {boot.base ? (
          <a className="logo to-hub" href="/" title="Back to the hub (Alt+H)">
            <IconBack size={13} />
            <span className="wide-only">dv</span>
            <span className="phone-only here">{meta?.repo || "dv"}</span>
          </a>
        ) : (
          <span className="logo">
            <span className="wide-only">dv</span>
            <span className="phone-only here">{meta?.repo || "dv"}</span>
          </span>
        )}
      </div>

      <div className="topbar-title">
        <span className="repo" title={meta?.root}>
          {meta?.repo}
        </span>
        {(meta?.head?.branch || meta?.head?.sha) && <HeadRef meta={meta} />}
        <PRChip pr={pr} />
      </div>

      <div className="topbar-right">
      {!agent && !folder && <ScopePicker scope={scope} resolved={resolvedScope} meta={meta} mode={mode} onScope={onScope} />}

      {!agent && !folder && <span className="divider wide-only" />}
      {agent && <NewSession onNew={onNewSession} onNewWorktree={onNewWorktree} canStart={canStart} />}

      {waiting > 0 && (
        <button
          className={cx("icon", "bell", bellOn && "on")}
          aria-pressed={bellOn}
          onClick={onBell}
          title={`Waiting on you: ${waiting === 1 ? "one request" : `${waiting} requests`}`}
        >
          {/* Keyed by arrivals, so the bell rings again for each new one. */}
          <span className="bell-glyph" key={arrived}>
            <IconBell size={14} />
          </span>
          <span className="bell-count">{waiting}</span>
        </button>
      )}
      <button
        className={cx("icon", "comments-toggle", commentsOn && "on")}
        aria-pressed={commentsOn}
        onClick={onComments}
        title={`Comments in the review${comments ? `: ${comments} open` : ""}`}
      >
        <IconComment size={14} />
        {comments > 0 && <span className="comments-count">{comments}</span>}
      </button>
      <button className="icon phone-only" onClick={onOpenFile} title={`Open a file (${modKey}+P)`}>
        <IconFile size={14} />
      </button>
      <button className="icon" onClick={onSearch} title={`Search definitions and text (${modKey}+K)`}>
        <IconSearch size={14} />
      </button>
      <button className="icon wide-only" onClick={onHelp} title="Keyboard shortcuts (?)">
        <IconKeyboard size={14} />
      </button>
      <button className={cx("icon", update && "has-update")} onClick={onSettings} title={update ? `Settings (,): dv v${update.latest} is out` : "Settings (,)"}>
        <IconSettings size={14} />
      </button>
      {sideRight && menu}
      </div>
    </header>
  );
}

// ModeSwitch is Diff, Files and Agent, at the top of the sidebar whose list
// each of them fills; a folder outside git has no Diff. Files is Code mode,
// by its name in the code. Agent counts what waits to go with the next message.
export function ModeSwitch({ mode, modes, onMode, attached }) {
  const agent = mode === "agent";
  return (
    <div className="seg mode-switch" role="group" aria-label="Mode">
      {modes.includes("diff") && (
        <button className={cx(mode === "diff" && "on")} aria-pressed={mode === "diff"} onClick={() => onMode("diff")} title="The changes (Shift+←/→ steps through the modes)">
          Diff
        </button>
      )}
      <button className={cx(mode === "code" && "on")} aria-pressed={mode === "code"} onClick={() => onMode("code")} title="Every file, one at a time (Shift+←/→ steps through the modes)">
        Files
      </button>
      <button
        className={cx(agent && "on")}
        aria-pressed={agent}
        onClick={() => onMode("agent")}
        title={`Claude Code and Codex sessions (Shift+←/→ steps through the modes)${attached ? ` - ${attached} added to your next message` : ""}`}
      >
        Agent
        {/* Keyed by the count, so each addition pulses. */}
        {attached > 0 && (
          <span className="attached-count" key={attached}>
            {attached}
          </span>
        )}
      </button>
    </div>
  );
}

export const CONTEXT_LINES = [0, 3, 8, 20];

const PRESETS = [
  { kind: "working", label: "Uncommitted", hint: () => "working tree vs HEAD" },
  { kind: "staged", label: "Staged", hint: () => "index vs HEAD" },
  { kind: "head", label: "Last commit", hint: () => "HEAD vs its parent" },
  { kind: "branch", label: "This branch", hint: (base) => (base ? `since it left ${base}` : "since the merge base") },
];

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
function ScopePicker({ scope, resolved, meta, mode, onScope }) {
  const [open, setOpen] = useState(false);
  const [custom, setCustom] = useState(scope.kind === "custom" ? scope.rev : "");
  const [wouldShow, setWouldShow] = useState("");
  const ref = useRef(null);
  const auto = scope.kind === "auto";
  const code = mode === "code";
  const base = meta?.defaultBranch || "";

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
            PRESETS.map((p) => {
              const on = auto ? resolved?.picked === p.kind : scope.kind === p.kind;
              return (
                <ScopeItem
                  key={p.kind}
                  on={on}
                  label={p.label}
                  // What the last commit is, which the bar no longer spells out.
                  sub={p.kind === "head" ? meta?.head?.subject : ""}
                  hint={p.hint(base)}
                  onClick={() => pick({ kind: p.kind, rev: "" })}
                />
              );
            })
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
            <button className="primary" type="submit">
              Go
            </button>
          </form>

          {!code && branches.length > 0 && (
            <div className="scope-chips">
              {branches.slice(0, 6).map((b) => (
                <button key={b} onClick={() => pick({ kind: "custom", rev: `${b}...` })} title={`Everything since the merge base with ${b}`}>
                  {b}
                </button>
              ))}
            </div>
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

function ScopeItem({ on, label, sub, hint, mono, onClick }) {
  return (
    <button className={cx("scope-item", on && "on")} onClick={onClick}>
      <span className="scope-tick">{on && <IconCheck size={12} />}</span>
      <span className={cx("scope-label", mono && "mono")}>
        {label}
        {sub && <span className="scope-sub">{sub}</span>}
      </span>
      {hint && <span className="scope-hint">{hint}</span>}
    </button>
  );
}
