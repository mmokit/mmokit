# Core Engine Improvement Roadmap

**Status:** Active tracking document  
**Review date:** 2026-07-13  
**Scope:** ECS lifecycle, game loop, client transport, replication, and distributed mesh

This document tracks the highest-value improvements found during a source review of the core engine. It is a living backlog, not a description of current guarantees. Current source and tests remain authoritative; evidence links below point to the code as it existed on the review date and may move as work lands.

The engine's overall shape is worth preserving: phase-ordered ECS execution, explicit authority epochs, spatial interest management, and delta replication provide a solid base. Correctness at lifecycle and delivery boundaries should be addressed before an ECS rewrite or broad performance tuning.

## Priority summary

| ID | Priority | Workstream | Impact | Effort | Status |
| --- | --- | --- | --- | --- | --- |
| CE-001 | P0 | Authoritative entity removal | Critical | Medium | Open |
| CE-002 | P0 | Bounded wire decoding and ingress | Critical | Medium | Open |
| CE-003 | P0 | Replication delivery and baseline contract | Critical | Medium–High | Open |
| CE-004 | P0 | Acknowledged cross-host handoff | Critical in distributed mode | Medium–High | Open |
| CE-005 | P0 | UDP production readiness | Critical when UDP is enabled | High | Open |
| CE-006 | P0 | Mesh authentication and identity binding | Critical in distributed mode | Medium–High | Open |
| CE-007 | P1 | Replication bandwidth scheduling | High at scale | Medium | Open |
| CE-008 | P1 | Precise tick and loop-job lifecycle | High | Low–Medium | Open |
| CE-009 | P1 | Protocol compatibility contract | High | Medium | Open |
| CE-010 | P2 | Framework isolation and iteration consistency | Medium–High | Medium–High | Open |
| CE-011 | P2 | Allocation and identifier capacity work | Medium–High at scale | Medium | Open |

## Recommended delivery order

1. **Correct local invariants:** CE-001 and CE-002.
2. **Make state delivery truthful:** CE-003, including the border re-entry regression described there.
3. **Harden enabled production modes:** CE-004 and CE-006 for distributed deployments; CE-005 before treating UDP as production-ready.
4. **Add scale controls:** CE-007 and CE-008.
5. **Strengthen framework contracts:** CE-009 through CE-011.

Items for disabled modes do not have to block a single-process WebSocket deployment, but the relevant mode should remain explicitly experimental until its P0 item is complete.

## P0 — Correctness and production safety

### CE-001 — Use one authoritative entity-removal pipeline

**Problem:** [`Commands.Despawn`](../../pkg/universe/commands.go#L25-L30) directly removes an ECS entity. It bypasses [`Engine.FlushRemovals`](../../pkg/engine/engine.go#L121-L135), which records `RemovedNetIDs` and invokes `OnEntityRemoved`. That callback clears the spatial grid and NetID index in [`coordinator.go`](../../pkg/universe/coordinator.go#L2395-L2401). Production systems, including projectile cleanup, use the command-buffer path.

**Risk:** stale spatial entries, stale NetID lookups, future ID conflicts, and genuine despawns being encoded as AoI exits instead of removals.

**Target outcome:** every genuine entity destruction path performs identical bookkeeping, regardless of whether removal is requested immediately, through `Commands`, or at end of tick.

**Acceptance criteria:**

- [ ] Introduce one authoritative removal primitive shared by command-buffer and end-of-tick paths.
- [ ] Preserve command-buffer semantics: a despawn from system N is visible to system N+1.
- [ ] Record the removed NetID exactly once.
- [ ] Deregister the entity from the spatial grid and NetID index exactly once.
- [ ] Verify command despawn produces `Removed`, not `Exited`, in replication output.
- [ ] Cover repeated removal and add/remove-after-despawn cases without panic.

### CE-002 — Make decoding bounded and enforce ingress budgets

**Problem:** the reflection decoder performs unchecked reads and trusts attacker-controlled lengths before slicing or allocating. The affected paths include primitive reads, strings, `[]byte`, and generic slices in [`reflect_marshal.go`](../../pkg/universe/reflect_marshal.go#L299-L369). Gateway-local operation dispatch invokes it without a decode error path in [`op_dispatch.go`](../../pkg/universe/op_dispatch.go#L71-L87). WebSocket, UDP, and virtual-connection inbound queues can also grow without a byte or message cap.

**Risk:** a malformed or incompatible client can panic a process, cause excessive allocation, or monopolize a cell tick by flooding queued inputs.

**Target outcome:** malformed traffic is rejected cheaply and predictably before allocation or game-handler execution, while one connection cannot consume unbounded memory or tick time.

**Acceptance criteria:**

- [ ] Replace void decoding with a checked decoder returning consumed bytes and an error.
- [ ] Check remaining bytes before every primitive read, slice, or nested decode.
- [ ] Enforce configurable limits for frame size, strings, slices, nesting, and aggregate decoded allocation.
- [ ] Reject truncated and trailing data; map operation failures to `OpErrorDecodeFailed`.
- [ ] Cap queued bytes and messages per connection across WebSocket, UDP, and virtual connections.
- [ ] Limit input entries and work per connection per tick; coalesce latest-state inputs where appropriate.
- [ ] Add codec fuzzing plus malformed-length and connection-fairness tests.
- [ ] Export bounded-cardinality metrics for rejection, throttling, queue depth, and disconnects.

### CE-003 — Align replication baselines with real delivery

**Problem:** replication advances a connection's baseline while building a frame in [`replication.go`](../../pkg/system/replication.go#L743-L818), before the transport reports whether the frame was accepted or delivered. [`BinaryFrameWriter`](../../pkg/system/frame_writer.go#L65-L77) has no result channel and sends world frames through `ConnManager.Send`, which selects unreliable delivery. WebSocket's bounded outbound queue silently drops on saturation in [`conn.go`](../../pkg/net/conn.go#L83-L90). UDP defaults can therefore combine an `AckReliable` baseline with unreliable frame delivery.

**Risk:** after one dropped frame, subsequent deltas may depend on state the client never received, producing persistent state corruption until a fresh snapshot happens to repair it.

**Target outcome:** a baseline becomes eligible only after the delivery guarantee required by its ACK mode has been satisfied.

**Acceptance criteria:**

- [ ] Make transport and frame-writer sends return a typed outcome.
- [ ] Separate frame construction, enqueue/delivery, and baseline commit.
- [ ] Commit `AckReliable` only after successful ordered enqueue on a transport that provides the required guarantee.
- [ ] Implement and wire explicit client frame ACKs/history for datagram replication, or send those frames through a reliable ordered channel.
- [ ] Force a fresh snapshot or disconnect when backpressure invalidates delta continuity.
- [ ] Eliminate the concurrent WebSocket `Send`/`Close` panic path and add bounded write deadlines.
- [ ] Test queue saturation, packet loss, reconnect, slow clients, and baseline recovery as end-to-end properties.
- [ ] Remove a border sender's baseline when an entity leaves its interest set so leave/re-enter cannot emit an unchanged sentinel against a destroyed replica. The current set rotation is in [`border_viewer.go`](../../pkg/universe/border_viewer.go#L101-L110).

### CE-004 — Require destination acceptance before cross-host demotion

**Problem:** remote mesh dispatch logs failures but returns no status in [`grpc_bridge.go`](../../pkg/universe/grpc_bridge.go#L74-L122); the boolean wrapper then unconditionally reports success for the remote path. [`HandoffDriver`](../../pkg/universe/handoff_driver.go#L374-L407) schedules source demotion after that result.

**Risk:** a transient peer, queue, encode, or stream failure can remove the source entity even though the destination never accepted it. Reconnect races can also allow an old stream's cleanup to delete the newer stream's registration.

**Target outcome:** a handoff preserves exactly one authoritative Live entity throughout loss, retry, reconnect, and duplicate delivery.

**Acceptance criteria:**

- [ ] Immediately propagate encode, peer lookup, queue, and send failures to the handoff caller.
- [ ] Add an idempotent `HandoffAccepted{NetID, Epoch, CommitTick}` response.
- [ ] Do not arm source demotion until the destination accepts the handoff.
- [ ] Deduplicate retries and promotions by `(NetID, Epoch)`.
- [ ] Fence control/data stream cleanup by connection generation or pointer identity.
- [ ] Replay complete desired state after reconnect: cells, gateway sessions, services, and subscriptions.
- [ ] Test absent peers, queue saturation, delayed/lost ACKs, duplicate frames, and reconnect at commit tick.

### CE-005 — Repair, replace, or explicitly gate UDP

**Problem:** the custom UDP reliability layer loses sequence identity in its send ring. Retransmission substitutes the slot index for the original sequence in [`udp_transport.go`](../../pkg/net/udp_transport.go#L307-L328), ACK handling does not verify that a slot still contains the acknowledged sequence, and new sends can overwrite an unacknowledged window. Duplicate delivery, weak token/address binding, allocation during handshake, and unsynchronized receive/ACK timestamps add correctness and abuse risks. Similar logic exists in the Go client and must stay compatible with the C# client.

**Target outcome:** UDP is either clearly experimental and disabled by default, replaced by a maintained secure transport, or backed by a correct shared protocol implementation.

**Acceptance criteria:**

- [ ] Decide and document whether to adopt QUIC/DTLS or retain the custom protocol.
- [ ] Until complete, mark UDP experimental and prevent accidental production enablement.
- [ ] If retained, store and validate the full sequence in every retransmission slot.
- [ ] Apply window backpressure rather than overwriting unacknowledged messages.
- [ ] Suppress duplicates and define ordered-delivery behavior.
- [ ] Add an authenticated handshake/token, address binding, and migration proof where needed.
- [ ] Give reliability state a clear synchronization or single-owner model.
- [ ] Share protocol tests across server, Go client, and C# client.
- [ ] Add deterministic loss, reorder, duplicate, spoof, window-wrap, timeout, and race tests.

### CE-006 — Authenticate and authorize MeshControl and MeshData

**Problem:** internal gRPC clients use insecure transport credentials, while servers do not authenticate peer identity. [`MeshData`](../../pkg/universe/mesh_data_server.go#L20-L35) routes frames immediately, and control messages frequently trust host/gateway IDs supplied in payloads.

**Risk:** any process able to reach the mesh can claim a cluster identity and forge assignments, input, disconnects, ownership transitions, or service announcements.

**Target outcome:** every mesh stream has a cryptographically authenticated process identity and role, and every state transition is authorized against that principal.

**Acceptance criteria:**

- [ ] Add cluster-CA mTLS to MeshControl and MeshData clients and servers.
- [ ] Derive host/gateway/service identity and role from the peer certificate.
- [ ] Bind a stream to that identity once; reject mismatched payload IDs.
- [ ] Authorize message variants and ownership transitions by role and current state.
- [ ] Provide certificate issuance, rotation, expiry, and development-mode guidance.
- [ ] Prefer loopback/private binds unless an external mesh bind is explicit.
- [ ] Test untrusted certificates, wrong roles, claimed-ID mismatch, and forged mutations.

## P1 — Scale controls and durable contracts

### CE-007 — Turn replication priority into a bandwidth scheduler

**Problem:** priority accumulates on skipped ticks and resets on send in [`replication.go`](../../pkg/system/replication.go#L720-L734), but there is no per-viewer byte/entity budget that sorts and drains candidates by that priority.

**Target outcome:** every connection has predictable bandwidth and CPU cost while important/new/stale entities receive timely updates without starvation.

**Acceptance criteria:**

- [ ] Define configurable per-viewer byte and entity budgets.
- [ ] Rank candidates by new visibility, gameplay criticality, accumulated priority, age, and distance.
- [ ] Guarantee bounded starvation and reserve capacity for control/removal data.
- [ ] Split or defer oversized frames without breaking baseline continuity.
- [ ] Export sent, deferred, starved, oversized, and budget-utilization metrics.
- [ ] Add deterministic fairness tests and benchmarks across viewer/AoI cardinalities.

### CE-008 — Make tick timing, overload, and loop jobs precise

**Problem:** [`GameLoop.Run`](../../pkg/engine/loop.go#L146-L167) truncates `1000 / TickRate` to whole milliseconds. At 60 Hz it schedules 16 ms ticks, or 62.5 Hz, while other callbacks may use `1/60`. [`RunOnLoop`](../../pkg/engine/run_on_loop.go#L52-L73) can enqueue with no running loop, and a caller that times out after enqueue can still have its mutation execute later. Effective-Hz metrics currently derive frequency from processing duration rather than tick cadence.

**Target outcome:** configured simulation time, actual scheduling, overload behavior, lifecycle errors, and reported metrics agree.

**Acceptance criteria:**

- [ ] Validate `TickRate > 0` and compute the interval with `time.Second / time.Duration(rate)`.
- [ ] Derive all fixed `dt` and cluster timing conversions from one validated representation.
- [ ] Schedule against absolute deadlines and explicitly choose bounded catch-up versus skip behavior.
- [ ] Record start-to-start cadence, deadline lateness, missed ticks, processing duration, and phase timing separately.
- [ ] Reject loop jobs when stopped and complete pending waiters with `ErrLoopStopped` during shutdown.
- [ ] Carry job context/cancellation into the queue and skip mutations canceled before execution.
- [ ] Add pre-start, shutdown-race, cancellation, overload, and non-divisor tick-rate tests.

### CE-009 — Negotiate a client protocol version and schema fingerprint

**Problem:** registered type IDs and positional layouts are implicit wire contracts, but clients receive no canonical schema fingerprint during connection setup. FNV-32 type IDs can collide, and deployment skew may decode valid bytes into the wrong shape instead of failing early.

**Target outcome:** incompatible clients are rejected before authentication/gameplay with a clear diagnostic, and all registered wire IDs are globally collision-checked.

**Acceptance criteria:**

- [ ] Define a wire-format version independent of application release version.
- [ ] Generate a canonical schema fingerprint covering inputs, events, broadcasts, operations, entity layouts, enums, and quantization.
- [ ] Negotiate version/fingerprint during WebSocket and UDP setup.
- [ ] Audit collisions across every registry, not only within each message category.
- [ ] Define whether N/N-1 compatibility is supported and test that policy.
- [ ] Make generated SDK metadata expose the negotiated version and fingerprint.

## P2 — Framework quality and long-term scale

### CE-010 — Isolate process state and make iteration semantics consistent

**Problem:** bundle queries exclude Ghost and Replica by default, while `ForEach1/2/3` iterate raw filters and can expose neighbor-owned replicas to mutation, including through WASM. Several wire registries and `coords.CellSize` are process-global, which prevents safe isolation of differently configured `Process` instances and parallel tests.

**Acceptance criteria:**

- [ ] Make all game-facing iteration default to authoritative live entities.
- [ ] Add clearly named escape hatches for Ghost/Replica/all-entity iteration.
- [ ] Test Query, ForEach, OnTick, and WASM behavior against Live, Ghost, and Replica entities.
- [ ] Move protocol/handler registries behind a process-owned immutable registry assembled at build time.
- [ ] Replace mutable global cell geometry with an explicit process/world dependency.
- [ ] Demonstrate isolated parallel processes/tests with different configurations.

### CE-011 — Reduce measured allocation cost and harden identifier capacity

**Problem:** replication allocates visibility maps and snapshot/delta buffers per viewer, while NetID allocation grants finite ranges without enforcing the range boundary during `NextNetID` growth.

**Acceptance criteria:**

- [ ] Add allocation and CPU benchmarks across representative viewer, entity, and AoI counts.
- [ ] Double-buffer/clear visibility sets and active-connection tracking where benchmarks justify it.
- [ ] Define frame-buffer ownership so safe pooling can remove redundant payload/wire copies.
- [ ] Enforce NetID range exhaustion before entering an adjacent allocation range.
- [ ] Add refill/lease or 64-bit identifier design before high-churn deployments rely on current capacity.
- [ ] Export remaining-range/exhaustion alarms and test wrap/recycle behavior.

## Cross-cutting verification

Every work item should add the smallest regression test that fails before the fix. The following quality gates should be introduced alongside the P0 work rather than deferred until the end:

- [ ] Race-enabled tests for engine scheduling, transports, connection teardown, and mesh reconnects.
- [ ] Fuzz targets with retained corpora for reflection codecs, operation/input frames, and mesh frame decoders.
- [ ] Deterministic simulated loss/reorder/duplicate harnesses for replication, UDP, and handoff.
- [ ] Linux integration jobs that permit localhost TCP/UDP and exercise multi-process role separation.
- [ ] Generated-schema diff checks and cross-language Go/TypeScript/C# golden vectors.
- [ ] Load tests that assert bounded memory, queue depth, tick work, and recovery after backpressure.

## Review validation record

At the time of the review:

- `go test -race ./pkg/engine ./pkg/system ./pkg/replication` passed.
- Broader `pkg/net` and `pkg/universe` integration tests could not run in the review sandbox because localhost TCP/UDP listeners were prohibited.
- Findings above are source-backed but should receive a focused reproducer before implementation where the acceptance criteria call for one.
