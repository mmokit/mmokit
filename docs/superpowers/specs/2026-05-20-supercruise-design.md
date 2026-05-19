# Supercruise Movement

**Date:** 2026-05-20
**Status:** Approved, ready for implementation plan
**Reference:** Albion Online mounting

## Goal

Add a player-toggleable "supercruise" travel mode bound to **Z**, modeled on Albion-style mounting. While active, the player moves significantly faster, but cannot fight, mine, or dock, and is knocked out of supercruise if enough damage lands. Combat involvement (in or out of supercruise) locks the player out of re-entering supercruise for 10 seconds — encouraging real disengage rather than instant kite.

The intent is travel-mode mobility: a clear "I am traversing, not fighting" state with a damage buffer that makes interdiction satisfying for both sides.

## Mechanic Summary

- **Hotkey:** `Z` (currently unbound on the web client).
- **Activation:** press Z → 3-second channel during which the ship is rooted. Any damage during the channel cancels with **no lockout** (so the player can simply retry once safe).
- **Active state:** speed multiplied by `SupercruiseSpeedMul` (default 2.5×). Player cannot fire abilities, mine, or initiate docking — attempting any of these silently cancels supercruise (no lockout).
- **Damage buffer:** at activation, a buffer is initialized to `Health.Max × SupercruiseBufferPct` (default 25%). Damage taken while Active drains the buffer 1:1 with damage applied; ship HP is unaffected until buffer hits 0. When buffer reaches 0, the player is knocked out of supercruise and enters a **10-second lockout**.
- **Unified combat lockout (10s):** any combat involvement starts/refreshes a 10-second lockout that prevents the next Z press. This fires from:
    - Damage taken (any phase: Idle, Channeling, or Active — except a Channeling-interrupt itself, which has no lockout).
    - Damage dealt (attacker also gets stamped).
- **Manual dismount:** pressing Z while Channeling or Active cancels immediately with no lockout. Useful for re-routing without waiting out the channel.

## Architecture

Three layers, mapped to existing engine seams:

| Concern | Where it lives |
|---|---|
| State machine (phase, timers, buffer HP) | New `Supercruise` component + `SupercruiseSystem` |
| Speed multiplier | Existing `StatusEffects` array — new `StatusSupercruise` entry added by the system on `Channeling → Active`, removed on exit |
| Damage interaction | New branch in [internal/game/verb_damage.go](../../../internal/game/verb_damage.go) `ApplyDamage` |
| Input | New `ToggleSuperCruise` typed message via `mmokit.HandleClient`; Z-key bind in [web-pixi/src/input.ts](../../../web-pixi/src/input.ts) |

The system owns transitions; the status effect carries the speed math; the damage hook owns drain + lockout. No new code in `system_ship_dynamics.go` — supercruise speed flows through `EffectiveSpeedMul()` like every other modifier.

## Components

### `Supercruise` (new — [internal/component/components.go](../../../internal/component/components.go))

```go
type SupercruisePhase uint8

const (
    SupercruiseIdle       SupercruisePhase = 0
    SupercruiseChanneling SupercruisePhase = 1
    SupercruiseActive     SupercruisePhase = 2
)

type Supercruise struct {
    Phase            SupercruisePhase
    BufferHP         float32  // remaining damage buffer (Active phase only)
    BufferMax        float32  // snapshot at Channeling→Active transition
    ChannelRemaining float32  // seconds left in channel (Channeling phase only)
    LockoutRemaining float32  // seconds until Z press is accepted again
}
```

Fields use `net:"auto"` quantization tags via the existing `AutoReplicator` path so the full component replicates to viewers. Phase and ChannelRemaining are publicly visible (telegraph + integrity drain); BufferHP/BufferMax are also replicated so attackers see how close they are to dismount — same precedent as Albion mount HP being visible to attackers.

Attached to ship entities at spawn in [internal/game/entity_ship.go](../../../internal/game/entity_ship.go); added to `ShipBundle` so it transfers across cells.

### `StatusSupercruise` (new enum value)

Added to the existing `StatusEffectType` enum in `components.go`. Carries the speed multiplier in `StatusEffect.Value`. Read by `EffectiveSpeedMul()` in [internal/game/system_ship_dynamics.go](../../../internal/game/system_ship_dynamics.go) — no new code in dynamics.

The status effect is added with `Duration = math.MaxFloat32` (effectively infinite) at `Channeling → Active`, and explicitly removed by the system on every exit path. The system, not the duration field, controls the lifetime.

## State Machine

```
                    ┌─────────────────────┐
                    │  Idle               │
                    │  (LockoutRemaining  │◄────────────────┐
                    │   ticks down)       │                 │
                    └──────────┬──────────┘                 │
                               │                            │
                  Z press, Lockout=0, not docked/dead       │
                               │                            │
                               ▼                            │
                    ┌─────────────────────┐                 │
                    │  Channeling         │                 │
                    │  (rooted, 3s)       │                 │
                    │                     │─────────────────┤
                    └──────────┬──────────┘                 │
                               │           damage taken     │
                               │             → lockout 10s  │
                               │           ability/mine/dock│
                               │             → no lockout   │
                               │           Z press          │
                               │             → no lockout   │
                  ChannelRemaining ≤ 0                      │
                               │                            │
                               ▼                            │
                    ┌─────────────────────┐                 │
                    │  Active             │                 │
                    │  (StatusSupercruise │─────────────────┘
                    │   in StatusEffects, │
                    │   BufferHP drains)  │
                    └─────────────────────┘
                          buffer ≤ 0    → lockout 10s
                          damage taken  → lockout 10s, cancel
                          ability/mine/dock → no lockout, cancel
                          Z press       → no lockout, cancel
                          death/disconnect → no lockout, cancel
```

### Transition Table

| From | Trigger | To | Lockout |
|---|---|---|---|
| Idle | Z press (Lockout=0, not docked/dead) | Channeling | — |
| Idle | Z press (Lockout>0 or docked/dead) | Idle | — (ignored) |
| Idle | Damage taken | Idle | **set to 10s** (combat-out-of-supercruise rule) |
| Idle | Damage dealt | Idle | **set to 10s** (attacker rule) |
| Channeling | ChannelRemaining ≤ 0 | Active | — |
| Channeling | Damage taken | Idle | **set to 10s** |
| Channeling | Z press | Idle | — |
| Channeling | Fires ability, mines, docks | Idle | — |
| Channeling | Death / disconnect / undock | Idle | — |
| Active | BufferHP ≤ 0 (drain) | Idle | **set to 10s** |
| Active | Damage taken (buffer>0) | Active | **set to 10s** (lockout stamped, stays Active until buffer empties) |
| Active | Z press | Idle | — |
| Active | Fires ability, mines, docks | Idle | — |
| Active | Death / disconnect / undock | Idle | — |

"Active + damage dealt" is not listed because it cannot occur: an Active player cannot fire abilities (cast auto-cancels), cannot mine, cannot dock, and has no reflection mechanic that would deal damage from a passive state. The attacker-lockout rule applies only to Idle (or Channeling) attackers — typically the more common case where one player ambushes another player who is supercruising.

The "set to 10s" semantics is `LockoutRemaining = max(LockoutRemaining, 10)` — a fresh combat event refreshes the countdown but never shortens an in-flight one.

**Lockout-on-damage-taken applies in every phase**, including Idle. That's the "engaging in combat while not in supercruise also locks you out" rule.

## Damage Hook

In [internal/game/verb_damage.go](../../../internal/game/verb_damage.go) `ApplyDamage(target, dmg, attackerNetID)`, before existing damage application:

```go
sc := mmokit.GetPtr[gamecomp.Supercruise](target)
if sc != nil {
    sc.LockoutRemaining = mathf.Max(sc.LockoutRemaining, cfg.SupercruiseLockoutTime)
    switch sc.Phase {
    case SupercruiseChanneling:
        sc.Phase = SupercruiseIdle
        sc.ChannelRemaining = 0
    case SupercruiseActive:
        sc.BufferHP -= dmg
        if sc.BufferHP <= 0 {
            sc.BufferHP = 0
            sc.Phase = SupercruiseIdle
            removeStatusEffect(target, StatusSupercruise)
        }
    }
}
if attackerNetID != 0 {
    if att, ok := gw.NetIDToEntity[attackerNetID]; ok {
        if asc := mmokit.GetPtr[gamecomp.Supercruise](att); asc != nil {
            asc.LockoutRemaining = mathf.Max(asc.LockoutRemaining, cfg.SupercruiseLockoutTime)
        }
    }
}
// existing ApplyDamage logic continues from here
```

Note: while buffer drains in the Active phase, the player's `Health.Current` is untouched. The buffer is a *replacement* HP pool during Active, not stacked on top. The instant buffer hits 0, the player exits Active and subsequent damage in the same tick (none in practice — `ApplyDamage` is one call per damage event) lands on Health normally.

## Auto-Cancel Sites

Three call sites get a one-line `cancelSupercruise(target)` invocation. The helper lives in `system_supercruise.go`:

```go
func cancelSupercruise(e mmokit.Entity) {
    sc := mmokit.GetPtr[gamecomp.Supercruise](e)
    if sc == nil { return }
    if sc.Phase == SupercruiseActive {
        removeStatusEffect(e, StatusSupercruise)
    }
    sc.Phase = SupercruiseIdle
    sc.ChannelRemaining = 0
    sc.BufferHP = 0
    // LockoutRemaining untouched — auto-cancel is not combat
}
```

Call sites:

1. **Ability cast** — [internal/game/input_handlers.go](../../../internal/game/input_handlers.go) `CastAbility` handler, after successful validation (so a rejected cast doesn't burn supercruise).
2. **Mining laser activation** — [internal/game/system_mining.go](../../../internal/game/system_mining.go), at the point the mining laser starts emitting (not on every tick of an already-active laser).
3. **Docking initiation** — [internal/game/system_docking.go](../../../internal/game/system_docking.go), at the point the dock request is accepted.

`S` key (manual stop) does **not** auto-cancel — you can be stationary while supercruising. This is intentional; supercruise is a movement *capability*, not a movement *command*.

## Input Wiring

### Server: `ToggleSuperCruise` message

New typed message in [internal/game/input_messages.go](../../../internal/game/input_messages.go):
```go
type ToggleSuperCruise struct {
    Sequence uint32 `net:"auto"`
}
```

Handler registered in [internal/game/input_handlers.go](../../../internal/game/input_handlers.go):
```go
mmokit.HandleClient[ToggleSuperCruise](process, func(p *mmokit.PlayerSession, msg *ToggleSuperCruise) {
    e := gw.PlayerEntities[p.ConnID]
    if !e.IsAlive() { return }
    sc := mmokit.GetPtr[gamecomp.Supercruise](e)
    if sc == nil { return }
    switch sc.Phase {
    case SupercruiseIdle:
        if sc.LockoutRemaining > 0 { return }
        if isDocked(e) || isDead(e) { return }
        sc.Phase = SupercruiseChanneling
        sc.ChannelRemaining = cfg.SupercruiseChannelTime
        if mt := mmokit.GetPtr[mmokit.MoveTarget](e); mt != nil {
            mt.Active = false
        }
    case SupercruiseChanneling, SupercruiseActive:
        cancelSupercruise(e)
    }
})
```

### Client: Z-key bind

In [web-pixi/src/input.ts](../../../web-pixi/src/input.ts) keydown handler:
```typescript
if (e.code === "KeyZ" && state.connected && state.client && !state.isDead && !state.isDocked) {
    state.inputSeq++;
    state.client.send(new ToggleSuperCruise({ sequence: state.inputSeq }));
}
```

Client-side display is driven entirely off the replicated `Supercruise` component — no optimistic state. This keeps client/server in sync and avoids speculative speed boosts.

## System Ordering

`SupercruiseSystem` slots into the existing order in [internal/game/factory.go](../../../internal/game/factory.go):

```
Docking → TargetLock → ShipControl → Mining → Economy → Equipment
→ Ability → StatusEffect → Supercruise → Physics → Lifetime → Spatial → Collision → ShieldRegen → Network
```

Placement rationale:
- **After `StatusEffect`** — so the system can read/write `StatusEffects` consistently with that system's tick.
- **Before `Physics`** — so speed multiplier changes apply in the same tick.
- **After `Ability`** — so an ability cast in the same tick has already triggered `cancelSupercruise` via the input handler.

## SupercruiseSystem Tick

Per ship entity with a `Supercruise` component:

```go
sc.LockoutRemaining = mathf.Max(0, sc.LockoutRemaining - dt)

switch sc.Phase {
case SupercruiseChanneling:
    sc.ChannelRemaining -= dt
    // keep player rooted while channeling
    if mt := mmokit.GetPtr[mmokit.MoveTarget](e); mt != nil {
        mt.Active = false
    }
    if sc.ChannelRemaining <= 0 {
        sc.ChannelRemaining = 0
        sc.Phase = SupercruiseActive
        h := mmokit.GetPtr[gamecomp.Health](e)
        sc.BufferMax = h.Max * cfg.SupercruiseBufferPct
        sc.BufferHP = sc.BufferMax
        addStatusEffect(e, StatusEffect{
            Type:     StatusSupercruise,
            Duration: math.MaxFloat32,
            Value:    cfg.SupercruiseSpeedMul,
        })
        gw.Log.Log(CatSupercruise, "active: player=%s buffer=%.1f", username(e), sc.BufferMax)
    }
case SupercruiseActive:
    // transitions out happen in damage hook + cancel sites; nothing to tick here
case SupercruiseIdle:
    // nothing
}
```

Bundles like `mmokit.AddComponent` and `mmokit.RemoveComponent` go through `Commands` per the project's deferred-mutation discipline (no Set inside a query loop).

## Config

New fields on `GameConfig` in [internal/game/config.go](../../../internal/game/config.go) (defaults shown):

```go
SupercruiseSpeedMul    float32 // 2.5
SupercruiseBufferPct   float32 // 0.25
SupercruiseChannelTime float32 // 3.0
SupercruiseLockoutTime float32 // 10.0
```

All four exposed via the existing `config get/set/list` console verbs. Values copied into components at spawn time (`BufferMax`) are not retroactively updated when config changes — per the project's [derived-stat caching](../../../memory/feedback_derived_stat_caching.md) memory, this is intentional; equipment-swap or admin tuning during an active supercruise is an edge case not worth handling. New activations pick up the new values.

## Logging

New category `CatSupercruise` in [internal/game/logcat.go](../../../internal/game/logcat.go). Log lines:

- `channel start: player=%s` — Idle → Channeling
- `channel cancel: player=%s reason=%s` — Channeling → Idle (reason: damage / ability / mine / dock / manual / death)
- `active: player=%s buffer=%.1f` — Channeling → Active
- `buffer drain: player=%s remaining=%.1f damage=%.1f` — damage taken while Active (verbose only)
- `knockout: player=%s` — Active → Idle via buffer drain
- `manual dismount: player=%s phase=%s` — Channeling/Active → Idle via Z press
- `lockout set: player=%s remaining=%.1f trigger=%s` — combat lockout stamped (trigger: damage-taken / damage-dealt / knockout)

Follows the project's [logging memory](../../../memory/feedback_logging.md): every state change logs with player identity and relevant quantities.

## Replication & Wire Format

`Supercruise` component declared via `mmokit.RegisterKind` in [internal/game/entity_kinds.go](../../../internal/game/entity_kinds.go) as a serialized field on the ship bundle. All four fields use `net:"auto"` so the existing `AutoReplicator` ships them to AoI viewers.

Bandwidth estimate (per ship, per tick *only while non-Idle*):
- `Phase` (uint8) + `BufferHP` (f32) + `BufferMax` (f32) + `ChannelRemaining` (f32) + `LockoutRemaining` (f32) = ~17 bytes raw, plus delta-encoder overhead. Delta encoding will collapse most of this to near-zero on idle ticks since the values change predictably.

The replicated Phase field on other players' ships drives client-side visuals: channel radial overlay, supercruise glow/trail effect, integrity bar.

## Client UX

Three visual elements on the local player's HUD:

1. **Channel progress radial** — surrounds the player ship during Channeling. Fills from 0 to 1 over `ChannelRemaining / SupercruiseChannelTime`.
2. **Supercruise integrity bar** — appears while Active. Shows `BufferHP / BufferMax`. Drains visibly on damage taken.
3. **Lockout indicator** — small icon near the Z hint while `LockoutRemaining > 0`, showing remaining seconds. Tells the player why their Z press isn't doing anything.

Other players' ships render only:
- A subtle "channeling" telegraph (e.g. spinning indicator) during Channeling phase — readable by attackers.
- A speed-trail / engine glow during Active phase, so the supercruising player is visually distinct.
- Their integrity bar is **not** shown on the other player's HUD (only on the supercruising player's own HUD), keeping the wire data available for future UI extensions but not visually cluttering the screen.

(The replicated BufferHP is available on the wire for any future tactical HUD that wants to surface "you almost dismounted them" — out of scope for v1.)

## Edge Cases

| Scenario | Behavior |
|---|---|
| Player undocks while LockoutRemaining > 0 | Lockout persists (ship/Supercruise component is fresh on undock — lockout starts at 0). |
| Player docks while Active | Docking triggers `cancelSupercruise` (auto-cancel site). |
| Player dies while Active | `cancelSupercruise` from death PreFlush hook. Respawn ship has fresh Supercruise (Idle, Lockout=0). |
| Player crosses cell boundary while Active | `Supercruise` field is in `ShipBundle` without the `mmokit:"local"` tag, so it serializes in cell transfer — Phase, BufferHP, ChannelRemaining, LockoutRemaining all persist across boundary. |
| Two damage events on same tick (Active, buffer=10, dmg=15 + dmg=5) | First damage drains buffer to 0, exits Active. Second damage lands on Health normally. Sequential `ApplyDamage` calls — natural ordering. |
| Player takes damage while Idle (not in supercruise) | LockoutRemaining set to 10s. No other state change. |
| Player takes damage at the same moment ChannelRemaining ticks to 0 | Damage is processed first by `ApplyDamage` (which runs from system updates), which cancels the channel. The system's transition to Active never fires. |

## Testing

### Unit tests (`pkg/system/` or `internal/game/`)
- `TestSupercruise_ChannelCompletesToActive` — tick 3s, expect Active.
- `TestSupercruise_ChannelInterruptedByDamage` — apply damage during channel, expect Idle + 10s lockout.
- `TestSupercruise_BufferDrainKnockout` — set Active, apply damage equal to BufferMax, expect Idle + 10s lockout + Health unchanged.
- `TestSupercruise_AbilityCancels` — Active, simulate ability cast, expect Idle + no lockout.
- `TestSupercruise_AttackerLockedOut` — attacker entity has Supercruise; deals damage; expect LockoutRemaining = 10s on attacker.
- `TestSupercruise_LockoutBlocksZ` — Idle with LockoutRemaining=5; Z input ignored.
- `TestSupercruise_ManualCancelNoLockout` — Active, Z press, expect Idle + no lockout.
- `TestSupercruise_StatusEffectAppliedAndRemoved` — Active applies StatusSupercruise; cancel removes it.

### Integration smoke (manual)
- Start `just dev`. Log in. Press Z, observe 3s channel + speed boost.
- Spawn a hostile NPC near the player; activate supercruise and let the NPC chip the buffer to 0; confirm knockout + lockout indicator appears.
- After knockout, immediately press Z; confirm no response until lockout expires.
- Cast an ability while Active; confirm immediate cancel with no lockout.
- Take damage while Idle (not in supercruise); confirm lockout stamps; confirm Z is blocked for 10s.

## Out of Scope

- **Tiered supercruise / mounts** — single global speed multiplier; no per-ship variants.
- **Energy / fuel cost** — supercruise is free to use; gated only by the combat lockout and damage buffer.
- **Visual effects polish** — engine trail, screen distortion, screen-shake on knockout. Implementation will ship with minimal visuals (channel radial, integrity bar, simple glow); polish is a follow-up.
- **Sound design** — supercruise spin-up / knockout audio cues. Follow-up.
- **NPC supercruise** — NPCs do not have a `Supercruise` component in v1. AI-driven supercruise (e.g. fleeing NPCs) is a possible future extension; the component supports it but no system writes to NPCs.
- **PvP-zone variants** — no zone-aware modifiers (e.g. "supercruise disabled in this region"). If needed later, a `SupercruiseDisabled` flag on the cell or POI could be added without protocol changes.

## Files Touched

**New:**
- `internal/game/system_supercruise.go` (~120 lines: system + helper)

**Modified:**
- `internal/component/components.go` — `Supercruise` struct + `SupercruisePhase` enum + `StatusSupercruise` enum value
- `internal/game/entity_ship.go` — add `Supercruise` to `ShipBundle`
- `internal/game/entity_kinds.go` — register `Supercruise` field on the ship kind
- `internal/game/factory.go` — register `SupercruiseSystem` in the system list
- `internal/game/input_messages.go` — `ToggleSuperCruise` message
- `internal/game/input_handlers.go` — handler registration + ability auto-cancel
- `internal/game/verb_damage.go` — damage hook (buffer drain + lockout stamping)
- `internal/game/system_mining.go` — auto-cancel on mining start
- `internal/game/system_docking.go` — auto-cancel on dock initiation
- `internal/game/config.go` — four `Supercruise*` fields
- `internal/game/logcat.go` — `CatSupercruise` category
- `web-pixi/src/input.ts` — Z keybind
- `web-pixi/src/` rendering glue — channel radial, integrity bar, glow effect (exact file depends on existing HUD architecture; implementer follows the same pattern used by other status-effect overlays)

SDK regenerates automatically via `just build`.

## References

- [Albion Online mounting](https://wiki.albiononline.com/wiki/Mounts) — design inspiration
- [internal/game/system_ship_dynamics.go](../../../internal/game/system_ship_dynamics.go) — `EffectiveSpeedMul()` is the speed-multiplier seam
- [internal/game/verb_damage.go](../../../internal/game/verb_damage.go) — `ApplyDamage` is the canonical damage entry point
- 2026-04-28-player-input-api-design.md — input handler registration pattern
