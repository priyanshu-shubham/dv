import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { api } from "./api.js";
import { DiffBody } from "./FileDiff.jsx";
import { ensureLanguage, highlightLines, langReady } from "./highlight.js";
import { MarkdownDocument, previewKind, PreviewToggle, SvgPreview } from "./Preview.jsx";
import { agentName, cx, isTyping, LRM, splitPath, useCopy, usePersisted } from "./util.js";
import { readPref, usePref } from "./prefs.js";
import { AgentIcon, IconFile, IconX } from "./icons.jsx";

const NO_COMMENTS = [];

// Keys this soon after a request comes up were meant for whatever had the
// focus before it, and a stray 1 or Enter would answer for the reader. Clicks
// too: the options may have just moved in under a pointer aimed elsewhere.
const ARM_MS = 400;

// numbersAnswer is whether an option's number answers with it at once, as the
// terminal's does, or only moves to it.
const numbersAnswer = () => readPref("user", "settings", NO_SETTINGS).numbersAnswer !== false;
const NO_SETTINGS = {};

// useAgentEvents is the requests waiting on the reader, and the open sessions'
// activity, null until first heard.
export function useAgentEvents() {
  const [requests, setRequests] = useState([]);
  const [sessions, setSessions] = useState(null);
  useEffect(
    () =>
      api.claudeEvents((e) => {
        if (e.requests) setRequests(e.requests);
        if (e.sessions) setSessions(e.sessions);
      }),
    [],
  );
  return { requests, sessions };
}

// AgentPrompt is the window Claude Code's and Codex's requests are answered in, over
// whatever the reader is doing, opened from a request's notice or the header's
// bell. The terminal asks at the same time; whichever answers first wins, and
// the request leaves here either way. It stays mounted while anything waits, so
// a half-written note survives being put away.
export default function AgentPrompt({
  requests, open, focusId, view, contextLines, wrap, onClose, onSymbol, onOpenFile, onOpenSession,
}) {
  const [shownId, setShownId] = useState(null);
  useEffect(() => {
    if (focusId) setShownId(focusId);
  }, [focusId]);
  const idx = Math.max(0, requests.findIndex((r) => r.id === shownId));
  const req = requests[idx];
  const [drafts, setDrafts] = useState({}); // id -> { note, comments }

  // Put away, the focus goes back to wherever the reader was.
  useEffect(() => {
    if (!open) return;
    const before = document.activeElement;
    return () => {
      if (before?.isConnected && before !== document.body) before.focus({ preventScroll: true });
    };
  }, [open]);

  const step = (d) => setShownId(requests[idx + d]?.id ?? req.id);

  // Built from the page's own parts: the overlays' shell and title bar, the
  // request as a file in the review, the answers as palette rows.
  return (
    <div className="prompt-backdrop" hidden={!open} onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <section className="modal modal-wide prompt" role="dialog" aria-modal="true" aria-label={`${agentName(req.via)} is asking`}>
        <div className="viewer-head prompt-head">
          <RequestTitle req={req} />
          <button className="prompt-session" onClick={() => onOpenSession(req.session)} title="Answer it in the session, in the Agent view">
            in {req.title || "a new session"}
          </button>
          <span className="spacer" />
          {requests.length > 1 && (
            <span className="prompt-count">
              <button className="nav" disabled={idx === 0} onClick={() => step(-1)} title="Previous request">
                ‹
              </button>
              {idx + 1} of {requests.length}
              <button className="nav" disabled={idx === requests.length - 1} onClick={() => step(1)} title="Next request">
                ›
              </button>
            </span>
          )}
          <button className="ghost" onClick={onClose} title="Later - it waits under the bell (Esc)">
            <IconX size={13} />
          </button>
        </div>
        <Request
          key={req.id}
          req={req}
          active={open}
          grab
          draft={drafts[req.id]}
          onDraft={(patch) => setDrafts((d) => ({ ...d, [req.id]: { ...d[req.id], ...patch } }))}
          onLater={onClose}
          view={view}
          contextLines={contextLines}
          wrap={wrap}
          onSymbol={onSymbol}
          onOpenFile={onOpenFile}
        />
      </section>
    </div>
  );
}

// RequestTitle is what a request's title bar says.
export function RequestTitle({ req }) {
  return (
    <>
      <AgentIcon agent={req.via} size={14} />
      <span className="prompt-title">
        {agentName(req.via)} wants to {req.headline}
      </span>
      {req.agent && (
        <span className="tag-generated" title="A subagent is asking">
          {req.agent}
        </span>
      )}
    </>
  );
}

// Request asks what Claude Code is asking - an edit shown as the diff it would
// make, anything else as the call itself - and takes the answer the way the
// terminal does: arrows and Enter, a number, Tab for a note. Comments left on
// an edit's lines go with the answer too. It has the keys while active, unless
// the window is up over it. grab takes the focus when the request comes up;
// onLater, when given, is what Esc does. draft is the note and comments, kept
// by the caller so they outlive the request being put away. children go at the
// end of its foot.
export function Request({
  req, active, grab, draft, onDraft, onLater, view, contextLines, wrap, onSymbol, onOpenFile, children,
}) {
  const note = draft?.note || "";
  const comments = draft?.comments || NO_COMMENTS;
  const said = saidWith(note, comments);
  const [sel, setSel] = useState(0);
  const selRef = useRef(sel);
  selRef.current = sel;
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const bodyRef = useRef(null);
  const listRef = useRef(null);
  const noteRef = useRef(null);
  const armed = useRef(0);
  const questionsRef = useRef(null);
  const questionKeys = useRef(null);

  const what = describe(req);
  const asking = req.tool === "AskUserQuestion";
  const manyQuestions = asking && (req.input?.questions?.length || 0) > 1;
  const options = req.options;
  const modeOption = options.findIndex((o) => o.mode === "acceptEdits");

  const answer = useCallback(
    async (a) => {
      if (busy) return;
      setBusy(true);
      setError("");
      try {
        await api.claudeAnswer(req.id, a);
      } catch (e) {
        setError(e.message);
      } finally {
        setBusy(false);
      }
    },
    [busy, req.id],
  );
  useEffect(() => setError(""), [comments]);
  const choose = useCallback(
    (i) => {
      const o = options[i];
      if (!o || Date.now() < armed.current) return;
      setSel(i);
      // A comment still being written would be left out of the answer.
      const unsaved = bodyRef.current?.querySelector(".composer[data-draft] textarea");
      if (unsaved) {
        setError("Your comment is not saved yet: Ctrl+Enter saves it, Cancel drops it.");
        unsaved.focus();
        return;
      }
      answer({ allow: o.allow, note: withComments(note, comments, req), suggestion: o.suggestion });
    },
    [options, answer, note, comments, req],
  );

  // Each request is met at Yes, held off for a moment when it shows and again
  // when it takes the keys.
  useEffect(() => {
    armed.current = Date.now() + ARM_MS;
    if (!active) return;
    setSel(0);
    const g = typeof grab === "function" ? grab() : grab;
    if (g) (asking ? questionsRef : listRef).current?.focus({ preventScroll: true });
  }, [active, req.id]);

  // On the window, ahead of the page's own shortcuts, so the page behind does
  // not act on keys meant for the request - however the focus has wandered
  // after a click in a diff. Chords pass, so the palettes stay in reach.
  useEffect(() => {
    if (!active) return;
    const onKey = (e) => {
      if (document.querySelector(".backdrop")) return; // a viewer opened over the request has the keys
      if (!listRef.current?.closest(".prompt-backdrop") && document.querySelector(".prompt-backdrop:not([hidden])")) return;
      if (asking) {
        if (questionKeys.current?.(e)) {
          e.preventDefault();
          e.stopPropagation();
        } else if (e.key === "Escape" && onLater && !isTyping(e.target)) onLater();
        return;
      }
      // Any other text box has its own keys: a comment's Ctrl+Enter saves it.
      const t = noteRef.current;
      const inNote = e.target === t;
      if (!inNote && isTyping(e.target)) return;
      if (e.metaKey || e.ctrlKey || e.altKey) {
        if (e.key === "Enter" && !e.altKey) {
          e.preventDefault();
          e.stopPropagation();
          choose(selRef.current);
        }
        return;
      }
      const move = e.key === "ArrowDown" ? 1 : e.key === "ArrowUp" ? -1 : 0;
      // In the note the arrows still pick the answer, unless there is a line of
      // the note that way to move to, or Shift is selecting text.
      const inText = inNote && (e.shiftKey || (move < 0 ? t.value.slice(0, t.selectionStart) : t.value.slice(t.selectionEnd)).includes("\n"));
      let handled = true;
      if (Date.now() < armed.current) {
        // dropped
      } else if (move && !inText) setSel((s) => (s + move + options.length) % options.length);
      else if (inNote) {
        if (e.key === "Enter" && !e.shiftKey) choose(selRef.current);
        else if (e.key === "Escape" || e.key === "Tab") listRef.current?.focus({ preventScroll: true });
        else handled = false;
      } else if (e.key === "Enter") choose(selRef.current);
      else if (e.key === "Tab" && e.shiftKey && modeOption >= 0) choose(modeOption);
      else if (e.key === "Tab") noteRef.current?.focus({ preventScroll: true });
      else if (e.key === "Escape") {
        if (onLater) onLater();
        else handled = false;
      } else if (/^[1-9]$/.test(e.key)) {
        const i = Number(e.key) - 1;
        if (numbersAnswer()) choose(i);
        else if (i < options.length) setSel(i);
      }
      else if (e.key === "c" || e.key === "a") {
        const cell = e.key === "c" && bodyRef.current?.querySelector("[data-line][data-side]:hover");
        if (cell) cell.closest(".prompt-card").dispatchEvent(commentEvent(cell));
        // Otherwise a request in the conversation leaves them to the edits there.
        else handled = !!listRef.current?.closest(".prompt-backdrop");
      } else handled = e.key.length === 1; // a letter would reach the page's shortcuts
      if (handled) {
        e.preventDefault();
        e.stopPropagation();
      }
    };
    window.addEventListener("keydown", onKey, true);
    return () => window.removeEventListener("keydown", onKey, true);
  }, [active, asking, options.length, modeOption, choose, onLater]);

  return (
    <>
      {asking ? (
        <Questions
          key={req.id}
          id={req.id}
          agent={req.via}
          questions={req.input?.questions || []}
          busy={busy}
          armed={armed}
          rootRef={questionsRef}
          keysRef={questionKeys}
          onAnswer={answer}
        />
      ) : (
        <div className="prompt-body" ref={bodyRef}>
          {(req.previews?.length ? req.previews : [null]).map((p) => (
            <RequestCard
              key={p?.path ?? ""}
              req={req}
              preview={p}
              what={what}
              comments={comments}
              onComments={(next) => onDraft({ comments: next })}
              view={view}
              contextLines={contextLines}
              wrap={wrap}
              onSymbol={onSymbol}
              onOpenFile={onOpenFile}
            />
          ))}
        </div>
      )}

      <div className="prompt-answer" hidden={asking}>
        <div className="prompt-question">{question(req, what)}</div>
        <div className="prompt-list">
          <div className="prompt-options" role="listbox" tabIndex={-1} ref={listRef} aria-activedescendant={`opt-${req.id}-${sel}`}>
            {options.map((o, i) => {
              const no = req.tool === "ExitPlanMode" ? "keep planning" : `stops ${agentName(req.via)}`;
              const hint = optionHint(o, said, no);
              return (
                <button
                  key={i}
                  id={`opt-${req.id}-${i}`}
                  role="option"
                  aria-selected={i === sel}
                  className={cx("palette-row", "prompt-option", i === sel && "on", !o.allow && "no")}
                  disabled={busy}
                  // Not mouseenter: that also fires when a scroll carries a row
                  // under a pointer at rest, which would change what Enter answers.
                  onMouseMove={() => setSel(i)}
                  onClick={() => choose(i)}
                  title={o.title}
                >
                  <span className="badge">{i + 1}</span>
                  <span className="label">{o.label}</span>
                  {hint && <span className="hint">{hint}</span>}
                  {i === modeOption && <kbd>Shift+Tab</kbd>}
                </button>
              );
            })}
          </div>
          <label className="prompt-note">
            <span className="badge" title="Tab">
              ⇥
            </span>
            <textarea
              ref={noteRef}
              rows={1}
              value={note}
              placeholder={
                options[sel]?.allow === false ? `Tell ${agentName(req.via)} what to do instead` : `Add a note for ${agentName(req.via)} - it goes with your answer`
              }
              onChange={(e) => onDraft({ note: e.target.value })}
            />
          </label>
        </div>
      </div>

      <div className="palette-foot prompt-foot">
        {error ? (
          <span className="prompt-error">{error}</span>
        ) : (
          <span className="prompt-keys">
            {manyQuestions && (
              <span>
                <kbd>←</kbd> <kbd>→</kbd> question
              </span>
            )}
            <span>
              <kbd>↑</kbd> <kbd>↓</kbd> choose
            </span>
            <span>
              <kbd>Enter</kbd> {asking ? "pick" : "answer"}
            </span>
            <span>
              <kbd>Tab</kbd> note
            </span>
            {onLater && (
              <span>
                <kbd>Esc</kbd> later
              </span>
            )}
            {/* In the conversation it goes without saying where Claude waits. */}
            {(!req.dv || onLater) && (
              <span className="also">
                {req.dv ? `${agentName(req.via)} is waiting in a session dv runs.` : "The terminal is asking too; the first answer counts."}
              </span>
            )}
          </span>
        )}
        <span className="spacer" />
        {children}
      </div>
    </>
  );
}

// Where a request's questions stand before any is answered: the tab and row on
// screen, and by question the labels picked, what was written in (and, where
// several are taken, whether it is ticked) and the note for Claude.
const NO_PLACE = { tab: 0, cursors: {} };
const NO_ANSWERS = { picked: {}, other: {}, otherOn: {}, notes: {} };

// Questions takes AskUserQuestion's answers as the terminal does: a question at
// a time under a tab for each, and a last tab summing up what was chosen, whose
// rows send them or cancel. A choice moves on to the next question; one that
// takes several moves on with its Submit row, → or Next. Under each question's
// options come something else, written in - where several are taken, writing
// there ticks it and Enter ticks it on and off like the rest - and a note on
// the answer, which Tab goes to and back from. Progress is kept by request id,
// so putting the request away, reopening it elsewhere in the page, reloading or
// picking it up on another device loses nothing. rootRef is what takes the
// focus; keysRef is handed the key handler Request calls while it has the
// keys, which says whether it took the key.
function Questions({ id, agent, questions, busy, armed, rootRef, keysRef, onAnswer }) {
  const name = agentName(agent);
  // Codex goes on without an answer; Claude stops.
  const declined = agent === "codex" ? `decline to answer, and ${name} goes on without it` : `decline to answer, which stops ${name}`;
  // The answers so far follow the request to another device; where this one is
  // among the tabs and rows stays with the browser tab.
  const [place, setPlace] = usePersisted("ask:" + id, NO_PLACE, { session: true, folder: true });
  const [answers, setAnswers] = usePref("repo", "ask:" + id, NO_ANSWERS);
  const { tab, cursors } = { ...NO_PLACE, ...place };
  const { picked, other, otherOn, notes } = { ...NO_ANSWERS, ...answers };
  const field = (set, none, key) => (v) => set((s) => ({ ...none, ...s, [key]: typeof v === "function" ? v({ ...none, ...s }[key]) : v }));
  const [setTab, setCursors] = ["tab", "cursors"].map((k) => field(setPlace, NO_PLACE, k));
  const [setPicked, setOther, setOtherOn, setNotes] = ["picked", "other", "otherOn", "notes"].map((k) => field(setAnswers, NO_ANSWERS, k));
  const otherRef = useRef(null);
  const noteRef = useRef(null);
  const n = questions.length;
  const many = n > 1;
  const written = (q, o = other) => (!q.multiSelect || otherOn[q.question] ? (o[q.question] || "").trim() : "");
  const said = (q, p = picked, o = other) => [...(p[q.question] || []), written(q, o)].filter(Boolean);
  const ready = (p = picked, o = other) => n > 0 && questions.every((q) => said(q, p, o).length > 0);
  const early = () => Date.now() < armed.current;
  const submit = (p = picked, o = other) => {
    if (busy || early() || !ready(p, o)) return;
    // A note, and the drawing of an option picked, go as the terminal sends them.
    const annotations = {};
    for (const q of questions) {
      const note = (notes[q.question] || "").trim();
      const drawn = q.options.find((o) => o.preview && (p[q.question] || []).includes(o.label))?.preview;
      if (note || drawn) annotations[q.question] = { ...(note && { notes: note }), ...(drawn && { preview: drawn }) };
    }
    onAnswer({
      allow: true,
      answers: Object.fromEntries(questions.map((q) => [q.question, said(q, p, o).join(", ")])),
      annotations: Object.keys(annotations).length ? annotations : undefined,
    });
  };
  const cancel = () => busy || early() || onAnswer({ allow: false });
  const go = (i) => {
    setTab(Math.max(0, Math.min(many ? n : 0, i)));
    rootRef.current?.focus({ preventScroll: true });
  };
  // The last tab comes up on Submit, or on the first question still unanswered.
  const cursor = cursors[tab] ?? (tab === n ? (ready() ? n : Math.max(0, questions.findIndex((x) => !said(x).length))) : 0);
  // Every move is a write to the stored place, so only one onto another row counts.
  const hover = (row) => () => row !== cursor && setCursors((c) => ({ ...c, [tab]: row }));
  // A question's rows: its options, then something else, the note, and where
  // several are taken, Submit. The two text rows take the focus.
  const moveTo = (row) => {
    setCursors((c) => ({ ...c, [tab]: row }));
    const k = questions[tab].options.length;
    const box = row === k ? otherRef : row === k + 1 ? noteRef : null;
    if (box) requestAnimationFrame(() => box.current?.focus());
    else rootRef.current?.focus({ preventScroll: true });
  };
  // Settled with one question, the answer goes; with more, the next one comes up.
  const onward = (p, o) => (many ? go(tab + 1) : submit(p, o));
  // A single choice is one option or what was written, never both.
  const pick = (label) => {
    if (early()) return;
    const q = questions[tab];
    const had = picked[q.question] || [];
    const labels = !q.multiSelect ? [label] : had.includes(label) ? had.filter((l) => l !== label) : [...had, label];
    const p = { ...picked, [q.question]: labels };
    const o = q.multiSelect ? other : { ...other, [q.question]: "" };
    setPicked(p);
    setOther(o);
    if (!q.multiSelect) onward(p, o);
  };
  const write = (text) => {
    const q = questions[tab];
    const had = (other[q.question] || "").trim();
    setOther((o) => ({ ...o, [q.question]: text }));
    if (!q.multiSelect) setPicked((p) => ({ ...p, [q.question]: [] }));
    // Starting to write ticks it, and emptying the box unticks it; Enter can still untick what is written.
    else if (!text.trim() || !had) setOtherOn((on) => ({ ...on, [q.question]: !!text.trim() }));
  };
  const tickOther = () => {
    const q = questions[tab];
    if ((other[q.question] || "").trim()) setOtherOn((on) => ({ ...on, [q.question]: !on[q.question] }));
  };

  keysRef.current = (e) => {
    const inNote = e.target === noteRef.current;
    const typing = inNote || e.target === otherRef.current;
    if ((!typing && isTyping(e.target)) || e.altKey) return false;
    if (early()) return !typing;
    if (e.metaKey || e.ctrlKey) {
      if (e.key !== "Enter") return false;
      submit();
      return true;
    }
    // Tab goes to the note and back, as it does for a permission's.
    if (e.key === "Tab") {
      if (tab === n) return true;
      const q = questions[tab];
      if (!inNote) moveTo(q.options.length + 1);
      else {
        setCursors((c) => ({ ...c, [tab]: Math.max(0, q.options.findIndex((o) => (picked[q.question] || []).includes(o.label))) }));
        rootRef.current?.focus({ preventScroll: true });
      }
      return true;
    }
    if (typing) {
      const t = e.target;
      const q = questions[tab];
      const k = q.options.length;
      if (e.key === "Enter" && !e.shiftKey) {
        if (inNote) {
          if (q.multiSelect) moveTo(k + 2);
          else if (said(q).length) onward(picked, other);
        } else if (!(other[q.question] || "").trim()) return true;
        else if (q.multiSelect) tickOther();
        else onward(picked, other);
      } else if (e.key === "Escape") rootRef.current?.focus({ preventScroll: true });
      else if (e.key === "ArrowUp" && !t.value.slice(0, t.selectionStart).includes("\n")) moveTo(inNote ? k : k - 1);
      else if (e.key === "ArrowDown" && !t.value.slice(t.selectionEnd).includes("\n")) moveTo(!inNote ? k + 1 : q.multiSelect ? k + 2 : 0);
      else return false;
      return true;
    }
    const move = e.key === "ArrowDown" ? 1 : e.key === "ArrowUp" ? -1 : 0;
    const number = /^[1-9]$/.test(e.key) ? Number(e.key) : 0;
    if ((e.key === "ArrowLeft" || e.key === "ArrowRight") && many) go(tab + (e.key === "ArrowRight" ? 1 : -1));
    else if (tab === n) {
      // The questions, to go back to, then Submit and Cancel.
      if (move) setCursors((c) => ({ ...c, [n]: (cursor + move + n + 2) % (n + 2) }));
      else if (number && number <= n) go(number - 1);
      else if (e.key === "Enter" || e.key === " ") {
        if (cursor < n) go(cursor);
        else if (cursor === n) submit();
        else cancel();
      } else return e.key.length === 1;
    } else {
      const q = questions[tab];
      const k = q.options.length;
      const rows = k + (q.multiSelect ? 3 : 2);
      if (move) moveTo((cursor + move + rows) % rows);
      else if (number && number <= k) {
        setCursors((c) => ({ ...c, [tab]: number - 1 }));
        if (numbersAnswer()) pick(q.options[number - 1].label);
      } else if (e.key === "Enter" || e.key === " ") {
        if (cursor < k) pick(q.options[cursor].label);
        else if (cursor <= k + 1) moveTo(cursor);
        else onward(picked, other);
      } else return e.key.length === 1; // a letter would reach the page's shortcuts
    }
    return true;
  };

  const q = questions[tab];
  // What Claude drew for its options, beside them: the one the cursor is on,
  // else the one picked. Fence lines are dropped, the box being code already.
  // The box is as tall as the tallest drawing, so moving between options does
  // not move the conversation around it.
  const previewing = q?.options.some((o) => o.preview);
  const drawn = (o) => (o?.preview || "").replace(/^```\w*\s*$/gm, "").trim();
  const preview = previewing && drawn(q.options[cursor] || q.options.find((o) => (picked[q.question] || []).includes(o.label)));
  const previewLines = previewing ? Math.max(...q.options.map((o) => drawn(o).split("\n").length)) : 0;
  return (
    <div className="prompt-body prompt-questions" ref={rootRef} tabIndex={-1}>
      {many && (
        <div className="prompt-tabs" role="tablist">
          {questions.map((x, i) => (
            <button key={x.question} role="tab" aria-selected={i === tab} className={cx(i === tab && "on", said(x).length > 0 && "done")} onClick={() => go(i)} title={x.question}>
              <span className="badge">{said(x).length > 0 ? "✓" : i + 1}</span>
              {x.header || `Question ${i + 1}`}
            </button>
          ))}
          <button role="tab" aria-selected={tab === n} className={cx(tab === n && "on")} onClick={() => go(n)}>
            Submit
          </button>
        </div>
      )}
      {tab === n ? (
        <div className="prompt-answer">
          <div className="prompt-question">{ready() ? "Your answers" : "Your answers so far"}</div>
          <div className="prompt-list">
            <div className="prompt-options">
              {questions.map((x, i) => {
                const s = said(x);
                return (
                  <button
                    key={x.question}
                    className={cx("palette-row", "prompt-option", "choice", cursor === i && "on")}
                    onMouseMove={hover(i)}
                    onClick={() => go(i)}
                    title={`${x.question} - click to change`}
                  >
                    <span className="badge">{i + 1}</span>
                    <span className="prompt-choice">
                      <span className="label">{x.header || x.question}</span>
                      <span className={cx("prompt-choice-desc", !s.length && "missing")}>{s.join(", ") || "Not answered yet"}</span>
                      {(notes[x.question] || "").trim() && <span className="prompt-choice-desc">Note: {notes[x.question].trim()}</span>}
                    </span>
                  </button>
                );
              })}
              <button
                className={cx("palette-row", "prompt-option", cursor === n && "on")}
                disabled={busy}
                onMouseMove={hover(n)}
                onClick={() => submit()}
              >
                <span className="badge">↵</span>
                <span className="label">Submit answers</span>
                {!ready() && <span className="hint">answer every question first</span>}
              </button>
              <button
                className={cx("palette-row", "prompt-option", "no", cursor === n + 1 && "on")}
                disabled={busy}
                onMouseMove={hover(n + 1)}
                onClick={cancel}
              >
                <span className="badge">✕</span>
                <span className="label">Cancel</span>
                <span className="hint">{declined}</span>
              </button>
            </div>
          </div>
        </div>
      ) : (
        <div className="prompt-answer">
          <div className="prompt-question">
            {!many && q.header && <span className="tag-generated">{q.header}</span>} {q.question}
            {q.multiSelect && <span className="dim"> (any that apply)</span>}
          </div>
          <div className={cx(previewing && "prompt-previewing")}>
          <div className="prompt-list">
            <div className="prompt-options" role="listbox">
              {q.options.map((o, i) => {
                const chosen = (picked[q.question] || []).includes(o.label);
                return (
                  <button
                    key={o.label}
                    role="option"
                    aria-selected={chosen}
                    className={cx("palette-row", "prompt-option", "choice", i === cursor && "on", chosen && "picked")}
                    disabled={busy}
                    onMouseMove={hover(i)}
                    onClick={() => pick(o.label)}
                  >
                    <span className="badge">{chosen ? "✓" : i + 1}</span>
                    <span className="prompt-choice">
                      <span className="label">{o.label}</span>
                      {o.description && <span className="prompt-choice-desc">{o.description}</span>}
                    </span>
                  </button>
                );
              })}
            </div>
            <label className={cx("prompt-note", q.multiSelect && otherOn[q.question] && "picked")}>
              <span
                className="badge"
                onClick={(e) => {
                  if (!q.multiSelect) return;
                  e.preventDefault();
                  tickOther();
                }}
              >
                {q.multiSelect && otherOn[q.question] ? "✓" : "…"}
              </span>
              <textarea
                ref={otherRef}
                rows={1}
                value={other[q.question] || ""}
                placeholder="Something else"
                onFocus={() => setCursors((c) => ({ ...c, [tab]: q.options.length }))}
                onChange={(e) => write(e.target.value)}
              />
            </label>
            <label className="prompt-note">
              <span className="badge" title="Tab">
                ⇥
              </span>
              <textarea
                ref={noteRef}
                rows={1}
                value={notes[q.question] || ""}
                placeholder={`Add a note for ${name} on this answer`}
                onFocus={() => setCursors((c) => ({ ...c, [tab]: q.options.length + 1 }))}
                onChange={(e) => setNotes((ns) => ({ ...ns, [q.question]: e.target.value }))}
              />
            </label>
            {/* Choices that take several have nothing to move on by, so they end in this. */}
            {q.multiSelect && (
              <button
                className={cx("palette-row", "prompt-option", cursor === q.options.length + 2 && "on")}
                disabled={busy}
                onMouseMove={hover(q.options.length + 2)}
                onClick={() => onward(picked, other)}
              >
                <span className="badge">↵</span>
                <span className="label">Submit</span>
                <span className="hint">{!many ? "send the answer" : tab < n - 1 ? "on to the next question" : "on to your answers"}</span>
              </button>
            )}
          </div>
          {previewing && (
            <pre className="prompt-preview" style={{ height: `calc(${previewLines} * var(--preview-line) + 22px)` }}>
              {preview || "No preview for this one."}
            </pre>
          )}
          </div>
        </div>
      )}
      {/* The last tab has these as its rows. */}
      {!(many && tab === n) && (
        <div className="prompt-question-actions">
          <button className="ghost" disabled={busy} onClick={cancel} title={declined[0].toUpperCase() + declined.slice(1)}>
            Cancel
          </button>
          {many ? (
            <button className="primary" disabled={busy} onClick={() => go(tab + 1)}>
              Next
            </button>
          ) : (
            <button className="primary" disabled={!ready() || busy} onClick={() => submit()}>
              Answer
            </button>
          )}
        </div>
      )}
    </div>
  );
}

// RequestCard shows what the request would do as a file in the review: an
// edit's diff under its file header, anything else under a header naming the
// tool. A div rather than the diff's section.file, which the page's scroll
// anchoring looks for under the pointer and would find here instead. The edit
// is not in the review yet, so comments on it are only drawn here, until they
// go with the answer. p is the file shown, one of the request's previews;
// comments are all the request's, each on the file its path names.
function RequestCard({ req, preview: p, what, comments, onComments, view, contextLines, wrap, onSymbol, onOpenFile }) {
  const [expanded, setExpanded] = useState({});
  const [selection, setSelection] = useState(null);
  const [composing, setComposing] = useState(null);
  const [, force] = useState(0);
  const ref = useRef(null);
  const fd = p?.diff;
  // A plan opens as the document it is; an edit opens as the diff it is.
  const [preview, setPreview] = useState(req.tool === "ExitPlanMode");
  const [copied, copy] = useCopy(p?.path || "");

  const startComment = useCallback(
    (side, start, end, selected) => {
      const src = side === "old" ? fd?.oldLines : fd?.newLines;
      setComposing({ side, start, end, quote: src ? src.slice(start - 1, end) : [], selected });
      setSelection(null);
    },
    [fd],
  );
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const on = (e) => startComment(e.detail.side, e.detail.line, e.detail.line);
    el.addEventListener("dv:comment", on);
    return () => el.removeEventListener("dv:comment", on);
  }, [startComment]);
  const threads = useMemo(
    () =>
      comments
        .filter((c) => c.path === p?.path)
        .map((c) => ({
          id: c.id,
          draft: true,
          file: c.path,
          side: c.side,
          startLine: c.startLine,
          endLine: c.endLine,
          quote: c.quote,
          comments: [{ id: c.id, author: "you", body: c.body, createdAt: c.createdAt }],
        })),
    [comments, p?.path],
  );
  const comment = useCallback(
    async ({ side, startLine, endLine, quote, body }) => {
      const id = "draft-" + Date.now().toString(36) + Math.random().toString(36).slice(2, 6);
      onComments([...comments, { id, path: p.path, side, startLine, endLine, quote, body, createdAt: new Date().toISOString() }]);
    },
    [comments, onComments, p?.path],
  );
  const threadAction = useCallback(
    async (a) => {
      if (a.type === "deleteComment") onComments(comments.filter((c) => c.id !== a.commentId));
      if (a.type === "editComment") onComments(comments.map((c) => (c.id === a.commentId ? { ...c, body: a.body } : c)));
    },
    [comments, onComments],
  );

  useEffect(() => {
    if (fd?.lang) ensureLanguage(fd.lang, () => force((n) => n + 1));
  }, [fd?.lang]);

  const expand = useCallback((id, amount) => {
    setExpanded((e) => ({ ...e, [id]: amount === "all" ? "all" : (typeof e[id] === "number" ? e[id] : 0) + amount }));
  }, []);

  if (!p) {
    const title = cardTitle(req, what);
    const prose = req.tool === "Bash" || req.tool === "PowerShell";
    return (
      <div className="file prompt-card">
        <header className="file-head">
          <span className="tag-generated">{toolTag(req.tool)}</span>
          <span className={cx("prompt-card-title", !prose && "mono")} title={title}>
            {title}
          </span>
        </header>
        <div className={cx("file-body", wrap && "wrap")}>
          <CallBody req={req} />
        </div>
      </div>
    );
  }

  const [dir, name] = splitPath(p.path);
  const commentable = {
    threads,
    selection,
    setSelection,
    composing,
    setComposing,
    onStartComment: startComment,
    onComment: comment,
    onThreadAction: threadAction,
  };

  // A plan is read as the document it is, not as a file being added.
  if (req.tool === "ExitPlanMode") {
    return (
      <div className="file prompt-card plan" ref={ref}>
        <header className="file-head">
          <span className="tag-generated">plan</span>
          {req.input?.planFilePath && (
            <span className="prompt-card-title mono copy-path" title={`Copy the path, ${p.path}`} onClick={copy}>
              {name}
              {copied && <span className="copied">copied</span>}
            </span>
          )}
          <span className="spacer" />
          <PreviewToggle kind="markdown" on={preview} onChange={setPreview} />
        </header>
        <div className={cx("file-body", wrap && "wrap")}>
          {preview ? (
            <MarkdownDocument lines={fd.newLines} path={p.path} {...commentable} />
          ) : (
            <DiffBody
              fd={fd}
              view="code"
              side="new"
              contextLines={0}
              expanded={expanded}
              onExpand={expand}
              {...commentable}
              onSymbol={onSymbol}
              path={p.path}
              wrap={wrap}
              drawAll
            />
          )}
        </div>
      </div>
    );
  }

  const shown = fd && !fd.binary && !fd.tooLarge;
  // A problem edit shows only the text it would replace, not the file it makes.
  const kind = previewKind(p.path);
  const previewable = shown && !p.problem && kind;
  return (
    <div className="file prompt-card" ref={ref}>
      <header className="file-head">
        {fd && (
          <span className={cx("badge", "st-" + fd.status)} title={fd.status === "A" ? "a new file" : "changed"}>
            {fd.status}
          </span>
        )}
        <h3 className="file-path copy-path" title={`Copy the path, ${p.path}`} onClick={copy}>
          <span className="dir">
            {LRM}
            {dir}
            {LRM}
          </span>
          <span className="name">{name}</span>
          {copied && <span className="copied">copied</span>}
        </h3>
        {!p.inRepo && (
          <span className="tag-generated" title="Outside the repository dv is reviewing">
            outside
          </span>
        )}
        <span className="spacer" />
        {shown && !p.problem && (
          <span className="stat">
            <span className="add">+{fd.additions}</span>
            <span className="del">-{fd.deletions}</span>
          </span>
        )}
        {previewable && <PreviewToggle kind={kind} on={preview} onChange={setPreview} />}
        {p.inRepo && fd?.status !== "A" && (
          <button className="view-file" onClick={() => onOpenFile(p.path, firstChange(fd))} title="The whole file as it is now, at the edit">
            <IconFile size={12} />
            <span className="btn-label">File</span>
          </button>
        )}
      </header>
      <div className={cx("file-body", wrap && "wrap")}>
        {p.problem && (
          <div className="file-note error">
            Claude Code will refuse this: {p.problem}.{fd && " Shown instead: the text Claude means to replace, and its replacement."}
          </div>
        )}
        {fd?.binary && <div className="file-note">Binary file - not shown.</div>}
        {fd?.tooLarge && <div className="file-note">File is too large to display.</div>}
        {previewable && preview && kind === "markdown" && (
          <MarkdownDocument lines={fd.newLines} path={p.path} onOpenFile={p.inRepo ? onOpenFile : null} {...commentable} />
        )}
        {previewable && preview && kind === "svg" && <SvgPreview oldLines={fd.status !== "A" && fd.oldLines} newLines={fd.newLines} />}
        {shown && !(previewable && preview) && (
          <DiffBody
            fd={fd}
            view={fd.status === "A" ? "unified" : view}
            oneNumber={fd.status === "A" && view === "split"}
            contextLines={p.problem ? 99 : contextLines}
            expanded={expanded}
            onExpand={expand}
            {...commentable}
            onSymbol={onSymbol}
            path={p.path}
            wrap={wrap}
            drawAll
          />
        )}
      </div>
    </div>
  );
}

function CallBody({ req }) {
  if (req.tool === "Bash" || req.tool === "PowerShell") return <Command input={req.input} lang={req.tool === "Bash" ? "bash" : "powershell"} />;
  return <Fields input={req.input} />;
}

// toolTag names the tool in the card's header chip.
function toolTag(tool) {
  if (tool === "ExitPlanMode") return "plan";
  const mcp = /^mcp__(.+?)__/.exec(tool);
  return mcp ? mcp[1] : tool;
}

// cardTitle is what sits where an edit's file name would. A command's own
// first line is left to the body below.
function cardTitle(req, what) {
  if (req.tool === "Bash" || req.tool === "PowerShell") return req.input?.description || "";
  const mcp = /^mcp__.+?__(.+)$/.exec(req.tool);
  if (mcp) return mcp[1];
  return what.target && what.target !== req.tool ? what.target : "";
}

function Command({ input, lang }) {
  const [, force] = useState(0);
  useEffect(() => {
    ensureLanguage(lang, () => force((n) => n + 1));
  }, [lang]);
  const ready = langReady(lang);
  const html = useMemo(() => highlightLines("prompt:" + input.command, (input.command || "").split("\n"), lang), [input.command, lang, ready]);
  return (
    <pre className="prompt-command">
      {html.map((h, n) => (
        <code key={n} dangerouslySetInnerHTML={{ __html: h || "&nbsp;" }} />
      ))}
    </pre>
  );
}

// Fields shows any other call as its arguments, which is all a hook is told.
function Fields({ input }) {
  const entries = Object.entries(input || {});
  if (!entries.length) return <div className="file-note">No arguments.</div>;
  return (
    <dl className="prompt-fields">
      {entries.map(([k, v]) => (
        <div key={k}>
          <dt>{k}</dt>
          <dd>{typeof v === "string" ? v : JSON.stringify(v, null, 2)}</dd>
        </div>
      ))}
    </dl>
  );
}

// describe phrases a request for the header.
function describe(req) {
  const input = req.input || {};
  const firstLine = (s) => (s || "").split("\n")[0];
  const p = req.previews?.[0];
  // A Codex patch can change several files at once.
  if (input.files?.length > 1) return { verb: "edit", target: `${input.files.length} files`, many: true };
  switch (req.tool) {
    case "Edit":
      return { verb: "edit", target: p?.path || input.file_path, path: true };
    case "Write":
      return { verb: p?.diff?.status === "A" ? "create" : "overwrite", target: p?.path || input.file_path, path: true };
    case "NotebookEdit":
      return { verb: "edit a notebook", target: input.notebook_path, path: true };
    case "Bash":
    case "PowerShell":
      return { verb: "run", target: firstLine(input.command) };
    case "WebFetch":
      return { verb: "fetch", target: input.url };
    case "WebSearch":
      return { verb: "search the web for", target: input.query };
    case "ExitPlanMode":
      return { verb: "start on this plan", target: input.planFilePath, path: true };
    case "AskUserQuestion":
      return { verb: "ask you", target: "" };
  }
  const mcp = /^mcp__(.+?)__(.+)$/.exec(req.tool);
  if (mcp) return { verb: "use", target: `${mcp[1]}: ${mcp[2]}` };
  return { verb: "use", target: req.tool };
}

// question is the line the terminal would ask above its options.
function question(req, what) {
  const name = what.path && what.target ? splitPath(what.target)[1] : "";
  if (what.many) return `Make these edits to ${what.target}?`;
  switch (req.tool) {
    case "Edit":
    case "NotebookEdit":
      return `Make this edit to ${name}?`;
    case "Write":
      return `${what.verb === "create" ? "Create" : "Overwrite"} ${name}?`;
    case "Bash":
    case "PowerShell":
      return "Run this command?";
    case "WebFetch":
      return "Fetch this page?";
    case "ExitPlanMode":
      return "Start on this plan?";
  }
  return "Allow this?";
}

function optionHint(o, said, no) {
  if (!o.allow) return said ? `sends ${said} as the reason` : no;
  const parts = [];
  if (o.where) parts.push(o.where === "this session" ? "for this session" : `saved to ${o.where}`);
  if (said) parts.push(`with ${said}`);
  return parts.join(" · ");
}

// saidWith names what goes with an answer.
function saidWith(note, comments) {
  const n = comments.length;
  return [note.trim() && "your note", n === 1 ? "your comment" : n > 1 && `your ${n} comments`].filter(Boolean).join(" and ");
}

// withComments is the note with the comments on the edit after it, set out as
// the Agent view sets out comments in a message.
function withComments(note, comments, req) {
  const on = req.tool === "ExitPlanMode" ? "your plan" : "your proposed edit";
  const parts = comments.map((c) => {
    const lines = c.startLine === c.endLine ? `${c.startLine}` : `${c.startLine}-${c.endLine}`;
    const quote = c.quote.map((l) => `> ${l}\n`).join("");
    return `<comment path="${c.path}" lines="${lines}" side="${c.side}" on="${on}">\n${quote}${c.body}\n</comment>`;
  });
  return [note.trim(), ...parts].filter(Boolean).join("\n\n");
}

// commentEvent asks the card a line is in to open a comment on it, for the c key.
export function commentEvent(cell) {
  return new CustomEvent("dv:comment", { detail: { side: cell.dataset.side, line: Number(cell.dataset.line) } });
}

// firstChange is the line of the file on disk where the edit begins.
function firstChange(fd) {
  const op = fd?.ops?.find((o) => o.k !== 0);
  return op ? op.os + 1 : 1;
}
