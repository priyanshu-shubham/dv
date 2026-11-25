// The Board tab: the tickets as a list, what waits on you first, and a
// ticket's screen over it. A ticket's agents are talked to in the session
// screen, as any session is.
import { useCallback, useMemo, useState } from "react";
import { api } from "../api.js";
import { cx } from "../util.js";
import {
  allLabels, byPriority, canView, cardState, createTicket, Doc, firstView, Foot, isQuiet, PriorityMark, LABEL, LabelEditor, Labels, needsYou, NewTicket, NothingNote, PRBadge, StageAge, Related, SendBack, STAGE_VIEW, stageAgent, startEarly, stageAgents, STAGES, StateGlyph, Steps, Suggested, useBoard, useTicket,
} from "../Board.jsx";
import { useBack } from "./back.js";
import { Icon } from "./icons.jsx";

const ORDER = Object.fromEntries(STAGES.map(([k], i) => [k, i]));

export default function PhoneBoard({ shown, models, onOpenSession, onOpenFile }) {
  const [board, load, loadError] = useBoard(shown);
  const [openId, setOpenId] = useState(null);
  const [adding, setAdding] = useState(false);
  const [search, setSearch] = useState("");
  const [labelFilter, setLabelFilter] = useState([]);
  const [archived, setArchived] = useState(false);
  const [back, setBack] = useState(null);
  const [error, setError] = useState("");
  useBack(shown && !!openId, () => setOpenId(null));

  const byId = useMemo(() => Object.fromEntries(board.tickets.map((t) => [t.id, t])), [board.tickets]);
  const known = useMemo(() => allLabels(board.tickets), [board.tickets]);
  const act = useCallback(
    (p) =>
      p.then(
        () => (setError(""), load()),
        (e) => (setError(e.message), load()),
      ),
    [load],
  );
  const move = (id, stage) => {
    const t = byId[id];
    if (ORDER[stage] < ORDER[t.stage] && stage !== "inbox") return setBack({ id, stage });
    const early = t.stage === "inbox" && stage !== "inbox" && t.blocked?.length > 0;
    if (early && !confirm(startEarly(t))) return;
    return act(api.moveTicket(id, stage, "", early));
  };

  const q = search.trim().toLowerCase();
  const rows = board.tickets
    .filter(
      (t) =>
        (archived ? t.archived : !t.archived) &&
        (!labelFilter.length || labelFilter.some((l) => t.labels?.includes(l))) &&
        (!q || [t.id, t.title, t.request, ...(t.labels || [])].join("\n").toLowerCase().includes(q)),
    )
    .map((t) => ({ t, s: cardState(t, byId) }));
  const needs = rows.filter(({ s, t }) => needsYou(s) && !t.archived);
  // A stage's waiting tickets under the rest, as on a computer.
  const groups = STAGES.map(([k, l]) => {
    const list = rows.filter(({ t, s }) => t.stage === k && !(needsYou(s) && !t.archived));
    const prio = (a, b) => byPriority(a.t, b.t);
    // Merged, the latest first: priority is spent by then.
    const at = ({ t }) => t.stageAt || t.updatedAt || "";
    if (k === "merged") return [l, list.sort((a, b) => at(b).localeCompare(at(a)))];
    return [l, [...list.filter(({ s }) => !isQuiet(s)).sort(prio), ...list.filter(({ s }) => isQuiet(s)).sort(prio)]];
  }).filter(([, list]) => list.length);

  const row = ({ t, s }) => (
    <button key={t.id} className={cx("p-row p-ticket", needsYou(s) && !t.archived && "needs")} onClick={() => setOpenId(t.id)}>
      <span className="p-ticket-body">
        <span className="p-ticket-top">
          <span className="board-id">{t.id}</span>
          <span className="p-ticket-stage">{LABEL[t.stage]}</span>
          <PriorityMark p={t.priority} />
          <PRBadge t={t} />
          <StageAge t={t} />
        </span>
        <span className="p-ticket-title">{t.title}</span>
        {s && !t.archived && (
          <span className={cx("board-state", s.kind)}>
            <StateGlyph kind={s.kind} />
            <span className="board-state-text">{s.text}</span>
          </span>
        )}
        <Labels labels={t.labels} />
      </span>
    </button>
  );

  return (
    <div className="p-board">
      <div className="p-board-filters">
        <input className="board-search" type="search" value={search} onChange={(e) => setSearch(e.target.value)} placeholder="Search tickets" />
        <div className="board-chips">
          <button className={cx("board-chip", "small", archived && "on")} onClick={() => setArchived((a) => !a)}>
            Archived
          </button>
          {known.map((l) => (
            <button key={l} className={cx("board-chip", "small", labelFilter.includes(l) && "on")} onClick={() => setLabelFilter((f) => (f.includes(l) ? f.filter((x) => x !== l) : [...f, l]))}>
              {l}
            </button>
          ))}
        </div>
      </div>
      {(error || loadError) && <div className="board-error p-board-error">{error || loadError}</div>}
      {needs.length > 0 && (
        <>
          <div className="p-sec">Needs you</div>
          {needs.map(row)}
        </>
      )}
      {groups.map(([label, list]) => (
        <div key={label}>
          <div className="p-sec">{label}</div>
          {list.map(row)}
        </div>
      ))}
      {!rows.length && <div className="p-empty">{board.tickets.length ? "No ticket matches." : "No tickets yet."}</div>}
      <button className="p-fab" onClick={() => setAdding(true)}>
        <Icon name="plus" />
        New ticket
      </button>

      {adding && (
        <NewTicket
          known={known}
          models={models}
          picks={board.picks}
          onClose={() => setAdding(false)}
          onCreate={(t) =>
            createTicket(t).then((made) => {
              setAdding(false);
              load();
              setOpenId(made.id);
            })
          }
        />
      )}
      <div className={cx("p-screen p-ticket-screen", openId && "open")} aria-hidden={!openId}>
        {openId && byId[openId] && (
          <TicketScreen
            key={openId}
            id={openId}
            summary={byId[openId]}
            byId={byId}
            known={known}
            drives={board.drives}
            models={models}
            onBack={() => setOpenId(null)}
            onOpenSession={onOpenSession}
            onOpenTicket={setOpenId}
            onOpenFile={onOpenFile}
            onMove={move}
            act={act}
          />
        )}
      </div>
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

function TicketScreen({ id, summary, byId, known, drives, models, onBack, onOpenSession, onOpenTicket, onOpenFile, onMove, act }) {
  const data = useTicket(id, summary);
  const t = data.ticket;
  // As on a computer, the strip picks the stage; its agent opens in the
  // session screen, which a phone has no room to show beside the document.
  const [view, setView] = useState(firstView(t));
  const shown = canView(t, view) ? view : t.stage;
  const agents = stageAgents(t, shown);
  const role = stageAgent(t, shown);
  return (
    <>
      <header className="p-bar">
        <button className="p-icon" onClick={onBack} aria-label="Back">
          <Icon name="back" />
        </button>
        <div className="p-title">
          <span className="p-title-main">{t.title}</span>
          <span className="p-title-sub">
            {t.id} · {LABEL[t.stage]}
            {t.archived && " · archived"}
          </span>
        </div>
        {drives && (
          <button className="p-icon" onClick={() => act(api.patchTicket(t.id, { archived: !t.archived }))} aria-label={t.archived ? "Restore" : "Archive"}>
            <Icon name={t.archived ? "undo" : "down"} />
          </button>
        )}
      </header>
      <div className="p-ticket-scroll">
        <div className="p-ticket-labels">
          <LabelEditor labels={t.labels} known={known} onChange={(labels) => act(api.patchTicket(t.id, { labels }))} />
          <Related t={t} byId={byId} onOpen={onOpenTicket} onWaits={drives && !t.archived ? (after) => act(api.patchTicket(t.id, { after })) : undefined} />
        </div>
        <NothingNote t={t} />
        <Suggested t={t} drives={drives} act={act} />
        {t.askingSession && (
          <div className="p-ticket-agents">
            <button className="p-chip on" onClick={() => onOpenSession(t.askingSession)}>
              An agent is waiting on your answer · Open it
            </button>
          </div>
        )}
        <Steps t={t} shown={shown} onPick={setView} />
        {agents.length > 0 && (
          <div className="p-ticket-agents">
            {agents.map((a) => (
              <button key={a.sid} className={cx("p-chip", a.role === role && "on")} onClick={() => onOpenSession(a.sid)}>
                {a.role === t.owner && shown === t.stage && t.working && <span className="board-spin" />}
                Talk to the {a.label.toLowerCase()}
              </button>
            ))}
          </div>
        )}
        <div className="p-ticket-doc">
          <Doc t={t} remote={data.remote} docs={data.docs} versions={data.versions} mockups={data.mockups} parent={data.parent} tab={STAGE_VIEW[shown][0]} models={models} drives={drives} act={act} onSession={onOpenSession} onOpenFile={onOpenFile} changes={data.changes} />
        </div>
      </div>
      <Foot t={t} remote={data.remote} drives={drives} models={models} onMove={onMove} act={act} />
    </>
  );
}
