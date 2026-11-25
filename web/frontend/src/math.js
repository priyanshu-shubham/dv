// TeX math, as GitHub reads it: $inline$, $$display$$ and ```math blocks.
// Markdown leaves each as its source in a .math-tex element, and drawMath has
// KaTeX draw over it once it is on the page, so KaTeX is fetched only when
// there is math to show, and the file renderer's sanitizing never sees its
// markup.

export function mathSyntax(md) {
  const { escapeHtml } = md.utils;
  md.inline.ruler.after("escape", "math_inline", inline);
  md.block.ruler.before("fence", "math_block", block, { alt: ["paragraph", "reference", "blockquote", "list"] });
  md.core.ruler.after("block", "math_fence", (state) => {
    for (const t of state.tokens) {
      if (t.type === "fence" && t.info.trim() === "math") {
        t.type = "math_block";
        t.tag = "div";
        t.attrSet("class", "math-tex math-display");
      }
    }
  });
  md.renderer.rules.math_inline = (tokens, i, opts, env, self) =>
    `<span${self.renderAttrs(tokens[i])}>${escapeHtml(tokens[i].content)}</span>`;
  md.renderer.rules.math_block = (tokens, i, opts, env, self) =>
    `<div${self.renderAttrs(tokens[i])}>${escapeHtml(tokens[i].content)}</div>\n`;
  return md;
}

const space = (c) => c === undefined || /\s/.test(c);

// inline takes $…$, or $$…$$ inside a paragraph. As in Pandoc, a single $
// opens only before a non-space and closes only after one and not before a
// digit, so "$5 and $10" stays prose.
function inline(state, silent) {
  const src = state.src;
  const from = state.pos;
  if (src[from] !== "$") return false;
  const display = src[from + 1] === "$";
  const mark = display ? "$$" : "$";
  const start = from + mark.length;
  if (!display && space(src[start])) return false;
  let end = start;
  while ((end = src.indexOf(mark, end)) !== -1 && src[end - 1] === "\\") end++;
  if (end === -1 || end === start) return false;
  if (!display && (space(src[end - 1]) || /\d/.test(src[end + 1] || ""))) return false;
  if (!silent) {
    const t = state.push("math_inline", "span", 0);
    t.attrSet("class", display ? "math-tex math-display" : "math-tex");
    t.content = src.slice(start, end);
    t.markup = mark;
  }
  state.pos = end + mark.length;
  return true;
}

// block takes $$ starting a line, through the line that ends with $$.
function block(state, startLine, endLine, silent) {
  if (state.sCount[startLine] - state.blkIndent >= 4) return false;
  const lineText = (n) => state.src.slice(state.bMarks[n] + state.tShift[n], state.eMarks[n]);
  const first = lineText(startLine);
  if (!first.startsWith("$$")) return false;
  const rest = first.slice(2);
  const close = rest.indexOf("$$");
  let content;
  let last = startLine;
  if (close !== -1) {
    // Text after the closing $$ makes it inline math in a paragraph.
    if (rest.slice(close + 2).trim()) return false;
    content = rest.slice(0, close);
  } else {
    const lines = [rest];
    for (;;) {
      if (++last >= endLine) return false;
      if (state.sCount[last] < state.blkIndent && state.bMarks[last] + state.tShift[last] < state.eMarks[last]) return false;
      const text = lineText(last).trimEnd();
      if (text.endsWith("$$")) {
        lines.push(text.slice(0, -2));
        break;
      }
      lines.push(text);
    }
    content = lines.join("\n");
  }
  if (silent) return true;
  const t = state.push("math_block", "div", 0);
  t.attrSet("class", "math-tex math-display");
  t.block = true;
  t.content = content.trim();
  t.markup = "$$";
  t.map = [startLine, last + 1];
  state.line = last + 1;
  return true;
}

let katex = null;
let loading = null;
const drawn = new Map(); // "D" or "I" + TeX -> KaTeX's HTML

// drawMath draws the math under root, after fetching KaTeX the first time.
// Until it has come, or if it never does, the TeX itself shows.
export function drawMath(root) {
  if (!root) return;
  const els = root.querySelectorAll(".math-tex:not(.math-drawn)");
  if (!els.length) return;
  if (!katex) {
    load().then(() => root.isConnected && drawMath(root), () => {});
    return;
  }
  for (const el of els) {
    const display = el.classList.contains("math-display");
    const tex = el.textContent;
    const key = (display ? "D" : "I") + tex;
    let html = drawn.get(key);
    if (html === undefined) {
      html = katex.renderToString(tex, { displayMode: display, throwOnError: false });
      drawn.set(key, html);
      if (drawn.size > 500) drawn.delete(drawn.keys().next().value);
    }
    el.innerHTML = html;
    el.classList.add("math-drawn");
  }
}

function load() {
  loading ||= import("katex").then(
    (m) => (katex = m.default),
    (e) => {
      loading = null;
      throw e;
    },
  );
  return loading;
}
