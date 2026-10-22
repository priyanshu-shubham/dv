// The Agent tab's list: what waits on you first - in any of the hub's folders -
// then what is at work here, then the rest. A row opens its session; held, it
// offers what else can be done to it; swiped aside, it closes.
import { useRef, useState } from "react";
import { AgentIcon } from "../icons.jsx";
import { agentName, openFolderSession, cx, limitWindow, relTime, resetsWhen } from "../util.js";
import { Icon } from "./icons.jsx";
import { Row, Sheet, useLongPress } from "./Sheet.jsx";
import { Marks } from "./Session.jsx";
import { say } from "../Notices.jsx";

const isOpen = (s) => s.open || s.running === "dv";

export default function Sessions({ sessions, requests, elsewhere, available, usage, onOpen, onClose, onRename }) {
  const [held, setHeld] = useState(null); // the session long-pressed
  const [renaming, setRenaming] = useState(null);
  const [limits, setLimits] = useState(false);
  const asking = new Map();
  for (const r of requests) if (!asking.has(r.session)) asking.set(r.session, r);

  // Waiting in other folders: their rows, and what each asks.
  const away = [];
  for (const f of elsewhere || []) {
    for (const r of f.requests || []) {
      if (away.some((a) => a.folder === f.slug && a.session.id === r.session)) continue;
      const s = (f.sessions || []).find((x) => x.id === r.session) || { id: r.session, title: "", prompt: "" };
      away.push({ folder: f.slug, name: f.name, session: s, req: r });
    }
  }

  const here = sessions.filter((s) => !asking.has(s.id));
  const working = here.filter((s) => s.busy && isOpen(s));
  // Last written first, as a chat app's are; those at work hold still, or two
  // writing at once would trade places.
  const open = here.filter((s) => isOpen(s) && !s.busy).sort((a, b) => Date.parse(b.updated) - Date.parse(a.updated));
  const recent = here.filter((s) => !isOpen(s));

  const row = (s, { req, folder, name } = {}) => (
    <SessionRow
      key={(folder || "") + s.id}
      s={s}
      req={req}
      folder={name}
      onOpen={() => (folder ? openFolderSession(folder, s.id) : onOpen(s.id))}
      onHold={folder ? null : () => setHeld(s)}
      onSwipe={folder || !isOpen(s) ? null : () => onClose(s.id)}
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

  const waiting = [...sessions.filter((s) => asking.has(s.id)).map((s) => row(s, { req: asking.get(s.id) })), ...away.map((a) => row(a.session, a))];
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
      {recent.length > 0 && <div className="p-sec">Recent</div>}
      {recent.map((s) => row(s))}
      {!sessions.length && !away.length && <div className="p-empty">No sessions here yet. Start one with New session.</div>}
      <div className="p-fab-room" />

      {held && (
        <Sheet onClose={() => setHeld(null)}>
          <div className="p-preview">{held.title || held.prompt || "New session"}</div>
          {held.running !== "terminal" && (
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
  const ref = useRef(null);
  const swipe = useRef(null);
  const [dx, setDx] = useState(0);
  const hold = useLongPress(() => onHold?.());
  const status = req ? (
    <span className="p-badge-text warn">Needs you</span>
  ) : s.busy && isOpen(s) ? (
    <span className="p-dot busy" />
  ) : null;
  const last = s.last && s.last.replace(/\*\*|__|`/g, "");
  const what = req ? `Wants to ${req.headline}` : last ? (s.lastBy === "you" ? `You: ${last}` : last) : s.prompt;
  const when = s.running === "terminal" ? "terminal" : s.busy && isOpen(s) ? s.status || "working" : relTime(s.updated);

  const touch = onSwipe && {
    onTouchStart: (e) => (swipe.current = { x: e.touches[0].clientX, y: e.touches[0].clientY, on: false }),
    onTouchMove: (e) => {
      const w = swipe.current;
      if (!w) return;
      const x = e.touches[0].clientX - w.x;
      const y = e.touches[0].clientY - w.y;
      if (!w.on) {
        if (Math.abs(y) > 12) return void (swipe.current = null);
        if (Math.abs(x) < 12) return;
        w.on = true;
      }
      setDx(x);
    },
    onTouchEnd: () => {
      const w = swipe.current;
      swipe.current = null;
      if (!w?.on) return;
      if (Math.abs(dx) > (ref.current?.offsetWidth || 360) / 3) {
        navigator.vibrate?.(10);
        setDx(Math.sign(dx) * 999);
        setTimeout(() => (onSwipe(), setDx(0)), 160);
      } else setDx(0);
    },
    // Taken by the system - Android's edge swipe for Back - it settles back.
    onTouchCancel: () => {
      swipe.current = null;
      setDx(0);
    },
  };
  const armed = Math.abs(dx) > (ref.current?.offsetWidth || 360) / 3;

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

function Rename({ s, onRename, onClose }) {
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
