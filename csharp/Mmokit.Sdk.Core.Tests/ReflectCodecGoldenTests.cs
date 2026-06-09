using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    public class ReflectCodecGoldenTests
    {
        readonly Manifest g = Golden.Load();

        // ReflectReader must decode Go's actual ReflectMarshal bytes to the
        // expected field values (in source order: f32, u32, string, bool, i64, []u32).
        [Fact]
        public void Reader_DecodesGoBytes()
        {
            var c = g.Reflect;
            var r = new ReflectReader(Golden.Hex(c.HexBytes));
            Assert.Equal(c.A, r.ReadF32(), 4);
            Assert.Equal(c.B, r.ReadU32());
            Assert.Equal(c.C, r.ReadString());
            Assert.Equal(c.D, r.ReadBool());
            Assert.Equal(c.E, r.ReadI64());
            int n = r.ReadSliceLen();
            Assert.Equal(c.F.Length, n);
            for (int i = 0; i < n; i++) Assert.Equal(c.F[i], r.ReadU32());
        }

        // ReflectWriter must reproduce Go's exact bytes for the same values.
        [Fact]
        public void Writer_ReproducesGoBytes()
        {
            var c = g.Reflect;
            var w = new ReflectWriter();
            w.WriteF32(c.A);
            w.WriteU32(c.B);
            w.WriteString(c.C);
            w.WriteBool(c.D);
            w.WriteI64(c.E);
            w.WriteSliceLen(c.F.Length);
            foreach (uint v in c.F) w.WriteU32(v);
            Assert.Equal(Golden.Hex(c.HexBytes), w.ToArray());
        }
    }
}
