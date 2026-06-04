# Runtime Tunables — design

**Date:** 2026-06-05
**Status:** Approved (brainstorming), pending implementation plan
**Branch:** `wasm-hot-systems`

## Problem

System code is littered with hand-tuned constants — `amplitude`, `freqHz`,
`spread` in the `wave` wasm demo; dozens of balance knobs in the space game.
Changing one means an edit + rebuild + restart (native) or edit + `just
wasm-build` + `wasm swap` (wasm). We want operators to tweak these values
**live** from the console and the admin dashboard, with the change taking effect
on the next tick, for **both native Go systems and hot-swappable wasm modules**,
through one uniform authoring convention and one operator surface.

This replaces the existing runtime-config method (the global `GameConfig`
reflection-based `config get/set/list/save/reset` command group). That surface
operates on a single global struct; tunables are per-system and carry bounds
metadata for slider UIs. The old runtime-config command surface is **removed**
in this work (not migrated). `tune` is built to be its successor: eventually the
global `GameConfig` knobs decompose into per-system tunables, but that migration
is out of scope here.

## Core principle

A tunable lives in **exactly one place**: a tagged field on the system. Every
reference reads that field live. `set` mutates that one field on the cell's loop
goroutine, between ticks. That *is* the reactive mechanism — there are no change
events, no observers, no re-wiring. One writer (the loop), one source of truth
(the field). Native systems read the field directly; the wasm guest reads its
struct field each tick after the host has pushed the new value across the
linear-memory boundary.

## Authoring convention — one tag, both worlds

A value becomes tunable by turning a `const` into an **exported scalar field**
with a `tune:` tag. Unexported fields remain private internal state and are never
touched. The tag grammar is identical for native and wasm authors.

**Native Go system:**

```go
type FieldSystem struct {
    mmokit.SystemBase
    Baseline float32 `tune:"default=0,min=-200,max=200,step=5"`
    // unexported fields stay private state
}

func (s *FieldSystem) Update(dt float32) { /* ... uses s.Baseline ... */ }
```

**Wasm module (`GOOS=wasip1`):**

```go
type wave struct {
    Amplitude float32 `tune:"default=220,min=60,max=420,step=10"`
    FreqHz    float32 `tune:"default=0.6,min=0.1,max=3,step=0.1"`
    Spread    float32 `tune:"default=0.012,min=0,max=0.05,step=0.001"`
    phase     float32 // unexported = internal state, untouched
}

func (w *wave) Update(ctx *wasmsys.Ctx, dt float32) { /* ... uses w.Amplitude ... */ }
```

### Tag grammar (`pkg/tunable`)

`tune:"default=<v>,min=<v>,max=<v>,step=<v>"`. All keys optional except that an
untagged exported scalar field is **not** a tunable (the tag is the opt-in
marker — only `tune:`-tagged fields are exposed). `min`/`max`/`step` are
metadata for the admin slider UI and for host-side range validation on `set`.
Supported kinds: `int`, `int32`, `int64`, `uint`, `uint32`, `uint64`,
`float32`, `float64`, `bool`. (Strings deferred — they need a length-prefixed
wasm encoding; rare for tuning knobs.)

The grammar parser, the `Kind` enum, the `Def` descriptor, and the
reflection-backed `Source` live in a new **zero-dependency `pkg/tunable`**
package that compiles for both native and `wasip1`. Host and guest import the
same parser, so the tag is interpreted identically on both sides.

## Host architecture — one interface, two providers

```go
// pkg/tunable
type Kind uint8 // Int, Uint, Float, Bool

type Def struct {
    Name    string
    Kind    Kind
    Default string
    Min     string // "" if unset
    Max     string // "" if unset
    Step    string // "" if unset
    Value   string // current value
}

// Source is the host-side contract for anything exposing tunables.
type Source interface {
    Tunables() []Def            // descriptors + current values
    Set(name, value string) error
}
```

Resolution — `tunableSourceFor(sys System) (tunable.Source, bool)`:

1. `sys` implements `tunable.Source` → use it directly. **The wasm adapter
   (`wasmSystem[T]`) implements `Source`**, hiding the linear-memory bridge.
2. Otherwise reflect `sys` for `tune:`-tagged exported scalar fields. If any
   exist, wrap the live struct pointer in a reflection-backed `Source`
   (`tunable.Reflect(ptr)`, generalizing the logic currently in
   `engine.ReflectConfig`). **Native authors implement nothing.**
3. Otherwise the system has no tunables.

### Per-process registry — the source of truth

A per-`*universe.Process` **tunable registry** holds, per
`(systemName, fieldName)`, the descriptor (kind/default/min/max/step) and the
**intended current value**. This is the single source of truth that the admin
UI reads and that newly-created cells inherit.

- **Descriptor harvest.** Native systems' descriptors are harvested by
  reflecting the system struct at `AddSystem` time. Wasm descriptors are
  harvested from the guest schema the first time a cell instance is wired (cells
  always exist at boot, so the registry is populated before any operator query).
- **`set` flow.** `tune set` (1) validates against the descriptor's min/max,
  (2) writes the registry value, then (3) pushes to every matching live cell
  instance — native via field reflection, wasm via the params ABI.
- **New cells.** A cell born from a split applies the registry's current values
  at `Init`, so it inherits live tweaks, not stale tag defaults.

Per-cell/per-node filtering (`--cell`/`--node`) is a console power-user path: a
filtered `set` pushes only to the matching live cells and does **not** rewrite
the registry, so it is an ephemeral local override. The admin UI always operates
on the registry (global) value.

## The wasm bridge — the only boundary-crossing part

The guest's tagged fields live in wasm linear memory; the host cannot poke a Go
variable. Three encodings were considered:

- **Direct memory poke** — host writes into the guest's struct address. Fast but
  couples the host to the guest's exact field layout/alignment and breaks the
  arena-copy isolation philosophy the rest of the ABI follows.
- **Typed-packed block** — each field serialized at its natural size; host walks
  the schema computing per-kind offsets. Compact but fiddly.
- **Uniform `float64`-per-field block (chosen).** Every tunable serializes to
  one `float64`; the block is a `[]float64` in guest-declared field order.
  Trivial host serialization, no per-kind offset math, and `bool` → `0.0/1.0`
  and all integer kinds round-trip cleanly (tuning knobs never exceed
  `float64`'s 2^53 integer precision).

### ABI additions (`pkg/wasmabi`, `pkg/wasmsys`, `pkg/wasmhost`)

Two **optional, nil-safe** guest exports — modeled on how `snapshot`/`restore`
are already optional, so **no `ABIVersion` bump** and existing modules without
tunables keep working:

- `wasmsys_params_schema() -> (ptr u32 << 32 | len u32)` — guest reflects its
  `tune:`-tagged fields and returns an encoded schema: `u16 count`, then per
  field `[u8 nameLen][name bytes][u8 kind][f64 default][f64 min][f64 max]
  [f64 step][u8 boundsMask]`. The host decodes this once at first instantiate to
  populate the registry descriptors.
- `wasmsys_params_set(ptr u32, len u32)` — host writes the `[]float64` block
  (one element per field, guest-declared order); the guest scatters each value
  into its struct field with kind conversion. Field order matches by
  construction because both sides walk the same guest-produced schema order.

The host calls `params_set` after every `Load` and after every hot-swap (from
the registry), and again on each `tune set` targeting that cell.

The guest SDK (`pkg/wasmsys`) builds the schema and applies blocks by reflecting
the registered system's tagged fields (it already reflects for nothing today;
this adds a one-time schema build at `init` and a scatter on `params_set`).
Defaults are applied to the fields in `init()` so the guest holds correct values
even before the host's first push.

### Tunables are config, not state

Tunables are kept **entirely separate from `Snapshot`/`Restore`**. On a hot-swap
the internal state (`phase`) carries via the snapshot blob; the tunables
(`Amplitude`…) are re-pushed from the registry onto the new instance. The two
channels never mix, so a swap can change code while preserving both live state
and operator tweaks independently.

## Operator surface

### Console — `tune` verb group (always present)

`cmdsys` verbs registered on the process registry, `RouteAllHosts` (fan out to
every host, each iterating its local cells), mirroring the `wasm.*` verbs:

- `tune list [--system X] [--cell C] [--node N]` → table:
  `System · Field · Value · Default · Min · Max · Kind`. Cells that disagree on
  a value render as `mixed`.
- `tune set <system> <field> <value> [--cell C] [--node N]`
- `tune get <system> <field>`
- `tune reset <system> [field]` — restore the tag default(s) and push.

Required args are positional; only `--cell`/`--node`/`--system` filters are
flags (per project command-arg convention). The `list` result is a flat table
(per console-table convention).

### Admin `/tunables` page (first-class route)

A first-class dashboard route alongside Hosts/Players, backed by the `tune.list`
(schema + values) and `tune.set` verbs through the existing
`POST /admin/api/commands/<verb>` path (so RBAC + audit-log come for free). Each
tunable renders the control its tag implies:

- bounded numeric (`min`+`max`) → **slider** with `step`, value label
- unbounded numeric → numeric input
- `bool` → toggle

Dragging a slider live-sets via a debounced `tune.set`. Current values stream on
a new `tunables` SSE topic (published on every `set`) so multiple operators and
the console stay in sync. Grouped by system. The page reads registry (global)
values; divergent-cell display is a console-only concern in v1.

## Lifecycle & scope

- **Defaults** apply at `Init` (native: the reflect `Source` sets the field from
  the tag; wasm: the guest sets fields in `init()`), then the registry value
  (= default until first `set`) is pushed. So a fresh instance always starts at
  the intended value.
- **Ephemeral.** No persistence. On restart every tunable resets to its tag
  default (native) or the value baked into the rebuilt wasm. Matches the
  workflow: live-tune to find the good number, then bake it into source. No
  Postgres/`ConfigRepository` wiring for tunables.
- **Per-cell instances.** Systems are per-cell, so each cell holds its own field
  values; `tune set` without a filter reaches every cell instance via the
  `RouteAllHosts` fan-out, exactly like the `wasm.*` verbs.

## Removal of the old runtime-config method

The global-`GameConfig` runtime-mutation command surface is **deleted** (not
migrated). The `GameConfig` struct itself and its **startup load/seed** stay —
`gw.Config.*` is read at spawn time throughout `internal/game`, and
`game.LoadConfig`/`SaveConfig`/`DefaultGameConfig` still seed/load the config
blob from Postgres at boot. Only the *reactive runtime* surface is removed:

- `pkg/engine/configurable.go` — `Configurable` interface + `ReflectConfig`
  (its reflection logic is generalized into `pkg/tunable.Reflect`).
- `pkg/engine/builtins_config.go` — the `config.list/get/set/save/reset`
  commands, their arg/result types, and renderers.
- `pkg/engine/builtins.go` — the config fields on `BuiltinOpts`
  (`Config`, `ConfigSave`, `ConfigReset`, `ConfigOnChanged`) and the
  `registerConfigCommands` call. If `RegisterBuiltins`/`BuiltinOpts` retain no
  other purpose afterward, remove them and relocate `snapshotBuiltinCategories`
  to its remaining caller.
- `pkg/universe/coordinator.go` — the `ConsoleOpts` config fields and the
  `builtinOpts` config wiring (~lines 2677–2708).
- `pkg/mmokit/mmokit.go` — the `Configurable`, `ReflectConfig`,
  `NewReflectConfig`, and `BuiltinOpts` facade aliases (no re-export shims —
  delete per project convention).
- `cmd/server/main.go` — the `console.RegisterBuiltins(BuiltinOpts{Config:…})`
  block including the `ConfigSave`/`ConfigReset`/`ConfigOnChanged` closures.
- Tests covering the removed config commands
  (`pkg/engine/configurable_test.go` and the config cases in
  `console_cmdsys_test.go` / completion tests).

`SaveConfig`/`LoadConfig` remain (they back the boot-time seed via `LoadConfig`,
not the removed commands). No admin-UI config surface exists, so the removal is
console + engine wiring only.

## Demo (validation)

- Convert the `wave` module's three `const`s to `tune:`-tagged fields.
- Give the native `FieldSystem` one tunable — `Baseline float32` (a vertical
  offset added to broadcast Y) — so `/tunables` shows a **native and a wasm
  system side by side**, their sliders morphing the same field of dots live.
- Manual smoke: `tune set wave amplitude 420` and a slider drag both reshape the
  field on the next tick; `wasm swap wave` preserves the current tunable values
  (re-pushed from the registry) and the wave phase (snapshot); restart resets to
  tag defaults.

## Package touchpoints

- **`pkg/tunable`** (new, zero-dep, native + wasip1): `Kind`, `Def`, tag parser,
  `Reflect(ptr) Source`, range validation.
- **`pkg/wasmabi`**: schema/block encoding helpers + the two export-name
  constants.
- **`pkg/wasmsys`**: guest schema build + `params_set` scatter + default-apply
  at `init`.
- **`pkg/wasmhost`**: `Module.ParamsSchema()` / `Module.ParamsSet(block)`.
- **`pkg/mmokit`**: per-process tunable registry, `tunableSourceFor` resolution,
  the wasm adapter `Source` impl, the `tune.*` cmdsys verbs, the `/tunables`
  admin route registration + `tunables` SSE topic.
- **`web-admin`**: `/tunables` route (slider/toggle/input controls).
- **Removals**: the old runtime-config surface listed above.
- **Demo**: `examples/simple` (`wave` module + `FieldSystem.Baseline`).
```
