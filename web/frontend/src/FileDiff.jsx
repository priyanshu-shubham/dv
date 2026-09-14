import { memo, useCallback, useEffect, useMemo, useRef, useState } from "react";
import { buildBlocks, codeLines, pairRows, unifiedRows } from "./hunks.js";
import { diffWords, spansToRanges } from "./worddiff.js";
import { applyRanges, ensureLanguage, highlightLines, langReady } from "./highlight.js";
import { charWidth, cx, LRM, splitPath, statusLabel, statusLetter, useElementWidth, visualLength } from "./util.js";
import { ThreadList, Composer } from "./Threads.jsx";
import { IconCheck, IconChevron, IconChevronDown, IconComment, IconFile } from "./icons.jsx";
import { AskButton } from "./AskPanel.jsx";

// One file's worth of diff. The parent mounts these lazily: `fd` arrives only
// once the section has been scrolled near, so a 500-file diff still opens
// instantly. Memoised, and every prop is either a primitive or a stable
// identity: a large diff renders tens of thousands of rows, and re-rendering
// them because some unrelated state moved is what makes scrolling stutter.
function FileDiff({
  entry, fd, loading, error, view, contextLines, wrap, threads, collapsed,
  onToggleCollapse, viewed, onToggleViewed, onComment, onThreadAction, onSymbol,
  isActive, composing, setComposing, onAsk, onView, reveal, generatedHidden, onShowGenerated,
}) {
  const [expanded, setExpanded] = useState({});
  const [selection, setSelection] = useState(null); // { side, start, end }
  const [, forceRender] = useState(0);

  useEffect(() => {
    if (fd?.lang) ensureLanguage(fd.lang, () => forceRender((n) => n + 1));
  }, [fd?.lang]);

  const [dir, name] = splitPath(entry.path);
  const openThreads = threads.filter((t) => !t.resolved).length;
  // An added or deleted file has one side; split would leave half of it blank.
  const oneSided = entry.status === "A" || entry.status === "D";

  const expand = useCallback((id, amount) => {
    setExpanded((e) => ({
      ...e,
      [id]: amount === "all" ? "all" : (typeof e[id] === "number" ? e[id] : 0) + amount,
    }));
  }, []);

  const startComment = useCallback(
    (side, start, end) => {
      const src = side === "old" ? fd?.oldLines : fd?.newLines;
      const quote = src ? src.slice(start - 1, end) : [];
      setComposing({ path: entry.path, side, start, end, quote });
      setSelection(null);
    },
    [fd, entry.path, setComposing],
  );

  // A body that is on its way; stepping between changes has to be able to
  // find it before it has any rows.
  const pending = !collapsed && !generatedHidden && !fd && !error;

  return (
    <section
      className={cx("file", isActive && "file-active")}
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
        <h3 className="file-path" title={entry.path}>
          <span className="dir">
            {LRM}
            {dir}
            {LRM}
          </span>
          <span className="name">{name}</span>
        </h3>
        {entry.oldPath && <span className="renamed-from">from {entry.oldPath}</span>}
        {entry.generated && <span className="tag-generated" title="Machine-written; see the README for what counts">generated</span>}
        <span className="spacer" />
        {openThreads > 0 && (
          <span className="file-comments">
            <IconComment size={12} /> {openThreads}
            <span className="btn-label">open</span>
          </span>
        )}
        <span className="stat">
          <span className="add">+{entry.additions}</span>
          <span className="del">-{entry.deletions}</span>
        </span>
        <button
          className="view-file"
          onClick={() => onView(entry.path)}
          title={entry.status === "D" ? "View the file as it was before it was deleted (f)" : "View the whole file (f)"}
        >
          <IconFile size={12} />
          <span className="btn-label">File</span>
        </button>
        <AskButton onClick={() => onAsk({ file: entry.path })} title="Ask Claude about this file's diff" />
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

      {!collapsed && !generatedHidden && (
        <div className={cx("file-body", wrap && "wrap")}>
          {loading && <div className="file-note">Loading...</div>}
          {error && <div className="file-note error">{error}</div>}
          {fd?.binary && <div className="file-note">Binary file - not shown.</div>}
          {fd?.tooLarge && <div className="file-note">File is too large to display.</div>}
          {fd && !fd.binary && !fd.tooLarge && (
            <DiffBody
              fd={fd}
              view={oneSided && view === "split" ? "unified" : view}
              contextLines={contextLines}
              expanded={expanded}
              onExpand={expand}
              threads={threads}
              selection={selection}
              setSelection={setSelection}
              composing={composing}
              setComposing={setComposing}
              onStartComment={startComment}
              onComment={onComment}
              onThreadAction={onThreadAction}
              onSymbol={onSymbol}
              onAsk={onAsk}
              path={entry.path}
              wrap={wrap}
              reveal={reveal}
            />
          )}
        </div>
      )}
    </section>
  );
}

export default memo(FileDiff);

export const cssId = (p) => p.replace(/[^A-Za-z0-9_-]/g, "_");

// Must match the diff's line-height in styles.css; it is only a first guess at
// a skipped block's height, which the browser replaces once it has rendered it.
const ROW_HEIGHT = 20;

// The code cell's +/- slot and its padding-right, from styles.css.
const SIGN_PX = 12;
const TRAIL_PX = 16;

// Code mode renders a file through the same rows as a diff - one column, every
// line, in runs this long so virtualisation still has blocks to let go of.
const CODE_BLOCK = 200;

// DiffBody renders a file's rows. view "code" is Code mode: just `side` of the
// file, whole, with changes marked in the gutter. readOnly takes the comment
// affordances away, for an edit Claude has only proposed.
export function DiffBody({
  fd, view, side, contextLines, expanded, onExpand, threads, selection, setSelection,
  composing, setComposing, onStartComment, onComment, onThreadAction, onSymbol, onAsk, path, wrap,
  reveal, hit = 0, readOnly = false,
}) {
  // Where comments hang, which stay in view whatever the context setting.
  const anchors = useMemo(() => {
    const out = threads.map((t) => ({ side: t.side, start: t.startLine, end: t.endLine }));
    if (composing) out.push({ side: composing.side, start: composing.start, end: composing.end });
    return out;
  }, [threads, composing]);
  const blocks = useMemo(() => {
    if (view !== "code") return buildBlocks(fd, contextLines, expanded, anchors);
    const lines = codeLines(fd, side);
    const out = [];
    for (let i = 0; i < lines.length; i += CODE_BLOCK) out.push({ kind: "rows", lines: lines.slice(i, i + CODE_BLOCK) });
    return out;
  }, [fd, view, side, contextLines, expanded, anchors]);
  const ready = langReady(fd.lang);
  const oldHtml = useMemo(() => highlightLines(path + " old", fd.oldLines, fd.lang), [path, fd, ready]);
  const newHtml = useMemo(() => highlightLines(path + " new", fd.newLines, fd.lang), [path, fd, ready]);

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
    const onMouseUp = (e) => {
      if (drag.current) return; // the gutter drag has already claimed this
      if (e.detail === 2) return; // a double-click is go-to-definition
      if (e.target.closest?.(".row-threads")) return; // selecting inside a comment

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
      onStartComment(from.dataset.side, Math.min(a, b), Math.max(a, b));
    };
    root.addEventListener("mouseup", onMouseUp);
    return () => root.removeEventListener("mouseup", onMouseUp);
  }, [onStartComment]);

  // The row blocks below are memoised on this object, so it must be stable
  // across renders that changed nothing they depend on.
  const ctx = useMemo(
    () => ({
      fd, oldHtml, newHtml, byAnchor, selection, composing, setComposing,
      onGutterDown, onGutterEnter, onComment, onThreadAction, onSymbol, onAsk, path, hit, readOnly,
    }),
    [
      fd, oldHtml, newHtml, byAnchor, selection, composing, setComposing,
      onGutterDown, onGutterEnter, onComment, onThreadAction, onSymbol, onAsk, path, hit, readOnly,
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
  // What the gutter occupies: one line number in split and code, two in unified.
  const gutter = view === "unified" ? 96 : 60;

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

  // Pre-negated: the stylesheet uses the variable straight, so scrolling does
  // not re-evaluate a calc() on every code element in the file.
  const shift = useCallback((side, px) => {
    rootRef.current?.style.setProperty(side === "old" ? "--sx-old" : "--sx-new", `${-px}px`);
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
    root.addEventListener("wheel", onWheel, { passive: false });
    return () => root.removeEventListener("wheel", onWheel);
  }, [overflows, split]);

  return (
    <div
      // hscroll gates the per-line transform. Files that fit - nearly all of
      // them - must not pay for a transform node on every line they render.
      // Code mode is one column, so it scrolls sideways the way unified does.
      className={cx(
        "diff", split ? "diff-split" : "diff-unified", view === "code" && "diff-code", overflows && "hscroll",
        readOnly && "read-only",
      )}
      ref={rootRef}
    >
      {blocks.map((b, i) =>
        b.kind === "gap" ? (
          <GapRow key={b.id + i} gap={b} onExpand={onExpand} />
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
            force={readOnly || holdsLine(b.lines, reveal)}
          />
        ),
      )}

      {overflows && (
        <div className="hbars" aria-hidden="true">
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
      )}
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
    const io = new IntersectionObserver(([e]) => setVisible(e.isIntersecting), { rootMargin: OVERSCAN });
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

function GapRow({ gap, onExpand }) {
  const actions = (
    <>
      <button
        className="gap-icon"
        onClick={(e) => {
          e.stopPropagation();
          onExpand(gap.id, 20);
        }}
        title="Expand 20 lines"
        aria-label="Expand 20 lines"
      />
      <button
        onClick={(e) => {
          e.stopPropagation();
          onExpand(gap.id, "all");
        }}
        title="Expand the whole gap"
      >
        all
      </button>
    </>
  );
  return (
    <div className="gap" onClick={() => onExpand(gap.id, 20)} title="Expand 20 lines">
      <div className="gap-side gap-left">
        {actions}
        <span className="gap-label">
          {gap.hidden} unchanged line{gap.hidden === 1 ? "" : "s"}
        </span>
      </div>
      <span className="spacer" />
      <div className="gap-side gap-right">{actions}</div>
    </div>
  );
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
          <ThreadList threads={list} onAction={ctx.onThreadAction} />
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
          aside={
            <AskButton
              onClick={() => ctx.onAsk({ file: ctx.path, side: c.side, startLine: c.start, endLine: c.end })}
              title="Ask Claude about these lines instead"
            />
          }
          autoFocus
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
    return <div className="cell blank" />;
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
      dualGutter
    />
  );
}

function LineCell({ ctx, side, no, oldNo, newNo, html, ranges, mark, dualGutter }) {
  const sel = ctx.selection;
  const selected = sel && sel.side === side && no >= sel.start && no <= sel.end;
  const body = ranges?.length ? applyRanges(html ?? "", ranges, "wd") : html ?? "";

  return (
    <div className={cx("cell", mark, selected && "sel", ctx.hit === no && "hit")} data-side={side} data-line={no}>
      {/* The gutter's "+" affordance and the +/- sign are both CSS
          pseudo-elements. As real nodes they were five extra elements on every
          line, and a large review renders tens of thousands of lines. Clicking
          the gutter opens the composer for this line; dragging selects a range. */}
      <div
        className="gutter"
        title={ctx.readOnly ? undefined : "Click to comment on this line, drag for a range"}
        onMouseDown={ctx.readOnly ? undefined : ctx.onGutterDown(side, no)}
        onMouseEnter={ctx.readOnly ? undefined : ctx.onGutterEnter(side, no)}
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
        onDoubleClick={() => {
          const word = selectedIdentifier();
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

// selectedIdentifier reads the word a double-click just selected, which is what
// go-to-definition looks up.
function selectedIdentifier() {
  const sel = window.getSelection();
  const text = sel ? sel.toString().trim() : "";
  return /^[A-Za-z_$][\w$]*$/.test(text) ? text : "";
}
