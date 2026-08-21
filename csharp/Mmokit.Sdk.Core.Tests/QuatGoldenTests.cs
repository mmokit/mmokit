using System;
using System.Collections.Generic;
using Mmokit.Sdk.Core;
using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    /// <summary>
    /// The third implementation of smallest-three decode and of slerp, checked
    /// against the same corpus Go and TypeScript use. Three independent ports
    /// is three chances to disagree; this is what makes a disagreement
    /// impossible to ship rather than merely unlikely.
    /// </summary>
    public class QuatGoldenTests
    {
        private static uint BitsOf(float v) => BitConverter.SingleToUInt32Bits(v);

        /// <summary>
        /// Compared on EXACT float32 bit identity, not a tolerance. A tolerance
        /// would hide precisely the rounding disagreement this corpus exists
        /// to catch.
        /// </summary>
        [Fact]
        public void UnQuat_ReproducesEveryGoldenVectorBitExactly()
        {
            var m = Golden.Load();
            Assert.True(m.Quat.Length > 100, "manifest is missing the quat corpus");

            foreach (var c in m.Quat)
            {
                byte[] bytes = Golden.Hex(c.Hex);
                Assert.Equal(DeltaDecoderCore.QuatWireSize, bytes.Length);

                Quat q = DeltaDecoderCore.UnQuat(bytes, 0);
                var got = new[] { BitsOf(q.X), BitsOf(q.Y), BitsOf(q.Z), BitsOf(q.W) };
                Assert.True(
                    got[0] == c.Bits[0] && got[1] == c.Bits[1] && got[2] == c.Bits[2] && got[3] == c.Bits[3],
                    $"{c.Name}: got [{string.Join(",", got)}], want [{string.Join(",", c.Bits)}]");
            }
        }

        [Fact]
        public void UnQuat_DecodesAtANonZeroOffset()
        {
            var m = Golden.Load();
            var c = m.Quat[0];
            byte[] src = Golden.Hex(c.Hex);
            var padded = new byte[src.Length + 5];
            Array.Copy(src, 0, padded, 3, src.Length);

            Quat q = DeltaDecoderCore.UnQuat(padded, 3);
            Assert.Equal(c.Bits[0], BitsOf(q.X));
            Assert.Equal(c.Bits[3], BitsOf(q.W));
        }

        [Fact]
        public void SlerpQuat_ReproducesEveryGoldenCase()
        {
            var m = Golden.Load();
            Assert.True(m.Slerp.Length > 50, "manifest is missing the slerp corpus");

            foreach (var c in m.Slerp)
            {
                var a = new Quat((float)c.A[0], (float)c.A[1], (float)c.A[2], (float)c.A[3]);
                var b = new Quat((float)c.B[0], (float)c.B[1], (float)c.B[2], (float)c.B[3]);
                Quat got = InterpolationCore.SlerpQuat(a, b, c.T);

                var outs = new[] { (double)got.X, got.Y, got.Z, got.W };
                for (int i = 0; i < 4; i++)
                {
                    Assert.True(Math.Abs(outs[i] - c.Out[i]) < 1e-6,
                        $"{c.Name}: component {i} = {outs[i]}, want {c.Out[i]}");
                }
            }
        }

        /// <summary>
        /// Shared with the Go reference rather than restated, because it is the
        /// most port-divergent line in the orientation path.
        /// </summary>
        [Fact]
        public void SlerpDotThreshold_MatchesTheGoReference()
        {
            Assert.Equal(0.9995, InterpolationCore.SlerpDotThreshold);
        }

        /// <summary>
        /// A 2D sample must produce a result carrying no 3D data at all —
        /// null, not 0, so a renderer cannot mistake "no data" for "at the
        /// origin".
        /// </summary>
        [Fact]
        public void InterpolateRing_OmitsThreeDFieldsForTwoDSamples()
        {
            var ring = new List<Sample>
            {
                new Sample { WorldX = 0, WorldY = 0, Rotation = 0, ProducedAtMs = 0 },
                new Sample { WorldX = 10, WorldY = 0, Rotation = 1, ProducedAtMs = 100 },
            };

            Assert.True(InterpolationCore.InterpolateRing(ring, 50, 0, 100, out var r));
            Assert.Null(r.RenderZ);
            Assert.Null(r.RenderQuat);
        }

        /// <summary>
        /// And a 3D sample must slerp orientation and lerp height.
        /// </summary>
        [Fact]
        public void InterpolateRing_SlerpsOrientationAndLerpsHeight()
        {
            var a = Quat.Identity;
            var b = new Quat(0f, 0f, (float)(Math.Sqrt(2) / 2), (float)(Math.Sqrt(2) / 2));

            var ring = new List<Sample>
            {
                new Sample { WorldX = 0, WorldY = 0, Rotation = 0, ProducedAtMs = 0, WorldZ = 10, VelZ = 0, Quat = a },
                new Sample { WorldX = 100, WorldY = 0, Rotation = 0, ProducedAtMs = 100, WorldZ = 20, VelZ = 0, Quat = b },
            };

            // renderDelayMs must be at least the sample gap or the ring holds.
            Assert.True(InterpolationCore.InterpolateRing(ring, 50, 0, 100, out var r));
            Assert.Equal(InterpolationMode.Interpolate, r.Mode);
            Assert.NotNull(r.RenderZ);
            Assert.True(Math.Abs(r.RenderZ!.Value - 15) < 1e-6);

            Quat want = InterpolationCore.SlerpQuat(a, b, 0.5);
            Assert.NotNull(r.RenderQuat);
            Assert.True(Math.Abs(r.RenderQuat!.Value.Z - want.Z) < 1e-6);
            Assert.True(Math.Abs(r.RenderQuat!.Value.W - want.W) < 1e-6);
        }
    }
}
