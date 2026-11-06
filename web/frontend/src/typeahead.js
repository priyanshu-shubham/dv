// Typing ahead of the echo, as mosh does: on a slow link, what is typed at a
// prompt shows at once, dimmed, until the shell's echo takes its place. The
// guesses are drawn over the terminal, never into it, so taking one back
// leaves the screen as the shell has it.
//
// A guess holds once the screen has the typing where it was guessed, the
// cursor where it left it. A run of typing starts unseen - after Enter may
// come a password prompt - and shows once the shell has echoed some of it.
// Typing into what was already on the line, a full-screen program, or one
// taking the mouse is left to the shell; guesses that keep proving wrong
// stop showing for a while.

const SLOW_MS = 40; // echoes this slow turn it on,
const FAST_MS = 20; // and this fast off again
const OVERDUE_MS = 250; // a guess unanswered this long shows on any link
const STRIKES = 3; // wrong guesses seen within a minute that bench it
const BENCH_MS = 30000;

// Characters one cell wide, which are drawn where they are typed.
const narrow = (c) => (c >= 0x20 && c < 0x7f) || (c >= 0xa0 && c < 0x300) || (c >= 0x370 && c < 0x530);

// typeAhead guesses for term, while on() says to. typed is told what is sent
// as it goes; cut, of what is sent without being guessed at.
export function typeAhead(term, on = () => true) {
  const screen = term.element.querySelector(".xterm-screen");
  const layer = document.createElement("div");
  layer.className = "term-guess";
  screen.appendChild(layer);

  let srtt = 0; // how long an echo takes, smoothed, as TCP does a round trip
  let rttvar = 0;
  let slow = false;
  // Runs of typing, oldest first: from a line's column lo, the text typed
  // there and the cursor in it, as guessed (ops, each key's) and as the
  // screen last had them (done).
  let runs = [];
  let open = null; // the run typing goes on, until a key it can't guess
  let strikes = [];
  let benched = 0;
  let timer = 0;

  const buf = () => term.buffer.active;
  const owned = () => buf().type === "alternate" || term.modes.mouseTrackingMode !== "none";
  const cursor = () => ({ row: buf().baseY + buf().cursorY, col: buf().cursorX });
  const scratch = buf().getNullCell();
  const cells = (row, col, n) => {
    const line = buf().getLine(row);
    let s = "";
    for (let x = col; x < Math.min(col + n, term.cols); x++) s += line?.getCell(x, scratch)?.getChars() || " ";
    return s;
  };
  const now = () => performance.now();
  // Past this, a guess is taken to be wrong: the echo would have come.
  const deadline = () => (srtt ? Math.min(Math.max(2 * srtt + 4 * rttvar, 500), 5000) : 2000);

  const drop = (r) => {
    r.marker.dispose();
    runs = runs.filter((x) => x !== r);
    if (open === r) open = null;
  };
  const clear = () => {
    for (const r of runs) r.marker.dispose();
    runs = [];
    open = null;
    clearTimeout(timer);
    draw();
  };

  const sample = (ms) => {
    if (!srtt) [srtt, rttvar] = [ms, ms / 2];
    else {
      rttvar = 0.75 * rttvar + 0.25 * Math.abs(srtt - ms);
      srtt = 0.875 * srtt + 0.125 * ms;
    }
    slow = srtt >= SLOW_MS || (slow && srtt > FAST_MS);
  };

  // The screen still as a run left it, with nothing of it waiting on the shell.
  const settled = (r) => {
    const at = cursor();
    return at.row === r.marker.line && at.col === r.lo + r.done.cur && cells(at.row, r.lo, r.done.text.length) === r.done.text;
  };
  // run is what a key goes on: the open run, a new one at the cursor for a
  // character, or null if it is not to be guessed.
  const run = (fresh) => {
    if (open && !open.dead && !open.ops.length && !settled(open)) drop(open);
    if (open || !fresh) return open?.dead ? null : open;
    const marker = term.registerMarker(0);
    if (!marker) return null;
    const { row, col } = cursor();
    // What the line has from the cursor on: typing in front of it shifts it
    // along, which the first echo shows, as a suggestion it types over doesn't.
    const after = cells(row, col, term.cols).trimEnd();
    open = { marker, lo: col, text: "", cur: 0, done: { text: "", cur: 0 }, ops: [], after, seen: false };
    runs.push(open);
    return open;
  };
  const guess = (r, text, cur) => {
    r.text = text;
    r.cur = cur;
    r.ops.push({ text, cur, at: now() });
  };
  const cut = () => (open = null);

  const char = (ch) => {
    const r = run(true);
    if (!r) return;
    // Off the last column, past which the line wraps.
    if (r.lo + r.text.length + 1 >= term.cols) return cut();
    guess(r, r.text.slice(0, r.cur) + ch + r.text.slice(r.cur), r.cur + 1);
  };
  // Backspace and the arrows within what the run typed, the rest of the line
  // being the shell's to know.
  const back = () => {
    const r = run(false);
    if (!r || !r.cur) return cut();
    guess(r, r.text.slice(0, r.cur - 1) + r.text.slice(r.cur), r.cur - 1);
  };
  const move = (by) => {
    const r = run(false);
    if (!r || r.cur + by < 0 || r.cur + by > r.text.length) return cut();
    guess(r, r.text, r.cur + by);
  };

  const typed = (s) => {
    if (!on() || owned()) return runs.length ? clear() : cut();
    for (let i = 0; i < s.length; ) {
      const arrow = /^\x1b[[O]([CD])/.exec(s.slice(i, i + 3));
      if (arrow) {
        move(arrow[1] === "C" ? 1 : -1);
        i += 3;
        continue;
      }
      const c = s.codePointAt(i);
      // Any other sequence, and what comes after it, is the shell's.
      if (c === 0x1b) {
        cut();
        break;
      }
      if (c === 0x7f || c === 0x08) back();
      else if (narrow(c)) char(String.fromCodePoint(c));
      else cut();
      i += c > 0xffff ? 2 : 1;
    }
    draw();
    arm();
  };

  const confirm = (r, i) => {
    const op = r.ops[i];
    sample(now() - op.at);
    r.ops = r.ops.slice(i + 1);
    r.done = op;
    if (r.after && !r.checked) {
      r.checked = true;
      if (cells(r.marker.line, r.lo + op.text.length, r.after.length) === r.after) {
        r.dead = true;
        r.ops = [];
      }
    }
    if (!r.dead) r.seen = true;
    if (!r.ops.length && r !== open) drop(r);
  };
  const miss = (r, strike) => {
    if (strike && r.drawn) {
      const t = now();
      strikes = [...strikes.filter((s) => t - s < 60000), t];
      if (strikes.length >= STRIKES) [benched, strikes] = [t + BENCH_MS, []];
    }
    drop(r);
  };

  // check holds the screen, as the shell has just left it, to what was guessed.
  const check = () => {
    if (!runs.length) return;
    if (owned()) return clear();
    const at = cursor();
    for (const r of [...runs]) {
      const row = r.marker.line;
      if (r.marker.isDisposed || row < 0) {
        drop(r);
        continue;
      }
      if (!r.ops.length) {
        if (r !== open) drop(r);
        continue;
      }
      const real = cells(row, r.lo, term.cols);
      const is = (s) => at.row === row && at.col === r.lo + s.cur && real.startsWith(s.text);
      let hit = r.ops.findLastIndex(is);
      // The cursor where a key would leave it, but not the line: wrong, unless
      // the shell has yet to take the next key, an arrow back to the same place.
      const off = hit < 0 && !is(r.done) && r.ops.some((s) => at.row === row && at.col === r.lo + s.cur);
      // A run a key it couldn't guess came after - Enter, Tab - has the cursor
      // wherever that put it, but what was typed on the line. Missing, it may
      // be that key's doing, which is not held against the guessing.
      const ended = r !== open;
      if (hit < 0 && ended && real.startsWith(r.ops.at(-1).text)) hit = r.ops.length - 1;
      if (hit >= 0) confirm(r, hit);
      else if (off) miss(r, true);
      else if (ended && at.row !== row) miss(r, false);
      else if (now() - r.ops[0].at > deadline()) miss(r, !ended);
    }
    draw();
    arm();
  };
  // arm checks again as the oldest guess runs out of time, or is overdue to show.
  const arm = () => {
    clearTimeout(timer);
    const waiting = runs.filter((r) => r.ops.length);
    if (!waiting.length) return;
    const t = now();
    const next = Math.min(...waiting.map((r) => r.ops[0].at + (r.seen && !slow && t - r.ops[0].at < OVERDUE_MS ? OVERDUE_MS : deadline())));
    timer = setTimeout(check, Math.max(0, next - t) + 5);
  };

  const draw = () => {
    layer.textContent = "";
    const t = now();
    if (!runs.some((r) => r.seen && r.ops.length) || !on() || t < benched || owned()) return;
    const b = buf();
    const w = screen.clientWidth / term.cols;
    const h = screen.clientHeight / term.rows;
    if (!w || !h) return;
    const shown = new Set();
    const cell = (col, y, ch, kind) => {
      const s = document.createElement("span");
      if (kind) s.className = kind;
      s.textContent = ch;
      s.style.cssText = `left:${col * w}px;top:${y * h}px;width:${w}px;height:${h}px`;
      layer.appendChild(s);
      shown.add(`${y}:${col}`);
    };
    let last = null;
    for (const r of runs) {
      if (!r.seen || !r.ops.length || !(slow || t - r.ops[0].at >= OVERDUE_MS)) continue;
      const y = r.marker.line - b.viewportY;
      if (y < 0 || y >= term.rows) continue;
      r.drawn = true;
      const want = r.ops.at(-1);
      const real = cells(r.marker.line, r.lo, Math.max(want.text.length, r.done.text.length));
      for (let k = 0; k < real.length; k++) {
        const ch = want.text[k] ?? " ";
        if (!(k < r.done.text.length && ch === real[k])) cell(r.lo + k, y, ch, "guess");
      }
      last = { r, y, want };
    }
    if (!last) return;
    const theme = term.options.theme || {};
    const o = term.options;
    layer.style.cssText = `font:${o.fontWeight} ${o.fontSize}px ${o.fontFamily};line-height:${h}px;--guess-fg:${theme.foreground};--guess-bg:${theme.background};--guess-cursor:${theme.cursor};--guess-accent:${theme.cursorAccent}`;
    // The cursor where the typing leaves it, the terminal's own hidden, but for
    // a newer run still unseen, whose place isn't known.
    if (last.r !== runs.at(-1)) return;
    const { r, y, want } = last;
    const col = r.lo + want.cur;
    const at = cursor();
    const ay = at.row - b.viewportY;
    if (ay >= 0 && ay < term.rows && !(ay === y && at.col === col) && !shown.has(`${ay}:${at.col}`)) cell(at.col, ay, cells(at.row, at.col, 1), "");
    // Shaped as the program last set it, which xterm keeps to itself.
    const shape = term._core?.coreService?.decPrivateModes?.cursorStyle ?? o.cursorStyle ?? "block";
    cell(col, y, want.text[want.cur] ?? cells(r.marker.line, col, 1), `cursor ${shape}`);
  };

  const subs = [
    term.onWriteParsed(check),
    term.onRender(() => layer.firstChild && draw()),
    term.onResize(clear),
    term.buffer.onBufferChange(clear),
  ];
  return {
    typed,
    cut,
    clear,
    // seed takes a round trip timed otherwise, until echoes are.
    seed: (ms) => srtt || sample(ms),
    stop() {
      clear();
      for (const s of subs) s.dispose();
      layer.remove();
    },
  };
}
