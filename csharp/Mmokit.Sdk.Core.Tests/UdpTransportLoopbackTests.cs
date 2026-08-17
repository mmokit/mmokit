using System;
using System.Net;
using System.Net.Sockets;
using System.Threading;
using System.Threading.Tasks;
using Xunit;

namespace Mmokit.Sdk.Core.Tests
{
    public class UdpTransportLoopbackTests
    {
        [Fact]
        public async Task Connect_Handshake_And_ReliableRoundTrip()
        {
            // Minimal in-test UDP "server": answer ConnReq with ConnAccept,
            // accept the ConnConfirm, then echo a reliable packet back as an
            // unreliable one the client can Recv().
            //
            // It has to derive the same session the client does, because every
            // data packet is now sealed — there is no plaintext path left for a
            // stub server to take.
            var master = new byte[32];
            for (int i = 0; i < 32; i++) master[i] = (byte)(0x30 + i);
            const ulong keyId = 0x1234;
            using var server = new UdpClient(new IPEndPoint(IPAddress.Loopback, 0));
            int port = ((IPEndPoint)server.Client.LocalEndPoint!).Port;
            uint serverToken = 0;
            var serverDone = new CancellationTokenSource();

            var serverTask = Task.Run(() =>
            {
                IPEndPoint? client = null;
                // 1) handshake
                byte[] req = server.Receive(ref client);
                Assert.Equal(UdpProto.TypeConnReq, req[0]);
                Assert.True(UdpProto.TryDecodeConnReq(req, out ulong cs));
                ulong ss = 0xABCDEF0123456789;
                serverToken = UdpProto.MakeToken(cs, ss);
                var cookie = new byte[UdpProto.CookieSize]; // stub: not verified in-test
                server.Send(UdpProto.EncodeConnAccept(cs, ss, cookie), client!);
                var srvSession = UdpSession.Derive(master, UdpSession.SessionSalt(cs, ss), isClient: false);

                // 1b) the client's ConnConfirm
                server.Client.ReceiveTimeout = 2000;
                byte[] confirm = server.Receive(ref client);
                Assert.Equal(UdpProto.TypeConnConfirm, confirm[0]);
                Assert.True(UdpProto.TryDecodeConnConfirm(confirm, out ulong gotKeyId, out _, out _, out _));
                Assert.Equal(keyId, gotKeyId);
                // 2) await a reliable packet, echo a reply as unreliable
                while (!serverDone.IsCancellationRequested)
                {
                    server.Client.ReceiveTimeout = 2000;
                    byte[] pkt;
                    try { pkt = server.Receive(ref client); }
                    catch (SocketException) { return; }
                    if (pkt[0] == UdpProto.TypeReliable &&
                        UdpProto.TryDecodeReliable(pkt, out _, out ushort seq, out ulong ctr, out byte[] aad, out byte[] body))
                    {
                        var payload = srvSession.OpenPacket(ctr, body, aad);
                        if (payload == null || payload.Length == 0) continue;

                        // ack it, then send a server→client reply.
                        server.Send(srvSession.SealPacket(UdpProto.EncodeAckBody(seq, 0),
                            c2 => UdpProto.EncodeAckHeader(serverToken, c2)), client!);
                        server.Send(srvSession.SealPacket(new byte[] { 42, 43 },
                            c2 => UdpProto.EncodeUnreliableHeader(serverToken, c2)), client!);
                        return;
                    }
                }
            });

            UdpTransport client2 = UdpTransport.Connect("127.0.0.1", port, keyId, master);
            try
            {
                client2.SendReliable(new byte[] { 1, 2, 3 });
                Assert.True(client2.TryRecv(out byte[]? reply, 3000), "expected a server reply within 3s");
                Assert.NotNull(reply);
                Assert.Equal(new byte[] { 42, 43 }, reply);
            }
            finally
            {
                serverDone.Cancel();
                client2.Close();
                await serverTask.WaitAsync(TimeSpan.FromSeconds(2));
            }
        }
    }
}
