// The Terminal tab: the folder's terminals as the Agent tab lists sessions,
// each opening to a screen of its own with a row of the keys a phone's
// keyboard lacks, and a sheet of what else there is to do with it.
import { useEffect, useRef, useState } from "react";
import TerminalView, { termDoing, termFor } from "../Terminal.jsx";
import { say } from "../Notices.jsx";
import { copyText, cx, duration, relTime } from "../util.js";
import { Icon } from "./icons.jsx";
import { Row, Sheet, useLongPress, useSwipe } from "./Sheet.jsx";
import { Rename } from "./Sessions.jsx";
import { FONT_MAX, FONT_MIN } from "./TermTouch.jsx";

const FONT_KEY = "dv:phone-term-font"; // this phone's, whichever folder
const SIZES = [10, 11, 12, 13, 14, 16];

// when is how long a terminal has been at what it does, as its row and screen say.
const when = (t) =>
  t.ended ? (t.exit ? `exit ${t.exit}` : "exited") : t.busy ? `${t.tmux ? "attached" : "running"} ${duration(Date.now() - Date.parse(t.since), true)}` : t.output ? relTime(t.output) : "";

export function TerminalList({ terms, sessions, folder, onOpen, onAttach, onClose, onRename }) {
  const [held, setHeld] = useState(null);
  const [renaming, setRenaming] = useState(null);
  if (!terms) return <div className="p-empty">Loading…</div>;
  return (
    <div className="p-scroll p-sessions">
      {terms.map((t) => (
        <TermRow key={t.id} t={t} folder={folder} onOpen={() => onOpen(t.id)} onHold={() => setHeld(t)} onSwipe={() => onClose(t.id)} />
      ))}
      {!terms.length && <div className="p-empty">No terminals here. Open one with New terminal.</div>}
      {sessions.length > 0 && <div className="p-sec">tmux sessions</div>}
      {sessions.map((s) => (
        <button key={`${s.flags?.join(" ")} ${s.name}`} className="p-row p-session" onClick={() => onAttach(s)}>
          <span className="p-lead hollow">
            <Icon name="terminal" size={20} />
          </span>
          <span className="p-row-body">
            <span className="p-row-top">
              <span className="p-row-title">{s.name}</span>
              <span className="p-when">{relTime(s.activity)}</span>
            </span>
            <span className="p-row-sub">
              {s.windows} window{s.windows === 1 ? "" : "s"} · {s.attached ? "attached elsewhere" : "detached"}
            </span>
            <span className="p-term-dir mono">{s.dir || folder}</span>
          </span>
        </button>
      ))}
      <div className="p-fab-room" />
      {held && (
        <Sheet onClose={() => setHeld(null)}>
          <div className="p-preview">{held.name}</div>
          <Row icon="edit" title="Rename" onClick={() => (setRenaming(held), setHeld(null))} />
          <Row icon="x" title="Close" sub={closing(held)} onClick={() => (onClose(held.id), setHeld(null))} />
        </Sheet>
      )}
      {renaming && <Rename s={{ id: renaming.id, title: renaming.name }} onRename={onRename} onClose={() => setRenaming(null)} />}
    </div>
  );
}

// closing says what closing a terminal does to what runs in it.
const closing = (t) => (t.ended ? "" : t.tmux ? "The tmux session goes on" : t.busy ? `Stops ${t.command || "what runs in it"}` : "Ends its shell");

function TermRow({ t, folder, onOpen, onHold, onSwipe }) {
  const { ref, dx, armed, touch } = useSwipe(onSwipe);
  const hold = useLongPress(onHold);
  return (
    <div className={cx("p-swipe", armed && "armed")} ref={ref}>
      <div className="p-swipe-bg" aria-hidden="true">
        <Icon name="x" />
        <Icon name="x" />
      </div>
      <button
        className={cx("p-row p-session", dx !== 0 && "moving")}
        style={dx ? { transform: `translateX(${dx}px)` } : undefined}
        onClick={() => dx === 0 && onOpen()}
        {...hold}
        {...touch}
      >
        <span className="p-lead">
          <Icon name="terminal" size={20} />
        </span>
        <span className="p-row-body">
          <span className="p-row-top">
            <span className="p-row-title">{t.name}</span>
            <span className={cx("p-when", t.ended && t.exit !== 0 && "p-failed")}>{when(t)}</span>
          </span>
          <span className={cx("p-row-sub p-term-doing", t.busy && t.command && "mono")}>
            {t.busy && <span className="p-dot busy" />}
            {termDoing(t)}
          </span>
          <span className="p-term-dir mono">{t.dir || folder}</span>
        </span>
      </button>
    </div>
  );
}

// TerminalScreen is a terminal over the tabs: what it runs at the top, the
// terminal, and the keys row at the foot.
export function TerminalScreen({ open, t, terms, id, folder, theme, typeAhead, onBack, onActions, onRename, onNotify, onClose, onAsk }) {
  const [fontSize, setFontSize] = useState(() => {
    const n = Number(localStorage.getItem(FONT_KEY));
    return n >= FONT_MIN && n <= FONT_MAX ? n : 12;
  });
  const sized = (px) => {
    setFontSize(px);
    localStorage.setItem(FONT_KEY, String(px));
  };
  const input = useRef(null);
  const [sheet, setSheet] = useState(false);
  const [renaming, setRenaming] = useState(false);
  // Gone while on screen - closed here or elsewhere, or dv restarted - it goes too.
  useEffect(() => {
    if (open && terms && !t) onBack();
  }, [open, terms, t]);

  const copyScreen = () => {
    const term = termFor(id);
    if (!term) return;
    const b = term.buffer.active;
    const lines = [];
    for (let i = b.viewportY; i < b.viewportY + term.rows; i++) lines.push(b.getLine(i)?.translateToString(true) || "");
    copyText(lines.join("\n").replace(/\s+$/, "")).then(
      () => say("Copied"),
      () => say("Could not copy"),
    );
  };

  return (
    <div className={cx("p-screen p-term", open && "open")} aria-hidden={!open}>
      <header className="p-bar">
        <button className="p-icon" onClick={onBack} aria-label="Back">
          <Icon name="back" />
        </button>
        <div className="p-title">
          <span className="p-title-main">{t?.name}</span>
          {t && (
            <span className="p-title-sub">
              <span className={cx(t.busy && t.command && "mono")}>{termDoing(t)}</span>
              {t.busy && ` · ${duration(Date.now() - Date.parse(t.since), true)}`}
            </span>
          )}
        </div>
        <button className="p-icon" onClick={onActions} aria-label="Actions">
          <Icon name="bolt" />
        </button>
        <button className="p-icon" onClick={() => setSheet(true)} aria-label="More">
          <Icon name="dots" />
        </button>
      </header>
      <TerminalView terms={terms} shownId={id} active={open} theme={theme} focus={0} copyOnSelect={false} typeAhead={typeAhead} phone={{ fontSize, onFontSize: sized, input, onAsk }} />
      <Keys id={id} input={input} />

      {sheet && t && (
        <Sheet onClose={() => setSheet(false)}>
          <div className="p-preview">
            {t.name} · <span className="mono">{t.dir || folder}</span>
          </div>
          {!t.tmux && !t.ended && (
            <Row
              icon="timer"
              on={t.notify}
              title={t.notify ? "Notifying when it finishes" : "Notify when it finishes"}
              sub={t.notify ? "Tap to take it back" : t.busy ? "Once, with the last lines it printed" : "When the next command ends, once"}
              onClick={() => (onNotify(t.id, !t.notify), setSheet(false))}
            />
          )}
          <Row icon="textsize" title="Text size" sub="Or pinch the terminal · on this phone" />
          <div className="p-seg">
            {SIZES.map((px) => (
              <button key={px} className={cx(fontSize === px && "on")} onClick={() => sized(px)}>
                {px}
              </button>
            ))}
          </div>
          <Row icon="edit" title="Rename" onClick={() => (setRenaming(true), setSheet(false))} />
          <Row icon="copy" title="Copy what's on screen" onClick={() => (copyScreen(), setSheet(false))} />
          <Row icon="x" title="Close" sub={closing(t)} onClick={() => (setSheet(false), onClose(t.id))} />
        </Sheet>
      )}
      {renaming && t && <Rename s={{ id: t.id, title: t.name }} onRename={onRename} onClose={() => setRenaming(false)} />}
    </div>
  );
}

// The keys row's keys, a page at a time: [label, what it sends]. A name in
// place of what it sends is worked out as it is pressed, the terminal's mode
// and the modifiers held deciding it.
const PAGES = [
  [["Esc", "\x1b"], ["Tab", "\t"], ["Ctrl", "ctrl"], ["Alt", "alt"], ["^C", "\x03"], ["←", "left"], ["↑", "up"], ["↓", "down"], ["→", "right"], ["⏎", "\r"]],
  [["|"], ["~"], ["/"], ["-"], ["_"], ["$"], ["*"], ["&"], ["`"], ["Paste", "paste"]],
  [["Home", "home"], ["End", "end"], ["PgUp", "pgup"], ["PgDn", "pgdn"], ["F1", "f1"], ["F2", "f2"], ["F10", "f10"], ["^D", "\x04"], ["^Z", "\x1a"], ["^R", "\x12"]],
];
const ARROWS = { up: "A", down: "B", right: "C", left: "D", home: "H", end: "F" };
const TILDES = { pgup: 5, pgdn: 6, f10: 21 };
const SS3 = { f1: "P", f2: "Q" };
const REPEATS = new Set(["left", "up", "down", "right"]);

// ctrlOf is a character as typed with Ctrl held.
function ctrlOf(c) {
  const code = c.toUpperCase().charCodeAt(0);
  if (code >= 64 && code <= 95) return String.fromCharCode(code - 64);
  return c === " " ? "\0" : c === "?" ? "\x7f" : c;
}

// Keys is the row of keys a phone's keyboard hasn't: Ctrl and Alt held for
// the next key - tapped twice, until tapped again - the arrows, which repeat
// held, and two more pages of symbols and the rest, swiped to.
function Keys({ id, input }) {
  const [mods, setMods] = useState({ ctrl: 0, alt: 0 }); // 1 for the next key, 2 kept on
  const modsRef = useRef(mods);
  modsRef.current = mods;
  const [page, setPage] = useState(0);
  const pages = useRef(null);
  const repeat = useRef(0);

  // Used: what was held for one key is let go.
  const spend = () => {
    const m = modsRef.current;
    if (m.ctrl === 1 || m.alt === 1) setMods({ ctrl: m.ctrl === 1 ? 0 : m.ctrl, alt: m.alt === 1 ? 0 : m.alt });
  };
  // What the phone's keyboard types takes the modifiers held too.
  input.current = (d) => {
    const { ctrl, alt } = modsRef.current;
    if ((!ctrl && !alt) || [...d].length !== 1) return d;
    spend();
    return (alt ? "\x1b" : "") + (ctrl ? ctrlOf(d) : d);
  };

  const seq = (key) => {
    const term = termFor(id);
    const { ctrl, alt } = modsRef.current;
    const mod = 1 + (alt ? 2 : 0) + (ctrl ? 4 : 0); // xterm's: 3 Alt, 5 Ctrl, 7 both
    if (ARROWS[key]) {
      if (mod > 1) return `\x1b[1;${mod}${ARROWS[key]}`;
      return (term?.modes.applicationCursorKeysMode ? "\x1bO" : "\x1b[") + ARROWS[key];
    }
    if (TILDES[key]) return `\x1b[${TILDES[key]}${mod > 1 ? `;${mod}` : ""}~`;
    if (SS3[key]) return mod > 1 ? `\x1b[1;${mod}${SS3[key]}` : `\x1bO${SS3[key]}`;
    return (alt ? "\x1b" : "") + (ctrl && key.length === 1 ? ctrlOf(key) : key);
  };
  const press = async (key) => {
    const term = termFor(id);
    if (!term) return;
    if (key === "ctrl" || key === "alt") return setMods((m) => ({ ...m, [key]: (m[key] + 1) % 3 }));
    if (key === "paste") {
      try {
        const text = await navigator.clipboard.readText();
        if (text) term.paste(text);
      } catch {
        say("Could not read the clipboard", "The browser may ask to allow it for dv first.");
      }
      return;
    }
    const s = seq(key);
    spend();
    term.input(s);
  };

  // Down, a key keeps the focus where it was - the keyboard up, or down -
  // and an arrow held goes on repeating.
  const repeated = useRef(false);
  const down = (e, key) => {
    e.preventDefault();
    repeated.current = false;
    if (!REPEATS.has(key)) return;
    clearTimeout(repeat.current);
    const go = (ms) => (repeat.current = setTimeout(() => ((repeated.current = true), press(key), go(60)), ms));
    go(400);
  };
  const up = () => clearTimeout(repeat.current);
  useEffect(() => () => clearTimeout(repeat.current), []);

  return (
    <div className="p-keys">
      <div className="p-keys-pages" ref={pages} onScroll={(e) => setPage(Math.round(e.currentTarget.scrollLeft / e.currentTarget.clientWidth))}>
        {PAGES.map((keys, i) => (
          <div key={i} className="p-keys-page">
            {keys.map(([label, key = label]) => (
              <button
                key={label}
                className={cx("p-key", (key === "ctrl" || key === "alt") && mods[key] && (mods[key] === 2 ? "kept" : "on"), key === "\x03" && "stop", label.length > 2 && "word")}
                onPointerDown={(e) => down(e, key)}
                onPointerUp={up}
                onPointerLeave={up}
                onPointerCancel={up}
                // The lift that ends a run of repeats is not one more.
                onClick={() => (repeated.current ? (repeated.current = false) : press(key))}
                aria-label={label}
              >
                {key === "paste" ? <Icon name="paste" size={18} /> : label}
              </button>
            ))}
          </div>
        ))}
      </div>
      <div className="p-keys-dots" aria-hidden="true">
        {PAGES.map((_, i) => (
          <i key={i} className={cx(i === page && "on")} onClick={() => pages.current?.scrollTo({ left: i * pages.current.clientWidth, behavior: "smooth" })} />
        ))}
      </div>
    </div>
  );
}
