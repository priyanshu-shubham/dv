import { useMemo, useState } from "react";
import { cx, LRM, splitPath, statusLabel } from "./util.js";
import { ThreadList } from "./Threads.jsx";
import { IconCheck, IconComment, IconFile } from "./icons.jsx";

// The left rail: the changed-file list, and every comment in the review.
export default function Sidebar({
  files, threads, activePath, viewed, onSelect, onThreadAction, onJump, commentsPath,
}) {
  const [tab, setTab] = useState("files");
  const [filter, setFilter] = useState("");

  const threadsByFile = useMemo(() => {
    const m = new Map();
    for (const t of threads) {
      if (!m.has(t.file)) m.set(t.file, []);
      m.get(t.file).push(t);
    }
    return m;
  }, [threads]);

  const shown = useMemo(() => {
    const q = filter.trim().toLowerCase();
    return q ? files.filter((f) => f.path.toLowerCase().includes(q)) : files;
  }, [files, filter]);

  const totals = useMemo(
    () =>
      files.reduce(
        (acc, f) => ({ add: acc.add + f.additions, del: acc.del + f.deletions }),
        { add: 0, del: 0 },
      ),
    [files],
  );
  const openCount = threads.filter((t) => !t.resolved).length;

  return (
    <aside className="sidebar">
      <div className="tabs">
        <button className={cx(tab === "files" && "on")} onClick={() => setTab("files")}>
          <IconFile size={13} /> Files <span className="count">{files.length}</span>
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
              onChange={(e) => setFilter(e.target.value)}
              onKeyDown={(e) => e.stopPropagation()}
            />
          </div>
          <div className="file-list">
            {shown.map((f) => {
              const [dir, name] = splitPath(f.path);
              const fileThreads = threadsByFile.get(f.path) || [];
              const open = fileThreads.filter((t) => !t.resolved).length;
              return (
                <button
                  key={f.path}
                  className={cx("file-row", activePath === f.path && "on", viewed.has(f.path) && "seen")}
                  onClick={() => onSelect(f.path)}
                  title={f.path + " - " + statusLabel[f.status]}
                >
                  <span className={cx("dot", "st-" + f.status)} />
                  <span className="name">
                    {LRM}
                    <span className="dim">{dir}</span>
                    {name}
                  </span>
                  {open > 0 && <span className="pill">{open}</span>}
                  {viewed.has(f.path) && <IconCheck size={12} className="seen-check" />}
                  <span className="counts">
                    <span className="add">+{f.additions}</span>
                    <span className="del">-{f.deletions}</span>
                  </span>
                </button>
              );
            })}
            {shown.length === 0 && <div className="empty">No files match.</div>}
          </div>
          <div className="sidebar-foot">
            <span className="add">+{totals.add}</span>
            <span className="del">-{totals.del}</span>
            <span className="spacer" />
            <span className="dim">
              {viewed.size}/{files.length} viewed
            </span>
          </div>
        </>
      ) : (
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
          <div className="sidebar-foot small" title={commentsPath}>
            saved to {commentsPath}
          </div>
        </div>
      )}
    </aside>
  );
}
