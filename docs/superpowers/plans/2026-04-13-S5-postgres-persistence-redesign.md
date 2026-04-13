# S5: Postgres Persistence — Clean-Slate Redesign

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Each task uses checkbox (`- [ ]`) syntax. This plan REPLACES the S5 portion of `2026-04-13-S4.5-S5-S6-distributed-mesh-continuation.md` — that earlier draft was based on a wrong assumption about the existing `pkg/persist.Store` interface (it assumed a player-centric API; the actual interface is a generic KV).

**Branch:** stay on `feature/distributed-mesh`.

---

## Why this plan exists

When the S4.5/S5/S6 master plan was drafted, the S5 section was designed against an imagined `Store` interface (`LoadPlayer`, `SavePlayer`, `SaveBatch`) that doesn't exist in the codebase. The actual `pkg/persist.Store` is a generic `Get/Put/Delete/ForEach(collection, key, []byte)` KV interface — domain serialization happens upstream in `internal/game/playerdb.go` (`PlayerRepo`) and `internal/marketplace/persist.go`.

Forcing the original plan onto reality would either:
1. Build a KV-on-Postgres adapter that loses every JSONB benefit, or
2. Introduce ad-hoc method additions to the KV interface that contradict its design.

The user's direction is to **redesign for the future architecture with no priors**. This plan does that: a domain-aware repository layer over Postgres, hybrid relational + JSONB schema, no backward compat with BoltDB, no generic KV escape hatch.

---

## Current state (audited 2026-04-13)

### Persistence consumers

| Consumer | Where | What it stores | Access pattern | Today |
|---|---|---|---|---|
| `PlayerRepo` | `internal/game/playerdb.go` | `PlayerData` (JSON-marshaled) | Medium-hot: dirty-marked on game tick, flushed every 300 ticks (~15s) and on shutdown | BoltDB bucket `players`, key=username |
| `LoadConfig`/`SaveConfig` | `internal/game/config.go` | `GameConfig` (JSON-marshaled) | Cold: read once at startup, written on `config save` admin command | BoltDB bucket `config`, key=`game` |
| Marketplace orders | `internal/marketplace/persist.go:28` | `Order` (JSON-marshaled) | Per-event (place/partial-fill), on operation-router worker goroutine | BoltDB bucket `market_orders`, key=`strconv(id)` |
| Marketplace order delete | `internal/marketplace/persist.go:42` | nil-value Op | Per-event (fill/cancel/expire) | Bucket `market_orders` Delete |
| Marketplace trades | `internal/marketplace/persist.go:55` | `Trade` (JSON-marshaled) | Per-event (append-only audit log, never read back) | BoltDB bucket `market_trades` |
| Marketplace next-id | `internal/marketplace/persist.go:70` | ASCII decimal | Per-event (write amplification — every order write also bumps the counter) | BoltDB bucket `market_meta`, key=`next_id` |

### Caller wiring

- `cmd/server/main.go:91` opens `data/gameserver.db` (player + config)
- `cmd/server/main.go:229` opens `data/marketplace.db` (orders + trades + meta)
- Two separate `AsyncWriter`s, each wrapping its own BoltDB
- `mmokit.OpenBolt` is a re-export from `pkg/persist`

### Domain types

**`PlayerData`** (`internal/game/playerdata.go:23`):
```go
type PlayerData struct {
    Username   string
    X, Y       float32
    CellX,CellY int32
    Currencies map[uint32]int64
    Cargo      map[uint32]int32
    Bank       map[uint32]int32
    Equipment  EquipmentSave   // 4-field struct
    HasSave    bool
    CreatedAt  time.Time
    LastLogin  time.Time
}
```

**`GameConfig`** (`internal/game/config.go`): ~40 tunable balance fields + `Version int`.

**`Order`** (`pkg/orderbook/types.go`): id, side, owner, location_id, item_id, price, quantity, orig_qty, created_at, expires_at.

**`Trade`** (`pkg/orderbook/types.go`): id, item_id, location_id, price, quantity, buyer, seller, timestamp.

### What we're throwing away

- `pkg/persist/store.go` (the generic KV interface)
- `pkg/persist/bbolt.go` (the BoltDB implementation)
- `pkg/persist/writer.go` (the generic op-queue AsyncWriter)
- `pkg/persist/persist_test.go` (BoltDB test fixture + AsyncWriter mock)
- `internal/marketplace/persist.go` (the marketplace KV adapter)
- `bbolt` from `go.mod`
- `mmokit.OpenBolt` re-export
- BoltDB on-disk databases under `data/*.db`
- The "collection + key + []byte" mental model

**Nothing is deprecated. Nothing is wrapped. Old call sites are rewritten.**

---

## Target architecture

### Design principles

1. **Domain-aware repositories, not generic KV.** Each persistent aggregate has its own typed interface (`PlayerRepository`, `MarketRepository`, `ConfigRepository`). Method names express domain operations, not key-value primitives.

2. **Hybrid schema: hot fields relational, cold fields JSONB.** Position, cell ID, last-seen time, and primary identifiers get explicit columns and indexes. Sparse maps (cargo, bank, currencies) and structs that mutate frequently as the game evolves (equipment) live in JSONB columns. Modeled on Albion Online and Nakama; rejects the flat-JSONB-everything pattern that creates GIN write amplification.

3. **Postgres SEQUENCE for monotonic IDs.** Marketplace's `next_id` counter becomes `BIGSERIAL`. No more "UPDATE counter on every insert" write amplification. Restart-safe by construction.

4. **Typed flush coordinator, not generic op-queue.** Replace the generic `AsyncWriter` with `PlayerFlusher` that owns dirty tracking + batched upserts via `pgx.Batch`. Marketplace writes are synchronous (small volume, off the game tick) — no flusher needed.

5. **Schema migrations via `golang-migrate` + `go:embed`.** Industry-standard Go tooling; runs migrations transactionally at process startup. Simple `.up.sql` / `.down.sql` files numbered `NNN_description`.

6. **Connection pool tuned for game-server write patterns.** `MaxConns = 4 * NumCPU` (capped at 32), `MinConns = 4`, `MaxConnLifetime = 30m`, `MaxConnIdleTime = 5m`. From pgx maintainer guidance + Nakama defaults.

7. **Sort batches by primary key to prevent deadlocks.** `PlayerFlusher` sorts the dirty set by username before sending each `pgx.Batch`. Multiple concurrent batches can't deadlock if they all touch rows in the same order.

8. **`FlushCell(cellID)` for cell-migration safety.** `PlayerFlusher` exposes a synchronous method that flushes only the players currently in a given cell. Source node calls before handoff so the destination node sees a consistent snapshot. (Used by S6 / S7; stub it for S5.)

9. **No backward compat. No legacy interfaces. No "transitional" code.** Every BoltDB call site is rewritten to the new repo interface in the same commit that introduces the interface.

10. **Tests use real Postgres via docker-compose, gated on `POSTGRES_URL`.** Game-domain tests that previously used BoltDB mocks switch to a small in-memory mock implementing the new repository interfaces.

### Package layout

```
pkg/persist/
├── doc.go                       # package overview
├── repository.go                # PlayerRepository, MarketRepository, ConfigRepository interfaces + snapshot types
├── errors.go                    # ErrNotFound + sentinel errors
├── postgres/
│   ├── postgres.go              # Store: pool open + Players()/Market()/Config() accessors
│   ├── migrate.go               # go:embed migration runner
│   ├── migrations/
│   │   └── 001_init.up.sql      # initial schema
│   ├── player_repo.go           # playerRepo: Load, LoadAll, SaveBatch
│   ├── market_repo.go           # marketRepo: PlaceOrder, UpdateOrderQuantity, DeleteOrder, RecordTrade, LoadActiveOrders
│   └── config_repo.go           # configRepo: Load, Save
├── persisttest/
│   └── mock.go                  # in-memory PlayerRepository/MarketRepository/ConfigRepository for game-domain tests
└── postgres_test.go             # //go:build pgtest — round-trip integration tests
```

`mmokit.OpenBolt` is **deleted**, not replaced. New API: `mmokit.OpenPostgres(ctx, url) (*postgres.Store, error)`.

### Domain interfaces

```go
// pkg/persist/repository.go
package persist

import (
    "context"
    "errors"
    "time"
)

var ErrNotFound = errors.New("persist: record not found")

// PlayerRepository persists player state. Implementation in pkg/persist/postgres.
type PlayerRepository interface {
    // Load fetches a player snapshot by username. Returns ErrNotFound if absent.
    Load(ctx context.Context, username string) (*PlayerSnapshot, error)

    // LoadAll streams every player record, calling fn for each. Used at game
    // startup to warm the in-memory PlayerRepo cache. Iteration order is
    // unspecified.
    LoadAll(ctx context.Context, fn func(*PlayerSnapshot) error) error

    // SaveBatch upserts multiple player snapshots in a single round-trip via
    // pgx.Batch. The caller MUST sort snapshots by username to prevent
    // deadlocks under concurrent batches (Postgres locks rows in the order
    // the batch hits them).
    SaveBatch(ctx context.Context, snapshots []*PlayerSnapshot) error
}

// PlayerSnapshot is the persistence-layer representation of a player.
// Distinct from internal/game.PlayerData — the snapshot is the subset that
// needs to survive process restart, with no runtime fields like ECS entity
// references.
type PlayerSnapshot struct {
    Username   string
    CellID     string             // e.g. "cell_2_1"
    PosX       float32
    PosY       float32
    Currencies map[uint32]int64
    Cargo      map[uint32]int32
    Bank       map[uint32]int32
    Equipment  EquipmentSnapshot
    CreatedAt  time.Time
    LastLogin  time.Time
}

type EquipmentSnapshot struct {
    Weapon1  uint32 `json:"weapon1,omitempty"`
    Weapon2  uint32 `json:"weapon2,omitempty"`
    Shield   uint32 `json:"shield,omitempty"`
    Thruster uint32 `json:"thruster,omitempty"`
}

// MarketRepository persists order book state. All methods are synchronous —
// orders need to survive even brief crashes because the in-memory book gets
// rebuilt from this storage at startup.
type MarketRepository interface {
    // PlaceOrder inserts a new resting order. Returns the database-assigned
    // ID from the BIGSERIAL sequence; the caller updates the in-memory order
    // with this ID.
    PlaceOrder(ctx context.Context, o *OrderRecord) (uint64, error)

    // UpdateQuantity decrements the remaining quantity on a partial fill.
    UpdateQuantity(ctx context.Context, id uint64, newQty int32) error

    // DeleteOrder removes a fully-filled, cancelled, or expired order.
    DeleteOrder(ctx context.Context, id uint64) error

    // RecordTrade appends a completed trade to the audit log.
    RecordTrade(ctx context.Context, t *TradeRecord) error

    // LoadActiveOrders streams all non-expired orders at startup.
    LoadActiveOrders(ctx context.Context, fn func(*OrderRecord) error) error
}

type OrderRecord struct {
    ID         uint64    // 0 on PlaceOrder; assigned by DB sequence
    Side       uint8     // 0 = buy, 1 = sell
    Owner      string
    LocationID uint32
    ItemID     uint32
    Price      int64
    Quantity   int32     // remaining
    OrigQty    int32
    CreatedAt  time.Time
    ExpiresAt  time.Time // zero value = never expires
}

type TradeRecord struct {
    ItemID     uint32
    LocationID uint32
    Price      int64
    Quantity   int32
    Buyer      string
    Seller     string
    OccurredAt time.Time
}

// ConfigRepository persists the singleton GameConfig blob. The implementation
// uses a single-row table with a CHECK constraint enforcing id = 1.
type ConfigRepository interface {
    // Load returns the saved config blob and its version. Returns ErrNotFound
    // if no config has been saved yet (first run).
    Load(ctx context.Context) (*ConfigSnapshot, error)

    // Save upserts the config blob.
    Save(ctx context.Context, snapshot *ConfigSnapshot) error
}

type ConfigSnapshot struct {
    Data    []byte    // game-specific serialization (JSON, kept opaque to persist)
    Version int64
}
```

### Schema (initial migration)

```sql
-- pkg/persist/postgres/migrations/001_init.up.sql

-- Players: hybrid relational + JSONB.
-- Hot fields (cell_id, pos_x, pos_y, last_login) are indexable columns;
-- sparse/evolving structures (currencies, cargo, bank, equipment) are JSONB.
CREATE TABLE players (
    username      TEXT        PRIMARY KEY,
    cell_id       TEXT        NOT NULL DEFAULT '',
    pos_x         REAL        NOT NULL DEFAULT 0,
    pos_y         REAL        NOT NULL DEFAULT 0,
    currencies    JSONB       NOT NULL DEFAULT '{}'::jsonb,
    cargo         JSONB       NOT NULL DEFAULT '{}'::jsonb,
    bank          JSONB       NOT NULL DEFAULT '{}'::jsonb,
    equipment     JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_login    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX players_cell_id_idx     ON players(cell_id);
CREATE INDEX players_last_login_idx  ON players(last_login DESC);

-- Game config: single-row table for the live game balance config.
-- The CHECK constraint enforces "exactly one row" — no race conditions
-- on multi-coordinator deploys.
CREATE TABLE game_config (
    id          INTEGER     PRIMARY KEY DEFAULT 1,
    data        BYTEA       NOT NULL,
    version     BIGINT      NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT  game_config_singleton CHECK (id = 1)
);

-- Marketplace orders: typed columns for indexed lookups.
-- BIGSERIAL replaces the legacy "next_id counter in a row" pattern.
CREATE TABLE market_orders (
    id           BIGSERIAL   PRIMARY KEY,
    side         SMALLINT    NOT NULL,                -- 0 = buy, 1 = sell
    owner        TEXT        NOT NULL,
    location_id  INTEGER     NOT NULL,
    item_id      INTEGER     NOT NULL,
    price        BIGINT      NOT NULL,
    quantity     INTEGER     NOT NULL,
    orig_qty     INTEGER     NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ                          -- NULL = never expires
);

CREATE INDEX market_orders_lookup_idx  ON market_orders(location_id, item_id, side, price);
CREATE INDEX market_orders_owner_idx   ON market_orders(owner);
CREATE INDEX market_orders_expires_idx ON market_orders(expires_at) WHERE expires_at IS NOT NULL;

-- Marketplace trades: append-only audit log.
-- Indexed for future "trade history" UIs but never read by today's game loop.
CREATE TABLE market_trades (
    id           BIGSERIAL   PRIMARY KEY,
    item_id      INTEGER     NOT NULL,
    location_id  INTEGER     NOT NULL,
    price        BIGINT      NOT NULL,
    quantity     INTEGER     NOT NULL,
    buyer        TEXT        NOT NULL,
    seller       TEXT        NOT NULL,
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX market_trades_buyer_idx   ON market_trades(buyer, occurred_at DESC);
CREATE INDEX market_trades_seller_idx  ON market_trades(seller, occurred_at DESC);
CREATE INDEX market_trades_item_idx    ON market_trades(item_id, occurred_at DESC);
```

```sql
-- pkg/persist/postgres/migrations/001_init.down.sql

DROP TABLE IF EXISTS market_trades;
DROP TABLE IF EXISTS market_orders;
DROP TABLE IF EXISTS game_config;
DROP TABLE IF EXISTS players;
```

### PlayerFlusher (replaces generic AsyncWriter)

Lives in `internal/game/player_flusher.go` (game-domain code, not pkg/persist — it knows about `PlayerData ↔ PlayerSnapshot` translation):

```go
// PlayerFlusher owns dirty-tracking + batched upserts for player state.
// Replaces the generic AsyncWriter pattern with a typed flush coordinator
// that knows how to convert internal PlayerData to a persist.PlayerSnapshot
// and submit it via pgx.Batch.
//
// Thread safety: Mark and Flush both take mu. Flush snapshots and clears
// the dirty map under the lock, then runs the batch outside the lock so
// concurrent Mark calls don't block on DB latency.
type PlayerFlusher struct {
    repo persist.PlayerRepository
    log  *logger.Logger

    mu    sync.Mutex
    dirty map[string]*persist.PlayerSnapshot
}

func NewPlayerFlusher(repo persist.PlayerRepository, log *logger.Logger) *PlayerFlusher

// Mark records that the given player needs to be flushed. The snapshot
// is captured immediately so subsequent in-memory mutations don't race.
func (f *PlayerFlusher) Mark(snapshot *persist.PlayerSnapshot)

// Flush sends the current dirty set as one pgx.Batch upsert. Sorts by
// username to prevent deadlocks across concurrent flushes. Resets the
// dirty map on success; preserves it on error so the next flush retries.
func (f *PlayerFlusher) Flush(ctx context.Context) (int, error)

// FlushCell is the cell-migration safety hook (S6/S7 will use this).
// Flushes only the dirty players currently assigned to the given cell.
// Stub for S5: implemented but no caller yet.
func (f *PlayerFlusher) FlushCell(ctx context.Context, cellID string) (int, error)
```

`PlayerRepo` (the in-memory cache) keeps the existing `MarkDirty(username)` API but routes flushes through `PlayerFlusher` instead of the deleted `AsyncWriter`. Translation between `PlayerData` (game-internal type with helper methods) and `persist.PlayerSnapshot` (storage type) happens in one place: `PlayerRepo.snapshot(*PlayerData) *persist.PlayerSnapshot`.

### Marketplace settlement rewrite

`internal/marketplace/persist.go` is **deleted**. Settlement gets a `persist.MarketRepository` field and calls it directly:

```go
// Old:
st.persistOrder(order)               // enqueues an Op on AsyncWriter
// New:
id, err := st.market.PlaceOrder(ctx, toRecord(order))
order.ID = id                         // sequence-assigned ID
```

Synchronous. The order-router worker goroutines block briefly on `PlaceOrder` (~1-3ms typical) which is acceptable: place/fill events are infrequent compared to the game tick.

### Config rewrite

`internal/game/config.go` `LoadConfig`/`SaveConfig` switch from `Store.Get`/`Store.Put` to `ConfigRepository.Load`/`ConfigRepository.Save`. Translation between `*GameConfig` and `*ConfigSnapshot` (just JSON-marshaling the whole struct) is local to `config.go`.

### Connection pool config

```go
// pkg/persist/postgres/postgres.go
const (
    poolMaxConnsCap     = 32
    poolMaxConnsPerCPU  = 4
    poolMinConns        = 4
    poolMaxConnLifetime = 30 * time.Minute
    poolMaxConnIdleTime = 5 * time.Minute
    poolHealthCheckIntv = 1 * time.Minute
)

func Open(ctx context.Context, url string) (*Store, error) {
    cfg, err := pgxpool.ParseConfig(url)
    if err != nil {
        return nil, fmt.Errorf("postgres: parse url: %w", err)
    }

    maxConns := runtime.NumCPU() * poolMaxConnsPerCPU
    if maxConns > poolMaxConnsCap {
        maxConns = poolMaxConnsCap
    }
    if maxConns < poolMinConns {
        maxConns = poolMinConns
    }
    cfg.MaxConns = int32(maxConns)
    cfg.MinConns = poolMinConns
    cfg.MaxConnLifetime = poolMaxConnLifetime
    cfg.MaxConnIdleTime = poolMaxConnIdleTime
    cfg.HealthCheckPeriod = poolHealthCheckIntv

    pool, err := pgxpool.NewWithConfig(ctx, cfg)
    if err != nil {
        return nil, fmt.Errorf("postgres: pool: %w", err)
    }
    if err := pool.Ping(ctx); err != nil {
        pool.Close()
        return nil, fmt.Errorf("postgres: ping: %w", err)
    }

    if err := runMigrations(ctx, pool); err != nil {
        pool.Close()
        return nil, fmt.Errorf("postgres: migrate: %w", err)
    }

    return &Store{pool: pool}, nil
}
```

### Migration runner

We use the `golang-migrate` library directly (not the CLI) with `go:embed` for the SQL files. The `pgx` driver bridge: `github.com/golang-migrate/migrate/v4/database/pgx/v5` (lands as a stable driver in 2024).

```go
// pkg/persist/postgres/migrate.go
package postgres

import (
    "context"
    "embed"
    "fmt"

    "github.com/golang-migrate/migrate/v4"
    pgxdriver "github.com/golang-migrate/migrate/v4/database/pgx/v5"
    "github.com/golang-migrate/migrate/v4/source/iofs"
    "github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

func runMigrations(ctx context.Context, pool *pgxpool.Pool) error {
    src, err := iofs.New(migrationFS, "migrations")
    if err != nil {
        return fmt.Errorf("iofs source: %w", err)
    }

    db, err := pgxdriver.WithInstance(ctx, pool, &pgxdriver.Config{})
    if err != nil {
        return fmt.Errorf("pgx driver: %w", err)
    }

    m, err := migrate.NewWithInstance("iofs", src, "pgx", db)
    if err != nil {
        return fmt.Errorf("migrate new: %w", err)
    }

    if err := m.Up(); err != nil && err != migrate.ErrNoChange {
        return fmt.Errorf("migrate up: %w", err)
    }
    return nil
}
```

If the `golang-migrate` pgx/v5 driver isn't yet stable enough (verify in T1), fall back to a custom embedded runner like the original plan proposed — but the typed library is preferred.

### Config plumbing

```go
// pkg/universe/coordinator.go Config:
PostgresURL string  // "postgres://mmo:mmo@localhost:5432/mmo?sslmode=disable" if empty
```

`mmokit.Config.PostgresURL` exposes it. `cmd/server/main.go` reads it from env or flag, defaults to the docker-compose URL.

### What goes in CLAUDE.md after S5

```markdown
### Persistence

Player state, marketplace orders/trades, and game config live in PostgreSQL
via pgx/v5. Schema is managed by `pkg/persist/postgres/migrations/*.sql`
files embedded in the binary and applied automatically at process startup
via `golang-migrate`. The schema is hybrid: hot fields like `cell_id`,
`pos_x`, `pos_y`, `last_login` are explicit relational columns; sparse and
evolving structures (`currencies`, `cargo`, `bank`, `equipment`) live in
JSONB columns.

**Repository pattern:** `pkg/persist/repository.go` defines domain-specific
interfaces (`PlayerRepository`, `MarketRepository`, `ConfigRepository`).
`pkg/persist/postgres.Store` implements all three via a single `pgxpool.Pool`.
There is no generic KV abstraction — every persistence operation is typed
to its domain.

**Player flush:** `internal/game/PlayerFlusher` tracks dirty players in
memory and submits batched upserts via `pgx.Batch` every 300 ticks (~15s)
and on shutdown. Marketplace writes are synchronous (per-event, not per-tick).

**Local dev:**
- `just db-up` — start PostgreSQL 17 via docker-compose
- `just db-psql` — drop into a psql shell
- `just db-reset` — wipe the volume and restart
- `just test-pg` — run the Postgres integration tests (`-tags=pgtest`)

Connection URL defaults to `postgres://mmo:mmo@localhost:5432/mmo?sslmode=disable`;
override via `Config.PostgresURL` or `POSTGRES_URL` env var.
```

---

## File structure

### Created

| Path | Responsibility |
|---|---|
| `pkg/persist/doc.go` | Package overview |
| `pkg/persist/repository.go` | `PlayerRepository`, `MarketRepository`, `ConfigRepository` interfaces + `PlayerSnapshot`, `OrderRecord`, `TradeRecord`, `ConfigSnapshot` types |
| `pkg/persist/errors.go` | `ErrNotFound` + sentinel errors |
| `pkg/persist/postgres/postgres.go` | `Store` struct, `Open`, `Close`, `Players()`, `Market()`, `Config()` |
| `pkg/persist/postgres/migrate.go` | `runMigrations` via golang-migrate + go:embed |
| `pkg/persist/postgres/migrations/001_init.up.sql` | Initial schema |
| `pkg/persist/postgres/migrations/001_init.down.sql` | Rollback |
| `pkg/persist/postgres/player_repo.go` | `playerRepo` implementing `PlayerRepository` |
| `pkg/persist/postgres/market_repo.go` | `marketRepo` implementing `MarketRepository` |
| `pkg/persist/postgres/config_repo.go` | `configRepo` implementing `ConfigRepository` |
| `pkg/persist/postgres/postgres_test.go` | `//go:build pgtest` round-trip integration tests |
| `pkg/persist/persisttest/mock.go` | In-memory mock repos for game-domain unit tests |
| `internal/game/player_flusher.go` | `PlayerFlusher` (replaces generic AsyncWriter) |
| `docker-compose.yml` | Postgres 17 + persistent volume |

### Modified

| Path | What changes |
|---|---|
| `go.mod` | + `github.com/jackc/pgx/v5`, `github.com/jackc/pgx/v5/pgxpool`, `github.com/golang-migrate/migrate/v4` (with `iofs` + `pgx` drivers) ; − `go.etcd.io/bbolt` |
| `pkg/mmokit/mmokit.go` | Replace `OpenBolt` re-export with `OpenPostgres = postgres.Open`. Re-export `persist.PlayerRepository`, `persist.MarketRepository`, `persist.ConfigRepository`, `persist.PlayerSnapshot`, etc. |
| `pkg/universe/coordinator.go` | Add `Config.PostgresURL` field |
| `internal/game/playerdb.go` | `PlayerRepo` now wraps a `persist.PlayerRepository` + `*PlayerFlusher`; `LoadAll` uses `repo.LoadAll`; `FlushDirty` delegates to `flusher.Flush`; translation `PlayerData ↔ PlayerSnapshot` in private helpers |
| `internal/game/config.go` | `LoadConfig`/`SaveConfig` switch to `ConfigRepository`; JSON marshal/unmarshal locally |
| `internal/marketplace/settlement.go` | Add `market persist.MarketRepository` field; rewrite all persist call sites to call repo methods directly; remove the AsyncWriter dependency |
| `cmd/server/main.go` | Open one `postgres.Store` via `mmokit.OpenPostgres(ctx, url)`; pass `store.Players()` / `store.Market()` / `store.Config()` to game/marketplace/config wiring; remove the two `OpenBolt` calls + AsyncWriter setup |
| `examples/4node-basic/main.go`, other examples | Verify none directly construct a Store; if they do (probably not), swap to PostgresStore via the same pattern |
| `justfile` | Add `db-up`, `db-down`, `db-psql`, `db-reset`, `test-pg` recipes |
| `CLAUDE.md` | Rewrite the Persistence section (template above) |

### Deleted

| Path | Why |
|---|---|
| `pkg/persist/store.go` | Generic KV interface obsoleted by typed repositories |
| `pkg/persist/bbolt.go` | BoltDB implementation gone |
| `pkg/persist/writer.go` | Generic AsyncWriter replaced by typed PlayerFlusher |
| `pkg/persist/persist_test.go` | Tests target the deleted types |
| `internal/marketplace/persist.go` | KV adapter replaced by direct MarketRepository calls |
| `data/gameserver.db` | BoltDB on-disk database (no migration; dev DBs are wiped) |
| `data/marketplace.db` | Same |

`mmokit.OpenBolt` is removed and **not replaced under that name**. The new entry point is `mmokit.OpenPostgres`.

---

## Task breakdown

### Task 1: Dependencies + docker-compose + justfile recipes

- [ ] **Step 1: Add Go dependencies**

```bash
go get github.com/jackc/pgx/v5@latest
go get github.com/jackc/pgx/v5/pgxpool@latest
go get github.com/golang-migrate/migrate/v4@latest
go get github.com/golang-migrate/migrate/v4/database/pgx/v5@latest
go get github.com/golang-migrate/migrate/v4/source/iofs@latest
go mod tidy
```

If the `pgx/v5` driver for golang-migrate doesn't exist or is unstable as of April 2026, escalate to the controller — fallback is the custom embedded runner from the original plan, but typed-library is strongly preferred.

- [ ] **Step 2: Create `docker-compose.yml`**

```yaml
services:
  postgres:
    image: postgres:17-alpine
    container_name: mmoserver-postgres
    environment:
      POSTGRES_USER: mmo
      POSTGRES_PASSWORD: mmo
      POSTGRES_DB: mmo
    ports:
      - "5432:5432"
    volumes:
      - mmo-pg-data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U mmo -d mmo"]
      interval: 2s
      timeout: 1s
      retries: 10

volumes:
  mmo-pg-data:
```

- [ ] **Step 3: Add justfile recipes** — append to `justfile`:

```make
db-up:
    docker compose up -d postgres

db-down:
    docker compose down

db-psql:
    docker compose exec postgres psql -U mmo -d mmo

db-reset:
    docker compose down -v
    docker compose up -d postgres

test-pg:
    POSTGRES_URL=postgres://mmo:mmo@localhost:5432/mmo?sslmode=disable \
        go test -count=1 -tags=pgtest ./pkg/persist/...
```

- [ ] **Step 4: Smoke test**

```bash
just db-up
docker compose ps  # verify postgres is healthy
just db-psql       # should drop into psql
\q
```

- [ ] **Step 5: Verify go.mod**

```bash
go vet ./...
```

(Should still be clean; no production code uses pgx yet.)

- [ ] **Step 6: Commit**

```
build: pgx/v5 + golang-migrate deps + docker compose for Postgres

Adds the pgx/v5 connection pool and golang-migrate (with iofs source
and pgx/v5 driver) for the S5 redesigned persistence layer. docker
compose runs Postgres 17 alpine with a persistent volume; just db-up
/ db-down / db-psql / db-reset / test-pg recipes wrap the common
local-dev workflow.
```

---

### Task 2: Repository interfaces + snapshot types

**File:** `pkg/persist/repository.go`, `pkg/persist/errors.go`, `pkg/persist/doc.go`

- [ ] **Step 1: Create `pkg/persist/doc.go`**

```go
// Package persist defines domain-aware repository interfaces for
// persistent game state. Implementations live in subpackages
// (pkg/persist/postgres). Game-domain code depends only on the
// interfaces here, never on a specific backend.
//
// There is no generic key-value Store interface — every persistence
// operation is typed to its domain (PlayerRepository, MarketRepository,
// ConfigRepository).
package persist
```

- [ ] **Step 2: Create `pkg/persist/errors.go`**

```go
package persist

import "errors"

// ErrNotFound is returned when a Load call cannot find a record.
var ErrNotFound = errors.New("persist: record not found")
```

- [ ] **Step 3: Create `pkg/persist/repository.go`**

(Use the full interface text from the "Domain interfaces" section above. Snapshot types, OrderRecord, TradeRecord, ConfigSnapshot all live here.)

- [ ] **Step 4: Verify**

```bash
go vet ./pkg/persist/
```

The package compiles standalone with no implementations yet.

- [ ] **Step 5: Commit**

```
feat(persist): domain repository interfaces

PlayerRepository, MarketRepository, ConfigRepository define typed
persistence operations for the game's three persistent aggregates.
Snapshot types (PlayerSnapshot, OrderRecord, TradeRecord,
ConfigSnapshot) are the persistence-layer DTOs — distinct from the
in-memory game types so storage can evolve independently.

No backend yet; pkg/persist/postgres lands in the next task.
```

---

### Task 3: Postgres backend — pool, migrations, schema

**Files:** `pkg/persist/postgres/postgres.go`, `pkg/persist/postgres/migrate.go`, `pkg/persist/postgres/migrations/001_init.up.sql`, `pkg/persist/postgres/migrations/001_init.down.sql`

- [ ] **Step 1: Create the migration SQL files** (use the schema from the "Schema (initial migration)" section above)

- [ ] **Step 2: Create `pkg/persist/postgres/migrate.go`** (use the runner code above)

- [ ] **Step 3: Create `pkg/persist/postgres/postgres.go`**

```go
// Package postgres is the PostgreSQL implementation of the persist
// repository interfaces. Open returns a *Store that exposes the three
// repos via Players(), Market(), Config().
package postgres

import (
    "context"
    "fmt"
    "runtime"
    "time"

    "github.com/jackc/pgx/v5/pgxpool"

    "github.com/zenion/mmoserver/pkg/persist"
)

const (
    poolMaxConnsCap     = 32
    poolMaxConnsPerCPU  = 4
    poolMinConns        = 4
    poolMaxConnLifetime = 30 * time.Minute
    poolMaxConnIdleTime = 5 * time.Minute
    poolHealthCheckIntv = 1 * time.Minute
)

// Store is the PostgreSQL-backed persistence root. Holds a single
// pgxpool.Pool shared by all three repository implementations.
type Store struct {
    pool *pgxpool.Pool
}

// Open creates a connection pool, pings the server, runs any pending
// schema migrations, and returns a ready-to-use Store.
func Open(ctx context.Context, url string) (*Store, error) {
    // (use the body from the architecture section above)
}

func (s *Store) Close() {
    s.pool.Close()
}

func (s *Store) Players() persist.PlayerRepository { return &playerRepo{pool: s.pool} }
func (s *Store) Market()  persist.MarketRepository { return &marketRepo{pool: s.pool} }
func (s *Store) Config()  persist.ConfigRepository { return &configRepo{pool: s.pool} }
```

(playerRepo, marketRepo, configRepo are defined as empty stub structs at this point — methods land in T4. The compile-time interface assertions can be deferred to T4 too.)

- [ ] **Step 4: Verify**

```bash
go vet ./...
```

- [ ] **Step 5: Local migration smoke test**

```bash
just db-up
# write a one-off test program or just rely on T4's tests
```

(If nothing else, ensure the embedded SQL parses by running the up.sql against psql manually:)

```bash
docker compose exec -T postgres psql -U mmo -d mmo < pkg/persist/postgres/migrations/001_init.up.sql
docker compose exec -T postgres psql -U mmo -d mmo -c "\dt"
docker compose exec -T postgres psql -U mmo -d mmo < pkg/persist/postgres/migrations/001_init.down.sql
```

- [ ] **Step 6: Commit**

```
feat(persist/postgres): connection pool + schema migrations

postgres.Open initializes a pgxpool.Pool with game-server-tuned
defaults (4 conns per CPU, capped at 32; 30m lifetime; 5m idle).
Pings the server and runs golang-migrate against the embedded
migrations/*.sql files transactionally before returning.

001_init.up.sql creates the players, game_config, market_orders,
and market_trades tables with the hybrid relational + JSONB schema.
Hot fields (cell_id, pos_x, pos_y, last_login, owner, location_id)
are typed columns with indexes; sparse maps (currencies, cargo,
bank, equipment) live in JSONB.
```

---

### Task 4: Repository implementations

**Files:** `pkg/persist/postgres/player_repo.go`, `pkg/persist/postgres/market_repo.go`, `pkg/persist/postgres/config_repo.go`

- [ ] **Step 1: `playerRepo`** — implements PlayerRepository

```go
type playerRepo struct {
    pool *pgxpool.Pool
}

var _ persist.PlayerRepository = (*playerRepo)(nil)

func (r *playerRepo) Load(ctx context.Context, username string) (*persist.PlayerSnapshot, error) {
    // SELECT username, cell_id, pos_x, pos_y, currencies, cargo, bank, equipment, created_at, last_login
    // FROM players WHERE username = $1
    // Decode JSONB columns into Go maps/structs. Return ErrNotFound on pgx.ErrNoRows.
}

func (r *playerRepo) LoadAll(ctx context.Context, fn func(*persist.PlayerSnapshot) error) error {
    // SELECT ... FROM players  -- streaming via pgx.Rows
    // For each row, decode and call fn.
}

func (r *playerRepo) SaveBatch(ctx context.Context, snapshots []*persist.PlayerSnapshot) error {
    if len(snapshots) == 0 {
        return nil
    }
    batch := &pgx.Batch{}
    for _, s := range snapshots {
        currenciesJSON, _ := json.Marshal(s.Currencies)
        cargoJSON, _ := json.Marshal(s.Cargo)
        bankJSON, _ := json.Marshal(s.Bank)
        equipmentJSON, _ := json.Marshal(s.Equipment)
        batch.Queue(`
            INSERT INTO players (username, cell_id, pos_x, pos_y, currencies, cargo, bank, equipment, created_at, last_login, updated_at)
            VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW())
            ON CONFLICT (username) DO UPDATE SET
                cell_id    = EXCLUDED.cell_id,
                pos_x      = EXCLUDED.pos_x,
                pos_y      = EXCLUDED.pos_y,
                currencies = EXCLUDED.currencies,
                cargo      = EXCLUDED.cargo,
                bank       = EXCLUDED.bank,
                equipment  = EXCLUDED.equipment,
                last_login = EXCLUDED.last_login,
                updated_at = NOW()
        `, s.Username, s.CellID, s.PosX, s.PosY, currenciesJSON, cargoJSON, bankJSON, equipmentJSON, s.CreatedAt, s.LastLogin)
    }
    br := r.pool.SendBatch(ctx, batch)
    defer br.Close()
    for i := range snapshots {
        if _, err := br.Exec(); err != nil {
            return fmt.Errorf("upsert player %q: %w", snapshots[i].Username, err)
        }
    }
    return nil
}
```

- [ ] **Step 2: `marketRepo`** — implements MarketRepository

```go
func (r *marketRepo) PlaceOrder(ctx context.Context, o *persist.OrderRecord) (uint64, error) {
    var id uint64
    err := r.pool.QueryRow(ctx, `
        INSERT INTO market_orders (side, owner, location_id, item_id, price, quantity, orig_qty, created_at, expires_at)
        VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
        RETURNING id
    `, o.Side, o.Owner, o.LocationID, o.ItemID, o.Price, o.Quantity, o.OrigQty, o.CreatedAt, nullableTime(o.ExpiresAt)).Scan(&id)
    if err != nil {
        return 0, fmt.Errorf("place order: %w", err)
    }
    return id, nil
}

func (r *marketRepo) UpdateQuantity(ctx context.Context, id uint64, newQty int32) error {
    _, err := r.pool.Exec(ctx, `UPDATE market_orders SET quantity = $1 WHERE id = $2`, newQty, id)
    return err
}

func (r *marketRepo) DeleteOrder(ctx context.Context, id uint64) error {
    _, err := r.pool.Exec(ctx, `DELETE FROM market_orders WHERE id = $1`, id)
    return err
}

func (r *marketRepo) RecordTrade(ctx context.Context, t *persist.TradeRecord) error {
    _, err := r.pool.Exec(ctx, `
        INSERT INTO market_trades (item_id, location_id, price, quantity, buyer, seller, occurred_at)
        VALUES ($1, $2, $3, $4, $5, $6, $7)
    `, t.ItemID, t.LocationID, t.Price, t.Quantity, t.Buyer, t.Seller, t.OccurredAt)
    return err
}

func (r *marketRepo) LoadActiveOrders(ctx context.Context, fn func(*persist.OrderRecord) error) error {
    rows, err := r.pool.Query(ctx, `
        SELECT id, side, owner, location_id, item_id, price, quantity, orig_qty, created_at, expires_at
        FROM market_orders
        WHERE expires_at IS NULL OR expires_at > NOW()
        ORDER BY id
    `)
    if err != nil {
        return err
    }
    defer rows.Close()
    for rows.Next() {
        var rec persist.OrderRecord
        var expiresAt sql.NullTime
        if err := rows.Scan(&rec.ID, &rec.Side, &rec.Owner, &rec.LocationID, &rec.ItemID, &rec.Price, &rec.Quantity, &rec.OrigQty, &rec.CreatedAt, &expiresAt); err != nil {
            return err
        }
        if expiresAt.Valid {
            rec.ExpiresAt = expiresAt.Time
        }
        if err := fn(&rec); err != nil {
            return err
        }
    }
    return rows.Err()
}
```

- [ ] **Step 3: `configRepo`** — implements ConfigRepository

```go
func (r *configRepo) Load(ctx context.Context) (*persist.ConfigSnapshot, error) {
    var data []byte
    var version int64
    err := r.pool.QueryRow(ctx, `SELECT data, version FROM game_config WHERE id = 1`).Scan(&data, &version)
    if err == pgx.ErrNoRows {
        return nil, persist.ErrNotFound
    }
    if err != nil {
        return nil, err
    }
    return &persist.ConfigSnapshot{Data: data, Version: version}, nil
}

func (r *configRepo) Save(ctx context.Context, snapshot *persist.ConfigSnapshot) error {
    _, err := r.pool.Exec(ctx, `
        INSERT INTO game_config (id, data, version, updated_at)
        VALUES (1, $1, $2, NOW())
        ON CONFLICT (id) DO UPDATE SET
            data       = EXCLUDED.data,
            version    = EXCLUDED.version,
            updated_at = NOW()
    `, snapshot.Data, snapshot.Version)
    return err
}
```

- [ ] **Step 4: Verify**

```bash
go vet ./...
```

- [ ] **Step 5: Commit**

```
feat(persist/postgres): PlayerRepository, MarketRepository, ConfigRepository

playerRepo.SaveBatch uses pgx.Batch for single-round-trip multi-row
upserts via INSERT ... ON CONFLICT DO UPDATE. Caller is expected to
sort snapshots by username (the persist.PlayerRepository contract)
to prevent deadlocks under concurrent batches.

marketRepo.PlaceOrder uses RETURNING id to surface the BIGSERIAL-
assigned ID, replacing the legacy "next_id counter in a row" write
amplification pattern.

configRepo upserts a single row enforced by the game_config table's
CHECK constraint.
```

---

### Task 5: Postgres integration tests

**File:** `pkg/persist/postgres/postgres_test.go` (build tag `pgtest`)

- [ ] **Step 1: Test scaffolding**

```go
//go:build pgtest

package postgres

import (
    "context"
    "os"
    "testing"
)

func openTestStore(t *testing.T) *Store {
    t.Helper()
    url := os.Getenv("POSTGRES_URL")
    if url == "" {
        t.Skip("POSTGRES_URL not set; skipping Postgres integration test")
    }
    s, err := Open(context.Background(), url)
    if err != nil {
        t.Fatalf("Open: %v", err)
    }
    t.Cleanup(s.Close)
    // Wipe relevant tables so each test starts clean. Order matters
    // for foreign keys (none here, but be explicit).
    _, _ = s.pool.Exec(context.Background(), `
        TRUNCATE players, game_config, market_orders, market_trades RESTART IDENTITY
    `)
    return s
}
```

- [ ] **Step 2: Player tests**

```go
func TestPlayerRepo_RoundTrip(t *testing.T) { ... }
func TestPlayerRepo_LoadNotFound(t *testing.T) { ... }
func TestPlayerRepo_LoadAll(t *testing.T) { ... }
func TestPlayerRepo_SaveBatchMultiple(t *testing.T) { ... }
func TestPlayerRepo_SaveBatchEmpty(t *testing.T) { ... }
```

Cover: insert, update via second SaveBatch, JSONB round-trip for currencies/cargo/bank/equipment, empty maps, large maps (~100 items), multi-byte usernames.

- [ ] **Step 3: Market tests**

```go
func TestMarketRepo_PlaceOrderAssignsID(t *testing.T) { ... }
func TestMarketRepo_UpdateQuantity(t *testing.T) { ... }
func TestMarketRepo_DeleteOrder(t *testing.T) { ... }
func TestMarketRepo_RecordTrade(t *testing.T) { ... }
func TestMarketRepo_LoadActiveOrdersFiltersExpired(t *testing.T) { ... }
func TestMarketRepo_LoadActiveOrdersReturnsAll(t *testing.T) { ... }
```

Verify the BIGSERIAL is monotonic across calls; verify expired orders are filtered.

- [ ] **Step 4: Config tests**

```go
func TestConfigRepo_LoadEmpty(t *testing.T) { ... }
func TestConfigRepo_RoundTrip(t *testing.T) { ... }
func TestConfigRepo_SingletonEnforcement(t *testing.T) { ... }
```

The singleton test attempts a direct `INSERT ... (id=2)` and expects the CHECK constraint to reject it.

- [ ] **Step 5: Run**

```bash
just db-up
just test-pg
```

All tests must pass. If anything fails, FIX before committing.

- [ ] **Step 6: Commit**

```
test(persist/postgres): integration tests under -tags=pgtest

Comprehensive round-trip tests for PlayerRepository, MarketRepository,
and ConfigRepository against a live Postgres. Gated on POSTGRES_URL
env var so regular `go test ./...` runs clean without a database.

Run via `just test-pg` which sets POSTGRES_URL pointing at the
docker-compose Postgres.
```

---

### Task 6: Game-domain rewrite — PlayerFlusher + PlayerRepo

**Files:** `internal/game/player_flusher.go` (new), `internal/game/playerdb.go` (rewrite)

- [ ] **Step 1: Create `internal/game/player_flusher.go`** (use the PlayerFlusher signature from the architecture section above; full implementation)

- [ ] **Step 2: Rewrite `internal/game/playerdb.go`**

- Remove `*mmokit.AsyncWriter` field; replace with `*PlayerFlusher`
- `LoadAll(store)` becomes `LoadAll(repo persist.PlayerRepository)` and uses `repo.LoadAll`
- `MarkDirty(username)` continues to flag the in-memory map; on `FlushDirty`, snapshot dirty `PlayerData` → `*persist.PlayerSnapshot`, call `flusher.Mark`, then `flusher.Flush(ctx)`
- Add `func (r *PlayerRepo) snapshot(pd *PlayerData) *persist.PlayerSnapshot` helper that does the type translation (cell_X_Y derivation, equipment struct, etc.)
- Remove every reference to `mmokit.OpenBolt`, `mmokit.AsyncWriter`, `persist.Op`

- [ ] **Step 3: Update game-domain tests that use a mock store**

Replace any `mockStore` references with `persisttest.NewPlayerRepoMock()` (which we'll create in T8).

For now, pass a small inline `&mockPlayerRepo{}` if needed.

- [ ] **Step 4: Verify**

```bash
go vet ./...
go test -count=1 ./internal/game/...
```

These tests will likely fail to compile until T7-T8 land. That's OK at the end of T6 if you've staged the change incrementally — but you SHOULD aim to get `go vet ./...` clean, even if some test files are temporarily disabled. Use `t.Skip` or build tags if needed; never leave compile errors.

If you're stuck on test compile issues, escalate before continuing — don't paper over with build tag exclusions.

- [ ] **Step 5: Commit**

```
refactor(game): PlayerRepo + PlayerFlusher use persist.PlayerRepository

internal/game/playerdb.go is rewritten to depend on the typed
persist.PlayerRepository interface instead of the deleted generic
KV Store. PlayerFlusher (new) owns dirty-tracking and submits
batched upserts via pgx.Batch. The old AsyncWriter coupling is
gone.

PlayerData ↔ persist.PlayerSnapshot translation lives in a private
helper on PlayerRepo so storage representation can evolve
independently from the in-memory game type.
```

---

### Task 7: Game-domain rewrite — Marketplace + Config

**Files:** `internal/marketplace/settlement.go`, `internal/marketplace/persist.go` (DELETE), `internal/game/config.go`

- [ ] **Step 1: Settlement rewrite**

- Add `market persist.MarketRepository` field to `Settlement`
- Replace every `st.persistOrder(...)` / `st.deletePersistOrder(...)` / `st.persistTrade(...)` / `st.persistNextID(...)` call with the corresponding repo method
- Order ID is now assigned by the DB sequence — remove the in-memory `nextID` counter and the `LoadAll` next-id seeding
- `LoadAll` becomes `LoadActiveOrders(ctx, fn)` that calls `market.LoadActiveOrders` and rebuilds the in-memory book

- [ ] **Step 2: Delete `internal/marketplace/persist.go`**

- [ ] **Step 3: Config rewrite**

- `LoadConfig(repo persist.ConfigRepository)` — calls `repo.Load`, JSON-unmarshals the `Data` bytes into `*GameConfig`, returns defaults on `ErrNotFound`
- `SaveConfig(repo, cfg)` — JSON-marshal `cfg`, call `repo.Save(&persist.ConfigSnapshot{Data: bytes, Version: cfg.Version})`

- [ ] **Step 4: Verify**

```bash
go vet ./...
go test -count=1 ./internal/...
```

Settlement tests use `mockBank` and a `nil` repo (synchronous calls just return early on nil). Update the nil-guards if you removed them.

- [ ] **Step 5: Commit**

```
refactor(marketplace,game/config): use typed persist repositories

Settlement now holds a persist.MarketRepository and calls PlaceOrder /
UpdateQuantity / DeleteOrder / RecordTrade / LoadActiveOrders directly.
Order IDs are assigned by the BIGSERIAL sequence in market_orders;
the legacy in-memory next_id counter and persistNextID write
amplification are deleted.

LoadConfig/SaveConfig switch to ConfigRepository. The single-row
game_config table replaces the BoltDB "config" bucket.

internal/marketplace/persist.go is deleted (no replacement —
direct repo calls subsume it).
```

---

### Task 8: Mock repositories for game-domain tests

**File:** `pkg/persist/persisttest/mock.go`

- [ ] **Step 1: Mock implementations**

```go
package persisttest

import (
    "context"
    "sort"
    "sync"

    "github.com/zenion/mmoserver/pkg/persist"
)

// PlayerRepoMock is an in-memory PlayerRepository for game-domain tests.
type PlayerRepoMock struct {
    mu    sync.Mutex
    rows  map[string]*persist.PlayerSnapshot
}

func NewPlayerRepoMock() *PlayerRepoMock {
    return &PlayerRepoMock{rows: make(map[string]*persist.PlayerSnapshot)}
}

func (m *PlayerRepoMock) Load(ctx context.Context, username string) (*persist.PlayerSnapshot, error) {
    m.mu.Lock()
    defer m.mu.Unlock()
    rec, ok := m.rows[username]
    if !ok {
        return nil, persist.ErrNotFound
    }
    cp := *rec // shallow copy; deep-copy maps
    return &cp, nil
}

func (m *PlayerRepoMock) LoadAll(ctx context.Context, fn func(*persist.PlayerSnapshot) error) error {
    m.mu.Lock()
    defer m.mu.Unlock()
    keys := make([]string, 0, len(m.rows))
    for k := range m.rows {
        keys = append(keys, k)
    }
    sort.Strings(keys)
    for _, k := range keys {
        if err := fn(m.rows[k]); err != nil {
            return err
        }
    }
    return nil
}

func (m *PlayerRepoMock) SaveBatch(ctx context.Context, snapshots []*persist.PlayerSnapshot) error {
    m.mu.Lock()
    defer m.mu.Unlock()
    for _, s := range snapshots {
        cp := *s
        m.rows[s.Username] = &cp
    }
    return nil
}
```

(MarketRepoMock and ConfigRepoMock follow the same pattern — small structs, minimal locking, persistence semantics that mirror the real repo's contract.)

- [ ] **Step 2: Migrate game-domain tests to use mocks**

Find every test that previously used `mockStore` from `pkg/persist/persist_test.go` (it's already deleted at this point — the tests were the primary callers). Update to use `persisttest.NewPlayerRepoMock()` etc.

- [ ] **Step 3: Verify**

```bash
go vet ./...
go test -count=1 ./...
```

Full test suite green (without Postgres — the mock covers the game-domain side).

- [ ] **Step 4: Commit**

```
test(persisttest): in-memory mock repositories for game-domain tests

PlayerRepoMock, MarketRepoMock, ConfigRepoMock implement the persist
interfaces in-memory so internal/game and internal/marketplace tests
can run without Postgres. Real DB integration coverage lives in
pkg/persist/postgres/postgres_test.go (gated on -tags=pgtest).
```

---

### Task 9: Wire everything in main.go + delete BoltDB

**Files:** `cmd/server/main.go`, `pkg/persist/store.go` (DELETE), `pkg/persist/bbolt.go` (DELETE), `pkg/persist/writer.go` (DELETE), `pkg/persist/persist_test.go` (DELETE), `pkg/mmokit/mmokit.go`, `pkg/universe/coordinator.go`

- [ ] **Step 1: Add `Config.PostgresURL`** to `pkg/universe/coordinator.go`:

```go
// PostgresURL is the connection string for player persistence.
// Format: postgres://user:pass@host:port/dbname?sslmode=disable
// Defaults to the local docker-compose instance if empty.
PostgresURL string
```

Apply the default in `NewCoordinator`:

```go
if cfg.PostgresURL == "" {
    cfg.PostgresURL = "postgres://mmo:mmo@localhost:5432/mmo?sslmode=disable"
}
```

- [ ] **Step 2: Update `pkg/mmokit/mmokit.go`**

- Delete `OpenBolt = persist.OpenBolt`
- Add `OpenPostgres = postgres.Open` (importing `pkg/persist/postgres`)
- Re-export the new types: `PlayerRepository = persist.PlayerRepository`, `MarketRepository`, `ConfigRepository`, `PlayerSnapshot`, `OrderRecord`, `TradeRecord`, `ConfigSnapshot`, `ErrNotFound`
- Delete the `Store`, `AsyncWriter`, `Op`, `BoltStore` re-exports

- [ ] **Step 3: Rewrite `cmd/server/main.go`**

- Replace the two `mmokit.OpenBolt` calls with one `mmokit.OpenPostgres(ctx, cfg.PostgresURL)`
- Replace the two `mmokit.NewAsyncWriter` + `Start` calls with: nothing (PlayerFlusher is created by `internal/game` wiring; marketplace is synchronous)
- Pass `store.Players()`, `store.Market()`, `store.Config()` into game/marketplace/config wiring
- Replace the shutdown flush sequence with: call `flusher.Flush(ctx)` synchronously, then `store.Close()`
- Remove `data/gameserver.db` and `data/marketplace.db` references; the new connection URL replaces them

- [ ] **Step 4: Delete dead files**

```bash
rm pkg/persist/store.go
rm pkg/persist/bbolt.go
rm pkg/persist/writer.go
rm pkg/persist/persist_test.go
```

- [ ] **Step 5: Remove bbolt from go.mod**

```bash
go mod tidy
grep bbolt go.mod  # should return nothing
```

- [ ] **Step 6: Update other examples**

Check `examples/*/main.go` for any `OpenBolt` / `mmokit.OpenBolt` usage. The `4node-basic` example doesn't currently use persistence (verified in the audit), but if any other example does, swap to `OpenPostgres` or remove the dependency.

- [ ] **Step 7: Verify**

```bash
just db-up
go vet ./...
go test -count=1 ./...        # without Postgres — mocks should cover game-domain tests
just test-pg                   # with Postgres — full integration suite
just build
```

All four must pass.

- [ ] **Step 8: Smoke test**

```bash
just db-reset
./bin/server &
# in another shell, connect a web client, log in, move around, disconnect, verify state survives a server restart
```

(Manual; document any issues but don't gate the commit on it.)

- [ ] **Step 9: Commit**

```
feat(persist): swap BoltDB for PostgreSQL via typed repositories

Deletes pkg/persist/{store.go,bbolt.go,writer.go,persist_test.go}
along with the entire generic KV abstraction and the bbolt
dependency. cmd/server/main.go now opens a single
pkg/persist/postgres.Store and passes typed repository handles
(Players(), Market(), Config()) to the game and marketplace wiring.

mmokit.OpenBolt is gone; mmokit.OpenPostgres is the new entry
point. Dev databases under data/*.db are obsolete — use
`just db-up` for the local docker-compose Postgres instance.

Per the project's no-backwards-compat preference there is no
fallback path: every mmoserver deployment now needs a Postgres
at Build time.
```

---

### Task 10: Final verification + CLAUDE.md docs

**Files:** `CLAUDE.md`

- [ ] **Step 1: Update CLAUDE.md Persistence section**

Replace the old "Memory-first with async writes... BoltDB" paragraph with the template from the "What goes in CLAUDE.md after S5" section above.

Also update the Package Layout section:
- `pkg/persist/` description changes from "Store interface + BoltStore + AsyncWriter" to "domain repository interfaces (PlayerRepository, MarketRepository, ConfigRepository) + Postgres implementation under pkg/persist/postgres"

Also remove any references to `mmokit.OpenBolt` in code examples, replacing with `mmokit.OpenPostgres`.

- [ ] **Step 2: Full suite verification**

```bash
just db-reset
go vet ./...
go test -count=1 ./...
just test-pg
just build
```

All must pass.

- [ ] **Step 3: Commit**

```
docs: S5 Postgres persistence architecture in CLAUDE.md
```

---

## Verification checklist

- [ ] `go vet ./...` clean
- [ ] `go test -count=1 ./...` all pass without Postgres (mocks cover game-domain tests)
- [ ] `just test-pg` all pass with Postgres
- [ ] `just build` produces `bin/server`
- [ ] `bbolt` removed from `go.mod`
- [ ] `pkg/persist/{store.go, bbolt.go, writer.go, persist_test.go}` deleted
- [ ] `internal/marketplace/persist.go` deleted
- [ ] `mmokit.OpenBolt` no longer exists; `mmokit.OpenPostgres` is the entry point
- [ ] `pkg/persist/repository.go` defines `PlayerRepository`, `MarketRepository`, `ConfigRepository` with no generic KV remnants
- [ ] `pkg/persist/postgres/Open` runs migrations automatically before returning
- [ ] `playerRepo.SaveBatch` uses `pgx.Batch`
- [ ] `marketRepo.PlaceOrder` returns the BIGSERIAL-assigned ID (no in-memory next_id counter)
- [ ] `internal/game/PlayerFlusher` sorts dirty snapshots by username before submitting each batch
- [ ] `docker-compose.yml` + `just db-*` recipes work
- [ ] CLAUDE.md persistence section rewritten

## Out of scope (deferred)

- **Per-field dirty tracking + `SavePartial`.** Optimization: today the entire PlayerSnapshot is rewritten on each flush. A future task can add `DirtyFlags` (Position / Credits / Inventory / Skills) and call separate `UPDATE` statements for the relevant columns only. Not needed for v1.
- **Cell state persistence (NPCs, asteroids, loot crates).** S5 only persists player + market + config. Crash recovery still loses ephemeral entities. Add a `cell_state` table later if needed.
- **`FlushCell` integration with the handoff path.** The `PlayerFlusher.FlushCell` method ships in T6 but no caller invokes it yet. S6 (or S7 cell migration) wires it up.
- **Trade history query API.** `market_trades` table is indexed for buyer/seller/item lookups but no HTTP/gRPC endpoint queries it. Add when a UI needs it.
- **Net-ID range grant persistence.** Spec mentions wanting this so a host with the same `--host-id` gets the same range after restart. Defer to S5.5 or S7.
- **Read replicas / PITR / multi-region.** Operational concerns; out of mmokit scope.
- **Migration rollback automation.** `001_init.down.sql` exists but nothing calls it programmatically.
- **`synchronous_commit = off` per-session for soft writes.** Performance optimization; only matters under heavy load. Can be added per-pool by setting a session GUC in `AfterConnect`.
- **`pg_advisory_lock` for multi-coordinator deploys.** Single-coordinator works without it; revisit if/when we run multiple coordinators.

---

## Risk notes

- **Dev database wipe.** Anyone running `feature/distributed-mesh` with existing `data/*.db` files loses that state on the first run after S5 lands. The plan accepts this — the user's preference is no backward compat. Document loudly in the commit message and CLAUDE.md.
- **golang-migrate pgx/v5 driver maturity.** The `database/pgx/v5` driver landed in 2024 but the ecosystem is still small. If it has bugs, T1 escalates to the controller and the fallback is a 30-line custom embedded runner like the original plan proposed. The custom path is fine; the typed library is just preferred.
- **`pgx.Batch` deadlock under concurrent flushes.** If two `PlayerFlusher.Flush` calls race (which shouldn't happen — the game tick flushes serially — but could under tests or admin commands), the sort-by-username guard prevents Postgres-level deadlocks. Verify the sort happens BEFORE the batch is built.
- **Settlement synchronous calls block worker goroutines.** Marketplace operations now do a ~1-3ms Postgres round-trip per place/fill/cancel. Acceptable: these events are infrequent compared to game ticks. If the orderbook becomes a hot path later, consider an async outbox pattern.
- **JSONB decoding error on schema drift.** If a future code change adds a field to `PlayerData` and doesn't update `PlayerSnapshot`, the JSON unmarshal will silently drop the new field. Mitigation: always update both types in the same commit; consider a struct-tag linter later.

---

## Approval gate

Before executing, the user reviews this plan and confirms one of:

1. **Approved as-is** → proceed to T1.
2. **Approved with changes** → list the changes, update the plan, then proceed.
3. **Defer S5 entirely** → skip to S6 and revisit later.

If approved, the plan replaces the S5 portion of `2026-04-13-S4.5-S5-S6-distributed-mesh-continuation.md`. The earlier draft is kept for historical context but should be marked superseded with a banner pointing here.
