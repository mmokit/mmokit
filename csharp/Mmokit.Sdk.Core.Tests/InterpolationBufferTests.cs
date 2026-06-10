using Mmokit.Sdk.Core;
using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    public class InterpolationBufferTests
    {
        static Sample S(double x, double t) => new Sample { WorldX = x, WorldY = 0, VelX = 0, VelY = 0, Rotation = 0, ProducedAtMs = t };

        [Fact]
        public void EmptyBufferSamplesNull()
        {
            var b = new InterpolationBuffer();
            Assert.False(b.SampleAt(123, out _));
            Assert.False(b.TryNewest(out _));
        }

        [Fact]
        public void StaleGateDropsOutOfOrderFrames()
        {
            var b = new InterpolationBuffer();
            b.Push(S(0, 1000));
            b.Push(S(10, 1050));
            Assert.True(b.IsStale(1000));        // older than tip 1050
            b.Push(S(99, 1000));                 // dropped by Push
            Assert.True(b.TryNewest(out var tip));
            Assert.Equal(10, tip.WorldX);        // still the 1050 sample
        }

        [Fact]
        public void InterpolatesMidpoint()
        {
            // renderDelayMs must be >= the inter-sample gap (100ms) for the
            // effS0Stamp cap to leave s0's stamp intact, so render-time 1050
            // lerps the full window rather than clamping to s0 (matches the
            // shared InterpolationCore / interpolation-core.ts semantics).
            var b = new InterpolationBuffer(renderDelayMs: 100);
            b.Push(S(0, 1000));
            b.Push(S(100, 1100));
            Assert.True(b.SampleAt(1050, out var r));
            Assert.Equal(50, r.RenderX, 6);      // halfway between the two
        }

        [Fact]
        public void RingEvictsBeyondSize()
        {
            var b = new InterpolationBuffer(ringSize: 2);
            b.Push(S(1, 1000));
            b.Push(S(2, 1100));
            b.Push(S(3, 1200));                  // evicts the 1000 sample
            Assert.True(b.SampleAt(900, out var r)); // before oldest -> clamps to oldest held (1100)
            Assert.Equal(2, r.RenderX, 6);
        }
    }
}
