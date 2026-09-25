import { useCallback, useEffect, useMemo, useState } from "react";
import { api } from "./api.js";
import { DiffBody } from "./FileDiff.jsx";
import { AttachButton, Composer, ThreadList } from "./Threads.jsx";
import { newLineFor } from "./hunks.js";
import { ensureLanguage } from "./highlight.js";
import { asMedia, MarkdownDocument, Media, previewKind, PreviewToggle, SvgPreview } from "./Preview.jsx";
import { cx, LRM, splitPath, statusLabel, statusLetter, useCopy } from "./util.js";
import { IconBack, IconComment, IconForward, IconSplit } from "./icons.jsx";

export const NO_EXPAND = {};
export const noop = () => {};

// commentsOn is the comments shown reading a file whole on one side. One on a
// removed line has no row of its own there, so it hangs under the line now
// standing in that line's place, as `changes` - the file's diff - has it.
export function commentsOn(threads, side, changes) {
  if (side === "old" || !changes) return threads.filter((t) => t.side === side);
  return threads.map((t) => (t.side === "old" ? { ...t, side: "new", endLine: newLineFor(changes, t.endLine) } : t));
}

// CodeView is Code mode's reading pane: one file, whole and read-only, with
// what the diff changed marked in the gutter. It renders through the diff's own
// rows, so commenting, selecting, go-to-definition and n/p work as they do there.
// A Markdown or SVG file can be read rendered instead, which `preview` remembers
// for all of them; an image, video or sound is only ever shown as itself.
// `media` is { type, stamp } for one outside the diff.
export default function CodeView({
  path, entry, fd, error, at, threads, wrap, reveal, hit, composing, setComposing,
  onComment, onThreadAction, onSymbol, onAttach, onSearch, onDiff, onBack, onForward, onBody, found, foundAt,
  scope, preview, onPreview, onOpenFile, media: plainMedia,
}) {
  const [selection, setSelection] = useState(null);
  const [, force] = useState(0);
  // A deleted file has nothing on the new side; it is read as it was.
  const side = entry?.status === "D" ? "old" : "new";
  const media = plainMedia || (asMedia(entry) ? { type: entry.media, stamp: entry.rev } : null);

  useEffect(() => {
    if (fd?.lang) ensureLanguage(fd.lang, () => force((n) => n + 1));
  }, [fd?.lang]);
  useEffect(() => setSelection(null), [path]);

  const startComment = useCallback(
    (s, start, end, selected) => {
      const src = s === "old" ? fd?.oldLines : fd?.newLines;
      setComposing({ path, side: s, start, end, quote: src ? src.slice(start - 1, end) : [], selected });
      setSelection(null);
    },
    [fd, path, setComposing],
  );

  const shown = useMemo(() => commentsOn(threads, side, fd), [threads, fd, side]);
  // A comment on the file itself rather than a line: what a picture, something
  // binary or a file too large to show can still take.
  const fileThreads = useMemo(() => shown.filter((t) => !t.startLine), [shown]);
  const lineThreads = useMemo(() => shown.filter((t) => t.startLine > 0), [shown]);
  const fileComposing = !!composing && !composing.start;

  const [dir, name] = splitPath(path);
  const [copied, copy] = useCopy(path);
  const kind = media ? "" : previewKind(path);
  const lines = side === "old" ? fd?.oldLines : fd?.newLines;
  const readable = fd && !fd.binary && !fd.tooLarge;
  const openDiff = useCallback((at) => onDiff({ path, ...at }), [onDiff, path]);

  return (
    <section className="file code-file" data-path={path} data-pending={(!fd && !error && !media) || undefined}>
      <header className="file-head">
        <button className="nav" onClick={onBack} disabled={!onBack} title="Back (Alt+Left)">
          <IconBack size={13} />
        </button>
        <button className="nav" onClick={onForward} disabled={!onForward} title="Forward (Alt+Right)">
          <IconForward size={13} />
        </button>
        {entry && (
          <span className={cx("badge", "st-" + statusLetter(entry))} title={(entry.untracked ? "untracked" : statusLabel[entry.status]) + " in this diff"}>
            {statusLetter(entry)}
          </span>
        )}
        <h3 className="file-path copy-path" title={`Copy the path, ${path}`} onClick={copy}>
          <span className="dir">
            {LRM}
            {dir}
            {LRM}
          </span>
          <span className="name">{name}</span>
          {copied && <span className="copied">copied</span>}
        </h3>
        {at && (
          <span className="at" title={`The ${side} side of the comparison, not the working tree`}>
            {at}
          </span>
        )}
        <span className="spacer" />
        {entry && (
          <span className="stat">
            <span className="add">+{entry.additions}</span>
            <span className="del">-{entry.deletions}</span>
          </span>
        )}
        {/* After the count, whose fixed column would otherwise open a gap beside it. */}
        {kind && <PreviewToggle kind={kind} on={preview} onChange={onPreview} />}
        {entry && (
          <button className="view-file" onClick={() => onDiff()} title="Show this file's diff (m)">
            <IconSplit size={12} />
            <span className="btn-label">Diff</span>
          </button>
        )}
        <button
          className="view-file"
          onClick={() => setComposing({ path, side, start: 0, end: 0, quote: [] })}
          title="Comment on the file, whatever is in it"
        >
          <IconComment size={12} />
          <span className="btn-label">Comment</span>
        </button>
        <AttachButton onClick={(to) => onAttach({ kind: "file", file: path }, to)} ask={() => ({ a: { kind: "file", file: path } })} what="this file" />
      </header>
      {(fileThreads.length > 0 || fileComposing) && (
        <div className="row-threads file-threads">
          <div className="thread-slot">
            <ThreadList
              threads={fileThreads}
              onAction={onThreadAction}
              onAttach={onAttach && ((t, to) => onAttach({ kind: "thread", threadId: t.id }, to))}
            />
            {fileComposing && (
              <Composer
                title="The whole file"
                autoFocus
                onCancel={() => setComposing(null)}
                onSubmit={async (body) => {
                  await onComment({ file: path, side, startLine: 0, endLine: 0, quote: [], body });
                  setComposing(null);
                }}
              />
            )}
          </div>
        </div>
      )}
      <div className={cx("file-body", wrap && "wrap")}>
        {media && <Media type={media.type} src={api.mediaURL(path, scope, side, media.stamp)} />}
        {!media && error && <div className="file-note error">{error}</div>}
        {!media && !fd && !error && <div className="file-note">Loading...</div>}
        {!media && fd?.binary && <div className="file-note">Binary file - not shown.</div>}
        {!media && fd?.tooLarge && <div className="file-note">File is too large to display.</div>}
        {readable && kind === "markdown" && preview && (
          <MarkdownDocument
            lines={lines}
            path={path}
            side={side}
            scope={scope}
            onOpenFile={onOpenFile}
            threads={lineThreads}
            composing={fileComposing ? null : composing}
            setComposing={setComposing}
            onStartComment={startComment}
            onComment={onComment}
            onThreadAction={onThreadAction}
            onAttach={onAttach}
            onSearch={onSearch}
          />
        )}
        {readable && kind === "svg" && preview && <SvgPreview newLines={lines} />}
        {readable && !media && !(kind && preview) && (
          <DiffBody
            view="code"
            side={side}
            fd={fd}
            contextLines={0}
            expanded={NO_EXPAND}
            onExpand={noop}
            threads={lineThreads}
            selection={selection}
            setSelection={setSelection}
            composing={fileComposing ? null : composing}
            setComposing={setComposing}
            onStartComment={startComment}
            onComment={onComment}
            onThreadAction={onThreadAction}
            onSymbol={onSymbol}
            onAttach={onAttach}
            onSearch={onSearch}
            onBody={onBody}
            found={found}
            foundAt={foundAt}
            path={path}
            wrap={wrap}
            reveal={reveal}
            hit={hit}
            onOpenDiff={entry ? openDiff : undefined}
          />
        )}
      </div>
    </section>
  );
}
