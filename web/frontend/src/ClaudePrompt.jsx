import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import MarkdownIt from "markdown-it";
import { api } from "./api.js";
import { DiffBody } from "./FileDiff.jsx";
import { ensureLanguage, highlightLines, langReady } from "./highlight.js";
import { cx, isTyping, LRM, splitPath } from "./util.js";
import { IconFile, IconSpark, IconX } from "./icons.jsx";

const NO_COMMENTS = [];
const md = new MarkdownIt({ html: false, linkify: true });

// Keys this soon after a request comes up were meant for whatever had the
// focus before it, and a stray 1 or Enter would answer for the reader.
const ARM_MS = 400;

// useClaudeRequests is the list of Claude Code prompts waiting on the reader.
// useClaudeEvents is the requests waiting on the reader, and the open sessions'
// activity, null until first heard.
export function useClaudeEvents() {
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

// ClaudePrompt is the window Claude Code's requests are answered in, over
// whatever the reader is doing, opened from a request's notice or the header's
// bell. The terminal asks at the same time; whichever answers first wins, and
// the request leaves here either way. It stays mounted while anything waits, so
// a half-written note survives being put away.
export default function ClaudePrompt({
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
      <section className="modal modal-wide prompt" role="dialog" aria-modal="true" aria-label="Claude Code is asking">
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
      <IconSpark size={14} className="spark" />
      <span className="prompt-title">Claude wants to {headline(req)}</span>
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

  const what = describe(req);
  const asking = req.tool === "AskUserQuestion";
  const options = useMemo(() => optionsFor(req), [req]);
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
      if (!o) return;
      setSel(i);
      // A comment still being written would be left out of the answer.
      const unsaved = bodyRef.current?.querySelector(".composer[data-draft] textarea");
      if (unsaved) {
        setError("Your comment is not saved yet: Ctrl+Enter saves it, Cancel drops it.");
        unsaved.focus();
        return;
      }
      answer({ allow: o.allow, note: withComments(note, comments, req.preview?.path), suggestion: o.suggestion });
    },
    [options, answer, note, comments, req.preview?.path],
  );

  // Each request is met at Yes, with the keys held off for a moment.
  useEffect(() => {
    if (!active) return;
    setSel(0);
    armed.current = Date.now() + ARM_MS;
    const g = typeof grab === "function" ? grab() : grab;
    if (g) listRef.current?.focus({ preventScroll: true });
  }, [active, req.id]);

  // On the window, ahead of the page's own shortcuts, so the page behind does
  // not act on keys meant for the request - however the focus has wandered
  // after a click in a diff. Chords pass, so the palettes stay in reach.
  useEffect(() => {
    if (!active) return;
    const onKey = (e) => {
      if (document.querySelector(".backdrop")) return; // a viewer opened over the request has the keys
      if (!listRef.current?.closest(".prompt-backdrop") && document.querySelector(".prompt-backdrop:not([hidden])")) return;
      // A question is answered with the pointer and the text boxes it has.
      if (asking) {
        if (e.key === "Escape" && onLater && !isTyping(e.target)) onLater();
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
      } else if (/^[1-9]$/.test(e.key)) choose(Number(e.key) - 1);
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
        <Questions questions={req.input?.questions || []} busy={busy} onAnswer={answer} />
      ) : (
        <div className="prompt-body" ref={bodyRef}>
          <RequestCard
            req={req}
            what={what}
            comments={comments}
            onComments={(next) => onDraft({ comments: next })}
            view={view}
            contextLines={contextLines}
            wrap={wrap}
            onSymbol={onSymbol}
            onOpenFile={onOpenFile}
          />
        </div>
      )}

      <div className="prompt-answer" hidden={asking}>
        <div className="prompt-question">{question(req, what)}</div>
        <div className="prompt-list">
          <div className="prompt-options" role="listbox" tabIndex={-1} ref={listRef} aria-activedescendant={`opt-${req.id}-${sel}`}>
            {options.map((o, i) => {
              const hint = optionHint(o, said);
              return (
                <button
                  key={i}
                  id={`opt-${req.id}-${i}`}
                  role="option"
                  aria-selected={i === sel}
                  className={cx("palette-row", "prompt-option", i === sel && "on", !o.allow && "no")}
                  disabled={busy}
                  onMouseEnter={() => setSel(i)}
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
              placeholder={options[sel]?.allow === false ? "Tell Claude what to do instead" : "Add a note for Claude - it goes with your answer"}
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
            <span>
              <kbd>↑</kbd> <kbd>↓</kbd> choose
            </span>
            <span>
              <kbd>Enter</kbd> answer
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
                {req.dv ? "Claude is waiting in a session dv runs." : "The terminal is asking too; the first answer counts."}
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

// Questions takes AskUserQuestion's answers: one or more of each question's
// options, or something else written in. Only a session dv runs asks here; in
// a terminal the terminal takes them.
function Questions({ questions, busy, onAnswer }) {
  const [picked, setPicked] = useState({}); // question -> labels
  const [other, setOther] = useState({}); // question -> text
  const pick = (q, label) =>
    setPicked((p) => {
      const had = p[q.question] || [];
      const on = had.includes(label);
      const next = q.multiSelect ? (on ? had.filter((l) => l !== label) : [...had, label]) : on ? [] : [label];
      return { ...p, [q.question]: next };
    });
  const said = (q) => [...(picked[q.question] || []), (other[q.question] || "").trim()].filter(Boolean);
  const ready = questions.length > 0 && questions.every((q) => said(q).length > 0);

  return (
    <div className="prompt-body prompt-questions">
      {questions.map((q) => (
        <div className="prompt-answer" key={q.question}>
          <div className="prompt-question">
            {q.header && <span className="tag-generated">{q.header}</span>} {q.question}
            {q.multiSelect && <span className="dim"> (any that apply)</span>}
          </div>
          <div className="prompt-list">
            <div className="prompt-options">
              {q.options.map((o, i) => {
                const on = (picked[q.question] || []).includes(o.label);
                return (
                  <button key={o.label} className={cx("palette-row", "prompt-option", on && "on")} disabled={busy} onClick={() => pick(q, o.label)}>
                    <span className="badge">{on ? "✓" : i + 1}</span>
                    <span className="label">{o.label}</span>
                    {o.description && <span className="hint">{o.description}</span>}
                  </button>
                );
              })}
            </div>
            <label className="prompt-note">
              <span className="badge">…</span>
              <textarea
                rows={1}
                value={other[q.question] || ""}
                placeholder="Something else"
                onChange={(e) => setOther((o) => ({ ...o, [q.question]: e.target.value }))}
              />
            </label>
          </div>
        </div>
      ))}
      <div className="prompt-question-actions">
        <button className="ghost" disabled={busy} onClick={() => onAnswer({ allow: false })} title="Decline to answer, which stops Claude">
          Skip
        </button>
        <button
          className="primary"
          disabled={!ready || busy}
          onClick={() => onAnswer({ allow: true, answers: Object.fromEntries(questions.map((q) => [q.question, said(q).join(", ")])) })}
        >
          Answer
        </button>
      </div>
    </div>
  );
}

// RequestCard shows what the request would do as a file in the review: an
// edit's diff under its file header, anything else under a header naming the
// tool. A div rather than the diff's section.file, which the page's scroll
// anchoring looks for under the pointer and would find here instead. The edit
// is not in the review yet, so comments on it are only drawn here, until they
// go with the answer.
function RequestCard({ req, what, comments, onComments, view, contextLines, wrap, onSymbol, onOpenFile }) {
  const [expanded, setExpanded] = useState({});
  const [selection, setSelection] = useState(null);
  const [composing, setComposing] = useState(null);
  const [, force] = useState(0);
  const ref = useRef(null);
  const p = req.preview;
  const fd = p?.diff;

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
      comments.map((c) => ({
        id: c.id,
        draft: true,
        file: p?.path,
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
      onComments([...comments, { id, side, startLine, endLine, quote, body, createdAt: new Date().toISOString() }]);
    },
    [comments, onComments],
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
  const shown = fd && !fd.binary && !fd.tooLarge;
  return (
    <div className="file prompt-card" ref={ref}>
      <header className="file-head">
        {fd && (
          <span className={cx("badge", "st-" + fd.status)} title={fd.status === "A" ? "a new file" : "changed"}>
            {fd.status}
          </span>
        )}
        <h3 className="file-path" title={p.path}>
          <span className="dir">
            {LRM}
            {dir}
            {LRM}
          </span>
          <span className="name">{name}</span>
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
        {shown && (
          <DiffBody
            fd={fd}
            view={fd.status === "A" ? "unified" : view}
            oneNumber={fd.status === "A" && view === "split"}
            contextLines={p.problem ? 99 : contextLines}
            expanded={expanded}
            onExpand={expand}
            threads={threads}
            selection={selection}
            setSelection={setSelection}
            composing={composing}
            setComposing={setComposing}
            onStartComment={startComment}
            onComment={comment}
            onThreadAction={threadAction}
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
  if (req.tool === "ExitPlanMode" && req.input?.plan) {
    return <div className="markdown prompt-plan" dangerouslySetInnerHTML={{ __html: md.render(req.input.plan) }} />;
  }
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
  switch (req.tool) {
    case "Edit":
      return { verb: "edit", target: req.preview?.path || input.file_path, path: true };
    case "Write":
      return { verb: req.preview?.diff?.status === "A" ? "create" : "overwrite", target: req.preview?.path || input.file_path, path: true };
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

// headline finishes "Claude wants to" in the title bar: what, and to what.
export function headline(req) {
  const what = describe(req);
  if (req.tool === "Bash" || req.tool === "PowerShell") return "run a command";
  if (!what.target || req.tool === "ExitPlanMode") return what.verb;
  return `${what.verb} ${what.path ? splitPath(what.target)[1] : what.target}`;
}

// question is the line the terminal would ask above its options.
function question(req, what) {
  const name = what.path && what.target ? splitPath(what.target)[1] : "";
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

// optionsFor lists the answers in the terminal's order: yes, the suggestions
// Claude Code offers with it, then no.
function optionsFor(req) {
  const offers = (req.suggestions || [])
    .map((s, i) => ({ ...offer(s), allow: true, suggestion: i }))
    .filter((o) => o.label);
  return [{ label: "Yes", allow: true }, ...offers, { label: "No", allow: false }];
}

const WHERE = {
  session: "this session",
  localSettings: ".claude/settings.local.json",
  projectSettings: ".claude/settings.json",
  userSettings: "~/.claude/settings.json",
};

// offer turns one of Claude Code's suggestions into the option the terminal
// shows for it. One dv cannot put into words is left out.
function offer(s) {
  const where = WHERE[s.destination] || "";
  if (s.type === "setMode") {
    const label = s.mode === "acceptEdits" ? "Yes, allow all edits this session" : `Yes, and switch to ${s.mode} mode`;
    return { label, mode: s.mode, where };
  }
  if (s.type === "addRules" && s.behavior === "allow" && s.rules?.length) {
    const rules = s.rules.map((r) => (r.toolName === "Bash" && r.ruleContent ? r.ruleContent : r.ruleContent ? `${r.toolName}(${r.ruleContent})` : r.toolName));
    return { label: `Yes, and don't ask again for ${rules.join(", ")}`, where, title: rules.join("\n") };
  }
  if (s.type === "addDirectories" && s.directories?.length) {
    return { label: `Yes, and always allow access to ${s.directories.join(", ")}`, where };
  }
  return {};
}

function optionHint(o, said) {
  if (!o.allow) return said ? `sends ${said} as the reason` : "stops Claude";
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
function withComments(note, comments, path) {
  const parts = comments.map((c) => {
    const lines = c.startLine === c.endLine ? `${c.startLine}` : `${c.startLine}-${c.endLine}`;
    const quote = c.quote.map((l) => `> ${l}\n`).join("");
    return `<comment path="${path}" lines="${lines}" side="${c.side}" on="your proposed edit">\n${quote}${c.body}\n</comment>`;
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
