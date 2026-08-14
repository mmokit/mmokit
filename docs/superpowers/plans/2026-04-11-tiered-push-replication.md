# Tiered Push Replication + Co-Simulation Handoff Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace mmokit's current border replication and ghost-based handoff with a tiered push replication system and a co-simulation handoff state machine, sharing primitives between the client and inter-node dispatchers, with client-baseline handover to keep meshing invisible to the client.

**Architecture:** Extract a new `pkg/replication/` package containing shared primitives (tier evaluation, baselines, frame types, viewer interface, dispatcher). `pkg/system/replication.go` (client dispatcher) and a new `pkg/universe/border_replication.go` (inter-node dispatcher) become parallel consumers. A new handoff state machine runs the `Border → Promoted → Handoff → committed` lifecycle with `MinWarmupTicks` warmup floor and `CrossingCooldownTicks` hysteresis. The old `ReplicaFrame`, `ProxySummary`, `MsgDetail{Request,Response}`, ghost-based server authority, and `cfg.ProxiesEnabled` are all deleted.

**Tech Stack:** Go 1.22+, mlange-42/ark ECS, existing `pkg/quantize/` framing, existing `pkg/net/` WebSocket transport, `just` task runner.

**Spec:** [docs/superpowers/specs/2026-04-11-tiered-push-replication-design.md](../specs/2026-04-11-tiered-push-replication-design.md)

**Branch:** `feature/tiered-push-replication` (already created, contains only spec commits so far)

---

## Status (2026-04-12, post-merge)

**Shipped on `main` via merge of `feature/tiered-push-replication`:**
- Phase 1-6 complete as planned: Epoch through NetworkID/quantize/system/TypeScript decoder, new `pkg/replication/` shared primitives, `pkg/system/replication.go` refactored as first consumer, NodeViewer adapter, BorderDispatcher, handoff state machine + baseline handover helpers, loopback test bridge.
- Phase 7.1-7.4: BorderDispatcher wired into production `PostSystems` alongside the legacy proxy path, then the `MsgBorderFrame` receive handler added.
- Phase 7.6: Atomic cutover deleted ~1800 lines of legacy code (`replication_scan.go`, `ReplicaFrame`, `ProxySummary`, `MsgReplica`, `MsgProxySummary`, `MsgDetailRequest`, `MsgDetailResponse`, `RequestDetail`, `SendDetailResponse`, `ProxiesEnabled` config, `ScanBorderProxies`/`ApplyProxySummaries`/`PromoteProxy`/etc.) and upgraded `MsgBorderFrame` to create/update replicas with world-space coordinates and epoch-based stale-packet detection.
- Phase 8.1: Inter-node metrics counters (`RecordBorderFrameSent`/`Recv` + `InterNodeSnapshot`) wired into `NodeViewer.Send` and `Node.processMessage`.
- Phase 8.2-8.5: Unit tests for `ApplyBorderFrame` (create, update, stale-epoch drop, truncated buf, multi-entity, auto-fill, registry-driven round-trip, legacy back-compat, unknown-ID skip, cross-frame updates), 2 benchmarks (apply hot path + encode/decode round-trip), counter plumbing tests.
- Phase 7.2 (post-cutover): Registry-driven per-component border frame tail — `[u16 count][repeated: u16 id, u16 len, N bytes]` — shipped after the cutover as the fix for the teleport panic. Ship replicas now carry real Health/Shield/Inventory/etc. from the sender, not zero-valued placeholders. Old 18-byte frames decode as zero-component frames for free.

**Post-merge bugfixes landed on the same branch:**

- `EnsureEntityKindComponents` auto-fills all kind-registered components on replica create so `reflectBinding.HasAll` never panics inside `ReplicationSystem.Update`. Revert of 602f0c5's overzealous dispatcher-level replica skip.
- `NodeViewer` default tier radius extended to `coords.CellSize * 2` so corner-of-cell entities reach both cardinal and diagonal neighbors (fix for asymmetric visibility where only the diagonal neighbor saw corner entities).
- Client-side teleport interpolation anchors prev to the previous server snapshot instead of `renderX`, eliminating multi-tick geometric ease-in on jump moves.

**Deferred to roadmap #12 follow-up (as unwired infrastructure):**

- Phase 7.5 + 7.7: Full co-simulation handoff state machine wiring (promote/commit flow, `MsgHandoffPrepare`/`MsgHandoffCommit` send/receive integration, `Coordinator.UpdatePlayerRoute` atomic routing update). The existing `MsgTransfer` + `Ghost` + `ArrivalConfirm` protocol continues to handle entity ownership transfer.
- Built-but-unwired for #12 to pick up: `pkg/universe/handoff.go` (state machine), `pkg/universe/baseline_handover.go` (Case A + Case B helpers), `MsgHandoffPrepare`/`MsgHandoffCommit`/`MsgForwardInput` message types in `pkg/universe/message.go`, `pkg/universe/loopback_bridge.go` (integration test harness).
- Delta compression of border frames (in progress on `cleanup/post-tiered-push`): current path sends the full registry-driven component tail every tick. The `BaselineStore` allocated on each `NodeViewer` is unused; wire it through `BorderDispatcher`'s Build closure for delta encoding against acknowledged baselines. Expected 60–80% bandwidth reduction for mostly-static entities.

**Known regressions on this branch:**
- Slither multi-node snake visual fidelity — long snakes whose body tail extends across a cell boundary while the head is elsewhere are not fully visible on the neighbor node. The legacy path had a slither-specific `ScanBorderEntities` override that walked body segments; `BorderDispatcher.entityNearNeighborEdge` only tests the head's Position. Fix requires a game-facing candidate-provider hook on BorderDispatcher. Out of scope for this refactor; documented for the slither example maintainer.
- The space game (`internal/game/`) has no equivalent regression — no entities have out-of-band spatial extent beyond their collider radius.

**Post-merge follow-up tracking:** See `docs/planning/mmokit-roadmap.md` Feature #11 and the user's auto-memory note `memory/project_cosim_handoff_deferred.md`.

---

## Work Plan Overview

Work is split into eight phases. Each phase ends at a known-good state where `just build` passes and relevant tests are green. Phase boundaries are natural commit/review checkpoints.

| Phase | Goal | Build-green at end? |
|---|---|---|
| 0 | Precondition checks, baseline measurements | yes |
| 1 | Add `Epoch` field to `NetworkID`, propagate through quantize | yes |
| 2 | Extract `pkg/replication/` package with shared primitives | yes |
| 3 | Refactor `pkg/system/replication.go` as consumer | yes |
| 4 | Build inter-node border dispatcher (new path only, old still runs) | yes |
| 5 | Handoff state machine + co-simulation + baseline handover | yes |
| 6 | Loopback test harness + correctness tests | yes |
| 7 | Delete old replica/proxy/ghost-authority paths; cut over to new | yes |
| 8 | Performance tests + metrics + verification | yes |

## File Structure

### New files

| Path | Responsibility |
|---|---|
| `pkg/replication/tier.go` | `ReplicationTier` struct + tier evaluation helpers |
| `pkg/replication/priority.go` | Per-viewer priority accumulator (entityPriorityState, priority math) |
| `pkg/replication/baseline.go` | `BaselineStore` (moved from `pkg/system/baseline.go`) and ack modes |
| `pkg/replication/frame.go` | `Frame`, `FrameEntry` types with `Encode`/`Decode`/`SizeEncoded` methods |
| `pkg/replication/viewer.go` | `Viewer` interface |
| `pkg/replication/dispatcher.go` | Shared dispatcher loop building a frame for a single viewer |
| `pkg/replication/tier_test.go` | Unit tests for tier evaluation |
| `pkg/replication/priority_test.go` | Unit tests for priority accumulator |
| `pkg/replication/baseline_test.go` | Unit tests for baseline store |
| `pkg/replication/frame_test.go` | Unit tests for frame encode/decode/size |
| `pkg/universe/border_replication.go` | Inter-node dispatcher, neighbor-as-viewer |
| `pkg/universe/handoff.go` | Co-simulation authority state machine |
| `pkg/universe/loopback_bridge.go` | Test-only `NodeBridge` with encode/decode + latency/loss |
| `pkg/universe/border_replication_test.go` | 8 correctness tests |
| `pkg/universe/border_replication_perf_test.go` | 7 performance benchmarks |
| `pkg/universe/testdata/border_replication_perf.golden` | Golden numbers for perf assertions |

### Modified files

| Path | Change |
|---|---|
| `pkg/component/core.go` | Add `Epoch uint32` field to `NetworkID` |
| `pkg/quantize/wireformat.go` | Add `Epoch uint32` to `FullEntry`/`DeltaEntry`; encoder/decoder write/read 4 extra bytes per entry |
| `pkg/system/replication.go` | Refactor as consumer of `pkg/replication/`; delete old `connectionState`/`entityPriorityState`/`entityBaseline` |
| `pkg/system/baseline.go` | Deleted (content moves to `pkg/replication/baseline.go`) |
| `pkg/universe/node_bridge_impl.go` | Remove `RequestDetail`/`SendDetailResponse`, replace `sendReplicas`/`sendProxies` with border dispatcher call |
| `pkg/universe/bridge.go` | Remove `RequestDetail`/`SendDetailResponse` from `NodeBridge` interface; add `SendBorderFrame`, `SendHandoffPrepare`, `SendHandoffCommit` |
| `pkg/universe/message.go` | Remove `MsgReplica`, `MsgProxySummary`, `MsgDetailRequest`, `MsgDetailResponse`; add `MsgBorderFrame`, `MsgHandoffPrepare`, `MsgHandoffCommit`, `MsgForwardInput` |
| `pkg/universe/world_base.go` | Delete `TickGhosts`/`RemoveGhostByNetID` authority role, `ScanBorderEntities`/`ScanBorderProxies`/`ApplyReplicas`/`ApplyProxySummaries`, `BuildDetailResponse`, `PromoteProxy`, `RequestPromotion`, `replicaNetIDs`/`proxyNetIDs` maps; add handoff state machine storage |
| `pkg/universe/replication.go` | Delete `ReplicaFrame`, `ProxySummary`, `DetailRequestMsg`, `DetailResponseMsg`, related marshal/unmarshal; keep `ReplicationRegistry`, `ComponentReplicator`, `ComponentID`, `ComponentSlice` (still used by `transfer.go`) |
| `pkg/universe/replication_scan.go` | Deleted |
| `pkg/universe/node.go` | Remove `MsgReplica`/`MsgProxySummary`/`MsgDetailRequest`/`MsgDetailResponse` cases in `processMessage`; add `MsgBorderFrame`/`MsgHandoffPrepare`/`MsgHandoffCommit`/`MsgForwardInput` cases |
| `pkg/universe/coordinator.go` | Remove `ProxiesEnabled` from `Config`; add `UpdatePlayerRoute(connID, nodeID)` method |
| `pkg/universe/transfer.go` | `TransferFrame.NetworkID` stays `uint32` (only the ID); handoff state machine carries Epoch separately in `HandoffPreparePayload` |
| `pkg/metrics/node_metrics.go` | New counters: `InterNodeBytesSent/Recv`, `BorderFramesSent`, `HandoffsInitiated/Committed`, `StalePacketsDropped`, `EntitiesInBorder/Promoted/Handoff` |
| `cmd/sdkgen/generate.go` | Recognize `NetworkID.Epoch` in schema reflection |
| `pkg/universe/universe_test.go` | Delete `MsgReplica` test case; other tests unaffected |
| `pkg/universe/replication_test.go` | Delete `TestReplicaFrame_*` and `TestProxySummary_*` tests; keep `TestReplicationRegistry_*` tests (registry is still used for transfers) |
| `internal/game/replica_test.go` | Delete or rewrite for new replica model (no direct old-path coverage) |
| `internal/game/transfer_test.go` | No changes (already uses `NetworkID{ID: x}` literal form, which stays valid) |

### Deleted files

- `pkg/universe/replication_scan.go`
- `pkg/system/baseline.go` (content migrated)

---

## Phase 0: Preconditions & Baseline

**Goal:** Confirm the working tree is clean on `feature/tiered-push-replication`, verify the current build is green, and capture before-refactor performance numbers to compare against later.

### Task 0.1: Verify branch and clean tree

**Files:**
- Read-only

- [ ] **Step 1: Confirm branch**

Run: `git status && git branch --show-current`
Expected:
```
On branch feature/tiered-push-replication
nothing to commit, working tree clean
feature/tiered-push-replication
```

- [ ] **Step 2: Confirm build passes**

Run: `just build`
Expected: compiles cleanly, writes binary to `bin/server`.

- [ ] **Step 3: Confirm tests pass**

Run: `go vet ./... && go test ./...`
Expected: all packages pass.

### Task 0.2: Capture baseline perf numbers

**Files:**
- Create: `pkg/universe/testdata/border_replication_perf.golden.baseline` (throwaway, checked in temporarily)

- [ ] **Step 1: Build the baseline capture program**

```bash
cat > /tmp/baseline_capture.go <<'EOF'
// Standalone measurement program — DO NOT check into the repo.
// Measures current inter-node byte send volume for 100 entities on a 2-cell mesh.
package main

import (
    "context"
    "fmt"
    "time"

    "github.com/mmokit/mmokit/pkg/mmokit"
)

func main() {
    // Use the simplest coordinator config with ProxiesEnabled off and on.
    // Record bytes-per-tick for both. Print both numbers.
    // Implementation details: spawn 100 dummy entities near the boundary,
    // run 200 ticks, sum the neighbor-message bytes from the inbox.
    // The exact measurement code is game-specific and will be replaced
    // by the real perf test in Phase 8. This baseline is only to give
    // the implementer a sanity-check target.
    _ = mmokit.Config{}
    _ = context.Background
    _ = time.Second
    fmt.Println("baseline capture placeholder — intentionally minimal")
}
EOF
```

- [ ] **Step 2: Document the manual baseline procedure**

Write `pkg/universe/testdata/border_replication_perf.golden.baseline` with:

```
# Pre-refactor baseline for border replication bandwidth.
# Captured on feature/tiered-push-replication at commit $(git rev-parse --short HEAD).
# Method: boot examples/4node-basic, spawn 100 NPCs on the 0,0<->1,0 boundary,
# let it run 10 seconds, read Prometheus metrics/log output for proxy byte counts.
# These numbers are illustrative and will be superseded by Phase 8 golden values.

# Full-replica mode (ProxiesEnabled = false):
#   ~105 bytes/entity/tick * 100 entities = ~10.5 KB/tick per neighbor
# Proxy mode (ProxiesEnabled = true):
#   29 bytes/entity/tick * 100 entities = ~2.9 KB/tick per neighbor
#
# Target after refactor (tiered push with soft visibility):
#   <= 40% of full-replica for unobserved entities
#   delta-compressed for observed entities
```

- [ ] **Step 3: Commit the baseline note**

```bash
git add pkg/universe/testdata/border_replication_perf.golden.baseline
git commit -m "chore(perf): capture baseline numbers before tiered-push refactor

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Phase 1: NetworkID.Epoch Field

**Goal:** Add an `Epoch uint32` field to `component.NetworkID` and propagate it through `pkg/quantize/` entries and the client SDK generator, without changing any runtime behavior. All tests stay green. Epoch is always 0 at this phase — handoff state machine in Phase 5 starts incrementing it.

### Task 1.1: Add Epoch field to NetworkID

**Files:**
- Modify: `pkg/component/core.go:34-36`

- [ ] **Step 1: Update the struct**

Edit `pkg/component/core.go` lines 33-36 from:

```go
// NetworkID is a stable identifier sent to clients.
type NetworkID struct {
	ID uint32
}
```

to:

```go
// NetworkID is a stable identifier sent to clients.
// Epoch increments on each authority transfer and is used by receivers
// to drop stale frames from a previous owner.
type NetworkID struct {
	ID    uint32
	Epoch uint32
}
```

- [ ] **Step 2: Verify build**

Run: `go vet ./pkg/... ./internal/... ./examples/... ./cmd/...`
Expected: clean — `NetworkID{ID: x}` literals still compile because Epoch defaults to 0.

- [ ] **Step 3: Run tests**

Run: `go test ./...`
Expected: all pass.

- [ ] **Step 4: Commit**

```bash
git add pkg/component/core.go
git commit -m "feat(component): add Epoch field to NetworkID

Zero runtime impact — Epoch always 0 until Phase 5 handoff state
machine begins incrementing on authority transfer.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 1.2: Carry Epoch through quantize FullEntry/DeltaEntry

**Files:**
- Modify: `pkg/quantize/wireformat.go` — Go encoder/decoder
- Modify: `pkg/quantize/wireformat_test.go` — Go round-trip test
- Modify: `pkg/quantize/ts/delta-decoder-core.ts` — canonical TypeScript binary decoder
- Modify: `examples/4node-basic/web/sdk/_core/delta-decoder-core.ts` — vendored copy, regenerated
- Modify: `web-pixi/sdk/_core/delta-decoder-core.ts` — vendored copy, regenerated

**Critical coupling:** Any change to the on-wire layout of `FullEntry`/`DeltaEntry` MUST update the hand-rolled TypeScript binary decoder in the same commit (or a same-phase follow-up commit). The TS decoder at `pkg/quantize/ts/delta-decoder-core.ts` parses byte offsets directly. If Go inserts a new field without a matching TS update, every client silently misaligns every frame decode, corrupting entity state. The user's `feedback_wire_format_schema_runtime_match` memory explicitly forbids this drift.

`cmd/sdkgen/main.go` copies the canonical decoder into each SDK's `_core/` directory during `just client-sdk` / `just space-sdk`. Updating the canonical file and re-running SDK generation propagates the fix to all vendored copies. No hand-editing of vendored copies is required.

- [ ] **Step 1: Write failing test**

Add to `pkg/quantize/wireformat_test.go`:

```go
func TestFrameEncoder_CarriesEpoch(t *testing.T) {
	enc := NewFrameEncoder(256)
	full := []FullEntry{{NetID: 42, Epoch: 7, EntityType: 1, Snapshot: []byte{0xAA, 0xBB}}}
	deltas := []DeltaEntry{{NetID: 43, Epoch: 9, EntityType: 2, Data: []byte{0xCC}}}
	data := enc.Encode(1, 1, full, deltas, nil, nil)

	dec := NewFrameDecoder(data)
	_ = dec.Header()
	got := dec.NextFull()
	if got.NetID != 42 || got.Epoch != 7 {
		t.Fatalf("full: got NetID=%d Epoch=%d, want 42/7", got.NetID, got.Epoch)
	}
	gotD := dec.NextDelta()
	if gotD.NetID != 43 || gotD.Epoch != 9 {
		t.Fatalf("delta: got NetID=%d Epoch=%d, want 43/9", gotD.NetID, gotD.Epoch)
	}
}
```

- [ ] **Step 2: Run test to see it fail to compile**

Run: `go test ./pkg/quantize/ -run TestFrameEncoder_CarriesEpoch`
Expected: compile error — `FullEntry` has no `Epoch` field.

- [ ] **Step 3: Add Epoch to FullEntry/DeltaEntry**

Edit `pkg/quantize/wireformat.go` lines 47-60:

```go
// FullEntry is a decoded full-snapshot entity from the wire format.
type FullEntry struct {
	NetID       uint32
	Epoch       uint32
	EntityType  uint8
	Snapshot    []byte
	InitialData []byte // nil if length was 0
}

// DeltaEntry is a decoded delta-encoded entity from the wire format.
type DeltaEntry struct {
	NetID      uint32
	Epoch      uint32
	EntityType uint8
	Data       []byte // bitmask + changed fields
}
```

- [ ] **Step 4: Update Encode to write Epoch**

Read the current `Encode` method (around line 73-126). For each `full` entry, after writing `NetID` add a write for `Epoch`. Same for `deltas`. Locate the two loops and add `e.buf = e.appendUint32(e.buf, full[i].Epoch)` directly after the existing `appendUint32` for `NetID`. Same pattern for the deltas loop.

- [ ] **Step 5: Update Decode (NextFull, NextDelta)**

Read the current `NextFull` (line ~164) and `NextDelta` (line ~183). Insert `result.Epoch = d.readUint32()` directly after the `NetID` read in each.

- [ ] **Step 6: Run the new test**

Run: `go test ./pkg/quantize/ -run TestFrameEncoder_CarriesEpoch -v`
Expected: PASS.

- [ ] **Step 7: Run all quantize tests**

Run: `go test ./pkg/quantize/ -v`
Expected: all pass. Existing tests that don't set `Epoch` get `Epoch=0` which round-trips identically.

- [ ] **Step 8: Run full build**

Run: `go vet ./... && go test ./pkg/...`
Expected: clean.

- [ ] **Step 9: Commit the Go change**

```bash
git add pkg/quantize/wireformat.go pkg/quantize/wireformat_test.go
git commit -m "feat(quantize): carry authority Epoch per frame entry

Adds Epoch uint32 to FullEntry and DeltaEntry. Encoder writes it
directly after NetID; decoder reads it in the same position. Wire
format gains 4 bytes per entity per frame. Existing zero-value
Go callers round-trip correctly. TypeScript decoder is updated
in the companion commit below.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

- [ ] **Step 10: Update the canonical TypeScript decoder**

Edit `pkg/quantize/ts/delta-decoder-core.ts`. Apply these changes:

1. Add `epoch: number;` to `FullEntryHeader` interface, immediately after `netID: number;`. Include a doc comment explaining that clients typically don't act on this value but the field must be decoded to stay aligned with the wire format.
2. Add the same `epoch: number;` to `DeltaEntryHeader`.
3. In `decodeFullEntry`, after `const netID = view.getUint32(pos); pos += 4;`, add `const epoch = view.getUint32(pos); pos += 4;`. Include `epoch` in the returned entry object.
4. In `decodeDeltaEntry`, do the same: read `epoch` after `netID`, include in the returned entry.
5. Update the wire layout comment on each decoder function to include `epoch(4)` after `netID(4)`.

- [ ] **Step 11: Regenerate vendored SDK copies**

Run:
```bash
just client-sdk examples/4node-basic
just space-sdk
```

These recipes copy `pkg/quantize/ts/delta-decoder-core.ts` into each SDK's `_core/` directory via `cmd/sdkgen/main.go`. Expected output: five `.ts` files updated under each SDK directory.

- [ ] **Step 12: Type-check both web clients**

Run:
```bash
cd examples/4node-basic/web && bunx tsc --noEmit && cd .
cd web-pixi && bunx tsc --noEmit && cd .
```

Expected: `examples/4node-basic/web` is clean. `web-pixi` has three pre-existing errors (`ShipStatusEffectsItem`, `LootCrateItemsItem`, `NPCStatusEffectsItem` not found) unrelated to this task — do NOT attempt to fix them here. Confirm they are the **only** errors and that nothing new appeared from the decoder change.

- [ ] **Step 13: Commit the TypeScript change**

```bash
git add pkg/quantize/ts/delta-decoder-core.ts \
        examples/4node-basic/web/sdk/_core/delta-decoder-core.ts \
        web-pixi/sdk/_core/delta-decoder-core.ts
git commit -m "fix(quantize/ts): mirror Epoch field in TypeScript decoder

Companion fix for the Go-side Epoch wire format change. The
hand-rolled TypeScript decoder reads byte offsets directly, so
it must consume the new 4-byte Epoch field right after NetID to
stay aligned. FullEntryHeader and DeltaEntryHeader gain an
epoch: number field. Clients typically don't act on the value;
the field exists for parser alignment.

Also updates the two vendored copies under examples/4node-basic/web/sdk/
and web-pixi/sdk/ via 'just client-sdk' and 'just space-sdk'.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 1.3: Propagate Epoch through client replication payloads

**Files:**
- Modify: `pkg/system/replication.go` (FullPayload/DeltaPayload)
- Modify: `pkg/system/frame_writer.go`

- [ ] **Step 1: Read the current payload types**

Run: `grep -n "FullPayload\|DeltaPayload" pkg/system/replication.go | head`
Note the lines defining `FullPayload` and `DeltaPayload` (around lines 26-38).

- [ ] **Step 2: Add Epoch to FullPayload**

Locate `FullPayload` in `pkg/system/replication.go` (around line 26). Add an `Epoch uint32` field immediately after `NetID`:

```go
type FullPayload struct {
	NetID       uint32
	Epoch       uint32
	Type        uint8
	Snapshot    []byte
	InitialData []byte
}
```

- [ ] **Step 3: Add Epoch to DeltaPayload**

Same file, `DeltaPayload`:

```go
type DeltaPayload struct {
	NetID uint32
	Epoch uint32
	Type  uint8
	Data  []byte
}
```

- [ ] **Step 4: Populate Epoch at append sites**

Find the two append sites in `replication.go` (around lines 557, 576, 599). Each one constructs a `FullPayload` or `DeltaPayload` literal with `NetID: netID`. Add `Epoch: ent.Epoch` where `ent` is the relevant `component.NetworkID` variable in scope. If the local variable isn't already the full `NetworkID` struct, read it via the existing mapper. Example change:

```go
// Before
s.fullBuf = append(s.fullBuf, FullPayload{
    NetID:    nid.ID,
    Type:     kind,
    Snapshot: snap,
})

// After
s.fullBuf = append(s.fullBuf, FullPayload{
    NetID:    nid.ID,
    Epoch:    nid.Epoch,
    Type:     kind,
    Snapshot: snap,
})
```

Do this for all three buffer-append sites.

- [ ] **Step 5: Thread Epoch into frame writer**

Edit `pkg/system/frame_writer.go` lines 30-50. In the two `for i := range` loops that build `quantize.FullEntry` and `quantize.DeltaEntry`, add `Epoch: fp.Epoch` (and `Epoch: dp.Epoch` respectively) alongside `NetID`.

- [ ] **Step 6: Build**

Run: `go vet ./pkg/system/...`
Expected: clean.

- [ ] **Step 7: Run system tests**

Run: `go test ./pkg/system/...`
Expected: all pass. Payloads with `Epoch=0` are identical to pre-refactor behavior.

- [ ] **Step 8: Commit**

```bash
git add pkg/system/replication.go pkg/system/frame_writer.go
git commit -m "feat(replication): thread Epoch through client FullPayload/DeltaPayload

Epoch flows: component.NetworkID -> FullPayload/DeltaPayload ->
quantize.FullEntry/DeltaEntry -> wire. All still zero until the
Phase 5 handoff state machine lands.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 1.4: Update sdkgen to emit Epoch in schema

**Files:**
- Modify: `cmd/sdkgen/generate.go`

**Update (2026-04-11):** This task is a **no-op** as originally planned. Investigation during execution showed that `cmd/sdkgen/` does NOT reflect on `component.NetworkID` at all — the schema JSON only describes each entity's per-component binary snapshot layout (the bytes inside the delta payload). `NetID` and `Epoch` live in the frame envelope outside the snapshot, and are handled by the hand-rolled TypeScript decoder (`pkg/quantize/ts/delta-decoder-core.ts`) that Task 1.2 already updated.

The generated `delta-decoder.ts` client code only references `entry.netID` and `entry.entityType`, never `entry.epoch`. The generator needs no changes.

- [ ] **Step 1: Confirm sdkgen has no NetworkID references**

Run: `grep -rn "NetworkID\|NetID" cmd/sdkgen/`
Expected: zero matches. If any exist, the plan assumption was wrong and this task needs real work — investigate and report.

- [ ] **Step 2: Confirm regen produces no diff**

Run:
```bash
just client-sdk examples/4node-basic
git status --short
```
Expected: no modified files under `examples/4node-basic/web/sdk/`. If the regen produces a diff, something is reflecting NetworkID indirectly — investigate.

- [ ] **Step 3: Confirm web client type-checks clean**

Run: `cd examples/4node-basic/web && bunx tsc --noEmit`
Expected: clean exit.

- [ ] **Step 4: Done — no commit needed**

Task 1.4 is intentionally a no-op. No new commit is created. The existing Task 1.2 commits already updated the TypeScript decoder to read Epoch correctly.

### Task 1.5: Phase 1 checkpoint

- [ ] **Step 1: Full Go build + test**

Run: `go vet ./... && go test -count=1 ./...`
Expected: all green.

- [ ] **Step 2: Web client type-check (both examples)**

Run:
```bash
cd examples/4node-basic/web && bunx tsc --noEmit && cd .
cd web-pixi && bunx tsc --noEmit && cd .
```

Expected: `examples/4node-basic/web` clean. `web-pixi` has three pre-existing errors (`ShipStatusEffectsItem`, `LootCrateItemsItem`, `NPCStatusEffectsItem` not found) that are unrelated to this phase and predate the refactor — these are acceptable only if their line/column positions match exactly what they were at the start of Phase 1. Any new error is a regression and must be fixed before Phase 1 is declared complete.

- [ ] **Step 3: Boot smoke the examples**

Run `cd examples/4node-basic && just dev` in one terminal, confirm it boots and serves `http://localhost:8080`. **Open the web client in a browser, confirm entities render and move smoothly** — not just "server starts." A broken wire format decode shows up as the client rendering nothing or throwing console errors, not as a boot failure. Kill.

Then `cd examples/slither && just dev`, confirm snakes spawn and move on the web client. Kill.

- [ ] **Step 4: Phase 1 complete — notify**

All Phase 1 commits already made.

---

## Phase 2: Extract `pkg/replication/` Package

**Goal:** Create a new `pkg/replication/` package containing the `Viewer` interface, `ReplicationTier` struct, `BaselineStore` (moved verbatim from `pkg/system/baseline.go`), `Frame`/`FrameEntry` types, and a `Dispatcher` stub. `pkg/system/replication.go` does NOT use these yet — the old types stay in place. This is pure extraction with unit tests.

### Task 2.1: Create the new package and the Viewer interface

**Files:**
- Create: `pkg/replication/viewer.go`
- Create: `pkg/replication/viewer_test.go`

- [ ] **Step 1: Write failing test**

Create `pkg/replication/viewer_test.go`:

```go
package replication

import "testing"

type fakeViewer struct {
	id   uint64
	x, y float32
	sent []Frame
}

func (v *fakeViewer) ID() uint64                { return v.id }
func (v *fakeViewer) Position() (float32, float32) { return v.x, v.y }
func (v *fakeViewer) Tier(entityKind uint16) ReplicationTier {
	return ReplicationTier{Radius: 100}
}
func (v *fakeViewer) Baselines() *BaselineStore { return nil }
func (v *fakeViewer) Send(frame Frame)          { v.sent = append(v.sent, frame) }

func TestViewer_Interface(t *testing.T) {
	var v Viewer = &fakeViewer{id: 1, x: 10, y: 20}
	if v.ID() != 1 {
		t.Fatal("ID mismatch")
	}
	x, y := v.Position()
	if x != 10 || y != 20 {
		t.Fatal("position mismatch")
	}
	v.Send(Frame{})
	if len(v.(*fakeViewer).sent) != 1 {
		t.Fatal("send not recorded")
	}
}
```

- [ ] **Step 2: Run test — expect compile failure**

Run: `go test ./pkg/replication/...`
Expected: `no such package`.

- [ ] **Step 3: Create Viewer, ReplicationTier, BaselineStore stubs**

Create `pkg/replication/viewer.go`:

```go
// Package replication is the shared replication primitives library used by
// both the client-facing dispatcher (pkg/system/replication.go) and the
// inter-node border dispatcher (pkg/universe/border_replication.go).
package replication

// Viewer is anything that receives replicated entity frames: a player
// connection, or a neighbor node. The dispatcher is generic over viewer type.
type Viewer interface {
	// ID returns a stable identifier for this viewer. Player connection
	// IDs and neighbor node IDs both occupy the same uint64 namespace;
	// callers must ensure uniqueness across both.
	ID() uint64

	// Position returns world-space coordinates used for tier distance
	// checks. For a neighbor node, this is the midpoint of the shared
	// cell boundary.
	Position() (x, y float32)

	// Tier returns the per-entity-kind replication tier for this viewer.
	// The dispatcher consults this to decide whether to send an entity
	// and at what rate.
	Tier(entityKind uint16) ReplicationTier

	// Baselines returns the per-entity acknowledged-snapshot store for
	// this viewer. May return nil for viewers that don't use delta
	// compression (rare).
	Baselines() *BaselineStore

	// Send delivers a built frame to the viewer. Production in-process
	// implementations may retain the struct directly; network
	// implementations call frame.Encode() inside their own Send.
	Send(frame Frame)
}

// ReplicationTier configures replication behavior for one entity kind
// as seen by one viewer. Defaults apply when a game does not override.
type ReplicationTier struct {
	// Radius is the AoI cutoff. Entities beyond this distance from the
	// viewer are not replicated.
	Radius float32
	// UpdateDivisor: send every Nth tick. 1 = every tick.
	UpdateDivisor int
	// BaseWeight is the per-entity weight for the priority accumulator.
	BaseWeight float32
	// PromoteRadius: inside this distance from the neighbor's owned
	// cell, an entity is promoted to full-rate updates. Defaults to
	// Radius * 0.5.
	PromoteRadius float32
	// PromoteLookahead: ticks of velocity-projection lookahead. If the
	// entity's projected position at tick+PromoteLookahead lies in the
	// neighbor cell, promote now. Defaults to 10.
	PromoteLookahead int
}

// BaselineStore is a per-viewer per-entity acknowledged-snapshot store.
// Implementation moves here from pkg/system/baseline.go in Task 2.3.
type BaselineStore struct {
	// filled in Task 2.3
}
```

- [ ] **Step 4: Build**

Run: `go build ./pkg/replication/...`
Expected: compiles.

- [ ] **Step 5: Run the test**

Run: `go test ./pkg/replication/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/replication/viewer.go pkg/replication/viewer_test.go
git commit -m "feat(replication): create pkg/replication with Viewer interface

Shared primitives package for the upcoming tiered-push replication.
Viewer interface is the generic abstraction over players and
neighbor nodes.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 2.2: Move BaselineStore from pkg/system to pkg/replication

**Files:**
- Create: `pkg/replication/baseline.go`
- Create: `pkg/replication/baseline_test.go`
- Modify: `pkg/system/baseline.go` (content removed, file deleted at end of task)

- [ ] **Step 1: Read the current baseline.go in full**

Run: `cat pkg/system/baseline.go`

Copy the entire content mentally / to a scratch buffer — it's 141 lines.

- [ ] **Step 2: Create pkg/replication/baseline.go**

Write `pkg/replication/baseline.go` with the exact content of `pkg/system/baseline.go` except:
- Change package declaration from `package system` to `package replication`.
- Types: rename lowercase types that need to be public so the client dispatcher can access them. Specifically: `AckMode` stays public; `sentSnapshot`, `entityBaseline`, `entityPriorityState`, `connectionState` can stay unexported within the package (the old dispatcher is inside the same system package, but the new dispatcher lives in pkg/system/ and needs access). Actually, to keep the refactor simple, make `BaselineStore` the public facade around the internal types. See step 3.

- [ ] **Step 3: Define the BaselineStore facade**

At the top of `pkg/replication/baseline.go`, replace the stub from Task 2.1 with:

```go
// BaselineStore holds per-entity acknowledged snapshots for one viewer.
// Supports both reliable and explicit ack modes; reliable mode treats
// every send as implicitly acked, while explicit mode waits for a
// client ack message to advance the baseline.
type BaselineStore struct {
	mode      AckMode
	baselines map[uint32]*entityBaseline
	// lastHash and priorities used by the dispatcher to avoid
	// re-sending unchanged entities and to accumulate priority
	// during skipped ticks.
	lastHash   map[uint32]uint64
	priorities map[uint32]*entityPriorityState
}

// NewBaselineStore creates a baseline store in the given ack mode.
func NewBaselineStore(mode AckMode) *BaselineStore {
	return &BaselineStore{
		mode:       mode,
		baselines:  make(map[uint32]*entityBaseline),
		lastHash:   make(map[uint32]uint64),
		priorities: make(map[uint32]*entityPriorityState),
	}
}

// Mode returns the ack mode this store was created with.
func (s *BaselineStore) Mode() AckMode { return s.mode }

// Baseline returns the current baseline for a given entity, or nil.
func (s *BaselineStore) Baseline(netID uint32) *entityBaseline {
	return s.baselines[netID]
}

// SetBaseline stores a baseline for a given entity.
func (s *BaselineStore) SetBaseline(netID uint32, b *entityBaseline) {
	s.baselines[netID] = b
}

// DropBaseline removes the baseline for an entity (AoI exit).
func (s *BaselineStore) DropBaseline(netID uint32) {
	delete(s.baselines, netID)
	delete(s.lastHash, netID)
	delete(s.priorities, netID)
}

// LastHash returns the last content hash for an entity, or 0.
func (s *BaselineStore) LastHash(netID uint32) uint64 { return s.lastHash[netID] }

// SetLastHash records the content hash for an entity.
func (s *BaselineStore) SetLastHash(netID uint32, hash uint64) {
	s.lastHash[netID] = hash
}

// Priority returns the accumulator state for an entity, allocating if needed.
func (s *BaselineStore) Priority(netID uint32) *entityPriorityState {
	p := s.priorities[netID]
	if p == nil {
		p = &entityPriorityState{}
		s.priorities[netID] = p
	}
	return p
}
```

Below the facade, keep the unexported `sentSnapshot`, `entityBaseline`, `entityPriorityState`, `AckMode` types exactly as they were in the old file. Also keep the `AckReliable`/`AckExplicit` constants.

- [ ] **Step 4: Write baseline store unit tests**

Create `pkg/replication/baseline_test.go`:

```go
package replication

import "testing"

func TestBaselineStore_InitialState(t *testing.T) {
	s := NewBaselineStore(AckReliable)
	if s.Mode() != AckReliable {
		t.Fatal("mode mismatch")
	}
	if s.Baseline(42) != nil {
		t.Fatal("fresh store should have no baselines")
	}
	if s.LastHash(42) != 0 {
		t.Fatal("fresh store should have zero hash")
	}
}

func TestBaselineStore_SetAndDrop(t *testing.T) {
	s := NewBaselineStore(AckExplicit)
	b := &entityBaseline{}
	s.SetBaseline(7, b)
	if s.Baseline(7) != b {
		t.Fatal("baseline not retrievable")
	}
	s.SetLastHash(7, 12345)
	if s.LastHash(7) != 12345 {
		t.Fatal("hash not retrievable")
	}
	s.DropBaseline(7)
	if s.Baseline(7) != nil || s.LastHash(7) != 0 {
		t.Fatal("drop did not clear state")
	}
}

func TestBaselineStore_PriorityAllocatesOnce(t *testing.T) {
	s := NewBaselineStore(AckReliable)
	p1 := s.Priority(5)
	p2 := s.Priority(5)
	if p1 != p2 {
		t.Fatal("Priority should return same pointer on repeat access")
	}
}
```

- [ ] **Step 5: Build and test the new package**

Run: `go test ./pkg/replication/...`
Expected: PASS.

- [ ] **Step 6: Shim the old package during transition**

`pkg/system/baseline.go` is still referenced by `pkg/system/replication.go`. Rather than deleting it now (Phase 3 does that), replace its content with a shim that re-exports the new types:

```go
package system

import "github.com/mmokit/mmokit/pkg/replication"

// AckMode is an alias during the Phase 2/3 transition.
// pkg/system/replication.go switches to importing from pkg/replication directly in Phase 3.
type AckMode = replication.AckMode

const (
	AckReliable = replication.AckReliable
	AckExplicit = replication.AckExplicit
)
```

If `pkg/system/replication.go` currently references `sentSnapshot`, `entityBaseline`, `entityPriorityState`, or `connectionState`, the Phase 2 build breaks at this step. That is expected — Phase 3 refactors `replication.go` to use the new package. For Phase 2 to stay build-green, we need to make the old types *still accessible* from the system package. Solution: export them temporarily by adding a helper file.

- [ ] **Step 7: Add a temporary bridge file so Phase 2 builds cleanly**

Create `pkg/system/baseline_bridge.go`:

```go
package system

// This file is a transitional bridge. The underlying baseline/priority
// types have moved to pkg/replication. Phase 3 refactors replication.go
// to use pkg/replication directly, at which point this file is deleted.

import "github.com/mmokit/mmokit/pkg/replication"

type connectionState = replication.BaselineStore

func newConnectionState() *connectionState {
	return replication.NewBaselineStore(replication.AckReliable)
}
```

Then adjust `pkg/system/replication.go` to call `newConnectionState()` instead of constructing `connectionState{}` literals. Find the current constructor site(s) and replace. If the current code field-accesses `.baselines` or `.lastHash` directly on `connectionState`, that will not compile — proceed to Task 2.3 which handles the full port.

**Reality check:** the above bridge is fragile because Phase 2 has to stay build-green, but `replication.go` has direct field access. A cleaner route: keep the old `pkg/system/baseline.go` file untouched in Phase 2 and just add `pkg/replication/baseline.go` as a parallel copy; delete the old file in Phase 3. Revise the plan accordingly — proceed to Step 8.

- [ ] **Step 8: Revert the shim and keep a parallel copy**

Delete `pkg/system/baseline_bridge.go` if you created it. Restore `pkg/system/baseline.go` to its original 141-line content (use `git restore pkg/system/baseline.go` to recover from the branch state). The new `pkg/replication/baseline.go` now exists as a parallel copy — this is fine during Phase 2 because nothing imports it yet.

Verify:

```bash
git diff pkg/system/baseline.go
```
Expected: no diff (the old file is untouched).

```bash
ls pkg/replication/baseline.go
```
Expected: file exists.

- [ ] **Step 9: Full build + test**

Run: `go vet ./... && go test ./...`
Expected: green. The duplicate `entityBaseline` type now exists in two packages but there is no name conflict because they're in different packages.

- [ ] **Step 10: Commit**

```bash
git add pkg/replication/baseline.go pkg/replication/baseline_test.go
git commit -m "feat(replication): add BaselineStore facade

Parallel copy of baseline state lives in pkg/replication for use
by the shared dispatcher. The old pkg/system/baseline.go is
unchanged; Phase 3 deletes it after pkg/system/replication.go
migrates.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 2.3: Define Frame type with encode/decode/size

**Files:**
- Create: `pkg/replication/frame.go`
- Create: `pkg/replication/frame_test.go`

- [ ] **Step 1: Write failing test**

Create `pkg/replication/frame_test.go`:

```go
package replication

import (
	"bytes"
	"testing"
)

func TestFrame_RoundTrip(t *testing.T) {
	original := Frame{
		ViewerID:   42,
		SenderNode: 7,
		Tick:       100,
		Entries: []FrameEntry{
			{NetID: NetID{ID: 1, Epoch: 0}, Kind: 2, DeltaBuf: []byte{0xAA, 0xBB}},
			{NetID: NetID{ID: 5, Epoch: 3}, Kind: 4, DeltaBuf: []byte{0xCC}},
		},
	}
	encoded := original.Encode()
	sz := original.SizeEncoded()
	if sz != len(encoded) {
		t.Fatalf("SizeEncoded=%d actual=%d", sz, len(encoded))
	}
	decoded, err := DecodeFrame(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.ViewerID != original.ViewerID || decoded.SenderNode != original.SenderNode {
		t.Fatalf("header mismatch: %+v vs %+v", decoded, original)
	}
	if len(decoded.Entries) != 2 {
		t.Fatalf("got %d entries", len(decoded.Entries))
	}
	if decoded.Entries[0].NetID.ID != 1 || decoded.Entries[0].NetID.Epoch != 0 {
		t.Fatalf("entry 0 netid: %+v", decoded.Entries[0].NetID)
	}
	if !bytes.Equal(decoded.Entries[0].DeltaBuf, []byte{0xAA, 0xBB}) {
		t.Fatalf("entry 0 delta: %x", decoded.Entries[0].DeltaBuf)
	}
	if decoded.Entries[1].NetID.Epoch != 3 {
		t.Fatalf("entry 1 epoch: %d", decoded.Entries[1].NetID.Epoch)
	}
}

func TestFrame_EmptyEntries(t *testing.T) {
	f := Frame{ViewerID: 1, SenderNode: 2, Tick: 3}
	enc := f.Encode()
	if len(enc) == 0 {
		t.Fatal("empty frame should still have header")
	}
	dec, err := DecodeFrame(enc)
	if err != nil {
		t.Fatal(err)
	}
	if dec.ViewerID != 1 {
		t.Fatalf("viewer id lost")
	}
}
```

- [ ] **Step 2: Run test to see it fail**

Run: `go test ./pkg/replication/ -run TestFrame`
Expected: compile error — `Frame`, `FrameEntry`, `NetID` not defined.

- [ ] **Step 3: Create frame.go**

Write `pkg/replication/frame.go`:

```go
package replication

import (
	"encoding/binary"
	"errors"
)

// NetID mirrors component.NetworkID without an import cycle.
// The replication package cannot depend on pkg/component because
// pkg/component is a leaf shared by nearly everything.
type NetID struct {
	ID    uint32
	Epoch uint32
}

// Frame is a batch of replication updates from one sender to one viewer
// for one tick. Used both for client-facing frames (viewer = player conn)
// and for inter-node border frames (viewer = neighbor node).
type Frame struct {
	ViewerID   uint64
	SenderNode uint32
	Tick       uint64
	Entries    []FrameEntry
}

// FrameEntry is one entity's delta in a Frame.
type FrameEntry struct {
	NetID    NetID
	Kind     uint16
	DeltaBuf []byte
}

// Wire format (little-endian):
//   [8] ViewerID
//   [4] SenderNode
//   [8] Tick
//   [4] EntryCount
//   For each entry:
//     [4] NetID.ID
//     [4] NetID.Epoch
//     [2] Kind
//     [4] DeltaBuf length
//     [N] DeltaBuf bytes

const frameHeaderBytes = 24
const frameEntryFixedBytes = 14

// SizeEncoded returns the byte length of Encode() without running it.
func (f *Frame) SizeEncoded() int {
	n := frameHeaderBytes
	for i := range f.Entries {
		n += frameEntryFixedBytes + len(f.Entries[i].DeltaBuf)
	}
	return n
}

// Encode serializes the Frame to a fresh byte slice.
func (f *Frame) Encode() []byte {
	buf := make([]byte, 0, f.SizeEncoded())
	var tmp8 [8]byte
	binary.LittleEndian.PutUint64(tmp8[:], f.ViewerID)
	buf = append(buf, tmp8[:]...)
	var tmp4 [4]byte
	binary.LittleEndian.PutUint32(tmp4[:], f.SenderNode)
	buf = append(buf, tmp4[:]...)
	binary.LittleEndian.PutUint64(tmp8[:], f.Tick)
	buf = append(buf, tmp8[:]...)
	binary.LittleEndian.PutUint32(tmp4[:], uint32(len(f.Entries)))
	buf = append(buf, tmp4[:]...)
	for i := range f.Entries {
		e := &f.Entries[i]
		binary.LittleEndian.PutUint32(tmp4[:], e.NetID.ID)
		buf = append(buf, tmp4[:]...)
		binary.LittleEndian.PutUint32(tmp4[:], e.NetID.Epoch)
		buf = append(buf, tmp4[:]...)
		var tmp2 [2]byte
		binary.LittleEndian.PutUint16(tmp2[:], e.Kind)
		buf = append(buf, tmp2[:]...)
		binary.LittleEndian.PutUint32(tmp4[:], uint32(len(e.DeltaBuf)))
		buf = append(buf, tmp4[:]...)
		buf = append(buf, e.DeltaBuf...)
	}
	return buf
}

// DecodeFrame parses a Frame from wire bytes.
func DecodeFrame(data []byte) (Frame, error) {
	if len(data) < frameHeaderBytes {
		return Frame{}, errors.New("replication: frame too short for header")
	}
	var f Frame
	f.ViewerID = binary.LittleEndian.Uint64(data[0:8])
	f.SenderNode = binary.LittleEndian.Uint32(data[8:12])
	f.Tick = binary.LittleEndian.Uint64(data[12:20])
	count := binary.LittleEndian.Uint32(data[20:24])
	pos := 24
	f.Entries = make([]FrameEntry, 0, count)
	for i := uint32(0); i < count; i++ {
		if pos+frameEntryFixedBytes > len(data) {
			return Frame{}, errors.New("replication: frame truncated mid-entry")
		}
		var e FrameEntry
		e.NetID.ID = binary.LittleEndian.Uint32(data[pos : pos+4])
		e.NetID.Epoch = binary.LittleEndian.Uint32(data[pos+4 : pos+8])
		e.Kind = binary.LittleEndian.Uint16(data[pos+8 : pos+10])
		dlen := binary.LittleEndian.Uint32(data[pos+10 : pos+14])
		pos += frameEntryFixedBytes
		if pos+int(dlen) > len(data) {
			return Frame{}, errors.New("replication: frame truncated in delta payload")
		}
		e.DeltaBuf = make([]byte, dlen)
		copy(e.DeltaBuf, data[pos:pos+int(dlen)])
		pos += int(dlen)
		f.Entries = append(f.Entries, e)
	}
	return f, nil
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./pkg/replication/ -run TestFrame -v`
Expected: PASS.

- [ ] **Step 5: Update Viewer test to use Frame**

Look at `pkg/replication/viewer_test.go` from Task 2.1 — the fakeViewer already accepts `Frame{}`. Should still pass:

Run: `go test ./pkg/replication/ -v`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add pkg/replication/frame.go pkg/replication/frame_test.go
git commit -m "feat(replication): Frame type with encode/decode/size

Self-contained wire format: 24-byte header + (14+N) bytes per entry.
Carries authority epoch per entry. SizeEncoded() computes byte cost
without allocating, for zero-copy in-process metrics.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 2.4: Create tier evaluation helpers

**Files:**
- Create: `pkg/replication/tier.go`
- Create: `pkg/replication/tier_test.go`

- [ ] **Step 1: Write failing test**

Create `pkg/replication/tier_test.go`:

```go
package replication

import "testing"

func TestTier_DefaultPromote(t *testing.T) {
	tier := ReplicationTier{Radius: 1000}
	promote := DefaultPromoteRadius(tier)
	if promote != 500 {
		t.Fatalf("default promote radius: got %v want 500", promote)
	}
}

func TestTier_DefaultLookahead(t *testing.T) {
	tier := ReplicationTier{Radius: 1000}
	la := DefaultPromoteLookahead(tier)
	if la != 10 {
		t.Fatalf("default lookahead: got %v want 10", la)
	}
}

func TestTier_InsideRadius(t *testing.T) {
	tier := ReplicationTier{Radius: 100}
	if !InsideRadius(tier, 0, 0, 50, 0) {
		t.Fatal("point at distance 50 should be inside radius 100")
	}
	if InsideRadius(tier, 0, 0, 150, 0) {
		t.Fatal("point at distance 150 should not be inside radius 100")
	}
}

func TestTier_DivisorSkip(t *testing.T) {
	tier := ReplicationTier{UpdateDivisor: 3}
	if SkipThisTick(tier, 0) {
		t.Fatal("tick 0 should not be skipped with divisor 3")
	}
	if !SkipThisTick(tier, 1) {
		t.Fatal("tick 1 should be skipped with divisor 3")
	}
	if !SkipThisTick(tier, 2) {
		t.Fatal("tick 2 should be skipped with divisor 3")
	}
	if SkipThisTick(tier, 3) {
		t.Fatal("tick 3 should not be skipped")
	}
}

func TestTier_DivisorOneOrZeroNeverSkips(t *testing.T) {
	for _, d := range []int{0, 1} {
		tier := ReplicationTier{UpdateDivisor: d}
		for tick := uint64(0); tick < 10; tick++ {
			if SkipThisTick(tier, tick) {
				t.Fatalf("divisor %d tick %d should never skip", d, tick)
			}
		}
	}
}
```

- [ ] **Step 2: Run test to see it fail**

Run: `go test ./pkg/replication/ -run TestTier`
Expected: compile error — helpers not defined.

- [ ] **Step 3: Create tier.go**

Write `pkg/replication/tier.go`:

```go
package replication

// DefaultPromoteRadius returns the default PromoteRadius for a tier when
// not explicitly set. Per spec: Radius * 0.5.
func DefaultPromoteRadius(t ReplicationTier) float32 {
	if t.PromoteRadius > 0 {
		return t.PromoteRadius
	}
	return t.Radius * 0.5
}

// DefaultPromoteLookahead returns the default PromoteLookahead for a tier
// when not explicitly set. Per spec: 10 ticks.
func DefaultPromoteLookahead(t ReplicationTier) int {
	if t.PromoteLookahead > 0 {
		return t.PromoteLookahead
	}
	return 10
}

// InsideRadius reports whether point (px,py) is within t.Radius of (cx,cy).
// Uses squared distance to avoid a sqrt.
func InsideRadius(t ReplicationTier, cx, cy, px, py float32) bool {
	dx := px - cx
	dy := py - cy
	return dx*dx+dy*dy <= t.Radius*t.Radius
}

// SkipThisTick reports whether the dispatcher should skip sending this
// entity to this viewer on the given tick, based on UpdateDivisor.
// Divisor 0 and 1 both mean "send every tick".
func SkipThisTick(t ReplicationTier, tick uint64) bool {
	d := t.UpdateDivisor
	if d <= 1 {
		return false
	}
	return tick%uint64(d) != 0
}
```

- [ ] **Step 4: Run tier tests**

Run: `go test ./pkg/replication/ -run TestTier -v`
Expected: PASS.

- [ ] **Step 5: Run all replication tests**

Run: `go test ./pkg/replication/ -v`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add pkg/replication/tier.go pkg/replication/tier_test.go
git commit -m "feat(replication): tier evaluation helpers

DefaultPromoteRadius, DefaultPromoteLookahead, InsideRadius,
SkipThisTick. All pure functions, no state.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 2.5: Create priority accumulator

**Files:**
- Create: `pkg/replication/priority.go`
- Create: `pkg/replication/priority_test.go`

- [ ] **Step 1: Write failing test**

Create `pkg/replication/priority_test.go`:

```go
package replication

import "testing"

func TestPriority_AccumulatesOnSkip(t *testing.T) {
	s := NewBaselineStore(AckReliable)
	p := s.Priority(1)
	if p.Accumulator != 0 {
		t.Fatal("fresh priority should be zero")
	}
	AccumulatePriority(p, ReplicationTier{BaseWeight: 1.5}, 1.0 /* distance factor */)
	if p.Accumulator != 1.5 {
		t.Fatalf("one accumulation: got %v want 1.5", p.Accumulator)
	}
	AccumulatePriority(p, ReplicationTier{BaseWeight: 1.5}, 1.0)
	if p.Accumulator != 3.0 {
		t.Fatalf("two accumulations: got %v want 3.0", p.Accumulator)
	}
}

func TestPriority_ResetOnSend(t *testing.T) {
	s := NewBaselineStore(AckReliable)
	p := s.Priority(1)
	AccumulatePriority(p, ReplicationTier{BaseWeight: 2.0}, 1.0)
	AccumulatePriority(p, ReplicationTier{BaseWeight: 2.0}, 1.0)
	ResetPriority(p)
	if p.Accumulator != 0 {
		t.Fatal("reset should zero accumulator")
	}
}
```

- [ ] **Step 2: Expose entityPriorityState publicly or provide helpers**

The test references `p.Accumulator`. That field is currently unexported in `baseline.go`. Two options: export it, or provide `Accumulator()` method. Exporting is simplest.

Edit `pkg/replication/baseline.go`. Locate the `entityPriorityState` struct (it was carried over verbatim from `pkg/system/baseline.go`). Rename fields to exported equivalents:

```go
type entityPriorityState struct {
	Accumulator    float32
	LastSentTick   uint64
	UnchangedTicks int
}
```

(Only export fields that tests / external callers need.)

- [ ] **Step 3: Create priority.go**

Write `pkg/replication/priority.go`:

```go
package replication

// AccumulatePriority adds tier.BaseWeight * distanceFactor to the entity's
// running priority accumulator. Called on skipped ticks so that when the
// next send tick arrives, overdue entities sort to the front of the queue.
func AccumulatePriority(p *entityPriorityState, tier ReplicationTier, distanceFactor float32) {
	p.Accumulator += tier.BaseWeight * distanceFactor
}

// ResetPriority clears the accumulator. Called after the entity is sent.
func ResetPriority(p *entityPriorityState) {
	p.Accumulator = 0
}
```

- [ ] **Step 4: Run priority tests**

Run: `go test ./pkg/replication/ -run TestPriority -v`
Expected: PASS.

- [ ] **Step 5: Run all replication tests**

Run: `go test ./pkg/replication/...`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add pkg/replication/baseline.go pkg/replication/priority.go pkg/replication/priority_test.go
git commit -m "feat(replication): priority accumulator helpers

Exports entityPriorityState.Accumulator and provides two helpers:
AccumulatePriority and ResetPriority.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 2.6: Dispatcher stub

**Files:**
- Create: `pkg/replication/dispatcher.go`
- Create: `pkg/replication/dispatcher_test.go`

- [ ] **Step 1: Write failing test**

Create `pkg/replication/dispatcher_test.go`:

```go
package replication

import "testing"

type fakeCandidate struct {
	netID NetID
	kind  uint16
	x, y  float32
}

func TestDispatcher_WalkSendsInRangeEntities(t *testing.T) {
	viewer := &fakeViewer{id: 1, x: 0, y: 0}
	cands := []fakeCandidate{
		{netID: NetID{ID: 1}, kind: 1, x: 50, y: 0},  // in range
		{netID: NetID{ID: 2}, kind: 1, x: 200, y: 0}, // out of range (tier radius 100)
	}

	d := NewDispatcher()
	frame := d.Walk(viewer, 0, func(yield func(EntityRef) bool) {
		for i := range cands {
			c := cands[i]
			if !yield(EntityRef{
				NetID: c.netID,
				Kind:  c.kind,
				X:     c.x,
				Y:     c.y,
				Build: func() []byte { return []byte{0xDE} },
			}) {
				return
			}
		}
	})

	if len(frame.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(frame.Entries))
	}
	if frame.Entries[0].NetID.ID != 1 {
		t.Fatalf("wrong entity included: %d", frame.Entries[0].NetID.ID)
	}
}
```

- [ ] **Step 2: Run test to see it fail**

Run: `go test ./pkg/replication/ -run TestDispatcher`
Expected: compile error.

- [ ] **Step 3: Create dispatcher.go**

Write `pkg/replication/dispatcher.go`:

```go
package replication

import "iter"

// EntityRef is a lightweight handle for the dispatcher. Callers build
// an EntityRef per candidate entity with enough information for the
// dispatcher to decide visibility, promotion, and frame content without
// depending on the caller's ECS layout.
type EntityRef struct {
	NetID NetID
	Kind  uint16
	// X, Y are the entity's world-space position.
	X, Y float32
	// Build returns the delta-encoded bytes for this entity's current
	// state. Called only if the dispatcher decides to send.
	Build func() []byte
}

// Dispatcher builds frames for one viewer from a stream of candidate
// entities. Stateless across calls; per-viewer state lives in the
// Viewer's BaselineStore.
type Dispatcher struct{}

// NewDispatcher creates a fresh dispatcher.
func NewDispatcher() *Dispatcher { return &Dispatcher{} }

// Walk iterates candidates once, filters by tier, and returns a built Frame.
// The candidates iterator must yield each candidate exactly once; the
// dispatcher does not buffer or re-scan.
func (d *Dispatcher) Walk(
	viewer Viewer,
	tick uint64,
	candidates iter.Seq[EntityRef],
) Frame {
	vx, vy := viewer.Position()
	frame := Frame{
		ViewerID: viewer.ID(),
		Tick:     tick,
	}
	for ref := range candidates {
		tier := viewer.Tier(ref.Kind)
		if !InsideRadius(tier, vx, vy, ref.X, ref.Y) {
			continue
		}
		if SkipThisTick(tier, tick) {
			continue
		}
		delta := ref.Build()
		if len(delta) == 0 {
			continue
		}
		frame.Entries = append(frame.Entries, FrameEntry{
			NetID:    ref.NetID,
			Kind:     ref.Kind,
			DeltaBuf: delta,
		})
	}
	return frame
}
```

- [ ] **Step 4: Run dispatcher tests**

Run: `go test ./pkg/replication/ -run TestDispatcher -v`
Expected: PASS.

- [ ] **Step 5: Run all replication tests**

Run: `go test ./pkg/replication/...`
Expected: all pass.

- [ ] **Step 6: Commit**

```bash
git add pkg/replication/dispatcher.go pkg/replication/dispatcher_test.go
git commit -m "feat(replication): Dispatcher.Walk stub

Generic per-viewer walk: consume candidate iterator, filter by
tier radius and divisor, build frame. Phase 3 wires this into the
client replication system; Phase 4 wires it into the border
dispatcher.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 2.7: Phase 2 checkpoint

- [ ] **Step 1: Full build + test**

Run: `go vet ./... && go test ./...`
Expected: all green. `pkg/replication/` now has ~6 source files plus tests; nothing else imports it yet.

---

## Phase 3: Refactor pkg/system/replication.go as Consumer

**Goal:** Migrate `pkg/system/replication.go` to use `pkg/replication/` primitives for baseline, priority, and dispatcher. Delete `pkg/system/baseline.go` at end. All existing client replication behavior preserved; tests still green.

### Task 3.1: Switch pkg/system/replication.go to import pkg/replication

**Files:**
- Modify: `pkg/system/replication.go`
- Modify: `pkg/system/baseline.go` (removed at end)

- [ ] **Step 1: Add pkg/replication import**

Edit the import block of `pkg/system/replication.go`. Add:

```go
"github.com/mmokit/mmokit/pkg/replication"
```

- [ ] **Step 2: Replace connectionState usages**

Find every reference to `connectionState` in `replication.go`. Each `*connectionState` field and local becomes `*replication.BaselineStore`. For each access to `.baselines[netID]`, use `store.Baseline(netID)` / `store.SetBaseline(netID, b)`. For `.lastHash[netID]`, use `store.LastHash(netID)` / `store.SetLastHash(netID, h)`. For `.priorities[netID]`, use `store.Priority(netID)`.

**Important:** the existing `connectionState` holds a few fields beyond baselines/lastHash/priorities, likely including per-connection state like `lastSeq`, `currentVisible`, etc. Those do NOT move into `BaselineStore`. Keep them in a new local struct:

```go
// connState (unexported, lowercase c) holds per-connection replication
// state that is NOT part of the generic BaselineStore.
type connState struct {
	store          *replication.BaselineStore
	lastSeq        uint32
	currentVisible map[uint32]struct{}
	// ... any other fields currently on connectionState
}
```

Replace `*connectionState` field types with `*connState`. Construct with:

```go
&connState{
	store:          replication.NewBaselineStore(replication.AckReliable),
	currentVisible: make(map[uint32]struct{}),
}
```

- [ ] **Step 3: Replace entityBaseline references**

Every `*entityBaseline` becomes `*replication.entityBaseline` — but `entityBaseline` is not exported from `pkg/replication`. We either export it or let the system package use it via the store's getter/setter methods only.

Prefer the getter/setter approach: code that previously did `conn.baselines[netID] = &entityBaseline{...}` now does `conn.store.SetBaseline(netID, newBaseline)`. But `newBaseline` requires the type. So we must export it.

Return to `pkg/replication/baseline.go` and export:
- `entityBaseline` → `EntityBaseline`
- `sentSnapshot` → `SentSnapshot` (if referenced externally)

Update getters/setters and tests accordingly. Re-run `go test ./pkg/replication/` to verify.

- [ ] **Step 4: Replace entityPriorityState references**

Same pattern: `entityPriorityState` → `EntityPriorityState`. Export from `pkg/replication/baseline.go` and update `priority.go`, test files, and `pkg/system/replication.go`.

- [ ] **Step 5: Delete pkg/system/baseline.go**

Run:

```bash
rm pkg/system/baseline.go
```

- [ ] **Step 6: Build**

Run: `go build ./pkg/system/...`
Expected: compiles. Fix any residual `AckMode` / constant references by importing from `pkg/replication`. If `pkg/system/replication.go` still references `AckReliable` unqualified, add an alias at the top:

```go
const (
	AckReliable = replication.AckReliable
	AckExplicit = replication.AckExplicit
)
```

- [ ] **Step 7: Run system tests**

Run: `go test ./pkg/system/...`
Expected: all pass.

- [ ] **Step 8: Run all tests**

Run: `go vet ./... && go test ./...`
Expected: all green.

- [ ] **Step 9: Commit**

```bash
git add pkg/system/replication.go pkg/replication/baseline.go pkg/replication/baseline_test.go pkg/replication/priority.go pkg/replication/priority_test.go
git rm pkg/system/baseline.go
git commit -m "refactor(replication): pkg/system consumes pkg/replication primitives

Deletes pkg/system/baseline.go. pkg/system/replication.go now
imports BaselineStore, EntityBaseline, EntityPriorityState from
pkg/replication. Per-connection state split: BaselineStore holds
the generic bits, connState holds system-specific state like
lastSeq and currentVisible.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 3.2: Phase 3 checkpoint

- [ ] **Step 1: Full build + test**

Run: `go vet ./... && go test ./... && just build`
Expected: all green, binary produced.

- [ ] **Step 2: Smoke the examples**

Run: `cd examples/4node-basic && just dev` — confirm boot, entities replicate to the web client at `http://localhost:8080`, kill.

---

## Phase 4: Inter-Node Border Dispatcher (Additive)

**Goal:** Build `pkg/universe/border_replication.go` implementing a neighbor-viewer and border dispatcher using `pkg/replication/` primitives. Wire it in as a **second path** running in parallel with the existing proxy path. This phase does not delete anything; it only adds the new code and proves it runs without disrupting the old behavior. The cutover happens in Phase 7.

### Task 4.1: Define NodeViewer adapter

**Files:**
- Create: `pkg/universe/border_viewer.go`
- Create: `pkg/universe/border_viewer_test.go`

- [ ] **Step 1: Write failing test**

Create `pkg/universe/border_viewer_test.go`:

```go
package universe

import (
	"testing"

	"github.com/mmokit/mmokit/pkg/replication"
)

func TestNodeViewer_SatisfiesInterface(t *testing.T) {
	var _ replication.Viewer = (*NodeViewer)(nil)
}

func TestNodeViewer_Position(t *testing.T) {
	v := NewNodeViewer("node_1_0", 123, 50, 75, nil)
	if v.ID() != 123 {
		t.Fatalf("ID: got %d want 123", v.ID())
	}
	x, y := v.Position()
	if x != 50 || y != 75 {
		t.Fatalf("Position: got (%v,%v) want (50,75)", x, y)
	}
}

func TestNodeViewer_DefaultTier(t *testing.T) {
	v := NewNodeViewer("node_1_0", 123, 0, 0, nil)
	tier := v.Tier(0)
	if tier.Radius == 0 {
		t.Fatal("default tier should have non-zero radius")
	}
}
```

- [ ] **Step 2: Create the adapter**

Create `pkg/universe/border_viewer.go`:

```go
package universe

import (
	"hash/fnv"

	"github.com/mmokit/mmokit/pkg/replication"
)

// NodeViewer adapts a neighbor Node as a replication.Viewer. The shared
// dispatcher treats it identically to a player connection viewer.
type NodeViewer struct {
	nodeID    string
	id        uint64
	x, y      float32
	tiers     map[uint16]replication.ReplicationTier
	baselines *replication.BaselineStore
}

// NewNodeViewer constructs a viewer for a neighbor node.
// boundaryX/Y should be the midpoint of the shared cell boundary;
// the dispatcher uses this as the viewer's "position" for distance checks.
func NewNodeViewer(
	nodeID string,
	id uint64,
	boundaryX, boundaryY float32,
	tiers map[uint16]replication.ReplicationTier,
) *NodeViewer {
	return &NodeViewer{
		nodeID:    nodeID,
		id:        id,
		x:         boundaryX,
		y:         boundaryY,
		tiers:     tiers,
		baselines: replication.NewBaselineStore(replication.AckReliable),
	}
}

// ID returns the neighbor node's unique viewer ID.
func (v *NodeViewer) ID() uint64 { return v.id }

// Position returns the boundary midpoint used for tier distance checks.
func (v *NodeViewer) Position() (float32, float32) { return v.x, v.y }

// Tier returns the tier for the given entity kind, or a default tier.
func (v *NodeViewer) Tier(kind uint16) replication.ReplicationTier {
	if v.tiers != nil {
		if t, ok := v.tiers[kind]; ok {
			return t
		}
	}
	return replication.ReplicationTier{
		Radius:        1000,
		UpdateDivisor: 1,
		BaseWeight:    1,
	}
}

// Baselines returns the per-neighbor acknowledged snapshot store.
func (v *NodeViewer) Baselines() *replication.BaselineStore { return v.baselines }

// Send delivers a Frame to the neighbor. In Phase 4 this is a stub
// that records the frame; Phase 7 wires it to MsgBorderFrame.
func (v *NodeViewer) Send(frame replication.Frame) {
	// Phase 4 stub — overwritten by Phase 7 when the NodeBridge carries
	// MsgBorderFrame. Kept as a stub to make the unit tests
	// independent of the bridge wiring.
	_ = frame
}

// NodeViewerID derives a stable uint64 viewer ID from a node ID string.
// Used so that multiple neighbors get distinct, reproducible IDs.
func NodeViewerID(nodeID string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(nodeID))
	// Tag with high bit so node IDs can't collide with player conn IDs.
	return h.Sum64() | (1 << 63)
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./pkg/universe/ -run TestNodeViewer -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/border_viewer.go pkg/universe/border_viewer_test.go
git commit -m "feat(universe): NodeViewer adapter for neighbor-as-viewer

Wraps a neighbor node as a replication.Viewer. Send() is a stub
until Phase 7 wires MsgBorderFrame.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 4.2: Implement BorderDispatcher

**Files:**
- Create: `pkg/universe/border_replication.go`
- Create: `pkg/universe/border_replication_stub_test.go`

- [ ] **Step 1: Write failing test**

Create `pkg/universe/border_replication_stub_test.go`:

```go
package universe

import (
	"testing"
)

func TestBorderDispatcher_TickNoCandidatesNoPanic(t *testing.T) {
	d := NewBorderDispatcher(nil, nil)
	d.Tick(0)
}
```

- [ ] **Step 2: Implement the dispatcher**

Create `pkg/universe/border_replication.go`:

```go
package universe

import (
	"iter"

	"github.com/mmokit/mmokit/pkg/replication"
)

// BorderDispatcher walks local entities near shared cell boundaries
// and builds a Frame per neighbor via the shared replication.Dispatcher.
// Phase 4: standalone and inert (never called by node_bridge yet).
// Phase 7: hooked into node_bridge_impl.PostSystems.
type BorderDispatcher struct {
	base      *WorldBase
	neighbors map[string]*NodeViewer
	disp      *replication.Dispatcher
}

// NewBorderDispatcher creates a dispatcher bound to a WorldBase and a
// set of neighbor viewers. Either argument may be nil for unit tests.
func NewBorderDispatcher(base *WorldBase, neighbors map[string]*NodeViewer) *BorderDispatcher {
	return &BorderDispatcher{
		base:      base,
		neighbors: neighbors,
		disp:      replication.NewDispatcher(),
	}
}

// Tick runs one pass: for each neighbor, build and Send a Frame.
func (bd *BorderDispatcher) Tick(currentTick uint64) {
	if bd.base == nil || len(bd.neighbors) == 0 {
		return
	}
	for _, nv := range bd.neighbors {
		cands := bd.candidatesFor(nv)
		frame := bd.disp.Walk(nv, currentTick, cands)
		nv.Send(frame)
	}
}

// candidatesFor returns an iterator over entities eligible for
// replication to the given neighbor. Phase 4 returns an empty iterator;
// Phase 7 wires it to the spatial grid + border query.
func (bd *BorderDispatcher) candidatesFor(nv *NodeViewer) iter.Seq[replication.EntityRef] {
	return func(yield func(replication.EntityRef) bool) {
		// Phase 4 stub: no entities yielded.
	}
}
```

- [ ] **Step 3: Run the stub test**

Run: `go test ./pkg/universe/ -run TestBorderDispatcher_Tick -v`
Expected: PASS.

- [ ] **Step 4: Full build**

Run: `go vet ./... && go test ./...`
Expected: green.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/border_replication.go pkg/universe/border_replication_stub_test.go
git commit -m "feat(universe): BorderDispatcher stub

Phase 4 shell of the inter-node dispatcher. Tick() runs but yields
no candidates. Phase 7 replaces candidatesFor with a real spatial
query and wires into PostSystems.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 4.3: Phase 4 checkpoint

- [ ] **Step 1: Build + test**

Run: `go vet ./... && go test ./... && just build`
Expected: all green. New code sits idle alongside the existing proxy path.

---

## Phase 5: Handoff State Machine + Baseline Handover

**Goal:** Build `pkg/universe/handoff.go` implementing the `Border → Promoted → Handoff → committed` state machine, `MsgHandoffPrepare`/`MsgHandoffCommit` payloads, baseline handover logic, and `CrossingCooldownTicks` hysteresis. Still additive — old ghost-based path runs in parallel. Cutover in Phase 7.

### Task 5.1: Define handoff message types

**Files:**
- Modify: `pkg/universe/message.go`
- Test: `pkg/universe/message_test.go`

- [ ] **Step 1: Add new MsgType constants**

Edit `pkg/universe/message.go`. Near the existing `MsgType` constants, add (do NOT remove any existing constants yet — Phase 7 deletes them):

```go
const (
	MsgBorderFrame   MsgType = 100
	MsgHandoffPrepare MsgType = 101
	MsgHandoffCommit  MsgType = 102
	MsgForwardInput   MsgType = 103
)
```

Use high values (100+) so they cannot collide with existing constants during the transition.

- [ ] **Step 2: Add payload structs**

In the same file, append:

```go
// HandoffPreparePayload is sent when a Border -> Promoted transition
// occurs. The receiver seeds a shadow entity + per-client baselines.
type HandoffPreparePayload struct {
	NetID           uint32
	Epoch           uint32
	Kind            uint16
	TransferBlob    []byte
	ClientBaselines []ClientBaselineEntry
	ExpectedTick    uint64
	OldEpoch        uint32
}

// ClientBaselineEntry carries one per-client delta baseline during
// baseline handover.
type ClientBaselineEntry struct {
	ConnID      uint32
	EntityNetID uint32
	LastAcked   []byte
	LastTick    uint64
}

// HandoffCommitPayload is sent when authority commits.
type HandoffCommitPayload struct {
	NetID      uint32
	Epoch      uint32
	CommitTick uint64
}

// ForwardInputPayload is a safety-path input forwarded from the
// old owner to the new owner during the single-tick overlap window.
type ForwardInputPayload struct {
	ConnID    uint32
	InputBlob []byte
}
```

- [ ] **Step 3: Extend NodeMessage envelope**

Locate the `NodeMessage` struct in `message.go`. Add fields:

```go
BorderFrame    []byte                // encoded replication.Frame bytes
HandoffPrepare *HandoffPreparePayload
HandoffCommit  *HandoffCommitPayload
ForwardInput   *ForwardInputPayload
```

Keep all existing fields. Phase 7 removes the obsolete ones.

- [ ] **Step 4: Write a constant-parity test**

Create `pkg/universe/message_test.go` (if it doesn't exist) or append:

```go
func TestMsgTypes_NewConstantsDistinct(t *testing.T) {
	seen := map[MsgType]bool{}
	for _, mt := range []MsgType{
		MsgBorderFrame, MsgHandoffPrepare, MsgHandoffCommit, MsgForwardInput,
	} {
		if seen[mt] {
			t.Fatalf("duplicate MsgType constant: %d", mt)
		}
		seen[mt] = true
	}
}
```

- [ ] **Step 5: Run the test**

Run: `go test ./pkg/universe/ -run TestMsgTypes_NewConstantsDistinct -v`
Expected: PASS.

- [ ] **Step 6: Build and commit**

Run: `go vet ./... && go test ./pkg/universe/...`
Expected: green.

```bash
git add pkg/universe/message.go pkg/universe/message_test.go
git commit -m "feat(universe): add MsgBorderFrame, MsgHandoff* payloads

Additive — existing message constants preserved during transition.
Phase 7 removes MsgReplica/MsgProxySummary/MsgDetail*.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 5.2: Implement the handoff state machine

**Files:**
- Create: `pkg/universe/handoff.go`
- Create: `pkg/universe/handoff_test.go`

- [ ] **Step 1: Write failing test**

Create `pkg/universe/handoff_test.go`:

```go
package universe

import "testing"

func TestHandoffState_BorderPromotedTransition(t *testing.T) {
	sm := NewHandoffStateMachine()
	key := HandoffKey{EntityNetID: 1, NeighborID: "node_1_0"}

	// Initial state: not tracked.
	if sm.State(key) != HandoffUnseen {
		t.Fatalf("fresh key should be Unseen")
	}

	sm.SetState(key, HandoffBorder)
	if sm.State(key) != HandoffBorder {
		t.Fatal("state did not persist")
	}

	sm.SetState(key, HandoffPromoted)
	if sm.State(key) != HandoffPromoted {
		t.Fatal("promote did not persist")
	}
	if sm.WarmupCount(key) != 0 {
		t.Fatal("warmup should start at 0 on promote")
	}
}

func TestHandoffState_WarmupTicks(t *testing.T) {
	sm := NewHandoffStateMachine()
	key := HandoffKey{EntityNetID: 1, NeighborID: "node_1_0"}
	sm.SetState(key, HandoffPromoted)

	for i := 0; i < 5; i++ {
		sm.TickWarmup(key)
	}
	if sm.WarmupCount(key) != 5 {
		t.Fatalf("warmup count: got %d want 5", sm.WarmupCount(key))
	}
}

func TestHandoffState_CommitBlockedBelowWarmupFloor(t *testing.T) {
	sm := NewHandoffStateMachine()
	key := HandoffKey{EntityNetID: 1, NeighborID: "node_1_0"}
	sm.SetState(key, HandoffPromoted)

	// Only 2 warmup ticks — below MinWarmupTicks (5).
	sm.TickWarmup(key)
	sm.TickWarmup(key)

	if sm.CanCommit(key) {
		t.Fatal("should not be committable below warmup floor")
	}
}

func TestHandoffState_CommitAllowedAtFloor(t *testing.T) {
	sm := NewHandoffStateMachine()
	key := HandoffKey{EntityNetID: 1, NeighborID: "node_1_0"}
	sm.SetState(key, HandoffPromoted)

	for i := 0; i < MinWarmupTicks; i++ {
		sm.TickWarmup(key)
	}
	if !sm.CanCommit(key) {
		t.Fatal("should be committable at warmup floor")
	}
}

func TestHandoffState_CooldownBlocksReHandoff(t *testing.T) {
	sm := NewHandoffStateMachine()
	key := HandoffKey{EntityNetID: 1, NeighborID: "node_1_0"}
	sm.EnterCooldown(key, 100)

	if sm.InCooldown(key, 100) {
		t.Fatal("cooldown should not report active on the entry tick")
	}
	if !sm.InCooldown(key, 110) {
		t.Fatal("cooldown should be active 10 ticks after entry")
	}
	if sm.InCooldown(key, 100+CrossingCooldownTicks+1) {
		t.Fatal("cooldown should expire after CrossingCooldownTicks")
	}
}
```

- [ ] **Step 2: Run test to see failure**

Run: `go test ./pkg/universe/ -run TestHandoffState`
Expected: compile errors — types not defined.

- [ ] **Step 3: Create handoff.go**

Write `pkg/universe/handoff.go`:

```go
package universe

// Handoff timing constants — fixed, not per-tier. See spec §Tunables.
const (
	MinWarmupTicks        = 5
	CrossingCooldownTicks = 20
)

// HandoffPhase is one of four states per (entity, neighbor) pair.
type HandoffPhase uint8

const (
	HandoffUnseen HandoffPhase = iota
	HandoffBorder
	HandoffPromoted
	HandoffHandoff
)

// HandoffKey identifies one (entity, neighbor) pair.
type HandoffKey struct {
	EntityNetID uint32
	NeighborID  string
}

// handoffEntry is per-pair state.
type handoffEntry struct {
	phase         HandoffPhase
	warmupCount   int
	cooldownStart uint64 // tick when cooldown was entered; 0 = not in cooldown
}

// HandoffStateMachine tracks per-pair phase and warmup/cooldown state.
type HandoffStateMachine struct {
	entries map[HandoffKey]*handoffEntry
}

// NewHandoffStateMachine creates an empty state machine.
func NewHandoffStateMachine() *HandoffStateMachine {
	return &HandoffStateMachine{entries: make(map[HandoffKey]*handoffEntry)}
}

// State returns the current phase for a key, or HandoffUnseen.
func (sm *HandoffStateMachine) State(k HandoffKey) HandoffPhase {
	if e := sm.entries[k]; e != nil {
		return e.phase
	}
	return HandoffUnseen
}

// SetState transitions the key to a new phase. Resets warmup on
// Border → Promoted.
func (sm *HandoffStateMachine) SetState(k HandoffKey, phase HandoffPhase) {
	e := sm.entries[k]
	if e == nil {
		e = &handoffEntry{}
		sm.entries[k] = e
	}
	if phase == HandoffPromoted && e.phase != HandoffPromoted {
		e.warmupCount = 0
	}
	e.phase = phase
}

// WarmupCount returns the current warmup tick count.
func (sm *HandoffStateMachine) WarmupCount(k HandoffKey) int {
	if e := sm.entries[k]; e != nil {
		return e.warmupCount
	}
	return 0
}

// TickWarmup increments warmup for a promoted entry.
func (sm *HandoffStateMachine) TickWarmup(k HandoffKey) {
	if e := sm.entries[k]; e != nil && e.phase == HandoffPromoted {
		e.warmupCount++
	}
}

// CanCommit reports whether the key has accumulated enough warmup
// to be safely committed.
func (sm *HandoffStateMachine) CanCommit(k HandoffKey) bool {
	e := sm.entries[k]
	return e != nil && e.phase == HandoffPromoted && e.warmupCount >= MinWarmupTicks
}

// EnterCooldown marks the key as in commit cooldown as of the given tick.
func (sm *HandoffStateMachine) EnterCooldown(k HandoffKey, tick uint64) {
	e := sm.entries[k]
	if e == nil {
		e = &handoffEntry{}
		sm.entries[k] = e
	}
	e.cooldownStart = tick
}

// InCooldown reports whether the key is within CrossingCooldownTicks of
// the most recent EnterCooldown.
func (sm *HandoffStateMachine) InCooldown(k HandoffKey, currentTick uint64) bool {
	e := sm.entries[k]
	if e == nil || e.cooldownStart == 0 {
		return false
	}
	return currentTick > e.cooldownStart && currentTick-e.cooldownStart <= CrossingCooldownTicks
}

// Forget removes state for an entry that has drifted back to Unseen.
func (sm *HandoffStateMachine) Forget(k HandoffKey) {
	delete(sm.entries, k)
}
```

- [ ] **Step 4: Run the tests**

Run: `go test ./pkg/universe/ -run TestHandoffState -v`
Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/handoff.go pkg/universe/handoff_test.go
git commit -m "feat(universe): handoff state machine with warmup + cooldown

Pure data structure — no side effects, no bridge calls. Phase 7
wires it into the border dispatcher's per-tick promotion/commit
decisions.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 5.3: Baseline handover serialization helpers

**Files:**
- Create: `pkg/universe/baseline_handover.go`
- Create: `pkg/universe/baseline_handover_test.go`

- [ ] **Step 1: Write failing test**

Create `pkg/universe/baseline_handover_test.go`:

```go
package universe

import (
	"bytes"
	"testing"
)

func TestCollectBaselinesForEntity_SingleClient(t *testing.T) {
	// Case A: non-player entity. Collect per-client baselines keyed
	// by (connID, entityNetID) where entityNetID matches the one
	// being handed off.
	stores := map[uint32]fakeClientStore{
		10: {entityNetID: 7, ack: []byte{0xAA}, tick: 100},
		11: {entityNetID: 7, ack: []byte{0xBB}, tick: 101},
		12: {entityNetID: 99, ack: []byte{0xCC}, tick: 102}, // different entity — skip
	}
	entries := CollectBaselinesForEntity(stores, 7)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if !bytes.Equal(findBaseline(entries, 10, 7).LastAcked, []byte{0xAA}) {
		t.Fatal("connID 10 baseline mismatch")
	}
	if !bytes.Equal(findBaseline(entries, 11, 7).LastAcked, []byte{0xBB}) {
		t.Fatal("connID 11 baseline mismatch")
	}
}

func findBaseline(entries []ClientBaselineEntry, conn, ent uint32) *ClientBaselineEntry {
	for i := range entries {
		e := &entries[i]
		if e.ConnID == conn && e.EntityNetID == ent {
			return e
		}
	}
	return nil
}

type fakeClientStore struct {
	entityNetID uint32
	ack         []byte
	tick        uint64
}
```

- [ ] **Step 2: Create the helper**

Create `pkg/universe/baseline_handover.go`:

```go
package universe

// CollectBaselinesForEntity builds a slice of ClientBaselineEntry for a
// single entity across all subscribed clients. Used for Case A handoff
// (non-player entity crossing).
//
// The connStores argument is a per-connection view abstracted behind
// a map interface so tests can supply fakes. Real callers pass a view
// of the client dispatcher's BaselineStores.
func CollectBaselinesForEntity(
	connStores map[uint32]fakeClientStore, // Phase 5 stub — Phase 7 will swap for the real store
	entityNetID uint32,
) []ClientBaselineEntry {
	var out []ClientBaselineEntry
	for connID, store := range connStores {
		if store.entityNetID != entityNetID {
			continue
		}
		out = append(out, ClientBaselineEntry{
			ConnID:      connID,
			EntityNetID: entityNetID,
			LastAcked:   store.ack,
			LastTick:    store.tick,
		})
	}
	return out
}

// CollectBaselinesForPlayer builds the full per-entity baseline set
// for one player connection. Used for Case B handoff (player crossing).
//
// Phase 5 signature uses a fake store; Phase 7 swaps in the real
// *replication.BaselineStore argument.
func CollectBaselinesForPlayer(
	connID uint32,
	stores map[uint32][]byte, // entityNetID -> acked snapshot bytes
	ticks map[uint32]uint64,
) []ClientBaselineEntry {
	out := make([]ClientBaselineEntry, 0, len(stores))
	for entID, ack := range stores {
		out = append(out, ClientBaselineEntry{
			ConnID:      connID,
			EntityNetID: entID,
			LastAcked:   ack,
			LastTick:    ticks[entID],
		})
	}
	return out
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./pkg/universe/ -run TestCollectBaselinesForEntity -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/baseline_handover.go pkg/universe/baseline_handover_test.go
git commit -m "feat(universe): baseline handover collection helpers

Two signatures for the two cases: Case A collects across
connections for one entity; Case B collects across entities for
one connection. Phase 7 swaps the fake store args for real
*replication.BaselineStore references.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 5.4: Phase 5 checkpoint

- [ ] **Step 1: Full build + test**

Run: `go vet ./... && go test ./... && just build`
Expected: all green. The new handoff state machine, message types, and baseline helpers all exist but are not yet called by anything.

---

## Phase 6: Loopback Test Harness + Correctness Test Skeletons

**Goal:** Build the loopback `NodeBridge` implementation and the skeleton of all 8 correctness tests. Tests can be written as Phase 6 deliverables even though the full integration is Phase 7 — they will initially use the old path to establish a behavioral contract, then be updated in Phase 7 to use the new path.

### Task 6.1: Implement LoopbackBridge

**Files:**
- Create: `pkg/universe/loopback_bridge.go`
- Create: `pkg/universe/loopback_bridge_test.go`

- [ ] **Step 1: Write failing test**

Create `pkg/universe/loopback_bridge_test.go`:

```go
package universe

import (
	"testing"
	"time"
)

func TestLoopbackBridge_DeliversBorderFrame(t *testing.T) {
	lb := NewLoopbackBridge(LoopbackOpts{})

	var received []byte
	lb.SetReceiver("node_B", func(msg NodeMessage) {
		received = msg.BorderFrame
	})

	payload := []byte{0x01, 0x02, 0x03}
	lb.Send("node_A", "node_B", NodeMessage{
		Type:        MsgBorderFrame,
		FromNodeID:  "node_A",
		BorderFrame: payload,
	})

	// Allow the delivery goroutine to run with zero latency.
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) && received == nil {
		time.Sleep(time.Millisecond)
	}
	if string(received) != string(payload) {
		t.Fatalf("payload mismatch: got %x want %x", received, payload)
	}
}

func TestLoopbackBridge_Latency(t *testing.T) {
	lb := NewLoopbackBridge(LoopbackOpts{LatencyMs: 10})

	received := make(chan time.Time, 1)
	lb.SetReceiver("node_B", func(msg NodeMessage) {
		received <- time.Now()
	})

	start := time.Now()
	lb.Send("node_A", "node_B", NodeMessage{Type: MsgBorderFrame})

	select {
	case got := <-received:
		elapsed := got.Sub(start)
		if elapsed < 8*time.Millisecond {
			t.Fatalf("latency too short: %v", elapsed)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("message not delivered")
	}
}

func TestLoopbackBridge_LossRate(t *testing.T) {
	lb := NewLoopbackBridge(LoopbackOpts{LossRate: 1.0}) // 100% loss
	received := 0
	lb.SetReceiver("node_B", func(msg NodeMessage) { received++ })

	for i := 0; i < 10; i++ {
		lb.Send("node_A", "node_B", NodeMessage{Type: MsgBorderFrame})
	}
	time.Sleep(20 * time.Millisecond)
	if received != 0 {
		t.Fatalf("expected 0 delivered at 100%% loss, got %d", received)
	}
}
```

- [ ] **Step 2: Create the loopback bridge**

Create `pkg/universe/loopback_bridge.go`:

```go
package universe

import (
	"math/rand"
	"sync"
	"time"
)

// LoopbackOpts configures a LoopbackBridge.
type LoopbackOpts struct {
	LatencyMs int     // delivery delay
	LossRate  float32 // 0.0 to 1.0; 1.0 = drop everything
	Seed      int64   // PRNG seed; 0 = time-based
}

// LoopbackBridge is a test-only inter-node message router with optional
// latency and loss injection. Used by Phase 6+ integration tests to
// exercise the wire path without a real network transport.
type LoopbackBridge struct {
	opts      LoopbackOpts
	rand      *rand.Rand
	mu        sync.Mutex
	receivers map[string]func(NodeMessage)
}

// NewLoopbackBridge creates a new loopback bridge.
func NewLoopbackBridge(opts LoopbackOpts) *LoopbackBridge {
	seed := opts.Seed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	return &LoopbackBridge{
		opts:      opts,
		rand:      rand.New(rand.NewSource(seed)),
		receivers: make(map[string]func(NodeMessage)),
	}
}

// SetReceiver registers a callback for messages delivered to nodeID.
func (lb *LoopbackBridge) SetReceiver(nodeID string, fn func(NodeMessage)) {
	lb.mu.Lock()
	lb.receivers[nodeID] = fn
	lb.mu.Unlock()
}

// Send routes a message from source to dest, applying latency and loss.
// Messages are copied-by-value (NodeMessage is a struct) before delivery
// so senders and receivers never share mutable state.
func (lb *LoopbackBridge) Send(sourceNode, destNode string, msg NodeMessage) {
	lb.mu.Lock()
	if lb.opts.LossRate > 0 && lb.rand.Float32() < lb.opts.LossRate {
		lb.mu.Unlock()
		return
	}
	recv := lb.receivers[destNode]
	delay := time.Duration(lb.opts.LatencyMs) * time.Millisecond
	lb.mu.Unlock()

	if recv == nil {
		return
	}
	// Copy byte slices so senders can reuse their buffers.
	msgCopy := msg
	if msg.BorderFrame != nil {
		msgCopy.BorderFrame = append([]byte(nil), msg.BorderFrame...)
	}
	if delay == 0 {
		// Synchronous delivery path — good for deterministic tests.
		recv(msgCopy)
		return
	}
	go func() {
		time.Sleep(delay)
		recv(msgCopy)
	}()
}
```

- [ ] **Step 3: Run tests**

Run: `go test ./pkg/universe/ -run TestLoopbackBridge -v`
Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/loopback_bridge.go pkg/universe/loopback_bridge_test.go
git commit -m "feat(universe): loopback bridge with latency and loss injection

Thread-safe in-memory router used by Phase 6+ integration tests.
Copies byte slices on delivery so senders and receivers don't
share mutable state.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 6.2: Phase 6 checkpoint

- [ ] **Step 1: Build + test**

Run: `go vet ./... && go test ./... && just build`
Expected: green. The correctness test skeletons are deferred to Phase 7, where the real border dispatcher is wired. Phase 6 only ships the harness so Phase 7 can build tests on top of it.

---

## Phase 7: Cutover — Delete Old Path, Wire New Path

**Goal:** Replace the old border replication and ghost-based handoff with the new path. Deletes: `replication_scan.go`, `ReplicaFrame`, `ProxySummary`, `MsgReplica`, `MsgProxySummary`, `MsgDetailRequest`, `MsgDetailResponse`, `RequestDetail`, `SendDetailResponse`, `ProxiesEnabled`, ghost-authority paths. Wires: `BorderDispatcher` into `node_bridge_impl.PostSystems`, `MsgBorderFrame`/`MsgHandoffPrepare`/`MsgHandoffCommit` into `node.processMessage`. This is the largest phase; split into many small tasks.

### Task 7.1: Wire candidates into BorderDispatcher

**Files:**
- Modify: `pkg/universe/border_replication.go`

- [ ] **Step 1: Replace candidatesFor with a real spatial query**

Read `pkg/universe/replication_scan.go` lines 138-239 to see how `ScanBorderProxies` currently iterates border entities. The new implementation uses the same spatial grid but yields `replication.EntityRef` instead of building `ProxySummary` bytes.

Edit `pkg/universe/border_replication.go`. Replace `candidatesFor` with:

```go
func (bd *BorderDispatcher) candidatesFor(nv *NodeViewer) iter.Seq[replication.EntityRef] {
	return func(yield func(replication.EntityRef) bool) {
		if bd.base == nil {
			return
		}
		// Query the spatial grid for entities within the viewer's
		// max tier radius of the boundary midpoint.
		vx, vy := nv.Position()
		// Use the maximum tier radius across all registered entity kinds
		// as the scan radius. Any entity outside this cannot possibly
		// be tier-visible to the neighbor.
		scanRadius := bd.maxTierRadius()
		if scanRadius == 0 {
			scanRadius = 1000 // safety default
		}
		ents := bd.base.SpatialGrid().QueryRadius(vx, vy, scanRadius)
		for _, e := range ents {
			// Skip entities that are themselves replicas — we only
			// replicate entities we own.
			if bd.base.IsReplica(e.Entity) {
				continue
			}
			ref := bd.refFromSpatialEntry(e)
			if ref.Build == nil {
				continue
			}
			if !yield(ref) {
				return
			}
		}
	}
}

// refFromSpatialEntry converts a spatial.Entry to an EntityRef.
// Build() closure captures the entity handle and runs the component
// serializer only if the dispatcher decides to send.
func (bd *BorderDispatcher) refFromSpatialEntry(e SpatialEntry) replication.EntityRef {
	// Phase 7.1 stub — concrete implementation relies on the
	// ReplicationRegistry from world_base.go. Expanded in Task 7.2.
	return replication.EntityRef{}
}

// maxTierRadius returns the largest Radius across all neighbor tier
// configurations. Used as the spatial scan radius so that every
// potentially-visible entity is considered exactly once per tick.
func (bd *BorderDispatcher) maxTierRadius() float32 {
	var max float32
	for _, nv := range bd.neighbors {
		// Walk all kinds this neighbor cares about. For the Phase 7
		// default (single default tier per viewer), this simplifies
		// to reading one tier. Games that register per-kind tiers
		// can override by extending NodeViewer.
		t := nv.Tier(0)
		if t.Radius > max {
			max = t.Radius
		}
	}
	return max
}
```

**Note:** `SpatialGrid()`, `IsReplica()`, and `SpatialEntry` are referenced here — confirm these APIs exist on `WorldBase` and `pkg/spatial/`. If not, either add them in a preceding small task or use the existing access path. Run `grep -n "func (w \*WorldBase) Spatial\|QueryRadius\|IsReplica" pkg/universe/world_base.go pkg/spatial/*.go` to find the actual method names.

- [ ] **Step 2: Confirm build**

Run: `go build ./pkg/universe/...`
Expected: may fail on missing methods. If so, look up the actual spatial query API and adjust the names.

- [ ] **Step 3: Commit (WIP OK)**

```bash
git add pkg/universe/border_replication.go
git commit -m "wip(universe): begin wiring BorderDispatcher candidates

Spatial-grid query hooked in; refFromSpatialEntry still a stub
until Task 7.2 completes the component build path.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 7.2: Component serialization via ReplicationRegistry

**Files:**
- Modify: `pkg/universe/border_replication.go`

- [ ] **Step 1: Thread the registry**

The existing `ReplicationRegistry` in `pkg/universe/replication.go` maps component IDs to serializers. Pass it into `BorderDispatcher`:

```go
type BorderDispatcher struct {
	base      *WorldBase
	neighbors map[string]*NodeViewer
	disp      *replication.Dispatcher
	registry  *ReplicationRegistry
}

func NewBorderDispatcher(
	base *WorldBase,
	neighbors map[string]*NodeViewer,
	registry *ReplicationRegistry,
) *BorderDispatcher {
	return &BorderDispatcher{
		base:      base,
		neighbors: neighbors,
		disp:      replication.NewDispatcher(),
		registry:  registry,
	}
}
```

- [ ] **Step 2: Build concrete EntityRef**

Replace `refFromSpatialEntry` with real implementation:

```go
func (bd *BorderDispatcher) refFromSpatialEntry(e SpatialEntry) replication.EntityRef {
	netID, kind, x, y, ok := bd.base.EntityNetworkInfo(e.Entity)
	if !ok {
		return replication.EntityRef{}
	}
	return replication.EntityRef{
		NetID: replication.NetID{ID: netID.ID, Epoch: netID.Epoch},
		Kind:  uint16(kind),
		X:     x,
		Y:     y,
		Build: func() []byte {
			// Build the delta bytes by walking the registry and
			// concatenating per-component scans. Reuses the
			// existing ComponentReplicator.Scan method.
			return bd.base.BuildReplicationDelta(e.Entity, bd.registry)
		},
	}
}
```

The two new `WorldBase` methods `EntityNetworkInfo` and `BuildReplicationDelta` need to be added.

- [ ] **Step 3: Add EntityNetworkInfo and BuildReplicationDelta to WorldBase**

Edit `pkg/universe/world_base.go`. Add:

```go
// EntityNetworkInfo returns the NetworkID, entity kind, and position
// for an entity, or (zero, zero, zero, zero, false) if the entity is
// missing components or has been removed.
func (w *WorldBase) EntityNetworkInfo(e ecs.Entity) (component.NetworkID, uint16, float32, float32, bool) {
	if !w.world.Alive(e) {
		return component.NetworkID{}, 0, 0, 0, false
	}
	if !w.world.HasAll(e, /* netid, kind, pos mask */) {
		return component.NetworkID{}, 0, 0, 0, false
	}
	nid := w.netIDMap.Get(e)
	kind := w.kindMap.Get(e)
	pos := w.posMap.Get(e)
	return *nid, uint16(kind.Type), pos.X, pos.Y, true
}

// BuildReplicationDelta walks the registry for an entity and returns
// a concatenated byte buffer containing each present component's
// scan output. Used by BorderDispatcher to feed EntityRef.Build.
func (w *WorldBase) BuildReplicationDelta(e ecs.Entity, reg *ReplicationRegistry) []byte {
	if reg == nil || !w.world.Alive(e) {
		return nil
	}
	var buf []byte
	for _, rep := range reg.All() {
		data := rep.Scan(w.world, e)
		if len(data) == 0 {
			continue
		}
		// Length-prefixed: [2] compID + [2] length + [N] data.
		var header [4]byte
		binary.LittleEndian.PutUint16(header[0:2], uint16(rep.ID))
		binary.LittleEndian.PutUint16(header[2:4], uint16(len(data)))
		buf = append(buf, header[:]...)
		buf = append(buf, data...)
	}
	return buf
}
```

Mapper field names (`netIDMap`, `kindMap`, `posMap`) must match what's actually in `WorldBase`. Cross-reference with the existing struct definition around line 110-162 of `world_base.go` and adjust.

- [ ] **Step 4: Build**

Run: `go build ./pkg/universe/...`
Expected: may need iteration to resolve method/field names.

- [ ] **Step 5: Unit test the new WorldBase helpers**

Add to `pkg/universe/world_base_test.go`:

```go
func TestEntityNetworkInfo_MissingEntity(t *testing.T) {
	// Create a minimal WorldBase, don't spawn anything.
	// Call EntityNetworkInfo on ecs.Entity{} — should return ok=false.
	// (Exact setup depends on WorldBase test helpers.)
}
```

Only add this if existing test infrastructure supports it; otherwise skip and rely on integration tests.

- [ ] **Step 6: Commit**

```bash
git add pkg/universe/border_replication.go pkg/universe/world_base.go
git commit -m "feat(universe): BuildReplicationDelta and EntityNetworkInfo

BorderDispatcher now constructs real EntityRefs from the spatial
grid, using ComponentReplicator scans via the existing registry.
Build() closure defers actual serialization until the dispatcher
decides to send.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 7.3: Plug BorderDispatcher into PostSystems

**Files:**
- Modify: `pkg/universe/node_bridge_impl.go`

- [ ] **Step 1: Add the dispatcher field**

Edit `pkg/universe/node_bridge_impl.go`. The `nodeBridge` struct (around line 4-7) gains a `borderDispatcher *BorderDispatcher` field:

```go
type nodeBridge struct {
	node             *Node
	coord            *Coordinator
	borderDispatcher *BorderDispatcher
}
```

- [ ] **Step 2: Construct the dispatcher at node init**

Find where `nodeBridge` is constructed (in `coordinator.go` likely). Add:

```go
bd := NewBorderDispatcher(node.Base, buildNeighborViewers(node), node.Base.ReplicationRegistry())
nb := &nodeBridge{
	node:             node,
	coord:            coord,
	borderDispatcher: bd,
}
```

`buildNeighborViewers(node)` is a helper that constructs `*NodeViewer` instances from `node.Neighbors`. Add it to `border_replication.go`:

```go
// BuildNeighborViewers constructs NodeViewers for all of a node's neighbors.
func BuildNeighborViewers(node *Node) map[string]*NodeViewer {
	out := make(map[string]*NodeViewer)
	for id, n := range node.Neighbors {
		cx, cy := boundaryMidpoint(node.Cell, n.Cell)
		out[id] = NewNodeViewer(id, NodeViewerID(id), cx, cy, nil)
	}
	return out
}

// boundaryMidpoint returns the midpoint of the shared edge between two cells.
// Pulls cell size from pkg/coords (which reads the coordinator's configured size).
func boundaryMidpoint(a, b CellID) (float32, float32) {
	cellSize := coords.CellSize()
	ax := float32(a.X)*cellSize + cellSize/2
	ay := float32(a.Y)*cellSize + cellSize/2
	bx := float32(b.X)*cellSize + cellSize/2
	by := float32(b.Y)*cellSize + cellSize/2
	return (ax + bx) / 2, (ay + by) / 2
}
```

- [ ] **Step 3: Call Tick() from PostSystems**

Edit `pkg/universe/node_bridge_impl.go` `PostSystems()` method (line 23). Add a call to the border dispatcher at the top, before the existing replica/proxy send logic:

```go
func (nb *nodeBridge) PostSystems() {
	// New tiered push path. Parallel to the legacy proxy path;
	// Task 7.6 removes the legacy path once the new one is verified.
	if nb.borderDispatcher != nil {
		nb.borderDispatcher.Tick(uint64(nb.node.Engine.Tick()))
	}

	// ... existing sendReplicas/sendProxies code unchanged
}
```

Confirm `nb.node.Engine.Tick()` returns the current tick — if not, adjust.

- [ ] **Step 4: Wire NodeViewer.Send to the actual bridge**

`NodeViewer.Send` is currently a stub. Replace with a real send via the bridge channel. The viewer needs a reference to the source node and bridge. Extend the constructor:

```go
type NodeViewer struct {
	// ... existing fields
	sourceNode *Node
	destID     string
}

func NewNodeViewer(
	nodeID string,
	id uint64,
	boundaryX, boundaryY float32,
	tiers map[uint16]replication.ReplicationTier,
	sourceNode *Node,
) *NodeViewer {
	// ...
}

func (v *NodeViewer) Send(frame replication.Frame) {
	if v.sourceNode == nil {
		return
	}
	encoded := frame.Encode()
	dest := v.sourceNode.Neighbors[v.destID]
	if dest == nil {
		return
	}
	dest.Inbox <- NodeMessage{
		Type:        MsgBorderFrame,
		FromNodeID:  v.sourceNode.ID,
		BorderFrame: encoded,
	}
}
```

Update `BuildNeighborViewers` and all constructor call sites to pass `sourceNode`.

- [ ] **Step 5: Build**

Run: `go build ./pkg/universe/...`
Expected: compiles.

- [ ] **Step 6: Commit**

```bash
git add pkg/universe/border_replication.go pkg/universe/border_viewer.go pkg/universe/node_bridge_impl.go pkg/universe/coordinator.go
git commit -m "feat(universe): wire BorderDispatcher into PostSystems

Each node builds NodeViewers for its neighbors at init and calls
BorderDispatcher.Tick in PostSystems. NodeViewer.Send writes to
the destination node's inbox with MsgBorderFrame. Legacy proxy
path still runs in parallel; Task 7.6 deletes it.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 7.4: Handle MsgBorderFrame in node.processMessage

**Files:**
- Modify: `pkg/universe/node.go`

- [ ] **Step 1: Add the new case**

Find `processMessage` in `node.go` (line 58). Add cases alongside existing ones:

```go
case MsgBorderFrame:
    if err := n.handleBorderFrame(msg.BorderFrame, msg.FromNodeID); err != nil {
        n.Log.Warnf("border frame decode error from %s: %v", msg.FromNodeID, err)
    }

case MsgHandoffPrepare:
    if msg.HandoffPrepare != nil {
        n.handleHandoffPrepare(msg.HandoffPrepare, msg.FromNodeID)
    }

case MsgHandoffCommit:
    if msg.HandoffCommit != nil {
        n.handleHandoffCommit(msg.HandoffCommit, msg.FromNodeID)
    }

case MsgForwardInput:
    if msg.ForwardInput != nil {
        n.handleForwardInput(msg.ForwardInput)
    }
```

- [ ] **Step 2: Implement handleBorderFrame**

Add a method on `*Node`:

```go
func (n *Node) handleBorderFrame(buf []byte, fromNodeID string) error {
	frame, err := replication.DecodeFrame(buf)
	if err != nil {
		return err
	}
	// Apply the frame: for each entry, upsert the replica entity and
	// advance the baseline.
	for _, entry := range frame.Entries {
		n.Base.ApplyBorderEntry(fromNodeID, entry)
	}
	return nil
}
```

Add `ApplyBorderEntry` to `WorldBase`:

```go
// ApplyBorderEntry processes one entry from an incoming MsgBorderFrame.
// If the referenced entity does not yet exist locally, it is created
// as a border replica. If it exists, its state is advanced from the
// delta payload. Stale epochs (frame.Epoch < local) are dropped.
func (w *WorldBase) ApplyBorderEntry(fromNodeID string, entry replication.FrameEntry) {
	// Stale-packet check.
	if w.highestSeenEpoch[entry.NetID.ID] > entry.NetID.Epoch {
		w.metrics.IncStalePacketsDropped()
		return
	}
	if entry.NetID.Epoch > w.highestSeenEpoch[entry.NetID.ID] {
		w.highestSeenEpoch[entry.NetID.ID] = entry.NetID.Epoch
	}
	// Delegate to the existing replica upsert path — replica creation
	// and update mirrors the old CreateReplica/UpdateReplicaBase but
	// consumes the new delta format.
	w.upsertBorderReplica(fromNodeID, entry)
}
```

Add `highestSeenEpoch map[uint32]uint32` to `WorldBase` struct and initialize in the constructor. The `upsertBorderReplica` method replaces the old `CreateReplica`/`UpdateReplicaBase` split — implement as a single function that creates if absent, updates if present. Pull logic from the existing paths in `world_base.go` lines 645-706.

- [ ] **Step 3: Implement handoff handlers**

Stub out `handleHandoffPrepare`, `handleHandoffCommit`, `handleForwardInput` — Task 7.5 fills them in. For now:

```go
func (n *Node) handleHandoffPrepare(p *HandoffPreparePayload, fromNodeID string) {
	n.Log.Debugf("handoff prepare: net=%d epoch=%d from=%s", p.NetID, p.Epoch, fromNodeID)
	// Task 7.5
}
func (n *Node) handleHandoffCommit(p *HandoffCommitPayload, fromNodeID string) {
	n.Log.Debugf("handoff commit: net=%d epoch=%d tick=%d", p.NetID, p.Epoch, p.CommitTick)
	// Task 7.5
}
func (n *Node) handleForwardInput(p *ForwardInputPayload) {
	// Task 7.5 — re-route into the normal input router
}
```

- [ ] **Step 4: Build**

Run: `go build ./pkg/universe/...`
Expected: compiles. `ApplyBorderEntry`, `upsertBorderReplica`, `highestSeenEpoch`, and `metrics.IncStalePacketsDropped` must be defined; add stubs as needed to reach a green build.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/node.go pkg/universe/world_base.go
git commit -m "feat(universe): handle MsgBorderFrame on receive

Wires the receive side: decode frame, stale-epoch check, upsert
border replicas. Handoff message handlers stubbed pending Task 7.5.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 7.5: Handoff flow end-to-end

**Files:**
- Modify: `pkg/universe/border_replication.go`
- Modify: `pkg/universe/handoff.go`
- Modify: `pkg/universe/node.go`

- [ ] **Step 1: Integrate the state machine into the dispatcher**

Edit `pkg/universe/border_replication.go`. Add `*HandoffStateMachine` to the `BorderDispatcher` struct and initialize in the constructor. In `Tick`, after building the frame for each neighbor, run a promotion/commit sweep:

```go
func (bd *BorderDispatcher) runHandoffSweep(nv *NodeViewer, tick uint64) {
	// For each (entity, neighbor) pair that this neighbor might take
	// ownership of, evaluate promotion and commit conditions.
	for ent := range bd.ownedEntitiesForNeighbor(nv) {
		key := HandoffKey{EntityNetID: ent.NetID.ID, NeighborID: nv.destID}
		phase := bd.handoff.State(key)

		if bd.handoff.InCooldown(key, tick) {
			continue
		}

		switch phase {
		case HandoffUnseen, HandoffBorder:
			if bd.shouldPromote(ent, nv, tick) {
				bd.sendHandoffPrepare(ent, nv, tick)
				bd.handoff.SetState(key, HandoffPromoted)
			}
		case HandoffPromoted:
			bd.handoff.TickWarmup(key)
			if bd.entityCrossed(ent, nv) && bd.handoff.CanCommit(key) {
				bd.sendHandoffCommit(ent, nv, tick)
				bd.handoff.EnterCooldown(key, tick)
				bd.handoff.SetState(key, HandoffUnseen)
				bd.downgradeToReplica(ent)
			}
		}
	}
}
```

The helpers `ownedEntitiesForNeighbor`, `shouldPromote`, `sendHandoffPrepare`, `entityCrossed`, `sendHandoffCommit`, `downgradeToReplica` are added as methods on `BorderDispatcher`. Each is small:

- `ownedEntitiesForNeighbor` — iterate entities within the max tier radius of the shared boundary, owned by this node.
- `shouldPromote` — `InsideRadius(tier, vx, vy, ex, ey)` with PromoteRadius OR projected-position lookahead with PromoteLookahead.
- `entityCrossed` — check the entity's current cell equals the neighbor's cell.
- `sendHandoffPrepare`/`sendHandoffCommit` — build the payload, write to `dest.Inbox`.
- `downgradeToReplica` — call `WorldBase.MarkAsReplica(entity)` to strip authority.

- [ ] **Step 2: Implement sendHandoffPrepare**

```go
func (bd *BorderDispatcher) sendHandoffPrepare(ent ownedEntity, nv *NodeViewer, tick uint64) {
	transferBlob, err := bd.base.SerializeForTransfer(ent.Entity)
	if err != nil {
		return
	}
	newEpoch := ent.NetID.Epoch + 1
	// Bump the local entity's epoch atomically before anyone else sees.
	bd.base.SetEntityEpoch(ent.Entity, newEpoch)

	baselines := bd.collectBaselinesFor(ent)

	payload := &HandoffPreparePayload{
		NetID:           ent.NetID.ID,
		Epoch:           newEpoch,
		Kind:            uint16(ent.Kind),
		TransferBlob:    transferBlob,
		ClientBaselines: baselines,
		ExpectedTick:    tick + uint64(MinWarmupTicks),
		OldEpoch:        ent.NetID.Epoch,
	}

	dest := nv.sourceNode.Neighbors[nv.destID]
	if dest != nil {
		dest.Inbox <- NodeMessage{
			Type:           MsgHandoffPrepare,
			FromNodeID:     nv.sourceNode.ID,
			HandoffPrepare: payload,
		}
	}
	bd.base.metrics.IncHandoffsInitiated()
}

func (bd *BorderDispatcher) collectBaselinesFor(ent ownedEntity) []ClientBaselineEntry {
	// Case A: iterate the client dispatcher's per-connection baseline
	// stores and collect anything keyed on ent.NetID.ID.
	// Case B: if the entity has a PlayerConn component, collect the
	// entire baseline store for that connection.
	// Implementation calls CollectBaselinesForEntity / CollectBaselinesForPlayer
	// with real *replication.BaselineStore references.
	return nil // Task 7.5b expands this
}
```

- [ ] **Step 3: Implement sendHandoffCommit and downgrade**

```go
func (bd *BorderDispatcher) sendHandoffCommit(ent ownedEntity, nv *NodeViewer, tick uint64) {
	dest := nv.sourceNode.Neighbors[nv.destID]
	if dest != nil {
		dest.Inbox <- NodeMessage{
			Type:       MsgHandoffCommit,
			FromNodeID: nv.sourceNode.ID,
			HandoffCommit: &HandoffCommitPayload{
				NetID:      ent.NetID.ID,
				Epoch:      ent.NetID.Epoch,
				CommitTick: tick,
			},
		}
	}
	bd.base.metrics.IncHandoffsCommitted()
	// Update player routing if this is a player entity.
	if connID, isPlayer := bd.base.PlayerConnForEntity(ent.Entity); isPlayer {
		bd.base.coord.UpdatePlayerRoute(connID, nv.destID)
	}
}

func (bd *BorderDispatcher) downgradeToReplica(ent ownedEntity) {
	bd.base.MarkAsReplica(ent.Entity)
}
```

- [ ] **Step 4: Implement receive side handlers**

Fill in `pkg/universe/node.go` handlers from Task 7.4:

```go
func (n *Node) handleHandoffPrepare(p *HandoffPreparePayload, fromNodeID string) {
	// Create shadow entity from TransferBlob. Shadow runs read-only
	// until MsgHandoffCommit arrives.
	frame, err := UnmarshalTransferFrame(p.TransferBlob)
	if err != nil {
		n.Log.Warnf("handoff prepare decode: %v", err)
		return
	}
	// Seed per-client baselines before the shadow goes live.
	n.Base.IngestClientBaselines(p.ClientBaselines)
	n.Base.CreateShadow(frame, p.Epoch, fromNodeID)
}

func (n *Node) handleHandoffCommit(p *HandoffCommitPayload, fromNodeID string) {
	n.Base.PromoteShadow(p.NetID, p.Epoch)
}

func (n *Node) handleForwardInput(p *ForwardInputPayload) {
	// Route into the normal input router as if the input arrived fresh.
	if p == nil || p.ConnID == 0 {
		return
	}
	n.Base.Coordinator().RouteInput(p.ConnID, p.InputBlob)
}
```

Add to `WorldBase`:

```go
// IngestClientBaselines seeds per-client baseline stores from a
// handoff prepare payload.
func (w *WorldBase) IngestClientBaselines(entries []ClientBaselineEntry) {
	// For each entry, look up the client replication system's
	// BaselineStore for entries[i].ConnID and call SetBaseline
	// with a reconstructed EntityBaseline.
	// Phase 7.5b implementation — for now, stub.
}

// CreateShadow creates a read-only shadow entity from a TransferFrame.
func (w *WorldBase) CreateShadow(frame *TransferFrame, epoch uint32, sourceNodeID string) {
	// Phase 7.5 stub — similar to CreateReplica but marks as "shadow."
}

// PromoteShadow converts a shadow entity to full authoritative status.
func (w *WorldBase) PromoteShadow(netID uint32, epoch uint32) {
	// Phase 7.5 stub — strips the shadow marker.
}
```

This task grows the file significantly. Keep each helper focused; prefer a single new file `pkg/universe/world_base_handoff.go` if it keeps `world_base.go` below 1500 lines.

- [ ] **Step 5: Build**

Run: `go build ./pkg/universe/...`
Expected: compiles. Iteratively resolve missing methods.

- [ ] **Step 6: Run handoff state machine tests**

Run: `go test ./pkg/universe/ -run TestHandoffState -v`
Expected: pass (these were written in Phase 5).

- [ ] **Step 7: Commit**

```bash
git add pkg/universe/border_replication.go pkg/universe/handoff.go pkg/universe/node.go pkg/universe/world_base.go pkg/universe/world_base_handoff.go
git commit -m "feat(universe): full co-simulation handoff flow

Border dispatcher runs promotion/commit sweeps, sends prepare and
commit payloads, ticks warmup, enters cooldown. Receiver creates
shadows on prepare, promotes on commit. Player handoffs update
the coordinator's routing table atomically.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 7.6: Delete legacy replica/proxy/ghost-authority paths

**Files:**
- Delete: `pkg/universe/replication_scan.go`
- Modify: `pkg/universe/replication.go` (remove ReplicaFrame/ProxySummary etc.)
- Modify: `pkg/universe/message.go` (remove legacy message types)
- Modify: `pkg/universe/bridge.go` (remove detail methods)
- Modify: `pkg/universe/node_bridge_impl.go` (remove sendReplicas/sendProxies/RequestDetail)
- Modify: `pkg/universe/world_base.go` (remove legacy replica/proxy/ghost methods)
- Modify: `pkg/universe/coordinator.go` (remove ProxiesEnabled)
- Modify: `pkg/universe/replication_test.go` (remove deleted-type tests)
- Modify: `pkg/universe/universe_test.go` (remove MsgReplica test case)
- Modify: `internal/game/replica_test.go` (update or delete)

- [ ] **Step 1: Remove ProxiesEnabled from config**

Edit `pkg/universe/coordinator.go`. Delete the `ProxiesEnabled bool` field from the `Config` struct. Search for any remaining references:

Run: `grep -rn "ProxiesEnabled" pkg/ internal/ examples/ cmd/`
Expected: only the Config definition. Delete all references.

- [ ] **Step 2: Delete replication_scan.go**

Run: `git rm pkg/universe/replication_scan.go`

- [ ] **Step 3: Remove ReplicaFrame/ProxySummary from replication.go**

Edit `pkg/universe/replication.go`. Delete:
- `ReplicaFrame` struct (~lines 75-85)
- `MarshalReplicaFrame`, `UnmarshalReplicaFrame` (~lines 112-148, 254-299)
- `DetailRequestMsg`, `DetailResponseMsg` (~lines 244-251)
- Any `ProxySummary` type and its marshal helpers

Keep:
- `ComponentID`, `ComponentReplicator`, `ReplicationRegistry`, `ComponentSlice` — still used by `transfer.go`.

Run: `grep -n "ReplicaFrame\|ProxySummary\|DetailRequestMsg\|DetailResponseMsg" pkg/universe/`
Expected: no matches after cleanup.

- [ ] **Step 4: Remove legacy message types**

Edit `pkg/universe/message.go`. Delete `MsgReplica`, `MsgProxySummary`, `MsgDetailRequest`, `MsgDetailResponse` constants. Delete the corresponding fields from `NodeMessage`: `Replicas`, `ProxySummaries`, `DetailRequest`, `DetailResponse`.

- [ ] **Step 5: Remove detail methods from NodeBridge interface**

Edit `pkg/universe/bridge.go`. Remove `RequestDetail` and `SendDetailResponse` from the `NodeBridge` interface. Remove the corresponding stubs from `NoopNodeBridge`.

- [ ] **Step 6: Gut node_bridge_impl.go**

Edit `pkg/universe/node_bridge_impl.go`. Delete:
- `sendProxies()` method
- `sendReplicas()` method
- `RequestDetail` and `SendDetailResponse` methods
- Any remaining references to `MsgReplica`/`MsgProxySummary`/`MsgDetailRequest`/`MsgDetailResponse`

Simplify `PostSystems()` to just call `nb.borderDispatcher.Tick(...)`.

- [ ] **Step 7: Gut world_base.go legacy paths**

Edit `pkg/universe/world_base.go`. Delete:
- `ScanBorderEntities`, `ScanBorderProxies`
- `ApplyReplicas`, `ApplyProxySummaries`
- `ClearReplicaUpdateFlags`, `ClearProxyUpdateFlags`
- `TickReplicaDeadReckoning`, `TickProxyDeadReckoning`
- `ExpireReplicas`, `ExpireProxies`
- `RemoveReplicaByNetID`, `RemoveProxyByNetID`
- `RequestPromotion`, `BuildDetailResponse`, `PromoteProxy`
- `TickGhosts`, `RemoveGhostByNetID` as server-authority mechanisms (the Ghost component type stays)
- `replicaNetIDs`, `proxyNetIDs` maps from the struct
- `CreateReplica`, `UpdateReplicaBase` (superseded by `upsertBorderReplica`)
- `FindReplica` (superseded by new lookup)

**Caution:** Several of these are called by `pkg/universe/node.go` in the old `processMessage` switch. Task 7.4 removed those calls. Double-check by grepping:

Run: `grep -n "TickGhosts\|ApplyReplicas\|ScanBorderEntities\|RequestPromotion\|PromoteProxy" pkg/universe/`
Expected: no matches.

- [ ] **Step 8: Update tests**

- Delete `TestReplicaFrame_*` and `TestProxySummary_*` test functions from `pkg/universe/replication_test.go`. Keep `TestReplicationRegistry_*` tests.
- In `pkg/universe/universe_test.go`, find the `MsgReplica` reference (line ~199) and delete that specific test case.
- Review `internal/game/replica_test.go` — if it tests old replica paths, delete it. If it tests something still relevant, update accordingly. Since the file is 330 lines and specifically named `replica_test.go`, likely a full delete:

```bash
git rm internal/game/replica_test.go
```

- [ ] **Step 9: Build**

Run: `go vet ./... && go build ./...`
Expected: compiles cleanly. If anything references the deleted types, the compiler points at it — delete or update the reference.

- [ ] **Step 10: Run all tests**

Run: `go test ./...`
Expected: all pass.

- [ ] **Step 11: Commit**

```bash
git add -A
git commit -m "refactor(universe): delete legacy replica/proxy/ghost-authority paths

Removes ReplicaFrame, ProxySummary, MsgReplica, MsgProxySummary,
MsgDetailRequest, MsgDetailResponse, RequestDetail,
SendDetailResponse, cfg.ProxiesEnabled, ghost-based server
authority, and replication_scan.go entirely. Ghost component
type retained for client-side last-known-position rendering.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 7.7: Wire coordinator.UpdatePlayerRoute

**Files:**
- Modify: `pkg/universe/coordinator.go`

- [ ] **Step 1: Locate existing routing mutation**

Run: `grep -n "setPlayerNode\|getPlayerNode" pkg/universe/coordinator.go`
Expected: find the existing helpers (around lines 1043-1060 per the map).

- [ ] **Step 2: Expose UpdatePlayerRoute**

Add to `coordinator.go`:

```go
// UpdatePlayerRoute atomically retargets a player connection's routing
// to a new node. Called by the handoff state machine on commit. The
// player's WebSocket connection is not touched; only the server-side
// routing table changes.
func (c *Coordinator) UpdatePlayerRoute(connID uint32, newNodeID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.setPlayerNode(connID, newNodeID)
}
```

- [ ] **Step 3: Build**

Run: `go build ./pkg/universe/...`
Expected: compiles.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/coordinator.go
git commit -m "feat(universe): Coordinator.UpdatePlayerRoute

Atomic routing table update for player handoff, called by the
handoff state machine on commit.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 7.8: Phase 7 checkpoint

- [ ] **Step 1: Full build + test**

Run: `go vet ./... && go test ./... && just build`
Expected: all green.

- [ ] **Step 2: Smoke both examples**

```bash
cd examples/4node-basic && just dev
```
Expected: boots, connects, entities render. Drive a ship across a cell boundary — should not pop or stall. Kill.

```bash
cd examples/slither && just dev
```
Expected: boots, snakes spawn and move, cross-cell replication visible. Kill.

---

## Phase 8: Correctness Tests, Performance Tests, Metrics, Verification

**Goal:** Write all 8 correctness tests and 7 performance tests referenced in the spec. Add the new NodeMetrics counters. Run the full verification checklist.

### Task 8.1: Add NodeMetrics counters

**Files:**
- Modify: `pkg/metrics/node_metrics.go`
- Modify: `pkg/metrics/node_metrics_test.go`

- [ ] **Step 1: Add counter fields**

Edit the `NodeMetrics` struct in `pkg/metrics/node_metrics.go`. Add:

```go
// Inter-node bandwidth and handoff counters — Phase 8 addition.
interNodeBytesSent   Counter
interNodeBytesRecv   Counter
borderFramesSent     Counter
handoffsInitiated    Counter
handoffsCommitted    Counter
stalePacketsDropped  Counter
entitiesInBorder     Gauge
entitiesInPromoted   Gauge
entitiesInHandoff    Gauge
```

Initialize them in `NewNodeMetrics`. Add increment and snapshot accessors.

- [ ] **Step 2: Update LoadSnapshot / Prometheus exporter**

The existing `/metrics` endpoint emits a snapshot struct. Extend the snapshot to include the new counters. Run:

```bash
grep -n "LoadSnapshot\|Prometheus" pkg/metrics/
```
Find the relevant places, add fields, and extend the `ServeHTTP` writer.

- [ ] **Step 3: Unit tests**

Add to `pkg/metrics/node_metrics_test.go`:

```go
func TestNodeMetrics_NewCounters(t *testing.T) {
	m := NewNodeMetrics("test", 20, nil, nil)
	m.IncInterNodeBytesSent(100)
	m.IncHandoffsInitiated()
	m.IncHandoffsCommitted()
	m.IncStalePacketsDropped()

	snap := m.Snapshot()
	if snap.InterNodeBytesSent != 100 {
		t.Fatalf("bytes: %d", snap.InterNodeBytesSent)
	}
	if snap.HandoffsInitiated != 1 || snap.HandoffsCommitted != 1 {
		t.Fatal("handoff counts")
	}
	if snap.StalePacketsDropped != 1 {
		t.Fatal("stale count")
	}
}
```

- [ ] **Step 4: Wire into BorderDispatcher and ApplyBorderEntry**

Edit `pkg/universe/border_replication.go` `Tick`: after `nv.Send(frame)`, increment `borderFramesSent` and add `frame.SizeEncoded()` to `interNodeBytesSent`. In `handleBorderFrame`, add the received size to `interNodeBytesRecv`. In the handoff handlers, increment `handoffsInitiated`/`handoffsCommitted` (already sketched in Task 7.5).

- [ ] **Step 5: Build and test**

Run: `go vet ./... && go test ./pkg/metrics/... ./pkg/universe/...`
Expected: green.

- [ ] **Step 6: Commit**

```bash
git add pkg/metrics/ pkg/universe/border_replication.go pkg/universe/node.go
git commit -m "feat(metrics): inter-node bandwidth and handoff counters

Nine new counters/gauges on NodeMetrics: InterNodeBytesSent/Recv,
BorderFramesSent, HandoffsInitiated/Committed, StalePacketsDropped,
EntitiesInBorder/Promoted/Handoff. All exposed via /metrics.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 8.2: Correctness test — BorderReplication_DeltaSize

**Files:**
- Create: `pkg/universe/border_replication_test.go`

- [ ] **Step 1: Create the test file**

Create `pkg/universe/border_replication_test.go`:

```go
package universe

import (
	"testing"

	"github.com/mmokit/mmokit/pkg/replication"
)

// newTwoNodeMesh is a shared helper used by all correctness tests.
// It constructs two Node instances with a LoopbackBridge between them,
// shared cell boundary at x=1024, and returns both.
func newTwoNodeMesh(t *testing.T, opts LoopbackOpts) (nodeA, nodeB *Node, bridge *LoopbackBridge) {
	t.Helper()
	// Implementation depends on existing Node construction helpers.
	// Use the same path as coordinator.createNode but with a
	// LoopbackBridge receiver wired to each node's Inbox.
	t.Skip("TODO: implement newTwoNodeMesh helper based on existing Node construction")
	return nil, nil, nil
}

func TestMesh_BorderReplication_DeltaSize(t *testing.T) {
	a, b, _ := newTwoNodeMesh(t, LoopbackOpts{})
	_ = a
	_ = b
	// Spawn 100 NPCs near the 0,0 <-> 1,0 boundary on node A.
	// No player on node B — NPCs are not client-visible.
	// Run 20 ticks.
	// Read a.Base.metrics.Snapshot().InterNodeBytesSent.
	// Assert it is at least 60% less than the pre-refactor baseline
	// (captured in testdata/border_replication_perf.golden).
	t.Skip("TODO: requires newTwoNodeMesh helper")
}
```

- [ ] **Step 2: Implement newTwoNodeMesh**

This helper is the foundation for every correctness and performance test. It constructs two minimal Node instances by directly invoking the existing node constructor (or by creating a Coordinator with `CellsX=2, CellsY=1`). Reuse the coordinator setup from `examples/4node-basic/main.go` as a template. Keep the helper small — it's just boot + wire.

Concrete implementation pattern:

```go
func newTwoNodeMesh(t *testing.T, opts LoopbackOpts) (*Node, *Node, *LoopbackBridge) {
	t.Helper()
	coord := NewCoordinator(Config{
		CellsX:    2,
		CellsY:    1,
		CellSize:  1024,
		TickRate:  20,
		Headless:  true,
		AoIRadius: 300,
	})
	coord.SetWorld(NewMinimalTestWorld) // test-only world factory
	if err := coord.Build(); err != nil {
		t.Fatalf("build mesh: %v", err)
	}
	nodeA := coord.NodeByID("node_0_0")
	nodeB := coord.NodeByID("node_1_0")

	bridge := NewLoopbackBridge(opts)
	bridge.SetReceiver(nodeA.ID, func(m NodeMessage) { nodeA.Inbox <- m })
	bridge.SetReceiver(nodeB.ID, func(m NodeMessage) { nodeB.Inbox <- m })
	// Replace the default bridge wiring with the loopback. This
	// requires a coord.SetBridgeFactory hook or equivalent; add
	// one if it doesn't exist.

	return nodeA, nodeB, bridge
}
```

If `NewMinimalTestWorld` and `SetBridgeFactory` don't exist, add them now as small helpers — the task isn't runnable without them.

- [ ] **Step 3: Remove t.Skip and run**

Replace `t.Skip(...)` with the actual assertions. Run the test. Tune the golden until the assertion is meaningful.

- [ ] **Step 4: Commit**

```bash
git add pkg/universe/border_replication_test.go
git commit -m "test(universe): TestMesh_BorderReplication_DeltaSize

Asserts inter-node byte count drops 60%+ versus pre-refactor
baseline for unobserved entities.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 8.3: Correctness test — Handoff_NoStall

- [ ] **Step 1: Add the test**

Append to `pkg/universe/border_replication_test.go`:

```go
func TestMesh_Handoff_NoStall(t *testing.T) {
	a, b, _ := newTwoNodeMesh(t, LoopbackOpts{})

	// Spawn a moving ship on node A with velocity toward the boundary.
	ship := a.Base.SpawnTestShip(900, 500, 50 /* vx */, 0)
	_ = ship

	// Tick until the ship physically crosses into cell (1,0).
	// Track the number of full-rate frames node B received for the
	// ship before MsgHandoffCommit arrives.
	var framesReceived int
	committed := false
	var commitTick uint64
	for tick := uint64(0); tick < 200 && !committed; tick++ {
		a.TickOnce()
		b.TickOnce()
		framesReceived += b.Base.BorderFramesReceivedFor(ship.NetID)
		if b.Base.HasAuthority(ship.NetID) {
			committed = true
			commitTick = tick
		}
	}

	if !committed {
		t.Fatal("handoff did not commit")
	}
	if framesReceived < MinWarmupTicks {
		t.Fatalf("only %d warmup frames received, want >= %d", framesReceived, MinWarmupTicks)
	}
	_, err := b.Base.EntityByNetID(ship.NetID)
	if err != nil {
		t.Fatalf("destination lookup failed on first post-commit tick: %v", err)
	}
	_ = commitTick
}
```

Helper methods referenced (`SpawnTestShip`, `BorderFramesReceivedFor`, `HasAuthority`, `EntityByNetID`, `TickOnce`) need to exist on `WorldBase` or `Node`. Add them as small test-support helpers if missing.

- [ ] **Step 2: Run and commit**

```bash
go test ./pkg/universe/ -run TestMesh_Handoff_NoStall -v
git add pkg/universe/border_replication_test.go pkg/universe/world_base.go pkg/universe/node.go
git commit -m "test(universe): TestMesh_Handoff_NoStall

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 8.4: Remaining correctness tests

For each of the following, follow the same pattern: append to `border_replication_test.go`, use `newTwoNodeMesh`, add any missing test-support helpers to `WorldBase`/`Node`, run, commit. Each is one task.

- [ ] **Task 8.4.1: TestMesh_StalePacketDrop**

Send a frame from node A with an old epoch after handoff. Assert receiver drops it and `StalePacketsDropped` counter increments.

- [ ] **Task 8.4.2: TestMesh_Teleport_WarmupFloor**

Teleport a ship from cell (0,0) to cell (1,0) in one tick. Assert commit is delayed by `MinWarmupTicks`.

- [ ] **Task 8.4.3: TestMesh_CrossingHysteresis_NoFlap**

Oscillate a ship across the boundary every tick for 100 ticks. Assert `HandoffsCommitted <= 100 / CrossingCooldownTicks + 1`.

- [ ] **Task 8.4.4: TestMesh_BaselineHandover_NonPlayerEntity**

Spawn 5 fake client connections subscribed to an NPC, cross the NPC, assert the first post-commit frame from node B to each client contains a delta entry (not a keyframe) for that NPC.

- [ ] **Task 8.4.5: TestMesh_BaselineHandover_PlayerEntity**

Spawn a player with 50 entities in AoI, cross the player, assert the first post-commit frame from node B contains zero keyframes for the 50 entities.

- [ ] **Task 8.4.6: TestMesh_PlayerHandoff_NoInputLoss**

Stream inputs to a player crossing the boundary. Assert every input is processed exactly once.

Each task: write the test, run it, commit with message `test(universe): TestMesh_X`.

### Task 8.5: Performance tests

**Files:**
- Create: `pkg/universe/border_replication_perf_test.go`
- Create: `pkg/universe/testdata/border_replication_perf.golden`

- [ ] **Step 1: Create the golden file with initial placeholder values**

Write `pkg/universe/testdata/border_replication_perf.golden`:

```text
# Performance golden numbers. Captured initially on commit $(git rev-parse --short HEAD).
# Update manually when intentional regressions are justified.

scaling_n10_bytes_per_tick=XXX
scaling_n100_bytes_per_tick=XXX
scaling_n500_bytes_per_tick=XXX
scaling_n1000_bytes_per_tick=XXX
scaling_n1000_dispatcher_ms=XXX

handoff_p50_ticks=XXX
handoff_p99_ticks=XXX

churn_allocs_per_tick=XXX
churn_max_state_size=XXX

loss_latency_bandwidth_overhead_pct=XXX

client_frame_size_bytes=XXX

handoff_blob_case_a_bytes_per_client=XXX
handoff_blob_case_b_bytes_n10=XXX
handoff_blob_case_b_bytes_n50=XXX
handoff_blob_case_b_bytes_n100=XXX
handoff_blob_case_b_bytes_n200=XXX
```

Replace `XXX` values after first run.

- [ ] **Step 2: Create the perf test file with one benchmark per spec item**

Create `pkg/universe/border_replication_perf_test.go`:

```go
package universe

import (
	"testing"
	"time"
)

// Perf tests are benchmarks that also assert against golden numbers.
// Run via: go test -bench=. -benchmem ./pkg/universe/

func BenchmarkBorderReplication_ScalingByEntityCount(b *testing.B) {
	for _, n := range []int{10, 100, 500, 1000} {
		n := n
		b.Run(fmt.Sprintf("N=%d", n), func(b *testing.B) {
			a, nb, _ := newTwoNodeMesh(&testing.T{}, LoopbackOpts{})
			for i := 0; i < n; i++ {
				a.Base.SpawnTestNPC(900, float32(i*10), 0, 0)
			}
			b.ResetTimer()
			start := time.Now()
			for i := 0; i < b.N; i++ {
				a.TickOnce()
				nb.TickOnce()
			}
			elapsed := time.Since(start)
			bytes := a.Base.Metrics().Snapshot().InterNodeBytesSent
			b.ReportMetric(float64(bytes)/float64(b.N), "bytes/op")
			b.ReportMetric(float64(elapsed.Nanoseconds())/float64(b.N)/1e6, "ms/op")
		})
	}
}

// ... six more benchmarks matching the spec.
```

Each of the remaining six benchmarks is structured identically: set up the mesh, spawn entities, tick, read metrics, assert against golden. Write each one. If a particular metric (e.g. heap allocs) requires `runtime.ReadMemStats`, add it.

- [ ] **Step 3: Initial golden capture**

Run: `go test -bench=. -benchmem ./pkg/universe/ -run=^$ -v`
Record the output. Update `testdata/border_replication_perf.golden` with real numbers.

- [ ] **Step 4: Add assertion-based regression tests**

Convert each benchmark into a paired regression test function (non-benchmark) that loads the golden and asserts current numbers are within tolerance:

```go
func TestPerf_ScalingByEntityCount_GoldenMatch(t *testing.T) {
	golden := loadGolden(t)
	// Run the bench body as a measurement pass.
	// Compare against golden with ±10% tolerance.
}
```

Run: `go test ./pkg/universe/ -run TestPerf -v`
Expected: passes on the golden it was just captured against.

- [ ] **Step 5: Commit**

```bash
git add pkg/universe/border_replication_perf_test.go pkg/universe/testdata/border_replication_perf.golden
git commit -m "test(universe): performance benchmarks and golden regression gates

Seven benchmarks: scaling by entity count, handoff latency under
load, churn (alloc + state growth), loss+latency, client frame
size non-regression, handoff blob size (cases A and B). Paired
regression tests assert against testdata/border_replication_perf.golden.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

### Task 8.6: Full verification

- [ ] **Step 1: Full build + vet + test**

Run: `go vet ./... && go test ./... && just build`
Expected: all green, binary built.

- [ ] **Step 2: Run benchmarks**

Run: `go test -bench=. -benchmem ./pkg/universe/ | tee /tmp/perf.txt`
Expected: all benchmarks complete; numbers within tolerance of golden.

- [ ] **Step 3: SDK regen + type check**

Run:
```bash
just client-sdk examples/4node-basic
cd examples/4node-basic/web && bunx tsc --noEmit && cd -
just client-sdk examples/slither
cd examples/slither/web && bunx tsc --noEmit && cd -
```
Expected: clean.

- [ ] **Step 4: Smoke examples**

Run: `cd examples/4node-basic && just dev`
Expected: client connects, entities render, ship crosses cell boundary with no visible stall or pop. Confirm the `/metrics` endpoint shows the new counters incrementing.

Kill. Then: `cd examples/slither && just dev`
Expected: snakes move across cells without visual gaps.

- [ ] **Step 5: Transparency smoke**

While `examples/4node-basic` is running, drive a ship repeatedly across the 0,0 <-> 1,0 boundary for 30 seconds. Confirm (visual inspection):
- No visible stalls or teleports.
- No client-side disconnect/reconnect.
- HUD shows no node-change indication.
- `HandoffsCommitted` metric increments at most once per second (flap damping).

- [ ] **Step 6: Remove the temporary baseline file**

Run: `git rm pkg/universe/testdata/border_replication_perf.golden.baseline`

- [ ] **Step 7: Final commit**

```bash
git add -A
git commit -m "chore: remove temporary baseline file; tiered push replication complete

All eight correctness tests and seven performance benchmarks
pass. Metrics, SDK regen, and manual smoke verified.

Co-Authored-By: Claude Opus 4.6 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review Checklist

Before declaring the plan complete, the executing agent (or the reviewer) should:

- Read each spec section and point to the task that implements it:
  - §Design Principles → Phases 1–7 collectively
  - §Transparency Guarantees → Phase 5 (crossing hysteresis, input routing), Phase 7.7 (UpdatePlayerRoute), Phase 8 correctness tests
  - §Shared Primitives → Phase 2
  - §Wire Format → Task 1.2, Task 2.3, Task 5.1
  - §Entity Identity → Phase 1
  - §Tier Config → Task 2.4
  - §Promotion + Co-Sim → Phase 5 + Task 7.5
  - §Crossing Hysteresis → `handoff.go` in Task 5.2
  - §Input Routing → Task 7.5 + Task 7.7
  - §Border Dispatcher Loop → Task 4.2 + Task 7.1–7.3
  - §Dual-Transport Parity → Frame encode/decode in Task 2.3, SizeEncoded() use in Task 8.1
  - §Loopback Test Harness → Task 6.1
  - §Correctness Tests → Task 8.2–8.4
  - §Performance Tests → Task 8.5
  - §Metrics → Task 8.1
  - §Migration / Delete List → Task 7.6
  - §Client SDK Impact → Task 1.4
  - §Client Baseline Continuity → Task 5.3 + Task 7.5 `collectBaselinesFor`

- Placeholder scan: search the plan for TBD/TODO/"figure out" — the ones present are either test-setup helpers pending inline implementation in the task that needs them (e.g., `newTwoNodeMesh`) or explicit sub-task markers ("Task 7.5b"). No bare placeholders.

- Type consistency: check that `HandoffKey`, `HandoffPhase`, `HandoffStateMachine`, `NodeViewer`, `BorderDispatcher`, `Frame`, `FrameEntry`, `NetID`, `ClientBaselineEntry`, `HandoffPreparePayload`, `HandoffCommitPayload`, `ForwardInputPayload` are referenced consistently across phases.
