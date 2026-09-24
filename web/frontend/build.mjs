// Bundles the review UI into the assets the Go binary embeds. `npm run build`
// for a one-shot production bundle, `npm run watch` while iterating on the UI.
import * as esbuild from "esbuild";
import { cpSync, mkdirSync, readdirSync, readFileSync, rmSync } from "node:fs";

const outdir = "../../internal/server/static";
const watch = process.argv.includes("--watch");

// PDF.js's worker and the data some PDFs need, fetched only when a PDF is
// shown. The folder is named for the version, so the server can cache it for good.
const pdfjsDir = "node_modules/pdfjs-dist";
const pdfjs = "pdfjs-" + JSON.parse(readFileSync(`${pdfjsDir}/package.json`)).version;

mkdirSync(outdir, { recursive: true });
// Content-hashed chunks accumulate across builds; clear the stale ones so they
// are not embedded into the binary as dead weight.
for (const f of readdirSync(outdir)) {
  if ((f.startsWith("chunk-") && f.endsWith(".js")) || (f.startsWith("pdfjs-") && f !== pdfjs)) rmSync(`${outdir}/${f}`, { recursive: true });
}
cpSync(`${pdfjsDir}/build/pdf.worker.min.mjs`, `${outdir}/${pdfjs}/pdf.worker.min.mjs`);
cpSync(`${pdfjsDir}/cmaps`, `${outdir}/${pdfjs}/cmaps`, { recursive: true });
cpSync(`${pdfjsDir}/iccs`, `${outdir}/${pdfjs}/iccs`, { recursive: true });
// The decoders for JPEG 2000, JBIG2 and colour profiles; not the fallbacks for
// browsers without WebAssembly, nor QuickJS, which runs a PDF's own scripts.
for (const f of ["jbig2.wasm", "openjpeg.wasm", "qcms_bg.wasm"]) cpSync(`${pdfjsDir}/wasm/${f}`, `${outdir}/${pdfjs}/wasm/${f}`);

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
  define: { "process.env.NODE_ENV": watch ? '"development"' : '"production"', __PDFJS__: JSON.stringify(pdfjs) },
};

const css = {
  entryPoints: ["src/styles.css"],
  outfile: `${outdir}/bundle.css`,
  bundle: true,
  // Unhashed, so index.html can preload the one every page needs.
  loader: { ".woff2": "file" },
  assetNames: "[name]",
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
