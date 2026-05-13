# DB schema split — engine vs game ownership

Status: Draft → ready for implementation plan
Date: 2026-05-13

## Problem

Today every persisted table lives in `public` with no convention for who owns what. The five tables come from three different layers but you can't tell from their names:

| Table              | Defined by              | Owner reality                                                  |
|--------------------|-------------------------|----------------------------------------------------------------|
| `players`          | `pkg/persist` (engine)  | **MIXED** — identity columns + game-specific JSONB in one row  |
| `admin_operators`  | `pkg/persist` (engine)  | Engine (admin dashboard, `pkg/admin`)                          |
| `game_config`      | `pkg/persist` (engine)  | Game (space-game `GameConfig` blob)                            |
| `market_orders`    | `pkg/persist` (engine)  | Game (`internal/marketplace`)                                  |
| `market_trades`    | `pkg/persist` (engine)  | Game                                                           |

Three concrete problems:

1. **No naming convention.** `admin_*`, `game_*`, `market_*`, bare `players` — five different styles. A new contributor can't tell what's engine infrastructure (schema controlled by engine version) from game tables (game decides shape).
2. **Layering violation.** `pkg/persist` (engine layer) defines `MarketRepository`, `ConfigRepository`, and a `PlayerSnapshot` DTO with `Currencies`/`Cargo`/`Bank`/`Equipment` fields. The space game's persistence concepts live in the engine layer, breaking the `pkg/internal` boundary enforced everywhere else.
3. **The `players` table is half-engine, half-game.** Identity columns (`username`, `cell_id`, `pos_x`, `pos_y`, `last_login`, `debug_flags`) belong to the engine. The JSONB columns (`currencies`, `cargo`, `bank`, `equipment`) belong to whichever game is running. Mixed in one row.

## Goal

A clean engine/game split at the DB layer that matches the `pkg/` vs `internal/` Go split. Engine tables live in an `engine` schema; each game's tables live in a schema named after `Config.Name`. The space game becomes the worked example: `engine.*` + `space.*`.

## Why schemas, not prefixes

Industry consensus (Postgres wiki, Bytebase style guide, Tiger Data, DBVisualizer, django-tenant-schemas):

- **Bounded contexts.** Schemas natively model module boundaries; prefixes (`engine_players`, `game_market_orders`) are an explicit workaround.
- **Permissions.** `GRANT ... ON SCHEMA engine` lets you lock down engine tables differently from game tables — impossible with prefixes.
- **Discovery.** `\dn` in psql shows the split immediately.
- **No collisions.** Same table name in two schemas (`engine.config` vs `space.config`) just works.

Schema name keyed off `Config.Name` (not a hard-coded `game`) because mmokit is opensource and each downstream game picks its own.

## Design

### Target schema layout

```sql
engine.players              -- identity columns only
  username      TEXT  PRIMARY KEY
  cell_id       TEXT  NOT NULL DEFAULT ''
  pos_x         REAL  NOT NULL DEFAULT 0
  pos_y         REAL  NOT NULL DEFAULT 0
  debug_flags   JSONB NOT NULL DEFAULT '[]'::jsonb
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
  last_login    TIMESTAMPTZ NOT NULL DEFAULT NOW()
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
  -- INDEX (cell_id), INDEX (last_login DESC)

engine.admin_operators      -- unchanged shape; moves out of public
  username      TEXT  PRIMARY KEY
  password_hash TEXT  NOT NULL
  grants        JSONB NOT NULL DEFAULT '[]'::jsonb
  created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()

space.player_state          -- game-side per-player JSONB
  username      TEXT  PRIMARY KEY
                      REFERENCES engine.players(username) ON DELETE CASCADE
  currencies    JSONB NOT NULL DEFAULT '{}'::jsonb
  cargo         JSONB NOT NULL DEFAULT '{}'::jsonb
  bank          JSONB NOT NULL DEFAULT '{}'::jsonb
  equipment     JSONB NOT NULL DEFAULT '{}'::jsonb
  updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()

space.market_orders         -- renamed from public.market_orders
  -- column shape unchanged; existing indexes recreated under `space` schema

space.market_trades         -- renamed from public.market_trades
  -- column shape unchanged; existing indexes recreated under `space` schema

space.config                -- renamed from public.game_config (singleton)
  id          INTEGER PRIMARY KEY DEFAULT 1
  data        BYTEA   NOT NULL
  version     BIGINT  NOT NULL
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
  CONSTRAINT  space_config_singleton CHECK (id = 1)
```

`engine.players.username` is the source of truth for "this username exists". `space.player_state` hangs off it via FK with `ON DELETE CASCADE`, so deleting a player from the engine clears game state automatically.

### Code layout

```text
pkg/persist/                       (engine-only)
  repository.go                    PlayerRepository, AdminOperatorRepository
                                   PlayerSnapshot { Username, CellID, PosX, PosY,
                                                    CreatedAt, LastLogin, DebugFlags }
                                   No Currencies / Cargo / Bank / Equipment / EquipmentSnapshot
  postgres/
    migrate.go                     unchanged (already supports extras)
    migrations/                    engine-only; targets `engine` schema
      001_init.up.sql              CREATE SCHEMA engine + engine.players + engine.admin_operators
    player_repo.go                 reads/writes engine.players (identity only)
    admin_operator_repo.go         reads/writes engine.admin_operators
    config_repo.go     DELETED →   moves to internal/persist
    market_repo.go     DELETED →   moves to internal/persist

internal/persist/                  (space-game persistence package, new)
  repository.go                    PlayerStateRepository, MarketRepository, ConfigRepository
                                   PlayerStateSnapshot { Username, Currencies, Cargo, Bank, Equipment }
                                   EquipmentSnapshot (moved from pkg/persist)
  postgres/
    migrations/                    game-only; targets `space` schema
      001_init.up.sql              CREATE SCHEMA space + space.player_state +
                                   space.market_orders + space.market_trades + space.config
    player_state_repo.go           reads/writes space.player_state
    market_repo.go                 reads/writes space.market_orders, space.market_trades
    config_repo.go                 reads/writes space.config
```

### Engine-side `PlayerSnapshot` — identity only

```go
type PlayerSnapshot struct {
    Username   string
    CellID     string
    PosX       float32
    PosY       float32
    CreatedAt  time.Time
    LastLogin  time.Time
    DebugFlags []string
}
```

`Currencies`, `Cargo`, `Bank`, `Equipment`, and `EquipmentSnapshot` are removed from `pkg/persist`. The engine layer no longer knows space-game state shape exists.

### Game-side `PlayerStateSnapshot`

Defined in `internal/persist/repository.go`:

```go
type PlayerStateSnapshot struct {
    Username   string
    Currencies map[uint32]int64
    Cargo      map[uint32]int32
    Bank       map[uint32]int32
    Equipment  EquipmentSnapshot
}

type EquipmentSnapshot struct { /* moved verbatim from pkg/persist */ }
```

`PlayerStateRepository` mirrors `PlayerRepository`: `Load`, `LoadAll`, `SaveBatch` keyed by username, same deadlock-prevention sort-by-username contract.

### Wiring — game migrations registration

Add to mmokit's facade:

```go
// RegisterGameMigrations registers an additional migration source that
// runs after engine migrations. label becomes the schema_migrations_<label>
// tracking table; migrations are responsible for creating their own
// CREATE SCHEMA statement.
func RegisterGameMigrations(coord *Process, label string, fs embed.FS, root string)
```

Internally appends to the `extras []extraSource` slice that `runMigrations` already iterates. Must be called before `Build()`. The space game's `cmd/server/main.go` calls it once with the embedded `internal/persist/postgres/migrations` FS.

### PlayerFlusher — two batches in one tx

Today's `internal/game.PlayerFlusher.FlushDirty`:

- captures dirty `PlayerSnapshot`s in memory
- sorts by username
- calls `PlayerRepository.SaveBatch` (single pgx.Batch)

After the split: same `FlushDirty` walks the dirty set, builds **both** halves (engine `PlayerSnapshot` + game `PlayerStateSnapshot`), opens one pgx.Tx, and runs both `SaveBatch` calls under it. Sort-by-username applies to both halves identically. Net cost: one extra round trip per flush cycle (15s cadence) — trivial.

The dirty-tracking layer stays in `internal/game`; `Mark(player)` walks both halves of `PlayerData` as it does today.

### Test reset

`pkg/persist/postgres/postgres_test.go::reset` today:

```sql
TRUNCATE players, game_config, market_orders, market_trades, admin_operators RESTART IDENTITY
```

Splits into two helpers:

- Engine test pkg: `TRUNCATE engine.players, engine.admin_operators RESTART IDENTITY CASCADE` (CASCADE clears `space.player_state` via the FK).
- Game test pkg (`internal/persist/postgres/postgres_test.go`): `TRUNCATE space.player_state, space.market_orders, space.market_trades, space.config RESTART IDENTITY`.

## Breaking change policy

Per `feedback_no_backward_compat`: single PR, no preservation of the historical 001/002/003 engine migration trail. Dev DB wiped with `just db-reset`. The rewritten `001_init.up.sql` for engine creates the final schema directly — no incremental ALTERs over the legacy `public.*` tables. Same for the game side: one `001_init.up.sql` that creates `space.*` from scratch.

`schema_migrations` and `schema_migrations_<label>` tables get wiped along with the dev DB; first migration run on a fresh DB creates them fresh.

## Risks considered

- **`engine.players` is opinionated.** Forces every game on this engine to use the same identity columns (username, cell_id, pos_x, pos_y, last_login, debug_flags). Accepted: those columns are universal to every game this engine targets. If a future game needs a different shape, that's an engine migration, not a game opt-out.
- **Two tables = two writes per player flush.** Wrapped in one tx; latency hit is one extra round trip every 15 s. Trivial.
- **FK across schemas.** Postgres treats `REFERENCES engine.players(username)` identically to within-schema FK. The schema must exist first — guaranteed by the order in `runMigrations` (engine source runs before extras).
- **`search_path` foot-guns.** Repository SQL fully qualifies every table reference (`SELECT ... FROM engine.players`, `INSERT INTO space.market_orders ...`). No reliance on `search_path` defaults. Tests verify by running with `SET search_path = ''`.

## Out of scope

- 4node-basic doesn't touch postgres today; no game-side schema is added for it. If a future contributor wires it up, they call `RegisterGameMigrations(coord, "basic", basicMigrationFS, "migrations")`.
- No multi-tenant / multi-game co-existence in one DB. Each deployment runs one engine + one game schema. The naming pattern supports it for future test fixtures but no code change wires it up.
- No data migration tooling for production users — there are none. Solo-dev workflow, dev DB gets wiped.
