// A session's screen in the phone app: its bar, the message box and its
// sheets, the answers to a request over the box, and the ways through a long
// conversation. AgentView keeps the state and does the work; these draw it.
import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { cx } from "../util.js";
import { say } from "../Notices.jsx";
import { Pad } from "../Notes.jsx";
import { Icon } from "./icons.jsx";
import { Choice, Menu, Row, Sheet } from "./Sheet.jsx";

// SessionBar names the session, with what runs it and how full it is under;
// the name opens the list of your messages to jump to. pad offers the pad,
// in a sheet over the conversation.
export function SessionBar({ title, sub, onBack, onTitle, onActions, pad, menu }) {
  const [open, setOpen] = useState(false);
  const [padOpen, setPadOpen] = useState(false);
  return (
    <header className="p-bar">
      <button className="p-icon" onClick={onBack} aria-label="Back">
        <Icon name="back" />
      </button>
      <button className="p-title" onClick={onTitle} disabled={!onTitle}>
        <span className="p-title-main">{title}</span>
        {sub && <span className="p-title-sub">{sub}</span>}
      </button>
      {pad && (
        <button className="p-icon" onClick={() => setPadOpen(true)} aria-label="Pad">
          <Icon name="note" />
        </button>
      )}
      {onActions && (
        <button className="p-icon" onClick={onActions} aria-label="Actions">
          <Icon name="bolt" />
        </button>
      )}
      {menu.some(Boolean) && (
        <button className="p-icon" onClick={() => setOpen(true)} aria-label="More">
          <Icon name="dots" />
        </button>
      )}
      {open && <Menu items={menu} onClose={() => setOpen(false)} />}
      {padOpen && (
        <Sheet title="Pad" className="p-pad-sheet" onClose={() => setPadOpen(false)}>
          <Pad />
        </Sheet>
      )}
    </header>
  );
}

// Marks are a session's temporary and kept-running marks, as its row in the
// list and its bar show them.
export function Marks({ s }) {
  return (
    <>
      {s?.temporary && (
        <span className="p-flag" role="img" aria-label="Temporary" title="Temporary: it leaves the list once closed">
          <Icon name="timer" size={14} />
        </span>
      )}
      {s?.kept && (
        <span className="p-flag" role="img" aria-label="Kept running" title="Kept running: not stopped when idle, and started with dv">
          <Icon name="pin" size={14} />
        </span>
      )}
    </>
  );
}

// JumpPad steps between your own messages, showing while the reader scrolls
// the conversation and for a moment after - not when the page scrolls it.
export function JumpPad({ scrollRef, mine, onStep }) {
  const [on, setOn] = useState(false);
  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    let t = 0;
    let touched = 0;
    const onTouch = () => (touched = performance.now());
    const onScroll = () => {
      if (performance.now() - touched > 1000 || mine() < 2) return;
      setOn(true);
      clearTimeout(t);
      t = setTimeout(() => setOn(false), 2500);
    };
    el.addEventListener("touchmove", onTouch, { passive: true });
    el.addEventListener("touchend", onTouch, { passive: true });
    el.addEventListener("wheel", onTouch, { passive: true });
    el.addEventListener("scroll", onScroll, { passive: true });
    return () => {
      el.removeEventListener("touchmove", onTouch);
      el.removeEventListener("touchend", onTouch);
      el.removeEventListener("wheel", onTouch);
      el.removeEventListener("scroll", onScroll);
      clearTimeout(t);
    };
  }, [scrollRef.current]);
  return (
    <div className={cx("p-jump", on && "on")}>
      <button onClick={() => onStep(-1)} aria-label="Your previous message">
        <Icon name="up" />
      </button>
      <button onClick={() => onStep(1)} aria-label="Your next message">
        <Icon name="down" />
      </button>
    </div>
  );
}

// JumpSheet lists what you said, your place marked, to go straight to one.
export function JumpSheet({ said, at, onPick, onClose }) {
  useEffect(() => {
    document.querySelector(".p-jump-list .p-row.on")?.scrollIntoView({ block: "center" });
  }, []);
  return (
    <Sheet title="Jump to" onClose={onClose} className="p-jump-list">
      {said.map((t, i) => (
        <Row
          key={i}
          on={i === at}
          lead={<span className="p-num">{i + 1}</span>}
          title={<span className="clamp2">{t}</span>}
          onClick={() => {
            onClose();
            onPick(i);
          }}
        />
      ))}
    </Sheet>
  );
}

// HoldSheet is what a long-press on your message offers.
export function HoldSheet({ text, onEdit, edited, onRewind, onAgain, onClose }) {
  return (
    <Sheet onClose={onClose}>
      <div className="p-preview">{text || "(images)"}</div>
      {onEdit && (
        <Row
          icon="edit"
          title="Edit"
          sub={edited ? "The conversation goes back to before it; files changed since stay as they are" : "The conversation goes back to before it, and it comes back to change"}
          onClick={() => (onClose(), onEdit())}
        />
      )}
      {onRewind && <Row icon="undo" title="Rewind to before this…" onClick={() => (onClose(), onRewind())} />}
      {text && onAgain && <Row icon="pull" title="Use again" sub="Into the message box, the conversation as it is" onClick={() => (onClose(), onAgain())} />}
      {text && <Row icon="copy" title="Copy" onClick={() => (onClose(), copy(text))} />}
    </Sheet>
  );
}

// QueuedSheet is what a queued message offers: taken back to edit, or dropped.
export function QueuedSheet({ text, name, onEdit, onDrop, onClose }) {
  return (
    <Sheet onClose={onClose}>
      <div className="p-preview">{text}</div>
      <Row icon="edit" title="Edit" sub={`Back into the box; ${name} won't read it until you send it again`} onClick={() => (onClose(), onEdit())} />
      <Row icon="trash" title="Remove" sub="Taken back and dropped" onClick={() => (onClose(), onDrop())} />
    </Sheet>
  );
}

function copy(text) {
  navigator.clipboard?.writeText(text).then(
    () => say("Copied"),
    () => say("Could not copy"),
  );
}

// Composer is the message box: + for pictures, files and the rest, the model
// and the mode, and Send - or Stop while the agent works and the box is empty.
// While a request waits, the box is the note that goes with the answer, and
// the answers sit over it in the dock.
export function Composer({
  name, draft, onDraft, inputRef, onPaste, placeholder, canSend, onSend, onStop, stop, lost,
  extras, model, effort, mode, pending, onModel, onMode, onAttach,
  asking, said, dockRef, offered, onOffer, chips,
}) {
  // What floats over the conversation - the bar for selected text - sits above the box.
  const ref = useRef(null);
  useEffect(() => {
    const el = ref.current;
    const root = document.documentElement.style;
    const ro = new ResizeObserver(() => root.setProperty("--p-box-h", el.offsetHeight + "px"));
    ro.observe(el);
    return () => {
      ro.disconnect();
      root.removeProperty("--p-box-h");
    };
  }, []);
  return (
    <div className="p-composer" ref={ref}>
      {!asking && !draft && offered.length > 0 && (
        <div className="p-chips">
          {offered.map((f, i) => (
            <button key={i} className="p-chip" onClick={() => onOffer(f.prompt)}>
              <span className="p-chip-text">{f.label}</span>
            </button>
          ))}
        </div>
      )}
      <div className="p-dock" ref={dockRef} hidden={!asking} />
      <div className="p-box">
        {extras}
        <div className="p-box-text">
          {!asking && (
            <button className="p-icon small" onClick={onAttach} aria-label="Add pictures, files and more">
              <Icon name="plus" />
            </button>
          )}
          <textarea
            ref={inputRef}
            value={draft}
            rows={1}
            placeholder={asking ? `Add a note for ${name} (optional)` : placeholder}
            onChange={(e) => onDraft(e.target.value)}
            onPaste={onPaste}
          />
        </div>
        {asking ? (
          <div className="p-with">{said ? `Goes with your answer: ${said}` : "Type a note to go with your answer"}</div>
        ) : (
          <div className="p-box-bar">
            {chips}
            {model && (
              <button className="p-pick" onClick={onModel}>
                <span className="p-pick-text">{model}</span>
                {effort && <span className="p-pick-effort">{effort}</span>}
                <Icon name="drop" size={18} />
              </button>
            )}
            {mode && (
              <button className="p-pick" onClick={onMode}>
                <span className="p-pick-text">{mode}</span>
                {pending && <span className="p-pick-effort">next turn</span>}
                <Icon name="drop" size={18} />
              </button>
            )}
            <span className="p-spacer" />
            {stop ? (
              <button className="p-send stop" onClick={onStop} aria-label={`Stop ${name}`}>
                <Icon name="stop" size={20} />
              </button>
            ) : (
              <button className="p-send" onClick={onSend} disabled={!canSend || lost} aria-label="Send">
                <Icon name="send" size={22} />
              </button>
            )}
          </div>
        )}
      </div>
    </div>
  );
}

// ModelSheet picks the model and how hard it thinks; for a session not yet
// started, the agent too. A model can be added by the name /model takes.
export function ModelSheet({ agents, agent, onAgent, models, model, efforts, effort, pending, onPick, onAdd, onDrop, onClose }) {
  const [adding, setAdding] = useState(null); // the name being typed; null shows the row that asks for one
  return (
    <Sheet title="Model" onClose={onClose}>
      {agents && (
        <div className="p-seg">
          {agents.map((a) => (
            <button key={a.id} className={cx(a.id === agent && "on")} onClick={() => onAgent(a.id)}>
              {a.id === agent && <Icon name="check" size={18} />}
              {a.label}
            </button>
          ))}
        </div>
      )}
      {pending && <div className="p-note">Changes take effect from the next turn.</div>}
      {models.map((m) => {
        const choice = (
          <Choice
            key={m.id}
            title={
              <>
                {m.label}
                {m.tag && <span className="p-tag">{m.tag}</span>}
              </>
            }
            sub={m.description}
            on={m.id === model}
            onClick={() => (onPick({ model: m.id }), onClose())}
          />
        );
        return m.added ? (
          <div key={m.id} className="p-added">
            {choice}
            <button className="p-icon small" onClick={() => onDrop(m.id)} aria-label={`Take ${m.label} off the list`}>
              <Icon name="x" size={20} />
            </button>
          </div>
        ) : (
          choice
        );
      })}
      {adding === null ? (
        <Row className="p-add-row" icon="plus" title="Add a model" onClick={() => setAdding("")} />
      ) : (
        <form
          className="p-inline-form"
          onSubmit={(e) => {
            e.preventDefault();
            if (!adding.trim()) return;
            onAdd(adding.trim());
            onClose();
          }}
        >
          <input
            className="p-field mono"
            value={adding}
            placeholder="Name, as for /model"
            autoCapitalize="off"
            autoCorrect="off"
            spellCheck={false}
            autoFocus
            onChange={(e) => setAdding(e.target.value)}
          />
          <button className="p-btn tonal" type="submit" disabled={!adding.trim()}>
            Add
          </button>
        </form>
      )}
      {efforts?.length > 0 && (
        <>
          <div className="p-sub-label">Effort</div>
          <div className="p-chips wrap">
            {efforts.map((e) => (
              <button key={e} className={cx("p-chip", e === effort && "on")} onClick={() => (onPick({ effort: e }), onClose())}>
                {e === effort && <Icon name="check" size={16} />}
                {e}
              </button>
            ))}
          </div>
        </>
      )}
    </Sheet>
  );
}

export function ModeSheet({ modes, mode, pending, onPick, gear, onClose }) {
  const [setting, setSetting] = useState(false);
  if (setting) {
    return (
      <Sheet title={gear.title} className="p-unasked-sheet" onClose={onClose}>
        {gear.panel}
      </Sheet>
    );
  }
  return (
    <Sheet title="Permission mode" onClose={onClose}>
      {pending && <div className="p-note">Changes take effect from the next turn.</div>}
      {modes.map((m) => {
        const choice = <Choice key={m.id} title={m.label} sub={m.description} on={m.id === mode} onClick={() => (onPick(m.id), onClose())} />;
        return m.id === gear?.id ? (
          <div key={m.id} className="p-added">
            {choice}
            <button className="p-icon small" onClick={() => setSetting(true)} aria-label={gear.title}>
              <Icon name="settings" size={20} />
            </button>
          </div>
        ) : (
          choice
        );
      })}
    </Sheet>
  );
}

// AttachSheet is +: pictures and files from the phone, the folder's comments,
// and the agent's own ways in - ! for the shell, /compact.
export function AttachSheet({ onFiles, comments, onComments, onShell, onCompact, onClose }) {
  const camera = useRef(null);
  const photos = useRef(null);
  const files = useRef(null);
  const take = (e) => {
    const list = [...e.target.files];
    e.target.value = "";
    onClose();
    if (list.length) onFiles(list);
  };
  return (
    <Sheet onClose={onClose}>
      <div className="p-tiles">
        <button className="p-tile" onClick={() => camera.current.click()}>
          <Icon name="camera" />
          Camera
        </button>
        <button className="p-tile" onClick={() => photos.current.click()}>
          <Icon name="image" />
          Photos
        </button>
        <button className="p-tile" onClick={() => files.current.click()}>
          <Icon name="attach" />
          Files
        </button>
      </div>
      <input ref={camera} type="file" accept="image/*" capture="environment" hidden onChange={take} />
      <input ref={photos} type="file" accept="image/*" multiple hidden onChange={take} />
      <input ref={files} type="file" multiple hidden onChange={take} />
      {comments > 0 && <Row icon="comment" title={`Comments (${comments})`} sub="Every open comment, with the lines it's on" onClick={() => (onClose(), onComments())} />}
      <Row icon="terminal" title="Run in your shell" sub="Starts the message with !, and the agent is sent what it prints" onClick={() => (onClose(), onShell())} />
      {onCompact && <Row icon="compress" title="Compact" sub="Summarise the conversation so far to free up context" onClick={() => (onClose(), onCompact())} />}
    </Sheet>
  );
}

// Answers are a request's choices, docked over the message box: No and Yes,
// and the rest - "and don't ask again", a plan's ways to start - behind More
// when they don't fit. A plan's are all there is, so they are listed in full.
export function Answers({ dock, question, options, said, plan, busy, error, onChoose }) {
  const [more, setMore] = useState(false);
  if (!dock) return null;
  const no = options.findIndex((o) => !o.allow);
  const yes = options.findIndex((o) => o.allow);
  const rest = options.map((o, i) => i).filter((i) => i !== no && i !== yes);
  const hint = (o) => (o.allow ? (said ? `with ${said}` : "") : said ? `sends ${said} as the reason` : "");
  return createPortal(
    <>
      <div className="p-dock-q">{question}</div>
      {error && <div className="p-dock-error">{error}</div>}
      {plan ? (
        <div className="p-dock-list">
          {options.map((o, i) => (
            <button key={i} className={cx("p-btn", i === yes ? "filled" : o.allow ? "tonal" : "outline")} disabled={busy} onClick={() => onChoose(i)}>
              {o.label}
            </button>
          ))}
        </div>
      ) : (
        <div className="p-dock-row">
          {no >= 0 && (
            <button className="p-btn outline" disabled={busy} onClick={() => onChoose(no)}>
              {said ? "No, with note" : "No"}
            </button>
          )}
          {/* Most often the one other answer is yes and don't ask again. */}
          {rest.length === 1 && options[rest[0]].allow ? (
            <button className="p-btn tonal" disabled={busy} onClick={() => setMore(true)}>
              Always…
            </button>
          ) : (
            rest.length > 0 && (
              <button className="p-btn tonal" disabled={busy} onClick={() => setMore(true)}>
                More…
              </button>
            )
          )}
          {yes >= 0 && (
            <button className="p-btn filled" disabled={busy} onClick={() => onChoose(yes)}>
              {said ? "Yes, with note" : "Yes"}
            </button>
          )}
        </div>
      )}
      {more && (
        <Sheet title={question} onClose={() => setMore(false)}>
          {options.map((o, i) => (
            <Row key={i} icon={o.allow ? "check" : "x"} title={<span className="p-wrap">{o.label}</span>} sub={hint(o) || o.title} onClick={() => (setMore(false), onChoose(i))} />
          ))}
        </Sheet>
      )}
    </>,
    dock,
  );
}
