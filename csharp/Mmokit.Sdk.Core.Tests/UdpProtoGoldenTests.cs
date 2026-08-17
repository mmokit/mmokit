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

        // The golden counter and cookie mirror cmd/csharp-golden/main.go. Only the
        // CLEARTEXT header is pinned here: sealing needs a key and a counter, and
        // UdpSession refuses to let a caller fix a counter because that is what
        // makes nonce reuse unrepresentable. The AEAD itself is pinned by
        // UdpCryptoGoldenTests and ChaCha20Poly1305RfcTests.
        const ulong GoldenCounter = 0x0102030405060708UL;

        static byte[] GoldenCookie()
        {
            var c = new byte[UdpProto.CookieSize];
            for (int i = 0; i < c.Length; i++) c[i] = (byte)(0xA0 + i);
            return c;
        }

        [Fact]
        public void Encode_MatchesGoBytes()
        {
            foreach (var p in g.Udp.Packets)
            {
                byte[] got = p.Kind switch
                {
                    "connReq" => UdpProto.EncodeConnReq(p.ClientSalt),
                    "connAccept" => UdpProto.EncodeConnAccept(p.ClientSalt, p.ServerSalt, GoldenCookie()),
                    "connConfirm" => UdpProto.EncodeConnConfirm(0x0BADC0DE, p.ClientSalt, p.ServerSalt, GoldenCookie()),
                    "unreliableHeader" => UdpProto.EncodeUnreliableHeader(p.Token, GoldenCounter),
                    "reliableHeader" => UdpProto.EncodeReliableHeader(p.Token, p.Seq, GoldenCounter),
                    "ackHeader" => UdpProto.EncodeAckHeader(p.Token, GoldenCounter),
                    "ackBody" => UdpProto.EncodeAckBody(p.AckSeq, p.AckBits),
                    "disconnectHeader" => UdpProto.EncodeDisconnectHeader(p.Token, GoldenCounter),
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
                        Assert.True(UdpProto.TryDecodeConnAccept(data, out ulong cs2, out ulong ss2, out byte[] ck2));
                        Assert.Equal(p.ClientSalt, cs2);
                        Assert.Equal(p.ServerSalt, ss2);
                        Assert.Equal(GoldenCookie(), ck2);
                        break;
                    case "connConfirm":
                        Assert.True(UdpProto.TryDecodeConnConfirm(data, out ulong kid, out ulong cs3, out ulong ss3, out byte[] ck3));
                        Assert.Equal(0x0BADC0DEUL, kid);
                        Assert.Equal(p.ClientSalt, cs3);
                        Assert.Equal(p.ServerSalt, ss3);
                        Assert.Equal(GoldenCookie(), ck3);
                        break;
                    case "ackBody":
                        Assert.True(UdpProto.TryDecodeAckBody(data, out ushort aseq, out uint abits));
                        Assert.Equal(p.AckSeq, aseq);
                        Assert.Equal(p.AckBits, abits);
                        break;
                    // Header-only cases carry no sealed body, so the full-packet
                    // decoders (which require one) are exercised by the round-trip
                    // tests rather than here.
                }
            }
        }

        // Every packet must carry the version byte, or a peer that disagrees
        // about the world's shape decodes valid bytes into the wrong types
        // instead of being rejected (CE-009).
        [Fact]
        public void EveryPacketCarriesTheVersionByte()
        {
            foreach (var p in g.Udp.Packets)
            {
                if (p.Kind == "ackBody") continue; // a body, not a packet
                byte[] data = Golden.Hex(p.HexBytes);
                Assert.True(data.Length >= 2, $"{p.Kind} is too short to hold a version byte");
                Assert.Equal(UdpProto.Version, data[1]);
            }
        }

        // A packet from a different protocol version must be refused rather than
        // misparsed.
        [Fact]
        public void WrongVersionIsRejected()
        {
            byte[] req = UdpProto.EncodeConnReq(0x1122334455667788);
            req[1] = 0xFE;
            Assert.False(UdpProto.TryDecodeConnReq(req, out _));
        }

        [Fact]
        public void SeqGreaterThan_MatchesGo()
        {
            foreach (var s in g.Udp.SeqCmp)
                Assert.Equal(s.Greater, UdpProto.SeqGreaterThan(s.S1, s.S2));
        }
    }
}
