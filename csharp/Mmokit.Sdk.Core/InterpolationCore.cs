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
        /// Authority epoch carried by the replication entry. Null keeps the
        /// pre-epoch behavior for callers that construct samples manually.
        public uint? AuthorityEpoch;
    }

    public enum InterpolationMode
    {
        Hold,
        Interpolate,
        Extrapolate,
    }

    /// Interpolated render position computed by InterpolateRing.
    public struct InterpolationResult
    {
        public double RenderX;
        public double RenderY;
        public double RenderRot;
        public InterpolationMode Mode;
        /// Producer-time distance applied to the newest sample's velocity.
        /// This remains populated at the extrapolation cap even when Mode is
        /// Hold because playback has exhausted the permitted prediction span.
        public double ExtrapolatedMs;
    }

    public enum SampleRejectReason
    {
        StaleTime,
        OlderEpoch,
    }

    /// Outcome of an epoch-aware ring append.
    public readonly struct SamplePushStatus
    {
        public SamplePushStatus(bool accepted, bool reset, SampleRejectReason? reason = null)
        {
            Accepted = accepted;
            Reset = reset;
            Reason = reason;
        }

        public bool Accepted { get; }
        /// True when a newer authority epoch moved producer time backward and
        /// the old timeline had to be discarded before accepting the sample.
        public bool Reset { get; }
        public SampleRejectReason? Reason { get; }
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
            AppendSample(ring, s, ringSize);
        }

        /// Epoch-aware append used by InterpolationBuffer. An older authority
        /// can never overwrite a newer one. A newer epoch preserves the ring
        /// when producer time remains monotonic, but resets it when the new
        /// producer's clock moves backward. Samples without an epoch retain
        /// the legacy timestamp-only behavior.
        public static SamplePushStatus PushSampleWithStatus(
            List<Sample> ring,
            ref uint? authorityEpoch,
            Sample s,
            int ringSize)
        {
            if (ringSize <= 0)
                throw new ArgumentOutOfRangeException(nameof(ringSize), "Ring size must be positive.");

            if (s.AuthorityEpoch.HasValue && authorityEpoch.HasValue)
            {
                uint incomingEpoch = s.AuthorityEpoch.Value;
                uint currentEpoch = authorityEpoch.Value;
                if (incomingEpoch != currentEpoch)
                {
                    if (!IsAuthorityEpochNewer(incomingEpoch, currentEpoch))
                        return new SamplePushStatus(false, false, SampleRejectReason.OlderEpoch);

                    authorityEpoch = incomingEpoch;
                    bool timelineMovedBackward = ring.Count > 0 &&
                        s.ProducedAtMs < ring[ring.Count - 1].ProducedAtMs;
                    if (timelineMovedBackward)
                        ring.Clear();

                    AppendSample(ring, s, ringSize);
                    return new SamplePushStatus(true, timelineMovedBackward);
                }
            }
            else if (s.AuthorityEpoch.HasValue)
            {
                authorityEpoch = s.AuthorityEpoch.Value;
                bool timelineMovedBackward = ring.Count > 0 &&
                    s.ProducedAtMs < ring[ring.Count - 1].ProducedAtMs;
                if (timelineMovedBackward)
                    ring.Clear();

                AppendSample(ring, s, ringSize);
                return new SamplePushStatus(true, timelineMovedBackward);
            }

            if (ring.Count > 0 && s.ProducedAtMs < ring[ring.Count - 1].ProducedAtMs)
                return new SamplePushStatus(false, false, SampleRejectReason.StaleTime);

            AppendSample(ring, s, ringSize);
            return new SamplePushStatus(true, false);
        }

        /// True if producedAtMs is older than the ring tip — i.e. PushSample
        /// would drop it. Glue code gates non-interpolated field snapshots on
        /// this to obey the same monotonicity rule as the position ring.
        public static bool IsStaleSample(List<Sample> ring, double producedAtMs)
        {
            return ring.Count > 0 && producedAtMs < ring[ring.Count - 1].ProducedAtMs;
        }

        /// Epoch-aware stale predicate matching PushSampleWithStatus. A newer
        /// epoch is accepted even if it must reset a backward producer
        /// timeline; an older epoch is always stale.
        public static bool IsStaleSample(
            List<Sample> ring,
            double producedAtMs,
            uint? currentAuthorityEpoch,
            uint? incomingAuthorityEpoch)
        {
            if (incomingAuthorityEpoch.HasValue && currentAuthorityEpoch.HasValue)
            {
                uint incomingEpoch = incomingAuthorityEpoch.Value;
                uint currentEpoch = currentAuthorityEpoch.Value;
                if (incomingEpoch != currentEpoch)
                    return !IsAuthorityEpochNewer(incomingEpoch, currentEpoch);
            }
            return IsStaleSample(ring, producedAtMs);
        }

        /// RFC 1982-style uint serial comparison used by authority epochs.
        /// The exactly-half-range case is intentionally unordered.
        public static bool IsAuthorityEpochNewer(uint candidate, uint reference)
        {
            uint distance = unchecked(candidate - reference);
            return distance != 0 && distance < 0x80000000u;
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
                result = new InterpolationResult
                {
                    RenderX = only.WorldX,
                    RenderY = only.WorldY,
                    RenderRot = only.Rotation,
                    Mode = InterpolationMode.Hold,
                    ExtrapolatedMs = 0,
                };
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
                result = new InterpolationResult
                {
                    RenderX = s0.WorldX,
                    RenderY = s0.WorldY,
                    RenderRot = s0.Rotation,
                    Mode = InterpolationMode.Hold,
                    ExtrapolatedMs = 0,
                };
                return true;
            }
            if (renderTimeMs >= s1.ProducedAtMs)
            {
                double requestedExtMs = Math.Max(0, renderTimeMs - s1.ProducedAtMs);
                double extMs = Math.Min(requestedExtMs, Math.Max(0, maxExtrapolateMs));
                double extS = extMs / 1000.0;
                result = new InterpolationResult
                {
                    RenderX = s1.WorldX + s1.VelX * extS,
                    RenderY = s1.WorldY + s1.VelY * extS,
                    RenderRot = s1.Rotation,
                    Mode = extMs > 0 && requestedExtMs <= maxExtrapolateMs
                        ? InterpolationMode.Extrapolate
                        : InterpolationMode.Hold,
                    ExtrapolatedMs = extMs,
                };
                return true;
            }
            double t = (renderTimeMs - effS0Stamp) / (s1.ProducedAtMs - effS0Stamp);
            result = new InterpolationResult
            {
                RenderX = Lerp(s0.WorldX, s1.WorldX, t),
                RenderY = Lerp(s0.WorldY, s1.WorldY, t),
                RenderRot = LerpAngle(s0.Rotation, s1.Rotation, t),
                Mode = InterpolationMode.Interpolate,
                ExtrapolatedMs = 0,
            };
            return true;
        }

        static void AppendSample(List<Sample> ring, Sample s, int ringSize)
        {
            if (ringSize <= 0)
                throw new ArgumentOutOfRangeException(nameof(ringSize), "Ring size must be positive.");
            ring.Add(s);
            if (ring.Count > ringSize)
                ring.RemoveAt(0);
        }
    }
}
