using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.Diagnostics.CodeAnalysis;
using System.Threading;

namespace Mmokit.Sdk.Core
{
    /// UDP transport: handshake + reliable/unreliable channels + ACK-based
    /// reliability, port of pkg/net/udpclient/client.go.
    ///
    /// This file has two faces:
    ///  - The protocol CORE (this ctor + SendReliable/SendUnreliable/
    ///    HandlePacket/Tick/TryRecv/Recv/Close): a pure state machine driven
    ///    by an injected sendRaw sink + nowMs clock. No sockets, no threads —
    ///    unit-testable directly.
    ///  - The real-socket Connect factory + receive/tick loops (added in the
    ///    socket section): wires sendRaw to a UdpClient and pumps HandlePacket.
    ///
    /// ENCRYPTION SEAM (spec §B): the only two byte chokepoints are the
    /// `_sendRaw` delegate (outbound) and the `HandlePacket(byte[])` entry
    /// (inbound). A future encryption layer wraps ONLY these two points; all
    /// logic here operates on decrypted bytes. Keep it that way.
    public sealed partial class UdpTransport
    {
        const int ReliableBufSize = 256;
        const long RetransmitIntervalMs = 100;
        const long ReliableTimeoutMs = 5000;
        const long KeepaliveIntervalMs = 1000;
        const long ConnectionTimeoutMs = 10000;

        struct ReliableEntry
        {
            public ushort Seq;
            public byte[] Payload;
            public long FirstSentAtMs;
            public long LastSentAtMs;
            public bool Used;
        }

        readonly Action<byte[]> _sendRaw;
        readonly Func<long> _nowMs;
        readonly uint _token;

        /// Owns this session's traffic keys, send counter and replay window.
        /// Every outbound body is sealed through it and every inbound one opened
        /// through it, so the token on the wire is only a session index.
        readonly UdpSession _session;

        readonly object _sendLock = new();
        ushort _sendSeq;
        readonly ReliableEntry[] _sendBuf = new ReliableEntry[ReliableBufSize];

        // Recv-tracking trio is written on the receive thread (HandlePacket) and
        // read on the tick thread (Tick) under the socket loops — guard it. (Go's
        // memory model tolerates the unsynchronized access; C#'s permits
        // read-hoisting that could stall ACK flushing, so the lock is required.)
        readonly object _recvLock = new();
        bool _recvInit;
        ushort _recvSeq;
        uint _recvBits;
        bool _ackDirty;

        readonly BlockingCollection<byte[]> _inbound = new(new ConcurrentQueue<byte[]>());
        readonly object _inboundLock = new();

        long _lastRecvMs;
        long _lastSendMs;
        readonly object _closeLock = new();
        bool _closed;

        /// Core ctor (and the testing entry point). The socket factory below
        /// constructs this with a sink that writes to the UdpClient.
        public UdpTransport(Action<byte[]> sendRaw, Func<long> nowMs, uint token, UdpSession session)
        {
            _sendRaw = sendRaw;
            _nowMs = nowMs;
            _token = token;
            _session = session ?? throw new ArgumentNullException(nameof(session));
            long now = nowMs();
            _lastRecvMs = now;
            _lastSendMs = now;
        }

        public uint Token => _token;

        public void SendReliable(byte[] data)
        {
            ushort seq;
            byte[] payload = (byte[])data.Clone();
            lock (_sendLock)
            {
                ThrowIfClosedLocked();
                seq = _sendSeq;
                int idx = seq % ReliableBufSize;
                if (_sendBuf[idx].Used)
                    throw new InvalidOperationException("UDP reliable send window is full");
                _sendSeq++;
                long now = _nowMs();
                _sendBuf[idx] = new ReliableEntry
                {
                    Seq = seq,
                    Payload = payload,
                    FirstSentAtMs = now,
                    LastSentAtMs = now,
                    Used = true,
                };
                try
                {
                    // Keep admission and the initial write atomic with Close.
                    // Otherwise Close can clear the slot and emit Disconnect
                    // before this reliable datagram reaches the socket.
                    _sendRaw(_session.SealPacket(payload,
                        ctr => UdpProto.EncodeReliableHeader(_token, seq, ctr)));
                    AdvanceClock(ref _lastSendMs, _nowMs());
                }
                catch
                {
                    if (_sendBuf[idx].Used && _sendBuf[idx].Seq == seq)
                        _sendBuf[idx] = default;
                    throw;
                }
            }
        }

        public void SendUnreliable(byte[] data)
        {
            lock (_sendLock)
            {
                ThrowIfClosedLocked();
                _sendRaw(_session.SealPacket(data,
                    ctr => UdpProto.EncodeUnreliableHeader(_token, ctr)));
                AdvanceClock(ref _lastSendMs, _nowMs());
            }
        }

        /// Inbound chokepoint. `data` is a full, decrypted packet.
        public void HandlePacket(byte[] data)
        {
            if (data.Length == 0) return;
            switch (data[0])
            {
                case UdpProto.TypeUnreliable:
                {
                    if (!UdpProto.TryDecodeUnreliable(data, out _, out ulong ctr, out byte[] aad, out byte[] body)) return;
                    var upay = _session.OpenPacket(ctr, body, aad);
                    // Deliberately before the liveness stamp: a packet that does
                    // not authenticate must not keep a dead session alive.
                    if (upay == null) return;
                    AdvanceClock(ref _lastRecvMs, _nowMs());
                    if (upay.Length == 0) return; // keepalive
                    TryQueueInbound(upay);
                    break;
                }
                case UdpProto.TypeReliable:
                {
                    if (!UdpProto.TryDecodeReliable(data, out _, out ushort seq, out ulong ctr, out byte[] aad, out byte[] body)) return;
                    var rpay = _session.OpenPacket(ctr, body, aad);
                    if (rpay == null) return;
                    AdvanceClock(ref _lastRecvMs, _nowMs());
                    bool deliver = UpdateRecvTracking(seq);
                    if (deliver && rpay.Length > 0) TryQueueInbound(rpay);
                    break;
                }
                case UdpProto.TypeAck:
                {
                    if (!UdpProto.TryDecodeAck(data, out _, out ulong ctr, out byte[] aad, out byte[] body)) return;
                    var ackBody = _session.OpenPacket(ctr, body, aad);
                    if (ackBody == null) return;
                    if (!UdpProto.TryDecodeAckBody(ackBody, out ushort ackSeq, out uint ackBits)) return;
                    AdvanceClock(ref _lastRecvMs, _nowMs());
                    ProcessAck(ackSeq, ackBits);
                    break;
                }
            }
        }

        void TryQueueInbound(byte[] payload)
        {
            // CompleteAdding racing Add/TryAdd throws InvalidOperationException.
            // Serialize that boundary so a packet already being decoded is
            // either admitted before Close or cleanly dropped after it.
            lock (_inboundLock)
            {
                if (!_inbound.IsAddingCompleted)
                    _inbound.Add(payload);
            }
        }

        bool UpdateRecvTracking(ushort seq)
        {
            lock (_recvLock)
            {
                bool deliver = false;
                if (!_recvInit)
                {
                    _recvInit = true;
                    _recvSeq = seq;
                    _recvBits = 0;
                    deliver = true;
                }
                else if (UdpProto.SeqGreaterThan(seq, _recvSeq))
                {
                    int diff = (ushort)(seq - _recvSeq);
                    if (diff <= 32)
                    {
                        // C# masks shift counts to 5 bits (x << 32 == x << 0), but Go
                        // yields 0 for a shift >= width. At diff==32 we must force 0 to
                        // match the Go reference, hence the explicit guard.
                        uint shifted = diff >= 32 ? 0u : (_recvBits << diff);
                        _recvBits = shifted | (1u << (diff - 1));
                    }
                    else _recvBits = 0;
                    _recvSeq = seq;
                    deliver = true;
                }
                else
                {
                    int diff = (ushort)(_recvSeq - seq);
                    if (diff > 0 && diff <= 32)
                    {
                        uint bit = 1u << (diff - 1);
                        if ((_recvBits & bit) == 0)
                        {
                            _recvBits |= bit;
                            deliver = true;
                        }
                    }
                }
                _ackDirty = true;
                return deliver;
            }
        }

        void ProcessAck(ushort ackSeq, uint ackBits)
        {
            lock (_sendLock)
            {
                MarkAcked(ackSeq);
                for (int i = 0; i < 32; i++)
                {
                    if ((ackBits & (1u << i)) != 0)
                    {
                        ushort s = (ushort)(ackSeq - i - 1);
                        MarkAcked(s);
                    }
                }
            }
        }

        void MarkAcked(ushort seq)
        {
            int idx = seq % ReliableBufSize;
            if (_sendBuf[idx].Used && _sendBuf[idx].Seq == seq)
                _sendBuf[idx] = default;
        }

        /// Drive periodic work: timeout, retransmit, ACK flush, keepalive.
        /// Returns false if the connection timed out (caller should close).
        public bool Tick()
        {
            long now = _nowMs();
            if (now - Interlocked.Read(ref _lastRecvMs) > ConnectionTimeoutMs)
            {
                Close();
                return false;
            }

            bool reliableTimedOut = false;
            bool sendFailed = false;
            lock (_sendLock)
            {
                lock (_closeLock)
                {
                    if (_closed) return false;
                }

                for (int i = 0; i < _sendBuf.Length; i++)
                {
                    if (!_sendBuf[i].Used) continue;
                    if (now - _sendBuf[i].FirstSentAtMs > ReliableTimeoutMs)
                    {
                        reliableTimedOut = true;
                        break;
                    }
                    if (now - _sendBuf[i].LastSentAtMs >= RetransmitIntervalMs)
                    {
                        try
                        {
                            // Re-sealed with a FRESH counter. Resending the
                            // original bytes would carry the original counter and
                            // the peer's replay window would discard them, making
                            // every retransmission a no-op.
                            var rseq = _sendBuf[i].Seq;
                            _sendRaw(_session.SealPacket(_sendBuf[i].Payload,
                                ctr => UdpProto.EncodeReliableHeader(_token, rseq, ctr)));
                        }
                        catch
                        {
                            sendFailed = true;
                            break;
                        }
                        _sendBuf[i].LastSentAtMs = now;
                        AdvanceClock(ref _lastSendMs, now);
                    }
                }

                if (!reliableTimedOut && !sendFailed)
                {
                    // Snapshot under the receive lock, but never hold that lock
                    // across user/socket I/O. A later receive sets dirty again.
                    bool flushAck;
                    ushort ackSeq;
                    uint ackBits;
                    lock (_recvLock)
                    {
                        flushAck = _ackDirty;
                        ackSeq = _recvSeq;
                        ackBits = _recvBits;
                        if (flushAck) _ackDirty = false;
                    }
                    if (flushAck)
                    {
                        try
                        {
                            _sendRaw(_session.SealPacket(UdpProto.EncodeAckBody(ackSeq, ackBits),
                                ctr => UdpProto.EncodeAckHeader(_token, ctr)));
                            AdvanceClock(ref _lastSendMs, now);
                        }
                        catch
                        {
                            // Preserve a newer dirty state, or restore this
                            // snapshot, so a non-closing caller could retry it.
                            lock (_recvLock) _ackDirty = true;
                            sendFailed = true;
                        }
                    }
                }

                if (!reliableTimedOut && !sendFailed &&
                    now - Interlocked.Read(ref _lastSendMs) > KeepaliveIntervalMs)
                {
                    try
                    {
                        _sendRaw(_session.SealPacket(null,
                            ctr => UdpProto.EncodeUnreliableHeader(_token, ctr)));
                        AdvanceClock(ref _lastSendMs, now);
                    }
                    catch
                    {
                        sendFailed = true;
                    }
                }
            }

            if (reliableTimedOut || sendFailed)
            {
                // Maintenance is background work: contain I/O failures, close
                // deterministically, and let TickLoop exit instead of faulting.
                Close();
                return false;
            }
            return true;
        }

        /// Non-blocking receive (used by tests + pollers). timeoutMs=0 → immediate.
        public bool TryRecv([MaybeNullWhen(false)] out byte[] msg, int timeoutMs)
            => _inbound.TryTake(out msg, timeoutMs);

        /// Blocking receive. Returns null when the transport is closed.
        public byte[]? Recv()
        {
            try { return _inbound.Take(); }
            catch (InvalidOperationException) { return null; } // CompleteAdding called
        }

        public void Close()
        {
            // Atomic check-then-set so concurrent Close() callers (consumer +
            // Tick timeout path) can't both run the body and double-complete the
            // inbound collection (which would throw on the second call).
            lock (_sendLock)
            {
                lock (_closeLock)
                {
                    if (_closed) return;
                    _closed = true;
                }
                Array.Clear(_sendBuf, 0, _sendBuf.Length);
                // Wake consumers and close inbound admission before potentially
                // blocking on the best-effort network write.
                lock (_inboundLock)
                {
                    if (!_inbound.IsAddingCompleted)
                        _inbound.CompleteAdding();
                }
                // Keep the final packet inside the outbound serialization gate:
                // no reliable, ACK, or keepalive can be emitted after it.
                // Sealed with an empty body, so the tag alone proves we hold the
                // session key; the server will not act on a teardown it cannot
                // authenticate.
                try
                {
                    _sendRaw(_session.SealPacket(null,
                        ctr => UdpProto.EncodeDisconnectHeader(_token, ctr)));
                }
                catch { /* best effort */ }
            }
            CloseSocket(); // partial-class hook; no-op for the core-only ctor
        }

        void ThrowIfClosedLocked()
        {
            // Callers hold _sendLock, preserving the repository-wide
            // send -> close lock order.
            lock (_closeLock)
            {
                if (_closed) throw new ObjectDisposedException(nameof(UdpTransport));
            }
        }

        static void AdvanceClock(ref long clock, long stamp)
        {
            // Interlocked both publishes activity across receive/tick/caller
            // threads and prevents a delayed older writer from moving it back.
            while (true)
            {
                long previous = Interlocked.Read(ref clock);
                if (stamp <= previous ||
                    Interlocked.CompareExchange(ref clock, stamp, previous) == previous)
                    return;
            }
        }

        // Implemented in the socket section; the core ctor leaves it a no-op.
        partial void CloseSocket();
    }
}
