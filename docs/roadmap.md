# MMOKIT Roadmap and Vision

**Status:** Single source of truth for project direction
**Last verified against source:** 2026-07-28 (CE-002 re-verified after implementation)
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
4. **No wire backward compatibility** across registered-type renames. Client wire IDs are `fnv32a(reflect.Type.String())` ([`pkg/mmokit/broadcast.go:54`](../pkg/mmokit/broadcast.go)), which qualifies by package *name*, not import path — so renaming a registered type or its package is a breaking protocol change by design, while moving a package between directories is not. The one path-qualified scheme in the tree is [`pkg/service.EventTypeName`](../pkg/service/event_codec.go) (`PkgPath()+"."+Name()`), which keys the server-internal service event bus; no client wire type reaches it.
5. **Not core's job:** client rendering, art pipeline, navmesh generation, dynamic rigid-body physics solving, and character controllers (slopes, step-up, crouch, air control). The framework provides broad-phase, queries, and static push-out; games bring their own solver. See [section 7.4](#74-collision-scope).

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

#### CE-002 — Bounded decoding and ingress budgets · **Done (8/8)**

Closed on branch `ce-002-bounded-decoding` (8 commits, `0dc6bafe`..`91da84ef`). The reflection decoder now bounds every read and charges every allocation before making it; client ingress is queue-capped, work-budgeted and panic-contained on every surface.

The original attack inputs, run against the branch:

| Attack | Result |
| --- | --- |
| `u16` string length `0xFFFF`, 2 payload bytes | `read of 65535 bytes at offset 2 exceeds the 4-byte body` |
| ~20-byte body declaring 65535 slice elements | `slice of 65535 elements needs at least 131070 bytes, 0 remain` |
| `u32` `[]byte` length `0xFFFFFFFF` | `byte field of 4294967295 bytes exceeds the 16777216-byte limit` |
| truncated scalar | `read of 8 bytes at offset 0 exceeds the 1-byte body` |

Note the slice bound is payload-**derived** rather than a configured knob — strictly stronger, and it cannot be misconfigured.

| # | Criterion | Evidence |
| --- | --- | --- |
| 1 | Checked decoder returning consumed bytes and an error | `decodeState` in [`reflect_marshal.go`](../pkg/universe/reflect_marshal.go); `decodeStruct` returns `(int, error)`. The three public wrappers return `error`; the consumed count surfaces through `ReflectUnmarshalStrict`'s `consumed != len(data)` check rather than a public `(int, error)` signature — see residuals |
| 2 | Bounds checks before every read | 16 `d.need(...)` preconditions; `rg 'binary\.LittleEndian\.Uint(16\|32\|64)\(data\[off'` over the file returns nothing. `TestReflectUnmarshal_Truncated` covers every switch a | **done** `25998982` |
| 3 | Configurable limits | `net.WireLimits` (9 fields) + `universe.Config.WireLimits` + `--wire-max-*` flags, frozen at `New()` with a `Normalized()` zero-value fallback because `flag.Parsed()` is always true under `go tes | **done** `3b8bcdd4` |
| 4 | Reject truncated and trailing data | `ReflectUnmarshalStrict` at 4 client-facing sites (`op_dispatch`, `op_dispatch_cell`, `event_dispatch`, `client_input_dispatch`). `TestReflectUnmarshalStrict_RejectsTrailing` / `TestReflectUnmarshal_ToleratesMeshTrailing` pin the deliberate asymmet | **done** `41e1d062` |
| 5 | Per-connection queue and per-tick work caps | Caps on all three surfaces (`conn.go`, `udp_transport.go`, `virtual_conn_manager.go`), `ws.SetReadLimit` finally called, budgets on `ops.Router.poll` and `Stage.DispatchClientInput` | **done** `0dc6bafe`
| 6 | Codec fuzzing | 6 targets covering all three families §6.7 names, incl. `FuzzDispatchInboundEventFrame` and `FuzzDecodeMeshFrame`; repo's first Go `testdata/fuzz` corpora; `just fuzz` | **done** `58c74f63`
| 7 | Bounded-cardinality rejection metrics | `mmokit_ingress_rejected_total{reason,surface}` over a fixed `[2][12]` array — 24 series at start, 24 after any hostile traffic. `TestIngressMetrics_CardinalityIsFixed` is the guard against someone making it a map | **done** `d851414e`
| 8 | No unrecovered panic on a pre-auth ingress goroutine | `recover()` in non-test Go rose 7 → 12, covering `Gateway.processOpFrame`, `ops.Router.dispatchOne`, `HostNetwork.routeInboundFrame` and a **scoped** `Cell` decode barrier | **done** `43297994`

**Residual, deliberately accepted:**

- **Mesh and transfer decode tolerate trailing bytes by design.** Those blobs are appended-to, and `UnmarshalTransferFrame` does not reject trailing data either. Strict decoding is client-facing only.
- **`RoutePlayerCell` op decode failure is a user-visible behaviour change** — the client's typed-op promise used to hang forever and now receives `OperationError` code 3.
- **Criterion 1's "consumed bytes" is satisfied by argument, not by a public signature.** `decodeState` carries `(int, error)` internally and `ReflectUnmarshalStrict` enforces exact consumption; no public `ReflectUnmarshalN` was added. Revisit if a caller ever needs the count.
- **`ReflectCodec.Decode` failures are counted as `truncated`.** A registered codec gets exactly `Size()` bytes so it cannot over-read — it refused the *value*. A 13th enum arm nothing would alert on was judged worse than the approximation.
- **The mesh profile widens `MaxTotalAllocBytes` to 16 MiB.** Defensible while the gRPC streams are already configured for 16 MiB messages, but it is [CE-006](#ce-006--mesh-authentication-and-authorization--done-1212) landing mesh authentication that actually makes that number safe.
- **`pkg/quantize`'s `FrameDecoder`, `SnapshotReader` and `DeltaEncoder.Decode` are left unchecked on purpose.** They are the most flagrantly unchecked decoders in the repo and have **zero** server-side inbound callers — the only production callers are in `internal/bot`, behind a recover. Anyone grepping for unchecked decoders finds these first; spending CE-002 budget there buys nothing.

**Fixed along the way, not in the original scope:** `ConnManager.AddTransport` never recorded a peer address for UDP connections, so `RemoteAddrString` returned `""` for every UDP conn — every UDP-originated login in the cluster shared one `IPRateLimiter` bucket and every UDP audit row recorded a null IP. Separately, both `ComponentReplicator` registrars decoded straight into live world storage, so a refused body left a **torn component** (leading fields peer-supplied, trailing fields stale) on the border and handoff paths; both now stage into a scratch and commit only on success.

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

Connection requests no longer allocate. An unauthenticated request now records only a `pendingHandshake`; the transport and its goroutine are created only when a data packet arrives carrying the token for that same source address, which proves return routability. Both the pending table and the session table are bounded, and drop counters exist with rate-limited logging so the log itself cannot be used for amplification.

Two Tier 1 claims are weaker than previously recorded, both measured:

- **The drop counters are unreadable in production.** [`Process.startUDPListener`](../pkg/universe/bootstrap.go) at `bootstrap.go:271-291` discards the `*UDPServer` — `go udpServer.Run(ctx)` is the last use of the handle — so `SetLimits`, `SourceMismatchDrops`, `CapacityDrops` and `PendingFullDrops` have **zero** production callers. Their only callers are in `pkg/net/udp_server_test.go`. The configured limits never reach the running server either.
- **Pending entries expire only under table pressure, never on a timer.** `sweepPendingLocked` is called from exactly one place, [`pkg/net/udp_server.go:335-337`](../pkg/net/udp_server.go), guarded by `if len(s.pending) >= s.maxPending`. So 1024 spoofed source addresses deny new connections for up to `pendingTTL` (15 s). Bounded and self-healing, but not the timer-based expiry the entry implied.

Both are fixed inside the P0 closure phase ([§6.8](#68-next-phase--p0-closure)).

**UDP is off in the shipped binary default:** `--udp-listen` defaults to empty ([`bootstrap.go:73-75`](../pkg/universe/bootstrap.go)) and logs an explicit experimental warning when enabled, escalated for a non-loopback bind. Note this is *not* true of local development — `just dev` (`justfile:65`) and `just distributed-space` (`justfile:145`) both pass `--udp-listen=:9000`, a wildcard bind, deliberately as of `52e6c15`. Every dev process carries the exposure. Nine regression tests in `pkg/net/udp_server_test.go` cover spoofed data, spoofed disconnect, foreign-token promotion, handshake idempotence, and both caps; the package is race-clean.

**Tier 2 remains open, and it is the P0 item the next phase does not close.** Required: real cryptographic connection identity (authenticated handshake and AEAD framing) so an *on-path* attacker cannot read or forge traffic, and so address rebinding for roaming clients can be supported safely. This section is the single owner of that work — **WS-002 Unit 2 and the UDP-key half of WS-003 are folded in here**; see [§6.6](#66-workstreams-not-covered-by-a-ce-item) for what remains of those rows.

Scope, roughly **12.5 engineer-days**: `POST /auth/udp-key` plus a process-level key registry and a UDP analogue of `Gateway.onAuthSuccess` (~2.5 d — the `AuthResolver` seam and cookie plumbing already exist); AEAD framing and unreliable-channel replay enforcement across `udpproto`/`udp_server`/`udp_transport`/`udpclient` (~5 d); C# parity plus golden interop (~3 d); retiring op-channel auth in the C# client (~2.5 d). A stateless HMAC handshake cookie replacing the pending table belongs here too — it is the natural first half of the authenticated handshake, and doing it separately means designing the same thing twice.

Two corrections for whoever picks this up:

- **The cipher must be reversed.** The umbrella spec names ChaCha20-Poly1305. `Mmokit.Sdk.Core.csproj` targets `netstandard2.1` — the TFM Unity consumes — where `ChaCha20Poly1305` does not exist and `AesGcm` does. `netstandard2.1` also has no `HKDF` class.
- **OIDC does not gate this.** WS-003's real dependency is one route plus a key registry. OIDC is a single unused SQL table (`auth.identities`) with zero Go readers or writers.

Also note the two approved specs contradict each other: `2026-06-06-csharp-sdk-unity-design.md` chose auth-over-op-channel specifically to *avoid* the split HTTPS-then-UDP design that the 2026-06-12 umbrella then locked in. The newer decision wins; the older spec section is superseded.

#### CE-006 — Mesh authentication and authorization · **Done (12/12)**

Both mesh channels now run over TLS and authenticate peers with a shared cluster secret; control-plane handlers act on the identity the stream registered with; payload frames are verified per-arm against the identity the stream presented; and RBAC grants are gone from the wire entirely.

| # | Criterion | Evidence |
| --- | --- | --- |
| 1 | `ClusterSecret`, flag, env, precedence | `Config.ClusterSecret`; `BindFlags` reads `MMO_CLUSTER_SECRET` *before* `stringFlag` so it becomes the flag default, giving flag > env > field with no `flag.Visit`; repeated in `New` because `BindFlags` is skipped under `go test` **and** for any game calling `flag.Parse` itself | **done** `07b908d3` |
| 2 | `MeshControl` rejects unauthenticated `RegisterHost` | `clusterSecretStreamInterceptor`; `TestMeshAuth_UnauthenticatedRegisterHostIsRejected` polls to assert `hostRegistry.Get` *stays* nil, with an authenticated positive control alongside | **done** `b19243e6`, `df78ef19` |
| 3 | `MeshData` rejects an unauthenticated stream | Same interceptor on `NewHostNetwork`'s server. Tested on a raw `NewHostNetwork` pair — **not** the `all` preset, which never assigns `Host.Network` and so cannot reach `routeInboundFrame` at all | **done** `b19243e6` |
| 4 | Wrong secret rejected as firmly as absent | `subtle.ConstantTimeCompare` against `""` when the key is missing, so both take the same path | **done** `b19243e6` |
| 5 | Both listeners non-plaintext, in-memory certs | `grpc.Creds` on both servers, `credentials.NewTLS` on all **three** dials, one lockstep commit. `generateDevCert` reused unchanged | **done** `9bb48285` |
| 6 | `all` closed by default, fingerprint logged | Role-gated `resolveClusterSecretPosture`; `sha256[:4]`, never the value | **done** `effc3433` |
| 7 | Multi-process with no secret warns and continues | Same function's default arm; enforcement is `if serverSecret != ""`, never a global flip | **done** `effc3433` |
| 8 | Twelve payload-identity reads use the stream-bound ID | `bindIdentity`; every site also *acts* on the stream ID, so deleting the check later cannot silently restore a body-trusted identity | **done** `53d3e30d` |
| 9 | Same-ID re-registration cannot evict a live peer | `admitHostRegistration` / `admitGatewayRegistration` | **done** `bfe410d4`, `0737d5f3` |
| 10 | RBAC grants never taken from the wire | `Grant` deleted from the schema; `Dispatcher.InvokeAsPeer` | **done** `a0f62e28` |
| 11 | No stale mechanism claims remain | Both `TODO(mTLS)`/`TODO(S4)` deleted; `SECURITY.md`, `docs/architecture.md`, `cmd/server/README.md` and this entry rewritten | **done** |
| 12 | `MeshData` frame contents bound to the stream | Stream-captured peer ID threaded as a parameter; per-arm rules | **done** `884b9bbc` |

**Criterion 9 was re-scoped, because half of it was wrong.** "Cannot overwrite its `GrpcAddr`" is backwards: all four production `NewHostNetwork` calls bind `":0"`, so a restarted host advertises a *new* port and freezing the address leaves it unreachable on the payload plane while its control stream looks healthy. `State: RemoteHostRegistered` is likewise the only exit from `RemoteHostDead`. The admitted path therefore still performs the **full** field refresh; what is rejected is a registration for an ID an incumbent is *still defending*, which needs three terms — a kill-channel check (or `host kill` becomes a 3 s lockout), an unconditional `Local` clause (or a staleness-only rule hands over `"local"`/`"inproc"`), and `State != Dead` **combined with** heartbeat age (a host wedged in `Registered` is never marked Dead). Two fixes landed with it or it would have been a net regression: `checkLiveness` now cancels the stream when it marks a host Dead, closing a permanent-zombie state, and the gateway side gained `cancelGatewayStream`.

**Criterion 10 was re-scoped, because its remedy was unimplementable and its obvious reading was an escalation.** There is no local grant store — `d.grants` is an empty `InMemoryGrantStore` whose `Set` has no non-test callers, and `InvokeLocal` never reads it anyway. Resolving by `Caller.ID` would take `perf.snapshot`'s synthesized `{ID:"admin", Grants:[{"perf"}]}` and match it against the seeded operator literally named `admin` with `["*.*"]`, widening one capability to all of them on every host; it also cannot resolve `"console"`, `"admin:bots-publisher"` or any test caller, three of the four production callers being synthesized in Go rather than rows in a table. Grants are therefore deleted from the wire outright, and authority is the authenticated peer relation. This extends a precedent already in the same function, which overwrites `caller.Source` "regardless of what the sender claims". A wire-sourced `Caller` now fails `cmdsys.Check` on every capability, so a receive path that forgets `InvokeAsPeer` fails **closed**.

**Criterion 12's mechanism is a stream-captured peer ID threaded as a parameter, not `MeshFrame.sender_id`.** The previous entry prescribed the field and was internally contradictory: proto3 strings have implicit presence, so a *server-populated* field is never serialized — it is written and read by the same process, which is a function parameter with a lockstep redeploy attached, and the "interned numeric peer ID" fallback argued about the width of a field never on the wire. Server-population also requires mutating a gRPC-owned inbound message, which `host_network.go` explicitly prohibits, and stamping it in the send path races in-flight marshaling because `service_event_dispatch` builds one frame and fans it to N peers. The parameter additionally forces a value at `grpc_bridge.go`'s always-proxy self-route, which bypasses gRPC entirely and would have silently yielded an empty field.

**The rules are per-arm, and the previous entry's justification for a blanket check was false.** It claimed "there is no legitimate case where a frame's claimed identity differs from its stream sender". Three counterexamples: `ClientFrame.GatewayId` names the *receiving* gateway and every producer is a host — one frame per player per tick, so a sender check drops 100% of replication traffic; `ForwardInput.GatewayId` names a third-party gateway on a host→host frame; and `newReplicationReceiptFrame` *echoes* `cf.GatewayId` from the inbound frame rather than stamping its own, self-equality being enforced upstream by `gateway.go` instead.

**Residual, deliberately accepted:**

- **The cluster secret is shared, not per-peer.** `MeshData` has no registration handshake, `n.peers` is the outbound dialed map, and TLS is server-side only with no client certificates, so a stream's claimed peer ID is authenticated only as "holds the secret". This closes outsider injection and enforces per-stream identity **consistency** — killing the one-stream-many-identities attack and making logs correlatable — but does **not** stop one authenticated member from claiming another's ID. Per-peer credentials are out of scope; treat every process holding the secret as equally trusted.
- **`InsecureSkipVerify` on every dial.** Confidentiality against a passive eavesdropper only; an active on-path MITM is answered by network isolation, not PKI.
- **`ForwardInput`, `FromCellId` and `SourceCellId` are unbound.** The first legitimately names a third party; the latter two would need a cell→host resolution through the eventually-consistent `cellToHost` snapshot, which is stale by construction during commit windows.
- **The audit trail is one flag away, not on by default.** `InvokeLocal` emitted nothing and the production sink was `NoopAuditSink{}`, so a mesh-delivered command left zero trace and "preserved for audit" was satisfied by a field nobody read. A logger-backed sink now exists under `cmdsys:audit`, which is registered but not in `StartupCategories`.
- **Coordinator-console chat moderation over the wire remains broken, and is not CE-006's.** `executeCommandRequest` forces `SourceMeshControl`, so `operatorOpContext` then `uuid.Parse`s `"console"` and fails. Pre-existing; "fixing" it by preserving `Source` would reopen a trust bypass.

**Fixed along the way, not in the original scope:** `outgoingMeshMD` initially attached no metadata at all when no secret was configured, which left `senderID` empty in every secret-less fixture and made each bound arm drop — the service event bus went silent. The secret and the peer ID answer different questions, so the ID is now attached unconditionally. The three stale-epoch bypasses on the untracked `ClientFrame` path were closed (the entry named one; there were three), the peer-supplied command deadline is clamped, and the `CellTransferAbort` arm was deleted outright as it had zero constructions repo-wide.

#### OSS-001 — Open-source readiness and CI · **Open** · cheap, and blocking

**The CI half is closed.** `LICENSE` (MIT), `CONTRIBUTING.md`, `SECURITY.md` and `.gitignore` secret hardening landed in `25998982`; `.github/workflows/ci.yml` (go, frontend, csharp, drift-go) and `nightly.yml` (race, fuzz) landed in `41e1d062`, after `3b8bcdd4` made `-race` green. Every job's commands were executed directly before the workflows were written; the GitHub Actions harness itself is necessarily unverified until a real run.

The **publication half remains open**, which is why this item is Partial rather than Done. See the remainder below.

**The licence is MIT**, and it covers **mmokit core only**. The reference space game is not part of the open-source distribution.

**The public boundary, verified.** `pkg/` and `examples/` contain **zero** imports of `internal/` — the only importers of `internal/` are `cmd/server`, `cmd/botclient` and `internal/` itself — and both `examples/simple` and `examples/4node-basic` ship their own `main.go`, so the public repo has runnable entry points without `cmd/server`. The split is therefore structurally clean today rather than something to be engineered.

| | |
| --- | --- |
| **Published** | `pkg/` · `examples/` · `cmd/sdkgen` · `cmd/csharp-golden` · `proto/` · `gen/` · `csharp/` · `web-admin/` · `scripts/` · `docs/` |
| **Not published** | `internal/` · `cmd/server` · `cmd/botclient` · `web-pixi/` · `data/` · `world/` · `db-init/` |

Three consequences that follow from this and are easy to miss:

- **Audio provenance stops being a blocker.** The 16 tracked `.ogg` files with no attribution anywhere in the tree live under `web-pixi/public/audio/`, which is not published. No archaeology required.
- **Several validation recipes are game-coupled and cannot run in the public repo.** `just lint-no-ark` targets `internal/game/`; `just shipdyn-golden` and the `just web-test` prediction goldens span `internal/game/` **and** `web-pixi/`; `just space-sdk` runs `cmd/server`. The public repo's CI is necessarily a subset of this repo's, and WS-001's ship-dynamics parity gate — the workstream's only continuously-triggered drift risk — has no home there at all. Whichever repo keeps the game must keep that gate.
- **The extraction mechanism is an open decision**, not covered by this item's estimate: a `git subtree` split, a fresh repo with the game as a downstream consumer of a tagged module, or publishing this repo with the game removed. The first two keep `go.mod:1`'s `github.com/zenion/mmokit` path stable for consumers; the third does not.

The CI half and the publication half are separable and the phase in [§6.8](#68-next-phase--p0-closure) closes the CI half completely.

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
| WS-002 | ~~Security Units 2–4~~ — **row split, see below** | — | The single row bundled three unrelated units and made all three invisible. |
| WS-002/2 | Security Unit 2 — UDP AEAD framing | Not started | **Owned by [CE-005b](#ce-005b--udp-security-and-gating--tier-1-done-tier-2-open) Tier 2.** Not tracked here. |
| WS-002/3 | Security Unit 3 — mesh shared-secret auth | **Done** | **Owned by [CE-006](#ce-006--mesh-authentication-and-authorization--done-1212), now closed.** Not tracked here. |
| WS-002/4 | Security Unit 4 — application hardening | Partial | The only orphan of the three: it belongs to neither CE item and has no owner. Umbrella spec records its detail as "to be written". |
| WS-003 | Auth over HTTPS with OIDC social login | HTTPS endpoints exist; OIDC is a schema seam only | The `/auth/udp-key` half is **owned by CE-005b Tier 2** (~2.5 d; the `AuthResolver` seam and cookie plumbing already exist). What stays here is OIDC proper — a single unused SQL table `auth.identities` with zero Go readers or writers. **OIDC does not gate the UDP key**; the previous entry implied it did. |
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

Each work item should add the smallest regression test that fails before the fix. These gates belong alongside P0, not after it — **three of the six are blocked on OSS-001**, since no CI exists. Gate 5 is split below because its two halves have different blockers, and gate 4 turns out not to be CI-blocked in the way it was recorded.

- [x] Race-enabled tests for engine scheduling, transports, connection teardown, and mesh reconnects — `nightly.yml` `race` job. It classifies its own failures: a `WARNING: DATA RACE` fails hard as a concurrency bug, a zero-race failure is labelled a flaky test. That split exists because the two known flakes below would otherwise cost a log-archaeology session per red
- [ ] Fuzz targets with retained corpora for reflection codecs, operation and input frames, and mesh frame decoders — **half closed.** Six targets and 40 committed seeds landed with CE-002, and `nightly.yml` runs a 5 min/target mutation campaign. What is still missing is *retention*: a discovered crasher is a 14-day artifact, not a committed corpus entry, and nothing auto-files it. *(Original note: Note the gate names **three** decoder families: the reflection codec, the operation **and input** frame decoders, and the mesh frame decoder. `UnmarshalTransferFrame` is a payload decoder reached from inside `decodeMeshFrame`, not the frame decoder itself — covering it does not cover the third family)*
- [x] Deterministic simulated loss/reorder/duplicate harnesses — landed as `pkg/system/lossy_link_test.go`. **Do not** build on `pkg/universe/loopback_bridge.go` as previously recommended here: it routes `CellMessage` (no path from a frame writer into it), `pkg/universe` imports `pkg/system` so the import cycle forbids it, it applies one constant latency so it can never reorder, and it has no duplicate injection
- [x] Linux integration jobs that permit localhost TCP and UDP listeners — closed by the `go` job on `ubuntu-latest`, which runs the six binding test files named in `ci.yml`. **not blocked on anything but the CI file itself.** The tests already exist and already bind localhost: `pkg/net/server_origin_test.go`, `pkg/net/udp_server_test.go`, `pkg/net/udpclient/handshake_race_test.go`, `pkg/admin/admin_e2e_test.go`, `internal/bot/auth_test.go`, `pkg/universe/udp_listener_test.go`. Any `go test ./...` job on `ubuntu-latest` satisfies this; tick it by naming that coverage, not by building something new
- [ ] Generated-schema diff checks *(blocked on CI **and** on Postgres: `just space-sdk` is not DB-free — `cmd/server/main.go:126-137` opens Postgres unconditionally and `log.Fatalf`s regardless of `--admin-listen=`)*
- [x] Cross-language Go/TypeScript/C# golden vectors — `drift-go` job regenerates both manifests and `git diff --exit-code`s them
- [x] Load tests asserting bounded memory, queue depth, tick work, and recovery after backpressure — landed as `TestIngress_SustainedLoadBoundedAndRecovers` (pkg/universe). Four assertions, one per clause; each was verified by neutering the cap it guards

**Two standing blind spots in any `go test ./...` gate**, stated so green is not mistaken for complete:

- **It covers zero PostgreSQL code.** All four `*/postgres` packages are behind `//go:build pgtest` and report `[no test files]`. Their tests are not skipped — they are invisible. Only `just test-pg` runs them.
- **`gofmt` cannot be gated tree-wide.** `gofmt -l $(git ls-files '*.go')` reports 72 files / 1358 diff lines, almost all pre-existing Go 1.19+ doc-comment reflow, and `AGENTS.md` forbids clearing that as incidental cleanup. Gate changed files only. Relatedly, `go.mod` declares `go 1.25.1` with **no `toolchain` directive** while only go1.26.x is installed, so `vet` and `gofmt` results are toolchain-dependent and CI would measure a moving target until it is pinned.

**Race inventory, re-measured at `17a9a2ce`:**

1. ~~**`pkg/admin` fails `-race` deterministically.**~~ **Fixed** (`3b8bcdd4`). `Unsubscribe` now joins the dispatcher before returning, so `handleStream`'s deferred unsubscribe cannot return while `Deliver` is still writing the handler's `ResponseWriter`; the dispatcher owns its own map-entry removal so the join cannot be missed, and the self-unsubscribe path passes `join=false` to avoid deadlocking on its own exit. `go test ./pkg/admin/ -race -count=5` exits 0; it was 5/5 red before. Kept here rather than deleted because the shape recurs: **the original defect was** `go test ./pkg/admin/ -race -count=1` fails 5/5 on `TestAdminE2E_SplitTriggersTopologyEvent`; the same package passes 3/3 without `-race`. It is a production bug, not a test artifact: `sseWriter.Deliver` ([`pkg/admin/sse.go:81`](../pkg/admin/sse.go)) calls `Flush()` on the handler's `http.ResponseWriter` from the `TopicBus.dispatcher` goroutine ([`topicbus.go:169`](../pkg/admin/topicbus.go)) **after `handleStream` returned**, racing net/http's own `finishRequest`/`chunkWriter.close`. `Unsubscribe` (`topicbus.go:77`) only closes `st.done` and never joins the dispatcher. `sseWriter`'s own mutex cannot help, because the other writer is net/http. Using a `ResponseWriter` after the handler returns violates the net/http contract. **Gate 1 cannot be ticked until this is fixed**, or the race job is red on its first run and everyone learns to ignore it.
2. **`examples/4node-basic` `TestE2EMeshSplitMergeWithBotTraffic` is a suspected pre-existing flake**, recorded here because it was not recorded anywhere. Observed twice consecutively during CE-002 (`post-resplit-0_0 entity conservation: 1 of 60 bots missing`), then 7 consecutive full-suite passes on byte-identical code, plus 16 standalone runs and 6 under 2× CPU oversubscription all green; a further 5 consecutive `-count=5` passes after the phase landed. Byte-identical code and an identical command producing failure-then-pass makes it nondeterministic by construction rather than attributable to a diff, and it did not reproduce on `HEAD` either. Note `9c320a96` shipped this test alongside three race fixes, so the area has prior form. Needs a targeted investigation, not a re-run.
3. **`pkg/mmokit` `TestTintSystem_PhaseFollowsClusterTimeNotCell` fails under full-suite `-race` load.** Found on the first full `go test -race ./... -p 1` run after the gate was greened: `color diverged across cells at equal cluster time: cellA={R:0 G:45 B:255} cellB={R:0 G:94 B:255}`. **Zero data races in that run** — this is a test failure, not a concurrency bug. It does not reproduce in isolation (3/3 on `main`, 3/3 on branch) nor under 24 CPU spinners (4/4), so it is rare and load-shaped.

    Worth someone's attention rather than a quarantine, because the failure is *not obviously possible*: the test pins cluster time with `ClusterClock.SetNowFn` to a constant, the tint guest is stateless and derives phase solely from `ctx.TimeSec()`, and `Stage.ClusterTimeMs()` quantizes that same pinned value — so tick count should not be able to change the colour at all. Something load-dependent is reaching the phase that should not be. The obvious suspect was cross-test mutation of the global tunable registry, but `pkg/mmokit`'s only `t.Parallel` tests are admin-topic tests and no test sets the tint rate. Unexplained.

4. **`TestTopicBus_SlowSubscriberDropped` is a real flake, and worse than first recorded** — it fails **without** `-race` too, roughly 1 run in 30 standalone (`slow subscriber got 5 events, want <=4`), and it reproduces on `main`. It asserts an exact drop count on a timing-dependent bound (`topicbus_test.go:67`). This is currently the single most likely cause of a spurious red on any full-suite run, so unit 3 should fix it before unit 4 makes `go test` a merge gate.
5. **`ensureBorderDispatcher` vs `applyPeerList`** in the cell bridge — **did not reproduce** at this commit. `go test ./pkg/universe/ -race -p 1 -count=1` passed (47.8 s), and `pkg/universe` was `ok` in a full `-race` run. Possibly closed by the cell-identity work in `1536ece`. Recorded as unreproducible rather than deleted; do not treat it as fixed without a targeted test.
6. ~~**`Cell.MeshID` during a rename.**~~ **Fixed.** A cell's `(MeshID, CellID)` pair is now one immutable `cellIdentity` record behind an `atomic.Pointer`, read through `Cell.MeshID()` / `Cell.CellID()` / `Cell.Identity()` and replaced only by `setIdentity` on the rename path. The mutable exported fields are gone, so the whole class of unsynchronized off-loop reads is closed rather than just the one the detector found. `Host.CellByID` additionally stopped scanning on the identity and now resolves through the map key (`ParseCellID` + lookup), which is both race-free and O(1) instead of O(cells) on a path the mesh data plane hits per inbound frame. Note that a single-load `Identity()` is the only way to get both halves consistently — two separate accessor calls may legitimately straddle a rename, which `TestCellIdentity_SeparateAccessorsMayStraddleARename` documents.

Also note: `pkg/universe` intermittently reports `executor: serialize timeout on cell_0_0` ([`cell_transfer_executor.go:239`](../pkg/universe/cell_transfer_executor.go)) under PARALLEL package execution. That one is CPU contention on a `RunOnLoop` deadline, not a logic race. The recorded "roughly 1 run in 4 with default `-p`" figure is **not confirmed at `17a9a2ce`** — it did not reproduce in 1 default-`-p` run (`go test ./... -count=1 -timeout 300s`, exit 0, 47.9 s, `ok pkg/universe 47.448s`) or 2 `-p 1` runs. One passing sample cannot refute a 1-in-4 rate, so **keep `-p 1` as retained insurance**, but treat it as insurance rather than a proven necessity, and note that under `-race` (2 m 55 s versus 48 s) contention is strictly worse, so a nightly race job should carry `-p 1` too.

### 6.8 Next phase — P0 closure

**Approximately 35 engineer-days.** This is the active phase. It takes CE-002, CE-006 and OSS-001's CI half to their full acceptance criteria. It is recorded here and nowhere else — no `docs/superpowers/` spec or plan is written for it — so this section is the deliverable, and [§4 principle 4](#4-design-principles) applies to it: **status is derived from source, not from these headings.**

#### 6.8.1 What it does and does not close

| Item | At phase end |
| --- | --- |
| CE-002 | **Done (8/8)** — landed, see [§6.3](#ce-002--bounded-decoding-and-ingress-budgets--done-88) |
| CE-006 | **Done (12/12)**, including the `MeshData` payload binding — closed with a stream-captured peer ID rather than the `sender_id` field this table previously anticipated, for the reasons in [§6.3](#ce-006--mesh-authentication-and-authorization--done-1212). The one proto change that did land is the replication-receipt oneof arm, with `just proto` + `just fuzz-corpus` and a lockstep cluster redeploy |
| OSS-001 | **Partial.** CI half closed completely; publication half is the mmokit-core extraction, which carries an open mechanism decision |
| CE-005b Tier 2 | **Still open** — see below |

**§7.1's gate does not lift when this phase ends.** [§7.1](#71-sequencing-rule) gates the entire 104-day 2D/3D program on *every* P0 item, and CE-005b Tier 2 sits in [§6.3](#63-p0--must-close-before-the-2d3d-program-begins). The successor phase is CE-005b Tier 2 at roughly 12.5 days, after which it does. This is stated plainly here because the phase name invites the opposite reading.

#### 6.8.2 Sequencing

Four units have no dependencies and can start together: the roadmap of record, publication hygiene, greening the race gate, and ingress containment. The ordering that matters is everything else.

- **Ingress containment does not wait for CI.** The pre-auth unbounded-queue DoS in `pkg/net/conn.go` and the missing panic barriers need neither the decoder refactor nor a malformed frame. Sequencing them behind an eleven-day refactor delays an active fix for no technical reason.
- **CI comes early but not first.** CE-002 criterion 6 is codec fuzzing, and §6.7's fuzz gate is explicitly CI-blocked — the *retained corpora* half needs scheduled mutation runs, though seeds alone execute under a plain `go test`. Independently: CE-002 changes `ReflectCodec.Decode`'s signature, both `ComponentReplicator` closure field types, 19 non-test decode call sites across 9 files and 23 test sites spanning three packages. `go vet ./...` catches all of that in 0.41 s and today runs only when someone remembers.
- **Greening `-race` gates the nightly job and nothing else.** `pkg/admin` fails deterministically at HEAD (§6.7). A race gate that is red on its first run is a gate nobody trusts.
- **The limits type must be declared in `pkg/net`, not `pkg/universe`.** `pkg/universe` imports `pkg/net` one-way — `go list -deps ./pkg/net` returns only `pkg/net` and `pkg/net/udpproto` — so a type in `pkg/universe` is unreachable from all three `pkg/net` enforcement sites. This is a hard constraint, not a preference.
- **`ReflectCodec.Decode`'s error return cannot be propagated before `decodeState` exists**, because `unmarshalStructOnStage` returns bare `int` until then. It belongs inside the decoder unit, not before it.
- **Strict decoding must be built and switched on by different units.** `ReflectUnmarshalStrict` is built with the decoder; it can only be *enabled* by the unit that also owns `ValidateMessageType` wiring and depends on the error propagation. Building it without an owner for the switch is how criterion 4 ships half-closed while the close-out records it Done.
- **Mesh TLS is a lockstep flip in one commit** — `grpc.Creds` on both servers and `credentials.NewTLS` on all **three** dials (`host_network.go`, `mesh_control_client.go`, `mesh_gateway_client.go`; the one most easily dropped is `ConnectPeer`, the payload plane). A TLS server cannot accept a plaintext client and there is no negotiation, but every multi-process launch in this repo builds one binary and runs N copies of it and every test cluster is N `Process` values in one OS process, so there is no version-skew path and no transition period. *(The previous wording — "flipping `RequireTransportSecurity()` breaks any dial that has not yet attached credentials" — had the mechanism backwards: that method belongs to `PerRPCCredentials`, and grpc-go rejects only insecure transport combined with a per-RPC credential demanding security, i.e. dials that HAVE attached credentials. Carrying the secret in stream metadata avoids the question entirely.)* Fixture propagation turned out **not** to be load-bearing: enforcement is gated on a non-empty server secret rather than a global flip, so every existing fixture takes the warn-and-continue path and none needed editing.
- **CE-006 splits three ways: authentication (1–7, ~6 d), control-plane authorization (8–11, ~5 d), payload-plane binding (12, ~2 d).** Split so a regression is attributable to one of the three. The control-plane authorization half touches crash-reconnect, graceful-leave and the operator command path and carries most of the phase's risk. The payload-plane unit is the only one carrying a proto change, so it is isolated to keep the `just proto` regeneration and the lockstep redeploy out of the other two units' blast radius — **isolated, not deferred.**
- **A proto change is a normal cost, not a hazard to route around.** [Non-goal 4](#3-non-goals) waives wire backward compatibility and [the field-cleanup rule](#8-superseded-closed-and-deliberately-not-doing) already requires renumbering from 1 rather than reserving. When a schema change is the right design, take it and schedule the redeploy; do not tunnel through an existing field or file the criterion out of scope. Judge mechanisms on per-frame cost, capture-point clarity and forgeability — never on whether they touch `proto/meshpb/`.

#### 6.8.3 Units

CE-002's seven units and OSS-001's CI half are **done**. Status is derived from the commits, not from this table — re-verify against source. What remains is OSS-001's publication half and all of CE-006.

| # | Unit | Item | Days | Risk | After | Status |
| --- | --- | --- | ---: | --- | --- | --- |
| 1 | Phase of record: this section, and the criteria rewrites above | tracking | 0.75 | low | — | done |
| 2 | Publication hygiene, MIT `LICENSE`, secret-filename hardening, `CONTRIBUTING`/`SECURITY` | OSS-001 | 0.75 | low | — | **done** `25998982` |
| 3 | Green the gate: `pkg/admin` SSE-after-handler race, `TopicBus` flake, toolchain pin | OSS-001 | 1 | med | — | **done** `3b8bcdd4` |
| 4 | CI foundation: PR jobs plus a nightly race and fuzz workflow | OSS-001 | 2 | low | 3 | **done** `41e1d062` |
| 5 | Ingress containment: panic barriers, pre-auth queue caps, per-drain work budgets, `WireLimits` carrier | CE-002 (5, 8) | 3.5 | med | — | **done** `0dc6bafe` |
| 6 | Fuzz harness covering all three decoder families §6.7 names | CE-002 (6) | 2 | low | 4 | **done** `58c74f63` |
| 7 | Encoder length guards that return an error rather than panicking | CE-002 | 0.75 | low | 4 | **done** `d851414e` |
| 8 | `decodeState`: bounds before every read, allocation charged before every allocation | CE-002 (1, 2, 4) | 3.25 | med | 6, 7 | **done** `43297994` |
| 9 | Propagate decode errors through all 19 call sites, incl. the `ComponentReplicator` facade break | CE-002 | 2 | med | 8 | **done** `3056091e` |
| 10 | Limits on `Config`, strict decoding switched on, registered wire types validated | CE-002 (3, 4) | 1.5 | med | 9 | **done** `403c1f8c` |
| 11 | Bounded-cardinality rejection metrics, actually scraped, plus the sustained-ingress load test | CE-002 (7); gate 6 | 2 | med | 5, 9 | **done** `47aa058c` |
| 11b | Component apply atomicity — found during CE-002 verification, not originally scoped | CE-002 | 0.25 | low | 9 | **done** `91da84ef` |
| 12 | CE-006 Phase A: authenticate and encrypt both mesh channels | CE-006 (1–7) | 6 | med | 2 | **done** `07b908d3`, `9bb48285`, `b19243e6`, `effc3433`, `df78ef19` |
| 13 | CE-006 Phase B: bind control-stream identity, stop trusting RBAC grants off the wire | CE-006 (8–11) | 5 | **high** | 12 | **done** `53d3e30d`, `bfe410d4`, `a0f62e28`, `0737d5f3` |
| 14 | CE-006 Phase C: stream-captured peer ID (**not** `MeshFrame.sender_id` — see the entry), payload-plane binding, receipt oneof arm, `just proto` + `just fuzz-corpus` | CE-006 (12) | 2 | med | 12 | **done** `d16ffc73`, `884b9bbc`, `e1367bda` |
| 15 | Phase close: propagate the secret to recipes, sweep stale claims, write status back here | all | 2.5 | low | 1, 2, 4, 5, 10, 11, 13, 14 | **done** |

Unit 4's dependency on units 6 and 7 was a sequencing preference, not a technical one, and was inverted when CE-002 ran first: the fuzz harness and encoder guards landed without CI. The CI foundation still owes them a scheduled mutation run, which is the half §6.7 gate 2 actually needs.

#### 6.8.4 Traps this phase must not fall into

Each of these was found by verifying a plausible plan against source and finding it wrong. They are recorded because they are cheap to re-derive incorrectly.

- **Do not commit crashing fuzz seeds before the decoder is checked.** Go executes `testdata/fuzz/<Name>/` entries as ordinary subtests under a plain `go test`, so crashers committed early redden the required job for days. Ship non-crashing seeds with the harness; the truncated and oversized seeds land in the *same commit* as the checked decoder. The bounds-regression table has the same property in reverse — it asserts today's panics *via recover*, so it is green on HEAD and green after, never in between.
- **Do not panic in the encoder guards.** `ReflectMarshal` runs on the cell tick goroutine and `gl.tick(dt)` runs bare with no recover — a panic there adds exactly the failure mode unit 5 exists to remove. Return an error. Widening a length prefix is also not an option: the widths are a frozen cross-language contract in `cmd/sdkgen` and `csharp/Mmokit.Sdk.Core/ReflectCodec.cs`.
- **Do not wrap the whole of `Cell.processMessage` in a recover.** `MsgSpawnTransfer` mutates `Engine.Players` mid-handler and the handoff arms promote/demote and `Stage.handoffAccepted`; a mid-handler unwind can leave a half-registered player or a netID holding both a Live and a Replica slot. Scope the barrier to the decode step, or pair it with a forced integrity re-assert and a counter so a recovered handoff is visible rather than silent.
- **Do not use `ValidateComponentType` for wire types.** 12 production types carry slice fields and pass only under `allowSlice=true`; the stricter validator panics all twelve at package init and looks like the guard causing mass breakage. A measured sweep of all 126 unique production wire types found **zero** failures under `ValidateMessageType` — the one offender repo-wide is test-only (`pkg/mmokit/messaging_all_test.go`, `N int`). This unit is small; it was previously priced against an unmeasured failure set.
- **Do not put a per-connection label on the rejection metrics.** `pkg/metrics/cell_metrics.go` has the in-repo definition: "Plain counters, no per-player labels — the cardinality has to stay bounded." A connID or IP label lets an attacker drive unbounded metric-map growth, converting the DoS fix into a new DoS vector. Note the precedent to avoid copying: `RecordInputAckFrame`/`RecordInputSequenceRejected` are incremented today and their only reader has no callers — the counters exist and were never shipped. This unit is done when a scrape shows them, not when they exist. A gateway-role-only process currently serves a structurally empty `/metrics`, because the handler is fed from `allCellLoads` and a gateway owns no cells.
- **Criteria 3 and 12 cannot be tested against the `all` preset.** It never assigns `Host.Network`, so `newBridgeForCell` returns a plain `cellBridge` and `routeInboundFrame` is structurally unreachable — a test written there is a false pass. Use `newDistributedFixture` or a raw `NewHostNetwork` pair. Criterion 6's test belongs on `all`; criterion 7's on `coordinator` + `host`.
- **Do not stamp a per-frame identity in the send path.** `service_event_dispatch` builds one frame and fans it to N peers, each with its own sender goroutine, so a write there is a concurrent mutation against in-flight marshaling and a guaranteed `-race` failure. This is one of several reasons the payload-plane identity is a parameter rather than a proto field.
- **The peer ID and the cluster secret are separate concerns.** They answer different questions — may this stream speak at all, versus which payload identities may its frames claim — so the ID must be attached even when no secret is configured. Withholding it makes every bound arm drop under the criterion-7 posture; the symptom is the service event bus going silent in every secret-less fixture, not an authentication error.
- **Do not gate the auto-generated cluster secret on a loopback check.** `--control-listen` defaults to `":9100"` and `isLoopbackBind` treats an empty host as all-interfaces, so the heuristic never fires for the bind every dev recipe uses. `just dev` opens an unauthenticated wildcard `MeshControl` listener today.
- **Do not reuse `Process.httpTLSConfig` for mesh TLS.** It `sync.Once`-memoizes the client-facing posture and falls back to plaintext on error; a mesh cert is required even when client TLS is plaintext, which is the default. Reuse `generateDevCert` unchanged instead, and do not "fix" its localhost-only SANs — peers dial with `InsecureSkipVerify`, so changing them would imply a verification that does not happen.
- **Flag defaults never reach tests.** `universe.New`'s `if !flag.Parsed()` guard is always false under `go test`, so `BindFlags` is skipped entirely. Defaults must be applied as a zero-value fallback in `New()`, and roughly twenty fixture sites must set `Config.ClusterSecret` directly.
- **A corrupt component blob must skip that component, not the entity.** Aborting the transfer turns a malformed blob into an entity-loss bug on the handoff path. The trade — an entity carrying a stale or absent component instead of a clean failure — is an authority-boundary decision and belongs in the roadmap entry, not only a code comment.

---

## 7. The 2D/3D and multi-genre program

**Approximately 104 engineer-days** (band 95–120; absolute range 85 if collision is cut hard, 140 if three overruns land together).

### 7.1 Sequencing rule

**No work in this section begins until every P0 item in [section 6.3](#63-p0--must-close-before-the-2d3d-program-begins) is closed and verified against source.** This rule is recorded here so the ordering survives independently of whoever decided it.

**The residual gate is CE-005b Tier 2.** The active phase in [§6.8](#68-next-phase--p0-closure) closes CE-002 and CE-006 and takes OSS-001 to Partial, but deliberately does not close CE-005b Tier 2 (~12.5 d). Four verified reasons: UDP is off in the shipped binary default while CE-002's decoder is reachable pre-auth on the WebSocket path that is on by default; AEAD does not help CE-002 at all, since the same reflection decoder runs on decrypted bytes; the locked cipher must be reversed first (ChaCha20-Poly1305 does not exist on `netstandard2.1`, the TFM Unity consumes); and CE-009 wants a version byte in the same 5-byte UDP header, so bundling them is one wire break and one golden regeneration instead of two. **This gate therefore does not lift at the end of §6.8** — it lifts one phase later.

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
| **CE-006's cluster-CA mTLS mechanism** | **Superseded** by the shared-secret plus ephemeral-TLS decision. The risk stays open; the mechanism is replaced. The two surviving `TODO(mTLS)`/`TODO(S4)` comments in `host_network.go` and `mesh_control_client.go` must be **deleted, not implemented** — and `host_network.go`'s claim that "S3 only runs in loopback" is false: all four production callers pass `":0"`, a wildcard ephemeral bind. |
| **Certificate pinning by fingerprint for mesh TLS** | **Declined**, out of scope. Peers dial with `InsecureSkipVerify`; the residual is an active on-path MITM on an untrusted network, answered by network isolation rather than by PKI. |
| **Audio-asset provenance as an OSS blocker** | **Moot.** The 16 unattributed `.ogg` files live under `web-pixi/`, which is not part of the mmokit open-source distribution. |
| **ChaCha20-Poly1305 for UDP AEAD** | **Superseded** by AES-GCM. `Mmokit.Sdk.Core` targets `netstandard2.1`, where `ChaCha20Poly1305` does not exist and `AesGcm` does. `netstandard2.1` also has no `HKDF` class. |
| **Auth over the op channel (C# client)** | **Superseded** by the 2026-06-12 umbrella's HTTPS-then-UDP decision. The 2026-06-06 Unity SDK spec chose the op channel specifically to avoid that split; the newer decision wins. |
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
