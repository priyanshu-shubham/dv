// Syntax highlighting for the diff and file views.
//
// highlight.js works on whole documents, but the viewer renders one row per
// line. Highlighting each line in isolation breaks anything spanning lines
// (block comments, template literals), so we highlight the full text once and
// then split the resulting HTML, reopening any spans that were still open at
// the line break. The result is cached per file side.

import hljs from "highlight.js/lib/common";

// Languages worth having beyond highlight.js's "common" bundle, registered
// lazily so they cost nothing until a file needs them.
const extra = {
  go: () => import("highlight.js/lib/languages/go"),
  rust: () => import("highlight.js/lib/languages/rust"),
  yaml: () => import("highlight.js/lib/languages/yaml"),
  dockerfile: () => import("highlight.js/lib/languages/dockerfile"),
  makefile: () => import("highlight.js/lib/languages/makefile"),
  protobuf: () => import("highlight.js/lib/languages/protobuf"),
  scss: () => import("highlight.js/lib/languages/scss"),
  less: () => import("highlight.js/lib/languages/less"),
  lua: () => import("highlight.js/lib/languages/lua"),
  elixir: () => import("highlight.js/lib/languages/elixir"),
  swift: () => import("highlight.js/lib/languages/swift"),
  dart: () => import("highlight.js/lib/languages/dart"),
  scala: () => import("highlight.js/lib/languages/scala"),
  clojure: () => import("highlight.js/lib/languages/clojure"),
  haskell: () => import("highlight.js/lib/languages/haskell"),
  graphql: () => import("highlight.js/lib/languages/graphql"),
  vim: () => import("highlight.js/lib/languages/vim"),
  zig: () => import("highlight.js/lib/languages/rust"),
  terraform: () => import("highlight.js/lib/languages/ruby"),
  nix: () => import("highlight.js/lib/languages/nix"),
  erlang: () => import("highlight.js/lib/languages/erlang"),
  r: () => import("highlight.js/lib/languages/r"),
  perl: () => import("highlight.js/lib/languages/perl"),
  objectivec: () => import("highlight.js/lib/languages/objectivec"),
};

const loading = new Map();

// ensureLanguage resolves once per language; callers re-render when it settles.
export function ensureLanguage(lang, onReady) {
  if (!lang || hljs.getLanguage(lang)) return true;
  const loader = extra[lang];
  if (!loader) return true; // unknown to us: fall back to plain text
  if (!loading.has(lang)) {
    loading.set(
      lang,
      loader().then((m) => {
        hljs.registerLanguage(lang, m.default);
      }),
    );
  }
  loading.get(lang).then(onReady);
  return false;
}

const escapeMap = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" };
export const escapeHtml = (s) => s.replace(/[&<>"]/g, (c) => escapeMap[c]);

// Files past this size are shown unhighlighted; the cost is a visible pause and
// nobody reads a 20k-line generated file token by token anyway.
const MAX_HIGHLIGHT_LINES = 12000;

const cache = new Map();

// highlightLines returns one HTML string per line of `lines`.
export function highlightLines(key, lines, lang) {
  const hit = cache.get(key);
  if (hit && hit.lines === lines) return hit.html;

  let html;
  if (!lang || !hljs.getLanguage(lang) || lines.length > MAX_HIGHLIGHT_LINES) {
    html = lines.map(escapeHtml);
  } else {
    try {
      html = splitHighlighted(hljs.highlight(lines.join("\n"), { language: lang, ignoreIllegals: true }).value);
      // A grammar that swallows newlines would desynchronise every row after
      // it, so fall back rather than render a scrambled file.
      if (html.length !== lines.length) html = lines.map(escapeHtml);
    } catch {
      html = lines.map(escapeHtml);
    }
  }
  cache.set(key, { lines, html });
  if (cache.size > 40) cache.delete(cache.keys().next().value);
  return html;
}

// splitHighlighted breaks highlighted HTML at newlines, closing open spans at
// the end of each line and reopening them at the start of the next.
function splitHighlighted(html) {
  const out = [];
  const open = [];
  let current = "";
  const tagRe = /<\/?span[^>]*>/g;
  let last = 0;
  let m;

  const pushText = (text) => {
    const parts = text.split("\n");
    parts.forEach((part, i) => {
      if (i > 0) {
        out.push(current + "</span>".repeat(open.length));
        current = open.join("");
      }
      current += part;
    });
  };

  while ((m = tagRe.exec(html)) !== null) {
    pushText(html.slice(last, m.index));
    last = tagRe.lastIndex;
    if (m[0][1] === "/") {
      open.pop();
      current += m[0];
    } else {
      open.push(m[0]);
      current += m[0];
    }
  }
  pushText(html.slice(last));
  out.push(current + "</span>".repeat(open.length));
  return out;
}

// applyRanges wraps character ranges of the underlying text in a marker span,
// splicing them into already-highlighted HTML. It only ever wraps inside a
// single text node, so the surrounding tag nesting stays valid.
export function applyRanges(html, ranges, cls) {
  if (!ranges || !ranges.length) return html;
  let out = "";
  let pos = 0; // offset in the plain text the HTML represents
  let ri = 0;
  const tagRe = /<[^>]+>/g;
  let last = 0;
  let m;

  const emitText = (chunk) => {
    let i = 0;
    while (i < chunk.length) {
      while (ri < ranges.length && ranges[ri][1] <= pos) ri++;
      const r = ranges[ri];
      if (!r || r[0] >= pos + (chunk.length - i)) {
        out += escapeUnescaped(chunk.slice(i));
        pos += chunk.length - i;
        return;
      }
      if (pos < r[0]) {
        const n = r[0] - pos;
        out += escapeUnescaped(chunk.slice(i, i + n));
        i += n;
        pos += n;
        continue;
      }
      const n = Math.min(r[1] - pos, chunk.length - i);
      out += `<span class="${cls}">` + escapeUnescaped(chunk.slice(i, i + n)) + "</span>";
      i += n;
      pos += n;
    }
  };

  while ((m = tagRe.exec(html)) !== null) {
    emitText(decodeEntities(html.slice(last, m.index)));
    out += m[0];
    last = tagRe.lastIndex;
  }
  emitText(decodeEntities(html.slice(last)));
  return out;
}

// The HTML we splice into is already escaped, so ranges have to be measured
// against the decoded text and re-escaped on the way out.
const entityRe = /&(amp|lt|gt|quot|#x27|#39);/g;
const entities = { amp: "&", lt: "<", gt: ">", quot: '"', "#x27": "'", "#39": "'" };
const decodeEntities = (s) => s.replace(entityRe, (_, e) => entities[e]);
const escapeUnescaped = (s) => escapeHtml(s);
