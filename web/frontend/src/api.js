import { boot } from "./boot.js";

const base = boot.base;

// Thin wrapper over the Go API. Every call throws on a non-2xx response with
// the server's error message, which the UI surfaces directly. The hub's own
// calls go to its root, as a folder's page makes them too.
async function req(path, opts = {}, at = base) {
  const res = await fetch(at + path, {
    ...opts,
    headers: opts.body ? { "content-type": "application/json" } : undefined,
  });
  const text = await res.text();
  const data = text ? JSON.parse(text) : null;
  if (!res.ok) throw new Error(data?.error || `${res.status} ${res.statusText}`);
  return data;
}

// follow reads a stream of JSON events until the returned func is called. It
// reconnects on its own after a drop, and the first event after is a reset.
function follow(path, onEvent, onDrop, at = base) {
  const es = new EventSource(at + path);
  es.onmessage = (e) => onEvent(JSON.parse(e.data));
  es.onerror = () => onDrop?.();
  return () => es.close();
}

const scopeQuery = (scope) => {
  const p = new URLSearchParams({ scope: scope.kind });
  if (scope.rev) p.set("rev", scope.rev);
  return p;
};

export const api = {
  meta: () => req("/api/meta"),

  diffList: (scope) => req(`/api/diff?${scopeQuery(scope)}`),

  // version fingerprints the repository; it moves whenever any diff could have.
  version: () => req("/api/version"),

  diffFile: (scope, path) => {
    const p = scopeQuery(scope);
    p.set("path", path);
    return req(`/api/diff/file?${p}`);
  },

  // diffFiles is diffFile for many paths: { path: { fd } or { error } }.
  diffFiles: (scope, paths) =>
    req(`/api/diff/files?${scopeQuery(scope)}`, { method: "POST", body: JSON.stringify({ paths }) }),

  // Without a side this is the working tree; with "old" or "new", that side of
  // the scope, which is where the diff's line numbers come from.
  file: (path, scope, side) => {
    const p = side ? scopeQuery(scope) : new URLSearchParams();
    p.set("path", path);
    if (side) p.set("side", side);
    return req(`/api/file?${p}`);
  },

  // mediaURL serves an image, video or sound off the scope's new side, or its
  // old one. stamp is the version to show, so a changed file is fetched again.
  mediaURL: (path, scope, side, stamp) => {
    const p = scope ? scopeQuery(scope) : new URLSearchParams();
    p.set("path", path);
    if (side === "old") p.set("side", "old");
    if (stamp) p.set("v", stamp);
    return `${base}/api/media?${p}`;
  },

  files: (q, limit = 60) => req(`/api/files?${new URLSearchParams({ q, limit: String(limit) })}`),

  // tree lists every file on the scope's new side, for Code mode's explorer.
  tree: (scope, open = []) => {
    const p = scopeQuery(scope);
    for (const dir of open) p.append("open", dir);
    return req(`/api/tree?${p}`);
  },

  threads: () => req("/api/threads"),

  // key is the comparison's label: a file is viewed in one diff, not all of them.
  viewed: (key) => req(`/api/viewed?${new URLSearchParams({ key })}`),

  markViewed: (key, paths, viewed) =>
    req("/api/viewed", { method: "POST", body: JSON.stringify({ key, paths, viewed }) }),

  // reset deletes every comment and viewed mark, as `dv reset` does.
  reset: () => req("/api/reset", { method: "POST" }),

  createThread: (body) =>
    req("/api/threads", { method: "POST", body: JSON.stringify(body) }),

  reply: (id, body) =>
    req(`/api/threads/${id}/replies`, { method: "POST", body: JSON.stringify({ body }) }),

  setResolved: (id, resolved) =>
    req(`/api/threads/${id}`, { method: "PATCH", body: JSON.stringify({ resolved }) }),

  deleteThread: (id) => req(`/api/threads/${id}`, { method: "DELETE" }),

  editComment: (id, cid, body) =>
    req(`/api/threads/${id}/comments/${cid}`, { method: "PATCH", body: JSON.stringify({ body }) }),

  deleteComment: (id, cid) =>
    req(`/api/threads/${id}/comments/${cid}`, { method: "DELETE" }),

  // from is the file the reader is in. The server does not filter by it, it
  // ranks by it, so the nearest definition comes back first.
  symbols: (q, from = "", limit = 60) =>
    req(`/api/symbols?${new URLSearchParams({ q, from, limit: String(limit) })}`),

  resolveSymbol: (name, from = "") =>
    req(`/api/symbols?${new URLSearchParams({ name, from })}`),

  refreshSymbols: () => req("/api/symbols/refresh", { method: "POST" }),

  // claudeEvents follows the Claude Code prompts waiting on the reader, and what
  // the open sessions are doing: { requests } or { sessions }, each whole, as a
  // stream rather than a poll so they still arrive while the tab is hidden.
  claudeEvents: (on) => {
    const es = new EventSource(base + "/api/claude/requests");
    es.onmessage = (e) => on(JSON.parse(e.data));
    // It reconnects on its own and is sent both afresh; until then nothing
    // shown could be answered.
    es.onerror = () => on({ requests: [] });
    return () => es.close();
  },

  // answer: { allow, note, suggestion } where suggestion indexes the request's
  // suggestions to apply along with an allow.
  claudeAnswer: (id, answer) =>
    req(`/api/claude/requests/${id}`, { method: "POST", body: JSON.stringify(answer) }),

  // claudeHooks: { on, path }, whether terminal sessions' prompts can reach dv
  // through hooks in the Claude Code settings file at path.
  claudeHooks: () => req("/api/claude/hooks"),
  setClaudeHooks: (on) => req("/api/claude/hooks", { method: "POST", body: JSON.stringify({ on }) }),

  agentSessions: () => req("/api/agent/sessions"),
  // agentCommands is the slash commands an agent's sessions take: { commands: [{ name, description, argumentHint }] }.
  agentCommands: (agent = "") => req(`/api/agent/commands${agent ? `?agent=${agent}` : ""}`),

  // agentCreate makes a session to send a first message to; nothing runs yet.
  // agent is "codex", or "" for Claude Code.
  agentCreate: (agent = "") => req("/api/agent/sessions", { method: "POST", body: JSON.stringify({ agent }) }),

  // Open sessions are the ones whose permission prompts come up in the page.
  agentOpen: (id, open) => req(`/api/agent/sessions/${id}/open`, { method: "POST", body: JSON.stringify({ open }) }),
  // A temporary session leaves the list once it is closed.
  agentTemporary: (id, temporary) => req(`/api/agent/sessions/${id}/temporary`, { method: "POST", body: JSON.stringify({ temporary }) }),

  // agentEvents follows a session: each update carries the items new or changed
  // since the last (all of them when `reset`) and what the session is doing.
  // from is where the conversation is shown from: "" for its latest compaction,
  // the key of an earlier one, or "all".
  agentEvents: (id, onUpdate, onDrop, from = "") =>
    follow(`/api/agent/sessions/${id}/events${from ? `?from=${encodeURIComponent(from)}` : ""}`, onUpdate, onDrop),
  // agentSubagentEvents is the same for the conversation of an agent that one
  // of a session's calls started.
  agentSubagentEvents: (id, call, onUpdate, onDrop) => follow(`/api/agent/sessions/${id}/agents/${call}/events`, onUpdate, onDrop),
  // agentTaskOutput follows what a call left running in the background writes:
  // { text, reset, cut } as it grows (cut: the start is left out), { gone } once
  // Claude Code has cleared the file away.
  agentTaskOutput: (id, tool, onChunk) => follow(`/api/agent/sessions/${id}/tools/${tool}/live`, onChunk),

  // images: [{ mediaType, data }], data in base64. uuid is what the message is
  // known by, in the transcript and in the session's queue.
  agentSend: (id, text, uuid, images) =>
    req(`/api/agent/sessions/${id}/messages`, { method: "POST", body: JSON.stringify({ text, uuid, images }) }),
  // agentShell runs a command typed after !, whose output goes to the agent as the message uuid.
  agentShell: (id, command, uuid) => req(`/api/agent/sessions/${id}/shell`, { method: "POST", body: JSON.stringify({ command, uuid }) }),
  // agentPrompts is every message and command in a session, back to its start: { prompts: [item] }.
  agentPrompts: (id) => req(`/api/agent/sessions/${id}/prompts`),
  agentUnqueue: (id, message) => req(`/api/agent/sessions/${id}/messages/${message}/unqueue`, { method: "POST", body: "{}" }),
  agentPromptImageURL: (id, message, n) => `${base}/api/agent/sessions/${id}/messages/${message}/images/${n}`,

  agentInterrupt: (id) => req(`/api/agent/sessions/${id}/interrupt`, { method: "POST", body: "{}" }),

  // settings: { model }, { mode } and/or { effort }.
  agentSettings: (id, settings) => req(`/api/agent/sessions/${id}/settings`, { method: "POST", body: JSON.stringify(settings) }),
  agentRename: (id, title) => req(`/api/agent/sessions/${id}/title`, { method: "POST", body: JSON.stringify({ title }) }),

  // rewind: { prompt, before, conversation, code }. The id that comes back is
  // a new session's when the rewind went past the first prompt.
  agentRewind: (id, rewind) => req(`/api/agent/sessions/${id}/rewind`, { method: "POST", body: JSON.stringify(rewind) }),

  agentEdit: (id, tool) => req(`/api/agent/sessions/${id}/edits/${encodeURIComponent(tool)}`),
  agentOutput: (id, tool) => req(`/api/agent/sessions/${id}/tools/${encodeURIComponent(tool)}`),
  agentImageURL: (id, tool) => `${base}/api/agent/sessions/${id}/tools/${encodeURIComponent(tool)}/image`,

  search: (opts) => {
    const p = new URLSearchParams({ q: opts.query });
    if (opts.regex) p.set("regex", "1");
    if (opts.caseSens) p.set("case", "1");
    if (opts.wholeWord) p.set("word", "1");
    if (opts.glob) p.set("glob", opts.glob);
    return req(`/api/search?${p}`);
  },

  // prefs is the settings kept on disk: { user, repo }, each by key.
  prefs: () => req("/api/prefs"),
  // setPref stores one, or deletes it given null. keepalive lets it finish as
  // the page goes away.
  setPref: (where, key, value, keepalive) =>
    req("/api/prefs", { method: "PATCH", body: JSON.stringify({ where, key, value }), keepalive }),

  // The hub's own. hubFolders: { folders, jobs, cloneInto, prefs }, prefs the
  // user's settings' version; with details, each git folder has its remote and
  // branch too.
  hubFolders: (details) => req(`/api/hub/folders${details ? "?details=1" : ""}`, {}, ""),
  hubAdd: (path) => req("/api/hub/folders", { method: "POST", body: JSON.stringify({ path }) }),
  // patch: { name, setup, teardown }, any of them.
  hubUpdate: (slug, patch) => req(`/api/hub/folders/${encodeURIComponent(slug)}`, { method: "PATCH", body: JSON.stringify(patch) }),
  hubRemove: (slug) => req(`/api/hub/folders/${encodeURIComponent(slug)}`, { method: "DELETE" }),
  hubClose: (slug) => req(`/api/hub/folders/${encodeURIComponent(slug)}/close`, { method: "POST", body: "{}" }),
  // hubBranches: { branches, base, repo, into }, what a new worktree is made with.
  hubBranches: (slug) => req(`/api/hub/folders/${encodeURIComponent(slug)}/branches`),
  hubWorktree: (slug, wt) => req(`/api/hub/folders/${encodeURIComponent(slug)}/worktrees`, { method: "POST", body: JSON.stringify(wt) }),
  hubSetup: (slug) => req(`/api/hub/folders/${encodeURIComponent(slug)}/setup`, { method: "POST", body: "{}" }),
  // how: { skipTeardown, force }, force deleting it with uncommitted changes.
  hubDeleteWorktree: (slug, how = {}) =>
    req(`/api/hub/folders/${encodeURIComponent(slug)}/delete`, { method: "POST", body: JSON.stringify(how) }),
  // hubDirs: { path, place, parent, parentPlace, git, dirs: [{ name, git }], more }.
  // hubActivity follows every folder open in the hub: { folders: [{ slug,
  // name, sessions, requests }] }, for notices about the folders not on screen.
  hubActivity: (onEvent, onDrop) => follow("/api/hub/activity", onEvent, onDrop, ""),
  hubDirs: (path) => req(`/api/hub/dirs?${new URLSearchParams({ path })}`),
  // hubMakeDir: { path, place }; a folder already there is taken as made.
  hubMakeDir: (into, name) => req("/api/hub/dirs", { method: "POST", body: JSON.stringify({ in: into, name }) }),
  hubClone: (source, into, name) => req("/api/hub/clones", { method: "POST", body: JSON.stringify({ source, into, name }) }),
  hubDismissJob: (id) => req(`/api/hub/jobs/${encodeURIComponent(id)}`, { method: "DELETE" }),
};
