import { createRoot } from "react-dom/client";
import App from "./App.jsx";
import { boot } from "./boot.js";

const root = createRoot(document.getElementById("root"));
if (boot.page === "hub") import("./Hub.jsx").then(({ default: Hub }) => root.render(<Hub />));
else root.render(<App />);
