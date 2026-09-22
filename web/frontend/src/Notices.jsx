// Notices tell the reader what the agents want and have done, wherever in the
// page they are: a toast for a session not on screen, and a desktop
// notification as well while the page is not in front of them, if they allowed
// it. dv decides what to tell and when (internal/notify); the page shows it.
import { useEffect, useRef, useState } from "react";
import { Toaster, toast } from "sonner";
import { api, RESTART_KEY } from "./api.js";
import { boot, slug } from "./boot.js";
import { AgentIcon, IconCheck, IconX } from "./icons.jsx";
import { cx, PHONE, useMedia } from "./util.js";

const DONE_MS = 8000;

// Below the header, clear of the message box.
export function Notices() {
  useRestartNotice();
  return (
    <Toaster position="top-right" offset={{ top: 46, right: 14 }} mobileOffset={{ top: 48 }} gap={8} visibleToasts={4} toastOptions={{ unstyled: true }} />
  );
}

// useRestartNotice tells of dv having restarted, seen as the page's connection
// to it coming back from another run. The tab that asked reloads itself and
// says so after; any other still has its old page, so offers a reload when
// the page changed.
function useRestartNotice() {
  useEffect(() => {
    const asked = sessionStorage.getItem(RESTART_KEY);
    sessionStorage.removeItem(RESTART_KEY);
    if (asked) {
      const was = JSON.parse(asked);
      if (was.started !== boot.run.started) {
        toast.custom((t) => <Notice id={t} kind="done" title="dv restarted" detail={restartedOn(was, boot.run)} />, { duration: DONE_MS });
      }
    }
    let seen = boot.run.started;
    const onBack = async () => {
      if (sessionStorage.getItem(RESTART_KEY)) return; // asked here: this tab reloads
      const now = await api.run().catch(() => null);
      if (!now || now.started === seen) return;
      seen = now.started;
      const stale = now.ui !== boot.run.ui;
      toast.custom(
        (t) => (
          <Notice
            id={t}
            kind="done"
            title="dv restarted"
            detail={restartedOn(boot.run, now) + (stale ? " This page is the one from before." : "")}
            actions={stale ? [{ label: "Reload", primary: true, run: () => location.reload() }] : []}
          />
        ),
        { id: "restart", duration: stale ? Infinity : DONE_MS },
      );
    };
    window.addEventListener("dv:reconnected", onBack);
    return () => window.removeEventListener("dv:reconnected", onBack);
  }, []);
}

const named = (v) => (/^\d/.test(v) ? "v" + v : v);

function restartedOn(was, now) {
  if (now.version !== was.version) return `Now ${named(now.version)}, was ${named(was.version)}.`;
  if (now.binary !== was.binary) return "Now a new build.";
  return now.version === "dev" ? "Still the same build." : `Still ${named(now.version)}.`;
}

// On a phone a tap anywhere on a notice does what its action marked opens
// does, and that action's button goes.
function Notice({ id, kind, agent, title, detail, last, actions }) {
  const phone = useMedia(PHONE);
  const opener = phone && actions?.find((a) => a.opens);
  const shown = opener ? actions.filter((a) => a !== opener) : actions;
  const down = useRef(null);
  const onClick = (e) => {
    // Swiping a notice away ends in a click as well.
    const d = down.current;
    if (e.target.closest("button") || !d || Math.hypot(e.clientX - d.x, e.clientY - d.y) > 10) return;
    toast.dismiss(id);
    opener.run();
  };
  return (
    <div
      className={cx("notice", kind, opener && "opens")}
      onPointerDown={(e) => (down.current = { x: e.clientX, y: e.clientY })}
      onClick={opener ? onClick : undefined}
    >
      <span className="notice-icon">{kind === "done" ? <IconCheck size={13} /> : <AgentIcon agent={agent} size={13} />}</span>
      <div className="notice-body">
        <div className="notice-title">{title}</div>
        {detail && <div className="notice-detail">{detail}</div>}
        {last && <div className="notice-last">{last}</div>}
        {shown?.length > 0 && (
          <div className="notice-actions">
            {shown.map((a) => (
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

// say is a notice of the page's own, with actions as a Notice takes them.
export const say = (title, detail, actions) =>
  toast.custom((t) => <Notice id={t} kind="info" title={title} detail={detail} actions={actions} />, { duration: 6000 });

// useHubActivity is what the hub's folders are doing: every folder on the hub's
// own page, the others on a folder's. Outside a hub there is nothing to follow.
export function useHubActivity() {
  const [folders, setFolders] = useState(null);
  useEffect(() => {
    if (boot.page !== "hub" && !boot.base) return;
    return api.hubActivity((e) => setFolders(e.folders.filter((f) => f.slug !== slug)));
  }, []);
  return folders;
}

// This page, to dv's notices, for as long as it is open.
const PAGE = Math.random().toString(36).slice(2) + Date.now().toString(36);

// Using the page is said at most this often; while it has the reader, it says
// so this often anyway, or dv takes it for one left open somewhere asleep.
const INPUT_MS = 2000;
const HEARTBEAT_MS = 30000;

const inFront = () => document.visibilityState === "visible" && document.hasFocus();

// A reply comes whole, for the phone; here it is its start, on one line.
const oneLine = (s = "") => s.replace(/\s+/g, " ").trim().slice(0, 240);

// usePresence tells dv whether the reader is at this page and when they last
// used it - a key, a click, a scroll, coming back to it - which is what puts
// off sending a notice on to their phone, and counts a turn's end as seen.
function usePresence() {
  useEffect(() => {
    let used = 0;
    let said = 0;
    let timer = 0;
    const report = (input) => {
      clearTimeout(timer);
      timer = 0;
      if (input) said = Date.now();
      api.presence({ page: PAGE, focused: inFront(), input, ago: input ? Date.now() - used : 0 }).catch(() => {});
    };
    const onInput = () => {
      used = Date.now();
      if (!timer) timer = setTimeout(() => report(true), Math.max(0, said + INPUT_MS - used));
    };
    const onFocus = () => (inFront() ? onInput() : report(false));
    const inputs = ["keydown", "pointerdown", "wheel", "touchstart"];
    for (const e of inputs) window.addEventListener(e, onInput, { capture: true, passive: true });
    window.addEventListener("focus", onFocus);
    window.addEventListener("blur", onFocus);
    document.addEventListener("visibilitychange", onFocus);
    const beat = setInterval(() => inFront() && report(false), HEARTBEAT_MS);
    onFocus();
    return () => {
      for (const e of inputs) window.removeEventListener(e, onInput, { capture: true });
      window.removeEventListener("focus", onFocus);
      window.removeEventListener("blur", onFocus);
      document.removeEventListener("visibilitychange", onFocus);
      clearInterval(beat);
      clearTimeout(timer);
    };
  }, []);
}

// useNotices shows dv's notices in the page: a toast for each, but for a
// request in the session on screen, which asks there itself; and a desktop
// notification for one dv marks loud, if the page is not in front of the
// reader. looking is the session on screen in the Agent view; review opens a
// request of this page's folder in its window, open goes to one of its
// sessions, and go to a session in another of the hub's folders. It returns
// the notices open.
export function useNotices({ looking, desktop, review, open, go }) {
  usePresence();
  const [notices, setNotices] = useState([]);
  useEffect(() => api.notices(PAGE, (e) => setNotices(e.notices)), []);
  const toasted = useRef(new Map()); // kinds, by notice
  const told = useRef(new Map()); // desktop notifications, by notice
  const now = useRef();
  now.current = { desktop, review, open, go };

  useEffect(() => {
    const ids = new Set(notices.map((n) => n.id));
    // A request's go once it is answered, wherever. A turn's end keeps its
    // toast for its time, and its notification until it is dismissed.
    for (const [id, kind] of toasted.current) {
      if (ids.has(id)) continue;
      toasted.current.delete(id);
      if (kind === "ask") toast.dismiss(id);
    }
    for (const [id, { kind, shown }] of told.current) {
      if (ids.has(id)) continue;
      told.current.delete(id);
      if (kind === "ask") shown?.close();
    }

    for (const n of notices) {
      const own = (n.folder || "") === slug; // a lone dv's folder is "", and left out
      const asking = n.kind === "ask";
      const session = n.where || (asking ? "a new session" : "A session");
      const where = own ? session : `${n.place}: ${session}`;
      // A command an action ran has no session, only its folder to go to.
      const goThere = () => {
        if (n.session) own ? now.current.open(n.session) : now.current.go(n.folder, n.session);
        else if (!own) location.href = `/${n.folder}/`;
      };
      const onScreen = own && !!n.session && n.session === looking;

      if (n.loud && !told.current.has(n.id)) {
        const shown = inFront() ? null : notify(n.id, asking ? n.title : `${n.title}: ${where}`, asking ? `in ${where}` : oneLine(n.body), goThere);
        told.current.set(n.id, { kind: n.kind, shown });
      }
      // A request waits on screen until the reader moves off it; a turn's end
      // there was seen.
      if (asking && onScreen) {
        toast.dismiss(n.id);
        toasted.current.delete(n.id);
        continue;
      }
      if (toasted.current.has(n.id)) continue;
      toasted.current.set(n.id, n.kind);
      if (onScreen) continue;
      const actions = !n.session
        ? own
          ? []
          : [{ label: "Open folder", opens: true, run: goThere }]
        : !asking
        ? [{ label: "Open session", opens: true, run: goThere }]
        : own
          ? [
              { label: "Review", primary: true, run: () => now.current.review(n.request) },
              { label: "Open session", opens: true, run: goThere },
            ]
          : [{ label: "Open session", primary: true, opens: true, run: goThere }];
      toast.custom(
        (t) => (
          <Notice
            id={t}
            kind={n.kind}
            agent={n.agent}
            title={n.title}
            detail={asking ? `in ${where}` : where}
            last={asking ? "" : oneLine(n.body)}
            actions={actions}
          />
        ),
        { id: n.id, duration: asking ? Infinity : DONE_MS },
      );
    }

    function notify(tag, title, body, onClick) {
      if (!now.current.desktop || typeof Notification === "undefined" || Notification.permission !== "granted") return null;
      try {
        const shown = new Notification(title, { body, tag });
        shown.onclick = () => {
          window.focus();
          onClick();
          shown.close();
        };
        return shown;
      } catch {
        return null;
      }
    }
  }, [notices, looking]);

  return notices;
}
