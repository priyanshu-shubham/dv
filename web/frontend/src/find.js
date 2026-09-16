// Find in page. The browser's own find only sees what is in the DOM, and the
// diff keeps most of itself out of it: files load as they near the screen and
// blocks let go of their rows off it. So dv finds in the rows each file lays
// out instead - the ones it would render, mounted or not - which is also what
// keeps a match looking the same wherever the page happens to have rendered.
import { pairRows, unifiedRows } from "./hunks.js";

// Past this many the count reads as "N+", and files further on go unsearched.
export const MAX_FOUND = 10000;

// findRegExp builds what the bar's query and toggles ask for, or { error } for
// a regex that does not compile. Whole word follows the repository search: a
// literal query is held to a word boundary only at an end that is itself a
// word character, so `Get(` still finds `Get(ctx)`.
export function findRegExp({ query, caseSens, wholeWord, regex }) {
  if (!query) return null;
  let src = regex ? query : query.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  if (wholeWord) {
    src = regex
      ? `\\b(?:${src})\\b`
      : (/\w/.test(query[0]) ? "\\b" : "") + src + (/\w/.test(query[query.length - 1]) ? "\\b" : "");
  }
  try {
    return { re: new RegExp(src, caseSens ? "g" : "gi") };
  } catch (e) {
    return { error: e.message };
  }
}

// eachCell walks a file's rows in the order they are drawn - in split view the
// old cell before the new one on each row - calling fn(side, index) with the
// 0-based line index on that side. body is what DiffBody published.
function eachCell({ blocks, view }, fn) {
  for (const b of blocks) {
    if (b.kind !== "rows") continue;
    if (view === "split") {
      for (const row of pairRows(b.lines)) {
        if (row.left && row.left.o >= 0) fn("old", row.left.o);
        if (row.right && row.right.n >= 0) fn("new", row.right.n);
      }
    } else if (view === "code") {
      for (const l of b.lines) fn(l.n < 0 ? "old" : "new", l.n < 0 ? l.o : l.n);
    } else {
      for (const { line: l } of unifiedRows(b.lines)) fn(l.t === "d" ? "old" : "new", l.t === "d" ? l.o : l.n);
    }
  }
}

// eachMatch calls fn(start, end) for each non-empty match of re in text.
function eachMatch(text, re, fn) {
  re.lastIndex = 0;
  let m;
  while ((m = re.exec(text))) {
    // An empty match marks nothing, and would find itself forever.
    if (!m[0]) {
      re.lastIndex++;
      continue;
    }
    if (fn(m.index, m.index + m[0].length) === false) return;
  }
}

const cached = new WeakMap(); // body -> { re, found }

// fileMatches finds re in one file's rows: `list` in page order, each match
// { path, side, line, start, end, pos } with pos its cell's place in the file,
// and `byLine` mapping "side:line" to that line's [start, end] ranges for the
// rows to mark. The result is kept per body, so a file whose rows did not move
// hands back the same object and its section can skip the render.
export function fileMatches(path, body, re) {
  const hit = cached.get(body);
  if (hit?.re === re) return hit.found;
  const list = [];
  const byLine = new Map();
  let pos = 0;
  eachCell(body, (side, i) => {
    pos++;
    if (list.length >= MAX_FOUND) return;
    const text = (side === "old" ? body.fd.oldLines : body.fd.newLines)[i];
    if (!text) return;
    let ranges = null;
    eachMatch(text, re, (start, end) => {
      list.push({ path, side, line: i + 1, start, end, pos });
      (ranges ||= []).push([start, end]);
      return list.length < MAX_FOUND;
    });
    if (ranges) byLine.set(`${side}:${i + 1}`, ranges);
  });
  const found = { list, byLine };
  cached.set(body, { re, found });
  return found;
}

const heads = new WeakMap(); // entry -> { re, found }

// headMatches finds re in a diff file's header - its path, then the path it was
// renamed from - which is on the page whether or not the file is expanded. The
// matches sit before the file's rows, at pos 0, with side "head" or "from";
// `ranges` has each one's [start, end] offsets for the header to mark. null
// when nothing matched, and kept per entry like fileMatches.
export function headMatches(entry, re) {
  const hit = heads.get(entry);
  if (hit?.re === re) return hit.found;
  const list = [];
  const ranges = {};
  for (const [side, text] of [["head", entry.path], ["from", entry.oldPath]]) {
    if (!text) continue;
    eachMatch(text, re, (start, end) => {
      list.push({ path: entry.path, side, line: 0, start, end, pos: 0 });
      (ranges[side] ||= []).push([start, end]);
    });
  }
  const found = list.length ? { list, ranges } : null;
  heads.set(entry, { re, found });
  return found;
}

// cellPos is where a line sits among a file's drawn cells, in fileMatches' pos
// terms, or 0 when the file does not draw it.
export function cellPos(body, side, line) {
  let pos = 0;
  let at = 0;
  eachCell(body, (s, i) => {
    pos++;
    if (!at && s === side && i === line - 1) at = pos;
  });
  return at;
}
