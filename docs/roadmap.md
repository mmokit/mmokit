# MMOKIT Roadmap and Vision

**Status:** Single source of truth for project direction
**Last verified against source:** 2026-07-26
**Companion:** [`architecture.md`](architecture.md) describes what exists today. This file describes where the project is going. Neither should restate the other.

---

## 1. What MMOKIT is, and who it is for

MMOKIT is a reusable, server-authoritative, horizontally-partitioned multiplayer game framework written in Go. It provides authority, dynamic cell partitioning, entity handoff between cells and hosts, interest-managed delta replication, and generated client SDKs as *infrastructure*, so that a game does not have to build any of it.

It is for developers building persistent multiplayer worlds who want those concerns solved beneath them rather than re-implemented as game features.

The repository ships a reference game (`internal/`, `cmd/server/`, `web-pixi/`) that exercises the framework end to end, plus two examples. Start with:

- [`pkg/mmokit/README.md`](../pkg/mmokit/README.md) — what building on MMOKIT actually feels like
- [`examples/simple/README.md`](../examples/simple/README.md) — the smallest runnable game
- [`examples/4node-basic/README.md`](../examples/4node-basic/README.md) — distributed roles, generated SDKs, services, WASM

This section owns the project's scope statement. Other documents link here rather than restating it.

---

## 2. Vision

### 2.1 Any game type, not one space game

MMOKIT should host any genre. Today that claim is under-evidenced: the reference game is a 2D space game, and the only structurally different game ever built on the framework lives on the `moita` branch, which predates `SystemBase`, `Stage`, the reflection codec, and pluggable services. Treat multi-genre support as a goal being actively worked toward, not a property already held.

### 2.2 2D and 3D both first-class

The framework must support 2D and 3D games equally well. A 3D game must not feel like a 2D framework with a Z axis bolted on, and a 2D game must not pay for 3D in wire bytes, CPU, or API awkwardness.

This covers all vectors, collision, orientation, raycast, movement, area-of-interest, and all client SDK cores. The blast radius is concrete:

| Area | Current 2D assumption |
| --- | --- |
| [`pkg/component/core.go`](../pkg/component/core.go) | `Position`/`Velocity` are `X, Y float32`; `Rotation` is a scalar radian angle |
| [`pkg/quantize/quantize.go`](../pkg/quantize/quantize.go) | Orientation quantizes to a single `uint16` angle |
| [`pkg/spatial/`](../pkg/spatial/) | `Entry` carries X/Y only; raycast takes `Vec2`; narrow phase is circle and oriented-rect |
| [`pkg/system/`](../pkg/system/) | Movement derives heading from `atan2`; physics integrates two axes |
| [`pkg/universe/`](../pkg/universe/) | Border replication and transfer frames carry two position and two velocity fields |

**Cost: approximately 104 engineer-days** (band 95–120). See [section 7](#7-the-2d3d-and-multi-genre-program) for the phased breakdown and the sequencing rule.

### 2.3 The reference game stays 2D

Making 3D first-class does not mean converting the space game. `internal/` and `web-pixi/` remain the 2D proof; a separate 3D example proves the other profile.

---

## 3. Non-goals

These are permanent boundaries, recorded here so they stop being re-litigated.

1. **Cell partitioning stays horizontal-only.** Cells partition the ground plane and are effectively infinite vertical columns. Verticality is *simulated*, never *partitioned*. `CellID` remains `{X, Y, Depth}` with four children and eight neighbours. Volumetric partitioning was costed at roughly 55 additional engineer-days and explicitly declined; revisit only with a measured load case that horizontal splitting cannot serve.
2. **Clients stay topology-agnostic** and receive absolute world coordinates. Cells are a server-internal concern.
3. **The coordinator is a control plane**, never a per-tick payload relay. Gameplay and replication traffic flow directly between gateways, hosts, and services.
4. **No wire backward compatibility** across registered-type moves or renames. Wire IDs derive from package-qualified Go type names; moving a type is a breaking protocol change by design.
5. **Not core's job:** client rendering, art pipeline, navmesh generation, dynamic rigid-body physics solving, and character controllers (slopes, step-up, crouch, air control). The framework provides broad-phase, queries, and static push-out; games bring their own solver. See [section 7.3](#73-collision-scope).

---

## 4. Design principles

1. **Preserve the shape that works.** Phase-ordered ECS execution, explicit authority epochs, spatial interest management, and delta replication are the framework's load-bearing design. Correctness at lifecycle and delivery boundaries comes before an ECS rewrite or broad performance tuning.
2. **One authoritative pipeline per concern.** Every path that destroys an entity performs identical bookkeeping; every path that commits a baseline observes the same delivery guarantee. CE-001 is the worked example.
3. **Every fact has exactly one owning document.** Others link. `architecture.md` describes what *is*; this file describes what *will be*. Never state direction in `architecture.md`.
4. **Status is derived from source, never from plan checkboxes.** All nine C# SDK plans record zero completed tasks while all nine shipped. The world-editor plan reads 0 of 106 while the feature is live in the admin dashboard. Checkboxes lie; `rg` does not.
5. **Retiring a tracker requires migrating its open items first.** The July 2026 retirement of the previous roadmap silently dropped five tracked items, recovered here in [section 6.6](#66-workstreams-not-covered-by-a-ce-item).

---

## 5. What exists today

A short factual inventory, doubling as the "do not rebuild this" list. Detail lives in [`architecture.md`](architecture.md).

- Distributed process roles (coordinator, host, gateway, service) with dynamic cell split, merge, and migrate, and epoch-gated entity handoff
- Interest-managed delta replication with quantized snapshots and per-connection baselines
- TypeScript and C# SDK generation from registered Go types (`cmd/sdkgen`), plus a C# smoke bot
- WASM hot-swappable systems: Phase 0 and the declarative lifecycle, ABI v2
- The world editor, admin dashboard, and typed `cmdsys` verbs backing both console and dashboard
- An auth service over HTTPS, client-facing transport TLS, and a WebSocket origin allowlist (security Unit 1, merged in `e918d83`)

---

## 6. Roadmap

### 6.1 How to read this section

Every status below was verified against source on the date at the top of this file. Statuses carry a `file:line` citation. **Re-verify before republishing** — line citations drift, and the previous roadmap shipped with evidence links pointing at unrelated code within three weeks.

| Status | Meaning |
| --- | --- |
| Open | No acceptance criteria met |
| Partial | Some criteria met; the remainder is stated explicitly |
| Done | All criteria met and verified in source |

### 6.2 Working-tree triage — **done**

The uncommitted work this section used to describe is committed. CE-001, CE-003, CE-004 and CE-005a are closed; CE-005b Tier 1 is closed with Tier 2 open; and the client-prediction workstream is documented as WS-001 below rather than existing only on disk. A roadmap read against `HEAD` is now accurate.

### 6.3 P0 — must close before the 2D/3D program begins

#### CE-002 — Bounded decoding and ingress budgets · **Open (0/8)** · highest priority

The reflection decoder performs unchecked reads and trusts wire-supplied lengths. [`pkg/universe/reflect_marshal.go:337`](../pkg/universe/reflect_marshal.go) slices `data[off:off+slen]` on an attacker-controlled `uint16`, and `:347-351` calls `make([]byte, n)` on an attacker-controlled `uint32`.

This is worse than previously recorded. The path is reachable **pre-authentication** — [`pkg/universe/gateway.go:1054`](../pkg/universe/gateway.go) gates only on route kind and explicitly tolerates a nil session — inside a per-connection goroutine with **no `recover`**. An unrecovered panic there aborts the process. Treat this as an **unauthenticated remote denial of service**, not a robustness nit. The `make([]byte, n)` path cannot be mitigated by `recover` at all: a large enough length produces Go's unrecoverable out-of-memory fatal error.

Acceptance criteria unchanged from the previous roadmap: a checked decoder returning consumed bytes and an error; bounds checks before every read; configurable limits on frame size, strings, slices, nesting, and aggregate allocation; rejection of truncated and trailing data; per-connection queue and per-tick work caps across WebSocket, UDP, and virtual connections; codec fuzzing; and bounded-cardinality rejection metrics. No fuzz target exists anywhere in the repository today.

#### CE-004 — Destination acceptance before cross-host demotion · **Done (7/7)**

Failure propagation landed earlier: [`pkg/universe/grpc_bridge.go:80`](../pkg/universe/grpc_bridge.go) `sendViaGrpc` returns a status on every remote failure path, with rollback in `handoff_driver.go:562-573`.

**The acceptance protocol now exists.** A destination cell answers every `MsgHandoff` with a `MsgHandoffAccepted`, and that acceptance — not the local enqueue — is what arms the source's demote.

- Transport: `MsgHandoffAccepted = 102` and `HandoffAckPayload` in [`pkg/universe/message.go`](../pkg/universe/message.go), tunnelled on the wire through the existing `meshpb.CrossCellAction` oneof under the engine-reserved `ActionHandoffAccepted = 101` ([`pkg/universe/handoff_ack.go`](../pkg/universe/handoff_ack.go), intercepted on both sides of `mesh_frame_codec.go`). **No meshpb change**, so this is not a lockstep cluster redeploy; `ActionTypedMessage = 100` is the existing precedent.
- Sequencing: `handleCrossing` records an `inflightHandoff` before the send and queues nothing on success ([`pkg/universe/handoff_driver.go:562`](../pkg/universe/handoff_driver.go)). Only `OnHandoffAccepted` (`:597`) moves the entry into `pendingDemotes` and records the anti-thrash cooldown, so `OnPlayerTransfer`, the gateway upstream switch, `StateTransferring` and `RemoveTransferred` cannot fire until the destination has accepted. `retryInflight` (`:648`) re-sends the identical payload every `HandoffAcceptRetryTicks` (4) and escalates once at `HandoffAcceptWarnAttempts` (5).
- Deduplication: `Stage.handoffAccepted` (netID → highest accepted epoch, [`pkg/universe/stage.go:144`](../pkg/universe/stage.go)) keyed at the promotion entry point `Cell.processMessage case MsgHandoff` (`cell.go:473`). A duplicate is re-acked and otherwise ignored, which also stops the eager session pre-register from re-running. The mark is forgotten in `DemoteLiveToReplica` and nowhere else.
- Abandonment: a handoff is given up **only** when `SendHandoff` returns false on a retry — the one condition under which the destination provably cannot have promoted. The epoch is never rolled back on an accept timeout, because border frames carrying the bumped epoch have already shipped and rewinding would make every neighbour reject the entity permanently.

**Residual, deliberately accepted:** if the destination receives the Handoff but its ack is lost, the destination promotes at `CommitTick` while the source stays Live until a retry ack lands. This is a bounded, self-healing **double**-authority window replacing a permanent **zero**-authority orphan; the destination already drops the source's border frames for a netID whose local slot is Live. Driving it to exactly zero requires the destination to gate its promote on a source-sent Commit — a three-phase protocol, filed as a separate item. The `(NetID, Epoch)` dedup and the ack plumbing landed here are its prerequisites.

**Control-stream fencing landed.** The six parallel maps (stream / send-mutex / kill-channel, per side) collapsed into one `controlStream` record per handler invocation, carrying its own send mutex, kill channel, and generation stamp ([`pkg/universe/mesh_control_server.go`](../pkg/universe/mesh_control_server.go)). Teardown is now gated on pointer identity via `releaseHostStream` / `releaseGatewayStream`: a superseded handler skips the **entire** teardown, not just the map delete, so `MarkDead`, `UnregisterByHost`, `serviceEventRouter.RemoveProcess`, `reassignOrphanedCells`, and `sessionRoutes.RemoveByGateway` can no longer fire against a freshly reconnected registration. Registration also evicts its predecessor deterministically by closing that record's kill channel instead of only logging, and the resulting log line and error are worded so a reconnect eviction is not read as an operator `host kill`.

**Reconnect replay closed at three levels.** Cells were already replayed (`reannounceOwnedCells`); added:

- Ownership arbitration on `CellReady` (`mesh_control_server.go`, host drain loop). A re-announce for a cell a *different live* host now owns is rejected and answered with the already-wired `CellRelease`, instead of producing a nondeterministic two-owner registry (`AssignCell` only adds; `HostForCell` is a map-order scan). Accepted normally when the current owner is `RemoteHostDead`, so crash recovery is unaffected.
- Gateway session replay: `meshGatewayClient.reannounceSessions` over the new `Gateway.snapshotSessions`, which copies under `g.mu` with the same discipline as `lookupSession`. Without it a gateway↔coord blip left `coord.sessionRoutes` empty while the gateway still held live WebSocket sessions, so client input routed nowhere after the next migrate.
- Service and bus-subscription replay: `reannounceServices` on both the host and gateway control clients, reusing the existing re-callable `Process.announceServices` and `sendServiceEventSubscribe`. Known benign wart: `service.CoordRegistry.Register` rejects a duplicate InstanceID, so re-announcing to a coordinator that still holds the entry logs a rejection. A `CoordRegistry.Upsert` is the clean fix and is not blocking.

Deliberately **not** done: preserving `HostRegistry.Register`'s `OwnedCells` across reconnects (restores ownership with no arbitration — the same two-owner bug from the other direction), and a versioned coordinator→host desired-state reconciliation (design item; the arbitration above is its safety-critical slice).

#### CE-005b — UDP security and gating · **Tier 1 done; Tier 2 open**

Tier 1 landed. [`pkg/net/udp_server.go`](../pkg/net/udp_server.go) now routes every data packet through a single identity chokepoint, `routeFor`, which requires the packet's source address to match the address its session is bound to. A token is no longer a bearer credential: replaying one from another address neither injects input nor tears the session down, and a mismatch is dropped silently rather than answered, so the server cannot be used to confirm a guessed token or as a reflector.

Connection requests no longer allocate. An unauthenticated request now records only a `pendingHandshake`; the transport and its goroutine are created only when a data packet arrives carrying the token for that same source address, which proves return routability. Both the pending table and the session table are bounded, pending entries expire, and drop counters are exposed with rate-limited logging so the log itself cannot be used for amplification.

**UDP is now off by default.** `--udp-listen` defaults to empty and logs an explicit experimental warning when enabled, escalated for a non-loopback bind. Nine regression tests in `pkg/net/udp_server_test.go` cover spoofed data, spoofed disconnect, foreign-token promotion, handshake idempotence, and both caps; the package is race-clean.

Tier 2 remains open: real cryptographic connection identity (authenticated handshake and AEAD framing) so that an *on-path* attacker cannot read or forge traffic, and so address rebinding for roaming clients can be supported safely. That is the same work as WS-002 Unit 2 and depends on WS-003 delivering a UDP session key over HTTPS — track it there, not twice.

#### CE-006 — Mesh authentication and authorization · **Open (0/7)**

Mesh gRPC still uses `insecure.NewCredentials()` with live TODOs in [`pkg/universe/host_network.go:229-236`](../pkg/universe/host_network.go), `mesh_control_client.go:191-196`, and `mesh_gateway_client.go:136-138`. Identity is still read from the message payload (`mesh_control_server.go:86`, `:481`). No interceptor, peer-certificate inspection, or metadata authentication exists.

**The June 2026 TLS work did not touch mesh code.** `pkg/universe/tls_config.go` scopes itself to client-facing HTTP listeners. Do not read Unit 1 as partially closing this item.

**The mechanism is restated.** The previous roadmap prescribed cluster-CA mTLS with identity derived from peer certificates. That contradicts the locked security decision, which chose shared-secret join authentication in gRPC metadata plus ephemeral in-memory self-signed TLS, and named CA-based mTLS an explicit non-goal. The *risk* is fully open; the *mechanism* is shared-secret. Acceptance criteria should be rewritten against that model before work starts.

#### OSS-001 — Open-source readiness and CI · **Open** · cheap, and blocking

There is no `LICENSE`, no `CONTRIBUTING.md`, no `.github/` directory, and no CI of any kind. This makes four of the six cross-cutting verification gates in [section 6.7](#67-cross-cutting-verification-gates) unimplementable. It is also incoherent with a stated goal of letting other developers build games on this framework.

### 6.4 P1 — quality and protocol

#### CE-010 — Process isolation and iteration consistency · **Partial** · promoted from P2, gates the 2D/3D program

Escape hatches exist (`IncludeAll`, `Without` in `pkg/query/query.go`), but `ForEach1/2/3` in [`pkg/mmokit/queries.go:41-71`](../pkg/mmokit/queries.go) still build raw unfiltered filters, and WASM systems both read and overwrite neighbour-owned Replica and Ghost components (`wasm_system.go:186`, `:207`).

Process isolation is untouched: `var CellSize float32 = 8192.0` remains a mutable package global in [`pkg/coords/coords.go:8`](../pkg/coords/coords.go), and four package-global wire registries exist with `ResetXxxForTest` helpers that exist only to unwind them.

**This is the load-bearing prerequisite for 3D.** A per-process dimension profile *is* the process-owned immutable registry this item describes. Doing CE-010 first pre-pays a meaningful fraction of the 2D/3D work.

#### CE-009 — Protocol version and schema fingerprint · **Open** · hard prerequisite for 3D

No version or fingerprint is negotiated at connection setup. The only handshake message carries a single tick-rate field; the frame header has no version byte; the generated schema has no version.

Collision auditing is worse than previously recorded. `RegisterEvent` and `RegisterOp` panic on duplicate type IDs, but `registerClientInputType` ([`pkg/mmokit/handle_client.go:55-61`](../pkg/mmokit/handle_client.go)) **silently overwrites**, and `RegisterBroadcastType` never computes a type ID at all. Two of four registries are unguarded.

Without this, a 2D client connecting to a 3D server decodes valid bytes into the wrong shape instead of being rejected.

#### CE-003 residual — Datagram frame ACKs · **Done (8/8)**

Earlier work landed typed delivery outcomes (`pkg/net/send_result.go`), a frame writer returning a result, baseline commit gated on delivery class, surfaced WebSocket backpressure, and the border re-entry fix (`border_viewer.go`). The ACK machinery existed but was **dead code**: `DefaultReplicationConfig` never set an ACK mode, so every `ReplicationSystem` ran with the zero value `AckReliable`, and `AckFrame`/`AckSequence` were called only from tests.

For a UDP client that meant `SendUnreliable` → `DeliveryBestEffort` → neither the `AckReliable` commit test nor the receipt-tracking test passed → the backpressure branch set `forceFresh`. **Every UDP frame was a complete FreshSnapshot, forever.** State was correct; bandwidth was worst-case and the entire delta pipeline was bypassed.

What shipped:

- **Per-connection ACK mode latched from the transport's static class.** `net.DeliveryClassProvider` (implemented by `WSTransport` → `DeliveryReliableOrdered`, `UDPTransport` → `DeliveryBestEffort`), `ConnManager.DeliveryClassFor`, `ReplicationConfig.AckModeFor`, and a `mode` field on `connState` resolved once in `getConn` and preserved across stream resets. It cannot be a single scalar: one `ConnManager` holds a mixed map of WebSocket and UDP transports, so two viewers of the same cell legitimately differ. It is deliberately driven from the transport TYPE, never from a `SendResult` or any mutable state, so the generated wire schema stays constant.
- **The ACK wire type and its routing.** `mmokit.ReplicationAck{StreamEpoch, Seq}`, registered as a client input by the engine-default `HandleClient` block in `pkg/mmokit/init.go` exactly the way `mmokit.Ping` is. Routing goes through a per-cell sink on `engine.Engine` (`SetReplicationAck` / `AckReplicationFrame`) that `NewReplicationSystem` installs via `ReplicationConfig.RegisterAck` — so `internal/game` and every other construction site pick it up with **zero** game-code changes. The typed-client-input phase runs before the system loop, so an ACK arriving before tick N commits before tick N's frame is built: zero added latency.
- **Deterministic loss/reorder/duplicate harness** in `pkg/system/lossy_link_test.go` + `replication_lossy_test.go`. `pkg/universe/loopback_bridge.go` was evaluated and is unsuitable: wrong domain (it routes `CellMessage`, with no path from a frame writer into it), an import cycle (`pkg/universe` imports `pkg/system`), one constant latency so it can never reorder, no duplicate injection, and unjoinable wall-clock goroutines. The new link encodes real `quantize` wire bytes, returns exactly what `UDPTransport.SendUnreliable` returns, and drives a reference client decoder mirroring the generated TS/C# accept gate. Seven end-to-end properties plus a mixed-transport per-connection latch test.
- `SentHistoryDepth` default 32 → 4. Under the one-attempt-in-flight invariant the ring never holds more than one live entry, while `GetOrCreateBaseline` eagerly allocates `ringDepth * sizeof(SentSnapshot)` per entity per viewer.
- **Distributed completion.** On a host, `eng.ConnMgr` is a `VirtualConnManager` that cannot see the client's transport, so the connection starts reliable. `host_network.go` now emits a mesh replication receipt for every *successful* enqueue rather than only reliable-ordered ones — the payload already carried the achieved `DeliveryClass`, so this needed no meshpb change — and `drainFrameReceipts` latches the connection to `AckExplicit` on the first receipt reporting a class below reliable-ordered, replacing the whole `connState` (the `BaselineStore` is constructed from the mode) and forcing one fresh frame. Previously a UDP client behind a separate gateway process got no receipt at all, so the host's attempt timed out every `PendingReceiptTimeoutTicks`: a full snapshot every third tick with two dead ticks between. Degrades safely against an old gateway — it simply stays on today's behaviour.

**Validated live (2026-07-27).** `just csharp-smoke` against a DISTRIBUTED 4node cluster, 500 bots, `cell_0_0` already split before the client connected:

- **239 of 241 frames were deltas** (`fresh=False`), `updatedTotal=82956`. Before this work every UDP frame was a `FreshSnapshot` forever — this is the bandwidth cliff actually gone, not just the code path being reachable.
- **The two leading fresh frames are the distributed latch, at its predicted cost.** On a host the `ConnMgr` is a `VirtualConnManager` that cannot see the client transport, so the connection starts `AckReliable`; the gateway's receipt reports `DeliveryBestEffort` and `drainFrameReceipts` swaps in an `AckExplicit` state with `forceFresh`. That is the documented "exactly one wasted frame per connection, self-healing" cost, observed.
- **241 frames across ticks 101→340 is ~1 frame per tick**, confirming no frame-rate loss when RTT ≤ tickInterval, and confirming the generated C# auto-ack is firing — without it every frame would stall `PendingAckTimeoutTicks` and then go fresh.

Not yet covered by that run: high-RTT behaviour (the `ceil(RTT/tickInterval)` reduction below is still unmeasured on a real network) and recovery on a genuinely lossy link — loopback does not drop, so the loss paths remain proven only by `replication_lossy_test.go`.

**Residual risk, measured not guessed:** while an attempt is in flight the per-viewer loop skips emission, so a datagram viewer's AoI rate is the tick rate when RTT ≤ tickInterval and `ceil(RTT/tickInterval)` ticks otherwise. Bandwidth improves enormously; worst-case latency does not. `OnBeforeSend`/`OnAfterSend` still fire on skipped ticks, so per-tick own-state events and client prediction are unaffected. The real fix is pipelined delta replication (filed as CE-003b) — that needs a `deltaFromSeq` field in the 20-byte header and a per-seq snapshot ring in both client cores, i.e. a wire break, and it invalidates the C# ack-on-receipt shortcut.

#### CE-005a — UDP reliability · **Done** (working tree)

Retransmission encodes the stored sequence, ACK handling requires sequence identity, unacknowledged-slot writes return backpressure, duplicates are suppressed by a receive window, and clock handling is monotonic. Mirrored in the Go and C# clients. Close this half; keep CE-005b open.

#### CE-007 — Replication bandwidth scheduling · **Open**

Verifiably inert: the priority accumulator is written on skip and zeroed on send, and read nowhere in non-test code. `pkg/replication/priority.go` has zero production callers. No per-viewer byte or entity budget exists. Slightly more urgent now that a rejected frame forces a full snapshot.

#### CE-008 — Tick timing and loop-job lifecycle · **Open** · cheapest item in the band

[`pkg/engine/loop.go:148`](../pkg/engine/loop.go) still truncates to whole milliseconds, so a configured 60 Hz schedules 16 ms ticks — 62.5 Hz — while the coordinator uses an exact fraction elsewhere. `RunOnLoop` enqueues with no running-loop check, a timed-out caller's job still executes, and `ErrLoopStopped` is dead code proving the shutdown drain was never built. There is no test file for it.

### 6.5 P2

#### CE-011 — Allocation cost and identifier capacity · **Open** · split it

The NetID half is higher risk than P2 implies and should be raised: the engine never stores its granted range size, so a busy cell silently walks into the neighbouring cell's range with no metric. The allocation half is genuinely P2 and is blocked on benchmarks that do not exist — the whole `pkg/` tree contains three benchmark functions, none for replication.

### 6.6 Workstreams not covered by a CE item

These exist in the working tree, in dated plans, or only in git history. They are recorded here so they stop being invisible.

| ID | Workstream | Status | Where it lives today |
| --- | --- | --- | --- |
| WS-001 | **Client prediction, reconciliation, adaptive playback** | **Partial — see [WS-001](#ws-001--client-prediction-reconciliation-and-adaptive-playback--partial) below** | Implemented, committed, and documented. Reverses a previously executed decision to remove client prediction. |
| WS-002 | Security Units 2–4 (UDP AEAD framing, mesh shared-secret auth, app hardening) | Units 2–3 not started; Unit 4 partial | Umbrella spec records all three as "to be written". Overlaps CE-005b and CE-006 — reconcile, do not track twice. |
| WS-003 | Auth over HTTPS with OIDC social login | HTTPS endpoints exist; OIDC is a schema seam only | Buried in a spec amendment. Gates WS-002 Unit 2. No `/auth/udp-key` route exists. |
| WS-004 | WASM systems Phases 1–3 | Phase 0 shipped; Phase 1 entirely unbuilt | `pkg/wasmabi` declares no host imports, so commands, multi-component queries, and the query manifest do not exist. Live perf TODO: the module recompiles on every load. |
| WS-005 | C# SDK / Unity client remainder | All nine SDK plans shipped; four scope items open | The Unity demo project lives outside this repository. The RPC ergonomics layer was promised as a follow-on spec and never written. |
| WS-006 | Async entity serialization | Open | Recovered from the retired roadmap. Network system is 15–25 ms of a 50 ms tick budget. CE-011 covers allocation, not moving frame construction off the loop goroutine. |
| WS-007 | Gateway session tokens for transparent crash recovery | Open | Recovered from the retired roadmap; also carved out of the security umbrella as "tracked separately", which was true of nowhere. |
| WS-008 | Rich network entity identity | Open | Recovered from the retired roadmap. `NetworkID` still carries only ID and epoch. |
| WS-009 | Auto-rebalance tuning and load-based placement | Open, ships disabled | `AutoRebalance` defaults to false. |
| WS-010 | Persistence schema-evolution tooling | Open | Migrations run embedded at startup; no rolling-migration story. |
| WS-011 | Second reference game (`moita` branch) | Stranded on a branch | The only empirical evidence a structurally different genre fits. Either port it as validation of the multi-genre goal or formally retire it. |

#### WS-001 — Client prediction, reconciliation, and adaptive playback · **Partial**

Server-authoritative local movement prediction for the reference game's ship, with cumulative input acknowledgement and an adaptive interpolation timeline. Implemented and committed; the closed loop is:

`web-pixi/src/input.ts` (sequence + buffer the command) → `internal/game/input_handlers.go` (`consumeMoveTargetInput` marks a non-zero sequence PROCESSED even when it rejects the target, so a client can retire a poison command instead of retrying forever) → `internal/game/system_network.go` (`ProcessedInputSeq` reads the authoritative `MoveTarget.Sequence`) → `pkg/system/replication.go` (attaches it to the frame) → `pkg/quantize/wireformat.go` (`FrameFlagInputAck` plus a four-byte trailer) → generated SDK decoders → `web-pixi/src/movement-reconciliation.ts` (pairs the owner seed with its accepted frame) → `web-pixi/src/main.ts` (prediction runs AFTER interpolation and overrides only `renderX/renderY/renderRot`).

Shipped, and now enforced rather than asserted:

- **Go↔TS ship-dynamics parity.** `just shipdyn-golden` drives the REAL `ShipDynamicsSystem` + `PhysicsSystem` through a real Stage over six scenarios for eight fixed ticks, and `web-pixi/src/__tests__/prediction-golden.test.ts` replays each tick through `projectShipPrediction`. This is the workstream's only continuously-triggered drift risk — ship dynamics get tuned routinely, and divergence used to surface in production as constant rubber-banding rather than as a failing build.
- **Cross-language goldens** for `AdaptivePlaybackController`, `PredictionBuffer`, and the `FrameFlagInputAck` trailer, replayed from one Go-produced manifest by both `pkg/quantize/ts/playback-golden.test.ts` and `csharp/Mmokit.Sdk.Core.Tests/PlaybackGoldenTests.cs`. Closes the "cross-language golden vectors" gate in §6.7 for these cores.
- **The reconciliation gate is shared, not game-specific.** `pkg/quantize/ts/reconciliation-gate.ts` and `csharp/Mmokit.Sdk.Core/ReconciliationGate.cs`, both in sdkgen's CoreFiles; `web-pixi`'s class is a game-typed alias. A Unity client no longer has to reinvent the most race-prone part of reconciliation. The C# port carries explicit insertion-order lists because `Dictionary` does not reproduce the JS `Map` ordering the TS reference relies on for oldest-first eviction — covered by an explicit eviction-order test on both sides.
- **PredictionBuffer surface parity in both directions**, so the two SDK cores are one contract a consumer can follow line for line.
- **Observability and an operator knob.** Movement commands the server did not apply are logged behind the new game-range `input` debug flag; `buildMovementState` says so once per entity when a missing component silently disables prediction; two unlabelled `CellMetrics` counters cover the ACK loop. `GameConfig.MovementPredictionHorizonMs` replaces a hard const, with 0 meaning prediction disabled.
- **The TS suites are actually run.** `just web-test` and `just client-test` cover `web-pixi/src/__tests__/` and `examples/4node-basic/web/src/__tests__/`, which no recipe ran before — the largest body of WS-001 tests was invisible to the standard validation sweep.

Known and deliberate:

- **The rotation feedback path is real but unreachable for the predicted entity.** `entityRotation` falls back to the live `renderRot` for an entity with no `angle` field that is not moving, so a predicted rotation could in principle re-enter the interpolation ring. It cannot for the local ship, because `ShipEntity` declares `angle` and prediction only ever writes `state.myEntityId`. `web-pixi/src/__tests__/interpolation.test.ts` pins both halves, so dropping `angle` from the ship schema fails there instead of shipping geometric convergence into the interpolator.
- **The input-ack trailer costs 4 bytes per frame** to every player with a non-zero `MoveTarget.Sequence`, ungated by change detection — about 80 B/s/player at 20 Hz. Negligible now; it interacts with CE-007 and should not be optimized independently of it.

Open, and the reason this is Partial rather than Done:

- No acceptance criteria exist for prediction *quality* — divergence budgets, correction magnitude distribution, or a rubber-banding metric. Everything above proves the client reproduces the server; nothing states how far apart they may drift before it is a defect.
- Prediction covers ship movement only. Abilities, docking, and supercruise channeling deliberately wait for authority (`movementPredictionTicks` returns 0 for `SupercruiseChanneling`), and no plan exists for extending it.
- `gamecomp.PlayerInput.Sequence` is still mirrored by `input_handlers.go` and read by nothing authoritative. Removing it is a separate cleanup with its own blast radius.

### 6.7 Cross-cutting verification gates

Each work item should add the smallest regression test that fails before the fix. These gates belong alongside P0, not after it — **all four marked below are blocked on OSS-001**, since no CI exists.

- [ ] Race-enabled tests for engine scheduling, transports, connection teardown, and mesh reconnects *(blocked on CI)*
- [ ] Fuzz targets with retained corpora for reflection codecs, operation and input frames, and mesh frame decoders *(blocked on CI; zero fuzz targets exist today)*
- [x] Deterministic simulated loss/reorder/duplicate harnesses — landed as `pkg/system/lossy_link_test.go`. **Do not** build on `pkg/universe/loopback_bridge.go` as previously recommended here: it routes `CellMessage` (no path from a frame writer into it), `pkg/universe` imports `pkg/system` so the import cycle forbids it, it applies one constant latency so it can never reorder, and it has no duplicate injection
- [ ] Linux integration jobs that permit localhost TCP and UDP listeners *(blocked on CI)*
- [ ] Generated-schema diff checks and cross-language Go/TypeScript/C# golden vectors *(blocked on CI)*
- [ ] Load tests asserting bounded memory, queue depth, tick work, and recovery after backpressure

One known pre-existing race remains under `-race`; the second is fixed. Quarantining or fixing the remaining one makes every other failure attributable.

1. **`ensureBorderDispatcher` vs `applyPeerList`** in the cell bridge.
2. ~~**`Cell.MeshID` during a rename.**~~ **Fixed.** A cell's `(MeshID, CellID)` pair is now one immutable `cellIdentity` record behind an `atomic.Pointer`, read through `Cell.MeshID()` / `Cell.CellID()` / `Cell.Identity()` and replaced only by `setIdentity` on the rename path. The mutable exported fields are gone, so the whole class of unsynchronized off-loop reads is closed rather than just the one the detector found. `Host.CellByID` additionally stopped scanning on the identity and now resolves through the map key (`ParseCellID` + lookup), which is both race-free and O(1) instead of O(cells) on a path the mesh data plane hits per inbound frame. Note that a single-load `Identity()` is the only way to get both halves consistently — two separate accessor calls may legitimately straddle a rename, which `TestCellIdentity_SeparateAccessorsMayStraddleARename` documents.

Also note: `pkg/universe` intermittently reports `executor: serialize timeout on cell_0_0` ([`cell_transfer_executor.go:239`](../pkg/universe/cell_transfer_executor.go)) under PARALLEL package execution. That one is CPU contention on a `RunOnLoop` deadline, not a logic race — it reproduces roughly 1 run in 4 with default `-p` and 0 in 4 when the package runs alone. Always use `-p 1` for any run that includes `pkg/universe`, or the flake gets misattributed.

---

## 7. The 2D/3D and multi-genre program

**Approximately 104 engineer-days** (band 95–120; absolute range 85 if collision is cut hard, 140 if three overruns land together).

### 7.1 Sequencing rule

**No work in this section begins until every P0 item in [section 6.3](#63-p0--must-close-before-the-2d3d-program-begins) is closed and verified against source.** This rule is recorded here so the ordering survives independently of whoever decided it.

### 7.2 Prerequisite gates

Two P1 items are hard prerequisites and should be scheduled immediately after P0:

- **CE-010** — a per-process dimension profile requires the process-owned immutable registries and injected cell geometry this item describes. `coords.CellSize` cannot remain a mutable package global.
- **CE-009** — without version and fingerprint negotiation, a 2D client meeting a 3D server decodes valid bytes into the wrong shape rather than being rejected.

### 7.3 Architecture

**One 3D-native component set, with a process-scoped profile that selects behaviour — bindings, systems, narrow phase, validators — at construction time but never selects types.**

The alternative, sibling `Position3`-style types, is rejected for a concrete reason: framework code that statically names `component.Position` (the `ecs.NewFilterN` sites in `cell_transfer_executor.go`, `border_replication.go`, `viewer_source.go`, plus five query-bundle sites) would silently match zero entities in a 3D world. A 3D cell split would serialize nothing, with no error, and none of the five integrity invariants would catch it because all five assert map consistency over cell identifiers rather than entity conservation. A single type set makes that failure structurally impossible rather than lint-guarded.

Two enabling facts, both verified: the ark ECS library exposes a full reflection-based runtime API already used as the dominant idiom here, so construction-time dimension selection works. But the wire codec's field walker is flat and tag-driven with no struct case, so nested vector value types would contribute zero wire fields until that walker is made recursive.

### 7.4 Collision scope

Collision is **new capability, not a port** — the existing separating-axis code in `pkg/spatial` has zero production callers, and the reference game hand-rolls its own contact routines for a single interaction. Recommended scope, roughly 34 days:

**Ship:** broad-phase with a layer matrix; narrow-phase boolean tests over sphere, box, and capsule; contact manifolds for sphere and capsule; static push-out and trigger enter/stay/exit; swept spheres for projectiles; 3D raycast and line-of-sight.

**Do not ship:** dynamic rigid-body response and triangle-mesh colliders. This is structural, not squeamish — entities near a cell boundary exist on the neighbouring host as dead-reckoned, quantized, up-to-one-tick-stale replicas, so a solver resolves against a lossy approximation with a different neighbour set on each side of the seam and the two hosts disagree continuously. `QueryCollisions` also ranges a Go map directly, and Go randomizes map iteration order per range.

**Out of scope entirely:** navmesh generation. `pkg/pathfinding` has zero importers in `pkg/`. Ship a `NavProvider` interface and a generic path-following system so games plug in their own navigation.

### 7.5 Phases

| # | Phase | Days | Ends runnable at |
| --- | --- | ---: | --- |
| 0 | Codec collapse and safety net (pure 2D, zero behaviour change) | 13 | Fixed-offset frame codecs rewritten on the reflection codec; byte-level wire goldens, which do not exist today; an entity-conservation integrity invariant. Full suite green, reference game unchanged. |
| 1 | Widen core types, still 2D only | 13 | Position and velocity gain Z; rotation becomes a quaternion with yaw helpers; collider becomes a shape union. Generated 2D SDK diff is empty. |
| 2 | The 3D profile | 12 | Quaternion quantization, dimension-selected bindings, gravity and move modes, cluster dimension agreement. A headless 3D example survives a cell split with a non-zero destination entity count asserted. |
| 3 | Client SDK and interpolation | 11 | Quaternion decode and slerp in TypeScript and C#, golden vectors. A browser client renders 3D with quaternion orientation. |
| 4 | Collision | 34 | Capsule characters against static boxes, line-of-sight gating, non-tunneling projectiles. |
| 5 | Parity fixtures and hardening | 11 | Both profiles covered in one test binary at the pre-existing race baseline. |

Phases 0 and 1 are deliberately pure 2D. Front-loading them means any later failure on the split-merge path is unambiguously a 3D bug rather than a byte-offset bug.

### 7.6 Validation

Port or formally retire the `moita` branch. It is the only empirical evidence that a structurally different genre fits this framework, and the multi-genre claim in [section 2.1](#21-any-game-type-not-one-space-game) is untested without it.

### 7.7 Known gameplay-visible change

The spatial query radius is currently under-expanded, and the reference game compensates with a hand-tuned terrain margin documented as covering for a query that "missed every wall". Fixing the query — required for capsules — makes currently-missed walls start being hit. Budget deliberate re-tuning with playtesting. This is the only part of the program where "the 2D game keeps working" needs human judgement rather than a green test suite.

---

## 8. Superseded, closed, and deliberately not doing

Recorded so the next reader does not resurrect dead work.

| Item | Disposition |
| --- | --- |
| **CE-001 — authoritative entity removal** | **Closed.** All criteria met; `Commands.Despawn` routes through a single removal primitive. The two criteria that previously had no direct test now do: `TestCommands_DespawnVisibleToNextSystem` (a command despawn in system N is dead in system N+1, replacing a `t.Skip`) and `TestNewNetworkSystem_CommandDespawnPublishesRemovedNotExited` (the command-buffer path emits a Removed tombstone, not an AoI Exited). Its problem statement is false and must not be carried forward. |
| **Co-simulation / overlap handoff** | **Deleted**, not deferred. The implementing files no longer exist. Some dated plans and older notes still describe it as merely unwired — they are wrong. The successor concern is CE-004. |
| **Border-frame delta compression** | **Landed.** |
| **World editor** | **Delivered** and live in the admin dashboard. |
| **CE-006's cluster-CA mTLS mechanism** | **Superseded** by the shared-secret plus ephemeral-TLS decision. The risk stays open; the mechanism is replaced. |
| **Volumetric cell partitioning** | **Declined** — see [non-goal 1](#3-non-goals). |
| **Client prediction removal** | **Reversed** by WS-001, which is implemented and committed. See the [WS-001 subsection](#ws-001--client-prediction-reconciliation-and-adaptive-playback--partial). |

---

## 9. Document map

| Document | Owns |
| --- | --- |
| `docs/roadmap.md` (this file) | Vision, direction, scope statement, non-goals, all active tracking |
| [`docs/architecture.md`](architecture.md) | Current implemented state only — links here exactly once |
| [`docs/README.md`](README.md) | Documentation index |
| [`README.md`](../README.md) | Project front door |
| [`AGENTS.md`](../AGENTS.md) | Authoritative contributor and agent rules |
| [`pkg/net/README.md`](../pkg/net/README.md) | Client channel bytes, transports, delivery classes |
| [`pkg/mmokit/README.md`](../pkg/mmokit/README.md) | ECS query and command-buffer rules, `Stage.Spawn` contract |
| [`internal/game/factory.go`](../internal/game/factory.go) | System registration order |

`docs/superpowers/` holds dated plans, specs, and audits owned by an external workflow. They explain how a feature was designed at a point in time. They are **not** proof of current behaviour and are not edited during ordinary documentation maintenance.

**Maintenance rule:** when a fact appears in two documents, delete one and link. When a tracker is retired, migrate its open items first.
