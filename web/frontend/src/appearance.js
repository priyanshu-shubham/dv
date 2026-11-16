// Settings → Appearance put on the page, as attributes on <html> that
// styles.css keys its rules to and a few values beside them. The server puts
// the same on the page it sends (pageAppearance in server.go), so the first
// frame is already drawn in them.
import { useEffect } from "react";
import { setTabIcon, TAB_COLORS } from "./favicon.js";

// Each theme's shades and accent, as styles.css has them: the ground a
// terminal takes, the accent the tab takes when none is picked, and the rest
// for Settings' small pictures of a look.
export const THEMES = {
  dark: { name: "Near-black", bg: "#0a0a0a", panel: "#141414", bar: "#1e1e1e", fg: "#d6d6d6", accent: "#5b9dff" },
  graphite: { name: "Graphite", bg: "#121212", panel: "#191919", bar: "#1f1f1f", fg: "#e6e6e6", accent: "#a78bfa" },
  obsidian: { name: "Obsidian", bg: "#050505", panel: "#0e0e0e", bar: "#151515", fg: "#e4e4e4", accent: "#2dd4bf" },
  ember: { name: "Ember", bg: "#0c0a08", panel: "#15120f", bar: "#1c1814", fg: "#ebe3d9", accent: "#f2a65a" },
  light: { name: "Light", bg: "#ffffff", panel: "#f3f3f3", bar: "#e9e9e9", fg: "#1f1f1f", accent: "#0969da" },
};

export const ACCENTS = ["#5b9dff", "#a78bfa", "#2dd4bf", "#4ade80", "#f2a65a", "#f472b6", "#f87171"];

// The families are styles.css's, by data-ui-font and data-code-font; these
// name them in Settings, each in its own face.
export const UI_FONTS = {
  system: ["System", ""],
  inter: ["Inter", '"Inter Variable"'],
  geist: ["Geist", '"Geist Variable"'],
  plex: ["IBM Plex Sans", '"IBM Plex Sans Variable"'],
};
export const CODE_FONTS = {
  jetbrains: ["JetBrains Mono", '"JetBrains Mono Variable"'],
  geist: ["Geist Mono", '"Geist Mono Variable"'],
  fira: ["Fira Code", '"Fira Code Variable"'],
  system: ["System", "ui-monospace, monospace"],
};
export const CODE_SIZES = [11, 12, 13, 14, 15];

// The looks Settings starts from: a theme and the switches below it. Not the
// accent, the tab or the code size, which are the computer's and the reader's.
export const LOOK_KEYS = ["density", "corners", "glass", "glow", "changes", "uiFont", "codeFont"];
export const LOOKS = {
  default: { name: "Default", theme: "dark" },
  graphite: { name: "Graphite", theme: "graphite", density: "roomy", corners: "round", uiFont: "geist", codeFont: "geist" },
  glass: { name: "Glass", theme: "obsidian", corners: "round", glass: true, glow: "soft", changes: "gutter", uiFont: "inter" },
  ember: { name: "Ember", theme: "ember", density: "compact", corners: "sharp", uiFont: "plex" },
  light: { name: "Light", theme: "light" },
  frost: { name: "Frost", theme: "light", corners: "round", glass: true, glow: "soft", changes: "light", uiFont: "geist", codeFont: "geist" },
};

// The Name in the tab is a word or two: letters, digits, . _ and -, single
// spaces, NAME_MAX at most. app.go's appName keeps the installed app's alike.
export const NAME_MAX = 20;
export const cleanName = (v) => [...(v || "").replace(/[^\p{L}\p{N} ._-]/gu, "").replace(/^ +/, "").replace(/ {2,}/g, " ")].slice(0, NAME_MAX).join("");
export const tabName = (s) => cleanName(s.tabName).trim() || "dv";

export const lookOn =(look, theme, s) => look.theme === theme && LOOK_KEYS.every((k) => (look[k] ?? undefined) === (s[k] ?? undefined));

// Before the accent there was only the tab's colour, from TAB_COLORS by name:
// it stands for both until an accent or tab icon is picked.
export const accentOf = (s) => (/^#[0-9a-f]{6}$/i.test(s.accent || "") ? s.accent.toLowerCase() : TAB_COLORS[s.tabColor] || "");
export function tabMark(theme, s) {
  const style = s.tabIcon ?? (s.tabColor ? "ring" : "");
  return { style, color: accentOf(s) || (THEMES[theme] || THEMES.dark).accent };
}

const word = (v) => (typeof v === "string" && /^[a-z]+$/.test(v) ? v : undefined);

// appearanceOf is what goes on <html>: attributes (each a word, or absent) and
// custom properties.
export function appearanceOf(theme, s) {
  const accent = accentOf(s);
  const attrs = {
    theme: word(theme),
    code: word(s.codeColors) || "github",
    density: word(s.density),
    corners: word(s.corners),
    changes: word(s.changes),
    glow: word(s.glow),
    glass: s.glass ? "on" : undefined,
    motion: s.motion === false ? "off" : undefined,
    "ui-font": word(s.uiFont),
    "code-font": word(s.codeFont),
    "code-size": CODE_SIZES.includes(s.codeSize) ? String(s.codeSize) : undefined,
    accent: accent ? "on" : undefined,
  };
  const vars = { "--tab-mark": tabMark(theme, s).color };
  if (accent) vars["--pick"] = accent;
  if (s.glass) {
    vars["--blur"] = `${clamp(s.glassBlur ?? 16, 0, 40)}px`;
    vars["--glass-a"] = String(clamp(s.glassOpacity ?? 60, 25, 90) / 100);
  }
  return { attrs, vars };
}

const clamp = (v, lo, hi) => Math.min(hi, Math.max(lo, Number(v) || 0));

// A code line's height, as styles.css's --code-lh works it out: the first
// guess at the height of rows not yet drawn.
const RATIOS = { compact: 1.5, roomy: 1.85 };
let codeRow = 20;
export const codeRowHeight = () => (document.documentElement.classList.contains("phone") ? 20 : codeRow);

export function useAppearance(theme, settings) {
  useEffect(() => {
    const html = document.documentElement;
    const { attrs, vars } = appearanceOf(theme, settings);
    for (const [k, v] of Object.entries(attrs)) {
      const name = "data-" + k;
      if (v) html.setAttribute(name, v);
      else html.removeAttribute(name);
    }
    for (const k of ["--tab-mark", "--pick", "--blur", "--glass-a"]) {
      if (vars[k]) html.style.setProperty(k, vars[k]);
      else html.style.removeProperty(k);
    }
    codeRow = Math.round((attrs["code-size"] || 12) * (RATIOS[attrs.density] || 1.667));
  }, [theme, settings]);
}

// useTabIcon draws the tab's icon, dot and all.
export function useTabIcon(theme, settings, dot) {
  const { style, color } = tabMark(theme, settings);
  useEffect(() => setTabIcon({ style, color }, dot), [style, color, dot]);
}
