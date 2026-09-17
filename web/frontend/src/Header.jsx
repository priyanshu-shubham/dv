import { useEffect, useRef, useState } from "react";
import { api } from "./api.js";
import { cx, modKey, useDismiss } from "./util.js";
import {
  IconBell, IconBranch, IconCheck, IconChevronDown, IconComment, IconKeyboard, IconMenu, IconMoon, IconPin,
  IconSearch, IconSplit, IconSun, IconUndo, IconUnified, IconWrap,
} from "./icons.jsx";

// AUTO is the scope dv starts on: whichever comparison has something in it,
// followed as the work moves. Any other scope is a pin.
export const AUTO = { kind: "auto", rev: "" };

// The bar names the repository in its middle, and holds how the page is drawn
// and the tools at its right. Which mode the page is in is the sidebar's, at
// the top of the list that mode fills. On a phone, what is marked wide-only
// gives way (the diff is unified and wrapped there), and the sidebar is behind
// the menu button.
export default function Header({
  meta, mode, scope, resolvedScope, onScope, view, onView, wrap, onWrap, contextLines, onContext,
  theme, onTheme, onSearch, onHelp, waiting, arrived, onBell, bellOn,
  comments, commentsOn, onComments, sideOn, onSide,
}) {
  const agent = mode === "agent";
  return (
    <header className="topbar">
      <div className="topbar-left">
        <button className={cx("icon", "menu-toggle", sideOn && "on")} aria-pressed={sideOn} onClick={onSide} title="Files and sessions">
          <IconMenu size={15} />
        </button>
        <span className="logo">dv</span>
      </div>

      <div className="topbar-title">
        <span className="repo" title={meta?.root}>
          {meta?.repo}
        </span>
        {(meta?.head?.branch || meta?.head?.sha) && (
          <span className="headref" title={meta.head.subject}>
            <IconBranch size={13} />
            <span>{meta.head.branch || meta.head.sha}</span>
          </span>
        )}
      </div>

      <div className="topbar-right">
      {!agent && <ScopePicker scope={scope} resolved={resolvedScope} meta={meta} mode={mode} onScope={onScope} />}

      {mode !== "code" && (
        <>
          <div className="seg wide-only" role="group" aria-label="Diff layout">
            <button className={cx(view === "split" && "on")} aria-pressed={view === "split"} onClick={() => onView("split")} title="Split view (u toggles)">
              <IconSplit size={14} />
            </button>
            <button className={cx(view === "unified" && "on")} aria-pressed={view === "unified"} onClick={() => onView("unified")} title="Unified view (u toggles)">
              <IconUnified size={14} />
            </button>
          </div>

          <ContextPicker value={contextLines} onPick={onContext} />
        </>
      )}

      <button className={cx("icon", wrap && "on")} aria-pressed={wrap} onClick={() => onWrap(!wrap)} title="Wrap long lines (w)">
        <IconWrap size={14} />
      </button>

      <span className="divider wide-only" />

      {waiting > 0 && (
        <button
          className={cx("icon", "bell", bellOn && "on")}
          aria-pressed={bellOn}
          onClick={onBell}
          title={`Claude is waiting on you: ${waiting === 1 ? "one request" : `${waiting} requests`}`}
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
      <button className="icon" onClick={onSearch} title={`Search definitions and text (${modKey}+K)`}>
        <IconSearch size={14} />
      </button>
      <button className="icon wide-only" onClick={() => onTheme(theme === "dark" ? "light" : "dark")} title="Toggle theme">
        {theme === "dark" ? <IconSun size={14} /> : <IconMoon size={14} />}
      </button>
      <button className="icon wide-only" onClick={onHelp} title="Keyboard shortcuts (?)">
        <IconKeyboard size={14} />
      </button>
      </div>
    </header>
  );
}

// ModeSwitch is Diff, Code and Agent, at the top of the sidebar whose list
// each of them fills. Agent counts what waits to go with the next message.
export function ModeSwitch({ mode, onMode, attached }) {
  const agent = mode === "agent";
  return (
    <div className="seg mode-switch" role="group" aria-label="Mode">
      <button className={cx(mode === "diff" && "on")} aria-pressed={mode === "diff"} onClick={() => onMode("diff")} title="The changes (Shift+←/→ steps through the modes)">
        Diff
      </button>
      <button className={cx(mode === "code" && "on")} aria-pressed={mode === "code"} onClick={() => onMode("code")} title="The whole repository (Shift+←/→ steps through the modes)">
        Code
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
    </div>
  );
}

const CONTEXT_LINES = [0, 3, 8, 20];
const linesLabel = (n) => (n ? `${n} lines` : "No context");

// ContextPicker is how many unchanged lines show around each change.
function ContextPicker({ value, onPick }) {
  const [open, setOpen] = useState(false);
  const ref = useDismiss(open, () => setOpen(false));
  useEffect(() => {
    if (!open) return;
    const onKey = (e) => {
      if (e.key !== "Escape") return;
      e.stopPropagation();
      setOpen(false);
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [open]);
  return (
    <div className="scope ctx-picker wide-only" ref={ref}>
      <button className="scope-button" onClick={() => setOpen((o) => !o)} aria-expanded={open} title="Lines of context around each change">
        <span>{linesLabel(value)}</span>
        <IconChevronDown size={12} />
      </button>
      {open && (
        <div className="scope-menu ctx-menu">
          <div className="scope-sep">Context around each change</div>
          {CONTEXT_LINES.map((n) => (
            <ScopeItem
              key={n}
              on={n === value}
              label={linesLabel(n)}
              onClick={() => {
                onPick(n);
                setOpen(false);
              }}
            />
          ))}
        </div>
      )}
    </div>
  );
}

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
