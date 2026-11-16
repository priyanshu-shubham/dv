import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { api, RESTART_KEY } from "./api.js";
import { boot, slug } from "./boot.js";
import { commentsOn, NO_EXPAND, noop } from "./CodeView.jsx";
import { CONTEXT_LINES, PRChip } from "./Header.jsx";
import { DiffBody } from "./FileDiff.jsx";
import { plainDiff } from "./hunks.js";
import { blockAt } from "./markdown.js";
import { cx, isMac, isTyping, LRM, modKey, openFolderSession, statusLabel, useCopy, useDebounced, useMedia } from "./util.js";
import { applyRanges, ensureLanguage, escapeHtml, highlightLines, langReady } from "./highlight.js";
import { findRegExp } from "./find.js";
import { MarkdownDocument, Media, previewKind, PreviewToggle, SvgPreview } from "./Preview.jsx";
import {
  IconAsk, IconBack, IconBell, IconBolt, IconFile, IconGauge, IconPalette, IconRefresh, IconSearch, IconServer, IconShell, IconSpark, IconSplit, IconSymbol, IconX,
} from "./icons.jsx";
import { MachineSetting } from "./Machine.jsx";
import { tabIconJPEG, tabIconURL } from "./favicon.js";
import { accentOf, ACCENTS, cleanName, CODE_FONTS, NAME_MAX, CODE_SIZES, LOOK_KEYS, lookOn, LOOKS, tabMark, THEMES, UI_FONTS } from "./appearance.js";
import { Picker } from "./Picker.jsx";
import { useBack } from "./phone/back.js";
import { PushSetting } from "./phone/PushSetting.jsx";
import { pushable } from "./phone/push.js";
import { SessionBar } from "./phone/Session.jsx";
import { chime } from "./Notices.jsx";

// Modal shell shared by every overlay. Escape retraces the trail of definitions
// one step; Shift+Escape and a click outside leave it altogether. With nothing
// behind the overlay the two are the same key doing the same thing.
// A palette hangs from near the top, where a list growing as you type does not
// move what is being typed in; a dialog or viewer, whose size is set, is centred.
export function Modal({ onClose, onBack, className, children, wide, centred }) {
  useEffect(() => {
    const onKey = (e) => {
      if (e.key === "Escape") {
        // The comment box's own Esc, and an open list's.
        if (e.target.closest?.('.composer, [aria-expanded="true"]')) return;
        e.stopPropagation();
        (e.shiftKey || !onBack ? onClose : onBack)();
      } else if (onBack && ((e.altKey && e.key === "ArrowLeft" && !(isMac && isTyping(e.target))) || ((e.metaKey || e.ctrlKey) && e.key === "["))) {
        // In a Mac's text box, Option and Left is a word back.
        e.preventDefault(); // Alt+Left is the browser's own Back, which would leave the page
        e.stopPropagation();
        onBack();
      }
    };
    document.addEventListener("keydown", onKey, true);
    return () => document.removeEventListener("keydown", onKey, true);
  }, [onClose, onBack]);

  const ref = useRef(null);
  useLayer(ref, 100);
  // In the phone app, Android's Back is this Esc.
  useBack(true, onBack || onClose);
  return createPortal(
    <div className={cx("backdrop", centred && "centred")} ref={ref} onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className={cx("modal", wide && "modal-wide", className)}>{children}</div>
    </div>,
    document.body,
  );
}

// The overlays open, each drawn outside the page so the page can be made inert
// while one is: find in page, Tab and a screen reader then keep to the one on
// top, the highest rank (its CSS z-index), the last opened among equals.
const layers = [];

function restack() {
  const top = layers.reduce((t, l) => (t && t.rank > l.rank ? t : l), null);
  for (const l of layers) l.el.inert = l !== top;
  document.getElementById("root").inert = layers.length > 0;
}

// useLayer makes the element ref holds, portalled to the body, such a layer
// while open. Put away, it hands the focus back to wherever the reader was,
// which only the page made inert again can take.
export function useLayer(ref, rank, open = true) {
  useLayoutEffect(() => {
    const el = ref.current;
    if (!open || !el) return;
    const before = document.activeElement;
    const l = { el, rank };
    layers.push(l);
    restack();
    return () => {
      layers.splice(layers.indexOf(l), 1);
      el.inert = false;
      restack();
      const now = document.activeElement;
      if (before?.isConnected && before !== document.body && (!now || now === document.body || el.contains(now))) before.focus({ preventScroll: true });
    };
  }, [ref, rank, open]);
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
export function usePaletteNav(count, open) {
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

export function markMatches(name, matches) {
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
              onMouseMove={() => setSel(i)}
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

// A few definitions head the results; a fuzzy match on a name finds plenty, and
// the ones past the first few would push the text matches out of sight.
const DEFS_SHOWN = 5;

// SearchPanel finds a name or text anywhere in the repository: definitions from
// the symbol index first, then the lines that contain it, grouped by file. A
// Cmd/Ctrl+clicked identifier that did not resolve to exactly one definition
// opens it seeded with its candidates, the text narrowed to the whole word.
export function SearchPanel({ initialQuery = "", seed, source, draft, from = "", opts = {}, place, onOpen, onLeave, onClose, onBack, backTo }) {
  const [query, setQuery] = useState(initialQuery);
  const inputRef = useRef(null);
  // A query it opens with was picked up elsewhere, so typing replaces it.
  useEffect(() => inputRef.current?.select(), []);
  const [regex, setRegex] = useState(!!opts.regex);
  const [caseSens, setCaseSens] = useState(!!opts.caseSens);
  const [wholeWord, setWholeWord] = useState(!!opts.wholeWord);
  const [glob, setGlob] = useState(opts.glob || "");
  const [res, setRes] = useState(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [defs, setDefs] = useState(seed || []);
  const [status, setStatus] = useState(null);
  const [allDefs, setAllDefs] = useState(false);
  const debounced = useDebounced(query, 180);
  const debouncedName = useDebounced(query.trim(), 90);
  const debouncedGlob = useDebounced(glob, 250);
  const seeded = seed && query === initialQuery;

  useEffect(() => {
    if (seeded || regex || !debouncedName) {
      setDefs(seeded ? seed : []);
      return;
    }
    let live = true;
    api
      .symbols(debouncedName, from)
      .then((r) => {
        if (!live) return;
        setDefs(r.hits || []);
        setStatus(r.status);
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [debouncedName, seeded, seed, regex, from]);

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
    // In path order: the search reads files in parallel, and hands them back
    // in whatever order they finished, which would move them each time.
    return [...m.entries()].sort(([a], [b]) => (a < b ? -1 : a > b ? 1 : 0));
  }, [res]);

  useEffect(() => setAllDefs(false), [debouncedName]);
  const shownDefs = seeded || allDefs || defs.length <= DEFS_SHOWN + 1 ? defs : defs.slice(0, DEFS_SHOWN);
  const moreDefs = defs.length - shownDefs.length;

  // One list for the keys, definitions then text: the row index is the item's.
  const items = [
    ...shownDefs.map((h) => ({ file: h.file, line: h.line, draft: h.draft })),
    ...(moreDefs ? [{ more: true }] : []),
    ...groups.flatMap(([file, hits]) => hits.map((h) => ({ file, line: h.line }))),
  ];
  // What it was left at goes with it: the query and toggles, and the row and
  // scroll of the results, which come back with it however it was left.
  const scrolled = useRef(place?.scroll || 0);
  const state = () => ({ query, opts: { regex, caseSens, wholeWord, glob }, place: { sel, at: items[sel], scroll: scrolled.current } });
  const left = useRef(null);
  const open = (i, line) => {
    const it = items[i];
    if (it.more) setAllDefs(true);
    else onOpen(line ? { ...it, line } : it, state());
  };
  const { sel, setSel, onKey, listRef } = usePaletteNav(items.length, open);
  left.current = state();
  useEffect(() => () => onLeave?.(left.current), []);
  // Coming back, the row and scroll it had, once its results are in again.
  const restoring = useRef(place);
  useEffect(() => {
    if (!restoring.current) setSel(0);
  }, [debounced, debouncedName, regex, caseSens, wholeWord, debouncedGlob, setSel]);
  useLayoutEffect(() => {
    const p = restoring.current;
    if (!p || busy || debounced !== query || (!res && !error)) return;
    restoring.current = null;
    if (listRef.current) listRef.current.scrollTop = p.scroll;
    // The same result, found again: the text search may list files in another order.
    const i = items.findIndex((it) => it.file === p.at?.file && it.line === p.at?.line);
    setSel(i >= 0 ? i : Math.max(0, Math.min(p.sel, items.length - 1)));
  });
  const wide = useMedia(SPLIT);
  const shown = !items[sel]?.more && items[sel];
  const cache = useMemo(() => new Map(), []);
  // The query as the search took it: without Match case, an upper-case letter
  // makes it case-sensitive, as ripgrep's smart case does.
  const re = useMemo(
    () => findRegExp({ query: debounced.trim(), regex, wholeWord, caseSens: caseSens || /\p{Lu}/u.test(debounced) })?.re,
    [debounced, regex, wholeWord, caseSens],
  );

  let n = shownDefs.length + (moreDefs ? 1 : 0);
  const matched = res?.matches.length || 0;

  return (
    <Modal onClose={onClose} onBack={onBack} wide className={cx("search", wide && "split")}>
      <div className="palette-input">
        <Back onBack={onBack} to={backTo} />
        <IconSearch size={15} />
        <input
          ref={inputRef}
          autoFocus
          value={query}
          placeholder="Search definitions and text..."
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={onKey}
        />
        <div className="toggles">
          <button className={cx(caseSens && "on")} onClick={() => setCaseSens((v) => !v)} title="Match case">
            Aa
          </button>
          <button className={cx(wholeWord && "on")} onClick={() => setWholeWord((v) => !v)} title="Whole word">
            ab
          </button>
          <button
            className={cx(regex && "on")}
            onClick={() => setRegex((v) => !v)}
            title="Regular expression - searches the text only"
          >
            .*
          </button>
        </div>
        <input
          className="glob"
          value={glob}
          placeholder="*.go"
          title="Only text in files matching this"
          onChange={(e) => setGlob(e.target.value)}
          onKeyDown={(e) => e.stopPropagation()}
        />
      </div>

      <div className={cx("search-split", wide && shown && "previewing")}>
      <div className="search-results" ref={listRef} onScroll={(e) => (scrolled.current = e.currentTarget.scrollTop)}>
        {shownDefs.length > 0 && (
          <div className="search-group">
            <div className="search-section">
              <IconSymbol size={12} />
              {seeded ? (
                <span>{sourceNote(source, defs.length, initialQuery)}</span>
              ) : (
                <>
                  Definitions <span className="dim">{defs.length}</span>
                </>
              )}
            </div>
            {shownDefs.map((h, i) => (
              <button
                key={`${h.file}:${h.line}:${h.name}`}
                className={cx("palette-row", i === sel && "on")}
                onMouseMove={() => setSel(i)}
                onClick={() => open(i)}
              >
                <span className={cx("kind", "kind-" + h.kind)}>{h.kind}</span>
                <span className="sym" dangerouslySetInnerHTML={{ __html: markMatches(h.name, h.matches) }} />
                {/* Said once per run: the same reason on every row hides the one that differs. */}
                {h.why && h.why !== shownDefs[i - 1]?.why && <span className="why">{h.why}</span>}
                <span className="spacer" />
                <span className="dim loc">
                  {LRM}
                  {h.file}:{h.line}
                </span>
              </button>
            ))}
            {moreDefs > 0 && (
              <button
                className={cx("palette-row", "search-more", sel === shownDefs.length && "on")}
                onMouseMove={() => setSel(shownDefs.length)}
                onClick={() => setAllDefs(true)}
              >
                {moreDefs} more definition{moreDefs === 1 ? "" : "s"}
              </button>
            )}
          </div>
        )}
        {groups.length > 0 && (
          <div className="search-section">
            <IconSearch size={12} />
            Text{" "}
            <span className="dim">
              {matched} match{matched === 1 ? "" : "es"} in {groups.length} file{groups.length === 1 ? "" : "s"}
              {res.truncated ? ", more not shown" : ""}
            </span>
          </div>
        )}
        {groups.map(([file, hits]) => (
          <div className="search-group" key={file}>
            <div className="search-file">
              {file} <span className="dim">{hits.length}</span>
            </div>
            {hits.map((h, i) => {
              const at = n++;
              return (
                <button
                  key={i}
                  className={cx("search-hit", at === sel && "on")}
                  onMouseMove={() => setSel(at)}
                  onClick={() => open(at)}
                >
                  <span className="ln">{h.line}</span>
                  <code dangerouslySetInnerHTML={{ __html: markSpans(h.text, h.spans) }} />
                </button>
              );
            })}
          </div>
        ))}
        {error && <div className="empty error">{error}</div>}
        {!error && !groups.length && !defs.length && (
          <div className="empty">
            {busy ? "Searching..." : query.trim() ? "Nothing matches." : "Type a name or any text."}
          </div>
        )}
      </div>
      {wide && shown && (
        <SearchPreview file={shown.file} line={shown.line} draft={shown.draft && draft} re={re} cache={cache} onOpen={(line) => open(sel, line)} />
      )}
      </div>
      {(res || status) && (
        <div className="palette-foot dim">
          {[status && `${status.symbols.toLocaleString()} symbols indexed${status.building ? ", refreshing" : ""}`, res?.engine]
            .filter(Boolean)
            .join(" · ")}
          <span className="spacer" />
          <button className="ghost" onClick={() => api.refreshSymbols().then(setStatus)} title="Rebuild the symbol index">
            <IconRefresh size={12} />
          </button>
        </div>
      )}
    </Modal>
  );
}

// Beside the results only where both have room.
const SPLIT = "(min-width: 900px)";
// How many lines either side of the result the preview draws.
const PREVIEW_AROUND = 150;

// SearchPreview is the file around the selected result as it is on disk,
// or for a definition in a draft as the edit would leave it, its line marked;
// each file is fetched once, into cache. A click on a line opens the file there.
function SearchPreview({ file, line, draft, re, cache, onOpen }) {
  const key = (draft ? "draft:" : "") + file;
  const [data, setData] = useState(() => cache.get(key) || null);
  const [, force] = useState(0);
  const bodyRef = useRef(null);
  useEffect(() => {
    if (draft && !cache.has(key)) cache.set(key, { lines: draft.fd.newLines, lang: draft.fd.lang });
    if (cache.has(key)) return setData(cache.get(key));
    setData(null);
    let live = true;
    api.file(file).then(
      (d) => (cache.set(key, d), live && setData(d)),
      (e) => live && setData({ error: e.message }),
    );
    return () => {
      live = false;
    };
  }, [key, file, draft, cache]);
  useEffect(() => {
    if (data?.lang) ensureLanguage(data.lang, () => force((n) => n + 1));
  }, [data?.lang]);
  const lines = data?.lines;
  const ready = langReady(data?.lang);
  const html = useMemo(() => lines && highlightLines("preview:" + key, lines, data.lang), [lines, key, data?.lang, ready]);
  const from = Math.max(0, line - 1 - PREVIEW_AROUND);
  const to = lines ? Math.min(lines.length, line + PREVIEW_AROUND) : 0;
  useLayoutEffect(() => {
    const body = bodyRef.current;
    const el = body?.querySelector(".on");
    if (el) body.scrollTop = el.offsetTop - (body.clientHeight - el.offsetHeight) / 2;
  }, [file, line, html]);
  return (
    <div className="search-preview">
      <div className="search-file">
        {LRM}
        {file}
        <span className="dim">:{line}</span>
      </div>
      <div className="search-preview-body" ref={bodyRef}>
        {data?.error && <div className="empty error">{data.error}</div>}
        {!data && <div className="empty">Loading...</div>}
        {data?.media && <div className="empty">Open it to see it.</div>}
        {html?.slice(from, to).map((h, i) => {
          const n = from + i + 1;
          const marks = re ? matchesIn(lines[n - 1], re) : [];
          return (
            <div key={n} className={cx("search-hit", n === line && "on")} onClick={() => onOpen(n)} title="Open the file here">
              <span className="ln">{n}</span>
              <code dangerouslySetInnerHTML={{ __html: applyRanges(h, marks, n === line ? "fm fm-on" : "fm") || " " }} />
            </div>
          );
        })}
      </div>
    </div>
  );
}

// matchesIn is where re matches in text, as [start, end) ranges.
function matchesIn(text, re) {
  const out = [];
  re.lastIndex = 0;
  for (let m; (m = re.exec(text)); ) {
    if (!m[0]) re.lastIndex++;
    else out.push([m.index, m.index + m[0].length]);
  }
  return out;
}

// markSpans highlights matched byte ranges. Offsets come from ripgrep in bytes,
// so the line is measured through a TextEncoder-free approximation: for ASCII
// they coincide, and for wider text we fall back to plain escaping.
function markSpans(full, spans) {
  // Its indentation is left out, so a match far into a line is still in view.
  const lead = full.length - full.trimStart().length;
  const text = full.slice(lead);
  if (!spans?.length) return escapeHtml(text);
  // eslint-disable-next-line no-control-regex
  if (/[^\x00-\x7F]/.test(full)) return escapeHtml(text);
  let out = "";
  let pos = 0;
  for (let [s, e] of spans) {
    s = Math.max(0, s - lead);
    e -= lead;
    if (s < pos || e <= s) continue;
    out += escapeHtml(text.slice(pos, s)) + "<mark>" + escapeHtml(text.slice(s, e)) + "</mark>";
    pos = e;
  }
  return out + escapeHtml(text.slice(pos));
}

// FileViewer shows a whole source file, which is where symbol jumps and search
// hits land. It is read-only on purpose: dv reviews, it does not edit. `side`
// reads the file off that side of the scope instead of the working tree. It
// draws through Code mode's rows, so its code is commented on, and added to a
// session, as there; `changes` is the file's diff, once loaded. A `draft`,
// { path, fd }, shows the file as an edit not yet made would leave it, which
// has no lines yet to comment on.
export function FileViewer({
  file, line, side, draft, scope, scroll = 0, threads, changes, wrap, preview, onPreview, onOpenFile, onClose, onBack, backTo, onSymbol, onComment, onThreadAction, onAttach: attach, onSearch,
}) {
  const [data, setData] = useState(null);
  const [error, setError] = useState("");
  const [, force] = useState(0);
  const bodyRef = useRef(null);
  const [copied, copy] = useCopy(file);
  const [selection, setSelection] = useState(null);
  const [composing, setComposing] = useState(null);
  const at = side === "old" ? "old" : "new";
  const kind = data && !data.media ? previewKind(file) : "";
  const rendered = !!kind && preview;
  // A file in the diff is drawn through it, so what changed is marked as in Code mode.
  const marked = !!changes && !changes.binary && !changes.tooLarge;

  useEffect(() => {
    setData(null);
    setError("");
    if (draft) return setData({ lines: draft.fd.newLines, lang: draft.fd.lang });
    api
      .file(file, scope, side)
      .then(setData)
      .catch((e) => setError(e.message));
  }, [file, scope, side, draft]);

  useEffect(() => {
    if (data?.lang) ensureLanguage(data.lang, () => force((n) => n + 1));
  }, [data?.lang]);

  // Arriving at a definition centres the line it is on; stepping back into a
  // file already read restores the position it was left at instead, so the way
  // out looks like the way in. The diff's removed lines move it once they come.
  useEffect(() => {
    if (!data) return;
    if (scroll && bodyRef.current) {
      bodyRef.current.scrollTop = scroll;
      return;
    }
    const body = bodyRef.current;
    const el = body && (body.querySelector(`[data-line="${line}"][data-side]`) || blockAt(body, line));
    el?.scrollIntoView({ block: "center" });
  }, [data, line, scroll, marked]);

  // Rendered or as source, the line at the top stays there: noted as Preview
  // is toggled, and put back once the other is drawn.
  const [kept, setKept] = useState(0);
  const togglePreview = (on) => {
    const body = bodyRef.current;
    // Past the 6px a block is put back at, so rounding cannot leave it under.
    const y = body.getBoundingClientRect().top + 8;
    const els = [...body.querySelectorAll("[data-line][data-side], .md-preview [data-src]")];
    // Blocks nest, a list around its items: the innermost across the top edge.
    const across = els.filter((el) => {
      const r = el.getBoundingClientRect();
      return r.top <= y && r.bottom > y;
    });
    const at = across.pop() || els.find((el) => el.getBoundingClientRect().top > y);
    setKept(Number(at?.dataset.line || at?.dataset.src || 0));
    onPreview(on);
  };
  useLayoutEffect(() => {
    const body = bodyRef.current;
    if (!kept || !body) return;
    const el = rendered ? blockAt(body, kept) : body.querySelector(`[data-line="${kept}"][data-side]`);
    if (el) body.scrollTop += el.getBoundingClientRect().top - body.getBoundingClientRect().top - 6;
  }, [rendered, kept]);

  // The horizontal bars go under the code rather than over its last rows, as
  // wide as the code is beside the vertical scrollbar.
  const [barsIn, setBarsIn] = useState(null);
  useLayoutEffect(() => {
    const body = bodyRef.current;
    if (barsIn && body) barsIn.style.paddingRight = `${body.offsetWidth - body.clientWidth}px`;
  });

  const fd = useMemo(() => (marked ? changes : data?.lines && plainDiff({ ...data, path: file }, at)), [marked, changes, data, file, at]);
  const shown = useMemo(() => commentsOn(threads, at, changes), [threads, at, changes]);
  const startComment = useCallback(
    (s, start, end, selected) => {
      if (draft) return setSelection(null);
      const src = s === "old" ? fd?.oldLines : fd?.newLines;
      setComposing({ path: file, side: s, start, end, quote: src ? src.slice(start - 1, end) : [], selected });
      setSelection(null);
    },
    [fd, file, draft],
  );
  const onAttach = draft ? null : attach;
  // Where the viewer was, so stepping back from a definition returns there.
  const jump = useCallback((name, from) => onSymbol(name, from, bodyRef.current?.scrollTop || 0, draft), [onSymbol, draft]);

  return (
    <Modal onClose={onClose} onBack={onBack} wide centred className="viewer">
      <div className="viewer-head">
        <Back onBack={onBack} to={backTo} />
        <span className="path copy-path" title={`Copy the path, ${file}`} onClick={copy}>
          {file}
          {copied && <span className="copied">copied</span>}
        </span>
        {line > 0 && <span className="dim">:{line}</span>}
        {data?.at && (
          <span className="at" title={`The ${side} side of the comparison, not the working tree`}>
            {data.at}
          </span>
        )}
        {draft && (
          <span className="at" title="As the edit waiting on you would leave it, not as it is on disk">
            edit
          </span>
        )}
        <span className="spacer" />
        {kind && fd && <PreviewToggle kind={kind} on={preview} onChange={togglePreview} />}
        <button className="ghost" onClick={onClose}>
          <IconX size={13} />
        </button>
      </div>
      <div className="viewer-body" ref={bodyRef}>
        {error && <div className="empty error">{error}</div>}
        {!data && !error && <div className="empty">Loading...</div>}
        {data?.media && <Media type={data.media} src={api.mediaURL(file, scope, side, data.stamp)} />}
        {fd && rendered && (
          <div className="file-body">
            {kind === "markdown" ? (
              <MarkdownDocument
                lines={at === "old" ? fd.oldLines : fd.newLines}
                path={file}
                side={at}
                scope={scope}
                onOpenFile={onOpenFile}
                threads={shown}
                composing={composing}
                setComposing={setComposing}
                onStartComment={startComment}
                onComment={onComment}
                onThreadAction={onThreadAction}
                onAttach={onAttach}
                onSearch={onSearch}
              />
            ) : (
              <SvgPreview newLines={at === "old" ? fd.oldLines : fd.newLines} />
            )}
          </div>
        )}
        {fd && !rendered && (
          <div className={cx("file-body", wrap && "wrap")}>
            <DiffBody
              view="code"
              side={at}
              fd={fd}
              contextLines={0}
              expanded={NO_EXPAND}
              onExpand={noop}
              threads={shown}
              selection={selection}
              setSelection={setSelection}
              composing={composing}
              setComposing={setComposing}
              onStartComment={startComment}
              onComment={onComment}
              onThreadAction={onThreadAction}
              onSymbol={jump}
              onAttach={onAttach}
              onSearch={onSearch}
              path={file}
              wrap={wrap}
              reveal={kept || line}
              hit={line}
              barsIn={barsIn}
            />
          </div>
        )}
      </div>
      <div className="viewer-bars" ref={setBarsIn} />
    </Modal>
  );
}

export const OFF_ON = [
  [false, "Off"],
  [true, "On"],
];

const CODE_COLORS = [["github", "GitHub"], ["one", "One"], ["solarized", "Solarized"], ["tomorrow", "Tomorrow"], ["monokai", "Monokai"]];
const fontChoices = (fonts, base) => Object.entries(fonts).map(([k, [name, family]]) => [k, <span style={{ fontFamily: family || base }}>{name}</span>, name]);

// AppearanceSettings is Settings' first tab. A look sets the theme and the
// switches under it at once; the accent is this computer's colour, which the
// tab icon, the name in the top bar and the installed app take too.
function AppearanceSettings({ theme, onTheme, settings: s, onChange, phoneApp }) {
  const codeColors = (
    <Setting
      label="Code colors"
      note="How code is highlighted. Monokai is dark only: the light theme shows GitHub's."
      value={s.codeColors || "github"}
      onPick={(v) => onChange({ codeColors: v })}
      choices={CODE_COLORS}
    />
  );
  const accent = accentOf(s);
  const mark = tabMark(theme, s);
  // Settings from before the accent hold the tab's colour by name; the first
  // pick here moves them over.
  const setMark = (patch) => onChange({ accent: accent || undefined, tabIcon: mark.style || undefined, tabColor: undefined, ...patch });
  const accentRow = (
    <div className="settings-row">
      <div className="settings-text">
        <div>Accent</div>
        <div className="settings-note">
          {phoneApp
            ? "This computer's colour, in Material's tones here: what is picked, and the name in the top bar on a computer."
            : "This computer's colour: links, focus and selection, and the tab icon and name below. Every dv on this computer uses it."}
        </div>
      </div>
      <span className="seg accent-picks">
        <button className={cx(!accent && "on")} aria-pressed={!accent} title="Each theme's own" onClick={() => setMark({ accent: undefined })}>
          Theme's
        </button>
        {ACCENTS.map((c) => (
          <button key={c} className={cx(accent === c && "on")} aria-pressed={accent === c} aria-label={c} title={c} onClick={() => setMark({ accent: c })}>
            <span className="swatch" style={{ background: c }} />
          </button>
        ))}
        <label className={cx("any-colour", accent && !ACCENTS.includes(accent) && "on")} title="Any colour">
          <span className="swatch" style={accent && !ACCENTS.includes(accent) ? { background: accent } : undefined} />
          <input type="color" value={accent || THEMES[theme]?.accent || THEMES.dark.accent} onChange={(e) => setMark({ accent: e.target.value })} />
        </label>
      </span>
    </div>
  );
  // The phone app keeps Material's dark look, in the accent, and has no
  // sidebar or tab to name.
  if (phoneApp)
    return (
      <>
        {accentRow}
        {codeColors}
      </>
    );
  const look = Object.keys(LOOKS).find((k) => lookOn(LOOKS[k], theme, s)) || "";
  const pickLook = (k) => {
    const l = LOOKS[k];
    onTheme(l.theme);
    onChange(Object.fromEntries(LOOK_KEYS.map((key) => [key, l[key]])));
  };
  const off = (v, none) => (v === none ? undefined : v);
  const accentFor = (t) => accent || THEMES[t].accent;
  return (
    <>
      <div className="settings-heading">Colour</div>
      <div className="settings-row">
        <div className="settings-text">
          <div>Look</div>
          <div className="settings-note">Sets the theme and the switches below it at once. The accent, the tab and the code size stay as they are.</div>
        </div>
        <div className="look-tiles" role="group" aria-label="Look">
          {Object.entries(LOOKS).map(([k, l]) => (
            <button key={k} className={cx("look-tile", k === look && "on")} aria-pressed={k === look} onClick={() => pickLook(k)}>
              <LookPicture theme={l.theme} accent={accentFor(l.theme)} glass={l.glass} />
              <span>{l.name}</span>
            </button>
          ))}
        </div>
      </div>
      <Setting
        label="Theme"
        value={theme}
        onPick={onTheme}
        choices={Object.entries(THEMES).map(([k, t]) => [
          k,
          <>
            <span className="theme-swatch" style={{ "--sw": t.bg, "--dot": accentFor(k) }} />
            {t.name}
          </>,
          t.name,
        ])}
      />
      {accentRow}
      {codeColors}
      <Setting
        label="Glow"
        note="A wash of the accent from the corners of the window."
        value={s.glow || "off"}
        onPick={(v) => onChange({ glow: off(v, "off") })}
        choices={[["off", "Off"], ["soft", "Soft"], ["strong", "Strong"]]}
      />
      <Setting
        label="Glass"
        note="Menus, palettes and dialogs frosted over the page behind them."
        value={!!s.glass}
        onPick={(v) => onChange({ glass: v || undefined })}
        choices={OFF_ON}
      />
      {s.glass && (
        <>
          <RangeSetting label="Blur" value={s.glassBlur ?? 16} min={0} max={40} step={2} unit="px" onChange={(v) => onChange({ glassBlur: off(v, 16) })} />
          <RangeSetting label="Opacity" value={s.glassOpacity ?? 60} min={25} max={90} step={5} unit="%" onChange={(v) => onChange({ glassOpacity: off(v, 60) })} />
        </>
      )}

      <div className="settings-heading">Space</div>
      <Setting
        label="Density"
        note="Room around the text, not the text's size: rows, bars, padding and the spacing of prose."
        value={s.density || "default"}
        onPick={(v) => onChange({ density: off(v, "default") })}
        choices={[["compact", "Compact"], ["default", "Default"], ["roomy", "Roomy"]]}
      />
      <Setting
        label="Corners"
        value={s.corners || "default"}
        onPick={(v) => onChange({ corners: off(v, "default") })}
        choices={[["sharp", "Sharp"], ["default", "Default"], ["round", "Round"]]}
      />
      <Setting
        label="Changed lines"
        note="How the diff marks an added or deleted line: its whole row shaded, lightly, or only a bar by its number."
        value={s.changes || "shaded"}
        onPick={(v) => onChange({ changes: off(v, "shaded") })}
        choices={[["shaded", "Shaded"], ["light", "Light"], ["gutter", "Gutter only"]]}
      />
      <Setting
        label="Sidebar"
        note="On the right, the comments open over it."
        value={s.sidebar || "left"}
        onPick={(v) => onChange({ sidebar: v === "left" ? undefined : v })}
        choices={[["left", "Left"], ["right", "Right"]]}
      />
      <Setting
        label="Reduce motion"
        note="Stops things sliding and fading as they change."
        value={s.motion === false}
        onPick={(v) => onChange({ motion: v ? false : undefined })}
        choices={OFF_ON}
      />

      <div className="settings-heading">Type</div>
      <Setting label="Interface font" value={s.uiFont || "system"} onPick={(v) => onChange({ uiFont: off(v, "system") })} choices={fontChoices(UI_FONTS, "")} />
      <Setting label="Code font" value={s.codeFont || "jetbrains"} onPick={(v) => onChange({ codeFont: off(v, "jetbrains") })} choices={fontChoices(CODE_FONTS, '"JetBrains Mono Variable"')} />
      <Setting label="Code size" value={s.codeSize || 12} onPick={(v) => onChange({ codeSize: off(v, 12) })} choices={CODE_SIZES.map((px) => [px, String(px)])} />

      <div className="settings-heading">This computer</div>
      <label className="settings-row">
        <div className="settings-text">
          <div>Name in the tab</div>
          <div className="settings-note">
            Beside dv's icon in the top bar, and in place of dv in the tab and as the installed app's name, to tell this computer's dv from another's: a word or two, as
            home or work-pc. Letters, digits, spaces, dots, dashes and underscores, up to {NAME_MAX}. Every dv on this computer uses it.
          </div>
        </div>
        <input
          value={s.tabName || ""}
          placeholder="dv"
          maxLength={NAME_MAX}
          spellCheck={false}
          onChange={(e) => onChange({ tabName: cleanName(e.target.value) || undefined })}
          onBlur={() => s.tabName && onChange({ tabName: cleanName(s.tabName).trim() || undefined })}
        />
      </label>
      <Setting
        label="Tab icon"
        note="Ringed or filled, it takes the accent, as does the name in the top bar, and the app's icon when installed after picking it."
        value={mark.style}
        onPick={(v) => setMark({ tabIcon: v || undefined })}
        choices={[
          ["", "dv's own"],
          ["ring", "Ringed"],
          ["filled", "Filled"],
        ].map(([v, name]) => [
          v,
          <>
            <img src={tabIconURL({ style: v, color: mark.color })} width={16} height={16} alt="" />
            {name}
          </>,
          name,
        ])}
      />
    </>
  );
}

function RangeSetting({ label, value, min, max, step, unit, onChange }) {
  return (
    <label className="settings-row">
      <div className="settings-text">
        <div>{label}</div>
      </div>
      <span className="range-setting">
        <input type="range" min={min} max={max} step={step} value={value} onChange={(e) => onChange(Number(e.target.value))} />
        <span className="range-value">
          {value}
          {unit}
        </span>
      </span>
    </label>
  );
}

// LookPicture is a look's window in small: its bar, sidebar card and a file
// card with a line picked out in its accent.
function LookPicture({ theme, accent, glass }) {
  const t = THEMES[theme];
  return (
    <span className={cx("look-picture", glass && "glass")} style={{ "--l-bg": t.bg, "--l-panel": t.panel, "--l-bar": t.bar, "--l-fg": t.fg, "--l-accent": accent }}>
      <i className="l-bar" />
      <i className="l-side" />
      <i className="l-main">
        <b />
        <b />
        <b className="l-pick" />
        <b />
      </i>
    </span>
  );
}

// SettingsOverlay is dv's preferences, kept in this browser: the page's own,
// as the header also sets them, and `settings`, which only this sets through
// onChange's patches. A phone always shows the diff unified, so it has no
// layout to pick.
// SettingsOverlay is `,` in a folder's page, and in the hub's, which has no
// keys to toggle the diff with (keys false). Its tabs are its sections, and
// actions, a folder page's, a tab of their own; tab is the one it opens on.
export function SettingsOverlay({
  theme, onTheme, view, onView, contextLines, onContext, wrap, onWrap, oneFile, onOneFile, phone, phoneApp, notices, onNotices, hooks, onHooks, terminal, settings, onChange, onClose, keys = true,
  working = 0,
  update,
  actions,
  ask,
  tab: opening = "appearance",
}) {
  const [tab, setTab] = useState(opening);
  // On a phone the tabs are a row scrolled sideways, which keeps the open one in it.
  const tabsRef = useRef(null);
  useEffect(() => {
    tabsRef.current.querySelector(".on")?.scrollIntoView({ block: "nearest", inline: "nearest" });
  }, [tab]);
  const tabs = [
    ["appearance", "Appearance", IconPalette],
    ["diff", "Diff", IconSplit],
    ["notifications", "Notifications", IconBell],
    ["agent", "Agent", IconSpark],
    ...(terminal ? [["terminal", "Terminal", IconShell]] : []),
    ...(ask ? [["ask", "Ask", IconAsk]] : []),
    ...(actions ? [["actions", "Actions", IconBolt]] : []),
    ["server", "Server", IconServer],
    ["monitor", "Monitor", IconGauge],
  ];
  return (
    <Modal onClose={onClose} centred className="settings">
      {phoneApp && <SessionBar title="Settings" onBack={onClose} menu={[]} />}
      <nav className="settings-tabs">
        {!phoneApp && <div className="settings-title">Settings</div>}
        <span className="seg" role="tablist" ref={tabsRef}>
          {tabs.map(([id, name, Icon]) => {
            // The Settings button's dot, on the tab the update is in.
            const dot = id === "server" && update;
            return (
              <button
                key={id}
                role="tab"
                className={cx(id === tab && "on", dot && "has-update")}
                aria-selected={id === tab}
                title={dot ? `dv v${update.latest} is out` : undefined}
                onClick={() => setTab(id)}
              >
                <Icon size={14} />
                {name}
              </button>
            );
          })}
        </span>
      </nav>
      {!phoneApp && (
        <button className="ghost settings-close" onClick={onClose} title="Close (Esc)">
          <IconX size={14} />
        </button>
      )}
      <div className="settings-body">
        {tab === "appearance" && <AppearanceSettings theme={theme} onTheme={onTheme} settings={settings} onChange={onChange} phoneApp={phoneApp} />}
        {tab === "diff" && (
          <>
            {!phone && <Setting label="Layout" note={keys ? "u toggles it" : ""} value={view} onPick={onView} choices={[["split", "Split"], ["unified", "Unified"]]} />}
            <Setting label="Context around each change" value={contextLines} onPick={onContext} choices={CONTEXT_LINES.map((n) => [n, n ? String(n) : "None"])} />
            <Setting label="Wrap long lines" note={phone || !keys ? "" : "w toggles it"} value={wrap} onPick={onWrap} choices={OFF_ON} />
            {!phone && onOneFile && (
              <Setting
                label="One file at a time"
                note={`Show the files one by one, marking one viewed moving on to the next${keys ? "; o toggles it" : ""}`}
                value={oneFile}
                onPick={onOneFile}
                choices={OFF_ON}
              />
            )}
          </>
        )}
        {tab === "notifications" && (
          <>
            {phoneApp ? (
              <>
                <PushSetting phone />
                <Setting
                  label="Vibrate in dv"
                  note="A buzz when an agent asks for you or finishes while dv is open: two for a request, one for a finished turn."
                  value={settings.noticeHaptics !== false}
                  onPick={(on) => onChange({ noticeHaptics: on ? undefined : false })}
                  choices={OFF_ON}
                />
              </>
            ) : pushable ? (
              <PushSetting />
            ) : (
              // Without push, the page tells of them itself, while it is open.
              <Setting
                label="Desktop notifications"
                note={
                  typeof Notification === "undefined"
                    ? "This browser does not offer them here: they need https or localhost."
                    : "You are told when an agent asks or finishes, if no dv page is in front of you."
                }
                value={!!notices}
                onPick={onNotices}
                choices={OFF_ON}
              />
            )}
            {!phoneApp && (
              <Setting
                label="Sound in dv"
                note="A short chime when an agent asks for you while dv is in front of you. Out of sight, the notification has your system's sound."
                value={settings.noticeSound !== false}
                onPick={(on) => {
                  onChange({ noticeSound: on ? undefined : false });
                  if (on) chime();
                }}
                choices={OFF_ON}
              />
            )}
            <ChatApps settings={settings} onChange={onChange} />
          </>
        )}
        {tab === "agent" && (
          <>
            {hooks && (
              <Setting
                label="Prompts from terminal sessions"
                note={`A session running in a terminal asks here too, through hooks dv puts in ${hooks.path}.`}
                value={hooks.on}
                onPick={onHooks}
                choices={OFF_ON}
              />
            )}
            <Setting
              label="Number keys answer prompts"
              note="An option's number answers with it at once, as in the terminal. Off, it moves to the option, and Enter answers."
              value={settings.numbersAnswer !== false}
              onPick={(on) => onChange({ numbersAnswer: on ? undefined : false })}
              choices={OFF_ON}
            />
            <Setting
              label="Suggest a next message"
              note="Shows what you might say next under the agent's reply, to put in the message box: Codex's own follow-ups, and a guess Claude Code makes after each turn with a small request of its own. A Claude session takes a change up the next time it starts."
              value={settings.promptSuggestions !== false}
              onPick={(on) => onChange({ promptSuggestions: on ? undefined : false })}
              choices={OFF_ON}
            />
            <Setting
              label="Title sessions"
              note="Names a session its agent has not, from how it began and where it has got to: once it has a reply, and once more after it has moved well on. Each title is one small request to Claude Code's Haiku, a fraction of a cent. Off, such a session is named by its first message. A name you give a session always stays."
              value={settings.autoTitles !== false}
              onPick={(on) => onChange({ autoTitles: on ? undefined : false })}
              choices={OFF_ON}
            />
            <Setting
              label="Show files in replies"
              note="Tells agents they can show a picture, video, sound, PDF or a web page of their own in a reply, and to make a page when something comes across better interactively than as text. A page runs as part of dv. A session takes a change up the next time it starts."
              value={settings.filesInReplies !== false}
              onPick={(on) => onChange({ filesInReplies: on ? undefined : false })}
              choices={OFF_ON}
            />
            <Setting
              label="Move idle sessions to Recent"
              note="An open session with no message for this long moves to Recent, as closing it would, and carries on when you next write to it. One kept running, or still at work, stays open."
              value={settings.closeIdleAfter ?? DAY}
              onPick={(v) => onChange({ closeIdleAfter: v === DAY ? undefined : v })}
              choices={CLOSE_IDLE}
            />
            <Setting
              label="Sessions opened by adding start temporary"
              note="A new session opened from Add, or from New session over selected text, starts temporary: once closed, it leaves the session list."
              value={!!settings.addedTemporary}
              onPick={(on) => onChange({ addedTemporary: on })}
              choices={OFF_ON}
            />
          </>
        )}
        {tab === "terminal" && (
          <>
            <Setting
              label="Copy on select"
              note={`Text selected in a terminal goes to the clipboard as you let go of the mouse, without ${isMac ? "Cmd" : "Ctrl"}+C, and the terminal goes back to its prompt, as tmux does.`}
              value={settings.copyOnSelect !== false}
              onPick={(on) => onChange({ copyOnSelect: on ? undefined : false })}
              choices={OFF_ON}
            />
            <Setting
              label="Type ahead"
              note="On a slow connection, what you type at a prompt shows at once, dimmed, until the shell echoes it back. Not at a password prompt, nor in a full-screen program such as Vim."
              value={settings.typeAhead !== false}
              onPick={(on) => onChange({ typeAhead: on ? undefined : false })}
              choices={OFF_ON}
            />
          </>
        )}
        {tab === "ask" && ask}
        {tab === "actions" && actions}
        {tab === "server" && (
          <>
            <UpdateCheck settings={settings} onChange={onChange} />
            <RestartRow working={working} update={update} />
          </>
        )}
        {tab === "monitor" && (
          <>
            <MachineSetting />
            <Logs />
          </>
        )}
      </div>
    </Modal>
  );
}

const LOG_LINES = 2000;
const clock = (at) => new Date(at).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit", hour12: false });

// Logs is what dv has printed to the terminal it runs in, followed while the
// tab is open: at the foot as lines come, unless scrolled up from it.
function Logs() {
  const [lines, setLines] = useState(null);
  const [error, setError] = useState("");
  const ref = useRef(null);
  const atEnd = useRef(true);
  useEffect(() => {
    let live = true;
    let timer;
    let after = 0;
    let run = "";
    const look = () =>
      api.logs(after).then(
        (r) => {
          if (!live) return;
          // A restarted dv counts from 1 again, so its lines are asked for afresh.
          if (run && r.started !== run) {
            run = r.started;
            after = 0;
            setLines([]);
            return look();
          }
          run = r.started;
          after = r.next - 1;
          setLines((had) => [...(had || []), ...r.lines].slice(-LOG_LINES));
          setError("");
          timer = setTimeout(look, 1500);
        },
        (e) => {
          if (!live) return;
          setError(e.message);
          timer = setTimeout(look, 3000);
        },
      );
    look();
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, []);
  useLayoutEffect(() => {
    if (atEnd.current && ref.current) ref.current.scrollTop = ref.current.scrollHeight;
  }, [lines]);
  const text = (lines || []).map((l) => `${clock(l.at)}  ${l.text}`).join("\n");
  const [copied, copy] = useCopy(text);
  return (
    <div className="settings-logs">
      <div className="settings-row">
        <span className="settings-note">What this dv has printed to its terminal, its latest {LOG_LINES.toLocaleString()} lines.</span>
        <span className="spacer" />
        <button className="btn" disabled={!lines?.length} onClick={copy}>
          {copied ? "Copied" : "Copy"}
        </button>
      </div>
      {error && <div className="settings-note error">{error}</div>}
      <pre
        ref={ref}
        className="command-output logs-output"
        onScroll={(e) => {
          const el = e.currentTarget;
          atEnd.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
        }}
      >
        {lines?.length === 0 && <span className="dim">Nothing yet.</span>}
        {lines?.map((l) => (
          <div key={l.n} className={cx(l.err && "logs-err")}>
            <span className="dim">{clock(l.at)}</span>
            {"  "}
            {l.text}
          </div>
        ))}
      </pre>
    </div>
  );
}

// WorktreeSession starts a session in a new worktree of the folder, which the
// hub makes on a branch named here, from what the folder has checked out, in
// a folder beside it named for the branch - and sets up, as its hook says.
// Once it is ready the page moves there, to a new session.
export function WorktreeSession({ repo, from, onClose }) {
  const [branch, setBranch] = useState("");
  const [job, setJob] = useState(null); // the hub's, once started
  const [error, setError] = useState("");
  const b = branch.trim();
  const create = async () => {
    if (!b || job) return;
    setError("");
    try {
      setJob(await api.hubWorktree(slug, { branch: b, here: true }));
    } catch (e) {
      setError(e.message);
    }
  };
  useEffect(() => {
    if (!job?.id) return;
    let timer;
    const look = async () => {
      const data = await api.hubFolders().catch(() => null);
      const running = data?.jobs.find((j) => j.id === job.id);
      const made = !running && data?.folders.find((f) => f.path === job.path);
      if (made) return openFolderSession(made.slug, "");
      if (running?.error) return setError(running.error);
      if (running) setJob((j) => ({ ...j, step: running.step, progress: running.progress }));
      timer = setTimeout(look, 600);
    };
    look();
    return () => clearTimeout(timer);
  }, [job?.id]);
  const note = job
    ? `${job.step || "Creating"}${job.progress ? `: ${job.progress}` : ""}…`
    : `A branch a remote has is checked out, tracking it; a new one starts from ${from}. In ${b ? `${repo}-${b.replaceAll("/", "-")}` : "a folder named for it"} beside ${repo}. What is not committed here stays behind.`;
  return (
    <Modal onClose={onClose} centred className="hub-form">
      <div className="viewer-head dialog-head">
        <span className="path">New session in a worktree</span>
        <span className="spacer" />
        <button className="ghost" onClick={onClose}>
          <IconX size={13} />
        </button>
      </div>
      <div className="settings-body hub-form-body">
        <label className="hub-field">
          <span>Branch</span>
          <input
            autoFocus
            value={branch}
            placeholder="fix/login"
            disabled={!!job}
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
            onChange={(e) => setBranch(e.target.value)}
            onKeyDown={(e) => {
              e.stopPropagation();
              if (e.key === "Enter") create();
            }}
          />
        </label>
      </div>
      {error && <div className="hub-picker-error">{error}</div>}
      <div className="hub-picker-foot">
        <span className="hub-picker-note">{note}</span>
        <button className="primary" disabled={!b || (!!job && !error)} onClick={create}>
          {job && !error ? "Creating…" : "Create"}
        </button>
      </div>
    </Modal>
  );
}

const plural = (n, one) => `${n} ${one}${n === 1 ? "" : "s"}`;

// FinishCommits lists what merging would bring in, newest first.
function FinishCommits({ info }) {
  const rest = info.ahead - info.commits.length;
  return (
    <ul className="finish-commits">
      {info.commits.map((c) => (
        <li key={c.sha} title={c.body ? `${c.subject}\n\n${c.body}` : c.subject}>
          <span className="finish-commit-subject">{c.subject}</span>
          <span className="finish-commit-meta">
            <span className="mono">{c.short}</span> · {c.when}
          </span>
        </li>
      ))}
      {rest > 0 && <li className="finish-commit-meta">and {plural(rest, "more commit")}</li>}
    </ul>
  );
}

// FinishWorktree closes the worktree the page is in: its branch merged first,
// if asked, into what the main checkout has, then the worktree deleted as the
// hub's card deletes it, and the branch too if asked. The page then goes to
// the main checkout's.
export function FinishWorktree({ meta, pr, onClose }) {
  const [info, setInfo] = useState(null);
  const [merge, setMerge] = useState(false);
  const [dropBranch, setDropBranch] = useState(false);
  const [busy, setBusy] = useState("");
  const [error, setError] = useState(null); // { message, retry, gone, id }
  const lastHow = useRef({});

  useEffect(() => {
    api.hubFinish(slug).then(
      (i) => {
        setInfo(i);
        setMerge(i.ahead > 0 && !i.mergeBlocked);
      },
      (e) => setError({ message: e.message }),
    );
  }, []);

  const leave = () => (location.href = info?.main ? `/${info.main}/` : "/");

  // The page's own review closes as the worktree goes, so the hub is asked.
  const remove = async (how) => {
    setBusy(info.teardown && !how.skipTeardown ? "Tearing down…" : "Deleting…");
    const job = await api.hubDeleteWorktree(slug, { ...how, deleteBranch: dropBranch });
    for (;;) {
      await new Promise((r) => setTimeout(r, 600));
      const data = await api.hubFolders().catch(() => null);
      const j = data?.jobs.find((x) => x.id === job.id);
      if (data && !j) return leave();
      if (j?.error) throw Object.assign(new Error(j.error), { retry: j.retry, id: j.id, gone: !data.folders.some((f) => f.slug === slug) });
      if (j?.step) setBusy(`${j.step}…`);
    }
  };

  const finish = async (how = {}) => {
    lastHow.current = how;
    setError(null);
    try {
      // Merged, it shows as such, and is not merged again if the delete is tried again.
      if (merge) {
        setBusy(`Merging into ${info.into}…`);
        await api.hubMerge(slug);
        setMerge(false);
        setInfo(await api.hubFinish(slug));
      }
      await remove(how);
    } catch (e) {
      setBusy("");
      setError({ message: e.message, retry: e.retry, gone: e.gone, id: e.id });
    }
  };

  const i = info;
  const facts = i && [
    !i.branch
      ? ["HEAD is on no branch here, so there is nothing to merge."]
      : i.ahead
        ? [`${plural(i.ahead, "commit")} on ${i.branch} that ${i.into || "the main checkout"} doesn't have:`, "", <FinishCommits key="c" info={i} />]
        : [`Everything on ${i.branch} is in ${i.into}.`],
    i.uncommitted > 0 && [`${plural(i.uncommitted, "file")} not committed, lost when the worktree goes.`, "del"],
    i.unpushed > 0 && [`${plural(i.unpushed, "commit")} not pushed to ${i.upstream}.`],
  ].filter(Boolean);

  let button = [merge ? "Merge and delete" : "Delete worktree", () => finish()];
  const after = i?.main ? i.intoPlace : "the hub";
  if (error?.gone) button = [`Go to ${after}`, () => api.hubDismissJob(error.id).finally(leave)];
  else if (error?.retry === "force") button = ["Delete anyway", () => finish({ force: true })];
  else if (error?.retry === "remove") button = ["Delete without teardown", () => finish({ ...lastHow.current, skipTeardown: true })];

  return (
    <Modal onClose={onClose} centred className="hub-form">
      <div className="viewer-head dialog-head">
        <span className="path">Finish this worktree</span>
        <span className="spacer" />
        <button className="ghost" onClick={onClose}>
          <IconX size={13} />
        </button>
      </div>
      {i && (
        <div className="settings-body hub-form-body">
          <ul className="finish-facts">
            {facts.map(([text, tone, more]) => (
              <li key={text} className={tone}>
                {text}
                {more}
              </li>
            ))}
            {pr && (
              <li>
                Pull request <PRChip pr={pr} /> {pr.title}
              </li>
            )}
          </ul>
          {i.branch && (
            <div className="settings-row">
              <div className="settings-text">
                <div>Merge into {i.into || "the main checkout"} first</div>
                <div className={cx("settings-note", i.mergeBlocked && i.ahead > 0 && "error")}>
                  {i.ahead === 0 ? "Nothing to merge." : i.mergeBlocked ? `Can't: ${i.mergeBlocked}.` : `In ${i.intoPlace}, as git merge does. A conflict leaves it as it was.`}
                </div>
              </div>
              <Switch on={merge} label="Merge first" disabled={!!i.mergeBlocked || !i.ahead || !!busy} onChange={setMerge} />
            </div>
          )}
          {i.branch && (
            <div className="settings-row">
              <div className="settings-text">
                <div>Delete the branch {i.branch}</div>
                <div className="settings-note">Only if it is merged: git keeps a branch with work nothing else has.</div>
              </div>
              <Switch on={dropBranch} label="Delete the branch" disabled={!!busy} onChange={setDropBranch} />
            </div>
          )}
        </div>
      )}
      {error && <div className="hub-picker-error">{error.message}</div>}
      <div className="hub-picker-foot">
        <span className="hub-picker-note">
          {busy ||
            (i &&
              `${i.teardown ? "The teardown hook runs, then the" : "The"} worktree at ${meta?.place} is deleted and you go to ${after}. Its sessions stop.`)}
        </span>
        <button className="primary" disabled={!i || !!busy} onClick={button[1]}>
          {button[0]}
        </button>
      </div>
    </Modal>
  );
}

const DAY = 24 * 60 * 60;
const CLOSE_IDLE = [
  [DAY, "After a day"],
  [7 * DAY, "After a week"],
  [0, "Never"],
];

const SEND_AFTER = [
  [0, "Right away"],
  [30, "30 s"],
  [60, "1 min"],
  [300, "5 min"],
];

const CHAT_WORKTREE = [
  [false, "The folder"],
  [true, "A new worktree"],
];

export const SESSION_MODES = [
  ["", "Usual", "The mode the agent's own settings start in"],
  ["default", "Ask", "Ask before edits"],
  ["acceptEdits", "Edits", "Accept edits"],
  ["plan", "Plan"],
  ["auto", "Auto"],
  ["autoAskEdits", "Auto, ask", "Auto, ask before edits (Codex asks before everything)"],
];

// modesFor is SESSION_MODES less what agent has no way to do: Codex's auto
// review takes every approval, edits too.
export const modesFor = (agent) => (agent === "codex" ? SESSION_MODES.filter(([id]) => id !== "autoAskEdits") : SESSION_MODES);

const onThisComputer = (origin) => /^https?:\/\/(localhost|127\.[\d.]+|\[::1\])(:\d+)?$/.test(origin);

// Telegram sets up the bot dv sends notices through: made with BotFather, its
// token pasted here, then the reader's chat connected by pressing Start in
// it, which this looks for every few seconds until it happens. A dv with no
// Telegram - a trial run - shows nothing.
function Telegram({ settings, onReady }) {
  const [st, setSt] = useState(null);
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState("");
  const [said, setSaid] = useState(null); // { text, bad }
  const load = useCallback(() => api.telegram().then(setSt, () => setSt((s) => s ?? undefined)), []);
  useEffect(() => {
    load();
  }, [load]);
  useEffect(() => {
    if (!st?.connect) return;
    const timer = setInterval(load, 2000);
    return () => clearInterval(timer);
  }, [st?.connect, load]);
  const connected = !!st?.bot && !st.connect;
  useEffect(() => onReady(connected), [connected, onReady]);
  if (!st) return null;

  const act = (what, run, done) => async () => {
    setBusy(what);
    setSaid(null);
    try {
      setSt(await run());
      if (done) setSaid({ text: done });
    } catch (e) {
      setSaid({ text: e.message, bad: true });
    } finally {
      setBusy("");
    }
  };
  const setUp = act("setup", () => api.telegramSetUp(token.trim()).then((s) => (setToken(""), s)));
  const remove = act("remove", () => (confirm(`Stop sending to Telegram, and forget @${st.bot}?`) ? api.telegramRemove() : Promise.resolve(st)));
  const note = (text) => <div className={cx("settings-note", said?.bad && "prompt-error")}>{said?.text || text}</div>;

  if (!st.bot) {
    return (
      <div className="settings-row">
        <div className="settings-text">
          <div>Telegram</div>
          {note(
            <>
              {st.lost && "The token kept for Telegram cannot be read on this computer, so set it up again. "}
              Notices on your phone, with buttons to answer them, and each session in a thread of its own to write to. In Telegram, send /newbot to{" "}
              <a className="link" href="https://t.me/BotFather" target="_blank" rel="noopener">
                @BotFather
              </a>
              , turn on topics for the new bot in BotFather's Mini App (Open, pick the bot, then Bot Settings), and paste the bot's token here.
            </>,
          )}
        </div>
        <input
          type="password"
          value={token}
          placeholder="Bot token"
          autoComplete="off"
          spellCheck={false}
          onChange={(e) => {
            setToken(e.target.value);
            setSaid(null);
          }}
          onKeyDown={(e) => e.key === "Enter" && token.trim() && !busy && setUp()}
        />
        <span className="settings-actions">
          <button className="primary" disabled={!token.trim() || !!busy} onClick={setUp}>
            {busy ? "Checking…" : "Set up"}
          </button>
        </span>
      </div>
    );
  }

  const removeButton = (
    <button className="btn" disabled={!!busy} onClick={remove}>
      Remove
    </button>
  );
  if (st.connect) {
    return (
      <div className="settings-row">
        <div className="settings-text">
          <div>Telegram</div>
          {note(`Open @${st.bot} in Telegram and press Start to connect your chat. This page notices once you have.`)}
        </div>
        <span className="settings-actions">
          <button className="primary" onClick={() => window.open(st.connect, "_blank", "noopener")}>
            Open Telegram
          </button>
          {removeButton}
        </span>
      </div>
    );
  }

  const links =
    st.origin && !onThisComputer(st.origin)
      ? ` Its links open ${st.origin}.`
      : " To have its messages link to dv, send a test from the address your phone opens dv at.";
  return (
    <>
      <div className="settings-row">
        <div className="settings-text">
          <div>Telegram</div>
          {note(`Sending to ${st.chat} through @${st.bot}.${st.sender ? ` The dv at ${st.sender} sends for this computer.` : ""}${links}`)}
          {st.noTopics && <div className="settings-note prompt-error">{st.noTopics}</div>}
        </div>
        <span className="settings-actions">
          <button className="btn" disabled={!!busy} onClick={act("test", api.telegramTest, "Sent. It should be in Telegram now.")}>
            {busy === "test" ? "Sending…" : "Send a test"}
          </button>
          <button
            className="btn"
            disabled={!!busy}
            title="Makes the bot's picture this computer's tab icon, to tell its bot from another's"
            onClick={act(
              "picture",
              async () => api.telegramPicture(await tabIconJPEG(tabMark(document.documentElement.dataset.theme, settings))),
              "Its picture is now the tab icon. Telegram can take a minute to show it.",
            )}
          >
            {busy === "picture" ? "Setting…" : "Use tab icon as picture"}
          </button>
          {removeButton}
        </span>
      </div>
    </>
  );
}

// ChatApps are the apps notices go on to and sessions are talked to from, and
// what goes for all of them once one is set up.
function ChatApps({ settings, onChange }) {
  const [on, setOn] = useState({});
  const ready = useCallback((app, is) => setOn((o) => (o[app] === is ? o : { ...o, [app]: is })), []);
  // A hub's folders, which a session started from a chat can go to; a lone
  // dv has its own alone.
  const [folders, setFolders] = useState(null);
  useEffect(() => {
    api.hubFolders().then((r) => setFolders(r.folders.filter((f) => !f.missing)), () => {});
  }, []);
  return (
    <>
      <Telegram settings={settings} onReady={(is) => ready("telegram", is)} />
      <GoogleChat onReady={(is) => ready("gchat", is)} />
      {(on.telegram || on.gchat) && (
        <>
          <Setting
            label="Send to chat apps after"
            note="How long you go without using dv before what an agent asks, or its finished turn, goes to your chat apps. Answering or looking in dv first keeps it here."
            value={settings.notifyAfter ?? 60}
            onPick={(v) => onChange({ notifyAfter: v === 60 ? undefined : v })}
            choices={SEND_AFTER}
          />
          {folders?.length > 1 && (
            <div className="settings-row">
              <div className="settings-text">
                <div>Folder for new sessions</div>
                <div className="settings-note">Where writing to dv in a chat starts one, unless the message says, as “notes: …” does.</div>
              </div>
              <Picker
                label={folders.find((f) => f.slug === settings.chatFolder)?.name || "Ask each time"}
                choices={[{ id: "", label: "Ask each time" }, ...folders.map((f) => ({ id: f.slug, label: f.name, description: f.place }))]}
                value={settings.chatFolder ?? ""}
                onPick={(v) => onChange({ chatFolder: v || undefined })}
              />
            </div>
          )}
          <Setting
            label="Start them in"
            note="“wt: …” or “here: …” in a message says otherwise."
            value={!!settings.chatWorktree}
            onPick={(v) => onChange({ chatWorktree: v || undefined })}
            choices={CHAT_WORKTREE}
          />
          <Setting
            label="Mode they begin in"
            note="What they ask still comes to you in the chat."
            value={settings.chatMode ?? ""}
            onPick={(v) => onChange({ chatMode: v || undefined })}
            choices={SESSION_MODES}
          />
        </>
      )}
    </>
  );
}

// GoogleChat pairs this computer's dv with a relay your organisation runs
// (dv-gchat-relay), which puts it in Google Chat.
function GoogleChat({ onReady }) {
  const [st, setSt] = useState(null);
  const [address, setAddress] = useState("");
  const [busy, setBusy] = useState("");
  const [said, setSaid] = useState(null); // { text, bad }
  const load = useCallback(() => api.gchat().then(setSt, () => setSt((s) => s ?? undefined)), []);
  useEffect(() => {
    load();
  }, [load]);
  useEffect(() => {
    if (!st?.code) return;
    const timer = setInterval(load, 2000);
    return () => clearInterval(timer);
  }, [st?.code, load]);
  const paired = !!st?.owner;
  useEffect(() => onReady(paired), [paired, onReady]);
  if (!st) return null;

  const act = (what, run, done) => async () => {
    setBusy(what);
    setSaid(null);
    try {
      setSt(await run());
      if (done) setSaid({ text: done });
    } catch (e) {
      setSaid({ text: e.message, bad: true });
    } finally {
      setBusy("");
    }
  };
  const connect = act("connect", () => api.gchatSetUp(address.trim()).then((s) => (setAddress(""), s)));
  const remove = act("remove", () => (confirm("Stop sending to Google Chat, and unpair this computer?") ? api.gchatRemove() : Promise.resolve(st)));
  const note = (text) => <div className={cx("settings-note", said?.bad && "prompt-error")}>{said?.text || text}</div>;
  const removeButton = (
    <button className="btn" disabled={!!busy} onClick={remove}>
      {paired ? "Remove" : "Cancel"}
    </button>
  );

  if (st.code) {
    return (
      <div className="settings-row">
        <div className="settings-text">
          <div>Google Chat</div>
          {note(
            <>
              In Google Chat, send the dv app <code>connect {st.code}</code>. This page notices once you have; the code works for 10 minutes.
            </>,
          )}
        </div>
        <span className="settings-actions">{removeButton}</span>
      </div>
    );
  }
  if (!paired) {
    return (
      <div className="settings-row">
        <div className="settings-text">
          <div>Google Chat</div>
          {note(
            <>
              {st.sealed && "The pairing kept for Google Chat cannot be read on this computer, so connect again. "}
              Notices in Google Chat, with buttons to answer them, and sessions to write to, through a relay your organisation runs
              (dv-gchat-relay). Paste its address here.
            </>,
          )}
        </div>
        <input
          value={address}
          placeholder="https://relay.example.com"
          autoComplete="off"
          spellCheck={false}
          onChange={(e) => {
            setAddress(e.target.value);
            setSaid(null);
          }}
          onKeyDown={(e) => e.key === "Enter" && address.trim() && !busy && connect()}
        />
        <span className="settings-actions">
          <button className="primary" disabled={!address.trim() || !!busy} onClick={connect}>
            {busy ? "Connecting…" : "Connect"}
          </button>
        </span>
      </div>
    );
  }
  const relayHost = st.relay.replace(/^https?:\/\//, "");
  return (
    <div className="settings-row">
      <div className="settings-text">
        <div>Google Chat</div>
        {note(
          `Sending to ${st.owner}${st.email ? ` (${st.email})` : ""} through ${relayHost}.` +
            (st.elsewhere ? " Another dv on this computer is connected, and sends for it." : ""),
        )}
        {st.lost && <div className="settings-note prompt-error">{st.lost}</div>}
      </div>
      <span className="settings-actions">
        <button className="btn" disabled={!!busy} onClick={act("test", api.gchatTest, "Sent. It should be in Google Chat now.")}>
          {busy === "test" ? "Sending…" : "Send a test"}
        </button>
        {removeButton}
      </span>
    </div>
  );
}

// useUpdate is the newer release, if one is out: asked as the page loads and
// on coming back to the tab, of a server that keeps its answer for hours. A
// build of one's own is never checked.
export function useUpdate(off) {
  const [found, setFound] = useState(null);
  useEffect(() => {
    if (off || boot.run.version === "dev") return setFound(null);
    const check = () => document.hidden || api.updateCheck().then((u) => setFound(u.newer ? u : null), () => {});
    check();
    document.addEventListener("visibilitychange", check);
    return () => document.removeEventListener("visibilitychange", check);
  }, [off]);
  useEffect(() => {
    updateHeard.add(setFound);
    return () => updateHeard.delete(setFound);
  }, []);
  return found;
}

// checkUpdate asks GitHub now, whenever it was last asked, and tells each
// useUpdate what it said, checks turned off or not: this one was asked for.
const updateHeard = new Set();
async function checkUpdate() {
  const u = await api.updateCheck(true);
  for (const set of updateHeard) set(u.newer ? u : null);
  return u;
}

// UpdateCheck is the Server tab's say over checking: whether a page does as
// it opens, and a check now, which says what it found. It names the version
// running, which a build of one's own has none of to check.
function UpdateCheck({ settings, onChange }) {
  // A newer release is for the Restart row below to say.
  const [state, setState] = useState(""); // "", "checking", "latest", or what went wrong
  const check = async () => {
    setState("checking");
    try {
      const u = await checkUpdate();
      setState(u.newer ? "" : "latest");
    } catch (e) {
      setState(e.message);
    }
  };
  const version = boot.run.version;
  if (version === "dev") {
    return (
      <div className="settings-row">
        <div className="settings-text">
          <div>Version</div>
          <div className="settings-note">This is a build of your own, so it is not checked for updates.</div>
        </div>
      </div>
    );
  }
  const said = {
    "": ". A page asks GitHub which release is newest as it opens, at most every six hours.",
    checking: ". Asking GitHub…",
    latest: ", the newest release.",
  }[state];
  return (
    <div className="settings-row">
      <div className="settings-text">
        <div>Check for updates</div>
        <div className="settings-note">
          This is v{version}
          {said ?? (
            <>
              . <span className="prompt-error">{state}</span>
            </>
          )}
        </div>
      </div>
      <span className="settings-actions">
        <button className="btn" disabled={state === "checking"} onClick={check}>
          Check now
        </button>
        <Switch on={settings.updateCheck !== false} label="Check for updates" onChange={(v) => onChange({ updateCheck: v ? undefined : false })} />
      </span>
    </div>
  );
}

const RESTART_WAIT_MS = 60000;

// okToRestart asks first when a restart would stop sessions at work, or
// commands in terminals: working is how many sessions dv runs are busy.
export async function okToRestart(working) {
  const running = await api.run().then((r) => r.terminals || 0, () => 0);
  const what = [
    working && (working === 1 ? "a session is still working" : `${working} sessions are still working`),
    running && (running === 1 ? "a terminal is running a command" : `${running} terminals are running commands`),
  ].filter(Boolean);
  if (!what.length) return true;
  const said = what.join(", and ");
  return confirm(`${said[0].toUpperCase()}${said.slice(1)}. Restart dv and stop ${working + running === 1 ? "it" : "them"}?`);
}

// restartDv runs dv again from the binary installed now, or first installs
// version; then it reloads the page once the new run answers, or throws.
export async function restartDv(version) {
  try {
    const was = await api.run();
    sessionStorage.setItem(RESTART_KEY, JSON.stringify(was));
    // The old run can close the connection before its answer is out.
    await (version ? api.update(version) : api.restart()).catch((e) => {
      if (!(e instanceof TypeError)) throw e;
    });
    for (const until = Date.now() + RESTART_WAIT_MS; Date.now() < until; ) {
      await new Promise((r) => setTimeout(r, 400));
      const now = await api.run().catch(() => null);
      if (now && now.started !== was.started) return location.reload();
    }
    throw new Error("dv did not come back within a minute; its terminal says why.");
  } catch (e) {
    sessionStorage.removeItem(RESTART_KEY);
    throw e;
  }
}

// RestartRow restarts dv, or updates it to update, the newer release.
function RestartRow({ working, update }) {
  const [state, setState] = useState(""); // "", "restarting", "updating", or what went wrong
  const go = (install) => async () => {
    if (!(await okToRestart(working))) return;
    setState(install ? "updating" : "restarting");
    restartDv(install && update.latest).catch((e) => setState(e.message));
  };
  const busy = state === "restarting" || state === "updating";
  const failed = state && !busy;
  const note = update ? (
    <>
      v{update.latest} is out.{" "}
      <a className="link" href={`https://github.com/priyanshu-shubham/dv/releases/tag/v${update.latest}`} target="_blank" rel="noopener">
        What is new
      </a>
    </>
  ) : (
    "Runs dv again from the version installed now, in the same terminal. Sessions it runs stop, and carry on at your next message."
  );
  return (
    <div className="settings-row">
      <div className="settings-text">
        <div>Restart</div>
        <div className={cx("settings-note", failed && "prompt-error")}>{failed ? state : note}</div>
      </div>
      <span className="settings-actions">
        {update && (
          <button className="primary" disabled={busy} onClick={go(true)}>
            {state === "updating" ? "Updating…" : "Update and restart"}
          </button>
        )}
        <button className="btn" disabled={busy} onClick={go(false)}>
          {state === "restarting" ? "Restarting…" : "Restart"}
        </button>
      </span>
    </div>
  );
}

// Setting is one preference and its choices, [value, label] pairs, with a
// tooltip third where the label is a picture.
export function Setting({ label, note, value, onPick, choices }) {
  return (
    <div className="settings-row">
      <div className="settings-text">
        <div>{label}</div>
        {note && <div className="settings-note">{note}</div>}
      </div>
      {choices === OFF_ON ? (
        <Switch on={!!value} label={label} onChange={onPick} />
      ) : (
        <span className="seg">
          {choices.map(([v, name, title]) => (
            <button key={String(v)} className={cx(v === value && "on")} aria-pressed={v === value} aria-label={title} title={title} onClick={() => onPick(v)}>
              {name}
            </button>
          ))}
        </span>
      )}
    </div>
  );
}

export function Switch({ on, label, disabled, onChange }) {
  return <button className={cx("switch", on && "on")} role="switch" aria-checked={on} aria-label={label} disabled={disabled} onClick={() => onChange(!on)} />;
}

// Keys reads a shortcut as keycaps: " / " between alternatives, + within a
// chord, and a spoken gesture, as "select code" is, as plain words.
function Keys({ k }) {
  if (/^[a-z]+ [a-z ]+$/.test(k)) return <span className="help-gesture">{k}</span>;
  return (
    <span className="help-keys">
      {k.split(" / ").map((alt, i) => (
        <span key={i} className="help-alt">
          {i > 0 && <span className="help-or">/</span>}
          {alt.split(/(?<=.)\+(?=.)| /).map((cap, j) => (
            <kbd key={j}>{cap}</kbd>
          ))}
        </span>
      ))}
    </span>
  );
}

export function HelpOverlay({ onClose }) {
  const sections = [
    [
      "Anywhere",
      [
        ["Shift+← / Shift+→", "Previous / next mode: Diff, Files, Agent, Terminal (with text selected, they extend it; in a terminal, unless what runs there is full screen or uses the mouse)"],
        ["Ctrl+`", "The Terminal view; from it, back to where you were"],
        [`${modKey}+P`, "Go to file"],
        [`${modKey}+K`, `Search definitions and text (also ${modKey}+Shift+F)`],
        ["Alt+A", `Run an action (also ${modKey}+Shift+P, where the browser allows it)`],
        ["Alt+P", "Your pad, from the message box too; from the pad, back to where you were"],
        [`${modKey}+F`, "Find in the page; Enter and Shift+Enter (or F3) step through matches"],
        [",", "Settings"],
        ...(boot.base
          ? [
              ["Alt+H", "Back to the hub (from the message box too)"],
              [`${modKey}+Shift+↑ / ↓`, "Switch between the hub's open folders, last used first: hold, step, let go (in Diff and Files, Shift alone). A tap of ↑ searches every folder, open or not"],
            ]
          : []),
        ["Esc", "Back a step, or close what is open"],
        ["Shift+Esc", "Close whatever is open"],
      ],
    ],
    [
      "Diff and Files",
      [
        ["/", "Filter the file list"],
        ["[ / ]", "Previous / next file (also Shift+P / Shift+N)"],
        ["n / p", "Next / previous change (one file at a time, on into the next file)"],
        ["f", "View the whole file at the focused line"],
        ["c", "Comment on the focused line"],
        ["a", "Ask about the focused line, or the file, in the Ask panel"],
        ["Shift+A", "Add the focused line, or the file, to your next message to Claude or Codex"],
        ["v", "Mark the current file viewed, in Diff (one file at a time, then on to the next)"],
        ["o", "One file at a time, or every file in one scroll, in Diff"],
        ["u", "Toggle split / unified"],
        ["w", "Toggle line wrapping"],
        ["r", "Reload the diff"],
        [`${modKey}+click`, "Jump to a symbol's definition, or search its uses (double tap on a phone)"],
        ["Alt+Left", "Back to the previous definition, or file in Files"],
        ["Alt+Right", "Forward again, in Files"],
        ["select code", "Comment on the selected lines"],
        ["drag line numbers", "Comment on a range of lines"],
        ["click a gap bar", "Expand 20 more lines of context"],
      ],
    ],
    [
      "Agent",
      [
        ["↑ ↓ Enter 1-9", "Answer what Claude or Codex is asking; Tab adds a note, Esc leaves it under the bell"],
        ["Esc Esc", "Rewind the session to before one of your messages"],
        ["Esc", "Take back a queued message, or one sent a moment ago; otherwise stop the agent"],
        ["[ / ]", "Your previous / next message"],
        [`${modKey}+↓`, "The end of the conversation (from the message box too)"],
        ["↑ / ↓", "In the empty message box, bring back a queued message, or step through what you said"],
        ["Shift+Tab", "In the message box, change the permission mode"],
        ["Shift+↑ / Shift+↓", "The previous / next open session (in the message box, when it is empty; with text selected, they extend it)"],
        ["Alt+N", "A new session (from the message box too)"],
      ],
    ],
    [
      "Terminal",
      [
        ["Shift+↑ / Shift+↓", "The previous / next terminal (in a terminal, unless what runs there is full screen, like vim or less, or uses the mouse, like fzf)"],
        ["Alt+N", "A new terminal"],
        [isMac ? "Cmd+C / Cmd+V" : "Ctrl+C / Ctrl+V", `Copy what is selected${isMac ? "" : " (Ctrl+C with nothing selected interrupts; Ctrl+Shift+C always copies)"} / paste`],
      ],
    ],
  ];
  return (
    <Modal onClose={onClose} centred className="help">
      <div className="help-head">
        <span className="help-title">Keyboard shortcuts</span>
        <span className="spacer" />
        <button className="ghost" onClick={onClose} title="Close (Esc)">
          <IconX size={14} />
        </button>
      </div>
      <div className="help-body">
        {sections.map(([title, keys]) => (
          <section className="help-section" key={title}>
            <div className="settings-heading">{title}</div>
            {keys.map(([k, d], i) => (
              <div className="help-row" key={i}>
                <span>{d}</span>
                <Keys k={k} />
              </div>
            ))}
          </section>
        ))}
      </div>
    </Modal>
  );
}
