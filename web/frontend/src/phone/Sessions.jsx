// The Agent tab's list: what waits on you first - in any of the hub's folders -
// then what is at work here, then the rest open here, then what is open in
// this repository's worktrees, then in other folders, then the rest. A row opens its session; held, it offers what
// else can be done to it; swiped aside, it closes, or leaves the list if closed.
import { useState } from "react";
import { AgentIcon } from "../icons.jsx";
import { secretsInText } from "../secrets.js";
import { agentName, byLastSent, heldElsewhere, openFolderSession, cx, limitWindow, plainText, relTime, resetsWhen } from "../util.js";
import { Icon } from "./icons.jsx";
import { Row, Sheet, useLongPress, useSwipe } from "./Sheet.jsx";
import { Marks } from "./Session.jsx";
import { say, useOpenIn } from "../Notices.jsx";

const isOpen = (s) => s.open || s.running === "dv";

export default function Sessions({ sessions, requests, elsewhere, repoRoot, available, usage, onOpen, onClose, onRemove, onRename }) {
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

  // A worktree of this repository is told from the others by its branch.
  const sibling = (f) => !!repoRoot && f.repoRoot === repoRoot;
  const row = (s, { req, f } = {}) => (
    <SessionRow
      key={(f ? f.slug + "/" : "") + s.id}
      s={s}
      req={req}
      folder={f && (sibling(f) && f.branch ? f.branch : f.name)}
      onOpen={() => (f ? openFolderSession(f.slug, s.id) : onOpen(s.id))}
      onHold={f ? null : () => setHeld(s)}
      onSwipe={f ? () => closeAway(f, s) : isOpen(s) ? () => onClose(s.id) : () => onRemove(s.id)}
      removes={!f && !isOpen(s)}
    />
  );

  if (!available) {
    return (
      <div className="p-empty">
        <h2>Claude Code is not installed</h2>
        <p>The Agent tab runs the claude CLI, which is not on the PATH of the computer dv runs on.</p>
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
  const worktrees = openAway.filter((a) => sibling(a.f));
  const others = openAway.filter((a) => !sibling(a.f));
  const plans = usage.filter((u) => u.limits?.session || u.limits?.week);
  return (
    <div className="p-scroll p-sessions">
      {plans.length > 0 && (
        <button className="p-limits" onClick={() => setLimits(true)}>
          {plans.map((u) => (
            <span key={u.agent} className="p-limits-row">
              <AgentIcon agent={u.agent} size={14} />
              {slots(u.limits).map(([k, l, m]) => (
                <Limit key={k} limit={l} minutes={m} />
              ))}
            </span>
          ))}
        </button>
      )}
      {limits && (
        <Sheet title="Plan usage" onClose={() => setLimits(false)}>
          {plans.map((u) =>
            slots(u.limits).map(([k, l, m]) => (
              <Row
                key={u.agent + k}
                lead={<AgentIcon agent={u.agent} size={20} />}
                title={`${l.model || agentName(u.agent)} · ${limitWindow(l, m)[1]}`}
                sub={l.resetsAt && "Resets " + resetsWhen(l.resetsAt)}
                trail={<Limit limit={l} />}
              />
            ))
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
      {worktrees.length > 0 && <div className="p-sec">Worktrees</div>}
      {worktrees.map((a) => row(a.s, a))}
      {others.length > 0 && <div className="p-sec">Other folders</div>}
      {others.map((a) => row(a.s, a))}
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
          <Row
            icon="trash"
            title="Remove from the list"
            sub={isOpen(held) ? "Closes it; its transcript stays" : "Its transcript stays"}
            onClick={() => {
              onRemove(held.id);
              setHeld(null);
            }}
          />
        </Sheet>
      )}
      {renaming && <Rename s={renaming} onRename={onRename} onClose={() => setRenaming(null)} />}
    </div>
  );
}

// SessionRow is one session: its agent, its name, the last thing said or what
// it asks, and how long ago. Swiped past a third of its width, it closes, or
// when closed already, leaves the list.
function SessionRow({ s, req, folder, onOpen, onHold, onSwipe, removes }) {
  const { ref, dx, armed, touch } = useSwipe(onSwipe);
  const hold = useLongPress(() => onHold?.());
  const status = req ? (
    <span className="p-badge-text warn">Needs you</span>
  ) : s.busy && isOpen(s) ? (
    <span className="p-dot busy" />
  ) : null;
  const last = plainText(s.last);
  const what = req ? `Wants to ${req.headline}` : last ? (s.lastBy === "you" ? `You: ${last}` : last) : s.prompt;
  const when = s.running === "terminal" ? "terminal" : s.busy && isOpen(s) ? s.status || "working" : relTime(s.updated);

  return (
    <div className={cx("p-swipe", armed && "armed")} ref={ref}>
      {onSwipe && (
        <div className="p-swipe-bg" aria-hidden="true">
          <Icon name={removes ? "trash" : "x"} />
          <Icon name={removes ? "trash" : "x"} />
        </div>
      )}
      <button
        className={cx("p-row p-session", dx !== 0 && "moving")}
        style={dx ? { transform: `translateX(${dx}px)` } : undefined}
        onClick={() => dx === 0 && onOpen()}
        {...(onHold ? hold : {})}
        {...touch}
      >
        <span className="p-lead claude">
          <AgentIcon size={18} />
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
            {secretsInText(what)}
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

// slots are a plan's limits as [key, limit, minutes], a model's own after the
// plan's.
function slots(limits) {
  return [
    ...SLOTS.filter(([k]) => limits[k]).map(([k, m]) => [k, limits[k], m]),
    ...(limits.models || []).map((l) => ["model:" + l.model, l, 7 * 24 * 60]),
  ];
}

// Limit is how much of one of the plan's usage limits is spent, labelled with
// its window when given its place's.
function Limit({ limit, minutes }) {
  if (!limit) return null;
  const pct = Math.min(100, Math.round(limit.percent));
  return (
    <span className={cx("p-limit", pct >= 90 ? "high" : pct >= 75 && "warn")}>
      {minutes && <span className="p-limit-label">{limit.model || limitWindow(limit, minutes)[0]}</span>}
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
