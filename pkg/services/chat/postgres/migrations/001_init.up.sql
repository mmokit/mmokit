CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE IF NOT EXISTS chat_channels (
  channel_id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  slug                TEXT NOT NULL UNIQUE,
  kind                TEXT NOT NULL,
  topic               TEXT NOT NULL DEFAULT '',
  slow_mode_seconds   INT  NOT NULL DEFAULT 0,
  password_hash       TEXT,
  owner_user_id       UUID,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  metadata            JSONB
);

CREATE INDEX IF NOT EXISTS chat_channels_kind ON chat_channels(kind);

CREATE TABLE IF NOT EXISTS chat_channel_members (
  channel_id      UUID NOT NULL REFERENCES chat_channels(channel_id) ON DELETE CASCADE,
  user_id         UUID NOT NULL,
  role            TEXT NOT NULL DEFAULT 'member',
  joined_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  banned_until    TIMESTAMPTZ,
  banned_by       UUID,
  banned_reason   TEXT,
  PRIMARY KEY (channel_id, user_id)
);

CREATE INDEX IF NOT EXISTS chat_members_user ON chat_channel_members(user_id);
CREATE INDEX IF NOT EXISTS chat_members_banned ON chat_channel_members(banned_until)
  WHERE banned_until IS NOT NULL;

CREATE TABLE IF NOT EXISTS chat_mutes (
  user_id     UUID NOT NULL,
  channel_id  UUID NOT NULL,
  expires_at  TIMESTAMPTZ NOT NULL,
  reason      TEXT,
  muted_by    UUID NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (user_id, channel_id)
);

CREATE INDEX IF NOT EXISTS chat_mutes_expiry ON chat_mutes(expires_at);
