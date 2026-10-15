// Android's Back, for the phone app: each screen, sheet or menu that opens is a
// history entry, and Back closes the one on top. Closed from the page instead,
// its entry is taken off again, so Back never lands on something already gone.
import { useEffect, useRef } from "react";

const open = []; // what is open, oldest first: { close }
let skip = 0; // pops the page asked for and has yet to hear of
let owed = 0; // pops asked for while one was under way, to ask for once it lands
let held = 0; // entries to add once the pops have landed
let dropping = 0; // closes in this task, gone back over as one step

let backOn = false;

// enableBack turns the stack on for as long as the phone app is up.
export function enableBack() {
  backOn = true;
  // From nothing, as a stack left from the last time would be out of step.
  open.length = 0;
  skip = owed = held = dropping = 0;
  const onPop = () => {
    if (skip) {
      if (--skip) return;
      // The browser may drop a go() asked for while another is under way, so
      // those wait for this one; entries pushed meanwhile wait for them all.
      if (owed) {
        const n = owed;
        owed = 0;
        return goBack(n);
      }
      for (; held; held--) history.pushState({ dvBack: true }, "");
      return;
    }
    const top = open.pop();
    if (top) top.close();
    // An entry left from before a reload closes nothing: Back goes on past it.
    else if (history.state?.dvBack) history.back();
  };
  addEventListener("popstate", onPop);
  return () => {
    backOn = false;
    removeEventListener("popstate", onPop);
  };
}

function goBack(n) {
  skip++;
  history.go(-n);
}

// pushBack adds an entry that close answers, and returns what takes it off
// when the page closes the thing itself.
function pushBack(close) {
  const entry = { close };
  open.push(entry);
  if (skip || owed || dropping) held++;
  else history.pushState({ dvBack: true }, "");
  return () => {
    const i = open.indexOf(entry);
    if (i < 0) return;
    open.splice(i, 1);
    if (held) return void held--;
    // Several closed at once - up three folders - go back as one step.
    if (!dropping++) {
      queueMicrotask(() => {
        const n = dropping;
        dropping = 0;
        if (skip) owed += n;
        else goBack(n);
      });
    }
  };
}

// useBack keeps an entry for as long as on holds, Back calling onBack.
export function useBack(on, onBack) {
  const latest = useRef(onBack);
  latest.current = onBack;
  useEffect(() => {
    if (!on || !backOn) return;
    return pushBack(() => latest.current());
  }, [on]);
}
