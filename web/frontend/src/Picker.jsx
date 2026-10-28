import { useLayoutEffect, useState } from "react";
import { cx, menuRoom, useDismiss } from "./util.js";
import { IconChevronDown, IconSettings } from "./icons.jsx";

// useRoom is the way a menu opens and how tall it may be, within the session
// pane or settings it is in, else the window. The composer is at the foot of a
// session but halfway down a new one's page, where opening upwards ran past
// the top; settings scroll, and cut off what runs past them.
export function useRoom(open, ref) {
  const [room, setRoom] = useState(null);
  useLayoutEffect(() => {
    if (!open) return;
    const pane = ref.current.closest(".agent-pane, .settings-body")?.getBoundingClientRect() || { top: 0, bottom: window.innerHeight };
    const h = ref.current.querySelector(".model-list").scrollHeight;
    setRoom(menuRoom(ref.current.getBoundingClientRect(), h, pane.top, pane.bottom, false));
  }, [open]);
  return open && room;
}

// Picker is a menu of choices, as the composer has for the agent and its mode.
// pending marks a pick that waits for the next turn. gear, { id, title, panel },
// puts a settings button on that choice, which turns the menu to the panel.
export function Picker({ label, title, choices, value, pending, onPick, gear }) {
  const [open, setOpen] = useState(false);
  const [setting, setSetting] = useState(false);
  const ref = useDismiss(open, () => setOpen(false));
  const room = useRoom(open, ref);
  return (
    <div className="model-menu" ref={ref}>
      <button className="mini" onClick={() => (setOpen((o) => !o), setSetting(false))} title={title}>
        {label}
        {pending && <span className="composer-effort">next turn</span>}
        <IconChevronDown size={10} />
      </button>
      {open && (
        <div className={cx("model-list up", room?.below && "below")} style={room ? { maxHeight: room.max } : undefined}>
          {setting
            ? gear.panel
            : choices.map((c) => {
                const row = (
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
                );
                return c.id === gear?.id ? (
                  <div key={c.id} className="model-added">
                    {row}
                    <button className="model-drop model-gear" title={gear.title} onClick={() => setSetting(true)}>
                      <IconSettings size={12} />
                    </button>
                  </div>
                ) : (
                  row
                );
              })}
        </div>
      )}
    </div>
  );
}
