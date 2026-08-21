-- Player height, for processes running the 3D dimension profile.
--
-- Without it a 3D player's Z is lost across logout and reconnect: they
-- respawn at ground level with no error anywhere, which is the same class of
-- silent drop the mesh transfer frame had before phase 2 unit 1.
--
-- DEFAULT 0 makes this free for the 2D profile, which never writes it, and
-- makes every existing row correct rather than merely non-null.
ALTER TABLE engine.players
    ADD COLUMN IF NOT EXISTS pos_z REAL NOT NULL DEFAULT 0;
