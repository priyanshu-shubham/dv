// The Files tab: the repository a folder at a time, as Android's file
// managers go - the trail along the top, Back up a level, a file opening over
// it all. Changed files say how; comments, how many.
import { useEffect, useMemo, useState } from "react";
import { buildTree } from "../tree.js";
import { cx } from "../util.js";
import { useBack } from "./back.js";
import { Icon } from "./icons.jsx";
import { Row } from "./Sheet.jsx";

export default function Files({ entries, loading, shown, threads, onOpen, onOpenIgnored }) {
  const tree = useMemo(() => ({ children: buildTree(entries) }), [entries]);
  const [trail, setTrail] = useState([]); // folder paths, outermost first
  // Left for another tab, it starts again at the top: each folder gone into
  // holds a Back entry, which would act on a tab no longer on screen.
  useEffect(() => {
    if (!shown) setTrail([]);
  }, [shown]);
  const at = trail.length ? trail[trail.length - 1] : "";
  const node = useMemo(() => (at ? find(tree, at) : tree), [tree, at]);
  const counts = useMemo(() => {
    const m = new Map();
    for (const t of threads) if (!t.resolved) m.set(t.file, (m.get(t.file) || 0) + 1);
    return m;
  }, [threads]);
  const changed = useMemo(() => new Set(entries.filter((e) => e.status).map((e) => e.path)), [entries]);

  if (!entries.length) return <div className="p-empty">{loading ? "Reading the folder…" : "The folder is empty."}</div>;
  return (
    <div className="p-scroll">
      <div className="p-subbar p-crumbs">
        <button className={cx("p-chip", !at && "on")} onClick={() => setTrail([])}>
          <Icon name="home" size={18} />
        </button>
        {trail.map((p, i) => (
          <span key={p} className="p-crumb">
            <span className="p-crumb-sep">/</span>
            <button className={cx("p-chip", i === trail.length - 1 && "on")} onClick={() => setTrail(trail.slice(0, i + 1))}>
              {p.split("/").slice(trail[i - 1] ? trail[i - 1].split("/").length : 0).join("/")}
            </button>
          </span>
        ))}
      </div>
      {trail.map((p) => (
        <Level key={p} onUp={() => setTrail((t) => t.slice(0, t.indexOf(p)))} />
      ))}
      {!node ? (
        <div className="p-empty">That folder is gone.</div>
      ) : (
        <div className="p-list">
          {node.children.map((c) =>
            c.dir ? (
              <Row
                key={c.path}
                icon="folder"
                className={cx(c.ignored && "dim")}
                title={c.name}
                trail={c.paths.some((p) => changed.has(p)) && <span className="p-mark" />}
                onClick={() => {
                  // An ignored folder is listed once gone into, as the tree's is once opened.
                  if (c.ignored) onOpenIgnored?.([c.path]);
                  setTrail((t) => [...t, c.path]);
                }}
              />
            ) : (
              <Row
                key={c.path}
                icon="file"
                className={cx(c.file?.ignored && "dim")}
                title={c.name}
                trail={
                  <span className="p-row-trail">
                    {counts.get(c.path) > 0 && (
                      <span className="p-count">
                        <Icon name="comment" size={14} />
                        {counts.get(c.path)}
                      </span>
                    )}
                    {c.file?.status && <span className={cx("p-status", "st-" + c.file.status)}>{c.file.status}</span>}
                  </span>
                }
                onClick={() => onOpen(c.path)}
              />
            ),
          )}
        </div>
      )}
    </div>
  );
}

// Level holds Back's entry for one folder gone into.
function Level({ onUp }) {
  useBack(true, onUp);
  return null;
}

function find(node, path) {
  for (const c of node.children) {
    if (!c.dir) continue;
    if (c.path === path) return c;
    if (path.startsWith(c.path + "/")) return find(c, path);
  }
  return null;
}
