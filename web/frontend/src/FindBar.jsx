import { useEffect, useRef } from "react";
import { cx } from "./util.js";
import { IconChevronDown, IconChevronUp, IconX } from "./icons.jsx";

// FindBar is find in page's box, floating over the pane it searches. `find` is
// the query and its toggles; `find.focus` moves on every Ctrl+F, which puts the
// cursor back in the box with its text selected, as a browser's find bar does.
export default function FindBar({ find, total, index, capped, pending, error, onChange, onStep, onClose }) {
  const input = useRef(null);
  useEffect(() => {
    input.current?.focus();
    input.current?.select();
  }, [find.focus]);

  const count = error
    ? "Invalid"
    : !find.query
      ? ""
      : total
        ? `${index >= 0 ? index + 1 : "?"} of ${total}${capped ? "+" : ""}`
        : pending
          ? ""
          : "No results";
  // Clicking a control leaves the cursor in the box, so typing carries on.
  const keep = (e) => e.preventDefault();
  const toggle = (key, label, title) => (
    <button className={cx(find[key] && "on")} onMouseDown={keep} onClick={() => onChange({ [key]: !find[key] })} title={title}>
      {label}
    </button>
  );

  return (
    <div className="find-dock">
      <div className="find-bar" role="search">
        <input
          ref={input}
          className={cx(error && "bad")}
          value={find.query}
          placeholder="Find"
          spellCheck={false}
          title={error || undefined}
          onChange={(e) => onChange({ query: e.target.value })}
          onKeyDown={(e) => {
            if (e.key === "Enter") {
              e.preventDefault();
              onStep(e.shiftKey ? -1 : 1);
            } else if (e.key === "Escape") {
              e.preventDefault();
              e.stopPropagation();
              onClose();
            }
          }}
        />
        <div className="toggles">
          {toggle("caseSens", "Aa", "Match case")}
          {toggle("wholeWord", "ab", "Whole word")}
          {toggle("regex", ".*", "Regular expression")}
        </div>
        <span className="find-count" title={pending ? `Still loading ${pending} file${pending === 1 ? "" : "s"}` : undefined}>
          {count}
          {pending > 0 && "..."}
        </span>
        <button className="ghost" onMouseDown={keep} onClick={() => onStep(-1)} disabled={!total} title="Previous match (Shift+Enter)">
          <IconChevronUp size={13} />
        </button>
        <button className="ghost" onMouseDown={keep} onClick={() => onStep(1)} disabled={!total} title="Next match (Enter)">
          <IconChevronDown size={13} />
        </button>
        <button className="ghost" onClick={onClose} title="Close (Esc)">
          <IconX size={12} />
        </button>
      </div>
    </div>
  );
}
