// The tab's icon: dv's own, or ringed in a colour so that dv on one computer is
// told from dv on another at a glance, where the tab is too narrow for its
// title. index.html has the plain one too, for the moment before this runs.
export const TAB_COLORS = {
  blue: "#2f81f7",
  purple: "#8957e5",
  pink: "#db61a2",
  orange: "#e0823d",
  yellow: "#d4a72c",
  teal: "#1f9e9e",
};

// A dot for what needs the reader: an agent waiting on them, or a turn that
// finished while they were away. The dark theme's --mod and --add, as the tab
// strip is the browser's rather than the page's theme.
const DOTS = { ask: "#d29922", done: "#3fb950" };

export function tabIconURL(color, dot) {
  const c = TAB_COLORS[color];
  // Drawn after the rest, so the divider running to the edge does not cut it.
  const ring = c ? `<rect x="1.75" y="1.75" width="28.5" height="28.5" rx="7" fill="none" stroke="${c}" stroke-width="3.5"/>` : "";
  const badge = DOTS[dot] ? `<circle cx="25" cy="7" r="7.5" fill="${DOTS[dot]}" stroke="#141414" stroke-width="2.5"/>` : "";
  const svg =
    `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32">` +
    `<rect x="1" y="1" width="30" height="30" rx="7" fill="#141414" stroke="#3e3e3e" stroke-width="2"/>` +
    `<rect x="14" y="2" width="4" height="28" fill="#2c2c2c"/>` +
    `<g fill="#6a6a6a"><rect x="6" y="8" width="6" height="4"/><rect x="20" y="8" width="6" height="4"/>` +
    `<rect x="6" y="20" width="6" height="4"/><rect x="20" y="20" width="6" height="4"/></g>` +
    `<rect x="6" y="14" width="6" height="4" fill="#f85149"/><rect x="20" y="14" width="6" height="4" fill="#3fb950"/>` +
    ring +
    badge +
    `</svg>`;
  return "data:image/svg+xml," + encodeURIComponent(svg);
}

// tabIconJPEG is the icon as a picture for the Telegram bot, in base64: 640
// pixels square, as Telegram likes them, on a dark ground with the icon clear
// of the circle Telegram crops it to.
export async function tabIconJPEG(color) {
  const icon = new Image();
  icon.src = tabIconURL(color);
  await icon.decode();
  const canvas = document.createElement("canvas");
  canvas.width = canvas.height = 640;
  const g = canvas.getContext("2d");
  g.fillStyle = "#0b0b0b";
  g.fillRect(0, 0, 640, 640);
  g.drawImage(icon, 120, 120, 400, 400);
  const blob = await new Promise((done) => canvas.toBlob(done, "image/jpeg", 0.92));
  let bytes = "";
  for (const b of new Uint8Array(await blob.arrayBuffer())) bytes += String.fromCharCode(b);
  return btoa(bytes);
}

export function setTabIcon(color, dot) {
  const link = document.querySelector('link[rel="icon"]');
  if (link) link.href = tabIconURL(color, dot);
}

// The notices are the whole hub's, so any tab of a hub tells of them all.
export function tabDot(notices) {
  if (notices.some((n) => n.kind === "ask")) return "ask";
  return notices.some((n) => n.kind === "done") ? "done" : "";
}
