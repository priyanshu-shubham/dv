// Files shown as what they are rather than as lines: Markdown rendered, and
// images, videos and sounds drawn or played by the browser.
import { useCallback, useEffect, useMemo, useState } from "react";
import { api } from "./api.js";
import { ensureLanguage } from "./highlight.js";
import { isMarkdown, renderMarkdown } from "./markdown.js";
import { cx } from "./util.js";
import { IconEye } from "./icons.jsx";

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
  const { html, waiting } = useMemo(() => renderMarkdown(lines.join("\n"), path, image), [lines, path, image, loaded]);
  useEffect(() => {
    for (const lang of waiting) ensureLanguage(lang, () => setLoaded((n) => n + 1));
  }, [waiting]);

  const onClick = (e) => {
    const a = e.target.closest("a[data-anchor], a[data-path]");
    if (!a) return;
    // Left to the browser, a relative link would navigate dv's own page away.
    e.preventDefault();
    if (a.dataset.path) onOpenFile?.(a.dataset.path, 0);
    else e.currentTarget.querySelector("#" + CSS.escape("md-" + a.dataset.anchor))?.scrollIntoView({ block: "start" });
  };

  return <div className="md-preview" data-side={side} onClick={onClick} dangerouslySetInnerHTML={{ __html: html }} />;
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

// Media draws an image, or plays a video or sound, with its size underneath. A
// PDF opens in the browser's own viewer, which a phone's browser does not
// have, so there is always the way to it in a tab of its own.
export function Media({ type, src, label }) {
  if (type === "application/pdf")
    return (
      <figure className="media media-pdf">
        {label && <figcaption className="media-label">{label}</figcaption>}
        <iframe src={src} title="PDF" />
        <a className="media-info" href={src} target="_blank" rel="noopener noreferrer">
          Open in a new tab
        </a>
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
