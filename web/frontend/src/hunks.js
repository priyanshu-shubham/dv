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
// ("all" for the whole thing).
export function buildBlocks(fd, context, expanded) {
  const lines = flatten(fd.ops || []);
  const blocks = [];
  const push = (b) => (Array.isArray(b) ? blocks.push(...b) : blocks.push(b));

  // An unchanged file — opened from the sidebar, or a pure rename — is one
  // collapsible gap rather than a wall of context nobody asked for.
  if (!lines.some((l) => l.t !== "e")) {
    if (lines.length <= context * 2) return [{ kind: "rows", lines }];
    push(gapFor("g0", lines, 0, lines.length, expanded));
    return blocks;
  }

  const keep = new Array(lines.length).fill(false);
  for (let i = 0; i < lines.length; i++) {
    if (lines[i].t === "e") continue;
    for (let j = Math.max(0, i - context); j <= Math.min(lines.length - 1, i + context); j++) {
      keep[j] = true;
    }
  }

  let i = 0;
  while (i < lines.length) {
    const start = i;
    if (keep[i]) {
      while (i < lines.length && keep[i]) i++;
      blocks.push({ kind: "rows", lines: lines.slice(start, i) });
    } else {
      while (i < lines.length && !keep[i]) i++;
      push(gapFor(`g${start}`, lines, start, i, expanded));
    }
  }
  return blocks;
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
