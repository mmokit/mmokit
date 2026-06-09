# C# Client SDK Generation + Windows/Unity Deploy Workflow

**Date:** 2026-06-06 (amended 2026-06-09: headless/stateless architecture)
**Status:** Design approved, pending implementation plan
**Target game:** `examples/4node-basic` (the actual 4node-basic backend, with a new Unity frontend)

## Goal

Generate a typed **C# client SDK** from the existing protocol schema (`--dump-schema`)
so a C# program can connect to the 4node-basic server over the project's **UDP
transport**, authenticate, send input, call server-side functions as RPCs, and
receive decoded entity/event state. The SDK is **renderer-agnostic and
headless-capable**: it is a complete game-client API with no rendering and no
dependency on Unity. Unity sits on top as a **pure renderer** reading SDK state;
a separate headless C# bot project can drive the game with no renderer at all.
The generated SDK deploys directly to a Windows filesystem path (reachable from
WSL at `/mnt/c/...`) so it can be pulled into a Unity project's `Assets/` tree.

This is the C# analog of the existing TypeScript SDK (`cmd/sdkgen` → `web-pixi/sdk/`),
unified under one schema. The Go load-test bot (`internal/bot/`) is the reference for
the *high-level client surface* a headless C# consumer needs — but per the
Architecture amendment, the bot's state-tracking stays in the consumer, not the SDK.

## Architecture: stateless, headless-capable client (amendment 2026-06-09)

The SDK is **stateless** — it owns decode, transport, RPC, and interpolation
primitives, and emits **typed events**; the **consumer owns accumulated world
state**. This mirrors the existing stack exactly: the TS SDK decodes deltas and
provides interpolation primitives while **web-pixi owns the entity map and
renders**. We replicate that proven split in C#.

**Rationale.** Game engines (Unity especially) have opinionated, idiomatic state
representations (GameObjects, or DOTS/ECS). An SDK that bakes in its own world
model fights the engine. The norm is: the SDK normalizes the wire into typed
events + interpolation helpers; the consumer owns the scene graph / entity store.
This still honors the minimize-duplication principle — the expensive, error-prone
shared work (binary delta decode, event normalization, interpolation math) lives
**once** in the SDK; only the trivial "put this entity into my container" loop
differs per consumer, and it *should* differ (Unity → GameObjects; bot → a dict).

**What the SDK provides (high-level, transport-abstracted):**
- Connection lifecycle: connect / authenticate / disconnect over UDP.
- Typed event callbacks: `OnEntitySpawned` / `OnEntityUpdated` / `OnEntityDespawned`
  (carrying decoded component fields), `OnServerEvent<T>` for `RegisterEvent` types.
- Typed RPC / op call methods (over `RegisterOp`, see below) and client-input send
  methods (over `HandleClient`).
- Interpolation primitives (the ring buffer) the consumer **feeds and queries** for
  smooth rendering — the SDK does not hold the entity map.

**What the SDK does NOT do:** accumulate world state. There is **no** port of
`internal/bot`'s stateful `WorldState` into the SDK core. State lives in Unity
(native) or in the separate headless C# bot project. *(Optional, deferred: a tiny
opt-in engine-agnostic `WorldState` accumulator could later ship as a convenience
module for headless bot projects — never wired into the core client, and Unity
ignores it. Out of scope here.)*

**RPC / "write a Go function, call it from the client":** the transport-abstracted
RPC substrate already exists as `RegisterOp[Req, Res]` (generates typed client
methods; transport hidden). This effort **generates C# over the existing
`RegisterOp` ops** — no new server mechanism. A Wails-style ergonomic `Bind()`
layer (plain Go funcs/methods auto-derived into ops via signature reflection, no
hand-written `Req`/`Res` envelopes) is a **deferred follow-on spec**, designed
once the C# SDK round-trips end-to-end.

## Scope

| Piece | In scope |
|---|---|
| 1. C# generator backend (`--lang=csharp` in `cmd/sdkgen`) | ✅ |
| 2. C# `_core` runtime — UDP transport port + delta-decoder + interpolation | ✅ |
| 3. Server-side: UDP transport op-channel (`0x01`) support | ✅ |
| 4. Windows deploy workflow (`UNITY_SDK_DIR` env var → `/mnt/c` justfile recipe) | ✅ |
| 5. Cross-language wire-safety test (golden bytes) | ✅ |
| 6. The Unity demo/load-test project itself | ❌ Built in Unity, consuming this SDK |
| 7. Headless C# bot project (state-tracking consumer) | ❌ Separate consumer project, not the SDK |
| 8. Wails-style `Bind()` RPC ergonomics layer | ❌ Deferred follow-on spec (generate over `RegisterOp` now) |
| 9. Opt-in engine-agnostic `WorldState` accumulator module | ❌ Deferred; never in core client |

## Guiding principle: minimize duplication

The surface that **iterates in mmokit** — entity kinds, components,
`RegisterEvent`/`HandleClient`/`RegisterOp`, field encodings — is 100%
schema-driven and therefore **fully code-generated for C#**. Adding a component,
event, or op in Go and re-running `--dump-schema` regenerates the C# SDK with
zero hand edits.

The **only** hand-written C# is the low-churn primitives:

- UDP framing protocol (frozen; Glenn-Fiedler-style; does not change when game
  types change)
- quantize read helpers (`qvel`/`qangle`/`qnorm`/`f32`/integers — stable)
- interpolation ring buffer

So high-churn files are generated; hand-written files rarely change. Protocol
constants (packet type bytes, `protocol_id`, timeouts, quantize scales) live in
**one place** (Go: `pkg/net/udpproto`, `pkg/quantize`) and the C# port is guarded
against drift by a golden-bytes cross-language test (§6).

## A. Generator architecture (the language-backend seam)

Refactor `cmd/sdkgen` so a `Backend` interface owns the language-specific
emission; `main.go`'s file-dispatch (which files to emit based on schema
contents) and schema decode stay shared and language-agnostic.

```go
type Backend interface {
    Lang() string                      // "ts" | "csharp"
    FileExt() string                   // ".ts" | ".cs"
    CoreFiles() []CoreFile             // runtime files to copy into _core/
    EncodingToType(enc string) string  // "qvel" -> "number"/"float"
    Transport(s ProtocolSchema) string
    Entities(s ProtocolSchema) string
    DeltaDecoder(s ProtocolSchema) string
    Client(s ProtocolSchema) string
    Broadcasts(s ProtocolSchema) string
    Inputs(s ProtocolSchema) string
    Operations(s ProtocolSchema) string
    EntityType(s ProtocolSchema) string
    Index(s ProtocolSchema) string
}
```

- `tsBackend` wraps the **existing** functions in `generate.go` / `broadcasts.go` /
  `inputs.go` / `operations.go` / `entitytype.go` / `server_events.go`. Behavior
  preserved — `just space-sdk` and `just client-sdk` output byte-identical to today.
- `csharpBackend` is new.
- `--lang` flag defaults to `ts` (no change to existing recipes).
- The conditional file emission in `main.go` (emit `broadcasts` only when broadcast
  or server-event types exist, etc.) stays shared — it is schema-driven, not
  language-driven.

## B. Server-side: UDP op-channel support (the one real gap)

Auth and every typed op (`RegisterOp[Req,Res]`) flow over **channel `0x01`**
(operations). The op path is already transport-agnostic at the gateway:

```
connect (unauthenticated)
  -> client sends authRegister/authLogin op over reliable 0x01
  -> runSessionPump -> op-router -> auth.handleLogin
  -> AuthLoginSucceededEvent on service.Bus
  -> gateway.onAuthSuccess(connID, userID, username, token)
  -> dispatchPostAuthAssignment -> PlayerAssignment -> spawn
```

This works identically for WS and UDP **except** that `UDPTransport` does not
demux channel `0x01`:

- `pkg/net/udp_transport.go:172` `routePayload` buckets `0x00` → event queue and
  dumps everything else there too; there is no op queue.
- `pkg/net/udp_transport.go:108` `DrainOpInput()` hard-returns `nil`.

### Fix (mirrors WS `Conn`, `pkg/net/conn.go:52-109`)

1. Add `opInbound [][]byte` (+ its own guard or reuse `inMu`) to `UDPTransport`.
2. In `routePayload`, route `ChannelOperation` (`0x01`) → `opInbound`; keep `0x00`
   → `inbound`; preserve the legacy no-prefix fallback. Apply on **both** the
   reliable and unreliable inbound paths (auth ops are sent reliable).
3. Implement `DrainOpInput()` to drain `opInbound` under the lock.

No other server change is required — auth handlers, the bus event, `onAuthSuccess`,
and PlayerAssignment are all transport-agnostic.

### Consequence for the C# client

The C# SDK authenticates **entirely over the op channel** (`authRegister` /
`authLogin` typed ops over UDP-reliable). **No HTTP client is needed in Unity** —
the SDK does not call `/auth/register` or `/auth/login` over HTTP; it uses the
generated typed ops. (HTTP auth remains available for the web client; it is simply
not the path the C# SDK takes.)

### Future requirement: transport encryption

Auth credentials (and everything else) currently travel as **plaintext** over UDP.
This is acceptable for the dev/load-test demo but MUST be encryptable later. The
op-channel approach is chosen partly because it keeps a single, controllable
encryption seam: all traffic — auth included — rides the one UDP transport, so a
future payload-encryption layer covers auth automatically (unlike a split
HTTP-TLS-then-UDP design).

The design must not foreclose this. Concretely, both the Go `udpproto` server side
and the C# `_core/UdpTransport.cs` port must keep the framing layered so encryption
can be inserted at the transport boundary without touching the op/event layers above:

- The handshake already exchanges client/server salts (`ConnReq`/`ConnAccept`) and
  derives a shared 32-bit token. That salt exchange is the natural hook to later
  upgrade into a real key-exchange step (e.g. derive a session key, or layer a
  Noise/DTLS-style handshake) — the token/salt plumbing must stay accessible, not
  buried.
- Channel-byte demux (`0x00`/`0x01`) and reliability operate on the **decrypted**
  payload. Keep encrypt/decrypt as a single chokepoint at packet send/recv
  (`sendRaw` / inbound dispatch) so encryption wraps the whole payload below the
  channel split.
- No design decision in this spec (auth-over-op-channel, the C# core port, the
  op-channel demux fix) may assume plaintext beyond the transport boundary.

Implementing encryption is **out of scope for this spec** — but the above seam is a
hard constraint on the transport implementation so it can be added without rework.

## C. C# `_core` runtime (hand-ported, copied like the TS cores)

Kept in-repo as the single source for the C# ports and copied into the SDK output
`_core/` by `csharpBackend.CoreFiles()`, exactly as the TS cores are copied today.

| File | Source it ports | Content |
|---|---|---|
| `_core/UdpTransport.cs` | `pkg/net/udpproto` + `pkg/net/udpclient` | ConnReq/ConnAccept handshake, 16-bit seq reliability + 32-bit ACK bitfield, unreliable channel, keepalive/timeout. Channel-byte prefix on send (`0x00`/`0x01`); demux on recv. Uses `System.Net.Sockets.UdpClient`. |
| `_core/DeltaDecoderCore.cs` | `pkg/quantize/ts/delta-decoder-core.ts` (+ Go `pkg/quantize`) | big-endian `BigEndianReader`; quantize decode for `qvel`/`qangle`/`qnorm`/`f32`/`u8`/`u16`/`u32`/`i16`/`bool`. |
| `_core/InterpolationCore.cs` | `pkg/quantize/ts/interpolation-core.ts` | per-entity ring keyed by `producedAtMs`; render-delay + extrapolation cap. |

Suggested in-repo home for the ports: `pkg/net/cs/UdpTransport.cs` and
`pkg/quantize/cs/*.cs` (mirroring the existing `pkg/quantize/ts/` convention), with
sdkgen flags pointing at them (parallel to the existing `--core` / `--interp` flags).

## D. Emitted C# files & type mapping

`csharpBackend` emits into the target dir under one namespace (default `Mmokit.Sdk`,
overridable via a `--namespace` flag):

| TS file | C# file | Content (all schema-generated) |
|---|---|---|
| `entities.ts` | `Entities.cs` | one `struct`/`class` per entity kind, from `EntitySchema` |
| `delta-decoder.ts` | `DeltaDecoder.cs` | per-entity decode, from the same `writeFieldDecoder` schema walk |
| `entityType.ts` | `EntityType.cs` | `enum` of kind IDs |
| `broadcasts.ts` | `Events.cs` | server-event + broadcast classes with `static Decode(...)` |
| `inputs.ts` | `Inputs.cs` | client-input classes with `Encode()` |
| `operations.ts` | `Operations.cs` | op Req/Res classes (incl. `authLogin`/`authRegister`) |
| `client.ts` | `Client.cs` | **stateless** typed facade (see below) |
| `index.ts` | (n/a) | C# uses namespaces; no barrel file. Optionally emit an `.asmdef`. |
| `_core/*` | `_core/*.cs` | §C ports |

`Client.cs` is the headless, renderer-agnostic, **stateless** game-client facade
(per the Architecture amendment). It exposes:

- **Lifecycle:** `Connect(host, port)`, `AuthRegister(...)` / `AuthLogin(...)`,
  `Disconnect()`.
- **Inbound events (callbacks, decoded — no managed entity map):**
  `OnEntitySpawned`, `OnEntityUpdated`, `OnEntityDespawned` (each carrying the
  decoded entity + component fields), and `OnServerEvent<T>` per `RegisterEvent`
  type. The consumer accumulates these into its own state (Unity GameObjects / a
  bot's dictionary).
- **Outbound:** typed RPC/op call methods (over `RegisterOp`, incl.
  `authLogin`/`authRegister`) and client-input send methods (over `HandleClient`).
- **Interpolation:** exposes the `InterpolationCore` primitive for the consumer to
  feed samples into and query — the client does not own the entity ring itself.

It deliberately does **not** hold accumulated world state.

`EncodingToType` map: `f32`→`float`; `qvel`/`qangle`/`qnorm`→`float` (decoded
value); `u8`→`byte`; `u16`→`ushort`; `u32`→`uint`; `i16`→`short`; `bool`→`bool`;
`string`→`string`.

Optionally emit a `Mmokit.Sdk.asmdef` so Unity treats the SDK as its own assembly
(keeps compile times down and namespaces clean). Decision deferred to the plan;
default to emitting one.

## E. Windows deploy workflow

A dedicated justfile recipe (not folded into the default `just build`, because it
writes a Windows path that is absent in headless/CI environments):

```just
# generate the C# client SDK for 4node-basic into the Unity Assets tree.
# Override the target with UNITY_SDK_DIR (defaults to a /mnt/c path).
csharp-sdk:
    go run ./examples/4node-basic --dump-schema \
        "--postgres-url={{ env('POSTGRES_URL', 'postgres://mmo:mmo@localhost:5432/mmo_4node?sslmode=disable') }}" \
      | go run ./cmd/sdkgen --lang=csharp \
          --out "{{ env('UNITY_SDK_DIR', '<WINDOWS-HOME>/unitygames/spacemmo-client/Assets/Mmokit/Sdk') }}"
```

- Default target: `<WINDOWS-HOME>/unitygames/spacemmo-client/Assets/Mmokit/Sdk`
  (the WSL view of `C:\Users\<YOU>\unitygames\spacemmo-client\Assets`, SDK nested
  under `Mmokit/Sdk` so it sits in its own folder).
- `UNITY_SDK_DIR` overrides the target per machine.
- The generator writes straight to the target — no intermediate repo copy.
- A mirror recipe may be added in `examples/4node-basic/justfile` (`just csharp-sdk`)
  delegating to the root recipe, matching the existing `sdk` recipe pattern.

## F. Testing strategy

1. **Generator golden test** (`cmd/sdkgen`): golden-file test for the C# backend so
   schema changes surface as reviewable `.cs` diffs.
2. **UDP op-channel test** (`pkg/net`): assert an inbound `0x01` reliable frame lands
   in `DrainOpInput()` and a `0x00` frame in `DrainInput()`.
3. **Cross-language wire-safety test (golden bytes — DRY drift guard):**
   - A Go test emits canonical frames (a reliable handshake, an unreliable world
     delta, a quantized sample set) to golden byte files under `testdata/`.
   - A C# decode test (run via `dotnet test`) reads the **same** golden files and
     asserts the C# `_core` decodes them to the expected values, and re-encodes to
     identical bytes where applicable.
   - This cross-checks both languages against one source of truth and fails loudly
     if the hand-ported C# core drifts from `udpproto`/`quantize`.
4. **End-to-end smoke (manual/optional in CI):** a `dotnet`-run console harness using
   the generated SDK connects to a running 4node-basic server over UDP,
   authenticates via `authLogin`, and receives world frames — the C# analog of the
   Go bot. Delivered as inline instructions, not a committed `*_SMOKE.md`.

## Dependencies & sequencing

1. Server UDP op-channel fix (§B) — unblocks auth; small and self-contained; land first.
2. Generator backend seam (§A) — refactor with TS output unchanged (golden-verified).
3. C# `_core` ports (§C) + cross-language golden test (§F.3).
4. C# emitters (§D) + generator golden test (§F.1).
5. `csharp-sdk` deploy recipe (§E).
6. (User) Unity project consuming the SDK; e2e smoke (§F.4) validates the whole chain.

## Open items (resolve during planning, not blocking)

- Whether to emit a `.asmdef` (lean: yes).
- `--namespace` default (`Mmokit.Sdk`) — confirm preferred Unity namespace.
- Exact in-repo home for the C# ports (`pkg/net/cs/`, `pkg/quantize/cs/`) vs a single
  `sdk/_core-cs/` source dir.
