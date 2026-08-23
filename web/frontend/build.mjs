// Bundles the review UI into the assets the Go binary embeds. `npm run build`
// for a one-shot production bundle, `npm run watch` while iterating on the UI.
import * as esbuild from "esbuild";
import { mkdirSync, readdirSync, rmSync } from "node:fs";

const outdir = "../../internal/server/static";
const watch = process.argv.includes("--watch");

mkdirSync(outdir, { recursive: true });
// Content-hashed chunks accumulate across builds; clear the stale ones so they
// are not embedded into the binary as dead weight.
for (const f of readdirSync(outdir)) {
  if (f.startsWith("chunk-") && f.endsWith(".js")) rmSync(`${outdir}/${f}`);
}

const js = {
  entryPoints: ["src/main.jsx"],
  outdir,
  entryNames: "bundle",
  chunkNames: "chunk-[hash]",
  bundle: true,
  format: "esm",
  splitting: true,
  jsx: "automatic",
  minify: !watch,
  sourcemap: watch,
  logLevel: "info",
  define: { "process.env.NODE_ENV": watch ? '"development"' : '"production"' },
};

const css = {
  entryPoints: ["src/styles.css"],
  outfile: `${outdir}/bundle.css`,
  bundle: true,
  minify: !watch,
  logLevel: "info",
};

if (watch) {
  const a = await esbuild.context(js);
  const b = await esbuild.context(css);
  await Promise.all([a.watch(), b.watch()]);
  console.log("watching…");
} else {
  await esbuild.build(js);
  await esbuild.build(css);
  console.log("bundle written to", outdir);
}
