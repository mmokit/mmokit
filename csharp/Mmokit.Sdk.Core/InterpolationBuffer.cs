using System.Collections.Generic;

namespace Mmokit.Sdk.Core
{
    /// Optional stateful per-entity playback buffer: a Sample ring + the two
    /// orchestration footguns bundled (stale-gated push, interpolate-at-render-
    /// time). Operates ONLY on Sample — carries no game semantics. The consumer
    /// owns one buffer per entity, converts its entity -> Sample (incl. rotation
    /// rule via TryNewest), and applies the InterpolationResult to its view.
    /// Headless/bot consumers ignore this and use InterpolationCore directly.
    public sealed class InterpolationBuffer
    {
        public const int DefaultRingSize = 8;
        public const double DefaultRenderDelayMs = 100;
        public const double DefaultMaxExtrapolateMs = 50;

        readonly object _sync = new();
        readonly List<Sample> _ring = new();
        uint? _authorityEpoch;

        public int RingSize { get; }
        public double RenderDelayMs { get; }
        public double MaxExtrapolateMs { get; }
        public int Count
        {
            get
            {
                lock (_sync)
                    return _ring.Count;
            }
        }

        public uint? AuthorityEpoch
        {
            get
            {
                lock (_sync)
                    return _authorityEpoch;
            }
        }

        public InterpolationBuffer(
            int ringSize = DefaultRingSize,
            double renderDelayMs = DefaultRenderDelayMs,
            double maxExtrapolateMs = DefaultMaxExtrapolateMs)
        {
            if (ringSize <= 0)
                throw new System.ArgumentOutOfRangeException(nameof(ringSize), "Ring size must be positive.");
            if (renderDelayMs < 0)
                throw new System.ArgumentOutOfRangeException(nameof(renderDelayMs), "Render delay cannot be negative.");
            if (maxExtrapolateMs < 0)
                throw new System.ArgumentOutOfRangeException(nameof(maxExtrapolateMs), "Maximum extrapolation cannot be negative.");

            RingSize = ringSize;
            RenderDelayMs = renderDelayMs;
            MaxExtrapolateMs = maxExtrapolateMs;
        }

        /// Stale-gated append (drops frames older than the newest held).
        public SamplePushStatus Push(Sample s) => PushWithStatus(s);

        /// Epoch-aware append with an explicit outcome for callers that need
        /// to gate non-interpolated fields on the exact same decision.
        public SamplePushStatus PushWithStatus(Sample s)
        {
            lock (_sync)
                return InterpolationCore.PushSampleWithStatus(_ring, ref _authorityEpoch, s, RingSize);
        }

        /// Whether Push(s) would drop s as stale. Gate non-interpolated field
        /// snapshots (size/health/…) on this to match the position ring.
        public bool IsStale(double producedAtMs, uint? authorityEpoch = null)
        {
            lock (_sync)
                return InterpolationCore.IsStaleSample(_ring, producedAtMs, _authorityEpoch, authorityEpoch);
        }

        /// Newest sample held (for a prev-rotation fallback on stationary entities).
        public bool TryNewest(out Sample s)
        {
            lock (_sync)
            {
                if (_ring.Count > 0) { s = _ring[_ring.Count - 1]; return true; }
                s = default;
                return false;
            }
        }

        /// Interpolated render pose at the given server render time.
        public bool SampleAt(double renderTimeMs, out InterpolationResult result)
        {
            lock (_sync)
                return InterpolationCore.InterpolateRing(_ring, renderTimeMs, MaxExtrapolateMs, RenderDelayMs, out result);
        }

        /// Interpolated render pose using a dynamic delay supplied by an
        /// adaptive playback controller for this frame.
        public bool SampleAt(double renderTimeMs, double renderDelayMs, out InterpolationResult result)
        {
            if (renderDelayMs < 0)
                throw new System.ArgumentOutOfRangeException(nameof(renderDelayMs), "Render delay cannot be negative.");
            lock (_sync)
                return InterpolationCore.InterpolateRing(_ring, renderTimeMs, MaxExtrapolateMs, renderDelayMs, out result);
        }

        /// Explicitly reset playback history, for example on a fresh snapshot
        /// after reconnect. AuthorityEpoch may seed the new timeline.
        public void Reset(uint? authorityEpoch = null)
        {
            lock (_sync)
            {
                _ring.Clear();
                _authorityEpoch = authorityEpoch;
            }
        }
    }
}
