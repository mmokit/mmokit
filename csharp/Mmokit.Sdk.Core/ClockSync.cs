using System.Collections.Generic;

namespace Mmokit.Sdk.Core
{
    /// Server-to-client wall-clock offset estimator. Faithful port of
    /// pkg/quantize/ts/clock-sync.ts: offset = sliding-window MAX of
    /// instant = serverMs - clientNowMs over the last InstantWindow samples.
    /// Max (not EWMA) because bursty delivery clusters frames with a fixed
    /// clientNow and advancing serverStamp; the max is the least-delayed
    /// (truest) reading, and window scope lets it drift if base latency shifts.
    ///
    /// Pure + engine-agnostic (clock injected as a scalar). The CONSUMER drives
    /// it from decoded producedAtMs stamps — consistent with the stateless SDK.
    public sealed class ClockSync
    {
        public const int InstantWindow = 40;

        readonly object _sync = new();
        readonly double[] _instants = new double[InstantWindow];
        int _idx;
        int _count;
        double _offsetMs;
        bool _initialized;

        public double OffsetMs
        {
            get
            {
                lock (_sync)
                    return _offsetMs;
            }
        }

        public bool Initialized
        {
            get
            {
                lock (_sync)
                    return _initialized;
            }
        }

        /// Feed one (serverTimeMs, clientNowMs) observation; recompute offset.
        public void ObserveServerTime(double serverTimeMs, double clientNowMs)
            => ObserveServerTimeAndGetOffset(serverTimeMs, clientNowMs);

        /// Observe and return the resulting offset under one clock lock. This
        /// lets AdaptivePlaybackController use the exact observation it just
        /// submitted even when the clock is shared by multiple controllers.
        internal double ObserveServerTimeAndGetOffset(double serverTimeMs, double clientNowMs)
        {
            lock (_sync)
            {
                return ObserveServerTimeUnsafe(serverTimeMs, clientNowMs);
            }
        }

        /// Atomically replace a superseded producer's samples and observe the
        /// replacement stream's first timestamp.
        internal double ResetAndObserveServerTimeAndGetOffset(double serverTimeMs, double clientNowMs)
        {
            lock (_sync)
            {
                ResetUnsafe();
                return ObserveServerTimeUnsafe(serverTimeMs, clientNowMs);
            }
        }

        /// Feed the freshest producedAtMs across a frame's decoded entities.
        public void ObserveFrameStamps(IEnumerable<ulong> producedAtMs, double clientNowMs)
        {
            ulong maxStamp = 0;
            foreach (var p in producedAtMs)
                if (p > maxStamp) maxStamp = p;
            if (maxStamp > 0) ObserveServerTime(maxStamp, clientNowMs);
        }

        /// Drop samples from a superseded producer stream. A replacement
        /// authority may legitimately publish an earlier cluster-clock stamp.
        public void Reset()
        {
            lock (_sync)
            {
                ResetUnsafe();
            }
        }

        double ObserveServerTimeUnsafe(double serverTimeMs, double clientNowMs)
        {
            double instant = serverTimeMs - clientNowMs;
            _instants[_idx] = instant;
            _idx = (_idx + 1) % _instants.Length;
            if (_count < _instants.Length) _count++;

            if (!_initialized)
            {
                _offsetMs = instant;
                _initialized = true;
                return _offsetMs;
            }
            double max = double.NegativeInfinity;
            for (int i = 0; i < _count; i++)
                if (_instants[i] > max) max = _instants[i];
            _offsetMs = max;
            return _offsetMs;
        }

        void ResetUnsafe()
        {
            System.Array.Clear(_instants, 0, _instants.Length);
            _idx = 0;
            _count = 0;
            _offsetMs = 0;
            _initialized = false;
        }

        /// Estimated current server wall-clock ms, given a client clock reading.
        public double EstimatedServerNow(double clientNowMs)
        {
            lock (_sync)
                return clientNowMs + _offsetMs;
        }

        /// Atomically test initialization and estimate server time.
        internal bool TryEstimatedServerNow(double clientNowMs, out double serverNowMs)
        {
            lock (_sync)
            {
                serverNowMs = clientNowMs + _offsetMs;
                return _initialized;
            }
        }
    }
}
