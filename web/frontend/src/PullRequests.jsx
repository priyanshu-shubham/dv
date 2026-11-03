import { useEffect, useState } from "react";
import { api } from "./api.js";
import { cx } from "./util.js";
import { Modal, usePaletteNav } from "./Overlays.jsx";
import { IconBranch } from "./icons.jsx";

// A number, #number or a pull request's page names one the list may not have:
// a closed one, or one past the first fifty.
const named = (q) => {
  const t = q.trim();
  if (/^#?\d+$/.test(t)) return t.replace("#", "");
  return /^https?:\/\/\S+\/pull\/\d+/.test(t) ? t : "";
};

function where(pr) {
  if (pr.here) return "checked out here";
  return pr.elsewhere ? `checked out in ${pr.elsewhere}` : "";
}

// PRPalette is the Review a pull request action: the repository's open pull
// requests to pick from, or one named by number or link. Picking fetches it
// and hands it to onPick; nothing is checked out.
export function PRPalette({ onPick, onClose }) {
  const [q, setQ] = useState("");
  const [list, setList] = useState(null); // { prs } | { error }
  const [state, setState] = useState(null); // { busy } | { error }
  useEffect(() => {
    let live = true;
    api.prs().then(
      (d) => live && setList(d),
      (e) => live && setList({ error: e.message }),
    );
    return () => {
      live = false;
    };
  }, []);

  const t = q.trim().toLowerCase();
  const rows = (list?.prs || []).filter((p) => !t || [`#${p.number}`, p.title, p.head, p.author].some((s) => s?.toLowerCase().includes(t)));
  const arg = named(q);
  if (arg && !rows.some((p) => String(p.number) === arg)) rows.unshift({ arg });

  const pick = (row) => {
    if (state?.busy) return;
    setState({ busy: `Fetching ${row.arg ? (/^\d+$/.test(row.arg) ? `#${row.arg}` : "it") : `#${row.number}`}…` });
    api.reviewPR(row.arg || row.number).then((d) => onPick(d.pr), (e) => setState({ error: e.message }));
  };
  const { sel, setSel, onKey, listRef } = usePaletteNav(rows.length, (i) => pick(rows[i]));
  const note = state?.busy || state?.error || list?.error || (!list ? "Asking GitHub…" : "");
  return (
    <Modal onClose={onClose} className="palette">
      <div className="palette-input">
        <IconBranch size={15} />
        <input
          autoFocus
          value={q}
          placeholder="Review a pull request: search, or a number or link..."
          onChange={(e) => {
            setQ(e.target.value);
            setSel(0);
            setState(null);
          }}
          onKeyDown={onKey}
        />
      </div>
      <div className="palette-list" ref={listRef}>
        {rows.map((p, i) =>
          p.arg ? (
            <button key="arg" className={cx("palette-row", "action-row", i === sel && "on")} onMouseMove={() => setSel(i)} onClick={() => pick(p)}>
              <span className="sym">Review {/^\d+$/.test(p.arg) ? `#${p.arg}` : p.arg}</span>
              <span className="dim">Whatever its state</span>
            </button>
          ) : (
            <button key={p.number} className={cx("palette-row", "action-row", i === sel && "on")} onMouseMove={() => setSel(i)} onClick={() => pick(p)}>
              <span className="sym">
                <span className="dim">#{p.number}</span> {p.title}
                {p.state === "draft" && <span className="dim"> · draft</span>}
              </span>
              <span className="dim mono">{[`${p.head} → ${p.base}`, p.author, where(p)].filter(Boolean).join(" · ")}</span>
            </button>
          ),
        )}
        {list?.prs && rows.length === 0 && <div className="empty">{t ? "No open pull request matches." : "No open pull requests."}</div>}
      </div>
      {note && <div className={cx("palette-foot", "dim", (state?.error || list?.error) && "del")}>{note}</div>}
    </Modal>
  );
}
