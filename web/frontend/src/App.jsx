import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { flushSync } from "react-dom";
import { api } from "./api.js";
import Header, { AUTO } from "./Header.jsx";
import Sidebar from "./Sidebar.jsx";
import FileDiff, { cssId } from "./FileDiff.jsx";
import CodeView from "./CodeView.jsx";
import { FilePalette, FileViewer, HelpOverlay, SearchPanel, SymbolPalette } from "./Overlays.jsx";
import AskPanel from "./AskPanel.jsx";
import { compareTreePaths, sortTreePaths } from "./tree.js";
import { mapLine, newLineFor } from "./hunks.js";
import { captureAnchor, restoreAnchor, useVersionPoll } from "./live.js";
import { cx, globMatcher, isTyping, modKey, usePersisted } from "./util.js";

// Shared empty list so files without comments keep a stable `threads` prop.
const NO_THREADS = [];
const NO_FILTER = { include: "", exclude: "" };

export default function App() {
  const [meta, setMeta] = useState(null);
  // A picked scope is a pin for this tab; a new session follows the work again.
  const [scope, setScope] = usePersisted("scope", AUTO, { session: true });
  const [diff, setDiff] = useState(null);
  const [threads, setThreads] = useState([]);
  const [error, setError] = useState("");
  const [refreshing, setRefreshing] = useState(false);

  const [view, setView] = usePersisted("view", "split");
  const [contextLines, setContextLines] = usePersisted("context", 3);
  const [wrap, setWrap] = usePersisted("wrap", false);
  const [theme, setTheme] = usePersisted("theme", "dark");
  const [hideGenerated, setHideGenerated] = usePersisted("hideGenerated", false);
  const [pathFilter, setPathFilter] = usePersisted("pathFilter", NO_FILTER);
  // Show all pauses the filters rather than clearing them, so they come back
  // as they were. Editing one, or a new page, turns them back on.
  const [filtersPaused, setFiltersPaused] = useState(false);
  const [sideWidth, setSideWidth] = usePersisted("sidebarWidth", 0); // 0: the stylesheet's default

  // Per-file view state. "I've read this" is a claim about one comparison, so
  // it is keyed on the resolved label — which for the auto scope is whatever it
  // settled on, not the word "auto". The marks live in .dv/viewed.json.
  const viewKey = diff ? scopeKey(scope, diff) : "";
  const [viewedList, setViewedList] = useState([]);
  const viewed = useMemo(() => new Set(viewedList), [viewedList]);
  const [collapsed, setCollapsed] = useState(() => new Set());

  const [fileData, setFileData] = useState({}); // path -> { fd, loading, error }
  const [activePath, setActivePath] = useState(null);
  // The line a jump is aiming at. Its block may be virtualised away, so the
  // file is told to keep that one mounted until the jump has landed.
  const [reveal, setReveal] = useState(null);
  const [composing, setComposing] = useState(null); // { path, side, start, end, quote }
  // The overlay trail. Its last entry is what is on screen; the ones behind it
  // are the definitions the reader walked through to reach it, so a jump that
  // led somewhere unhelpful can be retraced instead of restarted.
  const [stack, setStack] = useState([]);
  const overlay = stack[stack.length - 1] || null;
  const behind = stack[stack.length - 2] || null;
  const [ask, setAsk] = useState(null); // { file, side, startLine, endLine }

  const hovered = useRef(null); // { path, side, line } under the cursor
  const scrollRef = useRef(null);

  // Code mode: the whole repository, one file at a time. Both panes stay
  // mounted and the hidden one keeps its scroll, so switching back and forth
  // costs nothing. codeNav is its trail of files, like an editor's back and
  // forward: entries are { path, line, top, scroll }, scroll noted on leaving.
  const [mode, setMode] = usePersisted("mode", "diff");
  const modeRef = useRef(mode);
  modeRef.current = mode;
  const codeRef = useRef(null);
  const [lastCode, setLastCode] = usePersisted("codePath", "");
  const [codeNav, setCodeNav] = useState(() => ({ stack: lastCode ? [{ path: lastCode }] : [], at: lastCode ? 0 : -1 }));
  const codeAt = codeNav.stack[codeNav.at] || null;
  const codePath = codeAt?.path || "";
  const [plain, setPlain] = useState(null); // { path, fd } or { path, error }: a file outside the diff
  const [repoFiles, setRepoFiles] = useState(null);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
  }, [theme]);

  useEffect(() => {
    api.meta().then(setMeta).catch((e) => setError(e.message));
  }, []);

  // The versions of the comments and viewed marks on screen. Both can change
  // outside this page - another tab, `dv reset`, an agent - and the poll
  // reloads whichever moved.
  const threadsAt = useRef("");
  const viewedAt = useRef("");

  const loadThreads = useCallback(() => {
    api
      .threads()
      .then((r) => {
        threadsAt.current = r.version;
        setThreads(r.threads || []);
      })
      .catch(() => {});
  }, []);

  // Read the current set through a ref so callbacks keep one identity for the
  // life of the page; every memoised file section depends on that.
  const viewedRef = useRef(viewed);
  viewedRef.current = viewed;
  const viewKeyRef = useRef(viewKey);
  viewKeyRef.current = viewKey;
  // Counts marks sent, so a load that one of them overtook is dropped rather
  // than briefly undoing it. The write moves the version; the poll loads again.
  const viewedWrites = useRef(0);

  const loadViewed = useCallback(() => {
    const key = viewKeyRef.current;
    if (!key) return;
    const writes = viewedWrites.current;
    api
      .viewed(key)
      .then((r) => {
        if (key !== viewKeyRef.current || writes !== viewedWrites.current) return;
        viewedAt.current = r.version;
        // Unmarked elsewhere, a file opens back up as it would unmarked here.
        const now = new Set(r.paths);
        const gone = [...viewedRef.current].filter((p) => !now.has(p));
        setViewedList(r.paths);
        if (gone.length)
          setCollapsed((c) => {
            const next = new Set(c);
            for (const p of gone) next.delete(p);
            return next;
          });
      })
      .catch(() => {});
  }, []);

  useEffect(() => {
    setViewedList([]);
    loadViewed();
  }, [viewKey, loadViewed]);

  const onNotes = useCallback(
    (v) => {
      if (v.comments !== threadsAt.current) {
        threadsAt.current = v.comments;
        loadThreads();
      }
      if (v.viewed !== viewedAt.current) {
        viewedAt.current = v.viewed;
        loadViewed();
      }
    },
    [loadThreads, loadViewed],
  );

  // Live updates. versionRef is the repository fingerprint the diff on screen
  // was listed at. loadGen counts full loads, so a fetch one of them overtook
  // is dropped instead of landing on a different comparison.
  const versionRef = useRef("");
  const loadGen = useRef(0);
  const diffRef = useRef(diff);
  diffRef.current = diff;
  const fileDataRef = useRef(fileData);
  fileDataRef.current = fileData;
  const composingRef = useRef(composing);
  composingRef.current = composing;
  const fetchSeq = useRef(new Map()); // path -> latest request, so an older one cannot land last
  const held = useRef(new Map()); // path -> newer diff, waiting for the composer there to close

  // applyInPlace commits an update without moving what the reader is looking
  // at: the line at the top of the view is noted, the update rendered
  // synchronously, and the line put back. `swap` is a file whose diff is being
  // replaced, which that line may have moved within.
  const applyInPlace = useCallback((update, swap) => {
    const root = scrollRef.current;
    const a = captureAnchor(root);
    const follow = a?.line && swap?.path === a.path;
    if (follow) {
      const prev = fileDataRef.current[a.path]?.fd;
      if (prev) Object.assign(a, mapLine(prev, swap.fd, a.side, a.line));
    }
    flushSync(() => {
      // Where the line landed may be a block virtualisation has let go of.
      if (follow) setReveal({ path: a.path, line: a.line });
      update();
    });
    if (a) restoreAnchor(root, a);
  }, []);

  const swapFile = useCallback(
    (path, fd) => applyInPlace(() => setFileData((p) => (p[path] ? { ...p, [path]: { fd } } : p)), { path, fd }),
    [applyInPlace],
  );

  // fetchFile loads one file's diff. A refresh keeps the current one on screen
  // until the new one is in, rather than a loading stub whose height would
  // shift the page, and holds it back from a file that is being commented on:
  // swapping the rows would take the draft with them.
  const fetchFile = useCallback(
    (path, refresh) => {
      const gen = loadGen.current;
      const seq = (fetchSeq.current.get(path) || 0) + 1;
      fetchSeq.current.set(path, seq);
      const current = () => gen === loadGen.current && fetchSeq.current.get(path) === seq;
      api
        .diffFile(scope, path)
        .then((fd) => {
          if (!current()) return;
          if (refresh) {
            if (composingRef.current?.path === path) held.current.set(path, fd);
            else swapFile(path, fd);
            return;
          }
          setFileData((p) => ({ ...p, [path]: { fd } }));
          // The listing may have moved on while this was in flight.
          const e = diffRef.current?.files.find((f) => f.path === path);
          if (e && e.rev !== fd.rev) fetchFile(path, true);
        })
        .catch((e) => {
          if (current() && !refresh) setFileData((p) => ({ ...p, [path]: { error: e.message } }));
        });
    },
    [scope, swapFile],
  );

  const loadDiff = useCallback(
    async (opts = {}) => {
      const gen = ++loadGen.current;
      held.current.clear();
      setRefreshing(true);
      try {
        const d = await api.diffList(scope);
        if (gen !== loadGen.current) return;
        d.files?.sort((a, b) => compareTreePaths(a.path, b.path));
        versionRef.current = d.version;
        setDiff(d);
        setError("");
        // Dropping cached file bodies is what makes the refresh button honest:
        // each visible section refetches against the new working tree.
        setFileData({});
        if (!opts.keepActive) setActivePath(d.files[0]?.path ?? null);
      } catch (e) {
        if (gen !== loadGen.current) return;
        setError(e.message);
        setDiff(null);
      } finally {
        if (gen === loadGen.current) setRefreshing(false);
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
        fetchFile(path, false);
        return { ...prev, [path]: { loading: true } };
      });
    },
    [fetchFile],
  );

  // liveUpdate folds the repository's current state into the page. Entries
  // that did not change keep their identity, so their sections skip the
  // render; files whose content moved are refetched and swapped in place.
  const liveUpdate = useCallback(async () => {
    const was = diffRef.current;
    if (!was) return loadDiff({ keepActive: true });
    const gen = loadGen.current;
    const d = await api.diffList(scope);
    if (gen !== loadGen.current) return;
    d.files?.sort((a, b) => compareTreePaths(a.path, b.path));
    versionRef.current = d.version;

    const before = new Map(was.files.map((f) => [f.path, f]));
    const files = (d.files || []).map((f) => {
      const b = before.get(f.path);
      return b && JSON.stringify(b) === JSON.stringify(f) ? b : f;
    });
    const unchanged =
      files.length === was.files.length &&
      files.every((f, i) => f === was.files[i]) &&
      JSON.stringify([d.scope, d.head]) === JSON.stringify([was.scope, was.head]);
    if (unchanged) return;

    const byPath = new Map(files.map((f) => [f.path, f]));
    applyInPlace(() => {
      setDiff({ ...d, files });
      setMeta((m) => (m ? { ...m, head: d.head } : m));
      // A file gone from the diff drops its body; one that failed gets another try.
      setFileData((p) => Object.fromEntries(Object.entries(p).filter(([path, st]) => byPath.has(path) && !st.error)));
    });
    for (const [path, st] of Object.entries(fileDataRef.current)) {
      if (st.fd && st.fd.rev !== byPath.get(path)?.rev) fetchFile(path, true);
    }
  }, [scope, loadDiff, applyInPlace, fetchFile]);

  useVersionPoll(versionRef, liveUpdate, onNotes);

  // A file that changed while a comment was being written there catches up
  // once the composer closes. Deferred a tick, because the swap renders
  // synchronously and React will not do that from inside an effect.
  useEffect(() => {
    for (const [path, fd] of held.current) {
      if (composing?.path === path) continue;
      held.current.delete(path);
      setTimeout(() => swapFile(path, fd));
    }
  }, [composing, swapFile]);

  const generatedCount = useMemo(() => (diff?.files || []).filter((f) => f.generated).length, [diff]);
  const { files, filteredOut, hiddenGenerated } = useMemo(() => {
    const include = globMatcher(pathFilter.include);
    const exclude = globMatcher(pathFilter.exclude);
    let filteredOut = 0;
    let hiddenGenerated = 0;
    // Paused, the counts are still of what the filters would hide.
    const files = (diff?.files || []).filter((f) => {
      if (hideGenerated && f.generated) {
        hiddenGenerated++;
        return filtersPaused;
      }
      const keep = (!include || include(f.path)) && !exclude?.(f.path);
      if (!keep) filteredOut++;
      return keep || filtersPaused;
    });
    return { files, filteredOut, hiddenGenerated };
  }, [diff, hideGenerated, pathFilter, filtersPaused]);
  const showAll = useCallback(() => setFiltersPaused(true), []);
  const changeHideGenerated = useCallback(
    (v) => {
      setFiltersPaused(false);
      setHideGenerated(v);
    },
    [setHideGenerated],
  );
  const changePathFilter = useCallback(
    (f) => {
      setFiltersPaused(false);
      setPathFilter(f);
    },
    [setPathFilter],
  );
  const threadsByFile = useMemo(() => {
    const m = new Map();
    for (const t of threads) {
      if (!m.has(t.file)) m.set(t.file, []);
      m.get(t.file).push(t);
    }
    return m;
  }, [threads]);

  // jumpToFile brings a file, or a line in it, into the diff: the line a third
  // of the way down, a file where its header pins.
  const jumpToFile = useCallback(
    (path, line) => {
      setActivePath(path);
      if (line) setReveal({ path, line });
      needFile(path); // do not wait for the observer to notice we are heading there
      const root = scrollRef.current;
      const el = document.getElementById("file-" + cssId(path));
      if (!root || !el) return;
      chase(root, () => {
        const anchor = line ? el.querySelector(`[data-line="${line}"]`) : null;
        return { y: yOf(root, anchor || el, anchor ? root.clientHeight / 3 : FILE_TOP), final: !line || !!anchor };
      });
    },
    [needFile],
  );

  // openCode shows a file in Code mode as a new step in its trail, with `line`
  // brought to `top` px down (a third of the way when not given). `replace`
  // moves the current step instead when it is on the same file.
  const openCode = useCallback((path, line = 0, { top, replace } = {}) => {
    const root = codeRef.current;
    const y = modeRef.current === "code" && root ? root.scrollTop : undefined;
    setCodeNav((nav) => {
      const { stack, at } = nav;
      const cur = stack[at];
      if (cur?.path === path && (replace || cur.line === line)) {
        if (!replace) return nav; // already there
        return { stack: [...stack.slice(0, at), { path, line, top }, ...stack.slice(at + 1)], at };
      }
      const kept = stack.slice(0, at + 1);
      if (cur) kept[at] = { ...cur, scroll: y ?? cur.scroll };
      // A jump marks the line it lands on; a mode switch only keeps the place.
      const trail = [...kept, { path, line, top, hit: replace ? 0 : line }].slice(-50);
      return { stack: trail, at: trail.length - 1 };
    });
  }, []);

  const stepCode = useCallback((d) => {
    const y = codeRef.current?.scrollTop;
    setCodeNav(({ stack, at }) => {
      if (!stack[at + d]) return { stack, at };
      const trail = [...stack];
      trail[at] = { ...trail[at], scroll: y };
      trail[at + d] = { ...trail[at + d] }; // a new identity, so the view goes back to it
      return { stack: trail, at: at + d };
    });
  }, []);

  useEffect(() => setLastCode(codePath), [codePath, setLastCode]);

  // Arriving at a step: back to where it was left, or to its line.
  useEffect(() => {
    const root = codeRef.current;
    if (!root || !codeAt || modeRef.current !== "code") return;
    const { path, line, top, scroll } = codeAt;
    if (scroll != null) return chase(root, () => ({ y: scroll, final: !!root.querySelector(".code-file .row") }));
    if (!line) {
      root.scrollTo({ top: 0, behavior: "instant" });
      return;
    }
    setReveal({ path, line });
    return chase(root, () => {
      const cell = root.querySelector(`.code-file [data-line="${line}"]`);
      return cell && { y: yOf(root, cell, top ?? root.clientHeight / 3), final: true };
    });
  }, [codeAt]);

  // The explorer is what the new side of the comparison holds - plus what the
  // diff deletes, which is still part of the review - and follows live updates.
  useEffect(() => {
    if (mode !== "code") return;
    let live = true;
    api.tree(scope).then((r) => live && setRepoFiles(r.files)).catch(() => {});
    return () => {
      live = false;
    };
  }, [mode, scope, diff]);
  const explorer = useMemo(() => {
    if (!repoFiles) return [];
    const changed = new Map((diff?.files || []).map((f) => [f.path, f]));
    const paths = new Set(repoFiles);
    for (const p of changed.keys()) paths.add(p);
    return sortTreePaths([...paths]).map((p) => changed.get(p) || { path: p });
  }, [repoFiles, diff]);

  // A changed file comes from the diff, which knows what changed in it; any
  // other is read off the same side of the comparison.
  const codeEntry = useMemo(() => diff?.files.find((f) => f.path === codePath) || null, [diff, codePath]);
  useEffect(() => {
    if (mode !== "code" || !codePath) return;
    if (codeEntry) {
      needFile(codePath);
      return;
    }
    let live = true;
    api
      .file(codePath, scope, "new")
      .then((r) => live && setPlain({ path: codePath, fd: plainDiff(r) }))
      .catch((e) => live && setPlain({ path: codePath, error: e.message }));
    return () => {
      live = false;
    };
  }, [mode, codePath, codeEntry, scope, diff, needFile]);
  const codeState = codeEntry ? fileData[codePath] : plain?.path === codePath ? plain : null;

  // Switching mode keeps the reader's place: the line at the top of one view is
  // put at the same height in the other, when the other has it.
  const pendingDiff = useRef(null);
  const switchMode = useCallback(
    (next) => {
      const from = modeRef.current;
      if (next === from) return;
      const a = captureAnchor(from === "diff" ? scrollRef.current : codeRef.current);
      if (next === "code") {
        const path = a?.path || activePath;
        if (path) {
          const fd = fileDataRef.current[path]?.fd;
          const side = diffRef.current?.files.find((f) => f.path === path)?.status === "D" ? "old" : "new";
          const line = !a?.line ? 0 : a.side === side || !fd ? a.line : newLineFor(fd, a.line);
          openCode(path, line, { top: a?.line ? a.top : undefined, replace: true });
        }
      } else {
        const path = a?.path || codePath;
        if (diffRef.current?.files.some((f) => f.path === path)) pendingDiff.current = a?.line ? a : { path };
      }
      setMode(next);
    },
    [activePath, codePath, openCode, setMode],
  );
  useEffect(() => {
    if (mode !== "diff") return;
    const a = pendingDiff.current;
    pendingDiff.current = null;
    const root = scrollRef.current;
    const el = a && document.getElementById("file-" + cssId(a.path));
    if (!root || !el) return;
    setActivePath(a.path);
    needFile(a.path);
    if (a.line) setReveal({ path: a.path, line: a.line });
    return chase(root, () => {
      const cell = a.line && el.querySelector(`[data-side="${a.side}"][data-line="${a.line}"]`);
      // A line folded into a gap has no row; the top of its file is the nearest place.
      if (cell) return { y: yOf(root, cell, a.top), final: true };
      return { y: yOf(root, el, FILE_TOP), final: !a.line || !el.dataset.pending };
    });
  }, [mode, needFile]);

  const collapsedRef = useRef(collapsed);
  collapsedRef.current = collapsed;

  // Folding a file that has been scrolled into keeps its header where it was
  // pinned. Left alone, the page keeps its offset, and with the file's rows
  // gone that offset lands partway into the next file.
  const setFolded = useCallback((path, fold) => {
    const apply = () =>
      setCollapsed((c) => {
        const next = new Set(c);
        if (fold) next.add(path);
        else next.delete(path);
        return next;
      });
    const root = scrollRef.current;
    const el = document.getElementById("file-" + cssId(path));
    if (!fold || !root || !el || el.getBoundingClientRect().top >= root.getBoundingClientRect().top) return apply();
    flushSync(apply);
    const y = root.scrollTop + el.getBoundingClientRect().top - root.getBoundingClientRect().top - FILE_TOP;
    root.scrollTo({ top: y, behavior: "instant" });
  }, []);

  const toggleViewed = useCallback(
    (path) => {
      const wasViewed = viewedRef.current.has(path);
      setViewedList((list) => (wasViewed ? list.filter((p) => p !== path) : [...list, path]));
      viewedWrites.current++;
      api.markViewed(viewKeyRef.current, [path], !wasViewed).catch(loadViewed);
      // Marking a file viewed folds it away; un-marking opens it back up.
      setFolded(path, !wasViewed);
    },
    [setFolded, loadViewed],
  );

  const resetReview = useCallback(async () => {
    const n = threads.length;
    const what = n ? `${n === 1 ? "the comment thread" : `all ${n} comment threads`} and every viewed mark` : "every viewed mark";
    if (!confirm(`Start the review over? This deletes ${what}, in every comparison, and can't be undone.`)) return;
    try {
      await api.reset();
    } catch (e) {
      setError(e.message);
      return;
    }
    loadThreads();
    loadViewed();
  }, [threads, loadThreads, loadViewed]);

  const toggleCollapse = useCallback((path) => setFolded(path, !collapsedRef.current.has(path)), [setFolded]);

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
      if (type === "jump") {
        // A comment on a file outside the diff can only be shown in Code mode.
        const entry = diffRef.current?.files.find((f) => f.path === thread.file);
        if (modeRef.current === "diff" && entry) return jumpToFile(thread.file, thread.endLine);
        // Code shows the new side, where a removed line stands at its replacement.
        const fd = fileDataRef.current[thread.file]?.fd;
        const moved = thread.side === "old" && entry?.status !== "D" && fd;
        setMode("code");
        return openCode(thread.file, moved ? newLineFor(fd, thread.endLine) : thread.endLine);
      }
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
    [jumpToFile, loadThreads, openCode, setMode],
  );

  // Opening an overlay from the diff starts a fresh trail; following a symbol
  // out of one extends it. `left` is noted on the overlay being covered - where
  // it was scrolled to, what had been typed - so stepping back returns to it as
  // it was rather than to the top of the definition they arrived at.
  const openOverlay = useCallback((o) => setStack([o]), []);
  const pushOverlay = useCallback((o, left) => {
    setStack((s) => {
      const trail = left && s.length ? [...s.slice(0, -1), { ...s[s.length - 1], ...left }] : s;
      return [...trail, o];
    });
  }, []);
  const goBack = useCallback(() => setStack((s) => s.slice(0, -1)), []);
  const closeOverlay = useCallback(() => setStack([]), []);

  // Where a jump lands: in place in Code mode, and in a viewer over the diff.
  const goTo = useCallback(
    (file, line, left) => {
      if (modeRef.current !== "code") return pushOverlay({ type: "file", file, line }, left);
      setStack([]);
      openCode(file, line);
    },
    [pushOverlay, openCode],
  );

  // Double-clicking an identifier resolves it to definitions, ranked by how
  // close each one is to the file it was clicked in: one opens straight away,
  // several offer a choice, none falls back to a plain text search.
  const onSymbol = useCallback(
    async (name, from = "", leftAt = 0) => {
      const left = leftAt ? { scroll: leftAt } : null;
      try {
        const r = await api.resolveSymbol(name, from);
        const defs = r.defs || [];
        if (defs.length === 1) goTo(defs[0].file, defs[0].line, left);
        else if (defs.length > 1)
          pushOverlay({ type: "palette", query: name, seed: defs, source: r.source, from }, left);
        else pushOverlay({ type: "search", query: name }, left);
      } catch {
        pushOverlay({ type: "search", query: name }, left);
      }
    },
    [pushOverlay, goTo],
  );

  // viewFile opens the whole file at the line being read, off the side of the
  // comparison the diff shows so the line numbers agree. A deleted file only
  // has its old side; anywhere else an old-side line is taken to where it
  // stands in the new file.
  const viewFile = useCallback(
    (path) => {
      const entry = diffRef.current?.files.find((f) => f.path === path);
      if (!entry) return;
      const side = entry.status === "D" ? "old" : "new";
      const at = lineOnScreen(scrollRef.current, path);
      const fd = fileDataRef.current[path]?.fd;
      const line = !at ? 0 : at.side === side || !fd ? at.line : newLineFor(fd, at.line);
      openOverlay({ type: "file", file: path, line, side });
    },
    [openOverlay],
  );

  // Cmd+P reads files from the working tree, like every other jump; only a file
  // this diff deletes is shown from before, having nothing on disk to show.
  const openFromPalette = useCallback(
    (path, query) => {
      if (modeRef.current === "code") {
        setStack([]);
        return openCode(path);
      }
      const deleted = diffRef.current?.files.some((f) => f.path === path && f.status === "D");
      pushOverlay({ type: "file", file: path, side: deleted ? "old" : undefined }, { query });
    },
    [pushOverlay, openCode],
  );

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
    const fd = fileData[h.path]?.fd || (plain?.path === h.path ? plain.fd : null);
    const src = h.side === "old" ? fd?.oldLines : fd?.newLines;
    setComposing({
      path: h.path,
      side: h.side,
      start: h.line,
      end: h.line,
      quote: src ? [src[h.line - 1]] : [],
    });
  }, [fileData, plain]);

  // Scroll-spy: whichever file is crossing a line 12px below the top of the
  // viewport - the one whose header is pinned there - is the one file steps and
  // `v` act on. The line sits just under where a jump puts a file's top. It
  // used to be at 80px, and a jump to a file shorter than that (a folded one is
  // only its header) made the file after it active, so the next step skipped one.
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
        { root, rootMargin: `-12px 0px -${h - 13}px 0px`, threshold: 0 },
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
        openOverlay({ type: "palette" });
        return;
      }
      if (mod && !e.shiftKey && e.key.toLowerCase() === "p") {
        e.preventDefault(); // the browser's Print
        openOverlay({ type: "files" });
        return;
      }
      if (mod && e.shiftKey && e.key.toLowerCase() === "f") {
        e.preventDefault();
        openOverlay({ type: "search" });
        return;
      }
      const code = mode === "code";
      if (code && e.altKey && !mod && (e.key === "ArrowLeft" || e.key === "ArrowRight")) {
        e.preventDefault(); // otherwise the browser's own Back and Forward
        stepCode(e.key === "ArrowLeft" ? -1 : 1);
        return;
      }
      if (mod || e.altKey) return;

      // A step between files goes through the changed ones. In Code mode it
      // starts from the open file, which need not be one of them.
      const stepFile = (dir) => {
        if (!code) {
          const i = files.findIndex((f) => f.path === activePath);
          const next = dir > 0 ? Math.min(files.length - 1, i + 1) : Math.max(0, i - 1);
          if (files[next]) jumpToFile(files[next].path);
          return;
        }
        const next =
          dir > 0
            ? files.find((f) => !codePath || compareTreePaths(f.path, codePath) > 0)
            : files.findLast((f) => !codePath || compareTreePaths(f.path, codePath) < 0);
        if (next) openCode(next.path);
      };

      switch (e.key) {
        case "Escape":
          closeOverlay();
          setComposing(null);
          setAsk(null);
          break;
        case "?":
          openOverlay({ type: "help" });
          break;
        case "[":
        case "]":
          e.preventDefault();
          stepFile(e.key === "]" ? 1 : -1);
          break;
        case "n":
        case "N":
        case "p":
        case "P": {
          e.preventDefault();
          const dir = e.key.toLowerCase() === "n" ? 1 : -1;
          // Shift makes it a whole file. Read off shiftKey rather than the
          // letter's case, which caps lock flips.
          if (e.shiftKey) stepFile(dir);
          else stepChange(code ? codeRef.current : scrollRef.current, dir, needFile);
          break;
        }
        case "m":
          switchMode(code ? "diff" : "code");
          break;
        case "c":
          e.preventDefault();
          startCommentAtCursor();
          break;
        case "a":
          e.preventDefault();
          askHere();
          break;
        case "f": {
          if (code) break; // Code mode is already the whole file
          // The file under the pointer, if it is over a line; else the one being read.
          const cell = scrollRef.current?.querySelector("[data-line][data-side]:hover");
          const path = cell?.closest("section.file")?.dataset.path || activePath;
          if (path) viewFile(path);
          break;
        }
        case "v":
          // Viewed is a mark on the diff; Code mode reads files, it does not review them.
          if (!code && activePath) toggleViewed(activePath);
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
    loadDiff, loadThreads, startCommentAtCursor, askHere, openOverlay, closeOverlay, needFile, viewFile,
    mode, codePath, openCode, stepCode, switchMode,
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
        mode={mode}
        onMode={switchMode}
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
        onPalette={() => openOverlay({ type: "palette" })}
        onSearch={() => openOverlay({ type: "search" })}
        onHelp={() => openOverlay({ type: "help" })}
        onAsk={() => setAsk((a) => (a ? null : { file: (mode === "code" ? codePath : activePath) || "" }))}
        askOn={!!ask}
      />

      <div className={cx("main", ask && "with-ask")} style={sideWidth ? { "--side-w": sideWidth + "px" } : undefined}>
        <Sidebar
          mode={mode}
          files={mode === "code" ? explorer : files}
          threads={threads}
          activePath={mode === "code" ? codePath : activePath}
          viewed={viewed}
          onSelect={mode === "code" ? openCode : jumpToFile}
          onThreadAction={onThreadAction}
          onJump={(thread) => onThreadAction({ type: "jump", thread })}
          commentsPath={meta?.commentsPath || ""}
          generatedCount={generatedCount}
          hideGenerated={hideGenerated}
          onHideGenerated={changeHideGenerated}
          pathFilter={pathFilter}
          onPathFilter={changePathFilter}
          filteredOut={filteredOut}
          hiddenGenerated={hiddenGenerated}
          filtersPaused={filtersPaused}
          onPauseFilters={setFiltersPaused}
          onReset={resetReview}
          onWidth={setSideWidth}
        />

        <div className="content" ref={scrollRef} onMouseOver={onMouseOver} hidden={mode !== "diff"}>
          {error && <div className="banner error">{error}</div>}
          {diff && files.length === 0 && !error && (
            <div className="empty-state">
              <h2>Nothing to review</h2>
              {filteredOut + hiddenGenerated > 0 ? (
                <p>
                  {filteredOut > 0
                    ? "No changed file gets past the sidebar's filter."
                    : "Every change here is to a generated file, and the filter hides those."}{" "}
                  <button className="link" onClick={showAll}>
                    Show all
                  </button>
                </p>
              ) : (
                <p>{diff.scope.desc} produced no changes. Pick another scope from the header.</p>
              )}
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
              composing={mode === "diff" && composing?.path === entry.path ? composing : null}
              setComposing={setComposing}
              onComment={createComment}
              onThreadAction={onThreadAction}
              onSymbol={onSymbol}
              onAsk={setAsk}
              onView={viewFile}
              isActive={activePath === entry.path}
              reveal={reveal?.path === entry.path ? reveal.line : 0}
            />
          ))}
        </div>

        <div className="content code-pane" ref={codeRef} onMouseOver={onMouseOver} hidden={mode !== "code"}>
          {codePath ? (
            <CodeView
              path={codePath}
              entry={codeEntry}
              fd={codeState?.fd}
              error={codeState?.error}
              at={(codeEntry?.status === "D" ? diff?.scope?.oldAt : diff?.scope?.newAt) || ""}
              threads={threadsByFile.get(codePath) || NO_THREADS}
              wrap={wrap}
              reveal={reveal?.path === codePath ? reveal.line : 0}
              hit={codeAt?.hit || 0}
              composing={mode === "code" && composing?.path === codePath ? composing : null}
              setComposing={setComposing}
              onComment={createComment}
              onThreadAction={onThreadAction}
              onSymbol={onSymbol}
              onAsk={setAsk}
              onDiff={() => switchMode("diff")}
              onBack={codeNav.at > 0 ? () => stepCode(-1) : null}
              onForward={codeNav.at < codeNav.stack.length - 1 ? () => stepCode(1) : null}
            />
          ) : (
            <div className="empty-state">
              <h2>Pick a file</h2>
              <p>The explorer lists every file in the repository, with changed ones marked.</p>
              <span className="keys">
                <span>
                  <kbd>{modKey}+P</kbd> open a file
                </span>
                <span>
                  <kbd>[</kbd> <kbd>]</kbd> next changed file
                </span>
              </span>
            </div>
          )}
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
          from={overlay.from || activePath}
          onClose={closeOverlay}
          onBack={behind && goBack}
          backTo={trailLabel(behind)}
          onOpen={(h) => goTo(h.file, h.line)}
        />
      )}
      {overlay?.type === "files" && (
        <FilePalette
          initialQuery={overlay.query || ""}
          changed={files}
          onClose={closeOverlay}
          onBack={behind && goBack}
          backTo={trailLabel(behind)}
          onOpen={openFromPalette}
        />
      )}
      {overlay?.type === "search" && (
        <SearchPanel
          initialQuery={overlay.query || ""}
          onClose={closeOverlay}
          onBack={behind && goBack}
          backTo={trailLabel(behind)}
          onOpen={({ file, line }) => goTo(file, line)}
        />
      )}
      {overlay?.type === "file" && (
        <FileViewer
          file={overlay.file}
          line={overlay.line}
          side={overlay.side}
          scope={scope}
          scroll={overlay.scroll || 0}
          onClose={closeOverlay}
          onBack={behind && goBack}
          backTo={trailLabel(behind)}
          onSymbol={onSymbol}
        />
      )}
      {overlay?.type === "help" && <HelpOverlay onClose={closeOverlay} />}
    </div>
  );
}

// FileSection defers fetching a file's contents until it is nearly on screen.
// Memoised for the same reason FileDiff is: scrolling past file 3 must not
// re-render files 1 through 40.
const FileSection = memo(function FileSection({ entry, state, onNeed, ...rest }) {
  const ref = useRef(null);
  // A generated file's diff is not fetched at all until it is asked for, so a
  // branch that regenerates a lock file costs nothing to review.
  const [shown, setShown] = useState(false);
  const hidden = !!entry.generated && !shown;
  const onShowGenerated = useCallback(() => setShown(true), []);

  useEffect(() => {
    const el = ref.current;
    if (!el || hidden) return;
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
  }, [entry.path, onNeed, state, hidden]);

  return (
    <div ref={ref}>
      <FileDiff
        entry={entry}
        fd={state?.fd}
        loading={state?.loading}
        error={state?.error}
        generatedHidden={hidden}
        onShowGenerated={onShowGenerated}
        {...rest}
      />
    </div>
  );
});

// A diff's changed rows, and Code mode's marked ones.
const CHANGED = ".row:has(> .cell.add, > .cell.del, > .cell.mk)";
const UNRENDERED = ".block[data-changes]:empty, section.file[data-pending]";

// stepChange glides the next or previous run of changed lines up to the
// reading line: just under the file's pinned header, with three lines of
// context above. The reader is taken to be at that line - where a step lands -
// so each press moves on from the change the last one brought there. (It once
// measured from one place and landed at another, so `n` kept finding the change
// it had just shown and never moved.)
function stepChange(root, dir, need) {
  if (!root) return;
  const lead = (root.querySelector(".file-head")?.offsetHeight ?? 40) + 60;
  const line = () => root.getBoundingClientRect().top + lead;
  // Every place a step can land, in page order: the first row of each rendered
  // run of changes, and - standing in for runs not rendered yet - blocks that
  // virtualisation let go of and files still to load. Keeping them in one list
  // is what stops a step skipping a change just because it is off in a spacer.
  const stops = [...root.querySelectorAll(`.block[data-changes] > ${CHANGED}, ${UNRENDERED}`)].filter(
    (el) => !el.matches(CHANGED) || !el.previousElementSibling?.matches(CHANGED),
  );
  // Mid-glide, or still where the last step left the page, a press steps on
  // from that stop in page order. Positions would also do there, except where
  // the page ends before a change can reach the line.
  const here = glide.raf || Math.abs(root.scrollTop - glide.at) < 3 ? glide.row : null;
  const i = here ? stops.indexOf(here) : -1;
  const y = line();
  const stop =
    i >= 0
      ? stops[i + dir]
      : dir > 0
        ? stops.find((el) => el.getBoundingClientRect().top > y + 3)
        : stops.findLast((el) => el.getBoundingClientRect().bottom < y - 3);
  if (!stop) return;

  const aim = (row) => ({ y: root.scrollTop + row.getBoundingClientRect().top - line(), final: true });
  const go = (row) => {
    glide.row = row;
    glideTo(root, () => row.isConnected && aim(row));
  };
  if (stop.matches(CHANGED)) return go(stop);

  // Head for the unrendered place, and switch to its change the moment the
  // rows exist; the glide bends onto it without slowing. Fetching the file now
  // rather than when the scroll brings it near usually has it in first.
  if (stop.matches("section")) need(stop.dataset.path);
  glide.row = stop;
  const until = performance.now() + 4000;
  glideTo(root, () => {
    const rows = [...stop.querySelectorAll(`.block[data-changes] > ${CHANGED}`)].filter(
      (r) => !r.previousElementSibling?.matches(CHANGED),
    );
    const found = dir > 0 ? rows[0] : rows[rows.length - 1];
    if (found) {
      go(found);
      return aim(found);
    }
    if (performance.now() > until) return null;
    const r = stop.getBoundingClientRect();
    return { y: root.scrollTop + (dir > 0 ? r.top : r.bottom) - line(), final: false };
  });
}

// glideTo carries the diff towards whatever `aim` returns, reading it afresh
// each frame, so a destination that moves while content loads - or a new step
// taken mid-flight - bends the motion instead of restarting it. The motion is a
// critically damped spring: it eases in and out and keeps its speed through a
// change of target, which the browser's own smooth scroll does not; retargeting
// that one stalls it for a frame and starts over. `aim` returns { y, final }, or
// nothing to stop; a glide only ends once it has settled on a final target.
const glide = { raf: 0, row: null, at: -1, aim: null, y: 0, v: 0, t: 0 };
const SPRING = 170;
const RAMP = 45000; // px/s², about Chrome's own ease-in
const TAKEOVER = ["wheel", "touchstart", "mousedown", "keydown"];

function glideTo(root, aim) {
  glide.aim = aim;
  if (glide.raf) return;
  glide.y = root.scrollTop;
  glide.v = 0;
  glide.t = performance.now();
  const still = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  for (const e of TAKEOVER) window.addEventListener(e, takeover, { capture: true, passive: true });

  const frame = (now) => {
    const a = glide.aim();
    if (!a) return stopGlide();
    const to = Math.min(root.scrollHeight - root.clientHeight, Math.max(0, a.y));
    // Something else moved the page - scroll anchoring, as content above
    // loads - so carry on from where it actually is.
    if (Math.abs(root.scrollTop - glide.y) > 2) glide.y = root.scrollTop;
    // At most a frame and a bit: a frame that ran long (mounting and
    // highlighting a big block) should slow the glide a touch, not be made up
    // for with a visible leap.
    const dt = Math.max(0, Math.min(0.02, (now - glide.t) / 1000));
    glide.t = now;
    if (still) {
      glide.y = to;
      glide.v = 0;
    } else {
      let acc = SPRING * (to - glide.y) - 2 * Math.sqrt(SPRING) * glide.v;
      // Speeding up is capped so a long glide builds up instead of lurching off
      // the mark; slowing down is left to the spring, which never overshoots.
      if (!glide.v || Math.sign(acc) === Math.sign(glide.v)) acc = Math.max(-RAMP, Math.min(RAMP, acc));
      glide.v += acc * dt;
      glide.y += glide.v * dt;
    }
    root.scrollTo({ top: glide.y, behavior: "instant" });
    if (a.final && Math.abs(to - glide.y) < 1 && Math.abs(glide.v) < 30) {
      // Remember where it came to rest: the next press steps on from this
      // stop as long as the page has not been moved since.
      glide.at = root.scrollTop;
      return stopGlide(true);
    }
    glide.raf = requestAnimationFrame(frame);
  };
  glide.raf = requestAnimationFrame(frame);
}

// takeover ends a glide when the reader steers. A plain n or p is exempt: it
// retargets the glide rather than stopping it, which keeps quick presses fluid.
function takeover(e) {
  const step = e.type === "keydown" && /^[np]$/i.test(e.key) && !(e.shiftKey || e.metaKey || e.ctrlKey || e.altKey);
  if (!step) stopGlide();
}

function stopGlide(settled = false) {
  cancelAnimationFrame(glide.raf);
  glide.raf = 0;
  if (!settled) glide.row = null;
  for (const e of TAKEOVER) window.removeEventListener(e, takeover, true);
}

// Input that means the reader is steering, which a jump must give way to.
const INTERRUPTS = ["wheel", "touchstart", "keydown", "mousedown"];

// yOf is the scroll position that puts el `offset` px below the top of root.
const yOf = (root, el, offset) =>
  root.scrollTop + el.getBoundingClientRect().top - root.getBoundingClientRect().top - offset;

// A file lands with its top border just past the edge, so its header sits
// where it will stay pinned and the border doesn't double the top bar's.
const FILE_TOP = -1;

// chase scrolls root to aim().y every frame until it has held still on a final
// target for a moment. One scroll is not enough: a file the reader has not
// reached is an empty stub until its diff loads, so the page is far shorter
// than it will be and a jump near the end is clamped part-way, and each
// correction brings more files into range, which loads them, which moves the
// target again. It gives up the moment the reader steers - watching input, not
// the scroll position, which scroll anchoring moves on its own as content loads.
function chase(root, aim) {
  const QUIET_MS = 500;
  const LIMIT_MS = 8000;
  const started = performance.now();
  let lastMove = started;
  let live = true;
  const stop = () => {
    live = false;
    for (const e of INTERRUPTS) window.removeEventListener(e, stop, true);
  };
  for (const e of INTERRUPTS) window.addEventListener(e, stop, { capture: true, passive: true });
  const step = () => {
    if (!live) return;
    const a = aim();
    const now = performance.now();
    if (a) {
      const to = Math.round(Math.min(root.scrollHeight - root.clientHeight, Math.max(0, a.y)));
      if (Math.abs(to - root.scrollTop) >= 1) {
        root.scrollTo({ top: to, behavior: "instant" });
        lastMove = now;
      }
    }
    if ((a?.final && now - lastMove > QUIET_MS) || now - started > LIMIT_MS) stop();
    else requestAnimationFrame(step);
  };
  requestAnimationFrame(step);
  return stop;
}

const scopeKey = (s, diff) => diff?.scope?.label || (s.kind === "custom" ? "custom:" + s.rev : s.kind);

// plainDiff shapes a file the comparison leaves alone as a diff of itself, so
// Code mode renders every file through the same rows.
function plainDiff(r) {
  const n = r.lines.length;
  return { path: r.path, lang: r.lang, oldLines: [], newLines: r.lines, ops: [{ k: 0, os: 0, ol: n, ns: 0, nl: n }] };
}

// What the Back control says it leads to, so a step backwards is a known
// destination rather than a guess.
function trailLabel(o) {
  if (!o) return "";
  if (o.type === "file") return o.line ? `${o.file}:${o.line}` : o.file;
  if (o.type === "palette") return `definitions of ${o.query}`;
  if (o.type === "search") return `search for ${o.query}`;
  if (o.type === "files") return o.query ? `files matching ${o.query}` : "files";
  return "";
}

// lineOnScreen is the diff line of `path` the reader is at: the one under the
// pointer, else the first one showing below the file's pinned header.
function lineOnScreen(root, path) {
  const section = document.getElementById("file-" + cssId(path));
  if (!root || !section) return null;
  let cell = section.querySelector("[data-line][data-side]:hover");
  if (!cell) {
    const below = root.getBoundingClientRect().top + (section.querySelector(".file-head")?.offsetHeight ?? 0);
    cell = [...section.querySelectorAll("[data-line][data-side]")].find((c) => c.getBoundingClientRect().bottom > below);
  }
  return cell ? { side: cell.dataset.side, line: Number(cell.dataset.line) } : null;
}
