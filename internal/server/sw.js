// dv's service worker shows the notices dv pushes to a device, and opens the
// session one is about when it is tapped. It has no fetch handler: every page
// comes from dv itself, past whatever guards it, never from a cache.

self.addEventListener("install", () => self.skipWaiting());
self.addEventListener("activate", (event) => event.waitUntil(self.clients.claim()));

// A push is { id, kind, title, body, url, folder, session }.
self.addEventListener("push", (event) => {
  let n;
  try {
    n = event.data.json();
  } catch {
    return;
  }
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then((pages) => {
      // With no page open to count them, a request marks the installed app's
      // icon; a page opened counts them exactly.
      if (n.kind === "ask" && !pages.length) self.navigator.setAppBadge?.().catch(() => {});
      // A page in front of the reader tells of it itself. Chrome lets a push
      // go unshown only then. A test is asked for to be seen.
      if (n.id !== "dv-test" && pages.some((p) => p.focused && p.visibilityState === "visible")) return;
      // A session's notification takes the place of the one before it.
      const tag = n.session || n.id;
      return self.registration.getNotifications({ tag }).then((shown) =>
        self.registration.showNotification(n.title || "dv", {
          body: n.body || "",
          tag,
          // id is the notice's, which a page closes the notification by.
          data: { id: n.id, url: n.url || "/", session: n.session, folder: n.folder, action: n.action },
          // dv puts both before this file as data (HandleWorker).
          icon: ICON,
          badge: BADGE,
          // Taking another notice's place is silent without renotify, which
          // throws without a tag.
          renotify: !!tag && (n.kind === "ask" || shown.some((s) => s.data?.id !== n.id)),
        }),
      );
    }),
  );
});

self.addEventListener("notificationclick", (event) => {
  event.notification.close();
  const { url = "/", session, folder, action } = event.notification.data || {};
  // A hub has each folder at /<slug>/; a lone dv has its one at /.
  const base = folder ? `/${folder}/` : "/";
  event.waitUntil(
    self.clients.matchAll({ type: "window", includeUncontrolled: true }).then(async (pages) => {
      // The app open on the folder is told which session, or command's
      // output, rather than loaded again; of neither - the test - it is the place.
      const page = pages.find((p) => new URL(p.url).pathname.startsWith(base));
      if (page) {
        await page.focus().catch(() => {});
        if (session || action) page.postMessage({ type: "dv:open", session, action });
        return;
      }
      // Open on another of the hub's folders, it goes there, rather than a
      // second window. navigate takes only a page this worker controls.
      const other = pages[0];
      if (other) {
        await other.focus().catch(() => {});
        if (other.navigate && (await other.navigate(url).catch(() => null))) return;
      }
      return self.clients.openWindow(url);
    }),
  );
});
