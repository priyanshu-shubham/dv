import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import MarkdownIt from "markdown-it";
import { api } from "./api.js";
import { cx, LRM, modKey, relTime, usePersisted } from "./util.js";
import { IconChevronDown, IconSpark, IconX } from "./icons.jsx";

const md = new MarkdownIt({ html: false, linkify: true, breaks: true });

// Short enough to read at a glance and to fit two per row. Each is a real
// question a reviewer asks, not prompt scaffolding.
const PRESETS = [
  { label: "Explain it", text: "Explain what this change does." },
  { label: "What breaks?", text: "What could break because of this change?" },
  { label: "Find bugs", text: "Is this correct? Look for bugs and edge cases." },
  { label: "Ripple effects", text: "How does this interact with the rest of the codebase?" },
  { label: "Simplify", text: "Suggest a simpler way to write this." },
  { label: "Tests", text: "What tests should cover this change?" },
];

const DEFAULT_WIDTH = 430;
const MIN_WIDTH = 320;

// Leave enough of the diff visible that the panel stays a side panel.
const clampWidth = (w) => Math.max(MIN_WIDTH, Math.min(w, Math.max(MIN_WIDTH, window.innerWidth - 560)));

// AskPanel is a conversation about one slice of the diff. Follow-ups continue
// the same CLI session, so the diff and earlier answers stay in context without
// being re-sent.
export default function AskPanel({ target, scope, onClose, onSaveComment }) {
  const [models, setModels] = useState(null);
  const [model, setModel] = usePersisted("askModel", "claude-sonnet-5");
  const [draft, setDraft] = useState("");
  const [turns, setTurns] = useState([]);
  const [session, setSession] = useState(null);
  const [busy, setBusy] = useState(false);
  const abort = useRef(null);
  const scrollRef = useRef(null);
  const inputRef = useRef(null);
  const rootRef = useRef(null);
  const [width, setWidth] = usePersisted("askWidth", DEFAULT_WIDTH);
  const drag = useRef(null);

  // The drag writes straight to the DOM and only commits to state on release:
  // a pointermove-rate React render plus localStorage write is enough to make
  // the drag feel sticky.
  const onResizeDown = (e) => {
    e.preventDefault();
    drag.current = { x: e.clientX, w: rootRef.current.offsetWidth, to: 0 };
    e.currentTarget.setPointerCapture(e.pointerId);
    document.body.classList.add("resizing");
  };
  const onResizeMove = (e) => {
    const d = drag.current;
    if (!d) return;
    d.to = clampWidth(d.w + (d.x - e.clientX));
    rootRef.current.style.setProperty("--ask-w", d.to + "px");
  };
  const onResizeUp = () => {
    const d = drag.current;
    if (!d) return;
    drag.current = null;
    document.body.classList.remove("resizing");
    if (d.to) setWidth(d.to);
  };

  // Also runs on mount: a width stored from a wider display would otherwise
  // squeeze the diff to nothing here.
  useEffect(() => {
    const fit = () => setWidth((w) => (clampWidth(w) === w ? w : clampWidth(w)));
    fit();
    window.addEventListener("resize", fit);
    return () => window.removeEventListener("resize", fit);
  }, [setWidth]);

  useEffect(() => {
    api.askModels().then(setModels).catch(() => {});
  }, []);

  // A new target is a new subject, so it starts a new conversation rather than
  // asking follow-ups about code that is no longer on screen.
  const targetKey = `${target.file || ""}:${target.side || ""}:${target.startLine || 0}-${target.endLine || 0}`;
  useEffect(() => {
    abort.current?.abort();
    setTurns([]);
    setSession(null);
    setBusy(false);
    setDraft("");
    inputRef.current?.focus();
  }, [targetKey]);

  useEffect(() => () => abort.current?.abort(), []);

  const scopeLabel = !target.file
    ? "the whole comparison"
    : target.startLine
      ? `${target.file}:${target.startLine}${target.endLine !== target.startLine ? `-${target.endLine}` : ""}`
      : target.file;

  const ask = useCallback(
    async (text) => {
      const question = text.trim();
      if (!question || busy) return;
      const controller = new AbortController();
      abort.current = controller;
      setDraft("");
      setBusy(true);

      const id = `t${Date.now()}`;
      const resuming = session;
      setTurns((t) => [...t, { id, question, answer: "", model, at: new Date().toISOString() }]);
      const patch = (fields) =>
        setTurns((t) => t.map((turn) => (turn.id === id ? { ...turn, ...fields } : turn)));

      try {
        await api.ask(
          {
            scope: scope.kind,
            rev: scope.rev,
            file: target.file || "",
            side: target.side || "new",
            startLine: target.startLine || 0,
            endLine: target.endLine || 0,
            question,
            model,
            sessionId: resuming || "",
          },
          (ev) => {
            if (ev.type === "delta") {
              setTurns((t) => t.map((turn) => (turn.id === id ? { ...turn, answer: turn.answer + ev.text } : turn)));
            } else if (ev.type === "error") {
              patch({ error: ev.text });
            } else if (ev.type === "done") {
              patch({ stats: { cost: ev.cost, ms: ev.durationMs } });
              if (ev.sessionId) setSession(ev.sessionId);
            }
          },
          controller.signal,
        );
      } catch (e) {
        if (e.name === "AbortError") patch({ stopped: true });
        else patch({ error: e.message });
      } finally {
        setBusy(false);
      }
    },
    [busy, model, scope, target, session],
  );

  // Follow the stream unless the reader has scrolled up to re-read something.
  const lastAnswer = turns.length ? turns[turns.length - 1].answer : "";
  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    if (el.scrollHeight - el.scrollTop - el.clientHeight < 160) el.scrollTop = el.scrollHeight;
  }, [lastAnswer, turns.length]);

  const modelLabel = useMemo(
    () => models?.models?.find((m) => m.id === model)?.label || "Model",
    [models, model],
  );
  const unavailable = models && !models.available;

  return (
    <div className="ask" ref={rootRef} style={{ "--ask-w": width + "px" }}>
      <div
        className="ask-resize"
        title="Drag to resize, double-click to reset"
        onPointerDown={onResizeDown}
        onPointerMove={onResizeMove}
        onPointerUp={onResizeUp}
        onPointerCancel={onResizeUp}
        onDoubleClick={() => setWidth(DEFAULT_WIDTH)}
      />
      <header className="ask-head">
        <IconSpark size={14} className="ask-mark" />
        <strong>Ask Claude</strong>
        <span className="ask-target" title={scopeLabel}>
          {LRM}
          {scopeLabel}
        </span>
        <span className="spacer" />
        {turns.length > 0 && (
          <button
            className="ask-mini"
            title="Start a fresh conversation about this code"
            onClick={() => {
              abort.current?.abort();
              setTurns([]);
              setSession(null);
              inputRef.current?.focus();
            }}
          >
            New
          </button>
        )}
        <ModelMenu models={models?.models} value={model} label={modelLabel} onChange={setModel} />
        <button className="ask-mini" onClick={onClose} title="Close">
          <IconX size={12} />
        </button>
      </header>

      {unavailable ? (
        <div className="ask-empty">
          <p>
            The <code>claude</code> CLI is not on your PATH, so dv has nothing to ask. Install it and reload.
          </p>
        </div>
      ) : (
        <>
          <div className="ask-scroll" ref={scrollRef}>
            {turns.length === 0 ? (
              <div className="ask-empty">
                <p className="ask-empty-lead">Ask about {target.file ? "this code" : "this review"}.</p>
                <p className="dim">
                  dv sends the diff you are looking at. Claude can read the rest of the repo when it needs to.
                </p>
                <div className="ask-presets">
                  {PRESETS.map((p) => (
                    <button key={p.label} onClick={() => ask(p.text)} title={p.text}>
                      {p.label}
                    </button>
                  ))}
                </div>
              </div>
            ) : (
              turns.map((turn, i) => (
                <Turn
                  key={turn.id}
                  turn={turn}
                  streaming={busy && i === turns.length - 1}
                  onSave={
                    target.file
                      ? () =>
                          onSaveComment({
                            file: target.file,
                            side: target.side || "new",
                            startLine: target.startLine || 1,
                            endLine: target.endLine || target.startLine || 1,
                            quote: [],
                            body: `**Asked Claude:** ${turn.question}\n\n${turn.answer}`,
                            author: "claude",
                          })
                      : null
                  }
                />
              ))
            )}
          </div>

          <div className="ask-composer">
            <textarea
              ref={inputRef}
              rows={2}
              value={draft}
              placeholder={session ? "Ask a follow-up..." : "Ask anything about this change..."}
              onChange={(e) => setDraft(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === "Enter" && !e.shiftKey) {
                  e.preventDefault();
                  ask(draft);
                }
                e.stopPropagation();
              }}
            />
            <div className="ask-composer-bar">
              <span className="dim hint">
                {session ? "following up in the same session" : `Enter to send, Shift+Enter for a newline`}
              </span>
              <span className="spacer" />
              {busy ? (
                <button className="ask-stop" onClick={() => abort.current?.abort()}>
                  Stop
                </button>
              ) : (
                <button className="primary" onClick={() => ask(draft)} disabled={!draft.trim()}>
                  Send
                </button>
              )}
            </div>
          </div>
        </>
      )}
    </div>
  );
}

// Turn is one question and its answer.
function Turn({ turn, streaming, onSave }) {
  const [saved, setSaved] = useState(false);
  const [copied, setCopied] = useState(false);

  return (
    <article className="turn">
      <div className="turn-q">{turn.question}</div>

      <div className="turn-a">
        {turn.error ? (
          <div className="turn-error">{turn.error}</div>
        ) : (
          <>
            <div className="markdown" dangerouslySetInnerHTML={{ __html: md.render(turn.answer || "") }} />
            {streaming && <span className="caret" />}
            {turn.stopped && <div className="dim turn-note">stopped</div>}
          </>
        )}
      </div>

      {!streaming && turn.answer && (
        <div className="turn-foot">
          {turn.stats && (
            <span className="dim">
              {(turn.stats.ms / 1000).toFixed(1)}s
              {turn.stats.cost ? ` · $${turn.stats.cost.toFixed(3)}` : ""}
            </span>
          )}
          <span className="dim">{relTime(turn.at)}</span>
          <span className="spacer" />
          <button
            onClick={() => {
              navigator.clipboard?.writeText(turn.answer).then(
                () => {
                  setCopied(true);
                  setTimeout(() => setCopied(false), 1200);
                },
                () => {},
              );
            }}
          >
            {copied ? "Copied" : "Copy"}
          </button>
          {onSave && (
            <button
              disabled={saved}
              onClick={async () => {
                await onSave();
                setSaved(true);
              }}
            >
              {saved ? "Saved" : "Save as comment"}
            </button>
          )}
        </div>
      )}
    </article>
  );
}

// ModelMenu replaces a bare <select> so the model's one-line note is visible
// while choosing, which is the only reason to pick one over another.
function ModelMenu({ models, value, label, onChange }) {
  const [open, setOpen] = useState(false);
  const ref = useRef(null);

  useEffect(() => {
    if (!open) return;
    const onDown = (e) => {
      if (ref.current && !ref.current.contains(e.target)) setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);

  return (
    <div className="model-menu" ref={ref}>
      <button className="ask-mini" onClick={() => setOpen((o) => !o)} title="Model">
        {label}
        <IconChevronDown size={10} />
      </button>
      {open && (
        <div className="model-list">
          {(models || []).map((m) => (
            <button
              key={m.id}
              className={cx(m.id === value && "on")}
              onClick={() => {
                onChange(m.id);
                setOpen(false);
              }}
            >
              <span className="model-name">{m.label}</span>
              {m.note && <span className="dim">{m.note}</span>}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

export function AskButton({ className, onClick, title }) {
  return (
    <button className={cx("ask-btn", className)} onClick={onClick} title={title || "Ask Claude about this"}>
      <IconSpark size={12} /> Ask
    </button>
  );
}
