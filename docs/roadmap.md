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

### 6.2 Immediate action: triage the working tree

The working tree currently holds **198 changed paths, 155 files, roughly 11,136 insertions, and 43 untracked files, with zero commits**. That work substantially implements CE-001, CE-003, CE-004, and the reliability half of CE-005, and it introduces an entire client-prediction workstream that exists in no document and on no branch.

Until it is committed or deliberately discarded, any roadmap read against `HEAD` misstates four items and omits a workstream. **Commit or triage this before planning anything else.**

### 6.3 P0 — must close before the 2D/3D program begins

#### CE-002 — Bounded decoding and ingress budgets · **Open (0/8)** · highest priority

The reflection decoder performs unchecked reads and trusts wire-supplied lengths. [`pkg/universe/reflect_marshal.go:337`](../pkg/universe/reflect_marshal.go) slices `data[off:off+slen]` on an attacker-controlled `uint16`, and `:347-351` calls `make([]byte, n)` on an attacker-controlled `uint32`.

This is worse than previously recorded. The path is reachable **pre-authentication** — [`pkg/universe/gateway.go:1054`](../pkg/universe/gateway.go) gates only on route kind and explicitly tolerates a nil session — inside a per-connection goroutine with **no `recover`**. An unrecovered panic there aborts the process. Treat this as an **unauthenticated remote denial of service**, not a robustness nit. The `make([]byte, n)` path cannot be mitigated by `recover` at all: a large enough length produces Go's unrecoverable out-of-memory fatal error.

Acceptance criteria unchanged from the previous roadmap: a checked decoder returning consumed bytes and an error; bounds checks before every read; configurable limits on frame size, strings, slices, nesting, and aggregate allocation; rejection of truncated and trailing data; per-connection queue and per-tick work caps across WebSocket, UDP, and virtual connections; codec fuzzing; and bounded-cardinality rejection metrics. No fuzz target exists anywhere in the repository today.

#### CE-004 — Destination acceptance before cross-host demotion · **Partial (1/7)**

Failure propagation landed: [`pkg/universe/grpc_bridge.go:80-123`](../pkg/universe/grpc_bridge.go) now returns a status on every remote failure path, with rollback in `handoff_driver.go:449-465`.

The acceptance protocol itself does not exist. `HandoffAccepted` appears in no source file. Demotion is still armed on local enqueue rather than destination acceptance, there is no `(NetID, Epoch)` deduplication, and mesh stream cleanup still deletes unconditionally by a payload-supplied ID (`mesh_control_server.go:154-159`), so a reconnect race can delete the newer stream's registration.

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

#### CE-003 residual — Datagram frame ACKs · **Partial (6/8)**

Most of this landed in the working tree: typed delivery outcomes (`pkg/net/send_result.go`), a frame writer returning a result, baseline commit gated on delivery class (`pkg/system/replication.go:1525`), surfaced WebSocket backpressure, and the border re-entry fix (`border_viewer.go:115`).

Remaining: server-side frame ACKs are built but wired to nothing — no call site sets an ACK mode anywhere in `internal/`, `examples/`, `web-pixi/`, or `csharp/`. And there is no deterministic loss/reorder harness. **Reuse [`pkg/universe/loopback_bridge.go`](../pkg/universe/loopback_bridge.go)**, an existing latency and loss injection harness currently referenced by nothing but its own test, rather than building a new one.

Note the behaviour change: UDP now classifies as best-effort, so every UDP frame forces a fresh snapshot. That is a **bandwidth cliff**, not the correctness bug it used to be. It is blocked on CE-005.

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
| WS-001 | **Client prediction, reconciliation, adaptive playback** | In working tree, undocumented | Uncommitted files only — highest-risk omission. Reverses a previously executed decision to remove client prediction. |
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

### 6.7 Cross-cutting verification gates

Each work item should add the smallest regression test that fails before the fix. These gates belong alongside P0, not after it — **all four marked below are blocked on OSS-001**, since no CI exists.

- [ ] Race-enabled tests for engine scheduling, transports, connection teardown, and mesh reconnects *(blocked on CI)*
- [ ] Fuzz targets with retained corpora for reflection codecs, operation and input frames, and mesh frame decoders *(blocked on CI; zero fuzz targets exist today)*
- [ ] Deterministic simulated loss/reorder/duplicate harnesses — build on `pkg/universe/loopback_bridge.go`
- [ ] Linux integration jobs that permit localhost TCP and UDP listeners *(blocked on CI)*
- [ ] Generated-schema diff checks and cross-language Go/TypeScript/C# golden vectors *(blocked on CI)*
- [ ] Load tests asserting bounded memory, queue depth, tick work, and recovery after backpressure

There is also a known flaky race between `ensureBorderDispatcher` and `applyPeerList` in the cell bridge that makes distributed fixtures unreliable under `-race`. Fixing or quarantining it makes every other failure attributable, and is the highest-leverage schedule intervention available.

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
| **CE-001 — authoritative entity removal** | **Closed.** All criteria met in the working tree; `Commands.Despawn` now routes through a single removal primitive. Its problem statement is false and must not be carried forward. |
| **Co-simulation / overlap handoff** | **Deleted**, not deferred. The implementing files no longer exist. Some dated plans and older notes still describe it as merely unwired — they are wrong. The successor concern is CE-004. |
| **Border-frame delta compression** | **Landed.** |
| **World editor** | **Delivered** and live in the admin dashboard. |
| **CE-006's cluster-CA mTLS mechanism** | **Superseded** by the shared-secret plus ephemeral-TLS decision. The risk stays open; the mechanism is replaced. |
| **Volumetric cell partitioning** | **Declined** — see [non-goal 1](#3-non-goals). |
| **Client prediction removal** | **Reversed** by WS-001, which is being re-implemented in the working tree. |

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
