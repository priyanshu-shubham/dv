import { useEffect, useRef, useState } from "react";
import { api } from "./api.js";
import { boot, slug } from "./boot.js";
import { say } from "./Notices.jsx";
import { Modal } from "./Overlays.jsx";
import { cx, isTyping, LRM } from "./util.js";

const RECENT = "dv:hubRecent";

const readRecent = () => {
  try {
    return JSON.parse(localStorage.getItem(RECENT) || "[]");
  } catch {
    return [];
  }
};

// FolderSwitcher goes between the folders open in the hub serving the page, as
// Alt+Tab goes between windows: Ctrl+Shift+Down, held, lists them by when this
// browser was last in each, the one before this picked; Down and Up move along
// and letting go switches, so a tap goes back and forth between two. In Diff
// and Files, where Shift+Up and Down do nothing else, Shift is enough; the Agent
// view steps through its sessions with them. Ctrl on a Mac too, where
// Cmd+Shift+Up selects text.
export default function FolderSwitcher({ mode }) {
  const [shown, setShown] = useState(null); // { list, at }
  const run = useRef(null); // { hold, at, list, released }: the switch under way

  // Read afresh each time, as the other folders' tabs write it too.
  useEffect(() => {
    if (!slug) return;
    const bump = () => {
      if (document.hidden) return;
      try {
        localStorage.setItem(RECENT, JSON.stringify([slug, ...readRecent().filter((s) => s !== slug)].slice(0, 50)));
      } catch {}
    };
    bump();
    document.addEventListener("visibilitychange", bump);
    return () => document.removeEventListener("visibilitychange", bump);
  }, []);

  useEffect(() => {
    if (!boot.base) return;
    const cancel = () => {
      run.current = null;
      setShown(null);
    };
    const at = (r) => ((r.at % r.list.length) + r.list.length) % r.list.length;
    const go = (r) => {
      const to = r.list[at(r)];
      cancel();
      if (to.slug !== slug) location.href = `/${to.slug}/`;
    };

    const onDown = (e) => {
      if (e.key !== "ArrowUp" && e.key !== "ArrowDown") return;
      const step = e.key === "ArrowDown" ? 1 : -1;
      const r = run.current;
      if (r) {
        if (!(r.hold === "Control" ? e.ctrlKey : e.shiftKey)) return;
        e.preventDefault();
        e.stopPropagation();
        r.at += step;
        if (r.list) setShown({ list: r.list, at: at(r) });
        return;
      }
      if (!e.shiftKey || e.metaKey || e.altKey) return;
      if (!e.ctrlKey && (mode === "agent" || isTyping(e.target) || document.querySelector(".backdrop, .prompt-backdrop:not([hidden])"))) return;
      e.preventDefault();
      e.stopPropagation();
      const started = { hold: e.ctrlKey ? "Control" : "Shift", at: step, list: null, released: false };
      run.current = started;
      api.hubFolders().then(
        ({ folders }) => {
          if (run.current !== started) return;
          // This folder heads the list whatever the browser kept, so a step lands on another.
          const recent = readRecent();
          const rank = (f) => (f.slug === slug ? -1 : recent.includes(f.slug) ? recent.indexOf(f.slug) : recent.length);
          started.list = folders.filter((f) => f.open || f.slug === slug).sort((a, b) => rank(a) - rank(b));
          if (started.list.length < 2) {
            cancel();
            say("No other folder is open", "Folders opened from the hub are listed here.");
          } else if (started.released) go(started);
          else setShown({ list: started.list, at: at(started) });
        },
        (err) => {
          if (run.current === started) cancel();
          say("Could not reach the hub", err.message);
        },
      );
    };
    const onUp = (e) => {
      const r = run.current;
      if (!r || e.key !== r.hold) return;
      r.released = true;
      if (r.list) go(r);
    };

    window.addEventListener("keydown", onDown, true);
    window.addEventListener("keyup", onUp, true);
    window.addEventListener("blur", cancel);
    return () => {
      window.removeEventListener("keydown", onDown, true);
      window.removeEventListener("keyup", onUp, true);
      window.removeEventListener("blur", cancel);
    };
  }, [mode]);

  if (!shown) return null;
  return (
    <Modal
      onClose={() => {
        run.current = null;
        setShown(null);
      }}
      className="palette folders"
    >
      <div className="palette-list">
        {shown.list.map((f, i) => {
          const state = f.slug === slug ? "this one" : f.open?.waiting ? "waiting on you" : f.open?.working ? "Claude is working" : "";
          return (
            <button
              key={f.slug}
              className={cx("palette-row", "folder-hit", i === shown.at && "on")}
              onClick={() => {
                run.current = null;
                setShown(null);
                if (f.slug !== slug) location.href = `/${f.slug}/`;
              }}
            >
              <span className={cx("session-dot", "live-dv", f.open?.working && "busy", f.open?.waiting && "asking")} />
              <span className="sym">{f.name}</span>
              {state && <span className={cx("why", f.open?.waiting && "session-asking")}>{state}</span>}
              <span className="spacer" />
              <span className="dim loc">
                {LRM}
                {f.place}
              </span>
            </button>
          );
        })}
      </div>
      <div className="palette-foot dim">Let go to switch, Esc to stay</div>
    </Modal>
  );
}
