import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { api } from "./api.js";
import { Modal, SettingsOverlay } from "./Overlays.jsx";
import { followPrefs, usePref } from "./prefs.js";
import { Notices, useHubActivity, useNotices } from "./Notices.jsx";
import { copyText, cx, isMac, isTyping, LRM, openFolderSession, PHONE, useDismiss, useFixedMenu, useMedia, usePersisted, workingLabel } from "./util.js";
import { IconBranch, IconChevron, IconDots, IconPlus, IconSettings, IconX } from "./icons.jsx";
import { setTabIcon, tabDot } from "./favicon.js";

const POLL_MS = 2000;
const BUSY_POLL_MS = 700;
// How often git is asked again where each folder stands, which costs a few git
// commands a folder where the rest of a poll costs none.
const DETAILS_MS = 10000;
const NO_SETTINGS = {};

// Hub is the page of a hub: the folders it serves, each opened at /<slug>/,
// with the worktrees made of them under them, and the ways to add one, from
// this machine's folders or by cloning.
export default function Hub() {
  // The settings of every folder's page. The ones kept by the browser are
  // shared by all the folders here, which are pages of this one address.
  const phone = useMedia(PHONE);
  const [theme, setTheme] = usePref("user", "theme", "dark");
  const [settings, setSettings] = usePref("user", "settings", NO_SETTINGS);
  const [contextLines, setContextLines] = usePref("user", "context", 3);
  const [view, setView] = usePersisted("view", "split");
  const [wrapWide, setWrapWide] = usePersisted("wrap", false);
  const [wrapPhone, setWrapPhone] = usePersisted("wrapPhone", true);
  const [notices, setNotices] = usePersisted("desktopNotices", false);
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    document.documentElement.dataset.code = settings.codeColors || "github";
  }, [theme, settings.codeColors]);
  useEffect(() => {
    document.title = `${settings.tabName?.trim() || "dv"} hub`;
  }, [settings.tabName]);

  // "settings", "folder" or "clone", or { worktree } or { hooks }, a folder.
  const [dialog, setDialog] = useState(null);
  useEffect(() => {
    const onKey = (e) => {
      if (e.key !== "," || e.metaKey || e.ctrlKey || e.altKey || isTyping(e.target) || document.querySelector(".backdrop")) return;
      e.preventDefault();
      setDialog("settings");
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const [data, setData] = useState(null);
  // By path, a folder's remote and git status: read as the page loads, as a
  // folder first shows, and every DETAILS_MS.
  const [details, setDetails] = useState({});
  const detailsAt = useRef(0);
  const known = useRef({ data, details });
  known.current = { data, details };
  const [error, setError] = useState("");
  const [offline, setOffline] = useState(false);
  const [filter, setFilter] = useState("");
  // The clone or worktree started here, which is opened once it is done.
  const [awaited, setAwaited] = useState(null);

  // What the folders are doing, for notices about any of them: the hub's page
  // is on none of them.
  const elsewhere = useHubActivity();
  const ended = useNotices({ elsewhere, desktop: notices, go: openFolderSession });
  const dot = tabDot(null, elsewhere, ended);
  useEffect(() => setTabIcon(settings.tabColor, dot), [settings.tabColor, dot]);

  const pickNotices = async (on) => {
    if (!on || typeof Notification === "undefined") return setNotices(false);
    const allowed = Notification.permission === "default" ? await Notification.requestPermission() : Notification.permission;
    setNotices(allowed === "granted");
    if (allowed === "denied") setError("The browser blocks notifications for this page; allow them in its site settings.");
  };

  const load = useCallback(() => {
    const { data, details } = known.current;
    const unread = !data || Date.now() - detailsAt.current > DETAILS_MS || data.folders.some((f) => !f.missing && !(f.path in details));
    return api.hubFolders(unread).then(
      (d) => {
        setData(d);
        setOffline(false);
        followPrefs(d.prefs);
        if (unread) {
          detailsAt.current = Date.now();
          const read = d.folders.filter((f) => !f.missing).map((f) => [f.path, { git: f.git, remote: f.remote, status: f.status }]);
          setDetails((had) => ({ ...had, ...Object.fromEntries(read) }));
        }
      },
      (e) => (e instanceof TypeError ? setOffline(true) : setError(e.message)),
    );
  }, []);

  const busy = data?.jobs.some((j) => !j.error);
  useEffect(() => {
    let timer;
    const tick = async () => {
      if (!document.hidden) await load();
      timer = setTimeout(tick, busy ? BUSY_POLL_MS : POLL_MS);
    };
    tick();
    const onVisible = () => !document.hidden && load();
    document.addEventListener("visibilitychange", onVisible);
    return () => {
      clearTimeout(timer);
      document.removeEventListener("visibilitychange", onVisible);
    };
  }, [load, busy]);

  // A list from before the job began lists neither it nor its folder, so the
  // job is only given up on once it has been seen. One that stops at a failure
  // stays here, where the failure shows.
  useEffect(() => {
    if (!awaited || !data) return;
    const listed = data.jobs.find((j) => j.id === awaited.id);
    const done = !listed && data.folders.find((f) => f.path === awaited.path);
    if (done) {
      setAwaited(null);
      location.href = `/${done.slug}/`;
    } else if (listed?.error || (!listed && awaited.seen)) setAwaited(null);
    else if (listed && !awaited.seen) setAwaited({ ...awaited, seen: true });
  }, [data, awaited]);

  // A page that could not open sends the reader back here, saying why.
  const [from, setFrom] = useState(() => {
    const p = new URLSearchParams(location.search);
    return p.get("from") && { slug: p.get("from"), why: p.get("why") || "" };
  });
  const dismissFrom = () => {
    setFrom(null);
    history.replaceState(null, "", "/");
  };

  const act = (promise) => promise.then(load, (e) => (setError(e.message), load()));
  const sessions = (n) => (n === 1 ? "the Claude Code session" : `the ${n} Claude Code sessions`);
  const on = {
    close: (f) => {
      const n = f.open.sessions;
      if (!n || confirm(`Close ${f.name}? It stops ${sessions(n)} dv runs there.`)) act(api.hubClose(f.slug));
    },
    remove: (f) => {
      const n = f.open?.sessions;
      const also = n ? ` It stops ${sessions(n)} dv runs there.` : "";
      if (confirm(`Take ${f.name} off the hub? The folder stays where it is.${also}`)) act(api.hubRemove(f.slug));
    },
    rename: (f, name) => act(api.hubUpdate(f.slug, { name })),
    newWorktree: (f) => setDialog({ worktree: f }),
    hooks: (f) => setDialog({ hooks: f }),
    setup: (f) => act(api.hubSetup(f.slug)),
    deleteWorktree: (f, main) => {
      const first = main?.teardown ? "The teardown hook runs first; then git" : "Git";
      if (confirm(`Delete the worktree at ${f.place}? ${first} removes it, unless it has uncommitted changes. The branch stays.`)) {
        act(api.hubDeleteWorktree(f.slug));
      }
    },
    retry: (j) => {
      if (j.retry === "setup") return act(api.hubSetup(j.slug));
      if (j.retry === "remove") return act(api.hubDeleteWorktree(j.slug, { skipTeardown: true, force: j.force }));
      if (confirm(`Delete ${j.place} anyway? Its uncommitted changes are lost for good. The branch stays.`)) act(api.hubDeleteWorktree(j.slug, { force: true }));
    },
    dismiss: (j) => act(api.hubDismissJob(j.id)),
    error: setError,
  };

  // Worktrees go under the repository they were made of, when it is listed.
  const folders = useMemo(() => (data?.folders || []).map((f) => ({ ...f, ...details[f.path] })), [data, details]);
  const jobs = data?.jobs || [];
  const { mains, worktreesOf } = useMemo(() => {
    const paths = new Set(folders.map((f) => f.path));
    const worktreesOf = new Map();
    const mains = [];
    for (const f of folders) {
      if (f.worktreeOf && paths.has(f.worktreeOf)) worktreesOf.set(f.worktreeOf, [...(worktreesOf.get(f.worktreeOf) || []), f]);
      else mains.push(f);
    }
    return { mains, worktreesOf };
  }, [folders]);
  const q = filter.trim().toLowerCase();
  const matches = (f) => !q || [f.name, f.place, f.remote?.label, f.status?.branch].join(" ").toLowerCase().includes(q);
  // Open ones first, here or in another dv, and otherwise in the hub's order. A
  // card counts as open when any of its worktrees is.
  const live = (f) => (f.open || f.elsewhere ? 1 : 0);
  const shown = mains
    .map((main) => {
      const worktrees = [...(worktreesOf.get(main.path) || [])].sort((a, b) => live(b) - live(a));
      return { main, worktrees: matches(main) ? worktrees : worktrees.filter(matches) };
    })
    .filter(({ main, worktrees }) => matches(main) || worktrees.length)
    .sort((a, b) => Math.max(live(b.main), ...b.worktrees.map(live)) - Math.max(live(a.main), ...a.worktrees.map(live)));
  const loose = jobs.filter((j) => j.kind === "clone" || !mains.some((m) => m.slug === j.of));
  const empty = data && !folders.length && !jobs.length;

  return (
    <>
    {/* Out of the page's grid, whose rows are the header's and the rest's. */}
    <Notices />
    <div className="hub">
      <header className="topbar">
        <div className="topbar-left">
          <span className="logo">dv</span>
        </div>
        <div className="topbar-title" />
        <div className="topbar-right">
          <button className="icon" onClick={() => setDialog("settings")} title="Settings, for every folder (,)">
            <IconSettings size={14} />
          </button>
        </div>
      </header>
      <main className="hub-main">
        <div className="hub-col">
          {from && (
            <div className="filter-note hub-note">
              <span>{from.why || `Nothing is served at /${from.slug}/.`}</span>
              <button className="link" onClick={dismissFrom}>
                Dismiss
              </button>
            </div>
          )}
          {offline && <div className="filter-note hub-note error">The hub is not answering.</div>}
          {error && (
            <div className="filter-note hub-note error">
              <span>{error}</span>
              <button className="link" onClick={() => setError("")}>
                Dismiss
              </button>
            </div>
          )}

          {empty ? (
            <div className="hub-start">
              <h1 className="agent-start-title">Open a folder</h1>
              <div className="hub-start-actions">
                <button className="primary" onClick={() => setDialog("folder")}>
                  Add a folder
                </button>
                <button className="primary" onClick={() => setDialog("clone")}>
                  Clone a repository
                </button>
              </div>
            </div>
          ) : (
            data && (
              <>
                <div className="sidebar-filter hub-bar">
                  <input value={filter} placeholder="Filter by name, path, remote or branch" onChange={(e) => setFilter(e.target.value)} />
                  <button className="btn" onClick={() => setDialog("folder")} title="Add a folder on this machine">
                    <IconPlus size={13} />
                    Add
                  </button>
                  <button className="btn" onClick={() => setDialog("clone")} title="Clone a repository into a folder">
                    <IconBranch size={13} />
                    Clone
                  </button>
                </div>
                {loose.map((j) => (
                  <div key={j.id} className="session-row hub-row hub-loose">
                    <div className="session-row-head">
                      <span className={cx("session-dot", !j.error && "busy")} />
                      <span className="name">{j.title}</span>
                    </div>
                    <div className="hub-place">
                      {LRM}
                      {j.place}
                    </div>
                    <JobState job={j} on={on} />
                  </div>
                ))}
                {shown.map(({ main, worktrees }) => (
                  <div key={main.slug} className={cx("session-row", "hub-row", main.missing && "missing")}>
                    <Entry folder={main} job={jobs.find((j) => j.path === main.path)} on={on} />
                    <Worktrees main={main} worktrees={worktrees} jobs={jobs.filter((j) => j.of === main.slug && j.kind !== "clone")} on={on} />
                  </div>
                ))}
                {q && !shown.length && <div className="empty">No folders match.</div>}
              </>
            )
          )}
        </div>
      </main>

      {dialog === "settings" && (
        <SettingsOverlay
          theme={theme}
          onTheme={setTheme}
          view={view}
          onView={setView}
          contextLines={contextLines}
          onContext={setContextLines}
          wrap={phone ? wrapPhone : wrapWide}
          onWrap={phone ? setWrapPhone : setWrapWide}
          phone={phone}
          notices={notices}
          onNotices={pickNotices}
          hooks={null}
          settings={settings}
          onChange={(patch) => setSettings((s) => ({ ...s, ...patch }))}
          onClose={() => setDialog(null)}
          keys={false}
          working={(elsewhere || []).flatMap((f) => f.sessions || []).filter((s) => s.busy && s.running === "dv").length}
        />
      )}
      {dialog === "folder" && (
        <AddFolder
          onClose={() => setDialog(null)}
          onAdded={() => {
            setDialog(null);
            load();
          }}
        />
      )}
      {dialog === "clone" && (
        <CloneRepo
          into={data?.cloneInto || "~"}
          onClose={() => setDialog(null)}
          onStarted={(j) => {
            setDialog(null);
            setAwaited(j);
            load();
          }}
        />
      )}
      {dialog?.worktree && (
        <NewWorktree
          folder={dialog.worktree}
          onClose={() => setDialog(null)}
          onStarted={(j) => {
            setDialog(null);
            setAwaited(j);
            load();
          }}
        />
      )}
      {dialog?.hooks && (
        <WorktreeHooks
          folder={dialog.hooks}
          onClose={() => setDialog(null)}
          onSaved={() => {
            setDialog(null);
            load();
          }}
        />
      )}
    </div>
    </>
  );
}

// Worktrees lists a repository's worktrees on its card, with those still
// being made.
function Worktrees({ main, worktrees, jobs, on }) {
  const making = jobs.filter((j) => !worktrees.some((w) => w.path === j.path));
  if (!worktrees.length && !making.length) return null;
  return (
    <div className="hub-worktrees">
      {worktrees.map((w) => (
        <Entry key={w.slug} folder={w} main={main} job={jobs.find((j) => j.path === w.path)} on={on} />
      ))}
      {making.map((j) => (
        <div key={j.id} className="hub-worktree">
          <div className="session-row-head">
            <span className={cx("session-dot", !j.error && "busy")} />
            <span className="name">{j.place.slice(j.place.lastIndexOf("/") + 1)}</span>
          </div>
          <div className="hub-remote">
            <span className="hub-place">
              {LRM}
              {j.place}
            </span>
            <Branch name={j.title} />
          </div>
          <JobState job={j} on={on} />
        </div>
      ))}
    </div>
  );
}

// Entry is a folder as its card's link, like a session's card: a dot for its
// state, and under its name where it is, where it came from and what dv is
// doing in it. A worktree (main given) is a row under its repository's card,
// named with its branch. One open in another dv opens in a tab of its own, as
// that dv is not this hub's to come back from.
function Entry({ folder: f, main, job, on }) {
  const worktree = !!main;
  const [renaming, setRenaming] = useState(false);
  const [copied, setCopied] = useState(false);
  useEffect(() => {
    if (!copied) return;
    const t = setTimeout(() => setCopied(false), 1500);
    return () => clearTimeout(t);
  }, [copied]);

  const open = f.open;
  let state;
  if (f.missing) state = <span className="del">Not there anymore</span>;
  else if (f.elsewhere) state = <span title="Opened by a dv started on its own">Open in another dv at {f.elsewhere}</span>;
  else if (open?.waiting) state = <span className="session-asking">waiting on you</span>;
  else if (open) {
    const n = open.sessions;
    state = <span>{open.working ? workingLabel(open) : n ? `${n === 1 ? "a session" : `${n} sessions`} running` : "open"}</span>;
  }
  const menu = [
    { label: "Rename", run: () => setRenaming(true) },
    ...(!worktree && f.git && !f.missing
      ? [
          { label: "New worktree…", run: () => on.newWorktree(f) },
          { label: f.setup || f.teardown ? "Worktree hooks…" : "Add worktree hooks…", run: () => on.hooks(f) },
        ]
      : []),
    ...(worktree && main.setup && !f.missing ? [{ label: "Run setup again", run: () => on.setup(f) }] : []),
    { label: "Take off the hub", run: () => on.remove(f) },
    ...(worktree ? [{ label: "Delete worktree…", run: () => on.deleteWorktree(f, main), danger: true }] : []),
  ];

  return (
    <a
      className={cx(worktree ? "hub-worktree" : "hub-card-link", f.missing && "missing")}
      href={f.missing || renaming ? undefined : f.elsewhere || `/${f.slug}/`}
      {...(f.elsewhere && { target: "_blank", rel: "noopener" })}
    >
      <div className="session-row-head">
        <span className={cx("session-dot", open && "live-dv", f.elsewhere && "live-terminal", open?.working && "busy", open?.waiting && "asking")} />
        {renaming ? (
          <Rename
            name={f.name}
            onDone={(name) => {
              setRenaming(false);
              if (name !== null) on.rename(f, name);
            }}
          />
        ) : (
          <span className="name">{f.name}</span>
        )}
        {f.elsewhere && (
          <button
            className="mini hub-action"
            title={`Copy ${f.elsewhere}`}
            onClick={stop(() => copyText(f.elsewhere).then(() => setCopied(true), (e) => on.error(e.message)))}
          >
            {copied ? "Copied" : "Copy address"}
          </button>
        )}
        {open && (
          <button className="mini hub-action" title="Close it here, until it is next opened" onClick={stop(() => on.close(f))}>
            Close
          </button>
        )}
        <CardMenu items={menu} />
      </div>
      {/* A worktree's remote is its repository's, so where it is takes that place. */}
      {worktree ? (
        <div className="hub-remote">
          <span className="hub-place">
            {LRM}
            {f.place}
          </span>
          <GitState status={f.status} />
        </div>
      ) : (
        <div className="hub-place">
          {LRM}
          {f.place}
        </div>
      )}
      {!worktree && (f.remote || f.status) && (
        <div className="hub-remote">
          {f.remote?.web ? (
            <button className="hub-remote-link" title={`Open ${f.remote.web} in a new tab`} onClick={stop(() => window.open(f.remote.web, "_blank", "noopener"))}>
              {f.remote.label}
            </button>
          ) : (
            f.remote && <span title={f.remote.url}>{f.remote.label}</span>
          )}
          <GitState status={f.status} />
        </div>
      )}
      {state && <div className="session-meta">{state}</div>}
      {job && <JobState job={job} on={on} />}
    </a>
  );
}

function Branch({ name, title }) {
  if (!name) return null;
  return (
    <span className="hub-branch" title={title || name}>
      <IconBranch size={11} />
      <span>{name}</span>
    </span>
  );
}

// GitState is where a checkout stands: its branch, how far it has moved from
// what it tracks, and how much is uncommitted in it. Nothing is said of a
// clean one but its branch.
function GitState({ status: s }) {
  if (!s?.branch) return null;
  const n = (k) => s[k] || 0;
  const changed = n("staged") + n("unstaged") + n("untracked");
  const what = [
    [n("staged"), "staged"],
    [n("unstaged"), "changed"],
    [n("untracked"), "untracked"],
  ]
    .filter(([k]) => k)
    .map(([k, w]) => `${k} ${w}`)
    .join(", ");
  return (
    <span className="hub-git">
      <Branch name={s.branch} title={s.upstream ? `${s.branch}, tracking ${s.upstream}` : s.branch} />
      {(n("ahead") > 0 || n("behind") > 0) && (
        <span className="hub-ab" title={`${n("ahead")} to push and ${n("behind")} to pull, against ${s.upstream}`}>
          {[n("ahead") && `↑${n("ahead")}`, n("behind") && `↓${n("behind")}`].filter(Boolean).join(" ")}
        </span>
      )}
      {n("conflicts") > 0 ? (
        <span className="hub-changes del" title={`${n("conflicts")} in conflict${what ? `; ${what}` : ""}`}>
          {n("conflicts")} in conflict
        </span>
      ) : (
        changed > 0 && (
          <span className="hub-changes" title={what}>
            {changed} uncommitted
          </span>
        )
      )}
    </span>
  );
}

// stop keeps a button inside a card's link from following it.
const stop = (f) => (e) => {
  e.preventDefault();
  e.stopPropagation();
  f();
};

// Rename is a card's name being edited: Enter or leaving it keeps the name,
// Esc does not. An empty name goes back to the folder's own.
function Rename({ name, onDone }) {
  const [draft, setDraft] = useState(name);
  const done = (keep) => onDone(keep && draft.trim() !== name ? draft.trim() : null);
  return (
    <input
      className="session-rename"
      autoFocus
      value={draft}
      onFocus={(e) => e.target.select()}
      onChange={(e) => setDraft(e.target.value)}
      onBlur={() => done(true)}
      onKeyDown={(e) => {
        e.stopPropagation();
        if (e.key === "Enter") done(true);
        else if (e.key === "Escape") done(false);
      }}
    />
  );
}

const RETRY = { setup: "Run setup again", remove: "Delete without teardown", force: "Delete anyway" };

// JobState is what a clone, or work on a worktree, is doing, or why it failed
// and what can be done about that.
function JobState({ job: j, on }) {
  const failed = !!j.error;
  return (
    <div className="hub-job">
      <div className={cx("hub-job-text", failed && "del")}>{failed ? j.error : `${j.step}${j.progress ? `: ${j.progress}` : "…"}`}</div>
      <div className="hub-job-actions">
        {failed && j.retry && (
          <button className={cx("mini", j.retry === "force" && "del")} onClick={stop(() => on.retry(j))}>
            {RETRY[j.retry]}
          </button>
        )}
        <button className="mini" onClick={stop(() => on.dismiss(j))}>
          {failed ? "Dismiss" : "Stop"}
        </button>
      </div>
    </div>
  );
}

// CardMenu is a card's other actions, behind its dots. The list is in the
// page's top layer, out of the card's link.
function CardMenu({ items }) {
  const [open, setOpen] = useState(false);
  const menu = useRef(null);
  const ref = useDismiss(open, () => setOpen(false), menu);
  const at = useFixedMenu(open, ref, menu);
  useEffect(() => {
    if (!open) return;
    const close = () => setOpen(false);
    const onScroll = (e) => menu.current?.contains(e.target) || close();
    window.addEventListener("scroll", onScroll, true);
    window.addEventListener("resize", close);
    return () => {
      window.removeEventListener("scroll", onScroll, true);
      window.removeEventListener("resize", close);
    };
  }, [open]);
  return (
    <span className="hub-menu" ref={ref}>
      <button
        className="hub-menu-button hub-action"
        title="More"
        aria-expanded={open}
        onClick={stop(() => setOpen((o) => !o))}
      >
        <IconDots size={12} />
      </button>
      {open &&
        createPortal(
          <div className="model-list attach-menu hub-menu-list" ref={menu} style={at}>
            {items.map((it) => (
              <button
                key={it.label}
                className={cx(it.danger && "del")}
                onClick={() => {
                  setOpen(false);
                  it.run();
                }}
              >
                <span className="model-name">{it.label}</span>
              </button>
            ))}
          </div>,
          document.body,
        )}
    </span>
  );
}

function AddFolder({ onClose, onAdded }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const add = async (path) => {
    setBusy(true);
    try {
      await api.hubAdd(path);
      onAdded();
    } catch (e) {
      setError(e.message);
      setBusy(false);
    }
  };
  return (
    <Modal onClose={onClose} centred className="hub-picker">
      <DialogHead title="Add a folder" onClose={onClose} />
      <FolderPicker
        start="~"
        error={error}
        action={(chosen) => (
          <button className="primary" disabled={busy} onClick={() => add(chosen)}>
            Add
          </button>
        )}
        onSubmit={add}
        note={(chosen) => <>Add {chosen || "~"}; one inside a repository adds the repository</>}
      />
    </Modal>
  );
}

function DialogHead({ title, onClose }) {
  return (
    <div className="viewer-head">
      <span className="path">{title}</span>
      <span className="spacer" />
      <button className="ghost" onClick={onClose}>
        <IconX size={13} />
      </button>
    </div>
  );
}

function CloneRepo({ into, onClose, onStarted }) {
  const [source, setSource] = useState("");
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const named = name.trim() || repoName(source.trim());
  const clone = async (dir) => {
    if (!source.trim()) return setError("Say what to clone: a URL, or owner/repo on GitHub.");
    setBusy(true);
    try {
      onStarted(await api.hubClone(source.trim(), dir, name.trim()));
    } catch (e) {
      setError(e.message);
      setBusy(false);
    }
  };
  // Enter goes on down the dialog, to the folder box, where it clones.
  const next = (e) => {
    e.stopPropagation();
    if (e.key !== "Enter") return;
    e.preventDefault();
    const fields = [...e.currentTarget.closest(".modal").querySelectorAll("input")];
    fields[fields.indexOf(e.currentTarget) + 1]?.focus();
  };
  return (
    <Modal onClose={onClose} centred className="hub-picker">
      <DialogHead title="Clone a repository" onClose={onClose} />
      <div className="palette-input hub-source">
        <IconBranch size={15} />
        <input
          autoFocus
          value={source}
          placeholder="URL, or owner/repo on GitHub"
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          onChange={(e) => {
            setSource(e.target.value);
            setError("");
          }}
          onKeyDown={next}
        />
      </div>
      <div className="palette-input hub-as">
        <span className="hub-as-label">as</span>
        <input
          value={name}
          placeholder={repoName(source.trim()) || "the repository's own name"}
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          onChange={(e) => {
            setName(e.target.value);
            setError("");
          }}
          onKeyDown={next}
        />
      </div>
      <div className="menu-label hub-into">Into</div>
      <FolderPicker
        start={into}
        error={error}
        action={(chosen) => (
          <button className="primary" disabled={busy || !source.trim()} onClick={() => clone(chosen)}>
            Clone
          </button>
        )}
        onSubmit={clone}
        note={(chosen) => (
          <>
            Clone into {(chosen || "~").replace(/\/$/, "")}/{named || "…"}. Claude Code sessions there use the repository's own settings and hooks.
          </>
        )}
      />
    </Modal>
  );
}

// repoName is the folder git clone makes, as the hub works it out.
function repoName(source) {
  const s = source.replace(/\/+$/, "");
  return s.slice(Math.max(s.lastIndexOf("/"), s.lastIndexOf(":")) + 1).replace(/\.git$/, "");
}

// NewWorktree makes a worktree of a repository, beside it: of a branch there
// already, locally or on a remote, or of a new one started from another.
function NewWorktree({ folder: f, onClose, onStarted }) {
  const [info, setInfo] = useState(null);
  const [branch, setBranch] = useState("");
  const [base, setBase] = useState("");
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    api.hubBranches(f.slug).then(setInfo, (e) => setError(e.message));
  }, [f.slug]);

  const b = branch.trim();
  const local = !!info?.branches.includes(b);
  const exists = local || !!info?.remote.includes(b);
  // A branch only a remote has is checked out as a local one tracking it; a new
  // branch starts from a local one.
  const bases = useMemo(() => (info?.branches ?? []).map((value) => ({ value })), [info]);
  const branches = useMemo(
    () => [...bases, ...(info?.remote ?? []).filter((r) => !info.branches.includes(r)).map((value) => ({ value, tag: "remote" }))],
    [info, bases],
  );
  const folder = name.trim() || (b && info ? `${info.repo}-${b.replaceAll("/", "-")}` : "");
  const create = async () => {
    if (!b || busy) return;
    setBusy(true);
    try {
      onStarted(await api.hubWorktree(f.slug, { branch: b, base: exists ? "" : base.trim(), name: name.trim() }));
    } catch (e) {
      setError(e.message);
      setBusy(false);
    }
  };
  const onKey = (e) => {
    e.stopPropagation();
    if (e.key === "Enter") {
      e.preventDefault();
      create();
    }
  };
  const what = !b
    ? "Name a branch: one to check out, or a new one"
    : exists
      ? `Checks out ${b}${local ? "" : ", tracking the remote's"}`
      : `Makes ${b} from ${base.trim() || info?.base || "HEAD"}`;
  return (
    <Modal onClose={onClose} centred className="hub-form">
      <DialogHead title={`New worktree of ${f.name}`} onClose={onClose} />
      <div className="settings-body hub-form-body">
        <label className="hub-field">
          <span>Branch</span>
          <Combo autoFocus value={branch} options={branches} placeholder="fix/login" onChange={setBranch} onEnter={create} />
        </label>
        <label className="hub-field">
          <span>Start from</span>
          <Combo
            value={exists ? "" : base}
            options={bases}
            disabled={exists}
            placeholder={exists ? "the branch as it is" : info?.base}
            onChange={setBase}
            onEnter={create}
          />
        </label>
        <label className="hub-field">
          <span>Folder</span>
          <span className="hub-field-path">
            <span className="dim">{info ? `${info.into}/` : ""}</span>
            <input value={name} placeholder={folder || "…"} autoCapitalize="off" autoCorrect="off" spellCheck={false} onChange={(e) => setName(e.target.value)} onKeyDown={onKey} />
          </span>
        </label>
      </div>
      {error && <div className="hub-picker-error">{error}</div>}
      <div className="hub-picker-foot">
        <span className="hub-picker-note">
          {what}
          {b && f.setup ? ", then runs the setup hook" : ""}.
        </span>
        <button className="primary" disabled={busy || !b} onClick={create}>
          Create
        </button>
      </div>
    </Modal>
  );
}

// Combo is a text box offering choices as it is typed in, drawn as dv's own
// menus are rather than as the browser's list: options [{ value, tag }], the
// ones holding what is typed, those starting with it first. ↑↓ pick one and
// Enter takes it; with none picked Enter is onEnter. The list is in the page's
// top layer, so the dialog around the box does not clip it.
function Combo({ value, options, onChange, onEnter, disabled, ...rest }) {
  const [open, setOpen] = useState(false);
  const [sel, setSel] = useState(-1);
  const [at, setAt] = useState(null);
  const boxRef = useRef(null);
  const listRef = useRef(null);
  const q = value.trim().toLowerCase();
  const shown = useMemo(() => {
    const hits = options.filter((o) => o.value.toLowerCase().includes(q));
    return (q ? hits.sort((a, b) => b.value.toLowerCase().startsWith(q) - a.value.toLowerCase().startsWith(q)) : hits).slice(0, 100);
  }, [options, q]);
  // Once what is typed is the one choice there is, there is nothing to offer.
  const showing = open && !disabled && shown.length > 0 && !(shown.length === 1 && shown[0].value === value.trim());
  useEffect(() => setSel(-1), [q]);
  // Placed again whenever anything draws, as the dialog it sits over can move:
  // it is centred, and grows with an error under it.
  const place = useCallback(() => {
    const r = boxRef.current.getBoundingClientRect();
    setAt((a) => (a?.top === r.bottom + 4 && a.left === r.left && a.width === r.width ? a : { top: r.bottom + 4, left: r.left, width: r.width }));
  }, []);
  useLayoutEffect(() => {
    if (showing) place();
  });
  useEffect(() => {
    if (!showing) return;
    window.addEventListener("resize", place);
    return () => window.removeEventListener("resize", place);
  }, [showing, place]);
  useEffect(() => {
    listRef.current?.querySelector(".on")?.scrollIntoView({ block: "nearest" });
  }, [sel]);
  const pick = (o) => {
    onChange(o.value);
    setOpen(false);
  };
  return (
    <>
      <input
        {...rest}
        ref={boxRef}
        value={value}
        disabled={disabled}
        role="combobox"
        aria-expanded={showing}
        autoCapitalize="off"
        autoCorrect="off"
        spellCheck={false}
        onFocus={() => setOpen(true)}
        onBlur={() => setOpen(false)}
        onChange={(e) => {
          onChange(e.target.value);
          setOpen(true);
        }}
        onKeyDown={(e) => {
          e.stopPropagation();
          if (e.key === "ArrowDown") {
            e.preventDefault();
            setOpen(true);
            setSel((s) => Math.min(shown.length - 1, s + 1));
          } else if (e.key === "ArrowUp") {
            e.preventDefault();
            setSel((s) => Math.max(-1, s - 1));
          } else if (e.key === "Escape" && showing) {
            e.preventDefault();
            setOpen(false);
          } else if (e.key === "Enter") {
            e.preventDefault();
            if (showing && shown[sel]) pick(shown[sel]);
            else onEnter?.();
          }
        }}
      />
      {showing &&
        at &&
        createPortal(
          // Pressing in the list must not take the focus, and the list with it, from the box.
          <div className="model-list hub-combo-list" ref={listRef} style={at} onMouseDown={(e) => e.preventDefault()}>
            {shown.map((o, i) => (
              <button key={o.value} className={cx(i === sel && "on")} onMouseMove={() => setSel(i)} onClick={() => pick(o)}>
                <span className="model-name">
                  {o.value}
                  {o.tag && <span className="model-tag">{o.tag}</span>}
                </span>
              </button>
            ))}
          </div>,
          document.body,
        )}
    </>
  );
}

// WorktreeHooks edits the scripts run in a repository's worktrees as the hub
// makes and deletes them.
function WorktreeHooks({ folder: f, onClose, onSaved }) {
  const [setup, setSetup] = useState(f.setup || "");
  const [teardown, setTeardown] = useState(f.teardown || "");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const save = async () => {
    setBusy(true);
    try {
      await api.hubUpdate(f.slug, { setup, teardown });
      onSaved();
    } catch (e) {
      setError(e.message);
      setBusy(false);
    }
  };
  const onKey = (e) => {
    e.stopPropagation();
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      save();
    }
  };
  return (
    <Modal onClose={onClose} centred className="hub-form">
      <DialogHead title={`Worktree hooks of ${f.name}`} onClose={onClose} />
      <div className="settings-body hub-form-body">
        <label className="hub-field">
          <span>Setup</span>
          <span className="settings-note">Runs in a new worktree once git has made it.</span>
          <textarea rows={5} value={setup} placeholder={'npm ci\ncp "$DV_REPO/.env" .env'} spellCheck={false} onChange={(e) => setSetup(e.target.value)} onKeyDown={onKey} />
        </label>
        <label className="hub-field">
          <span>Teardown</span>
          <span className="settings-note">Runs before a worktree is deleted; if it fails, the worktree stays.</span>
          <textarea rows={3} value={teardown} placeholder="docker compose down" spellCheck={false} onChange={(e) => setTeardown(e.target.value)} onKeyDown={onKey} />
        </label>
        <div className="settings-note">
          Each runs in the worktree with sh, which stops at the first command that fails. <code>$DV_REPO</code> is this repository,{" "}
          <code>$DV_WORKTREE</code> the worktree and <code>$DV_BRANCH</code> its branch.
        </div>
      </div>
      {error && <div className="hub-picker-error">{error}</div>}
      <div className="hub-picker-foot">
        <span className="hub-picker-note">{isMac ? "⌘" : "Ctrl"}+Enter saves</span>
        <button className="primary" disabled={busy} onClick={save}>
          Save
        </button>
      </div>
    </Modal>
  );
}

// FolderPicker picks a folder on the hub's machine, which a phone has no file
// dialog onto. The box is a path, as typed at a shell: the folders listed are
// the ones in it up to its last slash, narrowed by what follows, and picking one
// goes into it. Enter goes into the one picked - typing picks the best match,
// the arrows any - and with none picked (or with Ctrl/Cmd) takes the folder the
// box names. A name typed that is not there offers to make it.
function FolderPicker({ start, error, action, onSubmit, note }) {
  const [text, setText] = useState(() => withSlash(start));
  const cut = text.lastIndexOf("/") + 1;
  const dir = text.slice(0, cut);
  const typed = text.slice(cut).trim();
  const tail = typed.toLowerCase();
  const [listing, setListing] = useState(null); // { dir, res } or { dir, error }
  const [sel, setSel] = useState(0);
  const [making, setMaking] = useState(false);
  const [makeError, setMakeError] = useState("");
  const listRef = useRef(null);
  const inputRef = useRef(null);

  useEffect(() => {
    let live = true;
    const t = setTimeout(
      () =>
        api.hubDirs(dir).then(
          (res) => live && setListing({ dir, res }),
          (e) => live && setListing({ dir, error: e.message }),
        ),
      listing ? 80 : 0,
    );
    return () => {
      live = false;
      clearTimeout(t);
    };
  }, [dir]);

  const res = listing?.res;
  const rows = useMemo(() => {
    if (!res) return [];
    const dirs = tail ? res.dirs.filter((d) => d.name.toLowerCase().includes(tail)) : res.dirs;
    // Names that start with what was typed come first, as a shell completes.
    const sorted = tail ? [...dirs].sort((a, b) => b.name.toLowerCase().startsWith(tail) - a.name.toLowerCase().startsWith(tail)) : dirs;
    // Not for what is on its way to a path: ~, . and .. before their slash.
    const makeable = typed && !/^(~|\.\.?)$/.test(typed) && !typed.startsWith("-") && !res.dirs.some((d) => d.name === typed);
    const make = makeable ? [{ name: typed, make: true }] : [];
    return [...(!tail && res.parent ? [{ name: "..", up: true }] : []), ...sorted, ...make];
  }, [res, tail, typed]);
  useEffect(() => setSel(tail ? 0 : -1), [dir, tail]);
  useEffect(() => {
    listRef.current?.querySelector(".on")?.scrollIntoView({ block: "nearest" });
  }, [sel]);

  const chosen = tail ? text : dir.replace(/(.)\/$/, "$1");
  const enter = async (row) => {
    if (row.make) {
      if (making) return;
      setMaking(true);
      try {
        await api.hubMakeDir(dir, row.name);
      } catch (e) {
        return setMakeError(e.message);
      } finally {
        setMaking(false);
      }
    }
    setText(row.up ? withSlash(res.parentPlace) : dir + row.name + "/");
    inputRef.current?.focus();
  };
  const onKey = (e) => {
    e.stopPropagation();
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setSel((s) => Math.min(rows.length - 1, s + 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setSel((s) => Math.max(0, s - 1));
    } else if (e.key === "Tab" && rows[sel] && tail) {
      e.preventDefault();
      enter(rows[sel]);
    } else if (e.key === "Enter") {
      e.preventDefault();
      if (rows[sel] && !(e.metaKey || e.ctrlKey)) enter(rows[sel]);
      else onSubmit(chosen);
    }
  };

  const failed = listing?.dir === dir && listing.error;
  return (
    <>
      <div className="palette-input">
        <input
          ref={inputRef}
          className="hub-dir-input"
          value={text}
          autoCapitalize="off"
          autoCorrect="off"
          spellCheck={false}
          onChange={(e) => {
            setText(e.target.value);
            setMakeError("");
          }}
          onKeyDown={onKey}
        />
      </div>
      <div className="palette-list hub-dirs" ref={listRef}>
        {rows.map((row, i) => (
          <button
            key={row.make ? "\0make" : row.name}
            className={cx("palette-row", i === sel && "on")}
            disabled={row.make && making}
            onMouseMove={() => setSel(i)}
            onClick={() => enter(row)}
          >
            {row.make && <IconPlus size={12} className="dim" />}
            <span className="hub-dir-name">{row.make ? <>New folder {row.name}</> : row.name}</span>
            {row.git && (
              <span className="dim hub-dir-git" title="A git repository">
                <IconBranch size={12} />
              </span>
            )}
            <span className="spacer" />
            {!row.up && !row.make && <IconChevron size={12} className="dim" />}
          </button>
        ))}
        {failed && <div className="empty error">{listing.error}</div>}
        {res && !failed && !rows.length && <div className="empty">{tail ? "No folders match." : "No folders in here. Type a name to make one."}</div>}
        {res?.more > 0 && !tail && <div className="empty">{res.more.toLocaleString()} more: type to narrow them down</div>}
      </div>
      {(makeError || error) && <div className="hub-picker-error">{makeError || error}</div>}
      <div className="hub-picker-foot">
        <span className="hub-picker-note">{note(chosen)}</span>
        {action(chosen)}
      </div>
      <div className="palette-foot dim hub-keys">
        Type or ↑↓ to pick a folder and Enter to go into it, or type a new name to make one; Enter with none picked, or {isMac ? "⌘" : "Ctrl"}+Enter, takes this one
      </div>
    </>
  );
}

const withSlash = (p) => (p.endsWith("/") ? p : p + "/");
