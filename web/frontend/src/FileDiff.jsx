import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { buildBlocks, codeLines, grow, pairRows, revealFor, stripOpener, unifiedRows } from "./hunks.js";
import { diffWords, spansToRanges } from "./worddiff.js";
import { applyRanges, ensureLanguage, escapeHtml, highlightLines, langReady } from "./highlight.js";
import { charWidth, cx, isMac, LRM, PHONE, searchSeed, splitPath, statusLabel, statusLetter, useCopy, useElementWidth, useMedia, visualLength } from "./util.js";
import { AttachButton, ThreadList, Composer } from "./Threads.jsx";
import { api } from "./api.js";
import { asMedia, MarkdownDocument, MediaCompare, previewKind, PreviewToggle, SvgPreview } from "./Preview.jsx";
import { IconCheck, IconChevron, IconChevronDown, IconComment, IconFile } from "./icons.jsx";

// One file's worth of diff. The parent mounts these lazily: `fd` arrives only
// once the section has been scrolled near, so a 500-file diff still opens
// instantly. Memoised, and every prop is either a primitive or a stable
// identity: a large diff renders tens of thousands of rows, and re-rendering
// them because some unrelated state moved is what makes scrolling stutter.
function FileDiff({
  entry, fd, loading, error, view, contextLines, wrap, threads, collapsed,
  onToggleCollapse, viewed, onToggleViewed, onComment, onThreadAction, onSymbol,
  composing, setComposing, onAttach, onSearch, onView, reveal, generatedHidden, onShowGenerated,
  onBody, found, foundAt, foundHead, scope, onOpenFile,
}) {
  const [expanded, setExpanded] = useState({});
  const [selection, setSelection] = useState(null); // { side, start, end }
  const [, forceRender] = useState(0);
  // A Markdown file can be read as it comes out of the change, rendered, and an
  // SVG drawn before and after. Other media only ever shows as itself.
  const [preview, setPreview] = useState(false);
  const media = asMedia(entry);
  const kind = previewKind(entry.path);
  const previewable = kind === "svg" || (kind === "markdown" && entry.status !== "D");
  const previewing = previewable && preview;

  useEffect(() => {
    if (fd?.lang) ensureLanguage(fd.lang, () => forceRender((n) => n + 1));
  }, [fd?.lang]);

  const [dir, name] = splitPath(entry.path);
  const [copied, copy] = useCopy(entry.path);
  const headAt = foundAt?.side === "head" ? foundAt.start : -1;
  const openThreads = threads.filter((t) => !t.resolved).length;
  // A comment on the file itself rather than a line, which anything can take:
  // a picture, something binary, a file too large to show.
  const fileThreads = useMemo(() => threads.filter((t) => !t.startLine), [threads]);
  const lineThreads = useMemo(() => threads.filter((t) => t.startLine > 0), [threads]);
  const fileComposing = !!composing && !composing.start;
  // An added or deleted file has one side; split would leave half of it blank.
  const oneSided = entry.status === "A" || entry.status === "D";

  const expand = useCallback((id, amount) => {
    setExpanded((e) => ({ ...e, [id]: grow(e[id], amount) }));
  }, []);

  const startComment = useCallback(
    (side, start, end, selected) => {
      const src = side === "old" ? fd?.oldLines : fd?.newLines;
      const quote = src ? src.slice(start - 1, end) : [];
      setComposing({ path: entry.path, side, start, end, quote, selected });
      setSelection(null);
    },
    [fd, entry.path, setComposing],
  );

  // A body that is on its way; stepping between changes has to be able to
  // find it before it has any rows.
  const pending = !collapsed && !generatedHidden && !media && !fd && !error;

  return (
    <section
      className="file"
      id={`file-${cssId(entry.path)}`}
      data-path={entry.path}
      data-pending={pending || undefined}
    >
      <header className="file-head">
        <button className="file-collapse" onClick={() => onToggleCollapse(entry.path)} title={collapsed ? "Expand" : "Collapse"}>
          {collapsed ? <IconChevron /> : <IconChevronDown />}
        </button>
        <span className={cx("badge", "st-" + statusLetter(entry))} title={entry.untracked ? "untracked" : statusLabel[entry.status]}>
          {statusLetter(entry)}
        </span>
        <h3 className="file-path copy-path" title={`Copy the path, ${entry.path}`} onClick={copy}>
          <span className="dir">
            {LRM}
            <Found text={dir} ranges={foundHead?.head} at={headAt} />
            {LRM}
          </span>
          <span className="name">
            <Found text={name} offset={dir.length} ranges={foundHead?.head} at={headAt} />
          </span>
          {copied && <span className="copied">copied</span>}
        </h3>
        {entry.oldPath && (
          <span className="renamed-from">
            from <Found text={entry.oldPath} ranges={foundHead?.from} at={foundAt?.side === "from" ? foundAt.start : -1} />
          </span>
        )}
        {entry.generated && <span className="tag-generated" title="Machine-written; see the README for what counts">generated</span>}
        <span className="spacer" />
        {openThreads > 0 && (
          <span className="file-comments">
            <IconComment size={12} /> {openThreads}
            <span className="btn-label">open</span>
          </span>
        )}
        {!media && (
          <span className="stat">
            <span className="add">+{entry.additions}</span>
            <span className="del">-{entry.deletions}</span>
          </span>
        )}
        {previewable &&<PreviewToggle kind={kind} on={preview} onChange={setPreview} />}
        <button
          className="view-file"
          onClick={() => setComposing({ path: entry.path, side: "new", start: 0, end: 0, quote: [] })}
          title="Comment on the file, whatever is in it"
        >
          <IconComment size={12} />
          <span className="btn-label">Comment</span>
        </button>
        <button
          className="view-file"
          onClick={() => onView(entry.path)}
          title={entry.status === "D" ? "View the file as it was before it was deleted (f)" : "View the whole file (f)"}
        >
          <IconFile size={12} />
          <span className="btn-label">File</span>
        </button>
        <AttachButton onClick={(to) => onAttach({ kind: "file", file: entry.path }, to)} what="this file" />
        <button
          className={cx("btn", "outline", "viewed", viewed && "on")}
          aria-pressed={viewed}
          onClick={() => onToggleViewed(entry.path)}
          title="Mark the file viewed (v)"
        >
          <IconCheck size={12} />
          <span className="btn-label">Viewed</span>
        </button>
      </header>

      {!collapsed && (fileThreads.length > 0 || fileComposing) && (
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
                  await onComment({ file: entry.path, side: "new", startLine: 0, endLine: 0, quote: [], body });
                  setComposing(null);
                }}
              />
            )}
          </div>
        </div>
      )}

      {!collapsed && generatedHidden && (
        <div className="file-body">
          <div className="file-note skipped">
            <span>
              Generated file - diff not shown.{" "}
              <span className="dim">
                {entry.additions + entry.deletions} line{entry.additions + entry.deletions === 1 ? "" : "s"} changed
              </span>
            </span>
            <button className="ghost" onClick={onShowGenerated}>
              Show it anyway
            </button>
          </div>
        </div>
      )}

      {!collapsed && !generatedHidden && media && (
        <div className="file-body">
          <MediaCompare
            type={entry.media}
            oldSrc={entry.status !== "A" && api.mediaURL(entry.oldPath || entry.path, scope, "old", entry.rev)}
            newSrc={entry.status !== "D" && api.mediaURL(entry.path, scope, "new", entry.rev)}
          />
        </div>
      )}

      {!collapsed && !generatedHidden && !media && (
        <div className={cx("file-body", wrap && "wrap")}>
          {loading && <div className="file-note">Loading...</div>}
          {error && <div className="file-note error">{error}</div>}
          {fd?.binary && <div className="file-note">Binary file - not shown.</div>}
          {fd?.tooLarge && <div className="file-note">File is too large to display.</div>}
          {fd && !fd.binary && !fd.tooLarge && previewing && kind === "markdown" && (
            <MarkdownDocument
              lines={fd.newLines}
              path={entry.path}
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
          {fd && !fd.binary && !fd.tooLarge && previewing && kind === "svg" && (
            <SvgPreview oldLines={entry.status !== "A" && fd.oldLines} newLines={entry.status !== "D" && fd.newLines} />
          )}
          {fd && !fd.binary && !fd.tooLarge && !previewing && (
            <DiffBody
              fd={fd}
              view={oneSided && view === "split" ? "unified" : view}
              oneNumber={oneSided && view === "split"}
              contextLines={contextLines}
              expanded={expanded}
              onExpand={expand}
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
              path={entry.path}
              wrap={wrap}
              reveal={reveal}
              onBody={onBody}
              found={found}
              foundAt={foundAt}
            />
          )}
        </div>
      )}
    </section>
  );
}

export default memo(FileDiff);

// Found draws a piece of a header's text with find in page's matches marked,
// the way a code line's are. ranges are offsets into the whole string, which
// starts `offset` before this piece; `at` is the start of the current match.
function Found({ text, offset = 0, ranges, at = -1 }) {
  if (!ranges) return text;
  const out = [];
  let pos = 0;
  for (const [start, end] of ranges) {
    const a = Math.max(start - offset, 0);
    const b = Math.min(end - offset, text.length);
    if (b <= a) continue;
    if (a > pos) out.push(text.slice(pos, a));
    out.push(
      <span key={a} className={cx("fm", start === at && "fm-on")}>
        {text.slice(a, b)}
      </span>,
    );
    pos = b;
  }
  if (pos < text.length) out.push(text.slice(pos));
  return out;
}

export const cssId = (p) => p.replace(/[^A-Za-z0-9_-]/g, "_");

// Must match the diff's line-height in styles.css; it is only a first guess at
// a skipped block's height, which the browser replaces once it has rendered it.
const ROW_HEIGHT = 20;

// The code cell's +/- slot and its padding-right, from styles.css.
const SIGN_PX = 12;
const TRAIL_PX = 16;
const TAP_SLOP_PX = 6;
const GLIDE_DECAY = 0.996; // of a fling's speed, per millisecond

// Rows are rendered in runs no longer than this, so virtualisation has blocks to
// let go of: Code mode's every line, and a diff's long hunks - a new file is one.
// Short, because what is mounted is what a sideways scroll moves.
const ROW_BLOCK = 60;

// Sideways shifts, a rule per file that has one. A rule rather than a variable
// set on the file: a variable is inherited, so changing it restyled every
// element in the file, each token of each line, on every step of a scroll.
const shiftSheet = new CSSStyleSheet();
document.adoptedStyleSheets = [...document.adoptedStyleSheets, shiftSheet];
let shifts = 0;

// chunk cuts a diff's long runs of rows. Split view cuts only at unchanged
// lines, which keeps a deletion and the addition it pairs with in one block.
function chunk(blocks, split) {
  const out = [];
  for (const b of blocks) {
    if (b.kind !== "rows" || b.lines.length <= ROW_BLOCK) {
      out.push(b);
      continue;
    }
    let start = 0;
    for (let i = ROW_BLOCK; i < b.lines.length; i++) {
      if (i - start >= ROW_BLOCK && (!split || b.lines[i].t === "e")) {
        out.push({ kind: "rows", lines: b.lines.slice(start, i) });
        start = i;
      }
    }
    out.push({ kind: "rows", lines: b.lines.slice(start) });
  }
  return out;
}

// DiffBody renders a file's rows. view "code" is Code mode: just `side` of the
// file, whole, with changes marked in the gutter. drawAll keeps every row
// mounted, for a request's preview, which the browser's own find searches. `unknown` is a diff
// whose unchanged lines were never kept, so its gaps say so and do not open.
// onBody hears the rows it lays out, which find in page searches; `found` marks
// the matches on them, by "side:line", and `foundAt` is the one the find bar is on.
// oneNumber is for a file with one side shown in one column among split ones:
// its gutter keeps to the one number a split side has, so its code lines up
// with theirs.
// toAgent is for an agent's own edit, where what you write is for it: the box
// sends a note to its session rather than leaving a comment in the review.
export function DiffBody({
  fd, view, oneNumber, side, contextLines, expanded, onExpand, threads, selection, setSelection,
  composing, setComposing, onStartComment, onComment, onThreadAction, onSymbol, onAttach, onSearch, path, wrap,
  reveal, hit = 0, drawAll = false, unknown = false, toAgent = false, onBody, found, foundAt, barsIn,
}) {
  // Where comments hang, which stay in view whatever the context setting.
  const anchors = useMemo(() => {
    const out = threads.map((t) => ({ side: t.side, start: t.startLine, end: t.endLine }));
    if (composing) out.push({ side: composing.side, start: composing.start, end: composing.end });
    return out;
  }, [threads, composing]);
  const blocks = useMemo(() => {
    if (view !== "code") return chunk(buildBlocks(fd, contextLines, expanded, anchors), view === "split");
    const lines = codeLines(fd, side);
    const out = [];
    for (let i = 0; i < lines.length; i += ROW_BLOCK) out.push({ kind: "rows", lines: lines.slice(i, i + ROW_BLOCK) });
    return out;
  }, [fd, view, side, contextLines, expanded, anchors]);
  useEffect(() => {
    if (!onBody) return;
    onBody(path, { blocks, view, fd });
    return () => onBody(path, null);
  }, [onBody, path, blocks, view, fd]);
  const ready = langReady(fd.lang);
  const oldHtml = useMemo(() => highlightLines(path + " old", fd.oldLines, fd.lang), [path, fd, ready]);
  const newHtml = useMemo(() => highlightLines(path + " new", fd.newLines, fd.lang), [path, fd, ready]);
  const html = useMemo(() => ({ old: oldHtml, new: newHtml }), [oldHtml, newHtml]);
  const revealScope = useCallback(
    (scope) => {
      for (const [id, amount] of revealFor(fd, blocks, scope)) onExpand(id, amount);
    },
    [fd, blocks, onExpand],
  );

  // Threads keyed by the row they hang under, so rendering stays a lookup.
  const byAnchor = useMemo(() => {
    const map = new Map();
    for (const t of threads) {
      const key = `${t.side}:${t.endLine}`;
      if (!map.has(key)) map.set(key, []);
      map.get(key).push(t);
    }
    return map;
  }, [threads]);

  // Dragging down the line-number gutter selects a range to comment on; the
  // mouseup listener lives on the window so releasing outside still commits.
  const rootRef = useRef(null);
  const barOld = useRef(null);
  const barNew = useRef(null);
  const drag = useRef(null);
  const onGutterDown = useCallback(
    (side, lineNo) => (e) => {
      if (e.button !== 0) return;
      e.preventDefault();
      drag.current = { side, anchor: lineNo };
      setSelection({ side, start: lineNo, end: lineNo });
    },
    [setSelection],
  );
  const onGutterEnter = useCallback(
    (side, lineNo) => () => {
      const d = drag.current;
      if (!d || d.side !== side) return;
      setSelection({ side, start: Math.min(d.anchor, lineNo), end: Math.max(d.anchor, lineNo) });
    },
    [setSelection],
  );
  useEffect(() => {
    const up = () => {
      if (!drag.current) return;
      drag.current = null;
      setSelection((sel) => {
        if (sel) onStartComment(sel.side, sel.start, sel.end);
        return sel;
      });
    };
    window.addEventListener("mouseup", up);
    return () => window.removeEventListener("mouseup", up);
  }, [onStartComment, setSelection]);

  // Selecting code opens the composer for the lines the selection covers, so
  // commenting is just "highlight the thing you want to talk about". This runs
  // on the diff itself rather than the window so it sees the event before the
  // gutter-drag handler above clears its state.
  useEffect(() => {
    const root = rootRef.current;
    if (!root) return;
    const commentOnSelection = () => {
      const sel = window.getSelection();
      if (!sel || sel.isCollapsed || !sel.rangeCount) return;
      const from = cellOf(sel.anchorNode);
      const to = cellOf(sel.focusNode);
      if (!from || !to || !root.contains(from) || !root.contains(to)) return;
      // A selection dragged across both columns has no single anchor line, so
      // it is left alone rather than guessed at.
      if (from.dataset.side !== to.dataset.side) return;

      const a = Number(from.dataset.line);
      const b = Number(to.dataset.line);
      onStartComment(from.dataset.side, Math.min(a, b), Math.max(a, b), searchSeed(sel.toString()));
    };
    const onMouseUp = (e) => {
      if (drag.current) return; // the gutter drag has already claimed this
      if (e.detail === 2) return; // a double-clicked word is to copy, not to comment on
      if (e.target.closest?.(".row-threads")) return; // selecting inside a comment
      commentOnSelection();
    };
    // A touch selection has no mouseup: its handles are dragged until they
    // rest, and a double tap is a search, not a selection to comment on.
    let settle = 0;
    let lastTap = null;
    // The browser selects in DOM order, which in split runs through both
    // columns of every row, so only the column pressed in stays selectable.
    const onPointerDown = (e) => {
      if (e.button === 0) root.dataset.selecting = e.target.closest?.(".cell")?.dataset.side || "";
    };
    const onSelection = () => {
      clearTimeout(settle);
      if (lastPointer === "touch") settle = setTimeout(commentOnSelection, SELECTION_SETTLE_MS);
    };
    const onPointerUp = (e) => {
      if (e.pointerType !== "touch" || !e.target.closest?.(".code")) return;
      const now = performance.now();
      if (lastTap && now - lastTap.at < DOUBLE_TAP_MS && Math.hypot(e.clientX - lastTap.x, e.clientY - lastTap.y) < 24) {
        lastTap = null;
        const word = identifierAt(e.clientX, e.clientY);
        if (!word) return;
        clearTimeout(settle);
        window.getSelection()?.removeAllRanges();
        onSymbol?.(word, path);
      } else {
        lastTap = { at: now, x: e.clientX, y: e.clientY };
      }
    };
    root.addEventListener("pointerdown", onPointerDown);
    root.addEventListener("mouseup", onMouseUp);
    root.addEventListener("pointerup", onPointerUp);
    document.addEventListener("selectionchange", onSelection);
    return () => {
      clearTimeout(settle);
      root.removeEventListener("pointerdown", onPointerDown);
      root.removeEventListener("mouseup", onMouseUp);
      root.removeEventListener("pointerup", onPointerUp);
      document.removeEventListener("selectionchange", onSelection);
    };
  }, [onStartComment, onSymbol, path]);

  // The row blocks below are memoised on this object, so it must be stable
  // across renders that changed nothing they depend on.
  const ctx = useMemo(
    () => ({
      fd, oldHtml, newHtml, byAnchor, selection, composing, setComposing,
      onGutterDown, onGutterEnter, onComment, onThreadAction, onSymbol, onAttach, onSearch, path, hit,
      found, foundAt, oneNumber, toAgent,
    }),
    [
      fd, oldHtml, newHtml, byAnchor, selection, composing, setComposing,
      onGutterDown, onGutterEnter, onComment, onThreadAction, onSymbol, onAttach, onSearch, path, hit,
      found, foundAt, oneNumber, toAgent,
    ],
  );

  // Each side scrolls on its own. Letting the widest line stretch the whole file
  // drags both columns along and leaves them unbalanced, so instead the columns
  // stay at an even half each and the code inside is shifted by a CSS variable,
  // driven by one scrollbar per side pinned to the bottom of the view.
  const widthPx = useElementWidth(rootRef);
  const content = useMemo(() => {
    const cw = charWidth();
    const widest = (lines) => {
      let n = 0;
      for (const l of lines) {
        const v = visualLength(l);
        if (v > n) n = v;
      }
      return n;
    };
    const oldCols = widest(fd.oldLines);
    const newCols = widest(fd.newLines);
    const px = (cols) => Math.ceil(cols * cw) + SIGN_PX + TRAIL_PX;
    return { old: px(oldCols), new: px(newCols), both: px(Math.max(oldCols, newCols)) };
  }, [fd]);

  const split = view === "split";
  // What the gutter occupies: one line number in split and code, two in
  // unified; on a phone, one number and no room for the "+".
  const phone = useMedia(PHONE);
  const gutter = phone ? 32 : view === "unified" ? 96 : 60;

  // What a rendered row is actually as wide as, gutter included, reported by
  // the blocks on screen. It only ever grows, so the scroll range does not
  // shrink out from under a reader who has already scrolled into it.
  const [measured, setMeasured] = useState({ old: 0, new: 0 });
  const onMeasure = useCallback((o, n) => {
    setMeasured((m) => (o > m.old || n > m.new ? { old: Math.max(m.old, o), new: Math.max(m.new, n) } : m));
  }, []);
  useEffect(() => setMeasured({ old: 0, new: 0 }), [split, wrap, path]);

  // The estimate covers the lines that are not mounted; the measurement covers
  // the ones that are. Whichever is larger is the honest width.
  const paneWidth = split ? widthPx / 2 : widthPx;
  // Code mode shows one side, so a long line only the other side has must not widen it.
  const single = view === "code" ? content[side === "old" ? "old" : "new"] : content.both;
  const need = {
    old: Math.max(content.old + gutter, measured.old),
    new: Math.max(content.new + gutter, measured.new),
    both: Math.max(single + gutter, measured.old, measured.new),
  };
  // The trailing padding is only room past the end of a scrolled line, so a
  // line that fits without it needs no scrollbar.
  const fits = (px) => px - TRAIL_PX <= paneWidth;
  const overflows = !wrap && widthPx > 0 && (split ? !fits(need.old) || !fits(need.new) : !fits(need.both));

  // This file's rules in shiftSheet, while it overflows: in one column both
  // sides move together.
  const shiftClass = useMemo(() => `sx${shifts++}`, []);
  const shiftRules = useRef(null);
  useEffect(() => {
    if (!overflows) return;
    const add = (sel) => shiftSheet.cssRules[shiftSheet.insertRule(`${sel} { transform: translate(0px, 0) }`, shiftSheet.cssRules.length)];
    const at = `.diff.hscroll.${shiftClass} .cell`;
    const rules = { old: add(`${at}[data-side="old"] .code`), new: add(`${at}[data-side="new"] .code`) };
    shiftRules.current = rules;
    return () => {
      shiftRules.current = null;
      for (const r of [rules.old, rules.new]) shiftSheet.deleteRule([...shiftSheet.cssRules].indexOf(r));
    };
  }, [overflows, shiftClass]);
  const shift = useCallback((side, px) => {
    shiftRules.current?.[side].style.setProperty("transform", `translate(${-px}px, 0)`);
  }, []);

  // Wrapping (or a window wide enough to fit everything) removes the overflow,
  // so any offset left over from before has to be undone.
  useEffect(() => {
    if (overflows) return;
    shift("old", 0);
    shift("new", 0);
    if (barOld.current) barOld.current.scrollLeft = 0;
    if (barNew.current) barNew.current.scrollLeft = 0;
  }, [overflows, shift]);

  // A match past the edge of a long line is brought into view sideways, once its
  // row is drawn - which after a jump down the page is some frames away.
  useEffect(() => {
    if (!foundAt || !overflows) return;
    let raf = 0;
    const until = performance.now() + 2000;
    const look = () => {
      const mark = rootRef.current?.querySelector(".fm-on");
      if (!mark) {
        if (performance.now() < until) raf = requestAnimationFrame(look);
        return;
      }
      const cell = mark.closest(".cell");
      const bar = split && cell.dataset.side === "new" ? barNew.current : barOld.current;
      if (!bar) return;
      const from = cell.firstElementChild.getBoundingClientRect().right;
      const to = cell.getBoundingClientRect().right - TRAIL_PX;
      const r = mark.getBoundingClientRect();
      if (r.left < from) bar.scrollLeft -= from - r.left + 3 * TRAIL_PX;
      else if (r.right > to) bar.scrollLeft += r.right - to + 3 * TRAIL_PX;
    };
    look();
    return () => cancelAnimationFrame(raf);
  }, [foundAt, overflows, split]);

  // Trackpad swipes and shift+wheel scroll whichever side is under the cursor.
  //
  // This is a manual listener because React registers onWheel as passive, where
  // preventDefault does nothing - the browser would keep the gesture as well and
  // hand a leftward swipe to the back button. Claiming every horizontal wheel
  // over an overflowing diff, including the ones at either end, keeps the
  // gesture inside the diff.
  useEffect(() => {
    const root = rootRef.current;
    if (!root || !overflows) return;
    const onWheel = (e) => {
      const dx = Math.abs(e.deltaX) > Math.abs(e.deltaY) ? e.deltaX : e.shiftKey ? e.deltaY : 0;
      if (!dx) return;
      // One column has one bar, and it is barOld; split has one per side.
      const cell = e.target.closest?.("[data-side]");
      const bar = !split || cell?.dataset.side === "old" ? barOld.current : barNew.current;
      if (!bar) return;
      e.preventDefault();
      bar.scrollLeft += dx;
    };
    // A finger drags the side it is on, and let go moving, the code carries on
    // and slows. Up and down are the browser's (touch-action: pan-y), which
    // cancels the pointer when it takes a drag as a scroll.
    let pan = null;
    let glide = 0;
    const onDown = (e) => {
      cancelAnimationFrame(glide);
      const cell = e.pointerType === "touch" && e.target.closest?.("[data-side]");
      const bar = cell && (!split || cell.dataset.side === "old" ? barOld.current : barNew.current);
      pan = bar ? { id: e.pointerId, bar, x: e.clientX, from: e.clientX, at: e.timeStamp, v: 0 } : null;
    };
    const onMove = (e) => {
      if (pan?.id !== e.pointerId) return;
      // Within a few pixels a tap is still a tap.
      if (!pan.moving && Math.abs(e.clientX - pan.from) < TAP_SLOP_PX) return;
      pan.moving = true;
      const dx = e.clientX - pan.x;
      pan.bar.scrollLeft -= dx;
      pan.v = -dx / Math.max(1, e.timeStamp - pan.at);
      pan.x = e.clientX;
      pan.at = e.timeStamp;
    };
    const onUp = (e) => {
      if (pan?.id !== e.pointerId) return;
      const { bar, moving } = pan;
      let v = pan.v;
      pan = null;
      if (!moving || Math.abs(v) < 0.05) return;
      let last = performance.now();
      const step = (now) => {
        bar.scrollLeft += v * (now - last);
        v *= Math.pow(GLIDE_DECAY, now - last);
        last = now;
        if (Math.abs(v) > 0.02) glide = requestAnimationFrame(step);
      };
      glide = requestAnimationFrame(step);
    };
    const onCancel = (e) => {
      if (pan?.id === e.pointerId) pan = null;
    };
    root.addEventListener("wheel", onWheel, { passive: false });
    root.addEventListener("pointerdown", onDown);
    root.addEventListener("pointermove", onMove);
    root.addEventListener("pointerup", onUp);
    root.addEventListener("pointercancel", onCancel);
    return () => {
      cancelAnimationFrame(glide);
      root.removeEventListener("wheel", onWheel);
      root.removeEventListener("pointerdown", onDown);
      root.removeEventListener("pointermove", onMove);
      root.removeEventListener("pointerup", onUp);
      root.removeEventListener("pointercancel", onCancel);
    };
  }, [overflows, split]);

  const bars = overflows && (
    <div className={cx("hbars", !split && "single")} aria-hidden="true">
      <div
        className="hbar"
        ref={barOld}
        onScroll={(e) => {
          shift("old", e.currentTarget.scrollLeft);
          // Unified interleaves both sides in one column, so one bar moves both.
          if (!split) shift("new", e.currentTarget.scrollLeft);
        }}
      >
        {/* The bar spans a whole half, gutter included, so the spacer has
            to as well - otherwise its scroll range is short by the gutter
            and the end of the longest line stays out of reach. */}
        <div style={{ width: split ? need.old : need.both }} />
      </div>
      {split && (
        <div className="hbar" ref={barNew} onScroll={(e) => shift("new", e.currentTarget.scrollLeft)}>
          <div style={{ width: need.new }} />
        </div>
      )}
    </div>
  );

  return (
    <div
      // hscroll gates the per-line transform. Files that fit - nearly all of
      // them - must not pay for a transform node on every line they render.
      // Code mode is one column, so it scrolls sideways the way unified does.
      className={cx(
        "diff", split ? "diff-split" : "diff-unified", view === "code" && "diff-code", overflows && "hscroll", shiftClass,
      )}
      ref={rootRef}
    >
      {blocks.map((b, i) =>
        b.kind === "gap" ? (
          <GapRow key={b.id + i} gap={b} onExpand={unknown ? null : onExpand} onScope={revealScope} view={view} html={html} />
        ) : (
          <Block
            key={`${i}:${view}:${wrap}`}
            lines={b.lines}
            view={view}
            wrap={wrap}
            ctx={ctx}
            onMeasure={onMeasure}
            // A proposed edit sits in a modal that is hidden and shown again, which
            // the visibility observer has been seen to miss; it is small, so drawn whole.
            force={drawAll || holdsLine(b.lines, reveal)}
          />
        ),
      )}

      {/* A view of one file can hold the bars under its scrolling, barsIn,
          where they cover no code; a stream of files keeps them stuck here. */}
      {bars && (barsIn ? createPortal(bars, barsIn) : bars)}
    </div>
  );
}

// GapRow is the bar standing in for a collapsed run of unchanged lines. The
// whole bar expands on click; the controls are duplicated and pinned to both
// edges of the viewport so they are in reach whichever column you are reading,

// Block is one run of rows. Off screen it collapses to a spacer of the same
// height, so only the hunks near the viewport are in the DOM at all.
//
// content-visibility used to do the off-screen part, but it only skips paint
// and layout - the elements still exist, and the browser still walks them for
// style, hit testing and memory. A 17,000-row review is 300,000 elements, and
// at that size the frame budget is gone before anything is drawn.
//
// The height is the estimate until the block has been rendered once, and its
// measured height after that, because comment threads and wrapped lines make a
// row taller than one line.
const OVERSCAN = "1000px 0px";

// holdsLine reports whether a block renders the given 1-based line number on
// either side, which is how a jump finds a row that virtualisation would
// otherwise have left unmounted.
function holdsLine(lines, line) {
  if (!line) return false;
  const i = line - 1;
  return lines.some((l) => l.o === i || l.n === i);
}

function Block({ lines, view, wrap, ctx, onMeasure, force }) {
  const ref = useRef(null);
  const [visible, setVisible] = useState(false);
  // force keeps the block holding a jump's target line mounted even while it is
  // off screen, so the scroll has something to aim at.
  const shown = visible || force;
  // The row count, not the line count: split view pairs a deletion with its
  // replacement, so a block of 14 lines can render as 7 rows. Getting this
  // wrong makes every spacer the wrong height and the page walks under you.
  const rows = useMemo(
    () => (view === "split" ? pairRows(lines).length : view === "code" ? lines.length : unifiedRows(lines).length),
    [lines, view],
  );
  const height = useRef(rows * ROW_HEIGHT);
  // Marks a block with changes in it even while it is only a spacer, so
  // stepping to the next change can find one that is not rendered.
  const changes = useMemo(() => lines.some((l) => l.t !== "e" || l.mark), [lines]);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    // Observed against the pane that scrolls: against the window, the pane
    // clips the block before the margin is applied, so nothing off screen is
    // ever near.
    const root = el.closest(".content, .agent-scroll, .viewer-body");
    const io = new IntersectionObserver(([e]) => setVisible(e.isIntersecting), { root, rootMargin: OVERSCAN });
    io.observe(el);
    return () => io.disconnect();
  }, []);

  // Keep the height honest while the block is mounted - a thread opening, or
  // wrapped lines, make a row taller than one line. An observer reports the
  // size without forcing a layout the way reading offsetHeight would.
  useEffect(() => {
    if (!shown || !ref.current) return;
    const ro = new ResizeObserver(([e]) => {
      const h = e.borderBoxSize?.[0]?.blockSize ?? e.contentRect.height;
      if (h > 0) height.current = h;
    });
    ro.observe(ref.current);
    return () => ro.disconnect();
  }, [shown]);

  // Report the widest row this block actually rendered. The character-count
  // estimate that sizes the scrollbars cannot know the real font, the real
  // gutter, or how wide a CJK glyph or an emoji draws, and when it comes up
  // short the end of the line is unreachable. Measuring what is on screen
  // corrects it for the lines the reader can actually be looking at.
  useEffect(() => {
    if (!shown || !ref.current) return;
    const el = ref.current;
    // Reading a width forces the browser to flush layout, so this waits for a
    // gap between frames. Doing it inline on mount put a synchronous layout in
    // the middle of every scroll, which cost more than the miscalculation did.
    const idle = window.requestIdleCallback || ((f) => setTimeout(f, 150));
    const cancel = window.cancelIdleCallback || clearTimeout;
    const id = idle(() => {
      let o = 0;
      let n = 0;
      for (const cell of el.querySelectorAll(".cell")) {
        const gutter = cell.firstElementChild;
        const code = cell.lastElementChild;
        if (!code || !gutter) continue;
        const w = gutter.offsetWidth + code.scrollWidth;
        if (cell.dataset.side === "old") {
          if (w > o) o = w;
        } else if (w > n) n = w;
      }
      if (o || n) onMeasure(o, n);
    });
    return () => cancel(id);
  }, [shown, onMeasure, lines]);

  return (
    <div
      className="block"
      ref={ref}
      style={shown ? undefined : { height: height.current }}
      data-changes={changes || undefined}
    >
      {shown &&
        (view === "split" ? (
          <SplitRows lines={lines} ctx={ctx} />
        ) : view === "code" ? (
          <CodeRows lines={lines} ctx={ctx} />
        ) : (
          <UnifiedRows lines={lines} ctx={ctx} />
        ))}
    </div>
  );
}

function GapRow({ gap, onExpand, onScope, view, html }) {
  if (!onExpand) {
    return (
      <div className="gap unknown">
        <span className="gap-label gap-end">
          {gap.hidden} line{gap.hidden === 1 ? "" : "s"} not kept in the transcript
        </span>
      </div>
    );
  }
  const count = (
    <span className="gap-label">
      {gap.hidden} unchanged line{gap.hidden === 1 ? "" : "s"}
    </span>
  );
  // Laid out as a code row, so the scope sits in the code's column, and in
  // split each side names its own.
  const half = (side, first, last) => {
    const scope = gap.scope?.[side];
    return (
      <div className="gap-half" key={side}>
        <div className="gutter gap-gutter">
          <span className="ln" />
          {view === "unified" && <span className="ln" />}
          <button
            className="gap-icon"
            onClick={(e) => {
              e.stopPropagation();
              onExpand(gap.id, 20);
            }}
            aria-label="Expand 20 lines"
          />
        </div>
        {scope ? (
          <code
            className="code gap-code"
            onClick={(e) => {
              const part = e.target.closest(".gap-part");
              if (!part) return;
              e.stopPropagation();
              onScope(scope[part.dataset.i]);
            }}
            dangerouslySetInnerHTML={{ __html: scopeHtml(scope, html[side]) }}
          />
        ) : (
          <span className="gap-code">{first && !gap.scope && count}</span>
        )}
        {last && (
          <div className="gap-end">
            {gap.scope && count}
            <button
              onClick={(e) => {
                e.stopPropagation();
                onExpand(gap.id, "all");
              }}
              title="Expand the whole gap"
            >
              all
            </button>
          </div>
        )}
      </div>
    );
  };
  return (
    <div className={cx("gap", view === "split" && "gap-split")} onClick={() => onExpand(gap.id, 20)} title="Expand 20 lines">
      {view === "split" ? [half("old", true, false), half("new", false, true)] : half(gap.scope?.side ?? "new", true, true)}
    </div>
  );
}

// scopeHtml draws a gap's scope from the lines' own highlighting, outdented,
// each header a part that opens the whole of what it heads.
function scopeHtml(scope, html) {
  const line = (i) => (html[i] ?? "").replace(/^((?:<span[^>]*>)*)[ \t]+/, "$1");
  return scope
    .map((s, i) => {
      const body = stripOpener(s.lines.map(line).join('<span class="gap-dim">…</span>'));
      return `<span class="gap-part" data-i="${i}" title="Show all of ${escapeHtml(s.text)}">${body}</span>`;
    })
    .join(' <span class="gap-dim">›</span> ');
}

// wordRanges computes the changed character ranges for a replaced line pair,
// memoised by the two strings so scrolling does not redo the work.
const wordCache = new Map();
function wordRanges(oldText, newText) {
  const key = oldText + "\u0000" + newText;
  let hit = wordCache.get(key);
  if (!hit) {
    const [l, r] = diffWords(oldText, newText);
    hit = { left: spansToRanges(l), right: spansToRanges(r) };
    if (wordCache.size > 4000) wordCache.clear();
    wordCache.set(key, hit);
  }
  return hit;
}

const SplitRows = memo(function SplitRows({ lines, ctx }) {
  const rows = useMemo(() => pairRows(lines), [lines]);
  const out = [];
  rows.forEach((row, i) => {
    let leftRanges = null;
    let rightRanges = null;
    if (row.word) {
      const r = wordRanges(ctx.fd.oldLines[row.left.o], ctx.fd.newLines[row.right.n]);
      leftRanges = r.left;
      rightRanges = r.right;
    }
    out.push(
      <div className="row" key={"r" + i}>
        <Cell side="old" line={row.left} ctx={ctx} ranges={leftRanges} kind={row.kind} />
        <Cell side="new" line={row.right} ctx={ctx} ranges={rightRanges} kind={row.kind} />
      </div>,
    );
    // Threads and the composer break out of the two-column grid and span it.
    const anchors = [];
    if (row.left && row.left.o >= 0) anchors.push({ side: "old", no: row.left.o + 1 });
    if (row.right && row.right.n >= 0) anchors.push({ side: "new", no: row.right.n + 1 });
    for (const a of anchors) out.push(...anchorNodes(a, ctx, `t${i}-${a.side}`));
  });
  return out;
});

const UnifiedRows = memo(function UnifiedRows({ lines, ctx }) {
  const rows = useMemo(() => unifiedRows(lines), [lines]);
  return rows.flatMap((row, i) => {
    const l = row.line;
    const side = l.t === "d" ? "old" : "new";
    let ranges = null;
    if (row.word) {
      const oldText = ctx.fd.oldLines[l.t === "d" ? l.o : row.word.o];
      const newText = ctx.fd.newLines[l.t === "d" ? row.word.n : l.n];
      const r = wordRanges(oldText, newText);
      ranges = l.t === "d" ? r.left : r.right;
    }
    const no = l.t === "d" ? l.o + 1 : l.n + 1;
    return [
      <div className="row" key={"r" + i}>
        <UnifiedCell line={l} ctx={ctx} ranges={ranges} />
      </div>,
      ...anchorNodes({ side, no }, ctx, `t${i}`),
    ];
  });
});

// CodeRows is Code mode: one line per row, from whichever side the lines came
// off (a deleted file has only its old one), its change mark as a class.
const CodeRows = memo(function CodeRows({ lines, ctx }) {
  return lines.flatMap((l, i) => {
    const side = l.n < 0 ? "old" : "new";
    const idx = side === "old" ? l.o : l.n;
    return [
      <div className="row" key={"r" + i}>
        <LineCell
          ctx={ctx}
          side={side}
          no={idx + 1}
          html={(side === "old" ? ctx.oldHtml : ctx.newHtml)[idx]}
          mark={l.mark && "mk mk-" + l.mark}
        />
      </div>,
      ...anchorNodes({ side, no: idx + 1 }, ctx, `t${i}`),
    ];
  });
});

// anchorNodes renders any threads anchored at a line plus the open composer.
function anchorNodes(anchor, ctx, key) {
  const nodes = [];
  const list = ctx.byAnchor.get(`${anchor.side}:${anchor.no}`);
  if (list?.length) {
    nodes.push(
      <div className={cx("row-threads", "side-" + anchor.side)} key={key + "-th"}>
        <div className="thread-slot">
          <ThreadList
            threads={list}
            onAction={ctx.onThreadAction}
            onAttach={ctx.onAttach && ((t, to) => ctx.onAttach({ kind: "thread", threadId: t.id }, to))}
          />
        </div>
      </div>,
    );
  }
  const c = ctx.composing;
  if (c && c.side === anchor.side && c.end === anchor.no) {
    nodes.push(
      <div className={cx("row-threads", "side-" + anchor.side)} key={key + "-c"}>
        <div className="thread-slot">
          <Composer
          title={c.start === c.end ? `Line ${c.end}` : `Lines ${c.start}-${c.end}`}
          submitLabel={ctx.toAgent ? "Send" : "Comment"}
          aside={
            !ctx.toAgent &&
            ctx.onAttach &&
            ((body) => (
              // Words written go as a note, kept nowhere: Comment is what leaves
              // one in the review.
              <AttachButton
                onClick={(to) => {
                  const lines = { file: ctx.path, side: c.side, start: c.start, end: c.end, quote: c.quote };
                  ctx.onAttach(body ? { kind: "note", ...lines, body } : { kind: "lines", ...lines }, to);
                  ctx.setComposing(null);
                }}
                what={body ? "this note" : "these lines"}
              />
            ))
          }
          autoFocus
          selected={c.selected}
          onSearch={ctx.onSearch}
          onCancel={() => ctx.setComposing(null)}
          onSubmit={async (body) => {
            await ctx.onComment({
              file: ctx.path,
              side: c.side,
              startLine: c.start,
              endLine: c.end,
              quote: c.quote,
              body,
            });
            ctx.setComposing(null);
          }}
          />
        </div>
      </div>,
    );
  }
  return nodes;
}

function Cell({ side, line, ctx, ranges, kind }) {
  if (!line || (side === "old" ? line.o : line.n) < 0) {
    return <div className="cell blank" data-side={side} />;
  }
  const idx = side === "old" ? line.o : line.n;
  const html = side === "old" ? ctx.oldHtml[idx] : ctx.newHtml[idx];
  const mark = kind === "e" ? "" : side === "old" ? "del" : "add";
  return <LineCell ctx={ctx} side={side} no={idx + 1} html={html} ranges={ranges} mark={mark} />;
}

function UnifiedCell({ line, ctx, ranges }) {
  const isDel = line.t === "d";
  const idx = isDel ? line.o : line.n;
  const html = isDel ? ctx.oldHtml[idx] : ctx.newHtml[idx];
  return (
    <LineCell
      ctx={ctx}
      side={isDel ? "old" : "new"}
      no={idx + 1}
      oldNo={line.o >= 0 ? line.o + 1 : null}
      newNo={line.n >= 0 ? line.n + 1 : null}
      html={html}
      ranges={ranges}
      mark={isDel ? "del" : line.t === "i" ? "add" : ""}
      dualGutter={!ctx.oneNumber}
    />
  );
}

function LineCell({ ctx, side, no, oldNo, newNo, html, ranges, mark, dualGutter }) {
  const sel = ctx.selection;
  const selected = sel && sel.side === side && no >= sel.start && no <= sel.end;
  let body = ranges?.length ? applyRanges(html ?? "", ranges, "wd") : html ?? "";
  const marks = ctx.found?.get(`${side}:${no}`);
  if (marks) {
    const at = ctx.foundAt?.side === side && ctx.foundAt.line === no ? ctx.foundAt.start : -1;
    body = applyRanges(body, marks.filter((r) => r[0] !== at), "fm");
    if (at >= 0) body = applyRanges(body, marks.filter((r) => r[0] === at), "fm fm-on");
  }

  return (
    <div className={cx("cell", mark, selected && "sel", ctx.hit === no && "hit")} data-side={side} data-line={no}>
      {/* The gutter's "+" affordance is a CSS pseudo-element: as real nodes,
          such things were extra elements on every line, and a large review
          renders tens of thousands of lines. Clicking the gutter opens the
          composer for this line; dragging selects a range. */}
      <div
        className="gutter"
        title="Click to comment on this line, drag for a range"
        onMouseDown={ctx.onGutterDown(side, no)}
        onMouseEnter={ctx.onGutterEnter(side, no)}
      >
        {dualGutter ? (
          <>
            <span className="ln">{oldNo ?? ""}</span>
            <span className="ln">{newNo ?? ""}</span>
          </>
        ) : (
          <span className="ln">{no}</span>
        )}
      </div>
      <code
        className="code"
        onClick={(e) => {
          if (!jumpKey(e)) return;
          const word = identifierAt(e.clientX, e.clientY);
          if (word) ctx.onSymbol(word, ctx.path);
        }}
        dangerouslySetInnerHTML={{ __html: body || "&nbsp;" }}
      />
    </div>
  );
}

// cellOf finds the diff line a DOM node sits in, walking up from a text node.
function cellOf(node) {
  const el = node?.nodeType === 3 ? node.parentElement : node;
  return el?.closest?.("[data-line][data-side]") || null;
}

const IDENTIFIER = /^[A-Za-z_$][\w$]*$/;
const SELECTION_SETTLE_MS = 800;
const DOUBLE_TAP_MS = 350;

// lastPointer is what the page was last pressed with. A finger's selection and
// double tap raise none of the mouse's events these views listen for.
let lastPointer = "mouse";
window.addEventListener("pointerdown", (e) => (lastPointer = e.pointerType), true);

// Go-to-definition is Cmd+click on a Mac, where Ctrl+click is the right
// button, and Ctrl+click elsewhere, as in an editor.
const jumpKey = (e) => (isMac ? e.metaKey : e.ctrlKey);

// wordAt is the range of the identifier under a point in code, null for none.
function wordAt(x, y) {
  let node, offset;
  if (document.caretRangeFromPoint) {
    const r = document.caretRangeFromPoint(x, y);
    [node, offset] = [r?.startContainer, r?.startOffset];
  } else {
    const p = document.caretPositionFromPoint?.(x, y);
    [node, offset] = [p?.offsetNode, p?.offset];
  }
  if (node?.nodeType !== Node.TEXT_NODE || !node.parentElement.closest(".code")) return null;
  const text = node.textContent;
  let start = offset;
  let end = start;
  while (start > 0 && /[\w$]/.test(text[start - 1])) start--;
  while (end < text.length && /[\w$]/.test(text[end])) end++;
  if (!IDENTIFIER.test(text.slice(start, end))) return null;
  const range = document.createRange();
  range.setStart(node, start);
  range.setEnd(node, end);
  return range;
}

// identifierAt is the word under a point, as a double tap or Cmd/Ctrl+click
// names it.
function identifierAt(x, y) {
  return wordAt(x, y)?.toString() || "";
}

// While Cmd/Ctrl is held, the name under the pointer is underlined, as an
// editor shows what a click would open.
let pointer = null;
let jumping = false;
const showJump = (e) => {
  const range = jumpKey(e) && pointer && wordAt(pointer.x, pointer.y);
  if (!range && !jumping) return;
  jumping = !!range;
  document.documentElement.classList.toggle("jumping", jumping);
  if (typeof Highlight === "undefined") return;
  if (range) CSS.highlights.set("jump", new Highlight(range));
  else CSS.highlights.delete("jump");
};
window.addEventListener("mousemove", (e) => ((pointer = { x: e.clientX, y: e.clientY }), showJump(e)), { passive: true });
window.addEventListener("keydown", showJump);
window.addEventListener("keyup", showJump);
window.addEventListener("blur", () => showJump({}));

// Text selected on one line is marked wherever else it is in that file, as an
// editor does. A CSS highlight rather than the rows' HTML, which would take the
// selection away; laid again as rows mount, since only those are in the page.
let same = null; // { root, text, observer, frame }
let selFrame = 0;

function clearSame() {
  if (!same) return;
  same.observer.disconnect();
  cancelAnimationFrame(same.frame);
  same = null;
  CSS.highlights.delete("same");
}

function laySame() {
  const { root, text } = same;
  const ranges = [];
  for (const code of root.querySelectorAll(".cell > .code")) {
    const whole = code.textContent;
    let at = whole.indexOf(text);
    if (at < 0) continue;
    // A match can run across the highlighter's spans.
    const nodes = [];
    let n = 0;
    const walker = document.createTreeWalker(code, NodeFilter.SHOW_TEXT);
    for (let t = walker.nextNode(); t; t = walker.nextNode()) {
      nodes.push([t, n]);
      n += t.length;
    }
    const point = (off) => {
      let i = nodes.length - 1;
      while (i > 0 && nodes[i][1] > off) i--;
      return [nodes[i][0], off - nodes[i][1]];
    };
    for (; at >= 0; at = whole.indexOf(text, at + text.length)) {
      const r = document.createRange();
      r.setStart(...point(at));
      r.setEnd(...point(at + text.length));
      ranges.push(r);
    }
  }
  CSS.highlights.set("same", new Highlight(...ranges));
}

function markSame() {
  selFrame = 0;
  // Selecting code opens the comment box, which takes the selection: what it
  // marked stays while that is written.
  if (document.activeElement?.matches("textarea, input")) return;
  const sel = window.getSelection();
  const text = sel && !sel.isCollapsed ? sel.toString() : "";
  const cell = text.trim().length > 1 && !text.includes("\n") && cellOf(sel.anchorNode);
  const inCode = cell && cell === cellOf(sel.focusNode) && cell.querySelector(".code")?.contains(sel.anchorNode);
  const root = inCode && cell.closest(".diff");
  if (!root) return clearSame();
  if (same?.root === root && same.text === text) return;
  clearSame();
  const s = { root, text, frame: 0 };
  s.observer = new MutationObserver(() => {
    s.frame ||= requestAnimationFrame(() => {
      s.frame = 0;
      if (same === s) laySame();
    });
  });
  s.observer.observe(root, { childList: true, subtree: true });
  same = s;
  laySame();
}

if (typeof Highlight !== "undefined") {
  document.addEventListener("selectionchange", () => (selFrame ||= requestAnimationFrame(markSame)));
}
