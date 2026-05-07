CREATE TABLE IF NOT EXISTS auth_capabilities (
  user_id     UUID NOT NULL REFERENCES auth_users(user_id) ON DELETE CASCADE,
  capability  TEXT NOT NULL,
  granted_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  granted_by  UUID NOT NULL,
  expires_at  TIMESTAMPTZ,
  PRIMARY KEY (user_id, capability)
);

CREATE INDEX IF NOT EXISTS auth_capabilities_user ON auth_capabilities(user_id);
CREATE INDEX IF NOT EXISTS auth_capabilities_expiry ON auth_capabilities(expires_at)
  WHERE expires_at IS NOT NULL;
