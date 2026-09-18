import { createContext, memo, useCallback, useContext, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import MarkdownIt from "markdown-it";
import { api } from "./api.js";
import { commentEvent, Request, RequestTitle } from "./AgentPrompt.jsx";
import { DiffBody } from "./FileDiff.jsx";
import { ensureLanguage, highlightLines, langReady } from "./highlight.js";
import { MarkdownDocument, previewKind, PreviewToggle, SvgPreview } from "./Preview.jsx";
import { Orb } from "./Orb.jsx";
import { Modal } from "./Overlays.jsx";
import { Picker, useRoom } from "./Picker.jsx";
import { AttachTarget } from "./Threads.jsx";
import { agentName, cx, duration, isTyping, LRM, modKey, relTime, splitPath, TOUCH, useCopy, useDismiss, useMedia } from "./util.js";
import { readPref, setPref, usePref } from "./prefs.js";
import {
  AgentIcon, IconArrowUp, IconBack, IconBranch, IconChevron, IconChevronDown, IconChevronUp, IconFile, IconNewSession, IconPlus, IconReply, IconStop, IconTemporary,
  IconUndo, IconX,
} from "./icons.jsx";

const md = new MarkdownIt({ html: false, linkify: true });
// A command's output is Markdown, or lines of plain text that must stay lines.
const mdOutput = new MarkdownIt({ html: false, linkify: true, breaks: true });
const NO_THREADS = [];
const NO_IMAGES = [];
const NO_QUEUED = [];
const NO_COMMANDS = [];
const NO_MODELS = [];
const NO_PICKED = {};
const DOUBLE_ESC_MS = 600;
// How long the reader goes without a key before a request takes the keys.
const KEYS_SETTLE_MS = 1000;
// How long after sending Esc takes the message back instead of stopping the
// agent. dv holds a message sent to Codex mid-turn for as long (holdFor).
const TAKE_BACK_MS = 2000;
const UNDO_ARMS_MS = 200;
const UNDER_PX = 40; // the bar naming the message the view is under, and its gap
// Claude scales down anything bigger itself; past this, the upload is only slower.
const IMAGE_PX = 2000;
const IMAGE_TYPES = ["image/png", "image/jpeg", "image/gif", "image/webp"];

// useSession follows one session: its conversation, kept in order and patched
// by key as updates arrive, and what it is doing now. lost is set while the
// stream is down - dv stopped, most likely - and live is only what it last was.
// With call, it is the conversation of the agent that call started instead.
function useSession(id, call = "", from = "") {
  const key = id && `${id} ${call}`;
  const [state, setState] = useState({ key, items: [], live: null, lost: false });
  useEffect(() => {
    // Shown from further back, what is on screen stays until the rest comes.
    setState((s) => (s.key === key ? s : { key, items: [], live: null, lost: false }));
    if (!id) return;
    const follow = call ? (...a) => api.agentSubagentEvents(id, call, ...a) : (update, drop) => api.agentEvents(id, update, drop, from);
    return follow(
      (u) =>
        setState((s) => {
          let items = s.items;
          if (u.reset) items = u.items || [];
          else if (u.items?.length) {
            const at = new Map(items.map((it, i) => [it.key, i]));
            items = items.slice();
            for (const it of u.items) {
              const i = at.get(it.key);
              if (i === undefined) {
                at.set(it.key, items.length);
                items.push(it);
              } else items[i] = it;
            }
          }
          return { key, items, live: u.live, earlier: u.earlier, lost: false };
        }),
      () => setState((s) => (s.lost ? s : { ...s, lost: true })),
    );
  }, [key, from]);
  return state.key === key ? state : { key, items: [], live: null, lost: false };
}

// toggled tells the conversation the reader opened or closed something in it,
// which then keeps that in view as it changes size.
const toggled = (el, open) => el?.dispatchEvent(new CustomEvent("dv:toggle", { bubbles: true, detail: { open } }));

// anchorOf is what holds a scrolled-up reader's place: the topmost thing in
// view, as deep as a diff line, then the item it is in should that one go, with
// where each is. It is below anything in view still loading, which is about to
// grow and is not what is being read; and a file header stuck to the top stays
// put whatever moves, so it cannot be one.
//
// Where is in the content, not on screen: a scroll and a diff arriving can land
// in one frame, with the scroll event after the growth.
// loadingNear reports whether anything close enough to the view to be fetched
// is still loading (EditCard fetches within 600px).
function loadingNear(scroller) {
  const box = scroller.getBoundingClientRect();
  return [...scroller.querySelectorAll(".loading")].some((n) => {
    const r = n.getBoundingClientRect();
    return r.bottom > box.top - 600 && r.top < box.bottom + 600;
  });
}

// commentInView is a comment box open where the reader can see it: the main
// message box is outside the scroller, so every .composer in it is one.
function commentInView(scroller) {
  const box = scroller.getBoundingClientRect();
  return [...scroller.querySelectorAll(".composer")].find((c) => {
    const r = c.getBoundingClientRect();
    return r.bottom > box.top && r.top < box.bottom;
  });
}

// itemEls are the conversation's items as drawn, one child of the log each,
// after the way to what is above them.
const itemEls = (scroller) => scroller.firstElementChild.querySelectorAll(":scope > :not(.agent-earlier)");

// placeOf is an anchor as what outlives the conversation being drawn again: the
// item's key, and how far down the view it is.
function placeOf(scroller, anchor, items) {
  const item = anchor?.at(-1).el;
  const i = item ? Array.prototype.indexOf.call(itemEls(scroller), item) : -1;
  return i >= 0 && i < items.length ? { key: items[i].key, at: item.getBoundingClientRect().top - scroller.getBoundingClientRect().top } : null;
}

const offsetIn = (scroller, el) => el.getBoundingClientRect().top - scroller.getBoundingClientRect().top + scroller.scrollTop;
function anchorOf(scroller) {
  const log = scroller.firstElementChild;
  const box = scroller.getBoundingClientRect();
  const x = log.getBoundingClientRect().left + 40;
  let from = box.top + 4;
  for (const note of log.querySelectorAll(".loading")) {
    const r = note.closest(".agent-log > *").getBoundingClientRect();
    if (r.top < box.bottom && r.bottom > box.top) from = Math.max(from, r.bottom + 4);
  }
  if (from >= box.bottom) from = box.top + 4;
  for (let y = from; y < box.bottom; y += 16) {
    const hit = document.elementFromPoint(x, y);
    // Nor can the way to earlier items, which they arrive under.
    if (!hit || hit === log || !log.contains(hit) || hit.closest(".file-head, .hbars, .agent-earlier")) continue;
    return [hit.closest("[data-line]") || hit, hit.closest(".agent-log > *")].map((el) => ({ el, at: offsetIn(scroller, el) }));
  }
  return null;
}

// What goes with a message is appended in a block Claude reads as context and
// the page reads back into chips: code as it was seen, whole files by name,
// and review comments with the lines they were left on.
const CONTEXT = /\n*<dv-context>\n?([\s\S]*?)\n?<\/dv-context>\s*$/;
const attr = (s) => String(s ?? "").replace(/[&"<>]/g, (c) => ({ "&": "&amp;", '"': "&quot;", "<": "&lt;", ">": "&gt;" })[c]);
const unattr = (s) => s.replace(/&(amp|quot|lt|gt);/g, (_, e) => ({ amp: "&", quot: '"', lt: "<", gt: ">" })[e]);
const span = (a, b) => (a === b ? `${a}` : `${a}-${b}`);

// commandOf is the slash command a message is, as Claude Code takes it -
// { name, description, argumentHint, text } - or null for words to Claude.
function commandOf(message, commands) {
  const m = /^\/(\S+)(?:\s+([\s\S]*))?$/.exec(message.trim());
  const c = m && commands.find((c) => c.name === m[1]);
  if (!c) return null;
  const args = (m[2] || "").trim();
  return { ...c, text: `/${c.name}${args ? " " + args : ""}` };
}

// commandsFor is what a box holding a slash and part of a name could go on to
// be: names that start so - or whose part after a plugin's colon does - then
// names that hold it anywhere.
function commandsFor(draft, commands) {
  const m = /^\/(\S*)$/.exec(draft);
  if (!m) return [];
  const q = m[1].toLowerCase();
  const starts = [];
  const within = [];
  for (const c of commands) {
    const n = c.name.toLowerCase();
    if (n.startsWith(q) || n.split(":").at(-1).startsWith(q)) starts.push(c);
    else if (n.includes(q)) within.push(c);
  }
  const byName = (a, b) => (a.name === q ? -1 : b.name === q ? 1 : a.name.includes(":") - b.name.includes(":") || a.name.localeCompare(b.name));
  return [...starts.sort(byName), ...within.sort(byName)];
}

function composeMessage(text, attached, threads) {
  const parts = [];
  for (const a of attached) {
    if (a.kind === "lines") {
      const width = String(a.end).length;
      const body = (a.quote || []).map((l, i) => `${String(a.start + i).padStart(width)}  ${l}`).join("\n");
      parts.push(`<code path="${attr(a.file)}" lines="${span(a.start, a.end)}" from="${attr(a.at)}">\n${body}\n</code>`);
    } else if (a.kind === "file") {
      parts.push(`<file path="${attr(a.file)}" />`);
    } else if (a.kind === "thread") {
      const t = threads.find((x) => x.id === a.threadId);
      if (!t) continue;
      const quote = (t.quote || []).map((l) => `> ${l}`).join("\n");
      const said = t.comments.map((c) => (t.comments.length > 1 ? `${c.author}: ${c.body}` : c.body)).join("\n\n");
      const on = t.origin ? ` on="your edit"` : "";
      // A comment on the file itself names no lines: it is about all of it.
      const lines = t.startLine ? ` lines="${span(t.startLine, t.endLine)}"` : "";
      parts.push(`<comment path="${attr(t.file)}"${lines} side="${t.side}"${on}>\n${quote ? quote + "\n" : ""}${said}\n</comment>`);
    }
  }
  const body = text.trim();
  return parts.length ? `${body}\n\n<dv-context>\n${parts.join("\n")}\n</dv-context>` : body;
}

// saidAs is a message as it was typed: a command run with ! keeps its !.
const saidAs = (it) => (it.kind === "shell" ? "!" + it.text : splitContext(it.text).text);

function splitContext(text) {
  const m = CONTEXT.exec(text || "");
  if (!m) return { text: text || "", refs: [] };
  const refs = [...m[1].matchAll(/<(code|file|comment) path="([^"]*)"(?: lines="([^"]*)")?/g)].map(([, kind, path, lines]) => ({
    kind,
    path: unattr(path),
    lines,
  }));
  return { text: text.slice(0, m.index), refs, via: VIA.exec(m[1])?.[1] };
}

// The server notes in a message where it was written - a chat app, or dv again
// after one - which the page did not put there.
const VIA = /<via app="([^"]*)">[\s\S]*?<\/via>/;
const withoutVia = (text) => text.replace(new RegExp("\\n?" + VIA.source), "").replace(/\n*<dv-context>\n*<\/dv-context>\s*$/, "");

function Chip({ kind, path, lines, onClick, onRemove }) {
  const name = splitPath(path)[1];
  return (
    <span className={cx("agent-chip", kind === "comment" && "comment")} title={path + (lines ? `:${lines}` : "")}>
      <button className="agent-chip-label" onClick={onClick} disabled={!onClick}>
        {kind === "comment" && "comment · "}
        {name}
        {lines && <span className="dim">:{lines}</span>}
      </button>
      {onRemove && (
        <button className="agent-chip-x" onClick={onRemove} title="Leave it out">
          <IconX size={10} />
        </button>
      )}
    </span>
  );
}

const EFFORTS = { none: "None", minimal: "Minimal", low: "Low", medium: "Medium", high: "High", xhigh: "Extra high", max: "Max", ultra: "Ultra" };

// The agents a new session can start with.
const agentLabel = (agent) => (
  <span className="agent-label">
    <AgentIcon agent={agent} size={12} />
    {agentName(agent)}
  </span>
);
const AGENT_CHOICES = [
  { id: "", label: agentLabel(""), description: "Claude Code, with your Claude settings and login" },
  { id: "codex", label: agentLabel("codex"), description: "Codex, with your Codex config and login" },
];

// resumeWith is how a closed session is picked up again outside dv.
const resumeWith = (agent) => (agent === "codex" ? "codex resume" : "claude --resume");

// What the blank session offers to start from: the work dv is for.
const SUGGESTIONS = [
  "Review my uncommitted changes",
  "Find bugs in the current diff",
  "Write tests for what changed",
  "Explain how this repository fits together",
  "Draft a commit message",
];

const dataURL = (img) => `data:${img.mediaType};base64,${img.data}`;

// newUUID works where crypto.randomUUID does not: dv reached by address over
// plain http is not a secure context.
function newUUID() {
  const b = crypto.getRandomValues(new Uint8Array(16));
  b[6] = (b[6] & 0x0f) | 0x40;
  b[8] = (b[8] & 0x3f) | 0x80;
  const h = [...b].map((x) => x.toString(16).padStart(2, "0")).join("");
  return `${h.slice(0, 8)}-${h.slice(8, 12)}-${h.slice(12, 16)}-${h.slice(16, 20)}-${h.slice(20)}`;
}

// readBack is what went with a message sent from elsewhere - another tab,
// before a reload - as the transcript kept it: its pictures, and what was
// added to it, read back from its context as far as the page can still find
// each (a comment by where it was left).
async function readBack(message, session, threads) {
  const images = [];
  for (let i = 0; message.uuid && i < (message.images || 0); i++) {
    try {
      const r = await fetch(api.agentPromptImageURL(session, message.uuid, i));
      if (r.ok) images.push(await imageOf(await r.blob()));
    } catch {}
  }
  const attached = [];
  const m = CONTEXT.exec(message.text || "");
  for (const [, kind, attrs, body = ""] of m ? m[1].matchAll(/<(code|file|comment)((?: \w+="[^"]*")*) ?(?:\/>|>\n?([\s\S]*?)\n?<\/\1>)/g) : []) {
    const a = Object.fromEntries([...attrs.matchAll(/(\w+)="([^"]*)"/g)].map(([, k, v]) => [k, unattr(v)]));
    const [start, end = start] = (a.lines || "").split("-").map(Number);
    if (kind === "file") attached.push({ kind, file: a.path });
    else if (kind === "code") attached.push({ kind: "lines", file: a.path, side: "new", start, end, at: a.from, quote: body.split("\n").map((l) => l.replace(/^ *\d+ {2}/, "")) });
    else {
      const t = threads.find((t) => t.file === a.path && t.side === a.side && t.startLine === start && t.endLine === end);
      if (t) attached.push({ kind: "thread", threadId: t.id });
    }
  }
  for (const a of attached) a.key = JSON.stringify([a.kind, a.file, a.side, a.start, a.end, a.threadId]);
  return { draft: splitContext(message.text).text, attached, images };
}

// imageOf is a picture as a message carries it, base64.
async function imageOf(blob, name) {
  const bytes = new Uint8Array(await blob.arrayBuffer());
  let bin = "";
  for (let i = 0; i < bytes.length; i += 0x8000) bin += String.fromCharCode(...bytes.subarray(i, i + 0x8000));
  return { key: newUUID(), mediaType: blob.type, data: btoa(bin), name };
}

// readImage takes a picture for a message, scaled down if it is bigger than
// Claude would look at or than it takes (5 MB, as base64).
async function readImage(file) {
  if (!IMAGE_TYPES.includes(file.type)) throw new Error(`${file.name || "That"} is not an image dv can send: PNG, JPEG, GIF or WebP.`);
  let blob = file;
  const bitmap = file.type === "image/gif" ? null : await createImageBitmap(file).catch(() => null);
  const long = bitmap ? Math.max(bitmap.width, bitmap.height) : 0;
  if (bitmap && (long > IMAGE_PX || file.size > 3.5e6)) {
    const scale = Math.min(1, IMAGE_PX / long);
    const canvas = new OffscreenCanvas(Math.round(bitmap.width * scale), Math.round(bitmap.height * scale));
    canvas.getContext("2d").drawImage(bitmap, 0, 0, canvas.width, canvas.height);
    blob = await canvas.convertToBlob({ type: "image/png" });
    if (blob.size > 3.5e6) blob = await canvas.convertToBlob({ type: "image/jpeg", quality: 0.9 });
  }
  if (blob.size > 3.75e6) throw new Error(`${file.name || "That image"} is over the 5 MB dv sends.`);
  return imageOf(blob, file.name);
}

// imagesIn is the pictures a paste carries. Some browsers list a copied image
// only among the items.
function imagesIn(data) {
  const files = [...data.files];
  if (!files.length) for (const it of data.items || []) if (it.kind === "file") files.push(it.getAsFile());
  return files.filter((f) => f?.type.startsWith("image/"));
}

// Android's keyboard and its long-press Paste put only text in a plain text
// box, so a phone pastes a picture by asking for the clipboard - which a page
// can do only over https or on localhost.
const canReadClipboard = () => !!navigator.clipboard?.read && matchMedia("(hover: none)").matches;

async function pasteImages() {
  const files = [];
  for (const item of await navigator.clipboard.read()) {
    const type = item.types.find((t) => t.startsWith("image/"));
    if (type) files.push(new File([await item.getType(type)], "Pasted image", { type }));
  }
  return files;
}

// AddImages is the box's +: a file, or on a phone that can, what was copied.
function AddImages({ onFiles, onPaste }) {
  const [open, setOpen] = useState(false);
  const ref = useDismiss(open, () => setOpen(false));
  const pick = (fn) => () => (setOpen(false), fn());
  return (
    <div className="model-menu" ref={ref}>
      <button
        className="ghost composer-add"
        onClick={canReadClipboard() ? () => setOpen((o) => !o) : onFiles}
        title="Add images - or paste or drop them in"
      >
        <IconPlus size={15} />
      </button>
      {open && (
        <div className="model-list up">
          <button onClick={pick(onFiles)}>
            <span className="model-name">Photo or file</span>
          </button>
          <button onClick={pick(onPaste)}>
            <span className="model-name">Paste image</span>
          </button>
        </div>
      )}
    </div>
  );
}

// sameModel matches a model id with or without its 1M suffix, which the
// transcript leaves off.
const sameModel = (a, b) => !!a && !!b && a.replace("[1m]", "") === b.replace("[1m]", "");

// SessionName is a session's name on its card, renamed by double-clicking it
// (the terminal's /rename). A session a terminal runs is its to rename.
function SessionName({ title, hint, onRename }) {
  const [draft, setDraft] = useState(null);
  if (draft === null) {
    return (
      <span
        className="name"
        title={onRename ? `${hint}\nDouble-click to rename` : hint}
        onDoubleClick={onRename && ((e) => (e.stopPropagation(), setDraft(title)))}
      >
        {title}
      </span>
    );
  }
  const done = (save) => {
    const t = draft.trim();
    if (save && t && t !== title) onRename(t);
    setDraft(null);
  };
  return (
    <input
      className="session-rename"
      autoFocus
      value={draft}
      onFocus={(e) => e.target.select()}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={() => done(true)}
      onClick={(e) => e.stopPropagation()}
      onKeyDown={(e) => {
        e.stopPropagation();
        if (e.key === "Enter") done(true);
        else if (e.key === "Escape") done(false);
      }}
    />
  );
}

const tokens = (n) => (n >= 1e6 ? `${+(n / 1e6).toFixed(1)}M` : `${Math.round(n / 1000)}k`);

// ContextMeter is how full the session's context is, with a tick where Claude
// Code compacts it. Near there it says how close, as the terminal does. Given
// onCompact, it is also where to compact it now.
function ContextMeter({ context: { used, max, compact }, agent, onCompact }) {
  const [open, setOpen] = useState(false);
  const ref = useDismiss(open, () => setOpen(false));
  const pct = Math.min(100, (used / max) * 100);
  const limit = compact || max;
  const left = Math.max(0, Math.round(((compact - used) / compact) * 100));
  const near = compact > 0 && left <= 20;
  const className = cx("agent-context", used >= limit * 0.9 ? "high" : used >= limit * 0.75 && "warn");
  const title =
    `Context: ${used.toLocaleString()} of ${max.toLocaleString()} tokens (${Math.round(pct)}%). ` +
    (agent === "codex"
      ? "Codex compacts the conversation as it nears the end, summarising it to make room."
      : compact
        ? `Claude Code compacts the conversation at ${compact.toLocaleString()}, summarising it to make room.`
        : "Claude Code compacts the conversation as it nears the end, when auto-compact is on.");
  const meter = (
    <>
      <Bar pct={pct} tick={compact && (compact / max) * 100} />
      <span className="agent-context-tokens">
        {tokens(used)} / {tokens(max)} ·
      </span>{" "}
      {Math.round(pct)}%
      {near && (
        <span className="agent-context-left">
          {left}%<span className="until"> until auto-compact</span>
          <span className="short"> left</span>
        </span>
      )}
    </>
  );
  if (!onCompact) {
    return (
      <span className={className} title={title}>
        {meter}
      </span>
    );
  }
  return (
    <div className="model-menu" ref={ref}>
      <button className={cx("ghost", className)} title={title} onClick={() => setOpen((o) => !o)}>
        {meter}
      </button>
      {open && (
        <div className="model-list">
          <button onClick={() => (setOpen(false), onCompact())}>
            <span className="model-name">Compact now</span>
            <span className="model-note">Summarise the conversation so far to free up context, as /compact does</span>
          </button>
        </div>
      )}
    </div>
  );
}

const Bar = ({ pct, tick }) => (
  <span className="meter">
    <span style={{ width: `${pct}%` }} />
    {tick > 0 && tick < 100 && <i style={{ left: `${tick}%` }} />}
  </span>
);

// prettyModel names a model id the way a person would: claude-opus-5[1m] is Opus 5 (1M).
function prettyModel(id) {
  const m = /^claude-([a-z]+)-(\d+)(?:-(\d)(?!\d))?/.exec(id || "");
  if (!m) return id || "";
  const name = `${m[1][0].toUpperCase()}${m[1].slice(1)} ${m[2]}${m[3] ? "." + m[3] : ""}`;
  return /\[1m\]/.test(id) ? `${name} (1M)` : name;
}

// AgentView is Agent mode's main pane: one session's conversation, and the box
// to talk to it in. A session running in a terminal is followed, not talked to.
export default function AgentView({
  id, session, agents, newAgent, onNewAgent, modes, root, view, contextLines, wrap, threads, attached,
  onAttach, onDetach, onClearAttached, onRestoreAttached, onJump, onComment, onThreadAction, onSymbol, onOpenFile,
  requests, hooks, onHooks, onSelect, onNew, onStart, onStartAdded, temporaryNew, onTemporaryNew, onClose, onChanged, reveal, offline, active = true,
}) {
  // A session shows from its latest compaction until the reader asks for what
  // came before: by session, where it is shown from then.
  const [fromBy, setFromBy] = useState({});
  const from = fromBy[id] || "";
  const { items: read, live, earlier, lost: dropped } = useSession(id, "", from);
  // Whose session this is, Codex's or Claude Code's (""); for the blank one,
  // whose the next session will be.
  const agentKind = id ? session?.agent || live?.agent || "" : newAgent;
  // Codex's turns come with their times.
  const items = useMemo(() => (agentKind === "codex" ? read : withWorked(read, !!live?.busy)), [read, agentKind, live?.busy]);
  const name = agentName(agentKind);
  const { available, models = NO_MODELS, mode: defaultMode } = agents[agentKind] || agents[""];
  // Hidden rather than gone in the other modes, so coming back draws nothing
  // again; its keys and its grabs for focus wait until then.
  const activeRef = useRef(active);
  activeRef.current = active;
  const lost = offline || dropped;
  const [draft, setDraft] = usePref("repo", "draft:" + (id || "new"), "");
  // The slash commands the agent takes, asked for on coming to the view and
  // again on starting to type one, which is when a new skill would be wanted.
  const [commandsBy, setCommandsBy] = useState({});
  const commands = commandsBy[agentKind] || NO_COMMANDS;
  const slashing = draft.startsWith("/");
  useEffect(() => {
    if (!active || !available || (commands.length && !slashing)) return;
    api.agentCommands(agentKind).then((r) => r.commands?.length && setCommandsBy((by) => ({ ...by, [agentKind]: r.commands })), () => {});
  }, [active, available, slashing, agentKind]);
  // The suggestions under a slash: which is picked, and the draft Esc put them away for.
  const [pick, setPick] = useState(0);
  const [unsuggested, setUnsuggested] = useState(null);
  const suggestions = useMemo(() => (draft === unsuggested ? NO_COMMANDS : commandsFor(draft, commands)), [draft, unsuggested, commands]);
  useEffect(() => setPick(0), [draft]);
  // Pictures to go with the next message, by session like the draft: { key, mediaType, data }.
  const [imagesBy, setImagesBy] = useState({});
  const images = imagesBy[id || ""] || NO_IMAGES;
  const setImages = (change, to = id || "") => setImagesBy((by) => ({ ...by, [to]: change(by[to] || NO_IMAGES) }));
  // Sent from here, not yet in the conversation: { text, uuid, seen }, seen
  // being how many prompts already said the same, so a repeat still waits.
  const [pending, setPending] = useState([]);
  // What each message sent from here was written as, to put back in the box
  // when it is taken back: uuid -> { draft, attached, images }.
  const sent = useRef(new Map());
  // The last message sent, which Esc takes back for a moment after.
  const lastSent = useRef(null);
  const carry = useRef(null); // sent from the blank session, until its own is on screen
  const shownId = useRef(id);
  shownId.current = id;
  const [justSent, setJustSent] = useState(null);
  const [undoing, setUndoing] = useState(null); // { uuid, until }
  const [error, setError] = useState("");
  const [rewinding, setRewinding] = useState(null); // { prompt } or {} to pick one
  const inputRef = useRef(null);
  const scrollRef = useRef(null);
  const paneRef = useRef(null);
  // Text quoted from the conversation goes after what the box holds, with the
  // caret after it. Taken to a new session, it waits until that box is the one
  // on screen, after its own draft is read.
  const quoted = useRef(null);
  const caretToEnd = useRef(false);
  const addQuote = useCallback(
    (q) => {
      caretToEnd.current = true;
      setDraft((d) => `${[d.trimEnd(), q].filter(Boolean).join("\n\n")}\n\n`);
    },
    [setDraft],
  );
  const replyWith = useCallback((text, code) => addQuote(quote(text, code)), [addQuote]);
  const askInNew = useCallback(
    (text, code) => {
      quoted.current = quote(text, code);
      onStartAdded();
    },
    [onStartAdded],
  );
  useEffect(() => {
    if (id || !quoted.current) return;
    addQuote(quoted.current);
    quoted.current = null;
  }, [id]);
  useLayoutEffect(() => {
    const el = inputRef.current;
    if (!caretToEnd.current || !el) return;
    caretToEnd.current = false;
    el.focus({ preventScroll: true });
    el.setSelectionRange(el.value.length, el.value.length);
    el.scrollTop = el.scrollHeight;
  }, [draft]);
  // stick follows the end as the conversation grows; otherwise anchor holds the
  // reader's place, and for a moment after something is opened, opened keeps it
  // in view while it fills. top is where the view last was, and back where to
  // put it once the conversation is in.
  const follow = useRef({ stick: true, anchor: null, opened: null, top: 0, back: null });
  const [away, setAway] = useState(false);
  // Where the reader left each session scrolled up, to come back to there.
  const places = useRef(new Map());
  const shown = useRef(null);
  shown.current = { items, live };

  const running = live?.running || session?.running || "";
  const readOnly = running === "terminal";
  // Not known while dv is out of reach; the rest of live is only what it was.
  // A command run with ! holds the session as a turn does.
  const shellRunning = !!live?.shell && !live.shell.error && !live.shell.stopped;
  const busy = (!!live?.busy || shellRunning) && !lost;
  // Code added from a conversation goes with that conversation's next message,
  // whichever session Diff and Files are adding to.
  const attachTarget = useMemo(
    () => ({ choices: [{ id, label: "this session", agent: agentKind }], target: id, onTarget: () => {}, newAgent }),
    [id, agentKind, newAgent],
  );
  const byTool = useMemo(() => {
    const m = new Map();
    for (const t of threads) if (t.origin?.session === id) m.set(t.origin.tool, [...(m.get(t.origin.tool) || []), t]);
    return m;
  }, [threads, id]);
  // What can be rewound to: a command too, whose output - or a skill's turn - goes with it.
  const prompts = useMemo(() => items.filter((it) => (it.kind === "prompt" || it.kind === "command" || it.kind === "shell") && it.uuid), [items]);

  // Before anything is drawn for the new session, or its first update would
  // be followed to the end.
  useLayoutEffect(() => {
    setError("");
    setPending(carry.current ? [carry.current] : []);
    carry.current = null;
    browse.current = null;
    const place = places.current.get(id);
    follow.current = { stick: true, anchor: null, opened: null, top: 0, back: place && { ...place } };
    setAway(false);
    if (!readOnly && activeRef.current) inputRef.current?.focus({ preventScroll: true });
  }, [id]);
  useEffect(() => {
    if (active && !readOnly) inputRef.current?.focus({ preventScroll: true });
  }, [active]);

  const said = useMemo(() => {
    const n = new Map();
    for (const it of items) {
      const key = it.kind === "shell" ? "!" + it.text : it.kind === "prompt" || it.kind === "command" ? withoutVia(it.text) : null;
      if (key !== null) n.set(key, (n.get(key) || 0) + 1);
    }
    return n;
  }, [items]);
  useEffect(() => {
    setPending((p) => (p.some((m) => (said.get(m.text) || 0) > m.seen) ? p.filter((m) => (said.get(m.text) || 0) <= m.seen) : p));
  }, [said]);
  // What is on its way in: queued in the session from anywhere - another tab,
  // before a reload - until Claude takes it up, then sent from here until the
  // transcript has it.
  const queued = live?.queued || NO_QUEUED;
  const incoming = useMemo(() => {
    const out = queued.map((q) => ({ ...q, queued: true }));
    for (const m of pending) if (!out.some((w) => w.uuid === m.uuid)) out.push(m);
    return out;
  }, [queued, pending]);
  // Nothing said yet: the page's blank session, or one made and not written to.
  const blank = !items.length && !incoming.length && (!id || (live && !live.found));
  // Up goes back through what was said here, as in the terminal.
  const history = useMemo(() => prompts.map(saidAs).filter((t) => t.trim()), [prompts]);
  const runs = useMemo(() => runsOf(items, !!live?.busy, running), [items, live?.busy, running]);
  // What Claude left at work - agents, and commands in the background - over
  // the message box, each opening on what it is doing; peek is the call open.
  const atWork = useAtWork(items, !!live?.busy, running);
  const [peek, setPeek] = useState(null);
  const peekItem = peek && items.find((it) => it.toolId === peek);
  const peekRef = useRef(null);
  peekRef.current = peekItem;
  useEffect(() => setPeek(null), [id]);
  const browse = useRef(null); // { i, saved }: the entry shown, and the draft it replaced

  // Whenever the conversation changes size - an update, an edit's diff fetched
  // as it nears the screen, rows drawn, something opened - it is put back to
  // where the reader was. The browser's own scroll anchoring does not hold here.
  const keep = useCallback(() => {
    const el = scrollRef.current;
    // Hidden, everything measures 0: a place taken now would be lost on return.
    if (!el?.clientHeight) return;
    const anchor = follow.current.anchor;
    const a = anchor?.find((a) => a.el.isConnected);
    const moved = a ? offsetIn(el, a.el) - a.at : 0;
    if (Math.abs(moved) < 1) return;
    for (const b of anchor) if (b.el.isConnected) b.at = offsetIn(el, b.el);
    el.scrollTop += moved;
    follow.current.top = el.scrollTop;
  }, []);
  const hold = useCallback(() => {
    const el = scrollRef.current;
    const f = follow.current;
    if (!el?.clientHeight) return;
    if (f.back) {
      const { items, live } = shown.current;
      if (!live) return;
      const i = items.findIndex((it) => it.key === f.back.key);
      const item = i >= 0 && itemEls(el)[i];
      f.back.until ??= performance.now() + 3000;
      if (item && performance.now() < f.back.until) {
        const off = () => item.getBoundingClientRect().top - el.getBoundingClientRect().top - f.back.at;
        el.scrollTop += off();
        Object.assign(f, { stick: false, anchor: [{ el: item, at: offsetIn(el, item) }], top: el.scrollTop });
        // Drawn again, its diffs load again: until what is below has, the view
        // may not go down far enough, and until those around it have, it is
        // not laid out as it was. Each time something loads, it goes again.
        if (off() < 1 && !loadingNear(el)) f.back = null;
        return;
      }
      f.back = null;
    }
    if (f.opened && (!f.opened.el.isConnected || performance.now() > f.opened.until)) f.opened = null;
    // Following the end would carry a comment being written out of sight. It
    // holds the view instead, and the end is followed again once it closes.
    const writing = !f.opened && f.stick && commentInView(el);
    if (f.opened || writing) {
      // As much of it as fits, without pushing its top out.
      const box = el.getBoundingClientRect();
      const r = (writing || f.opened.el).getBoundingClientRect();
      const down = Math.min(r.bottom - box.bottom + 12, r.top - box.top - 12);
      if (down > 0) el.scrollTop += down;
      f.top = el.scrollTop;
    } else if (f.stick) {
      el.scrollTop = el.scrollHeight;
      f.top = el.scrollTop;
    } else {
      keep();
    }
  }, [keep]);
  useLayoutEffect(hold, [items, live, incoming]);
  useEffect(() => {
    const el = scrollRef.current;
    if (!el?.firstElementChild) return;
    const ro = new ResizeObserver(hold);
    ro.observe(el.firstElementChild);
    // The view itself shrinks as the message box grows.
    ro.observe(el);
    // What was opened stays where it was clicked, and comes into view as it
    // fills; closing it leaves it where it was.
    const onToggle = (e) => {
      const f = follow.current;
      f.anchor = [{ el: e.target, at: offsetIn(el, e.target) }];
      f.opened = e.detail.open ? { el: e.target, until: performance.now() + 1500 } : null;
    };
    // Until the reader scrolls themselves.
    const onReader = () => {
      follow.current.opened = follow.current.back = null;
    };
    el.addEventListener("dv:toggle", onToggle);
    el.addEventListener("wheel", onReader, { passive: true });
    el.addEventListener("pointerdown", onReader);
    el.addEventListener("keydown", onReader);
    return () => {
      ro.disconnect();
      el.removeEventListener("dv:toggle", onToggle);
      el.removeEventListener("wheel", onReader);
      el.removeEventListener("pointerdown", onReader);
      el.removeEventListener("keydown", onReader);
    };
  }, [available, hold, blank]);
  const onScroll = () => {
    const el = scrollRef.current;
    const f = follow.current;
    if (!el.clientHeight) return;
    findUnder();
    // The last session's conversation giving way, not the reader.
    if (f.back || !live) return;
    const below = el.scrollHeight - el.scrollTop - el.clientHeight;
    // Only scrolling up leaves the end. Otherwise a diff that loaded after the
    // view went to the end, but before the event for that came, would read as
    // the reader having moved away from it.
    f.stick = below < 8 || (f.stick && !f.opened && el.scrollTop >= f.top - 1);
    f.top = el.scrollTop;
    if (f.stick) {
      f.anchor = null;
      if (below >= 8) hold();
    } else {
      // What grew in the frame this scroll came in, before taking a new place.
      keep();
      f.anchor = anchorOf(el);
    }
    places.current.set(id, f.stick ? null : placeOf(el, f.anchor, items));
    setAway(el.scrollHeight - el.scrollTop - el.clientHeight > 150);
  };
  // What came before shows above, the view holding on to what was at the top.
  const showEarlier = () => {
    const el = scrollRef.current;
    const first = el && itemEls(el)[0];
    if (first) Object.assign(follow.current, { stick: false, opened: null, back: null, anchor: [{ el: first, at: offsetIn(el, first) }] });
    setFromBy((by) => ({ ...by, [id]: earlier.next }));
  };
  const toLatest = () => {
    const el = scrollRef.current;
    el.scrollTop = el.scrollHeight;
    follow.current = { stick: true, anchor: null, opened: null, top: el.scrollTop };
  };

  // Your messages are easy to scroll past in a long session: the one the view
  // is under, once it is out of sight, is named over the conversation, and [
  // and ] step between them.
  const [under, setUnder] = useState(null); // { text }
  const yours = () => [...(scrollRef.current?.querySelectorAll(".agent-log > .agent-prompt:not(.pending)") || [])];
  const findUnder = () => {
    const el = scrollRef.current;
    const all = yours();
    // Under the bar's own height, a message counts as reached.
    const top = el.getBoundingClientRect().top + UNDER_PX;
    let lo = 0;
    let hi = all.length - 1;
    let at = -1;
    while (lo <= hi) {
      const mid = (lo + hi) >> 1;
      if (all[mid].getBoundingClientRect().top <= top) (at = mid), (lo = mid + 1);
      else hi = mid - 1;
    }
    const p = all[at];
    const out = p && p.getBoundingClientRect().bottom < top - UNDER_PX + 8;
    const text = out ? p.querySelector(".agent-prompt-text:not(.dim)")?.textContent.split("\n")[0] || "(images)" : null;
    setUnder((u) => (u?.text === text && u?.at === at ? u : text === null ? null : { text, at }));
  };
  const toYours = (dir) => {
    const el = scrollRef.current;
    if (!el) return;
    const top = el.getBoundingClientRect().top;
    const all = yours();
    const to = dir < 0 ? all.findLast((p) => p.getBoundingClientRect().top < top - 4) : all.find((p) => p.getBoundingClientRect().top > top + UNDER_PX);
    if (!to) return dir > 0 && toLatest();
    Object.assign(follow.current, { opened: null, back: null });
    el.scrollTop += to.getBoundingClientRect().top - top - 18; // clear of the strip under the header
  };
  useEffect(() => setUnder(null), [id]);
  const step = useRef();
  step.current = toYours;
  useEffect(() => {
    const onKey = (e) => {
      if (!activeRef.current || peekRef.current || (e.key !== "[" && e.key !== "]") || e.metaKey || e.ctrlKey || e.altKey || isTyping(e.target)) return;
      if (document.querySelector(".backdrop, .prompt-backdrop:not([hidden])")) return;
      e.preventDefault();
      step.current(e.key === "]" ? 1 : -1);
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
  // Ctrl/Cmd+Down goes to the end of the conversation, from the message box
  // too, which is why it is taken on the way down. At the end already, the box
  // keeps the key.
  const latest = useRef();
  latest.current = toLatest;
  useEffect(() => {
    const onKey = (e) => {
      if (!activeRef.current || e.key !== "ArrowDown" || !(e.metaKey || e.ctrlKey) || e.shiftKey || e.altKey) return;
      const el = scrollRef.current;
      if (!el || el.scrollHeight - el.scrollTop - el.clientHeight < 8) return;
      if (document.querySelector(".backdrop, .prompt-backdrop:not([hidden])")) return;
      e.preventDefault();
      e.stopPropagation();
      latest.current();
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, []);
  // Alt+N starts a session, since the browser keeps Ctrl+N. Read by code, as
  // Option+N on a Mac types a dead key.
  useEffect(() => {
    const onKey = (e) => {
      if (!activeRef.current || e.code !== "KeyN" || !e.altKey || e.metaKey || e.ctrlKey || e.shiftKey) return;
      if (document.querySelector(".backdrop, .prompt-backdrop:not([hidden])")) return;
      e.preventDefault();
      e.stopPropagation();
      onStart();
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [onStart]);

  // A comment's way back to the edit it was left on.
  useEffect(() => {
    if (!reveal?.tool || !items.length) return;
    const el = document.getElementById("tool-" + reveal.tool);
    if (!el) {
      // Before what is shown, maybe; items changing comes back here.
      if (earlier) setFromBy((by) => ({ ...by, [id]: "all" }));
      return;
    }
    follow.current = { stick: false, anchor: null, opened: null, top: 0 };
    el.scrollIntoView({ block: "center" });
  }, [reveal, items.length]);

  // The message shows at once, and from the blank session is carried into the
  // one made for it.
  const send = async () => {
    if (lost) return; // the note over the box says why; the draft stays
    if (draft.startsWith("!")) return runShell(draft.slice(1).trim());
    // A command goes alone; what was added and the pictures wait in the box for the next message.
    const command = commandOf(draft, commands);
    if (command?.name === "clear") {
      setDraft("");
      onStart();
      return;
    }
    const text = command ? command.text : composeMessage(draft, attached, threads);
    const pictures = command ? NO_IMAGES : images;
    if ((!text && !pictures.length) || readOnly) return;
    setError("");
    const uuid = newUUID();
    sent.current.set(uuid, { draft, attached: command ? [] : attached, images: pictures });
    const entry = { text, uuid, pictures, seen: said.get(text) || 0, command: !!command };
    carry.current = id ? null : entry;
    setPending((p) => [...p, entry]);
    setDraft("");
    if (!command) {
      setImages(() => NO_IMAGES);
      onClearAttached();
    }
    browse.current = null;
    Object.assign(follow.current, { stick: true, anchor: null, opened: null, back: null });
    let to = id;
    try {
      to ||= await onNew();
      const start = id ? null : { ...startWith, ...picks };
      if (start && Object.keys(start).length) {
        await api.agentSettings(to, start);
        setPicks({});
      }
      if (!id && temporaryNew) {
        await api.agentTemporary(to, true);
        onTemporaryNew(false);
      }
      await api.agentSend(to, text, uuid, pictures.map(({ mediaType, data }) => ({ mediaType, data })));
      // Taking a message back rewinds to before it, which does not undo a command.
      if (!command) {
        lastSent.current = { session: to, uuid, at: Date.now(), queued: busy };
        setJustSent(uuid);
      }
      onChanged();
    } catch (e) {
      carry.current = null;
      putBack([entry], to || "");
      setError(e.message);
    }
  };
  // runShell is the terminal's !: dv runs the command, and the agent is sent it
  // with what it printed. What was added and the pictures wait for the next message.
  const runShell = async (command) => {
    if (!command || readOnly) return;
    setError("");
    const uuid = newUUID();
    const entry = { text: "!" + command, uuid, shell: command, seen: said.get("!" + command) || 0 };
    sent.current.set(uuid, { draft, attached: [], images: NO_IMAGES });
    carry.current = id ? null : entry;
    setPending((p) => [...p, entry]);
    setDraft("");
    browse.current = null;
    Object.assign(follow.current, { stick: true, anchor: null, opened: null, back: null });
    let to = id;
    try {
      to ||= await onNew();
      await api.agentShell(to, command, uuid);
      onChanged();
    } catch (e) {
      carry.current = null;
      putBack([entry], to || "");
      setError(e.message);
    }
  };
  // A command stopped, or that could not reach the agent, comes back to the box.
  useEffect(() => {
    const s = live?.shell;
    const entry = (s?.error || s?.stopped) && pending.find((w) => w.uuid === s.uuid);
    if (!entry) return;
    putBack([entry], id);
    if (s.error) setError(s.error);
  }, [live?.shell?.error, live?.shell?.stopped]);
  // compactNow is the terminal's /compact, sent without touching the box.
  const compactNow = async () => {
    const entry = { text: "/compact", uuid: newUUID(), seen: said.get("/compact") || 0, command: true };
    setError("");
    setPending((p) => [...p, entry]);
    Object.assign(follow.current, { stick: true, anchor: null, opened: null, back: null });
    try {
      await api.agentSend(id, entry.text, entry.uuid, []);
      onChanged();
    } catch (e) {
      setPending((p) => p.filter((w) => w !== entry));
      setError(e.message);
    }
  };
  useEffect(() => {
    if (!justSent) return;
    const t = setTimeout(() => setJustSent(null), TAKE_BACK_MS);
    return () => clearTimeout(t);
  }, [justSent]);

  const addImages = async (files) => {
    for (const file of files) {
      try {
        const img = await readImage(file);
        setImages((list) => [...list, img]);
      } catch (e) {
        setError(e.message);
      }
    }
  };

  // putBack returns messages to the box they were sent from: their words, the
  // pictures and what was added with them. to is the session whose box it is,
  // "" for the blank one.
  const putBack = async (messages, to) => {
    const from = id;
    setPending((p) => p.filter((w) => !messages.some((m) => m.uuid === w.uuid)));
    setJustSent(null);
    const words = [];
    for (const m of messages) {
      if (lastSent.current?.uuid === m.uuid) lastSent.current = null;
      const s = sent.current.get(m.uuid) || (await readBack(m, from, threads));
      words.push(s.draft);
      if (s.images.length) setImages((list) => [...s.images, ...list], to);
      if (s.attached.length) onRestoreAttached(to, s.attached);
    }
    const put = (old) => [...words, old].filter((t) => t.trim()).join("\n\n");
    // The box's draft follows the session on screen, which may have moved on
    // since this was called.
    if (to === (shownId.current || "")) setDraft(put);
    else {
      const key = "draft:" + (to || "new");
      setPref("repo", key, put(readPref("repo", key, "")), "");
    }
    requestAnimationFrame(() => inputRef.current?.focus());
  };

  // Messages still waiting for the step Claude is on come back to be edited.
  const takeBackQueued = async () => {
    const back = [];
    for (const q of queued) {
      const r = await api.agentUnqueue(id, q.uuid).catch(() => null);
      if (r?.cancelled) back.push(q);
    }
    if (back.length) putBack(back, id);
  };

  // A message that started a turn a moment ago is taken back once Claude has
  // stopped and the transcript has it: the conversation goes back to before
  // it, and so does any file it got as far as changing.
  const takeBack = (s) => {
    lastSent.current = null;
    setUndoing({ uuid: s.uuid, until: Date.now() + 10_000 });
    if (busy) api.agentInterrupt(id).catch(() => {});
  };
  useEffect(() => {
    if (!undoing) return;
    const i = items.findIndex((it) => it.uuid === undoing.uuid);
    if (i < 0 || busy) {
      const t = setTimeout(() => {
        setUndoing(null);
        setError(`Could not take the message back: ${name} has not stopped. Esc Esc rewinds to before it once it has.`);
      }, undoing.until - Date.now());
      return () => clearTimeout(t);
    }
    setUndoing(null);
    const prompt = items[i];
    // Codex keeps no copies of the files, so its edits stay.
    const code = agentKind !== "codex" && items.slice(i + 1).some((it) => it.result?.edited);
    rewind(prompt, { conversation: true, code });
  }, [undoing, items, busy]);

  const interrupt = () => api.agentInterrupt(id).catch((e) => setError(e.message));
  // Saying no leaves the agent going, so stopping while it asks is both: no to
  // everything waiting, which lets it read again, and then the interrupt.
  const stopAsked = async () => {
    try {
      for (const r of waiting) await api.claudeAnswer(r.id, { allowed: false });
      await api.agentInterrupt(id);
    } catch (e) {
      setError(e.message);
    }
  };
  // With no session yet, the mode picked waits for the one the first message
  // starts; the model and effort are the remembered ones below.
  const [picks, setPicks] = useState({});
  // The model and effort last picked, by agent, since the models are the
  // agent's own: where a new session starts, as it starts with the agent last
  // picked. { "": { model, effort }, codex: … }
  const [picked, setPicked] = usePref("user", "newModel", NO_PICKED);
  // Only what the agent still offers: a model it has dropped, or an effort that
  // model no longer takes, is not asked for - dv would turn it down, as would
  // the agent. Until it has answered with its models, nothing is.
  const startWith = useMemo(() => {
    const want = picked[agentKind] || NO_PICKED;
    const on = models.find((c) => c.id === (want.model || ""));
    const start = {};
    // "" is the agent's own model, which a session starts on anyway.
    if (want.model && on) start.model = want.model;
    if (want.effort && on?.efforts?.includes(want.effort)) start.effort = want.effort;
    return start;
  }, [picked, agentKind, models]);
  const temporary = id ? !!session?.temporary : temporaryNew;
  const toggleTemporary = () => {
    if (!id) return onTemporaryNew(!temporary);
    api.agentTemporary(id, !temporary).then(onChanged, (e) => setError(e.message));
  };
  // The models are the agent's own, so what was picked goes with the agent.
  const pickAgent = (kind) => {
    setPicks({});
    onNewAgent(kind);
  };
  // A model or effort picked for the session to come is where the next one
  // starts too; one picked in a session under way is that conversation's own.
  // The mode is never remembered: a new session takes the agent's.
  const settings = (patch) => {
    if (id) return api.agentSettings(id, patch).catch((e) => setError(e.message));
    const { mode, ...pick } = patch;
    if (Object.keys(pick).length) {
      setPicked((all) => {
        const next = { ...(all[agentKind] || NO_PICKED), ...pick };
        // As a session does: an effort the new model does not take is dropped.
        const m = pick.model !== undefined && models.find((c) => c.id === pick.model);
        if (m && !m.efforts?.includes(next.effort)) delete next.effort;
        return { ...all, [agentKind]: next };
      });
    }
    if (mode !== undefined) setPicks((p) => ({ ...p, mode }));
  };

  // The picker has every message, those from before the compaction the page
  // shows from fetched as it opens: what is on the page lists at once.
  const [allPrompts, setAllPrompts] = useState(null); // { id, list }
  const picking = !!rewinding && !rewinding.prompt;
  useEffect(() => {
    if (!picking || !earlier) return;
    let gone = false;
    api.agentPrompts(id).then((r) => gone || setAllPrompts({ id, list: r.prompts || [] }), () => {});
    return () => {
      gone = true;
    };
  }, [picking, id]);
  const rewindable = useMemo(() => {
    if (!earlier || allPrompts?.id !== id) return prompts;
    const shown = new Set(prompts.map((p) => p.key));
    return [...allPrompts.list.filter((p) => !shown.has(p.key)).map((p) => ({ ...p, compacted: true })), ...prompts];
  }, [prompts, allPrompts, earlier, id]);

  const rewind = async (prompt, how) => {
    setRewinding(null);
    setError("");
    try {
      // Back past the first message there is no conversation left to keep:
      // the blank session starts again, and this one is put away.
      const first = !prompt.before && how.conversation;
      if (!first || how.code) await api.agentRewind(id, { prompt: prompt.uuid, before: prompt.before, conversation: how.conversation && !first, code: how.code });
      if (first) {
        await api.agentOpen(id, false);
        onSelect("");
      }
      // The message comes back to be edited, as the terminal does it.
      if (how.conversation) putBack([prompt], first ? "" : id);
      onChanged();
    } catch (e) {
      setError(e.message);
    }
  };

  // Esc twice picks a message to rewind to, as in the terminal. Once, it takes
  // back what is queued, or a message sent a moment ago; otherwise, while
  // Claude works, it stops it.
  const lastEsc = useRef(0);
  const escape = useRef();
  escape.current = () => {
    if (peekRef.current) return setPeek(null);
    const now = Date.now();
    if (now - lastEsc.current < DOUBLE_ESC_MS) {
      lastEsc.current = 0;
      if (!readOnly && (prompts.length || earlier)) setRewinding({});
      return;
    }
    lastEsc.current = now;
    if (readOnly || lost) return;
    // A request waiting holds the turn, so an interrupt alone would not reach
    // the agent: Esc says no to what is asked and then stops it, as it does in
    // the terminal. The buttons keep their own meanings, plans included.
    if (waiting.length) {
      stopAsked();
      lastEsc.current = 0;
      return;
    }
    if (!undoLast()) {
      if (busy && (running === "dv" || shellRunning)) interrupt();
      return;
    }
    lastEsc.current = 0;
  };
  // Takes back what is queued, or the message sent a moment ago, reporting
  // whether there was one.
  const undoLast = () => {
    const s = lastSent.current;
    if (queued.length) takeBackQueued();
    else if (s?.session === id && !s.queued && Date.now() - s.at < TAKE_BACK_MS && running === "dv") takeBack(s);
    else return false;
    return true;
  };
  // For a moment after sending, the send button takes the message back, as
  // Esc does: the one way to on a phone. A tap too soon for that is the one
  // that sent it, come down twice.
  const tapUndo = () => {
    if (Date.now() - (lastSent.current?.at || 0) >= UNDO_ARMS_MS) undoLast();
  };
  const onEscape = useCallback(() => escape.current(), []);
  useEffect(() => {
    const onKey = (e) => {
      if (!activeRef.current || e.key !== "Escape" || e.shiftKey || isTyping(e.target)) return;
      if (document.querySelector(".backdrop, .prompt-backdrop:not([hidden])")) return;
      onEscape();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onEscape]);

  // Up, on the box's first line, brings back what is queued, else steps back
  // through what was said; Down steps forward again, to the draft it replaced.
  const recall = (e) => {
    const el = e.currentTarget;
    if (e.altKey || e.metaKey || e.ctrlKey || e.shiftKey || el.selectionStart !== el.selectionEnd) return;
    const b = browse.current?.i < history.length && draft === history[browse.current.i] ? browse.current : null;
    if (e.key === "ArrowUp") {
      if (draft.lastIndexOf("\n", el.selectionStart - 1) >= 0) return;
      if (!draft.trim() && queued.length) {
        e.preventDefault();
        takeBackQueued();
        return;
      }
      const at = b ? b.i : draft.trim() ? 0 : history.length;
      if (at === 0) return;
      e.preventDefault();
      browse.current = { i: at - 1, saved: b ? b.saved : draft };
      setDraft(history[at - 1]);
      requestAnimationFrame(() => el.setSelectionRange(0, 0));
    } else if (e.key === "ArrowDown" && b) {
      if (draft.indexOf("\n", el.selectionEnd) >= 0) return;
      e.preventDefault();
      browse.current = b.i + 1 < history.length ? { i: b.i + 1, saved: b.saved } : null;
      setDraft(browse.current ? history[b.i + 1] : b.saved);
    }
  };

  // Until a session starts, it is on what was last picked for its agent, and
  // otherwise in whatever the user's settings start it in.
  const asked = id ? live : { ...startWith, ...picks };
  // Shift+Tab steps the picker at once, and only the mode it stops on goes to
  // Claude Code: stepping through plan mode mid-turn would put Claude in it.
  const [stepping, setStepping] = useState(null); // { id, mode, sent }
  const stepTimer = useRef(null);
  const mode = (stepping?.id === id && stepping.mode) || asked?.mode || defaultMode || "default";
  useEffect(() => {
    if (stepping?.sent && (stepping.id !== id || asked?.mode === stepping.mode)) setStepping(null);
  }, [stepping, id, asked?.mode]);
  const cycleMode = () => {
    const i = modes.findIndex((m) => m.id === mode);
    const next = modes[(i + 1) % modes.length].id;
    if (!id) return settings({ mode: next });
    setStepping({ id, mode: next });
    clearTimeout(stepTimer.current);
    stepTimer.current = setTimeout(() => {
      api.agentSettings(id, { mode: next }).then(
        () => setStepping((s) => (s?.mode === next ? { ...s, sent: true } : s)),
        (e) => {
          setError(e.message);
          setStepping(null);
        },
      );
    }, MODE_SETTLE_MS);
  };
  const pickMode = (m) => {
    clearTimeout(stepTimer.current);
    setStepping(null);
    settings({ mode: m });
  };
  const modelChoices = useMemo(
    // "" is the model the user's settings start on; Claude Code's "default" is the one it recommends.
    () =>
      models.map((c) => ({
        ...c,
        label: (/^claude-/.test(c.model) && prettyModel(c.model)) || c.label,
        tag: c.id === "" ? (c.model ? "default" : "") : c.id === "default" ? "recommended" : "",
      })),
    [models],
  );
  // The model the session is on: what it reports once running, else what was
  // asked for, else - resumed - what the transcript was last answered on.
  const chosen = modelChoices.find((m) => m.id === (asked?.model || ""));
  const onModel =
    (live?.using && modelChoices.find((m) => sameModel(m.model, live.using))) ||
    (!live?.model && live?.lastModel && modelChoices.find((m) => sameModel(m.model, live.lastModel))) ||
    chosen;
  // Claude's ids read as names; Codex's are named by its own list.
  const nameOf = (model) => (/^claude-/.test(model || "") ? prettyModel(model) : modelChoices.find((m) => sameModel(m.model, model))?.label || model);
  const modelLabel = nameOf(live?.using) || (!live?.model && live?.lastModel && (onModel?.label || nameOf(live.lastModel))) || chosen?.label || asked?.model;
  const effort = live?.effortUsing || asked?.effort || onModel?.effort || "";

  // Asked here, where the terminal would ask it, one at a time.
  const waiting = requests.filter((r) => r.session === id);
  const ask = waiting[0];
  const [drafts, setDrafts] = useState({}); // request id -> { note, comments }
  // The message box gives way to a request as the terminal's does, but not
  // with a message half written, where a digit typed next would answer it.
  const grabFocus = () => {
    const a = document.activeElement;
    return activeRef.current && (!a || a === document.body || (a === inputRef.current && !a.value));
  };
  // Nor while the reader is pressing keys: an empty box is also where Shift+Tab
  // steps the mode, and the next one would answer the request instead. It shows
  // at once, and has the keys once they stop.
  const lastKey = useRef(0);
  useEffect(() => {
    const onKey = () => (lastKey.current = performance.now());
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, []);
  const [settled, setSettled] = useState(null); // the request that has the keys
  useEffect(() => {
    if (!ask) return;
    let t;
    const wait = () => {
      const left = lastKey.current + KEYS_SETTLE_MS - performance.now();
      if (left <= 0) setSettled(ask.id);
      else t = setTimeout(wait, left);
    };
    wait();
    return () => clearTimeout(t);
  }, [ask?.id]);
  useEffect(() => {
    const a = document.activeElement;
    if (activeRef.current && !ask && !readOnly && (!a || a === document.body)) inputRef.current?.focus({ preventScroll: true });
  }, [ask?.id]);
  // Coming back to the window lands in the message box too, unless a field or
  // a window has the focus. Not on a touch screen, where it brings up the keyboard.
  useEffect(() => {
    if (ask || readOnly || matchMedia("(pointer: coarse)").matches) return;
    const onFocus = () => {
      if (!activeRef.current || isTyping(document.activeElement) || document.querySelector(".backdrop, .prompt-backdrop:not([hidden])")) return;
      inputRef.current?.focus({ preventScroll: true });
    };
    window.addEventListener("focus", onFocus);
    return () => window.removeEventListener("focus", onFocus);
  }, [ask?.id, readOnly]);

  // c and a act on the edit line under the pointer, as they do in Diff.
  useEffect(() => {
    const onKey = (e) => {
      if (!activeRef.current || (e.key !== "c" && e.key !== "a") || e.metaKey || e.ctrlKey || e.altKey || isTyping(e.target)) return;
      if (document.querySelector(".backdrop, .prompt-backdrop:not([hidden])")) return;
      const cell = document.querySelector(".agent-edit [data-line][data-side]:hover");
      if (!cell) return;
      e.preventDefault();
      const ev = commentEvent(cell);
      cell.closest(".agent-edit").dispatchEvent(e.key === "c" ? ev : new CustomEvent("dv:attach", { detail: ev.detail }));
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  // The box grows with what is written in it, to a point. Its card holds its
  // height while the box is measured: collapsed, the box would let the
  // conversation grow for a moment, and the view would slip off its end.
  useLayoutEffect(() => {
    const el = inputRef.current;
    if (!el) return;
    const card = el.parentElement;
    card.style.minHeight = card.offsetHeight + "px";
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight, 320) + "px";
    card.style.minHeight = "";
  }, [draft, blank, readOnly, lost]);

  const [dropping, setDropping] = useState(false);
  const fileRef = useRef(null);
  const canSend = !!(draft.trim() || images.length || attached.length);
  // A phone's keyboard shows Enter as a new line, and there the button sends.
  const enterSends = !useMedia(TOUCH);
  const command = useMemo(() => commandOf(draft, commands), [draft, commands]);
  const shell = draft.startsWith("!");
  const menuRef = useRef(null);
  useEffect(() => {
    menuRef.current?.children[pick]?.scrollIntoView({ block: "nearest" });
  }, [pick]);
  const complete = (c) => {
    setDraft(`/${c.name} `);
    inputRef.current?.focus();
  };
  const box = (
    <div className="agent-composer">
      <div
        className={cx("composer-card", dropping && "dropping", (command || shell) && "command")}
        onDragOver={(e) => {
          if (!e.dataTransfer.types.includes("Files")) return;
          e.preventDefault();
          setDropping(true);
        }}
        onDragLeave={(e) => e.currentTarget.contains(e.relatedTarget) || setDropping(false)}
        onDrop={(e) => {
          if (!e.dataTransfer.files.length) return;
          e.preventDefault();
          setDropping(false);
          addImages([...e.dataTransfer.files]);
        }}
      >
        {suggestions.length > 0 && (
          <div className="model-list up command-menu" ref={menuRef}>
            {suggestions.map((c, i) => (
              <button
                key={c.name}
                className={cx(i === pick && "on")}
                onMouseDown={(e) => e.preventDefault()}
                onMouseMove={() => setPick(i)}
                onClick={() => complete(c)}
              >
                <span className="model-name">
                  /{c.name}
                  {c.argumentHint && <span className="command-args"> {c.argumentHint}</span>}
                </span>
                {c.description && <span className="model-note">{c.description}</span>}
              </button>
            ))}
          </div>
        )}
        {(attached.length > 0 || images.length > 0) && (
          <div className={cx("agent-attached", (command || shell) && "held")}>
            {images.map((img) => (
              <span className="agent-image-chip" key={img.key} title={img.name || "Image"}>
                <img src={dataURL(img)} alt="" />
                <button className="agent-chip-x" onClick={() => setImages((list) => list.filter((x) => x !== img))} title="Leave it out">
                  <IconX size={10} />
                </button>
              </span>
            ))}
            {attached.map((a) => {
              const t = a.kind === "thread" ? threads.find((x) => x.id === a.threadId) : null;
              if (a.kind === "thread" && !t) return null;
              return (
                <Chip
                  key={a.key}
                  kind={a.kind === "lines" ? "code" : a.kind === "thread" ? "comment" : "file"}
                  path={t ? t.file : a.file}
                  lines={t ? span(t.startLine, t.endLine) : a.kind === "lines" ? span(a.start, a.end) : ""}
                  onClick={() => onJump(a)}
                  onRemove={() => onDetach(a.key)}
                />
              );
            })}
            {attached.length > 1 && (
              <button className="link" onClick={onClearAttached}>
                Clear
              </button>
            )}
            {(command || shell) && <span className="agent-attached-held">Kept for your next message</span>}
          </div>
        )}
        <textarea
          ref={inputRef}
          value={draft}
          rows={blank ? 2 : 1}
          placeholder={busy ? `Add to what ${name} is doing` : blank ? `Ask ${name} to do something in this repository` : `Reply to ${name}`}
          onChange={(e) => {
            setDraft(e.target.value);
          }}
          onPaste={(e) => {
            const files = imagesIn(e.clipboardData);
            if (!files.length) return;
            if (!e.clipboardData.getData("text/plain")) e.preventDefault();
            addImages(files);
          }}
          onKeyDown={(e) => {
            // With nothing to select, Shift+arrows go on to step the modes.
            if (!(draft === "" && e.shiftKey && (e.key === "ArrowLeft" || e.key === "ArrowRight"))) e.stopPropagation();
            // Over suggestions, the arrows pick one and Tab takes it; so does
            // Enter, unless it is what is written already.
            if (suggestions.length && !e.nativeEvent.isComposing) {
              const c = suggestions[pick];
              if (e.key === "ArrowDown" || e.key === "ArrowUp") {
                e.preventDefault();
                setPick((p) => (p + (e.key === "ArrowDown" ? 1 : -1) + suggestions.length) % suggestions.length);
                return;
              }
              if ((e.key === "Tab" && !e.shiftKey) || (e.key === "Enter" && !e.shiftKey && draft !== `/${c.name}`)) {
                e.preventDefault();
                complete(c);
                return;
              }
              if (e.key === "Escape") {
                e.preventDefault();
                setUnsuggested(draft);
                return;
              }
            }
            if (e.key === "Enter" && !e.shiftKey && !e.nativeEvent.isComposing && (enterSends || e.metaKey || e.ctrlKey)) {
              e.preventDefault();
              send();
            } else if (e.key === "Escape") {
              e.preventDefault();
              onEscape();
            } else if (e.key === "Tab" && e.shiftKey) {
              e.preventDefault();
              cycleMode();
            } else if (e.key === "ArrowUp" || e.key === "ArrowDown") {
              recall(e);
            }
          }}
        />
        {command && !suggestions.length && (
          <div className="composer-command" title={command.description}>
            <span className="composer-command-name">/{command.name}</span>
            <span className="composer-command-note">{command.description || `A ${agentKind === "codex" ? "Codex" : "Claude Code"} command`}</span>
          </div>
        )}
        {shell && (
          <div className="composer-command">
            <span className="composer-command-name">!</span>
            <span className="composer-command-note">Runs in your shell, and {name} is sent what it prints</span>
          </div>
        )}
        <div className="composer-bar">
          <AddImages
            onFiles={() => fileRef.current.click()}
            onPaste={() => pasteImages().then((files) => (files.length ? addImages(files) : setError("There is no image to paste."))).catch((e) => setError(e.message))}
          />
          <input
            ref={fileRef}
            type="file"
            accept={IMAGE_TYPES.join(",")}
            multiple
            hidden
            onChange={(e) => {
              addImages([...e.target.files]);
              e.target.value = "";
            }}
          />
          {!id && agents[""].available && agents.codex.available && (
            <Picker label={agentLabel(agentKind)} title="The agent this session runs" choices={AGENT_CHOICES} value={agentKind} onPick={pickAgent} />
          )}
          {/* Hidden for the moment it takes to ask the agent, rather than naming no model. */}
          {modelChoices.length > 0 && (
            <ModelPicker
              label={modelLabel}
              models={modelChoices}
              model={asked?.model || ""}
              efforts={onModel?.efforts}
              effort={effort}
              pending={live?.pending}
              onPick={(patch) => settings(patch)}
            />
          )}
          <Picker
            label={modes.find((m) => m.id === mode)?.label || MODE_NAMES[mode] || mode}
            title={live?.pending ? `Permission mode (Shift+Tab). ${name} takes it from the next turn.` : "Permission mode (Shift+Tab)"}
            pending={live?.pending}
            choices={modes}
            value={mode}
            onPick={pickMode}
          />
          {!readOnly && (
            <button
              className={cx("ghost", temporary && "on")}
              aria-pressed={temporary}
              onClick={toggleTemporary}
              title={
                temporary
                  ? `Temporary: once closed, this session leaves the list. Its transcript stays for ${resumeWith(agentKind)}. Click to keep it.`
                  : "Make this session temporary: once closed, it leaves the list"
              }
            >
              <IconTemporary size={13} />
              {temporary && <span className="btn-label">Temporary</span>}
            </button>
          )}
          <span className="spacer" />
          {justSent && lastSent.current?.session === id && !canSend && !lost ? (
            <button className="composer-send" onClick={tapUndo} title="Take back what you sent (Esc)">
              <IconUndo size={14} />
            </button>
          ) : busy && (running === "dv" || shellRunning) && !canSend ? (
            <button className="composer-send stop" onClick={interrupt} title={shellRunning ? "Stop the command (Esc)" : `Stop what ${name} is doing (Esc)`}>
              <IconStop size={12} />
            </button>
          ) : (
            <button
              className="composer-send"
              onClick={send}
              disabled={!canSend || lost}
              title={busy ? `Send: ${name} reads it once the step it is on is done (Enter)` : "Send (Enter, Shift+Enter for a new line)"}
            >
              <IconArrowUp size={15} />
            </button>
          )}
        </div>
      </div>
    </div>
  );

  // A message on its way in: pictures sent from here are shown from what was sent.
  const bubble = (w) => (
    <Prompt
      key={w.uuid}
      item={w.shell ? { kind: "shell", text: w.shell } : { kind: w.command || commandOf(w.text, commands) ? "command" : "prompt", text: w.text, images: w.images }}
      pictures={w.pictures || sent.current.get(w.uuid)?.images}
      pending
      queued={w.queued}
      hint={w.queued ? "Esc or ↑ to edit" : w.uuid === justSent ? "Esc to edit" : undefined}
    />
  );

  if (!agents[""].available && !agents.codex.available) {
    return (
      <div className="agent-pane" hidden={!active}>
        <div className="empty-state">
          <h2>Neither Claude Code nor Codex is installed</h2>
          <p>
            The Agent view runs the <code>claude</code> or <code>codex</code> CLI, and neither is on your PATH. Install one and reload.
          </p>
        </div>
      </div>
    );
  }

  return (
    <AttachTarget.Provider value={attachTarget}>
    <OpenCall.Provider value={setPeek}>
    <div className="agent-pane" hidden={!active} ref={paneRef}>
      <SelectionAsk within={paneRef} active={active} onReply={id && !readOnly ? replyWith : null} onAsk={askInNew} />
      {id && (
        // What the session is and where it runs, its card in the list says; this
        // is what to do in it.
        <header className="agent-head">
          {peekItem ? (
            <PeekTitle item={peekItem} root={root} busy={!!live?.busy} running={running} onBack={() => setPeek(null)} />
          ) : under && !blank && (
            <span className="agent-under">
              <button className="agent-under-text" onClick={() => toYours(-1)} title="Back to this message of yours ([)">
                <span className="dim">You:</span> {under.text}
              </button>
              <button className="ghost" onClick={() => toYours(-1)} title="Your previous message ([)">
                <IconChevronUp size={12} />
              </button>
              <button className="ghost" onClick={() => toYours(1)} title="Your next message (])">
                <IconChevronDown size={12} />
              </button>
            </span>
          )}
          <span className="spacer" />
          {live?.context?.max > 0 && <ContextMeter context={live.context} agent={agentKind} onCompact={!readOnly && !lost ? compactNow : null} />}
          {live?.cost > 0 && <span className="dim agent-cost">${live.cost.toFixed(2)}</span>}
          {/* As on its card: what closing does depends on what runs it, and says so. */}
          <button className="ghost agent-close" onClick={() => onClose(id)} title={closeHint(running, temporary, agentKind)}>
            <IconX size={13} />
          </button>
        </header>
      )}

      {blank ? (
        <div className="agent-start">
          <h1 className="agent-start-title">How can I help you today?</h1>
          {box}
          <div className="agent-suggestions">
            {SUGGESTIONS.map((s) => (
              <button
                key={s}
                onClick={() => {
                  setDraft(s);
                  inputRef.current?.focus();
                }}
              >
                {s}
              </button>
            ))}
          </div>
          {error && <div className="agent-note error">{error}</div>}
        </div>
      ) : (
      <>
      {peekItem &&
        (peekable(peekItem) === "agent" ? (
          <SubagentView
            key={peek}
            session={id}
            agent={agentKind}
            call={peek}
            working={working(peekItem, !!live?.busy, running)}
            root={root}
            view={view}
            contextLines={contextLines}
            wrap={wrap}
            onComment={onComment}
            onThreadAction={onThreadAction}
            onSymbol={onSymbol}
            onOpenFile={onOpenFile}
          />
        ) : (
          <TaskOutput key={peek} session={id} call={peek} />
        ))}
      {/* Kept, hidden, while a call is open: coming back finds the place held. */}
      <div className="agent-scroll" ref={scrollRef} onScroll={onScroll} hidden={!!peekItem}>
        <div className={cx("agent-log", lost && "lost")}>
          {earlier && items.length > 0 && (
            <div className="agent-earlier">
              {/* Asked for and not come yet, it is still the one it was. */}
              <button className="ghost" onClick={showEarlier} disabled={!!from && earlier.next === from}>
                <IconChevronUp size={12} />
                {from && earlier.next === from ? "Loading" : "Show the conversation before this"}
              </button>
              {earlier.messages > 0 && (
                <span className="dim">
                  {earlier.messages} {earlier.messages === 1 ? "message" : "messages"}
                </span>
              )}
            </div>
          )}
          {items.map((it) => (
            <Item
              key={it.key}
              item={it}
              session={id}
              agent={agentKind}
              hint={it.uuid === justSent && running === "dv" ? "Esc to edit" : undefined}
              run={runs.byFirst.get(it.key)}
              folded={runs.folded.has(it.key)}
              root={root}
              // As last heard, while dv is out of reach: the rows hold still instead.
              busy={!!live?.busy}
              running={running}
              view={view}
              contextLines={contextLines}
              wrap={wrap}
              threads={byTool.get(it.toolId) || NO_THREADS}
              canRewind={!readOnly}
              onRewind={setRewinding}
              onAttach={readOnly ? null : onAttach}
              onComment={onComment}
              onThreadAction={onThreadAction}
              onSymbol={onSymbol}
              onOpenFile={onOpenFile}
            />
          ))}
          {live?.blocks?.map((b, i) => (
            <Streaming key={i} block={b} />
          ))}
          {/* What Claude is doing is under what it was asked; what waits for it, under that. */}
          {incoming.filter((w) => !w.queued).map(bubble)}
          {/* A terminal's turn is only known to have started by its last message. */}
          {busy && !ask && <Activity live={live} items={items} root={root} since={live?.shell?.since || live?.since || items.findLast((it) => it.turn)?.at} />}
          {incoming.filter((w) => w.queued).map(bubble)}
          {ask && (
            <div className="agent-ask prompt">
              <div className="agent-ask-head prompt-head">
                <RequestTitle req={ask} />
                <span className="spacer" />
                {waiting.length > 1 && <span className="prompt-count">{waiting.length - 1} more after this</span>}
              </div>
              <Request
                key={ask.id}
                req={ask}
                active={settled === ask.id}
                grab={grabFocus}
                draft={drafts[ask.id]}
                onDraft={(patch) => setDrafts((d) => ({ ...d, [ask.id]: { ...d[ask.id], ...patch } }))}
                view={view}
                contextLines={contextLines}
                wrap={wrap}
                onSymbol={onSymbol}
                onOpenFile={onOpenFile}
              />
            </div>
          )}
          {(live?.error || error) && <div className="agent-note error">{error || live.error}</div>}
        </div>
        {/* A long conversation takes a moment to read and a moment to draw. */}
        {id && !live && !lost && !items.length && !incoming.length && (
          <div className="agent-loading">
            <Orb state="breathing" />
            Loading the conversation
          </div>
        )}
        {away && (
          <div className="agent-jump">
            <button onClick={toLatest} title={`Scroll to the end of the conversation (${modKey}+↓)`}>
              <IconChevronDown size={12} /> Latest
            </button>
          </div>
        )}
      </div>

      {atWork.length > 0 && <AtWork calls={atWork} open={peekItem?.toolId} root={root} onOpen={(call) => setPeek(peekItem?.toolId === call ? null : call)} />}
      {lost && id && (
        <div className="agent-readonly agent-lost">
          Lost the connection to dv. Is it still running? The page picks up again as soon as dv is back
          {readOnly ? "." : "; until then, a message waits in the box."}
        </div>
      )}
      {/* The box stays through a lost connection: taken out, it would take the
          focus and the cursor of someone still typing with it. */}
      {peekItem ? null : readOnly && agentKind !== "codex" && hooks?.on === false ? (
        // Asked here, where the prompts would have come up, rather than when dv
        // is installed or first run: hooks go in Claude Code's settings for good.
        <div className="agent-readonly">
          This session is open in a terminal, and only the terminal can talk to it. For its prompts to come up here as
          well, dv needs hooks in {hooks.path}.{" "}
          <button className="link" onClick={() => onHooks(true)}>
            Add them
          </button>
        </div>
      ) : readOnly ? (
        <div className="agent-readonly">
          {agentKind === "codex"
            ? "This session is open in a terminal. dv follows it, but only the terminal can talk to it or answer its prompts."
            : "This session is open in a terminal. dv follows it and shows its prompts, but only the terminal can talk to it."}
        </div>
      ) : (
        box
      )}
      </>
      )}

      {rewinding && (
        <RewindPicker
          prompts={rewindable}
          loading={picking && !!earlier && allPrompts?.id !== id}
          start={rewinding.prompt}
          running={running}
          agent={agentKind}
          onClose={() => setRewinding(null)}
          onRewind={rewind}
        />
      )}
    </div>
    </OpenCall.Provider>
    </AttachTarget.Provider>
  );
}

// OpenCall opens the conversation page on a call, from the call's own line.
const OpenCall = createContext(null);

// LINGER_MS is how long a call that has finished stays over the message box,
// so one ending is seen there rather than simply gone.
const LINGER_MS = 10000;

// useAtWork is what the agent left at work - its own agents, and commands in
// the background - each with `done` once it has ended and is only lingering.
function useAtWork(items, busy, running) {
  const at = useMemo(() => items.filter((it) => peekable(it) && working(it, busy, running)), [items, busy, running]);
  const [done, setDone] = useState([]); // [{ item, until }]
  const was = useRef([]);
  useEffect(() => {
    const ids = new Set(at.map((c) => c.toolId));
    const ended = was.current.filter((c) => !ids.has(c.toolId));
    was.current = at;
    if (!ended.length) return setDone((had) => (had.some((d) => ids.has(d.item.toolId)) ? had.filter((d) => !ids.has(d.item.toolId)) : had));
    // One at work again - a command read once more - is at work, not done.
    setDone((had) => [...had.filter((d) => !ids.has(d.item.toolId)), ...ended.map((item) => ({ item, until: Date.now() + LINGER_MS }))]);
  }, [at]);
  useEffect(() => {
    if (!done.length) return;
    const drop = () => setDone((had) => (had.some((d) => d.until <= Date.now()) ? had.filter((d) => d.until > Date.now()) : had));
    const t = setTimeout(drop, Math.max(0, Math.min(...done.map((d) => d.until)) - Date.now()) + 50);
    return () => clearTimeout(t);
  }, [done]);
  return useMemo(() => [...at, ...done.map((d) => ({ ...d.item, done: true }))], [at, done]);
}

// AtWork is what Claude left at work, over the message box: each agent, and
// each command in the background, opens on what it is doing.
// Where a selection is not offered: what is typed, and the edits' code, whose
// comment box has its own way to a new session.
const NOT_ASKED = "textarea, input, .agent-composer, .agent-head, .agent-edit .file-body";
const CODE_TEXT = "pre, code, .agent-code";

// SelectionAsk is a small bar over text selected in the conversation - what was
// said, a call, its result - that quotes it in a reply here (onReply, when this
// session can be written to) or in a new session's message box. It waits for
// the selection to be done, not each change.
function SelectionAsk({ within, active, onReply, onAsk }) {
  const [at, setAt] = useState(null); // { top, left, text, code }
  const bar = useRef(null);
  const shown = useRef(false);
  shown.current = !!at;
  useEffect(() => {
    if (!active) return setAt(null);
    let pressed = false;
    let settle = 0;
    const place = (snap) => {
      const sel = window.getSelection();
      const root = within.current;
      if (!sel?.rangeCount || sel.isCollapsed || !root) return setAt(null);
      const el = (n) => (n.nodeType === Node.ELEMENT_NODE ? n : n.parentElement);
      const ends = [el(sel.getRangeAt(0).startContainer), el(sel.getRangeAt(0).endContainer)];
      if (ends.some((e) => !root.contains(e) || e.closest(NOT_ASKED))) return setAt(null);
      if (snap) snapToWords(sel);
      const text = sel.toString().replace(/^\s*\n|\s+$/g, "");
      if (!text) return setAt(null);
      const range = sel.getRangeAt(0);
      const r = range.getBoundingClientRect();
      const box = root.getBoundingClientRect();
      if (r.bottom < box.top || r.top > box.bottom) return setAt(null);
      const above = r.top - 34;
      setAt({
        top: above >= box.top ? above : Math.min(r.bottom + 6, box.bottom - 34),
        left: Math.min(Math.max((r.left + r.right) / 2, box.left + 80), box.right - 80),
        text,
        code: ends.every((e) => e.closest(CODE_TEXT)),
      });
    };
    const onDown = (e) => {
      if (bar.current?.contains(e.target)) return;
      pressed = true;
      setAt(null);
    };
    const onUp = () => {
      pressed = false;
      setTimeout(() => place(true));
    };
    // Keys and a touch's handles select without a press to wait for.
    const onChange = () => {
      clearTimeout(settle);
      if (window.getSelection()?.isCollapsed) return setAt(null);
      if (!pressed) settle = setTimeout(() => place(true), 250);
    };
    const onMove = () => shown.current && place(false);
    document.addEventListener("pointerdown", onDown, true);
    document.addEventListener("pointerup", onUp, true);
    document.addEventListener("pointercancel", onUp, true);
    document.addEventListener("selectionchange", onChange);
    window.addEventListener("scroll", onMove, true);
    window.addEventListener("resize", onMove);
    return () => {
      clearTimeout(settle);
      document.removeEventListener("pointerdown", onDown, true);
      document.removeEventListener("pointerup", onUp, true);
      document.removeEventListener("pointercancel", onUp, true);
      document.removeEventListener("selectionchange", onChange);
      window.removeEventListener("scroll", onMove, true);
      window.removeEventListener("resize", onMove);
    };
  }, [active, within]);
  // Beside the selection with a pointer; under a finger the browser's own copy
  // menu sits right there, so it goes along the foot of the window instead.
  const touch = useMedia(TOUCH);
  if (!at) return null;
  const take = (to) => {
    window.getSelection()?.removeAllRanges();
    setAt(null);
    to(at.text, at.code);
  };
  return createPortal(
    <div
      ref={bar}
      className={cx("selection-ask", touch && "at-foot")}
      style={touch ? undefined : { top: at.top, left: at.left }}
      onMouseDown={(e) => e.preventDefault()}
    >
      {onReply && (
        <button className="attach-btn" onClick={() => take(onReply)} title="Quote this in your reply, in this session">
          <IconReply size={13} />
          <span>Reply</span>
        </button>
      )}
      <button className="attach-btn" onClick={() => take(onAsk)} title="Start a new session with this in its message">
        <IconNewSession size={13} />
        <span>New session</span>
      </button>
    </div>,
    document.body,
  );
}

const WORD = /[\p{L}\p{N}_]/u;

// snapToWords widens a selection that starts or ends partway into a word to
// the whole word, keeping which end the reader was dragging.
function snapToWords(sel) {
  const r = sel.getRangeAt(0);
  const inWord = (t, i) => i > 0 && i < t.length && WORD.test(t[i - 1]) && WORD.test(t[i]);
  let { startContainer: sn, startOffset: so, endContainer: en, endOffset: eo } = r;
  if (sn.nodeType === Node.TEXT_NODE && inWord(sn.data, so)) while (so > 0 && WORD.test(sn.data[so - 1])) so--;
  if (en.nodeType === Node.TEXT_NODE && inWord(en.data, eo)) while (eo < en.data.length && WORD.test(en.data[eo])) eo++;
  if (so === r.startOffset && eo === r.endOffset) return;
  const backward = sel.focusNode === r.startContainer && sel.focusOffset === r.startOffset;
  if (backward) sel.setBaseAndExtent(en, eo, sn, so);
  else sel.setBaseAndExtent(sn, so, en, eo);
}

// quote is text taken into a message: code fenced as it was, prose as a quote.
function quote(text, code) {
  if (!code) return text.split("\n").map((l) => `> ${l}`).join("\n");
  const fence = "`".repeat(Math.max(3, ...[...text.matchAll(/`+/g)].map((m) => m[0].length + 1)));
  return `${fence}\n${text}\n${fence}`;
}

function AtWork({ calls, open, root, onOpen }) {
  return (
    <div className="agent-at-work">
      {calls.map((c) => {
        const agent = peekable(c) === "agent";
        const { what } = toolSummary(c.tool, c.input, root);
        // A command is named by the command itself, as a terminal would: what
        // it is doing is the reason to look, and its own words say it best.
        const said = agent ? what : (c.input?.command || what || "").split("\n")[0];
        const why = c.done ? "Finished - here a moment longer" : agent ? "See this agent's conversation as it goes" : "See this command's output as it comes";
        const label = [!agent && c.input?.description, why].filter(Boolean).join(" · ");
        return (
          <button
            key={c.toolId}
            className={cx("agent-at-work-call", !agent && "mono", c.done && "done", open === c.toolId && "on")}
            onClick={() => onOpen(c.toolId)}
            title={open === c.toolId ? "Back to the conversation (Esc)" : label}
          >
            <span className="agent-at-work-dot" />
            <span className="agent-at-work-kind">{agent ? c.input?.subagent_type || "Agent" : "Command"}</span>
            <span className="agent-at-work-what">{said}</span>
          </button>
        );
      })}
    </div>
  );
}

// PeekTitle stands in the header for the call open: what it is, and how it is.
function PeekTitle({ item, root, busy, running, onBack }) {
  const agent = peekable(item) === "agent";
  const { what } = toolSummary(item.tool, item.input, root);
  const state = toolState(item.result, item.task, busy, running);
  return (
    <span className="agent-peek-title">
      <button className="ghost" onClick={onBack} title="Back to the conversation (Esc)">
        <IconBack size={13} />
      </button>
      <span className="agent-peek-kind">{agent ? item.input?.subagent_type || "Agent" : "Command"}</span>
      <span className="agent-peek-what">{what}</span>
      <span className={cx("agent-tool-state", state)} title={STATES[state]} />
      <span className="dim">{STATES[state]}</span>
    </span>
  );
}

// useAtEnd keeps a view at its end as what is in it grows, until the reader
// scrolls up from there.
function useAtEnd() {
  const ref = useRef(null);
  useEffect(() => {
    const el = ref.current;
    let stick = true;
    const onScroll = () => {
      stick = el.scrollHeight - el.scrollTop - el.clientHeight < 24;
    };
    const ro = new ResizeObserver(() => stick && (el.scrollTop = el.scrollHeight));
    ro.observe(el);
    ro.observe(el.firstElementChild);
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => {
      ro.disconnect();
      el.removeEventListener("scroll", onScroll);
    };
  }, []);
  return ref;
}

// SubagentView is the conversation of an agent a call started, followed as it
// goes. Its own agents are not opened from here.
function SubagentView({ session, call, working, root, ...rest }) {
  const { items, live } = useSession(session, call);
  const runs = useMemo(() => runsOf(items, working, "dv"), [items, working]);
  const ref = useAtEnd();
  return (
    <OpenCall.Provider value={null}>
      <div className="agent-scroll" ref={ref}>
        <div className="agent-log">
          {items.map((it) => (
            <Item
              key={it.key}
              item={it}
              session={session}
              run={runs.byFirst.get(it.key)}
              folded={runs.folded.has(it.key)}
              root={root}
              busy={working}
              running="dv"
              threads={NO_THREADS}
              canRewind={false}
              {...rest}
            />
          ))}
          {working && live && <Activity live={null} items={items} root={root} />}
        </div>
        {(!live || !live.found) && (
          <div className="agent-loading">
            <Orb state="breathing" />
            {live ? "Waiting for the agent to start" : "Loading the agent's conversation"}
          </div>
        )}
      </div>
    </OpenCall.Provider>
  );
}

// Past this, the start of a command's output is let go.
const OUTPUT_MAX = 1 << 20;
const ANSI = /\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)/g;

// TaskOutput is what a command left running in the background has written,
// followed as it writes more.
function TaskOutput({ session, call }) {
  const [out, setOut] = useState(null); // { text, cut, gone }
  useEffect(
    () =>
      api.agentTaskOutput(session, call, (c) =>
        setOut((o) => {
          if (c.gone) return { text: "", ...o, gone: true };
          const text = (c.reset || !o ? "" : o.text) + c.text;
          return { text: text.slice(-OUTPUT_MAX), cut: (c.reset ? c.cut : o?.cut) || text.length > OUTPUT_MAX, gone: false };
        }),
      ),
    [session, call],
  );
  // A progress line redraws itself after a carriage return: the last draw is what shows.
  const shown = useMemo(
    () => (out?.text || "").replace(ANSI, "").split("\n").map((l) => l.slice(l.lastIndexOf("\r", l.length - 2) + 1)).join("\n"),
    [out?.text],
  );
  const ref = useAtEnd();
  return (
    <div className="agent-scroll" ref={ref}>
      <div className="agent-log">
        {out?.cut && <div className="agent-note">Earlier output is left out.</div>}
        {shown && <pre className="agent-task-output">{shown}</pre>}
        {out && !shown && <div className="agent-note">{out.gone ? "The output is no longer kept." : "No output yet."}</div>}
      </div>
      {!out && (
        <div className="agent-loading">
          <Orb state="breathing" />
          Loading the output
        </div>
      )}
    </div>
  );
}

// closeHint says what closing a session does, which depends on what runs it.
function closeHint(running, temporary, agent) {
  if (running === "terminal") {
    return "Stop following this session in dv. It keeps running in its terminal, which goes back to being the only place it asks for permission.";
  }
  const after = temporary
    ? "It is temporary, so it leaves the list."
    : "It moves to Recent, and carries on from where it was when you next send to it.";
  const what = agent === "codex" ? "stops what Codex is doing and lets the session go" : "stops the Claude Code running it";
  return running === "dv" ? `Close the session: dv ${what}. ${after}` : `Close the session. ${after}`;
}

// TURN_STEPS are what a turn does, whose times say when it was last at work.
const TURN_STEPS = new Set(["text", "thinking", "tool", "note"]);

// withWorked ends each of Claude's turns with how long it worked, as the
// terminal does, where Claude Code did not write that down: from what began
// the turn to the last thing done in it. The turn still going has none, and a
// marker is an item of its own, so the log stays one child per item.
function withWorked(items, busy) {
  const out = [];
  let start = null;
  let end = 0;
  let last = -1;
  let recorded = false;
  const close = () => {
    if (start && last >= 0 && !recorded && end > start.at) out.splice(last + 1, 0, { key: "worked:" + start.key, kind: "worked", took: end - start.at });
  };
  for (const it of items) {
    if (it.turn) {
      close();
      [start, end, last, recorded] = [{ key: it.key, at: Date.parse(it.at) }, 0, -1, false];
    } else if (it.kind === "worked") recorded = true;
    else if (TURN_STEPS.has(it.kind) && it.at) {
      end = Math.max(end, Date.parse(it.at) + (it.result?.took || 0));
      last = out.length;
    }
    out.push(it);
  }
  if (!busy) close();
  return out;
}

const Item = memo(function Item(props) {
  const { item } = props;
  switch (item.kind) {
    case "worked":
      return <div className="agent-worked">Worked for {duration(Math.max(item.took, 1000), true)}</div>;
    case "prompt":
    case "command":
    case "shell":
      return <Prompt item={item} session={props.session} hint={props.hint} canRewind={props.canRewind} onRewind={props.onRewind} />;
    case "text":
      return <div className="markdown agent-text" dangerouslySetInnerHTML={{ __html: md.render(item.text) }} />;
    case "thinking":
      return <Thinking text={item.text} />;
    case "note":
      return <div className={cx("agent-note", item.error && "error")}>{item.text}</div>;
    case "output":
      return <div className={cx("markdown agent-text agent-output", item.error && "error")} dangerouslySetInnerHTML={{ __html: mdOutput.render(item.text) }} />;
    case "compact":
      return <Compacted item={item} />;
    case "mode":
      return <div className="agent-mode-line">{modeChange(item.from, item.text)}</div>;
    case "tool":
      // Folded into the run's first call, but still one child per item: the
      // view finds items by their place among the conversation's children.
      if (props.folded) return <div hidden />;
      if (props.run) {
        return <ToolGroup calls={props.run} session={props.session} root={props.root} busy={props.busy} running={props.running} onOpenFile={props.onOpenFile} />;
      }
      return item.result?.edited ? (
        <EditCard {...props} />
      ) : (
        <ToolRow item={item} session={props.session} root={props.root} busy={props.busy} running={props.running} onOpenFile={props.onOpenFile} />
      );
  }
  return null;
});

// Prompt is what you said, set to the right. One still on its way in is
// pending; its pictures are the ones sent, where the transcript's are fetched.
function Prompt({ item, session, pictures, pending, queued, hint, canRewind, onRewind }) {
  const { text, refs, via: viaApp } = useMemo(() => splitContext(item.text), [item.text]);
  const via = viaApp !== "dv" && viaApp;
  const n = item.images || 0;
  const srcs = pictures ? pictures.map(dataURL) : pending ? [] : Array.from({ length: n }, (_, i) => api.agentPromptImageURL(session, item.uuid, i));
  return (
    <div className={cx("agent-prompt", (item.kind === "command" || item.kind === "shell") && "command", item.kind === "shell" && "shell", pending && "pending")}>
      {srcs.length > 0 && (
        <div className="agent-prompt-images">
          {srcs.map((src, i) => (
            <ToolImage key={i} src={src} name={`Image ${i + 1}`} />
          ))}
        </div>
      )}
      {!srcs.length && n > 0 && <div className="agent-prompt-text dim">{n === 1 ? "An image" : `${n} images`}</div>}
      {item.kind === "shell" ? (
        <>
          <div className="agent-prompt-text">
            <span className="shell-bang">!</span> {item.text}
          </div>
          {item.result &&
            (item.result.text ? <Lines className="agent-result" text={item.result.text} /> : <div className="agent-prompt-foot">No output</div>)}
        </>
      ) : (
        text && <div className="agent-prompt-text">{text}</div>
      )}
      {refs.length > 0 && (
        <div className="agent-chips">
          {refs.map((r, i) => (
            <Chip key={i} {...r} />
          ))}
        </div>
      )}
      {(queued || hint || via) && (
        <div className="agent-prompt-foot">
          {via && <span title={`Sent from ${via}, where the reply was asked to be brief`}>via {via}</span>}
          {queued && <span title="Read once the step under way is done">Queued</span>}
          {hint && <span className="agent-prompt-hint">{hint}</span>}
        </div>
      )}
      {canRewind && item.uuid && !pending && (
        <button className="ghost agent-rewind" onClick={() => onRewind({ prompt: item })} title="Rewind to before this message (Esc Esc)">
          <IconUndo size={12} />
        </button>
      )}
    </div>
  );
}

// Kept as written, line breaks and all, less the blank lines around it.
const Thinking = ({ text }) => <div className="agent-thinking">{text.trim()}</div>;

// How long Shift+Tab waits for another press before the mode it is on is sent.
const MODE_SETTLE_MS = 1000;

// Permission modes as the mode picker names them.
const MODE_NAMES = {
  default: "Ask before edits",
  acceptEdits: "Accept edits",
  plan: "Plan",
  auto: "Auto",
  bypassPermissions: "Bypass permissions",
  dontAsk: "Don't ask",
  fullAccess: "Full access", // Codex's own config, without its sandbox
};

// modeChange words a change of permission mode: plan mode is a place Claude
// goes into and comes out of, the others are switched between.
function modeChange(from, to) {
  const name = MODE_NAMES[to] || to;
  if (to === "plan") return "Entered plan mode";
  if (from === "plan") return `Exited plan mode · ${name}`;
  return `Switched to ${name}`;
}

// Compacted is where Claude Code summarised the conversation to free its
// context. What came before stays on the page; Claude has only the summary.
function Compacted({ item }) {
  const [open, setOpen] = useState(false);
  const ref = useRef(null);
  const { auto, before, after } = item.compacted;
  return (
    <div className="agent-compacted" ref={ref}>
      <button
        className="agent-compacted-line"
        onClick={() => {
          setOpen(!open);
          toggled(ref.current, !open);
        }}
        disabled={!item.text}
        title={`${auto ? "The context was nearly full, so the conversation was" : "The conversation was"} summarised. The agent carries on from the summary, not from the messages above.`}
      >
        {item.text && (open ? <IconChevronDown size={11} /> : <IconChevron size={11} />)}
        Conversation compacted{auto ? " automatically" : ""}
        {before > 0 && <span className="agent-compacted-tokens">{after > 0 ? `${tokens(before)} → ${tokens(after)}` : tokens(before)}</span>}
      </button>
      {open && <div className="markdown agent-compacted-text" dangerouslySetInnerHTML={{ __html: md.render(item.text) }} />}
    </div>
  );
}

// Streaming is a reply as it arrives: its text, or its thinking. What a tool
// call is doing shows in the Activity line under it.
function Streaming({ block }) {
  if (block.kind === "text") {
    return <div className="markdown agent-text streaming" dangerouslySetInnerHTML={{ __html: md.render(block.text || "") }} />;
  }
  if (block.kind === "thinking" && block.text) return <Thinking text={block.text} />;
  return null;
}

// Activity is what Claude is doing, while it works: an orb whose motion is the
// kind of work, a word or two for it, and how long the turn has gone on since.
function Activity({ live, items, root, since }) {
  const [state, label] = JSON.parse(useDwell(JSON.stringify(activityOf(live, items, root)), ACTIVITY_DWELL_MS));
  return (
    <div className="agent-working">
      <Fade value={state} className="orb">
        {(s) => <Orb state={s} />}
      </Fade>
      <Fade value={label} className="agent-working-label">
        {(l) => l}
      </Fade>
      {since && <Elapsed from={since} className="agent-working-time" />}
    </div>
  );
}

// Elapsed is the time since from, counting up.
function Elapsed({ from, className }) {
  const [now, setNow] = useState(Date.now);
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);
  const ms = now - Date.parse(from);
  return ms >= 0 && <span className={className}>{duration(ms, true)}</span>;
}

// Longer than a fade, so one finishes before the next starts; a run of quick
// calls would otherwise flicker.
const ACTIVITY_DWELL_MS = 1000;
const FADE_MS = 600; // .fade-layer's animation

// Fade crossfades from one value to the next: the orbs themselves only cut.
function Fade({ value, className, children }) {
  const [layers, setLayers] = useState([value]);
  if (layers.at(-1) !== value) setLayers([layers.at(-1), value]);
  useEffect(() => {
    if (layers.length === 1) return;
    const t = setTimeout(() => setLayers((ls) => ls.slice(-1)), FADE_MS);
    return () => clearTimeout(t);
  }, [layers]);
  return (
    <span className={cx("fade", className)}>
      {layers.map((v, i) => (
        <span key={v} className={cx("fade-layer", i < layers.length - 1 && "leaving")}>
          {children(v)}
        </span>
      ))}
    </span>
  );
}

// useDwell follows value, but holds each value it takes for at least ms.
function useDwell(value, ms) {
  const [shown, setShown] = useState(value);
  const since = useRef(Date.now());
  useEffect(() => {
    if (value === shown) return;
    const t = setTimeout(() => {
      since.current = Date.now();
      setShown(value);
    }, since.current + ms - Date.now());
    return () => clearTimeout(t);
  }, [value, shown, ms]);
  return shown;
}

const TOOL_ORBS = {
  Read: "searching", Grep: "searching", Glob: "searching", WebSearch: "searching", WebFetch: "searching",
  Edit: "shaping", Write: "shaping", NotebookEdit: "shaping", ImageGeneration: "shaping",
  Agent: "connecting", Task: "connecting",
};

// activityOf is the orb for what Claude is doing and the words beside it,
// which name the call.
function activityOf(live, items, root) {
  if (live?.shell && !live.shell.error && !live.shell.stopped) return ["working", "Running your command"];
  if (live?.status === "compacting") return ["weaving", "Compacting the conversation"];
  const block = live?.blocks?.at(-1);
  if (block?.kind === "thinking") return ["solving", "Thinking"];
  if (block?.kind === "text") return ["composing", "Writing"];
  // A call still out, the latest first: its name, and what it is on.
  const call = block?.kind === "tool" ? { tool: block.tool } : items.findLast((it) => it.kind === "tool" && !it.result);
  if (call) {
    const { name, what } = toolSummary(call.tool, call.input, root);
    return [TOOL_ORBS[call.tool] || "working", [name || call.tool, what].filter(Boolean).join(" · ")];
  }
  // A terminal does not say it is compacting, and dv is not always told; past
  // the point Claude Code compacts at, with no call out, that is what it does.
  const c = live?.context;
  if (c?.compact > 0 && c.used >= c.compact) return ["weaving", "Compacting the conversation"];
  // Nothing streaming and no call out: waiting on the model.
  return ["breathing", "Working"];
}

// toolSummary is the line a tool call is shown as: what it acted on.
function toolSummary(tool, input = {}, root) {
  const rel = (p) => (p && root && p.startsWith(root + "/") ? p.slice(root.length + 1) : p || "");
  const firstLine = (s) => (s || "").split("\n")[0];
  switch (tool) {
    case "Bash":
    case "PowerShell":
      return { what: input.description || firstLine(input.command), mono: !input.description };
    case "Read":
      return { what: rel(input.file_path) + (input.offset ? `:${input.offset}` : ""), mono: true };
    case "Edit":
    case "Write":
    case "NotebookEdit":
      return { what: rel(input.file_path || input.notebook_path), mono: true };
    case "Grep":
      return { what: input.pattern + (input.glob ? `  ${input.glob}` : input.path ? `  ${rel(input.path)}` : ""), mono: true };
    case "Glob":
      return { what: input.pattern, mono: true };
    case "WebFetch":
      return { what: input.url, mono: true };
    case "WebSearch":
      return { what: input.query };
    case "Agent":
    case "Task":
      return { what: input.description || firstLine(input.prompt) };
    case "TodoWrite":
      return { what: "Updated the to-do list" };
    case "AskUserQuestion":
      return { what: input.questions?.[0]?.question || "" };
    case "ExitPlanMode":
      return { what: "Proposed a plan" };
    case "Skill":
      return { what: input.skill || input.command || "" };
    case "ImageGeneration":
      return { name: "Image", what: firstLine(input.prompt) || rel(input.file_path) };
  }
  const mcp = /^mcp__(.+?)__(.+)$/.exec(tool);
  if (mcp) return { name: mcp[1], what: mcp[2] };
  return { what: "" };
}

function ToolRow({ item, session, root, busy, running, onOpenFile }) {
  const [open, setOpen] = useState(false);
  const ref = useRef(null);
  const input = item.input || {};
  const r = item.result;
  const d = r?.detail;
  const { name, what, mono } = toolSummary(item.tool, input, root);
  const state = toolState(r, item.task, busy, running);
  const command = item.tool === "Bash" || item.tool === "PowerShell";
  // One left in the background ran on after its result, so its time is not known.
  const took = command && r?.took > 0 && !item.task && state !== "background" && duration(r.took);
  const sum = [toolDetail(item.tool, r, item.task, state), took].filter(Boolean).join(" · ");
  const rel = inRepo(input.file_path, root);
  const file = item.tool === "Read" && d?.kind === "text" && rel && { path: rel, line: d.start || 1 };
  const openCall = useContext(OpenCall);
  // A command's output is only kept while it runs; an agent's conversation, for good.
  const peek = openCall && peekable(item);
  const opens = peek === "agent" || (peek === "command" && state === "background");
  return (
    <div className={cx("agent-tool", open && "open", state)} id={"tool-" + item.toolId} ref={ref}>
      <div className="agent-tool-row">
        <button
          className="agent-tool-toggle"
          onClick={() => {
            setOpen(!open);
            toggled(ref.current, !open);
          }}
        >
          <span className={cx("agent-tool-state", state)} title={STATES[state]} />
          <span className="agent-tool-name">{name || item.tool}</span>
          <span className={cx("agent-tool-what", mono && "mono")}>
            {LRM}
            {what}
          </span>
          {sum && <span className="agent-tool-sum">{sum}</span>}
          {command && state === "running" && item.at && <Elapsed from={item.at} className="agent-tool-sum" />}
        </button>
        {file && (
          <button className="view-file" onClick={() => onOpenFile(file.path, file.line)} title="The file as it is now, where it was read">
            <IconFile size={12} />
            <span className="btn-label">File</span>
          </button>
        )}
        {opens && (
          <button className="view-file" onClick={() => openCall(item.toolId)} title={peek === "agent" ? "The agent's own conversation" : "The command's output so far, as it comes"}>
            <span className="btn-label">{peek === "agent" ? "Conversation" : "Output"}</span>
          </button>
        )}
      </div>
      {item.tool === "TodoWrite" && <Todos todos={input.todos} />}
      {/* A picture made is what the call is for, so it shows without opening. */}
      {item.tool === "ImageGeneration" && d?.kind === "image" && !r.isError && (
        <div className="agent-tool-body">
          <ToolImage src={api.agentImageURL(session, item.toolId)} name={what} />
        </div>
      )}
      {open &&
        (item.tool === "ImageGeneration" ? (
          <div className="agent-tool-body">{r?.isError ? <pre className="agent-result error">{r.text}</pre> : <Fields input={input} />}</div>
        ) : d?.kind === "image" && !r.isError ? (
          <ToolImage src={api.agentImageURL(session, item.toolId)} detail={d} name={what} />
        ) : (
          <div className="agent-tool-body">
            <ToolBody item={item} session={session} root={root} onOpenFile={onOpenFile} />
          </div>
        ))}
    </div>
  );
}

// ToolGroup is calls made one after another, once they are done: one line for
// what they did together, which opens on each call's own line.
function ToolGroup({ calls, session, root, busy, running, onOpenFile }) {
  const [open, setOpen] = useState(false);
  const ref = useRef(null);
  const failed = calls.filter((c) => c.result?.isError).length;
  return (
    <div className={cx("agent-tool", "agent-tools", open && "open")} ref={ref}>
      <div className="agent-tool-row">
        <button
          className="agent-tool-toggle"
          onClick={() => {
            setOpen(!open);
            toggled(ref.current, !open);
          }}
          title={open ? "Fold these calls into one line" : "Show each call"}
        >
          {open ? <IconChevronDown size={11} /> : <IconChevron size={11} />}
          <span className="agent-tool-what">{didTogether(calls)}</span>
          {failed > 0 && <span className="agent-tool-sum failed">{failed} failed</span>}
        </button>
      </div>
      {open && (
        <div className="agent-tools-list">
          {calls.map((c) => (
            <ToolRow key={c.key} item={c} session={session} root={root} busy={busy} running={running} onOpenFile={onOpenFile} />
          ))}
        </div>
      )}
    </div>
  );
}

// What calls of each kind did, counted: "read 3 files".
const DID = [
  [["Read"], "read", "a file", "files"],
  [["Grep", "Glob", "LS"], "searched for", "a pattern", "patterns"],
  [["Bash", "PowerShell"], "ran", "a command", "commands"],
  [["WebFetch"], "fetched", "a page", "pages"],
  [["WebSearch"], "ran", "a web search", "web searches"],
  [["Agent", "Task"], "ran", "an agent", "agents"],
  [["Skill"], "used", "a skill", "skills"],
  [["ImageGeneration"], "made", "an image", "images"],
];
// One kind for the rest, so they count together.
const DID_OTHER = [[], "used", "a tool", "tools"];

function didTogether(calls) {
  const counts = new Map();
  for (const c of calls) {
    const kind = DID.find(([tools]) => tools.includes(c.tool)) || DID_OTHER;
    counts.set(kind, (counts.get(kind) || 0) + 1);
  }
  const said = [...counts].map(([[, verb, one, many], n]) => `${verb} ${n === 1 ? one : `${n} ${many}`}`).join(", ");
  return said[0].toUpperCase() + said.slice(1);
}

// foldable is a call that goes into a run of them: done, and saying no more
// on its own line than the run's does. An edit is its diff, the to-do list
// its list, and a question or a plan is read for itself.
const UNFOLDED = new Set(["TodoWrite", "AskUserQuestion", "ExitPlanMode", "ImageGeneration"]);
function foldable(it, busy, running) {
  if (it.kind !== "tool" || it.result?.edited || UNFOLDED.has(it.tool)) return false;
  return ["done", "failed", "stopped"].includes(toolState(it.result, it.task, busy, running));
}

// runsOf finds calls done one after another, which read as one line: by the
// first call's key, the run; and the keys of the calls folded into it.
function runsOf(items, busy, running) {
  const byFirst = new Map();
  const folded = new Set();
  let run = [];
  const end = () => {
    if (run.length > 1) {
      byFirst.set(run[0].key, run);
      for (const it of run.slice(1)) folded.add(it.key);
    }
    run = [];
  };
  for (const it of items) {
    if (foldable(it, busy, running)) run.push(it);
    else end();
  }
  end();
  return { byFirst, folded };
}

// peekable is what a call can be opened on: an agent on its conversation, a
// command left in the background on its output.
function peekable(it) {
  if (it.kind !== "tool") return "";
  if (it.tool === "Agent" || it.tool === "Task") return "agent";
  if ((it.tool === "Bash" || it.tool === "PowerShell") && it.result?.detail?.background) return "command";
  return "";
}

const working = (it, busy, running) => ["running", "background"].includes(toolState(it.result, it.task, busy, running));

const STATES = {
  running: "Running",
  background: "Running in the background",
  unfinished: "Never finished",
  failed: "Failed",
  stopped: "Stopped",
  done: "Done",
};

// toolState is what a call's dot says. Work left in the background is not done
// when its result comes, which only says it started, but when Claude Code says
// it ended - or never, once nothing is running it.
function toolState(r, task, busy, running) {
  if (!r) return busy ? "running" : "unfinished";
  if (r.isError) return "failed";
  if (task) return task.status === "completed" ? "done" : task.status === "failed" ? "failed" : "stopped";
  if (r.detail?.background) return running ? "background" : "unfinished";
  return "done";
}

const plural = (n, one, many = one + "s") => `${n.toLocaleString()} ${n === 1 ? one : many}`;

// toolDetail is the few words after a call that say what came of it.
function toolDetail(tool, r, task, state) {
  const d = r?.detail;
  if (task) {
    const code = /exit code (\d+)/.exec(task.summary || "");
    return code ? `exit ${code[1]}` : task.status;
  }
  if (state === "background") return "in the background";
  if (!d || r.isError) return "";
  switch (tool) {
    case "Read":
      if (d.kind === "image") return d.width ? `${d.width}×${d.height}` : "image";
      if (d.kind !== "text") return d.kind;
      if (d.total > d.lines) return `lines ${d.start}–${d.start + d.lines - 1} of ${d.total.toLocaleString()}`;
      return plural(d.lines || 0, "line");
    case "Grep":
      return d.kind === "content" ? plural(d.lines || 0, "matching line") : plural(d.files ?? 0, "file");
    case "Glob":
      return plural(d.files ?? 0, "file") + (d.more ? "+" : "");
    case "Bash":
    case "PowerShell":
      if (d.interrupted) return "interrupted";
      return d.lines ? plural(d.lines, "line") + " of output" : "no output";
    case "WebFetch":
      return [d.status, d.bytes && `${Math.max(1, Math.round(d.bytes / 1024))} KB`].filter(Boolean).join(" · ");
    case "WebSearch":
      return plural(d.results ?? 0, "result");
  }
  return "";
}

// inRepo is a path relative to the repository, or "" for one outside it.
function inRepo(path, root) {
  return path && root && path.startsWith(root + "/") ? path.slice(root.length + 1) : "";
}

// ToolImage is a picture Claude was shown, under its line once that is opened.
function ToolImage({ src, detail = {}, name }) {
  const [zoom, setZoom] = useState(false);
  return (
    <>
      <button className="agent-image" onClick={() => setZoom(true)} title="See it full size">
        <img src={src} alt="" loading="lazy" width={detail.width || undefined} height={detail.height || undefined} />
      </button>
      {zoom && (
        <Modal wide centred className="image-view" onClose={() => setZoom(false)}>
          <div className="viewer-head">
            <span className="path">{name}</span>
            <span className="spacer" />
            <button className="ghost" onClick={() => setZoom(false)} title="Close (Esc)">
              <IconX size={13} />
            </button>
          </div>
          <div className="image-view-body">
            <img src={src} alt="" />
          </div>
        </Modal>
      )}
    </>
  );
}

// Tools whose calls say what they did well enough in their line; the rest
// show their arguments when opened.
const SAID = new Set(["Read", "Glob", "WebFetch", "WebSearch", "TodoWrite"]);

// ToolBody is a call opened: its arguments where they add to the line, and
// what it came back with, fetched whole.
function ToolBody({ item, session, root, onOpenFile }) {
  const input = item.input || {};
  const r = item.result;
  const [out, setOut] = useState(null);
  useEffect(() => {
    if (!r) return;
    let live = true;
    api.agentOutput(session, item.toolId).then(
      (o) => live && setOut(o),
      (e) => live && setOut({ error: e.message }),
    );
    return () => {
      live = false;
    };
  }, [session, item.toolId, !!r]);

  const tool = item.tool;
  return (
    <>
      {tool === "Bash" || tool === "PowerShell" ? (
        <Command text={input.command || ""} lang={tool === "Bash" ? "bash" : "powershell"} />
      ) : tool === "ExitPlanMode" && input.plan ? (
        <div className="markdown agent-plan" dangerouslySetInnerHTML={{ __html: md.render(input.plan) }} />
      ) : (tool === "Agent" || tool === "Task") && input.prompt ? (
        <div className="markdown agent-page" dangerouslySetInnerHTML={{ __html: md.render(input.prompt) }} />
      ) : (
        !SAID.has(tool) && <Fields input={input} />
      )}
      {r && !out && <div className="file-note loading">Loading...</div>}
      {out?.error && <pre className={cx("agent-result", r.isError && "error")}>{r.text}</pre>}
      {out && !out.error && <Output tool={tool} input={input} out={out} failed={r.isError} root={root} onOpenFile={onOpenFile} />}
    </>
  );
}

function Fields({ input }) {
  const entries = Object.entries(input);
  if (!entries.length) return null;
  return (
    <dl className="prompt-fields">
      {entries.map(([k, v]) => (
        <div key={k}>
          <dt>{k}</dt>
          <dd>{typeof v === "string" ? v : JSON.stringify(v, null, 2)}</dd>
        </div>
      ))}
    </dl>
  );
}

function Output({ tool, input, out, failed, root, onOpenFile }) {
  if (out.read) return <ReadLines read={out.read} onOpenFile={onOpenFile} />;
  if (out.found) return <FoundList found={out.found} path={inRepo(input.path, root) || input.path} onOpenFile={onOpenFile} />;
  if (out.stdout || out.stderr) {
    return (
      <>
        {out.stdout && <Lines className="agent-result" text={out.stdout} />}
        {out.stderr && <Lines className="agent-result stderr" text={out.stderr} />}
      </>
    );
  }
  // A command left in the background has only Claude Code's word on where its
  // output went.
  if ((tool === "Bash" || tool === "PowerShell") && !out.text) return <div className="file-note">No output.</div>;
  if (out.page) return <div className="markdown agent-page" dangerouslySetInnerHTML={{ __html: md.render(out.page) }} />;
  if (out.links) {
    return (
      <ul className="agent-links">
        {out.links.map((l, i) => (
          <li key={i}>
            <a href={l.url} target="_blank" rel="noreferrer noopener">
              {l.title || l.url}
            </a>
            <span className="dim">{hostOf(l.url)}</span>
          </li>
        ))}
      </ul>
    );
  }
  if (!out.text) return null;
  if (!failed && (tool === "Agent" || tool === "Task")) {
    return <div className="markdown agent-page" dangerouslySetInnerHTML={{ __html: md.render(out.text) }} />;
  }
  return <Lines className={cx("agent-result", failed && "error")} text={out.text} />;
}

const hostOf = (url) => {
  try {
    return new URL(url).host;
  } catch {
    return "";
  }
};

// Past these, a result opens on its start with a way to the rest: a long
// command's output or a whole file would push the conversation out of reach.
const SHOWN_LINES = 12;
const SHOWN_READ = 200;
const SHOWN_FOUND = 200;

function Lines({ className, text }) {
  const [all, setAll] = useState(false);
  const lines = text.replace(/\n$/, "").split("\n");
  const cut = !all && lines.length > SHOWN_LINES + 3;
  return (
    <div className={cx(className, "agent-lines")}>
      <pre>{cut ? lines.slice(0, SHOWN_LINES).join("\n") : text}</pre>
      {cut && (
        <button className="link agent-more" onClick={() => setAll(true)}>
          Show all {lines.length.toLocaleString()} lines
        </button>
      )}
    </div>
  );
}

// ReadLines is what a Read was given, highlighted, at its own line numbers.
function ReadLines({ read, onOpenFile }) {
  const [all, setAll] = useState(false);
  const [, force] = useState(0);
  useEffect(() => {
    if (read.lang) ensureLanguage(read.lang, () => force((n) => n + 1));
  }, [read.lang]);
  const lines = useMemo(() => read.content.replace(/\n$/, "").split("\n"), [read.content]);
  const shown = useMemo(() => (all ? lines : lines.slice(0, SHOWN_READ)), [lines, all]);
  const ready = langReady(read.lang);
  const html = useMemo(() => highlightLines("read:" + read.path + ":" + read.start, shown, read.lang), [shown, read.lang, read.path, read.start, ready]);
  return (
    <div className="agent-code">
      {html.map((h, i) => {
        const n = read.start + i;
        return (
          <div className="agent-code-line" key={n}>
            {read.inRepo ? (
              <button className="n" onClick={() => onOpenFile(read.path, n)} title="Open the file here">
                {n}
              </button>
            ) : (
              <span className="n">{n}</span>
            )}
            <code dangerouslySetInnerHTML={{ __html: h || "&nbsp;" }} />
          </div>
        );
      })}
      {shown.length < lines.length && (
        <button className="link agent-more" onClick={() => setAll(true)}>
          Show all {lines.length.toLocaleString()} lines
        </button>
      )}
    </div>
  );
}

// FoundList is what a search turned up: files, or matching lines under their
// file. A line with no file is in the one the search was given.
function FoundList({ found, path, onOpenFile }) {
  const [all, setAll] = useState(false);
  if (!found.length) return <div className="file-note">Nothing found.</div>;
  const shown = all ? found : found.slice(0, SHOWN_FOUND);
  const groups = [];
  for (const f of shown) {
    const p = f.path || path || "";
    const g = groups[groups.length - 1];
    if (g && g.path === p && f.line) g.lines.push(f);
    else groups.push({ path: p, inRepo: f.path ? f.inRepo : !!path && !path.startsWith("/"), lines: f.line ? [f] : [], text: f.line ? "" : f.text });
  }
  const open = (g, line) => g.inRepo && onOpenFile(g.path, line || 1);
  return (
    <div className="agent-found">
      {groups.map((g, i) =>
        !g.path ? (
          <div className="agent-found-text" key={i}>
            {g.text}
          </div>
        ) : (
          <div className="agent-found-file" key={i}>
            <button className="agent-found-path" disabled={!g.inRepo} onClick={() => open(g, g.lines[0]?.line)}>
              {LRM}
              {g.path}
            </button>
            {g.lines.map((f, j) => (
              <button className="agent-found-line" key={j} disabled={!g.inRepo} onClick={() => open(g, f.line)}>
                <span className="n">{f.line}</span>
                <code>{f.text}</code>
              </button>
            ))}
          </div>
        ),
      )}
      {shown.length < found.length && (
        <button className="link agent-more" onClick={() => setAll(true)}>
          Show all {found.length.toLocaleString()}
        </button>
      )}
    </div>
  );
}

function Command({ text, lang }) {
  const [, force] = useState(0);
  useEffect(() => {
    ensureLanguage(lang, () => force((n) => n + 1));
  }, [lang]);
  const ready = langReady(lang);
  const html = useMemo(() => highlightLines("agent:" + text, text.split("\n"), lang), [text, lang, ready]);
  return (
    <pre className="prompt-command">
      {html.map((h, n) => (
        <code key={n} dangerouslySetInnerHTML={{ __html: h || "&nbsp;" }} />
      ))}
    </pre>
  );
}

function Todos({ todos }) {
  if (!todos?.length) return null;
  const mark = { completed: "✓", in_progress: "▸", pending: "○" };
  return (
    <ul className="agent-todos">
      {todos.map((t, i) => (
        <li key={i} className={t.status}>
          <span className="agent-todo-mark">{mark[t.status] || "○"}</span>
          {t.status === "in_progress" ? t.activeForm || t.content : t.content}
        </li>
      ))}
    </ul>
  );
}

// EditCard is a change Claude made, as a file in the review: the diff is
// fetched when the card nears the screen, and commented on the way the diff
// is. A comment here remembers the edit it was left on, and goes with the next
// message unless taken out.
function EditCard({ item, session, agent, view, contextLines, wrap, threads, onAttach, onComment, onThreadAction, onSymbol, onOpenFile }) {
  const whose = `${agentName(agent)}'s edit`;
  const e = item.result.edited;
  const ref = useRef(null);
  const [state, setState] = useState(null); // { ed } or { error }
  const [expanded, setExpanded] = useState({});
  const [selection, setSelection] = useState(null);
  const [composing, setComposing] = useState(null);
  const [, force] = useState(0);
  const fd = state?.ed?.diff;
  // Only a whole file renders: with just the changed lines kept, it would not read.
  const [preview, setPreview] = useState(false);
  const kind = previewKind(e.path);
  const previewable = kind && fd && !fd.binary && !fd.tooLarge && !state.ed.partial;

  useEffect(() => {
    const el = ref.current;
    if (!el || state) return;
    const io = new IntersectionObserver(
      ([entry]) => {
        if (!entry.isIntersecting) return;
        io.disconnect();
        api
          .agentEdit(session, item.toolId)
          .then((ed) => setState({ ed }))
          .catch((err) => setState({ error: err.message }));
      },
      // The conversation's own scroller as the root: against the window, the
      // scroller clips the card first, and the margin never comes into it.
      { root: el.closest(".agent-scroll"), rootMargin: "600px 0px" },
    );
    io.observe(el);
    return () => io.disconnect();
  }, [session, item.toolId, state]);

  useEffect(() => {
    if (fd?.lang) ensureLanguage(fd.lang, () => force((n) => n + 1));
  }, [fd?.lang]);

  const expand = useCallback((gid, amount) => {
    setExpanded((x) => ({ ...x, [gid]: amount === "all" ? "all" : (typeof x[gid] === "number" ? x[gid] : 0) + amount }));
  }, []);

  const startComment = useCallback(
    (side, start, end, selected) => {
      const src = side === "old" ? fd?.oldLines : fd?.newLines;
      setComposing({ path: e.path, side, start, end, quote: src ? src.slice(start - 1, end) : [], selected });
      setSelection(null);
    },
    [fd, e.path],
  );

  const comment = useCallback(
    async (payload) => {
      const t = await onComment({ ...payload, scope: whose, origin: { session, tool: item.toolId } });
      if (t && onAttach) onAttach({ kind: "thread", threadId: t.id });
    },
    [onComment, onAttach, session, item.toolId, whose],
  );
  const attach = useMemo(() => onAttach && ((a, to) => onAttach({ ...a, at: whose }, to)), [onAttach, whose]);
  useEffect(() => {
    const el = ref.current;
    const onComment = (ev) => startComment(ev.detail.side, ev.detail.line, ev.detail.line);
    const onAttachLine = ({ detail: { side, line } }) => {
      const src = side === "old" ? fd?.oldLines : fd?.newLines;
      if (attach && src) attach({ kind: "lines", file: e.path, side, start: line, end: line, quote: [src[line - 1]] });
    };
    el.addEventListener("dv:comment", onComment);
    el.addEventListener("dv:attach", onAttachLine);
    return () => {
      el.removeEventListener("dv:comment", onComment);
      el.removeEventListener("dv:attach", onAttachLine);
    };
  }, [startComment, attach, fd, e.path]);

  const [dir, name] = splitPath(e.path);
  const [copied, copy] = useCopy(e.path);
  const first = fd?.ops?.find((o) => o.k !== 0);
  return (
    <div className="file agent-edit" id={"tool-" + item.toolId} ref={ref}>
      <header className="file-head">
        <span className={cx("badge", e.created ? "st-A" : "st-M")}>{e.created ? "A" : "M"}</span>
        <h3 className="file-path copy-path" title={`Copy the path, ${e.path}`} onClick={copy}>
          <span className="dir">
            {LRM}
            {dir}
            {LRM}
          </span>
          <span className="name">{name}</span>
          {copied && <span className="copied">copied</span>}
        </h3>
        {!e.inRepo && <span className="tag-generated">outside</span>}
        {state?.ed?.partial && (
          <span className="tag-generated" title="The transcript kept only the changed lines">
            hunks
          </span>
        )}
        <span className="spacer" />
        {threads.length > 0 && <span className="file-comments">{threads.length} comment{threads.length === 1 ? "" : "s"}</span>}
        <span className="stat">
          <span className="add">+{e.adds}</span>
          <span className="del">-{e.dels}</span>
        </span>
        {previewable && <PreviewToggle kind={kind} on={preview} onChange={setPreview} />}
        {e.inRepo && (
          <button className="view-file" onClick={() => onOpenFile(e.path, first ? first.ns + 1 : 1)} title="The whole file as it is now">
            <IconFile size={12} />
            <span className="btn-label">File</span>
          </button>
        )}
      </header>
      <div className={cx("file-body", wrap && "wrap")}>
        {!state && <div className="file-note loading">Loading...</div>}
        {state?.error && <div className="file-note error">{state.error}</div>}
        {previewable && preview && kind === "markdown" && (
          <MarkdownDocument
            lines={fd.newLines}
            path={e.path}
            onOpenFile={e.inRepo ? onOpenFile : null}
            threads={threads}
            composing={composing}
            setComposing={setComposing}
            onStartComment={startComment}
            onComment={comment}
            onThreadAction={onThreadAction}
            onAttach={attach}
          />
        )}
        {previewable && preview && kind === "svg" && <SvgPreview oldLines={fd.status !== "A" && fd.oldLines} newLines={fd.newLines} />}
        {fd && !fd.binary && !fd.tooLarge && !(previewable && preview) && (
          <DiffBody
            fd={fd}
            view={fd.status === "A" ? "unified" : view}
            oneNumber={fd.status === "A" && view === "split"}
            contextLines={contextLines}
            expanded={expanded}
            onExpand={expand}
            threads={threads}
            selection={selection}
            setSelection={setSelection}
            composing={composing}
            setComposing={setComposing}
            onStartComment={startComment}
            onComment={comment}
            onThreadAction={onThreadAction}
            onSymbol={onSymbol}
            onAttach={attach}
            path={e.path}
            wrap={wrap}
            unknown={state.ed.partial}
          />
        )}
      </div>
    </div>
  );
}

// ModelPicker is the model and how hard it thinks, in one menu, as the
// terminal's /model has them. onPick is given { model } or { effort }.
function ModelPicker({ label, models, model, efforts, effort, pending, onPick }) {
  const [open, setOpen] = useState(false);
  const ref = useDismiss(open, () => setOpen(false));
  const room = useRoom(open, ref);
  const levels = useMemo(() => [...new Set(models.map((m) => m.efforts?.join(" ")).filter(Boolean))], [models]);
  return (
    <div className="model-menu" ref={ref}>
      <button className="mini composer-pick" onClick={() => setOpen((o) => !o)} title={pending ? "Model and effort, from the next turn" : "Model and effort"}>
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
                        title={EFFORTS[e]}
                        onClick={() => {
                          onPick({ effort: e });
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

const HOW = [
  { label: "Restore the conversation", hint: "the code stays as it is", conversation: true, code: false },
  { label: "Restore the code and the conversation", hint: "undo Claude's edits since", conversation: true, code: true },
  { label: "Restore the code", hint: "keep the conversation", conversation: false, code: true },
  { label: "Never mind" },
];

// RewindPicker is the terminal's double Esc: pick a message, then what to take
// back to before it. Codex keeps no copies of the files it changes, so there
// only the conversation goes back.
function RewindPicker({ prompts, loading, start, running, agent, onClose, onRewind }) {
  const list = useMemo(() => prompts.slice().reverse(), [prompts]);
  const how = agent === "codex" ? HOW.filter((h) => !h.code) : HOW;
  const [prompt, setPrompt] = useState(start || null);
  const [sel, setSel] = useState(0);
  const rows = prompt ? how : list;
  const listRef = useRef(null);

  useEffect(() => {
    setSel(0);
    listRef.current?.focus();
  }, [prompt]);

  const choose = (i) => {
    if (!prompt) return setPrompt(list[i]);
    const h = how[i];
    if (!h.conversation && !h.code) return onClose();
    onRewind(prompt, { conversation: h.conversation, code: h.code });
  };

  const onKey = (e) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const d = e.key === "ArrowDown" ? 1 : -1;
      setSel((s) => (s + d + rows.length) % rows.length);
    } else if (e.key === "Enter") {
      e.preventDefault();
      choose(sel);
    }
  };

  return (
    <Modal onClose={onClose} onBack={prompt && !start ? () => setPrompt(null) : undefined} className="palette rewind">
      <div className="viewer-head">
        <IconUndo size={13} className="spark" />
        <span className="rewind-title">{prompt ? "Rewind to before this message" : "Rewind to before which message?"}</span>
        <span className="spacer" />
        <button className="ghost" onClick={onClose}>
          <IconX size={13} />
        </button>
      </div>
      {prompt && <div className="palette-note rewind-quote">{saidAs(prompt) || (prompt.images ? "(images)" : "(context only)")}</div>}
      {prompt?.compacted && (
        <div className="palette-note">
          From before the conversation was compacted: it comes back as it was then, whole, and Claude Code compacts it again when it no longer fits.
        </div>
      )}
      <div className="rewind-list" tabIndex={-1} ref={listRef} onKeyDown={onKey}>
        {rows.map((r, i) => (
          <button
            key={prompt ? r.label : r.key}
            className={cx("palette-row", i === sel && "on")}
            onMouseMove={() => setSel(i)}
            onClick={() => choose(i)}
          >
            {prompt ? (
              <>
                <span>{r.label}</span>
                {r.hint && <span className="why">{r.hint}</span>}
              </>
            ) : (
              <>
                <span className="rewind-text">{saidAs(r).split("\n")[0] || (r.images ? "(images)" : "(context only)")}</span>
                <span className="why">{r.compacted ? `before compacting · ${relTime(r.at)}` : relTime(r.at)}</span>
              </>
            )}
          </button>
        ))}
        {!prompt && loading && <div className="palette-note">Finding the messages from before the conversation was compacted…</div>}
      </div>
      <div className="palette-foot">
        <span>
          <kbd>↑</kbd> <kbd>↓</kbd> choose · <kbd>Enter</kbd> rewind · <kbd>Esc</kbd> {prompt && !start ? "back" : "close"}
        </span>
        {running === "dv" && <span className="spacer" />}
        {running === "dv" && <span>Anything {agentName(agent)} is doing stops.</span>}
      </div>
    </Modal>
  );
}

// SessionID is enough of the ID to tell sessions apart; a click copies all of
// it, for claude --resume or codex resume.
function SessionID({ id }) {
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const t = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(t);
  }, [copied]);
  return (
    <button
      className={cx("session-id", copied && "copied")}
      title={`Copy the session ID, ${id}`}
      onKeyDown={(e) => e.stopPropagation()}
      onClick={(e) => {
        e.stopPropagation();
        navigator.clipboard.writeText(id).then(() => setCopied(true), () => {});
      }}
    >
      {copied ? "copied" : id.slice(0, 8)}
    </button>
  );
}

// SessionList is the sidebar in Agent mode: sessions open in dv, then the rest
// of the repository's, newest first. asking is the set of sessions with a
// prompt or question waiting on the reader.
export function SessionList({ sessions, available, usage, asking, activeId, added = {}, onSelect, onNew, onNewWorktree, onClose, onRename }) {
  const [filter, setFilter] = useState("");
  // The card is where the session on screen is named, so it is kept in sight.
  const listRef = useRef(null);
  useEffect(() => {
    listRef.current?.querySelector(".session-row.on")?.scrollIntoView({ block: "nearest" });
  }, [activeId]);
  const q = filter.trim().toLowerCase();
  const shown = q ? sessions.filter((s) => `${s.title} ${s.prompt} ${s.id}`.toLowerCase().includes(q)) : sessions;
  const isOpen = (s) => s.open || s.running === "dv";
  const open = shown.filter(isOpen);
  const recent = shown.filter((s) => !isOpen(s));

  // A card: what the session is, the last thing said in it, and how full it is.
  const row = (s) => {
    const c = s.context;
    const pct = c && Math.min(100, (c.used / c.max) * 100);
    const limit = c && (c.compact || c.max);
    return (
      <div
        key={s.id}
        role="button"
        tabIndex={0}
        className={cx("session-row", s.id === activeId && "on")}
        onClick={() => onSelect(s.id)}
        onKeyDown={(e) => e.key === "Enter" && onSelect(s.id)}
      >
        <div className="session-row-head">
          <span className={cx("session-dot", s.running && "live-" + s.running, s.busy && "busy", asking?.has(s.id) && "asking")} />
          <SessionName
            key={s.title}
            title={s.title || s.prompt || "New session"}
            hint={s.title || s.prompt || ""}
            onRename={s.running !== "terminal" ? (t) => onRename(s.id, t) : null}
          />
          {isOpen(s) && (
            <button
              className="session-close"
              title={closeHint(s.running, s.temporary, s.agent)}
              onClick={(e) => {
                e.stopPropagation();
                onClose(s.id);
              }}
            >
              <IconX size={11} />
            </button>
          )}
        </div>
        {s.last && (
          <div className="session-last">
            {s.lastBy === "you" && <span className="session-you">You: </span>}
            {s.last}
          </div>
        )}
        <div className="session-meta">
          {asking?.has(s.id) ? (
            <span className="session-asking">waiting on you</span>
          ) : (
            <span>{s.running === "terminal" ? "in a terminal" : s.running === "dv" ? (s.busy ? s.status || "working" : "running in dv") : relTime(s.updated)}</span>
          )}
          <span className="session-agent" title={s.agent === "codex" ? "A Codex session" : "A Claude Code session"}>
            <AgentIcon agent={s.agent} size={11} />
          </span>
          <SessionID id={s.id} />
          {s.temporary && (
            <span className="session-temporary" title="Temporary: it leaves the list once closed">
              <IconTemporary size={11} />
            </span>
          )}
          {added[s.id]?.length > 0 && (
            <span className="session-added" title="Added from Diff or Files, to go with the next message">
              {added[s.id].length} added
            </span>
          )}
          <span className="spacer" />
          {c && (
            <span
              className={cx("session-context", c.used >= limit * 0.9 ? "high" : c.used >= limit * 0.75 && "warn")}
              title={`Context: ${c.used.toLocaleString()} of ${c.max.toLocaleString()} tokens`}
            >
              {Math.round(pct)}%
            </span>
          )}
        </div>
      </div>
    );
  };

  return (
    <>
      <div className="sidebar-filter">
        <input value={filter} placeholder="Filter by name or ID" onChange={(e) => setFilter(e.target.value)} onKeyDown={(e) => e.stopPropagation()} />
        <button className="ghost" onClick={onNew} title="New session (Alt+N)" disabled={!available}>
          <IconPlus size={13} />
        </button>
        {onNewWorktree && (
          <button className="ghost" onClick={onNewWorktree} title="New session in a worktree" disabled={!available}>
            <IconBranch size={13} />
          </button>
        )}
      </div>
      <div className="file-list session-list" ref={listRef}>
        {open.length > 0 && <div className="session-group">Open</div>}
        {open.map(row)}
        {recent.length > 0 && <div className="session-group">Recent</div>}
        {recent.map(row)}
        {!available && <div className="empty">Neither the claude nor the codex CLI is on your PATH.</div>}
        {available && shown.length === 0 && <div className="empty">{q ? "No sessions match." : "No sessions in this repository yet."}</div>}
      </div>
      <div className="sidebar-foot">
        <span className="dim session-count">
          {sessions.length} session{sessions.length === 1 ? "" : "s"}
        </span>
        <span className="spacer" />
        {usage && <PlanLimit label="5h" of="This five-hour window" limit={usage.session} />}
        {usage && <PlanLimit label="7d" of="This week" limit={usage.week} />}
      </div>
    </>
  );
}

// PlanLimit is how much of one of a Claude plan's usage limits is spent.
function PlanLimit({ label, of, limit }) {
  if (!limit) return null;
  const pct = Math.min(100, limit.percent);
  const resets = limit.resetsAt && new Date(limit.resetsAt);
  const when =
    resets &&
    (resets.toDateString() === new Date().toDateString()
      ? resets.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })
      : resets.toLocaleString([], { weekday: "short", hour: "numeric", minute: "2-digit" }));
  return (
    <span className={cx("plan-limit", pct >= 90 ? "high" : pct >= 75 && "warn")} title={`${of}: ${Math.round(limit.percent)}% of the plan's usage${when ? `, resets ${when}` : ""}`}>
      {/* Elements, not bare text: with the bar hidden, text would run together. */}
      <span>{label}</span>
      <Bar pct={pct} />
      <span>{Math.round(limit.percent)}%</span>
    </span>
  );
}
