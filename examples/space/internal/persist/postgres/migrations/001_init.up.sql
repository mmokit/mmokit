CREATE SCHEMA IF NOT EXISTS space;

-- Per-player space-game state. Identity columns live in engine.players;
-- this table hangs off the FK with ON DELETE CASCADE so removing an
-- engine player wipes their game state automatically. Keyed by the
-- immutable user_id (UUID) so a rename in auth.users doesn't have to
-- ripple through every game schema.
CREATE TABLE IF NOT EXISTS space.player_state (
    user_id    UUID        PRIMARY KEY
                           REFERENCES engine.players(user_id) ON DELETE CASCADE,
    currencies JSONB       NOT NULL DEFAULT '{}'::jsonb,
    cargo      JSONB       NOT NULL DEFAULT '{}'::jsonb,
    bank       JSONB       NOT NULL DEFAULT '{}'::jsonb,
    equipment  JSONB       NOT NULL DEFAULT '{}'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Marketplace orders. IDs owned by the application — the in-memory
-- orderbook allocates them and persists explicitly; LoadMaxOrderID
-- seeds the counter at startup.
CREATE TABLE IF NOT EXISTS space.market_orders (
    id           BIGINT      PRIMARY KEY,
    side         SMALLINT    NOT NULL,
    owner        TEXT        NOT NULL,
    location_id  INTEGER     NOT NULL,
    item_id      INTEGER     NOT NULL,
    price        BIGINT      NOT NULL,
    quantity     INTEGER     NOT NULL,
    orig_qty     INTEGER     NOT NULL,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS space_market_orders_lookup_idx
    ON space.market_orders(location_id, item_id, side, price);
CREATE INDEX IF NOT EXISTS space_market_orders_owner_idx
    ON space.market_orders(owner);
CREATE INDEX IF NOT EXISTS space_market_orders_expires_idx
    ON space.market_orders(expires_at) WHERE expires_at IS NOT NULL;

-- Marketplace trades: append-only audit log.
CREATE TABLE IF NOT EXISTS space.market_trades (
    id           BIGSERIAL   PRIMARY KEY,
    item_id      INTEGER     NOT NULL,
    location_id  INTEGER     NOT NULL,
    price        BIGINT      NOT NULL,
    quantity     INTEGER     NOT NULL,
    buyer        TEXT        NOT NULL,
    seller       TEXT        NOT NULL,
    occurred_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS space_market_trades_buyer_idx
    ON space.market_trades(buyer, occurred_at DESC);
CREATE INDEX IF NOT EXISTS space_market_trades_seller_idx
    ON space.market_trades(seller, occurred_at DESC);
CREATE INDEX IF NOT EXISTS space_market_trades_item_idx
    ON space.market_trades(item_id, occurred_at DESC);

-- Game config: single-row table. CHECK enforces singleton.
CREATE TABLE IF NOT EXISTS space.config (
    id          INTEGER     PRIMARY KEY DEFAULT 1,
    data        BYTEA       NOT NULL,
    version     BIGINT      NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT  space_config_singleton CHECK (id = 1)
);
