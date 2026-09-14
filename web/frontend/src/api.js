// Thin wrapper over the Go API. Every call throws on a non-2xx response with
// the server's error message, which the UI surfaces directly.
async function req(path, opts = {}) {
  const res = await fetch(path, {
    ...opts,
    headers: opts.body ? { "content-type": "application/json" } : undefined,
  });
  const text = await res.text();
  const data = text ? JSON.parse(text) : null;
  if (!res.ok) throw new Error(data?.error || `${res.status} ${res.statusText}`);
  return data;
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

  // Without a side this is the working tree; with "old" or "new", that side of
  // the scope, which is where the diff's line numbers come from.
  file: (path, scope, side) => {
    const p = side ? scopeQuery(scope) : new URLSearchParams();
    p.set("path", path);
    if (side) p.set("side", side);
    return req(`/api/file?${p}`);
  },

  files: (q, limit = 60) => req(`/api/files?${new URLSearchParams({ q, limit: String(limit) })}`),

  // tree lists every file on the scope's new side, for Code mode's explorer.
  tree: (scope) => req(`/api/tree?${scopeQuery(scope)}`),

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

  askModels: () => req("/api/ask/models"),

  // ask streams server-sent events from the `claude` CLI. It is a POST, so the
  // stream is read off the fetch body rather than through EventSource.
  ask: async (body, onEvent, signal) => {
    const res = await fetch("/api/ask", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify(body),
      signal,
    });
    if (!res.ok) {
      const text = await res.text();
      let msg = res.statusText;
      try {
        msg = JSON.parse(text).error || msg;
      } catch {}
      throw new Error(msg);
    }
    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    let buf = "";
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buf += decoder.decode(value, { stream: true });
      let i;
      while ((i = buf.indexOf("\n\n")) >= 0) {
        const chunk = buf.slice(0, i);
        buf = buf.slice(i + 2);
        for (const line of chunk.split("\n")) {
          if (line.startsWith("data: ")) onEvent(JSON.parse(line.slice(6)));
        }
      }
    }
  },

  // claudeRequests follows the Claude Code prompts waiting on the reader, as a
  // stream rather than a poll so they still arrive while the tab is hidden.
  claudeRequests: (onList) => {
    const es = new EventSource("/api/claude/requests");
    es.onmessage = (e) => onList(JSON.parse(e.data).requests || []);
    // It reconnects on its own and is sent the list afresh; until then nothing
    // shown could be answered.
    es.onerror = () => onList([]);
    return () => es.close();
  },

  // answer: { allow, note, suggestion } where suggestion indexes the request's
  // suggestions to apply along with an allow.
  claudeAnswer: (id, answer) =>
    req(`/api/claude/requests/${id}`, { method: "POST", body: JSON.stringify(answer) }),

  search: (opts) => {
    const p = new URLSearchParams({ q: opts.query });
    if (opts.regex) p.set("regex", "1");
    if (opts.caseSens) p.set("case", "1");
    if (opts.wholeWord) p.set("word", "1");
    if (opts.glob) p.set("glob", opts.glob);
    return req(`/api/search?${p}`);
  },
};
