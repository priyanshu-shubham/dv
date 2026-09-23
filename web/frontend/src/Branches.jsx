import { useEffect, useState } from "react";
import { api } from "./api.js";
import { cx } from "./util.js";
import { Modal, usePaletteNav } from "./Overlays.jsx";
import { IconBranch } from "./icons.jsx";

// useBranchChoices asks, each time open turns true or HEAD moves, which
// branches there are and what stops switching to or making one; then again
// once the remotes are fetched, fetching true meanwhile.
export function useBranchChoices(open, head) {
  const [can, setCan] = useState(null);
  useEffect(() => {
    if (!open) return;
    let live = true;
    setCan(null);
    api.canSwitch().then(
      (c) => {
        if (!live) return;
        setCan({ ...c, fetching: true });
        api.canSwitch(true).then(
          (f) => live && setCan(f),
          () => live && setCan(c),
        );
      },
      () => {},
    );
    return () => {
      live = false;
    };
  }, [open, head?.branch, head?.sha]);
  return can;
}

// branchRows is what the picker offers for q: the other branches matching it,
// then the remotes' ones no local branch is named for, and a row making q
// when it names none of them - not even a remote's, which it would only
// shadow with a branch of nothing in common.
export function branchRows(can, fallback, current, q, max) {
  const others = (can?.branches || fallback || []).filter((b) => b !== current);
  const t = q.trim();
  const has = (s) => s.toLowerCase().includes(t.toLowerCase());
  const rows = others
    .filter(has)
    .slice(0, max)
    .map((b) => ({ branch: b, off: !can || !!can.blocked || !!can.elsewhere?.includes(b) }));
  const remote = can?.remote || [];
  for (const r of remote.filter((r) => has(r.ref)).slice(0, max - rows.length)) {
    rows.push({ branch: r.branch, track: r.ref, off: !!can.blocked });
  }
  const named = t === current || others.includes(t) || remote.some((r) => r.branch === t || r.ref === t);
  if (t && !named) rows.push({ branch: t, create: true, off: !can || !!can.createBlocked });
  return rows;
}

export const rowLabel = (row) => (row.create ? `Create ${row.branch}` : row.track || row.branch);

export function rowHint(row, can) {
  if (row.create) return "A new branch at HEAD";
  if (row.track) return `A new branch ${row.branch}, tracking it`;
  return can?.elsewhere?.includes(row.branch) ? "Checked out in another worktree" : "";
}

export function switchDone(row) {
  if (row.create) return `Made ${row.branch} and switched to it`;
  return row.track ? `Switched to ${row.branch}, tracking ${row.track}` : `Switched to ${row.branch}`;
}

// fetchNote is how the fetch of the remotes' branches is going.
export function fetchNote(can) {
  if (can?.fetching) return "Fetching the remotes' branches…";
  return can?.fetchError ? `Could not fetch the remotes: ${can.fetchError}` : "";
}

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
    api.switchTo(row).then(() => onDone(switchDone(row)), (e) => setState({ error: e.message }));
  };
  const { sel, setSel, onKey, listRef } = usePaletteNav(rows.length, (i) => pick(rows[i]));
  const note = blockedNote(can) || fetchNote(can);
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
            key={(r.create ? "+" : "") + (r.track || r.branch)}
            className={cx("palette-row", "action-row", i === sel && "on", r.off && "unready")}
            onMouseMove={() => setSel(i)}
            onClick={() => pick(r)}
          >
            <span className="sym mono">{rowLabel(r)}</span>
            <span className="dim">{rowHint(r, can)}</span>
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
