import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import MarkdownIt from "markdown-it";
import { linkPaths, Markdown } from "./links.js";
import { AttachButton } from "./Threads.jsx";
import { say } from "./Notices.jsx";
import { Modal } from "./Overlays.jsx";
import { AgentIcon, IconCheck, IconChevron, IconChevronDown, IconDots, IconEdit, IconPlus, IconX } from "./icons.jsx";
import { cx, modKey, relTime, useDismiss, usePersisted } from "./util.js";

const md = linkPaths(new MarkdownIt({ html: false, linkify: true, breaks: true }));

const STATUSES = [
  { id: "doing", label: "Doing" },
  { id: "open", label: "Open" },
  { id: "done", label: "Done" },
];
const STATUS = Object.fromEntries(STATUSES.map((s) => [s.id, s]));
const STATUS_CHOICES = ["open", "doing", "done"].map((id) => ({ ...STATUS[id], icon: <StatusIcon status={id} /> }));

const PRIORITIES = [
  { id: "high", label: "High", bars: 3 },
  { id: "medium", label: "Medium", bars: 2 },
  { id: "low", label: "Low", bars: 1 },
  { id: "", label: "No priority", bars: 0 },
];
const PRIORITY = Object.fromEntries(PRIORITIES.map((p, rank) => [p.id, { ...p, rank }]));
const PRIORITY_CHOICES = PRIORITIES.map((p) => ({ ...p, icon: <PriorityIcon priority={p.id} /> }));

// openNotes is how many are still to be done, which the panel's switch counts.
export const openNotes = (notes) => notes.filter((n) => n.status !== "done").length;

// noteText is a note as it goes into a message box.
export const noteText = (note) => [note.title, note.body].filter(Boolean).join("\n\n");

// Notes is the repository's list, grouped by status, in the right-hand panel
// beside the comments. composing is whether the new note's form is open.
export function Notes({ notes, composing, onComposing, onAdd, onPatch, onDelete, onSend }) {
  const [filter, setFilter] = useState("");
  const [label, setLabel] = useState("");
  // The note open over the page, { id, edit }; gone from the list, it closes.
  const [opened, setOpened] = useState(null);
  const shown = opened && notes.find((n) => n.id === opened.id);
  const quickPatch = (note, patch) => onPatch(note, patch).catch((e) => say("Could not change the note", e.message));
  const [folded, setFolded] = usePersisted("notesFolded", ["done"]);
  const known = useMemo(() => [...new Set(notes.flatMap((n) => n.labels || []))].sort((a, b) => a.localeCompare(b)), [notes]);
  // A label filtered on that no note has any more goes with it.
  const shownLabel = known.includes(label) ? label : "";

  const groups = useMemo(() => {
    const q = filter.trim().toLowerCase();
    const keep = (n) =>
      (!shownLabel || n.labels?.includes(shownLabel)) &&
      (!q || [n.title, n.body, n.author, ...(n.labels || [])].some((s) => s?.toLowerCase().includes(q)));
    const byStatus = Object.fromEntries(STATUSES.map((s) => [s.id, []]));
    for (const n of notes) if (keep(n)) byStatus[n.status]?.push(n);
    // What is to be done by priority, then newest; what is done, latest done first.
    const toDo = (a, b) => PRIORITY[a.priority || ""].rank - PRIORITY[b.priority || ""].rank || b.createdAt.localeCompare(a.createdAt);
    byStatus.doing.sort(toDo);
    byStatus.open.sort(toDo);
    byStatus.done.sort((a, b) => b.updatedAt.localeCompare(a.updatedAt));
    return STATUSES.map((s) => ({ ...s, notes: byStatus[s.id] })).filter((g) => g.notes.length > 0);
  }, [notes, filter, shownLabel]);

  const fold = (id) => setFolded((f) => (f.includes(id) ? f.filter((x) => x !== id) : [...f, id]));
  const filtering = !!(filter.trim() || shownLabel);

  return (
    <>
      <div className="sidebar-filter notes-filter">
        {shownLabel && (
          <span className="note-label on" title="Only the notes with this label">
            {shownLabel}
            <button onClick={() => setLabel("")} title="Show every label">
              <IconX size={9} />
            </button>
          </span>
        )}
        <input
          value={filter}
          placeholder="Filter notes"
          onChange={(e) => setFilter(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Escape" && filter) setFilter("");
            e.stopPropagation();
          }}
        />
        <button className={cx("ghost", composing && "on")} onClick={() => onComposing(true)} title="New note">
          <IconPlus size={13} />
        </button>
      </div>
      {composing && (
        <NoteOverlay
          // Filtered on a label, a new note starts with it.
          labels={shownLabel ? [shownLabel] : []}
          known={known}
          onSave={onAdd}
          onClose={() => onComposing(false)}
        />
      )}
      {shown && (
        <NoteOverlay
          key={shown.id}
          note={shown}
          editing={opened.edit}
          known={known}
          onEditing={(edit) => setOpened({ id: shown.id, edit })}
          onSave={(patch) => onPatch(shown, patch)}
          onPatch={quickPatch}
          onDelete={() => {
            setOpened(null);
            onDelete(shown);
          }}
          onSend={(to) => {
            setOpened(null);
            onSend(shown, to);
          }}
          onLabel={(l) => {
            setOpened(null);
            setLabel(l);
          }}
          onClose={() => setOpened(null)}
        />
      )}
      <div className="comment-list note-list">
        {notes.length === 0 && (
          <div className="empty">
            Nothing noted yet. Keep here what is for later; agents add theirs with <code>dv note</code>.
            <div>
              <button className="link" onClick={() => onComposing(true)}>
                New note
              </button>
            </div>
          </div>
        )}
        {notes.length > 0 && groups.length === 0 && filtering && <div className="empty">No notes match.</div>}
        {groups.map((g) => {
          // A filter shows what it found, folded or not.
          const open = filtering || !folded.includes(g.id);
          return (
            <section className="note-group" key={g.id}>
              <button className="note-group-head" onClick={() => fold(g.id)} aria-expanded={open} disabled={filtering}>
                <IconChevron size={11} className={cx("twisty", open && "open")} />
                {g.label}
                <span className="count">{g.notes.length}</span>
              </button>
              {open &&
                g.notes.map((n) => (
                  <NoteCard
                    key={n.id}
                    note={n}
                    label={shownLabel}
                    onLabel={(l) => setLabel((was) => (was === l ? "" : l))}
                    onPatch={quickPatch}
                    onOpen={(edit) => setOpened({ id: n.id, edit })}
                    onDelete={onDelete}
                    onSend={onSend}
                  />
                ))}
            </section>
          );
        })}
      </div>
    </>
  );
}

function NoteCard({ note, label, onLabel, onPatch, onOpen, onDelete, onSend }) {
  // Its words open it, unless the click followed a link or ended a selection
  // being made to copy them.
  const open = (e) => {
    if (e.target.closest("a") || window.getSelection()?.toString()) return;
    onOpen(false);
  };
  const by = byline(note.author);
  const when = new Date(note.createdAt).toLocaleString();
  return (
    <article className={cx("note", "st-" + note.status)}>
      <ChoiceMenu
        className="note-status"
        title={`${STATUS[note.status].label}: change it`}
        label={<StatusIcon status={note.status} />}
        choices={STATUS_CHOICES}
        value={note.status}
        onPick={(status) => onPatch(note, { status })}
      />
      <div className="note-main">
        <div className="note-head">
          {/* Not a button, whose text cannot be selected. */}
          <div className="note-title" role="button" tabIndex={0} onClick={open} onKeyDown={(e) => e.key === "Enter" && onOpen(false)}>
            {note.title}
          </div>
          <ChoiceMenu
            className={cx("note-priority", !note.priority && "unset")}
            title={note.priority ? `${PRIORITY[note.priority].label} priority: change it` : "Set a priority"}
            label={<PriorityIcon priority={note.priority || ""} />}
            choices={PRIORITY_CHOICES}
            value={note.priority || ""}
            onPick={(priority) => onPatch(note, { priority })}
          />
        </div>
        {note.body && (
          <div onClick={open}>
            <Markdown md={md} text={note.body} className="markdown note-body" />
          </div>
        )}
        <div className="note-meta">
          {note.labels?.map((l) => (
            <button key={l} className={cx("note-label", l === label && "on")} onClick={() => onLabel(l)} title={l === label ? "Show every label" : `Only the notes labelled ${l}`}>
              {l}
            </button>
          ))}
          <span className="note-by" title={`${by.who ? by.who + ", " : ""}${when}${note.updatedAt !== note.createdAt ? ` - changed ${new Date(note.updatedAt).toLocaleString()}` : ""}`}>
            {by.agent !== undefined && <AgentIcon agent={by.agent} size={11} />}
            {by.name && <span>{by.name}</span>}
            {relTime(note.createdAt)}
          </span>
          <span className="spacer" />
          <span className="note-actions">
            {note.status !== "done" && <AttachButton what="this note" onClick={(to) => onSend(note, to)} />}
            <ChoiceMenu
              className="mini note-more"
              title="Edit or delete"
              label={<IconDots size={13} />}
              choices={[
                { id: "edit", label: "Edit" },
                { id: "delete", label: "Delete", danger: true },
              ]}
              onPick={(what) => (what === "edit" ? onOpen(true) : onDelete(note))}
            />
          </span>
        </div>
      </div>
    </article>
  );
}

// byline says who wrote a note: an agent by its mark, the reader not at all.
function byline(author) {
  if (author === "Claude") return { agent: "", who: author };
  if (author === "Codex") return { agent: "codex", who: author };
  if (!author || author === "you") return {};
  return { name: author, who: author };
}

// NoteOverlay is a note over the page, read whole or being written. Without a
// note it is a new one, starting with labels; onSave is given its fields, or
// for a note there is, the ones the form edits. Status and priority, in its
// head, are changed on the spot by onPatch.
function NoteOverlay({ note, labels = [], editing, known, onEditing, onSave, onPatch, onDelete, onSend, onLabel, onClose }) {
  const writing = !note || editing;
  const [status, setStatus] = useState("open");
  const [priority, setPriority] = useState("");
  const pick = note ? (patch) => onPatch(note, patch) : (patch) => ("status" in patch ? setStatus(patch.status) : setPriority(patch.priority));
  // Leaving the form asks first when it would lose what was written there.
  const dirty = useRef(() => false);
  const leave = (then) => () => (!dirty.current() || confirm("Discard what you wrote?")) && then();
  const close = leave(onClose);
  const stop = note && editing ? leave(() => onEditing(false)) : undefined;

  const shownStatus = note ? note.status : status;
  const shownPriority = note ? note.priority || "" : priority;
  return (
    <Modal onClose={close} onBack={stop} className={cx("note-view", writing && "writing")}>
      <div className="viewer-head note-view-head">
        <ChoiceMenu
          className="mini note-pick"
          title="Status"
          label={
            <>
              <StatusIcon status={shownStatus} />
              {STATUS[shownStatus].label}
              <IconChevronDown size={10} />
            </>
          }
          choices={STATUS_CHOICES}
          value={shownStatus}
          onPick={(s) => pick({ status: s })}
        />
        <ChoiceMenu
          className={cx("mini note-pick", !shownPriority && "unset")}
          title="Priority"
          label={
            <>
              <PriorityIcon priority={shownPriority} />
              {shownPriority ? PRIORITY[shownPriority].label : "Priority"}
              <IconChevronDown size={10} />
            </>
          }
          choices={PRIORITY_CHOICES}
          value={shownPriority}
          onPick={(p) => pick({ priority: p })}
        />
        <span className="spacer" />
        {!writing && (
          <>
            {note.status !== "done" && <AttachButton what="this note" onClick={onSend} />}
            <button className="ghost" onClick={() => onEditing(true)} title="Edit (E)">
              <IconEdit size={13} /> Edit
            </button>
            <ChoiceMenu
              className="ghost note-more"
              title="More"
              label={<IconDots size={13} />}
              choices={[{ id: "delete", label: "Delete", danger: true }]}
              onPick={onDelete}
            />
          </>
        )}
        <button className="ghost" onClick={close} title="Close (Esc)">
          <IconX size={13} />
        </button>
      </div>
      {writing ? (
        <NoteForm
          note={note}
          labels={labels}
          known={known}
          dirty={dirty}
          onSave={async (fields) => {
            if (note) {
              await onSave(fields);
              onEditing(false);
            } else {
              await onSave({ ...fields, status, priority });
              onClose();
            }
          }}
          onCancel={note ? stop : close}
        />
      ) : (
        <NoteRead note={note} onEdit={() => onEditing(true)} onLabel={onLabel} />
      )}
    </Modal>
  );
}

function NoteRead({ note, onEdit, onLabel }) {
  const by = byline(note.author);
  const changed = note.updatedAt !== note.createdAt;
  // E edits, as the button's title says; anything typed is in a field of its own.
  useEffect(() => {
    const onKey = (e) => {
      if (e.key !== "e" || e.metaKey || e.ctrlKey || e.altKey || e.target.closest?.("input, textarea")) return;
      e.preventDefault();
      onEdit();
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onEdit]);
  return (
    <div className="note-view-body">
      <h2 className={cx("note-view-title", note.status === "done" && "done")}>{note.title}</h2>
      <div className="note-view-meta">
        {note.labels?.map((l) => (
          <button key={l} className="note-label" onClick={() => onLabel(l)} title={`Only the notes labelled ${l}`}>
            {l}
          </button>
        ))}
        <span
          className="note-by"
          title={`Added ${new Date(note.createdAt).toLocaleString()}${changed ? `, changed ${new Date(note.updatedAt).toLocaleString()}` : ""}`}
        >
          {by.agent !== undefined && <AgentIcon agent={by.agent} size={11} />}
          {by.who ? `${by.who} · ` : ""}added {relTime(note.createdAt)}
          {changed && ` · changed ${relTime(note.updatedAt)}`}
        </span>
      </div>
      {note.body ? (
        <Markdown md={md} text={note.body} className="md-preview note-doc" />
      ) : (
        <p className="note-doc-empty">
          No details.{" "}
          <button className="link" onClick={onEdit}>
            Add some
          </button>
        </p>
      )}
    </div>
  );
}

// NoteForm writes a note's title, labels and details. dirty is set to say
// whether leaving would lose anything.
function NoteForm({ note, labels: startLabels, known, dirty, onSave, onCancel }) {
  const [title, setTitle] = useState(note?.title || "");
  const [body, setBody] = useState(note?.body || "");
  const [labels, setLabels] = useState(note?.labels || startLabels);
  const [labelText, setLabelText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const titleRef = useRef(null);
  const bodyRef = useRef(null);
  useEffect(() => {
    // A new note starts at its title; one being edited, at the end of its words.
    const el = note ? bodyRef.current : titleRef.current;
    el.focus();
    el.selectionStart = el.selectionEnd = el.value.length;
  }, []);

  const typed = labelText.trim();
  const allLabels = typed && !labels.some((l) => l.toLowerCase() === typed.toLowerCase()) ? [...labels, typed] : labels;
  dirty.current = () =>
    title.trim() !== (note?.title || "") || body.trim() !== (note?.body || "") || allLabels.join("\n") !== (note?.labels || startLabels).join("\n");
  useEffect(() => () => (dirty.current = () => false), []);
  const save = async () => {
    if (!title.trim() || busy) return;
    setBusy(true);
    setError("");
    try {
      await onSave({ title: title.trim(), body: body.trim(), labels: allLabels });
    } catch (e) {
      setError(e.message);
      setBusy(false);
    }
  };

  return (
    <div
      className="note-form"
      onKeyDown={(e) => {
        if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
          e.preventDefault();
          save();
        }
      }}
    >
      <div className="note-form-top">
        <input
          ref={titleRef}
          className="note-title-input"
          value={title}
          placeholder="Title"
          onChange={(e) => setTitle(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.metaKey && !e.ctrlKey && !e.nativeEvent.isComposing) {
              e.preventDefault();
              bodyRef.current.focus();
            }
          }}
        />
        <LabelInput labels={labels} onLabels={setLabels} text={labelText} onText={setLabelText} known={known} />
      </div>
      <textarea ref={bodyRef} className="note-body-input" value={body} placeholder="Details, in Markdown" onChange={(e) => setBody(e.target.value)} />
      {error && <div className="composer-error">{error}</div>}
      <div className="note-form-foot">
        <span className="hint">
          {modKey}+Enter to save · Esc to {note ? "stop editing" : "close"}
        </span>
        <span className="spacer" />
        <button className="ghost" onClick={onCancel}>
          Cancel
        </button>
        <button className="primary" onClick={save} disabled={!title.trim() || busy}>
          {busy ? "Saving..." : note ? "Save" : "Add note"}
        </button>
      </div>
    </div>
  );
}

// LabelInput takes labels as chips: Enter or a comma adds what is typed, Tab
// the first label of the notes' that it starts, and Backspace takes the last
// one off. The notes' labels are offered under it while it has focus.
function LabelInput({ labels, onLabels, text, onText, known }) {
  const [focused, setFocused] = useState(false);
  const inputRef = useRef(null);
  const has = (l) => labels.some((x) => x.toLowerCase() === l.toLowerCase());
  const q = text.trim().toLowerCase();
  const offered = known.filter((l) => !has(l) && l.toLowerCase().includes(q)).slice(0, 8);
  const completes = offered.find((l) => l.toLowerCase().startsWith(q));
  const add = (...list) => {
    const next = [...labels];
    for (let l of list) {
      l = l.trim();
      // Written as the notes have it already, as the server would.
      if (l && !next.some((x) => x.toLowerCase() === l.toLowerCase())) next.push(known.find((k) => k.toLowerCase() === l.toLowerCase()) || l);
    }
    onLabels(next);
    onText("");
  };
  return (
    <div className="note-labels-field">
      <div className="note-labels-input" onMouseDown={(e) => e.target === e.currentTarget && (e.preventDefault(), inputRef.current.focus())}>
        {labels.map((l) => (
          <span className="note-label" key={l}>
            {l}
            <button onClick={() => onLabels(labels.filter((x) => x !== l))} title="Take it off">
              <IconX size={9} />
            </button>
          </span>
        ))}
        <input
          ref={inputRef}
          value={text}
          placeholder={labels.length ? "" : "Add labels"}
          onChange={(e) => {
            // A comma, typed or pasted, ends a label.
            const parts = e.target.value.split(",");
            if (parts.length > 1) add(...parts.slice(0, -1));
            onText(parts.at(-1));
          }}
          onFocus={() => setFocused(true)}
          onBlur={() => setFocused(false)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && !e.metaKey && !e.ctrlKey && q) {
              e.preventDefault();
              add(text);
            } else if (e.key === "Tab" && !e.shiftKey && q && completes) {
              e.preventDefault();
              add(completes);
            } else if (e.key === "Backspace" && !text && labels.length) {
              onLabels(labels.slice(0, -1));
            }
          }}
        />
      </div>
      {focused && offered.length > 0 && (
        <div className="note-offered">
          {offered.map((l) => (
            // Before the field loses focus, which would take the offer away.
            <button key={l} className="note-label offer" onMouseDown={(e) => (e.preventDefault(), add(l))}>
              <IconPlus size={9} />
              {l}
            </button>
          ))}
        </div>
      )}
    </div>
  );
}

// ChoiceMenu is a button that opens a short list to pick from, drawn in the
// page's top layer: the panel's list would clip it.
function ChoiceMenu({ className, title, label, choices, value, onPick }) {
  const [open, setOpen] = useState(false);
  const menu = useRef(null);
  const ref = useDismiss(open, () => setOpen(false), menu);
  const [at, setAt] = useState(null);
  useLayoutEffect(() => {
    if (!open) return setAt(null);
    const r = ref.current.firstChild.getBoundingClientRect();
    const { offsetWidth: w, offsetHeight: h } = menu.current;
    const below = r.bottom + 4 + h <= window.innerHeight - 8 || r.top < h + 12;
    setAt({ top: below ? r.bottom + 4 : r.top - 4 - h, left: Math.max(8, Math.min(r.left, window.innerWidth - w - 8)) });
  }, [open]);
  useEffect(() => {
    if (!open) return;
    const close = () => setOpen(false);
    const onScroll = (e) => menu.current?.contains(e.target) || close();
    const onKey = (e) => {
      if (e.key !== "Escape") return;
      e.stopPropagation();
      close();
    };
    window.addEventListener("scroll", onScroll, true);
    window.addEventListener("resize", close);
    window.addEventListener("keydown", onKey, true);
    return () => {
      window.removeEventListener("scroll", onScroll, true);
      window.removeEventListener("resize", close);
      window.removeEventListener("keydown", onKey, true);
    };
  }, [open]);
  return (
    <span className="choice-menu" ref={ref}>
      <button className={className} title={title} aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        {label}
      </button>
      {open &&
        createPortal(
          <div className="model-list choice-list" ref={menu} style={at || { top: 0, left: 0, visibility: "hidden" }}>
            {choices.map((c) => (
              <button
                key={c.id}
                className={cx(c.id === value && "on", c.danger && "danger")}
                onClick={() => {
                  setOpen(false);
                  onPick(c.id);
                }}
              >
                <span className="choice">
                  {c.icon}
                  {c.label}
                  {c.id === value && <IconCheck size={12} className="choice-on" />}
                </span>
              </button>
            ))}
          </div>,
          document.body,
        )}
    </span>
  );
}

function StatusIcon({ status }) {
  return (
    <svg viewBox="0 0 16 16" width={14} height={14} className={cx("status-icon", status)} aria-hidden="true">
      <circle cx="8" cy="8" r="6" className="ring" />
      {status === "doing" && <path d="M8 4.5a3.5 3.5 0 0 1 0 7z" className="fill" />}
      {status === "done" && <path d="M5.3 8.3l1.8 1.8 3.6-3.8" className="tick-mark" />}
    </svg>
  );
}

// PriorityIcon is three bars, as many lit as the priority is high.
function PriorityIcon({ priority }) {
  const lit = PRIORITY[priority].bars;
  return (
    <svg viewBox="0 0 16 16" width={14} height={14} className={cx("priority-icon", priority || "none")} aria-hidden="true">
      {[4, 7.5, 11].map((h, i) => (
        <rect key={i} x={2.5 + i * 4} y={13.5 - h} width={3} height={h} rx={0.8} className={i < lit ? "lit" : ""} />
      ))}
    </svg>
  );
}
