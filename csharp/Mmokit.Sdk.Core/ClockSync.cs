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

        public double OffsetMs { get; private set; }
        public bool Initialized { get; private set; }

        readonly double[] _instants = new double[InstantWindow];
        int _idx;
        int _count;

        /// Feed one (serverTimeMs, clientNowMs) observation; recompute offset.
        public void ObserveServerTime(double serverTimeMs, double clientNowMs)
        {
            double instant = serverTimeMs - clientNowMs;
            _instants[_idx] = instant;
            _idx = (_idx + 1) % _instants.Length;
            if (_count < _instants.Length) _count++;

            if (!Initialized)
            {
                OffsetMs = instant;
                Initialized = true;
                return;
            }
            double max = double.NegativeInfinity;
            for (int i = 0; i < _count; i++)
                if (_instants[i] > max) max = _instants[i];
            OffsetMs = max;
        }

        /// Feed the freshest producedAtMs across a frame's decoded entities.
        public void ObserveFrameStamps(IEnumerable<ulong> producedAtMs, double clientNowMs)
        {
            ulong maxStamp = 0;
            foreach (var p in producedAtMs)
                if (p > maxStamp) maxStamp = p;
            if (maxStamp > 0) ObserveServerTime(maxStamp, clientNowMs);
        }

        /// Estimated current server wall-clock ms, given a client clock reading.
        public double EstimatedServerNow(double clientNowMs) => clientNowMs + OffsetMs;
    }
}
