// PushSetting is Settings' row for this phone's notifications, which come
// through dv's service worker: with dv closed, and the phone locked.
import { useEffect, useState } from "react";
import { api } from "../api.js";
import { say } from "../Notices.jsx";
import { cx, relTime } from "../util.js";
import { current, pushable, subscribe, unsubscribe } from "./push.js";

const TEST_AFTER = 10;

export function PushSetting() {
  const [on, setOn] = useState(null); // null until known
  const [last, setLast] = useState(null); // how dv's last message to this phone went
  const [busy, setBusy] = useState(false);
  const look = () =>
    current()
      .then((sub) => (sub ? api.pushStatus(sub.endpoint) : {}))
      .then(
        (st) => {
          setOn(!!st.subscribed);
          setLast(st.last || null);
        },
        () => setOn(false),
      );
  // Coming back to it, as after locking the phone for a test, it reads again.
  useEffect(() => {
    look();
    const back = () => !document.hidden && look();
    document.addEventListener("visibilitychange", back);
    return () => document.removeEventListener("visibilitychange", back);
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
  // Late enough to lock the phone first, which is what a test is for.
  const test = async () => {
    try {
      await api.pushTest((await current())?.endpoint, TEST_AFTER);
      say("Lock the phone now", `A test notification comes in ${TEST_AFTER} seconds.`);
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
        {on && last && (
          <div className={cx("settings-note", last.error && "error")}>
            {last.error ? `The last one, ${relTime(last.at)}, failed: ${last.error}` : `Last sent ${relTime(last.at)}: “${last.title}”`}
          </div>
        )}
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
