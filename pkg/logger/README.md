# pkg/logger

Category-based debug logging. Each log message belongs to a category that can be toggled on/off at runtime via the server console.

## Usage

```go
gameLog := logger.New(logger.CatConnect, logger.CatSpawn, logger.CatCombat)

gameLog.Log(logger.CatCombat, "hit: attacker=%d target=%d damage=%.1f", atkID, tgtID, dmg)
```

If the category is disabled, `Log` is a no-op (just a map lookup + RLock).

## Categories

| Constant | Category | What it logs |
|----------|----------|-------------|
| `CatCombat` | `combat` | Projectile hits, damage dealt |
| `CatKill` | `kill` | Entity destroyed |
| `CatMining` | `mining` | Mining start/stop, resource gained |
| `CatConnect` | `connect` | Player connect/disconnect |
| `CatSpawn` | `spawn` | Entity spawn/despawn |
| `CatCollision` | `collision` | Terrain collisions (bounce) |
| `CatInput` | `input` | Player input received |
| `CatPhysics` | `physics` | Physics events |
| `CatEconomy` | `economy` | Sell, loot pickup |
| `CatChat` | `chat` | Player chat messages |

`AllCategories` is a slice of all category strings, used by the console for iteration and help display.

## Thread Safety

All methods are safe for concurrent use. `Enable`/`Disable` use a write lock; `IsEnabled` and `Log` use a read lock. The logger is shared between the game loop goroutine (which calls `Log`) and the main goroutine (which runs the console and toggles categories).

## Console Integration

The engine console provides these commands for runtime control:

- `status` / `s` — show which categories are on/off
- `on <cat|all>` — enable category
- `off <cat|all>` — disable category
- `toggle <cat>` / `t <cat>` — toggle category
- `only <cat> [...]` — enable only these, disable rest
- `<cat>` — shortcut to toggle (any unrecognized command is tried as a category name)

Categories are matched by prefix, so `com` matches `combat`, `con` matches `connect`, etc.
