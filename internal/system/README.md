# internal/system

All 10 game systems, executed in order every tick. Each system implements `engine.System` with a single `Update(dt float32)` method and captures `*game.GameWorld` at construction time.

## Execution Order

```
Input → ShipControl → Mining → Economy → Combat → Physics → Lifetime → Spatial → Damage → Network
```

This order matters:
1. **Input** must run first to populate PlayerInput components
2. **ShipControl** converts input into velocity before **Physics** integrates it
3. **Mining/Economy/Combat** process gameplay before position updates
4. **Physics** moves entities
5. **Lifetime** marks expired entities for removal
6. **Spatial** rebuilds the grid (needed by Damage and Network)
7. **Damage** processes collisions from the grid
8. **Network** runs last to serialize the final state of the tick

## Systems

### InputSystem (`input.go`)

Drains protobuf `ClientMessage` from each player's connection buffer.

**For alive players:** Unmarshals input messages and writes to `PlayerInput` component (thrust, turn, fire, mine, sell, target, jettison). Also handles chat messages → `PendingChat`.

**For dead players:** Looks for `ClientMessage_Respawn` → appends connID to `PendingRespawns`.

### ShipControlSystem (`shipcontrol.go`)

Converts `PlayerInput` into physics changes.

- Applies turning: `angle += turn * turnRate * dt`
- Applies thrust in facing direction: `vel += cos/sin(angle) * thrust * dt`
- Clamps speed to `MaxSpeed`
- Applies 0.98 drag when not thrusting

**Query:** `Filter4[PlayerInput, ShipControl, Velocity, Rotation]`

### MiningSystem (`mining.go`)

Handles mining laser activation and cargo jettison.

**Jettison:** If `JettisonResource` is 1-4, zeroes out that resource slot.

**Mining flow:**
1. Resolve target asteroid by NetID → entity lookup
2. Validate: alive, has Minable component, within laser range
3. Calculate available cargo space (`MaxCargo - currentCargo`)
4. Extract: `amount = min(rate*dt, remaining, cargoSpace)`
5. Set `laser.Active = true`, `laser.Target = entity`
6. Mark depleted asteroids for removal

**Query:** `Filter4[PlayerInput, MiningLaser, Position, Inventory]`

### EconomySystem (`economy.go`)

Handles selling cargo at stations and picking up loot crates.

**Selling:** When `input.Sell` is true and player is within `SellRange` of a station:
- Calculates flux earned from cargo × sell prices
- Clears inventory
- Updates PlayerDB flux balance
- Sends `SellResultMsg` to client

**Loot pickup:** Auto-pickup for loot crates within `LootPickupRange`:
- Transfers resources respecting `MaxCargo`
- Marks empty crates for removal

Pre-collects station positions and crate info before iterating players (can't nest queries in Ark).

### CombatSystem (`combat.go`)

Handles weapon firing.

- Decrements `weapon.CooldownLeft` by dt each tick
- If `Fire && CooldownLeft <= 0`: records a deferred fire command (can't spawn entities during query iteration)
- After query: spawns all projectiles via `gw.SpawnProjectile()`

**Query:** `Filter5[PlayerInput, Weapon, Position, Rotation, NetworkID]`

### PhysicsSystem (`physics.go`)

Integrates velocity into position: `pos += vel * dt`

**Query:** `Filter2[Position, Velocity]`

### LifetimeSystem (`lifetime.go`)

Despawns entities whose lifetime has expired.

- Decrements `lifetime.Remaining` by dt
- If `Remaining <= 0`: marks for removal

Used by projectiles and loot crates.

**Query:** `Filter1[Lifetime]`

### SpatialSystem (`spatial.go`)

Rebuilds the spatial hash grid and NetID lookup every tick.

1. `grid.Clear()` — reset all cells
2. Clear `NetIDToEntity` map
3. For each entity with Position + Rotation + Collider + NetworkID:
   - Add to `NetIDToEntity[netID] = entity`
   - Insert into grid with full spatial data (position, shape, size, layer)

**Query:** `Filter4[Position, Rotation, Collider, NetworkID]`

### DamageSystem (`damage.go`)

Processes collisions from the spatial grid and handles shield regeneration.

**Collision matrix:**
```
Player     → collides with Terrain
Projectile → collides with Player | Terrain
```

**Projectile collision:**
1. Skip if hitting own owner
2. Shield absorbs damage first (sets DamageCooldown = RegenDelay)
3. Remaining damage goes to health
4. If health <= 0: `MarkPlayerDeath` for players, `MarkForRemoval` for others
5. Remove the projectile

**Terrain collision (bounce):**
1. Calculate collision normal from center-to-center vector
2. Separate by overlap amount
3. Reflect velocity along normal
4. Dampen by 0.5

**Shield regeneration:**
- If `DamageCooldown > 0`: decrement and skip
- Otherwise: `Current = min(Current + RegenRate*dt, Max)`

### NetworkSystem (`network.go`)

Serializes the visible game state and sends it to each connected player.

**Per-player each tick:**

1. Query `grid.QueryRadius(playerPos, AoIRadius)` for visible entities
2. Build `EntityState` protobuf for each:
   - ID, position, size, velocity, rotation, entity type
   - Health/shield as 0-1 fractions (normalized)
   - Owner ID for projectiles
   - Resource type/remaining for asteroids
   - Mining laser state (active, target ID)
   - Inventory: only for own player and loot crates
   - Pilot name for players; flux only for self
3. Compute removed IDs:
   - Entities visible last tick but not this tick (left AoI)
   - Plus globally removed entities that were in view
4. Send `WorldUpdateMsg` with tick, entities, removed IDs, chat messages

**Visibility tracking:** `lastVisible` map (connID → set of netIDs) persists between ticks. Cleaned up when players disconnect.

Chat messages are cleared after broadcasting to all players.

## Filter Lazy Initialization

All systems use lazy filter initialization (nil check on first `Update`). This is because Ark filters must be created after all component types have been registered with the ECS world, which happens during `NewGameWorld`.

## Entity Spawning During Queries

Ark does not allow spawning or removing entities during query iteration. Systems that need to spawn (CombatSystem) or remove (MiningSystem, LifetimeSystem, DamageSystem) collect the operations in a slice and execute them after the query completes.
