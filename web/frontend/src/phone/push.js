// Notifications on a phone: dv's service worker shows what dv pushes, so
// they come with dv closed. A notice settled elsewhere - answered on the
// computer - leaves the phone's shade once dv is opened.
import { useEffect } from "react";
import { api } from "../api.js";

export const pushable = typeof navigator !== "undefined" && "serviceWorker" in navigator && "PushManager" in window && isSecureContext;

let ready = null;
function registration() {
  if (!pushable) return Promise.resolve(null);
  return (ready ||= navigator.serviceWorker.register("/sw.js", { scope: "/" }).then(() => navigator.serviceWorker.ready));
}

export function usePush(notices) {
  useEffect(() => {
    registration().catch(() => {});
  }, []);
  useEffect(() => {
    if (!pushable || !notices) return;
    const open = new Set(notices.map((n) => n.id));
    registration()
      .then((r) => r?.getNotifications())
      .then((shown) => {
        for (const n of shown || []) if (n.tag && !open.has(n.tag)) n.close();
      })
      .catch(() => {});
  }, [notices]);
}

// current is this device's subscription, if it has one.
export async function current() {
  const r = await registration();
  return (await r?.pushManager.getSubscription()) || null;
}

// subscribe asks for permission, subscribes with the hub's key, and gives the
// subscription to dv, named for the device it is on.
export async function subscribe() {
  const r = await registration();
  if (!r) throw new Error("This browser can't receive notifications here. Over plain http only localhost can.");
  if ((await Notification.requestPermission()) !== "granted") throw new Error("Notifications are blocked for dv. Allow them in the app's settings, then try again.");
  const { key } = await api.pushStatus();
  const want = fromB64(key);
  let sub = await r.pushManager.getSubscription();
  // One made for a key dv has since replaced would be turned away on every push.
  if (sub && !same(new Uint8Array(sub.options.applicationServerKey || []), want)) {
    await sub.unsubscribe();
    sub = null;
  }
  sub ||= await r.pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: want });
  const { endpoint, keys } = sub.toJSON();
  await api.pushSubscribe({ endpoint, keys, name: await deviceName() });
  return sub;
}

const same = (a, b) => a.length === b.length && a.every((x, i) => x === b[i]);

export async function unsubscribe() {
  const sub = await current();
  if (!sub) return;
  await api.pushUnsubscribe(sub.endpoint).catch(() => {});
  await sub.unsubscribe();
}

// Chrome's user agent says only "K" for an Android phone's model; it gives the
// real one when asked.
async function deviceName() {
  const model = await navigator.userAgentData?.getHighEntropyValues(["model"]).then(
    (v) => v.model,
    () => "",
  );
  if (model) return model;
  const m = navigator.userAgent.match(/Android [\d.]+; ([^;)]+)/);
  return m && m[1].trim() !== "K" ? m[1].trim() : /Android/.test(navigator.userAgent) ? "Android phone" : navigator.platform || "This device";
}

function fromB64(s) {
  const b = atob(s.replace(/-/g, "+").replace(/_/g, "/") + "===".slice((s.length + 3) % 4));
  return Uint8Array.from(b, (c) => c.charCodeAt(0));
}
