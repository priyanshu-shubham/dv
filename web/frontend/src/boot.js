// What the server writes into the page: where its API is (base, "/<slug>" in a
// hub), which page this is, and the settings kept on disk, so the first frame
// is drawn with them.
const el = document.getElementById("dv-boot");
export const boot = { base: "", prefs: {}, ...(el && JSON.parse(el.textContent)) };

// A hub serves every folder from one origin, so what a folder keeps in the
// browser is told apart by its slug.
export const slug = boot.base.replace(/^\//, "");
