import { RENDER_DELAY } from "../constants";
import { estimatedServerNow } from "../clockSync";
import type { GameState } from "../state";

// Fixed-size circular buffer for rolling stats. Push is O(1); stats
// (mean/stddev/min/max) iterate the populated portion only.
interface RingBuf {
  buf: Float64Array;
  idx: number;
  filled: boolean;
}

function newRing(size: number): RingBuf {
  return { buf: new Float64Array(size), idx: 0, filled: false };
}

function pushRing(r: RingBuf, v: number): void {
  r.buf[r.idx] = v;
  r.idx = (r.idx + 1) % r.buf.length;
  if (r.idx === 0) r.filled = true;
}

interface RingStats {
  n: number;
  mean: number;
  stddev: number;
  min: number;
  max: number;
}

function statsOf(r: RingBuf): RingStats {
  const n = r.filled ? r.buf.length : r.idx;
  if (n === 0) return { n: 0, mean: 0, stddev: 0, min: 0, max: 0 };
  let sum = 0;
  let min = Infinity;
  let max = -Infinity;
  for (let i = 0; i < n; i++) {
    const v = r.buf[i];
    sum += v;
    if (v < min) min = v;
    if (v > max) max = v;
  }
  const mean = sum / n;
  let varSum = 0;
  for (let i = 0; i < n; i++) {
    const d = r.buf[i] - mean;
    varSum += d * d;
  }
  return { n, mean, stddev: Math.sqrt(varSum / n), min, max };
}

function fmt(v: number, digits = 2): string {
  if (!Number.isFinite(v)) return "—";
  return v.toFixed(digits);
}

// Continuously records rolling perf/sync stats so a mid-stutter toggle
// shows the seconds leading up to the moment of toggle. Hidden div in
// the corner; toggle with Backquote (~).
class DevOverlay {
  private el!: HTMLDivElement;
  private visible = false;
  private mounted = false;

  // ~2s at 60fps render rate, longer at 240fps but still bounded.
  private frameIntervals = newRing(240);
  // ~3s at 20Hz server tick.
  private serverFrameIntervals = newRing(60);
  // ~6s of offsetMs samples (push per frame).
  private offsetSamples = newRing(720);

  private lastFrameTime = 0;
  private lastServerFrameTime = 0;
  private serverFrameCount = 0;
  private lastProducedAtMs = 0;

  private ensureMounted(): void {
    if (this.mounted) return;
    this.el = document.createElement("div");
    this.el.id = "dev-overlay";
    this.el.style.cssText = [
      "position: fixed",
      "top: 8px",
      "left: 8px",
      "z-index: 99999",
      "padding: 8px 10px",
      "background: rgba(0,0,0,0.78)",
      "color: #cfc",
      "font: 11px/1.4 ui-monospace, SFMono-Regular, Menlo, monospace",
      "white-space: pre",
      "border: 1px solid #2d2",
      "border-radius: 4px",
      "pointer-events: none",
      "min-width: 320px",
      "display: none",
    ].join("; ");
    document.body.appendChild(this.el);
    this.mounted = true;
  }

  toggle(): void {
    this.ensureMounted();
    this.visible = !this.visible;
    this.el.style.display = this.visible ? "block" : "none";
  }

  // Called from network.ts after every applyDeltaUpdate.
  observeServerFrame(nowMs: number, maxProducedAtMs: number): void {
    if (this.lastServerFrameTime !== 0) {
      pushRing(this.serverFrameIntervals, nowMs - this.lastServerFrameTime);
    }
    this.lastServerFrameTime = nowMs;
    this.serverFrameCount++;
    if (maxProducedAtMs > this.lastProducedAtMs) {
      this.lastProducedAtMs = maxProducedAtMs;
    }
  }

  // Called once per render frame from main.ts ticker.
  update(state: GameState, now: number): void {
    if (this.lastFrameTime !== 0) {
      pushRing(this.frameIntervals, now - this.lastFrameTime);
    }
    this.lastFrameTime = now;
    pushRing(this.offsetSamples, state.clockSync.offsetMs);

    if (!this.visible) return;

    const frame = statsOf(this.frameIntervals);
    const serverFrame = statsOf(this.serverFrameIntervals);
    const offset = statsOf(this.offsetSamples);

    // Player ring health: how many samples, age range, render-cursor gap.
    let ringText = "  (no player entity)";
    const me = state.entities.get(state.myEntityId);
    if (me) {
      const samples = me.samples;
      const renderTime = state.clockSync.initialized
        ? estimatedServerNow(state.clockSync, now) - RENDER_DELAY
        : 0;
      if (samples.length === 0) {
        ringText = "  (player ring empty)";
      } else {
        const oldest = samples[0].producedAtMs;
        const newest = samples[samples.length - 1].producedAtMs;
        const range = newest - oldest;
        // Positive: renderTime is past the newest sample → extrapolating or frozen.
        // Negative: renderTime is between samples (healthy).
        const gapToNewest = renderTime - newest;
        const gapToOldest = renderTime - oldest;
        ringText = [
          `  samples:    ${samples.length}`,
          `  range:      ${range.toFixed(0)} ms (oldest→newest)`,
          `  cursor→newest: ${gapToNewest.toFixed(1)} ms (>0 = extrap)`,
          `  cursor→oldest: ${gapToOldest.toFixed(1)} ms (<0 = stale)`,
        ].join("\n");
      }
    }

    const fps = state.fps;
    const entityCount = state.entities.size;
    const synced = state.clockSync.initialized ? "yes" : "no";

    this.el.textContent = [
      `[dev overlay — toggle ~ ]`,
      ``,
      `fps:               ${fps}  (entities ${entityCount})`,
      `clockSync init:    ${synced}`,
      ``,
      `rAF frame interval  (n=${frame.n})`,
      `  mean:   ${fmt(frame.mean)} ms   stddev: ${fmt(frame.stddev, 3)} ms`,
      `  min:    ${fmt(frame.min)} ms   max:    ${fmt(frame.max)} ms`,
      ``,
      `server frame arrival (n=${serverFrame.n}, total ${this.serverFrameCount})`,
      `  mean:   ${fmt(serverFrame.mean)} ms   stddev: ${fmt(serverFrame.stddev, 2)} ms`,
      `  min:    ${fmt(serverFrame.min)} ms   max:    ${fmt(serverFrame.max)} ms`,
      ``,
      `clockSync.offsetMs (n=${offset.n})`,
      `  current: ${fmt(state.clockSync.offsetMs, 2)} ms`,
      `  mean:    ${fmt(offset.mean, 2)} ms   stddev: ${fmt(offset.stddev, 3)} ms`,
      `  range:   ${fmt(offset.max - offset.min, 2)} ms (max-min)`,
      ``,
      `player ring`,
      ringText,
      ``,
      `last producedAtMs: ${this.lastProducedAtMs}`,
    ].join("\n");
  }
}

export const devOverlay = new DevOverlay();
