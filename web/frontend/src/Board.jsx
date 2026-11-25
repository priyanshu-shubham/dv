import { createContext, Fragment, useCallback, useContext, useEffect, useId, useMemo, useRef, useState } from "react";
import { api } from "./api.js";
import { applyFileFilters, chase, cx, isMac, isTyping, laidOut, listFilter, relTime, usePersisted, yOf } from "./util.js";
import { NO_FILTER, PathFilterPanel } from "./PathFilter.jsx";

const altKey = isMac ? "⌥" : "Alt";
import FileDiff from "./FileDiff.jsx";
import { flatten } from "./hunks.js";
import { usePref } from "./prefs.js";
import { Modal } from "./Overlays.jsx";
import { MarkdownDocument, MarkdownPreview } from "./Preview.jsx";
import { ModelPicker, Picker, prettyModel, useModelMenu } from "./Picker.jsx";
import { filesIn, PlanUsage, readImage, Shown } from "./Agent.jsx";
import { IconBranch, IconCheck, IconChevron, IconChevronDown, IconFilter, IconFullScreen, IconLock, IconOpenOut, IconPanelFold, IconPanelOpen, IconPlus, IconShrink, IconTrash, IconX } from "./icons.jsx";

// The board: a repository's tickets, each taken from a rough request through
// scope, plan, build and review to a merged pull request, with an agent at
// each stage that the reader can talk to at any time.

export const STAGES = [
  ["inbox", "Inbox"],
  ["scope", "Scope"],
  ["plan", "Plan"],
  ["build", "Build"],
  ["review", "Your review"],
  ["merged", "Merged"],
];
export const LABEL = Object.fromEntries(STAGES);
const INDEX = Object.fromEntries(STAGES.map(([k], i) => [k, i]));
const STARTS = [
  ["inbox", "Inbox", "Waits there until you move it."],
  ["scope", "Scope", "An agent starts turning it into a full ticket right away."],
  ["plan", "Plan", "Skips scoping: the description is the ticket, and a planner starts from it."],
  ["build", "Build", "Skips scoping and planning: the description is the plan, and a builder starts from it."],
];
const TEMPLATES = { correctness: "Correctness", quality: "Code quality", tests: "Test value" };
const PHASE = { review: "reviewing", fix: "fixing", recheck: "rechecking", guide: "writing your review guide", done: "done" };
const POLL_MS = 2500;
const NO_BOARD = { tickets: [], drives: true, templates: [] };

// useBoard is the board, read again while shown: what its agents are doing
// changes with no file changing.
export function useBoard(active) {
  const [board, setBoard] = useState(NO_BOARD);
  const [error, setError] = useState("");
  const load = useCallback(() => {
    api.board().then(
      (b) => {
        setBoard({ ...b, tickets: b.tickets || [] });
        setError("");
      },
      (e) => setError(e.message),
    );
  }, []);
  useEffect(() => {
    if (!active) return;
    load();
    const t = setInterval(() => document.visibilityState === "visible" && load(), POLL_MS);
    return () => clearInterval(t);
  }, [active, load]);
  return [board, load, error];
}

// cardState is the one line a card says of where its ticket is, and whether
// that waits on the reader.
export function cardState(t, byId) {
  // Merged, what's left for the reader is its follow-ups, filed or dismissed.
  if (t.stage === "merged") {
    const n = t.suggestions?.length || 0;
    if (n) return { kind: "needs", text: `${n} suggested follow-up${n === 1 ? "" : "s"} to decide` };
    return t.nothing ? { kind: "idle", text: "No change" } : null;
  }
  if (t.problem) return { kind: "problem", text: t.problem.split("\n")[0] };
  // A split ticket's scoper still answers its parts' questions, with the reader.
  if (t.asking) return { kind: "needs", text: "Waiting on your answer" };
  if (t.nothing) return { kind: "needs", text: `Nothing to do, says the ${ROLE_NAME[t.nothing.by]?.toLowerCase() || t.nothing.by}` };
  if (t.split) {
    const kids = (t.children || []).map((id) => byId[id]).filter(Boolean);
    return { kind: "split", text: `${kids.filter((k) => k.stage === "merged").length} of ${kids.length} merged`, kids };
  }
  // What it handed over waits on the reader again once it stops: a question
  // answered leaves it as it was, and a change hands it over anew.
  if (t.ready && t.working) return { kind: "working", text: "Working on your message" };
  if (t.ready) return { kind: "needs", text: t.stage === "scope" ? "Ticket ready to approve" : "Plan ready to approve" };
  if (t.blocked?.length) return { kind: "blocked", text: `Waits for ${t.blocked.join(", ")}` };
  if (t.stage === "inbox") return null;
  if (t.stage === "review") return t.merging ? { kind: "ci", text: "Merges when CI passes" } : { kind: "needs", text: "Ready for your review" };
  if (t.stage === "plan" && t.question) return { kind: t.working ? "working" : "idle", text: "The planner has the builder's question" };
  if (t.scopeQuestion) return { kind: "idle", text: t.stage === "plan" ? `${t.parent}'s scoper has the planner's question` : "The scoper has the planner's question" };
  if (reviewing(t)) {
    const r = t.rounds[t.rounds.length - 1];
    const what = r.kind === "main" ? "Checking what the trunk brought in ·" : `Agent review · round ${t.rounds.length}`;
    return { kind: t.working ? "working" : "idle", text: `${what} ${PHASE[r.phase] || r.phase}` };
  }
  // The builder resolving conflicts with the trunk, before that check.
  if (t.catchUp?.conflicts && t.stage === "build") return { kind: t.working ? "working" : "idle", text: "Resolving conflicts with the trunk" };
  return { kind: t.working ? "working" : "idle", text: t.status || (t.working ? "Working" : "Idle") };
}

// worktreeAt is the ticket's worktree as a path from the repository's root,
// "" before it has one.
export const worktreeAt = (t) => (t.worktree ? t.worktree.slice(t.worktree.lastIndexOf("/.dv/") + 1) + "/" : "");

// startEarly asks before a ticket leaves the Inbox ahead of what it waits for.
export const startEarly = (t) =>
  `${t.id} waits for ${t.blocked.join(", ")}, which isn't merged. Started now, its worktree is cut from trunk without those changes, and its agents are told so.\n\nStart it anyway?`;

// reviewing is the built work under agent review, which is part of Build.
const reviewing = (t) => t.stage === "build" && !!t.rounds?.length && t.rounds[t.rounds.length - 1].phase !== "done";

// createTicket files a ticket with its images, and only then starts it, so
// its first agent has them.
export async function createTicket({ stage, images = [], pick, ...t }) {
  const made = await api.addTicket({ ...t, stage: "inbox" });
  for (const img of images) await api.addTicketImage(made.id, { mediaType: img.mediaType, data: img.data });
  if (pick) await api.patchTicket(made.id, { pick });
  if (stage !== "inbox") await api.moveTicket(made.id, stage);
  return made;
}

// readImages is what a paste or drop carries that a ticket can hold.
async function readImages(files) {
  const out = [];
  for (const f of files) if (f.type.startsWith("image/")) out.push(await readImage(f));
  return out;
}

const pasted = (e) => filesIn(e.clipboardData).filter((f) => f.type.startsWith("image/"));
const dropped = (e) => [...(e.dataTransfer?.files || [])].filter((f) => f.type.startsWith("image/"));

function Thumbs({ images, onRemove }) {
  if (!images.length) return null;
  return (
    <div className="board-thumbs">
      {images.map((img) => (
        <span key={img.key} className="board-thumb">
          <a href={img.url} target="_blank" rel="noreferrer">
            <img src={img.url} alt="" />
          </a>
          {onRemove && (
            <button title="Take this image off" onClick={() => onRemove(img)}>
              ×
            </button>
          )}
        </span>
      ))}
    </div>
  );
}

// TicketImages are a ticket's pictures, with room for more: pasted anywhere
// in the ticket's documents, or dropped on them.
function TicketImages({ t, drives, act }) {
  const images = (t.images || []).map((name) => ({ key: name, name, url: api.ticketImageURL(t.id, name) }));
  if (!images.length && !drives) return null;
  return (
    <>
      <h4>Images</h4>
      <Thumbs images={images} onRemove={drives ? (img) => act(api.deleteTicketImage(t.id, img.name)) : null} />
      {drives && <p className="board-muted">Paste a screenshot, or drop an image here.</p>}
    </>
  );
}

export const needsYou = (s) => s?.kind === "needs" || s?.kind === "problem";

// allLabels is every label on the board, by how many tickets carry it.
export function allLabels(tickets) {
  const count = {};
  for (const t of tickets) for (const l of t.labels || []) count[l] = (count[l] || 0) + 1;
  return Object.keys(count).sort((a, b) => count[b] - count[a] || a.localeCompare(b));
}

export function Labels({ labels }) {
  if (!labels?.length) return null;
  return (
    <div className="board-labels">
      {labels.map((l) => (
        <span key={l} className="board-label">
          {l}
        </span>
      ))}
    </div>
  );
}

// LabelEditor is a ticket's labels, each with a way off, and a box for one more
// that offers the board's others.
export function LabelEditor({ labels: given, known, onChange }) {
  const labels = given || [];
  const [text, setText] = useState("");
  const add = () => {
    const l = text.trim();
    setText("");
    if (l && !labels.some((x) => x.toLowerCase() === l.toLowerCase())) onChange([...labels, l]);
  };
  return (
    <div className="board-labels edit">
      {labels.map((l) => (
        <span key={l} className="board-label">
          {l}
          <button title={`Take ${l} off`} onClick={() => onChange(labels.filter((x) => x !== l))}>
            ×
          </button>
        </span>
      ))}
      <input
        list="board-label-list"
        value={text}
        placeholder={labels.length ? "+" : "Add a label"}
        onChange={(e) => setText(e.target.value)}
        onKeyDown={(e) => (e.key === "Enter" || e.key === ",") && (e.preventDefault(), add())}
        onBlur={add}
      />
      <datalist id="board-label-list">
        {known
          .filter((l) => !labels.includes(l))
          .map((l) => (
            <option key={l} value={l} />
          ))}
      </datalist>
    </div>
  );
}

export function StateGlyph({ kind }) {
  if (kind === "working") return <span className="board-spin" />;
  if (kind === "ci") return <span className="board-spin ci" />;
  if (kind === "blocked") return <IconLock size={11} />;
  if (kind === "split") return <IconBranch size={11} />;
  return <span className={cx("board-dot", kind)} />;
}

export function PRBadge({ t }) {
  if (!t.pr) return null;
  const state = t.stage === "merged" ? "merged" : t.ci;
  const said = { passing: "CI passed", failing: "CI failed", pending: "CI running", merged: "Merged" }[state] || "No CI checks";
  const Tag = t.prURL ? "a" : "span";
  return (
    <Tag
      className={cx("board-pr", state)}
      href={t.prURL || undefined}
      target="_blank"
      rel="noopener noreferrer"
      title={t.prURL ? `${said} · open #${t.pr} on GitHub` : said}
      // On a card, the link and not the card.
      onClick={(e) => e.stopPropagation()}
    >
      {state === "passing" && <IconCheck size={10} />}
      {state === "failing" && <span className="board-pr-x">✕</span>}
      {state === "pending" && <span className="board-dot ci" />}#{t.pr}
    </Tag>
  );
}

// isQuiet is a card nothing can be done with yet: it waits for other tickets,
// or for its parts.
export const isQuiet = (s) => s?.kind === "blocked" || s?.kind === "split";

// The priorities the reader gives tickets, highest first; "" is normal.
export const PRIORITIES = [
  ["urgent", "Urgent"],
  ["high", "High"],
  ["", "Normal"],
  ["low", "Low"],
];
const RANK = Object.fromEntries(PRIORITIES.map(([k], i) => [k, i]));
export const byPriority = (a, b) => RANK[a.priority || ""] - RANK[b.priority || ""];

// quietLast puts the quiet tickets under the rest, each by priority, then in
// the order filed.
const quietLast = (list, byId) => {
  const quiet = (t) => isQuiet(cardState(t, byId));
  return [...list.filter((t) => !quiet(t)).sort(byPriority), ...list.filter(quiet).sort(byPriority)];
};

// mergedOrder is the Merged column's: what still needs the reader (follow-ups
// to decide) first, then the latest merged. Priority is spent by then. A
// ticket merged before stage times were kept goes by its last update.
const mergedOrder = (list, byId) => {
  const at = (t) => t.stageAt || t.updatedAt || "";
  const latest = (a, b) => at(b).localeCompare(at(a));
  const waits = (t) => needsYou(cardState(t, byId));
  return [...list.filter(waits).sort(latest), ...list.filter((t) => !waits(t)).sort(latest)];
};

// PriorityIcon draws a priority as a scale of chevrons: three up for
// urgent, two for high, one down for low; normal has none.
// Normal draws an empty square, so a list of them lines up.
export function PriorityIcon({ p, size = 12 }) {
  const d = {
    urgent: "M4.5 5.5L8 2l3.5 3.5M4.5 9.5L8 6l3.5 3.5M4.5 13.5L8 10l3.5 3.5",
    high: "M4 8.5L8 4.5l4 4M4 12.5l4-4 4 4",
    low: "M4 6.5l4 4 4-4",
  }[p];
  return (
    <svg width={size} height={size} viewBox="0 0 16 16" aria-hidden="true">
      {d && <path d={d} fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" />}
    </svg>
  );
}

// PriorityMark is a ticket's priority on its card, its icon alone; normal shows nothing.
export const PriorityMark = ({ p }) =>
  p ? (
    <span className={cx("board-prio", p)} title={`${PRIORITIES.find(([k]) => k === p)?.[1]} priority`} aria-label={`${p} priority`}>
      <PriorityIcon p={p} size={14} />
    </span>
  ) : null;

// PriorityPick changes a ticket's priority, at any stage.
function PriorityPick({ t, act }) {
  const p = t.priority || "";
  return (
    <Picker
      button={cx("mini", "board-prio-pick", p)}
      label={
        <span className="board-prio-label">
          <PriorityIcon p={p} />
          {p ? `${PRIORITIES.find(([k]) => k === p)[1]} priority` : "Priority"}
        </span>
      }
      title="How soon it matters: it orders the board, and the agents don't see it"
      choices={PRIORITIES.map(([id, label]) => ({
        id,
        label: (
          <span className={cx("board-prio-label", id)}>
            <PriorityIcon p={id} />
            {label}
          </span>
        ),
      }))}
      value={p}
      down
      onPick={(priority) => act(api.patchTicket(t.id, { priority }))}
    />
  );
}

const MINUTE = 60_000;

// spanOf is the time since iso, as a card has room for (3h) or in words (3 hours).
function spanOf(iso, words) {
  const mins = Math.max(0, (Date.now() - new Date(iso).getTime()) / MINUTE);
  const [n, unit] =
    mins < 1 ? [0, "minute"] : mins < 60 ? [mins, "minute"] : mins < 24 * 60 ? [mins / 60, "hour"] : mins < 14 * 24 * 60 ? [mins / 1440, "day"] : [mins / 10080, "week"];
  const whole = Math.floor(n);
  if (!words) return whole ? `${whole}${unit[0]}` : "now";
  return whole ? `${whole} ${unit}${whole === 1 ? "" : "s"}` : "under a minute";
}

const stampOf = (iso) => new Date(iso).toLocaleString(undefined, { day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });

// StageAge is how long the ticket has been in its stage, the clock restarting
// whenever it moves; when its work began is in the tooltip. Tickets from
// before the board kept it show none.
export function StageAge({ t }) {
  if (!t.stageAt) return null;
  let title;
  if (t.stage === "merged") title = `Merged ${spanOf(t.stageAt, true)} ago, on ${stampOf(t.stageAt)}`;
  else if (t.stage === "inbox") title = `In the Inbox for ${spanOf(t.stageAt, true)}, since ${stampOf(t.stageAt)}`;
  else {
    title = `In ${LABEL[t.stage]} for ${spanOf(t.stageAt, true)}, since ${stampOf(t.stageAt)}`;
    if (t.startedAt) title += ` · work began ${spanOf(t.startedAt, true)} ago`;
  }
  return (
    <span className="board-age" title={title}>
      {spanOf(t.stageAt)}
    </span>
  );
}

function Card({ t, byId, models, act, onOpen, onMove, dragging, onDrag }) {
  const s = cardState(t, byId);
  const quiet = isQuiet(s);
  return (
    <div
      className={cx("board-card", needsYou(s) && !t.archived && "needs", quiet && "quiet", t.priority === "low" && "low", (t.stage === "merged" || t.archived) && "done", dragging && "dragging")}
      draggable
      onDragStart={(e) => {
        e.dataTransfer.effectAllowed = "move";
        e.dataTransfer.setData("text/plain", t.id);
        onDrag(t.id);
      }}
      onDragEnd={() => onDrag(null)}
      onClick={() => onOpen(t.id)}
    >
      <div className="board-card-top">
        <span className="board-id">
          {t.parent && <span className="board-parent" title={byId[t.parent]?.title}>{t.parent} › </span>}
          {t.from && <span className="board-parent" title={`A follow-up of ${t.from}: ${byId[t.from]?.title || ""}`}>{t.from} → </span>}
          {t.id}
        </span>
        <PriorityMark p={t.priority} />
        {t.images?.length > 0 && (
          <span className="board-imgs" title={`${t.images.length} image${t.images.length === 1 ? "" : "s"}`}>
            <svg width="11" height="11" viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" aria-hidden="true">
              <rect x="2" y="3" width="12" height="10" rx="1.5" />
              <path d="M2.5 11.5l3.5-3.5 3 3 2-2 2.5 2.5" />
            </svg>
            {t.images.length}
          </span>
        )}
        <PRBadge t={t} />
        <StageAge t={t} />
      </div>
      <div className="board-card-title">{t.title}</div>
      <Labels labels={t.archived ? ["archived", ...(t.labels || [])] : t.labels} />
      {s && !t.archived && (
        <div className={cx("board-state", s.kind)}>
          <StateGlyph kind={s.kind} />
          <span className="board-state-text">{s.text}</span>
          {s.kids && (
            <span className="board-bar">
              {s.kids.map((k) => (
                <i key={k.id} className={k.stage === "merged" ? "fin" : INDEX[k.stage] > INDEX.plan ? "mid" : ""} title={`${k.id} · ${LABEL[k.stage]}`} />
              ))}
            </span>
          )}
        </div>
      )}
      {t.question && t.stage === "plan" && <div className="board-notice">Build paused</div>}
      {t.scopeQuestion && <div className="board-notice">Plan paused</div>}
      {t.stage === "inbox" && !t.archived && (
        <div className="board-move-row" onClick={(e) => e.stopPropagation()}>
          <span title="The scoper starts on this">
            <RolePick t={t} role="scope" models={models} act={act} />
          </span>
          <button className="board-move" onClick={() => onMove(t.id, "scope")}>
            Scope →
          </button>
        </div>
      )}
    </div>
  );
}

// BoardSideList is the sidebar in Board mode: what waits on the reader, then
// what its agents are working on.
export function BoardSideList({ board, openId, onOpen, usage }) {
  const byId = useMemo(() => Object.fromEntries(board.tickets.map((t) => [t.id, t])), [board.tickets]);
  const rows = board.tickets
    .filter((t) => !t.archived)
    .map((t) => ({ t, s: cardState(t, byId) }))
    .filter(({ s }) => s && s.kind !== "split" && s.kind !== "blocked");
  const needs = rows.filter(({ s }) => needsYou(s));
  const working = rows.filter(({ s }) => !needsYou(s) && (s.kind === "working" || s.kind === "ci"));
  const group = (title, list) =>
    list.length > 0 && (
      <>
        <div className="board-side-head">{title}</div>
        {list.map(({ t, s }) => (
          <button key={t.id} className={cx("board-side-row", openId === t.id && "on")} onClick={() => onOpen(t.id)}>
            <span className="board-side-title">
              <span className="board-id">{t.id}</span> {t.title}
            </span>
            <span className={cx("board-state", s.kind)}>
              <StateGlyph kind={s.kind} />
              <span className="board-state-text">{s.text}</span>
            </span>
          </button>
        ))}
      </>
    );
  return (
    <>
      <div className="board-side">
        {group("Needs you", needs)}
        {group("Working", working)}
        {!needs.length && !working.length && <div className="board-empty">Nothing waits on you, and no agent is working.</div>}
      </div>
      {usage?.some((u) => u.limits) && (
        <div className="sidebar-foot">
          <span className="spacer" />
          <PlanUsage usage={usage} />
        </div>
      )}
    </>
  );
}

// BoardView is the Board mode: the columns, and the ticket open over them.
// session(id) draws a session's conversation, to talk to its agent.
export default function BoardView({ active, board, load, loadError, openId, onOpen: setOpenId, models, session, onOpenFile, onSymbol, onOpenCode, onLooking }) {
  const [adding, setAdding] = useState(false); // or the stage the new ticket starts at
  const [onlyNeeds, setOnlyNeeds] = useState(false);
  const [archived, setArchived] = useState(false);
  const [search, setSearch] = useState("");
  const [labelFilter, setLabelFilter] = useState([]);
  const [prioFilter, setPrioFilter] = useState([]);
  const [drag, setDrag] = useState(null);
  const [over, setOver] = useState(null);
  const [back, setBack] = useState(null); // { id, stage }
  const [error, setError] = useState("");
  const byId = useMemo(() => Object.fromEntries(board.tickets.map((t) => [t.id, t])), [board.tickets]);
  const live = board.tickets.filter((t) => !t.archived);
  const needs = live.filter((t) => needsYou(cardState(t, byId)));
  const known = useMemo(() => allLabels(board.tickets), [board.tickets]);

  const act = useCallback(
    (p) =>
      p.then(
        () => (setError(""), load()),
        (e) => (setError(e.message), load()),
      ),
    [load],
  );
  const move = useCallback(
    (id, stage) => {
      const t = byId[id];
      if (!t || t.stage === stage) return;
      if (INDEX[stage] < INDEX[t.stage] && stage !== "inbox") return setBack({ id, stage });
      const early = t.stage === "inbox" && stage !== "inbox" && t.blocked?.length > 0;
      if (early && !confirm(startEarly(t))) return;
      return act(api.moveTicket(id, stage, "", early));
    },
    [byId, act],
  );

  const q = search.trim().toLowerCase();
  const shown = board.tickets.filter(
    (t) =>
      (archived || !t.archived) &&
      (!onlyNeeds || needs.includes(t)) &&
      (!labelFilter.length || labelFilter.some((l) => t.labels?.includes(l))) &&
      (!prioFilter.length || prioFilter.includes(t.priority || "")) &&
      (!q || [t.id, t.title, t.request, ...(t.labels || [])].join("\n").toLowerCase().includes(q)),
  );
  // Offered once some ticket has one other than normal.
  const prios = PRIORITIES.filter(([k]) => board.tickets.some((t) => (t.priority || "") === k && !t.archived));
  const mergedShown = shown.filter((t) => t.stage === "merged" && !t.archived);
  return (
    <div className="board-pane" hidden={!active}>
      <div className="board-bar-top">
        <input className="board-search" type="search" value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Search tickets" />
        {known.length > 0 && (
          <div className="board-chips">
            {known.map((l) => (
              <button
                key={l}
                className={cx("board-chip", "small", labelFilter.includes(l) && "on")}
                onClick={() => setLabelFilter((f) => (f.includes(l) ? f.filter((x) => x !== l) : [...f, l]))}
                title={`Only tickets labelled ${l}`}
              >
                {l}
              </button>
            ))}
          </div>
        )}
        {prios.some(([k]) => k) && (
          <div className="board-chips">
            {prios.map(([k, l]) => (
              <button
                key={k || "normal"}
                className={cx("board-chip", "small", "board-prio-chip", k, prioFilter.includes(k) && "on")}
                onClick={() => setPrioFilter((f) => (f.includes(k) ? f.filter((x) => x !== k) : [...f, k]))}
                title={`Only ${l.toLowerCase()} priority tickets`}
              >
                {k && <PriorityIcon p={k} size={11} />}
                {l}
              </button>
            ))}
          </div>
        )}
        {!board.drives && <span className="board-note">The board's agents run in the dv of the repository's main checkout; open it there to move tickets.</span>}
        {(error || loadError) && <span className="board-error">{error || loadError}</span>}
        <span className="spacer" />
        <button className={cx("btn", archived && "on")} onClick={() => setArchived((o) => !o)} title="Show archived tickets too">
          Archived
        </button>
        <button className={cx("btn", onlyNeeds && "on")} onClick={() => setOnlyNeeds((o) => !o)} title="Show only what waits on you">
          <span className="board-dot needs" />
          Needs you {needs.length > 0 && <span className="board-count">{needs.length}</span>}
        </button>
        <button className="btn" onClick={() => setAdding("inbox")}>
          <IconPlus size={13} />
          New ticket
        </button>
      </div>
      <div className="board-cols">
        {STAGES.map(([key, label]) => {
          const list = (key === "merged" ? mergedOrder : quietLast)(shown.filter((t) => t.stage === key), byId);
          return (
            <section
              key={key}
              data-stage={key}
              className={cx("board-col", !list.length && "bare", over === key && "drop")}
              onDragOver={(e) => {
                if (!drag) return;
                e.preventDefault();
                setOver(key);
              }}
              onDragLeave={(e) => !e.currentTarget.contains(e.relatedTarget) && setOver(null)}
              onDrop={(e) => {
                e.preventDefault();
                setOver(null);
                if (drag) move(drag, key);
              }}
            >
              <div className="board-col-head">
                {label}
                {/* Past the Inbox, a ticket filed here starts work at once, so the + waits for a hover. */}
                {(key === "inbox" || MOVE_STARTS[key]) && (
                  <button
                    className={cx("board-col-add", key !== "inbox" && "on-hover")}
                    title={key === "inbox" ? "New ticket in the Inbox" : `New ticket starting at ${label}: its agent starts on it`}
                    onClick={() => setAdding(key)}
                  >
                    <IconPlus size={12} />
                  </button>
                )}
                {key === "merged" && mergedShown.length > 0 && board.drives && (
                  <button
                    className="board-col-act"
                    title="Archive every merged ticket shown"
                    onClick={() => act(Promise.all(mergedShown.map((t) => api.patchTicket(t.id, { archived: true }))))}
                  >
                    Archive all
                  </button>
                )}
              </div>
              <div className="board-col-body">
                {list.map((t) => (
                  <Card key={t.id} t={t} byId={byId} models={models} act={act} onOpen={setOpenId} onMove={move} dragging={drag === t.id} onDrag={setDrag} />
                ))}
              </div>
            </section>
          );
        })}
      </div>
      {adding && (
        <NewTicket
          start={adding}
          known={known}
          models={models}
          picks={board.picks}
          onClose={() => setAdding(false)}
          onCreate={(t) =>
            createTicket(t).then((made) => {
              setAdding(false);
              load();
              if (t.stage !== "inbox") setOpenId(made.id);
            })
          }
        />
      )}
      {openId && byId[openId] && (
        <TicketModal
          key={openId}
          id={openId}
          summary={byId[openId]}
          byId={byId}
          known={known}
          drives={board.drives}
          models={models}
          session={session}
          onOpenTicket={setOpenId}
          onLooking={onLooking}
          onOpenFile={onOpenFile}
          onSymbol={onSymbol}
          onOpenCode={onOpenCode}
          onClose={() => setOpenId(null)}
          onMove={move}
          act={act}
        />
      )}
      {back && (
        <SendBack
          id={back.id}
          stage={back.stage}
          onClose={() => setBack(null)}
          onSend={(note) => {
            setBack(null);
            act(api.moveTicket(back.id, back.stage, note));
          }}
        />
      )}
    </div>
  );
}

export function NewTicket({ start = "inbox", known, models, picks, onClose, onCreate }) {
  const [title, setTitle] = useState("");
  const [request, setRequest] = useState("");
  const [labels, setLabels] = useState([]);
  const [priority, setPriority] = useState("");
  const [images, setImages] = useState([]);
  const [stage, setStage] = useState(start);
  const [picked, setPicked] = useState({}); // by role, what was changed from the last pick
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const role = MOVE_STARTS[stage];
  const create = () => {
    if (!title.trim() || busy) return;
    setBusy(true);
    const pick = role && picked[role] && { role, ...picked[role] };
    onCreate({ title, request, labels, priority, stage, images, pick }).catch((e) => (setError(e.message), setBusy(false)));
  };
  const take = (files) =>
    files.length &&
    readImages(files).then(
      (got) => setImages((list) => [...list, ...got.map((g) => ({ ...g, url: `data:${g.mediaType};base64,${g.data}` }))]),
      (e) => setError(e.message),
    );
  return (
    <Modal onClose={onClose} className="board-form">
      <div
        onPaste={(e) => {
          const files = pasted(e);
          if (!files.length) return;
          if (!e.clipboardData.getData("text/plain")) e.preventDefault();
          take(files);
        }}
        onDragOver={(e) => e.preventDefault()}
        onDrop={(e) => {
          const files = dropped(e);
          if (!files.length) return;
          e.preventDefault();
          take(files);
        }}
      >
      <div className="board-form-head">New ticket</div>
      <label className="board-field">
        <span>Title</span>
        <input autoFocus value={title} onChange={(e) => setTitle(e.target.value)} placeholder="What should change" onKeyDown={(e) => e.key === "Enter" && create()} />
      </label>
      <label className="board-field">
        <span>Description</span>
        <textarea rows={4} value={request} onChange={(e) => setRequest(e.target.value)} placeholder="As rough as you like. Paste or drop screenshots too." />
      </label>
      <Thumbs images={images} onRemove={(img) => setImages((list) => list.filter((x) => x !== img))} />
      <div className="board-field">
        <span>Labels</span>
        <LabelEditor labels={labels} known={known} onChange={setLabels} />
      </div>
      <div className="board-field">
        <span>Priority</span>
        <div className="board-chips">
          {PRIORITIES.map(([k, l]) => (
            <button key={k || "normal"} className={cx("board-chip", "board-prio-chip", k, k === priority && "on")} onClick={() => setPriority(k)}>
              {k && <PriorityIcon p={k} />}
              {l}
            </button>
          ))}
        </div>
      </div>
      <div className="board-field">
        <span>Starts in</span>
        <div className="board-chips">
          {STARTS.map(([k, l]) => (
            <button key={k} className={cx("board-chip", k === stage && "on")} onClick={() => setStage(k)}>
              {l}
            </button>
          ))}
        </div>
        <div className="board-hint">{STARTS.find((s) => s[0] === stage)[2]}</div>
        {role && (
          <div className="board-next">
            <span className="board-muted">{ROLE_NAME[role]} starts on</span>
            <PickModel pick={{ ...picks?.[role], ...picked[role] }} models={models} onPick={(p) => setPicked((all) => ({ ...all, [role]: { ...all[role], ...p } }))} />
          </div>
        )}
      </div>
      {error && <div className="board-error">{error}</div>}
      <div className="board-form-foot">
        <span className="spacer" />
        <button className="btn" onClick={onClose}>
          Cancel
        </button>
        <button className="primary" disabled={!title.trim() || busy} onClick={create}>
          Create
        </button>
      </div>
      </div>
    </Modal>
  );
}

export function SendBack({ id, stage, onClose, onSend, head = `Send ${id} back to ${LABEL[stage]}`, ask = "What needs another look", button = "Send back" }) {
  const [note, setNote] = useState("");
  return (
    <Modal onClose={onClose} className="board-form">
      <div className="board-form-head">{head}</div>
      <label className="board-field">
        <span>{ask}</span>
        <textarea autoFocus rows={3} value={note} onChange={(e) => setNote(e.target.value)} placeholder="The agent picks up from here" />
      </label>
      <div className="board-form-foot">
        <span className="spacer" />
        <button className="btn" onClick={onClose}>
          Cancel
        </button>
        <button className="primary" onClick={() => onSend(note)}>
          {button}
        </button>
      </div>
    </Modal>
  );
}

// useTicket is a ticket with its documents, read again while it is open.
// The board's own read is as fresh for what it has, and drawn first.
export function useTicket(id, summary) {
  const [data, setData] = useState(null);
  useEffect(() => {
    let live = true;
    const load = () => api.ticket(id).then((d) => live && setData(d), () => {});
    load();
    const t = setInterval(() => document.visibilityState === "visible" && load(), POLL_MS);
    return () => {
      live = false;
      clearInterval(t);
    };
  }, [id]);
  return data ? { ...data, ticket: { ...data.ticket, ...summary } } : summary ? { ticket: summary, docs: null } : null;
}

// What each stage was about: the document it wrote or worked from, and its
// agent.
export const STAGE_VIEW = {
  inbox: ["ticket", null],
  scope: ["ticket", "scope"],
  plan: ["plan", "planner"],
  // From Build on, the code: what is being built, then reviewed.
  build: ["code", "builder"],
  review: ["code", "builder"],
  merged: ["code", "builder"],
};
export const ROLE_NAME = { scope: "Scoper", planner: "Planner", "plan-reviewer": "Plan reviewer", builder: "Builder", reviewer: "Reviewer" };

// stageAgents are the sessions a stage was worked in: the plan's reviewer
// beside its planner, the code's reviewer beside its builder. A reviewer is
// its latest round's, with every round's in rounds, newest first.
export function stageAgents(t, stage) {
  const s = t.sessions || {};
  const list = [];
  const add = (role, sid, rounds) => sid && !list.some((a) => a.sid === sid) && list.push({ role, sid, label: ROLE_NAME[role], rounds });
  const newest = (sids, name) =>
    sids
      .map((sid, i) => ({ sid, label: `${name} ${i + 1}` }))
      .filter((r) => r.sid)
      .reverse();
  if (stage === "plan") {
    add("planner", s.planner);
    const rounds = newest([...(t.earlierPlanReviews || []).map((r) => r.session), t.planReview?.session || s["plan-reviewer"]], "Round");
    add("plan-reviewer", rounds[0]?.sid, rounds);
  } else if (["build", "review", "merged"].includes(stage)) {
    add("builder", s.builder);
    const rounds = (t.rounds || [])
      .map((r, i) => ({ sid: r.session, label: r.kind === "main" ? "Catch-up" : `Round ${i + 1}` }))
      .filter((r) => r.sid)
      .reverse();
    add("reviewer", rounds[0]?.sid || s.reviewer, rounds);
  } else if (STAGE_VIEW[stage][1]) add(STAGE_VIEW[stage][1], s[STAGE_VIEW[stage][1]]);
  return list;
}

// canView is whether the strip opens a stage: one the ticket is in or has been
// through, a skipped one too, for the document it stood in for.
export const canView = (t, stage) => INDEX[stage] <= Math.max(INDEX[t.stage], INDEX[t.reached] ?? -1);

// firstView is the stage a ticket opens on: its own, but a split one's scope,
// where its sub-tickets came from.
export const firstView = (t) => (t.split ? "scope" : t.stage);

// stageAgent is the role whose session a stage shows: the one at work, for
// the stage the ticket is in, else the one the stage belongs to.
export function stageAgent(t, stage) {
  return stage === t.stage && t.owner ? t.owner : STAGE_VIEW[stage][1];
}

// Related is how the ticket stands to others: its parent, its sub-tickets,
// what it waits for and what waits for it, each a way to that ticket.
// onWaits, given, changes what it waits for: one more from a picker, or one
// taken off; it may wait for any number.
export function Related({ t, byId, onOpen, onWaits }) {
  const kids = t.children || [];
  const all = Object.values(byId);
  const after = t.after || [];
  const blocks = all.filter((x) => x.after?.includes(t.id)).map((x) => x.id);
  const followUps = all.filter((x) => x.from === t.id).map((x) => x.id);
  const groups = [
    t.parent && ["Part of", [t.parent]],
    t.from && ["Came from", [t.from]],
    kids.length && ["Sub-tickets", kids],
    followUps.length && ["Follow-ups", followUps],
    after.length && ["Waits for", after],
    blocks.length && ["Holds up", blocks],
  ].filter(Boolean);
  // What it could wait for: open tickets, not itself or one already waited for.
  const choices = onWaits
    ? all
        .filter((x) => x.id !== t.id && !x.archived && x.stage !== "merged" && !after.includes(x.id))
        .map((x) => ({ id: x.id, label: `${x.id} · ${x.title}`, tag: LABEL[x.stage] }))
    : [];
  if (!groups.length && !choices.length) return null;
  return (
    <div className="board-related">
      {groups.map(([label, ids]) => (
        <span key={label} className="board-related-group">
          <span className="board-muted">{label}</span>
          {ids.map((id) => (
            <span key={id} className="board-related-item">
              <button className="board-related-link" title={byId[id]?.title} onClick={() => onOpen(id)}>
                <span className="board-id">{id}</span>
                <span className="board-related-title">{byId[id]?.title}</span>
                {byId[id] && <span className="board-muted">· {LABEL[byId[id].stage]}</span>}
              </button>
              {label === "Waits for" && onWaits && (
                <button className="ghost board-related-x" title={`Stop waiting for ${id}`} onClick={() => onWaits(after.filter((a) => a !== id))}>
                  <IconX size={10} />
                </button>
              )}
            </span>
          ))}
        </span>
      ))}
      {choices.length > 0 && (
        <Picker
          button="mini board-waits-add"
          label={
            <>
              <IconPlus size={11} />
              Waits for
            </>
          }
          title="A ticket this one waits for: it stays in the Inbox until that's merged, unless you start it anyway"
          choices={choices}
          value=""
          down
          search="Find a ticket by its ID or title"
          onPick={(id) => onWaits([...after, id])}
        />
      )}
    </div>
  );
}

const NO_PANES = {};

// InboxEdit is an Inbox ticket's title and description, changed in place.
function InboxEdit({ draft, onChange, onSave, onCancel, rows = 10 }) {
  const save = () => draft.title.trim() && onSave();
  return (
    <div className="board-doc board-edit">
      <label className="board-field">
        <span>Title</span>
        <input autoFocus value={draft.title} onChange={(e) => onChange({ ...draft, title: e.target.value })} onKeyDown={(e) => e.key === "Enter" && save()} />
      </label>
      <label className="board-field">
        <span>Description</span>
        <textarea
          rows={rows}
          value={draft.request}
          onChange={(e) => onChange({ ...draft, request: e.target.value })}
          onKeyDown={(e) => (e.metaKey || e.ctrlKey) && e.key === "Enter" && save()}
        />
      </label>
      <div className="board-edit-foot">
        <button className="btn" onClick={onCancel}>
          Cancel
        </button>
        <button className="primary" disabled={!draft.title.trim()} onClick={save} title={`Save (${isMac ? "⌘" : "Ctrl"}+Enter)`}>
          Save
        </button>
      </div>
    </div>
  );
}

// PendingCode is how a ticket's changes tell it of line comments not yet sent
// to the builder, tell({ count, send }) or tell(null), and that some were
// sent, sent().
const PendingCode = createContext(null);

function TicketModal({ id, summary, byId, known, drives, models, session, onOpenTicket, onOpenFile, onSymbol, onOpenCode, onLooking, onClose, onMove, act }) {
  const data = useTicket(id, summary);
  const t = data.ticket;
  // The strip is the way round a ticket: a stage shows its document and its
  // agent together. An earlier round's reviewer is picked from the review.
  const [view, setView] = useState(firstView(t));
  const [chosen, setChosen] = useState(null); // a session picked within the stage
  // The agents fold to a strip, giving the page the room: by default while
  // the finished code is read or no agent has worked the stage, else as the
  // reader folded them on this ticket, kept by stage; a stage not folded
  // either way opens them as its agent starts. Open whatever while one asks.
  const [panes, setPanes] = usePref("repo", `board:pane:${id}`, NO_PANES);
  const [codePending, setCodePending] = useState(null);
  const [editing, setEditing] = useState(null); // an Inbox ticket's { title, request } being changed
  const [full, setFull] = usePref("user", "board:fullTicket", false);
  // Comments sent, the builder is shown taking them up.
  const toBuilder = () => {
    setView("build");
    setChosen(t.sessions?.builder || null);
    setPanes((p) => ({ ...p, build: true }));
  };
  const pendingCtx = { tell: setCodePending, sent: toBuilder };
  // An agent waiting on the reader, of whichever stage, is put in front of them.
  useEffect(() => {
    if (!t.askingSession) return;
    setView(t.askingStage);
    setChosen(t.askingSession);
  }, [t.askingSession, t.askingStage]);
  const shown = canView(t, view) ? view : t.stage;
  const agents = stageAgents(t, shown);
  const open = !!t.askingSession || (panes[shown] ?? !(shown === "review" || shown === "merged" || !agents.length));
  const setPaneOpen = (on) => setPanes((p) => ({ ...p, [shown]: on }));
  const choose = (sid) => {
    setChosen(sid);
    setPaneOpen(true);
  };
  // A part's question asked of its parent's scoper, while that scoper asks the reader.
  const earlier = (t.askingSession && t.scopeQuestion && t.parent ? [{ sid: t.askingSession, label: `${t.parent}'s scoper` }] : []).filter(
    (r) => !agents.some((a) => a.sid === r.sid),
  );
  // An earlier round's reviewer is shown under the reviewer's tab.
  const tabOf = (sid) => agents.find((a) => a.sid === sid || a.rounds?.some((r) => r.sid === sid));
  const picked = chosen && (tabOf(chosen) ? { ...tabOf(chosen), sid: chosen } : earlier.find((a) => a.sid === chosen));
  const current = picked || agents.find((a) => a.role === stageAgent(t, shown)) || agents[0];
  const sid = current?.sid;
  const inbox = t.stage === "inbox";
  // What the session shown here says is seen here, not raised as a notice.
  useEffect(() => {
    onLooking?.((open && sid) || null);
    return () => onLooking?.(null);
  }, [sid, open, onLooking]);
  const pick = (stage) => {
    setView(stage);
    setChosen(null);
  };

  return (
    <Modal onClose={onClose} wide={!inbox} className={cx("board-ticket", inbox && "narrow", !inbox && full && "full")}>
      {/* A path named in the ticket - its documents, its diff, what its agents say - is
          the worktree's, not this checkout's; caught first, so it opens once, there. */}
      <div
        style={{ display: "contents" }}
        onClickCapture={(e) => {
          const a = e.target.closest?.("a.path-link, a[data-path]");
          const at = worktreeAt(t);
          if (!a?.dataset.path || !at || !onOpenFile) return;
          e.preventDefault();
          e.stopPropagation();
          const p = a.dataset.path;
          onOpenFile(p.startsWith(".dv/") ? p : at + p, Number(a.dataset.line) || 0);
        }}
      >
      <div className="board-ticket-head">
        <span className="board-id">{t.id}</span>
        <span className="board-ticket-title">{t.title}</span>
        <PRBadge t={t} />
        {t.branch && <span className="board-branch">{t.branch}</span>}
        <span className="spacer" />
        {/* The Inbox holds your own words until it moves on; past it, the agents' ticket is changed by commenting. */}
        {drives && inbox && !t.archived && !editing && (
          <button className="btn" title="Change its title or description" onClick={() => setEditing({ title: t.title, request: t.request || "" })}>
            Edit
          </button>
        )}
        {drives && (
          <button
            className="btn"
            title={t.archived ? "Put it back on the board where it was" : "Put it out of the board's way; its agents stop, its worktree stays"}
            onClick={() => act(api.patchTicket(t.id, { archived: !t.archived }))}
          >
            {t.archived ? "Restore" : "Archive"}
          </button>
        )}
        {!inbox && (
          <button className="ghost" title={full ? "Back to a dialog over the board" : "Fill the window"} aria-pressed={full} onClick={() => setFull(!full)}>
            {full ? <IconShrink size={13} /> : <IconFullScreen size={13} />}
          </button>
        )}
        {drives && (
          <button
            className="ghost"
            title="Delete the ticket, its worktree and its branch"
            onClick={() => confirm(`Delete ${t.id}? Its worktree and branch go too.`) && (onClose(), act(api.deleteTicket(t.id)))}
          >
            <IconTrash size={13} />
          </button>
        )}
      </div>
      <div className="board-ticket-labels">
        {drives && <PriorityPick t={t} act={act} />}
        <LabelEditor labels={t.labels} known={known} onChange={(labels) => act(api.patchTicket(t.id, { labels }))} />
        <Related t={t} byId={byId} onOpen={onOpenTicket} onWaits={drives && !t.archived ? (after) => act(api.patchTicket(t.id, { after })) : undefined} />
      </div>
      <NothingNote t={t} />
      <Suggested t={t} drives={drives} act={act} />
      <Steps t={t} shown={shown} onPick={pick} />
      <div className={cx("board-split", inbox && "single", !inbox && !open && "folded")}>
        <div className="board-doc-pane">
          <div className="board-doc-body">
            {editing ? (
              <InboxEdit
                draft={editing}
                onChange={setEditing}
                onCancel={() => setEditing(null)}
                onSave={() => act(api.patchTicket(t.id, { title: editing.title.trim(), request: editing.request })).then(() => setEditing(null))}
              />
            ) : (
            <PendingCode.Provider value={pendingCtx}>
              <Doc t={t} remote={data.remote} docs={data.docs} versions={data.versions} mockups={data.mockups} parent={data.parent} tab={STAGE_VIEW[shown][0]} models={models} drives={drives} act={act} onSession={choose} onOpenFile={onOpenFile} onSymbol={onSymbol} onOpenCode={onOpenCode} changes={data.changes} commits={data.commits} />
            </PendingCode.Provider>
            )}
          </div>
        </div>
        {!inbox && !open && (
          <div className="board-agent-rail">
            <button className="ghost board-rail-open" title="Show the agents" onClick={() => setPaneOpen(true)}>
              <IconPanelOpen size={14} />
            </button>
            {(agents.length ? agents : [{ sid: "", label: "No agent" }]).map((a) => (
              <button key={a.sid || "none"} className="board-rail-agent" title={a.sid ? `Talk to the ${a.label.toLowerCase()}` : "No agent worked on this stage"} disabled={!a.sid} onClick={() => choose(a.sid)}>
                {a.role === t.owner && shown === t.stage && t.working && <span className="board-spin" />}
                <span>{a.label}</span>
              </button>
            ))}
          </div>
        )}
        {!inbox && open && (
          <div className="board-session-pane">
            <div className="board-pane-head">
              {/* At the edge between the panes, which the agents fold away from. */}
              {!t.askingSession && (
                <button className="ghost board-pane-fold" title="Fold the agents to the side, giving the page the room" onClick={() => setPaneOpen(false)}>
                  <IconPanelFold size={14} />
                </button>
              )}
              {/* A stage two agents work in names both, to switch between. */}
              {agents.length > 1 && !earlier.includes(current) ? (
                <span className="board-agents">
                  {agents.map((a) => {
                    const on = a.role === current?.role;
                    const spin = a.role === t.owner && shown === t.stage && t.working && <span className="board-spin" />;
                    // A reviewer of several rounds, shown, is its own picker of them.
                    return on && a.rounds?.length > 1 ? (
                      <Picker
                        key={a.sid}
                        button="btn on"
                        down
                        label={
                          <>
                            {spin}
                            {a.label} · {a.rounds.find((r) => r.sid === sid)?.label}
                          </>
                        }
                        title={`Each round's ${a.label.toLowerCase()}, the latest first`}
                        choices={a.rounds.map((r, i) => ({ id: r.sid, label: r.label, tag: i === 0 ? "latest" : "" }))}
                        value={sid}
                        onPick={setChosen}
                      />
                    ) : (
                      <button key={a.sid} className={cx("btn", on && "on")} onClick={() => setChosen(a.sid)}>
                        {spin}
                        {a.label}
                      </button>
                    );
                  })}
                </span>
              ) : (
                <>
                  {current && current.role === t.owner && shown === t.stage && t.working && <span className="board-spin" />}
                  <span className="board-pane-title">{current?.label || "No agent"}</span>
                </>
              )}
              {earlier.includes(current) && (
                <button className="btn" onClick={() => setChosen(null)}>
                  Back
                </button>
              )}
            </div>
            <div className="board-session">
              {sid ? session(sid) : <div className="board-empty">{shown === t.stage ? "No agent has started on this stage yet." : "No agent worked on this stage."}</div>}
            </div>
          </div>
        )}
      </div>
      <Foot t={t} remote={data.remote} drives={drives} models={models} onMove={onMove} act={act} codePending={codePending} onSent={toBuilder} />
      </div>
    </Modal>
  );
}

// Steps is the stage strip: how far the ticket is, and which stage is shown.
export function Steps({ t, shown, onPick }) {
  const now = INDEX[t.stage];
  const reached = INDEX[t.reached] ?? -1;
  // The stages questions wait on: a planner can ask while answering the builder.
  const held = (k) => (k === "build" && !!t.question) || (k === "plan" && t.stage === "scope" && !!t.scopeQuestion);
  return (
    <div className="board-steps">
      {STAGES.map(([k, l], i) => {
        // Skipped by where the ticket started, unless it came back to it, as
        // a builder's question takes it to Plan.
        const skip = i > 0 && i < INDEX[t.started] && i !== now && !t.sessions?.[STAGE_VIEW[k][1]];
        const cls = skip ? "skip" : i === now ? "now" : held(k) ? "held" : i < now || i <= reached ? "past" : "";
        const open = canView(t, k);
        return (
          <button
            key={k}
            className={cx("board-step", cls, k === shown && "shown")}
            disabled={!open}
            aria-pressed={k === shown}
            onClick={() => onPick(k)}
            title={open ? `${l}: what it ${skip ? "had" : "wrote"}, and its agent` : `Not reached yet`}
          >
            <span>
              {cls === "past" && <IconCheck size={10} />}
              {l}
            </span>
          </button>
        );
      })}
    </div>
  );
}

function Markdown({ text, onOpenFile }) {
  const lines = useMemo(() => text.split("\n"), [text]);
  return <MarkdownPreview lines={lines} path="ticket.md" onOpenFile={onOpenFile} />;
}

// lineDiff is how b's lines came from a's: rows of [kind, line], kind " " for
// kept, "-" removed and "+" added, by their longest common run of lines.
function lineDiff(a, b) {
  const x = a.split("\n");
  const y = b.split("\n");
  const lcs = Array.from({ length: x.length + 1 }, () => new Uint16Array(y.length + 1));
  for (let i = x.length - 1; i >= 0; i--) for (let j = y.length - 1; j >= 0; j--) lcs[i][j] = x[i] === y[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1]);
  const rows = [];
  let i = 0;
  let j = 0;
  while (i < x.length || j < y.length) {
    if (i < x.length && j < y.length && x[i] === y[j]) rows.push([" ", x[i++], j++]);
    else if (i < x.length && (j === y.length || lcs[i + 1][j] >= lcs[i][j + 1])) rows.push(["-", x[i++]]);
    else rows.push(["+", y[j++]]);
  }
  return rows;
}

const KEEP = 2; // kept lines shown round a change

function Changes({ from, to }) {
  const rows = useMemo(() => lineDiff(from, to), [from, to]);
  const near = rows.map((_, i) => rows.slice(Math.max(0, i - KEEP), i + KEEP + 1).some((r) => r[0] !== " "));
  const out = [];
  for (let i = 0; i < rows.length; i++) {
    if (near[i]) {
      out.push(
        <div key={i} className={cx("board-diff-line", rows[i][0] === "+" ? "add" : rows[i][0] === "-" ? "del" : "")}>
          {rows[i][1] || " "}
        </div>,
      );
      continue;
    }
    let n = 0;
    while (i + n < rows.length && !near[i + n]) n++;
    out.push(
      <div key={i} className="board-diff-gap">
        {n} unchanged line{n === 1 ? "" : "s"}
      </div>,
    );
    i += n - 1;
  }
  return <div className="board-diff">{rows.some((r) => r[0] !== " ") ? out : <p className="board-muted">No change.</p>}</div>;
}

// Versioned is a ticket's document with the texts it had before: stepped
// back through, each read whole or as what its revision changed. Only the
// current text takes comments.
function Versioned({ doc, text, versions: given, onOpenFile, children }) {
  const versions = given || [];
  const texts = [...versions.map((v) => v.text), text];
  const [at, setAt] = useState(null); // null is the current text
  const [changes, setChanges] = useState(false);
  const i = at === null || at >= texts.length - 1 ? texts.length - 1 : at;
  if (!versions.length) return children(text);
  const current = i === texts.length - 1;
  return (
    <>
      <div className="board-versions">
        <button className="btn" disabled={i === 0} onClick={() => setAt(i - 1)} title={`The ${doc} before`}>
          ‹
        </button>
        <span>
          Version {i + 1} of {texts.length}
          <span className="board-muted">{current ? " · current" : ` · replaced ${relTime(versions[i].replaced)}`}</span>
        </span>
        <button className="btn" disabled={current} onClick={() => setAt(i + 1 === texts.length - 1 ? null : i + 1)} title={`The ${doc} after`}>
          ›
        </button>
        {i > 0 && (
          <button className={cx("btn", changes && "on")} onClick={() => setChanges((c) => !c)} title={`What this version changed from the one before`}>
            Changes
          </button>
        )}
      </div>
      {changes && i > 0 ? <Changes from={texts[i - 1]} to={texts[i]} /> : current ? children(text) : <Markdown text={texts[i]} onOpenFile={onOpenFile} />}
    </>
  );
}

const AUTHOR = { ticket: ["scope", "scope", "scoper"], plan: ["planner", "plan", "planner"], pr: ["builder", "review", "builder"] };

// A plan's slices carry flags - its judgement calls, each a list item led by
// its kind - and what works after each; marked so they stand out from the
// prose. Again whenever the rendered document is redrawn.
const PLAN_FLAGS = { "You chose": "you", "Asks you": "ask", Choice: "choice", "Not in the ticket": "new", Assumes: "assume", Untested: "untested" };
const FLAG_LEAD = new RegExp(`^(${Object.keys(PLAN_FLAGS).join("|")}):`);

// markWorksLine marks "Works after this" written on the line after a slice's
// text, which Markdown runs into the same paragraph: from there to the end.
function markWorksLine(p) {
  const walk = document.createTreeWalker(p, NodeFilter.SHOW_TEXT);
  for (let n = walk.nextNode(); n; n = walk.nextNode()) {
    const i = n.data.search(/\nWorks after( this)?:/);
    if (i < 0 || n.parentNode !== p) continue;
    const rest = n.splitText(i + 1);
    const span = document.createElement("span");
    span.className = "plan-works";
    p.insertBefore(span, rest);
    while (span.nextSibling) span.append(span.nextSibling);
    return;
  }
}

function usePlanMarks(ref, on) {
  useEffect(() => {
    const root = ref.current;
    if (!on || !root) return;
    const mark = () => {
      for (const li of root.querySelectorAll(".md-preview li")) {
        const m = FLAG_LEAD.exec(li.textContent.trim());
        if (m && li.dataset.flag !== PLAN_FLAGS[m[1]]) li.dataset.flag = PLAN_FLAGS[m[1]];
      }
      for (const p of root.querySelectorAll(".md-preview p")) {
        if ("works" in p.dataset || p.querySelector(".plan-works")) continue;
        if (/^Works after( this)?:/.test(p.textContent)) p.dataset.works = "";
        else markWorksLine(p);
      }
    };
    mark();
    const mo = new MutationObserver(mark);
    mo.observe(root, { childList: true, subtree: true });
    return () => mo.disconnect();
  }, [ref, on]);
}
const DOC_NAME = { pr: "description" };

// Commentable is a ticket's document the reader can point at, as a file in
// review is: selecting some of it opens a comment box under its block, where
// the comment then stays; or a comment on the whole. They gather until sent
// together to the agent writing it, which revises it and hands it back.
function Commentable({ t, doc, text, drives, act, onOpenFile }) {
  const [role, stage, who] = AUTHOR[doc];
  const can = drives && !t.archived && t.stage === stage && !!t.sessions?.[role];
  const lines = useMemo(() => text.split("\n"), [text]);
  // Kept on disk until sent, so a reload or a restart of dv loses none.
  const [saved, setSaved] = usePref("repo", `board:comments:${t.id}:${doc}`, null);
  const pending = saved?.pending || []; // { key, end, quote, text }, end 0 for the whole
  const draft = saved?.draft || null; // { quote, text }, a comment on the whole
  const setPending = (f) => setSaved((s) => ({ ...s, pending: f(s?.pending || []) }));
  const setDraft = (d) => setSaved((s) => ({ ...s, draft: d }));
  const [composing, setComposing] = useState(null);
  const startComment = useCallback(
    (side, start, end, selected, excerpt) => setComposing({ side, start, end, quote: lines.slice(start - 1, end), selected, excerpt }),
    [lines],
  );
  const keep = (c) => setPending((p) => [...p, { key: String(Date.now() + Math.random()), ...c }]);
  const remove = (c) => setPending((p) => p.filter((x) => x.key !== c.key));
  const card = (c) => (
    <div className="board-comment">
      {c.quote && <blockquote>{c.quote}</blockquote>}
      <p>{c.text}</p>
      <button className="board-comment-x" title="Take this comment back" onClick={() => remove(c)}>
        ×
      </button>
    </div>
  );
  const notes = pending.filter((c) => c.end).map((c) => ({ key: c.key, line: c.end, node: card(c) }));
  const add = () => {
    if (draft.text.trim()) keep({ end: 0, quote: draft.quote, text: draft.text });
    setDraft(null);
  };
  const send = () => act(api.commentTicket(t.id, doc, pending.map(({ quote, text }) => ({ quote, text }))).then(() => setSaved(null)));
  const rootRef = useRef(null);
  usePlanMarks(rootRef, doc === "plan");
  return (
    <div className={cx("board-commentable", doc === "plan" && "plan-doc")} ref={rootRef}>
      <MarkdownDocument
        lines={lines}
        path={`${doc}.md`}
        onOpenFile={onOpenFile}
        composing={can ? composing : null}
        setComposing={setComposing}
        onStartComment={can ? startComment : undefined}
        onComment={async ({ endLine, quote, excerpt, body }) => keep({ end: endLine, quote: (excerpt || quote?.join("\n") || "").trim(), text: body })}
        notes={notes}
      />
      {can && (
        <div className="board-comments">
          {pending.filter((c) => !c.end).map((c) => (
            <div key={c.key}>{card(c)}</div>
          ))}
          {draft ? (
            <div className="board-comment draft">
              {draft.quote && <blockquote>{draft.quote}</blockquote>}
              <textarea
                autoFocus
                rows={3}
                value={draft.text}
                placeholder={`What should the ${who} change?`}
                onChange={(e) => setDraft({ ...draft, text: e.target.value })}
                onKeyDown={(e) => (e.key === "Enter" && (e.metaKey || e.ctrlKey) ? add() : e.key === "Escape" && (e.stopPropagation(), setDraft(null)))}
              />
              <div className="board-comment-acts">
                <button className="btn" onClick={() => setDraft(null)}>
                  Cancel
                </button>
                <button className="btn" disabled={!draft.text.trim()} onClick={add}>
                  Add comment
                </button>
              </div>
            </div>
          ) : (
            <div className="board-comment-acts">
              <span className="board-muted">Select text to comment on it, or</span>
              <button className="btn" onClick={() => setDraft({ quote: "", text: "" })}>
                comment on the whole {DOC_NAME[doc] || doc}
              </button>
              <span className="spacer" />
              {pending.length > 0 && (
                <button className="primary" onClick={send}>
                  Send {pending.length} comment{pending.length === 1 ? "" : "s"} to the {who}
                </button>
              )}
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// onOpenFile opens a path the document names in the ticket's worktree, which
// has the code it was written about, whatever the main checkout has.
// Asked is the questions the builder put to the planner, or the planner to the
// scoper: the one waiting, and those answered, which the document took in.
const ASKED = {
  builder: { open: "question", from: "Build", waits: "the build waits", during: "build", answerer: "planner" },
  planner: { open: "scopeQuestion", from: "Plan", waits: "the plan waits", during: "planning", answerer: "scoper" },
};

function Asked({ t, asker }) {
  const a = ASKED[asker];
  const open = t[a.open];
  return (
    <>
      {open && (
        <div className="board-box open">
          <div className="board-box-head">
            Question from {a.from} · {a.waits}
          </div>
          <p>{open.text}</p>
        </div>
      )}
      {(t.answered || [])
        .filter((q) => (q.asker || "builder") === asker)
        .map((q, i) => (
          <div key={i} className="board-box">
            <div className="board-box-head">
              Added during {a.during} · {q.by === "you" ? "answered by you" : `answered by the ${a.answerer} from ${q.source}`}
            </div>
            <p>
              <b>The {asker} asked:</b> {q.text}
            </p>
            <p>
              <b>Answer:</b> {q.answer}
            </p>
          </div>
        ))}
    </>
  );
}

// OriginalRequest is the reader's words the ticket began from. Once a ticket
// is written from them, a long one keeps to its first lines until asked.
function OriginalRequest({ text, long, onOpenFile }) {
  const [all, setAll] = useState(false);
  const folds = long && text.split("\n").length + text.length / 100 > 6;
  return (
    <>
      <h4>Original request</h4>
      <div className={cx("board-orig", folds && !all && "folded")}>
        <Markdown text={text} onOpenFile={onOpenFile} />
      </div>
      {folds && (
        <button className="link board-orig-more" onClick={() => setAll((a) => !a)}>
          {all ? "Show less" : "Show all"}
        </button>
      )}
    </>
  );
}

// NothingNote is an agent's finding that the ticket needs no change, with its
// evidence: for the reader to close it on, and after, why it closed.
export function NothingNote({ t }) {
  if (!t.nothing) return null;
  return (
    <div className="board-box board-nothing">
      <div className="board-box-head board-review-head">
        <span className="board-nothing-head">{t.stage === "merged" ? "Closed with no change" : "Nothing to do"}</span>
        <span className="board-muted">from the {ROLE_NAME[t.nothing.by]?.toLowerCase() || t.nothing.by}</span>
      </div>
      <div className="board-nothing-why">{t.nothing.why}</div>
    </div>
  );
}

// Suggested is the follow-ups the ticket's agents offered, each to read, file
// to the Inbox or dismiss.
export function Suggested({ t, drives, act }) {
  const [open, setOpen] = useState(null);
  const [editing, setEditing] = useState(null); // { id, title, request }
  const list = t.suggestions || [];
  if (!list.length) return null;
  return (
    <div className="board-box board-suggested">
      {list.map((s) => (
        <div key={s.id}>
          <div className="board-box-head board-review-head">
            <button className="board-fold" aria-expanded={open === s.id} onClick={() => setOpen(open === s.id ? null : s.id)}>
              {open === s.id ? <IconChevronDown size={10} /> : <IconChevron size={10} />}
              Suggested follow-up · {s.title}
            </button>
            <span className="board-muted">from the {ROLE_NAME[s.by]?.toLowerCase() || s.by}</span>
            <span className="spacer" />
            {drives && editing?.id !== s.id && (
              <>
                <button
                  className="btn"
                  title="Change its title and request before filing it"
                  onClick={() => (setEditing({ id: s.id, title: s.title, request: s.request || "" }), setOpen(s.id))}
                >
                  Edit
                </button>
                <button className="btn" onClick={() => act(api.suggestion(t.id, s.id, false))}>
                  Dismiss
                </button>
                <button className="btn" title="Put it in the Inbox as a follow-up of this ticket" onClick={() => act(api.suggestion(t.id, s.id, true))}>
                  File
                </button>
              </>
            )}
          </div>
          {editing?.id === s.id ? (
            <InboxEdit
              draft={editing}
              rows={5}
              onChange={setEditing}
              onCancel={() => setEditing(null)}
              onSave={() => act(api.editSuggestion(t.id, s.id, editing.title.trim(), editing.request).then(() => setEditing(null)))}
            />
          ) : (
            open === s.id && <Markdown text={s.request} />
          )}
        </div>
      ))}
    </div>
  );
}

// Background is the ticket a sub-ticket is part of, which holds the problem,
// edge cases and decisions the part doesn't repeat; folded, as it is read once.
function Background({ parent, onOpenFile }) {
  const [open, setOpen] = useState(false);
  if (!parent) return null;
  return (
    <div className="board-box">
      <div className="board-box-head board-review-head">
        <button className="board-fold" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
          {open ? <IconChevronDown size={10} /> : <IconChevron size={10} />}
          Background · from {parent.id}, which this is part of
        </button>
      </div>
      {open && <Markdown text={parent.ticket || ""} onOpenFile={onOpenFile} />}
    </div>
  );
}

// Mockups are the pages made to show how the ticket's UI should look, live
// here and full size on asking.
function Mockups({ paths }) {
  if (!paths?.length) return null;
  return (
    <div className="board-mockups">
      <h4>Mock-ups</h4>
      {paths.map((p) => (
        <Shown key={p} file={{ path: p, title: p.slice(p.lastIndexOf("/") + 1, -".html".length), rest: "" }} root="" />
      ))}
    </div>
  );
}

export function Doc({ t, remote, docs, versions, mockups, parent, changes, commits, tab, models, drives, act, onSession, onOpenFile: open, onSymbol: lookUp, onOpenCode }) {
  const at = worktreeAt(t);
  const onOpenFile = open && ((path, line) => open(at + path, line));
  // Cmd/Ctrl+click on a name in the changes finds it in the worktree.
  const onSymbol = lookUp && at && ((name, path) => lookUp(name, at + path));
  if (tab === "ticket") {
    const take = (files) =>
      readImages(files).then((got) => act(Promise.all(got.map((img) => api.addTicketImage(t.id, { mediaType: img.mediaType, data: img.data })))));
    return (
      <div
        className="board-doc"
        // Focusable, so a paste after clicking in it comes here.
        tabIndex={-1}
        onPaste={(e) => {
          const files = drives ? pasted(e) : [];
          if (!files.length || e.target.closest("input, textarea")) return;
          e.preventDefault();
          take(files);
        }}
        onDragOver={(e) => drives && e.preventDefault()}
        onDrop={(e) => {
          const files = drives ? dropped(e) : [];
          if (!files.length) return;
          e.preventDefault();
          take(files);
        }}
      >
        {/* A part's text is its ticket, written by the scoper, not a request in the reader's words. */}
        {t.request && !t.parent && <OriginalRequest text={t.request} long={!!docs?.ticket} onOpenFile={onOpenFile} />}
        <TicketImages t={t} drives={drives} act={act} />
        <Asked t={t} asker="planner" />
        <Background parent={parent} onOpenFile={onOpenFile} />
        <CommentLog t={t} doc="ticket" drives={drives} act={act} />
        {docs?.ticket ? (
          <Versioned doc="ticket" text={docs.ticket} versions={versions?.ticket} onOpenFile={onOpenFile}>
            {(text) => (
              <Commentable t={t} doc="ticket" text={text} drives={drives} act={act} onOpenFile={onOpenFile} />
            )}
          </Versioned>
        ) : (
          <p className="board-muted">{t.stage === "inbox" ? "Scope turns this into the full ticket: the problem, what's out, edge cases, and how you'll know it's done." : "The scoper hasn't written the ticket yet."}</p>
        )}
        {docs?.ticket && <Mockups paths={mockups} />}
      </div>
    );
  }
  if (tab === "plan") {
    return (
      <div className="board-doc">
        <PlanReview t={t} models={models} drives={drives} act={act} onSession={onSession} />
        <Asked t={t} asker="builder" />
        {/* A part's question waits in Plan, the stage on screen. */}
        {t.stage === "plan" && t.scopeQuestion && <Asked t={{ ...t, answered: [] }} asker="planner" />}
        <CommentLog t={t} doc="plan" drives={drives} act={act} />
        {docs?.plan ? (
          <Versioned doc="plan" text={docs.plan} versions={versions?.plan} onOpenFile={onOpenFile}>
            {(text) => (
              <Commentable t={t} doc="plan" text={text} drives={drives} act={act} onOpenFile={onOpenFile} />
            )}
          </Versioned>
        ) : (
          <p className="board-muted">The planner hasn't written the plan yet.</p>
        )}
        {/* A ticket begun at Plan has its interface, and so its mock-ups, from the planner. */}
        {!docs?.ticket && <Mockups paths={mockups} />}
      </div>
    );
  }
  return <CodeTab t={t} remote={remote} docs={docs} versions={versions} changes={changes} commits={commits} models={models} drives={drives} act={act} onSession={onSession} onOpenFile={onOpenFile} onSymbol={onSymbol} onOpenCode={onOpenCode} />;
}

// CodeTab is Build and Your review: the agent review, the pull request, the
// reviewer's walkthrough of the branch and the branch's changes, which the
// step walked to is marked and scrolled to in.
function CodeTab({ t, remote, docs, versions, changes, commits, models, drives, act, onSession, onOpenFile, onSymbol, onOpenCode }) {
  const guide = useMemo(() => parseGuide(t, docs?.guide), [t.guideRound, t.rounds?.length, docs?.guide]);
  const [step, setStep] = useState(null);
  const [returned, setReturned] = useState(0); // asks the changes back to the step
  const focus = guide && step != null ? guide.steps[step] : null;
  return (
    <div className="board-doc">
      <Review t={t} models={models} drives={drives} act={act} onSession={onSession} />
      {remote && <PullRequest t={t} text={docs?.pr || ""} versions={versions?.pr} drives={drives} act={act} onOpenFile={onOpenFile} />}
      {guide && <Guide t={t} guide={guide} step={step} onStep={setStep} onCode={onOpenCode && changes ? () => onOpenCode(changes, t.sessions?.builder, step, t.id) : undefined} />}
      {guide && step != null && <GuideStep t={t} guide={guide} step={step} onStep={setStep} onOpenFile={onOpenFile} onReturn={() => setReturned((n) => n + 1)} />}
      <CodeChanges t={t} range={changes} commits={commits} focus={focus} returned={returned} drives={drives} act={act} onOpenFile={onOpenFile} onSymbol={onSymbol} onOpenCode={onOpenCode} />
    </div>
  );
}

// parseGuide is the ticket's walkthrough while it is the current round's, and
// in the shape walked: one written as stories before has none.
export const parseGuide = (t, text) => {
  try {
    const g = t.guideRound && t.guideRound === t.rounds?.length && text ? JSON.parse(text) : null;
    return g?.steps?.length ? g : null;
  } catch {
    return null;
  }
};

// stepPlaces is a step's place, by file, as the diff marks it: its lines, by
// the new side, and in old the lines deleted among them, with those it
// replaced at its start, given fd, the file's diff.
export function stepPlaces(step, fd) {
  if (!step) return null;
  const old = [];
  const lines = fd?.ops ? flatten(fd.ops) : [];
  const end = Math.max(step.start, step.end || step.start);
  let i = lines.findIndex((l) => l.n >= 0 && l.n + 1 >= step.start);
  if (i >= 0) {
    while (i > 0 && lines[i - 1].t === "d") i--;
    for (; i < lines.length && !(lines[i].n >= 0 && lines[i].n + 1 > end); i++) if (lines[i].t === "d") old.push(lines[i].o + 1);
  }
  return new Map([[step.file, [{ ...step, old }]]]);
}

// stepAim is where a step is scrolled to in the diff root: its first line
// just under what is pinned over the diff (the strip, the step bar, the
// file's head), or the whole step centred in the room left, if it fits.
export function stepAim(root, section, step) {
  const first = section.querySelector(`[data-line="${step.start}"][data-side="new"]`);
  if (!laidOut(first)) return null;
  const last = section.querySelector(`[data-line="${step.end || step.start}"][data-side="new"]`);
  const css = getComputedStyle(root);
  const px = (v) => parseFloat(css.getPropertyValue(v)) || 0;
  const over = px("--inset") + px("--pin") + px("--file-head-h") + 8;
  const room = root.clientHeight - over;
  const span = laidOut(last) ? last.getBoundingClientRect().bottom - first.getBoundingClientRect().top : Infinity;
  return yOf(root, first, over + (span < room - 32 ? (room - span) / 2 : 16));
}

// usePinBar is a ref for a step bar pinned over a diff, which gives the
// diff's pane its height as --pin, for the files' heads to pin below it.
export function usePinBar() {
  const pinned = useRef(null);
  return useCallback((el) => {
    const was = pinned.current;
    was?.ro.disconnect();
    was?.on.style.removeProperty("--pin");
    pinned.current = null;
    const on = el?.closest(".content, .board-doc-body");
    if (!on) return;
    const ro = new ResizeObserver(() => on.style.setProperty("--pin", `${el.offsetHeight}px`));
    ro.observe(el);
    pinned.current = { ro, on };
  }, []);
}

// useStepScroll brings the step, in the diff under within, into view.
// Chased, not scrolled to once: the rows passed on the way are drawn as they
// come near and change height, which stopped a smooth scroll part-way, in
// another file.
function useStepScroll(places, within, ready, again) {
  useEffect(() => {
    const [file, looks] = places ? [...places][0] : [];
    if (!file) return;
    const sectionOf = () => document.querySelector(`${within} .file[data-path="${CSS.escape(file)}"]`);
    let root = sectionOf()?.parentElement;
    while (root && !/(auto|scroll)/.test(getComputedStyle(root).overflowY)) root = root.parentElement;
    if (!root) return;
    return chase(root, () => {
      const s = sectionOf();
      if (!s) return null;
      const y = stepAim(root, s, looks[0]);
      return y != null ? { y, final: true } : { y: yOf(root, s, 0), final: false };
    });
  }, [places, within, ready, again]);
}

// useGuideMarks marks, within root (the page if none), the lines a step's
// places cover, deleted ones too, as the diff draws them: a stripe beside
// them, which leaves the shades saying added and deleted to be read.
// As a stylesheet of the lines, not classes on them: a diff redrawing its rows
// would drop a class, never a rule. id keeps the ticket's and the Code view's
// apart; within scopes the rule to that diff.
export function useGuideMarks(id, places, within = "") {
  useEffect(() => {
    if (!places) return;
    const rules = [];
    for (const [file, looks] of places) {
      const at = (side, n) => `${within} .file[data-path="${CSS.escape(file)}"] .cell[data-side="${side}"][data-line="${n}"] > .code`;
      for (const l of looks) {
        const end = Math.min(Math.max(l.start, l.end || l.start), l.start + 400);
        for (let n = l.start; n <= end; n++) rules.push(at("new", n));
        for (const n of (l.old || []).slice(0, 400)) rules.push(at("old", n));
      }
    }
    const el = document.createElement("style");
    el.id = id;
    // Drawn in the gutter's padding, not over the code: a wrapped line starts at its edge.
    el.textContent = `${rules.join(",\n")} { border-left: 3px solid var(--accent); margin-left: -3px; }`;
    document.head.appendChild(el);
    return () => el.remove();
  }, [id, places, within]);
}

// useTicketGuide is the ticket a diff is the branch of, as "Open in Code view"
// sets it, or the pull request of, with its reviewer's guide while that is
// current.
// It is { t, guide, commits, changes }: the walkthrough null if it has none
// current, its commits newest first, and its branch's whole range. A scope set
// on a stretch of its commits names the ticket itself, as `ticket`.
export function useTicketGuide(scope) {
  const id = scope?.ticket || "";
  const branch = !id && scope?.kind === "custom" && scope.rev.includes("...") ? scope.rev.slice(scope.rev.indexOf("...") + 3) : "";
  const pr = !id && scope?.kind === "pr" ? Number(scope.rev) : 0;
  const [found, setFound] = useState(null);
  useEffect(() => {
    setFound(null);
    if (!id && !branch && !pr) return;
    let live = true;
    (id ? api.ticket(id) : api.board().then((b) => {
      const t = b.tickets.find((x) => (branch ? x.branch === branch : x.pr === pr));
      return t && api.ticket(t.id);
    }))
      .then((d) => {
        if (!live || !d?.ticket) return;
        setFound({ t: d.ticket, guide: parseGuide(d.ticket, d.docs?.guide), commits: d.commits || [], changes: d.changes });
      })
      .catch(() => {});
    return () => {
      live = false;
    };
  }, [id, branch, pr]);
  return found;
}

// The walkthrough's parts, in the order walked.
const GUIDE_PARTS = { users: "What users see", internals: "How it works", tests: "What the tests guarantee" };

// useSeen is the walkthrough's steps the reader has been to, kept with the
// ticket so a walk picks up where it stopped, here or in the Code view.
const useSeen = (t) => usePref("repo", `board:walk:${t.id}:${t.guideRound}`, []);
// useWalkAt is the step the reader was last on, which Resume goes back to;
// null once the walk is finished.
const useWalkAt = (t) => usePref("repo", `board:walk-at:${t.id}:${t.guideRound}`, null);

// Guide is the reviewer's walkthrough of the branch for the reader: its steps
// in order, under the part each belongs to, and what can be skimmed. A step
// picked is walked to; the ones been to are ticked. onCode takes the walk to
// the Code view, where the diff has the whole width.
export function Guide({ t, guide, step, onStep, onCode }) {
  const [seen] = useSeen(t);
  const [at] = useWalkAt(t);
  // Folded to its line: the list is for jumping, the step bar for walking.
  const [open, setOpen] = useState(false);
  const [skimOpen, setSkimOpen] = useState(false);
  const steps = guide.steps || [];
  const skim = guide.skim || [];
  const done = steps.filter((_, i) => seen.includes(i)).length;
  const left = at != null && at < steps.length;
  const resume = left ? at : Math.max(0, steps.findIndex((_, i) => !seen.includes(i)));
  const go = (i) => {
    onStep(i);
    setOpen(false);
  };
  return (
    <section className={cx("guide", !open && "folded")}>
      <header className="guide-head">
        <button className="guide-fold" aria-expanded={open} onClick={() => setOpen((o) => !o)} title={open ? "Fold the steps away" : "Every step, to jump to"}>
          {open ? <IconChevronDown size={10} /> : <IconChevron size={10} />}
          <span className="guide-title">Walkthrough</span>
        </button>
        <span className="guide-progress" title={`${done} of ${steps.length} steps read`}>
          <i style={{ width: `${(100 * done) / Math.max(1, steps.length)}%` }} />
        </span>
        <span className="guide-count">
          {done}/{steps.length}
        </span>
        <span className="spacer" />
        {onCode && (
          <button className="ghost guide-code" title="Walk through it in the Code view, side by side if it fits" onClick={onCode}>
            In Code view
          </button>
        )}
        {step == null && (
          <button className="btn" onClick={() => go(resume)} title={left ? `Back to step ${at + 1}, where you left off` : undefined}>
            {done === 0 ? "Start" : left || done < steps.length ? "Resume" : "Walk again"}
          </button>
        )}
      </header>
      {open && (
      <ol className="guide-steps">
        {steps.map((s, i) => (
          <Fragment key={i}>
            {s.part !== steps[i - 1]?.part && <li className="guide-part">{GUIDE_PARTS[s.part] || s.part}</li>}
            <li className={cx("guide-row", i === step && "on", seen.includes(i) && "done")}>
              <button onClick={() => go(i)}>
                <span className="guide-badge">{seen.includes(i) && i !== step ? <IconCheck size={11} /> : i + 1}</span>
                <span className="guide-row-title">{s.title}</span>
                <span className="guide-row-file">
                  {s.file.slice(s.file.lastIndexOf("/") + 1)}:{s.start}
                </span>
              </button>
            </li>
          </Fragment>
        ))}
      </ol>
      )}
      {open && skim.length > 0 && (
        <div className="guide-skim">
          <button className="guide-skim-head" aria-expanded={skimOpen} onClick={() => setSkimOpen((o) => !o)}>
            {skimOpen ? <IconChevronDown size={10} /> : <IconChevron size={10} />}
            Skim · {skim.length} file{skim.length === 1 ? "" : "s"}
          </button>
          {skimOpen && (
            <ul className="guide-places">
              {skim.map((k, i) => (
                <li key={i}>
                  <span className="guide-place quiet">{k.file}</span>
                  <span className="guide-check">{k.why}</span>
                </li>
              ))}
            </ul>
          )}
        </div>
      )}
    </section>
  );
}

// GuideStep is the step walked to, pinned over the changes while they are
// read: what happens there, what to check, or for a test what it guarantees
// and leaves unchecked; and the way on. onReturn brings the changes back to
// the step, scrolled away from; a step in a file the branch doesn't change
// isn't in them, and opens the file instead.
export function GuideStep({ t, guide, step, onStep, onOpenFile, onReturn }) {
  const [seen, setSeen] = useSeen(t);
  const [, setAt] = useWalkAt(t);
  const pin = usePinBar();
  const barRef = useRef(null);
  const steps = guide.steps || [];
  const s = steps[step];
  const last = step === steps.length - 1;
  const next = () => (last && setAt(null), onStep(last ? null : step + 1));
  const prev = () => step > 0 && onStep(step - 1);
  useEffect(() => {
    if (!s) return;
    if (!seen.includes(step)) setSeen((d) => [...d, step]);
    setAt(step);
  }, [step]);
  // Alt+→ and Alt+← walk on and back, the bar's Next and Back, unless
  // something is open over the bar or a box is being typed in.
  const keys = useRef();
  keys.current = { next, prev };
  useEffect(() => {
    const onKey = (e) => {
      if (!e.altKey || e.ctrlKey || e.metaKey || e.shiftKey || (e.key !== "ArrowRight" && e.key !== "ArrowLeft")) return;
      const bar = barRef.current;
      const over = [...document.querySelectorAll(".backdrop")].pop();
      if (!bar?.offsetParent || isTyping(e.target) || (over && !over.contains(bar))) return;
      e.preventDefault(); // the browser's Back and Forward
      e.key === "ArrowRight" ? keys.current.next() : keys.current.prev();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);
  if (!s) return null;
  const lines = `${s.start}${s.end > s.start ? `–${s.end}` : ""}`;
  const open = () => onOpenFile?.(s.file, s.start);
  const back = s.context || !onReturn ? open : onReturn;
  return (
    <div
      className="guide-step"
      ref={(el) => {
        barRef.current = el;
        pin(el);
      }}
    >
      <div className="guide-step-head">
        <span className="guide-step-part" title={GUIDE_PARTS[s.part] || s.part}>
          {step + 1}/{steps.length}
        </span>
        <button className="guide-step-title" onClick={back} title={s.context ? `Open ${s.file}` : "Back to this step's lines"}>
          {s.title}
        </button>
        <button className="guide-place" onClick={back} title={s.context ? `Open ${s.file} at ${lines}, which the branch doesn't change` : `Back to ${s.file}:${lines}`}>
          {s.file.slice(s.file.lastIndexOf("/") + 1)}
          <span>:{lines}</span>
        </button>
        {s.context ? (
          <span className="guide-context">context</span>
        ) : (
          <button className="ghost guide-open" onClick={open} title={`Open the whole of ${s.file} at ${lines}`}>
            <IconOpenOut size={12} />
          </button>
        )}
        <span className="spacer" />
        <button className="ghost" disabled={step === 0} onClick={prev} title={`The step before (${altKey}+←)`}>
          Back
        </button>
        <button className="btn" onClick={next} title={last ? "Done walking" : `The next step (${altKey}+→)`}>
          {last ? "Finish" : "Next"}
        </button>
        <button className="ghost guide-step-x" title="Stop walking; the changes stay as they are" onClick={() => onStep(null)}>
          <IconX size={12} />
        </button>
      </div>
      {s.say && <p className="guide-say">{s.say}</p>}
      <dl className="guide-facts">
        {s.check && (
          <>
            <dt>Check</dt>
            <dd>{s.check}</dd>
          </>
        )}
        {s.proves && (
          <>
            <dt>Guarantees</dt>
            <dd>{s.proves}</dd>
          </>
        )}
        {s.misses && (
          <>
            <dt>Leaves out</dt>
            <dd>{s.misses}</dd>
          </>
        )}
      </dl>
    </div>
  );
}

// PullRequest is the builder's title and description for the pull request,
// read and commented on before the reader publishes it, and once open, its
// link. Open on its own in Your review until then, the one time it matters.
function PullRequest({ t, text, versions, drives, act, onOpenFile }) {
  const [open, setOpen] = useState(null);
  if (!text && !t.pr) return null;
  const shown = open ?? (t.stage === "review" && !t.pr);
  return (
    <div className="board-box">
      <div className="board-box-head board-review-head">
        <button className="board-fold" aria-expanded={shown} onClick={() => setOpen(!shown)}>
          {shown ? <IconChevronDown size={10} /> : <IconChevron size={10} />}
          Pull request · {t.prTitle || t.title}
        </button>
        {t.pr ? <PRBadge t={t} /> : <span className="board-muted">not opened yet</span>}
      </div>
      {shown &&
        (text ? (
          <Versioned doc="pr" text={text} versions={versions} onOpenFile={onOpenFile}>
            {(now) => <Commentable t={t} doc="pr" text={now} drives={drives} act={act} onOpenFile={onOpenFile} />}
          </Versioned>
        ) : (
          <p className="board-muted">The builder hasn't written its description.</p>
        ))}
      {shown && <CommentLog t={t} doc="pr" drives={drives} act={act} />}
    </div>
  );
}

// CommentLog is the reader's comments on a document once sent, with what its
// author answered to each: the reader replies, or resolves one they're done
// with. Folded, but for an answer waiting to be read.
function CommentLog({ t, doc, drives, act }) {
  // Folded or not as the reader left it on this ticket; till then, open on answers to read.
  const [open, setOpen] = usePref("repo", `board:comments-open:${t.id}:${doc}`, null);
  const [showResolved, setShowResolved] = useState(false);
  const list = (t.comments || []).filter((c) => c.doc === doc);
  if (!list.length) return null;
  const [role, stage, who] = AUTHOR[doc];
  const withAuthor = (c) => !c.resolved && c.thread[c.thread.length - 1]?.by === "you";
  const live = list.filter((c) => !c.resolved);
  const done = list.filter((c) => c.resolved);
  const waiting = live.filter(withAuthor).length;
  const answered = live.length - waiting;
  const shown = open ?? answered > 0;
  // A reply goes to the author while the ticket is in its stage, where it can act on it.
  const canReply = drives && !t.archived && t.stage === stage && !!t.sessions?.[role];
  return (
    <div className="board-box board-comment-log">
      <div className="board-box-head board-review-head">
        <button className="board-fold" aria-expanded={shown} onClick={() => setOpen(!shown)}>
          {shown ? <IconChevronDown size={10} /> : <IconChevron size={10} />}
          Your comments · {list.length}
          {waiting > 0 && ` · ${waiting} with the ${who}`}
          {answered > 0 && ` · ${answered} answered`}
          {done.length > 0 && ` · ${done.length} resolved`}
        </button>
      </div>
      {shown && (
        <>
          <ol className="board-comment-list">
            {live.map((c) => (
              <CommentItem key={c.id} t={t} c={c} who={who} canReply={canReply} drives={drives} act={act} />
            ))}
          </ol>
          {done.length > 0 && (
            <button className="board-fold board-comment-done" aria-expanded={showResolved} onClick={() => setShowResolved((o) => !o)}>
              {showResolved ? <IconChevronDown size={10} /> : <IconChevron size={10} />}
              Resolved · {done.length}
            </button>
          )}
          {showResolved && (
            <ol className="board-comment-list resolved">
              {done.map((c) => (
                <CommentItem key={c.id} t={t} c={c} who={who} drives={drives} act={act} />
              ))}
            </ol>
          )}
        </>
      )}
    </div>
  );
}

// CommentItem is one comment's turns, and the reader's next one.
function CommentItem({ t, c, who, canReply, drives, act }) {
  const [draft, setDraft] = useState(null);
  const send = () => draft?.trim() && act(api.replyComment(t.id, c.id, { text: draft })).then(() => setDraft(null));
  const resolve = (resolved) => act(api.replyComment(t.id, c.id, { resolved }));
  return (
    <li className="board-comment">
      {c.quote && <blockquote className="board-comment-quote">{c.quote}</blockquote>}
      {c.thread.map((p, i) => (
        <div key={i} className={cx("board-comment-post", p.by === "you" ? "mine" : "theirs")}>
          <span className="board-comment-by">
            {p.by === "you" ? "You" : ROLE_NAME[p.by] || p.by}
            {p.kind && <span className={cx("board-comment-kind", p.kind)}>{p.kind}</span>}
          </span>
          <span className="board-comment-text">{p.text}</span>
        </div>
      ))}
      {c.thread[c.thread.length - 1]?.by === "you" && !c.resolved && <span className="board-muted">With the {who}.</span>}
      {draft != null ? (
        <div className="board-comment-reply">
          <textarea
            autoFocus
            rows={3}
            value={draft}
            placeholder={`Reply to the ${who}`}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => (e.metaKey || e.ctrlKey) && e.key === "Enter" && send()}
          />
          <div className="board-edit-foot">
            <button className="btn" onClick={() => setDraft(null)}>
              Cancel
            </button>
            <button className="primary" disabled={!draft.trim()} onClick={send}>
              Send to the {who}
            </button>
          </div>
        </div>
      ) : (
        drives && (
          <div className="board-comment-acts">
            {!c.resolved && canReply && (
              <button className="ghost" onClick={() => setDraft("")}>
                Reply
              </button>
            )}
            <button className="ghost" onClick={() => resolve(!c.resolved)}>
              {c.resolved ? "Reopen" : "Resolve"}
            </button>
          </div>
        )
      )}
    </li>
  );
}

// CodeCommentLog is the reader's comments on the branch's code in one place,
// as a document's are: each thread's lines, its turns, and where it stands,
// state(thread) being unsent, with, answered or resolved. A click on one
// takes the changes to it.
function CodeCommentLog({ id, threads, state, canReply, onAction, onJump }) {
  const [open, setOpen] = usePref("repo", `board:comments-open:${id}:code`, null);
  const [showResolved, setShowResolved] = useState(false);
  const live = threads.filter((th) => !th.resolved);
  const done = threads.filter((th) => th.resolved);
  const count = (s) => live.filter((th) => state(th) === s).length;
  const unsent = count("unsent");
  const withBuilder = count("with");
  const answered = count("answered");
  const shown = open ?? (answered > 0 || unsent > 0);
  const item = (th) => <CodeCommentItem key={th.id} th={th} st={state(th)} canReply={canReply} onAction={onAction} onJump={onJump} />;
  return (
    <div className="board-box board-comment-log">
      <div className="board-box-head board-review-head">
        <button className="board-fold" aria-expanded={shown} onClick={() => setOpen(!shown)}>
          {shown ? <IconChevronDown size={10} /> : <IconChevron size={10} />}
          Your comments on the code · {threads.length}
          {unsent > 0 && ` · ${unsent} not sent`}
          {withBuilder > 0 && ` · ${withBuilder} with the builder`}
          {answered > 0 && ` · ${answered} answered`}
          {done.length > 0 && ` · ${done.length} resolved`}
        </button>
      </div>
      {shown && (
        <>
          <ol className="board-comment-list">{live.map(item)}</ol>
          {done.length > 0 && (
            <button className="board-fold board-comment-done" aria-expanded={showResolved} onClick={() => setShowResolved((o) => !o)}>
              {showResolved ? <IconChevronDown size={10} /> : <IconChevron size={10} />}
              Resolved · {done.length}
            </button>
          )}
          {showResolved && <ol className="board-comment-list resolved">{done.map(item)}</ol>}
        </>
      )}
    </div>
  );
}

// CodeCommentItem is one thread on the code: where, its turns, the reader's next.
function CodeCommentItem({ th, st, canReply, onAction, onJump }) {
  const [draft, setDraft] = useState(null);
  const send = () => draft?.trim() && Promise.resolve(onAction({ type: "reply", thread: th, body: draft.trim() })).then(() => setDraft(null));
  const lines = th.endLine > th.startLine ? `${th.startLine}-${th.endLine}` : th.startLine;
  return (
    <li className="board-comment">
      <button className="board-comment-loc" title="Show it in the changes" onClick={() => onJump(th)}>
        {th.file}:{lines}
        {th.side === "old" && " (before the branch)"}
      </button>
      {th.quote?.length > 0 && <blockquote className="board-comment-quote board-comment-code">{th.quote.join("\n")}</blockquote>}
      {th.comments.map((c) => (
        <div key={c.id} className={cx("board-comment-post", c.author === "you" || !c.author ? "mine" : "theirs")}>
          <span className="board-comment-by">{c.author === "you" || !c.author ? "You" : ROLE_NAME[c.author] || c.author}</span>
          <span className="board-comment-text">{c.body}</span>
        </div>
      ))}
      {st === "unsent" && <span className="board-muted">Not sent to the builder yet.</span>}
      {st === "with" && <span className="board-muted">With the builder.</span>}
      {draft != null ? (
        <div className="board-comment-reply">
          <textarea
            autoFocus
            rows={3}
            value={draft}
            placeholder="Reply; it goes to the builder with your next comments"
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              e.stopPropagation();
              if ((e.metaKey || e.ctrlKey) && e.key === "Enter") send();
            }}
          />
          <div className="board-edit-foot">
            <button className="btn" onClick={() => setDraft(null)}>
              Cancel
            </button>
            <button className="primary" disabled={!draft.trim()} onClick={send}>
              Reply
            </button>
          </div>
        </div>
      ) : (
        onAction && (
          <div className="board-comment-acts">
            {!th.resolved && canReply && (
              <button className="ghost" onClick={() => setDraft("")}>
                Reply
              </button>
            )}
            <button className="ghost" onClick={() => onAction({ type: "resolve", thread: th, resolved: !th.resolved })}>
              {th.resolved ? "Reopen" : "Resolve"}
            </button>
          </div>
        )
      )}
    </li>
  );
}

const NO_THREADS = [];
// The narrowest the changes go side by side; half the dialog is narrower.
const SPLIT_WIDTH = 1000;

// useCommitRange is the stretch of a ticket's commits its changes are read
// across, the ticket's and the Code view's alike: { from, to }, the commit
// read after and the last one read, null for the branch's start and latest.
// Unset, it is the stage's: since the reader's last review, else all.
export const useCommitRange = (id) => usePref("repo", `board:commits:${id}`, null);

// readRange is that stretch for ticket t, its commits oldest first, the shas
// checked against them.
export function readRange(t, oldest, picked) {
  const has = (sha) => sha && oldest.some((c) => c.sha === sha);
  const lastReviewed = has(t.reviewedAt) && oldest[oldest.length - 1]?.sha !== t.reviewedAt ? t.reviewedAt : null;
  if (!picked) return { from: t.stage === "review" ? lastReviewed : null, to: null, lastReviewed };
  return { from: has(picked.from) ? picked.from : null, to: has(picked.to) ? picked.to : null, lastReviewed };
}

// commitRev is the diff range of that stretch: from the trunk, or from after
// `from`, to the branch's latest commit, or `to`.
// Shas cut short, as the Code view's header shows the range.
export const commitRev = (base, branch, from, to) => {
  const end = to ? to.slice(0, 12) : branch;
  return from ? `${from.slice(0, 12)}..${end}` : `${base}...${end}`;
};

// UNCOMMITTED is the last stop of a comparison that runs on to a working
// tree with work not yet committed.
export const UNCOMMITTED = { uncommitted: true, sha: "uncommitted" };

// stretchOf is the handles of { from, to } over stops: from the commit read
// after, null for the start; to the last commit read, null for the end.
export function stretchOf(stops, from, to) {
  const n = stops.length;
  const lo = from ? stops.findIndex((c) => c.sha === from) + 1 : 0;
  const hi = to ? stops.findIndex((c) => c.sha === to) : n - 1;
  return { lo: Math.min(Math.max(lo, 0), n - 1), hi: hi < 0 ? n - 1 : hi };
}

// pickOf is the { from, to } of handles lo and hi over stops.
export const pickOf = (stops, lo, hi) => ({ from: lo === 0 ? null : stops[lo - 1].sha, to: hi === stops.length - 1 ? null : stops[hi].sha });

// CommitRange picks the stretch of a comparison read: two handles on a slider
// of its stops, the commits oldest first and uncommitted work last. Ending
// before the last stop, the code is as it was then, where comments, held to
// the code as it stands, can't go. shortcuts are { label, title, run }.
export function CommitRange({ stops, lo, hi, onPick, shortcuts = [], locked }) {
  const ticks = useId();
  const n = stops.length;
  const hasU = !!stops[n - 1]?.uncommitted;
  const c = n - (hasU ? 1 : 0); // the commits
  const and = hasU && hi === n - 1 ? " and uncommitted changes" : "";
  const what =
    hasU && lo === n - 1
      ? "Only the uncommitted changes"
      : lo === 0 && hi === n - 1
        ? `All ${c} commit${c === 1 ? "" : "s"}${and}`
        : hi >= c - 1
          ? `The last ${c - lo} of ${c} commits${and}`
          : `Commits ${lo + 1}–${hi + 1} of ${c}`;
  // A thumb's centre travels between half its width (8px) from each end, not the slider's edges.
  const at = (i) => `calc(8px + (100% - 16px) * ${i / Math.max(1, n - 1)})`;
  const name = (s) =>
    s.uncommitted ? (
      "the uncommitted changes"
    ) : (
      <>
        <code>{s.short}</code> {s.subject}
      </>
    );
  const tip = (s) => (s.uncommitted ? "Not committed yet" : `${s.short} ${s.subject}${s.when ? ` · ${s.when}` : ""}`);

  // The slider drags its handles itself: the inputs' own would leave the top one
  // to take the pointer when they meet, which can't pass the other. Met, the
  // first move's direction picks the end ("either" until then).
  const slider = useRef(null);
  const drag = useRef(null);
  const stopAt = (x) => {
    const r = slider.current.getBoundingClientRect();
    return Math.round(Math.min(1, Math.max(0, (x - r.left - 8) / (r.width - 16))) * (n - 1));
  };
  const move = (end, i) => {
    const [l, h] = end === "lo" ? [Math.min(i, hi), hi] : [lo, Math.max(i, lo)];
    if (l !== lo || h !== hi) onPick(l, h);
  };
  const down = (e) => {
    if (locked || e.button !== 0) return;
    e.preventDefault();
    e.currentTarget.setPointerCapture(e.pointerId);
    const i = stopAt(e.clientX);
    const end = lo !== hi ? (i - lo < hi - i ? "lo" : "hi") : i < lo ? "lo" : i > hi ? "hi" : "either";
    drag.current = end;
    if (end !== "either") move(end, i);
  };
  const drift = (e) => {
    if (!drag.current) return;
    const i = stopAt(e.clientX);
    if (drag.current === "either") {
      if (i === lo) return;
      drag.current = i < lo ? "lo" : "hi";
    }
    move(drag.current, i);
  };
  const up = () => (drag.current = null);

  return (
    <div className={cx("board-commits", locked && "locked")}>
      <span
        ref={slider}
        className="board-commits-slider"
        title={locked ? "The walkthrough reads all of them" : undefined}
        onPointerDown={down}
        onPointerMove={drift}
        onPointerUp={up}
        onPointerCancel={up}
      >
        <span className="board-commits-track" />
        <span className="board-commits-fill" style={{ left: at(lo), right: `calc(100% - ${at(hi)})` }} />
        <input
          type="range"
          min={0}
          max={n - 1}
          value={lo}
          list={ticks}
          disabled={locked}
          aria-label="The first commit read"
          onChange={(e) => onPick(Math.min(Number(e.target.value), hi), hi)}
        />
        <input
          type="range"
          min={0}
          max={n - 1}
          value={hi}
          disabled={locked}
          aria-label="The last commit read"
          onChange={(e) => onPick(lo, Math.max(Number(e.target.value), lo))}
        />
        <datalist id={ticks}>
          {stops.map((s, i) => (
            <option key={s.sha} value={i} />
          ))}
        </datalist>
      </span>
      <span className="board-commits-what">{what}</span>
      {/* Each end is also picked by name, which a long branch's ticks are too close to pick by. */}
      {[
        ["from", lo, 0, hi, (i) => onPick(i, hi)],
        ["to", hi, lo, n - 1, (i) => onPick(lo, i)],
      ].map(([word, at, min, max, set]) => (
        <span key={word} className="board-commits-from" title={tip(stops[at])}>
          <Picker
            button="mini board-commits-pick"
            label={
              <span className="board-commits-pick-label">
                {word} {name(stops[at])}
              </span>
            }
            title={`The ${word === "from" ? "first" : "last"} ${stops[at].uncommitted ? "stop" : "commit"} read`}
            choices={stops
              .map((s, i) => ({ id: String(i), label: s.uncommitted ? "Uncommitted changes" : `${s.short} ${s.subject}`, tag: s.when }))
              .slice(min, max + 1)
              .reverse()}
            value={String(at)}
            down
            search={max - min > 8 ? "Find a commit" : undefined}
            onPick={(i) => !locked && set(Number(i))}
          />
        </span>
      ))}
      {!locked &&
        shortcuts.map((s) => (
          <button key={s.label} className="link" title={s.title} onClick={s.run}>
            {s.label}
          </button>
        ))}
      {!locked && (lo !== 0 || hi !== n - 1) && (
        <button className="link" onClick={() => onPick(0, n - 1)}>
          All
        </button>
      )}
      {hi !== n - 1 && <span className="board-commits-note">An earlier state of the code: comments go on the latest, so they're off here.</span>}
    </div>
  );
}

const sameSet = (set, files) => set.size === files.length && files.every((f) => set.has(f.path));

// CodeChanges is what the ticket's branch changes since it left the trunk,
// file by file as the Code view shows it; in Build, its worktree's too, as the
// builder makes them, marking the files not committed yet. Comments on its
// lines are the repository's review threads under the branch's range, so the
// Code view has them too; unresolved, they go to the builder together.
function CodeChanges({ t, range, commits, focus, returned, drives, act, onOpenFile, onSymbol, onOpenCode }) {
  const building = t.stage === "build" && !!t.worktree && range?.includes("...");
  // The stretch of its commits read, shared with the Code view. Back in their
  // review, the reader starts where they left off; a walkthrough goes over the
  // whole. Ending before the latest, it is read only.
  const oldest = useMemo(() => [...(commits || [])].reverse(), [commits]);
  const [picked, setPicked] = useCommitRange(t.id);
  const read = readRange(t, oldest, picked);
  const from = focus ? null : read.from;
  const to = focus ? null : read.to;
  const scope = useMemo(() => {
    const base = range?.slice(0, range.indexOf("..."));
    // Building, up to the worktree as it stands, uncommitted edits and all.
    if (building && !to) return { kind: "custom", rev: from ? `${from}..` : `${base}...`, in: t.id, own: !!from };
    return { kind: "custom", rev: commitRev(base, t.branch, from, to), in: building ? t.id : undefined, own: !!from };
  }, [range, building, from, to, t.id, t.branch]);
  const [uncommitted, setUncommitted] = useState(() => new Set());
  // Its stops: the commits, then in Build the builder's work not yet committed.
  const hasU = building && uncommitted.size > 0;
  const stops = useMemo(() => [...oldest, ...(hasU ? [UNCOMMITTED] : [])], [oldest, hasU]);
  const { lo, hi } = stretchOf(stops, from, to);
  const earlier = hi !== stops.length - 1; // read only: comments go on the code as it stands
  const [diff, setDiff] = useState(null); // { files, scope } or { error }
  const [fds, setFds] = useState({});
  // The walkthrough's step walked to, marked and scrolled to in the changes.
  const stepFd = focus && fds[focus.file]?.fd;
  const places = useMemo(() => stepPlaces(focus, stepFd), [focus, stepFd]);
  useGuideMarks("guide-marks-ticket", places, ".board-changes");
  const pin = useMemo(() => focus && { start: focus.start, end: focus.end }, [focus]);
  useStepScroll(places, ".board-changes", !!diff?.files, returned);
  const [threads, setThreads] = useState(NO_THREADS);
  const [collapsed, setCollapsed] = useState(() => new Set());
  // A step in a folded file opens it.
  useEffect(() => {
    if (focus) setCollapsed((c) => (c.has(focus.file) ? new Set([...c].filter((p) => p !== focus.file)) : c));
  }, [focus]);
  const [composing, setComposing] = useState(null);
  const [contextLines] = usePref("user", "context", 3);
  const [wrap] = usePersisted("wrap", false);
  // The repository's file filters, as the Code view has them and shared with
  // it; the box narrows the list for now, and isn't kept.
  const [hideGenerated, setHideGenerated] = usePref("repo", "hideGenerated", false);
  const [pathFilter, setPathFilter] = usePref("repo", "pathFilter", NO_FILTER);
  const [paused, setPaused] = useState(false);
  const [filtersOpen, setFiltersOpen] = useState(false);
  const [narrow, setNarrow] = useState("");
  // Side by side, as the Code view has it, once there is the room: the agents folded away.
  const [viewPicked] = usePersisted("view", "split");
  const boxRef = useRef(null);
  const [roomy, setRoomy] = useState(false);
  useEffect(() => {
    const el = boxRef.current;
    if (!el) return;
    const ro = new ResizeObserver(([e]) => setRoomy(e.contentRect.width >= SPLIT_WIDTH));
    ro.observe(el);
    return () => ro.disconnect();
  }, [!range]);
  const view = roomy && viewPicked === "split" ? "split" : "unified";
  const seen = useRef("");
  const loadThreads = useCallback(() => api.threads().then((r) => setThreads(r.threads || NO_THREADS), () => {}), []);
  useEffect(() => {
    if (!range) return;
    let live = true;
    seen.current = ""; // another range is new files, whatever their counts
    // The list is cheap; the files are fetched again only when it changes.
    const load = () => {
      if (building)
        api.diffList({ kind: "working", in: t.id }).then(
          (w) => live && setUncommitted((had) => (sameSet(had, w.files) ? had : new Set(w.files.map((f) => f.path)))),
          () => {},
        );
      return api.diffList(scope).then(
        async (d) => {
          if (!live) return;
          setDiff(d);
          // The version too: an edit can leave a file's counts as they were.
          const sig = d.scope?.newAt + d.version + JSON.stringify(d.files.map((f) => [f.path, f.additions, f.deletions]));
          if (sig === seen.current) return;
          seen.current = sig;
          const got = d.files.length ? await api.diffFiles(scope, d.files.map((f) => f.path)) : {};
          if (live) setFds(got);
        },
        (e) => live && setDiff({ error: e.message }),
      );
    };
    load();
    loadThreads();
    const tick = setInterval(() => document.visibilityState === "visible" && load(), 5000);
    return () => {
      live = false;
      clearInterval(tick);
    };
  }, [range, scope, building, t.id, loadThreads]);

  // The branch's range, building or not, so comments made in Build are there in review.
  const label = range;
  const mine = useMemo(() => threads.filter((th) => th.scope === label && !th.origin), [threads, label]);
  const byFile = useMemo(() => {
    const m = new Map();
    for (const th of mine) m.set(th.file, [...(m.get(th.file) || []), th]);
    return m;
  }, [mine]);
  // Open threads the builder hasn't had, or that the reader has added to since
  // it last did, are theirs to send; those it has and hasn't answered are
  // with it. It answers in the thread, and the reader resolves it.
  const sentAt = new Map((t.codeThreads || []).map((s) => [s.id, s.sent]));
  const lastBy = (th) => th.comments[th.comments.length - 1]?.author;
  const pending = mine.filter((th) => !th.resolved && (!sentAt.has(th.id) || (th.comments.length > sentAt.get(th.id) && lastBy(th) !== "builder")));
  const withBuilder = mine.filter((th) => !th.resolved && sentAt.has(th.id) && th.comments.length <= sentAt.get(th.id) && lastBy(th) !== "builder").length;
  const stateOf = (th) => (th.resolved ? "resolved" : pending.includes(th) ? "unsent" : lastBy(th) === "builder" ? "answered" : "with");
  // A comment picked in the list, brought into view in the changes: its file
  // unfolded and shown whatever hides it, at the code as it stands.
  const [jumpTo, setJumpTo] = useState(null); // { id, file, line }
  const jump = (th) => {
    setCollapsed((c) => (c.has(th.file) ? new Set([...c].filter((p) => p !== th.file)) : c));
    if (earlier) setPicked(pickOf(stops, lo, stops.length - 1));
    setJumpTo({ id: th.id, file: th.file, line: th.side === "old" ? 0 : th.startLine });
  };
  useEffect(() => {
    const box = boxRef.current;
    if (!jumpTo || !box) return;
    let root = box.parentElement;
    while (root && !/(auto|scroll)/.test(getComputedStyle(root).overflowY)) root = root.parentElement;
    if (!root) return;
    return chase(root, () => {
      const el = box.querySelector(`[data-thread="${CSS.escape(jumpTo.id)}"]`);
      if (el && laidOut(el)) return { y: yOf(root, el, 120), final: true };
      const s = box.querySelector(`.file[data-path="${CSS.escape(jumpTo.file)}"]`);
      return s ? { y: yOf(root, s, 0), final: false } : null;
    });
  }, [jumpTo]);
  const onComment = useCallback(async (payload) => {
    const th = await api.createThread({ scope: label, ...payload });
    loadThreads();
    return th;
  }, [label, loadThreads]);
  const onThreadAction = useCallback(
    async ({ type, thread, ...a }) => {
      if (type === "jump") return;
      if (type === "reply") await api.reply(thread.id, a.body);
      else if (type === "resolve") await api.setResolved(thread.id, a.resolved);
      else if (type === "editComment") await api.editComment(thread.id, a.commentId, a.body);
      else if (type === "deleteComment") {
        if (thread.comments.length === 1 && !confirm("Delete this comment? The thread goes with it.")) return;
        await api.deleteComment(thread.id, a.commentId);
      }
      loadThreads();
    },
    [loadThreads],
  );
  const toggle = useCallback((path) => setCollapsed((c) => {
    const n = new Set(c);
    n.has(path) ? n.delete(path) : n.add(path);
    return n;
  }), []);
  // A thread sent before goes again with only what the reader added since.
  const send = () =>
    act(
      api
        .codeComments(
          t.id,
          pending.map((th) => ({
            thread: th.id, file: th.file, start: th.startLine, end: th.endLine, side: th.side,
            quote: (th.quote || []).join("\n"),
            text: th.comments.slice(sentAt.get(th.id) || 0).map((c) => c.body).join("\n\n"),
          })),
        )
        .then(loadThreads),
    );
  const canSend = drives && !t.archived && !!t.sessions?.builder && ["build", "review"].includes(t.stage);
  // Told to the ticket, whose foot offers them in place of approving.
  const { tell, sent } = useContext(PendingCode) || {};
  const sendNow = useRef(send);
  sendNow.current = send;
  const unsent = canSend ? pending.length : 0;
  useEffect(() => {
    tell?.(unsent ? { count: unsent, send: () => sendNow.current() } : null);
  }, [tell, unsent]);
  useEffect(() => () => tell?.(null), [tell]);

  if (!range) return <p className="board-muted">{t.branch ? "The branch is gone; its pull request keeps the changes." : "No branch yet."}</p>;
  const all = diff?.files || [];
  const add = all.reduce((n, f) => n + (f.additions || 0), 0);
  const del = all.reduce((n, f) => n + (f.deletions || 0), 0);
  // The walkthrough's file is shown whatever the filters say.
  const keep = focus?.file || jumpTo?.file;
  const filtered = applyFileFilters(all, { hideGenerated, pathFilter, paused, keep });
  const boxFilter = listFilter(narrow);
  const files = boxFilter ? filtered.files.filter((f) => f.path === keep || boxFilter(f.path)) : filtered.files;
  const byBox = filtered.files.length - files.length;
  const hidden = filtered.hiddenGenerated + filtered.filteredOut;
  const filtersOn = !!(hideGenerated || pathFilter.include.trim() || pathFilter.exclude.trim());
  return (
    <div className="board-changes" ref={boxRef}>
      <div className="board-changes-head">
        <h4>Changes</h4>
        {diff && !diff.error && (
          <span className="board-muted">
            {all.length} file{all.length === 1 ? "" : "s"} · <span className="add">+{add}</span> <span className="del">-{del}</span>
            {building
              ? uncommitted.size > 0 && (
                  <span title="Changed in the builder's worktree and not committed yet: it is still working on them">
                    {" "}· {uncommitted.size} not committed yet
                  </span>
                )
              : t.stage === "build" && " · committed so far"}
          </span>
        )}
        <span className="spacer" />
        <input
          className="board-changes-filter"
          type="search"
          value={narrow}
          placeholder="Filter files"
          title="Part of a path, or a glob: *.go, src/**/*.ts. Separate several with commas; start one with ! to hide what it matches."
          onChange={(e) => setNarrow(e.target.value)}
          onKeyDown={(e) => e.stopPropagation()}
        />
        <button
          className={cx("ghost", filtersOn && !paused && "on")}
          aria-expanded={filtersOpen}
          title="Hide generated files, or pick the files to include or exclude - the repository's filters, which the Code view shares"
          onClick={() => setFiltersOpen((o) => !o)}
        >
          <IconFilter size={13} />
        </button>
        {onOpenCode && (
          <button className="btn" title={`See ${range} in the Code view, its comments going to the builder`} onClick={() => onOpenCode(range, t.sessions?.builder, undefined, t.id)}>
            Open in Code view
          </button>
        )}
        {withBuilder > 0 && (
          <span className="board-muted" title="Sent; its answers come as replies in the threads">
            {withBuilder} comment{withBuilder === 1 ? "" : "s"} with the builder
          </span>
        )}
        {canSend && pending.length > 0 && (
          <button className="primary" title={t.stage === "build" ? "The builder takes them now" : "Sends the ticket back to Build with them"} onClick={() => send().then(() => sent?.())}>
            Send {pending.length} comment{pending.length === 1 ? "" : "s"} to the builder
          </button>
        )}
      </div>
      {mine.length > 0 && <CodeCommentLog id={t.id} threads={mine} state={stateOf} canReply={canSend} onAction={drives ? onThreadAction : null} onJump={jump} />}
      {stops.length > 1 && (
        <CommitRange
          stops={stops}
          lo={lo}
          hi={hi}
          onPick={(l, h) => setPicked(pickOf(stops, l, h))}
          locked={!!focus}
          shortcuts={
            read.lastReviewed && (from !== read.lastReviewed || earlier)
              ? [{ label: "Since your last review", title: "What the builder did after you sent it back", run: () => setPicked({ from: read.lastReviewed, to: null }) }]
              : []
          }
        />
      )}
      {filtersOpen && (
        <PathFilterPanel
          hideGenerated={hideGenerated}
          onHideGenerated={(v) => (setPaused(false), setHideGenerated(v))}
          generatedCount={all.filter((f) => f.generated).length}
          pathFilter={pathFilter}
          onPathFilter={(f) => (setPaused(false), setPathFilter(f))}
        />
      )}
      {/* Nothing hidden without saying so, and how to see it. */}
      {(hidden > 0 || byBox > 0) && (
        <div className={cx("filter-note", paused && "paused")}>
          <span>
            {paused ? `Filters paused: ${hidden} would be hidden` : `${hidden + byBox} hidden`} (
            {[
              filtered.hiddenGenerated && `${filtered.hiddenGenerated} generated`,
              filtered.filteredOut && `${filtered.filteredOut} excluded`,
              byBox && `${byBox} by the filter box`,
            ]
              .filter(Boolean)
              .join(", ")}
            )
          </span>
          {hidden > 0 && (
            <button className="link" onClick={() => setPaused((p) => !p)}>
              {paused ? "Resume" : "Show all"}
            </button>
          )}
        </div>
      )}
      {!diff && <p className="board-muted">Loading…</p>}
      {diff?.error && <p className="board-error">{diff.error}</p>}
      {diff && !diff.error && !all.length && <p className="board-muted">Nothing committed on the branch yet.</p>}
      {files.map((entry) => (
        <div key={entry.path} className={cx("file-section", building && uncommitted.has(entry.path) && "uncommitted")}>
          <FileDiff
            reveal={(jumpTo?.file === entry.path && jumpTo.line) || places?.get(entry.path)?.[0]?.start || 0}
            pin={focus?.file === entry.path ? pin : undefined}
            entry={entry}
            fd={fds[entry.path]?.fd}
            loading={!fds[entry.path]}
            error={fds[entry.path]?.error}
            view={view}
            contextLines={contextLines}
            wrap={wrap}
            threads={earlier ? NO_THREADS : byFile.get(entry.path) || NO_THREADS}
            collapsed={collapsed.has(entry.path)}
            onToggleCollapse={toggle}
            composing={composing?.path === entry.path ? composing : null}
            setComposing={setComposing}
            onComment={earlier ? undefined : onComment}
            onThreadAction={onThreadAction}
            onSymbol={onSymbol}
            onView={onOpenFile}
            scope={scope}
            onOpenFile={onOpenFile}
          />
        </div>
      ))}
    </div>
  );
}

// modelName names a model off the list too, as a family's bare name.
const modelName = (id) => {
  const name = prettyModel(id);
  return name ? name[0].toUpperCase() + name.slice(1) : "Default model";
};

// RolePick is the model and effort a role starts on next; dv keeps a pick for
// the role on the tickets to come.
function RolePick({ t, role, models, act }) {
  return <PickModel pick={t.picks?.[role]} models={models} onPick={(p) => act(api.patchTicket(t.id, { pick: { role, ...p } }))} />;
}

// PickModel is a model and effort picked, as onPick is told them: { model } or { effort }.
function PickModel({ pick = {}, models, onPick }) {
  const menu = useModelMenu(models || [], pick.model || "");
  const of = (id) => menu.find((m) => m.id === id);
  const on = of(pick.model || "");
  const choose = (p) => {
    // An effort the new model doesn't take goes back to the model's own.
    if (p.model !== undefined && pick.effort && !of(p.model)?.efforts?.includes(pick.effort)) p = { ...p, effort: "" };
    onPick(p);
  };
  return <ModelPicker label={on?.label || modelName(pick.model)} models={menu} model={pick.model || ""} efforts={on?.efforts} effort={pick.effort} usual onPick={choose} />;
}

const PLAN_PHASE = { review: "Another model is reading the plan", judge: "The planner is weighing the findings", done: "Reviewed" };

// PlanReview is another model's reading of the plan, and the planner's answer
// to each finding; before a plan is handed on, whether there is one and who
// does it.
function PlanReview({ t, models, drives, act, onSession }) {
  const menu = useModelMenu(models || [], "");
  const [open, setOpen] = useState(false);
  const r = t.planReview;
  const can = t.stage === "plan" && drives && !t.archived;
  if (!r && !can) return null;
  const picker = <RolePick t={t} role="plan-reviewer" models={models} act={act} />;
  // A plan handed over without one - written before plan review was on, or
  // with it off - can still be sent to one.
  if (!r && t.ready) {
    return (
      <div className="board-settings">
        <span>Plan review</span>
        <div className="board-chips">
          <span className="board-muted">Not reviewed</span>
          {picker}
          <button className="btn" onClick={() => act(api.reviewPlan(t.id))}>
            Have another model review it
          </button>
        </div>
      </div>
    );
  }
  if (!r) {
    return (
      <div className="board-settings">
        <span>Plan review</span>
        <div className="board-chips">
          <button className={cx("board-chip", !t.noPlanReview && "on")} onClick={() => act(api.patchTicket(t.id, { noPlanReview: false }))}>
            On
          </button>
          <button className={cx("board-chip", t.noPlanReview && "on")} onClick={() => act(api.patchTicket(t.id, { noPlanReview: true }))}>
            Off
          </button>
          {!t.noPlanReview && picker}
        </div>
      </div>
    );
  }
  // Folded, one line of totals across every review, to say whether it is worth
  // opening; open, each review under its own head, newest first.
  const all = [...(t.earlierPlanReviews || []), r];
  const label = (x) => menu.find((m) => m.id === x.model)?.label || modelName(x.model);
  const title = all.length > 1 ? `Plan reviews · ${all.length} rounds` : `Plan review · ${label(r)}`;
  const totals = countFindings(all.flatMap((x) => x.findings || []));
  const line = r.phase === "done" ? totals || "No findings" : `${PLAN_PHASE[r.phase] || r.phase}${totals ? ` · ${totals}` : ""}`;
  return (
    <div className={cx("board-box", r.phase !== "done" && "open")}>
      <div className="board-box-head board-review-head">
        <button className="board-fold" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
          {open ? <IconChevronDown size={10} /> : <IconChevron size={10} />}
          {title} · {line}
        </button>
        <span className="spacer" />
        {r.phase === "done" && can && (
          <>
            {picker}
            <button className="btn" title="Have the model picked beside it read the plan as it stands now" onClick={() => act(api.reviewPlan(t.id))}>
              Review again
            </button>
          </>
        )}
      </div>
      {open &&
        all
          .map((x, i) => (
            <div key={i} className="board-review-round">
              <div className="board-review-head">
                <span>
                  {all.length > 1 ? `Review ${i + 1} · ` : ""}
                  {label(x)} · {x.phase === "done" ? countFindings(x.findings || []) || "No findings" : PLAN_PHASE[x.phase] || x.phase}
                </span>
                <span className="spacer" />
                {x.session && onSession && (
                  <button className="btn" onClick={() => onSession(x.session)}>
                    Its reviewer
                  </button>
                )}
              </div>
              <Findings findings={x.findings || []} />
            </div>
          ))
          .reverse()}
    </div>
  );
}

// countFindings says how many findings there are and how the planner took
// them, "" for none.
function countFindings(findings) {
  if (!findings.length) return "";
  const taken = findings.filter((f) => f.verdict === "taken").length;
  const rejected = findings.filter((f) => f.verdict === "rejected").length;
  const how = [taken && `${taken} taken`, rejected && `${rejected} rejected`].filter(Boolean).join(", ");
  return `${findings.length} finding${findings.length === 1 ? "" : "s"}${how ? `: ${how}` : ""}`;
}

function Findings({ findings }) {
  return findings.map((f) => (
    <div key={f.id} className="board-finding">
      <div>
        <span className="board-fid">{f.id}</span> {f.text}
      </div>
      {f.verdict && (
        <div className="board-verdict">
          <span className={cx("board-tag", f.verdict === "taken" ? "fixed" : "rejected")}>{f.verdict === "taken" ? "Taken" : "Rejected"}</span>
          {f.reason}
        </div>
      )}
    </div>
  ));
}

// Review is the agent review of the code, shown as the plan's is: folded to a
// line of totals across its rounds, with the next round's kinds, its model and
// Run another round on that line; open, each round, newest first.
function Review({ t, models, drives, act, onSession }) {
  const menu = useModelMenu(models || [], "");
  const [open, setOpen] = useState(false);
  const rounds = t.rounds || [];
  const last = rounds[rounds.length - 1];
  const picked = t.templates?.length ? t.templates : Object.keys(TEMPLATES);
  const toggle = (k) => {
    const next = picked.includes(k) ? picked.filter((x) => x !== k) : [...picked, k];
    if (next.length) act(api.patchTicket(t.id, { templates: next }));
  };
  const settable = drives && !t.archived && t.stage !== "merged";
  const canRun = settable && ["build", "review"].includes(t.stage) && !!t.sessions?.builder && (!last || last.phase === "done");
  const findings = rounds.flatMap((r) => r.findings || []);
  const fixed = findings.filter((f) => f.verdict === "fixed").length;
  const rejected = findings.filter((f) => f.verdict === "rejected").length;
  const hold = findings.length > 0 && findings.every((f) => f.recheck === "holds");
  const totals = findings.length
    ? `${findings.length} finding${findings.length === 1 ? "" : "s"}${fixed + rejected ? `: ${[fixed && `${fixed} fixed`, rejected && `${rejected} rejected`].filter(Boolean).join(", ")}` : ""}${hold ? " · all hold" : ""}`
    : "";
  const line = !last
    ? t.stage === "build" ? "runs when the builder hands over" : "not run yet"
    : last.phase === "done" ? totals || "No findings" : `round ${rounds.length} ${PHASE[last.phase] || last.phase}${totals ? ` · ${totals}` : ""}`;
  return (
    <div className={cx("board-box", last && last.phase !== "done" && "open")}>
      <div className="board-box-head board-review-head">
        <button className="board-fold" aria-expanded={open} disabled={!rounds.length && !settable} onClick={() => setOpen((o) => !o)}>
          {open ? <IconChevronDown size={10} /> : <IconChevron size={10} />}
          {rounds.length > 1 ? `Agent review · ${rounds.length} rounds` : "Agent review"} · {line}
        </button>
        <span className="spacer" />
        {settable && <RolePick t={t} role="reviewer" models={models} act={act} />}
        {canRun && (
          <button className="btn" title="Have the model picked beside it review the branch as it stands now" onClick={() => act(api.runRound(t.id))}>
            {rounds.length ? "Run another round" : "Run a round"}
          </button>
        )}
      </div>
      {open && settable && (
        <div className="board-settings">
          <span>Next round</span>
          <div className="board-chips">
            {Object.entries(TEMPLATES).map(([k, l]) => (
              <button key={k} className={cx("board-chip", picked.includes(k) && "on")} onClick={() => toggle(k)}>
                {l}
              </button>
            ))}
          </div>
        </div>
      )}
      {open &&
        rounds
          .map((r, i) => (
        <div key={i} className="board-review-round">
          <div className="board-review-head">
            <span>
              Round {i + 1} · {(r.templates || []).map((k) => TEMPLATES[k] || k).join(", ")}
              {r.model ? ` · ${menu.find((m) => m.id === r.model)?.label || modelName(r.model)}` : ""} · {PHASE[r.phase] || r.phase}
            </span>
            <span className="spacer" />
            {r.session && onSession && (
              <button className="btn" onClick={() => onSession(r.session)}>
                Its reviewer
              </button>
            )}
          </div>
          {!r.findings?.length && <p className="board-muted">{r.phase === "review" ? "Reviewing…" : "No findings."}</p>}
          {(r.findings || []).map((f) => (
            <div key={f.id} className="board-finding">
              <div>
                <span className="board-fid">{f.id}</span> {f.text}
              </div>
              {f.verdict && (
                <div className="board-verdict">
                  <span className={cx("board-tag", f.verdict)}>{f.verdict === "fixed" ? "Fixed" : "Rejected"}</span>
                  {f.reason}
                  {f.recheck === "holds" && <span className="board-ok"> ✓ {f.verdict === "fixed" ? "holds" : "reviewer agrees"}</span>}
                  {f.recheck === "fails" && <span className="board-fails"> ✕ {f.note}</span>}
                  {!f.recheck && r.phase === "recheck" && <span className="board-running"> Rechecking…</span>}
                </div>
              )}
            </div>
          ))}
        </div>
          ))
          .reverse()}
    </div>
  );
}

// The agent a move starts, and so picks the model for.
const MOVE_STARTS = { scope: "scope", plan: "planner", build: "builder" };

// codePending is the line comments not yet sent to the builder ({ count,
// send }); onSent shows the builder taking them.
export function Foot({ t, remote, drives, models, onMove, act, codePending, onSent }) {
  // The pull request description's, kept on disk as its box keeps them.
  const [prSaved, setPrSaved] = usePref("repo", `board:comments:${t.id}:pr`, null);
  // The button clicked while what it does is under way - pushing and opening a
  // pull request, a fetch and a worktree - which takes seconds, and says so.
  const [busy, setBusy] = useState("");
  const [deciding, setDeciding] = useState(false); // its follow-ups, asked about as it is approved
  const [disputing, setDisputing] = useState(false); // an agent's "nothing to do", answered with what's left
  const run = (key, work) => {
    setBusy(key);
    Promise.resolve(work()).finally(() => setBusy(""));
  };
  const working = (key, label, doing) => (busy === key ? doing || `${label}…` : label);
  if (!drives) return <div className="board-foot"><span className="board-muted">Open the main checkout's dv to move this ticket.</span></div>;
  if (t.archived) return <div className="board-foot"><span className="board-muted">Archived. Restore it to move it again.</span></div>;
  const to = (stage, label, primary, doing) => {
    const button = (
      <button key={stage + label} className={primary ? "primary" : "btn"} disabled={!!busy} onClick={() => run(stage, () => onMove(t.id, stage))}>
        {working(stage, label, doing)}
      </button>
    );
    const role = MOVE_STARTS[stage];
    if (!role || t.sessions?.[role] || INDEX[stage] < INDEX[t.stage]) return button;
    return (
      <span key={stage + label} className="board-next">
        <span className="board-muted">{ROLE_NAME[role]}</span>
        <RolePick t={t} role={role} models={models} act={act} />
        {button}
      </span>
    );
  };
  const back = (stage) => to(stage, `Send back to ${LABEL[stage]}`);
  let hint = "";
  let buttons = [];
  if (t.nothing && t.stage !== "merged") {
    buttons = [
      <button key="dispute" className="btn" disabled={!!busy} title="Tell the agent what's still to do; it carries on" onClick={() => setDisputing(true)}>
        It isn't done
      </button>,
      <button key="close" className="primary" disabled={!!busy} title="Merged with no change, releasing what waits for it" onClick={() => run("close", () => act(api.nothing(t.id, true)))}>
        {working("close", "Close as done", "Closing…")}
      </button>,
    ];
  } else switch (t.stage) {
    case "inbox":
      buttons = [to("plan", "Start at Plan"), to("scope", "Move to Scope", true)];
      break;
    case "scope":
      if (t.split) hint = "Its sub-tickets carry the work; it closes when they are merged.";
      else if (t.scopeQuestion) hint = "The plan resumes once the question is answered.";
      else buttons = [to("plan", t.children?.length ? "Approve the split" : "Approve ticket", t.ready && !t.working)];
      break;
    case "plan":
      if (t.question) hint = "The build resumes once the question is answered.";
      else if (t.scopeQuestion) hint = `The plan resumes once ${t.parent}'s scoper answers.`;
      else buttons = [INDEX[t.started] <= INDEX.scope && back("scope"), to("build", "Approve plan", t.ready && !t.working)].filter(Boolean);
      break;
    case "build":
      buttons = [back("plan"), reviewing(t) && to("review", "Skip to your review")].filter(Boolean);
      break;
    case "review": {
      if (t.merging) hint = "Merges once CI passes.";
      else if (t.suggestions?.length) hint = `${t.suggestions.length} suggested follow-up${t.suggestions.length === 1 ? "" : "s"} undecided`;
      // Comments not yet sent are the review going on: sending them is the
      // next step, and approving asks first.
      const prPending = t.sessions?.builder ? prSaved?.pending || [] : [];
      const unsent = (codePending?.count || 0) + prPending.length;
      const sendAll = async () => {
        // The description's first, while the ticket is still in review, where they go.
        if (prPending.length) await act(api.commentTicket(t.id, "pr", prPending.map(({ quote, text }) => ({ quote, text }))).then(() => setPrSaved(null)));
        if (codePending) await codePending.send();
        onSent?.();
      };
      const opening = remote && !t.pr;
      const approve = opening ? "Approve: open it, merge when CI passes" : "Approve, merge when CI passes";
      const approving = opening ? "Opening the pull request…" : "Approving…";
      // Nothing is published until you say so: open it for others to see, or approve and it opens on the way to merging.
      buttons = [
        back("build"),
        opening && (
          <button
            key="publish"
            className="btn"
            disabled={!!busy}
            title="Push the branch and open the pull request with the builder's description"
            onClick={() => run("publish", () => act(api.publishTicket(t.id)))}
          >
            {working("publish", "Open pull request", "Opening the pull request…")}
          </button>
        ),
        !t.merging && (
          <button
            key="approve"
            className={unsent ? "btn" : "primary"}
            disabled={!!busy}
            onClick={() => {
              if (unsent && !confirm(`${unsent} comment${unsent === 1 ? " hasn't" : "s haven't"} been sent to the builder. Approve anyway?`)) return;
              // Its follow-ups are decided before it goes, or they go with it into the archive.
              if (t.suggestions?.length) return setDeciding(true);
              run("merged", () => onMove(t.id, "merged"));
            }}
          >
            {working("merged", approve, approving)}
          </button>
        ),
        unsent > 0 && (
          <button key="send" className="primary" disabled={!!busy} title={codePending ? "The builder takes them in Build" : "The builder revises the description"} onClick={() => run("send", sendAll)}>
            {working("send", `Send ${unsent} comment${unsent === 1 ? "" : "s"} to the builder`, "Sending…")}
          </button>
        ),
      ].filter(Boolean);
      break;
    }
  }
  return (
    <div className="board-foot">
      {t.problem && <span className="board-error">{t.problem}</span>}
      <span className="spacer" />
      {hint && <span className="board-muted">{hint}</span>}
      {buttons}
      {disputing && (
        <SendBack
          id={t.id}
          head={`${t.id} isn't done`}
          ask="What's still to do"
          button={`Tell the ${ROLE_NAME[t.nothing?.by]?.toLowerCase() || "agent"}`}
          onClose={() => setDisputing(false)}
          onSend={(note) => (setDisputing(false), act(api.nothing(t.id, false, note)))}
        />
      )}
      {deciding && (
        <FollowUpsDialog
          t={t}
          act={act}
          onClose={() => setDeciding(false)}
          onApprove={(file) => {
            setDeciding(false);
            // The ticked filed, the rest dismissed, before it goes.
            run("merged", () =>
              act(
                (async () => {
                  if (file) for (const s of t.suggestions) await api.suggestion(t.id, s.id, file.has(s.id));
                })(),
              ).then(() => onMove(t.id, "merged")),
            );
          }}
        />
      )}
    </div>
  );
}

// FollowUpsDialog asks, as a ticket is approved, what becomes of the
// follow-ups its agents suggested: each filed to the Inbox, ticked by
// default, or dismissed; or all left on it for later.
function FollowUpsDialog({ t, act, onApprove, onClose }) {
  const list = t.suggestions || [];
  const [ticked, setTicked] = useState(() => new Set(list.map((s) => s.id)));
  const [editing, setEditing] = useState(null); // { id, title, request }
  const flip = (id) => setTicked((s) => (s.has(id) ? new Set([...s].filter((x) => x !== id)) : new Set([...s, id])));
  return (
    <Modal onClose={onClose} className="board-form board-followups">
      <div className="board-form-head">
        Before {t.id} merges: {list.length} suggested follow-up{list.length === 1 ? "" : "s"}
      </div>
      <p className="board-muted">Ticked ones go to the Inbox as follow-ups of {t.id}; the rest are dismissed.</p>
      <ul className="board-followup-list">
        {list.map((s) => (
          <li key={s.id}>
            {editing?.id === s.id ? (
              <InboxEdit
                draft={editing}
                rows={4}
                onChange={setEditing}
                onCancel={() => setEditing(null)}
                onSave={() => act(api.editSuggestion(t.id, s.id, editing.title.trim(), editing.request).then(() => setEditing(null)))}
              />
            ) : (
              <div className="board-followup-row">
                <label className="check">
                  <input type="checkbox" className="tick" checked={ticked.has(s.id)} onChange={() => flip(s.id)} />
                  <span>
                    <span className="board-followup-title">{s.title}</span>
                    <span className="board-muted"> · from the {ROLE_NAME[s.by]?.toLowerCase() || s.by}</span>
                    {s.request && <span className="board-followup-request">{s.request}</span>}
                  </span>
                </label>
                <button className="mini" title="Change its title and request before filing it" onClick={() => setEditing({ id: s.id, title: s.title, request: s.request || "" })}>
                  Edit
                </button>
              </div>
            )}
          </li>
        ))}
      </ul>
      <div className="board-edit-foot">
        <button className="btn" onClick={() => onApprove(null)} title="Approve, and leave them all on the ticket">
          Approve, decide later
        </button>
        <button className="primary" onClick={() => onApprove(ticked)}>
          {ticked.size ? `File ${ticked.size === list.length ? (list.length === 1 ? "it" : "them") : ticked.size} and approve` : "Dismiss them and approve"}
        </button>
      </div>
    </Modal>
  );
}
