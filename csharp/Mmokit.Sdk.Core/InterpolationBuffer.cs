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
        public const int DefaultRingSize = 4;
        public const double DefaultRenderDelayMs = 100;
        public const double DefaultMaxExtrapolateMs = 50;

        readonly List<Sample> _ring = new();

        public int RingSize { get; }
        public double RenderDelayMs { get; }
        public double MaxExtrapolateMs { get; }

        public InterpolationBuffer(
            int ringSize = DefaultRingSize,
            double renderDelayMs = DefaultRenderDelayMs,
            double maxExtrapolateMs = DefaultMaxExtrapolateMs)
        {
            RingSize = ringSize;
            RenderDelayMs = renderDelayMs;
            MaxExtrapolateMs = maxExtrapolateMs;
        }

        /// Stale-gated append (drops frames older than the newest held).
        public void Push(Sample s) => InterpolationCore.PushSample(_ring, s, RingSize);

        /// Whether Push(s) would drop s as stale. Gate non-interpolated field
        /// snapshots (size/health/…) on this to match the position ring.
        public bool IsStale(double producedAtMs) => InterpolationCore.IsStaleSample(_ring, producedAtMs);

        /// Newest sample held (for a prev-rotation fallback on stationary entities).
        public bool TryNewest(out Sample s)
        {
            if (_ring.Count > 0) { s = _ring[_ring.Count - 1]; return true; }
            s = default;
            return false;
        }

        /// Interpolated render pose at the given server render time.
        public bool SampleAt(double renderTimeMs, out InterpolationResult result)
            => InterpolationCore.InterpolateRing(_ring, renderTimeMs, MaxExtrapolateMs, RenderDelayMs, out result);
    }
}
