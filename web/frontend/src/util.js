import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { slug } from "./boot.js";

export const cx = (...parts) => parts.filter(Boolean).join(" ");

// openFolderSession goes to another of the hub's folders, landing on the Agent
// view with one of its sessions picked: the folders share this origin, so its
// page reads both from here as it loads.
export function openFolderSession(to, session) {
  try {
    localStorage.setItem(`dv:${to}:agentSession`, JSON.stringify(session));
    localStorage.setItem(`dv:${to}:mode`, JSON.stringify("agent"));
  } catch {}
  location.href = `/${to}/`;
}

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
  const el = document.createElement("textarea");
  el.value = text;
  el.setAttribute("readonly", "");
  el.style.cssText = "position: fixed; opacity: 0";
  document.body.appendChild(el);
  el.select();
  const ok = document.execCommand("copy");
  el.remove();
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

// agentName names the agent a session, request or activity is with: "codex",
// or Claude's "".
export const agentName = (agent) => (agent === "codex" ? "Codex" : "Claude");

// workingLabel says which agents are at work in a folder, from its hub status.
export const workingLabel = (open) => (open.codex && open.claude ? "Claude and Codex are working" : `${open.codex ? "Codex" : "Claude"} is working`);

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
