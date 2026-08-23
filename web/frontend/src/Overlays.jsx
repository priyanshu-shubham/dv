import { useEffect, useMemo, useRef, useState } from "react";
import { api } from "./api.js";
import { cx, LRM, modKey, useDebounced } from "./util.js";
import { ensureLanguage, escapeHtml, highlightLines } from "./highlight.js";
import { IconRefresh, IconSearch, IconSymbol, IconX } from "./icons.jsx";

// Modal shell shared by every overlay: click-outside and Escape both close.
function Modal({ onClose, className, children, wide }) {
  useEffect(() => {
    const onKey = (e) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        onClose();
      }
    };
    document.addEventListener("keydown", onKey, true);
    return () => document.removeEventListener("keydown", onKey, true);
  }, [onClose]);

  return (
    <div className="backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className={cx("modal", wide && "modal-wide", className)}>{children}</div>
    </div>
  );
}

// sourceNote explains a seeded list. "scan" means the index had no definition
// by that name and these are lines that merely look like declarations, which is
// worth saying rather than presenting a guess as a fact.
function sourceNote(source, n, name) {
  const sym = <code>{name}</code>;
  if (source === "scan") {
    return (
      <>
        No indexed definition of {sym} - {n} closest declaration{n === 1 ? "" : "s"}, nearest first
      </>
    );
  }
  return (
    <>
      {n} definition{n === 1 ? "" : "s"} of {sym}, nearest first
    </>
  );
}

// SymbolPalette is the Cmd+K jump-to-definition list. It also serves as the
// disambiguator when a double-clicked identifier has several definitions.
export function SymbolPalette({ initialQuery = "", seed, source, from = "", onOpen, onClose }) {
  const [q, setQ] = useState(initialQuery);
  const [hits, setHits] = useState(seed || []);
  const [status, setStatus] = useState(null);
  const [sel, setSel] = useState(0);
  const debounced = useDebounced(q, 90);
  const listRef = useRef(null);
  const seeded = seed && q === initialQuery;

  useEffect(() => {
    if (seeded) return; // showing a resolved lookup already
    let live = true;
    api
      .symbols(debounced, from)
      .then((r) => {
        if (!live) return;
        setHits(r.hits || []);
        setStatus(r.status);
        setSel(0);
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [debounced, seeded, from]);

  useEffect(() => {
    listRef.current?.querySelector(".on")?.scrollIntoView({ block: "nearest" });
  }, [sel]);

  const onKey = (e) => {
    if (e.key === "ArrowDown" || (e.key === "n" && e.ctrlKey)) {
      e.preventDefault();
      setSel((s) => Math.min(hits.length - 1, s + 1));
    } else if (e.key === "ArrowUp" || (e.key === "p" && e.ctrlKey)) {
      e.preventDefault();
      setSel((s) => Math.max(0, s - 1));
    } else if (e.key === "Enter") {
      e.preventDefault();
      if (hits[sel]) onOpen(hits[sel]);
    }
    e.stopPropagation();
  };

  return (
    <Modal onClose={onClose} className="palette">
      <div className="palette-input">
        <IconSymbol size={15} />
        <input
          autoFocus
          value={q}
          placeholder="Go to symbol..."
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={onKey}
        />
        <button className="ghost" onClick={() => api.refreshSymbols().then(setStatus)} title="Rebuild the index">
          <IconRefresh size={13} />
        </button>
      </div>
      {seeded && (
        <div className="palette-note dim">{sourceNote(source, hits.length, initialQuery)}</div>
      )}
      <div className="palette-list" ref={listRef}>
        {hits.map((h, i) => (
          <button
            key={`${h.file}:${h.line}:${h.name}`}
            className={cx("palette-row", i === sel && "on")}
            onMouseEnter={() => setSel(i)}
            onClick={() => onOpen(h)}
          >
            <span className={cx("kind", "kind-" + h.kind)}>{h.kind}</span>
            <span className="sym" dangerouslySetInnerHTML={{ __html: markMatches(h.name, h.matches) }} />
            {h.why && <span className="why">{h.why}</span>}
            <span className="spacer" />
            <span className="dim loc">
              {LRM}
              {h.file}:{h.line}
            </span>
          </button>
        ))}
        {hits.length === 0 && (
          <div className="empty">
            {status?.building ? "Building the symbol index..." : q ? "No symbols match." : "Type to search definitions."}
          </div>
        )}
      </div>
      {status && (
        <div className="palette-foot dim">
          {status.symbols.toLocaleString()} symbols in {status.files.toLocaleString()} files
          {status.building ? " - refreshing" : ""}
        </div>
      )}
    </Modal>
  );
}

function markMatches(name, matches) {
  if (!matches?.length) return escapeHtml(name);
  const set = new Set(matches);
  let out = "";
  for (let i = 0; i < name.length; i++) {
    out += set.has(i) ? `<b>${escapeHtml(name[i])}</b>` : escapeHtml(name[i]);
  }
  return out;
}

// SearchPanel is repo-wide text search, grouped by file.
export function SearchPanel({ initialQuery = "", onOpen, onClose }) {
  const [query, setQuery] = useState(initialQuery);
  const [regex, setRegex] = useState(false);
  const [caseSens, setCaseSens] = useState(false);
  const [wholeWord, setWholeWord] = useState(false);
  const [glob, setGlob] = useState("");
  const [res, setRes] = useState(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const debounced = useDebounced(query, 180);
  const debouncedGlob = useDebounced(glob, 250);

  useEffect(() => {
    if (!debounced.trim()) {
      setRes(null);
      setError("");
      return;
    }
    let live = true;
    setBusy(true);
    api
      .search({ query: debounced, regex, caseSens, wholeWord, glob: debouncedGlob })
      .then((r) => live && (setRes(r), setError("")))
      .catch((e) => live && (setError(e.message), setRes(null)))
      .finally(() => live && setBusy(false));
    return () => {
      live = false;
    };
  }, [debounced, regex, caseSens, wholeWord, debouncedGlob]);

  const groups = useMemo(() => {
    const m = new Map();
    for (const hit of res?.matches || []) {
      if (!m.has(hit.file)) m.set(hit.file, []);
      m.get(hit.file).push(hit);
    }
    return [...m.entries()];
  }, [res]);

  return (
    <Modal onClose={onClose} wide className="search">
      <div className="palette-input">
        <IconSearch size={15} />
        <input
          autoFocus
          value={query}
          placeholder="Search the repository..."
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={(e) => e.stopPropagation()}
        />
        <div className="toggles">
          <button className={cx(caseSens && "on")} onClick={() => setCaseSens((v) => !v)} title="Match case">
            Aa
          </button>
          <button className={cx(wholeWord && "on")} onClick={() => setWholeWord((v) => !v)} title="Whole word">
            ab
          </button>
          <button className={cx(regex && "on")} onClick={() => setRegex((v) => !v)} title="Regular expression">
            .*
          </button>
        </div>
        <input
          className="glob"
          value={glob}
          placeholder="*.go"
          onChange={(e) => setGlob(e.target.value)}
          onKeyDown={(e) => e.stopPropagation()}
        />
      </div>

      <div className="search-results">
        {error && <div className="empty error">{error}</div>}
        {!error && groups.length === 0 && (
          <div className="empty">{busy ? "Searching..." : query ? "No matches." : "Type to search."}</div>
        )}
        {groups.map(([file, hits]) => (
          <div className="search-group" key={file}>
            <div className="search-file">
              {file} <span className="dim">{hits.length}</span>
            </div>
            {hits.map((h, i) => (
              <button key={i} className="search-hit" onClick={() => onOpen({ file, line: h.line })}>
                <span className="ln">{h.line}</span>
                <code dangerouslySetInnerHTML={{ __html: markSpans(h.text, h.spans) }} />
              </button>
            ))}
          </div>
        ))}
      </div>
      {res && (
        <div className="palette-foot dim">
          {res.matches.length} match{res.matches.length === 1 ? "" : "es"} in {groups.length} file
          {groups.length === 1 ? "" : "s"}
          {res.truncated ? " (truncated)" : ""} - {res.engine}
        </div>
      )}
    </Modal>
  );
}

// markSpans highlights matched byte ranges. Offsets come from ripgrep in bytes,
// so the line is measured through a TextEncoder-free approximation: for ASCII
// they coincide, and for wider text we fall back to plain escaping.
function markSpans(text, spans) {
  if (!spans?.length) return escapeHtml(text);
  // eslint-disable-next-line no-control-regex
  if (/[^\x00-\x7F]/.test(text)) return escapeHtml(text);
  let out = "";
  let pos = 0;
  for (const [s, e] of spans) {
    if (s < pos) continue;
    out += escapeHtml(text.slice(pos, s)) + "<mark>" + escapeHtml(text.slice(s, e)) + "</mark>";
    pos = e;
  }
  return out + escapeHtml(text.slice(pos));
}

// FileViewer shows a whole source file, which is where symbol jumps and search
// hits land. It is read-only on purpose: dv reviews, it does not edit.
export function FileViewer({ file, line, onClose, onSymbol }) {
  const [data, setData] = useState(null);
  const [error, setError] = useState("");
  const [, force] = useState(0);
  const bodyRef = useRef(null);

  useEffect(() => {
    setData(null);
    setError("");
    api
      .file(file)
      .then(setData)
      .catch((e) => setError(e.message));
  }, [file]);

  useEffect(() => {
    if (data?.lang) ensureLanguage(data.lang, () => force((n) => n + 1));
  }, [data?.lang]);

  useEffect(() => {
    if (!data) return;
    const el = bodyRef.current?.querySelector(`[data-line="${line}"]`);
    el?.scrollIntoView({ block: "center" });
  }, [data, line]);

  const html = useMemo(
    () => (data ? highlightLines("view:" + file, data.lines, data.lang) : []),
    [data, file],
  );

  return (
    <Modal onClose={onClose} wide className="viewer">
      <div className="viewer-head">
        <span className="path">{file}</span>
        <span className="dim">:{line}</span>
        <span className="spacer" />
        <button className="ghost" onClick={onClose}>
          <IconX size={13} />
        </button>
      </div>
      <div className="viewer-body" ref={bodyRef}>
        {error && <div className="empty error">{error}</div>}
        {!data && !error && <div className="empty">Loading...</div>}
        {data &&
          data.lines.map((_, i) => (
            <div key={i} className={cx("vrow", i + 1 === line && "hit")} data-line={i + 1}>
              <span className="ln">{i + 1}</span>
              <code
                onDoubleClick={() => {
                  const sel = window.getSelection()?.toString().trim();
                  if (sel && /^[A-Za-z_$][\w$]*$/.test(sel)) onSymbol(sel, file);
                }}
                dangerouslySetInnerHTML={{ __html: html[i] || "&nbsp;" }}
              />
            </div>
          ))}
      </div>
    </Modal>
  );
}

export function HelpOverlay({ onClose }) {
  const keys = [
    [`${modKey}+K`, "Go to symbol"],
    [`${modKey}+Shift+F`, "Search the repository"],
    ["/", "Filter the file list"],
    ["j / k", "Next / previous file"],
    ["n / p", "Next / previous change"],
    ["c", "Comment on the focused line"],
    ["a", "Ask Claude about the focused line"],
    ["v", "Mark the current file viewed"],
    ["u", "Toggle split / unified"],
    ["w", "Toggle line wrapping"],
    ["r", "Reload the diff"],
    ["double-click", "Jump to a symbol's definition"],
    ["select code", "Comment on the selected lines"],
    ["drag line numbers", "Comment on a range of lines"],
    ["click a gap bar", "Expand 20 more lines of context"],
    ["Esc", "Close whatever is open"],
  ];
  return (
    <Modal onClose={onClose} className="help">
      <div className="viewer-head">
        <span className="path">Keyboard</span>
        <span className="spacer" />
        <button className="ghost" onClick={onClose}>
          <IconX size={13} />
        </button>
      </div>
      <div className="help-body">
        {keys.map(([k, d]) => (
          <div className="help-row" key={k}>
            <kbd>{k}</kbd>
            <span>{d}</span>
          </div>
        ))}
      </div>
    </Modal>
  );
}
