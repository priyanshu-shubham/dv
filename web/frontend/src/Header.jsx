import { useEffect, useRef, useState } from "react";
import { cx, modKey } from "./util.js";
import {
  IconBranch, IconChevronDown, IconKeyboard, IconMoon, IconRefresh,
  IconSearch, IconSpark, IconSplit, IconSun, IconSymbol, IconUnified,
} from "./icons.jsx";

export default function Header({
  meta, scope, resolvedScope, onScope, view, onView, wrap, onWrap, contextLines, onContext,
  theme, onTheme, onRefresh, refreshing, onPalette, onSearch, onHelp, onAsk, askOn,
}) {
  return (
    <header className="topbar">
      <div className="brand">
        <span className="logo">dv</span>
        <span className="repo" title={meta?.root}>
          {meta?.repo}
        </span>
      </div>

      {(meta?.head?.branch || meta?.head?.sha) && (
        <div className="headref" title={meta.head.subject}>
          <IconBranch size={13} />
          <span>{meta.head.branch || meta.head.sha}</span>
          <span className="dim subject">{meta.head.subject}</span>
        </div>
      )}

      <ScopePicker scope={scope} resolved={resolvedScope} meta={meta} onScope={onScope} />

      <span className="spacer" />

      <div className="seg" role="group" aria-label="Diff layout">
        <button className={cx(view === "split" && "on")} onClick={() => onView("split")} title="Split view (u toggles)">
          <IconSplit size={14} />
        </button>
        <button className={cx(view === "unified" && "on")} onClick={() => onView("unified")} title="Unified view (u toggles)">
          <IconUnified size={14} />
        </button>
      </div>

      <label className="ctx" title="Lines of context around each change">
        ctx
        <select value={contextLines} onChange={(e) => onContext(Number(e.target.value))}>
          {[0, 3, 8, 20].map((n) => (
            <option key={n} value={n}>
              {n}
            </option>
          ))}
        </select>
      </label>

      <button className={cx("icon", wrap && "on")} onClick={() => onWrap(!wrap)} title="Wrap long lines">
        wrap
      </button>

      <button className={cx("icon", askOn && "on")} onClick={onAsk} title="Ask Claude about this change (a)">
        <IconSpark size={14} />
      </button>
      <button className="icon" onClick={onPalette} title={`Go to symbol (${modKey}+K)`}>
        <IconSymbol size={14} />
      </button>
      <button className="icon" onClick={onSearch} title={`Search the repo (${modKey}+Shift+F)`}>
        <IconSearch size={14} />
      </button>
      <button className={cx("icon", refreshing && "spin")} onClick={onRefresh} title="Reload the diff (r)">
        <IconRefresh size={14} />
      </button>
      <button className="icon" onClick={() => onTheme(theme === "dark" ? "light" : "dark")} title="Toggle theme">
        {theme === "dark" ? <IconSun size={14} /> : <IconMoon size={14} />}
      </button>
      <button className="icon" onClick={onHelp} title="Keyboard shortcuts (?)">
        <IconKeyboard size={14} />
      </button>
    </header>
  );
}

const PRESETS = [
  { kind: "auto", label: "Auto", hint: "whichever of the below has something in it" },
  { kind: "working", label: "Uncommitted", hint: "working tree vs HEAD" },
  { kind: "staged", label: "Staged", hint: "index vs HEAD" },
  { kind: "head", label: "Last commit", hint: "HEAD vs its parent" },
  { kind: "branch", label: "This branch", hint: "since the merge base with the default branch" },
];

function ScopePicker({ scope, resolved, meta, onScope }) {
  const [open, setOpen] = useState(false);
  const [custom, setCustom] = useState(scope.rev || "");
  const ref = useRef(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e) => {
      if (ref.current && !ref.current.contains(e.target)) setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);

  const label = resolved?.label || PRESETS.find((p) => p.kind === scope.kind)?.label || scope.kind;

  return (
    <div className="scope" ref={ref}>
      <button className="scope-button" onClick={() => setOpen((o) => !o)}>
        <span>{label}</span>
        {resolved?.picked && <span className="auto-tag">auto</span>}
        <IconChevronDown size={12} />
      </button>
      {open && (
        <div className="scope-menu">
          {PRESETS.map((p) => (
            <button
              key={p.kind}
              className={cx("scope-item", scope.kind === p.kind && "on")}
              onClick={() => {
                onScope({ kind: p.kind, rev: "" });
                setOpen(false);
              }}
            >
              <span className="scope-label">{p.label}</span>
              <span className="dim">{p.hint}</span>
            </button>
          ))}

          <div className="scope-sep">Compare a range</div>
          <form
            className="scope-custom"
            onSubmit={(e) => {
              e.preventDefault();
              if (custom.trim()) {
                onScope({ kind: "custom", rev: custom.trim() });
                setOpen(false);
              }
            }}
          >
            <input
              value={custom}
              placeholder="main...HEAD, HEAD~3.., abc123"
              onChange={(e) => setCustom(e.target.value)}
              onKeyDown={(e) => e.stopPropagation()}
            />
            <button className="primary" type="submit">
              Go
            </button>
          </form>

          {meta?.branches?.length > 0 && (
            <div className="scope-chips">
              {meta.branches.slice(0, 6).map((b) => (
                <button
                  key={b}
                  onClick={() => {
                    onScope({ kind: "custom", rev: `${b}...` });
                    setOpen(false);
                  }}
                  title={`Everything since the merge base with ${b}`}
                >
                  {b}
                </button>
              ))}
            </div>
          )}
          {resolved?.desc && <div className="scope-desc">{resolved.desc}</div>}
        </div>
      )}
    </div>
  );
}
