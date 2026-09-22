import { useCallback, useEffect, useMemo, useState } from "react";
import { api } from "./api.js";
import { usePref } from "./prefs.js";
import { cx } from "./util.js";
import { Modal, OFF_ON, SESSION_MODES, Setting, usePaletteNav } from "./Overlays.jsx";
import { Picker } from "./Picker.jsx";
import { IconBolt, IconPlus } from "./icons.jsx";

// An action is kept to run in a click, from ⚡ at the top or Alt+A: a prompt,
// sent to the session last open in the Agent view or to a new one it sets up,
// which the server can close once its turn is done; or a command, which the
// server runs in the folder. Those of the repository come first, then the
// ones kept for every repository, then dv's own.
const NONE = [];
const BLANK = {
  name: "", kind: "prompt", prompt: "", command: "", scope: "repo", where: "new",
  agent: "", model: "", effort: "", mode: "", temporary: true, close: false,
};
const EDIT = { edit: true };

// builtIns are dv's own, where they apply: pulling the branch checked out,
// and bringing the trunk up to date from another; both only fast-forward.
function builtIns(meta) {
  const branch = meta?.head?.branch;
  const trunk = meta?.defaultBranch;
  if (!meta?.remote) return [];
  return [
    branch && { id: "pull", builtin: "pull", name: `Pull ${branch}` },
    trunk && trunk !== branch && meta.branches?.includes(trunk) && { id: "main", builtin: "main", name: `Update ${trunk}` },
  ].filter(Boolean);
}

function useActions() {
  const [repo, setRepo] = usePref("repo", "actions", NONE);
  const [user, setUser] = usePref("user", "actions", NONE);
  const all = useMemo(() => [...repo.map((a) => ({ ...a, scope: "repo" })), ...user.map((a) => ({ ...a, scope: "user" }))], [repo, user]);
  return { all, set: { repo: setRepo, user: setUser } };
}

// useAgentChoices is what a new session can start on. Claude Code is asked
// for its models by the first listing, so they can come a moment after it.
// The last answer is kept, so the list opens on it rather than redrawing.
let choices = null;
function useAgentChoices() {
  const [info, setInfo] = useState(choices);
  useEffect(() => {
    let live = true;
    let timer;
    const load = (tries) =>
      api.agentSessions().then((d) => {
        if (!live) return;
        choices = d;
        setInfo(d);
        if (tries < 5 && d.available && !d.models?.length) timer = setTimeout(() => load(tries + 1), 600);
      }, () => {});
    load(0);
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, []);
  return info;
}

const modelsFor = (a, info) => (a.agent === "codex" ? info?.codex?.models : info?.models) || [];

// describe is what an action does, in the few words under its name, from the
// action alone where it can be: the model's label is kept with it.
function describe(a, info) {
  if (a.builtin) return "Built in · only when it fast-forwards";
  if (a.kind === "command") return "Runs " + a.command.trim().split("\n")[0];
  if (a.where === "current") return "Current session";
  const model = modelsFor(a, info).find((m) => m.id === a.model);
  return [
    a.temporary ? "New temporary session" : "New session",
    a.agent === "codex" ? "Codex" : "Claude",
    a.model && (a.modelLabel || model?.label || a.model),
    a.effort,
    a.mode && SESSION_MODES.find(([id]) => id === a.mode)?.[1],
    a.close && "closes when done",
  ]
    .filter(Boolean)
    .join(" · ");
}

// runAction sends a to the server, to session when it runs in the current one.
export function runAction(a, session) {
  if (a.kind === "command") return api.runAction({ id: a.id, name: a.name, kind: "command", command: a.command });
  const current = a.where === "current";
  return api.runAction({
    name: a.name,
    prompt: a.prompt,
    session: current ? session : "",
    ...(!current && { agent: a.agent, model: a.model, effort: a.effort, mode: a.mode, temporary: a.temporary, close: a.close }),
  });
}

// useRunning follows the command actions under way, by id, while the list is
// open, with the time it was when they were last looked at.
function useRunning() {
  const [state, setState] = useState({ running: {}, at: Date.now() });
  const look = useCallback(
    () => api.actionsRunning().then((d) => setState({ running: Object.fromEntries(d.running.map((r) => [r.id, r])), at: Date.now() }), () => {}),
    [],
  );
  useEffect(() => {
    look();
    const t = setInterval(look, 1000);
    return () => clearInterval(t);
  }, [look]);
  return { ...state, look };
}

function elapsed(from, at) {
  const s = Math.max(0, Math.round((at - Date.parse(from)) / 1000));
  return s < 60 ? `${s}s` : `${Math.floor(s / 60)}m ${s % 60}s`;
}

// ActionsPalette is Alt+A: the actions, found by name, run with Enter. One for
// the current session waits for there to be one; Enter on a command running
// stops it.
export function ActionsPalette({ meta, session, onRun, onEdit, onClose }) {
  const { all } = useActions();
  const info = useAgentChoices();
  const { running, at, look } = useRunning();
  const [q, setQ] = useState("");
  const shown = [...all, ...builtIns(meta)].filter((a) => a.name.toLowerCase().includes(q.trim().toLowerCase()));
  const rows = [...shown, EDIT];
  const ready = (a) => a.kind === "command" || a.builtin || a.where !== "current" || !!session;
  const stop = (a) => api.stopAction(a.id).then(look, () => {});
  const pick = (row) => (row === EDIT ? onEdit() : running[row.id] ? stop(row) : ready(row) && onRun(row));
  const { sel, setSel, onKey, listRef } = usePaletteNav(rows.length, (i) => pick(rows[i]));
  const note = (a) => {
    if (running[a.id]) return `Running for ${elapsed(running[a.id].started, at)} · Enter stops it`;
    return ready(a) ? describe(a, info) : "Open a session in the Agent view first";
  };
  return (
    <Modal onClose={onClose} className="palette">
      <div className="palette-input">
        <IconBolt size={15} />
        <input
          autoFocus
          value={q}
          placeholder="Run an action..."
          onChange={(e) => {
            setQ(e.target.value);
            setSel(0);
          }}
          onKeyDown={onKey}
        />
      </div>
      <div className="palette-list" ref={listRef}>
        {shown.map((a, i) => (
          <button
            key={(a.scope || "") + a.id}
            className={cx("palette-row", "action-row", i === sel && "on", !ready(a) && "unready", running[a.id] && "running")}
            onMouseMove={() => setSel(i)}
            onClick={() => pick(a)}
            title={a.kind === "command" ? a.command : a.prompt}
          >
            <span className="sym">{a.name}</span>
            <span className="dim">{note(a)}</span>
          </button>
        ))}
        {shown.length === 0 && <div className="empty">{all.length ? "No actions match." : "No actions yet: a prompt or a command you keep here, to run in a click."}</div>}
        <button className={cx("palette-row", "action-edit", sel === shown.length && "on")} onMouseMove={() => setSel(shown.length)} onClick={onEdit}>
          <span className="dim">{all.length ? "Edit actions…" : "Add an action…"}</span>
        </button>
      </div>
    </Modal>
  );
}

// ActionsSettings is Settings' Actions tab: the list, and the form for one.
export function ActionsSettings() {
  const { all, set } = useActions();
  const info = useAgentChoices();
  const [editing, setEditing] = useState(null); // the action as it was, BLANK for a new one
  const save = (a) => {
    const { scope, ...kept } = a;
    const was = editing.scope;
    set[scope]((list) => {
      const i = list.findIndex((x) => x.id === kept.id);
      return i >= 0 ? list.with(i, kept) : [...list, kept];
    });
    if (was && was !== scope) set[was]((list) => list.filter((x) => x.id !== kept.id));
    setEditing(null);
  };
  const remove = (a) => confirm(`Delete the action ${a.name}?`) && set[a.scope]((list) => list.filter((x) => x.id !== a.id));

  if (editing) return <ActionForm action={editing} info={info} onSave={save} onCancel={() => setEditing(null)} />;
  return (
    <>
      <div className="settings-note">
        A prompt or a command you keep to run in a click, from <IconBolt size={11} /> at the top or Alt+A, in any mode. A prompt goes to the
        session open in the Agent view, or to a new one set up as it says; a command runs in this folder. You are told when it is done.
      </div>
      {all.map((a) => (
        <div key={a.scope + a.id} className="settings-row">
          <div className="settings-text">
            <div>{a.name}</div>
            <div className="settings-note">
              {describe(a, info)} · {a.scope === "repo" ? "this repo" : "every repo"}
            </div>
          </div>
          <span className="settings-actions">
            <button className="mini" onClick={() => setEditing(a)}>
              Edit
            </button>
            <button className="mini" onClick={() => remove(a)}>
              Delete
            </button>
          </span>
        </div>
      ))}
      <div>
        <button className="mini" onClick={() => setEditing(BLANK)}>
          <IconPlus size={12} /> New action
        </button>
      </div>
    </>
  );
}

function ActionForm({ action, info, onSave, onCancel }) {
  const [a, setA] = useState(() => ({ ...BLANK, id: newID(), ...action }));
  const edit = (patch) => setA((x) => ({ ...x, ...patch }));
  const models = modelsFor(a, info);
  const model = models.find((m) => m.id === a.model);
  const efforts = model?.efforts || [];
  const command = a.kind === "command";
  const ok = a.name.trim() && (command ? a.command : a.prompt).trim();
  const save = () =>
    ok && onSave({ ...a, name: a.name.trim(), prompt: a.prompt.trim(), command: a.command.trim(), modelLabel: a.modelLabel || (a.model && model?.label) || "" });
  const onKey = (e) => {
    e.stopPropagation();
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) save();
  };
  return (
    <>
      <label className="hub-field">
        <span>Name</span>
        <input autoFocus value={a.name} placeholder={command ? "Run the tests" : "Post review"} onChange={(e) => edit({ name: e.target.value })} onKeyDown={onKey} />
      </label>
      <Setting label="Does" value={a.kind} onPick={(kind) => edit({ kind })} choices={[["prompt", "Sends a prompt"], ["command", "Runs a command"]]} />
      {command ? (
        <label className="hub-field">
          <span>Command</span>
          <span className="settings-note">
            Runs in this folder with sh, which stops at the first command that fails. <code>$DV_REPO</code> is the repository,{" "}
            <code>$DV_WORKTREE</code> this folder and <code>$DV_BRANCH</code> its branch. You are told how it ended, with the last of its output.
          </span>
          <textarea rows={5} value={a.command} placeholder="npm test" spellCheck={false} onChange={(e) => edit({ command: e.target.value })} onKeyDown={onKey} />
        </label>
      ) : (
        <label className="hub-field">
          <span>Prompt</span>
          <textarea
            rows={6}
            value={a.prompt}
            placeholder="Post the open comments in .dv/comments.json as a review on this branch's pull request, with gh."
            onChange={(e) => edit({ prompt: e.target.value })}
            onKeyDown={onKey}
          />
        </label>
      )}
      <Setting label="Kept for" value={a.scope} onPick={(scope) => edit({ scope })} choices={[["repo", "This repo"], ["user", "Every repo"]]} />
      {!command && (
        <Setting
          label="Runs in"
          note={a.where === "current" ? "The session open in the Agent view, or last open there." : ""}
          value={a.where}
          onPick={(where) => edit({ where })}
          choices={[["new", "A new session"], ["current", "The current session"]]}
        />
      )}
      {!command && a.where === "new" && (
        <>
          {info?.codex && (
            <Setting label="Agent" value={a.agent} onPick={(agent) => edit({ agent, model: "", effort: "", modelLabel: "" })} choices={[["", "Claude"], ["codex", "Codex"]]} />
          )}
          <div className="settings-row">
            <div className="settings-text">Model</div>
            <Picker
              label={model?.label || a.model || "Default"}
              choices={models.map((m) => ({ id: m.id, label: m.label, description: m.description }))}
              value={a.model}
              onPick={(id) => edit({ model: id, effort: "", modelLabel: models.find((m) => m.id === id)?.label || "" })}
            />
          </div>
          {efforts.length > 0 && (
            <Setting label="Effort" value={a.effort} onPick={(effort) => edit({ effort })} choices={[["", "Usual"], ...efforts.map((e) => [e, e[0].toUpperCase() + e.slice(1)])]} />
          )}
          <Setting label="Mode" value={a.mode} onPick={(mode) => edit({ mode })} choices={SESSION_MODES} />
          <Setting label="Temporary" note="Once closed, it leaves the session list." value={a.temporary} onPick={(temporary) => edit({ temporary })} choices={OFF_ON} />
          <Setting
            label="When it is done"
            note="Closing stops the session. One that ends on an error, or that you interrupt, is left open."
            value={a.close}
            onPick={(close) => edit({ close })}
            choices={[[false, "Keep it"], [true, "Close it"]]}
          />
        </>
      )}
      <div className="action-form-foot">
        <button className="mini" onClick={onCancel}>
          Cancel
        </button>
        <button className="primary" disabled={!ok} onClick={save}>
          Save
        </button>
      </div>
    </>
  );
}

const newID = () => Date.now().toString(36) + Math.random().toString(36).slice(2, 6);
