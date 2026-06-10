import { RENDER_DELAY } from "../constants";
import { estimatedServerNow } from "../../sdk/_core/clock-sync.js";
import type { GameState } from "../state";

// Console-log threshold: an observation whose |instant - currentOffset|
// exceeds this triggers a structured log. Tuned to fire on the bursty
// frames that explain the stutter without spamming during clean play.
const CLOCK_DRIFT_LOG_THRESHOLD_MS = 50;

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
  // Per-frame decode+apply durations (ms). 60 frames @ 20Hz ≈ 3s window.
  private applyDurations = newRing(60);
  // Latest server conn-stats payload from /debug/conn-stats.
  private serverStats: { totalWrites: number; slowWrites: number; slowQueues: number; writeDurUsMean: number; writeDurUsMax: number; queueDurUsMean: number; queueDurUsMax: number } | null = null;
  private serverStatsLastFetchMs = 0;

  // In-browser /probe-ws connection. Opens on first toggle-visible.
  // Lets us compare "what does Chrome see when receiving from the
  // heartbeat endpoint" against "what does Chrome see when receiving
  // game frames" — same browser, same WS impl, same network path, only
  // the server-side code path differs. If probe-ws is clean and game
  // is bursty, the bug is in the game's replication+writePump pipeline.
  // If both are bursty, the bug is in the network/browser layer.
  private probeWs: WebSocket | null = null;
  private probeArrivals = newRing(60);  // inter-arrival times (ms)
  private probeLastArrivalMs = 0;
  private probeMessageCount = 0;
  private probeStatus: "idle" | "connecting" | "open" | "closed" | "error" = "idle";
  private probeErrorMsg = "";

  // Long-task observation (PerformanceObserver). A "long task" is any
  // task that monopolizes the main thread for ≥50ms (browser-defined).
  // If we see them happening, the main thread is being blocked — and
  // that blocks rAF, WS message handlers, and everything else. The
  // entry.attribution field tells us if the blocking script is our
  // own ("self") vs an extension / iframe.
  private longTasks = newRing(120);     // recent long-task durations (ms)
  private longTaskCount = 0;
  private longTaskTotalMs = 0;
  private longTaskMaxMs = 0;
  private longTaskObserver: PerformanceObserver | null = null;

  // rAF callback duration (start→end of the ticker body). Distinct from
  // rAF *interval* — if interval is long but duration is short, the
  // ticker body is fine and something else is blocking; if duration is
  // also long, the ticker itself is the bottleneck.
  private rafDurations = newRing(240);
  // Pixi auto-render pass duration. Pixi v8 runs render() AFTER our
  // ticker callback returns; that work doesn't show up in ticker phase
  // breakdown but eats real main-thread time. Geometry rebuilds from
  // .clear()+redraw on many Graphics each frame are a prime suspect
  // for this — every dungeon wall + the dungeon shell does it for the
  // vein-pulse / mote-drift animations.
  private renderDurations = newRing(240);
  private renderTotalMs = 0;
  private renderMaxMs = 0;

  // Chrome-specific heap pressure tracking. Sampled every render frame.
  // A sawtooth pattern (heap grows, then drops sharply) means V8 is
  // doing major GC cycles — those are stop-the-world pauses that
  // perfectly match the "long task with empty attribution" signature.
  // GC pauses are the prime suspect for "long task not in ticker".
  private heapSamples = newRing(240);    // usedJSHeapSize over time (MB)
  private heapLastSample = 0;            // last sample (MB)
  private heapDropEvents = 0;            // times heap dropped >5MB in one frame
  private heapMaxObserved = 0;
  private heapMinObserved = Infinity;
  // Latest per-segment timings from main.ts (set via observeRafSegments).
  private lastSegments: { interp: number; sync: number; effects: number; render: number; total: number } | null = null;
  private segmentRings = {
    interp: newRing(60),
    sync: newRing(60),
    effects: newRing(60),
    render: newRing(60),
    total: newRing(60),
  };

  private lastFrameTime = 0;
  private lastServerFrameTime = 0;
  private serverFrameCount = 0;
  private lastProducedAtMs = 0;
  // Counts of outlier server-frame observations grouped by drift band.
  // Useful in the overlay to confirm whether the bursty-buffering
  // hypothesis is firing without trawling the console for the structured
  // logs.
  private driftBucket50 = 0;   //  50–200 ms
  private driftBucket200 = 0;  // 200–500 ms
  private driftBucket500 = 0;  // 500 ms +

  // Subscribe to the browser's long-task notifications. Idempotent —
  // safe to call multiple times (the field gates re-subscription).
  // Failed init (browser doesn't support longtask) silently no-ops.
  private ensureLongTaskObserver(): void {
    if (this.longTaskObserver) return;
    // Capture attribution sources we've seen so the overlay can hint
    // at whether long tasks are from "self" (our own JS), "iframe",
    // or "unknown" (often = V8 GC pauses, which the longtask API
    // can't otherwise attribute).
    if (typeof PerformanceObserver === "undefined") return;
    try {
      const obs = new PerformanceObserver((list) => {
        for (const entry of list.getEntries()) {
          pushRing(this.longTasks, entry.duration);
          this.longTaskCount++;
          this.longTaskTotalMs += entry.duration;
          if (entry.duration > this.longTaskMaxMs) this.longTaskMaxMs = entry.duration;
          // Console-log the worst ones so we can correlate timing with
          // observed stutter events. attribution[].containerSrc is
          // usually empty for inline scripts but worth dumping anyway.
          if (entry.duration > 100) {
            const attrib = (entry as any).attribution as any[] | undefined;
            const sources = attrib ? attrib.map((a) => `${a.containerType ?? "?"}:${a.containerSrc ?? a.containerName ?? "self"}`).join(",") : "(no attribution)";
            console.warn(`[long-task] ${entry.duration.toFixed(0)}ms @${entry.startTime.toFixed(0)} sources=${sources}`);
          }
        }
      });
      obs.observe({ entryTypes: ["longtask"] });
      this.longTaskObserver = obs;
    } catch {
      // longtask isn't supported in some browsers — silently skip
    }
  }

  // Called from main.ts ticker each frame with per-segment durations.
  // Lets us attribute the 374ms outliers to a specific phase.
  observeRafSegments(s: { interp: number; sync: number; effects: number; render: number; total: number }): void {
    this.lastSegments = s;
    pushRing(this.segmentRings.interp, s.interp);
    pushRing(this.segmentRings.sync, s.sync);
    pushRing(this.segmentRings.effects, s.effects);
    pushRing(this.segmentRings.render, s.render);
    pushRing(this.segmentRings.total, s.total);
    pushRing(this.rafDurations, s.total);
  }

  // Called from main.ts around app.renderer.render (or the Pixi render
  // runner hooks). Captures the per-frame cost of the auto-render pass,
  // which happens BETWEEN ticker callbacks and is otherwise invisible
  // to our timing instrumentation.
  observeRenderDuration(durationMs: number): void {
    pushRing(this.renderDurations, durationMs);
    this.renderTotalMs += durationMs;
    if (durationMs > this.renderMaxMs) this.renderMaxMs = durationMs;
  }

  private ensureMounted(): void {
    if (this.mounted) return;
    this.ensureLongTaskObserver();
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
    // Open the in-browser probe-ws connection lazily on first toggle.
    // Keep it open across hide/show so we keep recording stats —
    // recording is the whole point; the toggle just gates the DOM render.
    if (this.visible && !this.probeWs) this.openBrowserProbe();
  }

  private openBrowserProbe(): void {
    const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
    const url = `${proto}//${window.location.host}/probe-ws`;
    this.probeStatus = "connecting";
    try {
      const ws = new WebSocket(url);
      ws.binaryType = "arraybuffer";
      ws.addEventListener("open", () => { this.probeStatus = "open"; });
      ws.addEventListener("message", () => {
        const t = performance.now();
        if (this.probeLastArrivalMs !== 0) {
          pushRing(this.probeArrivals, t - this.probeLastArrivalMs);
        }
        this.probeLastArrivalMs = t;
        this.probeMessageCount++;
      });
      ws.addEventListener("close", (ev) => {
        this.probeWs = null;
        this.probeStatus = "closed";
        this.probeErrorMsg = `code=${ev.code}` + (ev.reason ? ` reason=${ev.reason}` : "");
      });
      ws.addEventListener("error", () => {
        this.probeStatus = "error";
        this.probeErrorMsg = "WS error — check Vite proxy config or server route";
      });
      this.probeWs = ws;
    } catch (e) {
      this.probeStatus = "error";
      this.probeErrorMsg = String(e);
    }
  }

  // Called from network.ts after each applyDeltaUpdate completes.
  observeApplyDuration(durationMs: number): void {
    pushRing(this.applyDurations, durationMs);
  }

  // Background-poll the server-side conn-stats endpoint so the overlay
  // can show server→client write-path timing alongside arrival timing.
  // Called every render frame but throttled to ~1 Hz internally.
  private maybeFetchServerStats(nowMs: number): void {
    if (nowMs - this.serverStatsLastFetchMs < 1000) return;
    this.serverStatsLastFetchMs = nowMs;
    // Fire-and-forget; never block render.
    fetch("/debug/conn-stats")
      .then((r) => (r.ok ? r.json() : null))
      .then((json) => {
        if (!json || !Array.isArray(json.connections) || json.connections.length === 0) return;
        // Aggregate across all connections — this client only has one
        // anyway, but if a probe or test client is also connected we
        // want totals across the lot. Mean = weighted by write count.
        let totalWrites = 0;
        let slowWrites = 0;
        let slowQueues = 0;
        let writeDurUsSum = 0; // weighted sum
        let queueDurUsSum = 0;
        let writeDurUsMax = 0;
        let queueDurUsMax = 0;
        for (const c of json.connections) {
          totalWrites += c.totalWrites;
          slowWrites += c.slowWrites;
          slowQueues += c.slowQueues;
          writeDurUsSum += c.writeDurUsMean * c.totalWrites;
          queueDurUsSum += c.queueDurUsMean * c.totalWrites;
          if (c.writeDurUsMax > writeDurUsMax) writeDurUsMax = c.writeDurUsMax;
          if (c.queueDurUsMax > queueDurUsMax) queueDurUsMax = c.queueDurUsMax;
        }
        this.serverStats = {
          totalWrites,
          slowWrites,
          slowQueues,
          writeDurUsMean: totalWrites > 0 ? writeDurUsSum / totalWrites : 0,
          queueDurUsMean: totalWrites > 0 ? queueDurUsSum / totalWrites : 0,
          writeDurUsMax,
          queueDurUsMax,
        };
      })
      .catch(() => { /* swallow — endpoint may not be running */ });
  }

  // Called from network.ts after every applyDeltaUpdate. The caller
  // measures the offsetMs *before* feeding the clock so we can compute
  // how far the current observation's instant deviated from the running
  // offset — a direct measure of per-frame burst noise.
  observeServerFrame(
    nowMs: number,
    maxProducedAtMs: number,
    entityCount: number,
    offsetBefore: number,
    offsetAfter: number,
  ): void {
    const interval = this.lastServerFrameTime !== 0 ? nowMs - this.lastServerFrameTime : 0;
    if (interval > 0) pushRing(this.serverFrameIntervals, interval);
    this.lastServerFrameTime = nowMs;
    this.serverFrameCount++;
    if (maxProducedAtMs > this.lastProducedAtMs) {
      this.lastProducedAtMs = maxProducedAtMs;
    }

    // With max-instant clock sync this is direct: the observation IS
    // serverStamp - clientNow, no EWMA-reversal needed.
    const instant = maxProducedAtMs - nowMs;
    // Drift = how far this frame's instant is from the current offset
    // estimate. Under max-instant tracking, *bursty* frames have more
    // negative instants and get ignored — but they still register here
    // so the burst pattern remains visible.
    const drift = Math.abs(instant - offsetBefore);

    if (drift >= 500) this.driftBucket500++;
    else if (drift >= 200) this.driftBucket200++;
    else if (drift >= CLOCK_DRIFT_LOG_THRESHOLD_MS) this.driftBucket50++;

    if (drift >= CLOCK_DRIFT_LOG_THRESHOLD_MS) {
      // Structured single-line log — easy to grep. Note offsetAfter
      // may equal offsetBefore now (max didn't move) for delayed
      // burst frames — that's the algorithm correctly rejecting them.
      console.warn(
        `[clock-drift] drift=${drift.toFixed(0)}ms instant=${instant.toFixed(0)} offsetBefore=${offsetBefore.toFixed(0)} offsetAfter=${offsetAfter.toFixed(0)} interval=${interval.toFixed(0)}ms entities=${entityCount} serverStamp=${maxProducedAtMs} clientNow=${nowMs.toFixed(0)}`,
      );
    }
  }

  // Called once per render frame from main.ts ticker.
  update(state: GameState, now: number): void {
    if (this.lastFrameTime !== 0) {
      pushRing(this.frameIntervals, now - this.lastFrameTime);
    }
    this.lastFrameTime = now;
    pushRing(this.offsetSamples, state.clockSync.offsetMs);
    this.maybeFetchServerStats(now);

    // Sample heap usage if available (Chrome only). A sawtooth pattern
    // — heap climbing then dropping > 5MB in a single frame — is the
    // fingerprint of a major V8 GC pass, which matches the "long task
    // with no script attribution" signature.
    const mem = (performance as unknown as { memory?: { usedJSHeapSize: number } }).memory;
    if (mem) {
      const usedMb = mem.usedJSHeapSize / (1024 * 1024);
      pushRing(this.heapSamples, usedMb);
      if (this.heapLastSample > 0 && this.heapLastSample - usedMb > 5) {
        this.heapDropEvents++;
      }
      this.heapLastSample = usedMb;
      if (usedMb > this.heapMaxObserved) this.heapMaxObserved = usedMb;
      if (usedMb < this.heapMinObserved) this.heapMinObserved = usedMb;
    }

    if (!this.visible) return;

    const frame = statsOf(this.frameIntervals);
    const serverFrame = statsOf(this.serverFrameIntervals);
    const offset = statsOf(this.offsetSamples);
    const apply = statsOf(this.applyDurations);
    const probe = statsOf(this.probeArrivals);
    const rafDur = statsOf(this.rafDurations);
    const segInterp = statsOf(this.segmentRings.interp);
    const segSync = statsOf(this.segmentRings.sync);
    const segEffects = statsOf(this.segmentRings.effects);
    const segRender = statsOf(this.segmentRings.render);
    const longTasks = statsOf(this.longTasks);

    // Player ring health: how many samples, age range, render-cursor gap.
    let ringText = "  (no player entity)";
    const me = state.entities.get(state.myEntityId);
    if (me) {
      const samples = me.buffer.samples;
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
      `rAF callback DURATION (work inside the ticker body, n=${rafDur.n})`,
      `  mean:   ${fmt(rafDur.mean)} ms   stddev: ${fmt(rafDur.stddev, 2)} ms`,
      `  min:    ${fmt(rafDur.min)} ms   max:    ${fmt(rafDur.max)} ms`,
      `  ↑ if max ≪ rAF interval max, the block is OUTSIDE the ticker`,
      `    (likely GC pause, WS msg handler, or other event)`,
      ``,
      `ticker phase breakdown (ms, mean / max)`,
      `  interpolation+camera: ${fmt(segInterp.mean)} / ${fmt(segInterp.max)}`,
      `  entityManager.sync:   ${fmt(segSync.mean)} / ${fmt(segSync.max)}`,
      `  effects+aim+dungeon:  ${fmt(segEffects.mean)} / ${fmt(segEffects.max)}`,
      `  HTML UI + minimap:    ${fmt(segRender.mean)} / ${fmt(segRender.max)}`,
      ``,
      `Pixi auto-render duration (n=${statsOf(this.renderDurations).n})`,
      `  mean: ${fmt(statsOf(this.renderDurations).mean)} ms   max: ${fmt(statsOf(this.renderDurations).max)} ms`,
      `  total: ${this.renderTotalMs.toFixed(0)} ms   peakMax: ${fmt(this.renderMaxMs)} ms`,
      `  ↑ if max ≈ long-task duration, render IS the long task.`,
      `    Hunt for Graphics doing .clear()+redraw every frame.`,
      ``,
      `long tasks (PerformanceObserver, >50ms)`,
      `  count: ${this.longTaskCount}   total: ${this.longTaskTotalMs.toFixed(0)} ms   max: ${this.longTaskMaxMs.toFixed(0)} ms`,
      `  recent (n=${longTasks.n}) mean=${fmt(longTasks.mean)} max=${fmt(longTasks.max)}`,
      `  ↑ >100ms entries also dumped to console as [long-task]`,
      ``,
      `heap pressure (Chrome only)`,
      mem
        ? `  current: ${fmt(this.heapLastSample, 1)} MB   min: ${fmt(this.heapMinObserved, 1)} MB   max: ${fmt(this.heapMaxObserved, 1)} MB\n` +
          `  drop events (>5MB single-frame drops): ${this.heapDropEvents}\n` +
          `  ↑ frequent drop events = V8 major GC = stop-the-world pauses`
        : `  (not available — Chrome only via performance.memory)`,
      ``,
      `server frame arrival — GAME /ws (n=${serverFrame.n}, total ${this.serverFrameCount})`,
      `  mean:   ${fmt(serverFrame.mean)} ms   stddev: ${fmt(serverFrame.stddev, 2)} ms`,
      `  min:    ${fmt(serverFrame.min)} ms   max:    ${fmt(serverFrame.max)} ms`,
      ``,
      `browser /probe-ws arrival (n=${probe.n}, total ${this.probeMessageCount}, status=${this.probeStatus})`,
      this.probeStatus === "open"
        ? `  mean:   ${fmt(probe.mean)} ms   stddev: ${fmt(probe.stddev, 2)} ms\n  min:    ${fmt(probe.min)} ms   max:    ${fmt(probe.max)} ms`
        : `  (no data — status=${this.probeStatus}${this.probeErrorMsg ? `, ${this.probeErrorMsg}` : ""})`,
      `  ↑ same browser tab, heartbeat endpoint. If clean while GAME bursts,`,
      `    the burst is server-side game-replication-path, not network/browser.`,
      ``,
      `clockSync.offsetMs (n=${offset.n})`,
      `  current: ${fmt(state.clockSync.offsetMs, 2)} ms`,
      `  mean:    ${fmt(offset.mean, 2)} ms   stddev: ${fmt(offset.stddev, 3)} ms`,
      `  range:   ${fmt(offset.max - offset.min, 2)} ms (max-min)`,
      ``,
      `clock-drift outliers (per-frame |instant - offset|)`,
      `   50-200ms: ${this.driftBucket50}   200-500ms: ${this.driftBucket200}   500ms+: ${this.driftBucket500}`,
      `  (entries also printed to console as [clock-drift])`,
      ``,
      `player ring`,
      ringText,
      ``,
      `last producedAtMs: ${this.lastProducedAtMs}`,
      ``,
      `applyDeltaUpdate duration (n=${apply.n})`,
      `  mean:   ${fmt(apply.mean, 2)} ms   max: ${fmt(apply.max, 2)} ms`,
      `  (if mean approaches 50ms, client decode/apply is the bottleneck)`,
      ``,
      `server-side write stats  (/debug/conn-stats, 1Hz)`,
      this.serverStats
        ? `  writes=${this.serverStats.totalWrites}  slowWrites=${this.serverStats.slowWrites}  slowQueues=${this.serverStats.slowQueues}\n` +
          `  meanWrite=${(this.serverStats.writeDurUsMean / 1000).toFixed(2)}ms   maxWrite=${(this.serverStats.writeDurUsMax / 1000).toFixed(2)}ms\n` +
          `  meanQueue=${(this.serverStats.queueDurUsMean / 1000).toFixed(2)}ms   maxQueue=${(this.serverStats.queueDurUsMax / 1000).toFixed(2)}ms`
        : `  (no data — server not reachable or no /debug/conn-stats route)`,
    ].join("\n");
  }
}

export const devOverlay = new DevOverlay();
