import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api } from "./api.js";
import { cx, relTime } from "./util.js";
import { Modal } from "./Overlays.jsx";
import { MarkdownDocument, MarkdownPreview } from "./Preview.jsx";
import { ModelPicker, prettyModel, useModelMenu } from "./Picker.jsx";
import { filesIn, readImage } from "./Agent.jsx";
import { IconBranch, IconCheck, IconChevron, IconChevronDown, IconLock, IconPlus, IconTrash } from "./icons.jsx";

// The board: a repository's tickets, each taken from a rough request through
// scope, plan, build and review to a merged pull request, with an agent at
// each stage that the reader can talk to at any time.

export const STAGES = [
  ["inbox", "Inbox"],
  ["scope", "Scope"],
  ["plan", "Plan"],
  ["build", "Build"],
  ["agent-review", "Agent review"],
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
const PHASE = { review: "reviewing", fix: "fixing", recheck: "rechecking", done: "done" };
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
  if (t.stage === "merged") return null;
  if (t.problem) return { kind: "problem", text: t.problem.split("\n")[0] };
  if (t.split) {
    const kids = (t.children || []).map((id) => byId[id]).filter(Boolean);
    return { kind: "split", text: `${kids.filter((k) => k.stage === "merged").length} of ${kids.length} merged`, kids };
  }
  if (t.asking) return { kind: "needs", text: "Waiting on your answer" };
  // What it handed over waits on the reader again once it stops: a question
  // answered leaves it as it was, and a change hands it over anew.
  if (t.ready && t.working) return { kind: "working", text: "Working on your message" };
  if (t.ready) return { kind: "needs", text: t.stage === "scope" ? "Ticket ready to approve" : "Plan ready to approve" };
  if (t.blocked?.length) return { kind: "blocked", text: `Waits for ${t.blocked.join(", ")}` };
  if (t.stage === "inbox") return null;
  if (t.stage === "review") return t.merging ? { kind: "ci", text: "Merges when CI passes" } : { kind: "needs", text: "Ready for your review" };
  if (t.stage === "plan" && t.question) return { kind: t.working ? "working" : "idle", text: "The planner has the builder's question" };
  if (t.stage === "agent-review") {
    const r = t.rounds?.[t.rounds.length - 1];
    const text = r ? `Round ${t.rounds.length} · ${PHASE[r.phase] || r.phase}` : "Starting review";
    return { kind: t.working ? "working" : "idle", text };
  }
  return { kind: t.working ? "working" : "idle", text: t.status || (t.working ? "Working" : "Idle") };
}

// createTicket files a ticket with its images, and only then starts it, so
// its first agent has them.
export async function createTicket({ stage, images = [], ...t }) {
  const made = await api.addTicket({ ...t, stage: "inbox" });
  for (const img of images) await api.addTicketImage(made.id, { mediaType: img.mediaType, data: img.data });
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
  return (
    <span className={cx("board-pr", state)} title={{ passing: "CI passed", failing: "CI failed", pending: "CI running", merged: "Merged" }[state] || "No CI checks"}>
      {state === "passing" && <IconCheck size={10} />}
      {state === "failing" && <span className="board-pr-x">✕</span>}
      {state === "pending" && <span className="board-dot ci" />}#{t.pr}
    </span>
  );
}

function Card({ t, byId, onOpen, onMove, dragging, onDrag }) {
  const s = cardState(t, byId);
  const quiet = s?.kind === "blocked" || s?.kind === "split";
  return (
    <div
      className={cx("board-card", needsYou(s) && !t.archived && "needs", quiet && "quiet", (t.stage === "merged" || t.archived) && "done", dragging && "dragging")}
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
          {t.id}
        </span>
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
      {t.stage === "inbox" && !t.archived && (
        <button
          className="board-move"
          onClick={(e) => {
            e.stopPropagation();
            onMove(t.id, "scope");
          }}
        >
          Scope →
        </button>
      )}
    </div>
  );
}

// BoardSideList is the sidebar in Board mode: what waits on the reader, then
// what its agents are working on.
export function BoardSideList({ board, openId, onOpen }) {
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
    <div className="board-side">
      {group("Needs you", needs)}
      {group("Working", working)}
      {!needs.length && !working.length && <div className="board-empty">Nothing waits on you, and no agent is working.</div>}
    </div>
  );
}

// BoardView is the Board mode: the columns, and the ticket open over them.
// session(id) draws a session's conversation, to talk to its agent.
export default function BoardView({ active, board, load, loadError, openId, onOpen: setOpenId, models, session, onOpenSession, onOpenFile, onLooking }) {
  const [adding, setAdding] = useState(false);
  const [onlyNeeds, setOnlyNeeds] = useState(false);
  const [archived, setArchived] = useState(false);
  const [search, setSearch] = useState("");
  const [labelFilter, setLabelFilter] = useState([]);
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
      act(api.moveTicket(id, stage));
    },
    [byId, act],
  );

  const q = search.trim().toLowerCase();
  const shown = board.tickets.filter(
    (t) =>
      (archived || !t.archived) &&
      (!onlyNeeds || needs.includes(t)) &&
      (!labelFilter.length || labelFilter.some((l) => t.labels?.includes(l))) &&
      (!q || [t.id, t.title, t.request, ...(t.labels || [])].join("\n").toLowerCase().includes(q)),
  );
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
        <button className="btn" onClick={() => setAdding(true)}>
          <IconPlus size={13} />
          New ticket
        </button>
      </div>
      <div className="board-cols">
        {STAGES.map(([key, label]) => {
          const list = shown.filter((t) => t.stage === key);
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
                  <Card key={t.id} t={t} byId={byId} onOpen={setOpenId} onMove={move} dragging={drag === t.id} onDrag={setDrag} />
                ))}
              </div>
            </section>
          );
        })}
      </div>
      {adding && (
        <NewTicket
          known={known}
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
          onOpenSession={onOpenSession}
          onOpenTicket={setOpenId}
          onLooking={onLooking}
          onOpenFile={onOpenFile}
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

export function NewTicket({ known, onClose, onCreate }) {
  const [title, setTitle] = useState("");
  const [request, setRequest] = useState("");
  const [labels, setLabels] = useState([]);
  const [images, setImages] = useState([]);
  const [stage, setStage] = useState("inbox");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const create = () => {
    if (!title.trim() || busy) return;
    setBusy(true);
    onCreate({ title, request, labels, stage, images }).catch((e) => (setError(e.message), setBusy(false)));
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
        <span>Starts in</span>
        <div className="board-chips">
          {STARTS.map(([k, l]) => (
            <button key={k} className={cx("board-chip", k === stage && "on")} onClick={() => setStage(k)}>
              {l}
            </button>
          ))}
        </div>
        <div className="board-hint">{STARTS.find((s) => s[0] === stage)[2]}</div>
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

export function SendBack({ id, stage, onClose, onSend }) {
  const [note, setNote] = useState("");
  return (
    <Modal onClose={onClose} className="board-form">
      <div className="board-form-head">
        Send {id} back to {LABEL[stage]}
      </div>
      <label className="board-field">
        <span>What needs another look</span>
        <textarea autoFocus rows={3} value={note} onChange={(e) => setNote(e.target.value)} placeholder="The agent picks up from here" />
      </label>
      <div className="board-form-foot">
        <span className="spacer" />
        <button className="btn" onClick={onClose}>
          Cancel
        </button>
        <button className="primary" onClick={() => onSend(note)}>
          Send back
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
  build: ["plan", "builder"],
  "agent-review": ["review", "reviewer"],
  review: ["review", "builder"],
  merged: ["review", "builder"],
};
export const ROLE_NAME = { scope: "Scoper", planner: "Planner", "plan-reviewer": "Plan reviewer", builder: "Builder", reviewer: "Reviewer" };

// stageAgents are the sessions a stage was worked in: the plan's reviewer
// beside its planner, the code's reviewer beside its builder.
export function stageAgents(t, stage) {
  const s = t.sessions || {};
  const list = [];
  const add = (role, sid) => sid && !list.some((a) => a.sid === sid) && list.push({ role, sid, label: ROLE_NAME[role] });
  if (stage === "plan") {
    add("planner", s.planner);
    add("plan-reviewer", t.planReview?.session || s["plan-reviewer"]);
  } else if (stage === "agent-review") {
    add("reviewer", s.reviewer);
    add("builder", s.builder);
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
export function Related({ t, byId, onOpen }) {
  const kids = t.children || [];
  const blocks = Object.values(byId).filter((x) => x.after?.includes(t.id)).map((x) => x.id);
  const groups = [
    t.parent && ["Part of", [t.parent]],
    kids.length && ["Sub-tickets", kids],
    t.after?.length && ["Waits for", t.after],
    blocks.length && ["Holds up", blocks],
  ].filter(Boolean);
  if (!groups.length) return null;
  return (
    <div className="board-related">
      {groups.map(([label, ids]) => (
        <span key={label} className="board-related-group">
          <span className="board-muted">{label}</span>
          {ids.map((id) => (
            <button key={id} className="board-related-link" title={byId[id]?.title} onClick={() => onOpen(id)}>
              <span className="board-id">{id}</span>
              <span className="board-related-title">{byId[id]?.title}</span>
              {byId[id] && <span className="board-muted">· {LABEL[byId[id].stage]}</span>}
            </button>
          ))}
        </span>
      ))}
    </div>
  );
}

function TicketModal({ id, summary, byId, known, drives, models, session, onOpenSession, onOpenTicket, onOpenFile, onLooking, onClose, onMove, act }) {
  const data = useTicket(id, summary);
  const t = data.ticket;
  // The strip is the way round a ticket: a stage shows its document and its
  // agent together. An earlier round's reviewer is picked from the review.
  const [view, setView] = useState(firstView(t));
  const [chosen, setChosen] = useState(null); // a session picked within the stage
  const shown = canView(t, view) ? view : t.stage;
  const agents = stageAgents(t, shown);
  const earlier = [
    ...(t.rounds || []).map((r, i) => ({ sid: r.session, label: `Round ${i + 1}'s reviewer` })),
    ...(t.earlierPlanReviews || []).map((r, i) => ({ sid: r.session, label: `Plan review ${i + 1}'s reviewer` })),
  ].filter((r) => r.sid && !agents.some((a) => a.sid === r.sid));
  const current = [...agents, ...earlier].find((a) => a.sid === chosen) || agents.find((a) => a.role === stageAgent(t, shown)) || agents[0];
  const sid = current?.sid;
  const inbox = t.stage === "inbox";
  // What the session shown here says is seen here, not raised as a notice.
  useEffect(() => {
    onLooking?.(sid || null);
    return () => onLooking?.(null);
  }, [sid, onLooking]);
  const pick = (stage) => {
    setView(stage);
    setChosen(null);
  };

  return (
    <Modal onClose={onClose} wide={!inbox} className={cx("board-ticket", inbox && "narrow")}>
      <div className="board-ticket-head">
        <span className="board-id">{t.id}</span>
        <span className="board-ticket-title">{t.title}</span>
        <PRBadge t={t} />
        {t.branch && <span className="board-branch">{t.branch}</span>}
        <span className="spacer" />
        {drives && (
          <button
            className="btn"
            title={t.archived ? "Put it back on the board where it was" : "Put it out of the board's way; its agents stop, its worktree stays"}
            onClick={() => act(api.patchTicket(t.id, { archived: !t.archived }))}
          >
            {t.archived ? "Restore" : "Archive"}
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
        <LabelEditor labels={t.labels} known={known} onChange={(labels) => act(api.patchTicket(t.id, { labels }))} />
        <Related t={t} byId={byId} onOpen={onOpenTicket} />
      </div>
      <Steps t={t} shown={shown} onPick={pick} />
      <div className={cx("board-split", inbox && "single")}>
        <div className="board-doc-pane">
          <div className="board-doc-body">
            <Doc t={t} docs={data.docs} versions={data.versions} tab={STAGE_VIEW[shown][0]} models={models} drives={drives} act={act} onSession={setChosen} onOpenFile={onOpenFile} />
          </div>
        </div>
        {!inbox && (
          <div className="board-session-pane">
            <div className="board-pane-head">
              {/* A stage two agents work in names both, to switch between. */}
              {agents.length > 1 && !earlier.includes(current) ? (
                <span className="board-agents">
                  {agents.map((a) => (
                    <button key={a.sid} className={cx("btn", a === current && "on")} onClick={() => setChosen(a.sid)}>
                      {a.role === t.owner && shown === t.stage && t.working && <span className="board-spin" />}
                      {a.label}
                    </button>
                  ))}
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
              <span className="spacer" />
              {sid && (
                <button className="btn" onClick={() => onOpenSession(sid)} title="Open this session in the Agent view">
                  Open in Agent view
                </button>
              )}
            </div>
            <div className="board-session">
              {sid ? session(sid) : <div className="board-empty">{shown === t.stage ? "No agent has started on this stage yet." : "No agent worked on this stage."}</div>}
            </div>
          </div>
        )}
      </div>
      <Foot t={t} drives={drives} onMove={onMove} act={act} />
    </Modal>
  );
}

// Steps is the stage strip: how far the ticket is, and which stage is shown.
export function Steps({ t, shown, onPick }) {
  const now = INDEX[t.stage];
  const reached = INDEX[t.reached] ?? -1;
  const held = t.stage === "plan" && t.question;
  return (
    <div className="board-steps">
      {STAGES.map(([k, l], i) => {
        const skip = i > 0 && i < INDEX[t.started];
        const cls = skip ? "skip" : i === now ? "now" : held && k === "build" ? "held" : i < now || i <= reached ? "past" : "";
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

const AUTHOR = { ticket: ["scope", "scope", "scoper"], plan: ["planner", "plan", "planner"] };

// Commentable is a ticket's document the reader can point at, as a file in
// review is: selecting some of it opens a comment box under its block, where
// the comment then stays; or a comment on the whole. They gather until sent
// together to the agent writing it, which revises it and hands it back.
function Commentable({ t, doc, text, drives, act, onOpenFile }) {
  const [role, stage, who] = AUTHOR[doc];
  const can = drives && !t.archived && t.stage === stage && !!t.sessions?.[role];
  const lines = useMemo(() => text.split("\n"), [text]);
  const [pending, setPending] = useState([]); // { key, end, quote, text }, end 0 for the whole
  const [composing, setComposing] = useState(null);
  const [draft, setDraft] = useState(null); // { quote, text }, a comment on the whole
  const startComment = useCallback(
    (side, start, end, selected, excerpt) => setComposing({ side, start, end, quote: lines.slice(start - 1, end), selected, excerpt }),
    [lines],
  );
  const keep = (c) => setPending((p) => [...p, { key: String(Date.now() + Math.random()), ...c }]);
  const remove = (c) => setPending((p) => p.filter((x) => x !== c));
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
  const send = () => act(api.commentTicket(t.id, doc, pending.map(({ quote, text }) => ({ quote, text }))).then(() => setPending([])));
  return (
    <div className="board-commentable">
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
                comment on the whole {doc}
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
export function Doc({ t, docs, versions, tab, models, drives, act, onSession, onOpenFile: open }) {
  const at = t.worktree ? t.worktree.slice(t.worktree.lastIndexOf("/.dv/") + 1) + "/" : "";
  const onOpenFile = open && ((path, line) => open(at + path, line));
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
        {t.request && (
          <>
            <h4>Original request</h4>
            <div className="board-orig">{t.request}</div>
          </>
        )}
        <TicketImages t={t} drives={drives} act={act} />
        {docs?.ticket ? (
          <Versioned doc="ticket" text={docs.ticket} versions={versions?.ticket} onOpenFile={onOpenFile}>
            {(text) => (
              <Commentable t={t} doc="ticket" text={text} drives={drives} act={act} onOpenFile={onOpenFile} />
            )}
          </Versioned>
        ) : (
          <p className="board-muted">{t.stage === "inbox" ? "Scope turns this into the full ticket: the problem, what's out, edge cases, and how you'll know it's done." : "The scoper hasn't written the ticket yet."}</p>
        )}
      </div>
    );
  }
  if (tab === "plan") {
    return (
      <div className="board-doc">
        <PlanReview t={t} models={models} drives={drives} act={act} onSession={onSession} />
        {t.question && (
          <div className="board-box open">
            <div className="board-box-head">Question from Build · the build waits</div>
            <p>{t.question.text}</p>
          </div>
        )}
        {(t.answered || []).map((q, i) => (
          <div key={i} className="board-box">
            <div className="board-box-head">Added during build · {q.by === "you" ? "answered by you" : `answered by the planner from ${q.source}`}</div>
            <p>
              <b>The builder asked:</b> {q.text}
            </p>
            <p>
              <b>Answer:</b> {q.answer}
            </p>
          </div>
        ))}
        {docs?.plan ? (
          <Versioned doc="plan" text={docs.plan} versions={versions?.plan} onOpenFile={onOpenFile}>
            {(text) => (
              <Commentable t={t} doc="plan" text={text} drives={drives} act={act} onOpenFile={onOpenFile} />
            )}
          </Versioned>
        ) : (
          <p className="board-muted">The planner hasn't written the plan yet.</p>
        )}
      </div>
    );
  }
  return <Review t={t} models={models} drives={drives} act={act} onSession={onSession} />;
}

// modelName names a model off the list too, as a family's bare name.
const modelName = (id) => {
  const name = prettyModel(id);
  return name ? name[0].toUpperCase() + name.slice(1) : "Default model";
};

const PLAN_PHASE = { review: "Another model is reading the plan", judge: "The planner is weighing the findings", done: "Reviewed" };

// PlanReview is another model's reading of the plan, and the planner's answer
// to each finding; before a plan is handed on, whether there is one and who
// does it.
function PlanReview({ t, models, drives, act, onSession }) {
  const menu = useModelMenu(models || [], t.model?.["plan-reviewer"] || "");
  const [open, setOpen] = useState(false);
  const r = t.planReview;
  const can = t.stage === "plan" && drives && !t.archived;
  if (!r && !can) return null;
  const model = t.model?.["plan-reviewer"];
  const picker = (
    <ModelPicker
      label={model ? menu.find((m) => m.id === model)?.label || modelName(model) : "Another model"}
      models={menu}
      model={model || ""}
      onPick={({ model }) => model !== undefined && act(api.patchTicket(t.id, { model: { ...t.model, "plan-reviewer": model } }))}
    />
  );
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

function Review({ t, models, drives, act, onSession }) {
  const menu = useModelMenu(models || [], t.model?.reviewer || "");
  const picked = t.templates?.length ? t.templates : Object.keys(TEMPLATES);
  const label = menu.find((m) => m.id === (t.model?.reviewer || ""))?.label || "Default model";
  const toggle = (k) => {
    const next = picked.includes(k) ? picked.filter((x) => x !== k) : [...picked, k];
    if (next.length) act(api.patchTicket(t.id, { templates: next }));
  };
  return (
    <div className="board-doc">
      {drives && t.stage !== "merged" && (
        <div className="board-settings">
          <span>Next round</span>
          <div className="board-chips">
            {Object.entries(TEMPLATES).map(([k, l]) => (
              <button key={k} className={cx("board-chip", picked.includes(k) && "on")} onClick={() => toggle(k)}>
                {l}
              </button>
            ))}
          </div>
          <span>Reviewer</span>
          <ModelPicker label={label} models={menu} model={t.model?.reviewer || ""} onPick={({ model }) => model !== undefined && act(api.patchTicket(t.id, { model: { ...t.model, reviewer: model } }))} />
        </div>
      )}
      {!t.rounds?.length && <p className="board-muted">No review round has run yet.</p>}
      {(t.rounds || []).map((r, i) => (
        <div key={i} className="board-round">
          <div className="board-round-head">
            Round {i + 1}
            <span className="board-muted">
              {(r.templates || []).map((k) => TEMPLATES[k] || k).join(", ")}
              {r.model ? ` · ${menu.find((m) => m.id === r.model)?.label || r.model}` : ""} · {PHASE[r.phase] || r.phase}
            </span>
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
      ))}
    </div>
  );
}

export function Foot({ t, drives, onMove, act }) {
  if (!drives) return <div className="board-foot"><span className="board-muted">Open the main checkout's dv to move this ticket.</span></div>;
  if (t.archived) return <div className="board-foot"><span className="board-muted">Archived. Restore it to move it again.</span></div>;
  const to = (stage, label, primary) => (
    <button key={stage + label} className={primary ? "primary" : "btn"} onClick={() => onMove(t.id, stage)}>
      {label}
    </button>
  );
  const back = (stage) => to(stage, `Send back to ${LABEL[stage]}`);
  const round = (
    <button key="round" className="btn" onClick={() => act(api.runRound(t.id))}>
      Run another round
    </button>
  );
  let hint = "";
  let buttons = [];
  switch (t.stage) {
    case "inbox":
      hint = "Nothing runs until you move it.";
      buttons = [to("plan", "Start at Plan"), to("scope", "Move to Scope", true)];
      break;
    case "scope":
      if (t.split) hint = "Its sub-tickets carry the work; it closes when they are merged.";
      else buttons = [to("plan", t.children?.length ? "Approve the split" : "Approve ticket", t.ready && !t.working)];
      break;
    case "plan":
      if (t.question) hint = "The build resumes once the question is answered.";
      else buttons = [INDEX[t.started] <= INDEX.scope && back("scope"), to("build", "Approve plan", t.ready && !t.working)].filter(Boolean);
      break;
    case "build":
      buttons = [back("plan")];
      break;
    case "agent-review":
      buttons = [round, to("review", "Skip to your review")];
      break;
    case "review":
      if (t.merging) hint = "Merges once CI passes.";
      buttons = [back("build"), round, !t.merging && to("merged", "Approve, merge when CI passes", true)].filter(Boolean);
      break;
  }
  return (
    <div className="board-foot">
      {t.problem && <span className="board-error">{t.problem}</span>}
      <span className="spacer" />
      {hint && <span className="board-muted">{hint}</span>}
      {buttons}
    </div>
  );
}
