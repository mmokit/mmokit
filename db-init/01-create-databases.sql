-- Postgres container init: creates per-deployment databases owned by
-- the `mmo` user. Runs ONCE on a fresh volume (postgres-image
-- convention: scripts in /docker-entrypoint-initdb.d/ execute on first
-- container boot only, then are skipped on every subsequent up).
--
-- If you add a database here AFTER your local volume has already been
-- created, you must `just db-reset` (wipes the volume) to pick it up.
-- Alternatively, manually run the relevant CREATE DATABASE via
-- `just db-psql`.

CREATE DATABASE mmo_4node OWNER mmo;
