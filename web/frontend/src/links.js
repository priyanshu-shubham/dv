// Links in what agents, reviewers and Markdown files say: a web link opens in
// a tab of its own, away from dv, and a path to a file here - with a line, or
// not - opens the file at it. Only a path to a file the repository has is
// linked, so a word that merely looks like one stays a word.
import { createElement, useSyncExternalStore } from "react";

let known = { files: new Set(), byBase: new Map(), roots: [], version: 0, cache: new Map() };
const listeners = new Set();
const subscribe = (f) => (listeners.add(f), () => listeners.delete(f));

// setPaths is the repository's files, and the ways a path can start that
// mean its root: the folder, and the same written from ~.
export function setPaths(paths, roots) {
  const files = new Set(paths);
  const same = files.size === known.files.size && roots.join("\n") === known.roots.join("\n") && [...files].every((p) => known.files.has(p));
  if (same) return;
  const byBase = new Map();
  for (const p of files) {
    const base = p.slice(p.lastIndexOf("/") + 1);
    const named = byBase.get(base);
    if (named) named.push(p);
    else byBase.set(base, [p]);
  }
  known = { files, byBase, roots: roots.filter(Boolean), version: known.version + 1, cache: new Map() };
  listeners.forEach((f) => f());
}

// usePathsVersion redraws what links paths once the files are known, or change.
export const usePathsVersion = () => useSyncExternalStore(subscribe, () => known.version);

// resolve is the file a path names: as written from the root, absolute under
// it, or the end of just one file's path. Only files of its name are looked
// at, and each answer is kept, so a long conversation costs little.
function resolve(raw) {
  if (known.cache.has(raw)) return known.cache.get(raw);
  let s = raw.replace(/^\.\//, "");
  for (const root of known.roots) if (s.startsWith(root + "/")) s = s.slice(root.length + 1);
  let found = "";
  if (known.files.has(s)) found = s;
  else if (!s.startsWith("/") && !s.startsWith("~")) {
    const named = known.byBase.get(s.slice(s.lastIndexOf("/") + 1)) || [];
    const ends = s.includes("/") ? named.filter((p) => p.endsWith("/" + s)) : named;
    if (ends.length === 1) found = ends[0];
  }
  known.cache.set(raw, found);
  return found;
}

const PATH = /(?<![\w@+./~-])(?:~\/|\.{1,2}\/|\/)?[\w@+-][\w@+.-]*(?:\/[\w@+.-]+)*(?::\d+(?:[:-]\d+)?|#L\d+(?:-L?\d+)?)?/g;
const PARTS = /^(.*?)(?::(\d+)(?:[:-]\d+)?|#L(\d+)(?:-L?\d+)?)?$/;

// pathsIn finds the paths in text: [{ start, end, path, line }].
export function pathsIn(text) {
  if (!known.files.size) return [];
  const out = [];
  for (const m of text.matchAll(PATH)) {
    let [, p, a, b] = PARTS.exec(m[0]);
    let end = m.index + m[0].length;
    // A sentence's full stop is not the file's.
    if (!a && !b) {
      const trimmed = p.replace(/[.,:;]+$/, "");
      end -= p.length - trimmed.length;
      p = trimmed;
    }
    if (!p.includes("/") && !p.includes(".")) continue;
    const path = resolve(p);
    if (path) out.push({ start: m.index, end, path, line: Number(a || b || 0) });
  }
  return out;
}

// A link's own href is the file from the root, which a Markdown file's links
// are read as; the class and data are for the page's own text.
const hrefOf = (hit) => "/" + hit.path + (hit.line ? "#L" + hit.line : "");

function linkTokens(state, hit, inner) {
  const open = new state.Token("link_open", "a", 1);
  open.attrs = [["href", hrefOf(hit)], ["class", "path-link"], ["data-path", hit.path], ["data-line", String(hit.line)], ["title", hit.path + (hit.line ? ":" + hit.line : "")]];
  return [open, ...inner, new state.Token("link_close", "a", -1)];
}

function textToken(state, content) {
  const t = new state.Token("text", "", 0);
  t.content = content;
  return t;
}

// linkPaths has md link the paths in its text and inline code, outside the
// links written already, and open web links in a tab of their own.
export function linkPaths(md) {
  md.core.ruler.after("linkify", "dv_paths", (state) => {
    if (!known.files.size) return;
    for (const block of state.tokens) {
      if (block.type !== "inline" || !block.children) continue;
      let depth = 0;
      const out = [];
      for (const t of block.children) {
        if (t.type === "link_open") depth++;
        else if (t.type === "link_close") depth--;
        if (depth > 0 || (t.type !== "text" && t.type !== "code_inline")) {
          out.push(t);
          continue;
        }
        const hits = pathsIn(t.content);
        if (t.type === "code_inline") {
          const whole = hits.length === 1 && hits[0].start === 0 && hits[0].end === t.content.length;
          out.push(...(whole ? linkTokens(state, hits[0], [t]) : [t]));
          continue;
        }
        let at = 0;
        for (const h of hits) {
          if (h.start > at) out.push(textToken(state, t.content.slice(at, h.start)));
          out.push(...linkTokens(state, h, [textToken(state, t.content.slice(h.start, h.end))]));
          at = h.end;
        }
        if (at < t.content.length) out.push(textToken(state, t.content.slice(at)));
      }
      block.children = out;
    }
  });
  const render = md.renderer.rules.link_open || ((tokens, i, opts, env, self) => self.renderToken(tokens, i, opts));
  md.renderer.rules.link_open = (tokens, i, opts, env, self) => {
    const t = tokens[i];
    if (!t.attrGet("data-path")) {
      t.attrSet("target", "_blank");
      t.attrSet("rel", "noopener noreferrer");
    }
    return render(tokens, i, opts, env, self);
  };
  return md;
}

// Markdown draws text with md, drawn again as the paths it could link change.
export function Markdown({ md, text, className }) {
  usePathsVersion();
  return createElement("div", { className, dangerouslySetInnerHTML: { __html: md.render(text || "") } });
}

// linkText is plain text with its paths as links, for a <pre> of output.
export function linkText(text) {
  const hits = pathsIn(text);
  if (!hits.length) return text;
  const out = [];
  let at = 0;
  for (const h of hits) {
    if (h.start > at) out.push(text.slice(at, h.start));
    out.push(
      createElement("a", { key: h.start, href: hrefOf(h), className: "path-link", "data-path": h.path, "data-line": h.line }, text.slice(h.start, h.end)),
    );
    at = h.end;
  }
  if (at < text.length) out.push(text.slice(at));
  return out;
}
