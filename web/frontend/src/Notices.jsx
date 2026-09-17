// Notices tell the reader what Claude's sessions want and have done, wherever
// in the page they are: a toast for a session not on screen, and a desktop
// notification as well while the tab is in the background, if they allowed it.
import { useEffect, useRef, useState } from "react";
import { Toaster, toast } from "sonner";
import { headline } from "./ClaudePrompt.jsx";
import { IconCheck, IconSpark, IconX } from "./icons.jsx";
import { cx } from "./util.js";

// A turn counts as over once idle has held this long: a terminal's record can
// read idle for a moment between steps.
const SETTLE_MS = 1500;
const DONE_MS = 8000;

// Below the header, clear of the message box.
export const Notices = () => <Toaster position="top-right" offset={{ top: 46, right: 14 }} gap={8} visibleToasts={4} toastOptions={{ unstyled: true }} />;

function Notice({ id, kind, title, detail, last, actions }) {
  return (
    <div className={cx("notice", kind)}>
      <span className="notice-icon">{kind === "done" ? <IconCheck size={13} /> : <IconSpark size={13} />}</span>
      <div className="notice-body">
        <div className="notice-title">{title}</div>
        {detail && <div className="notice-detail">{detail}</div>}
        {last && <div className="notice-last">{last}</div>}
        {actions?.length > 0 && (
          <div className="notice-actions">
            {actions.map((a) => (
              <button
                key={a.label}
                className={a.primary ? "primary" : "ghost"}
                onClick={() => {
                  toast.dismiss(id);
                  a.run();
                }}
              >
                {a.label}
              </button>
            ))}
          </div>
        )}
      </div>
      <button className="notice-close" onClick={() => toast.dismiss(id)} title="Dismiss">
        <IconX size={11} />
      </button>
    </div>
  );
}

// say is a notice of the page's own.
export const say = (title, detail) => toast.custom((t) => <Notice id={t} kind="info" title={title} detail={detail} />, { duration: 6000 });

// useNotices raises them. looking is the session on screen in the Agent view,
// which says for itself what it is doing; review opens a request's window, open
// goes to a session. It returns how many turns ended while the tab was hidden.
export function useNotices({ requests, sessions, looking, desktop, review, open }) {
  const told = useRef(new Set()); // request ids, told of on the desktop if at all
  const toasted = useRef(new Set()); // request ids with a toast up
  const busy = useRef(null); // session id -> busy, as last heard
  const settling = useRef(new Map()); // session id -> timer
  const shown = useRef(new Map()); // desktop notifications by tag
  const [ended, setEnded] = useState(0);
  const now = useRef();
  now.current = { requests, looking, desktop, review, open };

  const notify = (tag, title, body, onClick) => {
    if (!now.current.desktop || !document.hidden || typeof Notification === "undefined" || Notification.permission !== "granted") return;
    try {
      const n = new Notification(title, { body, tag });
      n.onclick = () => {
        window.focus();
        onClick();
        n.close();
      };
      shown.current.set(tag, n);
    } catch {}
  };
  const unnotify = (tag) => {
    toast.dismiss(tag);
    shown.current.get(tag)?.close();
    shown.current.delete(tag);
  };

  // A request has a toast while its session is not on screen - including one
  // that came while it was, and is still waiting when the reader moves off -
  // and is told of on the desktop once. Both go once it is answered, wherever.
  useEffect(() => {
    const waiting = new Set(requests.map((r) => r.id));
    for (const id of told.current) {
      if (!waiting.has(id)) {
        unnotify("ask:" + id);
        told.current.delete(id);
        toasted.current.delete(id);
      }
    }
    for (const r of requests) {
      const tag = "ask:" + r.id;
      const title = `Claude wants to ${headline(r)}`;
      const where = `in ${r.title || "a new session"}`;
      if (!told.current.has(r.id)) {
        told.current.add(r.id);
        notify(tag, title, where, () => now.current.open(r.session));
      }
      if (r.session === looking) {
        toast.dismiss(tag);
        toasted.current.delete(r.id);
      } else if (!toasted.current.has(r.id)) {
        toasted.current.add(r.id);
        toast.custom(
          (t) => (
            <Notice
              id={t}
              kind="ask"
              title={title}
              detail={where}
              actions={[
                { label: "Review", primary: true, run: () => now.current.review(r.id) },
                { label: "Open session", run: () => now.current.open(r.session) },
              ]}
            />
          ),
          { id: tag, duration: Infinity },
        );
      }
    }
  }, [requests, looking]);

  // A session going from busy to idle, and staying there, finished its turn.
  useEffect(() => {
    if (!sessions) return;
    const was = busy.current;
    busy.current = new Map(sessions.map((s) => [s.id, !!s.busy]));
    if (!was) return;
    for (const s of sessions) {
      if (s.busy) {
        clearTimeout(settling.current.get(s.id));
        settling.current.delete(s.id);
        continue;
      }
      if (!was.get(s.id) || settling.current.has(s.id)) continue;
      const timer = setTimeout(() => {
        settling.current.delete(s.id);
        const { requests, looking } = now.current;
        // Waiting on the reader is not the end of a turn.
        if (busy.current.get(s.id) !== false || requests.some((r) => r.session === s.id)) return;
        const tag = "done:" + s.id;
        const title = "Claude finished";
        const where = s.title || "A session";
        if (s.id !== looking) {
          toast.custom(
            (t) => (
              <Notice id={t} kind="done" title={title} detail={where} last={s.last} actions={[{ label: "Open session", run: () => now.current.open(s.id) }]} />
            ),
            { id: tag, duration: DONE_MS },
          );
        }
        if (document.hidden) setEnded((n) => n + 1);
        notify(tag, `${title}: ${where}`, s.last || "", () => now.current.open(s.id));
      }, SETTLE_MS);
      settling.current.set(s.id, timer);
    }
  }, [sessions]);

  useEffect(() => {
    const onVisible = () => document.hidden || setEnded(0);
    document.addEventListener("visibilitychange", onVisible);
    return () => document.removeEventListener("visibilitychange", onVisible);
  }, []);
  useEffect(() => () => settling.current.forEach(clearTimeout), []);

  return ended;
}
