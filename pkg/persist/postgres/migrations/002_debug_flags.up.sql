ALTER TABLE players ADD COLUMN debug_flags JSONB NOT NULL DEFAULT '[]'::jsonb;
