// Mermaid diagrams, drawn in place of their code wherever Markdown is: what
// agents say (Codex is told to prefer Mermaid for a small diagram), comments,
// notes and Markdown files. Mermaid is large, so it is fetched the first time
// a diagram is shown. The HTML they sit in is React's, replaced whenever it
// changes, so each drawing is kept by its source and put back at once.

let mermaid = null;
let loading = null;
let queue = Promise.resolve(); // Mermaid draws one at a time
let configured = "";
let seq = 0;
const drawn = new Map(); // theme + "\n" + source -> { svg } or { error }
let watching = false;

const theme = () => document.documentElement.dataset.theme || "dark";

const TOGGLE =
  '<svg viewBox="0 0 16 16" width="13" height="13" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5.5 2.5L2 8l3.5 5.5M10.5 2.5L14 8l-3.5 5.5"/></svg>';

// drawDiagrams draws the finished Mermaid code blocks under root. One still
// being written would be drawn over and over as it grows, if it parsed at all.
export function drawDiagrams(root) {
  if (!root || root.closest(".streaming")) return;
  for (const code of root.querySelectorAll("pre > code.language-mermaid")) {
    const pre = code.parentElement;
    if (pre.parentElement.classList.contains("mermaid-block")) continue;
    const box = document.createElement("div");
    box.className = "mermaid-block";
    // The box stands for the block now, for a jump to a line or a comment on it.
    if (pre.dataset.src) {
      box.dataset.src = pre.dataset.src;
      delete pre.dataset.src;
    }
    pre.before(box);
    box.append(pre);
    show(box);
  }
}

function show(box) {
  const src = box.querySelector("pre").textContent;
  const key = theme() + "\n" + src;
  const hit = drawn.get(key);
  if (hit) return put(box, hit);
  draw(src).then((r) => {
    drawn.set(key, r);
    if (drawn.size > 60) drawn.delete(drawn.keys().next().value);
    if (box.isConnected) put(box, r);
  });
}

function put(box, { svg, error }) {
  if (error) {
    const note = document.createElement("div");
    note.className = "mermaid-error";
    note.textContent = "Not drawn: " + error;
    box.append(note);
    return;
  }
  const diagram = document.createElement("div");
  diagram.className = "mermaid-diagram";
  diagram.innerHTML = svg;
  const toggle = document.createElement("button");
  toggle.className = "ghost mermaid-toggle";
  toggle.innerHTML = TOGGLE;
  const label = () => (toggle.title = box.classList.contains("source") ? "Show the diagram" : "Show the source");
  toggle.addEventListener("click", (e) => {
    e.stopPropagation();
    box.classList.toggle("source");
    label();
  });
  label();
  box.prepend(diagram);
  box.append(toggle);
  box.classList.add("drawn");
  watchTheme();
}

function draw(src) {
  const run = queue.then(async () => {
    try {
      await load();
      if (configured !== theme()) configure();
      await mermaid.parse(src);
      const { svg } = await mermaid.render("dv-mermaid-" + ++seq, src);
      return { svg };
    } catch (e) {
      // Mermaid's parse errors run to several lines: where, a pointer, what it expected.
      const lines = String(e?.message || e).split("\n").filter((l) => l.trim());
      return { error: lines.length > 1 ? `${lines[0]} ${lines.at(-1)}` : lines[0] || "Mermaid could not read it." };
    }
  });
  queue = run;
  return run;
}

function load() {
  loading ||= import("mermaid").then(
    (m) => (mermaid = m.default),
    (e) => {
      loading = null; // to try again next time
      throw e;
    },
  );
  return loading;
}

// configure has Mermaid draw in the page's own colours, read off it.
function configure() {
  const css = getComputedStyle(document.documentElement);
  const v = (name) => css.getPropertyValue(name).trim();
  const font = v("--font-ui");
  mermaid.initialize({
    startOnLoad: false,
    securityLevel: "strict",
    suppressErrorRendering: true,
    theme: "base",
    fontFamily: font,
    themeVariables: {
      darkMode: theme() !== "light",
      fontFamily: font,
      fontSize: "13px",
      background: v("--bg-panel"),
      primaryColor: v("--bg-raised"),
      primaryTextColor: v("--fg"),
      primaryBorderColor: v("--border-strong"),
      secondaryColor: v("--bg-bar"),
      tertiaryColor: v("--bg-panel"),
      lineColor: v("--fg-3"),
      textColor: v("--fg"),
      edgeLabelBackground: v("--bg-panel"),
      clusterBkg: v("--bg-panel"),
      clusterBorder: v("--border"),
      noteBkgColor: v("--bg-bar"),
      noteTextColor: v("--fg"),
      noteBorderColor: v("--border-strong"),
    },
  });
  configured = theme();
}

// watchTheme draws every diagram on the page again when the theme changes.
function watchTheme() {
  if (watching) return;
  watching = true;
  new MutationObserver(() => {
    for (const box of document.querySelectorAll(".mermaid-block")) {
      box.querySelectorAll(".mermaid-diagram, .mermaid-toggle, .mermaid-error").forEach((el) => el.remove());
      box.classList.remove("drawn", "source");
      show(box);
    }
  }).observe(document.documentElement, { attributeFilter: ["data-theme"] });
}
