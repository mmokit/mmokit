-- Players: hybrid relational + JSONB.
-- Hot fields (cell_id, pos_x, pos_y, last_login) are indexable columns;
-- sparse/evolving structures (currencies, cargo, bank, equipment) are JSONB.
CREATE TABLE IF NOT EXISTS players (
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

CREATE INDEX IF NOT EXISTS players_cell_id_idx     ON players(cell_id);
CREATE INDEX IF NOT EXISTS players_last_login_idx  ON players(last_login DESC);

-- Game config: single-row table for the live game balance config.
-- The CHECK constraint enforces "exactly one row" — no race conditions
-- on multi-coordinator deploys.
CREATE TABLE IF NOT EXISTS game_config (
    id          INTEGER     PRIMARY KEY DEFAULT 1,
    data        BYTEA       NOT NULL,
    version     BIGINT      NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT  game_config_singleton CHECK (id = 1)
);

-- Marketplace orders: typed columns for indexed lookups.
-- IDs are owned by the application — the in-memory orderbook
-- allocates them and persists explicitly. On startup the orderbook
-- seeds its counter from MAX(id), avoiding the legacy "next_id
-- counter row" write amplification without giving the DB a sequence.
CREATE TABLE IF NOT EXISTS market_orders (
    id           BIGINT      PRIMARY KEY,
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

CREATE INDEX IF NOT EXISTS market_orders_lookup_idx  ON market_orders(location_id, item_id, side, price);
CREATE INDEX IF NOT EXISTS market_orders_owner_idx   ON market_orders(owner);
CREATE INDEX IF NOT EXISTS market_orders_expires_idx ON market_orders(expires_at) WHERE expires_at IS NOT NULL;

-- Marketplace trades: append-only audit log.
-- Indexed for future "trade history" UIs but never read by today's game loop.
CREATE TABLE IF NOT EXISTS market_trades (
    id           BIGSERIAL   PRIMARY KEY,
    item_id      INTEGER     NOT NULL,
    location_id  INTEGER     NOT NULL,
    price        BIGINT      NOT NULL,
    quantity     INTEGER     NOT NULL,
    buyer        TEXT        NOT NULL,
    seller       TEXT        NOT NULL,
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS market_trades_buyer_idx   ON market_trades(buyer, occurred_at DESC);
CREATE INDEX IF NOT EXISTS market_trades_seller_idx  ON market_trades(seller, occurred_at DESC);
CREATE INDEX IF NOT EXISTS market_trades_item_idx    ON market_trades(item_id, occurred_at DESC);
