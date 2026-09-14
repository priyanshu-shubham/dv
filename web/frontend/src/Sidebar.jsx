import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { cx, LRM, statusLabel, statusLetter } from "./util.js";
import { ancestorsOf, buildTree, dirPaths, visibleRows } from "./tree.js";
import { ThreadList } from "./Threads.jsx";
import {
  IconCheck, IconChevron, IconCollapse, IconComment, IconExpand, IconFile, IconFilter,
} from "./icons.jsx";

const MIN_WIDTH = 180;
const clampWidth = (w) => Math.round(Math.max(MIN_WIDTH, Math.min(w, window.innerWidth / 2)));

// The left rail: the file tree, and every comment in the review. In the diff
// the tree is the changed files; in Code mode it is the whole repository.
// Either way a changed file is decorated the way VS Code's explorer does it.
export default function Sidebar({
  mode, files, threads, activePath, viewed, onSelect, onThreadAction, onJump, commentsPath,
  generatedCount, hideGenerated, onHideGenerated, pathFilter, onPathFilter, filteredOut, hiddenGenerated,
  filtersPaused, onPauseFilters, onReset,
  onWidth,
}) {
  const code = mode === "code";
  const [tab, setTab] = useState("files");
  const [filter, setFilter] = useState("");
  const [pathFilterOpen, setPathFilterOpen] = useState(false);
  const pathFilterOn = !!(pathFilter.include.trim() || pathFilter.exclude.trim());
  const hidden = filteredOut + hiddenGenerated;
  // The tree starts folded and remembers what was opened. A filter starts with
  // every match showing and keeps its own throwaway folds, so clearing it
  // brings the tree back as it was.
  const [opened, setOpened] = useState(() => new Set());
  const [filterFolded, setFilterFolded] = useState(() => new Set());

  const openByFile = useMemo(() => {
    const m = new Map();
    for (const t of threads) if (!t.resolved) m.set(t.file, (m.get(t.file) || 0) + 1);
    return m;
  }, [threads]);

  const q = filter.trim().toLowerCase();
  const shown = useMemo(
    () => (q ? files.filter((f) => f.path.toLowerCase().includes(q)) : files),
    [files, q],
  );
  const tree = useMemo(() => buildTree(shown), [shown]);
  const isOpen = useCallback(
    (path) => (q ? !filterFolded.has(path) : opened.has(path)),
    [q, opened, filterFolded],
  );
  const rows = useMemo(() => visibleRows(tree, isOpen), [tree, isOpen]);
  const allDirs = useMemo(() => dirPaths(tree), [tree]);
  const allFolded = allDirs.length > 0 && !allDirs.some(isOpen);

  // Keep the file being read in sight, as VS Code's explorer does: open the
  // folders it sits in, then scroll the tree to it. Only a change of file (or
  // coming back to this tab) does this, so folding or browsing the tree by hand
  // is never undone from under you.
  const listRef = useRef(null);
  const revealing = useRef(null);
  useEffect(() => {
    if (!activePath) return;
    const shut = ancestorsOf(tree, activePath).filter((p) => !isOpen(p));
    if (shut.length && q) setFilterFolded((s) => new Set([...s].filter((p) => !shut.includes(p))));
    else if (shut.length) setOpened((s) => new Set([...s, ...shut]));
    revealing.current = activePath;
  }, [activePath, tab, mode]);

  // After every render: scroll once the row a reveal is waiting for exists. It
  // falls back to a folded folder only when that folder is not about to open.
  useEffect(() => {
    const path = revealing.current;
    const list = listRef.current;
    if (!path || !list) return;
    const el = list.querySelector(".file-row.on") || list.querySelector(".file-row.holds-active");
    if (!el?.classList.contains("on") && ancestorsOf(tree, path).some((p) => !isOpen(p))) return;
    revealing.current = null;
    if (!el) return;
    const box = list.getBoundingClientRect();
    const r = el.getBoundingClientRect();
    // A row's worth of margin, so it does not sit flush against the edge.
    const off = r.top < box.top ? r.top - box.top - r.height : r.bottom > box.bottom ? r.bottom - box.bottom + r.height : 0;
    if (off) list.scrollTo({ top: list.scrollTop + off, behavior: "smooth" });
  });

  const toggleDir = (path) =>
    (q ? setFilterFolded : setOpened)((c) => {
      const next = new Set(c);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });
  const setAllOpen = (open) =>
    q ? setFilterFolded(new Set(open ? [] : allDirs)) : setOpened(new Set(open ? allDirs : []));

  const totals = useMemo(
    () =>
      files.reduce(
        (acc, f) => ({ add: acc.add + (f.additions || 0), del: acc.del + (f.deletions || 0) }),
        { add: 0, del: 0 },
      ),
    [files],
  );
  const openCount = threads.filter((t) => !t.resolved).length;

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
    d.to = clampWidth(d.w + (e.clientX - d.x));
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

  return (
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
      <div className="tabs">
        <button className={cx(tab === "files" && "on")} onClick={() => setTab("files")}>
          <IconFile size={13} /> {code ? "Explorer" : "Files"} <span className="count">{files.length.toLocaleString()}</span>
        </button>
        <button className={cx(tab === "comments" && "on")} onClick={() => setTab("comments")}>
          <IconComment size={13} /> Comments {openCount > 0 && <span className="count">{openCount}</span>}
        </button>
      </div>

      {tab === "files" ? (
        <>
          <div className="sidebar-filter">
            <input
              value={filter}
              placeholder="Filter files"
              onChange={(e) => {
                setFilter(e.target.value);
                setFilterFolded(new Set());
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
                <input type="checkbox" checked={hideGenerated} onChange={(e) => onHideGenerated(e.target.checked)} />
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
            {rows.map(({ node, depth }) => {
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
                      holdsActive && "holds-active",
                      changed && "st-" + changed,
                      !code && node.paths.every((p) => viewed.has(p)) && "seen",
                    )}
                    style={{ "--depth": depth }}
                    onClick={() => toggleDir(node.path)}
                    aria-expanded={expanded}
                    title={node.path + "/"}
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
                  className={cx("file-row", activePath === f.path && "on", letter && "st-" + letter, seen && "seen")}
                  style={{ "--depth": depth }}
                  onClick={() => onSelect(f.path)}
                  title={f.path + (!letter ? "" : code ? ` - ${what} in this diff` : ` - ${what}${f.generated ? " (generated)" : ""}`)}
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
            {shown.length === 0 && <div className="empty">No files match.</div>}
          </div>
          <div className="sidebar-foot">
            {code ? (
              <span className="dim">
                {statusOf.size} changed of {files.length.toLocaleString()} files
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
      ) : (
        <>
          <div className="comment-list">
            {threads.length === 0 ? (
              <div className="empty">
                No comments yet. Drag across line numbers in the diff, or hover a line and hit +.
              </div>
            ) : (
              <ThreadList
                threads={threads}
                compact
                onAction={(action) => {
                  if (action.type === "jump") onJump(action.thread);
                  else onThreadAction(action);
                }}
              />
            )}
          </div>
          <div className="sidebar-foot">
            <span className="saved-to" title={commentsPath}>
              saved to {commentsPath}
            </span>
            <ResetButton onReset={onReset} />
          </div>
        </>
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
