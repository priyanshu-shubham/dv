// Orb is a thinking-orbs animation, drawn here rather than by the package's
// component: that one redraws its sphere of dots on every frame, 60 or more a
// second, which was most of the CPU a working session took. This draws
// FPS a second; the motion follows the clock, so it moves no slower.
import { useEffect, useRef } from "react";
import { MODE_DRAWS, resolvePreset } from "thinking-orbs";

const FPS = 24;
// The large preset drawn small: the small one enlarged would blur.
const PRESET = 64;

const dark = () => document.documentElement.dataset.theme !== "light";

export function Orb({ state, px = 32 }) {
  const ref = useRef(null);
  useEffect(() => {
    const canvas = ref.current;
    const ctx = canvas.getContext("2d");
    const ratio = Math.min(2, window.devicePixelRatio || 1);
    canvas.width = canvas.height = Math.round(px * ratio);
    const { mode, speed, opts } = resolvePreset(state, PRESET);
    const paint = (now) => {
      const k = (px / PRESET) * ratio;
      ctx.setTransform(k, 0, 0, k, 0, 0);
      ctx.clearRect(0, 0, PRESET, PRESET);
      MODE_DRAWS[mode](ctx, PRESET, (now / 1000) * speed, dark(), opts);
    };
    if (matchMedia("(prefers-reduced-motion: reduce)").matches) return paint(600);

    let frame = 0;
    let last = 0;
    let seen = true;
    const tick = (now) => {
      frame = requestAnimationFrame(tick);
      // A little under the interval, or a 60Hz display would skip to 20.
      if (now - last < 1000 / FPS - 4) return;
      last = now;
      paint(now);
    };
    // Nothing is drawn off screen, in a hidden view or a background tab.
    const run = () => {
      cancelAnimationFrame(frame);
      if (seen && !document.hidden) frame = requestAnimationFrame(tick);
    };
    const io = new IntersectionObserver(([e]) => {
      seen = e.isIntersecting;
      run();
    });
    io.observe(canvas);
    document.addEventListener("visibilitychange", run);
    paint(performance.now());
    return () => {
      cancelAnimationFrame(frame);
      io.disconnect();
      document.removeEventListener("visibilitychange", run);
    };
  }, [state, px]);
  return <canvas ref={ref} className="orb-canvas" data-state={state} aria-hidden="true" style={{ width: px, height: px }} />;
}
