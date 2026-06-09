using System;
using System.Collections.Concurrent;
using System.Collections.Generic;
using System.Diagnostics.CodeAnalysis;

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

        struct ReliableEntry { public byte[] Payload; public long SentAtMs; public bool Acked; public bool Used; }

        readonly Action<byte[]> _sendRaw;
        readonly Func<long> _nowMs;
        readonly uint _token;

        readonly object _sendLock = new();
        ushort _sendSeq;
        readonly ReliableEntry[] _sendBuf = new ReliableEntry[ReliableBufSize];

        // Recv-tracking trio is written on the receive thread (HandlePacket) and
        // read on the tick thread (Tick) under the socket loops — guard it. (Go's
        // memory model tolerates the unsynchronized access; C#'s permits
        // read-hoisting that could stall ACK flushing, so the lock is required.)
        readonly object _recvLock = new();
        ushort _recvSeq;
        uint _recvBits;
        bool _ackDirty;

        readonly BlockingCollection<byte[]> _inbound = new(new ConcurrentQueue<byte[]>());

        long _lastRecvMs;
        long _lastSendMs;
        readonly object _closeLock = new();
        bool _closed;

        /// Core ctor (and the testing entry point). The socket factory below
        /// constructs this with a sink that writes to the UdpClient.
        public UdpTransport(Action<byte[]> sendRaw, Func<long> nowMs, uint token)
        {
            _sendRaw = sendRaw;
            _nowMs = nowMs;
            _token = token;
            _lastRecvMs = nowMs();
            _lastSendMs = nowMs();
        }

        public uint Token => _token;

        public void SendReliable(byte[] data)
        {
            ushort seq;
            byte[] payload = (byte[])data.Clone();
            lock (_sendLock)
            {
                seq = _sendSeq;
                _sendSeq++;
                int idx = seq % ReliableBufSize;
                _sendBuf[idx] = new ReliableEntry { Payload = payload, SentAtMs = _nowMs(), Acked = false, Used = true };
            }
            _sendRaw(UdpProto.EncodeReliable(_token, seq, payload));
            _lastSendMs = _nowMs();
        }

        public void SendUnreliable(byte[] data)
        {
            _sendRaw(UdpProto.EncodeUnreliable(_token, data));
            _lastSendMs = _nowMs();
        }

        /// Inbound chokepoint. `data` is a full, decrypted packet.
        public void HandlePacket(byte[] data)
        {
            if (data.Length == 0) return;
            switch (data[0])
            {
                case UdpProto.TypeUnreliable:
                    if (!UdpProto.TryDecodeUnreliable(data, out _, out byte[] upay)) return;
                    _lastRecvMs = _nowMs();
                    if (upay.Length == 0) return; // keepalive
                    _inbound.Add(upay);
                    break;
                case UdpProto.TypeReliable:
                    if (!UdpProto.TryDecodeReliable(data, out _, out ushort seq, out byte[] rpay)) return;
                    _lastRecvMs = _nowMs();
                    UpdateRecvTracking(seq);
                    if (rpay.Length > 0) _inbound.Add(rpay);
                    break;
                case UdpProto.TypeAck:
                    if (!UdpProto.TryDecodeAck(data, out _, out ushort ackSeq, out uint ackBits)) return;
                    _lastRecvMs = _nowMs();
                    ProcessAck(ackSeq, ackBits);
                    break;
            }
        }

        void UpdateRecvTracking(ushort seq)
        {
            lock (_recvLock)
            {
                if (_recvSeq == 0 && !_ackDirty)
                {
                    _recvSeq = seq;
                    _recvBits = 0;
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
                }
                else
                {
                    int diff = (ushort)(_recvSeq - seq);
                    if (diff > 0 && diff <= 32) _recvBits |= 1u << (diff - 1);
                }
                _ackDirty = true;
            }
        }

        void ProcessAck(ushort ackSeq, uint ackBits)
        {
            lock (_sendLock)
            {
                int idx = ackSeq % ReliableBufSize;
                if (_sendBuf[idx].Used) _sendBuf[idx].Acked = true;
                for (int i = 0; i < 32; i++)
                {
                    if ((ackBits & (1u << i)) != 0)
                    {
                        ushort s = (ushort)(ackSeq - i - 1);
                        int j = s % ReliableBufSize;
                        if (_sendBuf[j].Used) _sendBuf[j].Acked = true;
                    }
                }
            }
        }

        /// Drive periodic work: timeout, retransmit, ACK flush, keepalive.
        /// Returns false if the connection timed out (caller should close).
        public bool Tick()
        {
            long now = _nowMs();
            if (now - _lastRecvMs > ConnectionTimeoutMs) { Close(); return false; }

            lock (_sendLock)
            {
                for (int i = 0; i < _sendBuf.Length; i++)
                {
                    if (!_sendBuf[i].Used || _sendBuf[i].Acked) continue;
                    long age = now - _sendBuf[i].SentAtMs;
                    if (age > ReliableTimeoutMs) { Close(); return false; }
                    if (age >= RetransmitIntervalMs)
                    {
                        // NOTE: reconstructs seq from the buffer index — faithful
                        // to the Go reference; correct only for the first
                        // ReliableBufSize reliable sends (known upstream limitation).
                        ushort seq = (ushort)i;
                        _sendRaw(UdpProto.EncodeReliable(_token, seq, _sendBuf[i].Payload));
                        _sendBuf[i].SentAtMs = now;
                    }
                }
            }

            // Snapshot the recv-tracking trio under the lock, then emit outside it.
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
            if (flushAck) _sendRaw(UdpProto.EncodeAck(_token, ackSeq, ackBits));

            if (now - _lastSendMs > KeepaliveIntervalMs)
            {
                _sendRaw(UdpProto.EncodeUnreliable(_token, null));
                _lastSendMs = now;
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
            lock (_closeLock)
            {
                if (_closed) return;
                _closed = true;
            }
            try { _sendRaw(UdpProto.EncodeDisconnect(_token)); } catch { /* best effort */ }
            _inbound.CompleteAdding();
            CloseSocket(); // partial-class hook; no-op for the core-only ctor
        }

        // Implemented in the socket section; the core ctor leaves it a no-op.
        partial void CloseSocket();
    }
}
