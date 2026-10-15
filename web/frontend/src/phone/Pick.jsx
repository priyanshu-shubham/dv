// Picking lines by touch, in the phone app: held, a line is picked and the
// lines down to the finger as it drags; tapped while picking, a line widens the
// range to it. A bar over the screen, as Android's contextual bar is, says
// what to do with them. The diff's own selection draws what is picked.
import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { codeHunks } from "../hunks.js";
import { say } from "../Notices.jsx";
import { useBack } from "./back.js";
import { Icon } from "./icons.jsx";

const HOLD_MS = 450;
const SLOP_PX = 10;
// Near the top or bottom of what scrolls, a drag scrolls it on.
const EDGE_PX = 56;

let current = null; // the cancel of whichever diff is picking

// endPicking puts away whatever lines are picked, wherever they are.
export const endPicking = () => current?.();

// usePick follows the fingers on root, a diff's rows, while on. It returns
// whether lines are being picked, and what ends that.
export function usePick(rootRef, on, selection, setSelection) {
  const [picking, setPicking] = useState(false);
  const sel = useRef(selection);
  sel.current = selection;
  const mine = useRef(false);
  mine.current = picking;
  const stop = useRef();
  stop.current = () => {
    setPicking(false);
    setSelection(null);
  };
  useBack(picking, () => stop.current());
  // Taken by the composer, or cleared anywhere else, it is picked no more.
  useEffect(() => {
    if (!selection) setPicking(false);
  }, [selection]);
  // One diff picks at a time: starting in another ends this one.
  useEffect(() => {
    if (!picking) return;
    const end = () => stop.current();
    current?.();
    current = end;
    return () => {
      if (current === end) current = null;
    };
  }, [picking]);

  useEffect(() => {
    const root = rootRef.current;
    if (!root || !on) return;
    let hold = null; // { x, y, timer, side, anchor, dragging }
    let scroll = 0;
    const lineOf = (el) => {
      const cell = el?.closest?.("[data-line][data-side]");
      return cell && root.contains(cell) && !cell.closest(".row-threads, .peek") ? cell : null;
    };
    const widen = (side, no) => {
      const s = sel.current;
      if (!s || s.side !== side) return setSelection({ side, start: no, end: no });
      setSelection({ side, start: Math.min(s.start, no), end: Math.max(s.end, no) });
    };
    const onStart = (e) => {
      if (e.touches.length !== 1) return void (hold && clearTimeout(hold.timer), (hold = null));
      const cell = lineOf(e.target);
      if (!cell) return;
      const t = e.touches[0];
      const side = cell.dataset.side;
      const no = Number(cell.dataset.line);
      hold = { x: t.clientX, y: t.clientY, side, anchor: no, dragging: false };
      hold.timer = setTimeout(() => {
        if (!hold) return;
        navigator.vibrate?.(12);
        window.getSelection()?.removeAllRanges();
        hold.dragging = true;
        setPicking(true);
        setSelection({ side, start: no, end: no });
      }, HOLD_MS);
    };
    const follow = (x, y) => {
      const cell = lineOf(document.elementFromPoint(x, y));
      if (!cell || cell.dataset.side !== hold.side) return;
      const no = Number(cell.dataset.line);
      setSelection({ side: hold.side, start: Math.min(hold.anchor, no), end: Math.max(hold.anchor, no) });
    };
    const onMove = (e) => {
      if (!hold) return;
      const t = e.touches[0];
      if (!hold.dragging) {
        if (Math.hypot(t.clientX - hold.x, t.clientY - hold.y) > SLOP_PX) {
          clearTimeout(hold.timer);
          hold = null;
        }
        return;
      }
      // The finger is picking, not scrolling.
      e.preventDefault();
      hold.x = t.clientX;
      hold.y = t.clientY;
      follow(t.clientX, t.clientY);
      const scroller = root.closest(".content, .agent-scroll, .prompt-body");
      cancelAnimationFrame(scroll);
      if (!scroller) return;
      const box = scroller.getBoundingClientRect();
      const by = t.clientY < box.top + EDGE_PX ? -8 : t.clientY > box.bottom - EDGE_PX ? 8 : 0;
      if (!by) return;
      const step = () => {
        if (!hold?.dragging) return;
        scroller.scrollTop += by;
        follow(hold.x, hold.y);
        scroll = requestAnimationFrame(step);
      };
      scroll = requestAnimationFrame(step);
    };
    const onEnd = (e) => {
      cancelAnimationFrame(scroll);
      const h = hold;
      hold = null;
      if (!h) return;
      clearTimeout(h.timer);
      // The lift after a hold would click whatever is under the finger.
      if (h.dragging) return void e.preventDefault();
      // A tap while picking widens the range to the line tapped.
      if (mine.current && sel.current) {
        e.preventDefault();
        widen(h.side, h.anchor);
      }
    };
    const onMenu = (e) => lineOf(e.target) && e.preventDefault();
    root.addEventListener("touchstart", onStart, { passive: true });
    root.addEventListener("touchmove", onMove, { passive: false });
    root.addEventListener("touchend", onEnd);
    root.addEventListener("touchcancel", onEnd);
    root.addEventListener("contextmenu", onMenu);
    return () => {
      cancelAnimationFrame(scroll);
      if (hold) clearTimeout(hold.timer);
      root.removeEventListener("touchstart", onStart);
      root.removeEventListener("touchmove", onMove);
      root.removeEventListener("touchend", onEnd);
      root.removeEventListener("touchcancel", onEnd);
      root.removeEventListener("contextmenu", onMenu);
    };
  }, [rootRef, on, setSelection]);

  return [picking && !!selection, () => stop.current()];
}

// hunkOf is the change a picked range is in: the run of changed lines holding
// its first line, on the side picked, or the other if this one has none of it.
export function hunkOf(fd, { side, start }) {
  const at = start - 1;
  const run = codeHunks(fd).find((r) => r.some((l) => (side === "old" ? l.o : l.n) === at));
  if (!run) return null;
  const range = (key, s) => {
    const nos = run.map((l) => l[key]).filter((n) => n >= 0);
    return nos.length ? { side: s, start: Math.min(...nos) + 1, end: Math.max(...nos) + 1 } : null;
  };
  return side === "old" ? range("o", "old") || range("n", "new") : range("n", "new") || range("o", "old");
}

// PickBar is what picked lines can be: commented on, added to the next
// message, copied, or widened to their change.
export function PickBar({ selection, lines, onHunk, onComment, onAttach, onClose }) {
  const n = selection.end - selection.start + 1;
  const copy = () =>
    navigator.clipboard?.writeText(lines.join("\n")).then(
      () => say(n === 1 ? "Line copied" : `${n} lines copied`),
      () => say("Could not copy"),
    );
  return createPortal(
    <div className="p-pickbar" role="toolbar">
      <button className="p-icon" onClick={onClose} aria-label="Done">
        <Icon name="x" />
      </button>
      <span className="p-pickbar-title">{n === 1 ? `Line ${selection.start}` : `${n} lines`}</span>
      {onHunk && (
        <button className="p-icon" onClick={onHunk} aria-label="The whole change">
          <Icon name="hunk" />
        </button>
      )}
      <button className="p-icon" onClick={copy} aria-label="Copy">
        <Icon name="copy" />
      </button>
      {onAttach && (
        <button className="p-icon" onClick={onAttach} aria-label="Add to your next message">
          <Icon name="spark" />
        </button>
      )}
      {onComment && (
        <button className="p-icon tonal" onClick={onComment} aria-label="Comment">
          <Icon name="comment" />
        </button>
      )}
    </div>,
    document.body,
  );
}
