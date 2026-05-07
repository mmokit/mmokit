CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS auth_users (
  user_id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  username         TEXT NOT NULL UNIQUE,
  email            TEXT,
  email_verified   BOOLEAN NOT NULL DEFAULT FALSE,
  mfa_secret       BYTEA,
  mfa_enabled      BOOLEAN NOT NULL DEFAULT FALSE,
  status           TEXT NOT NULL DEFAULT 'active',
  failed_attempts  INT NOT NULL DEFAULT 0,
  locked_until     TIMESTAMPTZ,
  last_login_at    TIMESTAMPTZ,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS auth_passwords (
  user_id        UUID PRIMARY KEY REFERENCES auth_users(user_id) ON DELETE CASCADE,
  password_hash  TEXT NOT NULL,
  hash_algorithm TEXT NOT NULL DEFAULT 'argon2id',
  changed_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS auth_identities (
  provider      TEXT NOT NULL,
  subject       TEXT NOT NULL,
  user_id       UUID NOT NULL REFERENCES auth_users(user_id) ON DELETE CASCADE,
  email         TEXT,
  display_name  TEXT,
  linked_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (provider, subject)
);
CREATE INDEX IF NOT EXISTS auth_identities_user ON auth_identities(user_id);

CREATE TABLE IF NOT EXISTS auth_sessions (
  token_hash       BYTEA PRIMARY KEY,
  user_id          UUID NOT NULL REFERENCES auth_users(user_id) ON DELETE CASCADE,
  issued_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  expires_at       TIMESTAMPTZ NOT NULL,
  last_used_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  revoked_at       TIMESTAMPTZ,
  client_meta      JSONB
);
CREATE INDEX IF NOT EXISTS auth_sessions_user_active ON auth_sessions(user_id) WHERE revoked_at IS NULL;
CREATE INDEX IF NOT EXISTS auth_sessions_expiry      ON auth_sessions(expires_at) WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS auth_audit_log (
  audit_id            BIGSERIAL PRIMARY KEY,
  occurred_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  event               TEXT NOT NULL,
  user_id             UUID REFERENCES auth_users(user_id) ON DELETE SET NULL,
  username_attempted  TEXT,
  ip_addr             INET,
  user_agent          TEXT,
  gateway_id          TEXT,
  metadata            JSONB
);
CREATE INDEX IF NOT EXISTS auth_audit_user_recent  ON auth_audit_log(user_id, occurred_at DESC);
CREATE INDEX IF NOT EXISTS auth_audit_event_recent ON auth_audit_log(event, occurred_at DESC);
