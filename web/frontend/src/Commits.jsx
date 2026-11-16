import { useEffect, useRef, useState } from "react";
import { api } from "./api.js";
import { IconBranch, IconSearch } from "./icons.jsx";
import { Modal, usePaletteNav } from "./Overlays.jsx";
import { Picker } from "./Picker.jsx";
import { cx, useDebounced } from "./util.js";

const EMPTY = { commits: [], more: false, missing: new Set() };

// CommitLog reads any branch's history without checking it out; ref "" is
// HEAD. Picking a commit shows it in the Diff. Read from another branch, the
// commits this one lacks are marked: what has moved on there since.
export function CommitLog({ meta, initialRef = "", onPick, onClose }) {
  const [ref, setRef] = useState(initialRef);
  const [q, setQ] = useState("");
  const query = useDebounced(q.trim(), 200);
  const [page, setPage] = useState(EMPTY);
  const [state, setState] = useState({ loading: true, error: "" });
  const [refs, setRefs] = useState(meta?.branches || []);
  const inputRef = useRef(null);
  // Where the list stands, read by loads started from any render: scrolling
  // can ask for the next page twice before the first answer re-renders, and a
  // page still coming for the last branch or search is dropped by its gen.
  const at = useRef({ ref, query, gen: 0, count: 0, more: false, loading: false });
  const here = meta?.head?.branch || meta?.head?.sha || "HEAD";
  const shown = ref || here;

  useEffect(() => {
    api.canSwitch().then((c) => setRefs([...(c.branches || []), ...(c.remote || []).map((r) => r.ref)]), () => {});
  }, []);

  const load = (next) => {
    const s = at.current;
    if (next && (s.loading || !s.more)) return;
    if (!next) s.gen++;
    const g = s.gen;
    s.loading = true;
    setState({ loading: true, error: "" });
    api.history(s.ref, next ? s.count : 0, s.query).then(
      (d) => {
        if (g !== s.gen) return;
        Object.assign(s, { loading: false, more: d.more, count: (next ? s.count : 0) + d.commits.length });
        setPage((p) => ({
          commits: next ? [...p.commits, ...d.commits] : d.commits,
          more: d.more,
          missing: new Set([...(next ? p.missing : []), ...d.missing]),
        }));
        setState({ loading: false, error: "" });
      },
      (e) => {
        if (g !== s.gen) return;
        Object.assign(s, { loading: false, more: false, count: 0 });
        setPage(EMPTY);
        setState({ loading: false, error: e.message });
      },
    );
  };
  useEffect(() => {
    Object.assign(at.current, { ref, query });
    load(false);
  }, [ref, query]);
  const more = () => load(true);

  const pick = (c) => {
    onPick(c.sha);
    onClose();
  };
  const { sel, setSel, onKey, listRef } = usePaletteNav(page.commits.length, (i) => pick(page.commits[i]));
  useEffect(() => setSel(0), [ref, query]);
  // The arrows reaching the end load the next page, as scrolling there does.
  useEffect(() => {
    if (sel >= page.commits.length - 5) more();
  }, [sel]);

  const choices = [
    { id: "", label: here, description: "This branch" },
    ...refs.filter((b) => b !== here).map((b) => ({ id: b, label: b })),
  ];
  const lacking = page.missing.size;
  return (
    <Modal onClose={onClose} className="palette commit-log">
      <div className="palette-input">
        <IconSearch size={15} />
        <input
          autoFocus
          ref={inputRef}
          value={q}
          placeholder={`Search ${shown}'s commit messages...`}
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={onKey}
        />
        <Picker
          label={
            <>
              <IconBranch size={12} />
              {shown}
            </>
          }
          title="Whose commits to read"
          choices={choices}
          value={ref}
          onPick={(r) => {
            setRef(r);
            inputRef.current?.focus();
          }}
        />
      </div>
      <div
        className="palette-list"
        ref={listRef}
        onScroll={(e) => {
          const l = e.currentTarget;
          if (l.scrollTop + l.clientHeight > l.scrollHeight - 200) more();
        }}
      >
        {page.commits.map((c, i) => (
          <button
            key={c.sha}
            className={cx("palette-row", "commit-row", i === sel && "on")}
            title={c.body ? `${c.subject}\n\n${c.body}` : c.subject}
            onMouseMove={() => setSel(i)}
            onClick={() => pick(c)}
          >
            <span className="commit-row-subject">{c.subject}</span>
            {page.missing.has(c.sha) && <span className="branch-hint">not on {here}</span>}
            <span className="commit-row-meta">
              <span className="mono">{c.short}</span> · {c.author}, {c.when}
            </span>
          </button>
        ))}
        {!state.loading && !state.error && page.commits.length === 0 && (
          <div className="empty">{query ? `No commit on ${shown} says “${query}”.` : `${shown} has no commits.`}</div>
        )}
        {state.error && <div className="empty del">{state.error}</div>}
      </div>
      <div className="palette-foot dim">
        {state.loading && !page.commits.length
          ? "Loading…"
          : `${page.commits.length}${page.more ? "+" : ""} commits${lacking ? `, ${lacking} of them not on ${here}` : ""}. Enter shows one in the Diff.`}
      </div>
    </Modal>
  );
}
