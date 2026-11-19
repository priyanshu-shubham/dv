import { Fragment, useEffect, useRef, useState, useSyncExternalStore } from "react";
import { createPortal } from "react-dom";
import { api } from "./api.js";
import { IconEye, IconX } from "./icons.jsx";
import { say } from "./Notices.jsx";
import { Modal } from "./Overlays.jsx";
import { glimpse, guessName, reloadSecrets, useSecrets } from "./secrets.js";
import { copyText, cx } from "./util.js";

// LooseSecrets asks about what looks like a secret and isn't masked, before
// it goes to a model: items [{ preview, name }], each masked under a name
// the reader can change (onMask), or all let go as they are (onAllow). A
// request the gateway holds (held, its id) also says where it comes from
// (from, and each item's where and line), shows a value on asking, and can
// stop the asking for its session (both answers then get quiet = true).
export function LooseSecrets({ title, from, note, items, held, onMask, onAllow, onClose }) {
  const [names, setNames] = useState(() => items.map((it) => it.name));
  const [quiet, setQuiet] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const act = (fn) => async () => {
    setBusy(true);
    try {
      await fn();
    } catch (e) {
      setError(e.message);
      setBusy(false);
    }
  };
  const rename = (i) => (e) => setNames((ns) => ns.map((n, j) => (j === i ? e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, "_") : n)));
  return (
    <Modal onClose={onClose} centred className="mask-secret">
      <div className="help-head">
        <span className="help-title">{title || (items.length === 1 ? "This looks like a secret" : "These look like secrets")}</span>
        <span className="spacer" />
        <button className="ghost" onClick={onClose} title="Close (Esc)">
          <IconX size={14} />
        </button>
      </div>
      <div className="mask-secret-body">
        {from && <div className="held-from">{from}</div>}
        <div className="settings-note">
          {note} Masked, it is saved to your secrets under the name you give it, the model is sent that name in its place from now on, and commands it runs still get the value. Sent as is, dv won't ask about it again.
        </div>
        {held ? (
          <HeldFindings held={held} found={items} names={names} onNames={setNames} />
        ) : (
          <dl className="mask-secret-seen">
            {items.map((it, i) => (
              <Fragment key={i}>
                <dt>
                  <span className="secret-chip">{it.preview}</span>
                </dt>
                <dd>
                  <input value={names[i]} spellCheck={false} onChange={rename(i)} />
                </dd>
              </Fragment>
            ))}
          </dl>
        )}
      </div>
      <div className="mask-secret-foot">
        {error && <div className="settings-note prompt-error">{error}</div>}
        {held && (
          <label className="check">
            <input type="checkbox" className="tick" checked={quiet} onChange={(e) => setQuiet(e.target.checked)} />
            Don't ask again in this session
          </label>
        )}
        <div className="action-form-foot">
          <button className="mini" onClick={onClose} title={onAllow.later ? "Asks again in a minute; the session waits meanwhile" : undefined}>
            {onAllow.later ? "Later" : "Cancel"}
          </button>
          <button className="btn" disabled={busy} onClick={act(() => onAllow(quiet))}>
            Send as is
          </button>
          <button className="primary" autoFocus disabled={busy || names.some((n) => !n)} onClick={act(() => onMask(names, quiet))}>
            Mask and send
          </button>
        </div>
      </div>
    </Modal>
  );
}

// HeldFindings are what a request the gateway holds (held, its id) would
// send, each with the name to mask it as.
export function HeldFindings({ held, found, names, onNames }) {
  const rename = (i) => (e) => onNames((ns) => ns.map((n, j) => (j === i ? e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, "_") : n)));
  return found.map((it, i) => (
    <HeldFinding key={i} item={it} valueOf={async () => (await api.secretHeldValue(held, i)).value} name={names[i]} onName={rename(i)} />
  ));
}

// HeldFinding is one thing a held request would send: where in it, the line
// it is on with the value shown on asking, and the name to mask it as.
function HeldFinding({ item, valueOf, name, onName }) {
  const [value, setValue] = useState(null);
  const [shown, setShown] = useState(false);
  useEffect(() => {
    if (!shown) return;
    const t = setTimeout(() => setShown(false), SHOWN_FOR);
    return () => clearTimeout(t);
  }, [shown]);
  const toggle = async () => {
    try {
      if (value === null) setValue(await valueOf());
      setShown((s) => !s);
    } catch (e) {
      say(e.message);
    }
  };
  return (
    <div className="held-finding">
      {item.where && (
        <div className="held-where" title={item.detail ? `${item.where}: ${item.detail}` : item.where}>
          {item.where}
          {item.detail && <code>{item.detail}</code>}
        </div>
      )}
      <div className="held-line">
        <span className="held-around before">{item.before}</span>
        <span className={cx("secret-chip", shown && "shown")} title={shown ? value : undefined}>
          {shown ? value : item.preview}
        </span>
        <span className="held-around after">{item.after}</span>
        <span className="spacer" />
        <button className="ghost" onClick={toggle} title={shown ? "Hide the value" : "Show the value"}>
          <IconEye size={13} />
        </button>
      </div>
      <label className="held-name" title={`Masked, it is kept among your secrets as ${name || "this name"}, the model sees [[dv-secret:${name || "NAME"}]], and #${name || "NAME"} puts it in a message`}>
        <span>Mask as</span>
        <input value={name} spellCheck={false} onChange={onName} />
      </label>
    </div>
  );
}

// looseItems are what a message holds that looks like a secret, named by
// the key each is set under or as its shape suggests, each name apart.
export function looseItems(values, names = []) {
  const taken = new Set();
  return values.map((v, i) => {
    let name = names[i] || guessName(v);
    for (let n = 2; taken.has(name); n++) name = name.replace(/_\d+$/, "") + "_" + n;
    taken.add(name);
    return { preview: glimpse(v), name };
  });
}

// A value shown is hidden again after this long.
const SHOWN_FOR = 10e3;

// The requests held over a secret, as last heard, and those put off by when:
// the session waits on one, so it comes back in a minute. The page's alone,
// so a session can say it waits and offer the question again.
let heldState = { held: [], later: new Map() };
const heldListeners = new Set();
function setHeldState(change) {
  heldState = { ...heldState, ...change(heldState) };
  heldListeners.forEach((f) => f());
}
const putOff = (later, id) => Date.now() - (later.get(id) || 0) < 60e3;

// useHeldFor is the request a session's turn waits on, put off from asking,
// or null; reviewHeld asks it again now.
export function useHeldFor(session) {
  const { held, later } = useSyncExternalStore(
    (f) => (heldListeners.add(f), () => heldListeners.delete(f)),
    () => heldState,
  );
  return (session && held.find((h) => !h.prompted && h.session === session && putOff(later, h.id))) || null;
}
export const reviewHeld = (id) =>
  setHeldState(({ later }) => {
    const next = new Map(later);
    next.delete(id);
    return { later: next };
  });

// HeldSecrets asks about what a session is about to send that looks like a
// secret, which the gateway holds until it is told: a session no open folder
// has, whose question can't be one of its prompts - a terminal's, say.
export function HeldSecrets({ sessions = [] }) {
  const { held, later } = useSyncExternalStore(
    (f) => (heldListeners.add(f), () => heldListeners.delete(f)),
    () => heldState,
  );
  useEffect(() => {
    let live = true;
    const look = () => !document.hidden && api.secretsHeld().then((h) => live && setHeldState(() => ({ held: h })), () => {});
    look();
    const t = setInterval(look, 2000);
    return () => {
      live = false;
      clearInterval(t);
    };
  }, []);
  // One put to its session as a prompt is answered there.
  const h = held.find((x) => !x.prompted && !putOff(later, x.id));
  if (!h) return null;
  const answer = async (names, quiet) => {
    await api.secretAnswer(h.id, names, quiet, from);
    if (names.some(Boolean)) await reloadSecrets();
    setHeldState(({ held }) => ({ held: held.filter((x) => x.id !== h.id) }));
  };
  const allow = (quiet) => answer(h.found.map(() => ""), quiet);
  allow.later = true;
  // Its title where the session is one of this folder's; else the folder it works in.
  const s = h.session && sessions.find((x) => x.id === h.session);
  const folder = h.cwd?.split(/[\\/]/).filter(Boolean).pop();
  const from = [s && (s.title || s.prompt || "New session"), folder].filter(Boolean).join(" · ");
  return (
    <LooseSecrets
      key={h.id}
      title="A session is about to send what looks like a secret"
      from={from}
      note="The session waits for you. Closed, this comes back in a minute; unanswered for five minutes, it is all masked."
      items={h.found}
      held={h.id}
      onMask={answer}
      onAllow={allow}
      onClose={() => setHeldState(({ later }) => ({ later: new Map(later).set(h.id, Date.now()) }))}
    />
  );
}

// MaskSecret masks a secret from the next request on: one picked out of the
// conversation (picked), one pasted in, or a new value for one already
// masked (name). suggested is a name written for a new one; onMasked hears
// the name it was masked as.
export function MaskSecret({ picked = "", name: fixed = "", suggested = "", onMasked, onClose }) {
  const [value, setValue] = useState(picked);
  const [name, setName] = useState(() => fixed || suggested || (picked ? guessName(picked) : ""));
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const ready = name && value.trim() && !busy;
  const save = async () => {
    setBusy(true);
    try {
      await api.secretAdd(name, value);
      await reloadSecrets();
      say(fixed ? `${name} changed` : `Masked as ${name}`);
      onMasked?.(name);
      onClose();
    } catch (e) {
      setError(e.message);
      setBusy(false);
    }
  };
  const enter = (e) => e.key === "Enter" && ready && save();
  return (
    <Modal onClose={onClose} centred className="mask-secret">
      <div className="help-head">
        <span className="help-title">{fixed ? `Change ${fixed}` : "Mask a secret"}</span>
        <span className="spacer" />
        <button className="ghost" onClick={onClose} title="Close (Esc)">
          <IconX size={14} />
        </button>
      </div>
      <div className="mask-secret-body">
        <div className="settings-note">
          Wherever it turns up in what a session sends - a file read, a command's output, your message - the model is sent a placeholder instead, and
          the value is put back in what it writes. It is kept on this computer, sealed.
        </div>
        {!picked && (
          <input
            type="password"
            value={value}
            autoFocus
            autoComplete="off"
            spellCheck={false}
            placeholder={fixed ? "Its new value" : "The secret"}
            onChange={(e) => {
              if (!fixed && !suggested && (!name || name === guessName(value.trim()))) setName(guessName(e.target.value.trim()));
              setValue(e.target.value);
              setError("");
            }}
            onKeyDown={enter}
          />
        )}
        {!fixed && (
          <input
            value={name}
            autoFocus={!!picked}
            spellCheck={false}
            placeholder="NAME"
            onChange={(e) => (setName(e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, "_")), setError(""))}
            onKeyDown={enter}
          />
        )}
        {value.trim() && (
          <dl className="mask-secret-seen">
            <dt>The page shows</dt>
            <dd>
              <span className="secret-chip">{glimpse(value.trim())}</span>
            </dd>
            <dt>The model sees</dt>
            <dd>
              <code>[[dv-secret:{name || "NAME"}]]</code>
            </dd>
          </dl>
        )}
        {error && <div className="settings-note prompt-error">{error}</div>}
        <div className="action-form-foot">
          <button className="mini" onClick={onClose}>
            Cancel
          </button>
          <button className="primary" disabled={!ready} onClick={save}>
            {fixed ? "Change" : "Mask"}
          </button>
        </div>
      </div>
    </Modal>
  );
}

// SecretHover is what a masked secret's chip offers, wherever in the page it
// is drawn: its name, the value shown in place of the glimpse, a copy, and a
// new value for it.
export function SecretHover() {
  const secrets = useSecrets();
  const [at, setAt] = useState(null); // { chip, name, top, left }
  const [changing, setChanging] = useState("");
  const [, redraw] = useState(0);
  const leave = useRef(null);
  const values = useRef(new Map());

  useEffect(() => {
    const show = (e) => {
      // A chip with no name is a glimpse of one not masked yet, as the held request's.
      const chip = e.target.closest?.(".secret-chip[data-secret]");
      if (!chip) return;
      clearTimeout(leave.current);
      const r = chip.getBoundingClientRect();
      setAt({ chip, name: chip.dataset.secret, top: r.bottom + 4, left: Math.min(r.left, innerWidth - 260) });
    };
    const hide = (e) => {
      if (!e.target.closest?.(".secret-chip, .secret-hover")) return;
      clearTimeout(leave.current);
      leave.current = setTimeout(() => setAt(null), 250);
    };
    const stay = (e) => e.target.closest?.(".secret-hover") && clearTimeout(leave.current);
    document.addEventListener("mouseover", show);
    document.addEventListener("focusin", show);
    document.addEventListener("mouseout", hide);
    document.addEventListener("mouseover", stay);
    return () => {
      document.removeEventListener("mouseover", show);
      document.removeEventListener("focusin", show);
      document.removeEventListener("mouseout", hide);
      document.removeEventListener("mouseover", stay);
    };
  }, []);

  const known = at && secrets.find((s) => s.name === at.name);
  // The list is the page's since it was asked: a .env read since, as of a
  // folder opened later, isn't in it yet.
  const [rechecked, setRechecked] = useState("");
  useEffect(() => {
    if (!at || known || rechecked === at.name) return;
    reloadSecrets().then(() => setRechecked(at.name));
  }, [at, known, rechecked]);

  if (changing) return <MaskSecret name={changing} onClose={() => (values.current.delete(changing), setChanging(""))} />;
  if (!at) return null;
  const shown = at.chip.classList.contains("shown");
  const valueOf = async () => {
    if (!values.current.has(at.name)) values.current.set(at.name, (await api.secretValue(at.name)).value);
    return values.current.get(at.name);
  };
  const toggle = async () => {
    try {
      const v = await valueOf();
      const { chip } = at;
      const preview = known?.preview || "••••••";
      chip.textContent = shown ? preview : v;
      chip.classList.toggle("shown", !shown);
      if (!shown) {
        setTimeout(() => {
          if (!chip.classList.contains("shown")) return;
          chip.textContent = preview;
          chip.classList.remove("shown");
          redraw((n) => n + 1);
        }, SHOWN_FOR);
      }
      redraw((n) => n + 1);
    } catch (e) {
      say(e.message);
    }
  };
  const copy = () => valueOf().then(copyText).then(() => say("Copied"), (e) => say(e.message));
  const change = () => {
    setAt(null);
    setChanging(at.name);
  };
  return createPortal(
    <div className="model-list secret-hover" style={{ top: at.top, left: at.left }}>
      <div className="menu-label">{at.name}</div>
      {!known ? (
        <div className="secret-hover-note">{rechecked === at.name ? "No longer among the masked secrets." : "Looking it up…"}</div>
      ) : (
        <>
          <button onClick={toggle}>
            <span className="model-name">{shown ? "Hide the value" : "Show the value"}</span>
          </button>
          <button onClick={copy}>
            <span className="model-name">Copy the value</span>
          </button>
          {known.from ? (
            <div className="secret-hover-note">Read from {known.from}; it changes with the file.</div>
          ) : (
            <button onClick={change}>
              <span className="model-name">Change the value…</span>
            </button>
          )}
        </>
      )}
    </div>,
    document.body,
  );
}
