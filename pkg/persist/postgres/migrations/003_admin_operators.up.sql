-- Admin dashboard operators. Username is the primary key (lowercased
-- by the application layer). Grants stored as a JSONB string array
-- following the cmdsys grant syntax. password_hash is the encoded
-- argon2id string from pkg/services/auth.HashPassword.
CREATE TABLE IF NOT EXISTS admin_operators (
    username      TEXT        PRIMARY KEY,
    password_hash TEXT        NOT NULL,
    grants        JSONB       NOT NULL DEFAULT '[]'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
