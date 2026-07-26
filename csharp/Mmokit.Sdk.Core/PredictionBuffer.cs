using System;
using System.Collections.Generic;

namespace Mmokit.Sdk.Core
{
    /// One locally-predicted input that has not yet been acknowledged by the
    /// authoritative server.
    public readonly struct PendingInput<TInput>
    {
        public PendingInput(uint sequence, TInput input)
        {
            Sequence = sequence;
            Input = input;
        }

        public uint Sequence { get; }
        public TInput Input { get; }
    }

    /// <summary>
    /// Bounded, engine-agnostic input history for client prediction.
    ///
    /// The buffer does not simulate game state and never decides authority. A
    /// client records each input after assigning its wire sequence, then feeds
    /// authoritative acknowledgements and snapshots back into this class.
    /// Reconciliation always starts from the authoritative state and replays
    /// only the still-unacknowledged inputs through a caller-owned callback.
    ///
    /// Sequence comparisons use RFC 1982-style uint serial arithmetic, so
    /// acknowledgement and replay remain correct across uint wraparound.
    /// Public operations are thread-safe so network acknowledgements may arrive
    /// on a background callback while the game thread records or replays input.
    /// Caller replay callbacks are always invoked after releasing the buffer
    /// lock.
    /// </summary>
    public sealed class PredictionBuffer<TInput>
    {
        public const int DefaultCapacity = 128;

        readonly object _sync = new();
        readonly List<PendingInput<TInput>> _pending;
        bool _hasLastAcknowledgement;
        uint _lastAcknowledgedSequence;
        long _droppedInputCount;

        public PredictionBuffer(int capacity = DefaultCapacity)
        {
            if (capacity <= 0)
                throw new ArgumentOutOfRangeException(nameof(capacity), "Prediction capacity must be positive.");

            Capacity = capacity;
            _pending = new List<PendingInput<TInput>>(capacity);
        }

        public int Capacity { get; }
        public int Count
        {
            get
            {
                lock (_sync)
                    return _pending.Count;
            }
        }

        /// A stable snapshot of the pending history. Mutating the buffer after
        /// reading this property cannot change the returned collection.
        public IReadOnlyList<PendingInput<TInput>> Pending
        {
            get
            {
                lock (_sync)
                    return _pending.ToArray();
            }
        }

        /// Number of oldest, still-unacknowledged inputs evicted to honor
        /// Capacity. A non-zero value is useful telemetry and normally means a
        /// consumer should prefer a hard correction over a long visual blend.
        public long DroppedInputCount
        {
            get
            {
                lock (_sync)
                    return _droppedInputCount;
            }
        }

        public bool HasLastAcknowledgement
        {
            get
            {
                lock (_sync)
                    return _hasLastAcknowledgement;
            }
        }

        public uint LastAcknowledgedSequence
        {
            get
            {
                lock (_sync)
                {
                    if (!_hasLastAcknowledgement)
                        throw new InvalidOperationException("No input acknowledgement has been observed.");
                    return _lastAcknowledgedSequence;
                }
            }
        }

        /// True when candidate is later than reference in the uint serial
        /// number space. Comparisons exactly half the uint range apart are
        /// intentionally treated as unordered.
        public static bool IsSequenceNewer(uint candidate, uint reference)
        {
            uint distance = unchecked(candidate - reference);
            return distance != 0 && distance < 0x80000000u;
        }

        /// Record a newly-sent local input. Returns false for a duplicate,
        /// out-of-order sequence, or an input already covered by the latest
        /// acknowledgement. When full, the oldest pending input is evicted.
        public bool TryRecord(uint sequence, TInput input)
        {
            lock (_sync)
            {
                if (_hasLastAcknowledgement &&
                    !IsSequenceNewer(sequence, _lastAcknowledgedSequence))
                {
                    return false;
                }

                if (_pending.Count > 0 && !IsSequenceNewer(sequence, _pending[_pending.Count - 1].Sequence))
                    return false;

                if (_pending.Count == Capacity)
                {
                    _pending.RemoveAt(0);
                    _droppedInputCount++;
                }

                _pending.Add(new PendingInput<TInput>(sequence, input));
                return true;
            }
        }

        /// Apply an authoritative cumulative acknowledgement. Returns the
        /// number of pending inputs removed. Duplicate and stale
        /// acknowledgements are ignored and never move the acknowledgement
        /// frontier backward.
        public int Acknowledge(uint acknowledgedSequence)
        {
            lock (_sync)
                return AcknowledgeLocked(acknowledgedSequence);
        }

        int AcknowledgeLocked(uint acknowledgedSequence)
        {
            if (_hasLastAcknowledgement && !IsSequenceNewer(acknowledgedSequence, _lastAcknowledgedSequence))
                return 0;

            _hasLastAcknowledgement = true;
            _lastAcknowledgedSequence = acknowledgedSequence;

            int removeCount = 0;
            while (removeCount < _pending.Count)
            {
                uint sequence = _pending[removeCount].Sequence;
                if (sequence != acknowledgedSequence && !IsSequenceNewer(acknowledgedSequence, sequence))
                    break;
                removeCount++;
            }

            if (removeCount > 0)
                _pending.RemoveRange(0, removeCount);
            return removeCount;
        }

        /// Replay all pending inputs, in sequence order, starting from an
        /// authoritative state. The callback owns all simulation semantics.
        public TState Replay<TState>(
            TState authoritativeState,
            Func<TState, uint, TInput, TState> replayInput)
        {
            if (replayInput == null)
                throw new ArgumentNullException(nameof(replayInput));

            PendingInput<TInput>[] pending;
            lock (_sync)
                pending = _pending.ToArray();

            TState state = authoritativeState;
            for (int i = 0; i < pending.Length; i++)
            {
                PendingInput<TInput> input = pending[i];
                state = replayInput(state, input.Sequence, input.Input);
            }
            return state;
        }

        /// Acknowledge through acknowledgedSequence, then rebuild predicted
        /// state from the supplied authoritative snapshot and remaining input
        /// history.
        public TState Reconcile<TState>(
            uint acknowledgedSequence,
            TState authoritativeState,
            Func<TState, uint, TInput, TState> replayInput)
        {
            if (replayInput == null)
                throw new ArgumentNullException(nameof(replayInput));

            PendingInput<TInput>[] pending;
            lock (_sync)
            {
                AcknowledgeLocked(acknowledgedSequence);
                pending = _pending.ToArray();
            }

            TState state = authoritativeState;
            for (int i = 0; i < pending.Length; i++)
            {
                PendingInput<TInput> input = pending[i];
                state = replayInput(state, input.Sequence, input.Input);
            }
            return state;
        }

        /// Drop all pending history and the acknowledgement frontier. This is
        /// appropriate after reconnect or another explicit session reset.
        public void Reset()
        {
            lock (_sync)
            {
                _pending.Clear();
                _hasLastAcknowledgement = false;
                _lastAcknowledgedSequence = 0;
                _droppedInputCount = 0;
            }
        }
    }

    /// Callback-based helpers for consumers that want to measure or visually
    /// blend a correction without coupling the SDK to a vector/math package.
    public static class PredictionCorrection
    {
        public static double Measure<TState>(
            TState predictedState,
            TState reconciledState,
            Func<TState, TState, double> measureError)
        {
            if (measureError == null)
                throw new ArgumentNullException(nameof(measureError));
            return measureError(predictedState, reconciledState);
        }

        public static TState Blend<TState>(
            TState displayedState,
            TState reconciledState,
            double factor,
            Func<TState, TState, double, TState> blend)
        {
            if (blend == null)
                throw new ArgumentNullException(nameof(blend));

            double clamped = Math.Max(0.0, Math.Min(1.0, factor));
            return blend(displayedState, reconciledState, clamped);
        }
    }
}
