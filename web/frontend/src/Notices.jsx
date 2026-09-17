// Notices tell the reader what Claude's sessions want and have done, wherever
// in the page they are: a toast for a session not on screen, and a desktop
// notification as well while the tab is in the background, if they allowed it.
import { useEffect, useMemo, useRef, useState } from "react";
import { Toaster, toast } from "sonner";
import { headline } from "./AgentPrompt.jsx";
import { api } from "./api.js";
import { boot, slug } from "./boot.js";
import { AgentIcon, IconCheck, IconX } from "./icons.jsx";
import { agentName, cx } from "./util.js";

// A turn counts as over once idle has held this long: a terminal's record can
// read idle for a moment between steps.
const SETTLE_MS = 1500;
const DONE_MS = 8000;

// Below the header, clear of the message box.
export const Notices = () => <Toaster position="top-right" offset={{ top: 46, right: 14 }} gap={8} visibleToasts={4} toastOptions={{ unstyled: true }} />;

function Notice({ id, kind, agent, title, detail, last, actions }) {
  return (
    <div className={cx("notice", kind)}>
      <span className="notice-icon">{kind === "done" ? <IconCheck size={13} /> : <AgentIcon agent={agent} size={13} />}</span>
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

// useHubActivity is what the hub's folders are doing, for notices about the
// ones this page is not on: every folder on the hub's own page, the others on a
// folder's. Outside a hub there is nothing to follow.
export function useHubActivity() {
  const [folders, setFolders] = useState(null);
  useEffect(() => {
    if (boot.page !== "hub" && !boot.base) return;
    return api.hubActivity((e) => setFolders(e.folders.filter((f) => f.slug !== slug)));
  }, []);
  return folders;
}

// useNotices raises them. looking is the session on screen in the Agent view,
// which says for itself what it is doing; review opens a request's window, open
// goes to a session, and go to one in another of the hub's folders, which
// elsewhere carries. It returns how many turns ended while the tab was hidden.
export function useNotices({ requests, sessions, elsewhere, looking, desktop, review, open, go }) {
  const told = useRef(new Set()); // keys told of on the desktop, if at all
  const toasted = useRef(new Set()); // keys with a toast up
  const busy = useRef(new Map()); // folder -> session id -> busy, as last heard
  const settling = useRef(new Map()); // key -> timer
  const shown = useRef(new Map()); // desktop notifications by tag
  const [ended, setEnded] = useState(0);
  const now = useRef();
  now.current = { requests, elsewhere, looking, desktop, review, open, go };
  // A folder's own sessions are one scope, each other folder another; a key
  // names a session within its scope, since a folder of one's own is "".
  const scopes = useMemo(
    () => [{ at: "", requests: requests || [], sessions }, ...(elsewhere || []).map((f) => ({ at: f.slug, name: f.name, requests: f.requests || [], sessions: f.sessions }))],
    [requests, sessions, elsewhere],
  );

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
    const waiting = new Set();
    for (const s of scopes) for (const r of s.requests) waiting.add(s.at + ":" + r.id);
    for (const key of told.current) {
      if (!waiting.has(key)) {
        unnotify("ask:" + key);
        told.current.delete(key);
        toasted.current.delete(key);
      }
    }
    for (const s of scopes) {
      for (const r of s.requests) {
        const key = s.at + ":" + r.id;
        const tag = "ask:" + key;
        const title = `${agentName(r.via)} wants to ${headline(r)}`;
        const session = r.title || "a new session";
        const where = s.at ? `in ${s.name}: ${session}` : `in ${session}`;
        const goThere = () => (s.at ? now.current.go(s.at, r.session) : now.current.open(r.session));
        if (!told.current.has(key)) {
          told.current.add(key);
          notify(tag, title, where, goThere);
        }
        if (!s.at && r.session === looking) {
          toast.dismiss(tag);
          toasted.current.delete(key);
        } else if (!toasted.current.has(key)) {
          toasted.current.add(key);
          toast.custom(
            (t) => (
              <Notice
                id={t}
                kind="ask"
                agent={r.via}
                title={title}
                detail={where}
                actions={
                  s.at
                    ? [{ label: "Open session", primary: true, run: goThere }]
                    : [
                        { label: "Review", primary: true, run: () => now.current.review(r.id) },
                        { label: "Open session", run: goThere },
                      ]
                }
              />
            ),
            { id: tag, duration: Infinity },
          );
        }
      }
    }
  }, [scopes, looking]);

  // A session going from busy to idle, and staying there, finished its turn. A
  // folder first heard of only has its sessions noted: nothing ended here yet.
  useEffect(() => {
    for (const scope of scopes) {
      if (!scope.sessions) continue;
      const was = busy.current.get(scope.at);
      busy.current.set(scope.at, new Map(scope.sessions.map((s) => [s.id, !!s.busy])));
      if (!was) continue;
      for (const s of scope.sessions) {
        const key = scope.at + ":" + s.id;
        if (s.busy) {
          clearTimeout(settling.current.get(key));
          settling.current.delete(key);
          continue;
        }
        if (!was.get(s.id) || settling.current.has(key)) continue;
        const timer = setTimeout(() => {
          settling.current.delete(key);
          const { requests, elsewhere, looking } = now.current;
          const asked = scope.at ? (elsewhere || []).find((f) => f.slug === scope.at)?.requests : requests;
          // Waiting on the reader is not the end of a turn.
          if (busy.current.get(scope.at)?.get(s.id) !== false || (asked || []).some((r) => r.session === s.id)) return;
          const tag = "done:" + key;
          const title = `${agentName(s.agent)} finished`;
          const session = s.title || "A session";
          const where = scope.at ? `${scope.name}: ${session}` : session;
          const goThere = () => (scope.at ? now.current.go(scope.at, s.id) : now.current.open(s.id));
          if (scope.at || s.id !== looking) {
            toast.custom((t) => <Notice id={t} kind="done" title={title} detail={where} last={s.last} actions={[{ label: "Open session", run: goThere }]} />, {
              id: tag,
              duration: DONE_MS,
            });
          }
          if (document.hidden) setEnded((n) => n + 1);
          notify(tag, `${title}: ${where}`, s.last || "", goThere);
        }, SETTLE_MS);
        settling.current.set(key, timer);
      }
    }
  }, [scopes]);

  useEffect(() => {
    const onVisible = () => document.hidden || setEnded(0);
    document.addEventListener("visibilitychange", onVisible);
    return () => document.removeEventListener("visibilitychange", onVisible);
  }, []);
  useEffect(() => () => settling.current.forEach(clearTimeout), []);

  return ended;
}
