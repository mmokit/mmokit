using System;
using System.Threading;
using System.Threading.Tasks;
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

        [Fact]
        public void DefaultsToEightSamples()
        {
            var b = new InterpolationBuffer();
            Assert.Equal(8, b.RingSize);
        }

        [Fact]
        public void DynamicRenderDelayOverridesConfiguredDelayPerSample()
        {
            var b = new InterpolationBuffer(renderDelayMs: 100);
            b.Push(S(0, 1000));
            b.Push(S(100, 1200));

            Assert.True(b.SampleAt(1150, renderDelayMs: 100, out var withShortDelay));
            Assert.True(b.SampleAt(1150, renderDelayMs: 200, out var withLongDelay));
            Assert.Equal(50, withShortDelay.RenderX, 6);
            Assert.Equal(75, withLongDelay.RenderX, 6);
        }

        [Fact]
        public void PushWithStatusTracksAuthorityAndReset()
        {
            var b = new InterpolationBuffer();
            Assert.True(b.PushWithStatus(new Sample
            {
                WorldX = 1,
                ProducedAtMs = 1000,
                AuthorityEpoch = 7,
            }).Accepted);

            SamplePushStatus staleAuthority = b.PushWithStatus(new Sample
            {
                WorldX = 99,
                ProducedAtMs = 1100,
                AuthorityEpoch = 6,
            });
            Assert.False(staleAuthority.Accepted);
            Assert.Equal(SampleRejectReason.OlderEpoch, staleAuthority.Reason);

            SamplePushStatus reset = b.PushWithStatus(new Sample
            {
                WorldX = 2,
                ProducedAtMs = 900,
                AuthorityEpoch = 8,
            });
            Assert.True(reset.Accepted);
            Assert.True(reset.Reset);
            Assert.Equal(8u, b.AuthorityEpoch);
            Assert.Equal(1, b.Count);
            Assert.True(b.TryNewest(out var newest));
            Assert.Equal(2, newest.WorldX);
        }

        [Fact]
        public async Task ConcurrentPushSampleAndResetRemainSafe()
        {
            const int iterations = 20_000;
            var buffer = new InterpolationBuffer(ringSize: 16);
            using var start = new ManualResetEventSlim();

            Task producer = Task.Run(() =>
            {
                start.Wait();
                for (int i = 1; i <= iterations; i++)
                    buffer.Push(S(i, i));
            });
            Task sampler = Task.Run(() =>
            {
                start.Wait();
                for (int i = 1; i <= iterations; i++)
                {
                    buffer.SampleAt(i, out _);
                    buffer.TryNewest(out _);
                    _ = buffer.IsStale(i - 1);
                    Assert.InRange(buffer.Count, 0, buffer.RingSize);
                    _ = buffer.AuthorityEpoch;
                }
            });
            Task resetter = Task.Run(() =>
            {
                start.Wait();
                for (int i = 0; i < iterations / 20; i++)
                    buffer.Reset();
            });

            start.Set();
            await Task.WhenAll(producer, sampler, resetter).WaitAsync(TimeSpan.FromSeconds(20));

            buffer.Reset(authorityEpoch: 9);
            Assert.True(buffer.PushWithStatus(new Sample
            {
                WorldX = 42,
                ProducedAtMs = iterations + 1,
                AuthorityEpoch = 9,
            }).Accepted);
            Assert.Equal(1, buffer.Count);
            Assert.Equal(9u, buffer.AuthorityEpoch);
            Assert.True(buffer.TryNewest(out var newest));
            Assert.Equal(42, newest.WorldX);
        }
    }
}
