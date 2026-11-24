import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { slug } from "./boot.js";

export const cx = (...parts) => parts.filter(Boolean).join(" ");

// openFolderSession goes to another of the hub's folders, landing on the Agent
// view with one of its sessions picked: the folders share this origin, so its
// page reads both from here as it loads.
export function openFolderSession(to, session) {
  pickSession(to, session);
  location.href = `/${to}/`;
}

// pickSession is where the page of the folder at slug ("" outside a hub) is
// to open, before it loads.
export function pickSession(slug, session) {
  const prefix = slug ? `dv:${slug}:` : "dv:";
  try {
    localStorage.setItem(prefix + "agentSession", JSON.stringify(session));
    localStorage.setItem(prefix + "mode", JSON.stringify("agent"));
    // The phone app reads this to open the session's screen, not just its tab.
    sessionStorage.setItem("dv:open", JSON.stringify({ slug, session }));
  } catch {}
}

// OUTPUT_KEY holds, in sessionStorage, the command action whose output the
// page loading next is to show.
export const OUTPUT_KEY = "dv:output";
// TERMINAL_KEY likewise holds the terminal it is to show.
export const TERMINAL_KEY = "dv:terminal";
// BOARD_KEY likewise says it is to show the board.
export const BOARD_KEY = "dv:board";

// usePersisted keeps a small preference in localStorage so the viewer opens the
// way you left it; `session` keeps it for the tab only. It is for what suits
// one screen: what should follow the reader to another device is in prefs.js.
// `folder` keeps it apart from the other folders a hub serves on the same
// origin. Storage failures (private windows) degrade to in-memory.
export function usePersisted(key, initial, { session = false, folder = false } = {}) {
  const store = () => (session ? sessionStorage : localStorage);
  const prefix = folder && slug ? `dv:${slug}:` : "dv:";
  const read = () => {
    try {
      const raw = store().getItem(prefix + key);
      return raw === null ? initial : JSON.parse(raw);
    } catch {
      return initial;
    }
  };
  const [value, setValue] = useState(read);
  const keyRef = useRef(key);

  // A changed key names different state, so it is read afresh rather than
  // carrying the old key's value across.
  useEffect(() => {
    if (keyRef.current === key) return;
    keyRef.current = key;
    setValue(read());
  }, [key]);

  const set = useCallback((v) => {
    setValue((prev) => {
      const next = typeof v === "function" ? v(prev) : v;
      try {
        store().setItem(prefix + keyRef.current, JSON.stringify(next));
      } catch {}
      return next;
    });
  }, []);
  return [value, set];
}

// useDismiss closes a menu on a press outside the element its ref is put on,
// or outside `also`, a menu drawn elsewhere in the page.
export function useDismiss(open, close, also) {
  const ref = useRef(null);
  useEffect(() => {
    if (!open) return;
    const onDown = (e) => {
      if (ref.current?.contains(e.target) || also?.current?.contains(e.target)) return;
      close();
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);
  return ref;
}

// menuRoom is where a menu h tall, opening from the button at r, goes between
// top and bottom: on its usual side if it fits there, else on the side with
// more room, and no taller than that room.
export function menuRoom(r, h, top, bottom, down) {
  const above = r.top - top - 12;
  const below = bottom - r.bottom - 12;
  if ((down ? below : above) < h) down = below > above;
  return { below: down, max: down ? below : above };
}

// useFixedMenu is the style of a menu drawn in the page's top layer, by the
// button ref is on, worked out once the menu is drawn and its height known:
// under the button, or over it where it would run off the window.
export function useFixedMenu(open, ref, menu) {
  const [style, setStyle] = useState(null);
  useLayoutEffect(() => {
    if (!open) return;
    const r = ref.current.getBoundingClientRect();
    const { below, max } = menuRoom(r, menu.current.scrollHeight, 0, window.innerHeight, true);
    const right = window.innerWidth - r.right;
    setStyle(below ? { top: r.bottom + 4, right, maxHeight: max } : { top: "auto", bottom: window.innerHeight - r.top + 4, right, maxHeight: max });
  }, [open]);
  return style;
}

// PHONE is the width dv lays itself out for a phone under, as styles.css has it.
export const PHONE = "(max-width: 760px)";
// A finger rather than a pointer: the browser has selection keys of its own there.
export const TOUCH = "(pointer: coarse)";
// PHONE_APP is where dv is the phone app rather than the page laid out small:
// a phone held upright, or turned on its side, where it is wide but short.
export const PHONE_APP = `${PHONE}, ${TOUCH} and (max-height: 500px)`;
// The hub folder the phone app was last in, which it opens on (main.jsx).
export const PHONE_FOLDER_KEY = "dv:phone-folder";

// limitWindow names a plan's usage limit, short and in full, by its window's
// length in minutes: the plan's where it says, else the one its place stands for.
export function limitWindow(limit, minutes) {
  const m = limit.window || minutes;
  if (m < 1440) {
    const h = Math.round(m / 60);
    return [`${h}h`, h === 5 ? "This five-hour window" : `This ${h}-hour window`];
  }
  const d = Math.round(m / 1440);
  return [`${d}d`, d === 7 ? "This week" : d === 30 ? "This month" : `These ${d} days`];
}

// resetsWhen is when a limit starts again, as closely as is of use: the time
// today, the day this week, else the date.
export function resetsWhen(at) {
  const t = new Date(at);
  if (t.toDateString() === new Date().toDateString()) return t.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
  if (t - Date.now() < 6 * 86400e3) return t.toLocaleString([], { weekday: "short", hour: "numeric", minute: "2-digit" });
  return t.toLocaleDateString([], { month: "short", day: "numeric" });
}

// useLasting is whether on has stayed on for ms of the page being seen: a
// connection that a locked phone or a dv restart drops has LOST_MS to come
// back before the page says it is lost.
export const LOST_MS = 4000;
export function useLasting(on, ms) {
  const [lasted, setLasted] = useState(false);
  useEffect(() => {
    if (!on) return setLasted(false);
    let timer = 0;
    const arm = () => {
      clearTimeout(timer);
      if (!document.hidden) timer = setTimeout(() => setLasted(true), ms);
    };
    arm();
    document.addEventListener("visibilitychange", arm);
    return () => {
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", arm);
    };
  }, [on, ms]);
  return on && lasted;
}

// useMedia is whether a media query matches, as the window changes.
export function useMedia(query) {
  const [on, setOn] = useState(() => matchMedia(query).matches);
  useEffect(() => {
    const m = matchMedia(query);
    const change = () => setOn(m.matches);
    change();
    m.addEventListener("change", change);
    return () => m.removeEventListener("change", change);
  }, [query]);
  return on;
}

// useDebounced delays a fast-changing value, used for search-as-you-type.
export function useDebounced(value, ms) {
  const [v, setV] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return v;
}

// useElementWidth tracks an element's rendered width, which the diff needs to
// know whether a side actually overflows and deserves a scrollbar.
export function useElementWidth(ref) {
  const [width, setWidth] = useState(0);
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    setWidth(el.getBoundingClientRect().width);
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(([entry]) => setWidth(entry.contentRect.width));
    ro.observe(el);
    return () => ro.disconnect();
  }, [ref]);
  return width;
}

// charWidth measures one character of the diff's monospace font. Everything is
// the same width in that font, so a line's pixel width is just its visual
// length times this — no per-line measuring.
let cachedCharWidth = 0;
// charWidth is the advance width of one monospace column, used to guess how
// wide a line will draw before it is rendered. Measured inside a real diff so
// it inherits whatever font the code actually resolved to; before the first
// diff exists, or while the code font is still loading, the reading is not
// cached because it may not be the font that wins.
export function charWidth() {
  if (cachedCharWidth) return cachedCharWidth;
  const host = document.querySelector(".diff");
  const probe = document.createElement("span");
  probe.style.cssText =
    "position:absolute;visibility:hidden;white-space:pre" +
    (host ? "" : ";font:12px/20px var(--font-mono)");
  probe.textContent = "0".repeat(100);
  (host || document.body).appendChild(probe);
  const w = probe.getBoundingClientRect().width / 100;
  probe.remove();
  if (!host || document.fonts?.status === "loading") return w || 7.2;
  cachedCharWidth = w;
  return cachedCharWidth || 7.2;
}

// visualLength is a line's length in columns, expanding tabs to 8-column stops
// the way the rendered text does.
export function visualLength(line) {
  let n = 0;
  for (let i = 0; i < line.length; i++) {
    if (line.charCodeAt(i) === 9) n += 8 - (n % 8);
    else n++;
  }
  return n;
}

// copyText puts text on the clipboard. The Clipboard API is only offered over
// https or on localhost, and a phone reaches dv over plain http, so without it
// the text is selected and copied the old way.
export async function copyText(text) {
  if (navigator.clipboard?.writeText) return navigator.clipboard.writeText(text);
  const back = document.activeElement; // a terminal copying on select is still typed in
  const el = document.createElement("textarea");
  el.value = text;
  el.setAttribute("readonly", "");
  el.style.cssText = "position: fixed; opacity: 0";
  document.body.appendChild(el);
  el.select();
  const ok = document.execCommand("copy");
  el.remove();
  back?.focus();
  if (!ok) throw new Error("The browser would not copy it.");
}

// useCopy is a click handler that copies text, and whether it just did. A
// click ending a selection leaves the selection to be copied instead.
export function useCopy(text) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const t = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(t);
  }, [copied]);
  const copy = useCallback(
    (e) => {
      if (window.getSelection()?.toString()) return;
      e?.stopPropagation();
      copyText(text).then(() => setCopied(true), () => {});
    },
    [text],
  );
  return [copied, copy];
}

export const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);
export const modKey = isMac ? "⌘" : "Ctrl";

export const isSearchKey = (e) =>
  (e.metaKey || e.ctrlKey) && (e.key.toLowerCase() === "k" || (e.shiftKey && e.key.toLowerCase() === "f"));

export const isFindKey = (e) => (e.metaKey || e.ctrlKey) && !e.shiftKey && !e.altKey && e.key.toLowerCase() === "f";

// searchSeed is what a selection puts in the search box. Search matches within
// a line, so a selection over several lines puts nothing there.
export function searchSeed(text) {
  const t = (text || "").trim();
  return t.includes("\n") ? "" : t;
}

// excerptOf is the text selected for a comment, or "" when it is all of the
// lines quoted, which say it already. Only letters and digits are compared, so
// indents and a rendered Markdown block's markup do not make it narrower.
export function excerptOf(text, quote) {
  const t = (text || "").trim();
  const bare = (s) => s.replace(/[^\p{L}\p{N}]/gu, "");
  return t && bare(t) !== bare((quote || []).join("\n")) ? t : "";
}

export const attr = (s) => String(s ?? "").replace(/[&"<>]/g, (c) => ({ "&": "&amp;", '"': "&quot;", "<": "&lt;", ">": "&gt;" })[c]);

// selectedAttr is an excerpt as the attribute an agent reads it in.
export const selectedAttr = (excerpt) => (excerpt ? ` selected="${attr(excerpt)}"` : "");

// isTyping guards the single-key shortcuts so they do not fire while a comment
// is being written. A checkbox is not text entry: leaving focus on one must not
// disable every shortcut on the page.
const NON_TEXT_INPUTS = new Set([
  "checkbox", "radio", "button", "submit", "reset", "range", "color", "file", "image",
]);

export function isTyping(el) {
  if (!el) return false;
  if (el.isContentEditable) return true;
  const tag = el.tagName;
  if (tag === "TEXTAREA" || tag === "SELECT") return true;
  return tag === "INPUT" && !NON_TEXT_INPUTS.has(el.type);
}

// selecting is text selected on the page, which Shift and an arrow extend.
export const selecting = () => !(window.getSelection()?.isCollapsed ?? true);

export const statusLabel = {
  A: "added",
  M: "modified",
  D: "deleted",
  R: "renamed",
  T: "type changed",
};

// statusLetter is the letter VS Code decorates a changed file with, where an
// untracked file is U rather than A.
export const statusLetter = (f) => (f.untracked ? "U" : f.status);

// LRM is a left-to-right mark. Elements that ellipsize a path at its *start*
// do it with direction:rtl, which otherwise moves leading punctuation to the
// end — ".dockerignore" renders as "dockerignore.". Prefixing the text with
// this mark pins the paragraph direction back to LTR.
export const LRM = "\u200e";

// plainText is Markdown flattened to a line of what it says, for a preview of
// a reply: its words without the marks, headings, list bullets, quote bars,
// table rules and fences, a link as its text. The server has made it one line
// already, so a bullet or quote is only taken where a line could have begun:
// at the start or after a sentence, which leaves 3 - 2 and a > b alone.
export const plainText = (s) =>
  (s || "")
    .replace(/```\w*/g, " ")
    .replace(/!?\[([^\]]*)\]\([^)]*\)/g, "$1")
    .replace(/\|?(?:\s*:?-{3,}:?\s*\|)+\s*(?::?-{3,}:?)?/g, " ")
    .replace(/\s*\|\s*/g, " · ")
    .replace(/(^|\s)#{1,6}(?=\s)/g, "$1")
    .replace(/(^|[.:!?]\s+)(?:>|[-*+]|\d+[.)])(?=\s)/g, "$1")
    .replace(/(\*\*|__)(?=\S)(.+?)(?<=\S)\1/g, "$2")
    .replace(/(^|[\s(])([*_])(?=\S)(.+?)(?<=\S)\2(?=[\s).,;:!?]|$)/g, "$1$3")
    .replace(/`([^`]*)`/g, "$1")
    .replace(/(?:\s*·\s*){2,}/g, " · ")
    .replace(/^\s*·\s*|\s*·\s*$/g, "")
    .replace(/\s{2,}/g, " ")
    .trim();

// agentName names who runs an action's prompt: "codex" for `codex exec`, else Claude.
export const agentName = (agent) => (agent === "codex" ? "Codex" : agent === "agy" ? "Antigravity" : "Claude");

// Only followed in dv: another process has it open and dv was not let in too.
export const heldElsewhere = (s) => s?.running === "terminal" && !s.shared;

// byLastSent orders open sessions the last written to first. By their last
// activity instead, those at work would trade places as they go. Without the
// time, the session's last activity stands in.
export const byLastSent = (a, b) => Date.parse(b.sent || b.updated) - Date.parse(a.sent || a.updated);

export const workingLabel = () => "Claude is working";

// modelSource is where a model on the picker comes from, for its editor: ""
// for Claude Code's own.
export const modelSource = (m) =>
  m.source || (m.id.startsWith("codex/") ? "Codex" : m.id.startsWith("gemini/") ? "Gemini" : m.added ? "Added" : "");

// openSettings opens Settings on tab, from wherever the page is: App opens it.
// With to, a selector, it is scrolled to that once there, for a second at most.
export function openSettings(tab, to) {
  window.dispatchEvent(new CustomEvent("dv:settings", { detail: { tab } }));
  if (!to) return;
  const until = Date.now() + 1000;
  const find = () => {
    const el = document.querySelector(to);
    if (el) el.scrollIntoView({ block: "start" });
    else if (Date.now() < until) setTimeout(find, 50);
  };
  setTimeout(find, 50);
}

// inOrder is items as order (ids) arranges them. One order doesn't name - a
// model released since it was set - keeps its place after the item it
// follows by default, rather than going to the end.
export function inOrder(items, order, id = (x) => x.id) {
  const at = new Map(order.map((k, i) => [k, i]));
  const out = items.filter((x) => at.has(id(x))).sort((a, b) => at.get(id(a)) - at.get(id(b)));
  items.forEach((x, i) => {
    if (at.has(id(x))) return;
    const before = i > 0 ? out.indexOf(items[i - 1]) : -1;
    out.splice(before + 1, 0, x);
  });
  return out;
}

// useReorder drags the rows of a list into a new order: grip(id) goes on a
// row's handle, row(id) is its ref, shown is the ids as they stand while one
// is dragged, and onOrder hears the order once it is let go. The handle also
// moves its row with ↑ and ↓.
export function useReorder(ids, onOrder) {
  const [drag, setDrag] = useState(null); // { id, order }
  const dragRef = useRef(null);
  dragRef.current = drag;
  const rows = useRef(new Map());
  // A row held near the edge of what scrolls it scrolls on, while the finger
  // or pointer stays still: a list longer than the screen is otherwise stuck.
  const edge = useRef(null); // { box, y, frame }
  const stopEdge = () => {
    cancelAnimationFrame(edge.current?.frame);
    edge.current = null;
  };
  useEffect(() => stopEdge, []);
  const move = (order, id, to) => {
    const rest = order.filter((x) => x !== id);
    return [...rest.slice(0, to), id, ...rest.slice(to)];
  };
  const place = (id, y) => {
    const d = dragRef.current;
    if (!d) return;
    const rest = d.order.filter((x) => x !== id);
    let to = rest.findIndex((x) => {
      const r = rows.current.get(x)?.getBoundingClientRect();
      return r && y < r.top + r.height / 2;
    });
    if (to < 0) to = rest.length;
    const order = move(d.order, id, to);
    if (order.join("\n") !== d.order.join("\n")) setDrag({ id, order });
  };
  const scrollEdge = (id) => {
    const a = edge.current;
    if (!a) return;
    const r = a.box.getBoundingClientRect();
    const near = 48;
    const by = a.y < r.top + near ? a.y - r.top - near : a.y > r.bottom - near ? a.y - r.bottom + near : 0;
    if (by) {
      const was = a.box.scrollTop;
      a.box.scrollTop += Math.round(by / 4);
      if (a.box.scrollTop !== was) place(id, a.y);
    }
    a.frame = requestAnimationFrame(() => scrollEdge(id));
  };
  const grip = (id) => ({
    onPointerDown: (e) => {
      if (e.button !== 0) return;
      e.preventDefault();
      e.currentTarget.setPointerCapture(e.pointerId);
      setDrag({ id, order: ids });
      let box = e.currentTarget.parentElement;
      while (box && !(box.scrollHeight > box.clientHeight && /auto|scroll/.test(getComputedStyle(box).overflowY))) box = box.parentElement;
      stopEdge();
      if (box) {
        edge.current = { box, y: e.clientY };
        scrollEdge(id);
      }
    },
    onPointerMove: (e) => {
      if (edge.current) edge.current.y = e.clientY;
      place(id, e.clientY);
    },
    onPointerUp: () => {
      stopEdge();
      const d = dragRef.current;
      setDrag(null);
      if (d && d.order.join("\n") !== ids.join("\n")) onOrder(d.order);
    },
    onPointerCancel: () => (stopEdge(), setDrag(null)),
    onKeyDown: (e) => {
      const step = e.key === "ArrowUp" ? -1 : e.key === "ArrowDown" ? 1 : 0;
      const to = ids.indexOf(id) + step;
      if (!step || to < 0 || to >= ids.length) return;
      e.preventDefault();
      e.stopPropagation();
      onOrder(move(ids, id, to));
    },
  });
  const row = (id) => (el) => (el ? rows.current.set(id, el) : rows.current.delete(id));
  return { shown: drag?.order || ids, dragging: drag?.id, grip, row };
}

// splitPath separates a path into its directory and file name so the sidebar
// can dim the directory.
export function splitPath(p) {
  const i = p.lastIndexOf("/");
  return i < 0 ? ["", p] : [p.slice(0, i + 1), p.slice(i + 1)];
}

// globMatcher compiles a comma-separated list of globs into a path test, or
// null for an empty list. It speaks the search panel's dialect - a pattern
// without a slash matches a name anywhere, one with a slash is anchored at the
// root, `**` spans folders - plus one thing a file filter needs: a pattern that
// names a folder matches everything inside it.
export function globMatcher(list) {
  const res = list
    .split(",")
    .map((g) => g.trim())
    .filter(Boolean)
    .map(globRegExp);
  return res.length ? (path) => res.some((re) => re.test(path)) : null;
}

// applyFileFilters is a diff's files as the repository's filters leave them:
// generated ones hidden if asked, and the include and exclude patterns.
// Paused, every file stays, the counts still of what would be hidden; keep,
// if given, is a file that stays whatever the filters say.
export function applyFileFilters(all, { hideGenerated, pathFilter, paused, keep }) {
  const include = globMatcher(pathFilter.include);
  const exclude = globMatcher(pathFilter.exclude);
  let filteredOut = 0;
  let hiddenGenerated = 0;
  const files = all.filter((f) => {
    if (f.path === keep) return true;
    if (hideGenerated && f.generated) {
      hiddenGenerated++;
      return paused;
    }
    const shown = (!include || include(f.path)) && !exclude?.(f.path);
    if (!shown) filteredOut++;
    return shown || paused;
  });
  return { files, filteredOut, hiddenGenerated };
}

// listFilter compiles the file list's filter box, or null when it is empty:
// comma-separated terms, each a piece of the path to look for or, given a * or
// ?, a glob as globMatcher reads one. A term starting with ! hides what it
// matches instead, so `*.go, !*_test.go` is the Go files but the tests.
export function listFilter(text) {
  const terms = text
    .split(",")
    .map((t) => t.trim())
    .filter((t) => t.replace(/^!/, ""));
  if (!terms.length) return null;
  const term = (t) => {
    if (/[*?]/.test(t)) {
      const re = globRegExp(t);
      return (path) => re.test(path);
    }
    const piece = t.toLowerCase();
    return (path) => path.toLowerCase().includes(piece);
  };
  const shown = terms.filter((t) => !t.startsWith("!")).map(term);
  const hidden = terms.filter((t) => t.startsWith("!")).map((t) => term(t.slice(1)));
  return (path) => (!shown.length || shown.some((f) => f(path))) && !hidden.some((f) => f(path));
}

function globRegExp(glob) {
  // A trailing slash only says "folder"; any other slash anchors the pattern.
  let src = glob.slice(0, -1).includes("/") ? "^" : "(?:^|/)";
  const body = glob.replace(/^\.?\//, "").replace(/\/$/, "");
  for (let i = 0; i < body.length; i++) {
    if (body.startsWith("**/", i)) {
      src += "(?:.*/)?";
      i += 2;
    } else if (body.startsWith("**", i)) {
      src += ".*";
      i++;
    } else if (body[i] === "*") src += "[^/]*";
    else if (body[i] === "?") src += "[^/]";
    else src += body[i].replace(/[.+^${}()|[\]\\]/g, "\\$&");
  }
  return new RegExp(src + "(?:/|$)", "i");
}

// duration says how long something took: "0.4s", "12s", "2m 5s", "1h 3m".
// whole leaves out the tenths, for a count still going up.
export function duration(ms, whole = false) {
  const s = ms / 1000;
  if (s < 10 && !whole) return `${s.toFixed(1)}s`;
  if (s < 60) return `${Math.floor(s)}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ${Math.floor(s % 60)}s`;
  return `${Math.floor(m / 60)}h ${m % 60}m`;
}

export function relTime(iso) {
  if (!iso) return "";
  const then = new Date(iso).getTime();
  const secs = Math.max(1, Math.round((Date.now() - then) / 1000));
  if (secs < 60) return "just now";
  const mins = Math.round(secs / 60);
  if (mins < 60) return `${mins}m ago`;
  const hrs = Math.round(mins / 60);
  if (hrs < 24) return `${hrs}h ago`;
  const days = Math.round(hrs / 24);
  if (days < 30) return `${days}d ago`;
  return new Date(iso).toLocaleDateString();
}

// Input that means the reader is steering, which a jump must give way to.
const INTERRUPTS = ["wheel", "touchstart", "keydown", "mousedown"];

// yOf is the scroll position that puts el `offset` px below the top of root.
export const yOf = (root, el, offset) =>
  root.scrollTop + el.getBoundingClientRect().top - root.getBoundingClientRect().top - offset;

// chase scrolls root to aim().y every frame until it has held still on a final
// target for a moment. One scroll is not enough: a file the reader has not
// reached is an empty stub until its diff loads, so the page is far shorter
// than it will be and a jump near the end is clamped part-way, and each
// correction brings more files into range, which loads them, which moves the
// target again. It gives up the moment the reader steers - watching input, not
// the scroll position, which scroll anchoring moves on its own as content loads.
export function chase(root, aim) {
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

// laidOut is whether el is drawn where it says: inside a file the browser is
// skipping (content-visibility), its box is not where it will be once shown.
export const laidOut = (el) => !!el && (el.checkVisibility?.({ contentVisibilityAuto: true }) ?? true);
