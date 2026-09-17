// Settings and unsent work that dv keeps on disk rather than in the browser, so
// every device open on it has the same: "user" ones everywhere, "repo" ones
// beside the review's comments. A value shows the moment it is set and is
// written a moment later. While a key waits to be written, or is on its way,
// what the server says of it is not taken: the device typing keeps its own
// until it has saved.
import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "./api.js";
import { boot } from "./boot.js";

const SAVE_MS = 400;
const RETRY_MS = 3000;

const values = { user: { ...boot.prefs.user }, repo: { ...boot.prefs.repo } };
const listeners = new Set();
// By where and key: seq counts sets, saved is the last seq the server has.
const writes = new Map();
let known = boot.prefsVersion || "";

const idOf = (where, key) => `${where}\n${key}`;
const dirty = (w) => w.seq > w.saved;
const notify = () => listeners.forEach((f) => f());

export function readPref(where, key, initial) {
  return key in values[where] ? values[where][key] : initial;
}

// setPref sets a value; one equal to initial is deleted rather than stored.
export function setPref(where, key, value, initial) {
  if (JSON.stringify(value) === JSON.stringify(initial)) value = undefined;
  if (JSON.stringify(value) === JSON.stringify(values[where][key])) return;
  if (value === undefined) delete values[where][key];
  else values[where][key] = value;
  const id = idOf(where, key);
  const w = writes.get(id) || { where, key, seq: 0, saved: 0, timer: null, chain: Promise.resolve() };
  writes.set(id, w);
  w.seq++;
  clearTimeout(w.timer);
  w.timer = setTimeout(() => save(w), SAVE_MS);
  notify();
}

// save sends the key's latest value, after any send already on its way, so
// the server never ends on an older one.
function save(w, keepalive = false) {
  w.timer = null;
  const seq = w.seq;
  const send = () => api.setPref(w.where, w.key, values[w.where][w.key] ?? null, keepalive);
  const done = () => (w.saved = Math.max(w.saved, seq));
  if (keepalive) {
    send().then(done, () => {});
    return;
  }
  w.chain = w.chain.then(send).then(done, () => {
    if (w.seq === seq && !w.timer) w.timer = setTimeout(() => save(w), RETRY_MS);
  });
}

function flush(keepalive) {
  for (const w of writes.values()) {
    if (!w.timer) continue;
    clearTimeout(w.timer);
    save(w, keepalive);
  }
}
addEventListener("pagehide", () => flush(true));
addEventListener("visibilitychange", () => document.hidden && flush(false));

// followPrefs takes the version the server fingerprints the files at, and
// reads them again when it moved: another device, or tab, set something.
export async function followPrefs(version) {
  if (!version || version === known) return;
  known = version;
  const before = new Map([...writes].map(([id, w]) => [id, dirty(w) ? -1 : w.seq]));
  let got;
  try {
    got = await api.prefs();
  } catch {
    known = "";
    return;
  }
  let changed = false;
  for (const where of ["user", "repo"]) {
    const next = got[where] || {};
    for (const key of new Set([...Object.keys(values[where]), ...Object.keys(next)])) {
      const id = idOf(where, key);
      const w = writes.get(id);
      if (w && (dirty(w) || before.get(id) !== w.seq)) continue;
      if (JSON.stringify(values[where][key]) === JSON.stringify(next[key])) continue;
      if (key in next) values[where][key] = next[key];
      else delete values[where][key];
      changed = true;
    }
  }
  if (changed) notify();
}

// usePref is usePersisted for a value on disk.
export function usePref(where, key, initial) {
  const id = idOf(where, key);
  const raw = () => values[where][key];
  const [state, setState] = useState(() => ({ id, value: raw() }));
  const value = state.id === id ? state.value : raw();
  const at = useRef();
  at.current = { where, key, initial };

  useEffect(() => {
    const update = () => setState((s) => (s.id === id && s.value === raw() ? s : { id, value: raw() }));
    update();
    listeners.add(update);
    return () => listeners.delete(update);
  }, [id]);

  const set = useCallback((v) => {
    const { where, key, initial } = at.current;
    setPref(where, key, typeof v === "function" ? v(readPref(where, key, initial)) : v, initial);
  }, []);
  return [value === undefined ? initial : value, set];
}

// Before these were on disk they were kept by the browser, for the one
// repository a lone dv's address served. The first page to find them there
// moves them, unless the disk already has its own.
const MOVED = { user: ["theme", "settings", "context", "preview"], repo: ["hideGenerated", "pathFilter", "agentAttachedBy", "agentAttachTo"] };
if (!boot.base && boot.page !== "hub") {
  try {
    const drafts = Object.keys(localStorage).filter((k) => k.startsWith("dv:draft:")).map((k) => k.slice(3));
    for (const [where, keys] of Object.entries({ user: MOVED.user, repo: [...MOVED.repo, ...drafts] })) {
      for (const key of keys) {
        const stored = localStorage.getItem("dv:" + key);
        if (stored === null) continue;
        localStorage.removeItem("dv:" + key);
        if (!(key in values[where])) setPref(where, key, JSON.parse(stored));
      }
    }
  } catch {}
}
