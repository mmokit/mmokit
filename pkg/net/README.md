# pkg/net

Transport-agnostic networking layer. Manages connections over WebSocket (TCP) and custom UDP with both reliable and unreliable channels. No game-specific logic — only deals with `[]byte` frames.

## Transport Interface (`transport.go`)

All network connections implement the `Transport` interface:

```go
type Transport interface {
    SendReliable(data []byte)    // guaranteed delivery (login, death, spawn)
    SendUnreliable(data []byte)  // fire-and-forget (world updates, input)
    DrainInput() [][]byte        // return and clear queued inbound messages
    Close()
}
```

For WebSocket (TCP), both send methods are identical. For UDP, they use different channels — unreliable messages bypass the reliability layer, avoiding head-of-line blocking.

## ConnManager (`server.go`)

Central hub for all active connections (any transport type).

```go
connMgr := net.NewConnManager()
go connMgr.ListenAndServe(ctx, ":8080")  // WebSocket
udpSrv, _ := net.NewUDPServer(":9000", connMgr)
go udpSrv.Run(ctx)                        // UDP
```

**Key methods:**

| Method | Description |
|--------|-------------|
| `Events() <-chan PlayerEvent` | Channel of connect/disconnect events |
| `Get(connID) Transport` | Look up a transport by ID |
| `Send(connID, data)` | Send unreliable (world updates) |
| `SendReliable(connID, data)` | Send reliable (login, death, spawn, sell) |
| `DrainInput(connID) [][]byte` | Return all queued input messages |
| `Remove(connID)` | Force-close and delete a connection |
| `AddTransport(t Transport) uint32` | Register any transport, returns connID |

**HTTP endpoints served by `ListenAndServe`:**

- `/ws` — WebSocket game endpoint
- `/gen/*` — generated protobuf files for clients
- `/` — web test client

## WebSocket Transport (`ws.go`)

Wraps the existing `Conn` (read/write pumps over WebSocket) to implement `Transport`. Since TCP is always reliable, both send methods are identical.

## Conn (`conn.go`)

Internal WebSocket connection with non-blocking send and buffered input. Used by `WSTransport`.

- **Outbound:** 64-message buffered channel, drops if full
- **Inbound:** Mutex-protected slice, drained atomically
- **Write pump:** Goroutine drains outbound channel → WebSocket
- **Read pump:** WebSocket → input buffer, runs until disconnect

## UDP Protocol (`udpproto/`)

Custom lightweight UDP protocol based on Glenn Fiedler's game networking patterns. Shared between server and Go client.

### Packet Types

| Type | Byte | Overhead | Description |
|------|------|----------|-------------|
| Unreliable | `0x00` | 5 bytes | `[type][token:4][payload]` |
| Reliable | `0x01` | 7 bytes | `[type][token:4][seq:2][payload]` |
| ACK | `0x02` | 11 bytes | `[type][token:4][ack_seq:2][ack_bits:4]` |
| ConnReq | `0x03` | 13 bytes | `[type][protocol_id:4][client_salt:8]` |
| ConnAccept | `0x04` | 21 bytes | `[type][protocol_id:4][client_salt:8][server_salt:8]` |
| Disconnect | `0x05` | 5 bytes | `[type][token:4]` |

### Connection Handshake

Client sends `ConnReq` with a random salt. Server responds with `ConnAccept` including its own salt. The XOR of both salts forms a 32-bit connection token used in all subsequent packets.

### Reliability

- 16-bit sequence numbers, 256-entry ring buffer
- ACKs encode highest received seq + 32-bit bitfield (33 messages per ACK)
- Retransmit after 100ms, timeout after 5s
- No head-of-line blocking — unreliable messages flow independently

### Timeouts

- No packet received for 10s → connection dead
- Keepalive sent if no traffic for 1s

## UDP Server (`udp_server.go`)

Single UDP socket with a read loop that dispatches by connection token. Handles connection handshakes and creates `UDPTransport` instances registered with `ConnManager`.

## UDP Transport (`udp_transport.go`)

Implements `Transport` over UDP. Runs a 10Hz tick loop for retransmission, standalone ACKs, keepalives, and timeout detection.

## Clients

### Go Client (`udpclient/`)

Self-contained Go UDP client:

```go
client, _ := udpclient.Dial("localhost:9000")
client.SendReliable(loginBytes)
msg, _ := client.Recv()  // blocks
client.Close()
```

### JS Client (`jsclient/transport.js`)

WebSocket transport wrapper with the same reliable/unreliable API shape. Browsers can't do raw UDP, so both methods go through TCP:

```js
import { WSTransport } from './transport.js';
const t = new WSTransport('ws://localhost:8080/ws');
t.onOpen(() => t.sendReliable(loginBytes));
t.onMessage((data) => { ... });
```

## Thread Safety

`ConnManager` uses `sync.RWMutex` for the connection map. `Conn` uses `sync.Mutex` for input. `UDPTransport` uses separate mutexes for send buffer and inbound queue. The events channel bridges networking goroutines to the game loop.
