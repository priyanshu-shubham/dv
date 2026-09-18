import { createRoot } from "react-dom/client";
import App from "./App.jsx";
import { boot, slug } from "./boot.js";
import { pickSession } from "./util.js";

// A link to a session, as a notice sent to the phone has, opens on it.
const linked = new URLSearchParams(location.search).get("session");
if (linked) {
  if (boot.page !== "hub" && /^[0-9a-f-]{36}$/.test(linked)) pickSession(slug, linked);
  history.replaceState(null, "", location.pathname + location.hash);
}

const root = createRoot(document.getElementById("root"));
if (boot.page === "hub") import("./Hub.jsx").then(({ default: Hub }) => root.render(<Hub />));
else root.render(<App />);
