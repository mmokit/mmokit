using System;
using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    public class DeltaDecoderGoldenTests
    {
        readonly Manifest g = Golden.Load();

        [Fact]
        public void Dequantizers_MatchGo()
        {
            foreach (var c in g.Dequant)
            {
                double got = c.Kind switch
                {
                    "unAngle" => DeltaDecoderCore.UnAngle((int)c.Q),
                    "unNorm" => DeltaDecoderCore.UnNorm((int)c.Q),
                    "unVel" => DeltaDecoderCore.UnVel((int)c.Q, c.Scale),
                    "unRel" => DeltaDecoderCore.UnRel((int)c.Q, c.Scale),
                    _ => throw new Xunit.Sdk.XunitException($"unknown kind {c.Kind}"),
                };
                Assert.Equal(c.Expected, got, 4);
            }
        }

        [Fact]
        public void BigEndianReads_RoundTripGoldenFrameHeader()
        {
            byte[] frame = Golden.Hex(g.Frame.HexBytes);
            var (h, off) = DeltaDecoderCore.DecodeFrameHeader(frame, 0);
            Assert.Equal(DeltaDecoderCore.FrameHeaderSize, off);
            Assert.Equal(g.Frame.Tick, h.Tick);
            Assert.Equal(g.Frame.Seq, h.Seq);
            Assert.Equal(g.Frame.Flags, h.Flags);
            Assert.Equal(g.Frame.FullCount, h.FullCount);
            Assert.Equal(g.Frame.DeltaCount, h.DeltaCount);
            Assert.Equal(g.Frame.RemovedCount, h.RemovedCount);
            Assert.Equal(g.Frame.ExitedCount, h.ExitedCount);
        }

        [Fact]
        public void DecodeFullAndDeltaAndRemoved_MatchGo()
        {
            byte[] frame = Golden.Hex(g.Frame.HexBytes);
            var (_, pos) = DeltaDecoderCore.DecodeFrameHeader(frame, 0);

            var (full, p2) = DeltaDecoderCore.DecodeFullEntry(frame, pos);
            var ef = g.Frame.Full[0];
            Assert.Equal(ef.NetID, full.NetID);
            Assert.Equal(ef.Epoch, full.Epoch);
            Assert.Equal(ef.EntityType, full.EntityType);
            Assert.Equal(ef.ProducedAtMs, full.ProducedAtMs);
            Assert.Equal(Golden.Hex(ef.SnapshotHex), full.Snapshot);
            Assert.Equal(Golden.Hex(ef.InitialHex), full.InitialData ?? Array.Empty<byte>());

            var (dlt, p3) = DeltaDecoderCore.DecodeDeltaEntry(frame, p2);
            var ed = g.Frame.Delta[0];
            Assert.Equal(ed.NetID, dlt.NetID);
            Assert.Equal(ed.ProducedAtMs, dlt.ProducedAtMs);
            Assert.Equal(Golden.Hex(ed.DeltaHex), dlt.DeltaData);

            var (ids, _) = DeltaDecoderCore.DecodeRemovedIDs(frame, p3, g.Frame.RemovedCount);
            Assert.Equal(g.Frame.RemovedIDs, ids);
        }

        [Fact]
        public void ApplyDelta_MatchesGo()
        {
            foreach (var c in g.ApplyDelta)
            {
                byte[] got = DeltaDecoderCore.ApplyDelta(c.FieldSizes, c.HasVarTail,
                    Golden.Hex(c.BaselineHex), Golden.Hex(c.DeltaHex));
                Assert.Equal(Golden.Hex(c.ExpectedHex), got);
            }
        }

        [Fact]
        public void Strings_MatchGo()
        {
            foreach (var c in g.Strings)
            {
                byte[] data = Golden.Hex(c.HexBytes);
                string got = c.Kind == "u8"
                    ? DeltaDecoderCore.DecodeLengthPrefixedStringU8(data)
                    : DeltaDecoderCore.DecodeLengthPrefixedStringU16(data);
                Assert.Equal(c.Expected, got);
            }
        }
    }
}
