import { isForwardSequence, sequenceDistance } from "./playback-controller.js";

export const DEFAULT_MAX_PENDING_INPUTS = 128;

export interface PendingInput<TInput> {
  seq: number;
  input: TInput;
}

export interface PredictionBufferConfig {
  maxPending?: number;
}

export interface PushInputResult<TInput> {
  accepted: boolean;
  /** Oldest unacknowledged input evicted to keep the buffer bounded. */
  dropped?: PendingInput<TInput>;
  reason?: "duplicate-or-stale";
}

export interface AcknowledgeResult {
  advanced: boolean;
  acknowledgedCount: number;
  pendingCount: number;
}

export interface ReconciliationResult<TState> {
  state: TState;
  pendingCount: number;
  acknowledgedCount: number;
}

function isAcknowledgedBy(sequence: number, cumulativeAck: number): boolean {
  const distance = sequenceDistance(sequence, cumulativeAck);
  return distance === 0 || distance < 0x80000000;
}

/**
 * A transport-independent buffer for server-authoritative client prediction.
 *
 * Inputs are retained in uint32 sequence order. A cumulative server ACK drops
 * confirmed inputs, then reconcile replays the remaining inputs over the
 * supplied authoritative state. The core knows nothing about movement or
 * physics: callers own state/input cloning and the deterministic replay
 * callback. If inputs are mutable, clone them before push.
 */
export class PredictionBuffer<TInput> {
  readonly maxPending: number;
  private entries: PendingInput<TInput>[] = [];
  private newestSequence: number | null = null;
  private lastAckSequence: number | null = null;
  private overflowCountValue = 0;

  constructor(config: PredictionBufferConfig = {}) {
    const configured = config.maxPending ?? DEFAULT_MAX_PENDING_INPUTS;
    if (!Number.isSafeInteger(configured) || configured < 1) {
      throw new RangeError("maxPending must be a positive safe integer");
    }
    this.maxPending = configured;
  }

  get pendingCount(): number {
    return this.entries.length;
  }

  get overflowCount(): number {
    return this.overflowCountValue;
  }

  get lastAcknowledgedSequence(): number | null {
    return this.lastAckSequence;
  }

  /** A shallow snapshot; input values remain caller-owned. */
  get pendingInputs(): readonly PendingInput<TInput>[] {
    return this.entries.slice();
  }

  push(seq: number, input: TInput): PushInputResult<TInput> {
    const sequence = seq >>> 0;
    if (
      (this.lastAckSequence !== null &&
        !isForwardSequence(this.lastAckSequence, sequence)) ||
      (this.newestSequence !== null &&
        !isForwardSequence(this.newestSequence, sequence))
    ) {
      return { accepted: false, reason: "duplicate-or-stale" };
    }

    this.entries.push({ seq: sequence, input });
    this.newestSequence = sequence;

    if (this.entries.length <= this.maxPending) {
      return { accepted: true };
    }

    const dropped = this.entries.shift()!;
    this.overflowCountValue++;
    return { accepted: true, dropped };
  }

  /** Apply a wrap-safe cumulative ACK. Duplicate/stale ACKs are no-ops. */
  acknowledge(ackSeq: number): AcknowledgeResult {
    const ack = ackSeq >>> 0;
    if (
      this.lastAckSequence !== null &&
      ack !== this.lastAckSequence &&
      !isForwardSequence(this.lastAckSequence, ack)
    ) {
      return {
        advanced: false,
        acknowledgedCount: 0,
        pendingCount: this.entries.length,
      };
    }
    if (ack === this.lastAckSequence) {
      return {
        advanced: false,
        acknowledgedCount: 0,
        pendingCount: this.entries.length,
      };
    }

    this.lastAckSequence = ack;
    let acknowledgedCount = 0;
    while (
      this.entries.length > 0 &&
      isAcknowledgedBy(this.entries[0].seq, ack)
    ) {
      this.entries.shift();
      acknowledgedCount++;
    }

    return {
      advanced: true,
      acknowledgedCount,
      pendingCount: this.entries.length,
    };
  }

  /**
   * Replay every pending input over an authoritative state WITHOUT
   * acknowledging. Use when a correction arrives out of band from the ACK
   * frontier — reconcile is the usual entry point.
   */
  replay<TState>(
    authoritativeState: TState,
    replay: (state: TState, input: TInput, seq: number) => TState,
  ): TState {
    let state = authoritativeState;
    for (const pending of this.entries) {
      state = replay(state, pending.input, pending.seq);
    }
    return state;
  }

  reconcile<TState>(
    authoritativeState: TState,
    ackSeq: number,
    replay: (state: TState, input: TInput, seq: number) => TState,
  ): ReconciliationResult<TState> {
    const acknowledgement = this.acknowledge(ackSeq);
    let state = authoritativeState;
    for (const pending of this.entries) {
      state = replay(state, pending.input, pending.seq);
    }
    return {
      state,
      pendingCount: this.entries.length,
      acknowledgedCount: acknowledgement.acknowledgedCount,
    };
  }

  /**
   * Clear pending prediction state at a connection/session boundary. Pass the
   * authoritative cumulative ACK when known to seed subsequent sequence order.
   */
  reset(ackSeq?: number): void {
    this.entries.length = 0;
    this.newestSequence = null;
    this.lastAckSequence = ackSeq === undefined ? null : ackSeq >>> 0;
    this.overflowCountValue = 0;
  }
}

/**
 * Callback-based helpers for consumers that want to measure or visually blend
 * a correction without coupling this core to a vector or math package.
 *
 * Mirrors PredictionCorrection in csharp/Mmokit.Sdk.Core/PredictionBuffer.cs so
 * the two SDK cores present one contract.
 */
export const predictionCorrection = {
  measure<TState>(
    predictedState: TState,
    reconciledState: TState,
    measureError: (predicted: TState, reconciled: TState) => number,
  ): number {
    return measureError(predictedState, reconciledState);
  },

  /** factor is clamped to 0..1 before it reaches the blend callback. */
  blend<TState>(
    displayedState: TState,
    reconciledState: TState,
    factor: number,
    blend: (displayed: TState, reconciled: TState, factor: number) => TState,
  ): TState {
    const clamped = Math.max(0, Math.min(1, factor));
    return blend(displayedState, reconciledState, clamped);
  },
};
