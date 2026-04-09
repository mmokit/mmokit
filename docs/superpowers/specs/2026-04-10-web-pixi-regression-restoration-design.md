# Web-Pixi Regression Restoration — Design

**Date:** 2026-04-10
**Branch:** `feature/web-pixi-sdk-modernization` (stack on top)
**Predecessor:** [2026-04-09-web-pixi-regression-restoration.md](2026-04-09-web-pixi-regression-restoration.md) (planning context)

## Problem

Phase 2 of the earlier architecture refactor simplified the wire protocol to only replicate components with `net:"..."` struct tags. Five features previously encoded via hand-coded `nethandler_*.go` files were dropped during that cut. The web-pixi SDK migration validated that the new pipeline works, and the user now wants these features back:

1. **Status effect VFX on all entities** (ion burn, fortified, afterburner, shield regen) — `drawStatusEffects` is a no-op.
2. **Loot crate inventory preview + popup contents** — target highlight shows only a label; popup shows "Contents unknown".
3. **Being-locked indicator ring for non-local players** — only the local player sees who is locking them.
4. **Other players' mining laser VFX** — only the local player's beam renders.
5. **Ability-bar mining beam highlight** — `isMiningBeamActive` returns `false` unconditionally.

## Guiding principles

- **Replicate game state, not visuals.** The server sends the facts; the client derives VFX from those facts. No component or field in this work carries "Visual" in its name.
- **Reuse existing infrastructure where it already works end-to-end.** `VarTail` encoding is fully wired in `pkg/quantize/delta.go`, `pkg/quantize/ts/delta-decoder-core.ts`, `cmd/sdkgen/generate.go`, and Slither uses it in production — the only gap is a `ComponentBinding` that *produces* a var-tail and a typed sdkgen decoder for the item format.
- **Don't invent array-valued reflection.** Fixed-capacity Go arrays with an explicit count field (e.g. `StatusEffects`) are *semantically* variable-length — they should be replicated as var-tails, not as padded fixed-size wire fields. Genuinely fixed cases (e.g. two mining beam slots) should be flat scalar fields.

## Engine additions

One new capability. Everything else reuses existing scalar reflection.

### Var-tail ComponentBinding + sdkgen typed decoder

**`pkg/system/` — new binding**
- A new `ComponentBinding` that writes `uint8 count + count * itemBytes` to the snapshot. Construction takes a component accessor, a count accessor, and an item writer (closures). Keeps the binding generic — one implementation serves `StatusEffects`, `Inventory`, and any future list-shaped component.
- The binding populates `EntitySchema.VarTail = &VarTailSchema{ItemSize, ItemFields}` using the existing type in [pkg/system/schema.go:30](../../pkg/system/schema.go#L30).
- An entity's binding list may contain at most one var-tail binding (placed last). The binding itself advertises zero per-tick scalar fields so the fixed-size prefix layout stays clean; the var-tail bytes follow.

**`cmd/sdkgen/generate.go` — typed tail decoder**
- When `EntitySchema.VarTail != nil`, emit a decoder that reads `count` then iterates, producing a typed array on the generated flat entity type. For example: `statusEffects: Array<{ type: number; duration: number }>` or `items: Array<{ itemId: number; qty: number }>`.
- The existing `HAS_VAR_TAIL` flag dispatcher continues to work — only the *decoded representation* on the entity type is new.

**Wire format and delta encoding**
- No changes. `DeltaEncoder.hasVarTail` already handles variable tails. `applyDelta` in the TS core already reconstructs them.

**Estimated scope:** ~150 LOC in `pkg/system/`, ~80 LOC in `cmd/sdkgen/`, plus unit tests for the binding and a codegen golden file update.

## Per-regression mapping

| # | Regression | Mechanism | Components |
|---|---|---|---|
| 1 | Status effect VFX on all entities | Var-tail binding on existing `StatusEffects` | No struct change; binding serializes `StatusEffects.Effects[0..Count]` as `u8 type + qnorm duration` per item |
| 2 | Loot crate inventory preview + popup | Var-tail binding on existing `Inventory` | No struct change; binding serializes map entries as `u32 itemId + u32 qty` per item |
| 3 | Being-locked ring on non-local players | New `LockedBy { NetID uint32, Progress float32 }` with `net:"u32"`/`net:"qnorm"` tags; populated each tick by `NetworkSystem.beforeTick` from the existing reverse-lock map | New component |
| 4 | Other players' mining beams | New `ActiveMining { Beam0Active bool, Beam1Active bool, TargetNetID uint32 }` with scalar net tags; `MiningSystem` writes it each tick | New component; `MiningLaser` stays local-only game state |
| 5 | Ability-bar mining highlight | Local player reads `ActiveMining` from its own entity snapshot | Free — no new code beyond wiring the read |

### Why `ActiveMining` is a separate component (not net tags on `MiningLaser`)

`MiningLaser` contains `Target ecs.Entity` (not serializable across nodes), `Beams [2]MiningBeamState` (local timers/cooldowns/heat), and other local-only fields. Replicating it directly would either require lifting the `LocalOnly` marker and excluding most of its fields, or adding array reflection — both worse than a lean sibling component that contains exactly the game state the client needs to render.

### Why status effects don't need a separate component

`StatusEffects` is already the game-state component. Its Go representation is a fixed-capacity `[4]StatusEffect` pool with an explicit `Count uint8`, but *semantically* it's a variable 0-4 list. The var-tail binding reads `comp.Count` and serializes `comp.Effects[0..Count]`, which matches the semantics exactly. `StatusEffect.Value` and `StatusEffect.Source` stay local-only; only `Type` and `Duration` cross the wire.

## Server-side work (`internal/game/`)

- **`entity_kinds.go`**: register the new bindings on each entity kind.
  - `ship`: add `StatusEffects` (var-tail) + `ActiveMining` (scalars) + `LockedBy` (scalars).
  - `asteroid`: add `LockedBy` only. Asteroids can be locked/mined but don't carry status effects or mining state in game logic.
  - `lootcrate`: add `Inventory` (var-tail). Loot crates are not target-lockable.
  - `npc`: add `StatusEffects` (var-tail) + `ActiveMining` (scalars, if NPCs mine) + `LockedBy` (scalars). The implementation plan verifies which NPCs actually use `MiningLaser` before adding `ActiveMining`.
  - `station`: no new bindings — stations are static and don't carry any of the replicated state.
- **`system_network.go`**: `NetworkSystem.beforeTick` walks the existing reverse lock map and writes `LockedBy` onto each victim entity. Clears before populating to avoid stale state.
- **`system_mining.go`**: `MiningSystem` writes `ActiveMining` when beams start, stop, or change target; clears when both beams go inactive.
- **Logging**: all new server-side writes include category-based debug logging via `gw.Log.Log(game.CatCombat / CatMining / ...)`, per the project's logging convention.

## Client-side work (`web-pixi/src/`)

- **SDK regeneration** picks up the new fields on each entity type automatically.
- **`network.ts`**: wire the new generated fields into `GameState`. Extend the per-entity update path to copy `statusEffects`, `items`, `lockedByNetId`, `lockedByProgress`, `beam0Active`, `beam1Active`, `miningTargetNetId`.
- **`effects/ability-effects.ts`**: restore `drawStatusEffects` to read `entity.statusEffects` and render the existing VFX per effect type.
- **`effects/being-locked-ring.ts`**: render ring for any entity with `lockedByNetId != 0`, not only the local player. Keep the local-player path as a fallback for the own-state message, but the generic path takes precedence.
- **`effects/mining-laser.ts`**: render beams for any ship with `beam0Active || beam1Active` active, drawing from the ship to the entity at `miningTargetNetId`.
- **`effects/target-highlight.ts`**: restore loot crate preview using the decoded `items` array.
- **`ui/loot-popup.ts`**: restore per-item buttons using `items`; remove the "Contents unknown" placeholder.
- **`ui/ability-bar.ts`**: `isMiningBeamActive(state, slot)` reads `state.ownEntity.beam0Active` / `beam1Active`.

## Sequencing

Order minimizes rework. Each step is independently verifiable.

1. **Engine — var-tail `ComponentBinding` + unit tests.** No game code touches this yet; test in isolation with a synthetic binding.
2. **Engine — sdkgen typed decoder.** Extend `cmd/sdkgen/generate.go` to emit typed item decoders when `VarTail != nil`. Golden-file update for `web-pixi/sdk/`.
3. **Regression 3 — `LockedBy` component.** Simplest game-side change; uses only existing scalar reflection. Validates the full server-to-client pipeline on a trivial case. Client-side `being-locked-ring.ts` update lands in the same step.
4. **Regressions 4 + 5 — `ActiveMining` component.** Still scalar reflection; validates `MiningSystem` write hooks. Ability-bar highlight falls out for free. Client-side `mining-laser.ts` + `ability-bar.ts` updates land in the same step.
5. **Regression 1 — status effects var-tail.** First consumer of the new var-tail binding; exercises the typed sdkgen decoder. Client-side `ability-effects.ts` update.
6. **Regression 2 — loot crate inventory var-tail.** Second consumer; cements the pattern. Client-side `loot-popup.ts` + `target-highlight.ts` updates.
7. **Verification pass**: `go vet ./...`, `just build`, `bun run build` in `web-pixi/`, two-player manual test exercising each of the five regressions.

## Branch strategy

Stack on `feature/web-pixi-sdk-modernization` (chosen: option (i) from brainstorming). The migration and regression fixes ship together as one package. The branch is already verified end-to-end and the two workstreams share natural context.

## Out of scope

- Array-valued struct field reflection — considered and rejected; fixed-cap pools with explicit counts should be var-tails, genuinely fixed tuples should be named scalar fields.
- On-demand `OP_INSPECT_CRATE` operation — considered and rejected; target-highlight tooltip needs instant data and delta-encoded var-tail is cheaper than the round-trip.
- Replicating full `MiningLaser` state (timers, cooldowns, heat) — client doesn't need it.
- Replicating `StatusEffect.Value` or `.Source` — client doesn't need effect magnitudes or source references to render VFX.

## Success criteria

- All five regressions restored; manual two-player session confirms:
  - Status effect VFX visible on both players and their targets.
  - Loot crate target highlight shows item list; popup shows per-item buttons; LOOT ALL still works.
  - Red lock ring appears around any entity being locked (observed from a third-party view).
  - Mining beams render for other players.
  - Ability-bar slots 1 and 3 highlight while their beam is active on the local player.
- `go vet ./...`, `just build`, `bun run build` in `web-pixi/` all clean.
- Unit tests pass for the new var-tail binding.
- sdkgen golden file updated and matches expected decoded types.
- No regressions in existing wire format for entities not touched by this work.
