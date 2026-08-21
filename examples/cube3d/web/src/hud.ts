/**
 * The debug overlay, in the shape 4node-basic's canvas HUD has always had:
 * what the client knows, what the server told it, and how healthy the stream
 * between them is.
 *
 * Everything a value is derived from lives in this file as a pure function,
 * and the DOM half at the bottom only formats what they return. That split is
 * what makes the overlay testable at all — the alternative is a renderer that
 * computes and paints in the same pass, which is what 4node has and why none
 * of its HUD arithmetic is covered.
 *
 * Imports only topology.ts, which imports nothing, so this module stays
 * reachable from a test without pulling three.js in. See flycontrol.ts.
 */
import { cellKey, worldBounds, type CellRect, type EntityClass, type Topology } from "./topology";

/** Frames-per-second over a sliding window. */
export class FpsMeter {
  private times: number[] = [];

  constructor(private readonly windowMs = 1000) {}

  /** Record a frame at nowMs and return the current rate. */
  sample(nowMs: number): number {
    this.times.push(nowMs);
    const cutoff = nowMs - this.windowMs;
    while (this.times.length > 0 && this.times[0] < cutoff) this.times.shift();
    // One frame in the window is not a measurement: with a single timestamp
    // there is no interval to divide by, and reporting 1 fps during the first
    // frame of a session reads as a stall that is not happening.
    if (this.times.length < 2) return 0;
    const span = this.times[this.times.length - 1] - this.times[0];
    if (span <= 0) return 0;
    return ((this.times.length - 1) * 1000) / span;
  }
}

/** How many entities of each class are on screen. */
export function countByClass(kinds: EntityClass[]): Record<EntityClass, number> {
  const out: Record<EntityClass, number> = { self: 0, local: 0, remote: 0, unknown: 0 };
  for (const k of kinds) out[k]++;
  return out;
}

/**
 * A cell's short name, matching the console's own `d1:0,1` form so what you
 * read here is what you type at `cell split`.
 */
export function formatCell(c: CellRect | null): string {
  if (!c) return "—";
  const coords = c.depth > 0 ? `d${c.depth}:${c.cellX},${c.cellY}` : `${c.cellX},${c.cellY}`;
  return c.nodeID ? `${coords} @${c.nodeID}` : coords;
}

/** A fixed-width number, so the panel does not reflow every frame. */
export function fmt(n: number, digits = 0): string {
  if (!Number.isFinite(n)) return "—";
  return n.toFixed(digits);
}

/** Position triple, or an em dash before the first snapshot arrives. */
export function formatPos(p: { x: number; y: number; z: number } | null): string {
  if (!p) return "—";
  return `${fmt(p.x)} ${fmt(p.y)} ${fmt(p.z)}`;
}

export interface HudInput {
  connected: boolean;
  fps: number;
  /** Newest decoded frame sequence, or null before the first frame. */
  seq: number | null;
  /** Interpolation delay the playback controller has settled on. */
  delayMs: number;
  jitterMs: number;
  lossRate: number;
  myNetID: number | null;
  myCell: CellRect | null;
  pos: { x: number; y: number; z: number } | null;
  counts: Record<EntityClass, number>;
  topology: Topology | null;
  aoiRadius: number;
}

export interface HudRow {
  label: string;
  value: string;
  /** "warn" when the value is a symptom rather than a reading. */
  tone?: "warn";
}

/**
 * The overlay's contents.
 *
 * Rows are stable — the same labels in the same order whatever the state — so
 * the panel never changes height and a value you are watching does not move
 * under the cursor. Missing data reads as an em dash, never as a dropped row.
 */
export function hudRows(input: HudInput): HudRow[] {
  const b = input.topology ? worldBounds(input.topology) : null;
  const cells = input.topology?.cells.length ?? 0;
  const total = input.counts.self + input.counts.local + input.counts.remote + input.counts.unknown;

  const rows: HudRow[] = [
    { label: "LINK", value: input.connected ? "connected" : "disconnected", ...(input.connected ? {} : { tone: "warn" as const }) },
    { label: "FPS", value: input.fps > 0 ? fmt(input.fps) : "—" },
    { label: "SEQ", value: input.seq === null ? "—" : String(input.seq) },
    { label: "INTERP", value: `${fmt(input.delayMs)} ms  ±${fmt(input.jitterMs)}` },
    {
      label: "LOSS",
      value: `${fmt(input.lossRate * 100, 1)}%`,
      // A percent of frames going missing is normal on a lossy link; ten is
      // the point where interpolation starts visibly reaching.
      ...(input.lossRate > 0.1 ? { tone: "warn" as const } : {}),
    },
    { label: "NET", value: input.myNetID === null ? "—" : `#${input.myNetID}` },
    { label: "CELL", value: formatCell(input.myCell) },
    { label: "POS", value: formatPos(input.pos) },
    { label: "ENTITIES", value: String(total) },
    {
      label: "  local",
      value: String(input.counts.local),
    },
    {
      label: "  remote",
      value: String(input.counts.remote),
    },
    { label: "CELLS", value: cells === 0 ? "— (no topology grant)" : String(cells) },
    { label: "AOI", value: input.aoiRadius > 0 ? fmt(input.aoiRadius) : "—" },
    { label: "WORLD", value: b && b.width > 0 ? `${fmt(b.width)} × ${fmt(b.height)}` : "—" },
  ];

  // An entity the client cannot place is worth surfacing rather than folding
  // into the total: before DebugInfo arrives every entity is unknown, and
  // AFTER it arrives an unknown one means a position outside every cell the
  // server admitted to owning.
  if (input.counts.unknown > 0) {
    rows.push({
      label: "  unplaced",
      value: String(input.counts.unknown),
      ...(cells > 0 ? { tone: "warn" as const } : {}),
    });
  }
  return rows;
}

/** The per-cell list, sorted so the order does not flicker between pushes. */
export function cellLegend(topo: Topology | null, myCell: CellRect | null): HudRow[] {
  if (!topo || topo.cells.length === 0) return [];
  const sorted = topo.cells.slice().sort((a, b) => {
    if (a.depth !== b.depth) return a.depth - b.depth;
    if (a.cellY !== b.cellY) return a.cellY - b.cellY;
    return a.cellX - b.cellX;
  });
  const mine = cellKey(myCell);
  return sorted.map((c) => ({
    label: cellKey(c) === mine ? "▸" : " ",
    value: formatCell(c),
  }));
}

// ---------------------------------------------------------------------------
// DOM. Nothing below is imported by a test; nothing below computes anything.
// ---------------------------------------------------------------------------

/**
 * Paint rows into a container, reusing its child nodes.
 *
 * Rows are reused rather than rebuilt because this runs on every animation
 * frame, and a container that drops and recreates fifteen elements sixty times
 * a second is the one part of a debug overlay that can measurably cost the
 * frames it is there to report.
 */
export function paintHud(el: HTMLElement, rows: HudRow[]): void {
  while (el.childElementCount > rows.length) el.lastElementChild!.remove();
  while (el.childElementCount < rows.length) {
    const line = document.createElement("div");
    line.className = "hud-row";
    line.append(document.createElement("span"), document.createElement("span"));
    el.append(line);
  }
  rows.forEach((row, i) => {
    const line = el.children[i] as HTMLElement;
    const [label, value] = line.children as unknown as [HTMLElement, HTMLElement];
    if (label.textContent !== row.label) label.textContent = row.label;
    if (value.textContent !== row.value) value.textContent = row.value;
    const warn = row.tone === "warn";
    if (line.classList.contains("warn") !== warn) line.classList.toggle("warn", warn);
  });
}
