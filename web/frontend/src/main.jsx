import { createRoot } from "react-dom/client";
import App from "./App.jsx";
import { boot, slug } from "./boot.js";
import { api } from "./api.js";
import { OUTPUT_KEY, PHONE_APP, PHONE_FOLDER_KEY, pickSession } from "./util.js";

// A link to a session, as a notice sent to the phone has, opens on it; one to
// a command action's output, as its notice's has, shows that.
const params = new URLSearchParams(location.search);
const linked = params.get("session");
const output = params.get("output");
if (linked || output) {
  if (boot.page !== "hub" && /^[0-9a-f-]{36}$/.test(linked)) pickSession(slug, linked);
  if (boot.page !== "hub" && output) sessionStorage.setItem(OUTPUT_KEY, output);
  history.replaceState(null, "", location.pathname + location.hash);
}

const root = createRoot(document.getElementById("root"));
const hub = () => import("./Hub.jsx").then(({ default: Hub }) => root.render(<Hub />));
// The phone app opens on the folder it was last in, as an app opens where it
// was left; /?folders is the hub's own page, as is a folder that would not
// open (?from), which would only be sent back here.
const asked = new URLSearchParams(location.search);
if (boot.page === "hub" && matchMedia(PHONE_APP).matches && !asked.has("folders") && !asked.has("from")) {
  api.hubFolders().then(({ folders = [] }) => {
    const used = (f) => Date.parse(f.used || f.added) || 0;
    const last = folders.find((f) => f.slug === localStorage.getItem(PHONE_FOLDER_KEY)) || folders.reduce((a, f) => (!a || used(f) > used(a) ? f : a), null);
    if (last) location.replace(`/${last.slug}/`);
    else hub();
  }, hub);
} else if (boot.page === "hub") hub();
else root.render(<App />);
