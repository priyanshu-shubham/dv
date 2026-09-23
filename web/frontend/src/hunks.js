// Turns the server's line alignment into rows the diff view renders.
//
// The server sends both complete file sides plus the ops that align them, so
// collapsing to N lines of context — and expanding back out — is pure client
// work with no round trip.

const EQUAL = 0, DELETE = 1, INSERT = 2;

// flatten expands the run-length ops into one entry per displayed line.
function flatten(ops) {
  const lines = [];
  for (const op of ops) {
    if (op.k === EQUAL) {
      for (let i = 0; i < (op.ol || 0); i++) lines.push({ t: "e", o: op.os + i, n: op.ns + i });
    } else if (op.k === DELETE) {
      for (let i = 0; i < (op.ol || 0); i++) lines.push({ t: "d", o: op.os + i, n: -1 });
    } else {
      for (let i = 0; i < (op.nl || 0); i++) lines.push({ t: "i", o: -1, n: op.ns + i });
    }
  }
  return lines;
}

// buildBlocks groups lines into visible runs separated by collapsed gaps.
// `expanded` maps a gap id to how much of it the user has revealed, as grow
// keeps it. `anchors` are the ranges comments hang from,
// { side, start, end } in line numbers.
export function buildBlocks(fd, context, expanded, anchors = []) {
  const lines = flatten(fd.ops || []);
  const keep = new Array(lines.length).fill(false);
  const around = (i, n) => {
    for (let j = Math.max(0, i - n); j <= Math.min(lines.length - 1, i + n); j++) keep[j] = true;
  };
  for (let i = 0; i < lines.length; i++) if (lines[i].t !== "e") around(i, context);
  const blocks = layout(lines, keep, expanded, context);

  // A comment on a folded line would never be seen, so a hidden anchor unfolds
  // with a line either side. Anchors already in view are left alone: cutting
  // the gaps again around them would fold away context the reader had opened.
  const pinned = anchoredLines(lines, anchors);
  if (!pinned.length) return withScopes(fd, lines, blocks);
  const shown = new Set();
  for (const b of blocks) if (b.kind === "rows") for (const l of b.lines) shown.add(l);
  const hidden = pinned.filter((i) => !shown.has(lines[i]));
  if (!hidden.length) return withScopes(fd, lines, blocks);
  for (const i of hidden) around(i, 1);
  return withScopes(fd, lines, layout(lines, keep, expanded, context));
}

// withScopes names, on each gap, what the change after it sits inside. The
// line above a gap is often the end of another function, which then reads as
// the one being changed.
function withScopes(fd, lines, blocks) {
  // A header on screen needs no naming, nor one opening above the rows just
  // before the gap: those rows are inside it, so it was named already.
  const shown = new Set();
  for (const b of blocks) if (b.kind === "rows") for (const l of b.lines) shown.add(l);
  const index = new Map(lines.map((l, i) => [l, i]));
  let above = -1;
  const hidden = (list) => {
    const out = list?.filter((s) => !shown.has(lines[s.from]) && s.from >= above);
    return out?.length ? out : undefined;
  };
  for (let i = 0; i < blocks.length; i++) {
    const b = blocks[i];
    if (b.kind !== "gap") continue;
    above = -1;
    for (let j = i - 1; j >= 0 && blocks[j].kind === "rows" && blocks[j].lines.length; j--) above = index.get(blocks[j].lines[0]);
    let t = b.next;
    while (t < lines.length && lines[t].t === "e") t++;
    if (t === lines.length) continue;
    const old = hidden(scopeOf(fd, lines, t, "o"));
    const now = hidden(scopeOf(fd, lines, t, "n"));
    // One column shows the new side, unless the change only deletes.
    let e = t;
    while (e < lines.length && lines[e].t === "d") e++;
    if (old || now) b.scope = { old, new: now, side: e < lines.length && lines[e].t === "i" ? "new" : "old" };
  }
  return blocks;
}

// Keywords that open a block without naming anything worth pointing to.
const CONTROL =
  /^(?:[)\]}]\s*)?(?:if|else|elif|elsif|for|foreach|while|do|switch|case|default|try|catch|except|finally|with|return|select|loop|match|unless|until|begin|rescue|ensure|when|defer|go|await|yield|throw)\b/;
const CLOSER = /^(?:[)\]}]+[;,]?|end)$/;
const COMMENT = /^(?:\/\/|\/\*|\*|#|--|<!--)/;
const DECL = /\b(?:func|function|def|class|struct|interface|enum|impl|trait|module|namespace|fn|sub|proc)\b/;
const OPENS = /(?:[{([:]|=>|\bdo(?:\s*\|[^|]*\|)?)$/;
const MAX_SCOPES = 3;

// scopeOf lists the headers of the blocks enclosing lines[at], outermost first. Like an editor's sticky scroll it goes by
// indentation - each header is the nearest line above that is indented less -
// so it needs no grammar, and a markdown file gets its headings instead. Each
// is { lines, text }, the file lines on `side` it is drawn from and as text,
// with where it starts in `lines` for revealFor.
function scopeOf(fd, lines, at, side) {
  const src = side === "n" ? fd.newLines : fd.oldLines;
  const text = (i) => src[lines[i][side]] ?? "";
  const heading = fd.lang === "markdown";
  const out = [];

  let limit = heading ? headingLevel(text(at)) || 7 : Infinity;
  // A blank line added says nothing about its depth; the code after it does.
  for (let i = at; !heading && i < Math.min(lines.length, at + 20); i++) {
    if (lines[i][side] < 0 || !text(i).trim()) continue;
    limit = indentOf(text(i));
    break;
  }

  for (let i = at - 1; i >= 0 && limit > 0 && out.length < MAX_SCOPES; i--) {
    if (lines[i][side] < 0) continue;
    const raw = text(i);
    const line = raw.trim();
    if (heading) {
      const level = headingLevel(line);
      if (!level || level >= limit) continue;
      limit = level;
      out.push({ lines: [lines[i][side]], text: line, from: i, at: i, depth: level, side, heading });
      continue;
    }
    if (!line || COMMENT.test(line) || CLOSER.test(line)) continue;
    const depth = indentOf(raw);
    if (depth >= limit) continue;
    limit = depth;
    const head = header(lines, text, side, i);
    if (head) out.push({ ...head, at: i, depth, side });
  }
  return out.length ? out.reverse() : undefined;
}

// header is the scope lines[i] opens, or null.
function header(lines, text, side, i) {
  let at = [i];
  let line = text(i).trim();
  // A brace on a line of its own belongs to the line above it.
  const brace = line === "{";
  if (brace) {
    let j = i - 1;
    while (j >= 0 && (lines[j][side] < 0 || !text(j).trim())) j--;
    if (j < 0) return null;
    at = [j];
    line = text(j).trim();
  }
  // A signature broken over lines ends on the bracket that closes it.
  if (/^[)\]]/.test(line)) {
    const from = opener(lines, text, side, at[0]);
    if (from === null) return null;
    at = [from, at[0]];
    line = text(from).trim() + "…" + line;
  }
  if (CONTROL.test(line) || !(brace || OPENS.test(line) || DECL.test(line))) return null;
  return { lines: at.map((j) => lines[j][side]), text: stripOpener(line), from: at[0] };
}

// stripOpener drops the brace or colon a header ends on; it works on the
// highlighted html of the line too, where punctuation is left unwrapped.
export function stripOpener(s) {
  return s.replace(/\s*[{:]$/, "");
}

// opener walks back from a line starting with closing brackets to the line
// that opened them.
function opener(lines, text, side, i) {
  let depth = 0;
  for (let j = i; j >= 0 && j > i - 50; j--) {
    if (lines[j][side] < 0) continue;
    for (const c of text(j)) {
      if (c === ")" || c === "]") depth++;
      else if (c === "(" || c === "[") depth--;
    }
    if (depth <= 0 && j < i) return j;
  }
  return null;
}

function indentOf(s) {
  let n = 0;
  for (const c of s) {
    if (c === " ") n++;
    else if (c === "\t") n += 4;
    else break;
  }
  return n;
}

function headingLevel(s) {
  const m = /^(#{1,6})\s/.exec(s);
  return m ? m[1].length : 0;
}

function layout(lines, keep, expanded, context) {
  // An unchanged file - opened from the sidebar, or a pure rename - is one
  // collapsible gap rather than a wall of context nobody asked for.
  if (!keep.includes(true) && lines.length <= context * 2) return [{ kind: "rows", lines }];
  const blocks = [];
  let i = 0;
  while (i < lines.length) {
    const start = i;
    if (keep[i]) {
      while (i < lines.length && keep[i]) i++;
      blocks.push({ kind: "rows", lines: lines.slice(start, i) });
    } else {
      while (i < lines.length && !keep[i]) i++;
      // Named by its first base line, which a working-tree edit cannot move, so
      // context the reader expanded survives the file being updated live.
      const gap = gapFor(`g${lines[start].o}`, lines, start, i, expanded);
      if (Array.isArray(gap)) blocks.push(...gap);
      else blocks.push(gap);
    }
  }
  return blocks;
}

// A long range keeps only its last lines in view; the comment hangs off the end.
const ANCHOR_SPAN = 20;

// anchoredLines turns anchors into indexes into `lines`.
function anchoredLines(lines, anchors) {
  if (!anchors.length) return [];
  const at = { old: new Map(), new: new Map() };
  lines.forEach((l, i) => {
    if (l.o >= 0) at.old.set(l.o + 1, i);
    if (l.n >= 0) at.new.set(l.n + 1, i);
  });
  const out = [];
  for (const a of anchors) {
    const m = at[a.side];
    if (!m || !(a.end > 0)) continue;
    for (let line = Math.max(a.start || a.end, a.end - ANCHOR_SPAN + 1); line <= a.end; line++) {
      const i = m.get(line);
      if (i !== undefined) out.push(i);
    }
  }
  return out;
}

// gapFor produces the blocks for one collapsed region, honouring any expansion
// the user applied. Expansion reveals lines from both ends inward so context
// grows around the hunks the gap separates, which is what a reader expects.
function gapFor(id, lines, start, end, expanded) {
  const total = end - start;
  const state = expanded[id];
  const { head, tail } = ends(state);
  const shown = head + tail;
  if (state === "all" || shown >= total) {
    return { kind: "rows", lines: lines.slice(start, end) };
  }
  const out = [];
  if (head > 0) out.push({ kind: "rows", lines: lines.slice(start, start + head) });
  out.push({
    kind: "gap",
    id,
    hidden: total - shown,
    firstLine: lines[start + head],
    lastLine: lines[end - tail - 1],
    next: end - tail,
    start,
    end,
  });
  if (tail > 0) out.push({ kind: "rows", lines: lines.slice(end - tail, end) });
  return out;
}

// grow widens what a gap shows: `amount` more lines split between its ends,
// "all", or { head, tail } for at least that many at each end.
export function grow(state, amount) {
  if (state === "all" || amount === "all") return "all";
  const { head, tail } = ends(state);
  if (typeof amount === "number") {
    const h = Math.ceil(amount / 2);
    return { head: head + h, tail: tail + amount - h };
  }
  return { head: Math.max(head, amount.head || 0), tail: Math.max(tail, amount.tail || 0) };
}

function ends(state) {
  if (typeof state === "number") return { head: Math.ceil(state / 2), tail: Math.floor(state / 2) };
  return state && typeof state === "object" ? state : { head: 0, tail: 0 };
}

// revealFor is how far each gap in `blocks` must open to show the whole of a
// scope: from its header down to where it closes.
export function revealFor(fd, blocks, scope) {
  const from = scope.from;
  const to = scopeEnd(fd, scope);
  const out = [];
  for (const b of blocks) {
    if (b.kind !== "gap" || b.end <= from || b.start > to) continue;
    const last = b.end - 1;
    if (from <= b.start && to >= last) out.push([b.id, "all"]);
    else if (from <= b.start) out.push([b.id, { head: to - b.start + 1 }]);
    else out.push([b.id, { tail: b.end - from }]);
  }
  return out;
}

// scopeEnd finds the last line of the scope scopeOf found: a closing line at
// the header's depth, else the last line indented deeper; for markdown, the
// line before the next heading as high.
function scopeEnd(fd, { side, at, depth, heading }) {
  const lines = flatten(fd.ops || []);
  const src = side === "n" ? fd.newLines : fd.oldLines;
  let last = at;
  for (let i = at + 1; i < lines.length; i++) {
    if (lines[i][side] < 0) continue;
    const raw = src[lines[i][side]] ?? "";
    const line = raw.trim();
    if (heading) {
      const level = headingLevel(line);
      if (level && level <= depth) break;
      last = i;
      continue;
    }
    if (!line || COMMENT.test(line)) continue;
    if (indentOf(raw) <= depth) {
      if (/^(?:[)\]}]|end\b)/.test(line)) last = i;
      break;
    }
    last = i;
  }
  return last;
}

// pairRows aligns a run of lines into left/right columns for the split view and
// marks which deleted/added pairs should get word-level highlighting.
export function pairRows(lines) {
  const rows = [];
  let i = 0;
  while (i < lines.length) {
    const l = lines[i];
    if (l.t === "e") {
      rows.push({ left: l, right: l, kind: "e" });
      i++;
      continue;
    }
    const dels = [];
    const adds = [];
    while (i < lines.length && lines[i].t === "d") dels.push(lines[i++]);
    while (i < lines.length && lines[i].t === "i") adds.push(lines[i++]);
    const n = Math.max(dels.length, adds.length);
    // A replacement of equal size lines up one-to-one, which is the case where
    // word-level diffing is meaningful; unbalanced runs are pure add/remove.
    const paired = dels.length > 0 && adds.length > 0;
    for (let k = 0; k < n; k++) {
      rows.push({
        left: dels[k] || null,
        right: adds[k] || null,
        kind: "c",
        word: paired && dels[k] && adds[k],
      });
    }
  }
  return rows;
}

// unifiedRows keeps document order but still flags the del/add pairs so the
// unified view gets the same word highlighting as split.
export function unifiedRows(lines) {
  const rows = [];
  let i = 0;
  while (i < lines.length) {
    if (lines[i].t === "e") {
      rows.push({ line: lines[i], word: false });
      i++;
      continue;
    }
    const start = i;
    const dels = [];
    const adds = [];
    while (i < lines.length && lines[i].t === "d") dels.push(lines[i++]);
    while (i < lines.length && lines[i].t === "i") adds.push(lines[i++]);
    const paired = dels.length > 0 && adds.length > 0;
    for (let k = 0; k < dels.length; k++) {
      rows.push({ line: dels[k], word: paired ? adds[k] || null : null });
    }
    for (let k = 0; k < adds.length; k++) {
      rows.push({ line: adds[k], word: paired ? dels[k] || null : null });
    }
    if (i === start) i++; // defensive: never spin on an unexpected op
  }
  return rows;
}

// mapLine follows a line from one version of a file's diff into the next. When
// only the new side moved, a new-side line is carried across by way of the
// base line it sits on or follows, since the base is common to both versions.
export function mapLine(prev, next, side, line) {
  const same = { side, line };
  const sameBase =
    prev.oldLines.length === next.oldLines.length && prev.oldLines.every((l, i) => l === next.oldLines[i]);
  if (side === "old" || !sameBase) return same;

  const a = flatten(prev.ops || []);
  let i = a.findIndex((l) => l.n === line - 1);
  let past = 0;
  while (i > 0 && a[i].o < 0) {
    i--;
    past++;
  }
  if (i < 0 || a[i].o < 0) return same;
  const m = flatten(next.ops || []).find((l) => l.o === a[i].o);
  if (!m) return same;
  // The line it hung off may since have been deleted; its old half is still
  // on screen, at the same place.
  return m.n >= 0 ? { side: "new", line: m.n + 1 + past } : { side: "old", line: m.o + 1 };
}

// codeLines lays out one side of a file for Code mode: every line, with what
// the diff changed carried as a mark on the lines rather than as rows of its
// own. A deletion marks the line that now stands where it was ("del"), or the
// last line when it was at the end ("del-end").
export function codeLines(fd, side) {
  const all = flatten(fd.ops || []);
  if (side === "old") return all.filter((l) => l.o >= 0).map((l) => ({ t: "e", o: l.o, n: -1 }));
  const out = [];
  for (let i = 0; i < all.length; ) {
    if (all[i].t === "e") {
      out.push(all[i++]);
      continue;
    }
    let dels = 0;
    const adds = [];
    for (; i < all.length && all[i].t !== "e"; i++) {
      if (all[i].t === "d") dels++;
      else adds.push(all[i]);
    }
    for (const l of adds) out.push({ ...l, mark: dels ? "mod" : "add" });
    if (adds.length) continue;
    if (i < all.length) out.push({ ...all[i++], mark: "del" });
    else if (out.length) out[out.length - 1] = { ...out[out.length - 1], mark: "del-end" };
  }
  return out;
}

// plainDiff is a file read whole as a diff that changed nothing, its lines on
// the side they were read from.
export function plainDiff(r, side = "new") {
  const n = r.lines.length;
  const old = side === "old";
  return { path: r.path, lang: r.lang, oldLines: old ? r.lines : [], newLines: old ? [] : r.lines, ops: [{ k: EQUAL, os: 0, ol: n, ns: 0, nl: n }] };
}

// newLineFor finds where an old-side line stands in the new file: the same line
// if it survived, or the one now in the place it was deleted from.
export function newLineFor(fd, line) {
  const o = line - 1;
  const op = (fd.ops || []).find((op) => op.k !== INSERT && o >= op.os && o < op.os + op.ol);
  if (!op) return line;
  const n = op.k === EQUAL ? op.ns + (o - op.os) : op.ns;
  return Math.max(1, Math.min(fd.newLines.length, n + 1));
}

// changeAnchors lists the new-side line numbers that start each hunk, for the
// next/previous-change keyboard shortcuts.
export function changeAnchors(fd) {
  const lines = flatten(fd.ops || []);
  const anchors = [];
  let inRun = false;
  for (const l of lines) {
    if (l.t === "e") {
      inRun = false;
      continue;
    }
    if (!inRun) {
      anchors.push(l);
      inRun = true;
    }
  }
  return anchors;
}
