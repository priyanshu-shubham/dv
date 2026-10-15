// The phone app's surfaces over the page: bottom sheets, the overflow menu,
// and the rows both are made of. Each is a layer (the page goes inert under
// it) and a history entry (Back closes it).
import { useCallback, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useLayer } from "../Overlays.jsx";
import { cx } from "../util.js";
import { useBack } from "./back.js";
import { Icon } from "./icons.jsx";

const LEAVE_MS = 180;

// A tap on the scrim puts it away, but not the lift of the long press that
// opened it, which came down before there was a scrim to press.
function useTapAway(away) {
  const pressed = useRef(false);
  return {
    onPointerDown: (e) => (pressed.current = e.target === e.currentTarget),
    onClick: (e) => e.target === e.currentTarget && pressed.current && away(),
  };
}

// Sheet slides up from the foot. A tap outside, Back, or a drag down by its
// handle puts it away.
export function Sheet({ title, onClose, foot, className, children }) {
  const ref = useRef(null);
  const [shown, setShown] = useState(false);
  const [dy, setDy] = useState(0);
  const from = useRef(null);
  useLayer(ref, 110);
  const leave = useRef(null);
  leave.current = () => {
    setShown(false);
    setTimeout(onClose, LEAVE_MS);
  };
  useBack(true, () => leave.current());
  const tapAway = useTapAway(() => leave.current());
  useLayoutEffect(() => {
    const f = requestAnimationFrame(() => setShown(true));
    return () => cancelAnimationFrame(f);
  }, []);
  const grab = {
    onPointerDown: (e) => {
      from.current = e.clientY;
      e.currentTarget.setPointerCapture(e.pointerId);
    },
    onPointerMove: (e) => from.current !== null && setDy(Math.max(0, e.clientY - from.current)),
    onPointerUp: () => {
      from.current = null;
      if (dy > 80) leave.current();
      setDy(0);
    },
    onPointerCancel: () => {
      from.current = null;
      setDy(0);
    },
  };
  return createPortal(
    <div className={cx("p-scrim", shown && "open")} ref={ref} {...tapAway}>
      <div className={cx("p-sheet", shown && "open", dy > 0 && "dragging", className)} style={dy ? { transform: `translateY(${dy}px)` } : undefined}>
        <div className="p-grab" {...grab}>
          <div className="p-handle" />
          {title && <div className="p-sheet-title">{title}</div>}
        </div>
        <div className="p-sheet-body">{children}</div>
        {foot && <div className="p-sheet-foot">{foot}</div>}
      </div>
    </div>,
    document.body,
  );
}

// Menu is the top bar's overflow menu, hanging from its corner. items are
// { icon, label, run } or "-" for a divider; falsy ones are left out.
export function Menu({ items, onClose }) {
  const ref = useRef(null);
  const [shown, setShown] = useState(false);
  useLayer(ref, 110);
  useBack(true, onClose);
  const tapAway = useTapAway(onClose);
  useLayoutEffect(() => {
    const f = requestAnimationFrame(() => setShown(true));
    return () => cancelAnimationFrame(f);
  }, []);
  return createPortal(
    <div className="p-scrim clear" ref={ref} {...tapAway}>
      <div className={cx("p-menu", shown && "open")} role="menu">
        {items.filter(Boolean).map((it, i) =>
          it === "-" ? (
            <div key={i} className="p-menu-sep" />
          ) : (
            <button
              key={it.label}
              role="menuitem"
              className="p-menu-item"
              onClick={() => {
                onClose();
                // After the menu's entry is off the history, so what it opens stacks on top.
                setTimeout(it.run, 0);
              }}
            >
              {it.icon && <Icon name={it.icon} size={22} />}
              <span>{it.label}</span>
            </button>
          ),
        )}
      </div>
    </div>,
    document.body,
  );
}

// Row is a list item: an icon or other lead, a headline with supporting text
// under it, and something at the end.
export function Row({ icon, lead, title, sub, trail, on, onClick, className, ...rest }) {
  return (
    <button className={cx("p-row", on && "on", className)} onClick={onClick} {...rest}>
      {lead || (icon && <Icon name={icon} className="p-row-icon" />)}
      <span className="p-row-body">
        <span className="p-row-title">{title}</span>
        {sub && <span className="p-row-sub">{sub}</span>}
      </span>
      {trail}
    </button>
  );
}

// Choice is a row in a sheet that picks one of several, its radio showing which.
export function Choice({ title, sub, on, onClick }) {
  return (
    <button className={cx("p-row p-choice", on && "on")} onClick={onClick} role="radio" aria-checked={!!on}>
      <span className="p-radio" />
      <span className="p-row-body">
        <span className="p-row-title">{title}</span>
        {sub && <span className="p-row-sub">{sub}</span>}
      </span>
    </button>
  );
}

const LONG_MS = 450;

// useLongPress calls onLong when a finger rests on the element without moving,
// and swallows the click that lifting it would make. The page's own long-press
// (text selection, the context menu) is held off there.
export function useLongPress(onLong) {
  const timer = useRef(0);
  const at = useRef(null);
  const fired = useRef(false);
  const latest = useRef(onLong);
  latest.current = onLong;
  const cancel = useCallback(() => {
    clearTimeout(timer.current);
    timer.current = 0;
  }, []);
  return {
    onPointerDown: (e) => {
      // Not on what has a tap of its own inside it, or what a portal draws from it.
      const own = e.target.closest?.("a, button");
      if (e.button || !e.currentTarget.contains(e.target) || (own && own !== e.currentTarget)) return;
      fired.current = false;
      at.current = { x: e.clientX, y: e.clientY };
      const target = e.currentTarget;
      timer.current = setTimeout(() => {
        timer.current = 0;
        fired.current = true;
        navigator.vibrate?.(12);
        latest.current(target);
      }, LONG_MS);
    },
    onPointerMove: (e) => {
      if (timer.current && Math.hypot(e.clientX - at.current.x, e.clientY - at.current.y) > 10) cancel();
    },
    onPointerUp: cancel,
    onPointerCancel: cancel,
    onContextMenu: (e) => e.preventDefault(),
    onClickCapture: (e) => {
      if (!fired.current) return;
      fired.current = false;
      e.stopPropagation();
      e.preventDefault();
    },
  };
}
