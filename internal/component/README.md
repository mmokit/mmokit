# internal/component

All ECS component types for the space MMO. These are pure data structs with no methods — behavior lives in systems.

## Collision Layers

Collision layer constants live in `pkg/spatial` (`LayerStatic`, `LayerProp`,
`LayerEntity`). The legacy `LayerPlayer = 1` / `LayerTerrain = 2` pair
that used to live here was removed because `LayerPlayer` numerically
collided with `spatial.LayerStatic` (both equal `1`), which caused
`hasLOSOnGrid` to treat every ship/NPC collider as a sight-blocker —
NPCs never aggro'd because the caster's own collider always returned a
hit at the ray origin.

| Layer | Bit value | Members | Blocks |
|-------|-----------|---------|--------|
| `LayerStatic` | `1 << 0 = 1` | Stations, dungeon walls | movement, sight, locks, shots |
| `LayerProp`   | `1 << 1 = 2` | Asteroids | shots only (sight + lock pass through) |
| `LayerEntity` | `1 << 2 = 4` | Ships, NPCs | nothing |

## Entity Kinds

Wire bytes that identify an entity's kind on the replication channel.
Names match the second arg passed to `mmokit.RegisterKind` in
`internal/game/entity_kinds.go`; sdkgen emits a matching TypeScript
const block from the same kind registry.

```text
KindShip, KindAsteroid, KindStation, KindLootCrate, KindNPC
```

## Components

### Transform

| Component | Fields | Description |
|-----------|--------|-------------|
| `Position` | `X, Y float32` | World-space position |
| `Velocity` | `X, Y float32` | Units per second |
| `Rotation` | `Angle float32` | Radians |

### Collision

| Component | Fields | Description |
|-----------|--------|-------------|
| `Collider` | `Radius, Width, Height float32; Layer, Shape uint8` | Circle or OBB. Radius is bounding radius for rects. Shape uses `spatial.ShapeCircle` / `spatial.ShapeRect` |

### Identity

| Component | Fields | Description |
|-----------|--------|-------------|
| `NetworkID` | `ID uint32` | Stable ID sent to clients |
| `EntityKind` | `Type uint8` | Entity type constant |

### Combat

| Component | Fields | Description |
|-----------|--------|-------------|
| `Health` | `Current, Max float32` | Hit points |
| `Shield` | `Current, Max, RegenRate, RegenDelay, DamageCooldown float32` | Shield with delayed regen |

### Ship

| Component | Fields | Description |
|-----------|--------|-------------|
| `ShipControl` | `Thrust, TurnRate, MaxSpeed float32` | Movement parameters |
| `PlayerConn` | `ConnID uint32` | Links entity to network connection |
| `PlayerInput` | `Thrust, Turn float32; Fire, Mine, Sell bool; Sequence uint32; TargetNetID uint32; JettisonResource uint8` | Current frame input |

### Resources

| Component | Fields | Description |
|-----------|--------|-------------|
| `Inventory` | `Resources [4]float32` | Ore, Crystal, Gas, Metal |
| `Minable` | `ItemID uint32; Remaining float32` | Mineable resource (ItemID references item registry) |
| `MiningLaser` | `Range, Rate float32; Active bool; Target ecs.Entity` | Ship mining equipment |

### Lifecycle

| Component | Fields | Description |
|-----------|--------|-------------|
| `Lifetime` | `Remaining float32` | Seconds until despawn |

### Markers

| Component | Fields | Description |
|-----------|--------|-------------|
| `Station` | (empty) | Tags trade station entities |
| `LootCrate` | (empty) | Tags dropped cargo entities |

## Usage with Ark ECS

Components are used with Ark's generic mappers:

```go
// Creation (multi-component)
mapper := ecs.NewMap8[Position, Velocity, ...](ecsWorld)
entity := mapper.NewEntity(&Position{X: 0, Y: 0}, &Velocity{}, ...)

// Access (single-component)
posMap := ecs.NewMap1[Position](ecsWorld)
pos := posMap.Get(entity)

// Query
filter := ecs.NewFilter2[Position, Velocity](ecsWorld)
query := filter.Query()
for query.Next() {
    pos, vel := query.Get()
    pos.X += vel.X * dt
}
```
