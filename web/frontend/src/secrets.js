import { createElement, useSyncExternalStore } from "react";
import { api } from "./api.js";

// The server shows a masked secret as the model saw it, [[dv-secret:NAME]];
// the page draws it as a chip with a glimpse of the value.
export const SECRET = /\[\[dv-secret:([A-Za-z0-9_]{1,64})\]\]/g;

let list = [];
let version = 0;
let asked = false;
const listeners = new Set();

function changed(next) {
  list = next;
  version++;
  listeners.forEach((f) => f());
}

// reloadSecrets asks for the list again, as after adding or removing one.
export const reloadSecrets = () => api.secrets().then(changed, () => {});

// useSecretsVersion changes with the list, for what is drawn from it to be
// drawn again: a session's conversation, asked for anew, comes masked by it.
export function useSecretsVersion() {
  if (!asked) {
    asked = true;
    reloadSecrets();
  }
  return useSyncExternalStore(
    (f) => (listeners.add(f), () => listeners.delete(f)),
    () => version,
  );
}

// useSecrets is the list, [{ name, preview }], asked for once in the page.
export function useSecrets() {
  useSecretsVersion();
  return list;
}

// guessName names a secret by the shape of its value, where that says whose it is.
export function guessName(value) {
  const shapes = [
    [/^sk-ant-/, "ANTHROPIC_API_KEY"],
    [/^sk-/, "OPENAI_API_KEY"],
    [/^(ghp_|gho_|ghs_|github_pat_)/, "GITHUB_TOKEN"],
    [/^glpat-/, "GITLAB_TOKEN"],
    [/^xox[abposr]-/, "SLACK_TOKEN"],
    [/^(AKIA|ASIA)[A-Z0-9]{12,}$/, "AWS_ACCESS_KEY_ID"],
    [/^AIza/, "GOOGLE_API_KEY"],
    [/^npm_/, "NPM_TOKEN"],
    [/^eyJ[\w-]+\.[\w-]+\./, "JWT"],
  ];
  const taken = new Set(list.map((s) => s.name));
  let name = shapes.find(([re]) => re.test(value))?.[1] || "SECRET";
  for (let n = 2; taken.has(name); n++) name = name.replace(/_\d+$/, "") + "_" + n;
  return name;
}

// glimpse is what the page shows of a value, as the server's preview has it.
export function glimpse(v) {
  if (v.length >= 20) return v.slice(0, 4) + "••••" + v.slice(-4);
  if (v.length >= 12) return v.slice(0, 2) + "••••" + v.slice(-2);
  return "••••••";
}

// hashesAsSecrets has each #NAME of a masked secret in a message as its
// placeholder, which the model uses the secret by.
export const hashesAsSecrets = (text) =>
  text.replace(/(^|\s)#([A-Za-z0-9_]+)\b/g, (whole, before, name) => (list.some((s) => s.name === name) ? `${before}[[dv-secret:${name}]]` : whole));

// maskable says whether selected text could be a secret: one line, no spaces.
export const maskable = (text) => text.length >= 6 && text.length <= 512 && !/\s/.test(text);

const previewOf = (name) => list.find((s) => s.name === name)?.preview || "••••••";

// Only a masked secret's placeholder is drawn as a chip: one with another
// name is text that says it, as dv's own source and docs do.
const masked = (name) => list.some((s) => s.name === name);

const escape = (s) => s.replace(/[&<>"]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;" })[c]);

const chipHTML = (name) => `<span class="secret-chip" data-secret="${name}" tabindex="0">${escape(previewOf(name))}</span>`;

// secretsInHTML draws the placeholders in HTML the page renders as is.
export const secretsInHTML = (html) => (html.includes("[[dv-secret:") ? html.replace(SECRET, (whole, name) => (masked(name) ? chipHTML(name) : whole)) : html);

// A highlighter could split a placeholder's brackets into tokens of their
// own, so code takes a word in its place while highlighted.
const WORD = /__dvsecret__([A-Za-z0-9_]+?)__dvsecret__/g;
export const secretsAsWords = (code) => (code.includes("[[dv-secret:") ? code.replace(SECRET, "__dvsecret__$1__dvsecret__") : code);
export const secretWordsInHTML = (html) => (html.includes("__dvsecret__") ? html.replace(WORD, (_, name) => (masked(name) ? chipHTML(name) : `[[dv-secret:${name}]]`)) : html);

// secretsInText is plain text with its placeholders as chips, for React.
export function secretsInText(text, wrap = (s) => s) {
  if (typeof text !== "string" || !text.includes("[[dv-secret:")) return wrap(text);
  const out = [];
  let at = 0;
  for (const m of text.matchAll(SECRET)) {
    if (!masked(m[1])) continue;
    if (m.index > at) out.push(wrap(text.slice(at, m.index), at));
    out.push(createElement("span", { key: "s" + m.index, className: "secret-chip", "data-secret": m[1], tabIndex: 0 }, previewOf(m[1])));
    at = m.index + m[0].length;
  }
  if (at < text.length) out.push(wrap(text.slice(at), at));
  return out.flat();
}
