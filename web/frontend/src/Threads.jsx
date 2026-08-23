import { useEffect, useRef, useState } from "react";
import MarkdownIt from "markdown-it";
import { cx, LRM, modKey, relTime } from "./util.js";
import { IconCheck, IconX } from "./icons.jsx";

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
          {i === thread.comments.length - 1 && !replying && !editing && (
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

// Composer is the single text box used for new threads, replies and edits.
// Cmd/Ctrl+Enter submits, Escape cancels.
export function Composer({ title, initial = "", submitLabel = "Comment", autoFocus, aside, onSubmit, onCancel }) {
  const [body, setBody] = useState(initial);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const ref = useRef(null);
  const rootRef = useRef(null);
  const bodyRef = useRef(initial);
  bodyRef.current = body;

  useEffect(() => {
    if (autoFocus && ref.current) {
      ref.current.focus();
      ref.current.selectionStart = ref.current.value.length;
    }
  }, [autoFocus]);

  // Clicking away dismisses a composer nobody has typed into, so opening one by
  // accident costs nothing. Anything half-written stays put, and an edit of an
  // existing comment is never dismissed this way.
  useEffect(() => {
    if (!onCancel || initial !== "") return;
    const onDown = (e) => {
      if (rootRef.current?.contains(e.target)) return;
      if (bodyRef.current.trim() !== "") return;
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
    <div className="composer" ref={rootRef}>
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
            onCancel?.();
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
