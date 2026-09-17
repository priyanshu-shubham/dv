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
// `expanded` maps a gap id to how many of its lines the user has revealed
// ("all" for the whole thing). `anchors` are the ranges comments hang from,
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
  if (!pinned.length) return blocks;
  const shown = new Set();
  for (const b of blocks) if (b.kind === "rows") for (const l of b.lines) shown.add(l);
  const hidden = pinned.filter((i) => !shown.has(lines[i]));
  if (!hidden.length) return blocks;
  for (const i of hidden) around(i, 1);
  return layout(lines, keep, expanded, context);
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
  if (state === "all" || (typeof state === "number" && state >= total)) {
    return { kind: "rows", lines: lines.slice(start, end) };
  }
  const shown = typeof state === "number" ? state : 0;
  const head = Math.ceil(shown / 2);
  const tail = shown - head;
  const out = [];
  if (head > 0) out.push({ kind: "rows", lines: lines.slice(start, start + head) });
  out.push({
    kind: "gap",
    id,
    hidden: total - shown,
    firstLine: lines[start + head],
    lastLine: lines[end - tail - 1],
  });
  if (tail > 0) out.push({ kind: "rows", lines: lines.slice(end - tail, end) });
  return out;
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
