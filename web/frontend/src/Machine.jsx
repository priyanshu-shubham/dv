import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { api } from "./api.js";
import { TAB_COLORS } from "./favicon.js";
import { cx, useDismiss, useFixedMenu } from "./util.js";

// HubMark is which dv this is, as Settings → Appearance names and colours it,
// where the bar would otherwise say dv on every computer alike.
export function HubMark({ settings }) {
  return (
    <span className="logo-name" style={{ color: TAB_COLORS[settings.tabColor] }}>
      {settings.tabName?.trim() || "dv"}
    </span>
  );
}

const MACHINE_MS = 3000;

// useMachine reads the computer every MACHINE_MS while on and the page is in
// sight.
export function useMachine(on = true) {
  const [m, setM] = useState(null);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!on) return;
    let live = true;
    let timer;
    const tick = async () => {
      if (!document.hidden) {
        await api.machine().then(
          (r) => live && (setM(r), setError("")),
          (e) => live && setError(e.message),
        );
      }
      if (live) timer = setTimeout(tick, MACHINE_MS);
    };
    tick();
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [on]);
  return [m, error];
}

export function bytes(n) {
  const units = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  // Stepping up at 1000 keeps four digits, as "1007 GB", from showing.
  while (n >= 1000 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${n >= 100 || i === 0 ? Math.round(n) : n < 10 ? n.toFixed(2).replace(/\.?0+$/, "") : n.toFixed(1)} ${units[i]}`;
}

const pct = (used, total) => (total ? (100 * used) / total : 0);
const level = (p) => (p >= 90 ? "high" : p >= 75 ? "warm" : "");

function uptime(s) {
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  return d ? `${d}d ${h}h` : h ? `${h}h ${m}m` : `${m}m`;
}

function ago(at) {
  const s = Math.round((Date.now() - at) / 1000);
  return s < 60 ? `${s}s ago` : `${Math.floor(s / 60)}m ${s % 60}s ago`;
}

// The disk that runs out first is the one worth a place in the bar.
const fullest = (m) => m.disks.reduce((a, d) => (!a || pct(d.used, d.total) > pct(a.used, a.total) ? d : a), null);

function Meter({ p }) {
  return (
    <span className={cx("machine-meter-track", level(p))}>
      <span style={{ width: `${Math.min(100, Math.max(p, 1.5))}%` }} />
    </span>
  );
}

const SPARK_H = 30;

// Spark is a history from zero to max, the latest point marked. Pointing at it
// hands onPoint the sample under the pointer, which the tile shows instead.
// Drawn at its own width in pixels, as a stretched one would make the dot an
// oval.
function Spark({ values, max, onPoint }) {
  const [hover, setHover] = useState(null);
  const ref = useRef(null);
  const [w, setW] = useState(0);
  useLayoutEffect(() => {
    const el = ref.current;
    const seen = new ResizeObserver(() => setW(el.clientWidth));
    seen.observe(el);
    return () => seen.disconnect();
  }, []);
  const n = values.length;
  if (n < 2 || !w) return <svg className="machine-spark" ref={ref} />;
  // Inset by the dot's radius, so it is whole at either end.
  const x = (i) => 3 + (i / (n - 1)) * (w - 6);
  const y = (v) => SPARK_H - 3 - (Math.min(v, max) / max) * (SPARK_H - 6);
  const line = values.map((v, i) => `${i ? "L" : "M"}${x(i).toFixed(1)},${y(v).toFixed(1)}`).join("");
  const point = (e) => {
    const r = e.currentTarget.getBoundingClientRect();
    const i = Math.round(((e.clientX - r.left) / r.width) * (n - 1));
    const at = Math.max(0, Math.min(n - 1, i));
    setHover(at);
    onPoint(at);
  };
  const leave = () => {
    setHover(null);
    onPoint(null);
  };
  const shown = hover ?? n - 1;
  return (
    <svg className="machine-spark" ref={ref} viewBox={`0 0 ${w} ${SPARK_H}`} onMouseMove={point} onMouseLeave={leave}>
      <path className="machine-spark-area" d={`${line}L${x(n - 1)},${SPARK_H}L${x(0)},${SPARK_H}Z`} />
      <path className="machine-spark-line" d={line} />
      {hover != null && <line className="machine-spark-rule" x1={x(hover)} x2={x(hover)} y1={0} y2={SPARK_H} />}
      <circle className={cx("machine-spark-dot", level(pct(values[shown], max)))} cx={x(shown)} cy={y(values[shown])} r={3} />
    </svg>
  );
}

function Tile({ label, aside, value, sub, title, children }) {
  return (
    <div className="machine-tile" title={title}>
      <div className="machine-label">
        <span>{label}</span>
        {aside && <span className="machine-aside">{aside}</span>}
      </div>
      <div className="machine-value">{value}</div>
      <div className="machine-sub">{sub}</div>
      {children}
    </div>
  );
}

// The CPU and memory tiles show the latest sample, or the one pointed at in
// their history.
function HistoryTile({ label, aside, history, pick, max, value, sub, title }) {
  const [at, setAt] = useState(null);
  const s = at != null && history[at];
  return (
    <Tile label={label} aside={aside} value={value(s ? pick(s) : null)} sub={s ? ago(s.at) : sub} title={title}>
      <Spark values={history.map(pick)} max={max} onPoint={setAt} />
    </Tile>
  );
}

// MachineDetails is the computer as tiles: CPU and memory over the last ten
// minutes, each disk the folders are on, and what dv and the agents it runs
// hold.
export function MachineDetails({ m }) {
  const mem = m.memory;
  const span = m.history.length > 1 ? Math.round((m.history.at(-1).at - m.history[0].at) / 60000) : 0;
  const recent = span ? `${span} min` : "";
  return (
    <div className="machine">
      <div className="machine-head">
        <span className="machine-host">{m.host}</span>
        <span className="machine-os">
          {m.os} · up {uptime(m.uptime)}
        </span>
      </div>
      <div className="machine-tiles">
        <HistoryTile
          label="CPU"
          history={m.history}
          pick={(s) => s.cpu}
          max={100}
          value={(v) => `${Math.round(v ?? m.cpu.percent)}%`}
          sub={[`${m.cpu.cores} cores`, m.cpu.load && `load ${m.cpu.load[0].toFixed(2)}`].filter(Boolean).join(" · ")}
          aside={recent}
        />
        <HistoryTile
          label="Memory"
          history={m.history}
          pick={(s) => s.mem}
          max={mem.total}
          value={(v) => bytes(v ?? mem.used)}
          sub={`of ${bytes(mem.total)}`}
          title={m.swap.total ? `Swap: ${bytes(m.swap.used)} of ${bytes(m.swap.total)}` : ""}
          aside={recent}
        />
        {m.disks.map((d) => (
          <Tile key={d.mount} label={`Disk ${d.mount}`} value={bytes(d.free)} sub={`free of ${bytes(d.total)}`} title={`${Math.round(pct(d.used, d.total))}% used`}>
            <Meter p={pct(d.used, d.total)} />
          </Tile>
        ))}
        <Tile
          label="dv"
          value={bytes(m.dv.memory)}
          sub={`${m.dv.processes} process${m.dv.processes === 1 ? "" : "es"}`}
          title="dv with the agents, terminals and tools it started. Memory they share is counted for each, so this is at most what they hold."
        >
          <Meter p={pct(m.dv.memory, mem.total)} />
        </Tile>
      </div>
    </div>
  );
}

// MachineMeter is CPU, memory and the fullest disk at a glance, in the hub's
// bar, opening to the rest.
export function MachineMeter() {
  const [m] = useMachine();
  const [open, setOpen] = useState(false);
  const panel = useRef(null);
  const ref = useDismiss(open, () => setOpen(false), panel);
  const at = useFixedMenu(open, ref, panel);
  if (!m) return null;
  const disk = fullest(m);
  const items = [
    ["CPU", m.cpu.percent],
    ["Mem", pct(m.memory.used, m.memory.total)],
    disk && ["Disk", pct(disk.used, disk.total)],
  ].filter(Boolean);
  return (
    <span className="machine-menu" ref={ref}>
      <button className={cx("machine-glance", open && "on")} aria-expanded={open} onClick={() => setOpen((o) => !o)} title={`${m.host}: how this computer is doing`}>
        {items.map(([label, p]) => (
          <span key={label} className={cx("machine-stat", level(p))}>
            {label}
            <Meter p={p} />
            <b>{Math.round(p)}%</b>
          </span>
        ))}
      </button>
      {open &&
        createPortal(
          <div className="model-list machine-pop" ref={panel} style={at}>
            <MachineDetails m={m} />
          </div>,
          document.body,
        )}
    </span>
  );
}

// MachineSetting is MachineDetails at the top of Settings → Monitor.
export function MachineSetting() {
  const [m, error] = useMachine();
  if (error && !m) return <div className="settings-note error">{error}</div>;
  return m ? <MachineDetails m={m} /> : null;
}
