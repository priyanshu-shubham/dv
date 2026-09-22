// Markdown files rendered for reading, as a repository host would show them.
// A README leans on inline HTML (centred logos, badges, <details>), so HTML is
// let through - but only what survives a whitelist, since the file is not
// necessarily trusted and the page can drive Claude.
import MarkdownIt from "markdown-it";
import hljs from "highlight.js/lib/common";
import { linkPaths } from "./links.js";

export const isMarkdown = (path) => /\.(md|markdown|mdown|mkd)$/i.test(path);

const TAGS = new Set(
  ("a abbr b blockquote br caption cite code dd del details div dfn dl dt em figcaption figure " +
    "h1 h2 h3 h4 h5 h6 hr i img ins kbd li mark ol p picture pre q rp rt ruby s samp small span " +
    "strike strong sub summary sup table tbody td tfoot th thead tr tt u ul var").split(" "),
);
// Removed with what is inside them; any other unknown tag gives way to its children.
const DROP = new Set(
  ("script style iframe object embed template noscript svg math form input textarea select button " +
    "link meta base frame frameset source video audio canvas dialog").split(" "),
);
const ATTRS = new Set("href src alt title width height align colspan rowspan open start reversed class data-src".split(" "));

let waiting = null;
const md = linkPaths(new MarkdownIt({
  html: true,
  linkify: true,
  highlight: (code, lang) => {
    if (!lang) return "";
    if (!hljs.getLanguage(lang)) {
      waiting.add(lang);
      return "";
    }
    try {
      return hljs.highlight(code, { language: lang, ignoreIllegals: true }).value;
    } catch {
      return "";
    }
  },
}));
// Each block names the line it starts on, so a jump to a line, or a switch
// from the source, lands on the block that holds it.
md.core.ruler.push("source_lines", (state) => {
  for (const t of state.tokens) if (t.map && t.nesting !== -1) t.attrSet("data-src", String(t.map[0] + 1));
});

// renderMarkdown returns the page's HTML for the file at `path`, and the code
// block languages whose grammars had not loaded yet. `image` maps a path in the
// repository to the URL it is served from.
export function renderMarkdown(text, path, image) {
  waiting = new Set();
  // Front matter would read as a rule and a heading; the fence keeps its lines.
  const src = text.replace(/^---\r?\n([\s\S]*?\r?\n)---(\r?\n|$)/, "```yaml\n$1```$2");
  // DOMParser's document is inert: nothing in it loads or runs while it is cleaned.
  const doc = new DOMParser().parseFromString(md.render(src), "text/html");
  const dir = path.includes("/") ? path.slice(0, path.lastIndexOf("/") + 1) : "";
  const slugs = new Map();

  for (const el of doc.body.querySelectorAll("*")) {
    const tag = el.localName;
    if (DROP.has(tag)) {
      el.remove();
      continue;
    }
    if (!TAGS.has(tag)) {
      el.replaceWith(...el.childNodes);
      continue;
    }
    for (const { name, value } of [...el.attributes]) {
      if (!ATTRS.has(name)) el.removeAttribute(name);
      else if (name === "class") {
        // Only highlighting's: the page's own classes carry behaviour.
        const kept = value.split(/\s+/).filter((c) => /^(hljs|language-)/.test(c));
        if (kept.length) el.setAttribute("class", kept.join(" "));
        else el.removeAttribute("class");
      } else if ((name === "href" || name === "src") && !safeURL(value)) el.removeAttribute(name);
    }

    if (tag === "img") {
      const local = repoPath(dir, el.getAttribute("src") || "#");
      if (local) el.setAttribute("src", image(local));
      el.setAttribute("loading", "lazy");
    } else if (tag === "a") {
      const href = el.getAttribute("href");
      if (!href) continue;
      const local = repoPath(dir, href);
      if (href.startsWith("#")) el.dataset.anchor = slugOf(decode(href.slice(1)));
      else if (local) {
        el.dataset.path = local;
        // GitHub's way of naming a line, as a path in the text is linked.
        const line = /^#L(\d+)/.exec(new URL(href, "http://repo/").hash);
        if (line) el.dataset.line = line[1];
      } else {
        el.setAttribute("target", "_blank");
        el.setAttribute("rel", "noopener noreferrer");
      }
    } else if (/^h[1-6]$/.test(tag)) {
      // Prefixed, so a heading cannot take the id of something on the page.
      const base = slugOf(el.textContent);
      const n = slugs.get(base) || 0;
      slugs.set(base, n + 1);
      el.id = "md-" + (n ? `${base}-${n}` : base);
    } else if (tag === "li") {
      taskBox(doc, el);
    }
  }
  const out = { html: doc.body.innerHTML, waiting: [...waiting] };
  waiting = null;
  return out;
}

// safeURL refuses every scheme but the web's and mail's. Browsers skip control
// characters and spaces inside a scheme, so those are dropped before looking.
function safeURL(v) {
  const m = /^([^/?#]*?):/.exec(v.replace(/[\u0000- \u007f]/g, ""));
  return !m || /^(https?|mailto)$/i.test(m[1]);
}

// repoPath resolves a link written in the file at `dir` to a path in the
// repository, or "" for one that leads elsewhere. A leading slash is the
// repository's root, as on GitHub.
function repoPath(dir, href) {
  if (/^([a-z][\w+.-]*:|\/\/|#)/i.test(href)) return "";
  return decode(new URL(href, "http://repo/" + dir).pathname.slice(1));
}

function decode(s) {
  try {
    return decodeURIComponent(s);
  } catch {
    return s;
  }
}

// slugOf is GitHub's anchor for a heading: lower case, punctuation gone,
// spaces as hyphens.
const slugOf = (s) =>
  s
    .trim()
    .toLowerCase()
    .replace(/[^\p{L}\p{N}\s_-]/gu, "")
    .replace(/\s/g, "-");

// taskBox draws "- [ ] item" as a checkbox, which Markdown itself leaves as text.
function taskBox(doc, li) {
  const holder = li.firstElementChild?.localName === "p" ? li.firstElementChild : li;
  const first = holder.firstChild;
  const m = first?.nodeType === 3 && /^\[( |x|X)\]\s/.exec(first.data);
  if (!m) return;
  first.data = first.data.slice(m[0].length);
  // Attributes, not properties: the markup is what survives into the page.
  const box = doc.createElement("input");
  box.setAttribute("type", "checkbox");
  box.setAttribute("disabled", "");
  if (m[1] !== " ") box.setAttribute("checked", "");
  holder.prepend(box);
  li.classList.add("md-task");
}

// blockAt is the rendered block holding a source line: the last to start at
// or before it.
export function blockAt(root, line) {
  let best = null;
  for (const b of root.querySelectorAll(".md-preview [data-src]")) {
    if (Number(b.dataset.src) > line) break;
    best = b;
  }
  return best;
}
