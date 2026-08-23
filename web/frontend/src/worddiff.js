// Intra-line diffing: given a removed line and the added line that replaced it,
// work out which words actually changed so the row can highlight those instead
// of painting the whole line red and green.

// Tokens are runs of word characters, runs of whitespace, or single symbols —
// splitting this way keeps identifier renames tight and avoids the "everything
// changed" look that character-level diffing produces on indented code.
function tokenize(s) {
  return s.match(/[A-Za-z0-9_$]+|\s+|[^\sA-Za-z0-9_$]/g) || [];
}

const MAX_TOKENS = 400;

// diffWords returns [leftSpans, rightSpans], each a list of
// { text, changed } segments covering the whole input line.
export function diffWords(oldLine, newLine) {
  if (oldLine === newLine) {
    return [[{ text: oldLine, changed: false }], [{ text: newLine, changed: false }]];
  }
  const a = tokenize(oldLine);
  const b = tokenize(newLine);
  if (a.length > MAX_TOKENS || b.length > MAX_TOKENS) {
    return [[{ text: oldLine, changed: true }], [{ text: newLine, changed: true }]];
  }

  let pre = 0;
  while (pre < a.length && pre < b.length && a[pre] === b[pre]) pre++;
  let suf = 0;
  while (suf < a.length - pre && suf < b.length - pre && a[a.length - 1 - suf] === b[b.length - 1 - suf]) suf++;

  const midA = a.slice(pre, a.length - suf);
  const midB = b.slice(pre, b.length - suf);
  const [ma, mb] = lcsMark(midA, midB);

  const build = (tokens, marks) => {
    const spans = [];
    const add = (text, changed) => {
      if (!text) return;
      const last = spans[spans.length - 1];
      if (last && last.changed === changed) last.text += text;
      else spans.push({ text, changed });
    };
    add(tokens.slice(0, pre).join(""), false);
    marks.forEach((changed, i) => add(tokens[pre + i], changed));
    add(tokens.slice(tokens.length - suf).join(""), false);
    return spans.length ? spans : [{ text: "", changed: false }];
  };
  return [build(a, ma), build(b, mb)];
}

// lcsMark flags the tokens on each side that are not part of the longest common
// subsequence — the classic O(nm) table, which is fine at this token budget.
function lcsMark(a, b) {
  const n = a.length, m = b.length;
  const ma = new Array(n).fill(true);
  const mb = new Array(m).fill(true);
  if (!n || !m) return [ma, mb];

  const dp = new Uint16Array((n + 1) * (m + 1));
  const at = (i, j) => i * (m + 1) + j;
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      dp[at(i, j)] = a[i] === b[j]
        ? dp[at(i + 1, j + 1)] + 1
        : Math.max(dp[at(i + 1, j)], dp[at(i, j + 1)]);
    }
  }
  let i = 0, j = 0;
  while (i < n && j < m) {
    if (a[i] === b[j]) {
      ma[i] = false;
      mb[j] = false;
      i++;
      j++;
    } else if (dp[at(i + 1, j)] >= dp[at(i, j + 1)]) i++;
    else j++;
  }
  return [ma, mb];
}

// spansToRanges converts segments into [start, end) character ranges over the
// original line, which is the form the highlighter overlay wants.
export function spansToRanges(spans) {
  const ranges = [];
  let pos = 0;
  for (const s of spans) {
    if (s.changed && s.text.length) ranges.push([pos, pos + s.text.length]);
    pos += s.text.length;
  }
  return ranges;
}
