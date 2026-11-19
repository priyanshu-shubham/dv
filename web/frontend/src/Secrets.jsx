import { Fragment, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { api } from "./api.js";
import { IconX } from "./icons.jsx";
import { say } from "./Notices.jsx";
import { Modal } from "./Overlays.jsx";
import { glimpse, guessName, reloadSecrets, useSecrets } from "./secrets.js";
import { copyText } from "./util.js";

// LooseSecrets asks about what looks like a secret and isn't masked, before
// it goes to a model: items [{ preview, name }], each masked under a name
// the reader can change (onMask), or all let go as they are (onAllow).
export function LooseSecrets({ title, note, items, onMask, onAllow, onClose }) {
  const [names, setNames] = useState(() => items.map((it) => it.name));
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
        <div className="settings-note">
          {note} Masked, the model is sent a placeholder in its place from now on, and commands it runs still get the value. Sent as is, dv won't ask about it again.
        </div>
        <dl className="mask-secret-seen">
          {items.map((it, i) => (
            <Fragment key={i}>
              <dt>
                <span className="secret-chip">{it.preview}</span>
              </dt>
              <dd>
                <input
                  value={names[i]}
                  spellCheck={false}
                  onChange={(e) => setNames((ns) => ns.map((n, j) => (j === i ? e.target.value.toUpperCase().replace(/[^A-Z0-9_]/g, "_") : n)))}
                />
              </dd>
            </Fragment>
          ))}
        </dl>
        {error && <div className="settings-note prompt-error">{error}</div>}
        <div className="action-form-foot">
          <button className="mini" onClick={onClose}>
            {onAllow.later ? "Later" : "Cancel"}
          </button>
          <button className="btn" disabled={busy} onClick={act(onAllow)}>
            Send as is
          </button>
          <button className="primary" autoFocus disabled={busy || names.some((n) => !n)} onClick={act(() => onMask(names))}>
            Mask and send
          </button>
        </div>
      </div>
    </Modal>
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

// HeldSecrets asks about what a session is about to send that looks like a
// secret, which the gateway holds until it is told; one put off comes back
// once nothing else is asked.
export function HeldSecrets() {
  const [held, setHeld] = useState([]);
  const [later, setLater] = useState(() => new Set());
  useEffect(() => {
    let live = true;
    const look = () => !document.hidden && api.secretsHeld().then((h) => live && setHeld(h), () => {});
    look();
    const t = setInterval(look, 2000);
    return () => {
      live = false;
      clearInterval(t);
    };
  }, []);
  const h = held.find((x) => !later.has(x.id));
  if (!h) return null;
  const answer = async (names) => {
    await api.secretAnswer(h.id, names);
    if (names.some(Boolean)) await reloadSecrets();
    setHeld((all) => all.filter((x) => x.id !== h.id));
  };
  const allow = () => answer(h.found.map(() => ""));
  allow.later = true;
  return (
    <LooseSecrets
      key={h.id}
      title="A session is about to send what looks like a secret"
      note="It waits for you; unanswered for five minutes, it is masked."
      items={h.found}
      onMask={answer}
      onAllow={allow}
      onClose={() => setLater((s) => new Set(s).add(h.id))}
    />
  );
}

// MaskSecret masks a secret from the next request on: one picked out of the
// conversation (picked), one pasted in, or a new value for one already
// masked (name).
export function MaskSecret({ picked = "", name: fixed = "", onClose }) {
  const [value, setValue] = useState(picked);
  const [name, setName] = useState(() => fixed || (picked ? guessName(picked) : ""));
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const ready = name && value.trim() && !busy;
  const save = async () => {
    setBusy(true);
    try {
      await api.secretAdd(name, value);
      await reloadSecrets();
      say(fixed ? `${name} changed` : `Masked as ${name}`);
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
              if (!fixed && (!name || name === guessName(value.trim()))) setName(guessName(e.target.value.trim()));
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
      const chip = e.target.closest?.(".secret-chip");
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

  if (changing) return <MaskSecret name={changing} onClose={() => (values.current.delete(changing), setChanging(""))} />;
  if (!at) return null;
  const known = secrets.find((s) => s.name === at.name);
  const shown = at.chip.classList.contains("shown");
  const valueOf = async () => {
    if (!values.current.has(at.name)) values.current.set(at.name, (await api.secretValue(at.name)).value);
    return values.current.get(at.name);
  };
  const toggle = async () => {
    try {
      const v = await valueOf();
      at.chip.textContent = shown ? known?.preview || "••••••" : v;
      at.chip.classList.toggle("shown", !shown);
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
        <div className="secret-hover-note">No longer among the masked secrets.</div>
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
