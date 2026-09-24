// PdfView draws a PDF with PDF.js, the same in every browser and on a phone,
// whose own viewers differ or are missing. PDF.js is fetched the first time a
// PDF is shown; the page never loads it otherwise.
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { cx } from "./util.js";
import { IconMinus, IconPlus } from "./icons.jsx";

/* global __PDFJS__ */
let loading = null;
function pdfjs() {
  loading ||= import("pdfjs-dist").then((lib) => {
    lib.GlobalWorkerOptions.workerSrc = asset("pdf.worker.min.mjs");
    return lib;
  });
  return loading;
}
// build.mjs copies the worker and its data into a folder named for the version.
const asset = (name) => new URL(`./${__PDFJS__}/${name}`, import.meta.url).href;

const GAP = 12; // between pages, and around them
const ZOOMS = [0.5, 0.67, 0.8, 1, 1.25, 1.5, 2, 3];
const MIN_ZOOM = 0.25;
const MAX_ZOOM = 5;

export function PdfView({ src }) {
  const [doc, setDoc] = useState(null); // { pdf, sizes: [{ w, h }] } at scale 1
  const [error, setError] = useState("");
  const [zoom, setZoom] = useState(null); // null fits the width
  const [width, setWidth] = useState(0);
  const [current, setCurrent] = useState(1);
  const scroller = useRef(null);
  const view = useRef(null);

  // The view is as tall as what it scrolls in shows, less what is above and
  // below it in its file's card: scrolled to, the PDF has the height to itself.
  useLayoutEffect(() => {
    const el = view.current;
    const card = el.parentElement.closest(".file, .modal") || el.parentElement;
    let outer = el.parentElement;
    while (outer && !/auto|scroll/.test(getComputedStyle(outer).overflowY)) outer = outer.parentElement;
    outer ||= document.scrollingElement;
    // In an overlay the scroller is inside the card, and is what to fill.
    const within = card !== outer && card.contains(outer) ? outer : card;
    const fit = () => {
      const r = el.getBoundingClientRect();
      const w = within.getBoundingClientRect();
      const above = r.top - w.top + (within === outer ? outer.scrollTop : 0);
      // What closes the boxes it is in, not what is beside it: Before and
      // After side by side would otherwise size each other.
      let below = 0;
      for (let p = el.parentElement; p; p = p.parentElement) {
        const s = getComputedStyle(p);
        below += parseFloat(s.paddingBottom) + parseFloat(s.borderBottomWidth);
        if (p === within) break;
      }
      // A card keeps the gap it has above it, from what comes before or the
      // scroller's top, below it too.
      let gap = 0;
      if (within === card) {
        let at = card;
        while (at !== outer && !at.previousElementSibling) at = at.parentElement;
        const top = at === outer ? outer.getBoundingClientRect().top - outer.scrollTop : at.previousElementSibling.getBoundingClientRect().bottom;
        gap = Math.min(24, Math.max(0, w.top - top));
      }
      const pad = 2 * gap;
      el.style.height = `${Math.max(320, Math.floor(outer.clientHeight - pad - above - below))}px`;
    };
    fit();
    const ro = new ResizeObserver(fit);
    ro.observe(outer);
    return () => ro.disconnect();
  }, []);

  useEffect(() => {
    let task = null;
    let gone = false;
    setDoc(null);
    setError("");
    pdfjs()
      .then(async (lib) => {
        if (gone) return;
        task = lib.getDocument({ url: src, cMapUrl: asset("cmaps/"), cMapPacked: true, wasmUrl: asset("wasm/"), iccUrl: asset("iccs/"), isEvalSupported: false });
        const pdf = await task.promise;
        // Every page's size up front keeps the scroll height true before any is drawn.
        const sizes = [];
        for (let i = 1; i <= pdf.numPages; i++) {
          const v = (await pdf.getPage(i)).getViewport({ scale: 1 });
          sizes.push({ w: v.width, h: v.height });
        }
        if (!gone) setDoc({ pdf, sizes });
      })
      .catch((e) => {
        if (gone) return;
        setError(e?.name === "PasswordException" ? "This PDF needs a password." : e?.name === "InvalidPDFException" ? "This is not a PDF dv can read." : "The PDF could not be shown.");
      });
    return () => {
      gone = true;
      task?.destroy();
    };
  }, [src]);

  useLayoutEffect(() => {
    const el = scroller.current;
    if (!el) return;
    const ro = new ResizeObserver(() => setWidth(el.clientWidth));
    ro.observe(el);
    return () => ro.disconnect();
  }, [doc]);

  const widest = doc ? Math.max(...doc.sizes.map((s) => s.w)) : 0;
  const fit = widest && width ? Math.max(0.1, (width - 2 * GAP) / widest) : 1;
  const scale = zoom ?? fit;
  const scaleRef = useRef(scale);
  scaleRef.current = scale;

  // A zoom keeps the spot at (x, y) in the view where it is: the pointer's for
  // a pinch, else the middle. The spot is held as a place on a page, since
  // centring and the gaps between pages do not scale with them. null fits the width.
  const anchor = useRef(null); // { i, fx, fy, x, y }: page, place on it at scale 1
  const laidOut = useRef(scale); // the scale the pages are laid out at
  const zoomTo = (next, x, y) => {
    const el = scroller.current;
    if (next !== null) next = Math.min(MAX_ZOOM, Math.max(MIN_ZOOM, next));
    if (!el?.children.length || (next ?? fit) === scaleRef.current) return;
    x ??= el.clientWidth / 2;
    y ??= el.clientHeight / 2;
    // A pinch can move again before the pages are laid out at its last step:
    // the spot is still the one it started on.
    if (anchor.current) Object.assign(anchor.current, { x, y });
    else {
      const px = el.scrollLeft + x;
      const py = el.scrollTop + y;
      const pages = [...el.children];
      const i = Math.max(0, pages.findLastIndex((p) => p.offsetTop <= py));
      const p = pages[i];
      anchor.current = { i, fx: (px - p.offsetLeft) / laidOut.current, fy: (py - p.offsetTop) / laidOut.current, x, y };
    }
    scaleRef.current = next ?? fit;
    setZoom(next);
  };
  useLayoutEffect(() => {
    laidOut.current = scale;
    const a = anchor.current;
    const p = scroller.current?.children[a?.i];
    anchor.current = null;
    if (!p) return;
    scroller.current.scrollLeft = p.offsetLeft + a.fx * scale - a.x;
    scroller.current.scrollTop = p.offsetTop + a.fy * scale - a.y;
  }, [scale]);
  const step = (dir) => {
    const next = dir > 0 ? ZOOMS.find((z) => z > scale + 0.01) : ZOOMS.findLast((z) => z < scale - 0.01);
    if (next) zoomTo(next);
  };

  // A pinch on a trackpad comes as a wheel with Ctrl held (and in Safari as a
  // gesture); it zooms the PDF rather than the whole page, as Ctrl+scroll does.
  useEffect(() => {
    const el = scroller.current;
    if (!el) return;
    const at = (e) => {
      const r = el.getBoundingClientRect();
      return [e.clientX - r.left, e.clientY - r.top];
    };
    const onWheel = (e) => {
      if (!e.ctrlKey) return;
      e.preventDefault();
      // A mouse's notch is a big step; a pinch comes in many small ones.
      const k = Math.min(1.25, Math.max(0.8, Math.exp(-e.deltaY * 0.01)));
      zoomTo(scaleRef.current * k, ...at(e));
    };
    let from = 1;
    const onGestureStart = (e) => {
      e.preventDefault();
      from = scaleRef.current;
    };
    const onGesture = (e) => {
      e.preventDefault();
      zoomTo(from * e.scale, ...at(e));
    };
    el.addEventListener("wheel", onWheel, { passive: false });
    el.addEventListener("gesturestart", onGestureStart);
    el.addEventListener("gesturechange", onGesture);
    return () => {
      el.removeEventListener("wheel", onWheel);
      el.removeEventListener("gesturestart", onGestureStart);
      el.removeEventListener("gesturechange", onGesture);
    };
  }, [doc]);

  // The page at the middle of the view is the one being read.
  const onScroll = () => {
    const el = scroller.current;
    if (!doc || !el) return;
    const mid = el.scrollTop + el.clientHeight / 2;
    let top = GAP;
    for (let i = 0; i < doc.sizes.length; i++) {
      top += doc.sizes[i].h * scale + GAP;
      if (top > mid) return setCurrent(i + 1);
    }
    setCurrent(doc.sizes.length);
  };
  useEffect(onScroll, [doc, scale]);

  const open = (
    <a className="pdf-open" href={src} target="_blank" rel="noopener noreferrer">
      Open in a new tab
    </a>
  );
  if (error)
    return (
      <div className="pdf-view failed">
        <div className="media-failed">{error}</div>
        {open}
      </div>
    );
  return (
    <div className="pdf-view" ref={view}>
      <div className="pdf-bar">
        <span className="dim">{doc ? `${current} / ${doc.sizes.length}` : "Loading…"}</span>
        <span className="spacer" />
        <button className="ghost" onClick={() => step(-1)} disabled={!doc} title="Zoom out">
          <IconMinus size={12} />
        </button>
        <button className={cx("ghost pdf-zoom", zoom === null && "on")} onClick={() => zoomTo(null)} disabled={!doc} title="Fit the width">
          {Math.round(scale * 100)}%
        </button>
        <button className="ghost" onClick={() => step(1)} disabled={!doc} title="Zoom in">
          <IconPlus size={12} />
        </button>
        {open}
      </div>
      <div className="pdf-pages" ref={scroller} onScroll={onScroll}>
        {doc?.sizes.map((s, i) => (
          <PdfPage key={i} pdf={doc.pdf} n={i + 1} w={s.w * scale} h={s.h * scale} scale={scale} root={scroller} />
        ))}
      </div>
    </div>
  );
}

// PdfPage draws one page once it comes near the view, again at a new scale,
// with its text laid over it to select.
function PdfPage({ pdf, n, w, h, scale, root }) {
  const box = useRef(null);
  const [near, setNear] = useState(false);
  useEffect(() => {
    const io = new IntersectionObserver(([e]) => setNear(e.isIntersecting), { root: root.current, rootMargin: "800px 0px" });
    io.observe(box.current);
    return () => io.disconnect();
  }, [root]);

  useEffect(() => {
    // A page far out of view lets its canvas go: a long PDF's would add up.
    if (!near) return void box.current.replaceChildren();
    let render = null;
    let text = null;
    let gone = false;
    (async () => {
      const lib = await pdfjs();
      const page = await pdf.getPage(n);
      if (gone) return;
      const viewport = page.getViewport({ scale });
      const dpr = window.devicePixelRatio || 1;
      const canvas = document.createElement("canvas");
      canvas.width = Math.floor(viewport.width * dpr);
      canvas.height = Math.floor(viewport.height * dpr);
      render = page.render({ canvas, viewport, transform: dpr === 1 ? null : [dpr, 0, 0, dpr, 0, 0] });
      await render.promise;
      if (gone) return;
      const layer = document.createElement("div");
      layer.className = "textLayer";
      box.current.style.setProperty("--total-scale-factor", String(scale * (page.userUnit || 1)));
      text = new lib.TextLayer({ textContentSource: page.streamTextContent(), container: layer, viewport });
      // Swapped in whole, so a page being redrawn keeps showing the old one.
      box.current.replaceChildren(canvas, layer);
      await text.render();
    })().catch(() => {});
    return () => {
      gone = true;
      render?.cancel();
      text?.cancel();
    };
  }, [near, pdf, n, scale]);

  return <div className="pdf-page" ref={box} style={{ width: w, height: h }} data-page={n} />;
}
