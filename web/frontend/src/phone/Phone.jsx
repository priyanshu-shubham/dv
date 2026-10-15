// The phone app: dv laid out as an Android app - a bar along the top, the four
// tabs along the foot, screens that slide over them and sheets from below.
// App keeps the state and does the work; this draws it for a finger.
import { useEffect, useMemo, useRef, useState } from "react";
import AgentView from "../Agent.jsx";
import { CommentsPanel } from "../Sidebar.jsx";
import { api } from "../api.js";
import { boot, slug } from "../boot.js";
import { cx, PHONE_FOLDER_KEY } from "../util.js";
import { enableBack, useBack } from "./back.js";
import { Icon } from "./icons.jsx";
import { AUTO, PRESETS } from "../Header.jsx";
import { Choice, Menu, Row, Sheet } from "./Sheet.jsx";
import Sessions from "./Sessions.jsx";
import Files from "./Files.jsx";
import { usePush } from "./push.js";
import { endPicking } from "./Pick.jsx";

const TABS = [
  { id: "agent", label: "Agent", icon: "spark" },
  { id: "diff", label: "Diff", icon: "diff" },
  { id: "files", label: "Files", icon: "file", on: "fileOn" },
  { id: "comments", label: "Comments", icon: "comment", on: "commentOn" },
];

const SESSION_KEY = `dv:${slug ? slug + ":" : ""}phone-session`;
const SCREEN_MS = 300; // a screen's slide, as phone.css has it

// Whether the page opens on a session - a notification's, or one picked in
// another of the hub's folders - whose screen then shows. Which one, App has
// read already (util.js pickSession).
function opening() {
  try {
    const o = JSON.parse(sessionStorage.getItem("dv:open") || "null");
    sessionStorage.removeItem("dv:open");
    return o?.slug === slug;
  } catch {
    return false;
  }
}

export default function Phone({ app }) {
  const { meta, mode, onMode } = app;
  useEffect(() => {
    const html = document.documentElement;
    html.classList.add("phone");
    if (boot.base) localStorage.setItem(PHONE_FOLDER_KEY, slug);
    const off = enableBack();
    return () => {
      html.classList.remove("phone");
      off();
    };
  }, []);
  usePush(app.notices);

  // Comments is the page's comments panel, open, so what opens it - an action,
  // a new note - turns to the tab.
  const tab = app.commentsOn ? "comments" : mode === "code" ? "files" : mode;
  const go = (t) => {
    // What is open in the code left behind goes with it: its Back would act
    // on what is no longer on screen. A comment being written is asked about.
    if (t !== tab) {
      if (document.querySelector(".p-tabs .composer[data-draft], .p-file .composer[data-draft]") && !confirm("Discard the comment you're writing?")) return;
      endPicking();
      app.endComposing();
    }
    app.onComments(t === "comments");
    if (t !== "comments") onMode(t === "files" ? "code" : t);
  };
  // Agent is home: Back from any other tab comes to it, as Android's own apps do.
  useBack(tab !== "agent", () => go("agent"));

  // Android reloads an app it put away; the screen it was on comes back.
  const [inSession, setInSession] = useState(() => opening() || !!sessionStorage.getItem(SESSION_KEY));
  const [inCode, setInCode] = useState(false); // a file's screen, over the session's
  useEffect(() => {
    if (inSession) sessionStorage.setItem(SESSION_KEY, "1");
    else sessionStorage.removeItem(SESSION_KEY);
    app.onSessionShown(inSession);
  }, [inSession]);
  // A notice tapped opens its session.
  const shownAt = useRef(app.showSession);
  useEffect(() => {
    if (shownAt.current === app.showSession) return;
    shownAt.current = app.showSession;
    setInCode(false);
    setInSession(true);
  }, [app.showSession]);
  useBack(inSession, () => setInSession(false));
  // Put away, the session stays drawn while its screen slides out.
  const [leaving, setLeaving] = useState(false);
  useEffect(() => {
    if (inSession) return;
    setLeaving(true);
    const t = setTimeout(() => setLeaving(false), SCREEN_MS);
    return () => clearTimeout(t);
  }, [inSession]);
  const openSession = (id) => {
    app.selectSession(id);
    go("agent");
    setInSession(true);
  };
  // A notification tapped while dv is open: the service worker says which.
  const opener = useRef();
  opener.current = openSession;
  useEffect(() => {
    const sw = navigator.serviceWorker;
    if (!sw) return;
    const onMessage = (e) => e.data?.type === "dv:open" && e.data.session && opener.current(e.data.session);
    sw.addEventListener("message", onMessage);
    return () => sw.removeEventListener("message", onMessage);
  }, []);
  const newSession = () => {
    go("agent");
    app.startSession();
    setInSession(true);
  };

  // Something put in the message box from elsewhere - lines, comments, a
  // note - opens its session to write the rest.
  const focusAt = useRef(app.agentProps.boxFocus);
  useEffect(() => {
    if (focusAt.current === app.agentProps.boxFocus) return;
    focusAt.current = app.agentProps.boxFocus;
    setInCode(false);
    setInSession(true);
  }, [app.agentProps.boxFocus]);

  // A file opens over everything else, wherever it was opened from. Showing it
  // turns the page to Files; put away, it turns back to the tab it came over.
  const cameFrom = useRef(null);
  const lastTab = useRef(tab);
  const codeAt = useRef(app.showCode);
  useEffect(() => {
    if (codeAt.current === app.showCode) return;
    codeAt.current = app.showCode;
    cameFrom.current = lastTab.current === "files" ? null : lastTab.current;
    setInCode(true);
  }, [app.showCode]);
  useEffect(() => {
    lastTab.current = tab;
  });
  const openFile = (path) => {
    app.openCode(path);
    cameFrom.current = null;
    setInCode(true);
  };
  const closeCode = () => {
    if (document.querySelector(".p-file .composer[data-draft]") && !confirm("Discard the comment you're writing?")) return;
    endPicking();
    app.endComposing();
    setInCode(false);
    if (cameFrom.current) go(cameFrom.current);
    cameFrom.current = null;
  };
  useBack(inCode, closeCode);

  const [sheet, setSheet] = useState(null); // "folders" | "scope" | "menu"
  const waiting = app.requests.length + (app.elsewhere || []).reduce((n, f) => n + (f.requests?.length || 0), 0);
  const tabs = TABS.filter((t) => t.id !== "diff" || app.modes.includes("diff"));

  const title = meta?.repo || slug || "dv";
  const branch = meta?.head?.branch || (meta?.head?.sha || "").slice(0, 7);
  const changed = app.uncommitted;

  return (
    <div className={cx("p-app", app.offline && "offline")}>
      <header className="p-bar">
        <button className="p-title" onClick={() => (boot.base ? setSheet("folders") : null)} disabled={!boot.base}>
          <span className="p-title-main">
            {title}
            {boot.base && <Icon name="drop" size={20} />}
          </span>
          {(branch || meta?.git === false) && (
            <span className="p-title-sub">
              {branch ? (
                <>
                  <Icon name="branch" size={14} />
                  {branch}
                  {changed > 0 && ` · ${changed} uncommitted`}
                </>
              ) : (
                "Not a git repository"
              )}
            </span>
          )}
        </button>
        <button className="p-icon" onClick={() => app.openOverlay({ type: "actions" })} aria-label="Actions">
          <Icon name="bolt" />
        </button>
        <button className="p-icon" onClick={() => app.openSearch()} aria-label="Search">
          <Icon name="search" />
        </button>
        <button className="p-icon" onClick={() => setSheet("menu")} aria-label="More">
          <Icon name="dots" />
        </button>
      </header>

      <main className="p-tabs">
        <section className="p-tab" hidden={tab !== "agent"}>
          <Sessions
            sessions={app.agent.sessions}
            requests={app.requests}
            elsewhere={app.elsewhere}
            available={app.agent.available || !!app.agent.codex?.available}
            usage={[
              { agent: "", limits: app.agent.usage },
              { agent: "codex", limits: app.agent.codex?.usage },
            ]}
            onOpen={openSession}
            onClose={app.closeSession}
            onRename={app.renameSession}
          />
        </section>

        <section className="p-tab" hidden={tab !== "diff"}>
          <div className="content p-scroll" ref={app.scrollRef}>
            {app.diff && (
              <div className="p-subbar">
                <button className="p-chip" onClick={() => setSheet("scope")}>
                  {app.diff.scope?.label || "Changes"}
                  <Icon name="drop" size={18} />
                </button>
                <span className="p-sum">
                  {app.files.length} {app.files.length === 1 ? "file" : "files"}
                  {app.viewedCount > 0 && ` · ${app.viewedCount} viewed`}
                </span>
              </div>
            )}
            {app.diffInner}
          </div>
        </section>

        <section className="p-tab" hidden={tab !== "files"}>
          <Files
            entries={app.explorer}
            loading={app.explorerLoading}
            shown={tab === "files"}
            threads={app.threads}
            onOpen={openFile}
            onOpenIgnored={app.listIgnored}
          />
        </section>

        <section className="p-tab p-comments" hidden={tab !== "comments"}>
          {tab === "comments" && (
            <CommentsPanel
              {...app.commentsProps}
              // To the line, off this tab: the Diff's, or a file's or session's screen.
              onJump={(thread) => {
                app.onComments(false);
                app.commentsProps.onJump(thread);
              }}
              onClose={() => go("agent")}
            />
          )}
        </section>
      </main>

      {tab === "agent" && (
        <button className="p-fab" onClick={newSession}>
          <Icon name="edit" />
          New session
        </button>
      )}

      <nav className="p-nav">
        {tabs.map((t) => (
          <button key={t.id} className={cx("p-nav-item", tab === t.id && "on")} onClick={() => go(t.id)}>
            <span className="p-nav-ind">
              <Icon name={tab === t.id && t.on ? t.on : t.icon} />
              {t.id === "agent" && waiting > 0 && <span className="p-badge">{waiting}</span>}
              {t.id === "comments" && app.openComments > 0 && <span className="p-badge quiet">{app.openComments}</span>}
            </span>
            {t.label}
          </button>
        ))}
      </nav>

      {/* Screens over the tabs. The code and the session stay put while hidden,
          so going back to them finds the place held. */}
      <div className={cx("p-screen p-file", inCode && "open")} aria-hidden={!inCode}>
        <header className="p-bar">
          <button className="p-icon" onClick={closeCode} aria-label="Back">
            <Icon name="back" />
          </button>
          <div className="p-title path">
            <span className="p-title-main">{app.codePath?.split("/").pop()}</span>
            <span className="p-title-sub">{app.codePath?.split("/").slice(0, -1).join("/")}</span>
          </div>
          <button className="p-icon" onClick={() => app.openSearch()} aria-label="Search">
            <Icon name="search" />
          </button>
        </header>
        <div className="content code-pane p-scroll" ref={app.codeRef}>
          {app.codeInner}
        </div>
      </div>

      <div className={cx("p-screen session", inSession && "open")} aria-hidden={!inSession}>
        {(app.agentVisited || inSession) && (
          <AgentView
            {...app.agentProps}
            active={inSession || leaving}
            phone={{
              onBack: () => setInSession(false),
              onActions: () => app.openOverlay({ type: "actions" }),
            }}
            onClose={async (id) => (await app.closeSession(id)) && setInSession(false)}
          />
        )}
      </div>

      {sheet === "folders" && <Folders elsewhere={app.elsewhere} here={app.requests.length} onClose={() => setSheet(null)} />}
      {sheet === "scope" && <ScopeSheet scope={app.scope} resolved={app.diff?.scope} meta={meta} onScope={app.onScope} onClose={() => setSheet(null)} />}
      {sheet === "menu" && (
        <Menu
          onClose={() => setSheet(null)}
          items={[
            { icon: "settings", label: "Settings", run: () => app.openOverlay({ type: "settings" }) },
            meta?.git && !app.folder && { icon: "branch", label: "Switch branch", run: () => app.openOverlay({ type: "branch" }) },
            meta?.remote && { icon: "pull", label: `Pull ${branch}`, run: () => app.pull() },
            { icon: "file", label: "Open a file", run: () => app.openOverlay({ type: "files" }) },
            boot.base && "-",
            boot.base && { icon: "home", label: "All folders", run: () => (location.href = "/?folders") },
          ]}
        />
      )}
    </div>
  );
}

// Folders is the hub's folders, each saying whether something there waits on
// you. The hub's page, with adding, cloning and tasks, is a tap further.
function Folders({ elsewhere, here, onClose }) {
  const [list, setList] = useState(null);
  useEffect(() => {
    api.hubFolders().then((r) => setList(r.folders || r), () => setList([]));
  }, []);
  const asks = useMemo(() => new Map((elsewhere || []).map((f) => [f.slug, f.requests?.length || 0])), [elsewhere]);
  return (
    <Sheet
      title="Folders"
      onClose={onClose}
      foot={
        <button className="p-btn text" onClick={() => (location.href = "/?folders")}>
          All folders
        </button>
      }
    >
      {list === null && <div className="p-empty">Loading…</div>}
      {list?.map((f) => {
        const n = f.slug === slug ? here : asks.get(f.slug) || 0;
        return (
          <Row
            key={f.slug}
            icon="folder"
            on={f.slug === slug}
            title={f.name || f.slug}
            sub={
              <>
                <span className="mono">{f.path}</span>
                {n > 0 && <span className="p-warn"> · {n === 1 ? "needs you" : `${n} need you`}</span>}
              </>
            }
            onClick={() => (f.slug === slug ? onClose() : (location.href = `/${f.slug}/`))}
          />
        );
      })}
    </Sheet>
  );
}

// ScopeSheet is what the Diff compares: the header's menu, as a sheet.
function ScopeSheet({ scope, resolved, meta, onScope, onClose }) {
  const [custom, setCustom] = useState(scope.kind === "custom" ? scope.rev : "");
  const auto = scope.kind === "auto";
  const base = meta?.defaultBranch || "";
  const pick = (s) => {
    onScope(s);
    onClose();
  };
  return (
    <Sheet title="Compare" onClose={onClose}>
      <Choice title="Automatic" sub={auto && resolved?.label ? `Follows your work · now ${resolved.label}` : "Follows your work"} on={auto} onClick={() => pick(AUTO)} />
      {PRESETS.map((p) => (
        <Choice
          key={p.kind}
          title={p.label}
          sub={p.kind === "head" && meta?.head?.subject ? `${p.hint(base)} · ${meta.head.subject}` : p.hint(base)}
          on={!auto && scope.kind === p.kind}
          onClick={() => pick({ kind: p.kind, rev: "" })}
        />
      ))}
      <div className="p-sub-label">A range</div>
      <form
        className="p-inline-form"
        onSubmit={(e) => {
          e.preventDefault();
          if (custom.trim()) pick({ kind: "custom", rev: custom.trim() });
        }}
      >
        <input className="p-field mono" value={custom} placeholder="main...HEAD, HEAD~3.." autoCapitalize="off" autoCorrect="off" onChange={(e) => setCustom(e.target.value)} />
        <button className="p-btn tonal" type="submit" disabled={!custom.trim()}>
          Go
        </button>
      </form>
      {meta?.branches?.length > 0 && (
        <div className="p-chips wrap">
          {meta.branches.slice(0, 6).map((b) => (
            <button key={b} className={cx("p-chip", scope.rev === `${b}...` && "on")} onClick={() => pick({ kind: "custom", rev: `${b}...` })}>
              <Icon name="branch" size={16} />
              {b}
            </button>
          ))}
        </div>
      )}
    </Sheet>
  );
}
