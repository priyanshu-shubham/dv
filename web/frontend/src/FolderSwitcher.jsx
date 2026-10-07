import { useEffect, useMemo, useRef, useState } from "react";
import { api } from "./api.js";
import { boot, slug } from "./boot.js";
import { IconSearch } from "./icons.jsx";
import { say } from "./Notices.jsx";
import { markMatches, Modal, usePaletteNav } from "./Overlays.jsx";
import { cx, isMac, isTyping, LRM, workingLabel } from "./util.js";

const RECENT = "dv:hubRecent";

// held is whether the key a switch began with is still down.
const held = (e, hold) => (hold === "Control" ? e.ctrlKey : hold === "Meta" ? e.metaKey : e.shiftKey);

const readRecent = () => {
  try {
    return JSON.parse(localStorage.getItem(RECENT) || "[]");
  } catch {
    return [];
  }
};

// byRecent orders folders by when this browser was last in each, this one
// first, so a step lands on another; those it was never in, as the hub last
// had them in use.
function byRecent(folders) {
  const recent = readRecent();
  const rank = (f) => (f.slug === slug ? -1 : recent.includes(f.slug) ? recent.indexOf(f.slug) : recent.length);
  return folders.sort((a, b) => rank(a) - rank(b) || (Date.parse(b.used) || 0) - (Date.parse(a.used) || 0));
}

const wordStart = (s, i) => i === 0 || /[\s_\-./:]/.test(s[i - 1]) || (/[a-z]/.test(s[i - 1]) && /[A-Z]/.test(s[i]));

// fuzzy finds q's letters in order in text as Go to file does: a prefix over a
// run, a run over scattered letters, which count for more at a word's start.
// [score, positions], or null; q is in lower case.
function fuzzy(q, text) {
  const t = text.toLowerCase();
  const at = t.indexOf(q);
  if (at >= 0) {
    const score = (at === 0 ? 5000 : 3000 - at * 4 + (wordStart(text, at) ? 800 : 0)) - text.length;
    return [score, Array.from(q, (_, k) => at + k)];
  }
  const hits = [];
  let score = 1000 - text.length;
  for (let k = 0, from = 0; k < q.length; k++) {
    const j = t.indexOf(q[k], from);
    if (j < 0) return null;
    const prev = hits.at(-1) ?? -2;
    score += (j === prev + 1 ? 25 : (prev - j) * 2) + (wordStart(text, j) ? 60 : 0);
    hits.push(j);
    from = j + 1;
  }
  return [score, hits];
}

// A name is what people type, so a term found in it outranks one that needed the place.
const IN_NAME = 20000;

// findFolders keeps the folders matching every word of q, best first; equals keep their order.
function findFolders(folders, q) {
  const terms = q.toLowerCase().split(/\s+/).filter(Boolean);
  const hit = (f) => {
    const h = { f, score: 0, name: [], place: [] };
    for (const t of terms) {
      const n = fuzzy(t, f.name);
      const m = n || fuzzy(t, f.place);
      if (!m) return null;
      h.score += m[0] + (n ? IN_NAME : 0);
      h[n ? "name" : "place"].push(...m[1]);
    }
    return h;
  };
  return folders
    .map(hit)
    .filter(Boolean)
    .sort((a, b) => b.score - a.score);
}

// A folder open in a dv started on its own is that dv's, as on the hub's page.
function goTo(f) {
  if (f.elsewhere) window.open(f.elsewhere, "_blank", "noopener");
  else if (f.slug !== slug) location.href = `/${f.slug}/`;
}

function FolderRow({ hit: { f, name, place }, on, onPick, onHover }) {
  const state =
    f.slug === slug ? "this one" : f.open?.waiting ? "waiting on you" : f.open?.working ? workingLabel(f.open) : f.elsewhere ? "open in another dv" : "";
  return (
    <button className={cx("palette-row", "folder-hit", on && "on")} onMouseMove={onHover} onClick={onPick}>
      <span className={cx("session-dot", f.open && "live-dv", f.elsewhere && "live-terminal", f.open?.working && "busy", f.open?.waiting && "asking")} />
      <span className="sym" dangerouslySetInnerHTML={{ __html: markMatches(f.name, name) }} />
      {state && <span className={cx("why", f.open?.waiting && "session-asking")}>{state}</span>}
      <span className="spacer" />
      <span className="dim loc" dangerouslySetInnerHTML={{ __html: LRM + markMatches(f.place, place) }} />
    </button>
  );
}

// FindFolder searches every folder on the hub, open or not, by name or place.
function FindFolder({ folders, onClose }) {
  const [q, setQ] = useState("");
  const hits = useMemo(() => findFolders(folders, q), [folders, q]);
  const pick = (i) => {
    onClose();
    goTo(hits[i].f);
  };
  const { sel, setSel, onKey, listRef } = usePaletteNav(hits.length, pick);
  useEffect(() => setSel(0), [q]);
  return (
    <Modal onClose={onClose} className="palette folders">
      <div className="palette-input">
        <IconSearch size={15} />
        <input autoFocus value={q} placeholder="Go to folder..." onChange={(e) => setQ(e.target.value)} onKeyDown={onKey} />
      </div>
      <div className="palette-list" ref={listRef}>
        {hits.map((h, i) => (
          <FolderRow key={h.f.slug} hit={h} on={i === sel} onHover={() => setSel(i)} onPick={() => pick(i)} />
        ))}
        {hits.length === 0 && <div className="empty">No folders match.</div>}
      </div>
      <div className="palette-foot dim">
        {q.trim() ? `${hits.length} of ${folders.length} folders match` : "Every other folder on the hub, last used first"}
      </div>
    </Modal>
  );
}

// FolderSwitcher goes between the folders open in the hub serving the page, as
// Alt+Tab goes between windows: Ctrl+Shift+Down, held, lists them by when this
// browser was last in each, the one before this picked; Down and Up move along
// and letting go switches, so a tap goes back and forth between two. In Diff
// and Files, where Shift+Up and Down do nothing else, Shift is enough; the Agent
// view steps through its sessions with them. On a Mac, Cmd holds it as Ctrl
// does, which is what the page's other keys use; the switch has the arrows
// while it is up, so neither selects text in a box.
// Past the last open folder is a search of all of them, which a tap of Up
// lands on; with no other folder open, the keys go straight to it.
export default function FolderSwitcher({ mode }) {
  const [shown, setShown] = useState(null); // { list, all, at }
  const [finding, setFinding] = useState(null); // every other folder on the hub
  const run = useRef(null); // { hold, at, list, all, released }: the switch under way

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

  // While searching, the arrows are the search box's.
  useEffect(() => {
    if (!boot.base || finding) return;
    const cancel = () => {
      run.current = null;
      setShown(null);
    };
    // One row past the folders is the search.
    const at = (r) => ((r.at % (r.list.length + 1)) + r.list.length + 1) % (r.list.length + 1);
    const go = (r) => {
      const i = at(r);
      cancel();
      if (i < r.list.length) goTo(r.list[i]);
      else setFinding(r.all);
    };

    const onDown = (e) => {
      if (e.key !== "ArrowUp" && e.key !== "ArrowDown") return;
      const step = e.key === "ArrowDown" ? 1 : -1;
      const r = run.current;
      if (r) {
        if (!held(e, r.hold)) return;
        e.preventDefault();
        e.stopPropagation();
        r.at += step;
        if (r.list) setShown({ list: r.list, all: r.all, at: at(r) });
        return;
      }
      if (!e.shiftKey || e.altKey || (e.metaKey && !isMac)) return;
      const mod = e.ctrlKey || e.metaKey;
      if (!mod && (mode === "agent" || isTyping(e.target) || document.querySelector(".backdrop, .prompt-backdrop:not([hidden])"))) return;
      e.preventDefault();
      e.stopPropagation();
      const started = { hold: e.ctrlKey ? "Control" : e.metaKey ? "Meta" : "Shift", at: step, list: null, all: null, released: false };
      run.current = started;
      api.hubFolders().then(
        ({ folders }) => {
          if (run.current !== started) return;
          const list = byRecent(folders.filter((f) => !f.missing));
          started.list = list.filter((f) => f.open || f.slug === slug);
          started.all = list.filter((f) => f.slug !== slug);
          if (started.list.length < 2) {
            cancel();
            if (started.all.length) setFinding(started.all);
            else say("No other folder on the hub", "Folders added to the hub are listed here.");
          } else if (started.released) go(started);
          else setShown({ list: started.list, all: started.all, at: at(started) });
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
  }, [mode, finding]);

  if (finding) return <FindFolder folders={finding} onClose={() => setFinding(null)} />;
  if (!shown) return null;
  const close = () => {
    run.current = null;
    setShown(null);
  };
  return (
    <Modal onClose={close} className="palette folders">
      <div className="palette-list">
        {shown.list.map((f, i) => (
          <FolderRow
            key={f.slug}
            hit={{ f }}
            on={i === shown.at}
            onPick={() => {
              close();
              goTo(f);
            }}
          />
        ))}
        <button
          className={cx("palette-row", shown.at === shown.list.length && "on")}
          onClick={() => {
            close();
            setFinding(shown.all);
          }}
        >
          <span className="dim">Find any folder…</span>
        </button>
      </div>
      <div className="palette-foot dim">Let go to switch, Esc to stay</div>
    </Modal>
  );
}
