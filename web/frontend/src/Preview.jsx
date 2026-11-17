// Files shown as what they are rather than as lines: Markdown rendered, and
// images, videos and sounds drawn or played by the browser.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { api } from "./api.js";
import { drawDiagrams } from "./diagrams.js";
import { ensureLanguage } from "./highlight.js";
import { blockAt, isMarkdown, renderMarkdown } from "./markdown.js";
import { usePathsVersion } from "./links.js";
import { AttachButton, Composer, ThreadList } from "./Threads.jsx";
import { cx, excerptOf, searchSeed } from "./util.js";
import { IconEye } from "./icons.jsx";
import { PdfView } from "./Pdf.jsx";

const SVG = "image/svg+xml";

// previewKind is what Preview toggles a file's lines to: "markdown", "svg" or "".
export const previewKind = (path) => (isMarkdown(path) ? "markdown" : /\.svg$/i.test(path) ? "svg" : "");

// asMedia is whether a listed file is shown as media in place of lines. An SVG
// is text as well, so it keeps its lines and previews instead.
export const asMedia = (entry) => !!entry?.media && entry.media !== SVG;

// MarkdownPreview shows a Markdown file's lines rendered. Images and links
// written relative to the file resolve inside the repository; a link to
// another file there opens it in dv. scope is where images are read from, the
// working tree when not given.
export function MarkdownPreview({ lines, path, side = "new", scope, onOpenFile }) {
  const [loaded, setLoaded] = useState(0);
  const image = useCallback((p) => api.mediaURL(p, scope), [scope]);
  const paths = usePathsVersion();
  const { html, waiting } = useMemo(() => renderMarkdown(lines.join("\n"), path, image), [lines, path, image, loaded, paths]);
  useEffect(() => {
    for (const lang of waiting) ensureLanguage(lang, () => setLoaded((n) => n + 1));
  }, [waiting]);

  const onClick = (e) => {
    const a = e.target.closest("a[data-anchor], a[data-path]");
    if (!a) return;
    // Left to the browser, a relative link would navigate dv's own page away.
    e.preventDefault();
    if (a.dataset.path) onOpenFile?.(a.dataset.path, Number(a.dataset.line) || 0);
    else e.currentTarget.querySelector("#" + CSS.escape("md-" + a.dataset.anchor))?.scrollIntoView({ block: "start" });
  };

  const ref = useRef(null);
  useLayoutEffect(() => drawDiagrams(ref.current));

  return <div className="md-preview" ref={ref} data-side={side} onClick={onClick} dangerouslySetInnerHTML={{ __html: html }} />;
}

// MarkdownDocument is a Markdown file rendered and open to the comments its
// lines take: selecting text comments on the blocks it covers, anchored to the
// lines they were written from, which is what is saved and what an agent is
// told. Every rendered block names the line it starts on, so the two line up.
// Without onStartComment it is the preview alone.
// toAgent is an agent's own edit, where the box sends a note to its session
// rather than leaving a comment in the review. See DiffBody.
export function MarkdownDocument({
  lines, path, side = "new", scope, onOpenFile, threads, composing, setComposing, onStartComment, onComment, onThreadAction, onAttach, onSearch, toAgent = false,
}) {
  const ref = useRef(null);
  // key -> { el: what it is drawn in, hold: the room kept for it inside a table or list }
  const slots = useRef(new Map());
  // A comment on the file itself is not on any block: its card shows it.
  const mine = (threads || []).filter((t) => t.startLine > 0 && (!t.side || t.side === side));
  const shown = [...mine.map((t) => ["t:" + t.id, t.endLine]), ...(composing?.side === side ? [["c", composing.end]] : [])];
  const slot = (key) => {
    if (!slots.current.has(key)) {
      const el = document.createElement("div");
      el.className = "row-threads md-threads";
      const ro = new ResizeObserver(() => fitHold(el));
      ro.observe(el);
      slots.current.set(key, { el, hold: null, ro });
    }
    return slots.current.get(key).el;
  };
  // Each goes under the block it is about. A layout effect, so it is in the
  // page before the composer's focus, which then scrolls it into view.
  useLayoutEffect(() => {
    const root = ref.current;
    if (!root) return;
    const want = new Set(shown.map(([key]) => key));
    for (const [key, s] of slots.current) {
      if (want.has(key)) continue;
      s.ro.disconnect();
      s.hold?.remove();
      s.el.remove();
      slots.current.delete(key);
    }
    // Those under one block are laid in the order shown, the composer last.
    // Only what is out of place moves: moving the composer takes its focus.
    const groups = new Map(); // block -> { parent, ref: what follows them, nodes }
    for (const [key, line] of shown) {
      const s = slots.current.get(key);
      const block = blockAt(root, line) || root.querySelector(".md-preview")?.lastElementChild;
      if (!block) continue;
      // Under the row or item itself: below the whole table or list could be screens away.
      let at = block.closest(".md-preview tr, .md-preview li");
      let parent, ref;
      if (at) {
        const tag = at.localName === "tr" ? "tr" : "div";
        if (s.hold?.localName !== tag) {
          s.hold?.remove();
          s.hold = document.createElement(tag);
          s.hold.className = "md-thread-hold";
          (tag === "tr" ? s.hold.appendChild(document.createElement("td")) : s.hold).append(s.el);
        }
        if (tag === "tr") {
          s.hold.firstChild.colSpan = [...at.cells].reduce((n, c) => n + c.colSpan, 0);
          [parent, ref] = [at.parentElement, nextOwn(at)];
        } else {
          // Before the item's own sublist, which has lines of its own.
          [parent, ref] = [at, [...at.children].find((c) => c.localName === "ul" || c.localName === "ol") || null];
        }
      } else {
        if (s.hold) {
          s.hold.remove();
          s.hold = null;
        }
        // After the whole quote a block belongs to, not inside it.
        at = block;
        while (at.parentElement && !at.parentElement.classList.contains("md-preview")) at = at.parentElement;
        [parent, ref] = [at.parentElement, nextOwn(at)];
      }
      if (!groups.has(at)) groups.set(at, { parent, ref, nodes: [] });
      groups.get(at).nodes.push(s.hold || s.el);
    }
    for (const { parent, ref, nodes } of groups.values()) {
      // From the end, so those already in order stay put.
      let next = ref;
      for (let i = nodes.length - 1; i >= 0; i--) {
        if (nodes[i].nextSibling !== next || nodes[i].parentNode !== parent) parent.insertBefore(nodes[i], next);
        next = nodes[i];
      }
    }
    for (const s of slots.current.values()) fitHold(s.el);
  });
  useEffect(() => () => slots.current.forEach((s) => s.ro.disconnect()), []);

  useEffect(() => {
    const root = ref.current;
    if (!root || !onStartComment) return;
    const onMouseUp = (e) => {
      if (e.detail === 2 || e.target.closest?.(".md-threads")) return;
      const sel = window.getSelection();
      if (!sel || sel.isCollapsed || !sel.rangeCount) return;
      const from = blockOf(root, sel.anchorNode);
      const to = blockOf(root, sel.focusNode);
      if (!from || !to) return;
      const [a, b] = [Number(from.dataset.src), Number(to.dataset.src)];
      onStartComment(side, Math.min(a, b), blockEnd(root, b < a ? from : to, lines.length), searchSeed(sel.toString()), sel.toString());
    };
    root.addEventListener("mouseup", onMouseUp);
    return () => root.removeEventListener("mouseup", onMouseUp);
  }, [onStartComment, side, lines.length]);

  const at = composing;
  return (
    <div className="md-commentable" ref={ref}>
      <MarkdownPreview lines={lines} path={path} side={side} scope={scope} onOpenFile={onOpenFile} />
      {shown.map(([key]) =>
        createPortal(
          <div className="thread-slot">
            {key === "c" ? (
              <Composer
                title={at.start === at.end ? `Line ${at.end}` : `Lines ${at.start}-${at.end}`}
                submitLabel={toAgent ? "Send" : "Comment"}
                autoFocus
                selected={at.selected}
                onSearch={onSearch}
                aside={
                  !toAgent &&
                  onAttach &&
                  ((body) => (
                    <AttachButton
                      onClick={(to) => {
                        // Words written go as a note, kept nowhere: Comment is
                        // what leaves one in the review.
                        const lines = { file: path, side, start: at.start, end: at.end, quote: at.quote, excerpt: excerptOf(at.excerpt, at.quote) };
                        onAttach(body ? { kind: "note", ...lines, body } : { kind: "lines", ...lines }, to);
                        setComposing(null);
                      }}
                      // What is written is the question.
                      ask={() => {
                        setComposing(null);
                        return { a: { kind: "lines", file: path, side, start: at.start, end: at.end, quote: at.quote }, question: body };
                      }}
                      what={body ? "this note" : "these lines"}
                    />
                  ))
                }
                onCancel={() => setComposing(null)}
                onSubmit={async (body) => {
                  await onComment({ file: path, side, startLine: at.start, endLine: at.end, quote: at.quote, excerpt: excerptOf(at.excerpt, at.quote), body });
                  setComposing(null);
                }}
              />
            ) : (
              <ThreadList
                threads={mine.filter((t) => "t:" + t.id === key)}
                onAction={onThreadAction}
                onAttach={onAttach && ((t, to) => onAttach({ kind: "thread", threadId: t.id }, to))}
              />
            )}
          </div>,
          slot(key),
          key,
        ),
      )}
    </div>
  );
}

// fitHold keeps a card's room in a table or list as tall as the card, which is
// laid over the page's full width rather than squeezed into a cell or indent.
function fitHold(el) {
  const box = el.parentElement;
  if (box?.closest(".md-thread-hold")) box.style.height = el.offsetHeight + "px";
}

// nextOwn is what follows a block in the document itself, past any cards under it.
function nextOwn(el) {
  let n = el.nextSibling;
  while (n?.classList?.contains("md-threads") || n?.classList?.contains("md-thread-hold")) n = n.nextSibling;
  return n;
}

// blockOf is the rendered block a node sits in: the innermost naming a line.
function blockOf(root, node) {
  const el = node?.nodeType === 3 ? node.parentElement : node;
  const block = el?.closest?.("[data-src]");
  return block && root.contains(block) ? block : null;
}

// blockEnd is the last line a block covers, up to where the next one starts.
function blockEnd(root, block, total) {
  const start = Number(block.dataset.src);
  for (const b of root.querySelectorAll(".md-preview [data-src]")) {
    const at = Number(b.dataset.src);
    if (at > start) return at - 1;
  }
  return total;
}

// SvgPreview draws an SVG from the lines in hand, so it shows exactly the
// version on screen, one Claude has not written yet included. As an <img> its
// scripts do not run.
export function SvgPreview({ oldLines, newLines }) {
  const src = (lines) => lines && "data:image/svg+xml;charset=utf-8," + encodeURIComponent(lines.join("\n"));
  return <MediaCompare type={SVG} oldSrc={src(oldLines)} newSrc={src(newLines)} />;
}

// MediaCompare is a changed media file: before on the left, after on the
// right, and one of them alone for a file added or deleted.
export function MediaCompare({ type, oldSrc, newSrc }) {
  const both = oldSrc && newSrc;
  return (
    <div className={cx("media-compare", both && "both")}>
      {oldSrc && <Media type={type} src={oldSrc} label={both && "Before"} />}
      {newSrc && <Media type={type} src={newSrc} label={both && "After"} />}
    </div>
  );
}

// Media draws an image, a PDF, or plays a video or sound, with its size underneath.
export function Media({ type, src, label }) {
  if (type === "application/pdf")
    return (
      <figure className="media media-pdf">
        {label && <figcaption className="media-label">{label}</figcaption>}
        <PdfView src={src} />
      </figure>
    );
  return <Playable type={type} src={src} label={label} />;
}

function Playable({ type, src, label }) {
  const kind = type.split("/")[0];
  const [info, setInfo] = useState("");
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    setInfo("");
    setFailed(false);
  }, [src]);

  const fail = () => setFailed(true);
  let el;
  if (failed) el = <div className="media-failed">The browser cannot show this {kind === "audio" ? "sound" : kind}.</div>;
  else if (kind === "image")
    el = <img src={src} alt="" onLoad={(e) => setInfo(`${e.target.naturalWidth} × ${e.target.naturalHeight}`)} onError={fail} />;
  else if (kind === "video")
    el = (
      <video
        src={src}
        controls
        preload="metadata"
        onLoadedMetadata={(e) => setInfo(`${e.target.videoWidth} × ${e.target.videoHeight}, ${duration(e.target.duration)}`)}
        onError={fail}
      />
    );
  else el = <audio src={src} controls preload="metadata" onLoadedMetadata={(e) => setInfo(duration(e.target.duration))} onError={fail} />;

  return (
    <figure className={cx("media", "media-" + kind)}>
      {label && <figcaption className="media-label">{label}</figcaption>}
      {el}
      {info && !failed && <figcaption className="media-info">{info}</figcaption>}
    </figure>
  );
}

function duration(s) {
  if (!Number.isFinite(s)) return "";
  const t = Math.round(s);
  const hms = [Math.floor(t / 3600), Math.floor(t / 60) % 60, t % 60];
  return (hms[0] ? [hms[0], String(hms[1]).padStart(2, "0")] : [hms[1]]).concat(String(hms[2]).padStart(2, "0")).join(":");
}

export function PreviewToggle({ on, onChange, kind }) {
  const what = kind === "svg" ? "image" : "Markdown";
  return (
    <button
      className={cx("btn", on && "on")}
      aria-pressed={on}
      onClick={() => onChange(!on)}
      title={on ? `Show the ${kind === "svg" ? "SVG" : "Markdown"} source` : `Show the ${what} rendered`}
    >
      <IconEye size={12} />
      <span className="btn-label">Preview</span>
    </button>
  );
}
