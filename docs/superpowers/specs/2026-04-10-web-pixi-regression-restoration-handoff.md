# Handoff: Finishing the web-pixi Regression Restoration Branch

**Date:** 2026-04-10
**Branch:** `feature/web-pixi-sdk-modernization` (16 new commits stacked on the SDK migration)
**Original spec:** [2026-04-10-web-pixi-regression-restoration-design.md](./2026-04-10-web-pixi-regression-restoration-design.md)
**Original plan:** [../plans/2026-04-10-web-pixi-regression-restoration.md](../plans/2026-04-10-web-pixi-regression-restoration.md)

## Status summary

All six regression tasks from the original plan are **implemented and compiled**:

| Task | Commit | What it does |
|---|---|---|
| 1 | `aef356c` | `VarTailComponent` binding + `KindComponentWithBinding` wrapper in `pkg/system` / `pkg/mmokit` |
| 2 | `773c859` | sdkgen typed var-tail decoder + `ReplicatorRegistry.Schema()` determinism fix |
| 3 | `5b9eb90` | `LockedBy` component — being-locked ring on non-local entities |
| 4 | `67f6ce7` | `ActiveMining` component — other players' mining beams + ability bar highlight |
| 5 | `bb0977b` | `StatusEffects` var-tail — effect VFX on all entities |
| 6 | `e1ff6ff` | Loot crate `Inventory` var-tail — preview + popup contents |

**Post-review fixes:**

| Commit | Issue |
|---|---|
| `1ea1028` | Status effect hash quantization asymmetry + loot popup signature-based refresh |
| `01e1280` | **sdkgen was emitting `-1` var-tail marker in the client `FIELD_SIZES` array**, which corrupted `applyDelta`'s `fixedSize`/`totalLogicalFields` math. Latent sdkgen bug surfaced by Task 5 being the first var-tail caller in the space SDK. |

**Bugs found and fixed during live playtesting** (all pre-existing, unrelated to regression work, surfaced by the new paths being exercised):

| Commit | Root cause |
|---|---|
| `abe806c` | Client was overwriting local player's decoded `worldX`/`worldY` with `update.viewerX`/`viewerY` from the frame header. The header carries the viewer's **cell-local** position but the entity's `worldX` is **world-absolute** — an ~8000-unit mismatch that tripped the rebase detection loop and produced runaway position drift. Leftover from an earlier quantized-position era. |
| `6eb87f1` | `handlePlayerInput` stored world-absolute `msg.MoveX`/`MoveY` directly into `MoveTarget.X`/`Y` (which are cell-local) and stamped the player's current cell as the destination cell. Click-to-move targets were always ~8192 units out of the cell, so the ship never reached them. Fixed by using `mmokit.SetMoveTarget()` which does the correct conversion. |
| `49c7a99` then `7880e4a` | Replaced `ShipControlSystem` with `ClickToMoveSystem` + `ShipDynamicsSystem`, then realized `ClickToMoveSystem` is arcade-style and loses drag/turn-rate/inertia. Restored the full thrust-based physics inside `ShipDynamicsSystem` and dropped the generic `ClickToMoveSystem`. |
| `1f1ae37` | **Ship spawn never added a `Rotation` component.** `ShipDynamicsSystem`'s query requires `Rot *mmokit.Rotation` and silently excluded the player entity. The old `ShipControlSystem` had the same bug but was hidden by the client-side `viewerX/Y` override — users thought movement worked because the client masked the symptom. Fixed by passing `mmokit.WithRotation(0)` in `entity_ship.go`. |
| `56e32c0` | Debug cleanup + `ShipTurnRate` lowered from `6.0` → `3.0 rad/s` for a more natural feel when starting a turn from rest. |

**Direction-vector input mode removed entirely** (per user request): `PlayerInput` struct no longer has `Dir{X,Y,Active}`, `PlayerInputMsg` proto fields `dir_x`/`dir_y`/`dir_active` deleted (renumbered 1-7, no reserved), client `sendInput` no longer sends them, docking system no longer clears them. The client still has unused `moveMode`/`dirTarget` state and UI (see "Dead code" section below).

## Verification status

**Green:**
- `go vet ./...` clean
- `just build` clean
- `bun run build` in `web-pixi/` clean
- `just space-sdk` idempotent (no diff on re-run)
- Task 1-4 unit tests (`TestVarTailComponent_*`) pass
- Basic manual smoke tests on live server: spawn, move via right-click, thrust/drag/turn-rate feel correct

**Not yet verified live (original plan steps 7.4-7.8):**
- Status effect VFX on both local and remote entities
- Being-locked ring on a third-party observer watching two other players lock each other
- Other players' mining beams rendering from a third-party view
- Ability-bar mining beam highlight activating on local beam toggle
- Loot crate preview + popup showing item list, popup refreshing after partial loot
- Cross-node transfer of entities carrying the new components (`LockedBy`, `ActiveMining`, `StatusEffects` var-tail, `Inventory` var-tail, plus `Rotation` now auto-added to ships)

**Known pre-existing test failures** (present on the branch base commit `d37acfc`, not caused by this work):

- `TestFinishTransferSpawn_Ship` — expects `Shield.Max=0`, and expects `PlayerInput`, `MiningLaser`, `ShipControl`, `TargetLock` to be present after transfer-side spawn
- `TestFinishTransferSpawn_LootCrate` — expects the `LootCrate` marker component to be present after transfer-side spawn

These tests likely broke during an earlier refactor of the transfer-side spawn path. They are NOT within scope of this work but should be addressed as a separate fixup.

## What's left to do

### 1. Manual live verification of all five regressions

Run the server + two browser windows and walk through the manual checklist from Task 7 of the original plan. The most code-sensitive paths are:

1. **Status effects var-tail on all entities** — hit yourself with ion burn, verify the purple overlay appears on both your own ship and observed from a second browser window. Same for fortified and afterburner.
2. **Being-locked ring for non-local entities** — have Player A target-lock Player B; a third observer must see the red/orange ring around Player B's ship.
3. **Other players' mining beams** — have Player A toggle on a mining beam on an asteroid; Player B (in AoI) must see the green beam drawn between Player A's ship and the asteroid.
4. **Ability-bar mining highlight** — on the local player, slots 1 and 3 (W/R keys — secondary mining abilities) should highlight while their respective beams are active.
5. **Loot crate preview + popup** — target a crate, sublabel should show the item list inline; open the popup, per-item buttons should appear; after a partial loot, the popup should refresh to show the updated contents.
6. **Cross-node transfer** — fly across a cell boundary. All five regressions above should continue working on the receiving node. Especially verify `StatusEffects` transfer: the pre-marshal hook zeroes the `Source ecs.Entity` before serialization and the binding re-applies after.

If a regression fails live, the original plan's per-task sections have enough detail to debug.

### 2. Dead client code cleanup (direction-vector mode leftovers)

The server- and proto-side removal is complete. The client still carries the full direction-mode plumbing, which is now dead weight:

- [web-pixi/src/input.ts](../../web-pixi/src/input.ts) lines 50, 199-200, 227-228 — `state.moveMode === 'direction'` toggle and dirTarget handling
- [web-pixi/src/state.ts](../../web-pixi/src/state.ts) lines 100-101, 228-229 — `moveMode` and `dirTarget` fields
- [web-pixi/src/ui/hud.ts](../../web-pixi/src/ui/hud.ts) lines 21, 400-407 — `moveModeEl` and the move-mode indicator rendering
- [web-pixi/src/main.ts](../../web-pixi/src/main.ts) lines 161-176 — the `moveMode === 'direction'` branch in the right-click loop

All of this should be deleted. The hud mode indicator should either be removed or repurposed.

### 3. Client state that PlayerOwnStateMsg still populates but nothing reads

- `state.beingLockedById` / `state.beingLockedProgress` — Task 3 moved the ring rendering to per-entity replication. The plan noted these fields might still be used by HUD alarm / audio cue. **Verify whether anything still reads them**; if not, remove from `GameState`, `createInitialState`, and the `network.ts` `onPlayerOwnState` handler.

### 4. Pre-existing `TestFinishTransferSpawn_*` failures

- `internal/game/transfer_test.go` — two tests that expect certain components to be present after cross-node transfer-side spawn. Failing on `d37acfc` and on all downstream commits (confirmed via `git checkout HEAD~N -- transfer_test.go` bisect). Likely broken by an earlier spawn-path refactor. Should be a standalone fix branch.

### 5. Unit test coverage for the new var-tail bindings

From the final code review:

- No round-trip tests for `NewStatusEffectsBinding` (qnorm scaling of duration, hash asymmetry correctness after the post-review fix)
- No round-trip tests for `NewInventoryBinding` (sort stability, u32 signedness correctness after the post-review fix)

A ~20-line test per binding that constructs a fake component with known values, calls `Snapshot` + hashes it, and verifies byte layout would close the gap. Not blocking for merge.

### 6. Known minor items deferred from final code review

All from the final-review report; all non-blocking:

- **M2** `STATUS_SHIELD_REGEN` constant defined in `ability-effects.ts` but never cased in `drawStatusEffects`. Either add a rendering case or remove the constant so a future reader isn't confused.
- **M4** Generated var-tail item type names are verbose: `ShipEntityStatusEffectsItem`, `LootCrateEntityItemsItem`. `cmd/sdkgen/generate.go:tailItemTypeName` uses `entityName + titleCase(VarTail.Name) + "Item"`. Stripping the `Entity` suffix would give `ShipStatusEffectsItem` / `LootCrateItemsItem` which is what the original plan envisioned.
- **M6** Duplicated `ActiveMining` sync logic between `system_mining.go` (end of per-entity loop) and `system_ability.go` (after the mining-beam toggle branch). Could extract to a helper `gw.syncActiveMining(entity, laser)` in `world.go` or `system_util.go`.
- **writeFieldDecoderIndented** in `cmd/sdkgen/generate.go` silently drops the `string` encoding case (same gap as `writeFieldDecoder`). No current caller puts a string in a var-tail item, but a future one would produce broken TypeScript silently. Add a default case that panics at codegen time, or document why it's intentional.
- **Array.prototype modernization** — a few `go vet` / `staticcheck` hints surface across the branch:
  - `internal/game/system_network.go:177,178,192` — `for loop can be modernized using range over int`
  - `system_ability.go:67` — same
  - `entity_ship.go:44` — `Replace m[k]=v loop with maps.Copy`
  - `config.go:196` — `reflect.TypeOf call can be simplified using TypeFor`
  None of these are regressions from this work; they're pre-existing hints that the toolchain now surfaces. Fix as part of a separate lint-cleanup pass if desired.

## Architectural improvements identified during the work

### A1. No guarantee that entity kinds carry the components their systems need

The root cause of the "can't move at all" bug in `1f1ae37` was `ShipDynamicsSystem.entities` querying for `Rot *mmokit.Rotation` while the ship entity kind never had Rotation registered. `EnsureEntityKindComponents` only auto-adds components registered via `KindComponent` on the kind def, and `Rotation` was neither in that list nor passed as a spawn option. The query silently returned empty and the symptom was hidden by the client-side `viewerX/Y` override (see `abe806c`).

**Option A:** Make `Rotation` part of the default core components that `SpawnEntity` always adds (alongside `Position`, `Velocity`, `NetworkID`, `CellCoord`, `Collider`, `EntityKind`), instead of being opt-in via `WithRotation`. Every entity that moves wants rotation; making it opt-in was a footgun.

**Option B:** Add a startup check that validates every registered system's query requirements against the components registered on each entity kind def. If a system's query would filter out all entities it's intended to process (because none of them carry all the required components), log a warning or panic. This is the more general fix.

**Option C:** Leave as-is and document the foot-gun. Lowest effort but the bug will recur.

Recommendation: **Option A** — rotation is cheap (12 bytes per entity), always present in our model, and assuming it exists removes an entire class of silent-query-exclusion bugs. Asteroids already spawn with `WithRotation(rand.Float32()*2*math.Pi)`; ships do now; stations have no rotation but also no movement systems. NPCs don't move either. So making it universal has zero runtime cost and catches future systems that assume rotation exists.

### A2. Client wire format assumed positions were quantized

The `update.viewerX/Y` override in `network.ts` (removed in `abe806c`) carried a comment explicitly citing "~0.37-unit steps that cause visible camera jitter". That suggests positions used to be quantized to a uint16 `QPos`, and a full-precision override in the frame header was how the camera avoided jitter. Positions are now `ViewerRelativePos` with full f32+f32 — no quantization — so the override is dead weight. It was also encoding a cell-local value as if it were world-absolute.

**Suggested cleanup:** Review the entire `pkg/quantize` wire format types for other "quantized position era" leftovers. `FrameHeader.ViewerX`/`ViewerY` are still in the binary layout. Consider whether they're still needed at all, or if they can be removed from the header.

### A3. Rendering of `viewerX/viewerY` header bytes

Related to A2 — even with the client override removed, the server still writes `frame.Viewer.X`/`Y` (cell-local) into the frame header. Nothing currently reads them on the client. Either remove the fields or populate them with something useful (e.g. the viewer's world-absolute position so future code has a convenient baseline).

### A4. `ShipControl.Thrust` / `TurnRate` ambiguity

`ShipControl.Thrust` and `ShipControl.TurnRate` are per-entity component fields, but their values are globally driven by `gw.Config.ShipThrust` / `ShipTurnRate`. Equipment can modify `Thrust` via `ApplyEquipmentStats`. NPCs technically could have `ShipControl` but don't. Consider whether these belong as per-entity state or as a per-ship-class config lookup. Not urgent, just ambiguous.

### A5. `input_handlers.go` logs every tick regardless of state change

Line ~63: `gw.eng.Log.Log(CatPlayerInput, "player=%d abilities=0x%x lock=%d seq=%d", ...)` — this fires on every input message received, even when nothing interesting happened. At 20Hz per player, that's 1200 log lines per player per minute. The `CatCombatLock` logging I added in Task 3 only fires on state transitions; `CatPlayerInput` should do the same (log on `abilityCast != 0` or lock target change).

### A6. `MoveTarget.X/Y` type signature hides the cell-local contract

The `MoveTarget` component stores `X, Y float32` (cell-local) and `CellX, CellY int32` (the cell the target is in). Whether a caller treats `X/Y` as world-absolute or cell-local is context-dependent and easy to get wrong — exactly the `6eb87f1` bug. Consider:

- Renaming the fields to `LocalX, LocalY` to make the contract visible at every call site
- Or adding a typed wrapper (`type CellLocal struct { X, Y float32; Cell CellID }`)
- Or exposing only `SetMoveTarget(worldX, worldY)` as the write API and making the struct fields unexported

Lowest effort: rename fields to `LocalX/LocalY`. Higher impact: make the write API world-absolute only.

### A7. Two pre-existing `TestFinishTransferSpawn_*` failures

Not caused by this work but a blocker for green `go test ./...`. Tests expect `Shield.Max=0` (seemingly expecting component zero-init rather than config-populated) and several components (`PlayerInput`, `MiningLaser`, `ShipControl`, `TargetLock`, `LootCrate`) to be present after transfer-side spawn. Fix the test expectations or fix the transfer-spawn path. Separate branch.

## Pointers for the incoming agent

- **Original spec:** [2026-04-10-web-pixi-regression-restoration-design.md](./2026-04-10-web-pixi-regression-restoration-design.md) — design decisions, per-regression mapping, out-of-scope rationale.
- **Original plan:** [../plans/2026-04-10-web-pixi-regression-restoration.md](../plans/2026-04-10-web-pixi-regression-restoration.md) — step-by-step implementation plan with exact code for each task. Task 7 has the manual smoke test checklist that's still outstanding.
- **Full branch commit history:** `git log --oneline main..HEAD` on `feature/web-pixi-sdk-modernization`.
- **User preferences (from memory):** Use `bun` not `npm`, no backward compat shims, `just build` not `go build ./...`, don't quantize positions, don't use worktrees for sequential work, prioritize proper refactors over stopgaps, always add `gw.eng.Log.Log(CatX, ...)` for new server-side state writes.

## Suggested order for finishing touches

1. **Manual live verification** (section "What's left to do" #1) — blocking for merge confidence.
2. **Dead client code cleanup** (#2 and #3) — small, mechanical, clears a ~200 LOC delta.
3. **A1 Rotation auto-inclusion** — small engine change, prevents a recurrence of the `1f1ae37` bug class.
4. **Pre-existing test failures** (#4 / A7) — out of scope for this branch but blocks `go test ./...` green.
5. **Unit tests for var-tail bindings** (#5) — nice-to-have, small.
6. **Minor code review items** (#6) — polish, can ship without.
7. **A2/A3/A5 cleanups** — defer to a separate refactor branch.
