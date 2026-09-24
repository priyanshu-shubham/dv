import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { flushSync } from "react-dom";
import { api } from "./api.js";
import Header, { AUTO, PRESETS, usePR } from "./Header.jsx";
import Sidebar, { CommentsPanel } from "./Sidebar.jsx";
import FileDiff, { cssId } from "./FileDiff.jsx";
import CodeView from "./CodeView.jsx";
import { FilePalette, FileViewer, HelpOverlay, SearchPanel, SettingsOverlay, useUpdate, WorktreeSession } from "./Overlays.jsx";
import FolderSwitcher from "./FolderSwitcher.jsx";
import { ActionsPalette, ActionsSettings, runAction } from "./Actions.jsx";
import { BranchPalette } from "./Branches.jsx";
import { setPaths } from "./links.js";
import AgentView, { attachKey } from "./Agent.jsx";
import AgentPrompt, { useAgentEvents } from "./AgentPrompt.jsx";
import { Notices, say, useHubActivity, useNotices } from "./Notices.jsx";
import { AttachTarget } from "./Threads.jsx";
import { noteText } from "./Notes.jsx";
import { compareTreePaths, sortTreePaths } from "./tree.js";
import { mapLine, newLineFor, plainDiff } from "./hunks.js";
import { captureAnchor, restoreAnchor, useVersionPoll } from "./live.js";
import FindBar from "./FindBar.jsx";
import { cellPos, fileMatches, findRegExp, headMatches, MAX_FOUND } from "./find.js";
import { blockAt } from "./markdown.js";
import { asMedia } from "./Preview.jsx";
import { agentName, cx, globMatcher, isFindKey, isMac, isSearchKey, isTyping, modKey, openFolderSession, PHONE, searchSeed, selecting, useDebounced, useMedia, usePersisted } from "./util.js";
import { followPrefs, readPref, setPref, usePref } from "./prefs.js";
import { boot } from "./boot.js";
import { setTabIcon, tabDot } from "./favicon.js";

// Shared empty lists so files without comments keep a stable `threads` prop.
const NO_THREADS = [];
const NO_ATTACHED = [];
const NO_ATTACHED_BY = {};
const NO_SETTINGS = {};
const MODES = ["diff", "code", "agent"];
const FOLDER_MODES = ["code", "agent"];
const NO_FILTER = { include: "", exclude: "" };

export default function App() {
  const [meta, setMeta] = useState(null);
  // A picked scope is a pin for this tab; a new session follows the work again.
  const [scope, setScope] = usePersisted("scope", AUTO, { session: true, folder: true });
  const [diff, setDiff] = useState(null);
  const [threads, setThreads] = useState([]);
  const [error, setError] = useState("");

  // A phone has room for one side of a diff, and wraps by default, with its own
  // setting; it shows one panel at a time, over the page: "side", "comments" or null.
  const phone = useMedia(PHONE);
  const [panel, setPanel] = useState(null);
  const [viewPicked, setView] = usePersisted("view", "split");
  const view = phone ? "unified" : viewPicked;
  const [contextLines, setContextLines] = usePref("user", "context", 3);
  const [wrapWide, setWrapWide] = usePersisted("wrap", false);
  const [wrapPhone, setWrapPhone] = usePersisted("wrapPhone", true);
  const [wrap, setWrap] = phone ? [wrapPhone, setWrapPhone] : [wrapWide, setWrapWide];
  const [theme, setTheme] = usePref("user", "theme", "dark");
  const [hideGenerated, setHideGenerated] = usePref("repo", "hideGenerated", false);
  const [pathFilter, setPathFilter] = usePref("repo", "pathFilter", NO_FILTER);
  // Show all pauses the filters rather than clearing them, so they come back
  // as they were. Editing one, or a new page, turns them back on.
  const [filtersPaused, setFiltersPaused] = useState(false);
  const [notesPaused, setNotesPaused] = useState(false);
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
  const [composing, setComposing] = useState(null); // { path, side, start, end, quote, selected }
  // The overlay trail. Its last entry is what is on screen; the ones behind it
  // are the definitions the reader walked through to reach it, so a jump that
  // led somewhere unhelpful can be retraced instead of restarted.
  const [stack, setStack] = useState([]);
  const overlay = stack[stack.length - 1] || null;
  const behind = stack[stack.length - 2] || null;
  // Agent mode: the session on screen, the sessions to list, and what goes
  // with the next message - code, files and comments added from anywhere.
  const [agentId, setAgentId] = usePersisted("agentSession", "", { folder: true });
  const [agent, setAgent] = useState({ available: true, sessions: [], models: [], modes: [] });
  // By session, "" being the one the next new session starts as.
  const [attachedBy, setAttachedBy] = usePref("repo", "agentAttachedBy", NO_ATTACHED_BY);
  const [attachPick, setAttachPick] = usePref("repo", "agentAttachTo", null);
  const [temporaryNew, setTemporaryNew] = useState(false); // the blank session's Temporary toggle
  // The agent a new session starts with, "" for Claude Code, as last picked;
  // one that is not installed gives way to the one that is.
  const [newAgentPicked, setNewAgent] = usePref("user", "newAgent", "");
  const newAgent = agent.codex?.available && (newAgentPicked === "codex" || !agent.available) ? "codex" : "";
  const agents = useMemo(
    () => ({
      "": { available: agent.available, models: agent.models || [], mode: agent.mode, usage: agent.usage },
      // Its models are null until the app-server has answered.
      codex: agent.codex ? { ...agent.codex, models: agent.codex.models || [] } : { available: false, models: [] },
    }),
    [agent],
  );
  const [settings, setSettings] = usePref("user", "settings", NO_SETTINGS);
  const sideRight = settings.sidebar === "right";
  const [agentReveal, setAgentReveal] = useState(null);
  // Counts what was put in the Agent view's message box from elsewhere, each
  // to leave the box ready to write in.
  const [boxFocus, setBoxFocus] = useState(0);
  const [commentsOpen, setCommentsOpen] = usePersisted("commentsOpen", false, { folder: true });
  const showComments = phone ? panel === "comments" : commentsOpen;
  const showPanel = useCallback((on) => (phone ? setPanel(on ? "comments" : null) : setCommentsOpen(on)), [phone, setCommentsOpen]);
  const [commentsWidth, setCommentsWidth] = usePersisted("commentsWidth", 0); // 0: the stylesheet's default
  const [panelTab, setPanelTab] = usePersisted("panelTab", "comments");
  const [notes, setNotes] = useState({ notes: [], path: "" });
  const [newNote, setNewNote] = useState(false);

  const hovered = useRef(null); // { path, side, line } under the cursor
  const scrollRef = useRef(null);

  // Code mode: the whole repository, one file at a time. Both panes stay
  // mounted and the hidden one keeps its scroll, so switching back and forth
  // costs nothing. codeNav is its trail of files, like an editor's back and
  // forward: entries are { path, line, top, scroll }, scroll noted on leaving.
  // A folder outside git has nothing to diff, so it opens on its files.
  const folder = meta?.git === false;
  const modes = folder ? FOLDER_MODES : MODES;
  const [modePicked, setMode] = usePersisted("mode", "diff", { folder: true });
  const mode = folder && modePicked === "diff" ? "code" : modePicked;
  const modeRef = useRef(mode);
  modeRef.current = mode;
  // Once opened, the Agent view stays, hidden in the other modes like they are
  // in it: drawing a long conversation again takes a second.
  const [agentVisited, setAgentVisited] = useState(mode === "agent");
  if (mode === "agent" && !agentVisited) setAgentVisited(true);
  const codeRef = useRef(null);
  const [lastCode, setLastCode] = usePersisted("codePath", "", { folder: true });
  const [codeNav, setCodeNav] = useState(() => ({ stack: lastCode ? [{ path: lastCode }] : [], at: lastCode ? 0 : -1 }));
  const codeAt = codeNav.stack[codeNav.at] || null;
  const codePath = codeAt?.path || "";
  const [plain, setPlain] = useState(null); // { path, fd } or { path, error }: a file outside the diff
  const [repoFiles, setRepoFiles] = useState(null); // { files, ignored }
  // The ignored folders opened in the explorer, which lists them a level at a time.
  const [openIgnored, setOpenIgnored] = useState(() => new Set());
  const listIgnored = useCallback(
    (dirs) => setOpenIgnored((s) => (dirs.every((d) => s.has(d)) ? s : new Set([...s, ...dirs]))),
    [],
  );

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
  }, [theme]);
  useEffect(() => {
    document.documentElement.dataset.code = settings.codeColors || "github";
  }, [settings.codeColors]);

  // Automatic moves to another comparison as the work does - a commit leaves
  // nothing uncommitted - and says so, or the diff changes under the reader
  // with nothing to show why. Not on the first load, nor on picking it. Only
  // the diff shows it, so a move made elsewhere is told on coming back to it.
  const autoWas = useRef(null); // { picked, label }
  const autoNow = scope.kind === "auto" && diff?.scope?.picked ? diff.scope : null;
  useEffect(() => {
    if (mode !== "diff") return;
    const was = autoWas.current;
    autoWas.current = autoNow && { picked: autoNow.picked, label: autoNow.label };
    if (!was || !autoNow || was.picked === autoNow.picked) return;
    const why = was.picked === "working" ? "Nothing is uncommitted any more" : autoNow.picked === "working" ? "There are uncommitted changes again" : autoNow.desc;
    say(`Diff now shows ${autoNow.label}`, why);
  }, [autoNow?.picked, mode]);

  // Claude Code's prompts waiting on the reader. The session on screen in the
  // Agent view asks in its conversation; the rest are told of in notices, and
  // answered in a window over the page opened from one or from the bell.
  const { requests, sessions: activity } = useAgentEvents();
  const windowed = useMemo(() => requests.filter((r) => mode !== "agent" || r.session !== agentId), [requests, mode, agentId]);
  const askingSessions = useMemo(() => new Set(requests.map((r) => r.session)), [requests]);
  const [promptOpen, setPromptOpen] = useState(false);
  const promptRef = useRef(false);
  promptRef.current = promptOpen && windowed.length > 0;
  const [promptFocus, setPromptFocus] = useState(null);
  const seenRequests = useRef(new Set());
  const [arrived, setArrived] = useState(0);
  useEffect(() => {
    const fresh = requests.filter((r) => !seenRequests.current.has(r.id));
    for (const r of fresh) seenRequests.current.add(r.id);
    if (fresh.length) setArrived((n) => n + fresh.length);
    if (!windowed.length) setPromptOpen(false);
  }, [requests, windowed]);
  const closePrompt = useCallback(() => setPromptOpen(false), []);
  const [desktopNotices, setDesktopNotices] = usePersisted("desktopNotices", false);

  useEffect(() => {
    api.meta().then(setMeta).catch((e) => setError(e.message));
  }, []);

  // The versions of the comments and viewed marks on screen. Both can change
  // outside this page - another tab, `dv reset`, an agent - and the poll
  // reloads whichever moved.
  const threadsAt = useRef("");
  const viewedAt = useRef("");
  const notesAt = useRef("");

  const loadThreads = useCallback(() => {
    api
      .threads()
      .then((r) => {
        threadsAt.current = r.version;
        setThreads(r.threads || []);
      })
      .catch(() => {});
  }, []);

  // Notes change on screen before the server has them, so a load that a
  // change overtook is dropped, as the viewed marks' are.
  const notesWrites = useRef(0);
  const loadNotes = useCallback(() => {
    const writes = notesWrites.current;
    api
      .notes()
      .then((r) => {
        if (writes !== notesWrites.current) return;
        notesAt.current = r.version;
        setNotes({ notes: r.notes || [], path: r.path });
      })
      .catch(() => {});
  }, []);
  useEffect(loadNotes, [loadNotes]);
  const changeNotes = useCallback(
    async (change, write) => {
      notesWrites.current++;
      if (change) setNotes((s) => ({ ...s, notes: change(s.notes) }));
      try {
        return await write();
      } finally {
        notesWrites.current++;
        loadNotes();
      }
    },
    [loadNotes],
  );
  const addNote = useCallback((note) => changeNotes(null, () => api.addNote(note)), [changeNotes]);
  const patchNote = useCallback(
    (note, patch) => changeNotes((list) => list.map((n) => (n.id === note.id ? { ...n, ...patch } : n)), () => api.patchNote(note.id, patch)),
    [changeNotes],
  );
  // Gone at once, with a way back while the notice lasts.
  const deleteNote = useCallback(
    (note) =>
      changeNotes((list) => list.filter((n) => n.id !== note.id), () => api.deleteNote(note.id)).then(
        () => say("Note deleted", note.title, [{ label: "Undo", primary: true, run: () => addNote(note).catch((e) => say("Could not put the note back", e.message)) }]),
        (e) => say("Could not delete the note", e.message),
      ),
    [changeNotes, addNote],
  );

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
      if (v.notes !== notesAt.current) {
        notesAt.current = v.notes;
        loadNotes();
      }
      followPrefs(v.prefs);
    },
    [loadThreads, loadViewed, loadNotes],
  );

  // Live updates. versionRef is the repository fingerprint the diff on screen
  // was listed at. loadGen counts full loads, so a fetch one of them overtook
  // is dropped instead of landing on a different comparison.
  const versionRef = useRef("");
  const [repoAt, setRepoAt] = useState(0);
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
      try {
        const d = await api.diffList(scope);
        if (gen !== loadGen.current) return;
        d.files?.sort((a, b) => compareTreePaths(a.path, b.path));
        versionRef.current = d.version;
        setDiff(d);
        setError("");
        // Dropping cached file bodies is what makes a reload (r) honest: each
        // visible section refetches against the new working tree.
        setFileData({});
        if (!opts.keepActive) setActivePath(d.files[0]?.path ?? null);
      } catch (e) {
        if (gen !== loadGen.current) return;
        setError(e.message);
        setDiff(null);
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
      // Media is shown from its own URL; its diff would only read it whole.
      if (asMedia(diffRef.current?.files.find((f) => f.path === path))) return;
      setFileData((prev) => {
        if (prev[path]) return prev;
        fetchFile(path, false);
        return { ...prev, [path]: { loading: true } };
      });
    },
    [fetchFile],
  );

  // needFiles is needFile for a whole list, a batch a request, for find in page:
  // it wants every file at once, and one request apiece would list the
  // comparison on the server once for each.
  const needFiles = useCallback(
    (paths) => {
      const fresh = paths.filter((p) => !fileDataRef.current[p]);
      if (!fresh.length) return;
      setFileData((prev) => {
        const next = { ...prev };
        for (const p of fresh) next[p] ||= { loading: true };
        return next;
      });
      const gen = loadGen.current;
      for (let i = 0; i < fresh.length; i += FILES_PER_REQUEST) {
        const batch = fresh.slice(i, i + FILES_PER_REQUEST);
        const seqs = new Map();
        for (const p of batch) {
          const seq = (fetchSeq.current.get(p) || 0) + 1;
          fetchSeq.current.set(p, seq);
          seqs.set(p, seq);
        }
        const current = (p) => gen === loadGen.current && fetchSeq.current.get(p) === seqs.get(p);
        const land = (each) => {
          const landed = {};
          for (const p of batch) if (current(p)) landed[p] = each(p);
          setFileData((prev) => ({ ...prev, ...landed }));
          return landed;
        };
        api
          .diffFiles(scope, batch)
          .then((res) => {
            const landed = land((p) => (res[p]?.fd ? { fd: res[p].fd } : { error: res[p]?.error || "Not loaded" }));
            // The listing may have moved on while this was in flight.
            for (const [p, st] of Object.entries(landed)) {
              const e = diffRef.current?.files.find((f) => f.path === p);
              if (st.fd && e && e.rev !== st.fd.rev) fetchFile(p, true);
            }
          })
          .catch((e) => land(() => ({ error: e.message })));
      }
    },
    [scope, fetchFile],
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
    // The same diff over different files: an edit outside it, or anything at
    // all in a folder outside git. Code mode reads those files still.
    if (unchanged) return setRepoAt((n) => n + 1);

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

  const [offline, setOffline] = useState(false);
  useVersionPoll(versionRef, liveUpdate, onNotes, setOffline);

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
      // The old side of Claude's edit is the file before that edit, which no
      // comparison here shows.
      if (t.origin && t.side === "old") continue;
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

  // Rendered or as source, the line at the top stays where it was.
  const [preview, setPreview] = usePref("user", "preview", false);
  const togglePreview = useCallback(
    (on) => {
      const a = captureAnchor(codeRef.current);
      setPreview(on);
      if (a?.line) openCode(a.path, a.line, { top: a.top, replace: true });
    },
    [setPreview, openCode],
  );

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
      const cell = root.querySelector(`.code-file [data-line="${line}"]`) || blockAt(root, line);
      return cell && { y: yOf(root, cell, top ?? root.clientHeight / 3), final: true };
    });
  }, [codeAt]);

  // The explorer is what the new side of the comparison holds - plus what the
  // diff deletes, which is still part of the review - and follows live updates.
  useEffect(() => {
    if (mode !== "code") return;
    let live = true;
    api.tree(scope, [...openIgnored]).then((r) => live && setRepoFiles(r)).catch(() => {});
    return () => {
      live = false;
    };
  }, [mode, scope, diff, repoAt, openIgnored]);
  // A file read inside an ignored folder, as when the page is opened again,
  // needs the folders down to it listed before the tree can show it.
  useEffect(() => {
    const top = codePath && repoFiles?.ignored.find((p) => p.endsWith("/") && codePath.startsWith(p));
    if (!top) return;
    const parts = codePath.split("/");
    const dirs = [];
    for (let i = top.split("/").length - 1; i < parts.length; i++) dirs.push(parts.slice(0, i).join("/"));
    listIgnored(dirs);
  }, [repoFiles, codePath, listIgnored]);
  // Ignored entries go under the rest, so a file force-added in an ignored
  // folder is not dimmed. A folder not yet listed ends in "/".
  const explorer = useMemo(() => {
    if (!repoFiles) return [];
    const entries = new Map();
    for (const p of repoFiles.ignored) entries.set(p, { path: p, ignored: true });
    for (const p of repoFiles.files) entries.set(p, { path: p });
    for (const f of diff?.files || []) entries.set(f.path, f);
    return sortTreePaths([...entries.keys()]).map((p) => entries.get(p));
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
      .then((r) => live && setPlain(r.media ? { path: codePath, media: { type: r.media, stamp: r.stamp } } : { path: codePath, fd: plainDiff(r) }))
      .catch((e) => live && setPlain({ path: codePath, error: e.message }));
    return () => {
      live = false;
    };
  }, [mode, codePath, codeEntry, scope, diff, needFile, repoAt]);
  const codeState = codeEntry ? fileData[codePath] : plain?.path === codePath ? plain : null;
  const viewing = overlay?.type === "file" ? overlay.file : "";
  useEffect(() => {
    if (viewing && diff?.files.some((f) => f.path === viewing)) needFile(viewing);
  }, [viewing, diff, needFile]);

  // Find in page, in whichever pane is showing. DiffBody publishes the rows each
  // file lays out into `bodies`, drawn or not, and the matches come from those,
  // so what is found does not depend on what virtualisation has mounted. `find`
  // is the bar's query and toggles while it is open; findCur is the match it is
  // on, { path, side, line, start }.
  const [find, setFind] = useState(null);
  const lastFind = useRef({ query: "", caseSens: false, wholeWord: false, regex: false, focus: 0 });
  const [findCur, setFindCur] = useState(null);
  const bodies = useRef({ diff: new Map(), code: new Map() });
  const [bodiesAt, setBodiesAt] = useState(0);
  const findOpen = useRef(false);
  findOpen.current = !!find;
  const onBody = useMemo(() => {
    const publish = (pane) => (path, body) => {
      if (body) bodies.current[pane].set(path, body);
      else bodies.current[pane].delete(path);
      if (findOpen.current) setBodiesAt((n) => n + 1);
    };
    return { diff: publish("diff"), code: publish("code") };
  }, []);

  const findQuery = useDebounced(find?.query ?? "", 120);
  const findSpec = find && { ...find, query: findQuery };
  const compiled = useMemo(() => findSpec && findRegExp(findSpec), [findQuery, find?.caseSens, find?.wholeWord, find?.regex, !!find]);
  const findPaths = useMemo(() => (mode === "code" ? (codePath ? [codePath] : []) : files.map((f) => f.path)), [mode, codePath, files]);
  // In the diff a file's header path is found too, expanded or not; in Code
  // mode the one header is the file already open, so only its lines are.
  const found = useMemo(() => {
    if (!compiled?.re) return null;
    const list = [];
    const byFile = new Map();
    const heads = new Map();
    let capped = false;
    for (const [i, path] of findPaths.entries()) {
      if (list.length >= MAX_FOUND) {
        capped = true;
        break;
      }
      const head = mode === "diff" && headMatches(files[i], compiled.re);
      if (head) {
        heads.set(path, head.ranges);
        for (const m of head.list) list.push(m);
      }
      const body = bodies.current[mode].get(path);
      const f = body && fileMatches(path, body, compiled.re);
      if (!f?.list.length) continue;
      byFile.set(path, f.byLine);
      for (const m of f.list) list.push(m);
    }
    return { list, byFile, heads, capped: capped || list.length > MAX_FOUND };
  }, [compiled, findPaths, files, mode, bodiesAt]);
  const findIndex = useMemo(() => (found && findCur ? found.list.findIndex((m) => sameMatch(m, findCur)) : -1), [found, findCur]);

  // The diff's files still to load: a match in one of them is not found yet.
  // Folded files and generated ones are left out, as they would be on screen.
  const findWants = useMemo(
    () => (find && mode === "diff" ? files.filter((f) => !f.generated && !asMedia(f) && !collapsed.has(f.path)).map((f) => f.path) : []),
    [!!find, mode, files, collapsed],
  );
  useEffect(() => {
    if (findQuery && findWants.length) needFiles(findWants);
  }, [findQuery, findWants, needFiles]);
  const findPending = findQuery ? findWants.filter((p) => !fileData[p] || fileData[p].loading).length : 0;

  // showMatch puts the find bar on a match, and scrolls to it unless it is
  // already in view. The row may be virtualised away, so reveal keeps its
  // block mounted until the scroll lands.
  const showMatch = useCallback(
    (m) => {
      setFindCur({ path: m.path, side: m.side, line: m.line, start: m.start });
      const code = modeRef.current === "code";
      const root = code ? codeRef.current : scrollRef.current;
      const section = code ? root?.querySelector(".code-file") : document.getElementById("file-" + cssId(m.path));
      if (!root || !section) return;
      if (!m.line) {
        setActivePath(m.path);
        const top = section.getBoundingClientRect().top - root.getBoundingClientRect().top;
        if (top >= 0 && top <= root.clientHeight - 80) return;
        return chase(root, () => ({ y: yOf(root, section, FILE_TOP), final: true }));
      }
      const cellAt = () => section.querySelector(`[data-side="${m.side}"][data-line="${m.line}"]`);
      const cell = cellAt();
      if (cell && inView(root, cell)) return;
      setReveal({ path: m.path, line: m.line });
      if (!code) {
        setActivePath(m.path);
        needFile(m.path);
      }
      chase(root, () => {
        const c = cellAt();
        return c ? { y: yOf(root, c, root.clientHeight / 3), final: true } : { y: yOf(root, section, FILE_TOP), final: false };
      });
    },
    [needFile],
  );

  // nearestMatch is the first match at or after the line at the top of the view.
  const nearestMatch = () => {
    const list = found.list;
    const pane = modeRef.current;
    const a = captureAnchor(pane === "code" ? codeRef.current : scrollRef.current);
    const order = new Map(findPaths.map((p, i) => [p, i]));
    const fa = order.get(a?.path);
    if (fa === undefined) return list[0];
    const body = bodies.current[pane].get(a.path);
    const pa = a.line && body ? cellPos(body, a.side, a.line) : 0;
    return list.find((m) => order.get(m.path) > fa || (m.path === a.path && m.pos >= pa)) || list[0];
  };

  const stepFind = useRef(null);
  stepFind.current = (dir) => {
    const list = found?.list;
    if (!list?.length) return;
    if (findIndex >= 0) return showMatch(list[(findIndex + dir + list.length) % list.length]);
    const near = nearestMatch();
    showMatch(dir > 0 ? near : list[(list.indexOf(near) - 1 + list.length) % list.length]);
  };

  // A new query lands on the match nearest the reader, as a browser's find does,
  // unless the match it was on still matches. It waits for files still loading
  // when nothing has matched yet.
  const landing = useRef(false);
  useEffect(() => {
    landing.current = true;
  }, [findQuery, find?.caseSens, find?.wholeWord, find?.regex]);
  useEffect(() => {
    if (!found || !landing.current) return;
    if (!found.list.length) {
      if (!findPending) landing.current = false;
      return;
    }
    landing.current = false;
    if (findIndex < 0) showMatch(nearestMatch());
  }, [found, findPending]);

  const openFind = useCallback((seed) => {
    setFind((f) => {
      const base = f || lastFind.current;
      return { ...base, query: seed || base.query, focus: base.focus + 1 };
    });
  }, []);
  const changeFind = useCallback((patch) => setFind((f) => f && { ...f, ...patch }), []);
  const closeFind = useCallback(() => {
    setFind((f) => {
      if (f) lastFind.current = f;
      return null;
    });
    setFindCur(null);
  }, []);

  // Ctrl+F is taken before anything focused can keep it, so it reaches find from
  // a composer too, seeded like Ctrl+K: the selection in a text box, else on the
  // page, else the code whose selection opened the composer - which goes if
  // nothing was typed in it. Over a modal, Claude's request or a Markdown preview
  // in Code mode the browser's own find stays: those draw every line.
  useEffect(() => {
    const onKey = (e) => {
      if (promptRef.current || document.querySelector(".backdrop") || modeRef.current === "agent") return;
      if (modeRef.current === "code" && codeRef.current?.querySelector(".md-preview")) return;
      if (isFindKey(e)) {
        e.preventDefault();
        if (e.target.closest?.(".find-bar")) return openFind("");
        const t = e.target;
        const box = t.closest?.(".composer");
        const typed = typeof t.selectionStart === "number" ? t.value.slice(t.selectionStart, t.selectionEnd) : "";
        const seed = searchSeed(typed || window.getSelection()?.toString()) || box?.dataset.selected || "";
        if (box?.dataset.selected && !box.dataset.draft && seed === box.dataset.selected) setComposing(null);
        openFind(seed);
      } else if (findOpen.current && (e.key === "F3" || ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === "g"))) {
        e.preventDefault();
        stepFind.current(e.shiftKey ? -1 : 1);
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [openFind]);

  // Alt+H goes where ← dv at the top left does, from the message box too. Read
  // by code, as Option+H on a Mac types a dead key.
  useEffect(() => {
    if (!boot.base) return;
    const onKey = (e) => {
      if (e.code !== "KeyH" || !e.altKey || e.metaKey || e.ctrlKey || e.shiftKey) return;
      if (document.querySelector(".backdrop, .prompt-backdrop:not([hidden])")) return;
      e.preventDefault();
      location.href = "/";
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, []);

  const findBar = find && (
    <FindBar
      find={find}
      total={found?.list.length || 0}
      index={findIndex}
      capped={!!found?.capped}
      pending={findPending}
      error={compiled?.error}
      onChange={changeFind}
      onStep={(dir) => stepFind.current(dir)}
      onClose={closeFind}
    />
  );

  // Switching mode keeps the reader's place: the line at the top of one view is
  // put at the same height in the other, when the other has it.
  const pendingDiff = useRef(null);
  const switchMode = useCallback(
    (next) => {
      const from = modeRef.current;
      if (next === from) return;
      if (next === "agent" || from === "agent") return setMode(next);
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
      const t = await api.createThread({ scope: diff?.scope?.label || scope.kind, ...payload });
      loadThreads();
      return t;
    },
    [diff, scope, loadThreads],
  );

  const onThreadAction = useCallback(
    async (action) => {
      const { type, thread } = action;
      if (type === "jump") {
        if (thread.origin) {
          setMode("agent");
          setAgentId(thread.origin.session);
          return setAgentReveal({ tool: thread.origin.tool });
        }
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
    [jumpToFile, loadThreads, openCode, setMode, setAgentId],
  );

  // Opening an overlay from the diff starts a fresh trail; following a symbol
  // out of one extends it. `left` is noted on the overlay being covered - where
  // it was scrolled to, what had been typed - so stepping back returns to it as
  // it was rather than to the top of the definition they arrived at.
  const openOverlay = useCallback((o) => setStack([o]), []);
  // The last search as it was left, which asking for search again without a
  // new query brings back: its results, and the row and scroll it had.
  const lastSearch = useRef(null);
  const keepSearch = useCallback((o, { query, opts, place }) => {
    const same = query === o.query;
    lastSearch.current = { type: "search", query, opts, place, from: o.from, seed: same ? o.seed : undefined, source: same ? o.source : undefined };
  }, []);
  const openSearch = useCallback(
    (query) => {
      const last = lastSearch.current;
      openOverlay(last && (!query || query === last.query) ? last : { type: "search", query: query || undefined });
    },
    [openOverlay],
  );
  const pushOverlay = useCallback((o, left) => {
    setStack((s) => {
      const trail = left && s.length ? [...s.slice(0, -1), { ...s[s.length - 1], ...left }] : s;
      return [...trail, o];
    });
  }, []);
  const goBack = useCallback(() => setStack((s) => s.slice(0, -1)), []);
  const closeOverlay = useCallback(() => setStack([]), []);

  // Where a jump lands: in place in Code mode, and in a viewer over the diff -
  // or over Claude's request, when that is open, so Esc comes back to it.
  const goTo = useCallback(
    (file, line, left) => {
      if (modeRef.current !== "code" || promptRef.current) return pushOverlay({ type: "file", file, line }, left);
      setStack([]);
      openCode(file, line);
    },
    [pushOverlay, openCode],
  );

  // A path in what an agent or a comment says opens where a jump does. The
  // files it can name are the repository's, read once as the page opens, or
  // as Files lists them, with the diff's, which are always current.
  const [pathFiles, setPathFiles] = useState(null);
  useEffect(() => {
    api.tree(AUTO).then((r) => setPathFiles(r.files), () => {});
  }, []);
  useEffect(() => {
    if (!meta) return;
    setPaths([...(repoFiles?.files || pathFiles || []), ...(diff?.files || []).map((f) => f.path)], [meta.root, meta.place]);
  }, [meta, pathFiles, repoFiles, diff]);
  useEffect(() => {
    const onClick = (e) => {
      const a = e.target.closest?.("a.path-link");
      if (!a) return;
      e.preventDefault();
      goTo(a.dataset.path, Number(a.dataset.line) || 0);
    };
    document.addEventListener("click", onClick);
    return () => document.removeEventListener("click", onClick);
  }, [goTo]);

  // Cmd/Ctrl+clicking an identifier resolves it to definitions, ranked by how
  // close each one is to the file it was clicked in. One opens straight away;
  // otherwise the search comes up with the candidates over the word's uses.
  const onSymbol = useCallback(
    async (name, from = "", leftAt = 0) => {
      const left = leftAt ? { scroll: leftAt } : null;
      const search = { type: "search", query: name, from, opts: { caseSens: true, wholeWord: true } };
      try {
        const r = await api.resolveSymbol(name, from);
        const defs = r.defs || [];
        if (defs.length === 1) goTo(defs[0].file, defs[0].line, left);
        else pushOverlay({ ...search, seed: defs, source: r.source }, left);
      } catch {
        pushOverlay(search, left);
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
      if (modeRef.current === "code" && !promptRef.current) {
        setStack([]);
        return openCode(path);
      }
      const deleted = diffRef.current?.files.some((f) => f.path === path && f.status === "D");
      pushOverlay({ type: "file", file: path, side: deleted ? "old" : undefined }, { query });
    },
    [pushOverlay, openCode],
  );

  const loadSessions = useCallback(() => {
    api.agentSessions().then(setAgent).catch(() => {});
  }, []);
  // Once in any mode: Send can go to a session that is not open, and names it.
  useEffect(() => loadSessions(), [loadSessions]);
  useEffect(() => {
    if (mode !== "agent") return;
    loadSessions();
    const t = setInterval(() => document.hidden || loadSessions(), 2500);
    return () => clearInterval(t);
  }, [mode, loadSessions]);
  // The models come from asking Claude Code and Codex, which the first load sets off.
  useEffect(() => {
    if (mode !== "agent" || (!agent.available || agent.models?.length) && (!agent.codex || agent.codex.models?.length)) return;
    const t = setTimeout(loadSessions, 500);
    return () => clearTimeout(t);
  }, [mode, agent, loadSessions]);

  // Opening a session is what brings its prompts into the page, which only
  // matters for one that is running; a past one opens when it is sent to.
  const selectSession = useCallback(
    (id) => {
      setAgentId(id);
      const row = agent.sessions.find((x) => x.id === id);
      if (row?.running && !row.open) api.agentOpen(id, true).then(loadSessions, () => {});
    },
    [agent.sessions, setAgentId, loadSessions],
  );
  // An open session dv stopped while it sat idle starts again once looked at
  // for a moment, so other sessions can send to it; stepping through the list
  // does not start each one passed.
  const onScreen = mode === "agent" ? agent.sessions.find((x) => x.id === agentId) : null;
  const wake = onScreen?.open && !onScreen.running && onScreen.agent !== "codex" ? onScreen.id : "";
  useEffect(() => {
    if (!wake) return;
    const t = setTimeout(() => api.agentStart(wake).then(loadSessions, () => {}), 1000);
    return () => clearTimeout(t);
  }, [wake, loadSessions]);
  // A session is made when its first message is sent; until then it is the
  // page's blank one.
  const newSession = useCallback(async () => {
    const { id } = await api.agentCreate(newAgent);
    setAgentId(id);
    loadSessions();
    return id;
  }, [newAgent, setAgentId, loadSessions]);
  const startSession = useCallback(() => {
    setAgentId("");
    requestAnimationFrame(() => document.querySelector(".agent-composer textarea")?.focus());
  }, [setAgentId]);
  // A session in a new worktree, which only a hub makes, of a repository.
  const [worktreeOpen, setWorktreeOpen] = useState(false);
  // A worktree starts from a commit; a task has no worktrees at all.
  const newWorktree = boot.base && meta?.git && meta.head?.sha && !meta.task ?() => (setPanel(null), setWorktreeOpen(true)) : null;
  const closeSession = useCallback(
    async (id) => {
      const row = agent.sessions.find((x) => x.id === id);
      if (row?.running === "dv" && row.busy && !confirm(`${agentName(row.agent)} is still working in this session. Stop it and close?`)) return;
      await api.agentOpen(id, false).catch(() => {});
      if (id === agentId) setAgentId("");
      loadSessions();
    },
    [agent.sessions, agentId, setAgentId, loadSessions],
  );

  const pr = usePR(meta?.head?.branch, meta?.remote);
  // The page's own actions, which do what their buttons and keys do. What the
  // diff compares is the bar's menu in Diff mode; Files reads by it instead.
  const base = meta?.defaultBranch || "";
  const comparing = mode === "diff" && !folder;
  const pageActions = [
    {
      id: "new-session",
      name: "New session",
      note: "In the Agent view · also Alt+N there",
      run: () => {
        switchMode("agent");
        startSession();
      },
    },
    newWorktree && { id: "new-worktree", name: "New worktree", note: "A session in a new worktree of this repo", run: newWorktree },
    // A task's folder is the hub's to make and hold, as the hub's button does.
    boot.base && {
      id: "new-task",
      name: "New task",
      note: "A session in a new folder, deleted when you close it",
      run: () => api.hubNewTask().then((f) => openFolderSession(f.slug, ""), (e) => say("Could not start a task", e.message)),
    },
    // Open, each closes the panel, as the header's button does; else it turns to them.
    ...[
      ["comments", "comments", "The comments in the review"],
      ["notes", "notes", "The repository's notes, shared by its worktrees"],
    ].map(([tab, what, note]) => {
      const shown = showComments && panelTab === tab;
      return { id: tab, name: `${shown ? "Close" : "Open"} ${what}`, note, run: () => (shown ? showPanel(false) : (setPanelTab(tab), showPanel(true))) };
    }),
    { id: "new-note", name: "New note", note: "For later, on the repository's list", run: () => (setPanelTab("notes"), setNewNote(true), showPanel(true)) },
    ...(comparing
      ? PRESETS.filter((p) => p.kind !== scope.kind).map((p) => ({
          id: "scope-" + p.kind,
          name: `Show ${p.label.toLowerCase()}`,
          note: `${p.hint(base)} · pinned for this tab`,
          run: () => setScope({ kind: p.kind, rev: "" }),
        }))
      : []),
    comparing && scope.kind !== "auto" && { id: "scope-auto", name: "Show automatically", note: "Follows your work, as the page starts", run: () => setScope(AUTO) },
    pr && { id: "pr", name: `Open pull request #${pr.number}`, note: pr.title, run: () => window.open(pr.url, "_blank", "noopener") },
    meta?.git && meta.head?.sha && { id: "branch", name: "Switch branch", note: "Or make a new one · also the branch in the bar", run: () => openOverlay({ type: "branch" }) },
    { id: "settings", name: "Open settings", note: "Also the , key", run: () => openOverlay({ type: "settings" }) },
    boot.base && { id: "hub", name: "Back to the hub", note: "Also Alt+H", run: () => (location.href = "/") },
  ].filter(Boolean);

  // An action run from outside the Agent view, or in a session of its own,
  // happens out of sight, so the page says it went.
  const doAction = useCallback(
    (a) => {
      closeOverlay();
      if (a.run) return a.run();
      if (a.builtin) {
        api.pull(a.builtin === "main").then(
          ({ branch, pulled }) => say(pulled ? `Pulled ${pulled} commit${pulled === 1 ? "" : "s"} into ${branch}` : `${branch} is up to date`),
          (e) => say(`Could not ${a.name.toLowerCase()}`, e.message),
        );
        return;
      }
      if (a.kind === "command") {
        runAction(a).then(
          () => say(`${a.name} started`, "You are told how it ends; Alt+A lists it while it runs."),
          (e) => say(`Could not run ${a.name}`, e.message),
        );
        return;
      }
      const openSession = (session) => {
        setPromptOpen(false);
        setPanel(null);
        setAgentId(session);
        switchMode("agent");
      };
      // Into the message box instead, after what is typed there, to be edited.
      if (a.where === "current" && a.draft) {
        const key = "draft:" + agentId;
        setPref("repo", key, [readPref("repo", key, ""), a.prompt].filter((t) => t.trim()).join("\n\n"), "");
        setBoxFocus((n) => n + 1);
        return openSession(agentId);
      }
      runAction(a, agentId).then(
        ({ session }) => {
          const open = { label: "Open", primary: true, opens: true, run: () => openSession(session) };
          if (a.where === "new") say(`${a.name} started`, "In a new session. You are told when it is done.", [open]);
          else if (mode !== "agent") say(`${a.name} sent`, "To the session open in the Agent view.", [open]);
          loadSessions();
        },
        (e) => say(`Could not run ${a.name}`, e.message),
      );
    },
    [agentId, mode, closeOverlay, loadSessions, setAgentId, switchMode],
  );
  // Alt+A lists the actions, from the message box too; Cmd/Ctrl+Shift+P as
  // well, where the browser leaves it to the page.
  useEffect(() => {
    const onKey = (e) => {
      const alt = e.code === "KeyA" && e.altKey && !e.metaKey && !e.ctrlKey && !e.shiftKey;
      const palette = e.code === "KeyP" && e.shiftKey && !e.altKey && (isMac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey);
      if (!alt && !palette) return;
      if (document.querySelector(".backdrop, .prompt-backdrop:not([hidden])")) return;
      e.preventDefault();
      openOverlay({ type: "actions" });
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [openOverlay]);

  const elsewhere = useHubActivity();
  const notices = useNotices({
    looking: mode === "agent" ? agentId : null,
    desktop: desktopNotices,
    go: openFolderSession,
    review: (id) => {
      setPromptFocus(id);
      setPromptOpen(true);
    },
    open: (id) => {
      setPromptOpen(false);
      setPanel(null);
      selectSession(id);
      switchMode("agent");
    },
  });
  // Desktop notifications are the browser's to allow, asked when turned on.
  // Hooks are asked after with the sessions, but Settings opens in any mode.
  const setHooks = useCallback((hooks) => setAgent((a) => ({ ...a, hooks })), []);
  useEffect(() => {
    if (overlay?.type === "settings") api.claudeHooks().then(setHooks, () => {});
  }, [overlay?.type, setHooks]);
  const pickHooks = useCallback(
    (on) => api.setClaudeHooks(on).then(setHooks, (e) => say(on ? "Could not add the hooks" : "Could not take the hooks out", e.message)),
    [setHooks],
  );
  const pickDesktopNotices = useCallback(
    async (on) => {
      if (!on || typeof Notification === "undefined") return setDesktopNotices(false);
      const allowed = Notification.permission === "default" ? await Notification.requestPermission() : Notification.permission;
      setDesktopNotices(allowed === "granted");
      if (allowed === "denied") say("Notifications are blocked", "The browser blocks them for this page; allow them in its site settings.");
    },
    [setDesktopNotices],
  );

  const dot = tabDot(notices);
  // The waiting note trails so a narrow tab still shows which repository it
  // is; the count up front is enough to catch the eye. The path is for
  // checkouts that share a name.
  useEffect(() => {
    const n = requests.length;
    const who = settings.tabName?.trim() || "dv";
    const repo = meta ? `${who} - ${meta.repo} (${meta.place})` : who;
    const ended = dot === "done";
    const note = n > 0 ? `${agentName(requests[0].via)} is waiting` : ended && "Finished";
    document.title = [n ? `(${n}) ${repo}` : ended ? `✓ ${repo}` : repo, note].filter(Boolean).join(" - ");
  }, [requests, dot, meta, settings.tabName]);
  const update = useUpdate(settings.updateCheck === false);
  useEffect(() => setTabIcon(settings.tabColor, dot), [settings.tabColor, dot]);

  // Shift+Up and Down go through the open sessions in the order the list has
  // them. The message box keeps them for selecting text, unless it is empty,
  // and so does text selected in the page.
  // Captured, because the box stops the keys it handles from bubbling.
  useEffect(() => {
    if (mode !== "agent") return;
    const onKey = (e) => {
      if (!e.shiftKey || e.metaKey || e.ctrlKey || e.altKey || (e.key !== "ArrowUp" && e.key !== "ArrowDown")) return;
      if (isTyping(e.target) && !(e.target.matches(".agent-composer textarea") && !e.target.value)) return;
      if (selecting()) return;
      if (document.querySelector(".backdrop, .prompt-backdrop:not([hidden])")) return;
      const open = agent.sessions.filter((s) => s.open || s.running === "dv");
      if (!open.length) return;
      e.preventDefault();
      e.stopPropagation();
      const i = open.findIndex((s) => s.id === agentId);
      const step = e.key === "ArrowDown" ? 1 : -1;
      selectSession(open[i < 0 ? (step > 0 ? 0 : open.length - 1) : (i + step + open.length) % open.length].id);
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [mode, agent.sessions, agentId, selectSession]);

  // What is added from Diff and Code goes to a session that can be written to:
  // the one last looked at or picked, open or not, else the first open one,
  // else a new one. The open sessions come with the requests, in every mode.
  const writable = (s) => s && s.running !== "terminal";
  useEffect(() => {
    if (agentId && agentId !== attachPick && writable(agent.sessions.find((s) => s.id === agentId))) setAttachPick(agentId);
  }, [agentId, agent.sessions, attachPick, setAttachPick]);
  const attachChoices = useMemo(() => {
    const choice = (s) => ({ id: s.id, label: s.title || s.prompt || "Untitled session", agent: s.agent || "" });
    const out = (activity || []).filter(writable).map(choice);
    const picked = agent.sessions.find((s) => s.id === attachPick);
    if (writable(picked) && !out.some((c) => c.id === picked.id)) out.unshift(choice(picked));
    return out;
  }, [activity, agent.sessions, attachPick]);
  const attachTo = attachChoices.some((c) => c.id === attachPick) ? attachPick : attachChoices[0]?.id || "";
  const attachTarget = useMemo(
    () => ({ choices: attachChoices, target: attachTo, onTarget: setAttachPick, newAgent }),
    [attachChoices, attachTo, setAttachPick, newAgent],
  );

  // attach puts something in a session's message box and goes there, where it
  // waits for the words that go with it. Code is taken as it reads on screen,
  // so it says which version that was.
  const openAdded = useCallback(() => {
    setStack([]);
    setAgentId("");
    if (settings.addedTemporary) setTemporaryNew(true);
    switchMode("agent");
  }, [setAgentId, settings.addedTemporary, switchMode]);
  const attach = useCallback(
    (a, to = attachTo) => {
      const sc = diffRef.current?.scope;
      const newAt = sc?.newAt === "index" ? "staged" : sc?.newAt || "working tree";
      const at = a.kind !== "lines" ? "" : a.at || (a.side === "old" && modeRef.current !== "code" ? sc?.oldAt || "HEAD" : newAt);
      const key = attachKey(a);
      setAttachedBy((by) => {
        const list = by[to] || [];
        return list.some((x) => x.key === key) ? by : { ...by, [to]: [...list, { ...a, at, key }] };
      });
      setBoxFocus((n) => n + 1);
      if (to === "") return openAdded();
      setStack([]);
      setAgentId(to);
      switchMode("agent");
    },
    [attachTo, setAttachedBy, openAdded, setAgentId, switchMode],
  );
  // sendThreads hands comments over together, with a line to send them with
  // where nothing is typed there yet; deleteThreads is the panel's way to
  // clear out what the review is done with.
  const sendThreads = useCallback(
    (list, to = attachTo) => {
      for (const t of list) attach({ kind: "thread", threadId: t.id }, to);
      const key = "draft:" + (to || "new");
      if (!readPref("repo", key, "")) setPref("repo", key, "Address these comments.", "");
    },
    [attach, attachTo],
  );
  // sendNote puts a note in a session's message box, after what is typed there.
  const sendNote = useCallback(
    (note, to = attachTo) => {
      const key = "draft:" + (to || "new");
      setPref("repo", key, [readPref("repo", key, ""), noteText(note)].filter((t) => t.trim()).join("\n\n"), "");
      setBoxFocus((n) => n + 1);
      if (phone) setPanel(null);
      if (to === "") return openAdded();
      setStack([]);
      setAgentId(to);
      switchMode("agent");
    },
    [attachTo, phone, openAdded, setAgentId, switchMode],
  );
  // Discarding is offered where the diff is your uncommitted work, the working
  // tree against the last commit: elsewhere it would undo something else.
  const uncommitted = diff?.scope?.kind === "working" || diff?.scope?.picked === "working";
  const discard = useCallback(
    (entry) => {
      const { path, oldPath, status } = entry;
      const ask =
        entry.untracked || status === "A"
          ? `Delete ${path}? It's new since the last commit, so it can't be brought back.`
          : status === "D"
            ? `Bring back ${path} as the last commit has it?`
            : `Discard your changes to ${path}${oldPath ? ` and move it back to ${oldPath}` : ""}? They can't be brought back.`;
      if (!confirm(ask)) return;
      api.discard(oldPath ? [oldPath, path] : [path]).then(
        () => loadDiff({ keepActive: true }),
        (e) => say(`Could not discard ${path}`, e.message),
      );
    },
    [loadDiff],
  );
  const deleteThreads = useCallback(
    async (list) => {
      const what = list.length === 1 ? "this comment thread" : `these ${list.length} comment threads`;
      if (!confirm(`Delete ${what}? This can't be undone.`)) return;
      for (const t of list) await api.deleteThread(t.id);
      loadThreads();
    },
    [loadThreads],
  );
  const attachedFor = useCallback(
    (to, change) =>
      setAttachedBy((by) => {
        const { [to]: list = [], ...rest } = by;
        const next = change(list);
        return next.length ? { ...rest, [to]: next } : rest;
      }),
    [setAttachedBy],
  );
  // A comment added and since deleted goes with nothing, so nothing counts it.
  const attachedLive = useMemo(() => {
    const ids = new Set(threads.map((t) => t.id));
    const out = {};
    for (const [to, list] of Object.entries(attachedBy)) {
      const kept = list.filter((a) => a.kind !== "thread" || ids.has(a.threadId));
      if (kept.length) out[to] = kept.length === list.length ? list : kept;
    }
    return out;
  }, [attachedBy, threads]);
  const attachedHere = attachedLive[agentId] || NO_ATTACHED;
  // Only what a session still open, or the next new one, would send.
  const attachedCount = attachChoices.reduce((n, c) => n + (attachedLive[c.id]?.length || 0), attachedLive[""]?.length || 0);

  // attachHere is `a`: the line under the pointer, else the file being read.
  const attachHere = useCallback(() => {
    const h = hovered.current;
    const fd = h && (fileDataRef.current[h.path]?.fd || (plain?.path === h.path ? plain.fd : null));
    if (fd) {
      const src = h.side === "old" ? fd.oldLines : fd.newLines;
      return attach({ kind: "lines", file: h.path, side: h.side, start: h.line, end: h.line, quote: [src[h.line - 1]] });
    }
    const path = modeRef.current === "code" ? codePath : activePath;
    if (path) attach({ kind: "file", file: path });
  }, [attach, plain, codePath, activePath]);

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
      const mod = e.metaKey || e.ctrlKey;
      // Round the modes in the order the sidebar has them. The Agent view's
      // message box nearly always has focus, so an empty one lets these by.
      // Selected text keeps them, as does an overlay, which has the page.
      if (e.shiftKey && !mod && !e.altKey && (e.key === "ArrowLeft" || e.key === "ArrowRight")) {
        if (selecting() || document.querySelector(".backdrop, .prompt-backdrop:not([hidden])")) return;
        if (!isTyping(e.target) || (e.target.closest(".agent-composer") && !e.target.value)) {
          e.preventDefault();
          const i = modes.indexOf(mode) + (e.key === "ArrowRight" ? 1 : -1);
          switchMode(modes[(i + modes.length) % modes.length]);
          return;
        }
      }
      if (isTyping(e.target)) return;

      if (isSearchKey(e)) {
        e.preventDefault();
        openSearch(searchSeed(window.getSelection()?.toString()));
        return;
      }
      if (mod && !e.shiftKey && e.key.toLowerCase() === "p") {
        e.preventDefault(); // the browser's Print
        openOverlay({ type: "files" });
        return;
      }
      const code = mode === "code";
      if (code && e.altKey && !mod && (e.key === "ArrowLeft" || e.key === "ArrowRight")) {
        e.preventDefault(); // otherwise the browser's own Back and Forward
        stepCode(e.key === "ArrowLeft" ? -1 : 1);
        return;
      }
      if (mod || e.altKey) return;
      // The Agent view has keys of its own; these few mean the same there.
      if (mode === "agent" && !["?", "u", "w"].includes(e.key)) return;

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
          closeFind();
          // Keys typed in a composer stop there, so one reached from here is
          // unfocused and its draft is read off the page.
          if (!document.querySelector(".composer[data-draft]")) setComposing(null);
          break;
        case "?":
          openOverlay({ type: "help" });
          break;
        case ",":
          openOverlay({ type: "settings" });
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
        case "c":
          e.preventDefault();
          startCommentAtCursor();
          break;
        case "a":
          e.preventDefault();
          attachHere();
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
    loadDiff, loadThreads, startCommentAtCursor, attachHere, openOverlay, openSearch, closeOverlay, closeFind, needFile, viewFile,
    mode, modes, codePath, openCode, stepCode, switchMode,
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
    <AttachTarget.Provider value={attachTarget}>
    {/* Out of the page's grid, whose rows are the header's and the rest's. */}
    <Notices />
    {worktreeOpen && <WorktreeSession repo={meta.repo} from={meta.head?.branch || meta.head?.sha || "HEAD"} onClose={() => setWorktreeOpen(false)} />}
    <div className={cx("app", offline && "offline")}>
      <Header
        meta={meta}
        folder={folder}
        mode={mode}
        onMode={switchMode}
        scope={scope}
        resolvedScope={diff?.scope}
        onScope={setScope}
        onSearch={() => openSearch()}
        onOpenFile={() => {
          setPanel(null);
          openOverlay({ type: "files" });
        }}
        onHelp={() => openOverlay({ type: "help" })}
        onSettings={() => openOverlay({ type: "settings" })}
        waiting={requests.length}
        arrived={arrived}
        onBell={() => (windowed.length ? setPromptOpen((o) => !o) : document.querySelector(".agent-ask")?.scrollIntoView({ block: "nearest" }))}
        bellOn={promptOpen}
        update={update}
        pr={pr}
        onActions={() => openOverlay({ type: "actions" })}
        comments={threads.filter((t) => !t.resolved).length}
        commentsOn={showComments}
        onComments={() => (phone ? setPanel((p) => (p === "comments" ? null : "comments")) : setCommentsOpen((o) => !o))}
        sideOn={phone && panel === "side"}
        onSide={() => setPanel((p) => (p === "side" ? null : "side"))}
        sideRight={sideRight}
        onNewSession={() => {
          setPanel(null);
          startSession();
        }}
        onNewWorktree={newWorktree}
        canStart={agent.available || !!agent.codex?.available}
      />

      <div
        className={cx("main", sideRight && "side-right", showComments && "with-comments", phone && panel === "side" && "side-open")}
        style={{ ...(sideWidth && { "--side-w": sideWidth + "px" }), ...(commentsWidth && { "--comments-w": commentsWidth + "px" }) }}
      >
        {phone && <div className={cx("panel-backdrop", panel && "on")} onClick={() => setPanel(null)} />}
        <Sidebar
          meta={meta}
          pr={pr}
          right={sideRight}
          mode={mode}
          modes={modes}
          onMode={switchMode}
          attached={attachedCount}
          files={mode === "code" ? explorer : files}
          onOpenIgnored={listIgnored}
          threads={threads}
          activePath={mode === "code" ? codePath : activePath}
          viewed={viewed}
          onSelect={(...a) => {
            if (phone) setPanel(null);
            (mode === "code" ? openCode : jumpToFile)(...a);
          }}
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
          agent={{
            sessions: agent.sessions,
            available: agent.available || !!agent.codex?.available,
            // The limits of the agent on screen, or the one a new session starts with.
            usage: agents[agentId ? agent.sessions.find((s) => s.id === agentId)?.agent || "" : newAgent]?.usage,
            activeId: agentId,
            added: attachedLive,
            asking: askingSessions,
            onSelect: (id) => {
              if (phone) setPanel(null);
              selectSession(id);
            },
            onNew: () => {
              if (phone) setPanel(null);
              startSession();
            },
            onNewWorktree: newWorktree,
            onClose: closeSession,
            onRename: (id, title) => api.agentRename(id, title).then(loadSessions, (e) => say("Could not rename the session", e.message)),
          }}
          onWidth={setSideWidth}
        />

        <div className="content" ref={scrollRef} onMouseOver={onMouseOver} hidden={mode !== "diff"}>
          {mode === "diff" && findBar}
          {error && <div className="banner error">{error}</div>}
          {meta && !folder && diff && files.length === 0 && !error && (
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
              onAttach={attach}
              onSearch={openSearch}
              onView={viewFile}
              onDiscard={uncommitted ? discard : null}
              scope={scope}
              onOpenFile={goTo}
              reveal={reveal?.path === entry.path ? reveal.line : 0}
              onBody={onBody.diff}
              found={(mode === "diff" && found?.byFile.get(entry.path)) || null}
              foundHead={(mode === "diff" && found?.heads.get(entry.path)) || null}
              foundAt={mode === "diff" && findCur?.path === entry.path ? findCur : null}
            />
          ))}
        </div>

        <div className="content code-pane" ref={codeRef} onMouseOver={onMouseOver} hidden={mode !== "code"}>
          {mode === "code" && findBar}
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
              onAttach={attach}
              onSearch={openSearch}
              onBody={onBody.code}
              found={(mode === "code" && found?.byFile.get(codePath)) || null}
              foundAt={mode === "code" && findCur?.path === codePath ? findCur : null}
              onDiff={() => switchMode("diff")}
              onBack={codeNav.at > 0 ? () => stepCode(-1) : null}
              onForward={codeNav.at < codeNav.stack.length - 1 ? () => stepCode(1) : null}
              scope={scope}
              preview={preview}
              onPreview={togglePreview}
              onOpenFile={openCode}
              media={codeEntry ? null : codeState?.media}
            />
          ) : (
            <div className="empty-state">
              <h2>Pick a file</h2>
              <p>{folder ? "The explorer lists every file in the folder." : "The explorer lists every file in the repository, with changed ones marked."}</p>
              <span className="keys">
                <span>
                  <kbd>{modKey}+P</kbd> open a file
                </span>
                {!folder && (
                  <span>
                    <kbd>[</kbd> <kbd>]</kbd> next changed file
                  </span>
                )}
              </span>
            </div>
          )}
        </div>

        {agentVisited && (
          <AgentView
            active={mode === "agent"}
            id={agentId}
            session={agent.sessions.find((x) => x.id === agentId)}
            agents={agents}
            newAgent={newAgent}
            onNewAgent={setNewAgent}
            modes={agent.modes}
            root={meta?.root || ""}
            view={view}
            contextLines={contextLines}
            wrap={wrap}
            threads={threads}
            attached={attachedHere}
            onAttach={(a, to = agentId) => attach(a, to)}
            onDetach={(key) => attachedFor(agentId, (list) => list.filter((x) => x.key !== key))}
            onClearAttached={() => attachedFor(agentId, () => [])}
            onRestoreAttached={(to, back) => attachedFor(to, (list) => [...back.filter((a) => !list.some((x) => x.key === a.key)), ...list])}
            onJump={(a) => (a.kind === "thread" ? onThreadAction({ type: "jump", thread: threads.find((t) => t.id === a.threadId) }) : goTo(a.file, a.start || 0))}
            onComment={createComment}
            onThreadAction={onThreadAction}
            onSymbol={onSymbol}
            onOpenFile={goTo}
            requests={requests}
            hooks={agent.hooks}
            onHooks={pickHooks}
            onSelect={selectSession}
            onNew={newSession}
            onStart={startSession}
            onStartAdded={openAdded}
            temporaryNew={temporaryNew}
            onTemporaryNew={setTemporaryNew}
            onClose={closeSession}
            onChanged={loadSessions}
            reveal={agentReveal}
            boxFocus={boxFocus}
            offline={offline}
          />
        )}
        {showComments && (
          <CommentsPanel
            tab={panelTab}
            onTab={setPanelTab}
            notes={{
              ...notes,
              composing: newNote,
              onComposing: setNewNote,
              onAdd: addNote,
              onPatch: patchNote,
              onDelete: deleteNote,
              onSend: sendNote,
              paused: notesPaused,
              onPaused: setNotesPaused,
            }}
            threads={threads}
            commentsPath={meta?.commentsPath || ""}
            onJump={(thread) => {
              if (phone) setPanel(null);
              onThreadAction({ type: "jump", thread });
            }}
            onThreadAction={onThreadAction}
            onAttach={(thread, to) => attach({ kind: "thread", threadId: thread.id }, to)}
            onSend={sendThreads}
            onDelete={deleteThreads}
            // Over the sidebar on the right, the two are one column, one width.
            widthVar={sideRight ? "--side-w" : "--comments-w"}
            onWidth={sideRight ? setSideWidth : setCommentsWidth}
            onClose={() => showPanel(false)}
          />
        )}
      </div>

      {windowed.length > 0 && (
        <AgentPrompt
          requests={windowed}
          open={promptOpen}
          focusId={promptFocus}
          view={view}
          contextLines={contextLines}
          wrap={wrap}
          onClose={closePrompt}
          onSymbol={onSymbol}
          onOpenFile={goTo}
          onOpenSession={(id) => {
            setPromptOpen(false);
            selectSession(id);
            switchMode("agent");
          }}
        />
      )}

      {boot.base && <FolderSwitcher mode={mode} />}
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
          key={stack.length}
          initialQuery={overlay.query || ""}
          seed={overlay.seed}
          source={overlay.source}
          from={overlay.from || activePath}
          opts={overlay.opts}
          place={overlay.place}
          onClose={closeOverlay}
          onBack={behind && goBack}
          backTo={trailLabel(behind)}
          onLeave={(left) => keepSearch(overlay, left)}
          onOpen={({ file, line }, { query, opts, place }) =>
            // Kept for the way back. The seed answered the query it came with, not an edited one.
            goTo(file, line, query === overlay.query ? { opts, place } : { query, opts, place, seed: undefined, source: undefined })
          }
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
          threads={threadsByFile.get(overlay.file) || NO_THREADS}
          changes={fileData[overlay.file]?.fd}
          wrap={wrap}
          preview={preview}
          onPreview={setPreview}
          onOpenFile={goTo}
          onComment={createComment}
          onThreadAction={onThreadAction}
          onAttach={attach}
          onSearch={openSearch}
        />
      )}
      {overlay?.type === "help" && <HelpOverlay onClose={closeOverlay} />}
      {overlay?.type === "actions" && (
        <ActionsPalette meta={meta} session={agentId} extra={pageActions} onRun={doAction} onEdit={() => openOverlay({ type: "settings", tab: "actions" })} onClose={closeOverlay} />
      )}
      {overlay?.type === "branch" && <BranchPalette meta={meta} onDone={(msg) => (closeOverlay(), say(msg))} onClose={closeOverlay} />}
      {overlay?.type === "settings" && (
        <SettingsOverlay
          theme={theme}
          onTheme={setTheme}
          view={view}
          onView={setView}
          contextLines={contextLines}
          onContext={setContextLines}
          wrap={wrap}
          onWrap={setWrap}
          phone={phone}
          notices={desktopNotices}
          onNotices={pickDesktopNotices}
          hooks={agent.available ? agent.hooks : null}
          onHooks={pickHooks}
          settings={settings}
          onChange={(patch) => setSettings((s) => ({ ...s, ...patch }))}
          onClose={closeOverlay}
          working={[activity, ...(elsewhere || []).map((f) => f.sessions)].flat().filter((s) => s?.busy && s.running === "dv").length}
          update={update}
          actions={<ActionsSettings />}
          tab={overlay.tab}
        />
      )}
    </div>
    </AttachTarget.Provider>
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
      // Against the pane that scrolls, or the margin never reaches past it.
      { root: el.closest(".content"), rootMargin: "800px 0px" },
    );
    io.observe(el);
    return () => io.disconnect();
  }, [entry.path, onNeed, state, hidden]);

  return (
    <div ref={ref} className="file-section">
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
  const lead = FILE_INSET + (root.querySelector(".file-head")?.offsetHeight ?? 40) + 60;
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

// The strip of ground kept over the diff and Code, which a file's header pins
// below (--inset in styles.css).
const FILE_INSET = 12;

// A file lands with its header where it will stay pinned.
const FILE_TOP = FILE_INSET;

// Batches of file diffs find in page asks for at once.
const FILES_PER_REQUEST = 50;

const sameMatch = (a, b) => a.path === b.path && a.side === b.side && a.line === b.line && a.start === b.start;

// inView is whether el sits in root's view clear of the pinned file header and
// the find bar floating under it.
const inView = (root, el) => {
  const box = root.getBoundingClientRect();
  const r = el.getBoundingClientRect();
  return r.top >= box.top + 80 && r.bottom <= box.bottom - 8;
};

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

// What the Back control says it leads to, so a step backwards is a known
// destination rather than a guess.
function trailLabel(o) {
  if (!o) return "";
  if (o.type === "file") return o.line ? `${o.file}:${o.line}` : o.file;
  if (o.type === "search") return o.query ? `search for ${o.query}` : "search";
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
    const below = root.getBoundingClientRect().top + FILE_INSET + (section.querySelector(".file-head")?.offsetHeight ?? 0);
    cell = [...section.querySelectorAll("[data-line][data-side]")].find((c) => c.getBoundingClientRect().bottom > below);
  }
  return cell ? { side: cell.dataset.side, line: Number(cell.dataset.line) } : null;
}
