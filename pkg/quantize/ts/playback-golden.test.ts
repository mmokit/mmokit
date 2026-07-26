import { describe, expect, it } from "bun:test";
import { readFileSync } from "node:fs";
import { join } from "node:path";

import {
  FRAME_FLAG_INPUT_ACK,
  decodeDeltaEntry,
  decodeFrameHeader,
  decodeFullEntry,
  decodeInputAck,
  readUint32,
} from "./delta-decoder-core.js";
import { AdaptivePlaybackController } from "./playback-controller.js";
import { PredictionBuffer } from "./prediction-buffer.js";

/**
 * Cross-language golden vectors for AdaptivePlaybackController,
 * PredictionBuffer and the FrameFlagInputAck trailer.
 *
 * This file and csharp/Mmokit.Sdk.Core.Tests/PlaybackGoldenTests.cs replay the
 * SAME Go-produced manifest. Before them, TS/C# parity for these two cores was
 * enforced only by two independently hand-written suites that happened to
 * agree — future drift would have been silent. Now a divergence fails on
 * exactly one side, which also says which one moved.
 *
 * Regenerate with `just csharp-golden`. Run this and `just csharp-test` as a
 * pair; running only one hides a divergence.
 */

interface PlaybackStep {
  note: string;
  seq: number;
  freshSnapshot: boolean;
  hasStreamChanged: boolean;
  streamChanged: boolean;
  arrivalTimeMs: number;
  hasProducedAt: boolean;
  producedAtMs: number;
  expectedTargetDelayMs: number;
  expectedJitterMs: number;
  expectedExcessDelayMs: number;
  expectedLossRate: number;
  expectedReceivedFrames: number;
  expectedLostFrames: number;
  expectedDuplicateFrames: number;
  expectedOutOfOrderFrames: number;
  hasRender: boolean;
  renderClientNowMs: number;
  expectedRenderNull: boolean;
  expectedRenderTimeMs: number;
  expectedPlaybackRate: number;
  expectedCurrentDelayMs: number;
}

interface PredictionStep {
  note: string;
  op: "push" | "acknowledge" | "reconcile" | "reset";
  seq: number;
  input: number;
  hasSeq: boolean;
  state: number;
  expectedAccepted: boolean;
  expectedDropped: boolean;
  expectedDroppedSeq: number;
  expectedAdvanced: boolean;
  expectedAcknowledgedCount: number;
  expectedPendingCount: number;
  expectedOverflowCount: number;
  expectedState: number;
  expectedHasLastAck: boolean;
  expectedLastAck: number;
}

interface Manifest {
  playback: {
    tickIntervalMs: number;
    minDelayMs: number;
    maxDelayMs: number;
    minPlaybackRate: number;
    maxPlaybackRate: number;
    convergenceWindowMs: number;
    attackFactor: number;
    decayFactor: number;
    jitterFactor: number;
    steps: PlaybackStep[];
  };
  prediction: { maxPending: number; steps: PredictionStep[] };
  inputAckFrame: {
    hexBytes: string;
    tick: number;
    seq: number;
    flags: number;
    fullCount: number;
    deltaCount: number;
    removedCount: number;
    exitedCount: number;
    full: { netID: number; epoch: number; entityType: number; producedAtMs: number }[];
    delta: { netID: number; epoch: number }[];
    removedIDs: number[];
    exitedIDs: number[];
    hasInputAck: boolean;
    expectedInputAck: number;
  };
}

const manifest: Manifest = JSON.parse(
  readFileSync(
    join(import.meta.dir, "../../../csharp/Mmokit.Sdk.Core.Tests/testdata/delta_golden.json"),
    "utf8",
  ),
);

function hexToBytes(hex: string): Uint8Array {
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i++) {
    out[i] = Number.parseInt(hex.slice(i * 2, i * 2 + 2), 16);
  }
  return out;
}

// The manifest is float64 in and float64 out on both sides, so this is
// JSON round-trip noise only.
const PRECISION = 9;

describe("AdaptivePlaybackController golden trace", () => {
  it("reproduces every recorded metric and render cursor", () => {
    const c = manifest.playback;
    expect(c.steps.length).toBeGreaterThan(0);

    const controller = new AdaptivePlaybackController({
      tickIntervalMs: c.tickIntervalMs,
      minDelayMs: c.minDelayMs,
      maxDelayMs: c.maxDelayMs,
      minPlaybackRate: c.minPlaybackRate,
      maxPlaybackRate: c.maxPlaybackRate,
      convergenceWindowMs: c.convergenceWindowMs,
      attackFactor: c.attackFactor,
      decayFactor: c.decayFactor,
      jitterFactor: c.jitterFactor,
    });

    for (const step of c.steps) {
      controller.observeFrame({
        seq: step.seq,
        freshSnapshot: step.freshSnapshot,
        ...(step.hasStreamChanged ? { streamChanged: step.streamChanged } : {}),
        arrivalTimeMs: step.arrivalTimeMs,
        ...(step.hasProducedAt ? { producedAtMs: step.producedAtMs } : {}),
      });

      const m = controller.metrics;
      expect(m.targetDelayMs).toBeCloseTo(step.expectedTargetDelayMs, PRECISION);
      expect(m.jitterMs).toBeCloseTo(step.expectedJitterMs, PRECISION);
      expect(m.excessDelayMs).toBeCloseTo(step.expectedExcessDelayMs, PRECISION);
      expect(m.lossRate).toBeCloseTo(step.expectedLossRate, PRECISION);
      expect(m.receivedFrames).toBe(step.expectedReceivedFrames);
      expect(m.lostFrames).toBe(step.expectedLostFrames);
      expect(m.duplicateFrames).toBe(step.expectedDuplicateFrames);
      expect(m.outOfOrderFrames).toBe(step.expectedOutOfOrderFrames);

      if (!step.hasRender) continue;

      const rendered = controller.renderTime(step.renderClientNowMs);
      if (step.expectedRenderNull) {
        expect(rendered).toBeNull();
        continue;
      }
      expect(rendered).not.toBeNull();
      expect(rendered as number).toBeCloseTo(step.expectedRenderTimeMs, PRECISION);

      const after = controller.metrics;
      expect(after.playbackRate).toBeCloseTo(step.expectedPlaybackRate, PRECISION);
      expect(after.currentDelayMs).toBeCloseTo(step.expectedCurrentDelayMs, PRECISION);
    }
  });
});

describe("PredictionBuffer golden trace", () => {
  it("reproduces push/acknowledge/reconcile across a wrap and an overflow", () => {
    const c = manifest.prediction;
    expect(c.steps.length).toBeGreaterThan(0);

    const buffer = new PredictionBuffer<number>({ maxPending: c.maxPending });

    for (const step of c.steps) {
      switch (step.op) {
        case "push": {
          const result = buffer.push(step.seq, step.input);
          expect(result.accepted).toBe(step.expectedAccepted);
          if (step.expectedDropped) {
            expect(result.dropped).toBeDefined();
            expect(result.dropped!.seq).toBe(step.expectedDroppedSeq);
          } else {
            expect(result.dropped).toBeUndefined();
          }
          break;
        }
        case "acknowledge": {
          const result = buffer.acknowledge(step.seq);
          expect(result.advanced).toBe(step.expectedAdvanced);
          expect(result.acknowledgedCount).toBe(step.expectedAcknowledgedCount);
          break;
        }
        case "reconcile": {
          const result = buffer.reconcile(
            step.state,
            step.seq,
            (state, input) => state + input,
          );
          expect(result.state).toBe(step.expectedState);
          expect(result.acknowledgedCount).toBe(step.expectedAcknowledgedCount);
          break;
        }
        case "reset":
          buffer.reset(step.hasSeq ? step.seq : undefined);
          break;
      }

      expect(buffer.pendingCount).toBe(step.expectedPendingCount);
      expect(buffer.overflowCount).toBe(step.expectedOverflowCount);
      if (step.expectedHasLastAck) {
        expect(buffer.lastAcknowledgedSequence).toBe(step.expectedLastAck);
      } else {
        expect(buffer.lastAcknowledgedSequence).toBeNull();
      }
    }
  });
});

describe("FrameFlagInputAck trailer golden", () => {
  it("decodes the four-byte processed-input-sequence trailer", () => {
    const c = manifest.inputAckFrame;
    expect(c.hasInputAck).toBe(true);

    const raw = hexToBytes(c.hexBytes);
    const { header, offset: afterHeader } = decodeFrameHeader(raw, 0);
    expect(header.tick).toBe(c.tick);
    expect(header.seq).toBe(c.seq);
    expect(header.flags).toBe(c.flags);
    expect(header.flags & FRAME_FLAG_INPUT_ACK).not.toBe(0);
    expect(header.fullCount).toBe(c.fullCount);
    expect(header.deltaCount).toBe(c.deltaCount);
    expect(header.removedCount).toBe(c.removedCount);
    expect(header.exitedCount).toBe(c.exitedCount);

    let pos = afterHeader;
    for (let i = 0; i < header.fullCount; i++) {
      const { entry, offset } = decodeFullEntry(raw, pos);
      expect(entry.netID).toBe(c.full[i].netID);
      expect(entry.epoch).toBe(c.full[i].epoch);
      expect(entry.entityType).toBe(c.full[i].entityType);
      expect(entry.producedAtMs).toBe(c.full[i].producedAtMs);
      pos = offset;
    }
    for (let i = 0; i < header.deltaCount; i++) {
      const { entry, offset } = decodeDeltaEntry(raw, pos);
      expect(entry.netID).toBe(c.delta[i].netID);
      expect(entry.epoch).toBe(c.delta[i].epoch);
      pos = offset;
    }
    for (let i = 0; i < header.removedCount; i++) {
      expect(readUint32(raw, pos)).toBe(c.removedIDs[i]);
      pos += 4;
    }
    for (let i = 0; i < header.exitedCount; i++) {
      expect(readUint32(raw, pos)).toBe(c.exitedIDs[i]);
      pos += 4;
    }

    const { sequence, offset: end } = decodeInputAck(raw, pos, header.flags);
    expect(sequence).toBe(c.expectedInputAck);
    expect(end).toBe(raw.length);
  });
});
