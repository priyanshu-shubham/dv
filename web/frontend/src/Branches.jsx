import { useEffect, useState } from "react";
import { api } from "./api.js";
import { cx } from "./util.js";
import { Modal, usePaletteNav } from "./Overlays.jsx";
import { IconBranch } from "./icons.jsx";

// useBranchChoices asks, each time open turns true or HEAD moves, which
// branches there are and what stops switching to or making one.
export function useBranchChoices(open, head) {
  const [can, setCan] = useState(null);
  useEffect(() => {
    if (!open) return;
    let live = true;
    setCan(null);
    api.canSwitch().then((c) => live && setCan(c), () => {});
    return () => {
      live = false;
    };
  }, [open, head?.branch, head?.sha]);
  return can;
}

// branchRows is what the picker offers for q: the other branches matching it,
// and a row making q when it names none of them.
export function branchRows(can, fallback, current, q, max) {
  const others = (can?.branches || fallback || []).filter((b) => b !== current);
  const t = q.trim();
  const rows = others
    .filter((b) => b.toLowerCase().includes(t.toLowerCase()))
    .slice(0, max)
    .map((b) => ({ branch: b, off: !can || !!can.blocked || !!can.elsewhere?.includes(b) }));
  if (t && t !== current && !others.includes(t)) rows.push({ branch: t, create: true, off: !can || !!can.createBlocked });
  return rows;
}

export const switchDone = (row) => (row.create ? `Made ${row.branch} and switched to it` : `Switched to ${row.branch}`);

// blockedNote says what the server refuses now: switching needs nothing to
// lose, making a branch only no merge or rebase under way.
export function blockedNote(can) {
  if (!can?.blocked) return "";
  return can.createBlocked ? `Can't switch or make a branch: ${can.blocked}.` : `Can't switch: ${can.blocked}. A new branch takes the changes with it.`;
}

// BranchPalette is the Switch branch action: the bar's branch menu as a
// palette, with Enter switching to the selected branch or making the typed one.
export function BranchPalette({ meta, onDone, onClose }) {
  const [q, setQ] = useState("");
  const [state, setState] = useState(null); // { busy } | { error }
  const can = useBranchChoices(true, meta?.head);
  const rows = branchRows(can, meta?.branches, meta?.head?.branch, q, 50);
  const pick = (row) => {
    if (row.off || state?.busy) return;
    setState({ busy: true });
    api.switchTo(row.branch, row.create).then(() => onDone(switchDone(row)), (e) => setState({ error: e.message }));
  };
  const { sel, setSel, onKey, listRef } = usePaletteNav(rows.length, (i) => pick(rows[i]));
  const note = blockedNote(can);
  return (
    <Modal onClose={onClose} className="palette">
      <div className="palette-input">
        <IconBranch size={15} />
        <input
          autoFocus
          value={q}
          placeholder={`Switch from ${meta?.head?.branch || meta?.head?.sha || "HEAD"}, or name a new branch...`}
          onChange={(e) => {
            setQ(e.target.value);
            setSel(0);
            setState(null);
          }}
          onKeyDown={onKey}
        />
      </div>
      <div className="palette-list" ref={listRef}>
        {rows.map((r, i) => (
          <button
            key={(r.create ? "+" : "") + r.branch}
            className={cx("palette-row", "action-row", i === sel && "on", r.off && "unready")}
            onMouseMove={() => setSel(i)}
            onClick={() => pick(r)}
          >
            <span className="sym mono">{r.create ? `Create ${r.branch}` : r.branch}</span>
            <span className="dim">
              {r.create ? "A new branch at HEAD" : can?.elsewhere?.includes(r.branch) ? "Checked out in another worktree" : ""}
            </span>
          </button>
        ))}
        {rows.length === 0 && <div className="empty">{q.trim() ? "That branch is checked out." : "No other branches yet. Type a name to make one."}</div>}
      </div>
      {(state || note) && (
        <div className={cx("palette-foot", "dim", state?.error && "del")}>{state?.busy ? "Switching…" : state?.error || note}</div>
      )}
    </Modal>
  );
}
