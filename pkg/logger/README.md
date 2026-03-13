# pkg/logger

Category-based debug logging with dynamic category registration. Each log message belongs to a category that can be toggled on/off at runtime via the server console. The logger is fully generic — categories are registered by the game layer, not hardcoded.

## Usage

```go
// Create logger with initial categories enabled
gameLog := logger.New("connect", "spawn", "combat")

// Register all known categories (for console tab-completion and help)
gameLog.RegisterCategories("connect", "spawn", "combat", "mining", "economy", ...)

// Log a message (no-op if category is disabled)
gameLog.Log("combat", "hit: attacker=%d target=%d damage=%.1f", atkID, tgtID, dmg)
```

Game-specific category constants live in the game layer (e.g. `internal/game/logcat.go`), not in this package.

## API

| Method | Description |
|--------|-------------|
| `New(enabled ...string)` | Create a logger with the given categories enabled |
| `RegisterCategories(cats ...string)` | Add categories to the known set (deduplicates) |
| `Categories() []string` | Returns a copy of all registered categories |
| `Enable(cats ...string)` | Turn on logging for categories (auto-registers unknown ones) |
| `Disable(cats ...string)` | Turn off logging for categories |
| `IsEnabled(cat) bool` | Check if a category is active |
| `Log(cat, format, args...)` | Log a message if category is enabled |

## Thread Safety

All methods are safe for concurrent use. `Enable`/`Disable`/`RegisterCategories` use a write lock; `IsEnabled`, `Log`, and `Categories` use a read lock. The logger is shared between the game loop goroutine (which calls `Log`) and the main goroutine (which runs the console and toggles categories).

## Console Integration

The engine console provides these commands for runtime control:

- `status` / `s` — show which categories are on/off
- `on <cat|all>` — enable category
- `off <cat|all>` — disable category
- `toggle <cat>` / `t <cat>` — toggle category
- `only <cat> [...]` — enable only these, disable rest
- `<cat>` — shortcut to toggle (any unrecognized command is tried as a category name)

Categories are matched by prefix, so `com` matches `combat`, `con` matches `connect`, etc.
