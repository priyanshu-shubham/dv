// The Agent tab's list: what waits on you first - in any of the hub's folders -
// then what is at work here, then the rest open here, then what is open in the
// other folders, then the rest. A row opens its session; held, it offers what
// else can be done to it; swiped aside, it closes.
import { useState } from "react";
import { AgentIcon } from "../icons.jsx";
import { agentName, byLastSent, heldElsewhere, openFolderSession, cx, limitWindow, relTime, resetsWhen } from "../util.js";
import { Icon } from "./icons.jsx";
import { Row, Sheet, useLongPress, useSwipe } from "./Sheet.jsx";
import { Marks } from "./Session.jsx";
import { say, useOpenIn } from "../Notices.jsx";

const isOpen = (s) => s.open || s.running === "dv";

export default function Sessions({ sessions, requests, elsewhere, available, usage, onOpen, onClose, onRename }) {
  const [held, setHeld] = useState(null); // the session long-pressed
  const [renaming, setRenaming] = useState(null);
  const [limits, setLimits] = useState(false);
  const [away, closeAway] = useOpenIn(elsewhere);
  const asking = new Map();
  for (const r of requests) if (!asking.has(r.session)) asking.set(r.session, r);

  const here = sessions.filter((s) => !asking.has(s.id));
  const working = here.filter((s) => s.busy && isOpen(s)).sort(byLastSent);
  const open = here.filter((s) => isOpen(s) && !s.busy).sort(byLastSent);
  const recent = here.filter((s) => !isOpen(s));

  const row = (s, { req, f } = {}) => (
    <SessionRow
      key={(f ? f.slug + "/" : "") + s.id}
      s={s}
      req={req}
      folder={f?.name}
      onOpen={() => (f ? openFolderSession(f.slug, s.id) : onOpen(s.id))}
      onHold={f ? null : () => setHeld(s)}
      onSwipe={f ? () => closeAway(f, s) : isOpen(s) ? () => onClose(s.id) : null}
    />
  );

  if (!available) {
    return (
      <div className="p-empty">
        <h2>Neither Claude Code nor Codex is installed</h2>
        <p>The Agent tab runs the claude or codex CLI, and neither is on the PATH of the computer dv runs on.</p>
      </div>
    );
  }

  const waiting = [
    ...sessions
      .filter((s) => asking.has(s.id))
      .sort(byLastSent)
      .map((s) => row(s, { req: asking.get(s.id) })),
    ...away.filter((a) => a.req).map((a) => row(a.s, a)),
  ];
  const openAway = away.filter((a) => !a.req);
  const plans = usage.filter((u) => u.limits?.session || u.limits?.week);
  return (
    <div className="p-scroll p-sessions">
      {plans.length > 0 && (
        <button className="p-limits" onClick={() => setLimits(true)}>
          {plans.map((u) => (
            <span key={u.agent} className="p-limits-row">
              <AgentIcon agent={u.agent} size={14} />
              {SLOTS.map(([k, m]) => (
                <Limit key={k} limit={u.limits[k]} minutes={m} />
              ))}
            </span>
          ))}
        </button>
      )}
      {limits && (
        <Sheet title="Plan usage" onClose={() => setLimits(false)}>
          {plans.map((u) =>
            SLOTS.map(
              ([k, m]) =>
                u.limits[k] && (
                  <Row
                    key={u.agent + k}
                    lead={<AgentIcon agent={u.agent} size={20} />}
                    title={`${agentName(u.agent)} · ${limitWindow(u.limits[k], m)[1]}`}
                    sub={u.limits[k].resetsAt && "Resets " + resetsWhen(u.limits[k].resetsAt)}
                    trail={<Limit limit={u.limits[k]} />}
                  />
                ),
            ),
          )}
        </Sheet>
      )}
      {waiting.length > 0 && (
        <>
          <div className="p-sec">
            Needs you{(elsewhere || []).length > 0 && <span className="p-sec-aside">In every folder</span>}
          </div>
          {waiting}
        </>
      )}
      {working.length > 0 && <div className="p-sec">Working</div>}
      {working.map((s) => row(s))}
      {open.length > 0 && <div className="p-sec">Open</div>}
      {open.map((s) => row(s))}
      {openAway.length > 0 && <div className="p-sec">Other folders</div>}
      {openAway.map((a) => row(a.s, a))}
      {recent.length > 0 && <div className="p-sec">Recent</div>}
      {recent.map((s) => row(s))}
      {!sessions.length && !away.length && <div className="p-empty">No sessions here yet. Start one with New session.</div>}
      <div className="p-fab-room" />

      {held && (
        <Sheet onClose={() => setHeld(null)}>
          <div className="p-preview">{held.title || held.prompt || "New session"}</div>
          {!heldElsewhere(held) && (
            <Row
              icon="edit"
              title="Rename"
              onClick={() => {
                setRenaming(held);
                setHeld(null);
              }}
            />
          )}
          <Row
            icon="copy"
            title="Copy its ID"
            sub={<span className="mono">{held.id}</span>}
            onClick={() => {
              navigator.clipboard?.writeText(held.id).then(() => say("Copied"), () => {});
              setHeld(null);
            }}
          />
          {isOpen(held) && (
            <Row
              icon="x"
              title="Close"
              sub={held.temporary ? "It's temporary, so it leaves the list" : "It stays in Recent"}
              onClick={() => {
                onClose(held.id);
                setHeld(null);
              }}
            />
          )}
        </Sheet>
      )}
      {renaming && <Rename s={renaming} onRename={onRename} onClose={() => setRenaming(null)} />}
    </div>
  );
}

// SessionRow is one session: its agent, its name, the last thing said or what
// it asks, and how long ago. Swiped past a third of its width, it closes.
function SessionRow({ s, req, folder, onOpen, onHold, onSwipe }) {
  const { ref, dx, armed, touch } = useSwipe(onSwipe);
  const hold = useLongPress(() => onHold?.());
  const status = req ? (
    <span className="p-badge-text warn">Needs you</span>
  ) : s.busy && isOpen(s) ? (
    <span className="p-dot busy" />
  ) : null;
  const last = s.last && s.last.replace(/\*\*|__|`/g, "");
  const what = req ? `Wants to ${req.headline}` : last ? (s.lastBy === "you" ? `You: ${last}` : last) : s.prompt;
  const when = s.running === "terminal" ? "terminal" : s.busy && isOpen(s) ? s.status || "working" : relTime(s.updated);

  return (
    <div className={cx("p-swipe", armed && "armed")} ref={ref}>
      {onSwipe && (
        <div className="p-swipe-bg" aria-hidden="true">
          <Icon name="x" />
          <Icon name="x" />
        </div>
      )}
      <button
        className={cx("p-row p-session", dx !== 0 && "moving")}
        style={dx ? { transform: `translateX(${dx}px)` } : undefined}
        onClick={() => dx === 0 && onOpen()}
        {...(onHold ? hold : {})}
        {...touch}
      >
        <span className={cx("p-lead", s.agent === "codex" ? "codex" : "claude")}>
          <AgentIcon agent={s.agent} size={18} />
        </span>
        <span className="p-row-body">
          <span className="p-row-top">
            <span className="p-row-title">{s.title || s.prompt || "New session"}</span>
            <Marks s={s} />
            <span className="p-when">{when}</span>
          </span>
          <span className="p-row-sub clamp2">
            {status}
            {folder && <span className="p-fold">{folder} · </span>}
            {what}
          </span>
        </span>
      </button>
    </div>
  );
}

// The plan's limits, each with the window its place stands for when the agent
// doesn't say.
const SLOTS = [
  ["session", 5 * 60],
  ["week", 7 * 24 * 60],
];

// Limit is how much of one of the plan's usage limits is spent, labelled with
// its window when given its place's.
function Limit({ limit, minutes }) {
  if (!limit) return null;
  const pct = Math.min(100, Math.round(limit.percent));
  return (
    <span className={cx("p-limit", pct >= 90 ? "high" : pct >= 75 && "warn")}>
      {minutes && <span className="p-limit-label">{limitWindow(limit, minutes)[0]}</span>}
      <span className="p-meter">
        <span style={{ width: `${pct}%` }} />
      </span>
      {pct}%
    </span>
  );
}

export function Rename({ s, onRename, onClose }) {
  const [title, setTitle] = useState(s.title || s.prompt || "");
  const save = () => {
    if (title.trim()) onRename(s.id, title.trim());
    onClose();
  };
  return (
    <Sheet
      title="Rename"
      onClose={onClose}
      foot={
        <>
          <button className="p-btn text" onClick={onClose}>
            Cancel
          </button>
          <button className="p-btn filled" onClick={save}>
            Save
          </button>
        </>
      }
    >
      <input className="p-field" value={title} autoFocus onChange={(e) => setTitle(e.target.value)} onKeyDown={(e) => e.key === "Enter" && save()} />
    </Sheet>
  );
}
