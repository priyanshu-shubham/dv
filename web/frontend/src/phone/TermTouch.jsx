// A terminal under a finger, in the phone app. xterm.js knows only the mouse,
// so the touches are dv's: a drag goes through the scrollback, or to a
// full-screen program as the wheel would, and flung goes on; two fingers set
// the text's size; held, a finger picks the word under it, which the handles
// at its ends, or the finger still down, widen. A tap raises the keyboard.
import { useEffect, useReducer, useRef } from "react";
import { createPortal } from "react-dom";
import { say } from "../Notices.jsx";
import { copyText } from "../util.js";
import { useBack } from "./back.js";
import { Icon } from "./icons.jsx";

export const FONT_MIN = 9;
export const FONT_MAX = 20;
const HOLD_MS = 450;
const SLOP_PX = 10;
// A handle is held above the finger, to see the cell it is on.
const LIFT_PX = 24;

// cells is the screen's place on the page and the size of a cell.
function cells(term) {
  const r = term.element.querySelector(".xterm-screen").getBoundingClientRect();
  return { r, w: r.width / term.cols, h: r.height / term.rows };
}

// cellAt is the cell under a point, its row counted from the scrollback's top.
function cellAt(term, x, y) {
  const { r, w, h } = cells(term);
  const clamp = (n, hi) => Math.max(0, Math.min(hi, n));
  return {
    col: clamp(Math.floor((x - r.left) / w), term.cols - 1),
    row: clamp(Math.floor((y - r.top) / h), term.rows - 1) + term.buffer.active.viewportY,
  };
}

const before = (a, b) => a.row < b.row || (a.row === b.row && a.col <= b.col);

// wordAt is the run of non-blank characters at a cell, or the cell alone.
function wordAt(term, at) {
  const line = term.buffer.active.getLine(at.row)?.translateToString(false) || "";
  const word = (c) => !!c && /\S/.test(c);
  if (!word(line[at.col])) return { a: at, b: at };
  let s = at.col;
  let e = at.col;
  while (s > 0 && word(line[s - 1])) s--;
  while (e < line.length - 1 && word(line[e + 1])) e++;
  return { a: { col: s, row: at.row }, b: { col: e, row: at.row } };
}

export function selectCells(term, { a, b }) {
  const [s, e] = before(a, b) ? [a, b] : [b, a];
  term.select(s.col, s.row, (e.row - s.row) * term.cols + e.col - s.col + 1);
}

// touchTerm follows the fingers on el, term's pane, until what it returns is
// called. picked is told the cells picked, null once none are; sized the
// text's size as two fingers set it, done when they lift.
export function touchTerm(el, term, { picked, sized }) {
  let one = null; // { x, y, last, hold, mode: null | "scroll" | "pick", anchor, v, at }
  let two = null; // { dist, size }
  let fling = 0;
  let has = null; // the cells picked
  const pick = (p) => {
    has = p;
    if (p) selectCells(term, p);
    else term.clearSelection();
    picked(p);
  };

  // A drag moves through the scrollback; with a program that has the whole
  // screen or the mouse, it goes to the program as the wheel's turns.
  let part = 0;
  const scroll = (px, x, y) => {
    const { h } = cells(term);
    part += px / h;
    const lines = Math.trunc(part);
    part -= lines;
    if (!lines) return;
    if (term.buffer.active.type === "normal" && term.modes.mouseTrackingMode === "none") return term.scrollLines(-lines);
    for (let i = 0; i < Math.abs(lines); i++)
      term.element.dispatchEvent(new WheelEvent("wheel", { deltaY: lines > 0 ? -1 : 1, deltaMode: 1, clientX: x, clientY: y, bubbles: true, cancelable: true }));
  };
  const stopFling = () => {
    cancelAnimationFrame(fling);
    fling = 0;
  };
  const flingOn = (v, x, y) => {
    let at = performance.now();
    const step = (now) => {
      const dt = now - at;
      at = now;
      v *= Math.pow(0.995, dt);
      if (Math.abs(v) < 0.05) return void (fling = 0);
      scroll(v * dt, x, y);
      fling = requestAnimationFrame(step);
    };
    fling = requestAnimationFrame(step);
  };

  const spread = (t) => Math.hypot(t[0].clientX - t[1].clientX, t[0].clientY - t[1].clientY);
  const onStart = (e) => {
    stopFling();
    if (e.touches.length === 2) {
      if (one) clearTimeout(one.hold);
      one = null;
      two = { dist: spread(e.touches), size: term.options.fontSize };
      return;
    }
    if (e.touches.length !== 1) return;
    const { clientX: x, clientY: y } = e.touches[0];
    one = { x, y, last: y, mode: null, v: 0, at: performance.now() };
    one.hold = setTimeout(() => {
      if (!one || one.mode) return;
      one.mode = "pick";
      navigator.vibrate?.(12);
      const w = wordAt(term, cellAt(term, x, y));
      one.anchor = w.a;
      pick(w);
    }, HOLD_MS);
  };
  const onMove = (e) => {
    if (two && e.touches.length === 2) {
      e.preventDefault();
      const size = Math.round(Math.max(FONT_MIN, Math.min(FONT_MAX, (two.size * spread(e.touches)) / two.dist)));
      if (size !== term.options.fontSize) sized(size, false);
      return;
    }
    if (!one) return;
    const { clientX: x, clientY: y } = e.touches[0];
    e.preventDefault();
    if (!one.mode) {
      if (Math.hypot(x - one.x, y - one.y) < SLOP_PX) return;
      clearTimeout(one.hold);
      if (Math.abs(y - one.y) < Math.abs(x - one.x)) return void (one = null);
      one.mode = "scroll";
    }
    if (one.mode === "pick") return pick({ a: one.anchor, b: cellAt(term, x, y) });
    const now = performance.now();
    const dy = y - one.last;
    one.v = dy / Math.max(1, now - one.at);
    one.last = y;
    one.at = now;
    scroll(dy, x, y);
  };
  const onEnd = (e) => {
    if (two) {
      if (e.touches.length < 2) {
        two = null;
        sized(term.options.fontSize, true);
      }
      return;
    }
    const was = one;
    one = null;
    if (!was) return;
    clearTimeout(was.hold);
    // What a finger did is not also a click.
    if (was.mode) e.preventDefault();
    if (was.mode === "scroll" && performance.now() - was.at < 80) flingOn(was.v, was.x, was.last);
    if (was.mode) return;
    // A tap: put away what is picked, else type.
    if (has) {
      e.preventDefault();
      pick(null);
    } else term.focus();
  };
  const onCancel = () => {
    if (one) clearTimeout(one.hold);
    one = two = null;
  };
  const noMenu = (e) => e.preventDefault();
  el.addEventListener("touchstart", onStart, { passive: true });
  el.addEventListener("touchmove", onMove, { passive: false });
  el.addEventListener("touchend", onEnd, { passive: false });
  el.addEventListener("touchcancel", onCancel);
  el.addEventListener("contextmenu", noMenu);
  return {
    pick,
    stop() {
      stopFling();
      onCancel();
      el.removeEventListener("touchstart", onStart);
      el.removeEventListener("touchmove", onMove);
      el.removeEventListener("touchend", onEnd);
      el.removeEventListener("touchcancel", onCancel);
      el.removeEventListener("contextmenu", noMenu);
    },
  };
}

// Picked is what is picked in a terminal: a handle at either end, dragged to
// widen or narrow it, and the bar of what to do with it. onAsk puts it in a
// session's message box.
export function Picked({ term, picked, onPick, onAsk }) {
  const [, redraw] = useReducer((n) => n + 1, 0);
  // Where the handles go follows the scrollback.
  useEffect(() => {
    const a = term.onScroll(redraw);
    const b = term.onResize(redraw);
    return () => (a.dispose(), b.dispose());
  }, [term]);
  useBack(true, () => onPick(null));
  const view = term.element.parentElement.parentElement; // .term-view, which the handles are placed in
  const { r, w, h } = cells(term);
  const o = view.getBoundingClientRect();
  const [s, e] = before(picked.a, picked.b) ? [picked.a, picked.b] : [picked.b, picked.a];
  const top = term.buffer.active.viewportY;
  const place = (c, end) => {
    const y = c.row - top + 1;
    if (y < 1 || y > term.rows) return null;
    return { left: r.left - o.left + (end ? c.col + 1 : c.col) * w, top: r.top - o.top + y * h };
  };
  const text = term.getSelection();
  const lines = text.split("\n").length;

  return (
    <>
      <Handle at={place(s, false)} side="start" term={term} onMove={(c) => onPick({ a: c, b: e })} />
      <Handle at={place(e, true)} side="end" term={term} onMove={(c) => onPick({ a: s, b: c })} />
      {createPortal(
        <div className="p-pickbar at-keys" role="toolbar">
          <button className="p-icon small" onClick={() => onPick(null)} aria-label="Done">
            <Icon name="x" size={20} />
          </button>
          <span className="p-pickbar-title">{lines > 1 ? `${lines} lines` : `${text.length} characters`}</span>
          <button
            className="p-icon small"
            onClick={() =>
              copyText(text).then(
                () => (say("Copied"), onPick(null)),
                () => say("Could not copy"),
              )
            }
            aria-label="Copy"
          >
            <Icon name="copy" size={20} />
          </button>
          <button className="p-icon tonal" onClick={() => (onPick(null), onAsk(text))} aria-label="Ask in a session">
            <Icon name="spark" />
          </button>
        </div>,
        document.body,
      )}
    </>
  );
}

function Handle({ at, side, term, onMove }) {
  const moving = useRef(false);
  if (!at) return null;
  const follow = (e) => {
    const t = e.touches[0];
    onMove(cellAt(term, t.clientX, t.clientY - LIFT_PX));
  };
  return (
    <span
      className={`term-handle ${side}`}
      style={{ left: at.left, top: at.top }}
      onTouchStart={() => (moving.current = true)}
      onTouchMove={(e) => moving.current && follow(e)}
      onTouchEnd={() => (moving.current = false)}
    />
  );
}
