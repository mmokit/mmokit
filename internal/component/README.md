# internal/component

All ECS component types for the space MMO. These are pure data structs with no methods — behavior lives in systems.

## Collision Layers

Bitmask constants for filtering which entities can collide:

```text
LayerPlayer     = 1
LayerTerrain    = 2
LayerProjectile = 4
```

The collision matrix (defined in DamageSystem) determines interactions:

- Players collide with Terrain (bounce)
- Projectiles collide with Players and Terrain (damage/remove)

## Entity Types

Derived from protobuf enums so client and server agree on values:

```text
TypeShip, TypeAsteroid, TypeProjectile, TypeStation, TypeLootCrate
```

## Resource Types

Also from protobuf enums:

```text
ResourceOre (0), ResourceCrystal (1), ResourceGas (2), ResourceMetal (3)
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
| `Weapon` | `Damage, FireRate, Speed, CooldownLeft float32` | Ship weapon stats |
| `Projectile` | `Damage float32` | Marks entity as projectile |
| `Owner` | `Entity ecs.Entity` | Links projectile to its creator |

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
| `Minable` | `ResourceType uint8; Remaining float32` | Mineable asteroid |
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
