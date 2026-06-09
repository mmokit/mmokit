using System;
using System.Collections.Generic;

namespace Mmokit.Sdk.Core
{
    /// One snapshot of an entity's authoritative state at a producer
    /// cluster-clock time. Port of interpolation-core.ts Sample.
    public struct Sample
    {
        public double WorldX;
        public double WorldY;
        public double VelX;
        public double VelY;
        public double Rotation;
        public double ProducedAtMs;
    }

    /// Interpolated render position computed by InterpolateRing.
    public struct InterpolationResult
    {
        public double RenderX;
        public double RenderY;
        public double RenderRot;
    }

    /// Render-lag interpolation primitives. Faithful port of
    /// pkg/quantize/ts/interpolation-core.ts. The CONSUMER owns the sample
    /// ring (a List&lt;Sample&gt; ordered ascending by ProducedAtMs); these
    /// helpers are stateless — consistent with the stateless-SDK design.
    public static class InterpolationCore
    {
        /// Linear interpolation between a and b at fraction t in [0,1].
        public static double Lerp(double a, double b, double t) => a + (b - a) * t;

        /// Angle lerp taking the shortest path around the unit circle.
        public static double LerpAngle(double a, double b, double t)
        {
            double diff = b - a;
            while (diff > Math.PI) diff -= Math.PI * 2;
            while (diff < -Math.PI) diff += Math.PI * 2;
            return a + diff * t;
        }

        /// Append a sample to the ring. Drops samples whose stamp predates
        /// the ring tip (out-of-order arrival across cell handoffs). Evicts
        /// the oldest sample when the ring would exceed ringSize.
        public static void PushSample(List<Sample> ring, Sample s, int ringSize)
        {
            if (ring.Count > 0 && s.ProducedAtMs < ring[ring.Count - 1].ProducedAtMs)
                return;
            ring.Add(s);
            if (ring.Count > ringSize)
                ring.RemoveAt(0);
        }

        /// True if producedAtMs is older than the ring tip — i.e. PushSample
        /// would drop it. Glue code gates non-interpolated field snapshots on
        /// this to obey the same monotonicity rule as the position ring.
        public static bool IsStaleSample(List<Sample> ring, double producedAtMs)
        {
            return ring.Count > 0 && producedAtMs < ring[ring.Count - 1].ProducedAtMs;
        }

        /// Compute the interpolated render position for one ring at
        /// renderTimeMs. Returns false (result = default) for an empty ring
        /// (caller leaves previous render state untouched). One sample →
        /// static. Two+ → newest bracketing pair lerped; past the newest
        /// sample, extrapolate with its velocity capped to maxExtrapolateMs.
        public static bool InterpolateRing(
            List<Sample> ring,
            double renderTimeMs,
            double maxExtrapolateMs,
            double renderDelayMs,
            out InterpolationResult result)
        {
            int n = ring.Count;
            result = default;
            if (n == 0) return false;
            if (n == 1)
            {
                var only = ring[0];
                result = new InterpolationResult { RenderX = only.WorldX, RenderY = only.WorldY, RenderRot = only.Rotation };
                return true;
            }

            Sample s0 = ring[0];
            Sample s1 = ring[1];
            for (int i = 1; i < n - 1; i++)
            {
                if (ring[i].ProducedAtMs <= renderTimeMs)
                {
                    s0 = ring[i];
                    s1 = ring[i + 1];
                }
            }

            double effS0Stamp = Math.Max(s0.ProducedAtMs, s1.ProducedAtMs - renderDelayMs);

            if (renderTimeMs <= effS0Stamp)
            {
                result = new InterpolationResult { RenderX = s0.WorldX, RenderY = s0.WorldY, RenderRot = s0.Rotation };
                return true;
            }
            if (renderTimeMs >= s1.ProducedAtMs)
            {
                double extMs = Math.Min(renderTimeMs - s1.ProducedAtMs, maxExtrapolateMs);
                double extS = extMs / 1000.0;
                result = new InterpolationResult
                {
                    RenderX = s1.WorldX + s1.VelX * extS,
                    RenderY = s1.WorldY + s1.VelY * extS,
                    RenderRot = s1.Rotation,
                };
                return true;
            }
            double t = (renderTimeMs - effS0Stamp) / (s1.ProducedAtMs - effS0Stamp);
            result = new InterpolationResult
            {
                RenderX = Lerp(s0.WorldX, s1.WorldX, t),
                RenderY = Lerp(s0.WorldY, s1.WorldY, t),
                RenderRot = LerpAngle(s0.Rotation, s1.Rotation, t),
            };
            return true;
        }
    }
}
