using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    public class UdpProtoGoldenTests
    {
        readonly Manifest g = Golden.Load();

        [Fact]
        public void MakeToken_MatchesGo()
        {
            foreach (var t in g.Udp.Tokens)
                Assert.Equal(t.Token, UdpProto.MakeToken(t.ClientSalt, t.ServerSalt));
        }

        [Fact]
        public void Encode_MatchesGoBytes()
        {
            foreach (var p in g.Udp.Packets)
            {
                byte[] got = p.Kind switch
                {
                    "connReq" => UdpProto.EncodeConnReq(p.ClientSalt),
                    "connAccept" => UdpProto.EncodeConnAccept(p.ClientSalt, p.ServerSalt),
                    "unreliable" => UdpProto.EncodeUnreliable(p.Token, Golden.Hex(p.PayloadHex)),
                    "reliable" => UdpProto.EncodeReliable(p.Token, p.Seq, Golden.Hex(p.PayloadHex)),
                    "ack" => UdpProto.EncodeAck(p.Token, p.AckSeq, p.AckBits),
                    "disconnect" => UdpProto.EncodeDisconnect(p.Token),
                    _ => throw new Xunit.Sdk.XunitException($"unknown kind {p.Kind}"),
                };
                Assert.Equal(Golden.Hex(p.HexBytes), got);
            }
        }

        [Fact]
        public void Decode_MatchesGoFields()
        {
            foreach (var p in g.Udp.Packets)
            {
                byte[] data = Golden.Hex(p.HexBytes);
                switch (p.Kind)
                {
                    case "connReq":
                        Assert.True(UdpProto.TryDecodeConnReq(data, out ulong cs));
                        Assert.Equal(p.ClientSalt, cs);
                        break;
                    case "connAccept":
                        Assert.True(UdpProto.TryDecodeConnAccept(data, out ulong cs2, out ulong ss2));
                        Assert.Equal(p.ClientSalt, cs2);
                        Assert.Equal(p.ServerSalt, ss2);
                        break;
                    case "unreliable":
                        Assert.True(UdpProto.TryDecodeUnreliable(data, out uint tok, out byte[] pay));
                        Assert.Equal(p.Token, tok);
                        Assert.Equal(Golden.Hex(p.PayloadHex), pay);
                        break;
                    case "reliable":
                        Assert.True(UdpProto.TryDecodeReliable(data, out uint tok2, out ushort seq, out byte[] pay2));
                        Assert.Equal(p.Token, tok2);
                        Assert.Equal(p.Seq, seq);
                        Assert.Equal(Golden.Hex(p.PayloadHex), pay2);
                        break;
                    case "ack":
                        Assert.True(UdpProto.TryDecodeAck(data, out uint tok3, out ushort aseq, out uint abits));
                        Assert.Equal(p.Token, tok3);
                        Assert.Equal(p.AckSeq, aseq);
                        Assert.Equal(p.AckBits, abits);
                        break;
                    case "disconnect":
                        Assert.True(UdpProto.TryDecodeDisconnect(data, out uint tok4));
                        Assert.Equal(p.Token, tok4);
                        break;
                }
            }
        }

        [Fact]
        public void SeqGreaterThan_MatchesGo()
        {
            foreach (var s in g.Udp.SeqCmp)
                Assert.Equal(s.Greater, UdpProto.SeqGreaterThan(s.S1, s.S2));
        }
    }
}
