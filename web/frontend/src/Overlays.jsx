import { useEffect, useMemo, useRef, useState } from "react";
import { api } from "./api.js";
import { cx, LRM, modKey, statusLabel, useDebounced } from "./util.js";
import { ensureLanguage, escapeHtml, highlightLines, langReady } from "./highlight.js";
import { IconBack, IconFile, IconRefresh, IconSearch, IconSymbol, IconX } from "./icons.jsx";

// Modal shell shared by every overlay. Escape retraces the trail of definitions
// one step; Shift+Escape and a click outside leave it altogether. With nothing
// behind the overlay the two are the same key doing the same thing.
function Modal({ onClose, onBack, className, children, wide }) {
  useEffect(() => {
    const onKey = (e) => {
      if (e.key === "Escape") {
        e.stopPropagation();
        (e.shiftKey || !onBack ? onClose : onBack)();
      } else if (onBack && ((e.altKey && e.key === "ArrowLeft") || ((e.metaKey || e.ctrlKey) && e.key === "["))) {
        e.preventDefault(); // Alt+Left is the browser's own Back, which would leave the page
        e.stopPropagation();
        onBack();
      }
    };
    document.addEventListener("keydown", onKey, true);
    return () => document.removeEventListener("keydown", onKey, true);
  }, [onClose, onBack]);

  return (
    <div className="backdrop" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className={cx("modal", wide && "modal-wide", className)}>{children}</div>
    </div>
  );
}

// Back is only rendered when there is somewhere to go, and names where that is.
function Back({ onBack, to }) {
  if (!onBack) return null;
  return (
    <button className="ghost back" onClick={onBack} title={to ? `Back to ${to}` : "Back"}>
      <IconBack size={13} />
    </button>
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

// usePaletteNav drives a result list from its input: arrows or Ctrl+N/P move
// the selection, which is kept in sight, and Enter opens it.
function usePaletteNav(count, open) {
  const [sel, setSel] = useState(0);
  const listRef = useRef(null);

  useEffect(() => {
    listRef.current?.querySelector(".on")?.scrollIntoView({ block: "nearest" });
  }, [sel]);

  const onKey = (e) => {
    if (e.key === "ArrowDown" || (e.key === "n" && e.ctrlKey)) {
      e.preventDefault();
      setSel((s) => Math.max(0, Math.min(count - 1, s + 1)));
    } else if (e.key === "ArrowUp" || (e.key === "p" && e.ctrlKey)) {
      e.preventDefault();
      setSel((s) => Math.max(0, s - 1));
    } else if (e.key === "Enter") {
      e.preventDefault();
      if (sel < count) open(sel);
    }
    e.stopPropagation();
  };
  return { sel, setSel, onKey, listRef };
}

// SymbolPalette is the Cmd+K jump-to-definition list. It also serves as the
// disambiguator when a double-clicked identifier has several definitions.
export function SymbolPalette({ initialQuery = "", seed, source, from = "", onOpen, onClose, onBack, backTo }) {
  const [q, setQ] = useState(initialQuery);
  const [hits, setHits] = useState(seed || []);
  const [status, setStatus] = useState(null);
  const debounced = useDebounced(q, 90);
  const { sel, setSel, onKey, listRef } = usePaletteNav(hits.length, (i) => onOpen(hits[i]));
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

  return (
    <Modal onClose={onClose} onBack={onBack} className="palette">
      <div className="palette-input">
        <Back onBack={onBack} to={backTo} />
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
            {/* Said once per run: the same reason on every row hides the one that differs. */}
            {h.why && h.why !== hits[i - 1]?.why && <span className="why">{h.why}</span>}
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

// FilePalette is Cmd+P: any file in the repository, by a fuzzy match on its
// path. Until something is typed it offers the files in this diff.
export function FilePalette({ initialQuery = "", changed, onOpen, onClose, onBack, backTo }) {
  const [q, setQ] = useState(initialQuery);
  const [res, setRes] = useState(null);
  const debounced = useDebounced(q.trim(), 60);
  const hits = debounced ? res?.hits || [] : changed;
  const { sel, setSel, onKey, listRef } = usePaletteNav(hits.length, (i) => onOpen(hits[i].path, q));
  const status = useMemo(() => new Map(changed.map((f) => [f.path, f.status])), [changed]);

  useEffect(() => {
    let live = true;
    api
      .files(debounced)
      .then((r) => {
        if (!live) return;
        setRes(r);
        setSel(0);
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [debounced]);

  return (
    <Modal onClose={onClose} onBack={onBack} className="palette">
      <div className="palette-input">
        <Back onBack={onBack} to={backTo} />
        <IconFile size={15} />
        <input
          autoFocus
          value={q}
          placeholder="Go to file..."
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={onKey}
        />
      </div>
      <div className="palette-list" ref={listRef}>
        {hits.map((h, i) => {
          const base = h.path.lastIndexOf("/") + 1;
          // Offsets count bytes, which stop lining up with characters past ASCII.
          // eslint-disable-next-line no-control-regex
          const m = /[^\x00-\x7F]/.test(h.path) ? [] : h.matches || [];
          const st = status.get(h.path);
          return (
            <button
              key={h.path}
              className={cx("palette-row", "file-hit", i === sel && "on")}
              onMouseEnter={() => setSel(i)}
              onClick={() => onOpen(h.path, q)}
              title={st ? `${h.path} - ${statusLabel[st].toLowerCase()} in this diff` : h.path}
            >
              <span className={cx("dot", st ? "st-" + st : "unchanged")} />
              <span
                className="sym"
                dangerouslySetInnerHTML={{ __html: markMatches(h.path.slice(base), m.map((x) => x - base)) }}
              />
              <span className="spacer" />
              <span
                className="dim loc"
                dangerouslySetInnerHTML={{ __html: LRM + markMatches(h.path.slice(0, Math.max(0, base - 1)), m) }}
              />
            </button>
          );
        })}
        {hits.length === 0 && (
          <div className="empty">{debounced ? "No files match." : "Type to search the repository's files."}</div>
        )}
      </div>
      {res && (
        <div className="palette-foot dim">
          {debounced
            ? `${res.matched.toLocaleString()} of ${res.total.toLocaleString()} files match`
            : `${changed.length} changed file${changed.length === 1 ? "" : "s"} - type to search all ${res.total.toLocaleString()}`}
        </div>
      )}
    </Modal>
  );
}

// SearchPanel is repo-wide text search, grouped by file.
export function SearchPanel({ initialQuery = "", onOpen, onClose, onBack, backTo }) {
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
    <Modal onClose={onClose} onBack={onBack} wide className="search">
      <div className="palette-input">
        <Back onBack={onBack} to={backTo} />
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
// hits land. It is read-only on purpose: dv reviews, it does not edit. `side`
// reads the file off that side of the scope instead of the working tree.
export function FileViewer({ file, line, side, scope, scroll = 0, onClose, onBack, backTo, onSymbol }) {
  const [data, setData] = useState(null);
  const [error, setError] = useState("");
  const [, force] = useState(0);
  const bodyRef = useRef(null);

  useEffect(() => {
    setData(null);
    setError("");
    api
      .file(file, scope, side)
      .then(setData)
      .catch((e) => setError(e.message));
  }, [file, scope, side]);

  useEffect(() => {
    if (data?.lang) ensureLanguage(data.lang, () => force((n) => n + 1));
  }, [data?.lang]);

  // Arriving at a definition centres the line it is on; stepping back into a
  // file already read restores the position it was left at instead, so the way
  // out looks like the way in.
  useEffect(() => {
    if (!data) return;
    if (scroll && bodyRef.current) {
      bodyRef.current.scrollTop = scroll;
      return;
    }
    const el = bodyRef.current?.querySelector(`[data-line="${line}"]`);
    el?.scrollIntoView({ block: "center" });
  }, [data, line, scroll]);

  const ready = langReady(data?.lang);
  const html = useMemo(
    () => (data ? highlightLines(`view:${side || ""}:${file}`, data.lines, data.lang) : []),
    [data, file, side, ready],
  );

  return (
    <Modal onClose={onClose} onBack={onBack} wide className="viewer">
      <div className="viewer-head">
        <Back onBack={onBack} to={backTo} />
        <span className="path">{file}</span>
        {line > 0 && <span className="dim">:{line}</span>}
        {data?.at && (
          <span className="at" title={`The ${side} side of the comparison, not the working tree`}>
            {data.at}
          </span>
        )}
        <span className="spacer" />
        <button className="ghost" onClick={onClose}>
          <IconX size={13} />
        </button>
      </div>
      <div className="viewer-body" ref={bodyRef}>
        {error && <div className="empty error">{error}</div>}
        {!data && !error && <div className="empty">Loading...</div>}
        {data && (
          <div className="viewer-lines">
            {data.lines.map((_, i) => (
              <div key={i} className={cx("vrow", i + 1 === line && "hit")} data-line={i + 1}>
                <span className="ln">{i + 1}</span>
                <code
                  onDoubleClick={() => {
                    const sel = window.getSelection()?.toString().trim();
                    if (sel && /^[A-Za-z_$][\w$]*$/.test(sel)) onSymbol(sel, file, bodyRef.current?.scrollTop || 0);
                  }}
                  dangerouslySetInnerHTML={{ __html: html[i] || "&nbsp;" }}
                />
              </div>
            ))}
          </div>
        )}
      </div>
    </Modal>
  );
}

export function HelpOverlay({ onClose }) {
  const keys = [
    ["m", "Switch between Diff and Code"],
    [`${modKey}+P`, "Go to file"],
    [`${modKey}+K`, "Go to symbol"],
    [`${modKey}+Shift+F`, "Search the repository"],
    ["/", "Filter the file list"],
    ["[ / ]", "Previous / next file (also Shift+P / Shift+N)"],
    ["n / p", "Next / previous change"],
    ["f", "View the whole file at the focused line"],
    ["c", "Comment on the focused line"],
    ["a", "Ask Claude about the focused line"],
    ["v", "Mark the current file viewed, in Diff"],
    ["u", "Toggle split / unified"],
    ["w", "Toggle line wrapping"],
    ["r", "Reload the diff"],
    ["double-click", "Jump to a symbol's definition"],
    ["Alt+Left", "Back to the previous definition, or file in Code"],
    ["Alt+Right", "Forward again, in Code"],
    ["select code", "Comment on the selected lines"],
    ["drag line numbers", "Comment on a range of lines"],
    ["click a gap bar", "Expand 20 more lines of context"],
    ["Esc", "Back a step, or close what is open"],
    ["Shift+Esc", "Close whatever is open"],
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
