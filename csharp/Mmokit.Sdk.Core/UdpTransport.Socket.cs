using System;
using System.Diagnostics;
using System.IO;
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

        /// Connect to host:port: perform the ConnReq/ConnAccept/ConnConfirm
        /// handshake, then start the receive + tick loops.
        ///
        /// udpKeyId and udpKey come from POST /auth/udp-key over HTTPS. The
        /// client authenticates there FIRST and only then opens a UDP session —
        /// v1 built a session first and authenticated afterwards over the op
        /// channel, which left every byte forgeable on path.
        public static UdpTransport Connect(string host, int port, ulong udpKeyId, byte[] udpKey, int handshakeTimeoutMs = 5000)
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
            byte[] cookie = Array.Empty<byte>();
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
                    UdpProto.TryDecodeConnAccept(resp, out echoedClientSalt, out serverSalt, out cookie) &&
                    echoedClientSalt == clientSalt)
                    break;
                // else: early ServerConfig / stray packet / stale accept — discard, retry.
            }
            socket.Client.ReceiveTimeout = 0;

            uint token = UdpProto.MakeToken(clientSalt, serverSalt);

            var session = UdpSession.Derive(udpKey, UdpSession.SessionSalt(clientSalt, serverSalt), isClient: true);

            // Echo the cookie and name the key. The server holds nothing until
            // this arrives, so a lost ConnConfirm costs a retry rather than a
            // leaked table entry, and server-side promotion is idempotent.
            byte[] confirm = UdpProto.EncodeConnConfirm(udpKeyId, clientSalt, serverSalt, cookie);
            socket.Send(confirm, confirm.Length);

            // Monotonic clock in ms (Stopwatch — netstandard2.1 has no Environment.TickCount64).
            var clock = Stopwatch.StartNew();
            var t = new UdpTransport(raw =>
            {
                int sent = socket.Send(raw, raw.Length);
                if (sent != raw.Length) throw new IOException("UDP socket reported a short datagram write");
            }, () => clock.ElapsedMilliseconds, token, session);
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
                catch
                {
                    if (!ct.IsCancellationRequested) Close();
                    return;
                }
                if (data.Length > 0)
                {
                    try { HandlePacket(data); } // inbound chokepoint
                    catch
                    {
                        Close();
                        return;
                    }
                }
            }
        }

        void TickLoop(CancellationToken ct)
        {
            while (!ct.IsCancellationRequested)
            {
                try { Thread.Sleep((int)RetransmitIntervalMs); } catch { return; }
                if (ct.IsCancellationRequested) return;
                try
                {
                    if (!Tick()) return; // timed out / background I/O failed
                }
                catch
                {
                    // Last-resort containment for an unexpected clock/protocol
                    // exception: background tasks must not fault silently.
                    Close();
                    return;
                }
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
