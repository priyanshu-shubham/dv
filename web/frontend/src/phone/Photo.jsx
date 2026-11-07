// Photo is a picture as Android's own viewers show one: the whole screen,
// pinched or double-tapped to zoom, moved about once zoomed, and dragged up or
// down to put away, the conversation showing through as it goes; swiped
// sideways to the next or previous, given those. A tap hides the bar over it.
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { Modal } from "../Overlays.jsx";
import { cx } from "../util.js";
import { Icon } from "./icons.jsx";

const MAX = 5;
const TAP = 10; // px a tap may move
const TWICE = 280; // ms within which a second tap is a double tap
const AWAY = 120; // px dragged that puts it away
const EASE_MS = 250;

const clamp = (v, lo, hi) => Math.min(hi, Math.max(lo, v));
const mid = (a, b) => ({ x: (a.x + b.x) / 2, y: (a.y + b.y) / 2 });
const dist = (a, b) => Math.hypot(a.x - b.x, a.y - b.y);

// prev and next are the pictures either side, if any, which onTurn is given
// to show instead.
export function Photo({ src, name, count, prev, next, onTurn, onClose }) {
  const [bar, setBar] = useState(true);
  const [loaded, setLoaded] = useState(() => new Set());
  const root = useRef(null);
  const stage = useRef(null);
  const track = useRef(null);
  const img = useRef(null);
  // Zoom, offset and the gesture under way, outside React so a drag redraws
  // only the transform. axis is what a one-finger drag turned out to be: "x"
  // a swipe to another picture, "y" a drag away.
  const g = useRef({ s: 1, x: 0, y: 0, drop: 0, slide: 0, axis: "", pts: new Map(), start: null, first: null, tap: null, timer: 0, turn: 0 }).current;
  useEffect(
    () => () => {
      clearTimeout(g.timer);
      clearTimeout(g.turn);
    },
    [g],
  );

  // The track has turned to the picture beside: it is now the middle one, so
  // the track goes back to the middle without moving anything on screen.
  const shown = useRef(src);
  useLayoutEffect(() => {
    if (shown.current === src) return;
    shown.current = src;
    Object.assign(g, { s: 1, x: 0, y: 0, drop: 0, slide: 0 });
    draw(false);
  }, [src]);

  const draw = (ease) => {
    const how = ease ? `transform ${EASE_MS}ms var(--m-ease)` : "none";
    img.current.style.transition = track.current.style.transition = how;
    img.current.style.transform = `translate(${g.x}px, ${g.y + g.drop}px) scale(${g.s})`;
    track.current.style.transform = `translateX(${g.slide}px)`;
    root.current.style.setProperty("--fade", 1 - Math.min(1, Math.abs(g.drop) / 400));
    root.current.classList.toggle("eased", ease);
  };

  // bound keeps a zoomed picture's edges from coming in past the screen's.
  const bound = () => {
    const bx = Math.max(0, (img.current.offsetWidth * g.s - stage.current.clientWidth) / 2);
    const by = Math.max(0, (img.current.offsetHeight * g.s - stage.current.clientHeight) / 2);
    g.x = clamp(g.x, -bx, bx);
    g.y = clamp(g.y, -by, by);
  };

  // Points are from the screen's middle, where the picture's own is unmoved.
  const at = (e) => {
    const r = stage.current.getBoundingClientRect();
    return { x: e.clientX - r.left - r.width / 2, y: e.clientY - r.top - r.height / 2 };
  };

  // begin starts over from the fingers now down, so lifting one of two
  // carries on as a drag rather than a jump.
  const begin = () => {
    const [a, b] = [...g.pts.values()];
    g.start = { s: g.s, x: g.x, y: g.y, a, mid: b ? mid(a, b) : a, dist: b ? dist(a, b) : 0 };
  };

  const down = (e) => {
    stage.current.setPointerCapture(e.pointerId);
    const p = at(e);
    g.pts.set(e.pointerId, p);
    g.first = g.pts.size === 1 ? { ...p, t: e.timeStamp, moved: false } : null;
    g.axis = "";
    if (g.pts.size > 1) g.drop = g.slide = 0; // a pinch, not a drag away or a swipe
    begin();
  };

  const move = (e) => {
    if (!g.pts.has(e.pointerId)) return;
    g.pts.set(e.pointerId, at(e));
    const [a, b] = [...g.pts.values()];
    const st = g.start;
    if (b) {
      const s = clamp((st.s * dist(a, b)) / st.dist, 1, MAX);
      const m = mid(a, b);
      // What was under the fingers stays under them.
      g.x = m.x - (s * (st.mid.x - st.x)) / st.s;
      g.y = m.y - (s * (st.mid.y - st.y)) / st.s;
      g.s = s;
    } else {
      const dx = a.x - st.a.x;
      const dy = a.y - st.a.y;
      if (g.first && Math.hypot(dx, dy) > TAP) g.first.moved = true;
      if (st.s > 1) {
        g.x = st.x + dx;
        g.y = st.y + dy;
        bound();
      } else if (g.first?.moved) {
        g.axis ||= Math.abs(dx) > Math.abs(dy) && (prev || next) ? "x" : "y";
        // With nothing that way, it gives a little and springs back.
        if (g.axis === "x") g.slide = (dx > 0 ? prev : next) ? dx : dx / 4;
        else g.drop = dy;
      }
    }
    draw(false);
  };

  const up = (e, cancelled) => {
    if (!g.pts.delete(e.pointerId)) return;
    if (g.pts.size) return begin();
    const f = g.first;
    if (g.slide) {
      const to = g.slide > 0 ? prev : next;
      const flung = f && Math.abs(g.slide) / (e.timeStamp - f.t) > 0.6;
      if (to && !cancelled && !g.turn && (Math.abs(g.slide) > stage.current.clientWidth / 4 || flung)) return turn(to, Math.sign(g.slide));
      g.slide = 0;
    } else if (g.drop) {
      const flung = f && Math.abs(g.drop) / (e.timeStamp - f.t) > 0.6;
      if (Math.abs(g.drop) > AWAY || flung) return away();
      g.drop = 0;
    } else if (f && !f.moved && !cancelled && e.timeStamp - f.t < 300) return tapped({ ...f, t: e.timeStamp });
    bound();
    draw(true);
  };

  const tapped = (p) => {
    const second = g.tap && p.t - g.tap.t < TWICE && dist(p, g.tap) < 40;
    clearTimeout(g.timer);
    g.tap = null;
    if (!second) {
      g.tap = p;
      g.timer = setTimeout(() => {
        g.tap = null;
        setBar((on) => !on);
      }, TWICE);
      return;
    }
    if (g.s > 1) {
      g.s = 1;
      g.x = g.y = 0;
    } else {
      g.s = 2.5;
      g.x = -1.5 * p.x;
      g.y = -1.5 * p.y;
      bound();
    }
    draw(true);
  };

  // turn carries on the swipe until the picture beside fills the screen -
  // loaded, or its spinner where it will be - and then makes it the one shown.
  const turn = (to, dir) => {
    g.slide = dir * stage.current.clientWidth;
    draw(true);
    g.turn = setTimeout(() => {
      g.turn = 0;
      onTurn(to);
    }, EASE_MS);
  };

  const away = () => {
    g.drop = Math.sign(g.drop) * window.innerHeight;
    draw(true);
    setTimeout(onClose, 200);
  };

  const done = (s) => setLoaded((l) => (l.has(s) ? l : new Set(l).add(s)));

  return (
    <Modal className="photo" onClose={onClose}>
      <div className="p-photo" ref={root}>
        <div
          className="p-photo-stage"
          ref={stage}
          onPointerDown={down}
          onPointerMove={move}
          onPointerUp={(e) => up(e, false)}
          onPointerCancel={(e) => up(e, true)}
        >
          {/* Those either side wait off screen, loading, so a swipe brings one
              in with the finger. Keyed by src, the one turned to keeps what it
              has drawn as it becomes the middle. */}
          <div className="p-photo-track" ref={track}>
            {[
              [prev, -1],
              [src, 0],
              [next, 1],
            ].map(
              ([s, side]) =>
                s && (
                  <div key={s} className="p-photo-slot" style={{ left: `${side * 100}%` }}>
                    {!loaded.has(s) && <span className="spinner" />}
                    <img
                      ref={side === 0 ? img : undefined}
                      className={cx(loaded.has(s) && "in")}
                      src={s}
                      alt=""
                      draggable={false}
                      onLoad={() => done(s)}
                      onError={() => done(s)}
                    />
                  </div>
                ),
            )}
          </div>
        </div>
        <header className={cx("p-photo-bar", !bar && "hidden")}>
          <button className="p-icon" onClick={onClose} aria-label="Back">
            <Icon name="back" />
          </button>
          <span className="p-photo-name">{name}</span>
          {count && <span className="p-photo-count">{count}</span>}
        </header>
      </div>
    </Modal>
  );
}
