using System;
using System.Diagnostics;
using System.Net;
using System.Net.Sockets;
using System.Security.Cryptography;
using System.Threading;
using System.Threading.Tasks;

namespace Mmokit.Sdk.Core
{
    /// Real-socket half of UdpTransport: the handshake factory + the receive
    /// and tick loops over a System.Net.Sockets.UdpClient. The protocol logic
    /// lives in the core (UdpTransport.cs); this is the I/O shell.
    public sealed partial class UdpTransport
    {
        UdpClient? _socket;
        CancellationTokenSource? _cts;

        /// Connect to host:port: perform the ConnReq/ConnAccept handshake, then
        /// start the receive + tick loops. handshakeTimeoutMs default 5000.
        public static UdpTransport Connect(string host, int port, int handshakeTimeoutMs = 5000)
        {
            var socket = new UdpClient();
            socket.Connect(host, port);

            // Client salt (8 random bytes, little-endian u64 — matches Go Dial).
            Span<byte> saltBuf = stackalloc byte[8];
            RandomNumberGenerator.Fill(saltBuf);
            ulong clientSalt = 0;
            for (int i = 0; i < 8; i++) clientSalt |= (ulong)saltBuf[i] << (8 * i);

            byte[] connReq = UdpProto.EncodeConnReq(clientSalt);
            socket.Send(connReq, connReq.Length);

            // Read until the ConnAccept (0x04). On connect the server also pushes
            // an unsolicited reliable ServerConfig frame (0x01) that races the
            // ConnAccept; the two are separate datagrams with no ordering
            // guarantee. Discard non-ConnAccept datagrams and keep reading until
            // the deadline — ServerConfig is sent reliably (the server retransmits
            // it), so the discarded copy is redelivered once our receive loop is
            // running, and we can't process it pre-handshake anyway (no serverSalt
            // → no token yet).
            var handshakeClock = Stopwatch.StartNew();
            IPEndPoint? remote = null;
            ulong serverSalt = 0, echoedClientSalt = 0;
            while (true)
            {
                int remainingMs = handshakeTimeoutMs - (int)handshakeClock.ElapsedMilliseconds;
                if (remainingMs <= 0)
                { socket.Dispose(); throw new TimeoutException("UDP handshake timed out (no ConnAccept received)"); }
                socket.Client.ReceiveTimeout = remainingMs;
                byte[] resp;
                try { resp = socket.Receive(ref remote); }
                catch (SocketException ex) { socket.Dispose(); throw new TimeoutException("UDP handshake timed out", ex); }
                if (resp.Length > 0 && resp[0] == UdpProto.TypeConnAccept &&
                    UdpProto.TryDecodeConnAccept(resp, out echoedClientSalt, out serverSalt) &&
                    echoedClientSalt == clientSalt)
                    break;
                // else: early ServerConfig / stray packet / stale accept — discard, retry.
            }
            socket.Client.ReceiveTimeout = 0;

            uint token = UdpProto.MakeToken(clientSalt, serverSalt);

            // Monotonic clock in ms (Stopwatch — netstandard2.1 has no Environment.TickCount64).
            var clock = Stopwatch.StartNew();
            var t = new UdpTransport(raw => { try { socket.Send(raw, raw.Length); } catch { /* closed */ } },
                                     () => clock.ElapsedMilliseconds, token);
            t.AttachSocket(socket);
            return t;
        }

        void AttachSocket(UdpClient socket)
        {
            _socket = socket;
            _cts = new CancellationTokenSource();
            _ = Task.Run(() => ReceiveLoop(_cts.Token));
            _ = Task.Run(() => TickLoop(_cts.Token));
        }

        void ReceiveLoop(CancellationToken ct)
        {
            IPEndPoint? remote = null;
            while (!ct.IsCancellationRequested)
            {
                byte[] data;
                try { data = _socket!.Receive(ref remote); }
                catch { return; } // socket closed
                if (data.Length > 0) HandlePacket(data); // inbound chokepoint
            }
        }

        void TickLoop(CancellationToken ct)
        {
            while (!ct.IsCancellationRequested)
            {
                try { Thread.Sleep((int)RetransmitIntervalMs); } catch { return; }
                if (ct.IsCancellationRequested) return;
                if (!Tick()) return; // timed out → Tick already closed
            }
        }

        // partial hook called from core Close().
        partial void CloseSocket()
        {
            try { _cts?.Cancel(); } catch { }
            try { _socket?.Dispose(); } catch { }
        }
    }
}
