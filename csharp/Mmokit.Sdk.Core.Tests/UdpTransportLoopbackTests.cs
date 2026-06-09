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
        public void Connect_Handshake_And_ReliableRoundTrip()
        {
            // Minimal in-test UDP "server": bind a loopback port, answer ConnReq
            // with ConnAccept, then on a reliable packet send back an unreliable
            // payload the client can Recv().
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
                server.Send(UdpProto.EncodeConnAccept(cs, ss), client!);
                // 2) await a reliable packet, echo a reply as unreliable
                while (!serverDone.IsCancellationRequested)
                {
                    server.Client.ReceiveTimeout = 2000;
                    byte[] pkt;
                    try { pkt = server.Receive(ref client); }
                    catch (SocketException) { return; }
                    if (pkt[0] == UdpProto.TypeReliable &&
                        UdpProto.TryDecodeReliable(pkt, out _, out ushort seq, out byte[] payload) &&
                        payload.Length > 0)
                    {
                        // ack it, then send a server→client reply.
                        server.Send(UdpProto.EncodeAck(serverToken, seq, 0), client!);
                        server.Send(UdpProto.EncodeUnreliable(serverToken, new byte[] { 42, 43 }), client!);
                        return;
                    }
                }
            });

            UdpTransport client2 = UdpTransport.Connect("127.0.0.1", port);
            try
            {
                client2.SendReliable(new byte[] { 1, 2, 3 });
                Assert.True(client2.TryRecv(out byte[] reply, 3000), "expected a server reply within 3s");
                Assert.Equal(new byte[] { 42, 43 }, reply);
            }
            finally
            {
                serverDone.Cancel();
                client2.Close();
                serverTask.Wait(2000);
            }
        }
    }
}
