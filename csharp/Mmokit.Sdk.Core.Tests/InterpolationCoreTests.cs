using System;
using System.Collections.Generic;
using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    public class InterpolationCoreTests
    {
        const double Eps = 1e-9;

        [Fact]
        public void Lerp_Midpoint()
        {
            Assert.Equal(5.0, InterpolationCore.Lerp(0, 10, 0.5), 12);
        }

        [Fact]
        public void LerpAngle_TakesShortestPath()
        {
            double a = 3.0, b = -3.0;
            double mid = InterpolationCore.LerpAngle(a, b, 0.5);
            Assert.Equal(3.0 + ((b - a) + 2 * Math.PI) / 2.0, mid, 9);
        }

        [Fact]
        public void PushSample_DropsOutOfOrderAndEvicts()
        {
            var ring = new List<Sample>();
            InterpolationCore.PushSample(ring, new Sample { ProducedAtMs = 100 }, 3);
            InterpolationCore.PushSample(ring, new Sample { ProducedAtMs = 200 }, 3);
            InterpolationCore.PushSample(ring, new Sample { ProducedAtMs = 150 }, 3);
            Assert.Equal(2, ring.Count);
            InterpolationCore.PushSample(ring, new Sample { ProducedAtMs = 300 }, 3);
            InterpolationCore.PushSample(ring, new Sample { ProducedAtMs = 400 }, 3);
            Assert.Equal(3, ring.Count);
            Assert.Equal(200, ring[0].ProducedAtMs, Eps);
            Assert.Equal(400, ring[2].ProducedAtMs, Eps);
        }

        [Fact]
        public void IsStaleSample_MatchesPushDrop()
        {
            var ring = new List<Sample> { new Sample { ProducedAtMs = 200 } };
            Assert.True(InterpolationCore.IsStaleSample(ring, 150));
            Assert.False(InterpolationCore.IsStaleSample(ring, 250));
            Assert.False(InterpolationCore.IsStaleSample(new List<Sample>(), 100));
        }

        [Fact]
        public void InterpolateRing_EmptyReturnsFalse()
        {
            Assert.False(InterpolationCore.InterpolateRing(
                new List<Sample>(), 0, 250, 100, out _));
        }

        [Fact]
        public void InterpolateRing_SingleSampleStatic()
        {
            var ring = new List<Sample> { new Sample { WorldX = 7, WorldY = 8, Rotation = 1.2, ProducedAtMs = 500 } };
            Assert.True(InterpolationCore.InterpolateRing(ring, 9999, 250, 100, out var r));
            Assert.Equal(7, r.RenderX, Eps);
            Assert.Equal(8, r.RenderY, Eps);
            Assert.Equal(1.2, r.RenderRot, Eps);
        }

        [Fact]
        public void InterpolateRing_LerpsBetweenPair()
        {
            var ring = new List<Sample>
            {
                new Sample { WorldX = 0,   WorldY = 0,  ProducedAtMs = 1000 },
                new Sample { WorldX = 100, WorldY = 50, ProducedAtMs = 1100 },
            };
            // renderDelayMs must be >= the s0..s1 gap so effS0Stamp stays at
            // s0 (1000) and the midpoint lerp path runs; with renderDelayMs=0
            // the effS0Stamp cap pulls the window start to s1 (1100) and
            // renderTime=1050 snaps back to s0 — faithful to interpolation-core.ts.
            Assert.True(InterpolationCore.InterpolateRing(ring, 1050, 250, 100, out var r));
            Assert.Equal(50, r.RenderX, 6);
            Assert.Equal(25, r.RenderY, 6);
        }

        [Fact]
        public void InterpolateRing_ExtrapolatesCapped()
        {
            var ring = new List<Sample>
            {
                new Sample { WorldX = 0, WorldY = 0, ProducedAtMs = 1000 },
                new Sample { WorldX = 0, WorldY = 0, VelX = 10, VelY = 0, ProducedAtMs = 1100 },
            };
            Assert.True(InterpolationCore.InterpolateRing(ring, 5000, 250, 0, out var r));
            Assert.Equal(2.5, r.RenderX, 6);
        }
    }
}
