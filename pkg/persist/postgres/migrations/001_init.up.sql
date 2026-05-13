CREATE SCHEMA IF NOT EXISTS engine;

-- Engine player identity. Game-specific state lives in <game>.player_state
-- in the per-game schema (e.g. space.player_state) with FK on
-- engine.players(username) ON DELETE CASCADE.
CREATE TABLE IF NOT EXISTS engine.players (
    username    TEXT        PRIMARY KEY,
    cell_id     TEXT        NOT NULL DEFAULT '',
    pos_x       REAL        NOT NULL DEFAULT 0,
    pos_y       REAL        NOT NULL DEFAULT 0,
    debug_flags JSONB       NOT NULL DEFAULT '[]'::jsonb,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_login  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS engine_players_cell_id_idx    ON engine.players(cell_id);
CREATE INDEX IF NOT EXISTS engine_players_last_login_idx ON engine.players(last_login DESC);

-- Admin dashboard operators. Username PK is lowercased by the app layer.
-- Grants is a JSONB string array using cmdsys grant syntax. password_hash
-- is the encoded argon2id string from pkg/services/auth.HashPassword.
CREATE TABLE IF NOT EXISTS engine.admin_operators (
    username      TEXT        PRIMARY KEY,
    password_hash TEXT        NOT NULL,
    grants        JSONB       NOT NULL DEFAULT '[]'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
