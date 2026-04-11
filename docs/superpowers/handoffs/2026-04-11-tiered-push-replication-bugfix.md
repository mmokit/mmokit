# Tiered Push Replication — Bugfix Handoff

**Date:** 2026-04-11
**Branch:** `feature/tiered-push-replication`
**Tip commit at handoff:** `096c523` (Revert "fix(system): autoReplicator degrades gracefully on border replicas")
**Parent branch:** `feature/web-pixi-sdk-modernization` (already merged to `main` at session start)

## Your job in one sentence

Make the space game work correctly after a major border-replication refactor: local entities must be visible to their own clients, players must be able to cross cell boundaries both directions, and players must see entities owned by neighbor nodes (border replicas) without the server panicking. Two previous fix attempts both broke things; a third attempt needs to be designed carefully against the real production flow, not just unit tests.

## Quick orientation

- The branch is 41 commits of a planned 9-phase refactor ([spec](../specs/2026-04-11-tiered-push-replication-design.md), [plan](../plans/2026-04-11-tiered-push-replication.md)).
- Phase 7.6 did a ~1800-line atomic delete of the legacy border replication path (`ScanBorderProxies`, `ApplyProxySummaries`, `ReplicaFrame`, `ProxySummary`, `MsgReplica`/`MsgProxySummary`/`MsgDetailRequest`/`MsgDetailResponse`, `ProxiesEnabled` config, `PromoteProxy`, etc.) and replaced it with a new `BorderDispatcher` + `MsgBorderFrame` + `WorldBase.ApplyBorderFrame` path.
- The new path is simpler but lower-fidelity: `BorderDispatcher.Build` encodes a fixed 18 bytes per entity (`worldX`, `worldY`, `radius`, `qvx`, `qvy`, 2 bytes padding) and `upsertBorderReplica` in `pkg/universe/world_base.go` creates replica entities with only `Position`, `Velocity`, `NetworkID`, `EntityKind`, `Collider`, `Replica` components — **no game-specific components** like `ShipControl`, `Rotation`, `Health`, etc.
- The legacy path used to copy arbitrary game components onto replicas via `ReplicationRegistry` + `ScanBorderWithRegistry` / `ApplyReplicasWithRegistry`. That's gone. Phase 7.2 of the original plan was supposed to re-wire the registry into the new path but was descoped during execution.
- Deferred handoff infrastructure lives in `pkg/universe/handoff.go`, `pkg/universe/baseline_handover.go`, `pkg/universe/loopback_bridge.go`, and in Phase 5.1 message types (`MsgHandoffPrepare`, `MsgHandoffCommit`, `MsgForwardInput`). All built and unit tested but **not wired into production** — do not touch unless you're re-enabling that whole design.

## The bug in detail

### Symptoms reported in the space game

1. **Teleport panic** (original reported bug): `tp xennion 5 5` panics the server with `panic: auto_replicator: required component missing on entity` at `pkg/system/auto_replicator.go:723` (inside `reflectBinding.hash`), call stack through `ReplicationSystem.Update` → `autoReplicator.Hash`.
2. After first fix attempt (`602f0c5`): teleport works, but **asteroids in adjacent cells are no longer visible** until the player physically crosses into them. Asteroids in the player's own cell are fine.
3. After second fix attempt (`fd0a473`, now reverted): **worse than (2)**. Specifically:
   - Local asteroids in the home cell (1_1) are **invisible to the player sitting in 1_1** (but collision still works — entities exist).
   - Teleport **doesn't apply until the player moves a bit**, then still doesn't tp instantly like it used to.
   - Re-entering cell 1_1 after leaving **glitches the player out at the boundary** and prevents re-entry.
   - Asteroids are only visible in the current local cell after leaving 1_1.

### Root cause of the original teleport panic

The panic path:
```
ReplicationSystem.Update iterates entities from the spatial grid
  → for each entity, looks up rep := Replicators.Get(entityKind)
  → rep.Hash(h, viewer, entry) ─ this is autoReplicator.Hash
    → for each binding in bindings: b.hash(entity, h, viewer, entry)
      → reflectBinding.hash: if !rb.ecsMap.HasAll(entity) { panic(...) }
```

`reflectBinding.hash` at `pkg/system/auto_replicator.go:717-733` panics when the entity is missing the component that binding wraps — **unless** the binding was created via `OptionalComponent[T]` (which sets `rb.optional=true` and falls back to `rb.hashZeros(h)`).

Ship has many required bindings (ShipControl, Rotation, Health, Shield, etc.). After teleport, the player's ship becomes a border replica on the source cell (or the destination node creates one, or both) — a replica has only the minimal 6-component set from `upsertBorderReplica`. When the local client-facing `ReplicationSystem` iterates its spatial grid and hits this replica, `rep.Hash` panics on the first required binding whose component isn't present.

Asteroid doesn't have this problem because its bindings only reference Position, Velocity, Collider — all present on every replica.

### Why the first fix (`602f0c5`) regressed asteroids in adjacent cells

`602f0c5` added a guard in `pkg/system/replication.go` that skips any entity with the `Replica` component inside the client `ReplicationSystem.Update` loop. This stopped the panic, but also stopped the client from ever seeing border replicas — for asteroids in adjacent cells, the replica on the local node is the **only** way for the player's client to render them (the client is connected to exactly one node and only receives frames from that node's `ReplicationSystem`). So adjacent-cell asteroids became invisible until the player crossed the boundary and the asteroid became local.

The existing `TestReplicationSystem_SkipsBorderReplicas` test in `replication_test.go` codifies the (wrong) 602f0c5 behavior — **you'll need to replace it** with a test that matches whatever third-attempt fix you ship.

### Why the second fix (`fd0a473`, reverted) broke the home cell entirely

`fd0a473` reverted the 602f0c5 skip and tried a different approach: make `autoReplicator.Hash/Snapshot/InitialData` detect replicas and wrap each binding call in a `recover()` block that silently swallows the specific panic string `"auto_replicator: required component missing on entity"`. The idea: replicas pass through all the bindings; missing-component bindings no-op via recovery; present-component bindings produce real hash/snapshot output.

Signature change: `AutoReplicator(entityType, bindings...)` → `AutoReplicator(world *ecs.World, entityType, bindings...)` so the replicator can allocate its own `*ecs.Map1[component.Replica]` via `ecs.NewMap1`.

**This broke things that my unit tests did not catch:**
- Local asteroids in the home cell 1_1 became **invisible** even though they're not replicas
- Teleport started applying with a delay (not instantly)
- Re-entering cell 1_1 bounced the player at the boundary
- Asteroids in new cells only appeared after movement

Running `go vet` + `go test ./...` was clean. Both unit tests I wrote (`TestAutoReplicator_ReplicaWithMissingComponentNoPanic` and `TestAutoReplicator_ReplicaWithAllComponentsStillHashes`) passed. The tests covered `AutoReplicator.Hash` in isolation but did **not** exercise the full `ReplicationSystem.Update` path, the spatial grid, or the ECS query machinery across many entities.

**Theories for the root cause** (unverified, need investigation in the real runtime):

1. **`ecs.NewMap1` side effect / registration order.** Ark ECS's `Map1[T]` may lazily register component type T with the world the first time it's called. `AutoReplicator` is constructed many times in `pkg/mmokit/mmokit.go:1026` inside a `for _, def := range defs` loop — each call created a *fresh* `ecs.NewMap1[component.Replica](w)`. Stacking N duplicate map handles on the same world may be clobbering a filter cache, archetype index, or iterator state that `Without(Replica)` queries depend on elsewhere.

2. **Interaction with `sdkgen --dump-schema`.** The sdkgen binary runs `BuildReplicators(w, nil, defs...)` with a dummy world to extract schema metadata. With the signature change, sdkgen now passes a *different* world to `AutoReplicator` than the production code. If any state is cached across those calls (unlikely but worth checking), the dummy world's state could leak into production.

3. **The `recover()` + re-`panic()` pattern in `gracefulHash`/`gracefulSnapshot`/`gracefulInitialData`.** These functions check `recover()`'s value, match against a specific panic string, and re-panic otherwise. Re-panicking inside a `defer` may have different stack/defer-chain semantics than a fresh panic — particularly across goroutines or when the panic is wrapped by ECS internals.

4. **Side effect on the spatial grid iterator.** `ecs.NewMap1[T]` might force a world rebuild/compaction that invalidates in-flight `QueryRadius` results. I did not investigate this path.

None of these are proven — they are candidates to investigate.

## Current state (where you start)

**Branch tip is `096c523` — the revert of `fd0a473`.** Net effect: the codebase is back at the `602f0c5` state:
- Teleport works (no panic)
- Local entities render in the player's own cell
- Border replicas (asteroids etc. in adjacent cells) are invisible until the player crosses boundaries
- The `TestReplicationSystem_SkipsBorderReplicas` test asserts the broken-by-design behavior (replicas skipped at dispatcher level)
- `go vet ./...` and `go test -count=1 ./...` are clean
- `just build` produces `bin/server`

**What you inherit:**
- 43 commits on the branch so far (38 from the main refactor + bugfix attempts + 2 reverts)
- A design spec and implementation plan at `docs/superpowers/specs/` and `docs/superpowers/plans/`
- A working but visually-incomplete refactor — ready to merge except for this bug

## Critical files and call path

### The bug lives in this hot path

1. `pkg/system/replication.go:449-520` — `ReplicationSystem.Update` iterates `s.results` (a `spatial.QueryRadius` output) and calls `rep.Hash(&s.hasher, viewer, entry)` at line ~514.

2. `pkg/system/auto_replicator.go:134-138` — `autoReplicator.Hash` iterates `a.bindings` and calls `b.hash(entry.Entity, h, viewer, entry)`.

3. `pkg/system/auto_replicator.go:717-733` — `reflectBinding.hash` panics if `!rb.ecsMap.HasAll(entity)` and `rb.optional == false`. Same panic at `:745-750` (snapshot) and `:770-778` (initial data).

4. `pkg/universe/world_base.go:600-660` (approximate — find via grep for `ApplyBorderFrame` / `upsertBorderReplica`) — creates replica entities from decoded border frames with the minimal 6-component set.

5. `pkg/mmokit/mmokit.go:991-1028` — `BuildReplicators` is where `AutoReplicator` is constructed per entity kind. This is the binding registration point.

### Relevant git commits to understand before fixing

- `9581693` — the big Phase 7.6 cutover commit that deleted ~1800 lines. **Read this commit carefully** to understand what was removed from `world_base.go`, `replication.go`, `replication_scan.go` (deleted entirely), and `internal/game/replica_test.go`. Pay special attention to what `ApplyReplicasWithRegistry` and `CreateReplica` used to do.
- `709788b` — the world-space border frame encoding + `ApplyBorderFrame` commit. This shows the new minimal 6-component replica shape.
- `602f0c5` — first bugfix attempt (dispatcher-level replica skip). Minimal diff; look at what it actually does in `pkg/system/replication.go`.
- `fd0a473` — second bugfix attempt (autoReplicator-level graceful hash). **REVERTED by `096c523`.** Read the diff to understand what was tried and why it didn't work.
- `096c523` — the revert itself; current branch tip.

### Test files

- `pkg/system/replication_test.go:523-610` — `TestReplicationSystem_SkipsBorderReplicas` codifies the current (wrong) 602f0c5 behavior. **Replace this** with a test that matches your fix.
- `pkg/system/auto_replicator_test.go` — existing AutoReplicator tests. My fd0a473 attempt added `TestAutoReplicator_ReplicaWithMissingComponentNoPanic` and `TestAutoReplicator_ReplicaWithAllComponentsStillHashes`; both were reverted with fd0a473 and are not present on the current tip.
- `pkg/universe/border_replication_apply_test.go` — 5 unit tests for `ApplyBorderFrame` + 2 perf benchmarks. These do NOT exercise the full `ReplicationSystem.Update` flow.

### What's missing in test coverage

**None of the existing unit tests exercise the real-world call path that broke:** player entity + spatial grid + ReplicationSystem.Update + border replica + teleport/transfer. The tests mock things out at unit boundaries. Any fix you attempt needs to be validated by running the actual space game (`just run` or `cd examples/4node-basic && just dev`), not just `go test ./...`.

## How to reproduce the original bug

Running `just run` (space game) then typing in the server console: `tp xennion 5 5` (with a connected player named `xennion`). You'll see `teleported to (1,1):(5, 5)` followed by the `panic: auto_replicator: required component missing on entity` stack trace. This reproduces at `HEAD~1` (before `096c523`) if you revert `096c523` to get back to `602f0c5` state — or rather, reverts the revert.

**Important:** At the *current* tip `096c523`, teleport does **not** panic (because 602f0c5's dispatcher-level skip is still in place). But adjacent-cell asteroids are invisible. Reverting `096c523` gives you the 602f0c5 state; then reverting `602f0c5` gives you the broken-teleport state.

## Design constraints for the fix

- **Do not delete or touch the handoff infrastructure** (`pkg/universe/handoff.go`, `pkg/universe/baseline_handover.go`, `pkg/universe/loopback_bridge.go`, or the `MsgHandoffPrepare`/`MsgHandoffCommit`/`MsgForwardInput` message types). These are built-but-unwired infrastructure for a future roadmap #12 follow-up.
- **Do not change the `MsgTransfer` + `ArrivalConfirm` + `Ghost` entity transfer protocol.** It works and is handling ownership transfer for the refactor.
- **Do not re-introduce `ReplicaFrame`, `ProxySummary`, `MsgReplica`, `MsgProxySummary`, or any of the deleted legacy types.** The cutover was the point of the refactor.
- **Do not break the `AutoReplicator` construction signature** without updating the six call sites in `pkg/system/auto_replicator_test.go` and the one in `pkg/mmokit/mmokit.go:1026`. If you do change it (as fd0a473 did), make sure you also verify the production game boots and runs, not just that `go test` passes.
- **The client is connected to exactly one node at a time.** That node's `ReplicationSystem` is the sole replication channel the client sees. For the client to ever render a neighbor-cell asteroid, that asteroid's state must reach the local node's `ReplicationSystem.Update` output.
- **The `Replica` component must remain the marker for "mirrored from a neighbor node."** Don't invent a new marker.

## Fix options ranked

### Option A: Rebuild the registry-driven border frame (Phase 7.2 resurrected)

The original design. `BorderDispatcher.candidatesFor` uses the game's `ReplicationRegistry` to encode per-component data into the border frame. `upsertBorderReplica` uses the same registry to apply that data onto replica entities, restoring the full component set. Client `ReplicationSystem` iterates replicas normally, the hash works because all required components are present, no panic dance needed. **This is the correct long-term fix** and matches the spec. Rough scope: 1–2 hours of careful integration work.

**Risks:**
- The old `ScanBorderWithRegistry` / `ApplyReplicasWithRegistry` were deleted in `9581693`. You'll need to re-implement them or study the old commit carefully to understand the component-serialization format.
- Must handle the case where sender and receiver have different sets of registered components (e.g., game updates).
- Wire format needs to be length-prefixed so receivers can skip unknown components.
- Old test coverage for this path was also deleted (`internal/game/replica_test.go` in the same commit).

**Why this is my recommendation:** It's the only option that actually restores full visual fidelity for border replicas — any other option produces degraded visuals for anything more complex than an asteroid.

### Option B: Component-level optional-ness check in `reflectBinding`

At the `reflectBinding` level, before checking `rb.optional`, also check if the entity has a `Replica` marker (via a replicaMap passed in at construction). If yes, treat the binding as optional for this call. This avoids the outer-level panic/recover dance and the `ecs.NewMap1` side effect theories.

**Risks:**
- Passing `replicaMap` through to every `ComponentBinding` constructor requires updating 8 binding types (`entryPositionBinding`, `viewerRelativePosBinding`, `qVelocityBinding`, `qAngleBinding`, `qSizeBinding`, `meshStateBinding`, `bindingGroup`, `reflectBinding`).
- Producing zero-bytes for ship-specific components means the client sees ships at (0, 0) rotation, zero health, etc. — visually degraded but not broken.
- Still doesn't solve the same underlying concern that fd0a473 ran into: why did adding `ecs.NewMap1[component.Replica]` create runtime side effects? That needs to be understood before any approach that relies on calling `NewMap1` in hot paths.

### Option C: Dispatcher-level two-pass

Revert the `602f0c5` skip, then at the dispatcher level iterate entities twice: once for non-replicas (normal hash + full snapshot + AutoReplicator), once for replicas (using a simple "position + collider radius + kind" hash bypass that does NOT call the AutoReplicator bindings). Produces minimal snapshots for replicas.

**Risks:**
- Client has to know to decode these minimal snapshots differently — the frame wire format becomes non-uniform.
- Visual fidelity is still limited to position/size (no rotation, no health, etc.).
- Doesn't resolve the underlying architectural mismatch that replicas lack game-specific components.

### Option D: Make all AutoReplicator bindings implicitly optional

Change `Component[T]` to default to the optional-zero behavior, delete `OptionalComponent[T]`, and remove the panic paths from all three `reflectBinding` methods. Programmer errors become silent — a forgotten component produces zero bytes instead of a loud crash at first AoI scan. **This is probably not what you want** but is the minimum-diff option.

## Execution guidance

1. **Start by reproducing the bug in isolation.** Run `just run`, connect a client (the web client at `http://localhost:8080` or the test bot), and confirm the current state (teleport works, adjacent-cell asteroids invisible). Do not make any changes before you can reproduce this baseline.

2. **Investigate the fd0a473 failure mode before deciding on the fix.** Check out `fd0a473` on a throwaway branch (`git checkout -b investigate fd0a473`), run the space game, and replicate the "no asteroids in home cell" symptom. Add printf logging inside `autoReplicator.Hash` to see:
   - How many entities are iterated per tick
   - Which entities are flagged as `isReplica` and why
   - What hash values are being computed
   - Whether `ecs.NewMap1[component.Replica](world)` is returning a map that incorrectly matches non-replica entities
   This investigation will tell you whether Theory 1/2/3/4 from above is the culprit. Only after you know the real cause should you design Option A/B/C/D.

3. **Once you understand fd0a473's failure**, pick an option and implement it. Whichever option, **do not land it until you've run the space game end-to-end** and verified:
   - Teleport works and applies instantly
   - Home cell asteroids are visible to a stationary player
   - Moving to an adjacent cell doesn't cause glitches
   - Returning to the home cell works without bouncing at the boundary
   - **Adjacent-cell asteroids become visible to a stationary player at the cell boundary** (the original regression 602f0c5 created — this is the success criterion)
   - The existing unit tests pass (`go test ./...`)
   - `go vet ./...` clean

4. **Replace `TestReplicationSystem_SkipsBorderReplicas`** in `pkg/system/replication_test.go` with a test that matches your fix's actual behavior. Whatever you ship, the current test's assertion ("border replica should NOT be visible") contradicts the spec's goal ("client sees neighbor entities at borders") — that test is a trap for future readers.

5. **Document what you changed** with a commit message that future-you can understand. Name it `fix(system): ...` or `refactor(universe): ...` as appropriate. Reference the failed fd0a473 attempt so the next person doesn't repeat the mistake.

## What's left after you fix this bug

Once the bug is fixed and the space game works end-to-end, the following items are **deferred** from the refactor but still expected to happen afterward (tracked in `memory/project_cosim_handoff_deferred.md` and in `docs/planning/mmokit-roadmap.md` Feature #11 status):

1. **Fix `sdkgen` repeated-field item types.** Three pre-existing errors in `web-pixi/sdk/delta-decoder.ts` — `Cannot find name 'ShipStatusEffectsItem'`, `'LootCrateItemsItem'`, `'NPCStatusEffectsItem'`. Root cause: `cmd/sdkgen/generate.go` references `<Field>Item` types for repeated/array fields but doesn't emit the corresponding type declarations in `entities.ts`. Only affects space game (4node-basic types clean). Tracked in `memory/project_sdkgen_repeated_field_bug.md`. User explicitly said to fix this *after* the tiered-push-replication branch merges.

2. **Slither body-extent border replication regression.** The legacy path had a `examples/slither/world.go` override for `ScanBorderEntities` that walked snake body segments to mark long snakes as near-border. Deleted in the cutover commit without replacement. `BorderDispatcher.entityNearNeighborEdge` only tests the head's `Position`. Long snakes whose body tail crosses a cell boundary won't be fully visible on the neighbor. Fix requires adding a game-facing candidate-provider hook to `BorderDispatcher` — deferred as a slither-only cleanup. Space game unaffected.

3. **Co-simulation handoff wiring (Phase 7.5 + 7.7).** The handoff state machine, baseline handover helpers, `MsgHandoffPrepare`/`MsgHandoffCommit`/`MsgForwardInput` types, and loopback test bridge are all fully built and unit-tested but not wired into `BorderDispatcher`. The existing `MsgTransfer`+`Ghost`+`ArrivalConfirm` protocol still handles entity ownership transfer. This was deferred to roadmap #12 follow-up. When #12 lands, the fully-built infrastructure at:
   - `pkg/universe/handoff.go` (state machine: `Unseen → Border → Promoted → Handoff` with `MinWarmupTicks=5` + `CrossingCooldownTicks=20`)
   - `pkg/universe/baseline_handover.go` (Case A + Case B collection helpers for per-client acknowledged snapshots)
   - `pkg/universe/message.go` (three new envelope types)
   - `pkg/universe/loopback_bridge.go` (test harness for 2-node integration tests)
   ...picks up and gets wired through. Also at this time: `Coordinator.UpdatePlayerRoute` for atomic routing-table updates.

4. **Delta compression of border frames.** The current path sends 18 bytes (absolute state) per entity per tick. The `BaselineStore` allocated on each `NodeViewer` in `pkg/universe/border_viewer.go:54` is unused — #12 should wire it through `BorderDispatcher.candidatesFor`'s Build closure to delta-encode against acknowledged baselines. Immediate 30–60% bandwidth reduction for mostly-static entities.

5. **Per-component `ReplicationRegistry` integration in border frames.** If you pick Option A above, this is the bug fix itself — and it subsumes this follow-up. If you pick Option B/C/D, this is still a follow-up for richer replica fidelity.

6. **Code review follow-ups from commit 897c04c's review.** Already mostly addressed but audit:
   - `pkg/universe/border_replication.go` stale comments still mention "Phase 7.6" and "legacy path runs in parallel" — might need another cleanup pass after your fix lands.
   - `pkg/universe/world_base.go` `ApplyBorderFrame` doc references deleted `ApplyReplicas`/`ApplyProxySummaries`. Accurate but wordy — trim if you're touching the file.

7. **`pkg/mmokit/README.md` Config example.** Already fixed in `897c04c` (removed `ProxiesEnabled` line). Verify it still matches the `Config` struct after your fix.

8. **Plan + roadmap docs.** Already updated in `897c04c` with deferral notes. Add one sentence to the plan under "Known regressions" describing whatever fix you landed so the next reader knows what state the border replication is in.

## Quick commands

```bash
# Get to the branch
cd .
git checkout feature/tiered-push-replication

# Current state (should show 096c523 as tip)
git log --oneline -5

# Build + test
go vet ./...
go test -count=1 ./...
just build

# Run the space game (interactive console for tp command)
just run

# Run the 4node mesh example (has visible cells via Vite client at :8080)
cd examples/4node-basic && just dev

# Check pre-fix state to reproduce the original crash
git checkout 4fdf800  # Phase 3 tip, before any border replication work
# or go further back to see the legacy ScanBorderProxies path

# Get back to tip
git checkout feature/tiered-push-replication
```

## What NOT to do

- Do not revert Phase 7.6 (`9581693`) or any earlier refactor commit. The legacy path is gone for a reason.
- Do not spend time on `fd0a473`'s approach without first understanding *why* it broke local entities — that was 90% of a good fix that mysteriously failed at runtime.
- Do not ship a fix without running the actual space game. `go test ./...` is not sufficient; the bug is runtime-integration-specific.
- Do not add new feature code. This is a bugfix-and-ship operation; the branch is otherwise ready to merge.

## If you get stuck

- The original design document is at `docs/superpowers/specs/2026-04-11-tiered-push-replication-design.md`. Section "Client Baseline Continuity" and "Entity Identity" are the most relevant for understanding the replica data model.
- Git history on individual files is very helpful here. `git log -p pkg/universe/world_base.go` in particular will show you what `CreateReplica`/`ApplyReplicasWithRegistry` used to do before the cutover.
- `git show 9581693 -- pkg/universe/replication_scan.go` shows the full deleted legacy scan path including component-serialization format.
- User is available for questions but prefers a complete plan before you start implementing. Summarize your investigation findings and proposed fix before touching code.
