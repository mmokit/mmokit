#!/usr/bin/env bun
/**
 * WebSocket arrival-timing probe.
 *
 * Connects to the server's /probe-ws heartbeat endpoint (no game logic,
 * just a 50ms tick) and measures when each frame arrives on the client.
 * Reports inter-arrival stats so we can compare:
 *
 *   - clean cadence (mean ≈ 50ms, p99 ≈ 50ms, max < 100ms, no bursts)
 *     → server emits cleanly AND network path is clean
 *
 *   - bursty cadence (max > 200ms, "burst" count > 0)
 *     → network path itself is batching messages. This isolates from any
 *       browser, V8, or game-loop scheduling effect because Bun uses
 *       Node's WebSocket impl directly — no rendering, no main-thread
 *       contention.
 *
 * Also fetches /debug/conn-stats at the end to compare server-side
 * write-path metrics against client-side observed arrivals.
 *
 * Usage:
 *   bun run diagnostics/probe.ts                    # 30s, default host
 *   bun run diagnostics/probe.ts --duration=60     # 60s sample
 *   bun run diagnostics/probe.ts --host=localhost:8080
 *
 * Exit codes:
 *   0 — clean cadence
 *   1 — bursty cadence (p99 inter-arrival > 80ms OR burst count > 0)
 *   2 — connection / setup error
 */

interface Arrival {
  arrivalMs: number;       // performance.now() at onmessage
  serverTickMs: number;    // server wall-clock at tick boundary
  writeStartMs: number;    // server wall-clock before ws.Write
  seq: number;
  bytes: number;
}

interface Stats {
  count: number;
  mean: number;
  stddev: number;
  p50: number;
  p95: number;
  p99: number;
  min: number;
  max: number;
}

function statsOf(values: number[]): Stats {
  if (values.length === 0) {
    return { count: 0, mean: 0, stddev: 0, p50: 0, p95: 0, p99: 0, min: 0, max: 0 };
  }
  const sorted = [...values].sort((a, b) => a - b);
  const mean = values.reduce((a, b) => a + b, 0) / values.length;
  const variance = values.reduce((acc, v) => acc + (v - mean) ** 2, 0) / values.length;
  return {
    count: values.length,
    mean,
    stddev: Math.sqrt(variance),
    p50: sorted[Math.floor(sorted.length * 0.5)],
    p95: sorted[Math.floor(sorted.length * 0.95)],
    p99: sorted[Math.floor(sorted.length * 0.99)],
    min: sorted[0],
    max: sorted[sorted.length - 1],
  };
}

function fmtStats(label: string, unit: string, s: Stats): string {
  return [
    `${label}  (n=${s.count})`,
    `  mean=${s.mean.toFixed(2)}${unit}   stddev=${s.stddev.toFixed(2)}${unit}`,
    `  p50=${s.p50.toFixed(2)}${unit}   p95=${s.p95.toFixed(2)}${unit}   p99=${s.p99.toFixed(2)}${unit}`,
    `  min=${s.min.toFixed(2)}${unit}   max=${s.max.toFixed(2)}${unit}`,
  ].join("\n");
}

function parseArgs(): { host: string; durationSec: number; jsonOut: boolean } {
  const args = { host: "localhost:8080", durationSec: 30, jsonOut: false };
  for (const a of process.argv.slice(2)) {
    if (a.startsWith("--host=")) args.host = a.slice("--host=".length);
    else if (a.startsWith("--duration=")) {
      const raw = a.slice("--duration=".length);
      const parsed = parseInt(raw, 10);
      if (!Number.isFinite(parsed) || parsed <= 0) {
        console.error(`probe: --duration must be a positive integer (got "${raw}")`);
        process.exit(2);
      }
      args.durationSec = parsed;
    }
    else if (a === "--json") args.jsonOut = true;
    else if (a.startsWith("--")) {
      console.error(`probe: unknown flag "${a}"`);
      process.exit(2);
    }
  }
  return args;
}

async function main() {
  const { host, durationSec, jsonOut } = parseArgs();
  const wsUrl = `ws://${host}/probe-ws`;
  const httpBase = `http://${host}`;

  if (!jsonOut) {
    console.log(`probe: connecting to ${wsUrl}`);
    console.log(`probe: sampling for ${durationSec}s`);
  }

  const arrivals: Arrival[] = [];

  const ws = new WebSocket(wsUrl);
  ws.binaryType = "arraybuffer";

  await new Promise<void>((resolve, reject) => {
    const timeout = setTimeout(() => reject(new Error("connect timed out after 5s")), 5000);
    ws.addEventListener("open", () => {
      clearTimeout(timeout);
      resolve();
    });
    ws.addEventListener("error", (e) => {
      clearTimeout(timeout);
      reject(new Error(`ws error: ${e}`));
    });
  }).catch((e) => {
    console.error(`probe: ${e.message}`);
    process.exit(2);
  });

  if (!jsonOut) console.log("probe: connected, sampling...");

  ws.addEventListener("message", (ev) => {
    const arrivalMs = performance.now();
    const ab = ev.data as ArrayBuffer;
    if (ab.byteLength < 24) return;
    const dv = new DataView(ab);
    const seq = Number(dv.getBigUint64(0, true));
    const serverTickMs = Number(dv.getBigUint64(8, true));
    const writeStartMs = Number(dv.getBigUint64(16, true));
    arrivals.push({ arrivalMs, serverTickMs, writeStartMs, seq, bytes: ab.byteLength });
  });

  await new Promise((r) => setTimeout(r, durationSec * 1000));
  ws.close();

  if (arrivals.length < 2) {
    console.error(`probe: only got ${arrivals.length} frame(s) in ${durationSec}s — server not running?`);
    process.exit(2);
  }

  // Compute inter-arrival deltas (client-side wall clock).
  const interArrivals: number[] = [];
  for (let i = 1; i < arrivals.length; i++) {
    interArrivals.push(arrivals[i].arrivalMs - arrivals[i - 1].arrivalMs);
  }
  const interArrivalStats = statsOf(interArrivals);

  // Inter-tick deltas (server-side). Should be exactly 50ms ± minor scheduler jitter.
  const interTicks: number[] = [];
  for (let i = 1; i < arrivals.length; i++) {
    interTicks.push(arrivals[i].serverTickMs - arrivals[i - 1].serverTickMs);
  }
  const interTickStats = statsOf(interTicks);

  // Transit estimate = arrival - writeStart, after subtracting a baseline
  // (we don't have a clock-sync handshake; instead compute the *relative*
  // transit per frame minus its minimum across the run — gives jitter
  // without absolute calibration).
  const transitRaw: number[] = arrivals.map((a) => a.arrivalMs - a.writeStartMs);
  const transitBaseline = Math.min(...transitRaw);
  const transitJitter = transitRaw.map((t) => t - transitBaseline);
  const transitStats = statsOf(transitJitter);

  // Burst events: inter-arrival < 5ms when previous was > 80ms.
  let bursts = 0;
  let longestGap = 0;
  for (let i = 1; i < interArrivals.length; i++) {
    if (interArrivals[i] < 5 && interArrivals[i - 1] > 80) bursts++;
    if (interArrivals[i] > longestGap) longestGap = interArrivals[i];
  }

  // Fetch server-side write-path stats.
  let serverStats: any = null;
  try {
    const resp = await fetch(`${httpBase}/debug/conn-stats`);
    if (resp.ok) serverStats = await resp.json();
  } catch (e) {
    if (!jsonOut) console.warn(`probe: warning: could not fetch /debug/conn-stats: ${e}`);
  }

  if (jsonOut) {
    console.log(JSON.stringify({
      durationSec,
      arrivals: arrivals.length,
      interArrival: interArrivalStats,
      interTick: interTickStats,
      transitJitter: transitStats,
      bursts,
      longestGap,
      serverStats,
    }, null, 2));
  } else {
    console.log("");
    console.log(`probe: ${arrivals.length} frames in ${durationSec}s (expected ≈ ${durationSec * 20})`);
    console.log("");
    console.log(fmtStats("inter-arrival (client wall clock)", "ms", interArrivalStats));
    console.log("");
    console.log(fmtStats("inter-tick   (server tick spacing)", "ms", interTickStats));
    console.log(`  ↑ server tick spacing should be tight ≈50ms; if not, server-side scheduling is bad`);
    console.log("");
    console.log(fmtStats("transit jitter (arrival - writeStart, relative to min)", "ms", transitStats));
    console.log(`  ↑ jitter on the server→client wire; should be small if the network is clean`);
    console.log("");
    console.log(`bursts:      ${bursts}   (cluster: gap > 80ms then < 5ms follow-up)`);
    console.log(`longest gap: ${longestGap.toFixed(2)} ms`);

    if (serverStats?.connections?.length) {
      console.log("");
      console.log("server-side write stats (from /debug/conn-stats):");
      for (const c of serverStats.connections) {
        console.log(`  conn ${c.connId}: writes=${c.totalWrites} ` +
          `meanWrite=${(c.writeDurUsMean / 1000).toFixed(2)}ms maxWrite=${(c.writeDurUsMax / 1000).toFixed(2)}ms ` +
          `meanQueue=${(c.queueDurUsMean / 1000).toFixed(2)}ms maxQueue=${(c.queueDurUsMax / 1000).toFixed(2)}ms ` +
          `slowWrites=${c.slowWrites} slowQueues=${c.slowQueues}`);
      }
    }
  }

  // Pass/fail: inter-arrival p99 > 80ms OR any burst event = fail.
  const failed = interArrivalStats.p99 > 80 || bursts > 0;
  if (!jsonOut) {
    console.log("");
    console.log(failed ? "RESULT: BURSTY (root cause is between server emit and Bun receive)" : "RESULT: CLEAN");
  }
  process.exit(failed ? 1 : 0);
}

main();
