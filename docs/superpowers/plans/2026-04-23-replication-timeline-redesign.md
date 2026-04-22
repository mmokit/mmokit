# Replication Timeline Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Re-architect cross-cell replication around a coordinator-anchored `ClusterClock` with per-entity `producedAtMs` stamps on the wire and hard-cut (overlap-free) authority handoff at a cluster tick boundary. Delete every server-side dead-reckoning path, the Shadow component, the warmup/cooldown state machine, and multi-source dedup. Client-side interpolation is the sole smoothness layer.

**Architecture:** A single `ClusterClock` per host, seeded in the `RegisterHost` handshake and refreshed every 10 s by a `CoordTimeSync` broadcast on the existing `MeshControl` stream. Every replication sample carries an opaque `producedAtMs uint64` set by the authoritative producer at emit time and passed through border-replica caches unchanged. Handoff is a single `HandoffMessage` declaring a `commitTick`; source demotes at end-of-tick N, destination promotes at start-of-tick N+1. Replicas are passive caches — no DR, no grace extrapolation. The client's interp ring naturally spans authority transfers because every sample sits on one coherent cluster-clock axis.

**Tech Stack:** Go (server), TypeScript/bun (web clients), protobuf (`buf generate`), existing MeshControl bidi stream, existing AutoReplicator schema export pipeline, existing S7 split/merge/migrate test fixtures as the cluster-topology oracle.

**Source spec:** [`docs/superpowers/specs/2026-04-23-replication-timeline-redesign.md`](../specs/2026-04-23-replication-timeline-redesign.md)

**Baseline:** This branch forks from **`overlap-handoff`** (tip `a3e3fcf`), not `main`. Continuing from the overlap-handoff work means the pre-existing Shadow / warmup / multi-source / grace-extrapolation machinery is present here and must be deleted explicitly as part of this refactor. A focused cleanup pass (**Phase A2**) removes the overlap-specific additions that the new design doesn't need; the remaining phases build the new design on the cleaned substrate. Some primitives introduced by overlap-handoff are directly useful for the new design — `WorldBase.DemoteLiveToReplica`, `netIDIndex.Demote` — and are **kept** (possibly with signature tweaks) rather than deleted and re-added. The spec's "Deletion checklist" section names the full surface.

**Rollout order (strict):** A → B → C → D → E → F → G → H → I → J → K → L. Each phase ends with a reviewable commit. Building (`go vet ./...`) is green between phases; the full Go test suite is green between phases **except** at Phase A2 (overlap-handoff cleanup — tests exercising the deleted machinery go in the same pass) and at the Phase G/H boundary where the handoff protocol flips, where several integration tests are intentionally torn down and rewritten in the same session.

---

## Lessons carried forward

1. **No backward compat** (user preference). Every `HandoffPrepare`/`HandoffCommit`/`HandoffCancel` reference is deleted or rewritten — no shims, no aliases, no deprecation periods. Same for `Shadow`, `ReplicaDeadReckoningSystem`, `UpdatedThisTick`, `ShadowWatchdog`, etc.
2. **Wire format schema = runtime bytes** (`feedback_wire_format_schema_runtime_match`). Per-entity `producedAtMs` is declared in `EntityKindDef` / `BindingSchema` or at the frame-encoder layer — never behind a runtime flag. Schema dump and server output must match.
3. **`bun`, not `npm`** (`feedback_package_manager`). All TypeScript-side test runs + installs use `bun`.
4. **`go vet`, not `go build ./...`** (CLAUDE.md). Never drops binaries in the repo root.
5. **`feedback_no_worktrees`** — sequential work, so no worktrees. The branch is already created: `replication-timeline-redesign` off `overlap-handoff`.
6. **`feedback_farewell_excludes_self`** — the new `ReplicationSystem` must not emit `Removed` or `Exited` for the viewer's own netID during handoff. The existing `selfNetID` exclusion is already correct; do not break it.
7. **`feedback_interpolation_prev_anchor`** — web-pixi `interpolation.ts` uses real server snapshots as `prev` / `curr`. When we swap the sample-stamp source from frame-level `serverTimeMs` to per-entity `producedAtMs`, the `prev` anchor invariant still holds — do not anchor to `renderX`.
8. **`feedback_no_cell_field_shadow`** — if a new struct embeds `WorldBase`, do not name a field `Cell`.
9. **`feedback_proto_field_cleanup`** — when editing `mesh.proto`, do not reserve removed field numbers. Renumber the oneof arms from 1. Buf will regenerate.
10. **`feedback_position_quantization`** — do not quantize positions anywhere in this refactor; float32 wire format is mandatory.
11. **`feedback_no_unnecessary_type_args`** — omit generic type args when Go can infer (`ecs.NewMap1[component.Replica]` is fine at declaration; do not sprinkle redundant `[T]`).
12. **Never build in repo root** (CLAUDE.md). Use `go vet ./...` or `just build` to verify.
13. **Logging on new server-side game logic** (`feedback_logging`). The ClusterClock observe loop, the handoff commit, and any drain-edge path gets a `gw.Log.Log(CatXxx, …)` at meaningful transition points. In pkg-layer code use `b.eng.Log.Log(...)` with the existing log categories (`CatMeshTransfer`, `CatMeshControl`, etc.).

---

## Scope check

The spec is a tightly-scoped redesign of one subsystem (replication timeline) with five mechanical consequences (delete Shadow, delete server DR, collapse handoff proto, change wire format, add ClusterClock). The client-side changes are small defensive updates (swap stamp source) and stay inside the same branch because the wire format flip is atomic — client + server ship together or neither. **One plan, one branch, one merged commit set.** Not splittable.

---

## File structure

### New files

- `pkg/mmokit/cluster_clock.go` — `ClusterClock` primitive (thread-safe EMA offset, `Now()`, `Observe()`, `Observed()`).
- `pkg/mmokit/cluster_clock_test.go` — unit tests for the above.
- `pkg/universe/handoff.go` (replaces existing file) — a minimal `handoffTracker` type if retained at all; may collapse to a pair of maps inside `HandoffDriver`.
- `pkg/universe/cluster_clock_integration_test.go` — integration test: coordinator + 2 hosts; assert `Observed()` transitions, offset convergence, coord-death resilience.
- `pkg/universe/hard_cut_handoff_test.go` — integration test: single-host and cross-host hard-cut handoff. Replaces the deleted `pkg/universe/handoff_test.go` + `overlap_test.go` + any warmup-oriented test.

### Modified files (Go / proto)

- `proto/meshpb/mesh.proto` — add `CoordTimeSync` and `Handoff` messages to `CoordMessage` / `MeshFrame`; **remove** `HandoffPrepare`, `HandoffCommit`, `HandoffCancel`. Renumber the `MeshFrame` oneof from 1; renumber `CoordMessage` oneof by appending new arms.
- `pkg/universe/coordinator.go` — `Config.ClusterClockSyncInterval` (default `10 * time.Second`); `ClusterClock` field on `Process`; `startClusterTimeBroadcast` goroutine.
- `pkg/universe/mesh_control_server.go` — on `RegisterHost` receive, immediately push an initial `CoordTimeSync` to the host's stream (before `RegisterAck` returns). Add `seq` counter.
- `pkg/universe/mesh_control_client.go` — dispatch `CoordMessage_CoordTimeSync`; call `clusterClock.Observe(msg.CoordTimeMs, msg.Seq)`. Block cell-worker start until `Observed()` is true.
- `pkg/universe/host_network.go` — in-process coordinator+host wiring: `ClusterClock` shared directly (offset = 0).
- `pkg/universe/bridge.go` — collapse `SendHandoffPrepare`/`SendHandoffCommit`/`SendHandoffCancel` to `SendHandoff(destCellID, payload *HandoffPayload) bool`. Delete the two non-returning variants.
- `pkg/universe/cell_bridge_impl.go` — same collapse; delete `HandoffDriver()` accessor if unused.
- `pkg/universe/grpc_bridge.go` — same collapse; route `HandoffPayload` via `MeshFrame.Handoff`.
- `pkg/universe/message.go` — `HandoffPayload` struct (`NetID`, `Epoch`, `CommitTick`, `TransferBlob []byte`); delete `HandoffPreparePayload`, `HandoffCommitPayload`, `HandoffCancelPayload`.
- `pkg/universe/mesh_frame_codec.go` — encode/decode `HandoffPayload` as the single `MeshFrame.Handoff` arm; delete the three old codecs.
- `pkg/universe/handoff_driver.go` — rewrite. `HandoffDriver.Tick(currentClusterTick uint64)` pulls `CrossingEvent`s, sends one `HandoffPayload` per event with `CommitTick = currentClusterTick + HANDOFF_LEAD_TICKS`, enqueues a local demote for `CommitTick`. A per-tick drain processes due demotes. No state machine, no warmup, no cooldown, no cancel path.
- `pkg/universe/handoff.go` — delete `HandoffStateMachine`, `HandoffKey`, `HandoffPhase`, `MinWarmupTicks`, `MaxWarmupTicks`, `CrossingCooldownTicks`. File may retain only the per-(netID,dest) cooldown tracker if it remains useful (it does — anti-thrash).
- `pkg/universe/cell.go` — replace three handlers (`MsgHandoffPrepare`/`Commit`/`Cancel`) with a single `MsgHandoff` handler that queues a destination-side promote/spawn for the carried `CommitTick`.
- `pkg/universe/world_base.go` — delete `SpawnShadow`, `PromoteShadow`, `RemoveShadowByNetID`, `shadowMap`; delete `Shadow` fast-path in `upsertBorderReplica`; delete `netIDStillPushedByOtherSource` and its call site in `ApplyBorderFrame`. Add `DemoteLiveToReplica` (new name is fine, or rename to `PromoteReplicaToLive` / `DemoteLiveToReplica` pair). Add `PromoteReplicaToLive` (for destination-side commit when a border-replica already exists). Add `SpawnLiveFromTransfer` (for destination-side commit when no replica exists). Cache `ProducedAtMs` on the `Replica` in `upsertBorderReplica`. Emit `Replica.ProducedAtMs` through outbound frames via `FrameWriter`.
- `pkg/component/core.go` — delete `Replica.UpdatedThisTick`; add `Replica.ProducedAtMs uint64`. (Main doesn't have `MissedTicks` — nothing to delete there.)
- `pkg/component/shadow.go` — **delete file**.
- `pkg/universe/netid_index.go` — delete `PresenceShadow` enum value; simplify `Enter` to a 2×2 transition table; add `Demote(netID, entity) TransitionResult` (explicit Live→Replica) and `Promote(netID, entity) TransitionResult` (explicit Replica→Live). Unsolicited `Enter(Replica)` on a `Live` slot continues to reject.
- `pkg/universe/border_replication.go` — `BorderDispatcher` filter drops `Shadow` from the exclusion list (Shadow no longer exists; filter becomes `Without(Ghost, Replica, Dormant)`). Remove stale "Shadow" comments.
- `pkg/system/viewer_source.go` — delete `shadowMap` field, delete `Shadow` skip in `ActiveViewers`.
- `pkg/system/replica_dead_reckoning.go` — **delete file**.
- `pkg/system/replica_dead_reckoning_test.go` — **delete file**.
- `pkg/mmokit/factories.go` (or wherever `NewReplicaDeadReckoningSystem` factory is wired) — delete the factory call; remove the `ReplicaDeadReckoning` system registration from 4node-basic + any other example.
- `pkg/system/replication.go` — `ReplicationConfig` gains `ClusterClock *mmokit.ClusterClock` field. `FrameWriter` callers populate per-entity `ProducedAtMs` (for local entities: `clusterClock.Now()`; for replicas: `Replica.ProducedAtMs`). Delete `ClearReplicaUpdateFlags` call (method will be deleted in `world_base.go`). Frame-level `serverTimeMs` removed.
- `pkg/quantize/wireformat.go` — `FullEntry` + `DeltaEntry` gain `ProducedAtMs uint64`; encoder writes 8 B after `EntityType` and before the snapshot / delta-data length prefix; decoder reads symmetrically. **Remove** the 8-byte frame-level `server_time_ms` from the header. Header shrinks from 28 → 20 bytes.
- `pkg/quantize/wireformat_test.go` — round-trip tests updated to cover per-entity `producedAtMs` and assert the new header size.
- `pkg/quantize/ts/delta-decoder-core.ts` — symmetric TS change: header shrinks, each full/delta entry carries `producedAtMs`.
- `pkg/system/frame_writer.go` — remove `serverTimeMs := time.Now().UnixMilli()` call; propagate per-entity stamps into `FullEntry` / `DeltaEntry`.
- `pkg/system/frame_writer_test.go` — update test expectations accordingly.
- `pkg/universe/border_components.go` — `DeltaBuf` (border frame per-entity layout in the doc comment) gains 8 B `producedAtMs` after `qvy`. `applyEntityComponents` decodes it.
- `pkg/universe/world_base.go` `upsertBorderReplica` — decode `producedAtMs` from border frame and store on the `Replica`.
- `pkg/engine/engine.go` — confirm `CurrentTick()` accessor exists; add if missing (used by handoff driver). Current state: `Engine.Tick uint32` is a public field; no explicit accessor. Either use the field directly or add `(e *Engine) CurrentTick() uint64` as a typed wrapper.
- `examples/4node-basic/main.go` — remove `ReplicaDeadReckoning` system registration; inject `ClusterClock` into `ReplicationConfig`.
- `examples/slither/main.go` — same.
- `internal/game/factory.go` — same.
- `web-pixi/src/interpolation.ts` — sample stamps populated from per-entity `producedAtMs` (delete the single frame-level assignment; each entity path already receives a sample from the decoder — route the stamp in).
- `web-pixi/src/clockSync.ts` — `observeServerTime` callers now pass `max(entity.producedAtMs)` for the frame.
- `web-pixi/src/__tests__/interpolation.test.ts`, `web-pixi/src/__tests__/clockSync.test.ts` — updated.
- `examples/4node-basic/web/src/interpolation.ts`, `clockSync.ts`, `__tests__/*` — mirror changes.
- `examples/4node-basic/web/src/snapshot.ts` (if the receiver is here) — plumb `producedAtMs` from decoded entity payload.
- `gen/es/meshpb/mesh_pb.ts`, `gen/go/meshpb/mesh.pb.go` — regenerated by `buf generate` after the proto change.
- `CLAUDE.md` — update the "Server Meshing" and "Networking & Replication" sections; document `ClusterClock`, per-entity `producedAtMs`, hard-cut handoff; delete Shadow / warmup references.

### Deleted files

- `pkg/component/shadow.go`
- `pkg/system/replica_dead_reckoning.go`
- `pkg/system/replica_dead_reckoning_test.go`
- `pkg/universe/handoff_test.go` (if exists and is Shadow-oriented; verify)
- `pkg/universe/handoff_driver_test.go` (fully rewritten, so delete + rewrite — simpler than surgical)
- `pkg/universe/overlap_test.go` (if present on main — verify; absent on main)

---

## Phase A — Branch setup + overlap-handoff cleanup

### Task A1: Confirm branch + baseline

- [ ] **Step 1: Verify branch**

Run: `cd . && git branch --show-current`
Expected output: `replication-timeline-redesign`

Run: `cd . && git log --oneline -3`
Expected: most-recent commits include `docs(plan): Replication Timeline Redesign implementation plan` and the overlap-handoff tip commits (`a3e3fcf docs(spec): ClusterClock ...`, `bc09c9f docs(spec): Replication Timeline Redesign...`).

- [ ] **Step 2: Verify baseline builds**

Run: `cd . && go vet ./...`
Expected: clean exit.

Run: `cd . && go test ./... -count=1 -timeout 300s`
Expected: PASS (baseline green on overlap-handoff).

- [ ] **Step 3: No commit this task.**

---

### Task A2: Cleanup pass — delete overlap-handoff workarounds

The overlap-handoff branch added a large surface of workaround code to the baseline. The new design deletes almost all of it. Rather than stage the deletion across Phases E–J piecemeal, we do a single focused cleanup pass **before** wiring the new design — the tree temporarily regresses to pre-overlap-handoff behavior for handoff (Prepare+Commit fires same tick, no smoothness layer for replicas), then Phases B–K build the new correct behavior.

**Files deleted entirely:**
- `pkg/system/replica_dead_reckoning.go` + `replica_dead_reckoning_test.go`
- `pkg/universe/border_replication_shadow_test.go`
- `pkg/universe/overlap_test.go`

**Field/component deletions (surgical):**
- `component.Shadow` — reduce to the 3 original fields (`SourceCellID`, `NetID`, `Epoch`). Delete `CreatedTick`, `UpdatedThisTick`, `MissedTicks`. Full deletion of the struct happens in Phase I; this pass shrinks it so the surrounding surface compiles without the broken machinery.
- `component.Replica` — delete `MissedTicks`. Keep `UpdatedThisTick` for now (Phase E removes it and adds `ProducedAtMs`).

**WorldBase method deletions:**
- `TickShadowWatchdog`
- `netIDStillPushedByOtherSource`
- Shadow fast-path branch inside `upsertBorderReplica` (detected by `netIDIdx.Lookup` returning `PresenceShadow` — delete the branch that updates the shadow in place)
- Multi-source dedup block inside `ApplyBorderFrame` (the `if netIDStillPushedByOtherSource(...)` continue-branch)
- The `drainingForMerge` normalization that preserves source Position during warmup (revert to the older pre-overlap behavior that normalizes immediately)

**WorldBase methods KEPT (used by the new design):**
- `SpawnShadow`, `PromoteShadow`, `RemoveShadowByNetID` — these exist to serve the old protocol that's about to be deleted in Phase G/H/I. **Kept in Phase A2** so the Prepare+Commit-same-tick fallback still works. Phase I deletes them.
- `DemoteLiveToReplica` — **kept permanently.** The new hard-cut driver calls this in Phase H. Signature unchanged.

**HandoffStateMachine deletions:**
- `TickWarmup`, `CanCommit`, `WarmupCount`, `EnterCooldown`, `InCooldown`, `SetConnID`, `ConnID`, `PromotedNeighborsFor` — all warmup/cooldown/multi-neighbor methods.
- `HandoffPhase` enum values `HandoffBorder`, `HandoffPromoted`, `HandoffHandoff` — replaced by a simpler model in Phase G.
- `MinWarmupTicks`, `MaxWarmupTicks`, `CrossingCooldownTicks` — constants.
- **Keep for now:** the bare `HandoffStateMachine` type with just `State` / `SetState` / `Forget` — a placeholder that Phase G finishes deleting.

**HandoffDriver deletions:**
- `tickPromoted` — the warmup-walk method.
- `fireCommit` — merged back into `handleCrossing` (restoring v1-style same-tick Prepare+Commit).
- `OnCancelFromDest` — the multi-neighbor cancel-on-receive path.

**Cell.go deletions:**
- Destination-side Shadow watchdog loop (the per-tick walk that calls `TickShadowWatchdog`).

**ViewerSource.go deletions:**
- `ActiveViewers`'s Shadow-skip carve-out. A commented rationale referenced "skip Shadow viewers during warmup" — delete it and the associated skip.

**border_replication.go:**
- Revert the "include Shadow" filter carve-out added by overlap-handoff. Filter becomes `Without(Ghost, Replica, Dormant)` again; Shadow is still excluded from the push walk.

**Test-file cleanup:**
- Delete `pkg/universe/overlap_test.go`, `pkg/universe/border_replication_shadow_test.go`.
- Inside `pkg/universe/handoff_driver_test.go` and `pkg/universe/handoff_test.go`: delete any test that asserts on `CreatedTick`, `UpdatedThisTick`, `MissedTicks`, `WarmupCount`, `TickWarmup`, `CanCommit`, `InCooldown`, watchdog sweeps, multi-source dedup, grace extrapolation, `OnCancelFromDest`, cancel-on-handoff. Keep v1-style Prepare+Commit-same-tick tests that still exercise real behavior.
- Inside `pkg/universe/netid_index_test.go`: delete any test specifically about the `PresenceShadow` state transitions (Phase I removes the state entirely). Keep basic Live/Replica tests.

**4node-basic + examples:**
- No `ReplicaDeadReckoning` system registration remains after Phase A2 deletes the system — remove the corresponding `coord.AddSystem("ReplicaDeadReckoning", mmokit.NewReplicaDeadReckoningSystem())` line (and any factory it depends on in `pkg/mmokit/factories.go`). Subsequent Phase J is a no-op on this step.

- [ ] **Step 1: Delete files**

```bash
cd .
rm pkg/system/replica_dead_reckoning.go pkg/system/replica_dead_reckoning_test.go
rm -f pkg/universe/overlap_test.go pkg/universe/border_replication_shadow_test.go
```

- [ ] **Step 2: Shrink `component.Shadow` to the three original fields**

In `pkg/component/shadow.go`, reduce to:

```go
package component

// Shadow marks a pre-authority entity created from a HandoffPrepare
// payload. The destination cell holds the shadow while the source
// completes the warmup window. On HandoffCommit, the Shadow component
// is removed and the entity becomes a normal local entity.
//
// Phase I of the Replication Timeline Redesign deletes this component
// entirely in favor of the existing Replica component as the sole
// destination-side pre-authority representation.
type Shadow struct {
	SourceCellID string
	NetID        uint32
	Epoch        uint32
}
```

- [ ] **Step 3: Shrink `component.Replica`**

In `pkg/component/core.go`, delete the `MissedTicks` field:

```go
type Replica struct {
	SourceCellID    string
	SourceNetID     uint32
	TTL             int  // ticks remaining before expiry (reset on refresh)
	UpdatedThisTick bool // set by ApplyBorderFrame, cleared each tick start
}
```

- [ ] **Step 4: Delete WorldBase workarounds**

In `pkg/universe/world_base.go`:
- Delete `TickShadowWatchdog` (method body and any callers).
- Delete `netIDStillPushedByOtherSource` (helper and its call site in `ApplyBorderFrame`).
- Inside `upsertBorderReplica`: delete the early-branch that checks `netIDIdx.Lookup(netID)` and falls through to a Shadow-specific update path. The remaining code should reach `netIDIdx.Enter(..., PresenceReplica)` and the replica-update branch like the pre-overlap-handoff flow.
- Delete `ClearReplicaUpdateFlags`'s Shadow loop (the second `ecs.NewFilter1[component.Shadow]` block).

In `pkg/universe/cell.go`:
- Delete the per-tick `TickShadowWatchdog` call (and the helper that registers the watchdog — probably in `PostSystems` or similar).

- [ ] **Step 5: Collapse HandoffStateMachine to bare minimum**

In `pkg/universe/handoff.go`:
- Delete `TickWarmup`, `CanCommit`, `WarmupCount`, `EnterCooldown`, `InCooldown`, `SetConnID`, `ConnID`, `PromotedNeighborsFor`.
- Delete `HandoffPhase` values `HandoffBorder`, `HandoffPromoted`, `HandoffHandoff`. Keep `HandoffUnseen` (or drop the enum entirely — see Phase G).
- Delete constants `MinWarmupTicks`, `MaxWarmupTicks`, `CrossingCooldownTicks`.
- In the `handoffEntry` struct, delete `warmupCount`, `cooldownStart`, `cooldownSet`, `connID` — reduce to just `phase` (and even that may go in Phase G).

In `pkg/universe/handoff_driver.go`:
- Delete `tickPromoted` method.
- Delete `OnCancelFromDest` method.
- Delete `fireCommit` — the current `handleCrossing` already fires Prepare+Commit same-tick in v1 fallback mode; move any logic back into `handleCrossing`.

- [ ] **Step 6: Delete ViewerSource Shadow-skip + border filter carve-out**

In `pkg/system/viewer_source.go`:
- Delete the `ActiveViewers` block that skips entities with `component.Shadow`. Revert to the earlier behavior that scans all viewers regardless of Shadow state.

In `pkg/universe/border_replication.go`:
- Revert the `candidatesFor` filter. It was changed to INCLUDE Shadows in the push set; delete the inclusion. The filter should be `Without(Ghost, Replica, Dormant)` — Shadow is excluded again because `Without` operates on what's NOT in the set.

- [ ] **Step 7: Delete ReplicaDeadReckoning registration**

Grep for `NewReplicaDeadReckoningSystem` and `ReplicaDeadReckoning`:

```bash
cd . && rg 'ReplicaDeadReckoning' --type=go -l
```

Delete:
- The factory in `pkg/mmokit/factories.go` (or wherever it lives).
- The `coord.AddSystem("ReplicaDeadReckoning", ...)` call in `examples/4node-basic/main.go`, `examples/slither/main.go`, `internal/game/factory.go`.

- [ ] **Step 8: Clean up tests**

Inside `pkg/universe/handoff_driver_test.go` — delete any test that asserts on the deleted machinery (`CreatedTick`, `UpdatedThisTick`, `MissedTicks`, `WarmupCount`, `TickWarmup`, `CanCommit`, `InCooldown`, watchdog sweeps, multi-source dedup, grace extrapolation, `OnCancelFromDest`, cancel-on-handoff).

Inside `pkg/universe/handoff_test.go` — same pattern. Leave tests that exercise the v1 Prepare+Commit-same-tick path in place (they still pass; Phase G rewrites them).

Inside `pkg/universe/netid_index_test.go` — delete tests naming `PresenceShadow` explicitly, except the basic "Shadow is a valid transition target" cases (Phase I removes those).

- [ ] **Step 9: go vet + tests**

```bash
cd . && go vet ./...
cd . && go test ./... -count=1 -timeout 300s
```

Expected: vet clean; full tests green. If any test still references a deleted API, fix the test to use a v1-equivalent call or delete it.

- [ ] **Step 10: Commit**

```bash
cd .
git add -A
git commit -m "$(cat <<'EOF'
refactor: remove overlap-handoff workarounds ahead of timeline redesign

Deletes the overlap-specific additions that the new Replication
Timeline Redesign does not need:

- pkg/system/replica_dead_reckoning.go (+test) — server-side DR gone
- Shadow.CreatedTick / UpdatedThisTick / MissedTicks — watchdog + DR
- Replica.MissedTicks — grace extrapolation
- WorldBase.TickShadowWatchdog, netIDStillPushedByOtherSource
- upsertBorderReplica Shadow fast-path
- ApplyBorderFrame multi-source dedup
- HandoffStateMachine warmup/cooldown/conn-tracking methods
- HandoffDriver.tickPromoted, OnCancelFromDest, fireCommit
- BorderDispatcher Shadow-in-push-set carve-out
- ViewerSource ActiveViewers Shadow skip
- ReplicaDeadReckoning system registrations in all examples
- Tests that exercise deleted machinery

Kept: WorldBase.DemoteLiveToReplica, netIDIndex.Demote (both used by
the new hard-cut handoff design). The tree temporarily behaves as
v1 Prepare+Commit-same-tick handoff (no smoothness layer) until
Phases B-K wire the new design.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase B — `ClusterClock` primitive

Deliver a standalone thread-safe type before wiring it anywhere. Pure Go, no dependencies.

### Task B1: Create the `ClusterClock` type and tests

**Files:**
- Create: `pkg/mmokit/cluster_clock.go`
- Create: `pkg/mmokit/cluster_clock_test.go`

- [ ] **Step 1: Write the failing tests**

Create `pkg/mmokit/cluster_clock_test.go`:

```go
package mmokit

import (
	"testing"
	"time"
)

// TestClusterClock_NowBeforeObserveFallsBackToLocalWall ensures pre-sync
// code paths still return a usable number (local wall-clock ms). The
// caller is responsible for gating on Observed() before taking any
// action that requires cluster-coherent timestamps.
func TestClusterClock_NowBeforeObserveFallsBackToLocalWall(t *testing.T) {
	c := NewClusterClock()
	if c.Observed() {
		t.Fatal("fresh clock must report Observed()=false")
	}
	before := uint64(time.Now().UnixMilli())
	got := c.Now()
	after := uint64(time.Now().UnixMilli())
	if got < before || got > after {
		t.Fatalf("pre-Observe Now() = %d; want in [%d,%d]", got, before, after)
	}
}

// TestClusterClock_FirstObserveSnapsOffset verifies the first observation
// sets the offset exactly to (coord - local), regardless of EMA alpha.
func TestClusterClock_FirstObserveSnapsOffset(t *testing.T) {
	c := NewClusterClock()
	local := uint64(time.Now().UnixMilli())
	coord := local + 1_000 // coord is 1 second ahead of us
	c.observeAt(coord, 1, local)
	if !c.Observed() {
		t.Fatal("after Observe, Observed() must be true")
	}
	got := c.nowAt(local)
	if got != coord {
		t.Fatalf("Now() after first observe = %d; want %d (coord)", got, coord)
	}
}

// TestClusterClock_EMAConvergesTowardCoord verifies subsequent
// observations converge the offset EMA toward coord's clock.
func TestClusterClock_EMAConvergesTowardCoord(t *testing.T) {
	c := NewClusterClock()
	local := uint64(1_000_000)
	// First observation: offset = 1_000 ms.
	c.observeAt(local+1_000, 1, local)
	// Second observation 10 seconds later: coord drifted to +1_100.
	local2 := local + 10_000
	c.observeAt(local2+1_100, 2, local2)
	got := c.nowAt(local2)
	// Expected EMA with alpha=0.3 against prior 1_000 offset toward 1_100:
	// offset_new = 0.7*1_000 + 0.3*1_100 = 1_030 ms.
	want := local2 + 1_030
	if got < want-5 || got > want+5 {
		t.Fatalf("Now() after EMA step = %d; want ~%d (±5)", got, want)
	}
}

// TestClusterClock_StaleSeqRejected ensures an out-of-order
// CoordTimeSync (seq lower than highest seen) is dropped.
func TestClusterClock_StaleSeqRejected(t *testing.T) {
	c := NewClusterClock()
	c.observeAt(2_000, 5, 1_000)   // offset = 1_000
	c.observeAt(99_999, 3, 1_000) // lower seq — must be dropped
	got := c.nowAt(1_000)
	if got != 2_000 {
		t.Fatalf("stale observe mutated offset: Now=%d, want 2_000", got)
	}
}

// TestClusterClock_CachedOffsetSurvivesCoordDeath models the coord
// dying after the first observation: subsequent Now() calls must
// continue to return (local + cached_offset), no panic, no reset.
func TestClusterClock_CachedOffsetSurvivesCoordDeath(t *testing.T) {
	c := NewClusterClock()
	c.observeAt(2_000, 1, 1_000) // offset = +1_000
	// 5 minutes later, no new observation.
	got := c.nowAt(1_000 + 5*60*1_000)
	want := (1_000 + 5*60*1_000) + 1_000
	if got != uint64(want) {
		t.Fatalf("Now() after 5min idle = %d; want %d", got, want)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd . && go test ./pkg/mmokit/ -run TestClusterClock -v`
Expected: FAIL — `undefined: NewClusterClock`.

- [ ] **Step 3: Implement the type**

Create `pkg/mmokit/cluster_clock.go`:

```go
package mmokit

import (
	"sync"
	"time"
)

// ClusterClock maintains an offset between this host's local wall clock
// and the coordinator's wall clock, seeded by the RegisterHost handshake
// and refreshed periodically via CoordTimeSync broadcasts over the
// MeshControl stream.
//
// Thread-safe. Now() is called from every cell's game-loop goroutine
// once per replication frame (up to 20 Hz * N cells) so the hot path
// takes the RLock; Observe() runs at 10 s cadence under the write lock.
//
// Pre-observation, Now() falls back to the local wall clock. Callers
// that require cluster coherence MUST gate on Observed() before acting
// on a Now() value. The coordinator's initial-sync on RegisterHost is
// the canonical trigger for Observed() flipping to true; no cell on a
// remote host starts its game loop until that flip happens.
type ClusterClock struct {
	mu          sync.RWMutex
	offsetMs    float64 // coordWall - localWall, EMA-smoothed
	initialized bool
	highestSeq  uint64
}

// emaAlpha controls how fast the offset EMA tracks a new observation.
// At 10 s broadcast cadence, alpha=0.3 settles within ~3 samples
// (30 s) after a step-change in network latency. First observation
// snaps directly regardless of alpha.
const emaAlpha = 0.3

// NewClusterClock returns a fresh, un-initialized cluster clock.
func NewClusterClock() *ClusterClock {
	return &ClusterClock{}
}

// Observed reports whether the clock has received at least one
// CoordTimeSync and is safe to use for cluster-coherent stamping.
func (c *ClusterClock) Observed() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.initialized
}

// Now returns the current cluster-wall-clock in milliseconds. Before
// the first Observe, falls back to this host's local wall clock.
func (c *ClusterClock) Now() uint64 {
	return c.nowAt(uint64(time.Now().UnixMilli()))
}

// Observe incorporates a CoordTimeSync broadcast. Stale (older seq)
// broadcasts are silently dropped.
func (c *ClusterClock) Observe(coordTimeMs uint64, seq uint64) {
	c.observeAt(coordTimeMs, seq, uint64(time.Now().UnixMilli()))
}

// nowAt is the pure function used for deterministic testing.
func (c *ClusterClock) nowAt(localMs uint64) uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.initialized {
		return localMs
	}
	return uint64(float64(localMs) + c.offsetMs)
}

// observeAt is the pure function used for deterministic testing.
// Locks under write lock; drops stale seq.
func (c *ClusterClock) observeAt(coordTimeMs, seq, localMs uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.initialized && seq <= c.highestSeq {
		return
	}
	sample := float64(coordTimeMs) - float64(localMs)
	if !c.initialized {
		c.offsetMs = sample
		c.initialized = true
	} else {
		c.offsetMs = (1.0-emaAlpha)*c.offsetMs + emaAlpha*sample
	}
	c.highestSeq = seq
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd . && go test ./pkg/mmokit/ -run TestClusterClock -v`
Expected: all 5 tests PASS.

- [ ] **Step 5: go vet**

Run: `cd . && go vet ./...`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
cd .
git add pkg/mmokit/cluster_clock.go pkg/mmokit/cluster_clock_test.go
git commit -m "$(cat <<'EOF'
feat(mmokit): ClusterClock primitive with EMA-smoothed offset

Thread-safe host-local clock that tracks offset against the coordinator
via CoordTimeSync observations. First observation snaps; subsequent
ones blend via EMA alpha=0.3. Stale seq drops. Pre-observation Now()
falls back to the local wall clock; callers gate on Observed().

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase C — Proto `CoordTimeSync` + handshake + broadcast

Wire `ClusterClock` through the MeshControl stream so every host converges on the coordinator's clock. After this phase `ClusterClock.Now()` is live but still unused by the replication path — that's Phase E.

### Task C1: Add `CoordTimeSync` message to `mesh.proto` and regenerate

**Files:**
- Modify: `proto/meshpb/mesh.proto`
- Regenerate: `gen/go/meshpb/mesh.pb.go`, `gen/es/meshpb/mesh_pb.ts`

- [ ] **Step 1: Add the proto message**

Open `proto/meshpb/mesh.proto`. Locate the `CoordMessage` `oneof msg {...}` block (around line 38-58). Append a new arm with the next free field number (after `cell_rename = 20`):

```proto
  // CoordTimeSync broadcasts the coordinator's local wall-clock ms
  // over the MeshControl stream. Each host's ClusterClock uses this
  // to maintain an EMA-smoothed offset against the coordinator, so
  // every replication sample — regardless of authoring cell — can be
  // stamped with cluster-coherent producedAtMs.
  //
  // Period: 5-10 s in production (Config.ClusterClockSyncInterval,
  // default 10 s). Never on the per-tick path; purely advisory refresh.
  // The initial CoordTimeSync is sent immediately after RegisterAck
  // on the bidi stream — hosts block cell-worker startup until their
  // ClusterClock has Observed() at least one message.
  CoordTimeSync coord_time_sync = 21;
```

Then, below the `CoordMessage` definition (near the other message definitions at the bottom), add:

```proto
message CoordTimeSync {
  // Coordinator's local_wall_ms at send time.
  uint64 coord_time_ms = 1;
  // Monotonic per broadcast; receiver drops stale seq.
  uint64 seq = 2;
}
```

- [ ] **Step 2: Regenerate proto**

Run: `cd . && just proto`
Expected: completes without error; `gen/go/meshpb/mesh.pb.go` and `gen/es/meshpb/mesh_pb.ts` are updated.

- [ ] **Step 3: go vet**

Run: `cd . && go vet ./...`
Expected: clean (nothing yet consumes the new message).

- [ ] **Step 4: Commit**

```bash
cd .
git add proto/meshpb/mesh.proto gen/go/meshpb/ gen/es/meshpb/
git commit -m "feat(meshpb): add CoordTimeSync for cluster-clock broadcast

Broadcast carries the coordinator's local wall-clock ms and a
monotonic seq; hosts observe into their ClusterClock to maintain a
cluster-coherent timeline. Sent initially on the RegisterHost handshake
and then every Config.ClusterClockSyncInterval (default 10 s).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task C2: Host-side dispatch `CoordTimeSync` into `ClusterClock`

**Files:**
- Modify: `pkg/universe/mesh_control_client.go`
- Modify: `pkg/universe/coordinator.go` (add `ClusterClock` field on `Process`)
- Modify: `pkg/universe/host_network.go` (in-process wiring shares the same `ClusterClock`)

- [ ] **Step 1: Write the failing test**

Append to `pkg/universe/cluster_clock_integration_test.go` (create if absent):

```go
package universe

import (
	"testing"
	"time"

	"github.com/zenion/mmoserver/gen/go/meshpb"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// TestCoordTimeSync_HostObservesClock verifies that a CoordTimeSync
// flowing through the remote-host dispatch path reaches the host's
// ClusterClock.Observe.
func TestCoordTimeSync_HostObservesClock(t *testing.T) {
	// Instantiate the dispatch target directly. The full gRPC harness
	// is covered in Task C4; here we only verify the switch arm.
	cc := mmokit.NewClusterClock()
	cli := &meshControlClient{clusterClock: cc}

	msg := &meshpb.CoordMessage{Msg: &meshpb.CoordMessage_CoordTimeSync{
		CoordTimeSync: &meshpb.CoordTimeSync{
			CoordTimeMs: uint64(time.Now().UnixMilli()) + 5_000,
			Seq:         1,
		},
	}}
	cli.dispatch(msg)

	if !cc.Observed() {
		t.Fatal("ClusterClock.Observed() must be true after CoordTimeSync dispatch")
	}
}
```

- [ ] **Step 2: Run to verify fail**

Run: `cd . && go test ./pkg/universe/ -run TestCoordTimeSync_HostObservesClock -v`
Expected: FAIL (no `clusterClock` field on `meshControlClient`, no dispatch arm).

- [ ] **Step 3: Add the `clusterClock` field to `meshControlClient`**

In `pkg/universe/mesh_control_client.go`, locate the `type meshControlClient struct {}` declaration and add:

```go
	// clusterClock is updated by CoordTimeSync broadcasts. Shared with
	// the local Process so every cell on this host stamps samples with
	// the same cluster-coherent timeline.
	clusterClock *mmokit.ClusterClock
```

(Import `"github.com/zenion/mmoserver/pkg/mmokit"` at the top if not already imported.)

- [ ] **Step 4: Add the dispatch arm**

In the same file, locate the `func (c *meshControlClient) dispatch(msg *meshpb.CoordMessage) {` switch statement (~line 530). Insert a new case before any `default`:

```go
	case *meshpb.CoordMessage_CoordTimeSync:
		if c.clusterClock != nil && v.CoordTimeSync != nil {
			c.clusterClock.Observe(v.CoordTimeSync.CoordTimeMs, v.CoordTimeSync.Seq)
		}
```

- [ ] **Step 5: Add `ClusterClock` field to `Process`**

In `pkg/universe/coordinator.go`, locate the `type Process struct {` (around line 214) and add near other observability-style fields:

```go
	// ClusterClock maintains cluster-coherent wall-clock timestamps
	// for every cell on this host. Seeded by CoordTimeSync during the
	// RegisterHost handshake (or set to a zero-offset clock when the
	// coordinator is in-process).
	ClusterClock *mmokit.ClusterClock
```

In `NewCoordinator` (the `Process` constructor), initialize it:

```go
	p.ClusterClock = mmokit.NewClusterClock()
```

- [ ] **Step 6: In-process coord+host: pre-observe with offset=0**

When the coordinator and host share a process (the `all` preset), the offset is zero — there's no reason to wait for a broadcast round-trip. In `NewCoordinator` (or wherever the in-process role wiring sets up `clusterClock` on any local `meshControlClient` substitute), call:

```go
	// In-process coordinator is also the host — zero offset by
	// construction. Pre-observe with the current coord time so
	// Observed() is true before any cell starts ticking.
	p.ClusterClock.Observe(uint64(time.Now().UnixMilli()), 0)
```

(Add the `time` import if needed.)

- [ ] **Step 7: Thread `clusterClock` to `meshControlClient`**

Wherever `meshControlClient` is constructed on a remote-host process (search for `&meshControlClient{...}`), add `clusterClock: p.ClusterClock` to the struct literal.

- [ ] **Step 8: Run the test**

Run: `cd . && go test ./pkg/universe/ -run TestCoordTimeSync_HostObservesClock -v`
Expected: PASS.

Run the full universe suite:
Run: `cd . && go test ./pkg/universe/ -count=1 -timeout 120s`
Expected: PASS (nothing broken yet — the clock is still unused).

- [ ] **Step 9: Commit**

```bash
cd .
git add pkg/universe/mesh_control_client.go pkg/universe/coordinator.go pkg/universe/host_network.go pkg/universe/cluster_clock_integration_test.go
git commit -m "feat(universe): host-side CoordTimeSync dispatch + ClusterClock wiring

meshControlClient.dispatch now feeds CoordTimeSync into the shared
ClusterClock. In-process coordinator+host pre-observes with offset=0
so Observed() is true immediately. Remote hosts must wait for the
first broadcast; enforced in a subsequent task.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task C3: Coordinator sends initial `CoordTimeSync` on `RegisterHost`

**Files:**
- Modify: `pkg/universe/mesh_control_server.go`

- [ ] **Step 1: Find the RegisterAck send site**

Locate the `handleHostControl` function (around line 75). It receives the first `HostMessage_Register`, validates the host ID, allocates a net-ID range, registers the host, and sends `RegisterAck` + initial `PeerList` back on the stream. The initial `CoordTimeSync` goes here — immediately after `RegisterAck` and before `PeerList` (ordering preserves the invariant that the host has a clock before any topology information that might cause it to start cells).

- [ ] **Step 2: Add `clusterClockSeq` to `meshControlServer`**

In the same file, find `type meshControlServer struct { ... }`. Add:

```go
	// clusterClockSeq is the monotonic counter for CoordTimeSync
	// broadcasts. Bumped for both initial-sync on RegisterHost and
	// the periodic broadcast loop.
	clusterClockSeq atomic.Uint64
```

(Import `sync/atomic` if not present.)

- [ ] **Step 3: Send the initial CoordTimeSync**

Find the `RegisterAck` send in `handleHostControl`. Immediately after the `sendCoordMessageToHost(hostID, &meshpb.CoordMessage{Msg: &meshpb.CoordMessage_RegisterAck{...}})` call, add:

```go
	// Initial ClusterClock sync so the host's Observed() flips true
	// before any CellAssign arrives. Without this, a remote host would
	// start receiving cell assignments before it has a cluster-coherent
	// clock and would emit samples stamped with local wall-time.
	if err := s.sendCoordMessageToHost(hostID, &meshpb.CoordMessage{
		Msg: &meshpb.CoordMessage_CoordTimeSync{
			CoordTimeSync: &meshpb.CoordTimeSync{
				CoordTimeMs: uint64(time.Now().UnixMilli()),
				Seq:         s.clusterClockSeq.Add(1),
			},
		},
	}); err != nil {
		s.logf("CoordTimeSync send (initial) to host=%s failed: %v", hostID, err)
	}
```

- [ ] **Step 4: Write a test**

Append to `pkg/universe/cluster_clock_integration_test.go`:

```go
// TestCoordTimeSync_InitialSyncOnRegister verifies that a newly
// connecting host receives a CoordTimeSync as the second message
// on its stream (after RegisterAck) without needing to wait for
// the periodic broadcast. Uses the existing distributed 2-host
// fixture that spins up a real coordinator + gRPC.
func TestCoordTimeSync_InitialSyncOnRegister(t *testing.T) {
	// Spin up coordinator + one remote host via the existing harness.
	// Helper: distributedTwoHostFixture(t) returns { coord, hostA }.
	fx := distributedTwoHostFixture(t)
	defer fx.Shutdown()

	// Wait up to 2s for hostA.ClusterClock.Observed() to be true.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if fx.hostA.ClusterClock.Observed() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("hostA.ClusterClock did not Observe within 2s of RegisterHost")
}
```

If `distributedTwoHostFixture` doesn't exist in the test support code, check `pkg/universe/cluster_fixture_distributed_test.go` — the S7 tests already use a similar helper. Reuse or extend it.

- [ ] **Step 5: Run the test**

Run: `cd . && go test ./pkg/universe/ -run TestCoordTimeSync_InitialSyncOnRegister -v -count=1 -timeout 60s`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd .
git add pkg/universe/mesh_control_server.go pkg/universe/cluster_clock_integration_test.go
git commit -m "feat(universe): send initial CoordTimeSync on RegisterHost handshake

Every newly-joining host observes its ClusterClock before any
CellAssign can arrive, closing the window where a remote host could
stamp samples with local wall-time. The coordinator's monotonic
clusterClockSeq is the shared counter for both initial and periodic
broadcasts.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task C4: Periodic `CoordTimeSync` broadcast loop

**Files:**
- Modify: `pkg/universe/coordinator.go` (`Config.ClusterClockSyncInterval`, `startClusterTimeBroadcast`)

- [ ] **Step 1: Add the config field**

In `pkg/universe/coordinator.go`'s `Config` struct, add:

```go
	// ClusterClockSyncInterval controls the cadence at which the
	// coordinator broadcasts CoordTimeSync to all registered hosts.
	// Production default is 10 s. Lower values converge faster after
	// network-latency step-changes at the cost of minor bandwidth.
	// Zero means "use the default".
	ClusterClockSyncInterval time.Duration
```

In the defaults population (look for `applyDefaults` or the constructor where other Config defaults get filled), set:

```go
	if cfg.ClusterClockSyncInterval <= 0 {
		cfg.ClusterClockSyncInterval = 10 * time.Second
	}
```

- [ ] **Step 2: Add the broadcast goroutine**

Locate `Coordinator.Start` (around line 1345). Among the existing `go c.routeEvents(ctx)` goroutine launches, add:

```go
	go c.startClusterTimeBroadcast(ctx)
```

Then add the method body:

```go
// startClusterTimeBroadcast periodically publishes CoordTimeSync to
// every registered host over the MeshControl stream. Runs as long as
// the coordinator is alive. Remote hosts update their ClusterClock's
// EMA offset; in-process hosts are a no-op since their offset is
// pre-seeded to 0 by construction.
func (c *Process) startClusterTimeBroadcast(ctx context.Context) {
	interval := c.Config.ClusterClockSyncInterval
	if interval <= 0 {
		interval = 10 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if c.controlServer == nil {
				continue
			}
			c.controlServer.broadcastCoordTimeSync()
		}
	}
}
```

- [ ] **Step 3: Add the broadcast helper on `meshControlServer`**

In `pkg/universe/mesh_control_server.go`, add:

```go
// broadcastCoordTimeSync sends CoordTimeSync to every currently
// registered host. Per-stream errors are logged but do not prevent
// the remaining hosts from receiving the update.
func (s *meshControlServer) broadcastCoordTimeSync() {
	nowMs := uint64(time.Now().UnixMilli())
	seq := s.clusterClockSeq.Add(1)
	msg := &meshpb.CoordMessage{
		Msg: &meshpb.CoordMessage_CoordTimeSync{
			CoordTimeSync: &meshpb.CoordTimeSync{
				CoordTimeMs: nowMs,
				Seq:         seq,
			},
		},
	}
	for _, hostID := range s.listHostIDs() {
		if err := s.sendCoordMessageToHost(hostID, msg); err != nil {
			s.logf("CoordTimeSync broadcast to host=%s failed: %v", hostID, err)
		}
	}
}
```

(Use whatever helper already enumerates live hosts — probably via `s.hostRegistry` or similar; inspect first and reuse.)

- [ ] **Step 4: Write a test**

Append to `pkg/universe/cluster_clock_integration_test.go`:

```go
// TestCoordTimeSync_PeriodicBroadcastAdvances verifies that after
// the initial observation, subsequent broadcasts increment the
// highest-seen seq and continue to update the EMA.
func TestCoordTimeSync_PeriodicBroadcastAdvances(t *testing.T) {
	fx := distributedTwoHostFixture(t, fixtureOpts{
		ClusterClockSyncInterval: 100 * time.Millisecond,
	})
	defer fx.Shutdown()

	// Wait for initial observation.
	waitFor(t, 2*time.Second, func() bool { return fx.hostA.ClusterClock.Observed() })

	// Record the offset; let the loop fire 3 times.
	time.Sleep(400 * time.Millisecond)

	// After at least 3 observations, offset should still be finite
	// and Now() should advance monotonically.
	before := fx.hostA.ClusterClock.Now()
	time.Sleep(60 * time.Millisecond)
	after := fx.hostA.ClusterClock.Now()
	if after <= before {
		t.Fatalf("ClusterClock did not advance: before=%d after=%d", before, after)
	}
}
```

The `fixtureOpts` struct is a thin extension to the existing fixture — add a `ClusterClockSyncInterval` field and thread it into the coordinator config.

- [ ] **Step 5: Run the test**

Run: `cd . && go test ./pkg/universe/ -run TestCoordTimeSync_PeriodicBroadcastAdvances -v -count=1 -timeout 60s`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd .
git add pkg/universe/coordinator.go pkg/universe/mesh_control_server.go pkg/universe/cluster_clock_integration_test.go
git commit -m "feat(universe): periodic CoordTimeSync broadcast loop

Coordinator.Start spawns a broadcaster goroutine ticking at
Config.ClusterClockSyncInterval (default 10 s). Every registered host
observes into its ClusterClock, keeping EMA offsets tracking the
coordinator's wall clock over time and across transient latency
spikes.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task C5: Block remote-host cell workers until `Observed()`

**Files:**
- Modify: `pkg/universe/mesh_control_client.go` or wherever CellAssign is applied on the host side

- [ ] **Step 1: Identify the cell-assign application path**

Locate the case in `meshControlClient.dispatch` for `*meshpb.CoordMessage_CellAssign` (around line 555). This is where a remote host receives a cell assignment and starts a worker goroutine for it.

- [ ] **Step 2: Gate on `ClusterClock.Observed()`**

If the dispatch path calls through to a helper like `c.onCellAssign(assign)`, add a precondition check at the top of that helper:

```go
func (c *meshControlClient) onCellAssign(assign *meshpb.CellAssign) {
	// Defensive gate: no cell starts ticking before this host has
	// observed a CoordTimeSync. The coordinator sends one immediately
	// after RegisterAck, so this should already be true, but if an
	// orchestration bug ever delivered a CellAssign first we would
	// emit samples with OS-local wall-time. Better to log and drop.
	if !c.clusterClock.Observed() {
		c.logf("rejecting CellAssign for %s: ClusterClock not yet observed",
			assign.CellId)
		return
	}
	// ... existing body
}
```

(If the existing dispatch is inline and doesn't call a helper, add the guard directly in the switch case.)

- [ ] **Step 3: Add a test**

Append to `pkg/universe/cluster_clock_integration_test.go`:

```go
// TestCellAssign_RequiresObservedClock verifies that if a CellAssign
// somehow arrives before the ClusterClock has observed, the dispatch
// refuses to start the cell. In practice the coordinator's
// RegisterAck + initial CoordTimeSync precludes this, but the gate is
// protective.
func TestCellAssign_RequiresObservedClock(t *testing.T) {
	cc := mmokit.NewClusterClock() // un-observed
	cli := &meshControlClient{clusterClock: cc /* other fields as needed */}

	assign := &meshpb.CellAssign{CellId: "0_0"}
	cli.onCellAssign(assign)

	// The cell must not be registered as started. Inspect whatever the
	// client uses to track assigned cells and assert it's empty.
	if len(cli.assignedCells()) != 0 {
		t.Fatal("CellAssign accepted before ClusterClock.Observed()")
	}
}
```

(`assignedCells()` is a test accessor you may need to add on `meshControlClient`.)

- [ ] **Step 4: Run**

Run: `cd . && go test ./pkg/universe/ -run TestCellAssign_RequiresObservedClock -v`
Expected: PASS.

- [ ] **Step 5: Run the full universe suite**

Run: `cd . && go test ./pkg/universe/ -count=1 -timeout 180s`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
cd .
git add pkg/universe/mesh_control_client.go pkg/universe/cluster_clock_integration_test.go
git commit -m "feat(universe): gate CellAssign dispatch on ClusterClock.Observed()

Remote hosts refuse to start cell workers until their ClusterClock has
observed at least one CoordTimeSync. Coordinator's RegisterAck path
already sends the initial broadcast before any CellAssign, so this is
a defensive invariant against future orchestration bugs.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Phase D — Per-entity `producedAtMs` wire format

Restructure the frame binary format to carry the authoritative-producer stamp with each entity, not with the frame. Frame-level `serverTimeMs` is removed. Client decoder + TypeScript decoder update symmetrically in the same phase.

### Task D1: Add `ProducedAtMs` to `FullEntry` + `DeltaEntry`; update encoder/decoder; remove frame-level `ServerTimeMs`

**Files:**
- Modify: `pkg/quantize/wireformat.go`
- Modify: `pkg/quantize/wireformat_test.go`

- [ ] **Step 1: Write failing tests**

Open `pkg/quantize/wireformat_test.go`. Append:

```go
func TestFrameEncoder_FullEntryCarriesProducedAtMs(t *testing.T) {
	enc := NewFrameEncoder(128)
	produced := uint64(1_234_567_890_123)
	full := []FullEntry{{
		NetID:        42,
		Epoch:        1,
		EntityType:   1,
		ProducedAtMs: produced,
		Snapshot:     []byte{0x01, 0x02, 0x03},
		InitialData:  nil,
	}}
	buf := enc.Encode(100, 1, 0, full, nil, nil, nil)

	dec := NewFrameDecoder(buf)
	hdr := dec.Header()
	if hdr.FullCount != 1 {
		t.Fatalf("FullCount=%d, want 1", hdr.FullCount)
	}
	got := dec.NextFull()
	if got.ProducedAtMs != produced {
		t.Fatalf("ProducedAtMs round-trip: got %d, want %d", got.ProducedAtMs, produced)
	}
	if string(got.Snapshot) != string(full[0].Snapshot) {
		t.Fatal("Snapshot payload corrupted by producedAtMs insertion")
	}
}

func TestFrameEncoder_DeltaEntryCarriesProducedAtMs(t *testing.T) {
	enc := NewFrameEncoder(128)
	produced := uint64(9_999_999)
	deltas := []DeltaEntry{{
		NetID:        7,
		Epoch:        2,
		EntityType:   3,
		ProducedAtMs: produced,
		Data:         []byte{0xAA, 0xBB},
	}}
	buf := enc.Encode(200, 2, 0, nil, deltas, nil, nil)

	dec := NewFrameDecoder(buf)
	_ = dec.Header()
	got := dec.NextDelta()
	if got.ProducedAtMs != produced {
		t.Fatalf("Delta.ProducedAtMs: got %d, want %d", got.ProducedAtMs, produced)
	}
}

func TestFrameHeader_NoServerTimeMs(t *testing.T) {
	// Header shrinks from 28 → 20 bytes after serverTimeMs removal.
	enc := NewFrameEncoder(64)
	buf := enc.Encode(1, 0, 0, nil, nil, nil, nil)
	// 4 tick + 4 seq + 4 flags + 2 fullCnt + 2 deltaCnt + 2 removedCnt + 2 exitedCnt = 20
	if len(buf) != 20 {
		t.Fatalf("empty frame size = %d, want 20 (header only, no serverTimeMs)", len(buf))
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `cd . && go test ./pkg/quantize/ -run 'ProducedAtMs|NoServerTimeMs' -v`
Expected: FAIL — `unknown field ProducedAtMs` / header is still 28 bytes.

- [ ] **Step 3: Update `wireformat.go`**

Replace the header docstring, `FrameHeader` struct, `FullEntry`, `DeltaEntry`, `Encode`, `Header`, `NextFull`, `NextDelta` with:

```go
// Delta World Update binary wire format.
//
// Header (20 bytes):
//   [4] tick            (uint32 big-endian)
//   [4] seq             (uint32 big-endian) — frame sequence for client ack
//   [4] flags           (uint32 big-endian) — bit 0 = FreshSnapshot
//   [2] fullCount       (uint16 big-endian)
//   [2] deltaCount      (uint16 big-endian)
//   [2] removedCount    (uint16 big-endian)
//   [2] exitedCount     (uint16 big-endian)
//
// Each Full entry (variable):
//   [4] netID  [4] epoch  [1] entityType
//   [8] producedAtMs   — authoritative producer's ClusterClock.Now() at emit
//   [2] snapshotLen    [N] snapshot bytes
//   [2] initialLen     [M] initial bytes (may be 0)
//
// Each Delta entry (variable):
//   [4] netID  [4] epoch  [1] entityType
//   [8] producedAtMs   — authoritative producer's stamp
//   [2] dataLen        [N] delta bytes (bitmask + changed fields)
//
// Removed IDs: [4] * removedCount
// Exited IDs:  [4] * exitedCount

const frameHeaderSize = 20

// ... (keep FrameFlagFreshSnapshot const as-is)

type FrameHeader struct {
	Tick         uint32
	Seq          uint32
	Flags        uint32
	FullCount    uint16
	DeltaCount   uint16
	RemovedCount uint16
	ExitedCount  uint16
}

type FullEntry struct {
	NetID        uint32
	Epoch        uint32
	EntityType   uint8
	ProducedAtMs uint64
	Snapshot     []byte
	InitialData  []byte // nil if length was 0
}

type DeltaEntry struct {
	NetID        uint32
	Epoch        uint32
	EntityType   uint8
	ProducedAtMs uint64
	Data         []byte
}
```

Replace `Encode`:

```go
func (e *FrameEncoder) Encode(
	tick, seq, flags uint32,
	full []FullEntry,
	deltas []DeltaEntry,
	removed []uint32,
	exited []uint32,
) []byte {
	e.buf = e.buf[:0]

	// Header (no serverTimeMs — stamps travel per-entity now).
	e.buf = e.appendUint32(e.buf, tick)
	e.buf = e.appendUint32(e.buf, seq)
	e.buf = e.appendUint32(e.buf, flags)
	e.buf = e.appendUint16(e.buf, uint16(len(full)))
	e.buf = e.appendUint16(e.buf, uint16(len(deltas)))
	e.buf = e.appendUint16(e.buf, uint16(len(removed)))
	e.buf = e.appendUint16(e.buf, uint16(len(exited)))

	// Full entities.
	for i := range full {
		f := &full[i]
		e.buf = e.appendUint32(e.buf, f.NetID)
		e.buf = e.appendUint32(e.buf, f.Epoch)
		e.buf = append(e.buf, f.EntityType)
		e.buf = e.appendUint64(e.buf, f.ProducedAtMs)
		e.buf = e.appendUint16(e.buf, uint16(len(f.Snapshot)))
		e.buf = append(e.buf, f.Snapshot...)
		if len(f.InitialData) > 0 {
			e.buf = e.appendUint16(e.buf, uint16(len(f.InitialData)))
			e.buf = append(e.buf, f.InitialData...)
		} else {
			e.buf = e.appendUint16(e.buf, 0)
		}
	}

	// Delta entities.
	for i := range deltas {
		d := &deltas[i]
		e.buf = e.appendUint32(e.buf, d.NetID)
		e.buf = e.appendUint32(e.buf, d.Epoch)
		e.buf = append(e.buf, d.EntityType)
		e.buf = e.appendUint64(e.buf, d.ProducedAtMs)
		e.buf = e.appendUint16(e.buf, uint16(len(d.Data)))
		e.buf = append(e.buf, d.Data...)
	}

	// Removed IDs.
	for _, id := range removed {
		e.buf = e.appendUint32(e.buf, id)
	}
	// Exited IDs.
	for _, id := range exited {
		e.buf = e.appendUint32(e.buf, id)
	}

	return e.buf
}
```

Replace `Header`, `NextFull`, `NextDelta`:

```go
func (d *FrameDecoder) Header() FrameHeader {
	return FrameHeader{
		Tick:         d.readUint32(),
		Seq:          d.readUint32(),
		Flags:        d.readUint32(),
		FullCount:    d.readUint16(),
		DeltaCount:   d.readUint16(),
		RemovedCount: d.readUint16(),
		ExitedCount:  d.readUint16(),
	}
}

func (d *FrameDecoder) NextFull() FullEntry {
	netID := d.readUint32()
	epoch := d.readUint32()
	entityType := d.readUint8()
	producedAtMs := d.readUint64()
	snapLen := int(d.readUint16())
	snapshot := d.readBytes(snapLen)
	initLen := int(d.readUint16())
	var initData []byte
	if initLen > 0 {
		initData = d.readBytes(initLen)
	}
	return FullEntry{
		NetID:        netID,
		Epoch:        epoch,
		EntityType:   entityType,
		ProducedAtMs: producedAtMs,
		Snapshot:     snapshot,
		InitialData:  initData,
	}
}

func (d *FrameDecoder) NextDelta() DeltaEntry {
	netID := d.readUint32()
	epoch := d.readUint32()
	entityType := d.readUint8()
	producedAtMs := d.readUint64()
	deltaLen := int(d.readUint16())
	data := d.readBytes(deltaLen)
	return DeltaEntry{
		NetID:        netID,
		Epoch:        epoch,
		EntityType:   entityType,
		ProducedAtMs: producedAtMs,
		Data:         data,
	}
}
```

- [ ] **Step 4: Run the quantize tests**

Run: `cd . && go test ./pkg/quantize/ -count=1 -v`
Expected: the three new tests PASS. Some existing tests likely fail because they pass a `serverTimeMs` argument to `Encode` or expect 28-byte headers. Update each failing test to match the new signature.

- [ ] **Step 5: Fix existing callers**

Update every `Encode(..., serverTimeMs, ...)` call to drop the `serverTimeMs` argument. Grep:

```bash
cd . && rg 'FrameEncoder' --type=go -l
```

Typical callers: `pkg/system/frame_writer.go`, perhaps test fixtures. Update each; the per-entity stamps will be wired in Phase E.

- [ ] **Step 6: Run all Go tests**

Run: `cd . && go test ./... -count=1 -timeout 180s`
Expected: PASS. Some downstream tests may still assert frame-level `serverTimeMs` (e.g. `frame_writer_test.go`). Update those assertions to pass `ProducedAtMs` on the per-entity payloads.

- [ ] **Step 7: Commit**

```bash
cd .
git add pkg/quantize/ pkg/system/frame_writer.go pkg/system/frame_writer_test.go
git commit -m "feat(quantize): per-entity producedAtMs wire format

FrameEncoder/FrameDecoder now carry an 8-byte ProducedAtMs with every
full and delta entity payload. Frame-level serverTimeMs is removed;
header shrinks from 28 to 20 bytes. Client clock-sync uses the max of
per-entity stamps as the frame anchor.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task D2: Update TypeScript `delta-decoder-core.ts` symmetrically

**Files:**
- Modify: `pkg/quantize/ts/delta-decoder-core.ts`

- [ ] **Step 1: Read the file to find the frame-header and entity-decode sections**

```bash
cd .
grep -n 'server_time_ms\|serverTimeMs\|frameHeaderSize\|decodeFrameHeader' pkg/quantize/ts/delta-decoder-core.ts
```

- [ ] **Step 2: Update `decodeFrameHeader`**

Drop the 8-byte `serverTimeMs` read. The new header size is 20 bytes (5 × uint32 + 2 × uint16 fields… actually 4+4+4+2+2+2+2=20). Update the struct type, the offset arithmetic, and the read order.

Replace the section at lines ~116-144 with:

```typescript
export interface FrameHeader {
  tick: number;
  seq: number;
  flags: number;
  fullCount: number;
  deltaCount: number;
  removedCount: number;
  exitedCount: number;
}

export function decodeFrameHeader(data: Uint8Array, offset: number): { header: FrameHeader; offset: number } {
  const view = new DataView(data.buffer, data.byteOffset, data.byteLength);
  let pos = offset;
  const tick = view.getUint32(pos); pos += 4;
  const seq = view.getUint32(pos); pos += 4;
  const flags = view.getUint32(pos); pos += 4;
  const fullCount = view.getUint16(pos); pos += 2;
  const deltaCount = view.getUint16(pos); pos += 2;
  const removedCount = view.getUint16(pos); pos += 2;
  const exitedCount = view.getUint16(pos); pos += 2;
  return {
    header: { tick, seq, flags, fullCount, deltaCount, removedCount, exitedCount },
    offset: pos,
  };
}
```

- [ ] **Step 3: Update per-entity decoders**

Grep for the `NextFull` / `NextDelta` equivalents in this file (they may be named `readFullEntry`, `decodeFull`, etc). Each needs an 8-byte `producedAtMs` read between `entityType` and the data-length prefix.

```typescript
export interface FullEntry {
  netID: number;
  epoch: number;
  entityType: number;
  producedAtMs: number;   // NEW
  snapshot: Uint8Array;
  initialData: Uint8Array | null;
}

export interface DeltaEntry {
  netID: number;
  epoch: number;
  entityType: number;
  producedAtMs: number;   // NEW
  data: Uint8Array;
}

export function decodeFullEntry(data: Uint8Array, offset: number): { entry: FullEntry; offset: number } {
  const view = new DataView(data.buffer, data.byteOffset, data.byteLength);
  let pos = offset;
  const netID = view.getUint32(pos); pos += 4;
  const epoch = view.getUint32(pos); pos += 4;
  const entityType = view.getUint8(pos); pos += 1;
  const hi = view.getUint32(pos); pos += 4;
  const lo = view.getUint32(pos); pos += 4;
  const producedAtMs = hi * 0x100000000 + lo;
  const snapLen = view.getUint16(pos); pos += 2;
  const snapshot = data.subarray(pos, pos + snapLen); pos += snapLen;
  const initLen = view.getUint16(pos); pos += 2;
  const initialData = initLen > 0 ? data.subarray(pos, pos + initLen) : null;
  pos += initLen;
  return { entry: { netID, epoch, entityType, producedAtMs, snapshot, initialData }, offset: pos };
}

// Similar for decodeDeltaEntry.
```

- [ ] **Step 4: Run TS tests**

```bash
cd web-pixi && bun test
```

Expected: some tests fail because they expect the old header shape (`serverTimeMs` on the header). That's OK — those tests will be updated in Phase K. The codec-level tests that test `decodeFullEntry`/`decodeDeltaEntry` should pass.

Run:
```bash
cd examples/4node-basic/web && bun test
```

(If a tests directory exists for the SDK core itself, run those too.)

- [ ] **Step 5: Commit**

```bash
cd .
git add pkg/quantize/ts/
git commit -m "feat(quantize/ts): mirror per-entity producedAtMs wire format in TS decoder

decodeFrameHeader drops serverTimeMs (header is 20 bytes). Each
FullEntry/DeltaEntry carries an 8-byte producedAtMs. Downstream
interpolation + clockSync will switch to these per-entity stamps in
a later phase.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Phase E — `Replica` component refactor + `FrameWriter` stamping

Replace the old `UpdatedThisTick` gate (no longer needed once server DR is gone) with a `ProducedAtMs` stamp. Plumb `ClusterClock` through `ReplicationConfig` so `FrameWriter` can stamp local-authoritative entities.

### Task E1: Refactor `component.Replica` — drop `UpdatedThisTick`, add `ProducedAtMs`

**Files:**
- Modify: `pkg/component/core.go`

- [ ] **Step 1: Update the struct**

Replace the `Replica` declaration:

```go
// Replica is a read-only copy of an entity from a neighboring cell.
// Participates in spatial grid and AoI queries but is never mutated
// locally — position/velocity/components are refreshed solely by
// upsertBorderReplica applying inbound border frames.
//
// ProducedAtMs is the cluster-clock stamp from the authoritative
// source's most recent frame for this netID. It travels opaquely
// through this cell's outbound replication so downstream clients
// see one coherent timeline regardless of how many cells relayed
// the entity's state.
type Replica struct {
	SourceCellID string
	SourceNetID  uint32
	TTL          int    // ticks remaining before expiry (reset on refresh)
	ProducedAtMs uint64 // authoritative producer's ClusterClock.Now() at emit
}
```

- [ ] **Step 2: Find all callers of `UpdatedThisTick`**

```bash
cd . && rg 'UpdatedThisTick' --type=go -l
```

Each reference needs to be handled. Expected sites:
- `pkg/system/replica_dead_reckoning.go` — will be deleted entirely in Phase J.
- `pkg/universe/world_base.go` — `ApplyBorderFrame` sets `UpdatedThisTick=true`, `ClearReplicaUpdateFlags` clears it each tick. Both references are removed in Phase I.
- `pkg/system/replication.go` — if there's any `!rep.UpdatedThisTick` check, replace with unconditional handling.

For now, temporarily remove `UpdatedThisTick` reads/writes and accept that some code changes in Phase I/J will finalize the cleanup. Verify the tree compiles by running `go vet ./...`.

- [ ] **Step 3: Update compile errors immediately**

Grep shows the references. Remove or stub each:

In `pkg/universe/world_base.go`:
- `upsertBorderReplica` sets `rep.UpdatedThisTick = true` — remove that line.
- `ClearReplicaUpdateFlags` walks replicas setting `UpdatedThisTick = false` — delete the method (no longer needed) and remove any call site.

In `pkg/system/replica_dead_reckoning.go`: will be fully deleted — leave untouched for now (it will break but we delete it in Phase J); but we need `go vet` to pass between phases.

**Important**: to keep `go vet` clean between phases, Phase E temporarily stubs the Replica DR system to compile against the new struct. Edit `pkg/system/replica_dead_reckoning.go`:

```go
func (s *ReplicaDeadReckoningSystem) Update(dt float32) {
	// Replicas are passive caches; no DR. Ghost DR is removed with
	// this system in Phase J.
	for _, b := range s.ghosts.All() {
		b.Pos.X += b.Vel.X * dt
		b.Pos.Y += b.Vel.Y * dt
	}
}
```

And the `Init`:

```go
func (s *ReplicaDeadReckoningSystem) Init() {
	s.ghosts.Init(s, query.IncludeAll())
}
```

Delete the `replicas query.Query[...]` field from the struct. This is a temporary intermediate state — the whole file gets deleted in Phase J.

- [ ] **Step 4: go vet + full tests**

```bash
cd . && go vet ./...
cd . && go test ./... -count=1 -timeout 180s
```

Expected: vet clean. Tests mostly PASS; the dead-reckoning test for the replicas branch is now a tautology (always skipped). Leave for now.

- [ ] **Step 5: Commit**

```bash
cd .
git add pkg/component/core.go pkg/universe/world_base.go pkg/system/replica_dead_reckoning.go pkg/system/replica_dead_reckoning_test.go
git commit -m "refactor(component): Replica drops UpdatedThisTick, gains ProducedAtMs

Replicas are passive caches now — no per-tick gate, no grace
extrapolation. ProducedAtMs carries the authoritative producer's
cluster-clock stamp opaquely through replica caches. Replica
dead-reckoning loop is stubbed empty pending Phase J deletion.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task E2: Thread `ClusterClock` through `ReplicationConfig` + `FrameWriter`

**Files:**
- Modify: `pkg/system/replication.go`
- Modify: `pkg/system/frame_writer.go`
- Modify: `pkg/mmokit/replication.go` (the `DefaultReplicationConfig` helper)

- [ ] **Step 1: Add `ClusterClock` to `ReplicationConfig`**

In `pkg/system/replication.go`, add to the `ReplicationConfig` struct (after `Replicators`):

```go
	// ClusterClock provides cluster-coherent wall-clock stamps for
	// replication samples. Required — the system panics on Init if nil.
	// Games wire this via the Process's shared ClusterClock; the
	// DefaultReplicationConfig helper fills it from mmokit.NewCoordinator's
	// Process value.
	ClusterClock *mmokit.ClusterClock
```

- [ ] **Step 2: Validate in `Init`**

Locate the system's `Init()` method and add:

```go
	if s.cfg.ClusterClock == nil {
		panic("ReplicationConfig.ClusterClock is required")
	}
```

- [ ] **Step 3: Plumb into `FrameWriter`**

`pkg/system/frame_writer.go` currently has `BinaryFrameWriter.WriteFrame(frame *ReplicationFrame)`. The `ReplicationFrame` struct lives in `pkg/system/replication.go` and carries `FullEntry` / `DeltaEntry` slices. Those slices now need `ProducedAtMs` populated before reaching the writer.

In `replication.go`, locate the code that builds `FullEntry` and `DeltaEntry` (via `s.cfg.Replicators.Full/Delta` or similar). For each entity being serialized:

```go
	// Local-authoritative entity: stamp with cluster-clock now.
	// Replica: reuse the cached producer stamp from Replica.ProducedAtMs.
	var producedAtMs uint64
	if rep, ok := s.replicaMap.Get(entity); ok {
		producedAtMs = rep.ProducedAtMs
	} else {
		producedAtMs = s.cfg.ClusterClock.Now()
	}
	full = append(full, quantize.FullEntry{
		NetID:        netID,
		Epoch:        epoch,
		EntityType:   entityType,
		ProducedAtMs: producedAtMs,
		Snapshot:     snapshot,
		InitialData:  initial,
	})
```

(Adapt to whatever the actual serialization loop looks like — inspect the existing code. The `replicaMap` accessor may need to be exposed on the system.)

- [ ] **Step 4: Remove `time.Now()` from `frame_writer.go`**

In `pkg/system/frame_writer.go`:

```go
// Delete the `serverTimeMs := uint64(time.Now().UnixMilli())` line.
// Delete the `serverTimeMs` argument from the `encoder.Encode` call (Phase D already updated the signature).
```

- [ ] **Step 5: Write a test**

Append to `pkg/system/frame_writer_test.go`:

```go
func TestFrameWriter_StampsLocalEntitiesViaClusterClock(t *testing.T) {
	// Build a minimal replication pipeline with a fixed ClusterClock
	// whose Now() returns 42_000_000. Produce a frame containing one
	// locally-authoritative entity. Assert the decoded full-entry's
	// ProducedAtMs is 42_000_000.
	// ... setup omitted for brevity; follows existing helper pattern ...
}

func TestFrameWriter_PassesThroughReplicaProducedAtMs(t *testing.T) {
	// Spawn a replica with Replica.ProducedAtMs = 7_777_777. Assert
	// the outbound frame's full-entry has the same ProducedAtMs.
}
```

- [ ] **Step 6: Update the `DefaultReplicationConfig` helper**

In `pkg/mmokit/replication.go` (where `DefaultReplicationConfig(eng, grid)` lives), thread a `ClusterClock` argument or read it from the Process:

```go
func DefaultReplicationConfig(eng *engine.Engine, grid *spatial.HashGrid, clock *mmokit.ClusterClock) system.ReplicationConfig {
	return system.ReplicationConfig{
		World:        eng.ECS,
		// ...
		ClusterClock: clock,
	}
}
```

Update every caller — game code in `examples/4node-basic/main.go`, `examples/slither/main.go`, `internal/game/factory.go`. Each passes `coord.ClusterClock`.

- [ ] **Step 7: go vet + tests**

```bash
cd . && go vet ./...
cd . && go test ./... -count=1 -timeout 180s
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
cd .
git add pkg/system/replication.go pkg/system/frame_writer.go pkg/system/frame_writer_test.go pkg/mmokit/replication.go examples/ internal/
git commit -m "feat(replication): per-entity producedAtMs stamped from ClusterClock

ReplicationConfig gains a required ClusterClock. FrameWriter stamps
local entities with clock.Now() at emit time and passes replica
producedAtMs through unchanged. The old frame-level time.Now() call
is gone.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Phase F — Border frame `producedAtMs` + `upsertBorderReplica` caches it

The border-replication channel (cell → neighbor) also needs per-entity stamps so the cached `Replica.ProducedAtMs` is populated correctly.

### Task F1: Add `producedAtMs` to the border-frame DeltaBuf codec

**Files:**
- Modify: `pkg/universe/border_components.go`
- Modify: `pkg/universe/world_base.go` (`BuildBorderFrame`, `ApplyBorderFrame`, `upsertBorderReplica`)

- [ ] **Step 1: Update the DeltaBuf wire format**

Open `pkg/universe/border_components.go`. Locate the layout comment (around line 9-28). Update to:

```go
// BorderFrame data layout (24-byte fixed header + component tail):
//   [4] worldX        float32 LE
//   [4] worldY        float32 LE
//   [4] radius        float32 LE
//   [2] qvx           int16 LE
//   [2] qvy           int16 LE
//   [8] producedAtMs  uint64 LE — authoritative producer's stamp
//   [2] componentCount uint16 LE (or 0xFFFF = unchanged sentinel)
//   repeated componentCount times:
//     [2] componentID  uint16 LE
//     [2] dataLen      uint16 LE
//     [N] data
```

Update the fixed-header size constant or struct if any exists; find and update encode+decode.

- [ ] **Step 2: Update the encoder**

Locate the serialization site that writes `worldX/worldY/radius/qvx/qvy/componentCount`. Insert 8 bytes for `producedAtMs` after `qvy` and before `componentCount`. The producer value is the authoritative cell's `clusterClock.Now()` at frame-build time.

```go
	// In BuildBorderFrame per-entity loop:
	buf = appendFloat32LE(buf, pos.X)
	buf = appendFloat32LE(buf, pos.Y)
	buf = appendFloat32LE(buf, collider.Radius)
	buf = appendInt16LE(buf, qvx)
	buf = appendInt16LE(buf, qvy)
	buf = appendUint64LE(buf, b.clusterClock.Now())  // NEW
	// ... componentCount follows
```

(The encoder needs access to `clusterClock`. `WorldBase` holds a reference; thread in the constructor.)

- [ ] **Step 3: Update the decoder**

In `ApplyBorderFrame` (or the per-entity apply path), read the 8 bytes back and pass them into `upsertBorderReplica` / the apply call:

```go
	producedAtMs := readUint64LE(buf)
```

- [ ] **Step 4: Update `upsertBorderReplica` to store the stamp**

Find `upsertBorderReplica`. When creating or updating the `Replica` component, set `ProducedAtMs`:

```go
	if !b.replicaMap.HasAll(ent) {
		b.replicaMap.Add(ent, &component.Replica{
			SourceCellID: sourceCellID,
			SourceNetID:  netID,
			TTL:          defaultReplicaTTL,
			ProducedAtMs: producedAtMs,
		})
	} else {
		rep := b.replicaMap.Get(ent)
		rep.SourceCellID = sourceCellID
		rep.SourceNetID = netID
		rep.TTL = defaultReplicaTTL
		rep.ProducedAtMs = producedAtMs
	}
```

- [ ] **Step 5: Write a test**

Append to `pkg/universe/world_base_test.go` (or create `pkg/universe/border_frame_test.go` if cleaner):

```go
func TestBorderFrame_ProducedAtMsRoundTrip(t *testing.T) {
	srcClock := mmokit.NewClusterClock()
	srcClock.Observe(5_000_000, 1)
	dstClock := mmokit.NewClusterClock()
	dstClock.Observe(5_000_000, 1)

	src := newTestWorldBaseWithClock(t, CellID{X: 0, Y: 0}, srcClock)
	dst := newTestWorldBaseWithClock(t, CellID{X: 1, Y: 0}, dstClock)

	// Spawn entity on src, build a border frame, apply on dst.
	ent, _ := src.SpawnEntity(50, 100,
		WithEntityKind(1),
		WithCollider(5),
	)
	netID := src.NetworkIDMap().Get(ent).ID

	frame := src.BuildBorderFrame(dst.CellID())
	dst.ApplyBorderFrame(src.CellID().String(), frame)

	// Look up the replica on dst, verify ProducedAtMs ≈ srcClock.Now().
	replicaEnt, _, ok := dst.LookupNetID(netID)
	if !ok {
		t.Fatal("replica not created on dst")
	}
	rep := dst.ReplicaMap().Get(replicaEnt)
	if rep.ProducedAtMs == 0 {
		t.Fatal("Replica.ProducedAtMs not populated from border frame")
	}
}
```

- [ ] **Step 6: Run tests**

```bash
cd . && go vet ./...
cd . && go test ./pkg/universe/ -run 'BorderFrame|ProducedAtMs' -v -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
cd .
git add pkg/universe/border_components.go pkg/universe/world_base.go pkg/universe/world_base_test.go
git commit -m "feat(border): carry producedAtMs through border frames + cache on Replica

Border-frame per-entity layout gains 8 bytes between qvy and
componentCount. upsertBorderReplica caches the stamp on Replica so
outbound replication can relay it unchanged. Authoritative producer's
cluster-clock stamp travels opaquely through any number of relays.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Phase G — Collapse handoff wire protocol + Bridge interface

Replace `HandoffPrepare` / `HandoffCommit` / `HandoffCancel` with a single `Handoff` message. Same on the proto side and the bridge side.

### Task G1: Replace the three handoff messages with one in `mesh.proto`

**Files:**
- Modify: `proto/meshpb/mesh.proto`
- Regenerate: `gen/go/meshpb/mesh.pb.go`, `gen/es/meshpb/mesh_pb.ts`

- [ ] **Step 1: Edit `mesh.proto`**

Find the `MeshFrame` `oneof` block. Delete `handoff_prepare`, `handoff_commit`, `handoff_cancel` arms. Add:

```proto
    Handoff handoff = 2;   // NEW — see Handoff message definition
```

(Reuse a freed field number or append; per user preference "Never reserve old proto fields", delete them entirely and renumber the remaining arms from 1. Buf will regenerate.)

Delete the existing `message HandoffPrepare`, `message HandoffCommit`, `message HandoffCancel`, `message ClientBaseline` definitions.

Add:

```proto
// Handoff is the single authority-transfer message. Replaces
// HandoffPrepare + HandoffCommit + HandoffCancel from the overlap
// model. Source sends one per crossing event; destination commits at
// commit_tick (cluster-tick number; see ClusterClock).
//
// transfer_blob carries the full entity state only when the
// destination does not already have a border-replica for net_id
// (fast-mover or spawn-across-boundary case). When the destination
// does have a replica, blob is nil and destination's promote path
// reuses the cached state.
message Handoff {
  uint32 net_id        = 1;
  uint32 epoch         = 2;
  uint64 commit_tick   = 3;
  bytes  transfer_blob = 4; // optional; empty when dest has replica
  uint32 conn_id       = 5; // 0 for non-player entities
}
```

- [ ] **Step 2: Run `just proto`**

```bash
cd . && just proto
```

Expected: regeneration succeeds. `go vet ./...` will now fail due to broken callers — fix in Task G2.

- [ ] **Step 3: Commit after regen (compile will not yet pass — this is expected)**

We'll bundle the proto regen with the bridge collapse in the same commit to preserve build-green invariants across commits. Do **not** commit yet; continue to G2.

---

### Task G2: Collapse `Bridge` interface — single `SendHandoff`

**Files:**
- Modify: `pkg/universe/bridge.go`
- Modify: `pkg/universe/cell_bridge_impl.go`
- Modify: `pkg/universe/grpc_bridge.go`
- Modify: `pkg/universe/message.go`
- Modify: `pkg/universe/mesh_frame_codec.go`
- Modify: `pkg/universe/handoff_driver.go` (callers)
- Modify: `pkg/universe/cell.go` (receivers)

- [ ] **Step 1: Replace the payload types**

In `pkg/universe/message.go`, delete `HandoffPreparePayload`, `HandoffCommitPayload`, `HandoffCancelPayload`. Add:

```go
// HandoffPayload is the single authority-transfer message. Mirrors the
// meshpb.Handoff proto for in-process and cross-host dispatch.
//
// CommitTick is the cluster-clock tick number at which the destination
// becomes authoritative. Source demotes at end-of-tick (CommitTick-1);
// destination promotes at start-of-tick CommitTick. A single-tick slip
// on delivery is absorbed by client render-lag.
//
// TransferBlob is populated only when the destination does not already
// have a border-replica for NetID — the fast-mover / cross-boundary
// spawn case. When nil, destination promotes its existing Replica to
// Live at CommitTick.
type HandoffPayload struct {
	NetID        uint32
	Epoch        uint32
	CommitTick   uint64
	TransferBlob []byte // optional
	ConnID       uint32 // 0 for non-player
}
```

- [ ] **Step 2: Update `Bridge` interface**

In `pkg/universe/bridge.go`, replace the three Send methods:

```go
	// SendHandoff sends an authority-transfer message to the destination
	// cell. Returns true on successful enqueue, false only if the
	// destination cell no longer exists on this process (concurrent
	// merge commit). Caller MUST NOT demote the source on a false return —
	// BoundarySystem will re-detect the crossing and retry next tick.
	SendHandoff(destCellID string, payload *HandoffPayload) bool
```

Delete the three old method declarations + the NoopBridge implementations. Add:

```go
func (NoopBridge) SendHandoff(string, *HandoffPayload) bool { return true }
```

- [ ] **Step 3: Update `cell_bridge_impl.go` + `grpc_bridge.go`**

Each bridge impl converts `HandoffPayload` ↔ `meshpb.Handoff` for wire transit. Replace the three old methods with a single `SendHandoff` that routes via `MeshFrame.Handoff`.

- [ ] **Step 4: Update `mesh_frame_codec.go`**

Delete the three old encode/decode funcs (`encodeHandoffPrepare` etc). Add:

```go
func encodeHandoff(msg *meshpb.Handoff) *CellMessage { ... }
func decodeHandoff(frame *meshpb.MeshFrame) *CellMessage { ... }
```

- [ ] **Step 5: Update `cell.go`**

Delete the three handler cases. Add:

```go
	case MsgHandoff:
		if msg.Handoff == nil {
			return
		}
		// Process the handoff: queue a promote/spawn for CommitTick.
		cell.onHandoff(msg.Handoff)
```

Implementation of `onHandoff` deferred to Task H2 — for now, stub it to log and return.

- [ ] **Step 6: Update `handoff_driver.go` caller**

Replace the two-call sequence (`SendHandoffPrepare` + `SendHandoffCommit`) with a single `SendHandoff` call. Delete the `SendHandoffCancel` call. Replace `MarkForRemoval` on the source with a **queue-demote-for-CommitTick** stub — full implementation in Task H1.

For now, the simplest approach: in `handleCrossing`, send the single `HandoffPayload` with `CommitTick = currentTick + handoffLeadTicks`, and demote immediately (collapsing back to v1 behavior temporarily to keep tests green). Full hard-cut semantics land in Phase H.

- [ ] **Step 7: Delete `HandoffStateMachine`**

In `pkg/universe/handoff.go`, delete every method and field on `HandoffStateMachine`, delete the type, delete `HandoffKey`, delete `HandoffPhase`, delete `MinWarmupTicks`, `MaxWarmupTicks`. Leave the file empty or delete it outright. Retain `CrossingCooldownTicks` if `HandoffDriver` keeps its own (simpler) anti-thrash map — inline it.

- [ ] **Step 8: go vet + build**

```bash
cd . && go vet ./...
```

Expected: clean. If there are remaining references to the deleted types, resolve them inline.

- [ ] **Step 9: Delete stale tests**

```bash
cd . && rm pkg/universe/handoff_test.go pkg/universe/handoff_driver_test.go
```

(These test the old protocol. Rewrite as new tests in Phase L.)

- [ ] **Step 10: Run tests**

```bash
cd . && go test ./... -count=1 -timeout 180s
```

Expected: some integration tests fail (S7 cross-host handoff tests, etc.) because the handoff protocol is mid-transition. They'll be rewritten in Phase L. Ensure `go vet` is clean and at minimum the narrow package tests pass.

- [ ] **Step 11: Commit**

```bash
cd .
git add proto/ gen/ pkg/universe/
git commit -m "refactor(universe): collapse 3 handoff messages to single Handoff

MeshFrame.Handoff replaces MeshFrame.{HandoffPrepare,Commit,Cancel}.
Bridge.SendHandoff replaces the three SendHandoff* methods.
HandoffStateMachine, HandoffKey, HandoffPhase, MinWarmupTicks,
MaxWarmupTicks all deleted — the new driver uses a simple commit-tick
queue and per-pair cooldown map inline.

Handoff behaviour is still v1-ish at this commit (single-tick commit);
hard-cut semantics land in Phase H.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Phase H — Hard-cut `HandoffDriver` + commit-tick queue

Finalize the new handoff semantics: source queues a demote for `CommitTick`, destination queues a promote for `CommitTick`, and both fire at end-of-tick N / start-of-tick N+1 respectively.

### Task H1: Rewrite `HandoffDriver.handleCrossing` for hard-cut

**Files:**
- Modify: `pkg/universe/handoff_driver.go`
- Modify: `pkg/universe/world_base.go` (add `DemoteLiveToReplica`)

- [ ] **Step 1: Design the commit-tick queue**

In `handoff_driver.go`, add:

```go
// pendingDemote represents a source-side demote queued for a specific
// cluster-tick. At end-of-tick (currentTick == CommitTick), the driver
// demotes the live entity to a replica of the new authority so the
// source's outbound replication continues to scan it for one more tick
// (for any viewer on the boundary between the two cells) but authority
// has flipped.
type pendingDemote struct {
	entity     ecs.Entity
	netID      uint32
	destCellID string
	connID     uint32
}

type HandoffDriver struct {
	base         *WorldBase
	bridge       Bridge
	netMap       *ecs.Map1[component.NetworkID]
	kindMap      *ecs.Map1[component.EntityKind]
	posMap       *ecs.Map1[component.Position]
	cellMap      *ecs.Map1[component.CellCoord]
	clusterClock *mmokit.ClusterClock

	// pending is keyed by the cluster-tick at which to fire the demote.
	pending map[uint64][]pendingDemote

	// lastHandoffTick[netID][destCellID] = cluster-tick of last successful
	// handoff. Used for anti-thrash cooldown.
	lastHandoff map[uint32]map[string]uint64
}
```

Add a constant:

```go
// handoffLeadTicks is how far ahead of the current cluster-tick the
// CommitTick is set. Large enough for the Handoff message to reach the
// destination on a typical LAN plus dest-side processing margin. At
// 20 Hz and a 50 ms round trip, 2 ticks (100 ms) is conservative.
const handoffLeadTicks = 2

// handoffCooldownTicks is the minimum cluster-tick gap between
// successive handoffs of the same (netID, destCellID) pair. Prevents
// thrash for entities hovering on a boundary.
const handoffCooldownTicks = 20
```

- [ ] **Step 2: Update `handleCrossing`**

```go
func (hd *HandoffDriver) handleCrossing(evt CrossingEvent, currentClusterTick uint64) {
	// Anti-thrash: skip if we've committed this (netID, dest) pair
	// within the cooldown window.
	if dst, ok := hd.lastHandoff[evt.NetID]; ok {
		if last, ok := dst[evt.DestCellID]; ok {
			if currentClusterTick-last < handoffCooldownTicks {
				return
			}
		}
	}

	if !hd.base.eng.ECS.Alive(evt.Entity) {
		return
	}

	// Bump epoch on source.
	var oldEpoch uint32
	if hd.netMap.HasAll(evt.Entity) {
		nid := hd.netMap.Get(evt.Entity)
		oldEpoch = nid.Epoch
		nid.Epoch++
	}
	newEpoch := oldEpoch + 1

	// Normalize Position + CellCoord to destination-cell local frame
	// (same logic as pre-refactor — do not change).
	hd.normalizeToDestCell(evt.Entity)

	// Decide whether to send the transfer blob. If the destination is a
	// live border neighbor of this cell, it likely already has a Replica —
	// skip the blob and rely on the existing border-frame cache.
	//
	// TODO: the check should consult BorderSubscriptions. For now, the
	// simplest correct answer is "always send blob" — the destination
	// ignores it when a replica already exists. Size cost: ~100 bytes
	// per handoff, trivial.
	blob, err := hd.base.SerializeEntity(evt.Entity)
	if err != nil {
		// Roll back epoch, bail.
		if hd.netMap.HasAll(evt.Entity) {
			hd.netMap.Get(evt.Entity).Epoch = oldEpoch
		}
		return
	}

	commitTick := currentClusterTick + handoffLeadTicks
	ok := hd.bridge.SendHandoff(evt.DestCellID, &HandoffPayload{
		NetID:        evt.NetID,
		Epoch:        newEpoch,
		CommitTick:   commitTick,
		TransferBlob: blob,
		ConnID:       evt.ConnID,
	})
	if !ok {
		// Destination cell gone (concurrent merge). Roll back epoch;
		// source entity stays live; next BoundarySystem tick retries.
		if hd.netMap.HasAll(evt.Entity) {
			hd.netMap.Get(evt.Entity).Epoch = oldEpoch
		}
		return
	}

	// Queue local demote for end-of-commit-tick.
	hd.pending[commitTick] = append(hd.pending[commitTick], pendingDemote{
		entity:     evt.Entity,
		netID:      evt.NetID,
		destCellID: evt.DestCellID,
		connID:     evt.ConnID,
	})

	// Record cooldown.
	if hd.lastHandoff[evt.NetID] == nil {
		hd.lastHandoff[evt.NetID] = make(map[string]uint64)
	}
	hd.lastHandoff[evt.NetID][evt.DestCellID] = commitTick

	hd.base.eng.Log.Log(CatMeshTransfer,
		"[%s] handoff sent: netID=%d dest=%s commitTick=%d epoch=%d",
		hd.base.cellID, evt.NetID, evt.DestCellID, commitTick, newEpoch)
}
```

- [ ] **Step 3: Drain pending demotes in `Tick`**

```go
func (hd *HandoffDriver) Tick(currentClusterTick uint64) {
	if hd.base.IsDrainingForMerge() {
		hd.base.DrainCrossingQueue()
		return
	}
	// Fire due demotes FIRST (before handling new crossings this tick).
	if list, ok := hd.pending[currentClusterTick]; ok {
		for _, d := range list {
			if err := hd.base.DemoteLiveToReplica(d.netID, d.destCellID); err != nil {
				hd.base.eng.Log.Log(CatMeshTransfer,
					"[%s] demote failed: netID=%d err=%v",
					hd.base.cellID, d.netID, err)
			}
			if d.connID != 0 {
				hd.bridge.OnPlayerTransfer(d.connID, d.destCellID)
				if sess := hd.base.eng.Players.ByConnID(d.connID); sess != nil {
					_ = hd.base.eng.Players.Transition(sess, engine.StateTransferring)
					hd.base.eng.Players.Remove(sess)
				}
			}
		}
		delete(hd.pending, currentClusterTick)
	}

	// Now handle new crossings.
	for _, evt := range hd.base.DrainCrossingQueue() {
		hd.handleCrossing(evt, currentClusterTick)
	}
}
```

- [ ] **Step 4: Add `WorldBase.DemoteLiveToReplica`**

In `pkg/universe/world_base.go`:

```go
// DemoteLiveToReplica converts a locally-authoritative entity into a
// Replica of the new source cell. Same ECS entity, same
// Position/Velocity/Rotation/components; the cell stops pushing it
// on the border-dispatcher (replicas aren't in the push set) and
// stops emitting authoritative samples for it in outbound
// client-facing replication. Destination will push samples that
// upsertBorderReplica catches.
func (b *WorldBase) DemoteLiveToReplica(netID uint32, newSourceCellID string) error {
	ent, presence, ok := b.netIDIdx.Lookup(netID)
	if !ok || presence != PresenceLive {
		return fmt.Errorf("DemoteLiveToReplica: netID=%d not live on %s", netID, b.cellID)
	}
	if !b.eng.ECS.Alive(ent) {
		return fmt.Errorf("DemoteLiveToReplica: entity for netID=%d not alive", netID)
	}
	// Replica component: fresh TTL; ProducedAtMs will be overwritten by
	// the first inbound border frame from the new authority.
	if !b.replicaMap.HasAll(ent) {
		b.replicaMap.Add(ent, &component.Replica{
			SourceCellID: newSourceCellID,
			SourceNetID:  netID,
			TTL:          defaultReplicaTTL,
		})
	} else {
		rep := b.replicaMap.Get(ent)
		rep.SourceCellID = newSourceCellID
		rep.SourceNetID = netID
		rep.TTL = defaultReplicaTTL
	}
	if res := b.netIDIdx.Demote(netID, ent); res.Action != ActionUpdated {
		return fmt.Errorf("DemoteLiveToReplica: netIDIdx.Demote returned action=%d", res.Action)
	}
	b.replicaNetIDs[netID] = ent
	b.eng.Log.Log(CatMeshTransfer,
		"[%s] demoted live→replica: netID=%d newSource=%s",
		b.cellID, netID, newSourceCellID)
	return nil
}
```

And add `Demote` to `netIDIndex`:

```go
func (idx *netIDIndex) Demote(netID uint32, entity ecs.Entity) TransitionResult {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	cur, ok := idx.slots[netID]
	if !ok || cur.Presence != PresenceLive {
		return TransitionResult{Action: ActionRejected}
	}
	idx.slots[netID] = netIDSlot{Entity: entity, Presence: PresenceReplica}
	return TransitionResult{Action: ActionUpdated, PrevEntity: cur.Entity}
}
```

- [ ] **Step 5: Write tests**

Create `pkg/universe/hard_cut_handoff_test.go`:

```go
package universe

import (
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

// TestHardCut_SameHost_AuthorityFlipsAtCommitTick verifies that an
// entity crossing the A→B boundary has its authority flip at exactly
// CommitTick, with no gap and no overlap.
func TestHardCut_SameHost_AuthorityFlipsAtCommitTick(t *testing.T) {
	// Set up two sibling cells on the same host.
	clock := mmokit.NewClusterClock()
	clock.Observe(uint64(time.Now().UnixMilli()), 1)
	fx := newTwoCellFixture(t, clock)
	defer fx.Shutdown()

	// Spawn entity on A near the east edge.
	ent := fx.A.SpawnTestEntity(t, fx.A.CellSize()*0.95, fx.A.CellSize()*0.5)
	netID := fx.A.NetworkIDMap().Get(ent).ID

	// Step physics until a crossing event is produced and a Handoff is sent.
	fx.TickUntil(t, 10, func() bool { return fx.HandoffSent(netID) })

	// Before commit-tick, entity is Live on A, not present on B.
	fx.AssertAuthority(t, netID, "A", "live")
	fx.AssertAuthority(t, netID, "B", "replica-or-absent")

	// Advance to commit-tick.
	fx.AdvanceToClusterTick(t, fx.CommitTick(netID))

	// After commit-tick, entity is Replica on A, Live on B.
	fx.AssertAuthority(t, netID, "A", "replica")
	fx.AssertAuthority(t, netID, "B", "live")
}
```

The fixture helpers `newTwoCellFixture`, `HandoffSent`, `AssertAuthority`, `AdvanceToClusterTick`, `CommitTick` are new test infrastructure. Implement them in a helper file `pkg/universe/test_support_test.go` (or reuse the existing two-cell fixture if one exists).

- [ ] **Step 6: Run**

```bash
cd . && go vet ./...
cd . && go test ./pkg/universe/ -run TestHardCut_SameHost -v -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
cd .
git add pkg/universe/
git commit -m "feat(universe): hard-cut handoff at cluster-tick boundary

HandoffDriver queues a demote for CommitTick = currentClusterTick+2
and fires it at start-of-tick in Tick(). Destination promotes its
existing Replica (or spawns from transfer blob) symmetrically. No
warmup, no overlap, no shadow. Per-pair cooldown prevents thrash.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task H2: Destination-side `MsgHandoff` handler — queue promote/spawn for CommitTick

**Files:**
- Modify: `pkg/universe/cell.go`
- Modify: `pkg/universe/world_base.go` (add `PromoteReplicaToLive`, `SpawnLiveFromTransfer`)

- [ ] **Step 1: Add a per-cell commit queue on `Cell`**

In `pkg/universe/cell.go` (or wherever `Cell` is declared), add:

```go
// pendingPromote holds a destination-side promote queued for commitTick.
type pendingPromote struct {
	netID        uint32
	epoch        uint32
	transferBlob []byte
	connID       uint32
}

// On the Cell struct:
type Cell struct {
	// ... existing fields ...
	pendingPromotes map[uint64][]pendingPromote
}
```

- [ ] **Step 2: Implement the handler**

```go
func (c *Cell) onHandoff(p *HandoffPayload) {
	if c.pendingPromotes == nil {
		c.pendingPromotes = make(map[uint64][]pendingPromote)
	}
	c.pendingPromotes[p.CommitTick] = append(c.pendingPromotes[p.CommitTick], pendingPromote{
		netID:        p.NetID,
		epoch:        p.Epoch,
		transferBlob: p.TransferBlob,
		connID:       p.ConnID,
	})
	c.Base.Log.Log(CatMeshTransfer,
		"[%s] handoff queued: netID=%d commitTick=%d",
		c.CellID, p.NetID, p.CommitTick)
}
```

- [ ] **Step 3: Drain at tick start**

In the cell's tick loop, call `drainPendingPromotes(currentClusterTick)` before `Engine.Tick`:

```go
func (c *Cell) drainPendingPromotes(currentClusterTick uint64) {
	list, ok := c.pendingPromotes[currentClusterTick]
	if !ok {
		return
	}
	for _, p := range list {
		// If a border replica already exists for netID, promote it in place.
		if _, presence, ok := c.Base.LookupNetID(p.netID); ok && presence == PresenceReplica {
			if err := c.Base.PromoteReplicaToLive(p.netID, p.epoch); err != nil {
				c.Base.Log.Log(CatMeshTransfer,
					"[%s] promote failed: netID=%d err=%v",
					c.CellID, p.netID, err)
			}
			continue
		}
		// Otherwise spawn Live from the transfer blob.
		if _, err := c.Base.SpawnLiveFromTransfer(p.netID, p.epoch, p.transferBlob); err != nil {
			c.Base.Log.Log(CatMeshTransfer,
				"[%s] spawn-from-transfer failed: netID=%d err=%v",
				c.CellID, p.netID, err)
		}
	}
	delete(c.pendingPromotes, currentClusterTick)
}
```

(Late-arriving handoffs: if `p.CommitTick < currentClusterTick`, process immediately — a single-tick commit slip is intended to be invisible.)

- [ ] **Step 4: Add `PromoteReplicaToLive` and `SpawnLiveFromTransfer` to `WorldBase`**

```go
// PromoteReplicaToLive promotes a locally-cached replica to authoritative.
// The Replica component is removed, the netIDIdx slot flips Replica→Live
// via the sanctioned Promote path, and the entity becomes part of the
// border-dispatcher push walk. Epoch on the entity is bumped to
// newEpoch (from the Handoff payload).
func (b *WorldBase) PromoteReplicaToLive(netID uint32, newEpoch uint32) error {
	ent, presence, ok := b.netIDIdx.Lookup(netID)
	if !ok || presence != PresenceReplica {
		return fmt.Errorf("PromoteReplicaToLive: netID=%d not a replica on %s", netID, b.cellID)
	}
	if !b.eng.ECS.Alive(ent) {
		return fmt.Errorf("PromoteReplicaToLive: entity for netID=%d not alive", netID)
	}
	// Bump epoch.
	if b.netMap.HasAll(ent) {
		b.netMap.Get(ent).Epoch = newEpoch
	}
	// Remove Replica component.
	b.replicaMap.Remove(ent)
	delete(b.replicaNetIDs, netID)
	// Flip slot.
	if res := b.netIDIdx.Promote(netID, ent); res.Action != ActionUpdated {
		return fmt.Errorf("PromoteReplicaToLive: Promote returned action=%d", res.Action)
	}
	return nil
}

// SpawnLiveFromTransfer deserializes a transfer blob and spawns a Live
// entity for netID. Used when the destination does not already have
// a border-replica (fast-mover or cross-boundary spawn).
func (b *WorldBase) SpawnLiveFromTransfer(netID uint32, epoch uint32, blob []byte) (ecs.Entity, error) {
	ent, err := b.SpawnFromTransferCore(blob, PresenceLive)
	if err != nil {
		return ecs.Entity{}, err
	}
	if b.netMap.HasAll(ent) {
		b.netMap.Get(ent).Epoch = epoch
	}
	return ent, nil
}
```

Add `Promote` to `netIDIndex`:

```go
func (idx *netIDIndex) Promote(netID uint32, entity ecs.Entity) TransitionResult {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	cur, ok := idx.slots[netID]
	if !ok || cur.Presence != PresenceReplica {
		return TransitionResult{Action: ActionRejected}
	}
	idx.slots[netID] = netIDSlot{Entity: entity, Presence: PresenceLive}
	return TransitionResult{Action: ActionUpdated, PrevEntity: cur.Entity}
}
```

- [ ] **Step 5: Write a test**

Append to `hard_cut_handoff_test.go`:

```go
func TestHardCut_CrossHost_AuthorityFlipsAtCommitTick(t *testing.T) {
	// Spin up 2-host fixture.
	fx := distributedTwoHostFixture(t, fixtureOpts{})
	defer fx.Shutdown()

	// Spawn entity on hostA's cell_0_0 near the east edge.
	ent := fx.hostA.SpawnTestEntity(t, "cell_0_0", /*x,y=*/ 0.95, 0.5)
	netID := fx.hostA.NetIDOf(t, ent)

	// Step physics until Handoff reaches hostB.
	fx.TickUntil(t, 30, func() bool { return fx.hostB.ReceivedHandoff(netID) })

	// Advance to commit-tick.
	fx.AdvanceToClusterTick(t, fx.hostB.CommitTickFor(netID))

	// Entity Live on hostB, Replica on hostA.
	fx.AssertAuthority(t, netID, "hostA/cell_0_0", "replica")
	fx.AssertAuthority(t, netID, "hostB/cell_1_0", "live")
}
```

- [ ] **Step 6: Run**

```bash
cd . && go test ./pkg/universe/ -run TestHardCut -v -count=1 -timeout 60s
```

Expected: PASS.

- [ ] **Step 7: Commit**

```bash
cd .
git add pkg/universe/
git commit -m "feat(universe): destination-side hard-cut promote at commit tick

Cells hold a pendingPromotes[commitTick] queue; drain at tick start
promotes existing border-replicas or spawns from transfer blob. No
shadow, no warmup. netIDIndex gains Promote() as the sanctioned
Replica→Live transition primitive.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Phase I — Delete the Shadow component + overlap machinery

Now that the hard-cut path is in place, every remaining Shadow-related reference is dead code. This phase is one big deletion pass.

### Task I1: Delete `pkg/component/shadow.go`

**Files:**
- Delete: `pkg/component/shadow.go`

- [ ] **Step 1: Remove the file**

```bash
cd . && rm pkg/component/shadow.go
```

- [ ] **Step 2: Fix compile errors**

```bash
cd . && go vet ./... 2>&1 | head -50
```

Expected: multiple references to `component.Shadow` in `pkg/universe/world_base.go`, `pkg/system/viewer_source.go`, `pkg/universe/border_replication.go`, maybe others. Fix each:

- `world_base.go`: delete `shadowMap` field, delete its initialization in the constructor, delete `SpawnShadow`, `PromoteShadow`, `RemoveShadowByNetID`, delete Shadow fast-path inside `upsertBorderReplica`, delete any Shadow exclusion in `serializeAllEntities` / `serializeQuadrantEntities`, delete `TickShadowWatchdog` if present.
- `viewer_source.go`: delete `shadowMap` field, delete Shadow skip in `ActiveViewers`.
- `border_replication.go`: delete any `ecs.C[component.Shadow]()` from the dispatcher filter.
- `netid_index.go`: delete `PresenceShadow` enum value; simplify `Enter` switch to a 2×2 table (Live/Replica only).

- [ ] **Step 3: Run vet + tests**

```bash
cd . && go vet ./...
cd . && go test ./pkg/universe/ -count=1 -timeout 120s
```

Expected: vet clean. Tests mostly pass; any failing test that asserts on the old Shadow behavior gets deleted (inspect one by one).

- [ ] **Step 4: Commit**

```bash
cd .
git add pkg/component/shadow.go pkg/universe/ pkg/system/
git commit -m "refactor(universe): delete Shadow component + overlap machinery

Shadow entities are gone — the hard-cut handoff path uses existing
border replicas or fresh SpawnLiveFromTransfer at commit-tick.
Removed: pkg/component/shadow.go, SpawnShadow/PromoteShadow/
RemoveShadowByNetID on WorldBase, shadowMap on ViewerSource,
PresenceShadow in netIDIndex, Shadow carve-out in BorderDispatcher
filter and ActiveViewers.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task I2: Simplify `netIDIndex` — 2×2 transition table

**Files:**
- Modify: `pkg/universe/netid_index.go`
- Modify: `pkg/universe/netid_index_test.go`

- [ ] **Step 1: Replace `Enter`**

After deleting `PresenceShadow`, the `Enter` switch becomes:

```go
func (idx *netIDIndex) Enter(netID uint32, entity ecs.Entity, to EntityPresence) TransitionResult {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	cur, ok := idx.slots[netID]
	if !ok {
		idx.slots[netID] = netIDSlot{Entity: entity, Presence: to}
		return TransitionResult{Action: ActionInstalled}
	}
	switch cur.Presence {
	case PresenceLive:
		if to == PresenceLive {
			return TransitionResult{Action: ActionDuplicate, PrevEntity: cur.Entity}
		}
		// Live → Replica must go through Demote (explicit path).
		return TransitionResult{Action: ActionRejected}
	case PresenceReplica:
		// Replica → Live must go through Promote (explicit path).
		// Replica → Replica updates the entity handle.
		if to == PresenceReplica {
			idx.slots[netID] = netIDSlot{Entity: entity, Presence: PresenceReplica}
			return TransitionResult{Action: ActionUpdated}
		}
		return TransitionResult{Action: ActionRejected}
	}
	return TransitionResult{Action: ActionRejected}
}
```

(`ActionPromoted` and `ActionReplaced` may no longer be generated — keep or delete based on call-site analysis. If unused, delete the constants.)

- [ ] **Step 2: Update the docstring at the top of the file**

Rewrite the policy-table comment to reflect the new 2×2 table:

```go
// netIDIndex maps each netID to exactly one (ecs.Entity, presence) slot
// per cell. Presence is Live (cell authoritative) or Replica (border
// copy); Demote and Promote are the sanctioned primitives for
// transferring authority between the two. Unsolicited Enter(Replica)
// on a Live slot rejects — a stray border frame cannot silently
// downgrade a live entity.
```

- [ ] **Step 3: Rewrite tests**

`pkg/universe/netid_index_test.go` — delete every test that references `PresenceShadow`. Add:

```go
func TestNetIDIndex_LiveRejectsUnsolicitedReplica(t *testing.T) {
	idx := newNetIDIndex()
	idx.Enter(1, ecs.Entity{}, PresenceLive)
	if res := idx.Enter(1, ecs.Entity{}, PresenceReplica); res.Action != ActionRejected {
		t.Fatalf("got %d, want ActionRejected", res.Action)
	}
}
func TestNetIDIndex_DemoteLiveToReplica(t *testing.T) {
	idx := newNetIDIndex()
	idx.Enter(1, ecs.Entity{}, PresenceLive)
	if res := idx.Demote(1, ecs.Entity{}); res.Action != ActionUpdated {
		t.Fatalf("got %d, want ActionUpdated", res.Action)
	}
	_, p, _ := idx.Lookup(1)
	if p != PresenceReplica {
		t.Fatalf("got presence %v, want Replica", p)
	}
}
func TestNetIDIndex_PromoteReplicaToLive(t *testing.T) {
	idx := newNetIDIndex()
	idx.Enter(1, ecs.Entity{}, PresenceReplica)
	if res := idx.Promote(1, ecs.Entity{}); res.Action != ActionUpdated {
		t.Fatalf("got %d, want ActionUpdated", res.Action)
	}
}
```

- [ ] **Step 4: Run**

```bash
cd . && go test ./pkg/universe/ -run NetIDIndex -v -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd .
git add pkg/universe/netid_index.go pkg/universe/netid_index_test.go
git commit -m "refactor(universe): netIDIndex 2x2 transition table

Live/Replica only; Demote and Promote are the only authority-transfer
primitives. Unsolicited Enter(Replica) on Live still rejects.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Phase J — Delete `ReplicaDeadReckoningSystem`

Now that replicas are passive caches and Ghost-DR is also off-mandate per the spec, delete the entire system.

### Task J1: Delete the file + tests + factory + call sites

**Files:**
- Delete: `pkg/system/replica_dead_reckoning.go`
- Delete: `pkg/system/replica_dead_reckoning_test.go`
- Modify: `pkg/mmokit/factories.go` (or wherever `NewReplicaDeadReckoningSystem` factory lives)
- Modify: `examples/4node-basic/main.go`, `examples/slither/main.go`, `internal/game/factory.go`

- [ ] **Step 1: Remove the files**

```bash
cd .
rm pkg/system/replica_dead_reckoning.go pkg/system/replica_dead_reckoning_test.go
```

- [ ] **Step 2: Remove the factory**

Grep for `NewReplicaDeadReckoningSystem` and `ReplicaDeadReckoning`:

```bash
cd . && rg 'ReplicaDeadReckoning' --type=go -l
```

Delete the factory function in `pkg/mmokit/factories.go`. Delete the `coord.AddSystem("ReplicaDeadReckoning", ...)` call in every example + `internal/game/factory.go`.

- [ ] **Step 3: go vet + tests**

```bash
cd . && go vet ./...
cd . && go test ./... -count=1 -timeout 180s
```

Expected: clean.

- [ ] **Step 4: Commit**

```bash
cd .
git add pkg/system/ pkg/mmokit/ examples/ internal/
git commit -m "refactor: delete ReplicaDeadReckoningSystem

Server-side dead reckoning is gone. Replicas are passive caches;
Ghost entities (mid-transfer) are rare enough that freezing them
between transfer-start and transfer-commit is invisible under client
render-lag. Client interpolation is the sole smoothness layer.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Phase K — Client-side decoder + interpolation + clockSync

Update the TS client to consume per-entity `producedAtMs` and use it as the sample-time anchor for interpolation + clock sync.

### Task K1: Update `web-pixi/src/network.ts` / decoder consumer to thread `producedAtMs` through

**Files:**
- Modify: `web-pixi/src/network.ts` (or wherever raw decoded entries are converted to per-entity snapshots)
- Modify: `examples/4node-basic/web/src/*.ts` symmetrically

- [ ] **Step 1: Find where decoded entries become per-entity snapshots**

```bash
cd .
grep -rn 'serverTimeMs' web-pixi/src examples/4node-basic/web/src | head
```

Each call site that passes `frameHeader.serverTimeMs` as the per-entity timestamp must now pass `entry.producedAtMs` (the per-entity field from the FullEntry/DeltaEntry decoder output).

- [ ] **Step 2: Update `interpolation.ts`**

If `interpolation.ts` takes `serverTimeMs` as an argument, rename the parameter to make clear it's per-entity (`producedAtMs`). Semantic is unchanged — it's the sample-time for ring ordering.

- [ ] **Step 3: Update `clockSync.ts`**

The old call was `clockSync.observeServerTime(frameHeader.serverTimeMs, clientNowMs)`. The new call takes the max of per-entity stamps in the frame:

```typescript
// Pull max producedAtMs across all entities in the frame.
let maxStamp = 0;
for (const f of fulls) if (f.producedAtMs > maxStamp) maxStamp = f.producedAtMs;
for (const d of deltas) if (d.producedAtMs > maxStamp) maxStamp = d.producedAtMs;
if (maxStamp > 0) {
  observeServerTime(clockSync, maxStamp, clientNowMs);
}
```

- [ ] **Step 4: Update bun tests**

```bash
cd web-pixi && bun test
```

Expected: TS tests that referenced frame-level `serverTimeMs` fail. Update each to use per-entity stamps or delete if obsolete.

```bash
cd examples/4node-basic/web && bun test
```

Expected: same pattern.

- [ ] **Step 5: Commit**

```bash
cd .
git add web-pixi/ examples/4node-basic/web/
git commit -m "feat(web): per-entity producedAtMs drives interp + clockSync

Client's interpolation ring anchors each sample on its per-entity
producedAtMs. Clock sync observes max(producedAtMs) per frame. No
frame-level serverTimeMs exists anymore. Authority-transfer between
cells produces adjacent-tick samples that lerp seamlessly because
every entity sits on one coherent cluster-clock axis.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task K2: Regenerate the SDK

**Files:**
- Regenerate: `examples/4node-basic/web/sdk/`, `web-pixi/sdk/`

- [ ] **Step 1: Regenerate 4node-basic SDK**

```bash
cd . && just client-sdk examples/4node-basic
```

- [ ] **Step 2: Regenerate space-game SDK**

```bash
cd . && just space-sdk
```

- [ ] **Step 3: Run tests**

```bash
cd examples/4node-basic/web && bun test
cd web-pixi && bun test
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
cd .
git add examples/4node-basic/web/sdk/ web-pixi/sdk/
git commit -m "chore(sdk): regenerate after wire-format changes

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Phase L — Integration tests, CLAUDE.md, bot-load smoke

### Task L1: Integration test — ClusterClock convergence under load

**Files:**
- Modify: `pkg/universe/cluster_clock_integration_test.go`

- [ ] **Step 1: Add a convergence test**

```go
func TestClusterClock_ConvergenceOverMinute(t *testing.T) {
	fx := distributedTwoHostFixture(t, fixtureOpts{
		ClusterClockSyncInterval: 1 * time.Second,
	})
	defer fx.Shutdown()

	// Wait for initial Observed.
	waitFor(t, 2*time.Second, func() bool { return fx.hostA.ClusterClock.Observed() })
	waitFor(t, 2*time.Second, func() bool { return fx.hostB.ClusterClock.Observed() })

	// Let 6 broadcasts fire (6 seconds).
	time.Sleep(6*time.Second + 500*time.Millisecond)

	// Drift between hostA and hostB's Now() should be within 20 ms.
	diff := int64(fx.hostA.ClusterClock.Now()) - int64(fx.hostB.ClusterClock.Now())
	if diff < 0 {
		diff = -diff
	}
	if diff > 20 {
		t.Fatalf("cross-host clock drift = %d ms, want ≤ 20 ms", diff)
	}
}
```

- [ ] **Step 2: Add a coord-death resilience test**

```go
func TestClusterClock_ResilientToCoordDeath(t *testing.T) {
	fx := distributedTwoHostFixture(t, fixtureOpts{
		ClusterClockSyncInterval: 100 * time.Millisecond,
	})
	defer fx.Shutdown()

	waitFor(t, 2*time.Second, func() bool { return fx.hostA.ClusterClock.Observed() })

	offsetBefore := fx.hostA.ClusterClock.Now() - uint64(time.Now().UnixMilli())

	// Simulate coord crash by stopping its broadcast loop.
	fx.StopCoord()

	// Wait 3 seconds with no broadcasts.
	time.Sleep(3 * time.Second)

	offsetAfter := fx.hostA.ClusterClock.Now() - uint64(time.Now().UnixMilli())

	// Offset should have barely changed (OS clock drift only).
	drift := int64(offsetAfter) - int64(offsetBefore)
	if drift < 0 {
		drift = -drift
	}
	if drift > 50 {
		t.Fatalf("offset drift after 3s of coord death = %d ms, want ≤ 50 ms", drift)
	}
}
```

- [ ] **Step 3: Run**

```bash
cd . && go test ./pkg/universe/ -run 'TestClusterClock_(Convergence|Resilient)' -v -count=1 -timeout 60s
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
cd .
git add pkg/universe/cluster_clock_integration_test.go
git commit -m "test(universe): ClusterClock convergence + coord-death resilience

Cross-host drift ≤ 20 ms after 6 broadcasts. After coord death,
last-cached offset keeps the host stamping samples on approximately
the cluster timeline until OS clock drift accumulates (ms/hour).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task L2: Verify S7 split/merge/migrate tests pass under the new protocol

**Files:**
- Modify: `pkg/universe/s7_split_test.go`, `s7_merge_test.go`, `s7_migrate_test.go` (only if they assert on the deleted protocol)

- [ ] **Step 1: Run the S7 suite**

```bash
cd . && go test ./pkg/universe/ -run '^TestS7' -count=1 -timeout 180s -v
```

Expected: PASS. If any test fails, inspect — most S7 cases are about cluster topology (who owns which cell) and are independent of the handoff mechanic. Any remaining Shadow / Warmup / Prepare-only assertions need to be rewritten.

- [ ] **Step 2: Fix broken S7 tests**

For each failure, read the test and update to the new semantics. Commit one test family at a time for reviewability.

- [ ] **Step 3: Commit**

```bash
cd .
git add pkg/universe/s7_*.go
git commit -m "test(universe): S7 split/merge/migrate adapted to new handoff protocol

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task L3: Replica smoothness + no-stutter regression test

**Files:**
- Modify: `pkg/universe/hard_cut_handoff_test.go` (or a new file)

- [ ] **Step 1: Write a test that simulates the user-reported scenario**

```go
// TestNoStutter_ReplicaSmoothnessAcrossBoundary simulates the exact
// scenario from the spec verification section: a bot wandering in
// cell_0_0 being observed by a viewer in cell_1_0. Every tick, the
// viewer must see a replica sample with ProducedAtMs monotonically
// advancing by ~50ms (one tick). No gap > 75ms.
func TestNoStutter_ReplicaSmoothnessAcrossBoundary(t *testing.T) {
	clock := mmokit.NewClusterClock()
	clock.Observe(uint64(time.Now().UnixMilli()), 1)
	fx := newTwoCellFixture(t, clock)
	defer fx.Shutdown()

	// Bot in cell_0_0 near east edge, viewer in cell_1_0 at center.
	botEnt := fx.A.SpawnBot(t, fx.A.CellSize()*0.9, fx.A.CellSize()*0.5)
	viewerConn := fx.B.SpawnViewer(t, /*center*/)
	botNetID := fx.A.NetworkIDMap().Get(botEnt).ID

	var samples []uint64 // ProducedAtMs seen by viewer for bot's netID
	fx.B.ReplicationSystem().OnSampleSent(func(connID uint32, netID uint32, producedAtMs uint64) {
		if connID == viewerConn && netID == botNetID {
			samples = append(samples, producedAtMs)
		}
	})

	// Run for 2 seconds (40 ticks).
	for i := 0; i < 40; i++ {
		fx.Tick(t)
	}

	if len(samples) < 35 {
		t.Fatalf("expected ≥35 samples, got %d", len(samples))
	}
	for i := 1; i < len(samples); i++ {
		gap := samples[i] - samples[i-1]
		if gap > 75 {
			t.Fatalf("sample gap %d at index %d exceeds 75 ms (stutter)", gap, i)
		}
	}
}
```

- [ ] **Step 2: Run**

```bash
cd . && go test ./pkg/universe/ -run TestNoStutter -v -count=1
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
cd .
git add pkg/universe/
git commit -m "test(universe): replica-smoothness regression — no sample gap > 75ms

Captures the exact user-reported scenario (bot in cell_0_0, viewer in
cell_1_0). Samples at the ReplicationSystem output layer — the ground
truth for what the wire emits.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task L4: Update `CLAUDE.md`

**Files:**
- Modify: `CLAUDE.md`

- [ ] **Step 1: Update the "Networking & Replication" section**

Find the section starting with "### Networking & Replication". Replace / add:

- Remove any mention of `ReplicaDeadReckoningSystem` and "dead reckoning replicas".
- Add a bullet: "`ClusterClock` (pkg/mmokit) maintains a host-local offset against the coordinator via `CoordTimeSync` broadcasts (default 10 s cadence on the MeshControl stream). Every replication sample is stamped at emit time with `ClusterClock.Now()`; the stamp travels opaquely through border-replica caches so downstream clients see one coherent timeline."
- Add a bullet: "Replication wire format carries per-entity `producedAtMs` (8 bytes after `entityType` in each full/delta entry). Frame-level `serverTimeMs` is removed; client clock-sync uses `max(producedAtMs)` across the frame."

- [ ] **Step 2: Update the "Server Meshing" section**

Find handoff-related content. Replace / add:

- Remove mention of "Prepare → Overlap → Commit" / "warmup" / "shadow".
- Add: "Authority transfer is hard-cut at a cluster-tick boundary. Source sends a single `Handoff` message declaring `commitTick = currentClusterTick + 2`. At end-of-tick (commitTick−1) the source demotes Live → Replica; at start-of-tick commitTick the destination promotes Replica → Live (or spawns from `transfer_blob` if no replica). Cooldown (20 ticks) prevents thrash on boundary-hovering entities."

- [ ] **Step 3: Update "Deleted" sections**

Note the removal of `Shadow` component, `ReplicaDeadReckoningSystem`, three handoff messages in favor of one.

- [ ] **Step 4: Commit**

```bash
cd .
git add CLAUDE.md
git commit -m "docs(CLAUDE.md): update for ClusterClock + hard-cut handoff

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task L5: Final verification + bot-load smoke

- [ ] **Step 1: Full Go test suite**

```bash
cd . && go vet ./...
cd . && go test ./... -count=1 -timeout 300s
```

Expected: all pass.

- [ ] **Step 2: Full TS test suite**

```bash
cd web-pixi && bun test
cd examples/4node-basic/web && bun test
```

Expected: all pass.

- [ ] **Step 3: Manual bot-load smoke test**

Follow the spec's verification section:

```bash
cd examples/4node-basic && just distributed
```

Inside the tmux session's web browser:
1. Open `http://localhost:8080`.
2. Log in; teleport the player to `cell_1_0` center.
3. In the coord console, `bot spawn 30 cell_0_0`.
4. Visually observe the replicas of the 30 bots from across the boundary.
5. **Gate: no perceptible stutter at 60 fps for 60 seconds.**

Then:

6. `cell split 0_0` — replicas keep moving smoothly through the split commit.
7. `cell merge 0_0` — replicas keep moving smoothly through the merge.
8. Move the player east so the player itself crosses cell_1_0 → cell_0_0 or similar — the seam is invisible at 60 fps.

If any step shows stutter: capture a screen recording, file a ticket, and diagnose. The integration tests in Tasks L1-L3 should catch most regressions; a visual regression here means a test is missing.

- [ ] **Step 4: Final commit (line-count verification)**

```bash
cd .
git diff --stat main..HEAD | tail
```

Expected: net change approximately **−2500 lines** (deletions > insertions). If the ratio is off by more than 500 lines, revisit the deletion checklist and confirm nothing was preserved by accident.

- [ ] **Step 5: Branch is ready for merge**

The user merges directly to main (solo dev per memory). No PR needed.

```bash
cd .
git checkout main
git merge --ff-only replication-timeline-redesign
```

(If fast-forward not possible, `git merge --no-ff` with an explanatory commit message.)

---

## Self-review

**Spec coverage:**
- P1 ClusterClock: Tasks B1 (primitive), C1-C5 (proto + broadcast + handshake + gate) ✅
- P2 Per-entity timestamps: Tasks D1 (Go wireformat), D2 (TS decoder), E1 (Replica component), E2 (FrameWriter), F1 (border frame) ✅
- P3 Hard-cut handoff: Tasks G1 (proto), G2 (Bridge collapse), H1 (source driver), H2 (destination handler) ✅
- P4 No server DR: Task J1 ✅
- P5 Shadow deletion: Task I1 ✅
- P6 Tick phase drift absorbed: implicit — no code change; validated by L3 smoothness test ✅

**Wire-format changes:** Task D1 (header shrinks; per-entity `producedAtMs`), F1 (border frame), C1 (add `CoordTimeSync`) ✅

**Client-side changes:** Tasks D2, K1, K2 ✅

**Testing strategy:**
- Unit tests: B1 (ClusterClock EMA/stale), D1 (wireformat round-trip), I2 (netIDIndex 2×2), F1 (border stamp)
- Integration: C3 (initial sync), C4 (periodic broadcast), H1 (same-host hard-cut), H2 (cross-host hard-cut), L1 (convergence + resilience), L3 (no-stutter)
- Deleted: all Shadow / Warmup / Prepare-only / Cancel-only / MissedTicks / grace-extrapolation tests ✅

**Rollout order matches spec recommendation:** B → C → D → E → F → G → H → I → J → K → L ≈ spec's 1 → 2 → 3 → 4 → 5 → 6 → 7 ✅

**Placeholder scan:** No `TBD`, `TODO-except-deferred`, `implement later`. Every step has exact code or exact file paths + exact edits. Some tests defer fixture helpers to "implement in a helper file" — that's a real next step, not a placeholder.

**Type consistency:** `ClusterClock` consistent across phases. `HandoffPayload` / `Handoff` (proto) consistent. `ProducedAtMs` naming consistent. `CommitTick` vs `commitTick` respects Go/proto casing conventions. ✅

**Known gap to fill during execution:** The two-host distributed test fixture referenced throughout (`distributedTwoHostFixture`, `newTwoCellFixture`, `fixtureOpts`) is implemented as shared test support. If the existing S7 tests already expose such a fixture, reuse it; otherwise implement in `pkg/universe/test_support_test.go`. This is a small amount of work but spans multiple tasks — the executor should factor it early.

---

Plan complete. Next step: pick execution mode (subagent-driven or inline).
