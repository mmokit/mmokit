# `pkg/net`

Game-agnostic client transport layer. It manages byte frames over WebSocket
and the repository's custom UDP protocol; typed messages and replication live
in higher-level packages.

## Frame channels

Application frames use a leading channel byte:

| Channel | Value | Inbound queue |
| --- | --- | --- |
| Event / typed client input | `0x00` | `DrainInput` |
| Correlated operation | `0x01` | `DrainOpInput` |

The WebSocket read pump removes the channel byte before queueing the payload.
UDP does the same when a recognized channel byte is present. Outbound callers
provide the complete framed bytes, including the channel byte.

## Transport contract

Every registered connection implements:

```go
type Transport interface {
    SendReliable(data []byte) SendResult
    SendUnreliable(data []byte) SendResult
    DrainInput() [][]byte
    DrainOpInput() [][]byte
    InjectInput(data []byte)
    Close()
}
```

`InjectInput` is used by the cell-handoff path to replay event-channel input
on a destination. `ByteCounter` and `ConnStatsProvider` are optional
diagnostic interfaces.

Engine hot paths depend on the narrower `ConnSender` interface rather than a
concrete manager. Both `*ConnManager` and the universe's mesh-backed virtual
manager satisfy that shape.

Every send returns a `SendResult`. `SendQueued` means the transport accepted
ownership of the frame; it is not a remote acknowledgement. The result also
reports the accepted path's delivery class (`DeliveryBestEffort`,
`DeliveryOrdered`, or `DeliveryReliableOrdered`). Callers that advance delta
baselines must require `result.Supports(DeliveryReliableOrdered)`. Other
dispositions distinguish backpressure, a closed connection, a missing route,
definite failure, and an indeterminate downstream result where retrying may
duplicate the frame.

Distributed replication can opt into `TrackedReplicationSender` and
`ReplicationReceiptSource`. Its initial ordered mesh admission does not advance
a delta baseline; a writer-scoped receipt confirms the gateway's final client
transport enqueue. Generic events and operations still use the ordinary
`ConnSender` result and do not receive this end-to-end acknowledgement.

## Connection manager

```go
connMgr := net.NewConnManager()
err := connMgr.ListenAndServe(ctx, ":8080")
```

`ConnManager` assigns monotonically increasing connection IDs and stores any
registered transport. Its main operations are:

| Method | Purpose |
| --- | --- |
| `Events()` | Receive connect and disconnect notifications. |
| `Get(id)` | Look up a transport. |
| `AddTransport(t)` | Register a transport and emit a connected event. |
| `Remove(id)` | Remove and close a transport. |
| `Unregister(id)` | Remove it without closing and emit a disconnect event. |
| `Send(id, data)` | Select the transport's unreliable path. |
| `SendReliable(id, data)` | Select its reliable path. |
| `DrainInput(id)` / `DrainOpInput(id)` | Drain the two inbound queues. |
| `InjectInput(id, data)` | Append to a connection's event/input queue. |
| `ActiveConnIDs()` / `ConnectionCount()` | Return connection snapshots. |
| `TotalBytesSent()` / `TotalBytesRecv()` | Aggregate active `ByteCounter` transports. |
| `RemoteAddrString(id)` | Return the direct WebSocket peer address when known. |

`OnUpgrade` runs synchronously after a successful WebSocket upgrade and
registration but before the read loop. The universe gateway uses it to inspect
the original HTTP request and authenticate cookies.

Set `AllowedOrigins` before accepting WebSockets. An empty list enforces
same-origin browser requests; native clients without an `Origin` header are
accepted.

### Standalone HTTP server

`ListenAndServe` mounts:

- `/ws`: game WebSocket
- `/probe-ws`: direct 20 Hz diagnostic heartbeat
- `/debug/conn-stats`: per-connection write and queue timing JSON
- routes registered through `Handle` before the server starts

Normal MMOKIT processes let `pkg/universe` own the HTTP mux and mount these
handlers alongside metrics, commands, authentication, and game routes.

## WebSocket transport

`WSTransport` wraps the internal `Conn`. Because WebSocket runs over TCP,
`SendReliable` and `SendUnreliable` both enqueue into the same 64-entry
outbound channel. Enqueue is non-blocking; a full channel logs and drops the
frame and returns `SendBackpressure`.

The read pump routes binary messages into mutex-protected event or operation
queues. The write pump records queue delay, write duration, slow-write counts,
and cumulative byte counters exposed by `/debug/conn-stats`. Each write has a
five-second deadline; a client that stops reading is closed instead of pinning
the writer and its queue indefinitely.

## UDP

```go
udpServer, err := net.NewUDPServer(":9000", connMgr)
if err != nil {
    return err
}
udpServer.SetKeyRegistry(keys)             // else every handshake is refused
udpServer.SetOnAuthenticated(bindSession)  // else sessions are sealed but anonymous
udpServer.Run(ctx)                         // blocks until cancellation
```

The server uses one UDP socket and dispatches datagrams to `UDPTransport`
instances by token. Datagrams are capped at a 1400-byte receive buffer; this
protocol does not fragment oversized application messages.

### Packet layout — protocol version 2

Every packet carries a type byte and then a version byte. `‖ sealed(n+16)` is an
AEAD-sealed body: `n` plaintext bytes plus a 16-byte tag, with the cleartext
header fed in as additional authenticated data.

| Type | Byte | Layout | Size |
| --- | --- | --- | ---: |
| Unreliable | `0x00` | `[type][ver][token:u32][counter:u64] ‖ sealed(n+16)` | 14+n+16 |
| Reliable | `0x01` | `[type][ver][token:u32][seq:u16][counter:u64] ‖ sealed(n+16)` | 16+n+16 |
| ACK | `0x02` | `[type][ver][token:u32][counter:u64] ‖ sealed(6+16)` | 36 |
| Connection request | `0x03` | `[type][ver][protocol_id:u32][client_salt:u64]` | 14 |
| Connection accept | `0x04` | `[type][ver][protocol_id:u32][client_salt:u64][server_salt:u64][cookie:16]` | 38 |
| Disconnect | `0x05` | `[type][ver][token:u32][counter:u64] ‖ sealed(0+16)` | 30 |
| Connection confirm | `0x06` | `[type][ver][key_id:u64][client_salt:u64][server_salt:u64][cookie:16]` | 42 |

Client and server salts are XOR-folded into the 32-bit demultiplexing token.
**The token is a session index, not a credential** — the AEAD tag is what proves
the sender holds the session key. Header fields are cleartext because the server
must read the token to find the session before it can decrypt, but they are
authenticated as AAD, so a valid body cannot be moved onto another session or
another packet type.

### Handshake and identity

The handshake is stateless on the server. `ConnReq` is answered with a
`ConnAccept` carrying an HMAC cookie over the peer's address and both salts;
nothing is retained. `ConnConfirm` echoes that cookie — proving return
routability from the same address — and names the key ID the client drew from
`POST /auth/udp-key` over HTTPS. Only then does a session exist.

The key registry resolves that ID to a user, and `SetOnAuthenticated` delivers
the identity to the gateway, which binds the player session. So a UDP connection
is authenticated from its first datagram and there is no post-connect
authentication step; the client authenticates over HTTPS before it opens a
socket. Directional keys are derived per session via HKDF-SHA256 over both
salts, so the two directions never share a key, and each direction enforces a
64-packet sliding replay window.

The reliable path uses 16-bit sequence numbers, a 256-slot send ring, and an
ACK containing the highest sequence plus a 32-bit history. Unacknowledged
packets are retried on the 100 ms transport tick and expire five seconds after
their initial send; retransmission does not extend that lifetime. Exact
sequence identity prevents stale ACKs from clearing a wrapped ring slot, and a
full ring reports backpressure rather than overwriting live data. The receive
window suppresses duplicate retransmissions and accepts an unseen out-of-order
packet once. It does not provide an application-level in-order buffer, and the
send API does not report remote delivery.

A retransmission re-seals the frame under a **fresh** AEAD counter rather than
resending the original bytes. Resending them would present a counter the peer's
replay window has already retired, so every retransmission would be silently
discarded — reliability would fail exactly when it was needed.

An idle transport sends a keepalive after 1 second and times out after 10
seconds without receiving a packet. `UDPTransport` runs those checks on a
100 ms ticker.

### Go UDP client

`pkg/net/udpclient` contains the matching standalone client:

```go
// keyID and key come from POST /auth/udp-key over HTTPS.
client, err := udpclient.Dial("localhost:9000", keyID, key)
if err != nil {
    return err
}
defer client.Close()

if err := client.SendReliable(frame); err != nil {
    return err
}
message, err := client.Recv()
```

Browser clients use WebSocket; browsers cannot open raw UDP sockets.
The Go client mirrors the server's sequence-wrap, stale-ACK, receive-dedup,
five-second lifetime, and concurrency rules. `SendReliable` returns
`udpclient.ErrReliableWindowFull` when its 256-slot window cannot advance.

## Ownership and copying

Inbound injection and UDP reliable sends copy their payloads. WebSocket
`Conn.Send` queues the supplied slice without copying it, so callers must treat
that memory as immutable after sending.
