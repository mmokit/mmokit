using System;
using System.Collections.Generic;
using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    public class UdpTransportCoreTests
    {
        // Build a core with a captured send-sink and a controllable clock.
        static (UdpTransport t, List<byte[]> sent, long[] clock) NewCore(uint token = 0xCAFEBABE)
        {
            var sent = new List<byte[]>();
            var clock = new long[] { 0 };
            var t = new UdpTransport(raw => sent.Add(raw), () => clock[0], token);
            return (t, sent, clock);
        }

        [Fact]
        public void SendReliable_EmitsReliablePacket_AndIncrementsSeq()
        {
            var (t, sent, _) = NewCore();
            t.SendReliable(new byte[] { 1, 2, 3 });
            t.SendReliable(new byte[] { 4 });
            Assert.Equal(2, sent.Count);
            Assert.True(UdpProto.TryDecodeReliable(sent[0], out _, out ushort s0, out _));
            Assert.True(UdpProto.TryDecodeReliable(sent[1], out _, out ushort s1, out _));
            Assert.Equal(0, s0);
            Assert.Equal(1, s1);
        }

        [Fact]
        public void HandlePacket_Reliable_QueuesPayloadForRecv()
        {
            var (t, _, _) = NewCore();
            byte[] pkt = UdpProto.EncodeReliable(0xCAFEBABE, 0, new byte[] { 9, 9 });
            t.HandlePacket(pkt);
            Assert.True(t.TryRecv(out var got, 0));
            Assert.Equal(new byte[] { 9, 9 }, got);
        }

        [Fact]
        public void HandlePacket_Unreliable_EmptyIsKeepalive_NotQueued()
        {
            var (t, _, _) = NewCore();
            t.HandlePacket(UdpProto.EncodeUnreliable(0xCAFEBABE, null));
            Assert.False(t.TryRecv(out _, 0));
        }

        [Fact]
        public void Tick_RetransmitsUnackedAfterInterval()
        {
            var (t, sent, clock) = NewCore();
            t.SendReliable(new byte[] { 7 });
            sent.Clear();
            clock[0] = 150; // > retransmitInterval (100ms)
            t.Tick();
            Assert.Single(sent); // retransmitted
            Assert.Equal(UdpProto.TypeReliable, sent[0][0]);
        }

        [Fact]
        public void Ack_StopsRetransmit()
        {
            var (t, sent, clock) = NewCore();
            t.SendReliable(new byte[] { 7 }); // seq 0
            sent.Clear();
            // ACK seq 0, no prior bits.
            t.HandlePacket(UdpProto.EncodeAck(0xCAFEBABE, 0, 0));
            clock[0] = 150;
            t.Tick();
            Assert.Empty(sent); // acked → not retransmitted (ackDirty=false here too)
        }

        [Fact]
        public void Tick_SendsAck_WhenInboundReliableReceived()
        {
            var (t, sent, clock) = NewCore();
            t.HandlePacket(UdpProto.EncodeReliable(0xCAFEBABE, 0, new byte[] { 1 }));
            sent.Clear();
            clock[0] = 100;
            t.Tick();
            // First sent packet should include an ACK for the received reliable.
            Assert.Contains(sent, p => p[0] == UdpProto.TypeAck);
        }

        [Fact]
        public void Tick_KeepaliveWhenIdle()
        {
            var (t, sent, clock) = NewCore();
            clock[0] = 1100; // > keepaliveInterval (1000ms), nothing sent yet
            t.Tick();
            Assert.Contains(sent, p => p[0] == UdpProto.TypeUnreliable && p.Length == UdpProto.UnreliableHeaderSize);
        }

        [Fact]
        public void Tick_ConnectionTimeout_ReturnsFalse_AndDisconnects()
        {
            var (t, sent, clock) = NewCore();
            clock[0] = 10_001; // > connectionTimeout (10000ms) since lastRecv=0
            Assert.False(t.Tick());
            Assert.Contains(sent, p => p[0] == UdpProto.TypeDisconnect); // Close() sent disconnect
        }

        [Fact]
        public void Tick_ReliableTimeout_ReturnsFalse()
        {
            var (t, _, clock) = NewCore();
            t.SendReliable(new byte[] { 7 }); // sentAt = 0, never acked
            clock[0] = 5001; // > reliableTimeout (5000ms), but < connectionTimeout
            Assert.False(t.Tick());
        }
    }
}
