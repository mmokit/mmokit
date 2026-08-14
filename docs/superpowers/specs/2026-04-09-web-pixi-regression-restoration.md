# Context for Planning: Restore web-pixi Regressions from SDK Migration

## What just happened

The web-pixi client was migrated from hand-coded transport/decoder/protocol to the auto-generated `SpaceClient` SDK at `web-pixi/sdk/`. This landed on branch `feature/web-pixi-sdk-modernization` (6 commits, not yet merged to main). The migration plan is at `docs/superpowers/plans/` — see "Web-Pixi Modernization — Adopt Generated SDK" (file `./.claude/plans/validated-sparking-pike.md`).

**The SDK is working. Two-player visibility works. Core gameplay (move, login, spawn, damage, mining) functions.** But 5 visual/UX features were intentionally dropped during the Phase 2 wire-protocol simplification. The user wants them restored.

## Background: why each feature was dropped

Phase 2 of the earlier architecture refactor simplified the wire protocol to only replicate components that have `net:"..."` struct tags. Anything that was previously serialized via hand-coded `nethandler_*.go` files but doesn't now have a `net:"..."` tag was dropped. Specifically:

- `lockedBy` fields (who is locking this entity) — was computed from a reverse lock map in `NetworkSystem.beforeTick`, embedded into every entity's snapshot base fields.
- Ship mining flags + mining target netID — was read from `MiningLaser` component, hand-encoded into ship snapshots.
- Loot crate inventory contents — was variable-length tail in `nethandler_lootcrate.go`, manually encoded.
- Status effects (ion burn, fortified, afterburner) — read from `StatusEffects` component, hand-encoded.

All of these now need a **new mechanism** to get the data to the client. The plan should decide mechanism per regression.

## The 5 regressions to restore

### 1. Status effect visuals on all entities

**Current state:** `web-pixi/src/effects/ability-effects.ts` has a no-op `drawStatusEffects` method with a comment:
> Disabled after Phase 2: StatusEffects component is no longer replicated per-entity on the wire (no net tags). Restoring requires either adding net tags to the StatusEffects component or extending PlayerOwnStateMsg with the local player's active effects.

**What it does when enabled:** draws ion-burn overlay, fortified shield bubble, or afterburner trails on entities that have those effects active.

**Server component:** `internal/component/components.go` → `StatusEffects` struct has a fixed-size array of `StatusEffect { Type, Duration, Value, Source }`. `Source` is an `ecs.Entity` reference (local-only, gets zeroed on transfer via `WithPreMarshal` hook in `internal/game/entity_kinds.go`). The component has NO net tags.

**Options (pick one, or hybrid):**
- **(a) Add per-entity net-tag replication**: put `net:"u8"` on `StatusEffects.Count`, and net tags on `Effects[0..N].Type` and `Effects[0..N].Duration`. Fixed-size array replication needs extending the reflection binding in `pkg/system/auto_replicator.go` to handle arrays/slices (currently it only handles flat scalar struct fields). Bandwidth: ~24 bytes per entity with effects (4 effects × 6 bytes).
- **(b) Extend `PlayerOwnStateMsg`** (proto `gamepb.PlayerOwnStateMsg`) with a `status_effects[]` repeated field — only broadcasts the local player's effects. Other players don't show status VFX. Simple, zero wire format churn for other entities.
- **(c) New `SE_STATUS_EFFECTS_CHANGED` event**: when an entity's status effects change, broadcast a small event with `{netID, effects[]}` to viewers in AoI. More complex but efficient (only sends on change, not every tick).

**Recommendation:** Option **(a)** with a new reflection capability for fixed-size arrays. It's the most architecturally clean — keeps replication uniform and doesn't bifurcate "local" vs "remote" rendering paths. Requires extending the auto_replicator (~100 LOC) but unlocks future array-valued components (AbilitySet cooldowns, Inventory if we wanted it back, etc.).

### 2. Loot crate inventory preview

**Current state:**
- `web-pixi/src/effects/target-highlight.ts` — loot crate target preview now shows just "LOOT CRATE" label, no item list.
- `web-pixi/src/ui/loot-popup.ts` — popup shows "Contents unknown" placeholder; only the LOOT ALL button works. Per-item loot buttons are disabled.

**Server component:** `internal/component/components.go` → `Inventory { Items map[uint32]int32, MaxMass float32 }`. Maps don't fit the AutoReplicator model.

**Why dropped:** `nethandler_lootcrate.go` had a variable-length tail encoding (`uint16 byteLen + uint8 count + [uint32 itemId + uint32 qty] * count`) that AutoReplicator doesn't support.

**Options:**
- **(a) Per-tick replication via custom ComponentBinding**: write an `InventoryBinding` in Go that produces a variable-length field. `pkg/system/schema.go` already has a `VarTailSchema` hook but sdkgen needs codegen support for decoding it. The old hand-coded decoder had this logic — port it to sdkgen as a code-generated template. Medium effort (~200 LOC across pkg/system + cmd/sdkgen).
- **(b) On-demand fetch**: new `GCE_INSPECT_CRATE` client event → server replies with `GSE_CRATE_CONTENTS { crateNetId, items[] }` protobuf. Client sends when opening the popup or targeting a crate. Cleaner architecture (moves from broadcast to explicit request), but changes UX slightly — there's a latency moment before contents appear.
- **(c) Marketplace-style operation**: request/response on channel 0x01 (`OP_INSPECT_CRATE` with `MarketBrowse`-like pattern). Client gets typed `Promise<CrateContents>`. Cleanest for single-target queries.

**Recommendation:** Option **(c)** — adds a new operation, uses the already-extended sdkgen typed operation codegen. Low risk, clean async API, and sets a pattern for other "query entity details" needs (inspect another player's equipment, etc.). ~150 LOC: proto definition + server handler + schema.go registration + client call.

### 3. Being-locked indicators for non-local players

**Current state:** `web-pixi/src/effects/being-locked-ring.ts` renders the ring ONLY around the local player entity (using `state.beingLockedById` / `state.beingLockedProgress` from `PlayerOwnStateMsg`). Non-local players no longer show "someone is locking them" rings.

**Previous behavior:** every entity's wire snapshot included `lockedByID (uint32)` + `lockedByProgress (uint8 qnorm)` as part of the base fields. Rendered as a ring around any targeted entity.

**Server logic:** `NetworkSystem.beforeTick` builds `ctx.lockedBy map[ecs.Entity]lockerInfo{netID, progress}` — the reverse lock map computed from all entities' `TargetLock` components. The old `hashBaseFields`/`snapshotBaseFields` in the deleted `nethandler_shared.go` read from this context map to emit the 2 fields.

**Options:**
- **(a) New component `LockedBy { NetID uint32, Progress float32 }`** with `net:"u32"`/`net:"qnorm"` tags. `NetworkSystem.beforeTick` populates it from the reverse lock map each tick. Client renders a ring on any entity with non-zero `LockedBy.NetID`. Clean — reuses the existing component pipeline.
- **(b) Custom `LockedByBinding`** that reads from the beforeTick context via a `GameWorld` accessor. Avoids the component overhead but is more invasive.
- **(c) Accept the regression**: local player still sees who is locking them via PlayerOwnState. Visual "red ring" on other players being locked is mostly nice-to-have PvP flavor.

**Recommendation:** Option **(a)**. Write `NetworkSystem.beforeTick` to clear/populate a `LockedBy` component on each entity currently being locked. Component has `net:"..."` tags → AutoReplicator handles it → client renders it. ~50 LOC total.

### 4. Other players' mining laser VFX

**Current state:** `web-pixi/src/effects/mining-laser.ts` only renders the local player's beam, derived from `state.targetId` (the client's locally-targeted asteroid). Other players' mining beams don't render at all.

**Previous behavior:** ship snapshots included `flags (u8 bitmask: beam0/beam1 active)` + `miningTargetId (u32)`. Client rendered a beam from ship to target asteroid whenever active.

**Server component:** `MiningLaser { Beams [2]MiningBeamState, Target ecs.Entity }` — LocalOnly after Phase 2. `Target` is an entity reference (not serializable).

**Options:**
- **(a) New component `MiningVisual { Beam0Active bool, Beam1Active bool, TargetNetID uint32 }`** with net tags. Updated by the MiningSystem each tick when beams activate/deactivate. Replaces the old bit packing with an explicit component.
- **(b) Extend `MiningLaser` with net tags** on the Beams active flags + a separate `TargetNetID uint32` field (since Entity isn't serializable). Remove the LocalOnly designation so it gets replicated. The only snag: `Beams` is a `[2]MiningBeamState` array — reflection binding needs array support (same as status effects Option 1a).
- **(c) Accept the regression**: PvE games barely notice; PvP mining contests are rare.

**Recommendation:** Option **(a)** — simplest and keeps MiningLaser as local-only game state. The new `MiningVisual` component is 6 bytes/ship. ~60 LOC.

### 5. Per-ability mining beam mask highlight on ability bar

**Current state:** `web-pixi/src/ui/ability-bar.ts` has `isMiningBeamActive` returning `false` always:
```typescript
// Mining beam active state is no longer replicated per-entity after Phase 2
// (the MiningLaser component is LocalOnly). The local player's active beam
// mask could be tracked client-side if we want per-slot highlighting back.
function isMiningBeamActive(_state: GameState, _slot: number): boolean {
  return false;
}
```

**Previous behavior:** ship entity had a `miningBeamMask: number` field (bit 0 = weapon1 beam, bit 1 = weapon2 beam). Ability bar slots 1 and 3 (W and R keys, secondary abilities) lit up while their beam was active.

**Options:**
- **(a) Restore from same source as (4)**: once `MiningVisual` (Option 4a) is replicated, ability-bar reads the local player's `MiningVisual.Beam0Active`/`Beam1Active` flags.
- **(b) Extend `PlayerOwnStateMsg`** with mining beam state for the local player only. Client reads directly from own state without needing per-entity replication.
- **(c) Track client-side**: when the client presses W or R (secondary mining abilities), track locally that the beam should be active until a cooldown or range-out. Less accurate but no server changes.

**Recommendation:** Follow regression 4's choice. If 4a is chosen, this is free (read the same component). If 4c is chosen, then 5c for consistency.

## Architectural patterns to consider

### Array-valued components

Options 1a and 4b both need reflection support for fixed-size Go arrays in `pkg/system/auto_replicator.go`. The current `reflectBinding` only handles flat scalar struct fields. Adding array support would unlock:
- StatusEffects (4x StatusEffect structs)
- AbilitySet.Cooldowns ([6]float32)
- MiningLaser.Beams ([2]MiningBeamState)
- Equipment weapon slots (4x uint32)

The pattern in the sdkgen delta-decoder core (`applyDelta` with fieldSizes) already supports fixed-length binary fields, so the wire side works. Need:
1. `parseNetTag` to recognize array syntax (e.g. `net:"array,4,u8"`)
2. `reflectBinding.fields` emits N wire fields per array element
3. Schema export lists all N fields with collision-resolved names (`effects0Type`, `effects0Duration`, etc.)
4. sdkgen delta-decoder handles the flat sequence

~150-200 LOC in `pkg/system/auto_replicator.go` + minor sdkgen tweaks.

### The `nextTick` populator pattern

Options 3a (LockedBy component) and 4a (MiningVisual component) both need a system hook that writes to a "visual state" component each tick from other sources (reverse lock map, mining system state). This is a common pattern. Consider adding a `PreReplicationSystem` interface:

```go
type VisualStatePopulator interface {
    PopulateVisualState()
}
```

...or just do it in existing system `beforeTick` hooks.

## Critical files to read for a planning session

**Server side:**
- `pkg/system/auto_replicator.go` — ComponentBinding interface, reflectBinding, qSizeBinding as reference for custom bindings
- `pkg/system/schema.go` — BindingSchema, EntitySchema, VarTailSchema (scaffolding exists for variable tails)
- `pkg/universe/entity_kind.go` — KindComponent / KindComponentLocalOnly
- `pkg/mmokit/protocol.go` — ServerEvent, Operation registration
- `internal/game/entity_kinds.go` — current EntityKindDefs (Ship, Asteroid, etc.)
- `internal/game/system_network.go` — NetworkSystem, beforeTick reverse lock map
- `internal/game/system_mining.go` — mining system (for Option 4 MiningVisual)
- `internal/game/system_statuseffect.go` — status effect system
- `internal/component/components.go` — all component definitions (Health, Shield, Inventory, MiningLaser, StatusEffects, TargetLock, etc.)
- `cmd/server/schema.go` — --dump-schema registration (add new events/ops here)
- `cmd/sdkgen/generate.go` — reference for how bindings/events get codegen'd

**Client side:**
- `web-pixi/sdk/entities.ts` — generated flat entity types (Ship, Asteroid, NPC, etc.)
- `web-pixi/sdk/client.ts` — generated SpaceClient with sendXxx/onXxx methods
- `web-pixi/src/network.ts` — SDK wiring, entity update pipeline
- `web-pixi/src/effects/mining-laser.ts` — regression 4 target
- `web-pixi/src/effects/being-locked-ring.ts` — regression 3 target
- `web-pixi/src/effects/ability-effects.ts` (drawStatusEffects) — regression 1 target
- `web-pixi/src/effects/target-highlight.ts` — regression 2 (crate preview)
- `web-pixi/src/ui/loot-popup.ts` — regression 2 (popup contents)
- `web-pixi/src/ui/ability-bar.ts` (isMiningBeamActive) — regression 5 target

## Relevant skills

- `superpowers:brainstorming` — for design discussion before planning (the scope is small enough that this might be overkill; you could go straight to writing-plans if the user has clear requirements)
- `superpowers:writing-plans` — for the implementation plan
- `superpowers:subagent-driven-development` or `executing-plans` — for execution

## Key questions to settle before planning

1. **Array-valued component reflection** — do we add it to auto_replicator (unlocks options 1a, 4b, and future use cases), or use simpler per-regression workarounds (PlayerOwnState extensions, explicit visual components)?
2. **Scope** — all 5 regressions, or priority subset? Regressions 1 (status effects) and 2 (loot inventory) are the most user-visible; 3/4/5 are PvP flavor that's nice-to-have.
3. **One PR or phased** — similar to the SDK migration, this is one focused effort. Probably one PR with all 5 fixes.
4. **Acceptable UX changes** — for regression 2, is on-demand fetch (options b/c) acceptable even though it adds a latency beat before crate contents appear? Or must it feel instant (option a)?

## Current state of the branch

Branch `feature/web-pixi-sdk-modernization` has 6 commits, NOT yet merged to main. Full verification has passed (tsc, vite build, go vet, go test, all builds). Ready to merge or build on. The user can either:
- Merge the migration branch to main first, then open a new branch for regressions
- Stack the regression work on top of the migration branch
- Discard the migration if the regressions are blocking

## Memory context

- User prefers `bun` for JS/TS (not npm)
- No backward compatibility — no re-exports, update callers directly
- Refactor over stopgaps — prioritize proper refactors, deprecate legacy code
- Don't quantize positions (Float32 only); quantize velocity/rotation/sizes
- No git worktrees for sequential work
- Proto field cleanup: renumber from 1 on changes, don't reserve
- All new server-side game logic needs category-based debug logging (`gw.Log.Log(CatX, ...)`)

## Handoff: what the next agent should do

1. Read this file end-to-end.
2. Optionally skim `docs/superpowers/plans/validated-sparking-pike.md` (the migration plan) for migration-era context.
3. Start with the brainstorming skill or go straight to writing-plans depending on the user's appetite.
4. Ask the user the "Key questions to settle" above.
5. Produce an implementation plan at `docs/superpowers/plans/2026-04-09-web-pixi-regression-restoration.md`.
6. Execute via subagent-driven-development or executing-plans.
