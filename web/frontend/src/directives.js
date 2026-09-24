// Codex writes a few directives into its replies for its own app to draw:
// :codex-file-citation{path="…"} names the file a claim comes from, and
// :codex-followup[Label]{prompt="…"} offers a next message. Written as text
// they read as noise, so a citation shows as its path and a follow-up is taken
// out, for followupsIn to offer instead.
const DIRECTIVE = /^(:{1,3})(codex-file-citation|codex-followup)(?:\[([^\]\n]*)\])?\{((?:\s*[\w-]+\s*=\s*(?:"(?:\\.|[^"\\\n])*"|'(?:\\.|[^'\\\n])*'|[^\s}"']+))*)\s*\}/;
const ATTR = /([\w-]+)\s*=\s*(?:"((?:\\.|[^"\\\n])*)"|'((?:\\.|[^'\\\n])*)'|([^\s}"']+))/g;

function directiveAt(src) {
  const m = DIRECTIVE.exec(src);
  if (!m) return null;
  const attrs = {};
  for (const [, key, dq, sq, bare] of m[4].matchAll(ATTR)) {
    const v = dq ?? sq ?? bare;
    // A path keeps its backslashes, as Codex reads one; elsewhere \" is a quote.
    attrs[key] = m[2] === "codex-file-citation" ? v : v.replace(/\\(["'\\])/g, "$1");
  }
  return { name: m[2], label: m[3] || "", attrs, length: m[0].length };
}

export function codexDirectives(md) {
  md.inline.ruler.before("linkify", "codex_directives", (state, silent) => {
    if (state.src.charCodeAt(state.pos) !== 0x3a || state.src[state.pos - 1] === ":") return false;
    const d = directiveAt(state.src.slice(state.pos));
    if (!d || (d.name === "codex-file-citation" && !d.attrs.path)) return false;
    if (!silent) {
      if (d.name === "codex-file-citation") {
        const t = state.push("code_inline", "code", 0);
        t.markup = "`";
        t.content = d.attrs.path;
      } else {
        const prompt = d.attrs.prompt || d.label;
        if (prompt) (state.env.followups ||= []).push({ label: d.label || prompt, prompt });
      }
    }
    state.pos += d.length;
    return true;
  });
  // A follow-up on a line of its own leaves an empty paragraph, and in a list
  // of them, empty items in a list that may be left with none.
  md.core.ruler.after("inline", "codex_empty", (state) => {
    const t = state.tokens;
    const blank = (tok) => tok.type === "inline" && tok.children.every((c) => c.type === "softbreak" || (c.type === "text" && !c.content.trim()));
    for (let i = t.length - 3; i >= 0; i--) {
      if (t[i].type === "paragraph_open" && blank(t[i + 1]) && t[i + 2].type === "paragraph_close") t.splice(i, 3);
    }
    for (const [open, close] of [["list_item_open", "list_item_close"], ["bullet_list_open", "bullet_list_close"], ["ordered_list_open", "ordered_list_close"]]) {
      for (let i = t.length - 2; i >= 0; i--) {
        if (t[i].type === open && t[i + 1].type === close) t.splice(i, 2);
      }
    }
  });
  return md;
}

// followupsIn is the follow-ups a reply offers: [{ label, prompt }].
export function followupsIn(md, text) {
  if (!text?.includes("codex-followup")) return [];
  const env = {};
  md.parse(text, env);
  return env.followups || [];
}
