import { useLayoutEffect, useState } from "react";
import { cx, menuRoom, useDismiss } from "./util.js";
import { IconChevronDown } from "./icons.jsx";

// useRoom is the way a menu opens and how tall it may be, within the session
// pane it is in, else the window. The composer is at the foot of a session
// but halfway down a new one's page, where opening upwards ran past the top.
export function useRoom(open, ref) {
  const [room, setRoom] = useState(null);
  useLayoutEffect(() => {
    if (!open) return;
    const pane = ref.current.closest(".agent-pane")?.getBoundingClientRect() || { top: 0, bottom: window.innerHeight };
    const h = ref.current.querySelector(".model-list").scrollHeight;
    setRoom(menuRoom(ref.current.getBoundingClientRect(), h, pane.top, pane.bottom, false));
  }, [open]);
  return open && room;
}

// Picker is a menu of choices, as the composer has for the agent and its mode.
// pending marks a pick that waits for the next turn.
export function Picker({ label, title, choices, value, pending, onPick }) {
  const [open, setOpen] = useState(false);
  const ref = useDismiss(open, () => setOpen(false));
  const room = useRoom(open, ref);
  return (
    <div className="model-menu" ref={ref}>
      <button className="mini" onClick={() => setOpen((o) => !o)} title={title}>
        {label}
        {pending && <span className="composer-effort">next turn</span>}
        <IconChevronDown size={10} />
      </button>
      {open && (
        <div className={cx("model-list up", room?.below && "below")} style={room ? { maxHeight: room.max } : undefined}>
          {choices.map((c) => (
            <button
              key={c.id}
              className={cx(c.id === value && "on")}
              onClick={() => {
                onPick(c.id);
                setOpen(false);
              }}
            >
              <span className="model-name">
                {c.label}
                {c.tag && <span className="model-tag">{c.tag}</span>}
              </span>
              {c.description && <span className="model-note">{c.description}</span>}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
