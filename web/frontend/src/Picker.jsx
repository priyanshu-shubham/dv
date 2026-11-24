import { useLayoutEffect, useMemo, useState } from "react";
import { cx, inOrder, menuRoom, openSettings, useDismiss } from "./util.js";
import { IconChevronDown, IconSettings } from "./icons.jsx";
import { usePref } from "./prefs.js";

export const EFFORTS = { none: "None", minimal: "Minimal", low: "Low", medium: "Medium", high: "High", xhigh: "Extra high", max: "Max", ultra: "Ultra" };
export const NO_HIDDEN = [];
export const NO_ORDER = [];
export const NO_NAMES = {};

// prettyModel names a model id the way a person would: claude-opus-5[1m] is Opus 5 (1M).
export function prettyModel(id) {
  const m = /^claude-([a-z]+)-(\d+)(?:-(\d)(?!\d))?/.exec(id || "");
  if (!m) return id || "";
  const name = `${m[1][0].toUpperCase()}${m[1].slice(1)} ${m[2]}${m[3] ? "." + m[3] : ""}`;
  return /\[1m\]/.test(id) ? `${name} (1M)` : name;
}

// choicesOf is the picker's models as it lists them, each by the user's own
// name for it where they gave one. "" is the model the user's settings start
// on; Claude Code's "default" is the one it recommends.
export function choicesOf(models, names) {
  return models.map((c) => {
    const own = (/^claude-/.test(c.model) && prettyModel(c.model)) || c.label;
    return {
      ...c,
      label: names[c.id] || own,
      own,
      // One the user added is named as they typed it too, where the name reads otherwise.
      description: c.added && own !== c.id ? c.id : c.description,
      tag: c.id === "" ? (c.model ? "default" : "") : c.id === "default" ? "recommended" : "",
    };
  });
}

// useModelMenu is models as the picker lists them, by the user's names and
// order from Settings, without those taken off the menu but for current.
export function useModelMenu(models, current) {
  const [names] = usePref("user", "modelNames", NO_NAMES);
  const [order] = usePref("user", "modelOrder", NO_ORDER);
  const [hidden] = usePref("user", "hiddenModels", NO_HIDDEN);
  return useMemo(
    () => inOrder(choicesOf(models, names), order).filter((c) => !hidden.includes(c.id) || c.id === current),
    [models, names, order, hidden, current],
  );
}

// ModelPicker is the model and how hard it thinks, in one menu, as the
// terminal's /model has them. onPick is given { model } or { effort }; with
// usual, picking the effort picked again leaves it to the model. The list
// itself is edited in Settings, Models, unless fixed says it can't be.
export function ModelPicker({ label, models, model, efforts, effort, onPick, usual, fixed, className = "composer-pick" }) {
  const [open, setOpen] = useState(false);
  const ref = useDismiss(open, () => setOpen(false));
  const room = useRoom(open, ref);
  const levels = useMemo(() => [...new Set(models.map((m) => m.efforts?.join(" ")).filter(Boolean))], [models]);
  return (
    <div className="model-menu" ref={ref}>
      <button className={cx("mini", className)} onClick={() => setOpen((o) => !o)} title="Model and effort">
        {label}
        {effort && <span className="composer-effort">{EFFORTS[effort] || effort}</span>}
        <IconChevronDown size={10} />
      </button>
      {open && (
        <div className={cx("model-list up", room?.below && "below")} style={room ? { maxHeight: room.max } : undefined}>
          {models.map((c) => (
            <button key={c.id} className={cx(c.id === model && "on")} onClick={() => onPick({ model: c.id })}>
              <span className="model-name">
                {c.label}
                {c.tag && <span className="model-tag">{c.tag}</span>}
              </span>
              {c.description && <span className="model-note">{c.description}</span>}
            </button>
          ))}
          {!fixed && (
            <button className="model-add-row" onClick={() => (setOpen(false), openSettings("models", ".modal.settings .model-picker-heading"))}>
              <span className="model-name">
                <IconSettings size={10} />
                Edit the list…
              </span>
            </button>
          )}
          {levels.length > 0 && (
            <div className="model-efforts" title="How hard the model thinks before answering">
              <span>Effort</span>
              {/* Every model's levels, out of sight under the picked one's, so
                  the list keeps one width whichever model is picked. */}
              <span className="effort-levels">
                {levels.map((l) => (
                  <span key={l} className="seg" aria-hidden="true">
                    {l.split(" ").map((e) => (
                      <button key={e} tabIndex={-1}>
                        {e}
                      </button>
                    ))}
                  </span>
                ))}
                {efforts?.length > 0 ? (
                  <span className="seg">
                    {efforts.map((e) => (
                      <button
                        key={e}
                        className={cx(e === effort && "on")}
                        title={usual && e === effort ? "Picked: again leaves it to the model" : EFFORTS[e]}
                        onClick={() => {
                          onPick({ effort: usual && e === effort ? "" : e });
                          setOpen(false);
                        }}
                      >
                        {e}
                      </button>
                    ))}
                  </span>
                ) : (
                  <span>not for this model</span>
                )}
              </span>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// useRoom is the way a menu opens and how tall it may be, within the session
// pane, settings or ticket it is in, else the window. The composer is at the foot of a
// session but halfway down a new one's page, where opening upwards ran past
// the top; settings scroll, and cut off what runs past them.
// down opens it downwards where there is the room, as a menu from a header.
export function useRoom(open, ref, down = false) {
  const [room, setRoom] = useState(null);
  useLayoutEffect(() => {
    if (!open) return;
    // A dialog cuts off what reaches past it, as a short one's foot would a menu.
    const pane = ref.current.closest(".agent-pane, .settings-body, .board-doc-body, .p-ticket-scroll, .modal")?.getBoundingClientRect() || { top: 0, bottom: window.innerHeight };
    const h = ref.current.querySelector(".model-list").scrollHeight;
    setRoom(menuRoom(ref.current.getBoundingClientRect(), h, pane.top, pane.bottom, down));
  }, [open]);
  return open && room;
}

// Picker is a menu of choices, as the composer has for the permission mode.
// gear, { id, title, panel }, puts a settings button on that choice, which
// turns the menu to the panel. button is its button's class; down opens it
// downwards, as from a header. search, its placeholder, puts a box over a long
// list that narrows it by what is typed, Enter taking the first left.
export function Picker({ label, title, choices, value, onPick, gear, button = "mini", down = false, search }) {
  const [open, setOpen] = useState(false);
  const [setting, setSetting] = useState(false);
  const [q, setQ] = useState("");
  const ref = useDismiss(open, () => setOpen(false));
  const room = useRoom(open, ref, down);
  const words = q.trim().toLowerCase();
  const shown = words ? choices.filter((c) => `${typeof c.label === "string" ? c.label : c.id} ${c.tag || ""}`.toLowerCase().includes(words)) : choices;
  const pick = (id) => {
    onPick(id);
    setOpen(false);
    setQ("");
  };
  return (
    <div className="model-menu" ref={ref}>
      <button className={button} onClick={() => (setOpen((o) => !o), setSetting(false))} title={title}>
        {label}
        <IconChevronDown size={10} />
      </button>
      {open && (
        <div className={cx("model-list up", room?.below && "below")} style={room ? { maxHeight: room.max } : undefined}>
          {search && !setting && (
            <input
              className="model-search"
              autoFocus
              value={q}
              placeholder={search}
              onChange={(e) => setQ(e.target.value)}
              onKeyDown={(e) => {
                e.stopPropagation();
                if (e.key === "Enter" && shown[0]) pick(shown[0].id);
                if (e.key === "Escape") setOpen(false);
              }}
            />
          )}
          {search && !setting && !shown.length && <div className="model-note model-empty">Nothing matches.</div>}
          {setting
            ? gear.panel
            : shown.map((c) => {
                const row = (
                  <button key={c.id} className={cx(c.id === value && "on")} onClick={() => pick(c.id)}>
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
