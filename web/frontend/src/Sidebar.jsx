import { useCallback, useDeferredValue, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { cx, isTyping, listFilter, LRM, statusLabel, statusLetter } from "./util.js";
import { ancestorsOf, buildTree, dirPaths, ignoredDirs, visibleRows } from "./tree.js";
import { AttachButton, ThreadList } from "./Threads.jsx";
import { Notes, openNotes } from "./Notes.jsx";
import { SessionList } from "./Agent.jsx";
import { BranchRow, ModeSwitch } from "./Header.jsx";
import {
  IconCheck, IconChevron, IconCollapse, IconComment, IconExpand, IconFilter, IconX,
} from "./icons.jsx";

const MIN_WIDTH = 180;
// Rows drawn beyond each edge of the list's view, so a scroll does not show a gap.
const ROW_MARGIN = 20;
const clampWidth = (w) => Math.round(Math.max(MIN_WIDTH, Math.min(w, window.innerWidth / 2)));

// The left rail: the mode, over what it lists. In the diff that is the changed
// files; in Code mode it is the whole repository, with changed files marked
// as they are in the diff. In Agent mode the tree's place is taken by the
// sessions.
export default function Sidebar({
  meta, pr, right, mode, files, onOpenIgnored, threads, activePath, viewed, onSelect,
  generatedCount, hideGenerated, onHideGenerated, pathFilter, onPathFilter, filteredOut, hiddenGenerated,
  filtersPaused, onPauseFilters, onReset, agent, onWidth, modes, onMode, attached,
}) {
  const code = mode === "code";
  const [filter, setFilter] = useState("");
  const [pathFilterOpen, setPathFilterOpen] = useState(false);
  const pathFilterOn = !!(pathFilter.include.trim() || pathFilter.exclude.trim());
  const hidden = filteredOut + hiddenGenerated;
  // Each tree keeps the folders flipped from where it starts. The diff's starts
  // open, since it holds only what changed; Code mode's is the whole
  // repository, so it starts folded. A filter starts with every match showing
  // and keeps its own throwaway folds, so clearing it brings the tree back. An
  // ignored folder starts folded in any of them: it is listed once opened.
  const [flips, setFlips] = useState(() => ({ diff: new Set(), code: new Set(), filter: new Set() }));

  const openByFile = useMemo(() => {
    const m = new Map();
    for (const t of threads) if (!t.resolved) m.set(t.file, (m.get(t.file) || 0) + 1);
    return m;
  }, [threads]);

  // Deferred, so what is typed shows at once and the list catches up after:
  // filtering and building the tree for a large folder takes a moment.
  const q = useDeferredValue(filter.trim());
  const shown = useMemo(() => {
    const keep = listFilter(q);
    return keep ? files.filter((f) => keep(f.path)) : files;
  }, [files, q]);
  const tree = useMemo(() => buildTree(shown), [shown]);
  const treeKind = q ? "filter" : code ? "code" : "diff";
  const ignored = useMemo(() => ignoredDirs(tree), [tree]);
  const startsOpen = useCallback((path) => treeKind !== "code" && !ignored.has(path), [treeKind, ignored]);
  const flipped = flips[treeKind];
  const isOpen = useCallback((path) => startsOpen(path) !== flipped.has(path), [startsOpen, flipped]);
  const setOpen = (paths, open) =>
    setFlips((f) => {
      const next = new Set(f[treeKind]);
      for (const p of paths) open === startsOpen(p) ? next.delete(p) : next.add(p);
      return { ...f, [treeKind]: next };
    });
  const rows = useMemo(() => visibleRows(tree, isOpen), [tree, isOpen]);
  const allDirs = useMemo(() => dirPaths(tree), [tree]);
  const allFolded = allDirs.length > 0 && !allDirs.some(isOpen);

  useEffect(() => {
    for (const { node } of rows) if (node.ignored && isOpen(node.path)) onOpenIgnored?.([node.path]);
  }, [rows]);

  // Keep the file being read in sight, as VS Code's explorer does: open the
  // folders it sits in, then scroll the tree to it. Only a change of file (or
  // of mode) does this, so folding or browsing the tree by hand is never undone
  // from under you. Code mode's tree arrives after its file does, and a file in
  // an ignored folder after that folder is listed, so the file turning up
  // counts as a change too.
  const listRef = useRef(null);
  const revealing = useRef(null);
  const activeIn = useMemo(() => !!activePath && files.some((f) => f.path === activePath), [files, activePath]);
  useEffect(() => {
    if (!activePath) return;
    const shut = ancestorsOf(tree, activePath).filter((p) => !isOpen(p));
    if (shut.length) setOpen(shut, true);
    revealing.current = activePath;
  }, [activePath, mode, activeIn]);

  // The list draws only the rows in view, plus a margin: a filter opens every
  // folder, and a button apiece for a folder of 40,000 files took seconds to
  // lay out. Rows are all one height, which the stylesheet sets.
  const [win, setWin] = useState({ top: 0, height: 0, row: 24 });
  const listMounted = mode !== "agent";
  useLayoutEffect(() => {
    const list = listRef.current;
    if (!list) return;
    const measure = () =>
      setWin({
        top: list.scrollTop,
        height: list.clientHeight,
        row: parseFloat(getComputedStyle(list).getPropertyValue("--row-h")) || 24,
      });
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(list);
    list.addEventListener("scroll", measure, { passive: true });
    return () => {
      ro.disconnect();
      list.removeEventListener("scroll", measure);
    };
  }, [listMounted]);
  const first = Math.max(0, Math.floor(win.top / win.row) - ROW_MARGIN);
  const last = Math.min(rows.length, Math.ceil((win.top + win.height) / win.row) + ROW_MARGIN);

  // After every render: scroll once the row a reveal is waiting for is in the
  // list. It falls back to a folded folder only when that folder is not about
  // to open.
  useEffect(() => {
    const path = revealing.current;
    const list = listRef.current;
    if (!path || !list) return;
    const i = rows.findIndex(({ node }) => node.path === path || (node.dir && !isOpen(node.path) && path.startsWith(node.path + "/")));
    if (rows[i]?.node.path !== path && ancestorsOf(tree, path).some((p) => !isOpen(p))) return;
    revealing.current = null;
    if (i < 0) return;
    const top = i * win.row - list.scrollTop;
    // A row's worth of margin, so it does not sit flush against the edge.
    const off = top < 0 ? top - win.row : top + win.row > list.clientHeight ? top + 2 * win.row - list.clientHeight : 0;
    if (off) list.scrollTo({ top: list.scrollTop + off, behavior: "smooth" });
  });

  const toggleDir = (path) => setOpen([path], !isOpen(path));
  // Expanding all leaves ignored folders folded, or node_modules would be listed.
  const setAllOpen = (open) =>
    setFlips((f) => ({ ...f, [treeKind]: new Set(allDirs.filter((p) => (open && !ignored.has(p)) !== startsOpen(p))) }));

  const totals = useMemo(
    () =>
      files.reduce(
        (acc, f) => ({ add: acc.add + (f.additions || 0), del: acc.del + (f.deletions || 0) }),
        { add: 0, del: 0 },
      ),
    [files],
  );

  // As the ask panel does it: the drag writes to the DOM and commits on release.
  const rootRef = useRef(null);
  const drag = useRef(null);
  const onResizeDown = (e) => {
    e.preventDefault();
    drag.current = { x: e.clientX, w: rootRef.current.offsetWidth, to: 0 };
    e.currentTarget.setPointerCapture(e.pointerId);
    document.body.classList.add("resizing", "resizing-side");
  };
  const onResizeMove = (e) => {
    const d = drag.current;
    if (!d) return;
    d.to = clampWidth(d.w + (right ? d.x - e.clientX : e.clientX - d.x));
    rootRef.current.parentElement.style.setProperty("--side-w", d.to + "px");
  };
  const onResizeUp = () => {
    const d = drag.current;
    if (!d) return;
    drag.current = null;
    document.body.classList.remove("resizing", "resizing-side");
    if (d.to) onWidth(d.to);
  };
  // Code mode's decorations: what changed, by path.
  const statusOf = useMemo(() => new Map(files.filter((f) => f.status).map((f) => [f.path, f])), [files]);
  const ignoredCount = useMemo(() => files.reduce((n, f) => n + (f.ignored ? 1 : 0), 0), [files]);

  return (
    // Slid off a phone's screen it is out of reach already. It is not made inert:
    // with the accessibility tree on, as a phone's autofill turns it on, that
    // rebuilds the tree for every row on each open and close.
    <aside className="sidebar" ref={rootRef}>
      <div
        className="side-resize"
        title="Drag to resize, double-click to reset"
        onPointerDown={onResizeDown}
        onPointerMove={onResizeMove}
        onPointerUp={onResizeUp}
        onPointerCancel={onResizeUp}
        onDoubleClick={() => {
          rootRef.current.parentElement.style.removeProperty("--side-w");
          onWidth(0);
        }}
      />
      <BranchRow meta={meta} pr={pr} />
      <div className="sidebar-modes">
        <ModeSwitch mode={mode} modes={modes} onMode={onMode} attached={attached} />
      </div>
      {mode === "agent" ? (
        <SessionList {...agent} />
      ) : (
        <>
          <div className="sidebar-filter">
            <input
              value={filter}
              placeholder="Filter files or globs"
              title="Part of a path, or a glob: *.go, src/**/*.ts. Separate several with commas; start one with ! to hide what it matches."
              onChange={(e) => {
                setFilter(e.target.value);
                setFlips((f) => ({ ...f, filter: new Set() }));
              }}
              onKeyDown={(e) => e.stopPropagation()}
            />
            {!code && (
              <button
                className={cx("ghost", (pathFilterOn || hideGenerated) && !filtersPaused && "on")}
                aria-expanded={pathFilterOpen}
                title={
                  filtersPaused
                    ? "Filters paused - showing every file"
                    : "Hide generated files, or pick the files to include or exclude"
                }
                onClick={() => setPathFilterOpen((o) => !o)}
              >
                <IconFilter size={13} />
              </button>
            )}
            {allDirs.length > 0 && (
              <button
                className="ghost"
                title={allFolded ? "Expand all folders" : "Collapse all folders"}
                onClick={() => setAllOpen(allFolded)}
              >
                {allFolded ? <IconExpand size={13} /> : <IconCollapse size={13} />}
              </button>
            )}
          </div>
          {!code && pathFilterOpen && (
            <div className="path-filter">
              <label className="check">
                <input type="checkbox" className="tick" checked={hideGenerated} onChange={(e) => onHideGenerated(e.target.checked)} />
                Hide generated files
                <span className="count">{generatedCount}</span>
              </label>
              {[
                ["include", "files to include", "src/, *.go"],
                ["exclude", "files to exclude", "*_test.go, docs/"],
              ].map(([key, label, hint]) => (
                <label key={key}>
                  {label}
                  <input
                    value={pathFilter[key]}
                    placeholder={hint}
                    onChange={(e) => {
                      const v = e.target.value;
                      onPathFilter((f) => ({ ...f, [key]: v }));
                    }}
                    onKeyDown={(e) => e.stopPropagation()}
                  />
                </label>
              ))}
            </div>
          )}
          {!code && hidden > 0 && (
            <div className={cx("filter-note", filtersPaused && "paused")}>
              <span>
                {filtersPaused ? `Filters paused: ${hidden} would be hidden` : `${hidden} hidden`} (
                {[hiddenGenerated && `${hiddenGenerated} generated`, filteredOut && `${filteredOut} excluded`]
                  .filter(Boolean)
                  .join(", ")}
                )
              </span>
              <button className="link" onClick={() => onPauseFilters(!filtersPaused)}>
                {filtersPaused ? "Resume" : "Show all"}
              </button>
            </div>
          )}
          <div className="file-list" ref={listRef}>
            <div style={{ height: rows.length * win.row, paddingTop: first * win.row, boxSizing: "border-box" }}>
            {rows.slice(first, last).map(({ node, depth }) => {
              if (node.dir) {
                const expanded = isOpen(node.path);
                // A folded folder stands in for everything inside it: its
                // unresolved comments, and the file being read.
                const open = expanded ? 0 : node.paths.reduce((n, p) => n + (openByFile.get(p) || 0), 0);
                const holdsActive = !expanded && activePath?.startsWith(node.path + "/");
                const changed = code && folderStatus(node.paths, statusOf);
                return (
                  <button
                    key={"d:" + node.path}
                    className={cx(
                      "file-row", "dir-row",
                      node.ignored && "ignored",
                      holdsActive && "holds-active",
                      changed && "st-" + changed,
                      !code && node.paths.every((p) => viewed.has(p)) && "seen",
                    )}
                    style={{ "--depth": depth }}
                    onClick={() => toggleDir(node.path)}
                    aria-expanded={expanded}
                    title={node.path + "/" + (node.ignored ? " - ignored" : "")}
                  >
                    <IconChevron size={12} className={cx("twisty", expanded && "open")} />
                    <span className="name">
                      {LRM}
                      {node.name}
                    </span>
                    {open > 0 && <CommentCount n={open} />}
                    {changed && <span className="change-dot" title="Holds changed files" />}
                  </button>
                );
              }
              const f = node.file;
              const open = openByFile.get(f.path) || 0;
              const letter = f.status && statusLetter(f);
              const seen = !code && viewed.has(f.path);
              const what = f.untracked ? "untracked" : statusLabel[f.status];
              return (
                <button
                  key={f.path}
                  className={cx("file-row", activePath === f.path && "on", letter && "st-" + letter, seen && "seen", f.ignored && "ignored")}
                  style={{ "--depth": depth }}
                  onClick={() => onSelect(f.path)}
                  title={
                    f.path +
                    (f.ignored ? " - ignored" : !letter ? "" : code ? ` - ${what} in this diff` : ` - ${what}${f.generated ? " (generated)" : ""}`)
                  }
                >
                  <span className="slot" />
                  <span className="name">
                    {LRM}
                    {node.name}
                  </span>
                  {!code && f.generated && <span className="gen-tag" title="Generated - diff not shown">gen</span>}
                  {open > 0 && <CommentCount n={open} />}
                  {seen && <IconCheck size={12} className="seen-check" />}
                  {!code && (
                    <span className="counts">
                      <span className="add">+{f.additions}</span>
                      <span className="del">-{f.deletions}</span>
                    </span>
                  )}
                  {letter && <span className="st-letter">{letter}</span>}
                </button>
              );
            })}
            </div>
            {shown.length === 0 && <div className="empty">No files match.</div>}
          </div>
          <div className="sidebar-foot">
            {code ? (
              <span className="dim">
                {modes.includes("diff") && `${statusOf.size} changed of `}
                {(files.length - ignoredCount).toLocaleString()} files
              </span>
            ) : (
              <>
                <span className="add">+{totals.add}</span>
                <span className="del">-{totals.del}</span>
                <span className="spacer" />
                <span className="dim">
                  {files.filter((f) => viewed.has(f.path)).length}/{files.length} viewed
                </span>
                <ResetButton onReset={onReset} />
              </>
            )}
          </div>
        </>
      )}
    </aside>
  );
}

// CommentsPanel is every comment in the review, at the page's right in any
// mode, opened and closed from the header, which counts them; its switch
// turns it to the repository's notes. Its width is dragged from its left edge.
// ask is the Ask tab's view, where the page has one.
export function CommentsPanel({ tab, onTab, notes, threads, commentsPath, onJump, onThreadAction, onAttach, onSend, onDelete, widthVar, onWidth, onClose, ask }) {
  if (tab === "ask" && !ask) tab = "comments";
  const open = threads.filter((t) => !t.resolved).length;
  const toDo = openNotes(notes.notes);
  const savedTo = tab === "notes" ? notes.path : commentsPath;
  const rootRef = useRef(null);
  // Ticked comments go to a session, or go, together. Ones since deleted drop out.
  const [ticked, setTicked] = useState(() => new Set());
  const picked = threads.filter((t) => ticked.has(t.id));
  const tick = useCallback((id, on) => {
    setTicked((was) => {
      const next = new Set(was);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });
  }, []);
  const all = threads.length > 0 && picked.length === threads.length;
  const drag = useRef(null);
  const onResizeDown = (e) => {
    e.preventDefault();
    drag.current = { x: e.clientX, w: rootRef.current.offsetWidth, to: 0 };
    e.currentTarget.setPointerCapture(e.pointerId);
    document.body.classList.add("resizing", "resizing-comments");
  };
  const onResizeMove = (e) => {
    const d = drag.current;
    if (!d) return;
    d.to = Math.round(Math.max(MIN_WIDTH + 60, Math.min(d.w - (e.clientX - d.x), window.innerWidth / 2)));
    rootRef.current.parentElement.style.setProperty(widthVar, d.to + "px");
  };
  const onResizeUp = () => {
    const d = drag.current;
    if (!d) return;
    drag.current = null;
    document.body.classList.remove("resizing", "resizing-comments");
    if (d.to) onWidth(d.to);
  };
  return (
    // Esc from within closes it, but not from a comment or note being written,
    // where it cancels that.
    <aside className="comments-panel" ref={rootRef} onKeyDown={(e) => e.key === "Escape" && !isTyping(e.target) && onClose()}>
      <div
        className="side-resize comments-resize"
        title="Drag to resize, double-click to reset"
        onPointerDown={onResizeDown}
        onPointerMove={onResizeMove}
        onPointerUp={onResizeUp}
        onPointerCancel={onResizeUp}
        onDoubleClick={() => {
          rootRef.current.parentElement.style.removeProperty(widthVar);
          onWidth(0);
        }}
      />
      <div className="sidebar-modes">
        <div className="seg" role="group" aria-label="Show">
          <button className={cx(tab === "comments" && "on")} aria-pressed={tab === "comments"} onClick={() => onTab("comments")} title="The comments in this review">
            Comments
            {open > 0 && <span className="seg-count">{open}</span>}
          </button>
          <button className={cx(tab === "notes" && "on")} aria-pressed={tab === "notes"} onClick={() => onTab("notes")} title="The repository's notes, which all its worktrees share">
            Notes
            {toDo > 0 && <span className="seg-count">{toDo}</span>}
          </button>
          {ask && (
            <button className={cx(tab === "ask" && "on")} aria-pressed={tab === "ask"} onClick={() => onTab("ask")} title="Questions about the code you are reading, answered here">
              Ask
            </button>
          )}
        </div>
        <button className="ghost" onClick={onClose} title="Close (Esc)">
          <IconX size={13} />
        </button>
      </div>
      {tab === "ask" && ask}
      {tab === "notes" && <Notes {...notes} />}
      {tab === "comments" && threads.length > 0 && onSend && (
        <div className="comment-picks">
          <label className="pick-all" title={all ? "Take the ticks off" : "Tick them all"}>
            <input type="checkbox" className="tick" checked={all} onChange={() => setTicked(all ? new Set() : new Set(threads.map((t) => t.id)))} />
            {picked.length > 0 ? `${picked.length} of ${threads.length}` : "All"}
          </label>
          {picked.length > 0 && (
            <>
              <AttachButton
                what={picked.length === 1 ? "this comment" : `these ${picked.length} comments`}
                onClick={(to) => {
                  onSend(picked, to);
                  setTicked(new Set());
                }}
              />
              <button className="mini danger" onClick={() => onDelete(picked).then(() => setTicked(new Set()))}>
                Delete
              </button>
            </>
          )}
        </div>
      )}
      {tab === "comments" && (
        <div className="comment-list">
          {threads.length === 0 ? (
            <div className="empty">No comments yet. Drag across line numbers, or hover a line and hit +.</div>
          ) : (
            <ThreadList
              threads={threads}
              compact
              onAttach={onAttach}
              ticked={ticked}
              onTick={onSend ? tick : undefined}
              onAction={(action) => {
                if (action.type === "jump") onJump(action.thread);
                else onThreadAction(action);
              }}
            />
          )}
        </div>
      )}
      {tab !== "ask" && (
        <div className="sidebar-foot">
          <span className="saved-to" title={savedTo}>
            saved to {savedTo}
          </span>
        </div>
      )}
    </aside>
  );
}

const CommentCount = ({ n }) => (
  <span className="pill" title={`${n} open comment${n === 1 ? "" : "s"}`}>
    <IconComment size={11} />
    {n}
  </span>
);

// folderStatus is the decoration a folder takes from the changes inside it:
// modified wins over added, which wins over deleted, as in VS Code.
function folderStatus(paths, statusOf) {
  let best = "";
  for (const p of paths) {
    const f = statusOf.get(p);
    if (!f) continue;
    const s = f.status === "A" || f.status === "D" ? statusLetter(f) : "M";
    if (s === "M") return "M";
    if (best !== "A" && best !== "U") best = s;
  }
  return best;
}

function ResetButton({ onReset }) {
  return (
    <button className="reset" onClick={onReset} title="Delete every comment and viewed mark, to start the review over">
      Reset
    </button>
  );
}
