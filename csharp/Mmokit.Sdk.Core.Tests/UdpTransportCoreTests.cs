using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.IO;
using System.Threading;
using System.Threading.Tasks;
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
        public void SendReliable_DoesNotOverwriteAnUnackedFullWindow()
        {
            var (t, sent, _) = NewCore();
            for (int i = 0; i < 256; i++) t.SendReliable(new byte[] { (byte)i });

            Assert.Throws<InvalidOperationException>(() => t.SendReliable(new byte[] { 99 }));
            Assert.Equal(256, sent.Count);

            // Releasing seq=0 opens its aliased ring slot. The failed send did
            // not consume a sequence, so the next packet must be seq=256.
            t.HandlePacket(UdpProto.EncodeAck(0xCAFEBABE, 0, 0));
            t.SendReliable(new byte[] { 42 });
            Assert.True(UdpProto.TryDecodeReliable(sent[256], out _, out ushort seq, out _));
            Assert.Equal(256, seq);
        }

        [Fact]
        public void SendReliable_FailedInitialWriteDoesNotLeaveARetry()
        {
            var clock = new long[] { 0 };
            bool fail = true;
            var t = new UdpTransport(_ =>
            {
                if (fail)
                {
                    fail = false;
                    throw new InvalidOperationException("send failed");
                }
            }, () => clock[0], 7);
            Assert.Throws<InvalidOperationException>(() => t.SendReliable(new byte[] { 1 }));

            // Swap-free core cannot replace its sink, but a later maintenance
            // tick must not encounter a retained reliable entry and close on
            // its lifetime. It should survive until the connection timeout.
            clock[0] = 5_001;
            Assert.True(t.Tick());
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
        public void HandlePacket_Reliable_DeduplicatesAndDeliversUnseenOutOfOrder()
        {
            var (t, _, _) = NewCore();
            t.HandlePacket(UdpProto.EncodeReliable(0xCAFEBABE, 10, new byte[] { 10 }));
            t.HandlePacket(UdpProto.EncodeReliable(0xCAFEBABE, 12, new byte[] { 12 }));
            t.HandlePacket(UdpProto.EncodeReliable(0xCAFEBABE, 10, new byte[] { 10 }));
            t.HandlePacket(UdpProto.EncodeReliable(0xCAFEBABE, 11, new byte[] { 11 }));
            t.HandlePacket(UdpProto.EncodeReliable(0xCAFEBABE, 11, new byte[] { 11 }));

            Assert.True(t.TryRecv(out var first, 0));
            Assert.True(t.TryRecv(out var second, 0));
            Assert.True(t.TryRecv(out var third, 0));
            Assert.Equal(new byte[] { 10 }, first);
            Assert.Equal(new byte[] { 12 }, second);
            Assert.Equal(new byte[] { 11 }, third);
            Assert.False(t.TryRecv(out _, 0));
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
        public void Ack_RequiresExactWrappedSequenceIdentity()
        {
            var (t, sent, clock) = NewCore();
            // Advance to seq=256 without retaining the earlier ring entries.
            for (ushort seq = 0; seq < 256; seq++)
            {
                t.SendReliable(new byte[] { 1 });
                t.HandlePacket(UdpProto.EncodeAck(0xCAFEBABE, seq, 0));
            }
            sent.Clear();
            t.SendReliable(new byte[] { 42 }); // seq=256 aliases slot zero
            sent.Clear();

            t.HandlePacket(UdpProto.EncodeAck(0xCAFEBABE, 0, 0));
            clock[0] = 150;
            Assert.True(t.Tick());
            Assert.Contains(sent, packet =>
                UdpProto.TryDecodeReliable(packet, out _, out ushort seq, out _) && seq == 256);

            sent.Clear();
            t.HandlePacket(UdpProto.EncodeAck(0xCAFEBABE, 256, 0));
            clock[0] = 300;
            Assert.True(t.Tick());
            Assert.DoesNotContain(sent, packet => packet[0] == UdpProto.TypeReliable);
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

        [Fact]
        public void Tick_ReliableLifetimeIsNotResetByRetransmission()
        {
            var (t, _, clock) = NewCore();
            t.SendReliable(new byte[] { 7 });

            clock[0] = 100;
            Assert.True(t.Tick());
            clock[0] = 2_000;
            Assert.True(t.Tick());
            clock[0] = 4_900;
            Assert.True(t.Tick());
            clock[0] = 5_001;
            Assert.False(t.Tick());
        }

        [Fact]
        public async Task SendReliable_CloseWaitsForInitialWrite_AndDisconnectIsLast()
        {
            var reliableEntered = new ManualResetEventSlim();
            var releaseReliable = new ManualResetEventSlim();
            var closeStarted = new ManualResetEventSlim();
            var disconnectSent = new ManualResetEventSlim();
            var sentTypes = new ConcurrentQueue<byte>();
            var t = new UdpTransport(raw =>
            {
                if (raw[0] == UdpProto.TypeReliable)
                {
                    reliableEntered.Set();
                    if (!releaseReliable.Wait(TimeSpan.FromSeconds(5)))
                        throw new TimeoutException("test did not release reliable write");
                }
                sentTypes.Enqueue(raw[0]);
                if (raw[0] == UdpProto.TypeDisconnect) disconnectSent.Set();
            }, () => 0, 0xCAFEBABE);

            Task send = Task.Run(() => t.SendReliable(new byte[] { 1 }));
            Assert.True(reliableEntered.Wait(TimeSpan.FromSeconds(5)));
            Task close = Task.Run(() =>
            {
                closeStarted.Set();
                t.Close();
            });
            Assert.True(closeStarted.Wait(TimeSpan.FromSeconds(5)));

            try
            {
                // Close has started, but must not pass the in-flight initial
                // write and emit Disconnect ahead of it.
                Assert.False(disconnectSent.Wait(TimeSpan.FromMilliseconds(100)));
                Assert.False(close.IsCompleted);
            }
            finally
            {
                releaseReliable.Set();
            }

            await Task.WhenAll(send, close);
            Assert.Equal(
                new byte[] { UdpProto.TypeReliable, UdpProto.TypeDisconnect },
                sentTypes.ToArray());
        }

        [Fact]
        public async Task Close_SerializesWithTick_AndNoPacketFollowsDisconnect()
        {
            var disconnectEntered = new ManualResetEventSlim();
            var releaseDisconnect = new ManualResetEventSlim();
            var tickStarted = new ManualResetEventSlim();
            var sentTypes = new ConcurrentQueue<byte>();
            long clock = 0;
            var t = new UdpTransport(raw =>
            {
                sentTypes.Enqueue(raw[0]);
                if (raw[0] == UdpProto.TypeDisconnect)
                {
                    disconnectEntered.Set();
                    if (!releaseDisconnect.Wait(TimeSpan.FromSeconds(5)))
                        throw new TimeoutException("test did not release disconnect write");
                }
            }, () => Interlocked.Read(ref clock), 0xCAFEBABE);

            Interlocked.Exchange(ref clock, 2_000); // Tick would send a keepalive.
            Task close = Task.Run(t.Close);
            Assert.True(disconnectEntered.Wait(TimeSpan.FromSeconds(5)));
            Task<bool> tick = Task.Run(() =>
            {
                tickStarted.Set();
                return t.Tick();
            });
            Assert.True(tickStarted.Wait(TimeSpan.FromSeconds(5)));

            try
            {
                Task early = await Task.WhenAny(tick, Task.Delay(TimeSpan.FromMilliseconds(100)));
                Assert.NotSame(tick, early);
                Assert.Equal(new byte[] { UdpProto.TypeDisconnect }, sentTypes.ToArray());
            }
            finally
            {
                releaseDisconnect.Set();
            }

            await close;
            Assert.False(await tick);
            Assert.Equal(new byte[] { UdpProto.TypeDisconnect }, sentTypes.ToArray());
        }

        [Fact]
        public async Task HandlePacket_RacingClose_DropsInsteadOfThrowing()
        {
            var receiveClockEntered = new ManualResetEventSlim();
            var releaseReceiveClock = new ManualResetEventSlim();
            int receiveThreadId = 0;
            var t = new UdpTransport(_ => { }, () =>
            {
                if (Environment.CurrentManagedThreadId == Volatile.Read(ref receiveThreadId))
                {
                    receiveClockEntered.Set();
                    if (!releaseReceiveClock.Wait(TimeSpan.FromSeconds(5)))
                        throw new TimeoutException("test did not release receive clock");
                    return 1;
                }
                return 0;
            }, 0xCAFEBABE);

            Exception? receiveError = null;
            Task receive = Task.Run(() =>
            {
                Volatile.Write(ref receiveThreadId, Environment.CurrentManagedThreadId);
                try
                {
                    t.HandlePacket(UdpProto.EncodeUnreliable(
                        0xCAFEBABE,
                        new byte[] { 9 }));
                }
                catch (Exception ex)
                {
                    receiveError = ex;
                }
            });
            Assert.True(receiveClockEntered.Wait(TimeSpan.FromSeconds(5)));

            t.Close();
            releaseReceiveClock.Set();
            await receive;

            Assert.Null(receiveError);
            Assert.False(t.TryRecv(out _, 0));
        }

        [Fact]
        public async Task Close_WakesBlockedRecv()
        {
            var (t, _, _) = NewCore();
            var recvStarted = new ManualResetEventSlim();
            Task<byte[]?> receive = Task.Run(() =>
            {
                recvStarted.Set();
                return t.Recv();
            });
            Assert.True(recvStarted.Wait(TimeSpan.FromSeconds(5)));
            Task early = await Task.WhenAny(receive, Task.Delay(TimeSpan.FromMilliseconds(100)));
            Assert.NotSame(receive, early);

            t.Close();

            Task completed = await Task.WhenAny(receive, Task.Delay(TimeSpan.FromSeconds(5)));
            Assert.Same(receive, completed);
            Assert.Null(await receive);
        }

        [Fact]
        public async Task ActivityClock_CannotRegressAcrossConcurrentReceives()
        {
            var olderClockEntered = new ManualResetEventSlim();
            var releaseOlderClock = new ManualResetEventSlim();
            int olderThreadId = 0;
            long currentClock = 0;
            var t = new UdpTransport(_ => { }, () =>
            {
                if (Environment.CurrentManagedThreadId == Volatile.Read(ref olderThreadId))
                {
                    olderClockEntered.Set();
                    if (!releaseOlderClock.Wait(TimeSpan.FromSeconds(5)))
                        throw new TimeoutException("test did not release older clock read");
                    return 100;
                }
                return Interlocked.Read(ref currentClock);
            }, 0xCAFEBABE);

            Task olderReceive = Task.Run(() =>
            {
                Volatile.Write(ref olderThreadId, Environment.CurrentManagedThreadId);
                t.HandlePacket(UdpProto.EncodeUnreliable(0xCAFEBABE, Array.Empty<byte>()));
            });
            Assert.True(olderClockEntered.Wait(TimeSpan.FromSeconds(5)));

            Interlocked.Exchange(ref currentClock, 200);
            t.HandlePacket(UdpProto.EncodeUnreliable(0xCAFEBABE, Array.Empty<byte>()));
            releaseOlderClock.Set();
            await olderReceive;

            // 10,101 - 200 is still live; 10,101 - a regressed 100 times out.
            Interlocked.Exchange(ref currentClock, 10_101);
            Assert.True(t.Tick());
        }

        [Fact]
        public void Tick_BackgroundWriteFailureClosesWithoutThrowing()
        {
            long clock = 0;
            bool rejectWrites = false;
            var t = new UdpTransport(_ =>
            {
                if (Volatile.Read(ref rejectWrites))
                    throw new IOException("injected socket failure");
            }, () => Interlocked.Read(ref clock), 0xCAFEBABE);

            Volatile.Write(ref rejectWrites, true);
            Interlocked.Exchange(ref clock, 1_001); // force keepalive write

            bool result = true;
            Exception? error = Record.Exception(() => result = t.Tick());
            Assert.Null(error);
            Assert.False(result);
            Assert.Throws<ObjectDisposedException>(() => t.SendUnreliable(new byte[] { 1 }));
            Assert.Null(t.Recv());
        }
    }
}
