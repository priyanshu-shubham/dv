// The Actions sheet, ⚡ in the phone app: the reader's own actions first, a
// command's running shown on its row with a Stop; then dv's own, the ones
// most reached for as tiles and the rest as rows. ActionsPalette is the
// desktop's.
import { useState } from "react";
import { api } from "../api.js";
import { builtIns, describe, elapsed, ready, useActions, useAgentChoices, useRunning } from "../Actions.jsx";
import { say } from "../Notices.jsx";
import { cx } from "../util.js";
import { Icon } from "./icons.jsx";
import { Sheet } from "./Sheet.jsx";

// The page's own as tiles, by id: [label, icon].
const TILES = {
  "new-session": ["New session", "edit"],
  comments: ["Comments", "comment"],
  notes: ["Notes", "note"],
  pad: ["Pad", "pad"],
  "new-note": ["New note", "plus"],
  branch: ["Branch", "branch"],
};
// And the rest's icons, by id or its start.
const ICONS = { pull: "pull", main: "pull", "new-worktree": "folder", "new-task": "folder", "close-task": "x", scope: "diff", pr: "branch", settings: "settings", restart: "restart", hub: "home" };
const iconOf = (a) => ICONS[a.id] || ICONS[a.id.split("-")[0]] || "bolt";

export default function ActionsSheet({ meta, session, extra, onRun, onEdit, onOutput, onClose }) {
  const { all } = useActions();
  const info = useAgentChoices();
  const { running, ran, at, look } = useRunning();
  const [q, setQ] = useState("");
  const match = (...names) => names.some((n) => n.toLowerCase().includes(q.trim().toLowerCase()));
  const mine = all.filter((a) => match(a.name));
  const tiles = extra.filter((a) => TILES[a.id] && match(a.name, TILES[a.id][0]));
  const rest = [...builtIns(meta), ...extra.filter((a) => !TILES[a.id])].filter((a) => match(a.name));

  // A command keeps the sheet open, to be watched or stopped; the rest go somewhere.
  const run = (a) => {
    if (running[a.id]) return;
    if (!ready(a, session)) return say("Open a session first", "This action goes to the session open in the Agent tab.");
    onRun(a, a.kind === "command");
    if (a.kind === "command") setTimeout(look, 300);
  };

  return (
    <Sheet
      title="Actions"
      className="p-actions"
      onClose={onClose}
      aside={
        <button className="p-icon" onClick={onEdit} aria-label="Edit actions">
          <Icon name="edit" size={22} />
        </button>
      }
      head={
        <label className="p-search-field">
          <Icon name="search" size={22} />
          <input value={q} placeholder="Search actions" autoCapitalize="off" autoCorrect="off" onChange={(e) => setQ(e.target.value)} />
        </label>
      }
      foot={
        <button className="p-btn text" onClick={onEdit}>
          <Icon name="plus" size={18} />
          New action
        </button>
      }
    >
      {mine.length > 0 && (
        <div className="p-sec">
          Yours<span className="p-sec-aside">{mine.length} · tap to run</span>
        </div>
      )}
      {mine.map((a) => (
        <Mine
          key={a.scope + a.id}
          a={a}
          sub={describe(a, info)}
          running={running[a.id] && elapsed(running[a.id].started, at)}
          ran={a.kind === "command" && ran.has(a.id)}
          unready={!ready(a, session)}
          onRun={() => run(a)}
          onStop={() => api.stopAction(a.id).then(look, () => {})}
          onOutput={() => onOutput(a.id)}
        />
      ))}
      {tiles.length > 0 && <div className="p-sec">dv</div>}
      {tiles.length > 0 && (
        <div className="p-tiles">
          {tiles.map((a) => (
            // Open, a panel's tile closes it, as its name says.
            <button key={a.id} className={cx("p-tile", a.name.startsWith("Close") && "on")} onClick={() => onRun(a)}>
              <Icon name={TILES[a.id][1]} size={22} />
              {TILES[a.id][0]}
            </button>
          ))}
        </div>
      )}
      {rest.map((a) => (
        <button key={a.id} className="p-row" onClick={() => onRun(a)}>
          <span className="p-lead">
            <Icon name={iconOf(a)} size={20} />
          </span>
          <span className="p-row-body">
            <span className="p-row-title">{a.name}</span>
            {describe(a, info) && <span className="p-row-sub p-one-line">{describe(a, info)}</span>}
          </span>
        </button>
      ))}
      {!mine.length && !tiles.length && !rest.length && <div className="p-empty">No actions match.</div>}
    </Sheet>
  );
}

// Mine is one of the reader's actions: a command, with what it runs, or a
// prompt, with where it goes; one running counts up beside a Stop.
function Mine({ a, sub, running, ran, unready, onRun, onStop, onOutput }) {
  const command = a.kind === "command";
  return (
    <div role="button" tabIndex={0} className={cx("p-row p-action", unready && "unready")} onClick={onRun}>
      <span className={cx("p-lead", command ? "cmd" : a.agent === "codex" ? "codex" : "claude", running && "running")}>
        <Icon name={command ? "terminal" : "spark"} size={20} />
      </span>
      <span className="p-row-body">
        <span className="p-row-title">{a.name}</span>
        {running ? (
          <span className="p-row-sub">Running · {running}</span>
        ) : unready ? (
          <span className="p-row-sub p-warn">Open a session first</span>
        ) : (
          <span className={cx("p-row-sub p-one-line", command && "p-action-command")}>{command ? a.command.trim().split("\n")[0] : sub}</span>
        )}
      </span>
      {running ? (
        <button className="p-chip stop" onClick={(e) => (e.stopPropagation(), onStop())}>
          <Icon name="stop" size={16} />
          Stop
        </button>
      ) : (
        ran && (
          <button className="p-chip" onClick={(e) => (e.stopPropagation(), onOutput())}>
            <Icon name="fileOn" size={16} />
            Output
          </button>
        )
      )}
    </div>
  );
}
