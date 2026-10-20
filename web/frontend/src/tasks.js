// Tasks are the hub's folders for a session with no project of its own, made
// in a click and deleted on closing. Closing one goes back to the folder it
// was started from, which only the starting page knows.
import { api } from "./api.js";
import { slug } from "./boot.js";
import { openFolderSession, PHONE_FOLDER_KEY } from "./util.js";

const FROM = "dv:task-from:";

export async function startTask() {
  const f = await api.hubNewTask();
  try {
    localStorage.setItem(FROM + f.slug, slug);
  } catch {}
  openFolderSession(f.slug, "");
}

// closeTask deletes this page's task and leaves it, never to settle: what its
// caller would do next - a screen closing, which steps back through history -
// would only cancel leaving. The phone app opens on the folder it was last in,
// which is not to be this one.
export async function closeTask() {
  await api.hubCloseTask(slug);
  let from = "";
  try {
    from = localStorage.getItem(FROM + slug) || "";
    localStorage.removeItem(FROM + slug);
    if (localStorage.getItem(PHONE_FOLDER_KEY) === slug) {
      if (from) localStorage.setItem(PHONE_FOLDER_KEY, from);
      else localStorage.removeItem(PHONE_FOLDER_KEY);
    }
  } catch {}
  location.replace(from ? `/${from}/` : "/?folders");
  return new Promise(() => {});
}
