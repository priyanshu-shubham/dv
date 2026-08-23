import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api } from "./api.js";
import Header from "./Header.jsx";
import Sidebar from "./Sidebar.jsx";
import FileDiff, { cssId } from "./FileDiff.jsx";
import { FileViewer, HelpOverlay, SearchPanel, SymbolPalette } from "./Overlays.jsx";
import AskPanel from "./AskPanel.jsx";
import { cx, isTyping, usePersisted } from "./util.js";

// Shared empty list so files without comments keep a stable `threads` prop.
const NO_THREADS = [];

export default function App() {
  const [meta, setMeta] = useState(null);
  const [scope, setScope] = usePersisted("scope.v2", { kind: "auto", rev: "" });
  const [diff, setDiff] = useState(null);
  const [threads, setThreads] = useState([]);
  const [error, setError] = useState("");
  const [refreshing, setRefreshing] = useState(false);

  const [view, setView] = usePersisted("view", "split");
  const [contextLines, setContextLines] = usePersisted("context", 3);
  const [wrap, setWrap] = usePersisted("wrap", false);
  const [theme, setTheme] = usePersisted("theme", "dark");

  // Per-file view state. "I've read this" is a claim about one comparison, so
  // it is keyed on the resolved label — which for the auto scope is whatever it
  // settled on, not the word "auto".
  const [viewedList, setViewedList] = usePersisted("viewed:" + scopeKey(scope, diff), []);
  const viewed = useMemo(() => new Set(viewedList), [viewedList]);
  const [collapsed, setCollapsed] = useState(() => new Set());

  const [fileData, setFileData] = useState({}); // path -> { fd, loading, error }
  const [activePath, setActivePath] = useState(null);
  // The line a jump is aiming at. Its block may be virtualised away, so the
  // file is told to keep that one mounted until the jump has landed.
  const [reveal, setReveal] = useState(null);
  const [composing, setComposing] = useState(null); // { path, side, start, end, quote }
  const [overlay, setOverlay] = useState(null);
  const [ask, setAsk] = useState(null); // { file, side, startLine, endLine }

  const hovered = useRef(null); // { path, side, line } under the cursor
  const scrollRef = useRef(null);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
  }, [theme]);

  useEffect(() => {
    api.meta().then(setMeta).catch((e) => setError(e.message));
  }, []);

  const loadThreads = useCallback(() => {
    api.threads().then((r) => setThreads(r.threads || [])).catch(() => {});
  }, []);

  const loadDiff = useCallback(
    async (opts = {}) => {
      setRefreshing(true);
      try {
        const d = await api.diffList(scope);
        setDiff(d);
        setError("");
        // Dropping cached file bodies is what makes the refresh button honest:
        // each visible section refetches against the new working tree.
        setFileData({});
        if (!opts.keepActive) setActivePath(d.files[0]?.path ?? null);
      } catch (e) {
        setError(e.message);
        setDiff(null);
      } finally {
        setRefreshing(false);
      }
    },
    [scope],
  );

  useEffect(() => {
    loadDiff();
    loadThreads();
  }, [loadDiff, loadThreads]);

  const needFile = useCallback(
    (path) => {
      setFileData((prev) => {
        if (prev[path]) return prev;
        api
          .diffFile(scope, path)
          .then((fd) => setFileData((p) => ({ ...p, [path]: { fd } })))
          .catch((e) => setFileData((p) => ({ ...p, [path]: { error: e.message } })));
        return { ...prev, [path]: { loading: true } };
      });
    },
    [scope],
  );

  const files = diff?.files || [];
  const threadsByFile = useMemo(() => {
    const m = new Map();
    for (const t of threads) {
      if (!m.has(t.file)) m.set(t.file, []);
      m.get(t.file).push(t);
    }
    return m;
  }, [threads]);

  // A file the reader has not reached is an empty stub until its diff loads, so
  // the page is shorter than it will be and a jump to something near the end
  // gets clamped part-way. One scroll cannot get this right: the target has to
  // be chased until the growing content stops moving it.
  const jumpToFile = useCallback(
    (path, line) => {
      setActivePath(path);
      if (line) setReveal({ path, line });
      needFile(path); // do not wait for the observer to notice we are heading there
      const root = scrollRef.current;
      const el = document.getElementById("file-" + cssId(path));
      if (!root || !el) return;

      // Every section the reader has not reached is an empty stub until its
      // diff arrives, so the page is far shorter than it will be and a single
      // scroll to something near the end gets clamped part-way. Each correction
      // brings more sections into range, which loads them, which moves the
      // target again - so keep re-aiming until the layout has been quiet for a
      // moment rather than for a fixed number of frames.
      const QUIET_MS = 500;
      const LIMIT_MS = 8000;
      const started = performance.now();
      let lastMove = started;

      // Give up the moment the reader steers themselves. This watches for input
      // rather than for the scroll position changing, because Chrome's scroll
      // anchoring moves the position on its own as the sections above finish
      // loading - which is the very thing being corrected for.
      const INTERRUPTS = ["wheel", "touchstart", "keydown", "mousedown"];
      let live = true;
      const stop = () => {
        live = false;
        for (const e of INTERRUPTS) window.removeEventListener(e, stop, true);
      };
      for (const e of INTERRUPTS) window.addEventListener(e, stop, { capture: true, passive: true });

      const step = () => {
        if (!live) return;
        const anchor = line ? el.querySelector(`[data-line="${line}"]`) : null;
        const node = anchor || el;
        const want = anchor ? root.clientHeight / 3 : 8;
        const off = Math.round(node.getBoundingClientRect().top - root.getBoundingClientRect().top - want);
        const now = performance.now();
        if (off !== 0) {
          root.scrollTo({ top: root.scrollTop + off, behavior: "instant" });
          lastMove = now;
        }
        const settled = now - lastMove > QUIET_MS && (!line || anchor);
        if (settled || now - started > LIMIT_MS) stop();
        else requestAnimationFrame(step);
      };
      requestAnimationFrame(step);
    },
    [needFile],
  );

  // Read the current set through a ref so these callbacks keep one identity for
  // the life of the page; every memoised file section depends on that.
  const viewedRef = useRef(viewed);
  viewedRef.current = viewed;

  const toggleViewed = useCallback(
    (path) => {
      const wasViewed = viewedRef.current.has(path);
      setViewedList((list) => {
        const set = new Set(list);
        if (wasViewed) set.delete(path);
        else set.add(path);
        return [...set];
      });
      // Marking a file viewed folds it away; un-marking opens it back up.
      setCollapsed((c) => {
        const next = new Set(c);
        if (wasViewed) next.delete(path);
        else next.add(path);
        return next;
      });
    },
    [setViewedList],
  );

  const toggleCollapse = useCallback((path) => {
    setCollapsed((c) => {
      const next = new Set(c);
      if (next.has(path)) next.delete(path);
      else next.add(path);
      return next;
    });
  }, []);

  const createComment = useCallback(
    async (payload) => {
      await api.createThread({ ...payload, scope: diff?.scope?.label || scope.kind });
      loadThreads();
    },
    [diff, scope, loadThreads],
  );

  const onThreadAction = useCallback(
    async (action) => {
      const { type, thread } = action;
      if (type === "jump") return jumpToFile(thread.file, thread.endLine);
      if (type === "reply") await api.reply(thread.id, action.body);
      else if (type === "resolve") await api.setResolved(thread.id, action.resolved);
      else if (type === "editComment") await api.editComment(thread.id, action.commentId, action.body);
      else if (type === "deleteComment") {
        const last = thread.comments.length === 1;
        if (last && !confirm("Delete this comment? The thread goes with it.")) return;
        await api.deleteComment(thread.id, action.commentId);
      }
      loadThreads();
    },
    [jumpToFile, loadThreads],
  );

  // Double-clicking an identifier resolves it to definitions, ranked by how
  // close each one is to the file it was clicked in: one opens straight away,
  // several offer a choice, none falls back to a plain text search.
  const onSymbol = useCallback(async (name, from = "") => {
    try {
      const r = await api.resolveSymbol(name, from);
      const defs = r.defs || [];
      if (defs.length === 1) setOverlay({ type: "file", file: defs[0].file, line: defs[0].line });
      else if (defs.length > 1) setOverlay({ type: "palette", query: name, seed: defs, source: r.source });
      else setOverlay({ type: "search", query: name });
    } catch {
      setOverlay({ type: "search", query: name });
    }
  }, []);

  // Asking follows the cursor: the hovered line if there is one, otherwise the
  // file being read, otherwise the comparison as a whole.
  const askHere = useCallback(() => {
    const h = hovered.current;
    if (h) setAsk({ file: h.path, side: h.side, startLine: h.line, endLine: h.line });
    else if (activePath) setAsk({ file: activePath });
    else setAsk({});
  }, [activePath]);

  const startCommentAtCursor = useCallback(() => {
    const h = hovered.current;
    if (!h) return;
    const fd = fileData[h.path]?.fd;
    const src = h.side === "old" ? fd?.oldLines : fd?.newLines;
    setComposing({
      path: h.path,
      side: h.side,
      start: h.line,
      end: h.line,
      quote: src ? [src[h.line - 1]] : [],
    });
  }, [fileData]);

  // Scroll-spy: whichever file is crossing a line 80px below the top of the
  // viewport is the one j/k/v act on.
  //
  // This used to measure every mounted file on each animation frame, which
  // forces a synchronous layout per frame - and with content-visibility on the
  // hunks, that layout is not cheap. An observer watching a one-pixel band gets
  // the same answer off the main thread. The band is expressed in pixels, so it
  // is rebuilt whenever the viewport height changes.
  useEffect(() => {
    const root = scrollRef.current;
    if (!root || files.length === 0) return;

    let io = null;
    const inBand = new Set();
    const build = () => {
      io?.disconnect();
      const h = root.clientHeight;
      if (h < 100) return;
      io = new IntersectionObserver(
        (entries) => {
          for (const e of entries) {
            if (e.isIntersecting) inBand.add(e.target);
            else inBand.delete(e.target);
          }
          if (inBand.size === 0) return;
          // Ties go to the last file to cross, which is the one being read.
          let best = null;
          for (const el of inBand) {
            if (!best || el.offsetTop > best.offsetTop) best = el;
          }
          setActivePath(best.dataset.path);
        },
        { root, rootMargin: `-80px 0px -${h - 81}px 0px`, threshold: 0 },
      );
      for (const el of root.querySelectorAll("section.file")) io.observe(el);
    };

    build();
    const ro = new ResizeObserver(build);
    ro.observe(root);
    return () => {
      ro.disconnect();
      io?.disconnect();
    };
  }, [files.length]);

  useEffect(() => {
    const onKey = (e) => {
      if (isTyping(e.target)) return;
      const mod = e.metaKey || e.ctrlKey;

      if (mod && e.key.toLowerCase() === "k") {
        e.preventDefault();
        setOverlay({ type: "palette" });
        return;
      }
      if (mod && e.shiftKey && e.key.toLowerCase() === "f") {
        e.preventDefault();
        setOverlay({ type: "search" });
        return;
      }
      if (mod || e.altKey) return;

      switch (e.key) {
        case "Escape":
          setOverlay(null);
          setComposing(null);
          setAsk(null);
          break;
        case "?":
          setOverlay({ type: "help" });
          break;
        case "j":
        case "k": {
          e.preventDefault();
          const i = files.findIndex((f) => f.path === activePath);
          const next = e.key === "j" ? Math.min(files.length - 1, i + 1) : Math.max(0, i - 1);
          if (files[next]) jumpToFile(files[next].path);
          break;
        }
        case "n":
        case "p":
          e.preventDefault();
          stepChange(scrollRef.current, e.key === "n" ? 1 : -1);
          break;
        case "c":
          e.preventDefault();
          startCommentAtCursor();
          break;
        case "a":
          e.preventDefault();
          askHere();
          break;
        case "v":
          if (activePath) toggleViewed(activePath);
          break;
        case "u":
          setView(view === "split" ? "unified" : "split");
          break;
        case "w":
          setWrap(!wrap);
          break;
        case "r":
          loadDiff({ keepActive: true });
          loadThreads();
          break;
        default:
          break;
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [
    files, activePath, view, wrap, jumpToFile, toggleViewed, setView, setWrap,
    loadDiff, loadThreads, startCommentAtCursor, askHere,
  ]);

  const onMouseOver = useCallback((e) => {
    const cell = e.target.closest?.("[data-line][data-side]");
    const section = e.target.closest?.("section.file");
    if (cell && section) {
      hovered.current = {
        path: section.dataset.path,
        side: cell.dataset.side,
        line: Number(cell.dataset.line),
      };
    }
  }, []);

  return (
    <div className="app">
      <Header
        meta={meta}
        scope={scope}
        resolvedScope={diff?.scope}
        onScope={setScope}
        view={view}
        onView={setView}
        wrap={wrap}
        onWrap={setWrap}
        contextLines={contextLines}
        onContext={setContextLines}
        theme={theme}
        onTheme={setTheme}
        refreshing={refreshing}
        onRefresh={() => {
          loadDiff({ keepActive: true });
          loadThreads();
        }}
        onPalette={() => setOverlay({ type: "palette" })}
        onSearch={() => setOverlay({ type: "search" })}
        onHelp={() => setOverlay({ type: "help" })}
        onAsk={() => setAsk((a) => (a ? null : { file: activePath || "" }))}
        askOn={!!ask}
      />

      <div className={cx("main", ask && "with-ask")}>
        <Sidebar
          files={files}
          threads={threads}
          activePath={activePath}
          viewed={viewed}
          onSelect={jumpToFile}
          onThreadAction={onThreadAction}
          onJump={(t) => jumpToFile(t.file, t.endLine)}
          commentsPath={meta?.commentsPath || ""}
        />

        <div className="content" ref={scrollRef} onMouseOver={onMouseOver}>
          {error && <div className="banner error">{error}</div>}
          {diff && files.length === 0 && !error && (
            <div className="empty-state">
              <h2>Nothing to review</h2>
              <p>{diff.scope.desc} produced no changes. Pick another scope from the header.</p>
            </div>
          )}
          {files.map((entry) => (
            <FileSection
              key={entry.path}
              entry={entry}
              state={fileData[entry.path]}
              onNeed={needFile}
              view={view}
              contextLines={contextLines}
              wrap={wrap}
              threads={threadsByFile.get(entry.path) || NO_THREADS}
              collapsed={collapsed.has(entry.path)}
              onToggleCollapse={toggleCollapse}
              viewed={viewed.has(entry.path)}
              onToggleViewed={toggleViewed}
              composing={composing?.path === entry.path ? composing : null}
              setComposing={setComposing}
              onComment={createComment}
              onThreadAction={onThreadAction}
              onSymbol={onSymbol}
              onAsk={setAsk}
              isActive={activePath === entry.path}
              reveal={reveal?.path === entry.path ? reveal.line : 0}
            />
          ))}
        </div>

        {ask && (
          <AskPanel
            target={ask}
            scope={scope}
            onClose={() => setAsk(null)}
            onSaveComment={createComment}
          />
        )}
      </div>

      {overlay?.type === "palette" && (
        <SymbolPalette
          initialQuery={overlay.query || ""}
          seed={overlay.seed}
          source={overlay.source}
          from={activePath}
          onClose={() => setOverlay(null)}
          onOpen={(h) => setOverlay({ type: "file", file: h.file, line: h.line })}
        />
      )}
      {overlay?.type === "search" && (
        <SearchPanel
          initialQuery={overlay.query || ""}
          onClose={() => setOverlay(null)}
          onOpen={({ file, line }) => setOverlay({ type: "file", file, line })}
        />
      )}
      {overlay?.type === "file" && (
        <FileViewer
          file={overlay.file}
          line={overlay.line}
          onClose={() => setOverlay(null)}
          onSymbol={onSymbol}
        />
      )}
      {overlay?.type === "help" && <HelpOverlay onClose={() => setOverlay(null)} />}
    </div>
  );
}

// FileSection defers fetching a file's contents until it is nearly on screen.
// Memoised for the same reason FileDiff is: scrolling past file 3 must not
// re-render files 1 through 40.
const FileSection = memo(function FileSection({ entry, state, onNeed, ...rest }) {
  const ref = useRef(null);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const io = new IntersectionObserver(
      ([e]) => {
        if (e.isIntersecting) {
          onNeed(entry.path);
          io.disconnect();
        }
      },
      { rootMargin: "800px 0px" },
    );
    io.observe(el);
    return () => io.disconnect();
  }, [entry.path, onNeed, state]);

  return (
    <div ref={ref}>
      <FileDiff entry={entry} fd={state?.fd} loading={state?.loading} error={state?.error} {...rest} />
    </div>
  );
});

// stepChange scrolls to the next or previous changed row anywhere in the diff.
function stepChange(root, dir) {
  if (!root) return;
  const rows = [...root.querySelectorAll(".row")].filter((r) => r.querySelector(".cell.add, .cell.del"));
  if (!rows.length) return;
  const originTop = root.getBoundingClientRect().top;
  const current = rows.findIndex((r) => r.getBoundingClientRect().top - originTop > 90);
  let target;
  if (dir > 0) target = rows[current === -1 ? rows.length - 1 : current];
  else {
    const before = current === -1 ? rows.length - 1 : current - 1;
    // Walk back past the rest of the hunk we are sitting in, so "previous"
    // lands on the previous change rather than the line above the cursor.
    let i = before - 1;
    while (i > 0 && rows[i - 1] === rows[i].previousElementSibling) i--;
    target = rows[Math.max(0, i)];
  }
  target?.scrollIntoView({ block: "center", behavior: "smooth" });
}

const scopeKey = (s, diff) => diff?.scope?.label || (s.kind === "custom" ? "custom:" + s.rev : s.kind);
