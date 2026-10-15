import { createRoot } from "react-dom/client";
import App from "./App.jsx";
import { boot, slug } from "./boot.js";
import { api } from "./api.js";
import { PHONE_APP, PHONE_FOLDER_KEY, pickSession } from "./util.js";

// A link to a session, as a notice sent to the phone has, opens on it.
const linked = new URLSearchParams(location.search).get("session");
if (linked) {
  if (boot.page !== "hub" && /^[0-9a-f-]{36}$/.test(linked)) pickSession(slug, linked);
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
