// Files a reply shows in place, written as dv tells agents to: ![title](path)
// on a line of its own, the path absolute, from ~, or from the session's
// folder. A web address stays a picture, as Markdown has it.
import { api } from "./api.js";

// splitShown cuts text into its Markdown and the files it shows, in order:
// { text } and { title, path, rest }, rest being the address's ?query or
// #fragment. Only a paragraph of nothing but such files is taken out; one
// inside a sentence, a list or a quote stays in the text.
export function splitShown(md, text) {
  if (!text?.includes("![")) return [{ text }];
  const lines = text.split(/\r?\n/);
  const tokens = md.parse(text, {});
  const out = [];
  let at = 0;
  for (let i = 0; i < tokens.length; i++) {
    const t = tokens[i];
    if (t.type !== "paragraph_open" || t.level !== 0 || !t.map) continue;
    const files = filesIn(tokens[i + 1]);
    if (!files) continue;
    const [start, end] = t.map;
    pushText(out, lines.slice(at, start));
    out.push(...files);
    at = end;
  }
  if (!out.length) return [{ text }];
  pushText(out, lines.slice(at));
  return out;
}

function pushText(out, lines) {
  const text = lines.join("\n");
  if (text.trim()) out.push({ text });
}

function filesIn(inline) {
  const files = [];
  for (const c of inline?.children || []) {
    if (c.type === "image") {
      const file = localFile(c.attrGet("src"));
      if (!file) return null;
      files.push({ title: c.content, ...file });
    } else if (c.type !== "softbreak" && !(c.type === "text" && !c.content.trim())) return null;
  }
  return files.length ? files : null;
}

// inlineFiles has md write a file shown inside a sentence, where it cannot be
// drawn, as a link to it by its title, opened in a tab of its own. Rendered
// with env.root, the session's folder; without it, a relative one is its title.
export function inlineFiles(md) {
  const image = md.renderer.rules.image;
  md.renderer.rules.image = (tokens, i, opts, env, self) => {
    const t = tokens[i];
    const file = localFile(t.attrGet("src"));
    if (!file) return image(tokens, i, opts, env, self);
    const title = md.utils.escapeHtml(t.content || file.path);
    const path = absolute(file.path, env.root || "");
    if (!env.root && path !== file.path) return title;
    const href = md.utils.escapeHtml(api.viewURL(path) + file.rest);
    return `<a href="${href}" target="_blank" rel="noopener" title="${md.utils.escapeHtml(file.path)}">${title}</a>`;
  };
  return md;
}

// localFile reads a src as a file on disk, or null for an address elsewhere.
function localFile(src) {
  const [, raw, rest] = /^([^?#]*)(.*)$/.exec(src || "");
  if (!raw || (/^([a-z][\w+.-]*:|\/\/)/i.test(raw) && !/^[a-z]:[\\/]/i.test(raw))) return null;
  try {
    return { path: decodeURI(raw), rest };
  } catch {
    return { path: raw, rest };
  }
}

// absolute is the file's path with a relative one put under root.
export function absolute(path, root) {
  if (/^(\/|~\/|[a-z]:[\\/])/i.test(path)) return path;
  return root.replace(/[\\/]+$/, "") + "/" + path.replace(/^\.\//, "");
}

// inside is path from root if it is under it, else "".
export function inside(path, root) {
  const r = root.replace(/\\/g, "/").replace(/\/+$/, "") + "/";
  const p = path.replace(/\\/g, "/");
  return root && p.startsWith(r) ? p.slice(r.length) : "";
}

const KINDS = [
  [/\.html?$/i, "page"],
  [/\.(png|jpe?g|gif|webp|avif|bmp|ico|svg)$/i, "image"],
  [/\.(mp4|m4v|webm|mov|ogv)$/i, "video"],
  [/\.(mp3|wav|ogg|oga|opus|m4a|flac)$/i, "audio"],
  [/\.pdf$/i, "pdf"],
];

export const kindOf = (path) => KINDS.find(([re]) => re.test(path))?.[1] || "file";
