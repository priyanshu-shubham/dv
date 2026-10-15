// PushSetting is Settings' row for this phone's notifications, which come
// through dv's service worker: with dv closed, and the phone locked.
import { useEffect, useState } from "react";
import { api } from "../api.js";
import { say } from "../Notices.jsx";
import { cx } from "../util.js";
import { current, pushable, subscribe, unsubscribe } from "./push.js";

export function PushSetting() {
  const [on, setOn] = useState(null); // null until known
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    current()
      .then((sub) => (sub ? api.pushStatus(sub.endpoint).then((st) => !!st.subscribed) : false))
      .then(setOn, () => setOn(false));
  }, []);
  const pick = async (want) => {
    if (want === on || busy) return;
    setBusy(true);
    try {
      if (want) await subscribe();
      else await unsubscribe();
      setOn(want);
    } catch (e) {
      say("Could not turn notifications " + (want ? "on" : "off"), e.message);
    } finally {
      setBusy(false);
    }
  };
  const test = async () => {
    try {
      await api.pushTest((await current())?.endpoint);
      say("Sent", "Lock the phone or leave dv to see it: dv in front shows notices itself.");
    } catch (e) {
      say("Could not send one", e.message);
    }
  };
  return (
    <div className="settings-row">
      <div className="settings-text">
        <div>Notifications on this phone</div>
        <div className="settings-note">
          {pushable
            ? "When an agent asks or finishes and dv isn't in front of you, even with the phone locked. Tapping one opens its session."
            : "This browser can't receive them here: they need https, as Cloudflare gives dv."}
          {on && (
            <>
              {" "}
              <button className="link" onClick={test}>
                Send a test
              </button>
            </>
          )}
        </div>
      </div>
      <span className="seg">
        {[
          [false, "Off"],
          [true, "On"],
        ].map(([v, name]) => (
          <button key={name} className={cx(v === on && "on")} aria-pressed={v === on} disabled={!pushable || busy || on === null} onClick={() => pick(v)}>
            {name}
          </button>
        ))}
      </span>
    </div>
  );
}
