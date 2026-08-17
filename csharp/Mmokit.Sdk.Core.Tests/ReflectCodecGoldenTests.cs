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

        // Nested struct + slice-of-struct + a trailing scalar.
        //
        // The flat case above was the entire reflect coverage until §6.10 unit
        // 2c, while the real 4node schema already carries chat types shaped
        // like this, and §7 phase 1 turns position and velocity into nested
        // value types. The trailing Tick is the load-bearing part of the
        // fixture: a nested walker that consumes the wrong number of bytes
        // leaves the cursor misaligned, and only a field AFTER the nesting
        // notices.
        //
        // Nested structs are inlined by the codec — there is no envelope, so
        // the reader simply reads the inner fields in order.
        [Fact]
        public void Reader_DecodesNestedGoBytes()
        {
            var c = g.ReflectNested;
            var r = new ReflectReader(Golden.Hex(c.HexBytes));

            Assert.Equal(c.Channel.Slug, r.ReadString());
            Assert.Equal(c.Channel.MemberCount, r.ReadI32());

            int n = r.ReadSliceLen();
            Assert.Equal(c.Members.Length, n);
            for (int i = 0; i < n; i++)
            {
                Assert.Equal(c.Members[i].UserID, r.ReadString());
                Assert.Equal(c.Members[i].Role, r.ReadString());
            }

            Assert.Equal(c.Tick, r.ReadU32());
        }

        [Fact]
        public void Writer_ReproducesNestedGoBytes()
        {
            var c = g.ReflectNested;
            var w = new ReflectWriter();

            w.WriteString(c.Channel.Slug);
            w.WriteI32(c.Channel.MemberCount);

            w.WriteSliceLen(c.Members.Length);
            foreach (var m in c.Members)
            {
                w.WriteString(m.UserID);
                w.WriteString(m.Role);
            }

            w.WriteU32(c.Tick);
            Assert.Equal(Golden.Hex(c.HexBytes), w.ToArray());
        }
    }
}
