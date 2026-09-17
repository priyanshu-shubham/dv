import { createContext, useContext, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import MarkdownIt from "markdown-it";
import { cx, isSearchKey, LRM, modKey, relTime, searchSeed, useDismiss } from "./util.js";
import { IconCheck, IconChevronDown, IconNewSession, IconSpark, IconX } from "./icons.jsx";

// Comment bodies are markdown. Links are rendered but HTML is not, since the
// text is written locally and there is no reason to let it inject markup.
const md = new MarkdownIt({ html: false, linkify: true, breaks: true });

export function ThreadList({ threads, onAction, compact }) {
  return (
    <div className={cx("threads", compact && "threads-compact")}>
      {threads.map((t) => (
        <Thread key={t.id} thread={t} onAction={onAction} compact={compact} />
      ))}
    </div>
  );
}

function Thread({ thread, onAction, compact }) {
  const [replying, setReplying] = useState(false);
  const [editing, setEditing] = useState(null);

  return (
    <article className={cx("thread", thread.resolved && "resolved")}>
      {!compact && thread.quote?.length > 0 && (
        <pre className="quote">
          {thread.quote.slice(0, 6).join("\n")}
          {thread.quote.length > 6 ? "\n..." : ""}
        </pre>
      )}
      {compact && (
        <button className="thread-loc" onClick={() => onAction({ type: "jump", thread })} title="Jump to this line">
          {LRM}
          {thread.file}
          {thread.startLine > 0 && (
            <span className="dim">
              :{thread.startLine}
              {thread.endLine !== thread.startLine ? `-${thread.endLine}` : ""}
            </span>
          )}
        </button>
      )}

      {thread.comments.map((c, i) => (
        <div className="comment" key={c.id}>
          <div className="comment-head">
            <strong>{c.author}</strong>
            <span className="dim">{relTime(c.createdAt)}</span>
            {c.updatedAt && <span className="dim">(edited)</span>}
            <span className="spacer" />
            <div className="comment-actions">
              <button onClick={() => setEditing({ id: c.id, body: c.body })}>Edit</button>
              <button onClick={() => onAction({ type: "deleteComment", thread, commentId: c.id })}>Delete</button>
            </div>
          </div>
          {editing?.id === c.id ? (
            <Composer
              initial={editing.body}
              submitLabel="Save"
              autoFocus
              onCancel={() => setEditing(null)}
              onSubmit={async (body) => {
                await onAction({ type: "editComment", thread, commentId: c.id, body });
                setEditing(null);
              }}
            />
          ) : (
            <div className="markdown" dangerouslySetInnerHTML={{ __html: md.render(c.body) }} />
          )}
          {/* A draft goes out with an answer, so there is nothing to reply to or resolve. */}
          {i === thread.comments.length - 1 && !replying && !editing && !thread.draft && (
            <div className="thread-foot">
              <button className="link" onClick={() => setReplying(true)}>
                Reply
              </button>
              <button
                className="link"
                onClick={() => onAction({ type: "resolve", thread, resolved: !thread.resolved })}
              >
                {thread.resolved ? "Reopen" : "Resolve"}
              </button>
              {thread.resolved && (
                <span className="resolved-tag">
                  <IconCheck size={12} /> resolved
                </span>
              )}
            </div>
          )}
        </div>
      ))}

      {replying && (
        <Composer
          autoFocus
          submitLabel="Reply"
          onCancel={() => setReplying(false)}
          onSubmit={async (body) => {
            await onAction({ type: "reply", thread, body });
            setReplying(false);
          }}
        />
      )}
    </article>
  );
}

// AttachTarget is the sessions code can be added to, and the one it goes to:
// { choices: [{ id, label }], target, onTarget }, target "" being a new session
// when there is none to choose.
export const AttachTarget = createContext(null);

// AttachButton adds code to what goes with the next message to Claude, in the
// Agent view. onClick is given the session it goes to, "" for a new one; its
// menu picks another, which stays picked.
export function AttachButton({ className, onClick, title = "Add to your next message to Claude (a)" }) {
  const t = useContext(AttachTarget);
  const [at, setAt] = useState(null); // where the open menu goes, under the button
  const menu = useRef(null);
  const ref = useDismiss(!!at, () => setAt(null), menu);
  // Fixed where it opened, it closes rather than drift from the button.
  useEffect(() => {
    if (!at) return;
    const close = () => setAt(null);
    const onScroll = (e) => menu.current?.contains(e.target) || close();
    window.addEventListener("scroll", onScroll, true);
    window.addEventListener("resize", close);
    return () => {
      window.removeEventListener("scroll", onScroll, true);
      window.removeEventListener("resize", close);
    };
  }, [at]);
  const to = t?.choices.find((c) => c.id === t.target);
  const picks = t?.choices.length > 1;
  return (
    <span className={cx("attach-split", className)} ref={ref}>
      {/* Add, not send: it waits in that session's message box for you to send. */}
      {/* Where it goes is in the title and marked in the menu, which keeps the button short. */}
      <button
        className={cx("attach-btn", picks && "attach-picked")}
        onClick={() => onClick(t?.target)}
        title={t ? `${title}, in ${to ? to.label : "a new session"}` : title}
      >
        <IconSpark size={12} />
        <span className="btn-label">Add</span>
      </button>
      {picks && (
        <button
          className="attach-btn attach-to"
          onClick={() => {
            const r = ref.current.getBoundingClientRect();
            setAt(at ? null : { top: r.bottom + 4, right: window.innerWidth - r.right });
          }}
          title="Pick the session to add to"
          aria-expanded={!!at}
        >
          <IconChevronDown size={10} />
        </button>
      )}
      {to && (
        <button className="attach-btn attach-new" onClick={() => onClick("")} title="Add to a new session, and go to it">
          <IconNewSession size={13} />
        </button>
      )}
      {/* In the page's top layer: a file card clips what overflows it. */}
      {at &&
        createPortal(
        <div className="model-list attach-menu" ref={menu} style={at}>
          <div className="menu-label">Add to</div>
          {t.choices.map((c) => (
            <button
              key={c.id}
              className={cx(c.id === t.target && "on")}
              onClick={() => {
                setAt(null);
                t.onTarget(c.id);
                onClick(c.id);
              }}
            >
              <span className="model-name">{c.label}</span>
            </button>
          ))}
        </div>,
          document.body,
        )}
    </span>
  );
}

// Composer is the single text box used for new threads, replies and edits.
// Cmd/Ctrl+Enter submits. Escape cancels only when that loses nothing typed;
// a draft is let go of with Cancel. `selected` is the code whose selection
// opened it: opening the composer took that selection away, so the search keys
// look for it here.
export function Composer({
  title, initial = "", submitLabel = "Comment", autoFocus, aside, selected = "", onSearch, onSubmit, onCancel,
}) {
  const [body, setBody] = useState(initial);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const ref = useRef(null);
  const bodyRef = useRef(initial);
  const pressedInside = useRef(null); // the last mousedown in the composer
  bodyRef.current = body;
  const draft = body.trim() !== initial.trim();

  useEffect(() => {
    if (autoFocus && ref.current) {
      ref.current.focus();
      ref.current.selectionStart = ref.current.value.length;
    }
  }, [autoFocus]);

  // Clicking away dismisses a composer nobody has typed into, so opening one by
  // accident costs nothing. Anything half-written stays put, and an edit of an
  // existing comment is never dismissed this way. Inside is the composer's React
  // tree rather than its DOM, which takes in the Add to menu portaled out of it.
  useEffect(() => {
    if (!onCancel || initial !== "") return;
    const onDown = (e) => {
      if (e === pressedInside.current || bodyRef.current.trim() !== "") return;
      onCancel();
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [onCancel, initial]);

  const submit = async () => {
    if (!body.trim() || busy) return;
    setBusy(true);
    setError("");
    try {
      await onSubmit(body.trim());
      setBody("");
    } catch (e) {
      setError(e.message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div
      className="composer"
      onMouseDownCapture={(e) => (pressedInside.current = e.nativeEvent)}
      data-draft={draft || undefined}
      data-selected={selected || undefined}
    >
      {title && <div className="composer-title">{title}</div>}
      <textarea
        ref={ref}
        value={body}
        rows={Math.min(12, Math.max(3, body.split("\n").length + 1))}
        placeholder="Leave a comment. Markdown supported."
        onChange={(e) => setBody(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
            e.preventDefault();
            submit();
          } else if (e.key === "Escape") {
            e.preventDefault();
            if (!draft) onCancel?.();
          } else if (onSearch && isSearchKey(e)) {
            e.preventDefault();
            const t = e.currentTarget;
            onSearch(searchSeed(t.value.slice(t.selectionStart, t.selectionEnd)) || selected);
            // The code was selected to search it, not to comment on it.
            if (selected && !draft) onCancel?.();
          }
          e.stopPropagation();
        }}
      />
      {error && <div className="composer-error">{error}</div>}
      <div className="composer-actions">
        <span className="dim hint">{modKey}+Enter to save</span>
        {aside}
        <span className="spacer" />
        {onCancel && (
          <button className="ghost" onClick={onCancel}>
            <IconX size={12} /> Cancel
          </button>
        )}
        <button className="primary" onClick={submit} disabled={!body.trim() || busy}>
          {busy ? "Saving..." : submitLabel}
        </button>
      </div>
    </div>
  );
}
