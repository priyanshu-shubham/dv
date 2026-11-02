import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api.js";
import { boot } from "./boot.js";
import { SessionName } from "./Agent.jsx";
import { IconPlus, IconX } from "./icons.jsx";
import { say } from "./Notices.jsx";
import { copyText, cx, duration, isMac, relTime, useLasting } from "./util.js";
import { Icon } from "./phone/icons.jsx";
import { Picked, touchTerm } from "./phone/TermTouch.jsx";

// The Terminal view: shells dv runs in the folder. The sidebar lists them with
// what each is running; the one picked fills the rest. The shells are dv's, so
// a reload, or another page, attaches to them again.

const POLL_MS = 1500;

// useTerminals lists the folder's terminals, and the tmux sessions none of
// them shows, polled while watch is on: the list says what each is running,
// which changes with nothing to tell the page.
export function useTerminals(watch) {
  const [list, setList] = useState(null); // null until listed
  const [sessions, setSessions] = useState([]);
  const load = useCallback(
    () =>
      api.terminals().then(
        ({ terminals, tmux }) => (setList(terminals), setSessions(tmux || []), terminals),
        () => null,
      ),
    [],
  );
  // Each after the last is answered, so a slow link isn't asked faster than it answers.
  useEffect(() => {
    if (!watch) return;
    let timer = 0;
    let live = true;
    const tick = async () => {
      if (!document.hidden) await load();
      if (live) timer = setTimeout(tick, POLL_MS);
    };
    tick();
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [watch, load]);
  return [list, load, sessions];
}

// termDoing is what a terminal is at, in a line.
export const termDoing = (t) =>
  t.ended ? "exited" : t.tmux ? `tmux ${t.tmux}${t.command ? ` · ${t.command}` : ""}` : t.busy ? t.command || "running a command" : "at the prompt";

export function TerminalList({ terms, sessions = [], activeId, folder, onSelect, onNew, onAttach, onClose, onRename }) {
  const [filter, setFilter] = useState("");
  const q = filter.trim().toLowerCase();
  const shown = (terms || []).filter((t) => !q || `${t.name} ${t.tmux || ""} ${t.command || ""} ${t.dir}`.toLowerCase().includes(q));
  const tmux = sessions.filter((s) => !q || `${s.name} ${s.dir}`.toLowerCase().includes(q));
  // A session to open a terminal on; hollow, as nothing here shows it.
  const session = (s) => (
    <div
      key={`${s.flags?.join(" ")} ${s.name}`}
      role="button"
      tabIndex={0}
      className="session-row term-row"
      title="Open in a new terminal"
      onClick={() => onAttach(s)}
      onKeyDown={(e) => e.key === "Enter" && onAttach(s)}
    >
      <div className="session-row-head">
        <span className="session-dot" />
        <span className="name">{s.name}</span>
      </div>
      <div className="session-last">
        {s.windows} window{s.windows === 1 ? "" : "s"} · {s.attached ? "attached elsewhere" : "detached"}
      </div>
      <div className="session-meta">
        <span className="term-dir">{s.dir || folder}</span>
        <span className="spacer" />
        {s.flags?.length > 0 && <span title="The tmux server it is on">{s.flags.join(" ")}</span>}
        <span title="Last used">{relTime(s.activity)}</span>
      </div>
    </div>
  );
  // Redrawn each poll, which keeps "running 14s" counting.
  const row = (t) => (
    <div
      key={t.id}
      role="button"
      tabIndex={0}
      className={cx("session-row", "term-row", t.id === activeId && "on")}
      onClick={() => onSelect(t.id)}
      onKeyDown={(e) => e.key === "Enter" && onSelect(t.id)}
    >
      <div className="session-row-head">
        <span className={cx("session-dot", !t.ended && "live-dv", t.busy && "busy")} />
        <SessionName key={t.name} title={t.name} hint={t.name} onRename={(name) => onRename(t.id, name)} />
        <button
          className="session-close"
          title={t.ended ? "Close" : t.tmux ? "Close; the tmux session goes on" : t.busy ? "Close, stopping what runs in it" : "Close, ending its shell"}
          onClick={(e) => {
            e.stopPropagation();
            onClose(t.id);
          }}
        >
          <IconX size={11} />
        </button>
      </div>
      {/* One line of it, all of it on hover. */}
      <div className={cx("session-last", t.busy && t.command && "term-command")} title={t.busy && t.command ? termDoing(t) : undefined}>
        {termDoing(t)}
      </div>
      <div className="session-meta">
        <span className="term-dir">{t.dir || folder}</span>
        <span className="spacer" />
        {t.ended ? (
          <span className={cx(t.exit !== 0 && "term-failed")}>exit {t.exit}</span>
        ) : t.busy ? (
          <span>
            {t.tmux ? "attached" : "running"} {duration(Date.now() - Date.parse(t.since), true)}
          </span>
        ) : (
          t.output && <span title="When it last printed">{relTime(t.output)}</span>
        )}
      </div>
    </div>
  );
  return (
    <>
      <div className="sidebar-filter">
        <input value={filter} placeholder="Filter terminals" onChange={(e) => setFilter(e.target.value)} onKeyDown={(e) => e.stopPropagation()} />
        <button className="ghost" onClick={onNew} title="New terminal (Alt+N)">
          <IconPlus size={13} />
        </button>
      </div>
      <div className="file-list session-list">
        {shown.map(row)}
        {terms?.length === 0 && <div className="empty">No terminals open.</div>}
        {terms?.length > 0 && shown.length === 0 && <div className="empty">No terminals match.</div>}
        {tmux.length > 0 && <div className="session-group">tmux sessions</div>}
        {tmux.map(session)}
      </div>
    </>
  );
}

// xterm.js is fetched the first time a terminal is shown.
let xtermLoad = null;
const FONT = '"JetBrains Mono Variable", ui-monospace, "SF Mono", Menlo, Consolas, monospace';
function loadXterm() {
  xtermLoad ||= Promise.all([
    import("@xterm/xterm"),
    import("@xterm/addon-fit"),
    import("@xterm/addon-web-links"),
    import("@xterm/addon-clipboard"),
    // Measured as it opens, so it has to have the font by then.
    document.fonts?.load(`12px ${FONT}`).catch(() => {}),
  ]).then(([x, fit, links, clip]) => ({ Terminal: x.Terminal, FitAddon: fit.FitAddon, WebLinksAddon: links.WebLinksAddon, ClipboardAddon: clip.ClipboardAddon }));
  return xtermLoad;
}

// GitHub's terminal colours, as the code's are GitHub's.
const PALETTES = {
  dark: {
    background: "#0a0a0a", foreground: "#d6d6d6", cursor: "#d6d6d6", cursorAccent: "#0a0a0a",
    selectionBackground: "rgba(91, 157, 255, 0.3)",
    scrollbarSliderBackground: "rgba(255, 255, 255, 0.12)", scrollbarSliderHoverBackground: "rgba(255, 255, 255, 0.2)", scrollbarSliderActiveBackground: "rgba(255, 255, 255, 0.25)",
    black: "#484f58", red: "#ff7b72", green: "#3fb950", yellow: "#d29922", blue: "#58a6ff", magenta: "#bc8cff", cyan: "#39c5cf", white: "#b1bac4",
    brightBlack: "#6e7681", brightRed: "#ffa198", brightGreen: "#56d364", brightYellow: "#e3b341", brightBlue: "#79c0ff", brightMagenta: "#d2a8ff", brightCyan: "#56d4dd", brightWhite: "#f0f6fc",
  },
  light: {
    background: "#ffffff", foreground: "#1f1f1f", cursor: "#1f1f1f", cursorAccent: "#ffffff",
    selectionBackground: "rgba(9, 105, 218, 0.2)",
    scrollbarSliderBackground: "rgba(0, 0, 0, 0.15)", scrollbarSliderHoverBackground: "rgba(0, 0, 0, 0.25)", scrollbarSliderActiveBackground: "rgba(0, 0, 0, 0.3)",
    black: "#24292f", red: "#cf222e", green: "#116329", yellow: "#4d2d00", blue: "#0969da", magenta: "#8250df", cyan: "#1b7c83", white: "#6e7781",
    brightBlack: "#57606a", brightRed: "#a40e26", brightGreen: "#1a7f37", brightYellow: "#633c01", brightBlue: "#218bff", brightMagenta: "#a475f9", brightCyan: "#3192aa", brightWhite: "#8c959f",
  },
};

// dv's own keys, which reach dv from a terminal too and must not reach the
// shell as well: the window takes them first, but xterm would still type them.
const dvKey = (e) =>
  (e.code === "Backquote" && e.ctrlKey && !e.altKey && !e.metaKey) ||
  (e.altKey && !e.ctrlKey && !e.metaKey && !e.shiftKey && ["KeyA", "KeyN", "KeyP", ...(boot.base ? ["KeyH"] : [])].includes(e.code)) ||
  (e.code === "KeyP" && e.shiftKey && !e.altKey && (isMac ? e.metaKey : e.ctrlKey));

// A terminal can't tell whether its program uses a key; but a program that
// draws the whole screen, or asks for the mouse, is one that may (fzf scrolls
// its preview with Shift+↑), so Shift and the arrows are left to it. With
// anything else running, or at the prompt, they go through dv's terminals
// and views, as they do from an empty message box.
const ownsKeys = (term) => term.buffer.active.type === "alternate" || term.modes.mouseTrackingMode !== "none";
const shiftArrow = (e) => e.shiftKey && !e.ctrlKey && !e.altKey && !e.metaKey && e.key.startsWith("Arrow");
const wordArrow = (e) =>
  (e.key === "ArrowLeft" || e.key === "ArrowRight") && !e.metaKey && !e.shiftKey && (e.altKey ? !e.ctrlKey : e.ctrlKey && !isMac);
const lineArrow = (e) =>
  isMac && e.metaKey && !e.altKey && !e.ctrlKey && !e.shiftKey && (e.key === "ArrowLeft" || e.key === "ArrowRight");
const xterms = new WeakMap(); // pane -> its xterm

// leavesArrows is whether Shift and an arrow pressed in el, a terminal, are dv's.
export const leavesArrows = (el) => {
  const term = xterms.get(el?.closest?.(".term-pane"));
  return !!term && !ownsKeys(term);
};

// termFor is the xterm showing terminal id, for what types into it from outside.
const byId = new Map();
export const termFor = (id) => byId.get(id);

// What a phone asks for of a terminal's past when it first attaches: a slow
// link would take a while over all dv keeps.
const PHONE_MOST = 256 << 10;

// In the phone app, phone is { fontSize, onFontSize, input, onAsk }: input.current
// changes what is typed - the keys row's Ctrl held - and onAsk takes text picked.
export default function TerminalView({ terms, shownId, active, theme, focus, copyOnSelect, onNew, phone }) {
  const [xterm, setXterm] = useState(null);
  useEffect(() => {
    loadXterm().then(setXterm, (e) => say("Could not load the terminal", e.message));
  }, []);
  // A terminal shown once stays attached, hidden, so going back to it is
  // instant. It is first opened on screen, where xterm can measure its font.
  const [opened, setOpened] = useState(() => new Set());
  if (active && shownId && !opened.has(shownId)) setOpened(new Set([...opened, shownId]));
  const open = terms ? terms.filter((t) => opened.has(t.id)) : [];
  return (
    <div className="term-view" hidden={!active}>
      {xterm &&
        open.map((t) => (
          <TermPane
            key={t.id}
            id={t.id}
            xterm={xterm}
            shown={t.id === shownId}
            active={active}
            theme={theme}
            focus={focus}
            copyOnSelect={copyOnSelect}
            phone={phone}
          />
        ))}
      {terms?.length === 0 && !phone && (
        <div className="term-empty">
          <button className="btn" onClick={onNew}>
            New terminal
          </button>
          <kbd>Alt+N</kbd>
        </div>
      )}
    </div>
  );
}

function TermPane({ id, xterm, shown, active, theme, focus, copyOnSelect, phone }) {
  const box = useRef(null);
  const live = useRef(null); // { term, fit, link, touch }
  const visible = shown && active;
  const visibleRef = useRef(visible);
  visibleRef.current = visible;
  const copyOnSelectRef = useRef(copyOnSelect);
  copyOnSelectRef.current = copyOnSelect;
  const phoneRef = useRef(phone);
  phoneRef.current = phone;
  const [term, setTerm] = useState(null);
  const [down, setDown] = useState(false); // the connection, while it is being made again
  const [back, setBack] = useState(false); // scrolled back from the end
  const [picked, setPicked] = useState(null);
  const [readout, setReadout] = useState("");

  useEffect(() => {
    const term = new xterm.Terminal({
      fontFamily: FONT,
      fontSize: phoneRef.current?.fontSize || 12,
      lineHeight: 1.3,
      scrollback: 10000,
      cursorBlink: true,
      theme: PALETTES[theme] || PALETTES.dark,
    });
    const fit = new xterm.FitAddon();
    term.loadAddon(fit);
    term.loadAddon(new xterm.WebLinksAddon((e, url) => window.open(url, "_blank", "noopener")));
    // What a program copies goes to this device's clipboard, as tmux's copy
    // mode sends it; asked for the clipboard, a program is told it is empty.
    term.loadAddon(new xterm.ClipboardAddon(undefined, { readText: () => "", writeText: (_, text) => copyText(text).catch(() => {}) }));
    // xterm reports a mouse selection once, as the button is let go, so a drag
    // copies once rather than at every step, and still within the gesture the
    // browser wants for writing to the clipboard. Then, as tmux leaves copy
    // mode, the view goes back to the prompt; the selection goes too, or Ctrl+C
    // would copy it, out of sight, instead of interrupting. A word or line
    // taken by a double or triple click stays lit a moment first, as in tmux,
    // or it would never be seen; the next press keeps it from going mid-drag.
    const el = box.current;
    let clicks = 0;
    let settle = 0;
    const press = (e) => {
      clicks = e.detail;
      clearTimeout(settle);
    };
    el.addEventListener("mousedown", press, true);
    const settled = () => {
      term.clearSelection();
      term.scrollToBottom();
    };
    term.onSelectionChange(() => {
      if (!copyOnSelectRef.current || !term.hasSelection()) return;
      copyText(term.getSelection()).catch(() => {});
      if (clicks > 1) settle = setTimeout(settled, 300);
      else settled();
    });
    term.attachCustomKeyEventHandler((e) => {
      if (dvKey(e) || (shiftArrow(e) && !ownsKeys(term))) return false;
      // Alt (Option) and an arrow is a word back or on, as is Ctrl and one off
      // a Mac, sent as Terminal.app does: zsh, and bash without Debian's
      // inputrc, leave xterm's own sequences for them unbound, and print them.
      // On a Mac it always is, as there; elsewhere at a prompt only, as tmux
      // and other full-screen programs may bind the sequences themselves.
      if (wordArrow(e) && (isMac || !ownsKeys(term))) {
        if (e.type === "keydown") term.input(e.key === "ArrowLeft" ? "\x1bb" : "\x1bf");
        e.preventDefault();
        return false;
      }
      // Cmd and an arrow is the line's start or end, as Ctrl+A and Ctrl+E; at
      // a prompt only, as a full-screen program may take those for its own
      // (tmux's prefix, or Vim adding to a number).
      if (lineArrow(e) && !ownsKeys(term)) {
        if (e.type === "keydown") term.input(e.key === "ArrowLeft" ? "\x01" : "\x05");
        e.preventDefault();
        return false;
      }
      // A Mac copies and pastes with Cmd, which the terminal leaves be.
      if (e.type !== "keydown" || isMac || !e.ctrlKey || e.altKey || e.metaKey) return true;
      const k = e.key.toLowerCase();
      // Ctrl+C copies what is selected, else interrupts; Ctrl+Shift+C always copies.
      if (k === "c" && (e.shiftKey || term.hasSelection())) {
        e.preventDefault();
        copyText(term.getSelection());
        term.clearSelection();
        return false;
      }
      // Left to the browser, whose paste the terminal takes.
      return k !== "v";
    });
    term.open(box.current);
    xterms.set(box.current, term);
    byId.set(id, term);
    const link = attach(id, term, () => visibleRef.current, {
      most: phoneRef.current ? PHONE_MOST : 0,
      onDown: setDown,
      input: (d) => phoneRef.current?.input?.current?.(d) ?? d,
    });
    const scrolled = term.onScroll(() => {
      const b = term.buffer.active;
      setBack(b.viewportY < b.baseY);
    });
    // A phone's own: a finger instead of the mouse, and the size it sets kept.
    const touch =
      phoneRef.current &&
      touchTerm(el, term, {
        picked: setPicked,
        sized: (px, done) => {
          term.options.fontSize = px;
          fit.fit();
          link.size();
          setReadout(`${px}px · ${term.cols} columns`);
          if (done) {
            setTimeout(() => setReadout(""), 900);
            phoneRef.current?.onFontSize(px);
          }
        },
      });
    live.current = { term, fit, link, touch };
    setTerm(term);
    return () => {
      clearTimeout(settle);
      el.removeEventListener("mousedown", press, true);
      touch?.stop();
      scrolled.dispose();
      link.stop();
      if (byId.get(id) === term) byId.delete(id);
      term.dispose();
    };
  }, [id, xterm]);

  useEffect(() => {
    live.current.term.options.theme = PALETTES[theme] || PALETTES.dark;
  }, [theme]);

  const fontSize = phone?.fontSize;
  useEffect(() => {
    const { term, fit, link } = live.current;
    if (!fontSize || term.options.fontSize === fontSize) return;
    term.options.fontSize = fontSize;
    if (visibleRef.current && box.current?.clientWidth) (fit.fit(), link.size());
  }, [fontSize]);

  // The page showing a terminal sets its size; another page having set it
  // since - a phone, narrower - it sets it again as the reader comes back.
  useEffect(() => {
    if (!visible) return;
    const { term, fit, link } = live.current;
    const refit = () => {
      if (!box.current?.clientWidth) return;
      fit.fit();
      link.size();
    };
    refit();
    const ro = new ResizeObserver(refit);
    ro.observe(box.current);
    const again = () => !document.hidden && link.size();
    window.addEventListener("focus", again);
    document.addEventListener("visibilitychange", again);
    term.textarea?.addEventListener("focus", again);
    return () => {
      ro.disconnect();
      window.removeEventListener("focus", again);
      document.removeEventListener("visibilitychange", again);
      term.textarea?.removeEventListener("focus", again);
    };
  }, [visible]);

  // A phone's keyboard comes up only at a tap: opened, a terminal is read first.
  useEffect(() => {
    if (visible && !phoneRef.current) live.current.term.focus();
  }, [visible, focus]);

  // Lost a while, not a moment, as a phone's connection is as it changes network.
  const lost = useLasting(down, 1500);
  return (
    <>
      <div className="term-pane" hidden={!shown} ref={box} />
      {shown && lost && <div className="term-link">Reconnecting · what you type is sent once it's back</div>}
      {shown && phone && back && !picked && (
        <button className="term-latest" onClick={() => live.current.term.scrollToBottom()}>
          <Icon name="down" size={18} />
          Latest
        </button>
      )}
      {shown && readout && <div className="term-readout">{readout}</div>}
      {shown && term && picked && <Picked term={term} picked={picked} onPick={(p) => live.current.touch.pick(p)} onAsk={(t) => phoneRef.current?.onAsk(t)} />}
    </>
  );
}

// What is typed while the connection is down is held for it, up to this much.
const HOLD_BYTES = 16 << 10;

// attach follows terminal id into term, and sends it what is typed, coming
// back after a drop - dv restarting, a laptop waking - until stopped or the
// terminal is closed. most bounds what it is sent of the past when it first
// attaches; onDown is told when the connection goes and is back; input may
// change what is typed before it is sent.
function attach(id, term, isShown, { most = 0, onDown = () => {}, input = (d) => d } = {}) {
  let ws = null;
  let stopped = false;
  let timer = 0;
  let wait = 0;
  let ended = false;
  let starting = false;
  // What a program asked the terminal before this page came is not answered:
  // the answer would be typed into whatever runs now.
  let replaying = false;
  // Where what is on screen ends, { epoch, at }, for attaching again to be
  // sent only what came after; and when dv was last heard from.
  let mark = null;
  let heard = 0;
  let held = []; // typed while the connection was down
  let heldBytes = 0;
  const encode = new TextEncoder();

  const send = (data) => ws?.readyState === WebSocket.OPEN && ws.send(data);
  const hold = (bytes) => {
    if (heldBytes + bytes.length > HOLD_BYTES) return;
    held.push(bytes);
    heldBytes += bytes.length;
  };
  const type = (bytes) => send(bytes) !== false || hold(bytes);
  // A pinch, or a phone's keyboard coming up, sizes it at every frame; the
  // shell is told once it settles.
  let sizing = 0;
  const sendSize = () => send(JSON.stringify({ cols: term.cols, rows: term.rows }));
  const size = () => {
    clearTimeout(sizing);
    sizing = setTimeout(sendSize, 120);
  };
  const again = () => {
    timer = setTimeout(open, wait);
    wait = Math.min(Math.max(wait * 2, 500), 5000);
  };
  // A socket that looks open but has heard nothing for longer than this is
  // dead (dv says {} every 15s), as one is after a phone changes network.
  // Closing it could wait on an answer that never comes, so it is let go.
  const quiet = (ms) => ws?.readyState === WebSocket.OPEN && Date.now() - heard > ms;
  const drop = () => {
    const sock = ws;
    ws = null;
    sock.onclose = sock.onmessage = null;
    sock.close();
    clearTimeout(timer);
    wait = 0;
    onDown(true);
    open();
  };
  // Doubled each time silence is what dropped it, until dv is heard: a
  // message is heard only once all of it is in, which on a slow link is a while.
  let patience = 40000;
  const watch = setInterval(() => {
    if (!quiet(patience)) return;
    patience = Math.min(patience * 2, 320000);
    drop();
  }, 10000);
  const back = () => !document.hidden && quiet(patience / 2) && drop();
  // A new network, which the old connection may not have survived.
  const online = () => ws && drop();
  document.addEventListener("visibilitychange", back);
  window.addEventListener("online", online);
  const open = async () => {
    let url;
    try {
      url = await api.terminalSocket(id, mark, most);
    } catch (e) {
      // Gone, as dv restarting takes its shells; the list will catch up.
      return stopped || e.status === 404 || again();
    }
    if (stopped) return;
    const sock = (ws = new WebSocket(url));
    sock.binaryType = "arraybuffer";
    sock.onopen = () => {
      wait = 0;
      heard = Date.now();
      replaying = true;
      if (isShown()) sendSize();
    };
    sock.onmessage = (e) => {
      heard = Date.now();
      patience = 40000;
      if (typeof e.data !== "string") {
        if (mark) mark.at += e.data.byteLength;
        return term.write(new Uint8Array(e.data));
      }
      const s = JSON.parse(e.data);
      // All dv keeps of it is coming, the place it was at not among it.
      if (s.reset) {
        term.reset();
        ended = false; // until the replay says so
        mark = null;
      }
      if (s.attached) {
        mark = { epoch: s.epoch, at: s.at };
        term.write("", () => {
          replaying = false;
          for (const b of held) send(b);
          held = [];
          heldBytes = 0;
          onDown(false);
        });
        // Taken up where it was, it may have started again since, unseen here.
        if (!s.ended) ended = false;
      }
      if (s.ended && !ended) {
        ended = true;
        term.write(`\r\n\x1b[2m[The shell exited${s.exit ? ` with ${s.exit}` : ""}. Enter starts a new one here.]\x1b[0m\r\n`);
      }
      if (s.started) ended = false;
    };
    sock.onclose = (e) => {
      if (ws === sock) ws = null;
      if (stopped || e.code === 4000) return;
      onDown(true);
      again();
    };
  };

  term.onData((d) => {
    // The terminal's answers to what the replay asks of it all start with an
    // escape, and are not sent; what is typed meanwhile, but for such keys, waits.
    if (replaying) return void (!ended && !d.startsWith("\x1b") && hold(encode.encode(input(d))));
    if (!ended) return type(encode.encode(input(d)));
    if (d.includes("\r") && !starting) {
      starting = true;
      api.restartTerminal(id).then(
        () => (starting = false),
        (e) => ((starting = false), say("Could not start a new shell", e.message)),
      );
    }
  });
  term.onBinary((d) => !replaying && !ended && type(Uint8Array.from(d, (c) => c.charCodeAt(0))));
  open();
  return {
    size,
    stop() {
      stopped = true;
      clearTimeout(timer);
      clearTimeout(sizing);
      clearInterval(watch);
      document.removeEventListener("visibilitychange", back);
      window.removeEventListener("online", online);
      ws?.close();
    },
  };
}
