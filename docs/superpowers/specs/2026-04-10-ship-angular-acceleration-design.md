# Ship Angular Acceleration

## Problem

Ship turning in `ShipDynamicsSystem` uses a hard per-tick clamp:

```go
// internal/game/system_ship_dynamics.go
maxTurn := ship.TurnRate * dt
turnStep := angleDiff
if turnStep > maxTurn { turnStep = maxTurn }
else if turnStep < -maxTurn { turnStep = -maxTurn }
rot.Angle += turnStep
```

Angular velocity is effectively binary: `0` when facing the target,
`±TurnRate` otherwise. No ramp-up when starting a turn, no ramp-down when
stopping. Players describe it as "turret-like" rather than ship-like and the
resulting path is visibly kinked instead of a smooth curved arc.

## Goal

Make ships feel like they have rotational inertia: angular velocity
accelerates smoothly from rest, peaks at a max, then decelerates to come to
rest on the target heading. Because linear thrust is applied along the
current heading while the heading is still rotating, the ship naturally
traces a curved arc entering and exiting a turn — matching the "arc
acceleration" feel the user asked for.

## Scope

- `ShipDynamicsSystem` only (player ships and any ship entity that uses
  `ShipControl` + `MoveTarget`).
- `WanderSystem` (NPCs) is **out of scope**. NPCs use a different
  wander-heading model and their constant-rate turn is fine for dumb AI.
  Flagged as possible future work.
- No proto changes. `AngularVel` is server-only transient state; clients
  continue to receive only `rot.Angle` via existing replication.

## Design

### New state

**`internal/component/components.go` — `ShipControl`**

```go
type ShipControl struct {
    Thrust     float32
    TurnRate   float32 // max angular velocity, rad/sec (unchanged meaning)
    TurnAccel  float32 // angular acceleration, rad/sec^2 (NEW)
    MaxSpeed   float32
    AngularVel float32 // current angular velocity, rad/sec (NEW, runtime)
}
```

`TurnRate` keeps its existing meaning as the angular velocity **cap**.
`TurnAccel` is the new acceleration knob. `AngularVel` is per-entity runtime
state.

**`internal/game/config.go` — `GameConfig`**

```go
ShipTurnAccel float32 `json:"shipTurnAccel"` // rad/sec^2, NEW
```

Default: `8.0` rad/s². With `ShipTurnRate = 4.0`, this gives `4.0/8.0 = 0.5s`
from rest to max angular velocity — "heavy fighter" feel, easy to dial.

Bump `ConfigVersion` from `2` → `3` so existing saved configs reload defaults.

### Seeding AngularVel / TurnAccel

`ShipTurnAccel` is copied into `ShipControl.TurnAccel` anywhere the other
ship config values are seeded:

- `internal/game/entity_ship.go` `SpawnShip` — when the `ShipControl`
  component is initialized
- `internal/game/world.go:328` — equipment-stats reapply path
  (`sc.TurnRate = gw.Config.ShipTurnRate`)
- `internal/game/commands.go:802` — any admin spawn command that builds a
  `ShipControl` literal

`AngularVel` defaults to `0` (zero value), no explicit init needed.

### Control loop — `ShipDynamicsSystem`

Replace the current clamp block ([system_ship_dynamics.go:102-112][sd]) with
angular-accel integration. Pseudocode:

```go
const angEps = 0.001 // rad and rad/s snap threshold

targetAngle := atan2(dy, dx)
angleDiff   := normalizeAngle(targetAngle - rot.Angle)
angVel      := ship.AngularVel
turnAccel   := ship.TurnAccel
maxOmega    := ship.TurnRate

// Braking distance at current angular velocity (rad).
brakeDist := (angVel*angVel) / (2*turnAccel)

switch {
case abs(angleDiff) < angEps && abs(angVel) < angEps:
    // Arrived. Snap to exact target, kill residual angVel.
    rot.Angle = targetAngle
    angVel    = 0

case sign(angVel) != 0 && sign(angVel) != sign(angleDiff):
    // Moving the wrong way — brake toward zero, then next tick we'll
    // accelerate in the correct direction.
    angVel -= sign(angVel) * turnAccel * dt

case abs(angleDiff) <= brakeDist:
    // Close enough that we must start braking now to stop on target.
    angVel -= sign(angVel) * turnAccel * dt

default:
    // Accelerate toward the target.
    angVel += sign(angleDiff) * turnAccel * dt
}

// Clamp to max angular velocity.
if angVel >  maxOmega { angVel =  maxOmega }
if angVel < -maxOmega { angVel = -maxOmega }

// Integrate.
rot.Angle += angVel * dt
ship.AngularVel = angVel
```

**When `MoveTarget` is inactive**: decelerate `AngularVel` toward zero at
`turnAccel * dt` per tick (symmetric with active braking). Done inside the
existing `if !mt.Active { continue }` branch before the `continue`:

```go
if !mt.Active {
    if ship.AngularVel != 0 {
        step := ship.TurnAccel * dt
        if abs(ship.AngularVel) <= step {
            ship.AngularVel = 0
        } else {
            ship.AngularVel -= sign(ship.AngularVel) * step
        }
    }
    continue
}
```

### Why this produces an arc path

Linear thrust in the existing code is applied along `rot.Angle` each tick:

```go
vel.X += cos(rot.Angle) * thrustMag
vel.Y += sin(rot.Angle) * thrustMag
```

While `AngularVel` ramps `0 → max`, the ship is already moving forward with
its previous velocity vector, and the new heading differs only slightly from
the old one. The divergence between heading and velocity vector grows
gradually, producing a smooth curved entry into the turn. On exit, the same
thing happens in reverse: the ship decelerates its rotation while continuing
to thrust along the now-rotating heading. The result is a visibly arced path
instead of today's straight-line kinks.

## Testing

New test file: `internal/game/system_ship_dynamics_test.go`.

Set up a minimal `GameWorld` (reuse helpers from `testutil_test.go` if
available) with one ship entity, an active `MoveTarget`, and run the system
at fixed `dt = 0.05` (20 Hz). Cases:

1. **Ramp-up from rest**: start at `angle=0`, target far off to the side
   (e.g. `+pi/2`). After `TurnRate/TurnAccel` seconds, `AngularVel` should
   equal `TurnRate` within epsilon.
2. **Clean stop on target**: start with a target at `+pi/4` and enough
   simulated ticks; after convergence, `|rot.Angle - target| < angEps` and
   `|AngularVel| < angEps` — no overshoot oscillation.
3. **Direction reversal**: while turning right at max ω, flip the
   `MoveTarget` to the opposite side; `AngularVel` must decelerate through
   zero and then accelerate negative.
4. **Inactive target bleed-off**: preset `AngularVel = TurnRate`, clear
   `MoveTarget.Active`, run; after `TurnRate/TurnAccel` seconds, `AngularVel`
   is zero.
5. **Small angle**: target within `angEps` — ship snaps to target, no NaN.

Existing `internal/game` tests must stay green.

## Verification

- `just build` (or `go vet ./...`) — must pass
- `go test ./internal/game/...` — new tests + existing tests green
- Manual: `just dev`, click around in `web-pixi`, confirm the ship curves
  into and out of turns rather than rotating in place while drifting. Then
  hit the server console: `config set ShipTurnAccel 2` (sluggish),
  `config set ShipTurnAccel 20` (snappy) — feel the range.

## Tuning knobs

Exposed live via the existing `config` console command:

| Field           | Default  | Meaning                                   |
| --------------- | -------- | ----------------------------------------- |
| `ShipTurnRate`  | `4.0`    | Max angular velocity, rad/s               |
| `ShipTurnAccel` | `8.0`    | Angular acceleration, rad/s²              |

Time from rest to max ω: `TurnRate / TurnAccel` seconds (0.5s at defaults).
Peak brake distance at max ω: `TurnRate² / (2·TurnAccel)` rad (1.0 rad ≈
57° at defaults).

## Risks / reversibility

- **Blast radius**: one component field, one config field, one system
  block. Fully local.
- **Backward compat**: none needed — no proto changes, no persisted state
  with `AngularVel`, `ConfigVersion` bump forces defaults to reload.
- **Reversibility**: revert the commit. Zero data migrations.

## Out of scope / follow-ups

- `WanderSystem` NPC turning (would want different tuning anyway).
- Per-ship-class `TurnAccel` override via equipment (easy to bolt on once
  the equipment stat plumbing supports it — currently only `Thrust` is
  equipment-modifiable).
- Heading-prediction smoothing on the web client. Clients already
  interpolate `rot.Angle` between ticks, so smoother server rotation should
  "just work" visually without client changes.

[sd]: ../../../internal/game/system_ship_dynamics.go
