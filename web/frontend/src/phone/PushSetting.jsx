// PushSetting is Settings' row for this device's notifications, which come
// through dv's service worker: with dv closed, a phone locked or a computer's
// dv window shut while its browser runs.
import { useEffect, useState } from "react";
import { api } from "../api.js";
import { say } from "../Notices.jsx";
import { cx, relTime } from "../util.js";
import { current, pushable, subscribe, unsubscribe } from "./push.js";

const TEST_AFTER = 10;

// What the row says on a phone, and on a computer.
const WORDS = {
  phone: {
    label: "Notifications on this phone",
    note: "When an agent asks or finishes and dv isn't in front of you, even with the phone locked. Tapping one opens its session.",
    cannot: "This browser can't receive them here: they need https, as Cloudflare gives dv.",
  },
  desktop: {
    label: "Desktop notifications",
    note: "When an agent asks or finishes and dv isn't in front of you, even with dv's window closed while the browser runs. Clicking one opens its session.",
    cannot: "This browser can't receive them here: they need https, or localhost.",
  },
};

export function PushSetting({ phone = false }) {
  const words = WORDS[phone ? "phone" : "desktop"];
  const [on, setOn] = useState(null); // null until known
  const [last, setLast] = useState(null); // how dv's last message to this device went
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
  // On a phone, late enough to lock it first, which is what a test is for. A
  // test shows even over dv, so a computer has it at once.
  const test = async () => {
    try {
      await api.pushTest((await current())?.endpoint, phone ? TEST_AFTER : 0);
      if (phone) say("Lock the phone now", `A test notification comes in ${TEST_AFTER} seconds.`);
      setTimeout(look, phone ? 0 : 1500);
    } catch (e) {
      say("Could not send one", e.message);
    }
  };
  return (
    <div className="settings-row">
      <div className="settings-text">
        <div>{words.label}</div>
        <div className="settings-note">
          {pushable ? words.note : words.cannot}
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
