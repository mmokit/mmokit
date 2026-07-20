# `pkg/logger`

Thread-safe, category-based debug logging with hierarchical groups, regular
expression filters, and synchronous hooks.

Categories conventionally use `group:subcategory`, such as `mesh:transfer`.
Registering a category also registers the text before `:` as a group.
Enabling or disabling a known group applies the change to all registered
categories in that group.

## Usage

```go
gameLog := logger.New("combat:hit") // enabled, but not yet registered
gameLog.RegisterCategories(
    "combat:hit",
    "combat:ability",
    "mesh:transfer",
)

gameLog.Enable("combat")
gameLog.Log("combat:hit", "attacker=%d target=%d", attackerID, targetID)
```

Keep the calls to `New` and `RegisterCategories` distinct: `New` enables its
arguments, while `RegisterCategories` populates discovery, group expansion,
console status, and completion.

## API

| Method | Purpose |
| --- | --- |
| `New(enabled ...string)` | Construct a logger with the supplied names enabled. |
| `RegisterCategories(cats ...string)` | Register known categories and derive groups. |
| `Categories()` / `Groups()` | Return snapshots of registered names. |
| `CategoriesInGroup(group)` | Return registered `group:*` categories. |
| `Enable(cats ...string)` | Enable categories or expand known groups; unknown categories are registered. |
| `Disable(cats ...string)` | Disable categories or expand known groups. |
| `IsEnabled(cat)` | Report whether an exact category is enabled. |
| `EnableFromFlag(csv)` | Disable registered categories, then enable comma-separated categories/groups. |
| `Log(cat, format, args...)` | Format and emit an enabled, filter-matching message. |
| `SetFilter(cat, regexp, source)` | Install a filter on one category or a known group. |
| `ClearFilter(cats...)` | Clear selected filters, or all filters when called without arguments. |
| `Filters()` | Return a category-to-source-pattern snapshot. |
| `AddHook(h)` / `RemoveHook(h)` | Attach or detach a log sink. |

`SetFilter` receives a compiled `*regexp.Regexp`; the `source` string is kept
for status display. Filters are applied after the enabled check.

## Hooks

A hook receives each line that passes both category and filter checks:

```go
type Hook interface {
    Emit(category, message string, at time.Time)
}
```

Hooks run synchronously in `Log`. They must not block; use a bounded channel
and a separate consumer for I/O or other substantial work.

## Console integration

`pkg/engine` exposes logger controls through typed commands:

- `log status`
- `log on <category|group|all>`
- `log off <category|group|all>`
- `log toggle <category>`
- `log only <category|group> ...`
- `log filter [category pattern | clear [category]]`

The console resolver accepts exact categories, exact groups, and the first
registered category matching a prefix.
