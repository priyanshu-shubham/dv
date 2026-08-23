import { useCallback, useEffect, useRef, useState } from "react";

export const cx = (...parts) => parts.filter(Boolean).join(" ");

// usePersisted keeps a small preference in localStorage so the viewer opens the
// way you left it. Storage failures (private windows) degrade to in-memory.
export function usePersisted(key, initial) {
  const read = () => {
    try {
      const raw = localStorage.getItem("dv:" + key);
      return raw === null ? initial : JSON.parse(raw);
    } catch {
      return initial;
    }
  };
  const [value, setValue] = useState(read);
  const keyRef = useRef(key);

  // A changed key means we are now looking at different state — which files are
  // marked viewed belongs to one comparison, not to whichever opened first.
  useEffect(() => {
    if (keyRef.current === key) return;
    keyRef.current = key;
    setValue(read());
  }, [key]);

  const set = useCallback((v) => {
    setValue((prev) => {
      const next = typeof v === "function" ? v(prev) : v;
      try {
        localStorage.setItem("dv:" + keyRef.current, JSON.stringify(next));
      } catch {}
      return next;
    });
  }, []);
  return [value, set];
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
// diff exists it falls back to the declared stack, and that reading is not
// cached because it may not be the font that wins.
export function charWidth() {
  if (cachedCharWidth) return cachedCharWidth;
  const host = document.querySelector(".diff");
  const probe = document.createElement("span");
  probe.style.cssText =
    "position:absolute;visibility:hidden;white-space:pre" +
    (host ? "" : ";font:12px/20px ui-monospace,'SF Mono',Menlo,Consolas,monospace");
  probe.textContent = "0".repeat(100);
  (host || document.body).appendChild(probe);
  const w = probe.getBoundingClientRect().width / 100;
  probe.remove();
  if (!host) return w || 7.2;
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

export const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);
export const modKey = isMac ? "⌘" : "Ctrl";

// isTyping guards the single-key shortcuts so they do not fire while a comment
// is being written. A checkbox is not text entry: leaving focus on the Viewed
// box must not disable every shortcut on the page.
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

// LRM is a left-to-right mark. Elements that ellipsize a path at its *start*
// do it with direction:rtl, which otherwise moves leading punctuation to the
// end — ".dockerignore" renders as "dockerignore.". Prefixing the text with
// this mark pins the paragraph direction back to LTR.
export const LRM = "\u200e";

// splitPath separates a path into its directory and file name so the sidebar
// can dim the directory.
export function splitPath(p) {
  const i = p.lastIndexOf("/");
  return i < 0 ? ["", p] : [p.slice(0, i + 1), p.slice(i + 1)];
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
