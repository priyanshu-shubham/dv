// Live updates: the page polls a cheap fingerprint of the repository and folds
// what changed into the diff, keeping the reader's place while it does.
import { useEffect, useRef } from "react";
import { api } from "./api.js";
import { cssId } from "./FileDiff.jsx";

const POLL_MS = 1500;

// useVersionPoll calls onChange when the repository's fingerprint moves off
// versionRef, the one the diff on screen was listed at, and hands every poll's
// versions of the comments and viewed marks to onNotes. A slow repository is
// polled less often, and a hidden tab not at all until it is shown again.
export function useVersionPoll(versionRef, onChange, onNotes) {
  const cb = useRef(onChange);
  cb.current = onChange;
  const notes = useRef(onNotes);
  notes.current = onNotes;

  useEffect(() => {
    let timer = null;
    let running = false;
    let alive = true;

    const tick = async () => {
      timer = null;
      if (document.hidden) return;
      running = true;
      let wait = POLL_MS;
      try {
        const t0 = performance.now();
        const { version, ...rest } = await api.version();
        wait = Math.max(POLL_MS, (performance.now() - t0) * 5);
        notes.current(rest);
        if (version !== versionRef.current) {
          // One attempt per version: a listing that keeps failing must not
          // turn into a reload every tick.
          const had = versionRef.current;
          versionRef.current = version;
          if (had) await cb.current();
        }
      } catch {}
      running = false;
      if (alive && !document.hidden) timer = setTimeout(tick, wait);
    };

    const onVisibility = () => {
      if (document.hidden || running) return;
      clearTimeout(timer);
      tick();
    };

    timer = setTimeout(tick, POLL_MS);
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      alive = false;
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [versionRef]);
}

// captureAnchor notes the diff line nearest the top of the view and how far
// down it sits. A file header is the fallback: it pins the view to a file but
// not to a place within one.
export function captureAnchor(root) {
  if (!root) return null;
  const box = root.getBoundingClientRect();
  // Both columns: in split view one of them is blank beside a pure insertion
  // or deletion, and the old one holds still when only the working tree moved.
  const xs = [box.left + box.width * 0.25, box.left + box.width * 0.75];
  let fallback = null;
  for (let y = box.top + 8; y < box.top + box.height / 2; y += 12) {
    for (const x of xs) {
      const el = document.elementFromPoint(x, y);
      const section = el?.closest?.("section.file");
      if (!section) continue;
      const at = { path: section.dataset.path, sectionTop: section.getBoundingClientRect().top - box.top };
      const cell = el.closest("[data-line][data-side]");
      if (cell) {
        return { ...at, side: cell.dataset.side, line: Number(cell.dataset.line), top: cell.getBoundingClientRect().top - box.top };
      }
      fallback ||= at;
    }
  }
  return fallback;
}

// restoreAnchor scrolls the anchored line, or its file, back to where it was.
export function restoreAnchor(root, a) {
  const section = document.getElementById("file-" + cssId(a.path));
  if (!root || !section) return;
  const cell = a.line ? section.querySelector(`[data-side="${a.side}"][data-line="${a.line}"]`) : null;
  const el = cell || section;
  const off = el.getBoundingClientRect().top - root.getBoundingClientRect().top - (cell ? a.top : a.sectionTop);
  // The container scrolls smoothly by default, which here would read as the
  // page drifting on its own.
  if (Math.abs(off) >= 1) root.scrollTo({ top: root.scrollTop + off, behavior: "instant" });
}
