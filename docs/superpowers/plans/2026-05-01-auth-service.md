# Auth Service Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace mmokit's bespoke `LoginHandler` with a first-class engine-tier auth service running on the pluggable services framework — opaque sliding-TTL session tokens, UUID identity, OIDC schema seam, argon2id passwords, per-IP + per-account rate limit, audit log, full gateway integration. Gateway-crash → transparent reconnect.

**Architecture:** Engine-tier package `pkg/auth/` registered as a service kind; gateway gains a per-connection auth-state map + auth-op gating + a response-interception hook from `pkg/auth`. The existing `pkg/universe/login.go` is deleted entirely. PKs become UUIDs throughout (`PlayerSession.UserID`, `PlayerRouter(userID, username)`, `PlayerAssignment.user_id`). Password-only v1 with `auth_users` / `auth_passwords` / `auth_identities` / `auth_sessions` / `auth_audit_log` schema split so OIDC v2 lands additively. Reference spec: [docs/superpowers/specs/2026-05-01-auth-service-design.md](../specs/2026-05-01-auth-service-design.md).

**Tech Stack:** Go 1.23+, protobuf via `buf generate`, PostgreSQL via pgx/v5, `golang.org/x/crypto/argon2`, `github.com/google/uuid`, services framework in `pkg/service/`, mesh data plane via `gen/go/meshpb/`.

---

## File-structure overview

**New:**
- `proto/enginepb/auth.proto` — auth ops + messages
- `gen/go/enginepb/auth.pb.go` — regenerated
- `pkg/auth/` — package: kind, service, handlers, password, token, ratelimit, repo, gateway_hook, console, doc
- `pkg/auth/postgres/` — Postgres impl + migrations
- `pkg/auth/authtest/` — in-memory mock
- `pkg/mmokit/auth.go` — facade entrypoints
- `pkg/universe/auth_e2e_test.go` — cluster integration

**Modified:**
- `proto/meshpb/mesh.proto` — `PlayerAssignment` adds `user_id`, `session_token`, renumber
- `pkg/ops/router.go` — `OpContext` gets `ClientIP netip.Addr`
- `pkg/universe/coordinator.go` — `Config.LoginHandler` deletion, `Config.ExtraMigrations` add, `activeUsers` keyed by UUID
- `pkg/universe/gateway.go` — `authStates` map, op gating, response interception, post-auth dispatch
- `pkg/persist/postgres/postgres.go` — apply `Config.ExtraMigrations` after engine migrations
- `pkg/mmokit/mmokit.go` — drop `HandleLogin` / `ValidateUsername` / `LoginHandler` / `ErrLoginPending` re-exports
- `examples/4node-basic/main.go` — `RegisterAuthService` wiring instead of inline `LoginHandler`
- `examples/4node-basic/proto/basicpb/basic.proto` — delete `LoginMsg`, delete `BCE_LOGIN`
- `examples/4node-basic/web/src/*.ts` — login/register form
- `internal/game/*.go` — `ConnToUserID`, `PlayerRepo` keyed by UUID, `entity_player.go` spawn signature

**Deleted:**
- `pkg/universe/login.go` — entire file (`ErrLoginPending`, `LoginHandler`, `HandleLogin`, `ValidateUsername`, `loginService`, `pendingConn`, `processLogins`, `loginResult`, `PlayerRouter`)

---

## Phase A — Foundation (no behavior change to existing flow)

### Task 1: Auth proto definitions

**Files:**
- Create: `proto/enginepb/auth.proto`
- Regenerate: `gen/go/enginepb/auth.pb.go` (via `just proto`)
- Regenerate: `gen/csharp/Auth.cs`, `gen/es/enginepb/auth_pb.{js,d.ts}` (paths driven by `buf.gen.yaml`)

- [ ] **Step 1: Create `proto/enginepb/auth.proto`**

```proto
syntax = "proto3";
package enginepb;
option go_package = "github.com/zenion/mmoserver/gen/go/enginepb";
option csharp_namespace = "Zenion.GameServer.Proto.Engine";

// AuthOpCode are op codes for engine-provided auth operations on the
// 0x01 operations channel. Range 1-15 reserved for engine-tier auth.
enum AuthOpCode {
  AUTH_OPCODE_UNSPECIFIED         = 0;
  AUTH_OPCODE_LOGIN               = 1;
  AUTH_OPCODE_REGISTER            = 2;
  AUTH_OPCODE_VALIDATE_TOKEN      = 3;
  AUTH_OPCODE_LOGOUT              = 4;
  AUTH_OPCODE_CHANGE_PASSWORD     = 5;
  // 6-10 reserved for OIDC v2.
}

// AuthError codes ride on the op envelope status field.
enum AuthError {
  AUTH_ERROR_UNSPECIFIED         = 0;
  AUTH_ERROR_INVALID_CREDENTIALS = 1;
  AUTH_ERROR_USERNAME_TAKEN      = 2;
  AUTH_ERROR_USERNAME_INVALID    = 3;
  AUTH_ERROR_PASSWORD_TOO_WEAK   = 4;
  AUTH_ERROR_ACCOUNT_LOCKED      = 5;
  AUTH_ERROR_RATE_LIMITED        = 6;
  AUTH_ERROR_MFA_REQUIRED        = 7;
  AUTH_ERROR_MFA_INVALID         = 8;
  AUTH_ERROR_TOKEN_INVALID       = 9;
  AUTH_ERROR_TOKEN_EXPIRED       = 10;
  AUTH_ERROR_NOT_AUTHENTICATED   = 11;
  AUTH_ERROR_INTERNAL            = 12;
}

message AuthLoginRequest {
  string username = 1;
  string password = 2;
  string mfa_code = 3;  // ignored when mfa_enabled=false; v2 enforces
}
message AuthLoginResponse {
  string user_id        = 1;
  string username       = 2;
  string session_token  = 3;
  int64  expires_at_ms  = 4;
}

message AuthRegisterRequest {
  string username = 1;
  string password = 2;
  string email    = 3;
}
message AuthRegisterResponse {
  string user_id        = 1;
  string username       = 2;
  string session_token  = 3;
  int64  expires_at_ms  = 4;
}

message AuthValidateTokenRequest  { string session_token = 1; }
message AuthValidateTokenResponse {
  string user_id      = 1;
  string username     = 2;
  int64  expires_at_ms = 3;
}

message AuthLogoutRequest  {}
message AuthLogoutResponse {}

message AuthChangePasswordRequest  { string current_password = 1; string new_password = 2; }
message AuthChangePasswordResponse {}

// AuthErrorMetadata is JSON-encoded into the op envelope's error metadata
// field for ACCOUNT_LOCKED / RATE_LIMITED responses.
message AuthErrorMetadata {
  int64 retry_after_ms = 1;
  string canonical     = 2;  // canonical (lowercased) username for USERNAME_TAKEN
}
```

- [ ] **Step 2: Regenerate**

Run: `just proto`
Expected: writes `gen/go/enginepb/auth.pb.go`, `gen/csharp/.../auth.cs`, `gen/es/enginepb/auth_pb.ts`. No errors.

- [ ] **Step 3: Verify generated code compiles**

Run: `go vet ./gen/go/enginepb/...`
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add proto/enginepb/auth.proto gen/go/enginepb/auth.pb.go gen/csharp/ gen/es/
git commit -m "feat(proto): auth service op codes + messages

AUTH_LOGIN, AUTH_REGISTER, AUTH_VALIDATE_TOKEN, AUTH_LOGOUT,
AUTH_CHANGE_PASSWORD on enginepb. Codes 6-10 reserved for OIDC v2."
```

---

### Task 2: `Config.ExtraMigrations` hook

**Files:**
- Modify: `pkg/persist/postgres/postgres.go` — extend migrator chain
- Modify: `pkg/universe/coordinator.go` — `Config.ExtraMigrations fs.FS`
- Modify: `pkg/persist/postgres/postgres_test.go` — extra-migrations test

- [ ] **Step 1: Add `ExtraMigrations` field to `Config`**

In `pkg/universe/coordinator.go`, find the `Config struct` and add:

```go
// ExtraMigrations layers additional Postgres migration filesystems on
// top of the engine's built-in migrations. Engine migrations run first,
// then each entry in ExtraMigrations is applied in slice order. Used
// by pkg/auth/ to ship its own schema. Nil/empty means engine
// migrations only.
ExtraMigrations []fs.FS
```

Import `io/fs` if not present. The slice shape (vs. a single `fs.FS`) lets multiple packages each contribute their own migrations without a merge helper.

- [ ] **Step 2: Extend `mmokit.OpenPostgres` to accept extras**

In `pkg/persist/postgres/postgres.go`, change `OpenPostgres` to accept variadic `fs.FS` extras and run them after the engine migrations:

```go
func OpenPostgres(ctx context.Context, url string, extras ...fs.FS) (*Store, error) {
    // ... existing engine-migration apply code ...
    for _, extra := range extras {
        if err := applyMigrationsFromFS(ctx, pool, extra); err != nil {
            return nil, fmt.Errorf("OpenPostgres: extra migration: %w", err)
        }
    }
    return store, nil
}
```

`applyMigrationsFromFS` wraps `iofs.New(fsys, ".")` and runs the resulting source through `golang-migrate`. Each extra runs as its own migrate sequence with its own `schema_migrations` table — name the table per-extra (e.g. `schema_migrations_auth`) by passing a custom database driver config so multiple extras don't collide.

- [ ] **Step 3: Wire it into the coordinator's bootstrap**

Find where `OpenPostgres` is called in `pkg/universe/coordinator.go` (search for `OpenPostgres`). Pass `cfg.ExtraMigrations` through.

- [ ] **Step 4: Test — extra migration applies after engine**

In `pkg/persist/postgres/postgres_test.go` (build tag `pgtest`), add:

```go
//go:build pgtest

func TestExtraMigrationsAppliedAfterEngine(t *testing.T) {
    extras := fstest.MapFS{
        "001_extra.up.sql":   &fstest.MapFile{Data: []byte("CREATE TABLE extra_test (id INT PRIMARY KEY);")},
        "001_extra.down.sql": &fstest.MapFile{Data: []byte("DROP TABLE extra_test;")},
    }
    s, err := mmokit.OpenPostgres(context.Background(), testURL(t), extras)
    if err != nil { t.Fatal(err) }
    defer s.Close()
    var n int
    if err := s.Pool().QueryRow(context.Background(), "SELECT COUNT(*) FROM extra_test").Scan(&n); err != nil {
        t.Fatalf("extra_test missing: %v", err)
    }
}
```

- [ ] **Step 5: Run test**

Run: `just test-pg -run TestExtraMigrationsAppliedAfterEngine`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/persist/postgres/ pkg/universe/coordinator.go
git commit -m "feat(persist): Config.ExtraMigrations layered after engine schema

Lets pkg/ packages ship their own migrations via Config.ExtraMigrations.
Engine migrations run first, then extras alphabetical."
```

---

### Task 3: `AuthRepository` interface + DTO types

**Files:**
- Create: `pkg/auth/doc.go`
- Create: `pkg/auth/repo.go`

- [ ] **Step 1: Create `pkg/auth/doc.go`**

```go
// Package auth provides mmokit's engine-tier identity service.
//
// Auth is registered as a pluggable service kind via mmokit.RegisterAuthService.
// Every game built on mmokit gets production-grade auth:
//   - argon2id password storage
//   - opaque 256-bit session tokens with sliding TTL
//   - per-IP token-bucket rate limiting
//   - per-account lockout (DB-persistent)
//   - audit log
//   - schema seam for OIDC v2 (auth_identities table present from day one)
//
// Auth is wire-protocol-agnostic. Per-connection / per-message authentication
// (UDP AEAD, DTLS, sequence + replay window) is the gateway's concern; the
// auth service knows nothing about it.
//
// See docs/superpowers/specs/2026-05-01-auth-service-design.md for the
// full design.
package auth
```

- [ ] **Step 2: Create `pkg/auth/repo.go` with the Repository interface**

```go
package auth

import (
    "context"
    "errors"
    "net/netip"
    "time"

    "github.com/google/uuid"
)

// Errors returned by Repository implementations.
var (
    ErrUserNotFound    = errors.New("auth: user not found")
    ErrUsernameTaken   = errors.New("auth: username taken")
    ErrSessionNotFound = errors.New("auth: session not found")
)

// User is the canonical identity record. Mirrors auth_users.
type User struct {
    UserID         uuid.UUID
    Username       string  // always lowercase
    Email          string  // empty when not provided
    EmailVerified  bool
    MFAEnabled     bool
    Status         string  // "active" | "locked" | "disabled"
    FailedAttempts int
    LockedUntil    time.Time // zero value = not locked
    LastLoginAt    time.Time
    CreatedAt      time.Time
    UpdatedAt      time.Time
}

// PasswordCredential mirrors auth_passwords (one per user in v1).
type PasswordCredential struct {
    UserID        uuid.UUID
    PasswordHash  string  // argon2id encoded
    HashAlgorithm string  // 'argon2id'
    ChangedAt     time.Time
}

// Session mirrors auth_sessions.
type Session struct {
    TokenHash   []byte  // sha256(raw_token)
    UserID      uuid.UUID
    IssuedAt    time.Time
    ExpiresAt   time.Time
    LastUsedAt  time.Time
    RevokedAt   time.Time  // zero value = not revoked
    ClientMeta  map[string]string  // ip, ua, gateway_id
}

// AuditEvent mirrors auth_audit_log row inputs.
type AuditEvent struct {
    Event             string
    UserID            uuid.UUID  // zero value when unknown
    UsernameAttempted string
    IPAddr            netip.Addr  // zero value when unavailable
    UserAgent         string
    GatewayID         string
    Metadata          map[string]any
}

// Repository abstracts persistence. Postgres impl: pkg/auth/postgres.
// In-memory mock for tests: pkg/auth/authtest.
type Repository interface {
    // Users
    CreateUser(ctx context.Context, u User, password string) (User, error)
    GetUserByUsername(ctx context.Context, username string) (User, error)
    GetUserByID(ctx context.Context, userID uuid.UUID) (User, error)
    UpdateUserLogin(ctx context.Context, userID uuid.UUID, at time.Time) error
    IncrementFailedAttempts(ctx context.Context, userID uuid.UUID, lockoutThreshold int, lockoutDuration time.Duration) (newCount int, lockedUntil time.Time, err error)
    ResetFailedAttempts(ctx context.Context, userID uuid.UUID) error
    SetUserStatus(ctx context.Context, userID uuid.UUID, status string, lockedUntil time.Time) error

    // Passwords
    GetPassword(ctx context.Context, userID uuid.UUID) (PasswordCredential, error)
    UpdatePassword(ctx context.Context, userID uuid.UUID, newHash string) error

    // Sessions
    CreateSession(ctx context.Context, s Session) error
    GetSession(ctx context.Context, tokenHash []byte) (Session, error)
    SlideSession(ctx context.Context, tokenHash []byte, newExpiry time.Time) error
    RevokeSession(ctx context.Context, tokenHash []byte) error
    RevokeAllSessionsExcept(ctx context.Context, userID uuid.UUID, keepTokenHash []byte) (int, error)
    RevokeAllSessionsForUser(ctx context.Context, userID uuid.UUID) (int, error)
    ListActiveSessions(ctx context.Context, userID uuid.UUID) ([]Session, error)

    // Reaper
    DeleteExpiredSessions(ctx context.Context, retentionAfterExpiry time.Duration) (int, error)
    DeleteOldAuditRows(ctx context.Context, olderThan time.Duration) (int, error)

    // Audit
    Audit(ctx context.Context, ev AuditEvent) error
    RecentAudit(ctx context.Context, userID uuid.UUID, limit int) ([]AuditEvent, error)
}
```

- [ ] **Step 3: Verify it compiles**

Run: `go vet ./pkg/auth/...`
Expected: no errors. (The file has no callers yet.)

- [ ] **Step 4: Commit**

```bash
git add pkg/auth/doc.go pkg/auth/repo.go
git commit -m "feat(auth): Repository interface + DTOs

User, PasswordCredential, Session, AuditEvent. Repository covers
identity, credentials, sessions, audit, and reaper paths."
```

---

### Task 4: Postgres migration `001_init.sql`

**Files:**
- Create: `pkg/auth/postgres/migrations/001_init.up.sql`
- Create: `pkg/auth/postgres/migrations/001_init.down.sql`

- [ ] **Step 1: Create up migration**

`pkg/auth/postgres/migrations/001_init.up.sql`:

```sql
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE auth_users (
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

CREATE TABLE auth_passwords (
  user_id        UUID PRIMARY KEY REFERENCES auth_users(user_id) ON DELETE CASCADE,
  password_hash  TEXT NOT NULL,
  hash_algorithm TEXT NOT NULL DEFAULT 'argon2id',
  changed_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE auth_identities (
  provider      TEXT NOT NULL,
  subject       TEXT NOT NULL,
  user_id       UUID NOT NULL REFERENCES auth_users(user_id) ON DELETE CASCADE,
  email         TEXT,
  display_name  TEXT,
  linked_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (provider, subject)
);
CREATE INDEX auth_identities_user ON auth_identities(user_id);

CREATE TABLE auth_sessions (
  token_hash       BYTEA PRIMARY KEY,
  user_id          UUID NOT NULL REFERENCES auth_users(user_id) ON DELETE CASCADE,
  issued_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  expires_at       TIMESTAMPTZ NOT NULL,
  last_used_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  revoked_at       TIMESTAMPTZ,
  client_meta      JSONB
);
CREATE INDEX auth_sessions_user_active ON auth_sessions(user_id) WHERE revoked_at IS NULL;
CREATE INDEX auth_sessions_expiry      ON auth_sessions(expires_at) WHERE revoked_at IS NULL;

CREATE TABLE auth_audit_log (
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
CREATE INDEX auth_audit_user_recent  ON auth_audit_log(user_id, occurred_at DESC);
CREATE INDEX auth_audit_event_recent ON auth_audit_log(event, occurred_at DESC);
```

- [ ] **Step 2: Create down migration**

`pkg/auth/postgres/migrations/001_init.down.sql`:

```sql
DROP TABLE IF EXISTS auth_audit_log;
DROP TABLE IF EXISTS auth_sessions;
DROP TABLE IF EXISTS auth_identities;
DROP TABLE IF EXISTS auth_passwords;
DROP TABLE IF EXISTS auth_users;
```

- [ ] **Step 3: Commit**

```bash
git add pkg/auth/postgres/migrations/
git commit -m "feat(auth): Postgres schema — users, passwords, identities, sessions, audit"
```

---

### Task 5: Postgres `Repository` implementation

**Files:**
- Create: `pkg/auth/postgres/repo.go`
- Create: `pkg/auth/postgres/repo_test.go` (build tag `pgtest`)

Use existing `pkg/persist/postgres/player_repo.go` as the structural reference for query patterns.

- [ ] **Step 1: Create repo skeleton**

`pkg/auth/postgres/repo.go`:

```go
// Package postgres provides the Postgres implementation of auth.Repository.
package postgres

import (
    "context"
    "embed"
    "encoding/json"
    "errors"
    "net/netip"
    "time"

    "github.com/google/uuid"
    "github.com/jackc/pgx/v5"
    "github.com/jackc/pgx/v5/pgxpool"

    "github.com/zenion/mmoserver/pkg/auth"
)

//go:embed migrations/*.sql
var Migrations embed.FS

type Repo struct {
    pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

var _ auth.Repository = (*Repo)(nil)
```

- [ ] **Step 2: Implement user methods**

Add to `pkg/auth/postgres/repo.go`:

```go
func (r *Repo) CreateUser(ctx context.Context, u auth.User, password string) (auth.User, error) {
    var out auth.User
    tx, err := r.pool.Begin(ctx)
    if err != nil { return out, err }
    defer tx.Rollback(ctx)

    err = tx.QueryRow(ctx, `
        INSERT INTO auth_users (username, email)
        VALUES ($1, NULLIF($2, ''))
        RETURNING user_id, username, COALESCE(email,''), email_verified, mfa_enabled,
                  status, failed_attempts, COALESCE(locked_until, 'epoch'::timestamptz),
                  COALESCE(last_login_at, 'epoch'::timestamptz), created_at, updated_at
    `, u.Username, u.Email).Scan(
        &out.UserID, &out.Username, &out.Email, &out.EmailVerified, &out.MFAEnabled,
        &out.Status, &out.FailedAttempts, &out.LockedUntil, &out.LastLoginAt,
        &out.CreatedAt, &out.UpdatedAt,
    )
    if err != nil {
        if isUniqueViolation(err) { return out, auth.ErrUsernameTaken }
        return out, err
    }
    if _, err := tx.Exec(ctx, `
        INSERT INTO auth_passwords (user_id, password_hash) VALUES ($1, $2)
    `, out.UserID, password); err != nil {
        return out, err
    }
    return out, tx.Commit(ctx)
}

func (r *Repo) GetUserByUsername(ctx context.Context, username string) (auth.User, error) {
    return r.fetchUser(ctx, `WHERE username = $1`, username)
}
func (r *Repo) GetUserByID(ctx context.Context, id uuid.UUID) (auth.User, error) {
    return r.fetchUser(ctx, `WHERE user_id = $1`, id)
}

func (r *Repo) fetchUser(ctx context.Context, where string, arg any) (auth.User, error) {
    var u auth.User
    err := r.pool.QueryRow(ctx, `
        SELECT user_id, username, COALESCE(email,''), email_verified, mfa_enabled,
               status, failed_attempts, COALESCE(locked_until, 'epoch'::timestamptz),
               COALESCE(last_login_at, 'epoch'::timestamptz), created_at, updated_at
        FROM auth_users `+where, arg,
    ).Scan(&u.UserID, &u.Username, &u.Email, &u.EmailVerified, &u.MFAEnabled,
        &u.Status, &u.FailedAttempts, &u.LockedUntil, &u.LastLoginAt,
        &u.CreatedAt, &u.UpdatedAt)
    if errors.Is(err, pgx.ErrNoRows) { return u, auth.ErrUserNotFound }
    return u, err
}

func (r *Repo) UpdateUserLogin(ctx context.Context, id uuid.UUID, at time.Time) error {
    _, err := r.pool.Exec(ctx, `
        UPDATE auth_users SET last_login_at = $2, failed_attempts = 0, locked_until = NULL,
                              updated_at = NOW() WHERE user_id = $1`, id, at)
    return err
}

func (r *Repo) IncrementFailedAttempts(ctx context.Context, id uuid.UUID, threshold int, dur time.Duration) (int, time.Time, error) {
    var count int
    var locked time.Time
    err := r.pool.QueryRow(ctx, `
        UPDATE auth_users
        SET failed_attempts = failed_attempts + 1,
            locked_until = CASE
                WHEN failed_attempts + 1 >= $2 THEN NOW() + ($3 || ' microseconds')::interval
                ELSE locked_until
            END,
            updated_at = NOW()
        WHERE user_id = $1
        RETURNING failed_attempts, COALESCE(locked_until, 'epoch'::timestamptz)
    `, id, threshold, dur.Microseconds()).Scan(&count, &locked)
    return count, locked, err
}

func (r *Repo) ResetFailedAttempts(ctx context.Context, id uuid.UUID) error {
    _, err := r.pool.Exec(ctx, `
        UPDATE auth_users SET failed_attempts = 0, locked_until = NULL, updated_at = NOW()
        WHERE user_id = $1`, id)
    return err
}

func (r *Repo) SetUserStatus(ctx context.Context, id uuid.UUID, status string, locked time.Time) error {
    _, err := r.pool.Exec(ctx, `
        UPDATE auth_users SET status = $2, locked_until = NULLIF($3, 'epoch'::timestamptz),
                              updated_at = NOW() WHERE user_id = $1
    `, id, status, locked)
    return err
}
```

- [ ] **Step 3: Implement password methods**

```go
func (r *Repo) GetPassword(ctx context.Context, id uuid.UUID) (auth.PasswordCredential, error) {
    var p auth.PasswordCredential
    err := r.pool.QueryRow(ctx, `
        SELECT user_id, password_hash, hash_algorithm, changed_at
        FROM auth_passwords WHERE user_id = $1
    `, id).Scan(&p.UserID, &p.PasswordHash, &p.HashAlgorithm, &p.ChangedAt)
    if errors.Is(err, pgx.ErrNoRows) { return p, auth.ErrUserNotFound }
    return p, err
}

func (r *Repo) UpdatePassword(ctx context.Context, id uuid.UUID, newHash string) error {
    _, err := r.pool.Exec(ctx, `
        UPDATE auth_passwords SET password_hash = $2, changed_at = NOW() WHERE user_id = $1
    `, id, newHash)
    return err
}
```

- [ ] **Step 4: Implement session methods**

```go
func (r *Repo) CreateSession(ctx context.Context, s auth.Session) error {
    metaJSON, _ := json.Marshal(s.ClientMeta)
    _, err := r.pool.Exec(ctx, `
        INSERT INTO auth_sessions (token_hash, user_id, expires_at, client_meta)
        VALUES ($1, $2, $3, $4)
    `, s.TokenHash, s.UserID, s.ExpiresAt, metaJSON)
    return err
}

func (r *Repo) GetSession(ctx context.Context, tokenHash []byte) (auth.Session, error) {
    var s auth.Session
    var meta []byte
    err := r.pool.QueryRow(ctx, `
        SELECT token_hash, user_id, issued_at, expires_at, last_used_at,
               COALESCE(revoked_at, 'epoch'::timestamptz), COALESCE(client_meta::text, '{}')
        FROM auth_sessions WHERE token_hash = $1
    `, tokenHash).Scan(&s.TokenHash, &s.UserID, &s.IssuedAt, &s.ExpiresAt,
        &s.LastUsedAt, &s.RevokedAt, &meta)
    if errors.Is(err, pgx.ErrNoRows) { return s, auth.ErrSessionNotFound }
    if err != nil { return s, err }
    _ = json.Unmarshal(meta, &s.ClientMeta)
    return s, nil
}

func (r *Repo) SlideSession(ctx context.Context, tokenHash []byte, newExpiry time.Time) error {
    _, err := r.pool.Exec(ctx, `
        UPDATE auth_sessions SET expires_at = $2, last_used_at = NOW()
        WHERE token_hash = $1 AND revoked_at IS NULL
    `, tokenHash, newExpiry)
    return err
}

func (r *Repo) RevokeSession(ctx context.Context, tokenHash []byte) error {
    _, err := r.pool.Exec(ctx, `
        UPDATE auth_sessions SET revoked_at = NOW() WHERE token_hash = $1 AND revoked_at IS NULL
    `, tokenHash)
    return err
}

func (r *Repo) RevokeAllSessionsExcept(ctx context.Context, id uuid.UUID, keep []byte) (int, error) {
    tag, err := r.pool.Exec(ctx, `
        UPDATE auth_sessions SET revoked_at = NOW()
        WHERE user_id = $1 AND token_hash != $2 AND revoked_at IS NULL
    `, id, keep)
    return int(tag.RowsAffected()), err
}

func (r *Repo) RevokeAllSessionsForUser(ctx context.Context, id uuid.UUID) (int, error) {
    tag, err := r.pool.Exec(ctx, `
        UPDATE auth_sessions SET revoked_at = NOW()
        WHERE user_id = $1 AND revoked_at IS NULL
    `, id)
    return int(tag.RowsAffected()), err
}

func (r *Repo) ListActiveSessions(ctx context.Context, id uuid.UUID) ([]auth.Session, error) {
    rows, err := r.pool.Query(ctx, `
        SELECT token_hash, user_id, issued_at, expires_at, last_used_at,
               'epoch'::timestamptz, COALESCE(client_meta::text, '{}')
        FROM auth_sessions WHERE user_id = $1 AND revoked_at IS NULL
        ORDER BY issued_at DESC
    `, id)
    if err != nil { return nil, err }
    defer rows.Close()
    var out []auth.Session
    for rows.Next() {
        var s auth.Session
        var meta []byte
        if err := rows.Scan(&s.TokenHash, &s.UserID, &s.IssuedAt, &s.ExpiresAt,
            &s.LastUsedAt, &s.RevokedAt, &meta); err != nil {
            return nil, err
        }
        _ = json.Unmarshal(meta, &s.ClientMeta)
        out = append(out, s)
    }
    return out, rows.Err()
}
```

- [ ] **Step 5: Implement reaper + audit methods**

```go
func (r *Repo) DeleteExpiredSessions(ctx context.Context, retention time.Duration) (int, error) {
    tag, err := r.pool.Exec(ctx, `
        DELETE FROM auth_sessions
        WHERE revoked_at IS NOT NULL OR expires_at < NOW() - ($1 || ' microseconds')::interval
    `, retention.Microseconds())
    return int(tag.RowsAffected()), err
}

func (r *Repo) DeleteOldAuditRows(ctx context.Context, olderThan time.Duration) (int, error) {
    tag, err := r.pool.Exec(ctx, `
        DELETE FROM auth_audit_log WHERE occurred_at < NOW() - ($1 || ' microseconds')::interval
    `, olderThan.Microseconds())
    return int(tag.RowsAffected()), err
}

func (r *Repo) Audit(ctx context.Context, ev auth.AuditEvent) error {
    metaJSON, _ := json.Marshal(ev.Metadata)
    var ipStr *string
    if ev.IPAddr.IsValid() { s := ev.IPAddr.String(); ipStr = &s }
    var uid *uuid.UUID
    if ev.UserID != uuid.Nil { u := ev.UserID; uid = &u }
    _, err := r.pool.Exec(ctx, `
        INSERT INTO auth_audit_log
            (event, user_id, username_attempted, ip_addr, user_agent, gateway_id, metadata)
        VALUES ($1, $2, NULLIF($3,''), $4::INET, NULLIF($5,''), NULLIF($6,''), $7)
    `, ev.Event, uid, ev.UsernameAttempted, ipStr, ev.UserAgent, ev.GatewayID, metaJSON)
    return err
}

func (r *Repo) RecentAudit(ctx context.Context, id uuid.UUID, limit int) ([]auth.AuditEvent, error) {
    rows, err := r.pool.Query(ctx, `
        SELECT event, COALESCE(user_id, '00000000-0000-0000-0000-000000000000'::uuid),
               COALESCE(username_attempted,''), COALESCE(host(ip_addr),''),
               COALESCE(user_agent,''), COALESCE(gateway_id,''),
               COALESCE(metadata::text, '{}')
        FROM auth_audit_log WHERE user_id = $1
        ORDER BY occurred_at DESC LIMIT $2
    `, id, limit)
    if err != nil { return nil, err }
    defer rows.Close()
    var out []auth.AuditEvent
    for rows.Next() {
        var ev auth.AuditEvent
        var ipStr, metaStr string
        if err := rows.Scan(&ev.Event, &ev.UserID, &ev.UsernameAttempted, &ipStr,
            &ev.UserAgent, &ev.GatewayID, &metaStr); err != nil {
            return nil, err
        }
        if ipStr != "" { ev.IPAddr, _ = netip.ParseAddr(ipStr) }
        _ = json.Unmarshal([]byte(metaStr), &ev.Metadata)
        out = append(out, ev)
    }
    return out, rows.Err()
}

// isUniqueViolation maps pgx pgconn errors. Match what pkg/persist/postgres uses.
func isUniqueViolation(err error) bool {
    var pgErr interface{ SQLState() string }
    return errors.As(err, &pgErr) && pgErr.SQLState() == "23505"
}
```

- [ ] **Step 6: Tests against pgtest harness**

`pkg/auth/postgres/repo_test.go`:

```go
//go:build pgtest

package postgres

import (
    "context"
    "testing"
    "time"

    "github.com/google/uuid"

    "github.com/zenion/mmoserver/pkg/auth"
)

func TestCreateUserAndFetch(t *testing.T) {
    repo := openTestRepo(t)
    u, err := repo.CreateUser(context.Background(), auth.User{Username: "alice"}, "argon2id-hash-here")
    if err != nil { t.Fatal(err) }
    if u.UserID == uuid.Nil { t.Fatal("user_id zero") }

    got, err := repo.GetUserByUsername(context.Background(), "alice")
    if err != nil { t.Fatal(err) }
    if got.UserID != u.UserID { t.Fatalf("mismatch: %v vs %v", got.UserID, u.UserID) }
}

func TestDuplicateUsername(t *testing.T) {
    repo := openTestRepo(t)
    _, _ = repo.CreateUser(context.Background(), auth.User{Username: "bob"}, "h")
    if _, err := repo.CreateUser(context.Background(), auth.User{Username: "bob"}, "h2"); err != auth.ErrUsernameTaken {
        t.Fatalf("expected ErrUsernameTaken, got %v", err)
    }
}

func TestSessionLifecycle(t *testing.T) {
    repo := openTestRepo(t)
    u, _ := repo.CreateUser(context.Background(), auth.User{Username: "carol"}, "h")
    sess := auth.Session{
        TokenHash: []byte("hash-32-bytes-padded-to-len-here"),
        UserID:    u.UserID,
        ExpiresAt: time.Now().Add(time.Hour),
    }
    if err := repo.CreateSession(context.Background(), sess); err != nil { t.Fatal(err) }
    got, err := repo.GetSession(context.Background(), sess.TokenHash)
    if err != nil { t.Fatal(err) }
    if got.UserID != u.UserID { t.Fatal("session user mismatch") }

    newExp := time.Now().Add(2 * time.Hour)
    if err := repo.SlideSession(context.Background(), sess.TokenHash, newExp); err != nil { t.Fatal(err) }

    if err := repo.RevokeSession(context.Background(), sess.TokenHash); err != nil { t.Fatal(err) }
    got2, _ := repo.GetSession(context.Background(), sess.TokenHash)
    if got2.RevokedAt.IsZero() { t.Fatal("revoked_at should be set") }
}

func TestIncrementAndLockout(t *testing.T) {
    repo := openTestRepo(t)
    u, _ := repo.CreateUser(context.Background(), auth.User{Username: "dave"}, "h")
    var locked time.Time
    for i := 1; i <= 5; i++ {
        n, l, err := repo.IncrementFailedAttempts(context.Background(), u.UserID, 5, 15*time.Minute)
        if err != nil { t.Fatal(err) }
        if n != i { t.Fatalf("attempt %d: count=%d", i, n) }
        locked = l
    }
    if locked.IsZero() || locked.Before(time.Now()) {
        t.Fatalf("expected locked_until in future, got %v", locked)
    }
    if err := repo.ResetFailedAttempts(context.Background(), u.UserID); err != nil { t.Fatal(err) }
    got, _ := repo.GetUserByID(context.Background(), u.UserID)
    if got.FailedAttempts != 0 { t.Fatal("not reset") }
}

func openTestRepo(t *testing.T) *Repo {
    t.Helper()
    // wires through pkg/persist/postgres test harness; calls New(pool) on a
    // fresh pgxpool against the local docker-compose Postgres after applying
    // engine migrations + auth migrations. Mirrors the pattern in
    // pkg/persist/postgres/postgres_test.go.
    pool := openTestPool(t) // helper defined alongside this file
    return New(pool)
}
```

Add `openTestPool(t)` helper that mirrors the existing pattern in `pkg/persist/postgres/postgres_test.go` (uses `POSTGRES_URL` env, applies migrations from this package's embedded FS).

- [ ] **Step 7: Run tests**

Run: `just test-pg ./pkg/auth/postgres/...`
Expected: all pass.

- [ ] **Step 8: Commit**

```bash
git add pkg/auth/postgres/
git commit -m "feat(auth): Postgres Repository implementation + pgtest tests"
```

---

### Task 6: `authtest` in-memory mock

**Files:**
- Create: `pkg/auth/authtest/mock.go`

- [ ] **Step 1: Implement in-memory `RepoMock`**

`pkg/auth/authtest/mock.go`:

```go
// Package authtest provides an in-memory auth.Repository for tests.
package authtest

import (
    "context"
    "sync"
    "time"

    "github.com/google/uuid"

    "github.com/zenion/mmoserver/pkg/auth"
)

type RepoMock struct {
    mu        sync.Mutex
    users     map[uuid.UUID]auth.User
    byName    map[string]uuid.UUID
    passwords map[uuid.UUID]auth.PasswordCredential
    sessions  map[string]auth.Session // keyed by string(tokenHash)
    audit     []auth.AuditEvent
}

func NewMock() *RepoMock {
    return &RepoMock{
        users:     map[uuid.UUID]auth.User{},
        byName:    map[string]uuid.UUID{},
        passwords: map[uuid.UUID]auth.PasswordCredential{},
        sessions:  map[string]auth.Session{},
    }
}

var _ auth.Repository = (*RepoMock)(nil)

func (m *RepoMock) CreateUser(_ context.Context, u auth.User, password string) (auth.User, error) {
    m.mu.Lock(); defer m.mu.Unlock()
    if _, exists := m.byName[u.Username]; exists { return auth.User{}, auth.ErrUsernameTaken }
    if u.UserID == uuid.Nil { u.UserID = uuid.New() }
    u.Status = "active"
    u.CreatedAt = time.Now()
    u.UpdatedAt = u.CreatedAt
    m.users[u.UserID] = u
    m.byName[u.Username] = u.UserID
    m.passwords[u.UserID] = auth.PasswordCredential{
        UserID: u.UserID, PasswordHash: password, HashAlgorithm: "argon2id", ChangedAt: time.Now(),
    }
    return u, nil
}

func (m *RepoMock) GetUserByUsername(_ context.Context, name string) (auth.User, error) {
    m.mu.Lock(); defer m.mu.Unlock()
    id, ok := m.byName[name]
    if !ok { return auth.User{}, auth.ErrUserNotFound }
    return m.users[id], nil
}

func (m *RepoMock) GetUserByID(_ context.Context, id uuid.UUID) (auth.User, error) {
    m.mu.Lock(); defer m.mu.Unlock()
    u, ok := m.users[id]
    if !ok { return auth.User{}, auth.ErrUserNotFound }
    return u, nil
}

func (m *RepoMock) UpdateUserLogin(_ context.Context, id uuid.UUID, at time.Time) error {
    m.mu.Lock(); defer m.mu.Unlock()
    u, ok := m.users[id]; if !ok { return auth.ErrUserNotFound }
    u.LastLoginAt = at; u.FailedAttempts = 0; u.LockedUntil = time.Time{}; u.UpdatedAt = time.Now()
    m.users[id] = u
    return nil
}

func (m *RepoMock) IncrementFailedAttempts(_ context.Context, id uuid.UUID, threshold int, dur time.Duration) (int, time.Time, error) {
    m.mu.Lock(); defer m.mu.Unlock()
    u, ok := m.users[id]; if !ok { return 0, time.Time{}, auth.ErrUserNotFound }
    u.FailedAttempts++
    if u.FailedAttempts >= threshold { u.LockedUntil = time.Now().Add(dur) }
    u.UpdatedAt = time.Now()
    m.users[id] = u
    return u.FailedAttempts, u.LockedUntil, nil
}

func (m *RepoMock) ResetFailedAttempts(_ context.Context, id uuid.UUID) error {
    m.mu.Lock(); defer m.mu.Unlock()
    u, ok := m.users[id]; if !ok { return auth.ErrUserNotFound }
    u.FailedAttempts = 0; u.LockedUntil = time.Time{}; u.UpdatedAt = time.Now()
    m.users[id] = u
    return nil
}

func (m *RepoMock) SetUserStatus(_ context.Context, id uuid.UUID, status string, locked time.Time) error {
    m.mu.Lock(); defer m.mu.Unlock()
    u, ok := m.users[id]; if !ok { return auth.ErrUserNotFound }
    u.Status = status; u.LockedUntil = locked; u.UpdatedAt = time.Now()
    m.users[id] = u
    return nil
}

func (m *RepoMock) GetPassword(_ context.Context, id uuid.UUID) (auth.PasswordCredential, error) {
    m.mu.Lock(); defer m.mu.Unlock()
    p, ok := m.passwords[id]; if !ok { return auth.PasswordCredential{}, auth.ErrUserNotFound }
    return p, nil
}

func (m *RepoMock) UpdatePassword(_ context.Context, id uuid.UUID, newHash string) error {
    m.mu.Lock(); defer m.mu.Unlock()
    p, ok := m.passwords[id]; if !ok { return auth.ErrUserNotFound }
    p.PasswordHash = newHash; p.ChangedAt = time.Now()
    m.passwords[id] = p
    return nil
}

func (m *RepoMock) CreateSession(_ context.Context, s auth.Session) error {
    m.mu.Lock(); defer m.mu.Unlock()
    s.IssuedAt = time.Now(); s.LastUsedAt = s.IssuedAt
    m.sessions[string(s.TokenHash)] = s
    return nil
}

func (m *RepoMock) GetSession(_ context.Context, h []byte) (auth.Session, error) {
    m.mu.Lock(); defer m.mu.Unlock()
    s, ok := m.sessions[string(h)]; if !ok { return auth.Session{}, auth.ErrSessionNotFound }
    return s, nil
}

func (m *RepoMock) SlideSession(_ context.Context, h []byte, newExp time.Time) error {
    m.mu.Lock(); defer m.mu.Unlock()
    s, ok := m.sessions[string(h)]; if !ok { return auth.ErrSessionNotFound }
    s.ExpiresAt = newExp; s.LastUsedAt = time.Now()
    m.sessions[string(h)] = s
    return nil
}

func (m *RepoMock) RevokeSession(_ context.Context, h []byte) error {
    m.mu.Lock(); defer m.mu.Unlock()
    s, ok := m.sessions[string(h)]; if !ok { return auth.ErrSessionNotFound }
    s.RevokedAt = time.Now()
    m.sessions[string(h)] = s
    return nil
}

func (m *RepoMock) RevokeAllSessionsExcept(_ context.Context, id uuid.UUID, keep []byte) (int, error) {
    m.mu.Lock(); defer m.mu.Unlock()
    n := 0
    for k, s := range m.sessions {
        if s.UserID == id && k != string(keep) && s.RevokedAt.IsZero() {
            s.RevokedAt = time.Now(); m.sessions[k] = s; n++
        }
    }
    return n, nil
}

func (m *RepoMock) RevokeAllSessionsForUser(_ context.Context, id uuid.UUID) (int, error) {
    m.mu.Lock(); defer m.mu.Unlock()
    n := 0
    for k, s := range m.sessions {
        if s.UserID == id && s.RevokedAt.IsZero() {
            s.RevokedAt = time.Now(); m.sessions[k] = s; n++
        }
    }
    return n, nil
}

func (m *RepoMock) ListActiveSessions(_ context.Context, id uuid.UUID) ([]auth.Session, error) {
    m.mu.Lock(); defer m.mu.Unlock()
    var out []auth.Session
    for _, s := range m.sessions {
        if s.UserID == id && s.RevokedAt.IsZero() { out = append(out, s) }
    }
    return out, nil
}

func (m *RepoMock) DeleteExpiredSessions(_ context.Context, retention time.Duration) (int, error) {
    m.mu.Lock(); defer m.mu.Unlock()
    n := 0
    cutoff := time.Now().Add(-retention)
    for k, s := range m.sessions {
        if !s.RevokedAt.IsZero() || s.ExpiresAt.Before(cutoff) {
            delete(m.sessions, k); n++
        }
    }
    return n, nil
}

func (m *RepoMock) DeleteOldAuditRows(_ context.Context, olderThan time.Duration) (int, error) {
    return 0, nil  // mock keeps audit in-memory; reaper test irrelevant here
}

func (m *RepoMock) Audit(_ context.Context, ev auth.AuditEvent) error {
    m.mu.Lock(); defer m.mu.Unlock()
    m.audit = append(m.audit, ev)
    return nil
}

func (m *RepoMock) RecentAudit(_ context.Context, id uuid.UUID, limit int) ([]auth.AuditEvent, error) {
    m.mu.Lock(); defer m.mu.Unlock()
    var out []auth.AuditEvent
    for i := len(m.audit) - 1; i >= 0 && len(out) < limit; i-- {
        if m.audit[i].UserID == id { out = append(out, m.audit[i]) }
    }
    return out, nil
}

// AuditEvents returns all recorded events for test inspection.
func (m *RepoMock) AuditEvents() []auth.AuditEvent {
    m.mu.Lock(); defer m.mu.Unlock()
    out := make([]auth.AuditEvent, len(m.audit))
    copy(out, m.audit)
    return out
}
```

- [ ] **Step 2: Compile check**

Run: `go vet ./pkg/auth/...`
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add pkg/auth/authtest/
git commit -m "feat(auth): in-memory authtest.RepoMock for tests"
```

---

## Phase B — Domain primitives

### Task 7: argon2id password hashing

**Files:**
- Create: `pkg/auth/password.go`
- Create: `pkg/auth/password_test.go`

- [ ] **Step 1: Write the failing test**

`pkg/auth/password_test.go`:

```go
package auth

import (
    "strings"
    "testing"
)

func TestPasswordHashAndVerify(t *testing.T) {
    h, err := HashPassword("hunter2", DefaultArgonParams())
    if err != nil { t.Fatal(err) }
    if !strings.HasPrefix(h, "$argon2id$") { t.Fatalf("bad encoded form: %s", h) }
    ok, err := VerifyPassword("hunter2", h)
    if err != nil { t.Fatal(err) }
    if !ok { t.Fatal("expected verify true") }
    bad, err := VerifyPassword("wrong", h)
    if err != nil { t.Fatal(err) }
    if bad { t.Fatal("expected verify false") }
}

func TestPasswordHashUniqueSalts(t *testing.T) {
    h1, _ := HashPassword("same", DefaultArgonParams())
    h2, _ := HashPassword("same", DefaultArgonParams())
    if h1 == h2 { t.Fatal("salts must differ") }
}

func TestVerifyParsesEncodedParams(t *testing.T) {
    weaker := ArgonParams{Memory: 8192, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}
    h, _ := HashPassword("p", weaker)
    ok, _ := VerifyPassword("p", h)
    if !ok { t.Fatal("must verify with embedded weak params") }
}
```

- [ ] **Step 2: Run, confirm fails**

Run: `go test ./pkg/auth/ -run TestPassword`
Expected: FAIL — `HashPassword`/`VerifyPassword`/`DefaultArgonParams` undefined.

- [ ] **Step 3: Implement**

`pkg/auth/password.go`:

```go
package auth

import (
    "crypto/rand"
    "crypto/subtle"
    "encoding/base64"
    "errors"
    "fmt"
    "strings"

    "golang.org/x/crypto/argon2"
)

// ArgonParams configures argon2id hashing. Defaults track OWASP 2024.
type ArgonParams struct {
    Memory      uint32 // KiB
    Iterations  uint32
    Parallelism uint8
    SaltLen     uint32
    KeyLen      uint32
}

func DefaultArgonParams() ArgonParams {
    return ArgonParams{Memory: 64 * 1024, Iterations: 3, Parallelism: 4, SaltLen: 16, KeyLen: 32}
}

// HashPassword returns the encoded argon2id hash of password.
func HashPassword(password string, p ArgonParams) (string, error) {
    salt := make([]byte, p.SaltLen)
    if _, err := rand.Read(salt); err != nil { return "", err }
    hash := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLen)
    return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
        argon2.Version, p.Memory, p.Iterations, p.Parallelism,
        base64.RawStdEncoding.EncodeToString(salt),
        base64.RawStdEncoding.EncodeToString(hash),
    ), nil
}

// VerifyPassword returns true if the password matches the encoded hash.
func VerifyPassword(password, encoded string) (bool, error) {
    p, salt, hash, err := decodeArgon2(encoded)
    if err != nil { return false, err }
    cmp := argon2.IDKey([]byte(password), salt, p.Iterations, p.Memory, p.Parallelism, p.KeyLen)
    return subtle.ConstantTimeCompare(hash, cmp) == 1, nil
}

// HashUsesParams reports whether encoded was produced with the given params.
// Use to decide whether to re-hash on verify.
func HashUsesParams(encoded string, p ArgonParams) bool {
    got, _, _, err := decodeArgon2(encoded)
    if err != nil { return false }
    return got == p
}

func decodeArgon2(encoded string) (ArgonParams, []byte, []byte, error) {
    parts := strings.Split(encoded, "$")
    if len(parts) != 6 || parts[1] != "argon2id" {
        return ArgonParams{}, nil, nil, errors.New("auth: bad argon2 encoded form")
    }
    var ver int
    if _, err := fmt.Sscanf(parts[2], "v=%d", &ver); err != nil {
        return ArgonParams{}, nil, nil, fmt.Errorf("auth: bad version: %w", err)
    }
    if ver != argon2.Version {
        return ArgonParams{}, nil, nil, fmt.Errorf("auth: argon2 version mismatch: %d", ver)
    }
    var p ArgonParams
    if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.Memory, &p.Iterations, &p.Parallelism); err != nil {
        return ArgonParams{}, nil, nil, fmt.Errorf("auth: bad params: %w", err)
    }
    salt, err := base64.RawStdEncoding.DecodeString(parts[4])
    if err != nil { return ArgonParams{}, nil, nil, fmt.Errorf("auth: bad salt: %w", err) }
    hash, err := base64.RawStdEncoding.DecodeString(parts[5])
    if err != nil { return ArgonParams{}, nil, nil, fmt.Errorf("auth: bad hash: %w", err) }
    p.SaltLen = uint32(len(salt))
    p.KeyLen = uint32(len(hash))
    return p, salt, hash, nil
}
```

- [ ] **Step 4: Run, confirm pass**

Run: `go test ./pkg/auth/ -run TestPassword -v`
Expected: PASS for all three tests.

- [ ] **Step 5: Commit**

```bash
git add pkg/auth/password.go pkg/auth/password_test.go
git commit -m "feat(auth): argon2id password hash + verify with self-describing encoded form"
```

---

### Task 8: Session token primitives

**Files:**
- Create: `pkg/auth/token.go`
- Create: `pkg/auth/token_test.go`

- [ ] **Step 1: Write the failing test**

```go
package auth

import (
    "encoding/base64"
    "testing"
)

func TestNewTokenLengthAndEntropy(t *testing.T) {
    t1, h1, err := NewToken()
    if err != nil { t.Fatal(err) }
    raw, err := base64.RawURLEncoding.DecodeString(t1)
    if err != nil { t.Fatal(err) }
    if len(raw) != 32 { t.Fatalf("want 32 bytes, got %d", len(raw)) }
    if len(h1) != 32 { t.Fatalf("hash want 32 bytes, got %d", len(h1)) }
    t2, _, _ := NewToken()
    if t1 == t2 { t.Fatal("tokens must differ") }
}

func TestHashTokenStable(t *testing.T) {
    tok, h1, _ := NewToken()
    h2 := HashToken(tok)
    if string(h1) != string(h2) { t.Fatal("HashToken not stable") }
}
```

- [ ] **Step 2: Run, confirm fails**

Run: `go test ./pkg/auth/ -run TestNewToken`
Expected: FAIL — `NewToken`, `HashToken` undefined.

- [ ] **Step 3: Implement**

`pkg/auth/token.go`:

```go
package auth

import (
    "crypto/rand"
    "crypto/sha256"
    "encoding/base64"
)

// TokenBytes is the entropy length of a session token. Not configurable
// (256 bits is the right answer; a knob invites someone to drop it).
const TokenBytes = 32

// NewToken returns a base64url-encoded random token plus its SHA-256 hash.
// The raw token is what the client sees; the hash is what the DB stores.
func NewToken() (token string, hash []byte, err error) {
    b := make([]byte, TokenBytes)
    if _, err := rand.Read(b); err != nil { return "", nil, err }
    s := base64.RawURLEncoding.EncodeToString(b)
    return s, HashToken(s), nil
}

// HashToken returns the SHA-256 hash of a base64url-encoded token.
// Deterministic; suitable as a primary key.
func HashToken(token string) []byte {
    h := sha256.Sum256([]byte(token))
    return h[:]
}
```

- [ ] **Step 4: Run, confirm pass**

Run: `go test ./pkg/auth/ -run TestNewToken -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/auth/token.go pkg/auth/token_test.go
git commit -m "feat(auth): session token generate + hash primitives"
```

---

### Task 9: Per-IP rate limiter

**Files:**
- Create: `pkg/auth/ratelimit.go`
- Create: `pkg/auth/ratelimit_test.go`

- [ ] **Step 1: Write the failing test**

```go
package auth

import (
    "net/netip"
    "testing"
    "time"
)

func TestRateLimitAllowsUnderThreshold(t *testing.T) {
    rl := NewIPRateLimiter(IPRateLimitConfig{Max: 3, Window: time.Minute, Lockout: time.Minute})
    ip := netip.MustParseAddr("1.2.3.4")
    for i := 0; i < 3; i++ {
        if locked, _ := rl.CheckAndCount(ip, false); locked { t.Fatalf("locked at attempt %d", i) }
    }
    if locked, _ := rl.CheckAndCount(ip, false); !locked { t.Fatal("4th attempt should lock") }
}

func TestRateLimitResetOnSuccess(t *testing.T) {
    rl := NewIPRateLimiter(IPRateLimitConfig{Max: 3, Window: time.Minute, Lockout: time.Minute})
    ip := netip.MustParseAddr("5.6.7.8")
    rl.CheckAndCount(ip, false)
    rl.CheckAndCount(ip, false)
    rl.CheckAndCount(ip, true)  // success → reset
    for i := 0; i < 3; i++ {
        if locked, _ := rl.CheckAndCount(ip, false); locked { t.Fatalf("locked too soon at %d", i) }
    }
}

func TestRateLimitWindowExpiry(t *testing.T) {
    rl := NewIPRateLimiter(IPRateLimitConfig{Max: 3, Window: 50 * time.Millisecond, Lockout: time.Minute})
    ip := netip.MustParseAddr("9.9.9.9")
    rl.CheckAndCount(ip, false)
    rl.CheckAndCount(ip, false)
    time.Sleep(60 * time.Millisecond)
    rl.CheckAndCount(ip, false)
    if locked, _ := rl.CheckAndCount(ip, false); locked {
        t.Fatal("window expired counter should not lock")
    }
}
```

- [ ] **Step 2: Run, confirm fails**

Run: `go test ./pkg/auth/ -run TestRateLimit`
Expected: FAIL — `NewIPRateLimiter`, `IPRateLimitConfig`, `CheckAndCount` undefined.

- [ ] **Step 3: Implement**

`pkg/auth/ratelimit.go`:

```go
package auth

import (
    "net/netip"
    "sync"
    "time"
)

type IPRateLimitConfig struct {
    Max     int           // max failures per Window before lockout
    Window  time.Duration // sliding window
    Lockout time.Duration // how long the IP is locked when Max is exceeded
}

type ipBucket struct {
    failures    int
    windowStart time.Time
    lockedUntil time.Time
}

// IPRateLimiter is the in-memory per-IP rate limiter described in §9 of
// the spec. Successful auth resets the counter; failures accrue.
type IPRateLimiter struct {
    cfg     IPRateLimitConfig
    mu      sync.Mutex
    buckets map[netip.Addr]*ipBucket
    now     func() time.Time
}

func NewIPRateLimiter(cfg IPRateLimitConfig) *IPRateLimiter {
    return &IPRateLimiter{cfg: cfg, buckets: map[netip.Addr]*ipBucket{}, now: time.Now}
}

// CheckAndCount records an auth attempt outcome for ip. Returns
// (locked, retryAfter). When locked is true, retryAfter > 0 and the
// caller should reject the operation with RATE_LIMITED. Successful
// attempts reset the bucket.
func (rl *IPRateLimiter) CheckAndCount(ip netip.Addr, success bool) (bool, time.Duration) {
    rl.mu.Lock()
    defer rl.mu.Unlock()
    now := rl.now()
    b, ok := rl.buckets[ip]
    if !ok { b = &ipBucket{}; rl.buckets[ip] = b }

    // Already locked?
    if !b.lockedUntil.IsZero() && b.lockedUntil.After(now) {
        return true, b.lockedUntil.Sub(now)
    }

    if success {
        delete(rl.buckets, ip)
        return false, 0
    }

    // Window expired?
    if !b.windowStart.IsZero() && now.Sub(b.windowStart) > rl.cfg.Window {
        b.failures = 0
        b.windowStart = time.Time{}
        b.lockedUntil = time.Time{}
    }

    if b.failures == 0 { b.windowStart = now }
    b.failures++
    if b.failures > rl.cfg.Max {
        b.lockedUntil = now.Add(rl.cfg.Lockout)
        return true, rl.cfg.Lockout
    }
    return false, 0
}

// Sweep evicts buckets idle longer than maxIdle. Called periodically
// from the service's reaper goroutine.
func (rl *IPRateLimiter) Sweep(maxIdle time.Duration) int {
    rl.mu.Lock()
    defer rl.mu.Unlock()
    now := rl.now()
    n := 0
    for ip, b := range rl.buckets {
        latest := b.windowStart
        if b.lockedUntil.After(latest) { latest = b.lockedUntil }
        if now.Sub(latest) > maxIdle { delete(rl.buckets, ip); n++ }
    }
    return n
}
```

- [ ] **Step 4: Run, confirm pass**

Run: `go test ./pkg/auth/ -run TestRateLimit -v`
Expected: PASS for all three.

- [ ] **Step 5: Commit**

```bash
git add pkg/auth/ratelimit.go pkg/auth/ratelimit_test.go
git commit -m "feat(auth): per-IP rate limiter with sliding window + reset-on-success"
```

---

## Phase C — Service core

### Task 10: `ServiceOpts` + `Kind` descriptor

**Files:**
- Create: `pkg/auth/kind.go`

- [ ] **Step 1: Implement**

```go
package auth

import (
    "embed"
    "io/fs"
    "time"

    enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
    "github.com/zenion/mmoserver/pkg/service"
)

// ServiceOpts is the configuration handed to RegisterAuthService.
type ServiceOpts struct {
    Repository Repository

    SessionTTL          time.Duration
    PasswordMinLen      int
    Argon2id            ArgonParams

    IPRateLimitMax      int
    IPRateLimitWindow   time.Duration
    IPLockoutDuration   time.Duration

    LockoutThreshold    int
    LockoutDuration     time.Duration

    AuditRetention      time.Duration
    ReapInterval        time.Duration

    TrustedProxyHeader  bool
}

func DefaultServiceOpts() ServiceOpts {
    return ServiceOpts{
        SessionTTL:        30 * 24 * time.Hour,
        PasswordMinLen:    8,
        Argon2id:          DefaultArgonParams(),
        IPRateLimitMax:    10,
        IPRateLimitWindow: 60 * time.Second,
        IPLockoutDuration: 5 * time.Minute,
        LockoutThreshold:  5,
        LockoutDuration:   15 * time.Minute,
        AuditRetention:    90 * 24 * time.Hour,
        ReapInterval:      time.Hour,
    }
}

// KindName is the engine-reserved name. RegisterAuthService rejects
// games attempting to register a kind named "auth" for other purposes.
const KindName = "auth"

//go:embed postgres/migrations/*.sql
var pgMigrations embed.FS

// MigrationsFS returns the auth-package's Postgres migrations as an
// fs.FS suitable for Config.ExtraMigrations.
func MigrationsFS() fs.FS {
    sub, err := fs.Sub(pgMigrations, "postgres/migrations")
    if err != nil { panic(err) }  // build-time assertion
    return sub
}

// kindFor returns the service.Kind registration descriptor.
func kindFor(opts ServiceOpts) service.Kind {
    return service.Kind{
        Name: KindName,
        OpCodes: []uint32{
            uint32(enginepb.AuthOpCode_AUTH_OPCODE_LOGIN),
            uint32(enginepb.AuthOpCode_AUTH_OPCODE_REGISTER),
            uint32(enginepb.AuthOpCode_AUTH_OPCODE_VALIDATE_TOKEN),
            uint32(enginepb.AuthOpCode_AUTH_OPCODE_LOGOUT),
            uint32(enginepb.AuthOpCode_AUTH_OPCODE_CHANGE_PASSWORD),
        },
        Factory:     func(ctx *service.Context) service.Service { return newService(ctx, opts) },
        RequiresDB:  opts.Repository == nil, // injected mock skips DB requirement
        Description: "engine-tier identity service: argon2id passwords, opaque sliding-TTL session tokens, OIDC schema seam",
    }
}
```

- [ ] **Step 2: Compile check**

Run: `go vet ./pkg/auth/...`
Expected: error — `newService` undefined. (Will land in Task 11.)

- [ ] **Step 3: Don't commit yet**

Hold the commit until Task 11 — the kind references `newService`.

---

### Task 11: Service core (`Init`/`Shutdown`/`RegisterOps`)

**Files:**
- Create: `pkg/auth/service.go`

- [ ] **Step 1: Implement Service type and Init**

`pkg/auth/service.go`:

```go
package auth

import (
    "context"
    "errors"
    "sync"
    "time"

    "github.com/zenion/mmoserver/pkg/service"
    pgrepo "github.com/zenion/mmoserver/pkg/auth/postgres"
)

const logCat = "services:auth"

// Service is the running auth service instance.
type Service struct {
    ctx     *service.Context
    opts    ServiceOpts
    repo    Repository
    rl      *IPRateLimiter
    reapCh  chan struct{}
    reapWG  sync.WaitGroup
}

func newService(ctx *service.Context, opts ServiceOpts) service.Service {
    return &Service{ctx: ctx, opts: opts}
}

func (s *Service) Init(ctx *service.Context) error {
    if s.opts.Repository != nil {
        s.repo = s.opts.Repository
    } else {
        if ctx.DB == nil {
            return errors.New("auth.Init: DB required (RequiresDB=true should have caught this)")
        }
        s.repo = pgrepo.New(ctx.DB.Pool())
    }
    s.rl = NewIPRateLimiter(IPRateLimitConfig{
        Max: s.opts.IPRateLimitMax, Window: s.opts.IPRateLimitWindow, Lockout: s.opts.IPLockoutDuration,
    })
    s.reapCh = make(chan struct{})
    s.reapWG.Add(1)
    go s.reapLoop()
    ctx.Logger.Log(logCat, "auth service initialized: instance=%s", ctx.InstanceID)
    return nil
}

func (s *Service) Shutdown(ctx context.Context) error {
    if s.reapCh != nil { close(s.reapCh) }
    done := make(chan struct{})
    go func() { s.reapWG.Wait(); close(done) }()
    select {
    case <-done:
    case <-ctx.Done():
    }
    s.ctx.Logger.Log(logCat, "auth service shutdown: instance=%s", s.ctx.InstanceID)
    return nil
}

// (RegisterOps and reapLoop in subsequent tasks.)
```

- [ ] **Step 2: Add stub `RegisterOps` so kind.go compiles**

Append to `pkg/auth/service.go`:

```go
// RegisterOps wires the five auth handlers into the process-shared OpRouter.
// Handlers themselves live in handlers.go.
func (s *Service) RegisterOps(_ /* router; signature confirmed in Task 12 */ any) error {
    return nil
}
```

Note: this is intentionally a stub — Task 12 replaces it with the real signature once we lock the OpRouter type from `pkg/mmokit`.

- [ ] **Step 3: Compile check**

Run: `go vet ./pkg/auth/...`
Expected: errors fixed (kind.go's `newService` reference resolves; no undefined symbols).

- [ ] **Step 4: Commit**

```bash
git add pkg/auth/kind.go pkg/auth/service.go
git commit -m "feat(auth): Kind descriptor + Service skeleton (Init/Shutdown)"
```

---

### Task 12: `AUTH_LOGIN` handler

> **Prerequisite — do Task 18 first.** The handler code below uses `OpContext.ClientIP` and `OpContext.Bag()`, which are added by Task 18 in Phase D. Execute Task 18, then return to Task 12. (The plan keeps Phase D after Phase C in the document for narrative grouping; the actual dependency edge is `18 → {12, 13, 14, 15, 16, 17}`.)

**Files:**
- Modify: `pkg/auth/service.go` (replace stub `RegisterOps` with real signature)
- Create: `pkg/auth/handlers.go`
- Create: `pkg/auth/handlers_test.go`

- [ ] **Step 1: Replace `RegisterOps` stub**

In `pkg/auth/service.go` replace the stub:

```go
import (
    // ...
    "github.com/zenion/mmoserver/pkg/ops"
    enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
    "github.com/zenion/mmoserver/pkg/mmokit"
)

func (s *Service) RegisterOps(router *ops.Router) error {
    mmokit.RegisterOp(router, enginepb.AuthOpCode_AUTH_OPCODE_LOGIN, "authLogin", s.handleLogin)
    mmokit.RegisterOp(router, enginepb.AuthOpCode_AUTH_OPCODE_REGISTER, "authRegister", s.handleRegister)
    mmokit.RegisterOp(router, enginepb.AuthOpCode_AUTH_OPCODE_VALIDATE_TOKEN, "authValidateToken", s.handleValidateToken)
    mmokit.RegisterOp(router, enginepb.AuthOpCode_AUTH_OPCODE_LOGOUT, "authLogout", s.handleLogout)
    mmokit.RegisterOp(router, enginepb.AuthOpCode_AUTH_OPCODE_CHANGE_PASSWORD, "authChangePassword", s.handleChangePassword)
    return nil
}
```

- [ ] **Step 2: Create `pkg/auth/handlers.go` with `handleLogin`**

```go
package auth

import (
    "context"
    "errors"
    "fmt"
    "net/netip"
    "strings"
    "time"

    enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
    "github.com/zenion/mmoserver/pkg/ops"
)

// authError is the typed error the handler returns. The router maps it
// to the AuthError code on the op envelope.
type authError struct {
    Code     enginepb.AuthError
    Msg      string
    Metadata *enginepb.AuthErrorMetadata
}

func (e *authError) Error() string { return e.Msg }

func errorf(c enginepb.AuthError, f string, a ...any) error {
    return &authError{Code: c, Msg: fmt.Sprintf(f, a...)}
}

func errorWithRetry(c enginepb.AuthError, retry time.Duration, f string, a ...any) error {
    return &authError{
        Code:     c,
        Msg:      fmt.Sprintf(f, a...),
        Metadata: &enginepb.AuthErrorMetadata{RetryAfterMs: retry.Milliseconds()},
    }
}

func normalizeUsername(raw string) (string, error) {
    name := strings.ToLower(strings.TrimSpace(raw))
    if name == "" || len(name) > 32 {
        return "", errorf(enginepb.AuthError_AUTH_ERROR_USERNAME_INVALID, "username invalid")
    }
    return name, nil
}

func (s *Service) handleLogin(opCtx *ops.OpContext, req *enginepb.AuthLoginRequest) (*enginepb.AuthLoginResponse, error) {
    ip := opCtx.ClientIP
    if locked, retry := s.rl.CheckAndCount(ip, false); locked {
        s.audit(opCtx, "login_failure", uuid.Nil, req.Username, map[string]any{"reason": "ip_ratelimit"})
        return nil, errorWithRetry(enginepb.AuthError_AUTH_ERROR_RATE_LIMITED, retry, "too many attempts")
    }

    name, err := normalizeUsername(req.Username)
    if err != nil { return nil, err }

    ctx := context.Background()
    user, err := s.repo.GetUserByUsername(ctx, name)
    if err != nil {
        if errors.Is(err, ErrUserNotFound) {
            s.audit(opCtx, "login_failure", uuid.Nil, name, map[string]any{"reason": "no_such_user"})
            return nil, errorf(enginepb.AuthError_AUTH_ERROR_INVALID_CREDENTIALS, "invalid credentials")
        }
        return nil, errorf(enginepb.AuthError_AUTH_ERROR_INTERNAL, "lookup: %v", err)
    }

    if user.Status == "disabled" {
        s.audit(opCtx, "login_failure", user.UserID, name, map[string]any{"reason": "disabled"})
        return nil, errorf(enginepb.AuthError_AUTH_ERROR_ACCOUNT_LOCKED, "account disabled")
    }
    if !user.LockedUntil.IsZero() && user.LockedUntil.After(time.Now()) {
        retry := time.Until(user.LockedUntil)
        s.audit(opCtx, "login_failure", user.UserID, name, map[string]any{"reason": "account_locked"})
        return nil, errorWithRetry(enginepb.AuthError_AUTH_ERROR_ACCOUNT_LOCKED, retry, "account locked")
    }

    pw, err := s.repo.GetPassword(ctx, user.UserID)
    if err != nil { return nil, errorf(enginepb.AuthError_AUTH_ERROR_INTERNAL, "password lookup: %v", err) }
    ok, err := VerifyPassword(req.Password, pw.PasswordHash)
    if err != nil || !ok {
        n, lockedUntil, _ := s.repo.IncrementFailedAttempts(ctx, user.UserID, s.opts.LockoutThreshold, s.opts.LockoutDuration)
        meta := map[string]any{"reason": "wrong_password", "failed_attempts": n}
        s.audit(opCtx, "login_failure", user.UserID, name, meta)
        if n >= s.opts.LockoutThreshold {
            s.audit(opCtx, "lockout_triggered", user.UserID, name, map[string]any{
                "failed_attempts": n, "locked_until": lockedUntil,
            })
        }
        return nil, errorf(enginepb.AuthError_AUTH_ERROR_INVALID_CREDENTIALS, "invalid credentials")
    }

    // Re-hash if params drifted from current defaults.
    if !HashUsesParams(pw.PasswordHash, s.opts.Argon2id) {
        if newHash, err := HashPassword(req.Password, s.opts.Argon2id); err == nil {
            _ = s.repo.UpdatePassword(ctx, user.UserID, newHash)
        }
    }

    _ = s.repo.UpdateUserLogin(ctx, user.UserID, time.Now())
    s.rl.CheckAndCount(ip, true) // reset bucket on success

    tok, hash, err := NewToken()
    if err != nil { return nil, errorf(enginepb.AuthError_AUTH_ERROR_INTERNAL, "token: %v", err) }
    expires := time.Now().Add(s.opts.SessionTTL)
    if err := s.repo.CreateSession(ctx, Session{
        TokenHash: hash, UserID: user.UserID, ExpiresAt: expires,
        ClientMeta: clientMeta(opCtx, ip),
    }); err != nil {
        return nil, errorf(enginepb.AuthError_AUTH_ERROR_INTERNAL, "session: %v", err)
    }

    s.audit(opCtx, "login_success", user.UserID, name, nil)
    s.ctx.Logger.Log(logCat, "login: user=%s ip=%s", name, ip)
    return &enginepb.AuthLoginResponse{
        UserId: user.UserID.String(), Username: user.Username,
        SessionToken: tok, ExpiresAtMs: expires.UnixMilli(),
    }, nil
}

func clientMeta(opCtx *ops.OpContext, ip netip.Addr) map[string]string {
    m := map[string]string{}
    if ip.IsValid() { m["ip"] = ip.String() }
    return m
}

// audit shims through to the repo with a fresh background ctx so handler
// failures don't get tangled in audit cancellation paths.
func (s *Service) audit(opCtx *ops.OpContext, event string, userID uuid.UUID, name string, meta map[string]any) {
    if err := s.repo.Audit(context.Background(), AuditEvent{
        Event: event, UserID: userID, UsernameAttempted: name,
        IPAddr: opCtx.ClientIP, GatewayID: "", Metadata: meta,
    }); err != nil {
        s.ctx.Logger.Log(logCat, "audit write failed: %v", err)
    }
}
```

Note: `uuid` import — add `"github.com/google/uuid"` at top.

- [ ] **Step 3: Test against authtest mock**

`pkg/auth/handlers_test.go`:

```go
package auth

import (
    "context"
    "net/netip"
    "testing"

    enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
    "github.com/zenion/mmoserver/pkg/auth/authtest"
    "github.com/zenion/mmoserver/pkg/logger"
    "github.com/zenion/mmoserver/pkg/ops"
    "github.com/zenion/mmoserver/pkg/service"
)

func newTestService(t *testing.T) (*Service, *authtest.RepoMock) {
    t.Helper()
    repo := authtest.NewMock()
    opts := DefaultServiceOpts()
    opts.Repository = repo
    s := newService(&service.Context{
        KindName: "auth", InstanceID: "test-0",
        Logger: logger.New(), Roles: map[string]struct{}{"service": {}},
    }, opts).(*Service)
    if err := s.Init(s.ctx); err != nil { t.Fatal(err) }
    t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
    return s, repo
}

func TestLoginUnknownUserReturnsInvalidCredentials(t *testing.T) {
    s, _ := newTestService(t)
    opCtx := &ops.OpContext{ConnID: 1, ClientIP: netip.MustParseAddr("1.1.1.1")}
    _, err := s.handleLogin(opCtx, &enginepb.AuthLoginRequest{Username: "ghost", Password: "x"})
    ae, ok := err.(*authError)
    if !ok || ae.Code != enginepb.AuthError_AUTH_ERROR_INVALID_CREDENTIALS {
        t.Fatalf("want INVALID_CREDENTIALS, got %v", err)
    }
}
```

- [ ] **Step 4: Run, confirm fails**

Run: `go test ./pkg/auth/ -run TestLogin`
Expected: FAIL — handler not yet wired or missing imports. Fix and re-run.

- [ ] **Step 5: Run, confirm passes after fixes**

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/auth/service.go pkg/auth/handlers.go pkg/auth/handlers_test.go
git commit -m "feat(auth): AUTH_LOGIN handler + rate limit + audit"
```

---

### Task 13: `AUTH_REGISTER` handler

**Files:**
- Modify: `pkg/auth/handlers.go`
- Modify: `pkg/auth/handlers_test.go`

- [ ] **Step 1: Add `handleRegister`**

Append to `pkg/auth/handlers.go`:

```go
func (s *Service) handleRegister(opCtx *ops.OpContext, req *enginepb.AuthRegisterRequest) (*enginepb.AuthRegisterResponse, error) {
    ip := opCtx.ClientIP
    if locked, retry := s.rl.CheckAndCount(ip, false); locked {
        return nil, errorWithRetry(enginepb.AuthError_AUTH_ERROR_RATE_LIMITED, retry, "too many attempts")
    }

    name, err := normalizeUsername(req.Username)
    if err != nil { return nil, err }

    if len(req.Password) < s.opts.PasswordMinLen {
        return nil, errorf(enginepb.AuthError_AUTH_ERROR_PASSWORD_TOO_WEAK,
            "password must be at least %d chars", s.opts.PasswordMinLen)
    }
    hash, err := HashPassword(req.Password, s.opts.Argon2id)
    if err != nil { return nil, errorf(enginepb.AuthError_AUTH_ERROR_INTERNAL, "hash: %v", err) }

    ctx := context.Background()
    user, err := s.repo.CreateUser(ctx, User{Username: name, Email: req.Email}, hash)
    if err != nil {
        if errors.Is(err, ErrUsernameTaken) {
            return nil, &authError{
                Code: enginepb.AuthError_AUTH_ERROR_USERNAME_TAKEN,
                Msg:  "username taken",
                Metadata: &enginepb.AuthErrorMetadata{Canonical: name},
            }
        }
        return nil, errorf(enginepb.AuthError_AUTH_ERROR_INTERNAL, "create: %v", err)
    }

    s.rl.CheckAndCount(ip, true)
    _ = s.repo.UpdateUserLogin(ctx, user.UserID, time.Now())

    tok, h, err := NewToken()
    if err != nil { return nil, errorf(enginepb.AuthError_AUTH_ERROR_INTERNAL, "token: %v", err) }
    expires := time.Now().Add(s.opts.SessionTTL)
    if err := s.repo.CreateSession(ctx, Session{
        TokenHash: h, UserID: user.UserID, ExpiresAt: expires, ClientMeta: clientMeta(opCtx, ip),
    }); err != nil { return nil, errorf(enginepb.AuthError_AUTH_ERROR_INTERNAL, "session: %v", err) }

    s.audit(opCtx, "register", user.UserID, name, nil)
    s.ctx.Logger.Log(logCat, "register: user=%s ip=%s", name, ip)
    return &enginepb.AuthRegisterResponse{
        UserId: user.UserID.String(), Username: user.Username,
        SessionToken: tok, ExpiresAtMs: expires.UnixMilli(),
    }, nil
}
```

- [ ] **Step 2: Add tests**

Append to `pkg/auth/handlers_test.go`:

```go
func TestRegisterCreatesUserAndSession(t *testing.T) {
    s, repo := newTestService(t)
    opCtx := &ops.OpContext{ConnID: 1, ClientIP: netip.MustParseAddr("1.1.1.1")}
    resp, err := s.handleRegister(opCtx, &enginepb.AuthRegisterRequest{Username: "Alice", Password: "hunter22"})
    if err != nil { t.Fatal(err) }
    if resp.Username != "alice" { t.Fatalf("want lowercased; got %s", resp.Username) }
    if resp.SessionToken == "" { t.Fatal("no session token") }

    // can subsequently log in
    if _, err := s.handleLogin(opCtx, &enginepb.AuthLoginRequest{Username: "alice", Password: "hunter22"}); err != nil {
        t.Fatalf("login after register: %v", err)
    }
    _ = repo
}

func TestRegisterDuplicateUsername(t *testing.T) {
    s, _ := newTestService(t)
    opCtx := &ops.OpContext{ConnID: 1, ClientIP: netip.MustParseAddr("1.1.1.1")}
    _, _ = s.handleRegister(opCtx, &enginepb.AuthRegisterRequest{Username: "bob", Password: "hunter22"})
    _, err := s.handleRegister(opCtx, &enginepb.AuthRegisterRequest{Username: "BOB", Password: "hunter22"})
    ae, ok := err.(*authError)
    if !ok || ae.Code != enginepb.AuthError_AUTH_ERROR_USERNAME_TAKEN {
        t.Fatalf("want USERNAME_TAKEN, got %v", err)
    }
    if ae.Metadata == nil || ae.Metadata.Canonical != "bob" {
        t.Fatalf("expected canonical='bob', got %+v", ae.Metadata)
    }
}

func TestRegisterPasswordTooWeak(t *testing.T) {
    s, _ := newTestService(t)
    opCtx := &ops.OpContext{ConnID: 1, ClientIP: netip.MustParseAddr("1.1.1.1")}
    _, err := s.handleRegister(opCtx, &enginepb.AuthRegisterRequest{Username: "carol", Password: "short"})
    ae, ok := err.(*authError)
    if !ok || ae.Code != enginepb.AuthError_AUTH_ERROR_PASSWORD_TOO_WEAK {
        t.Fatalf("want PASSWORD_TOO_WEAK, got %v", err)
    }
}
```

- [ ] **Step 3: Run, confirm pass**

Run: `go test ./pkg/auth/ -run TestRegister -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/auth/handlers.go pkg/auth/handlers_test.go
git commit -m "feat(auth): AUTH_REGISTER handler with auto-login + dup detection"
```

---

### Task 14: `AUTH_VALIDATE_TOKEN` handler

**Files:**
- Modify: `pkg/auth/handlers.go`
- Modify: `pkg/auth/handlers_test.go`

- [ ] **Step 1: Add `handleValidateToken`**

```go
func (s *Service) handleValidateToken(opCtx *ops.OpContext, req *enginepb.AuthValidateTokenRequest) (*enginepb.AuthValidateTokenResponse, error) {
    ip := opCtx.ClientIP
    if locked, retry := s.rl.CheckAndCount(ip, false); locked {
        return nil, errorWithRetry(enginepb.AuthError_AUTH_ERROR_RATE_LIMITED, retry, "rate limited")
    }
    if req.SessionToken == "" {
        return nil, errorf(enginepb.AuthError_AUTH_ERROR_TOKEN_INVALID, "empty token")
    }

    ctx := context.Background()
    h := HashToken(req.SessionToken)
    sess, err := s.repo.GetSession(ctx, h)
    if err != nil {
        s.audit(opCtx, "token_validate_failure", uuid.Nil, "", map[string]any{"reason": "unknown"})
        return nil, errorf(enginepb.AuthError_AUTH_ERROR_TOKEN_INVALID, "token unknown")
    }
    if !sess.RevokedAt.IsZero() {
        s.audit(opCtx, "token_validate_failure", sess.UserID, "", map[string]any{"reason": "revoked"})
        return nil, errorf(enginepb.AuthError_AUTH_ERROR_TOKEN_INVALID, "token revoked")
    }
    if time.Now().After(sess.ExpiresAt) {
        s.audit(opCtx, "token_validate_failure", sess.UserID, "", map[string]any{"reason": "expired"})
        return nil, errorf(enginepb.AuthError_AUTH_ERROR_TOKEN_EXPIRED, "token expired")
    }

    user, err := s.repo.GetUserByID(ctx, sess.UserID)
    if err != nil { return nil, errorf(enginepb.AuthError_AUTH_ERROR_INTERNAL, "user lookup: %v", err) }
    if user.Status == "disabled" || (!user.LockedUntil.IsZero() && user.LockedUntil.After(time.Now())) {
        return nil, errorf(enginepb.AuthError_AUTH_ERROR_ACCOUNT_LOCKED, "account not active")
    }

    newExp := time.Now().Add(s.opts.SessionTTL)
    _ = s.repo.SlideSession(ctx, h, newExp)
    s.rl.CheckAndCount(ip, true)
    return &enginepb.AuthValidateTokenResponse{
        UserId: user.UserID.String(), Username: user.Username, ExpiresAtMs: newExp.UnixMilli(),
    }, nil
}
```

- [ ] **Step 2: Add tests**

```go
func TestValidateTokenReconnect(t *testing.T) {
    s, _ := newTestService(t)
    opCtx := &ops.OpContext{ConnID: 1, ClientIP: netip.MustParseAddr("1.1.1.1")}
    reg, _ := s.handleRegister(opCtx, &enginepb.AuthRegisterRequest{Username: "evan", Password: "hunter22"})

    resp, err := s.handleValidateToken(opCtx, &enginepb.AuthValidateTokenRequest{SessionToken: reg.SessionToken})
    if err != nil { t.Fatal(err) }
    if resp.UserId != reg.UserId { t.Fatalf("user mismatch") }
    if resp.ExpiresAtMs <= reg.ExpiresAtMs { t.Fatal("expiry should slide") }
}

func TestValidateTokenInvalid(t *testing.T) {
    s, _ := newTestService(t)
    opCtx := &ops.OpContext{ConnID: 1, ClientIP: netip.MustParseAddr("1.1.1.1")}
    _, err := s.handleValidateToken(opCtx, &enginepb.AuthValidateTokenRequest{SessionToken: "deadbeef"})
    ae, ok := err.(*authError)
    if !ok || ae.Code != enginepb.AuthError_AUTH_ERROR_TOKEN_INVALID {
        t.Fatalf("want TOKEN_INVALID, got %v", err)
    }
}
```

- [ ] **Step 3: Run, confirm pass**

Run: `go test ./pkg/auth/ -run TestValidateToken -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/auth/handlers.go pkg/auth/handlers_test.go
git commit -m "feat(auth): AUTH_VALIDATE_TOKEN handler with sliding TTL"
```

---

### Task 15: `AUTH_LOGOUT` handler

**Files:**
- Modify: `pkg/auth/handlers.go`
- Modify: `pkg/auth/handlers_test.go`

- [ ] **Step 1: Add `handleLogout`**

```go
// AuthBoundSessionToken keys ops.OpContext for the bound session token
// of an authenticated connection. Set by the gateway after successful
// auth; missing/empty means caller is unauthenticated.
type ctxKey int
const sessionTokenKey ctxKey = 1

func SessionTokenFrom(opCtx *ops.OpContext) string {
    v, _ := opCtx.Bag().Load(sessionTokenKey)
    s, _ := v.(string)
    return s
}

func WithSessionToken(opCtx *ops.OpContext, token string) {
    opCtx.Bag().Store(sessionTokenKey, token)
}

func (s *Service) handleLogout(opCtx *ops.OpContext, _ *enginepb.AuthLogoutRequest) (*enginepb.AuthLogoutResponse, error) {
    tok := SessionTokenFrom(opCtx)
    if tok == "" {
        return nil, errorf(enginepb.AuthError_AUTH_ERROR_NOT_AUTHENTICATED, "no bound session")
    }
    h := HashToken(tok)
    ctx := context.Background()
    sess, err := s.repo.GetSession(ctx, h)
    if err != nil { return &enginepb.AuthLogoutResponse{}, nil }
    _ = s.repo.RevokeSession(ctx, h)
    s.audit(opCtx, "logout", sess.UserID, "", nil)
    s.ctx.Logger.Log(logCat, "logout: user=%s", sess.UserID)
    return &enginepb.AuthLogoutResponse{}, nil
}
```

Note: `OpContext.Bag()` is the per-context typed-bag the gateway uses to forward connection-bound state to handlers. If `Bag()` doesn't exist on the current `OpContext`, add a `sync.Map` field there in Task 18 alongside `ClientIP` and ensure the helper methods exist.

- [ ] **Step 2: Test**

```go
func TestLogoutRevokesSession(t *testing.T) {
    s, _ := newTestService(t)
    opCtx := &ops.OpContext{ConnID: 1, ClientIP: netip.MustParseAddr("1.1.1.1")}
    reg, _ := s.handleRegister(opCtx, &enginepb.AuthRegisterRequest{Username: "fay", Password: "hunter22"})
    WithSessionToken(opCtx, reg.SessionToken)

    if _, err := s.handleLogout(opCtx, &enginepb.AuthLogoutRequest{}); err != nil { t.Fatal(err) }

    // subsequent validate fails
    _, err := s.handleValidateToken(opCtx, &enginepb.AuthValidateTokenRequest{SessionToken: reg.SessionToken})
    if err == nil { t.Fatal("expected error after logout") }
}
```

- [ ] **Step 3: Run, confirm pass**

Run: `go test ./pkg/auth/ -run TestLogout -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/auth/handlers.go pkg/auth/handlers_test.go
git commit -m "feat(auth): AUTH_LOGOUT handler + session-token context helper"
```

---

### Task 16: `AUTH_CHANGE_PASSWORD` handler

**Files:**
- Modify: `pkg/auth/handlers.go`
- Modify: `pkg/auth/handlers_test.go`

- [ ] **Step 1: Add `handleChangePassword`**

```go
func (s *Service) handleChangePassword(opCtx *ops.OpContext, req *enginepb.AuthChangePasswordRequest) (*enginepb.AuthChangePasswordResponse, error) {
    tok := SessionTokenFrom(opCtx)
    if tok == "" {
        return nil, errorf(enginepb.AuthError_AUTH_ERROR_NOT_AUTHENTICATED, "no bound session")
    }
    h := HashToken(tok)
    ctx := context.Background()
    sess, err := s.repo.GetSession(ctx, h)
    if err != nil || !sess.RevokedAt.IsZero() {
        return nil, errorf(enginepb.AuthError_AUTH_ERROR_NOT_AUTHENTICATED, "invalid session")
    }
    pw, err := s.repo.GetPassword(ctx, sess.UserID)
    if err != nil { return nil, errorf(enginepb.AuthError_AUTH_ERROR_INTERNAL, "password lookup: %v", err) }
    ok, _ := VerifyPassword(req.CurrentPassword, pw.PasswordHash)
    if !ok {
        return nil, errorf(enginepb.AuthError_AUTH_ERROR_INVALID_CREDENTIALS, "current password mismatch")
    }
    if len(req.NewPassword) < s.opts.PasswordMinLen {
        return nil, errorf(enginepb.AuthError_AUTH_ERROR_PASSWORD_TOO_WEAK, "new password too short")
    }
    newHash, err := HashPassword(req.NewPassword, s.opts.Argon2id)
    if err != nil { return nil, errorf(enginepb.AuthError_AUTH_ERROR_INTERNAL, "hash: %v", err) }
    if err := s.repo.UpdatePassword(ctx, sess.UserID, newHash); err != nil {
        return nil, errorf(enginepb.AuthError_AUTH_ERROR_INTERNAL, "save: %v", err)
    }
    n, _ := s.repo.RevokeAllSessionsExcept(ctx, sess.UserID, h)
    s.audit(opCtx, "password_change", sess.UserID, "", map[string]any{"sessions_revoked": n})
    s.ctx.Logger.Log(logCat, "password change: user=%s sessions_revoked=%d", sess.UserID, n)
    return &enginepb.AuthChangePasswordResponse{}, nil
}
```

- [ ] **Step 2: Test**

```go
func TestChangePasswordRevokesOtherSessions(t *testing.T) {
    s, _ := newTestService(t)
    opCtx := &ops.OpContext{ConnID: 1, ClientIP: netip.MustParseAddr("1.1.1.1")}
    reg, _ := s.handleRegister(opCtx, &enginepb.AuthRegisterRequest{Username: "gail", Password: "hunter22"})
    // Open a second session via login
    second, _ := s.handleLogin(opCtx, &enginepb.AuthLoginRequest{Username: "gail", Password: "hunter22"})

    WithSessionToken(opCtx, reg.SessionToken)
    if _, err := s.handleChangePassword(opCtx, &enginepb.AuthChangePasswordRequest{
        CurrentPassword: "hunter22", NewPassword: "newer-than-eight",
    }); err != nil { t.Fatal(err) }

    // Original (current-context) session still works.
    if _, err := s.handleValidateToken(opCtx, &enginepb.AuthValidateTokenRequest{SessionToken: reg.SessionToken}); err != nil {
        t.Fatalf("current session should survive: %v", err)
    }
    // Second session is revoked.
    if _, err := s.handleValidateToken(opCtx, &enginepb.AuthValidateTokenRequest{SessionToken: second.SessionToken}); err == nil {
        t.Fatal("second session should be revoked")
    }
}

func TestChangePasswordCurrentMismatch(t *testing.T) {
    s, _ := newTestService(t)
    opCtx := &ops.OpContext{ConnID: 1, ClientIP: netip.MustParseAddr("1.1.1.1")}
    reg, _ := s.handleRegister(opCtx, &enginepb.AuthRegisterRequest{Username: "harry", Password: "hunter22"})
    WithSessionToken(opCtx, reg.SessionToken)
    _, err := s.handleChangePassword(opCtx, &enginepb.AuthChangePasswordRequest{
        CurrentPassword: "wrong", NewPassword: "newer-than-eight",
    })
    ae, ok := err.(*authError)
    if !ok || ae.Code != enginepb.AuthError_AUTH_ERROR_INVALID_CREDENTIALS {
        t.Fatalf("want INVALID_CREDENTIALS, got %v", err)
    }
}
```

- [ ] **Step 3: Run, confirm pass**

Run: `go test ./pkg/auth/ -run TestChangePassword -v`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/auth/handlers.go pkg/auth/handlers_test.go
git commit -m "feat(auth): AUTH_CHANGE_PASSWORD handler + revoke-other-sessions"
```

---

### Task 17: Background reaper

**Files:**
- Modify: `pkg/auth/service.go`

- [ ] **Step 1: Implement `reapLoop`**

Append to `pkg/auth/service.go`:

```go
import (
    "context"
    "time"
    // ... existing imports ...
)

func (s *Service) reapLoop() {
    defer s.reapWG.Done()
    t := time.NewTicker(s.opts.ReapInterval)
    defer t.Stop()
    for {
        select {
        case <-s.reapCh:
            return
        case <-t.C:
            s.reapOnce()
        }
    }
}

func (s *Service) reapOnce() {
    ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
    defer cancel()
    if n, err := s.repo.DeleteExpiredSessions(ctx, 7*24*time.Hour); err == nil && n > 0 {
        s.ctx.Logger.Log(logCat, "reaper: removed %d expired sessions", n)
    }
    if n, err := s.repo.DeleteOldAuditRows(ctx, s.opts.AuditRetention); err == nil && n > 0 {
        s.ctx.Logger.Log(logCat, "reaper: removed %d old audit rows", n)
    }
    if n := s.rl.Sweep(time.Hour); n > 0 {
        s.ctx.Logger.Log(logCat, "reaper: evicted %d idle ip buckets", n)
    }
}
```

- [ ] **Step 2: Add a one-shot reaper test**

Append to `pkg/auth/handlers_test.go`:

```go
func TestReaperOnceRunsWithoutError(t *testing.T) {
    s, _ := newTestService(t)
    s.reapOnce()
}
```

- [ ] **Step 3: Run**

Run: `go test ./pkg/auth/ -v`
Expected: all PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/auth/service.go pkg/auth/handlers_test.go
git commit -m "feat(auth): background reaper for expired sessions, old audit, idle ip buckets"
```

---

## Phase D — Services-framework extension

> **Execution order note:** Task 18 in this phase is a prerequisite for Phase C handler tasks (12–17). Subagent or in-line executors should run Task 18 immediately after Task 11 and before Task 12, even though it lives under "Phase D" in this document. The phase grouping is narrative — Task 18 touches `pkg/ops/`, not `pkg/auth/`, which is why it's grouped here. The dependency edge is `18 → {12, 13, 14, 15, 16, 17}`.

### Task 18: `OpContext.ClientIP` + per-context bag

**Files:**
- Modify: `pkg/ops/router.go`
- Modify: `pkg/universe/gateway.go` — populate `ClientIP` for routed ops
- Modify: `pkg/auth/handlers.go` — drop the inline `Bag()` reliance once shipped

- [ ] **Step 1: Add `ClientIP` and `Bag()` to `OpContext`**

In `pkg/ops/router.go`, replace the existing `OpContext` definition:

```go
import (
    "net/netip"
    "sync"
)

type OpContext struct {
    ConnID   uint32
    Username string
    ClientIP netip.Addr  // populated by gateway from WS RemoteAddr (or X-Forwarded-For when trusted)

    bag sync.Map
}

// Bag returns a per-context key/value store used to thread connection-
// bound state from the gateway into handlers (e.g. session token).
func (c *OpContext) Bag() *sync.Map { return &c.bag }
```

- [ ] **Step 2: Populate `ClientIP` in the gateway**

Find where the gateway constructs `OpContext` for each routed op (search `pkg/universe/gateway.go` for `OpContext{` or `ops.OpContext{`). Populate `ClientIP` from the WS connection's `RemoteAddr`. Add a config-conditional `X-Forwarded-For` parse if `cfg.TrustedProxyHeader` is set (resolve from gateway-side config plumb-through).

- [ ] **Step 3: Compile check + existing tests still pass**

Run: `go vet ./...`
Run: `go test ./pkg/ops/ ./pkg/universe/ -count=1`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add pkg/ops/router.go pkg/universe/gateway.go
git commit -m "feat(ops): OpContext.ClientIP + per-context Bag

Gateway populates ClientIP from WS RemoteAddr; handlers can store
connection-bound state via OpContext.Bag()."
```

---

## Phase E — Gateway integration (additive — existing flow still works)

### Task 19: `GatewayHook` for response interception

**Files:**
- Create: `pkg/auth/gateway_hook.go`

- [ ] **Step 1: Implement `GatewayHook`**

`pkg/auth/gateway_hook.go`:

```go
package auth

import (
    "time"

    "google.golang.org/protobuf/proto"

    enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
    "github.com/zenion/mmoserver/pkg/logger"
)

// GatewayHook lets the gateway extract identity-bind fields from auth
// service responses without depending on enginepb auth proto types
// directly. Auth registers an instance via mmokit.RegisterAuthService.
type GatewayHook struct {
    Logger     *logger.Logger
    OnSuccess  func(connID uint32, userID, username, sessionToken string, expiresAtMs int64)
    OnLogout   func(connID uint32)
}

// ProcessResponse is called by the gateway whenever a response from the
// "auth" kind is about to be forwarded to the client.
func (h *GatewayHook) ProcessResponse(connID uint32, opCode uint32, payload []byte) {
    if h == nil { return }
    switch enginepb.AuthOpCode(opCode) {
    case enginepb.AuthOpCode_AUTH_OPCODE_LOGIN:
        var m enginepb.AuthLoginResponse
        if err := proto.Unmarshal(payload, &m); err == nil && h.OnSuccess != nil {
            h.OnSuccess(connID, m.UserId, m.Username, m.SessionToken, m.ExpiresAtMs)
        } else if err != nil && h.Logger != nil {
            h.Logger.Log(logCat, "gateway hook: bad LoginResponse: %v", err)
        }
    case enginepb.AuthOpCode_AUTH_OPCODE_REGISTER:
        var m enginepb.AuthRegisterResponse
        if err := proto.Unmarshal(payload, &m); err == nil && h.OnSuccess != nil {
            h.OnSuccess(connID, m.UserId, m.Username, m.SessionToken, m.ExpiresAtMs)
        }
    case enginepb.AuthOpCode_AUTH_OPCODE_VALIDATE_TOKEN:
        var m enginepb.AuthValidateTokenResponse
        if err := proto.Unmarshal(payload, &m); err == nil && h.OnSuccess != nil {
            // ValidateToken response doesn't carry the session_token (client
            // already has it) — the gateway uses the request's token instead.
            h.OnSuccess(connID, m.UserId, m.Username, "", m.ExpiresAtMs)
        }
    case enginepb.AuthOpCode_AUTH_OPCODE_LOGOUT:
        if h.OnLogout != nil { h.OnLogout(connID) }
    }
    _ = time.Now() // keep import; remove if unused
}
```

- [ ] **Step 2: Compile check**

Run: `go vet ./pkg/auth/...`
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add pkg/auth/gateway_hook.go
git commit -m "feat(auth): GatewayHook lets gateway extract auth state without proto knowledge"
```

---

### Task 20: Gateway `authStates` + auth-kind op gating + post-auth dispatch

**Files:**
- Modify: `pkg/universe/gateway.go`

This task entangles three small changes; commit at the end.

- [ ] **Step 1: Add `authStates` map to gateway struct**

In `pkg/universe/gateway.go`, find the `Gateway` struct definition. Add:

```go
import (
    "github.com/google/uuid"
    "github.com/zenion/mmoserver/pkg/auth"
)

// authState is the per-connection auth binding. Cleared on WS close.
type authState struct {
    authenticated bool
    userID        uuid.UUID
    username      string
    sessionToken  string
    expiresAtMs   int64
    authedAt      time.Time
}

// On Gateway struct, add:
//   authMu      sync.Mutex
//   authStates  map[uint32]authState  // keyed by connID
//   authHook    *auth.GatewayHook
//   authRouter  PlayerRouter           // post-auth assignment dispatcher
```

Initialize `authStates` in the gateway constructor.

- [ ] **Step 2: Add op gating**

Find the gateway's per-op routing path (the function that decodes ops and dispatches to a service kind — search `dispatchOp` or `route`). Before forwarding to the service, add:

```go
// Auth-kind op gating: unauthenticated connIDs may only call auth-kind ops.
if !g.isAuthenticated(connID) {
    if g.opRouting.opToKind[opCode] != auth.KindName {
        g.replyError(connID, requestID, opCode, int32(enginepb.AuthError_AUTH_ERROR_NOT_AUTHENTICATED), "not authenticated")
        return
    }
}
```

`isAuthenticated` reads from `authStates` under the mutex.

- [ ] **Step 3: Wire response interception**

Find the gateway's response-forwarding path (search `forwardResponse` or `sendOpResponse`). Before sending to the client, add:

```go
if g.authHook != nil && g.opRouting.opToKind[opCode] == auth.KindName {
    g.authHook.ProcessResponse(connID, opCode, payload)
}
```

- [ ] **Step 4: Implement `OnSuccess` callback**

In the gateway constructor, after creating the gateway, wire the callback:

```go
g.authHook = &auth.GatewayHook{
    Logger: g.engine.Logger,
    OnSuccess: func(connID uint32, userIDStr, username, token string, expiresAtMs int64) {
        uid, err := uuid.Parse(userIDStr)
        if err != nil {
            g.engine.Logger.Log("gateway", "auth: bad user_id %q from auth: %v", userIDStr, err)
            return
        }
        g.authMu.Lock()
        prev := g.authStates[connID]
        // For ValidateToken responses, token is empty — preserve from request.
        if token == "" { token = prev.sessionToken }
        g.authStates[connID] = authState{
            authenticated: true,
            userID:        uid,
            username:      username,
            sessionToken:  token,
            expiresAtMs:   expiresAtMs,
            authedAt:      time.Now(),
        }
        g.authMu.Unlock()
        // Dispatch PlayerAssignment via PlayerRouter (existing path).
        g.dispatchPostAuthAssignment(connID, uid, username, token)
    },
    OnLogout: func(connID uint32) {
        g.authMu.Lock(); delete(g.authStates, connID); g.authMu.Unlock()
        // Close WS — client returns to login screen.
        g.connMgr.Close(connID)
    },
}
```

- [ ] **Step 5: Implement `dispatchPostAuthAssignment` + capture session-token from request**

Add a method:

```go
func (g *Gateway) dispatchPostAuthAssignment(connID uint32, userID uuid.UUID, username, token string) {
    if g.authRouter == nil { return }
    cellID := g.authRouter(userID, username)
    sess := &localSession{
        connID:   connID,
        userID:   userID,
        username: username,
        cellID:   cellID,
        token:    token,
        // (existing fields filled in here per current dispatchPlayerAssignment usage)
    }
    if err := g.dispatchPlayerAssignment(sess, nil); err != nil {
        g.engine.Logger.Log("gateway", "post-auth assignment failed: connID=%d user=%s err=%v", connID, userID, err)
    }
}
```

Note: `localSession` is the existing internal type; ensure it gains `userID` and `token` fields in Task 22 — for now, leave them as fields here that we'll wire up next phase.

- [ ] **Step 6: Capture session-token on AUTH_VALIDATE_TOKEN request**

Before forwarding an `AUTH_VALIDATE_TOKEN` op, capture the request's token so the `OnSuccess` callback can preserve it (since the response doesn't carry it):

```go
// In op-dispatch path, before forwarding to the auth kind:
if opCode == uint32(enginepb.AuthOpCode_AUTH_OPCODE_VALIDATE_TOKEN) {
    var req enginepb.AuthValidateTokenRequest
    if err := proto.Unmarshal(reqPayload, &req); err == nil {
        g.authMu.Lock()
        st := g.authStates[connID]
        st.sessionToken = req.SessionToken
        g.authStates[connID] = st
        g.authMu.Unlock()
    }
}
```

Also capture session-token on AUTH_LOGOUT and AUTH_CHANGE_PASSWORD via `auth.WithSessionToken(opCtx, ...)` — these handlers need the token from the connection's bound state. Add to the gateway's per-op `OpContext` construction:

```go
g.authMu.Lock()
tok := g.authStates[connID].sessionToken
g.authMu.Unlock()
opCtx := &ops.OpContext{ConnID: connID, ClientIP: clientIP}
if tok != "" { auth.WithSessionToken(opCtx, tok) }
```

- [ ] **Step 7: WS close clears authStates**

Find the WS-close handler in the gateway. Add:

```go
g.authMu.Lock(); delete(g.authStates, connID); g.authMu.Unlock()
```

- [ ] **Step 8: No commit yet — entangled with Tasks 21–22**

Tasks 20, 21, and 22 form a single entangled commit set. Task 20 references `localSession.userID`, `localSession.token`, the new `PlayerRouter` signature, and the new `PlayerAssignment` fields — all of which land in Task 22. Task 21 wires the constructor against Task 22's new `PlayerRouter` type.

Do NOT commit at the end of Task 20. Continue to Task 21, then Task 22. Task 22's final commit captures all three. `go vet ./...` is expected to fail until Task 22's step 11.

---

### Task 21: Gateway constructor wiring `authRouter`

**Files:**
- Modify: `pkg/universe/coordinator.go` — `SetPlayerRouter` accepts the new signature
- Modify: `pkg/universe/gateway.go` — store `authRouter` from coord config

- [ ] **Step 1: Add `authRouter` field on Gateway struct**

Already added in Task 20 step 1 (`authRouter PlayerRouter`).

- [ ] **Step 2: Wire it through coordinator**

In `pkg/universe/coordinator.go`, where `Gateway` is constructed (search `NewGateway` or similar), pass `cfg.PlayerRouter` into the gateway. The gateway stores it as `authRouter`.

- [ ] **Step 3: Compile check**

Skip to Task 22 — `PlayerRouter` signature changes there.

---

## Phase F — Schema migration (entangled cleanup, single commit)

### Task 22: Big migration — proto + signatures + delete login.go

**Files:**
- Modify: `proto/meshpb/mesh.proto`
- Regenerate: `gen/go/meshpb/`
- Modify: `pkg/universe/coordinator.go` — `Config.LoginHandler` removal, `PlayerRouter` signature, `activeUsers` keyed by UUID, kick-old policy
- Modify: `pkg/universe/gateway.go` — `localSession.userID`/`.token`, MeshFrame_PlayerAssignment uses new fields
- Modify: `pkg/universe/cell_transfer_executor.go` — PlayerAssignment construction sites
- Modify: any caller of `PlayerSession.Data` — drop the field
- Delete: `pkg/universe/login.go` (whole file)
- Modify: `pkg/engine/types.go` (or wherever `PlayerSession` is defined) — add `UserID uuid.UUID`, drop `Data any`
- Modify: `pkg/mmokit/mmokit.go` — drop re-exports of `LoginHandler`, `HandleLogin`, `ValidateUsername`, `ErrLoginPending`
- Modify: `pkg/universe/login_test.go` (and any other test that uses LoginHandler) — convert to use `RegisterAuthServiceWithMock` (defined in Task 23) or just delete

This is one task with many sub-steps and one commit. After this, the build is green again.

- [ ] **Step 1: Update `proto/meshpb/mesh.proto`**

Replace the existing `PlayerAssignment` message (renumber from 1; per `feedback_proto_field_cleanup`, no `reserved`):

```proto
message PlayerAssignment {
  string   from_cell_id    = 1;
  string   to_cell_id      = 2;
  uint32   conn_id         = 3;
  string   gateway_id      = 4;
  string   user_id         = 5;   // NEW: UUID string
  string   username        = 6;
  string   session_token   = 7;   // NEW: opaque session token from auth
  bool     is_reconnect    = 8;
  uint64   epoch           = 9;
  Location spawn_location  = 10;
}
```

(The `bytes data` field is dropped — see `PlayerSession.Data` removal below.)

- [ ] **Step 2: Regenerate**

Run: `just proto`
Expected: `gen/go/meshpb/` updates.

- [ ] **Step 3: Update `PlayerSession`**

Find `PlayerSession` struct (likely `pkg/engine/types.go` or `pkg/universe/`). Replace:

```go
import "github.com/google/uuid"

type PlayerSession struct {
    ConnID        uint32
    UserID        uuid.UUID  // NEW
    Username      string
    // Data field removed
    SpawnLocation coords.Location
    // ... existing fields unchanged
}
```

Update every constructor and field-touch site.

- [ ] **Step 4: Update `PlayerRouter` signature**

Define in a non-deleted location (e.g. `pkg/universe/router.go` new file) since `login.go` is going away:

```go
package universe

import "github.com/google/uuid"

// PlayerRouter resolves an authenticated player to their target cell.
type PlayerRouter func(userID uuid.UUID, username string) string
```

Update every caller. Update `Config.PlayerRouter` type. Update `DefaultPlayerRouter` in `bootstrap.go` to accept the new signature.

- [ ] **Step 5: Update `Coordinator.activeUsers` to UUID keying**

Find the existing `activeUsers map[string]CellID` (search `activeUsers`). Change to `map[uuid.UUID]CellID`. Update every reader/writer. Adapt the duplicate-detection path:

```go
// Old: rejectIfDuplicate(username) -> error
// New: kickIfDuplicate(userID) -> previous CellID + connID, sends SE_KICKED
func (c *Coordinator) onLoginCompleted(userID uuid.UUID, username string, connID uint32, cellID CellID) {
    c.activeMu.Lock()
    if existing, ok := c.activeUsers[userID]; ok {
        // Kick the existing connection
        c.activeMu.Unlock()
        c.kickConnection(existing.gatewayID, existing.connID, "replaced_by_new_session")
        c.activeMu.Lock()
    }
    c.activeUsers[userID] = cellID
    c.activeMu.Unlock()
}
```

The `kickConnection` helper sends an `SE_KICKED` event to the holding gateway and then closes the WS. Wire `SE_KICKED` if not already in `enginepb`.

- [ ] **Step 6: Update `localSession` and PlayerAssignment dispatches**

In `pkg/universe/gateway.go`, add `userID uuid.UUID` and `token string` fields to `localSession`. Update every PlayerAssignment construction site:

```go
&meshpb.PlayerAssignment{
    FromCellId:     "",
    ToCellId:       sess.cellID,
    ConnId:         sess.connID,
    GatewayId:      g.id,
    UserId:         sess.userID.String(),
    Username:       sess.username,
    SessionToken:   sess.token,
    IsReconnect:    isReconnect,
    Epoch:          epoch,
    SpawnLocation:  spawn,
}
```

In `cell_transfer_executor.go` and any other PlayerAssignment construction site, propagate `UserId` and `SessionToken` (zero values are fine for cell-transfer paths where auth has already happened).

- [ ] **Step 7: Delete `pkg/universe/login.go`**

```bash
rm pkg/universe/login.go
```

This removes `ErrLoginPending`, `LoginHandler`, `HandleLogin`, `ValidateUsername`, `loginService`, `pendingConn`, `processLogins`, `loginResult`, and the old `PlayerRouter` typedef.

- [ ] **Step 8: Remove `Config.LoginHandler` and login-drain phase**

In `pkg/universe/coordinator.go`:
- Delete the `LoginHandler` field from `Config`
- Delete the login-drain phase from the coordinator's tick (search `processLogins` or `loginService`)
- Delete the `pendingConn`/`loginService` initialization

- [ ] **Step 9: Drop mmokit re-exports**

In `pkg/mmokit/mmokit.go` find and delete:
- `type LoginHandler = universe.LoginHandler`
- `type ErrLoginPending = universe.ErrLoginPending` (or `var`)
- `func HandleLogin[...] ...`
- `func ValidateUsername(...)`

(Anything referenced by `examples/4node-basic/main.go` will break. That's expected — Task 25 fixes example wiring.)

- [ ] **Step 10: Delete `pkg/universe/login_test.go` and `stubLoginHandler` references**

Delete the test file outright — every test in it exercises machinery that no longer exists (`HandleLogin`, `LoginHandler`, `loginService`, `processLogins`).

Then `grep -l stubLoginHandler pkg/universe/*_test.go` to find any other tests that use the stub helper. For each:

- If the test is asserting login-flow behavior specifically: delete the test.
- If the test just needs "an authenticated player exists" as setup: delete the `cfg.LoginHandler = stubLoginHandler` line and the test will be re-fixtured in Task 23 (which adds `RegisterAuthServiceWithMock` as the new test entrypoint).

Do NOT leave commented-out tests behind. Either it goes or it migrates cleanly in Task 23.

- [ ] **Step 11: Compile check**

Run: `go vet ./...`
Expected: errors in `examples/4node-basic/main.go` (still references `mmokit.HandleLogin`) — leave for Task 25.
Expected: errors in `pkg/universe/login_test.go` — fix in Task 23 / 24.
Expected: `pkg/auth/`, `pkg/universe/coordinator.go`, `pkg/universe/gateway.go`, `pkg/universe/cell_transfer_executor.go` should now compile.

- [ ] **Step 12: Commit**

```bash
git add proto/meshpb/mesh.proto gen/go/meshpb/ pkg/universe/ pkg/engine/ pkg/mmokit/mmokit.go
git rm pkg/universe/login.go
git commit -m "refactor: schema migration to UUID identity + delete login.go

- proto: PlayerAssignment renumbered, adds user_id + session_token, drops data
- pkg/engine PlayerSession: drop Data, add UserID
- PlayerRouter: func(userID, username) string
- Coordinator.activeUsers: keyed by UUID; kick-old on duplicate
- delete pkg/universe/login.go in full (LoginHandler, HandleLogin,
  ValidateUsername, loginService — replaced by pkg/auth service)
- drop mmokit re-exports of login machinery"
```

---

## Phase G — mmokit facade + consumers

### Task 23: `mmokit.RegisterAuthService` + `RegisterAuthServiceWithMock`

**Files:**
- Create: `pkg/mmokit/auth.go`

- [ ] **Step 1: Implement facade**

```go
package mmokit

import (
    "fmt"

    "github.com/zenion/mmoserver/pkg/auth"
    "github.com/zenion/mmoserver/pkg/universe"
)

// AuthOpts re-exports auth.ServiceOpts for game code.
type AuthOpts = auth.ServiceOpts

// AuthRepository re-exports auth.Repository for tests injecting a mock.
type AuthRepository = auth.Repository

// DefaultAuthOpts returns sane defaults (30d TTL, argon2id-OWASP-2024,
// 5-attempt 15min lockout, 90d audit retention).
func DefaultAuthOpts() AuthOpts { return auth.DefaultServiceOpts() }

// RegisterAuthService registers the engine-tier auth kind on the
// coordinator, registers the gateway hook, and adds Postgres migrations
// to Config.ExtraMigrations. Idempotent. Must be called BEFORE
// coord.Build().
//
// The game must include "auth" in --services= when running with
// --mode=...,service for the kind to instantiate.
func RegisterAuthService(p *universe.Process, opts AuthOpts) error {
    if opts.SessionTTL == 0 { opts = DefaultAuthOpts() }
    if err := p.RegisterService(auth.Kind(opts)); err != nil {
        return fmt.Errorf("RegisterAuthService: %w", err)
    }
    p.AddGatewayAuthHook(opts) // wire gateway-side hook (gateway.go helper added in Task 20)
    p.AppendExtraMigrations(auth.MigrationsFS())
    return nil
}

// RegisterAuthServiceWithMock registers auth using an in-memory
// AuthRepository — for tests that want a real auth flow without a
// Postgres dependency.
func RegisterAuthServiceWithMock(p *universe.Process, repo AuthRepository) error {
    opts := DefaultAuthOpts()
    opts.Repository = repo
    return RegisterAuthService(p, opts)
}

// (No layered helper needed — Config.ExtraMigrations is now a []fs.FS
// and AppendExtraMigrations on Process appends to it.)
```

Note: `auth.Kind(opts)` is the exported wrapper around the unexported `kindFor(opts)` helper from Task 10 — make `Kind` public:

```go
// In pkg/auth/kind.go:
func Kind(opts ServiceOpts) service.Kind { return kindFor(opts) }
```

`AddGatewayAuthHook`, `AppendExtraMigrations`, and `Config()` accessors on `*universe.Process` are small additions added here so the facade can wire everything.

- [ ] **Step 2: Add the gateway hook accessor**

In `pkg/universe/coordinator.go`, add:

```go
// AddGatewayAuthHook installs the auth response interceptor on the
// process's gateway. No-op if the process doesn't have RoleGateway.
func (p *Process) AddGatewayAuthHook(opts auth.ServiceOpts) {
    if !p.roles.Has(RoleGateway) { return }
    // The actual wiring happens in gateway.go where the gateway is constructed.
    p.cfg.authOpts = &opts
}

func (p *Process) AppendExtraMigrations(fs fs.FS) {
    p.cfg.ExtraMigrations = append(p.cfg.ExtraMigrations, fs)
}
func (p *Process) Config() *Config { return &p.cfg }
```

In gateway construction (Task 20 step 4), if `p.cfg.authOpts != nil`, instantiate the `auth.GatewayHook`.

- [ ] **Step 3: Test — register with mock and assert kind shows up**

`pkg/mmokit/auth_test.go`:

```go
package mmokit_test

import (
    "testing"

    "github.com/zenion/mmoserver/pkg/auth/authtest"
    "github.com/zenion/mmoserver/pkg/mmokit"
    "github.com/zenion/mmoserver/pkg/universe"
)

func TestRegisterAuthServiceWithMock(t *testing.T) {
    p := universe.New(universe.Config{Mode: "all"})
    if err := mmokit.RegisterAuthServiceWithMock(p, authtest.NewMock()); err != nil {
        t.Fatal(err)
    }
    if err := p.Build(); err != nil { t.Fatal(err) }
    // After Build, the auth kind should be in the registry.
}
```

- [ ] **Step 4: Run, confirm pass**

Run: `go test ./pkg/mmokit/ -run TestRegisterAuthServiceWithMock -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/mmokit/auth.go pkg/mmokit/auth_test.go pkg/auth/kind.go pkg/universe/coordinator.go
git commit -m "feat(mmokit): RegisterAuthService + RegisterAuthServiceWithMock

One-call wiring: kind registration, gateway hook, ExtraMigrations chain.
Mock variant uses authtest.RepoMock for tests without Postgres."
```

---

### Task 24: Console commands

**Files:**
- Create: `pkg/auth/console.go`

- [ ] **Step 1: Implement cmdsys-typed commands**

`pkg/auth/console.go`:

```go
package auth

import (
    "context"

    "github.com/google/uuid"

    "github.com/zenion/mmoserver/pkg/cmdsys"
)

// RegisterConsoleCommands adds the auth.* command group to the cmdsys
// dispatcher. Called from RegisterAuthService.
func RegisterConsoleCommands(reg *cmdsys.Registry, repo Repository) {
    reg.Register(cmdsys.Command{
        Verb: "auth.user.list", Capability: "admin", Route: cmdsys.RouteLocal,
        Handler: func(ctx context.Context, _ cmdsys.Caller, _ struct{}) (any, error) {
            // simplified: in Postgres we'd page; mock returns all
            return repo.ListActiveSessions(ctx, uuid.Nil)
        },
    })
    reg.Register(cmdsys.Command{
        Verb: "auth.user.lock", Capability: "admin", Route: cmdsys.RouteLocal,
        Handler: func(ctx context.Context, c cmdsys.Caller, args struct{ Username, Duration string }) (any, error) {
            u, err := repo.GetUserByUsername(ctx, args.Username)
            if err != nil { return nil, err }
            // parse args.Duration → time.Duration (use time.ParseDuration)
            // set SetUserStatus + RevokeAllSessionsForUser
            _ = u
            return struct{ OK bool }{true}, nil
        },
    })
    reg.Register(cmdsys.Command{
        Verb: "auth.user.unlock", Capability: "admin", Route: cmdsys.RouteLocal,
        Handler: func(ctx context.Context, _ cmdsys.Caller, args struct{ Username string }) (any, error) {
            u, err := repo.GetUserByUsername(ctx, args.Username)
            if err != nil { return nil, err }
            return struct{ OK bool }{true}, repo.ResetFailedAttempts(ctx, u.UserID)
        },
    })
    reg.Register(cmdsys.Command{
        Verb: "auth.user.kick", Capability: "admin", Route: cmdsys.RouteLocal,
        Handler: func(ctx context.Context, _ cmdsys.Caller, args struct{ Username string }) (any, error) {
            u, err := repo.GetUserByUsername(ctx, args.Username)
            if err != nil { return nil, err }
            n, err := repo.RevokeAllSessionsForUser(ctx, u.UserID)
            return struct{ Revoked int }{n}, err
        },
    })
    reg.Register(cmdsys.Command{
        Verb: "auth.session.list", Capability: "admin", Route: cmdsys.RouteLocal,
        Handler: func(ctx context.Context, _ cmdsys.Caller, args struct{ Username string }) (any, error) {
            u, err := repo.GetUserByUsername(ctx, args.Username)
            if err != nil { return nil, err }
            sessions, err := repo.ListActiveSessions(ctx, u.UserID)
            // Redact: show only token-hash prefix (first 8 hex)
            for i := range sessions { sessions[i].TokenHash = sessions[i].TokenHash[:8] }
            return sessions, err
        },
    })
    reg.Register(cmdsys.Command{
        Verb: "auth.audit.recent", Capability: "admin", Route: cmdsys.RouteLocal,
        Handler: func(ctx context.Context, _ cmdsys.Caller, args struct{ Username string; Limit int }) (any, error) {
            u, err := repo.GetUserByUsername(ctx, args.Username)
            if err != nil { return nil, err }
            limit := args.Limit; if limit <= 0 { limit = 50 }
            return repo.RecentAudit(ctx, u.UserID, limit)
        },
    })
}
```

- [ ] **Step 2: Wire into `RegisterAuthService`**

In `pkg/mmokit/auth.go`, after `p.RegisterService(...)`, call:

```go
auth.RegisterConsoleCommands(p.CmdRegistry(), repo)
```

(Add `CmdRegistry()` accessor to `*universe.Process` if not present.)

- [ ] **Step 3: Compile check**

Run: `go vet ./...`
Expected: no errors.

- [ ] **Step 4: Commit**

```bash
git add pkg/auth/console.go pkg/mmokit/auth.go pkg/universe/coordinator.go
git commit -m "feat(auth): console commands — user list/lock/unlock/kick, session list, audit recent"
```

---

### Task 25: `examples/4node-basic` wiring

**Files:**
- Modify: `examples/4node-basic/main.go`
- Modify: `examples/4node-basic/justfile` (add `--services=auth` to distributed recipe)

- [ ] **Step 1: Replace `LoginHandler` setup with `RegisterAuthService`**

In `examples/4node-basic/main.go`:

```go
// Old (delete):
// cfg.LoginHandler = mmokit.HandleLogin(
//     basicpb.ClientEventCode_BCE_LOGIN,
//     func(m *basicpb.LoginMsg) (string, any, error) {
//         return mmokit.ValidateUsername(m.Name, 20)
//     },
// )

// New: after coord := mmokit.New(cfg)
if err := mmokit.RegisterAuthService(coord, mmokit.DefaultAuthOpts()); err != nil {
    log.Fatalf("RegisterAuthService: %v", err)
}
```

- [ ] **Step 2: Update `SetPlayerRouter` to new signature**

```go
// Old:
// coord.SetPlayerRouter(func(username string) string {
//     return coord.CellAtPosition(spawnX, spawnY)
// })

// New:
coord.SetPlayerRouter(func(userID uuid.UUID, username string) string {
    return coord.CellAtPosition(spawnX, spawnY)
})
```

- [ ] **Step 3: Update `OnPlayerJoin` hook to use `s.UserID`**

```go
coord.OnPlayerJoin(func(s *mmokit.PlayerSession, stage *mmokit.Stage) {
    // s.UserID and s.Username available
    stage.SpawnPlayer(s, mmokit.WithEntityKind(KindShip))
})
```

- [ ] **Step 4: Update justfile distributed recipe**

In `examples/4node-basic/justfile`, find `distributed:` and add `--services=auth` to the `--mode=...,service` lines:

```
--mode=all --services=auth   # for the all-in-one process
```

For multi-process recipe, run an additional `--mode=service --services=auth` process.

- [ ] **Step 5: Compile check**

Run: `cd examples/4node-basic && go build -o /dev/null ./...`
Expected: PASS (modulo errors from Task 26 / 27 — fix as we go).

If compilation fails because `basicpb.LoginMsg` is referenced elsewhere in the example, comment out those references; Task 26 deletes the proto.

- [ ] **Step 6: Commit**

```bash
git add examples/4node-basic/main.go examples/4node-basic/justfile
git commit -m "feat(4node-basic): wire RegisterAuthService instead of inline LoginHandler"
```

---

### Task 26: Delete `LoginMsg` from basicpb

**Files:**
- Modify: `examples/4node-basic/proto/basicpb/basic.proto`
- Regenerate: `gen/go/basicpb/`, `gen/es/basicpb/`

- [ ] **Step 1: Remove `LoginMsg` and `BCE_LOGIN`**

In `examples/4node-basic/proto/basicpb/basic.proto`, delete:
- `message LoginMsg`
- The `BCE_LOGIN` enum value from `BasicClientEventCode`

Per `feedback_proto_field_cleanup`, do not add `reserved`. Renumber remaining `BasicClientEventCode` enum values from 0/1 if needed.

- [ ] **Step 2: Regenerate**

Run: `just proto`
Expected: `gen/go/basicpb/` and `gen/es/basicpb/` regenerate.

- [ ] **Step 3: Verify example builds**

Run: `cd examples/4node-basic && go vet ./...`
Expected: any remaining `LoginMsg` references are flagged. Delete those references — there shouldn't be any after Task 25.

- [ ] **Step 4: Commit**

```bash
git add examples/4node-basic/proto/basicpb/ gen/go/basicpb/ gen/es/basicpb/
git commit -m "feat(basicpb): drop LoginMsg + BCE_LOGIN — auth service replaces them"
```

---

### Task 27: `internal/game` — UUID identity ripple

**Files:**
- Modify: `internal/game/world.go` (or wherever `GameWorld` is defined)
- Modify: `internal/game/factory.go`
- Modify: `internal/game/entity_player.go`
- Modify: `internal/game/player_db.go` (or wherever `PlayerDB` lives)
- Modify: `pkg/persist/postgres/migrations/` — `players` table PK becomes `user_id`

- [ ] **Step 1: Add `players.user_id` UUID PK migration**

Create a new engine migration (find next number in `pkg/persist/postgres/migrations/`):

```sql
-- 00X_players_user_id.up.sql
ALTER TABLE players DROP CONSTRAINT players_pkey;
ALTER TABLE players ADD COLUMN user_id UUID;
ALTER TABLE players ADD CONSTRAINT players_user_id_pkey PRIMARY KEY (user_id);
-- Note: solo dev, no real users — wipe + recreate is the deployment story.
-- If existing rows present in dev, this migration will fail; run db-reset first.
ALTER TABLE players ADD CONSTRAINT players_user_id_fkey
    FOREIGN KEY (user_id) REFERENCES auth_users(user_id) ON DELETE CASCADE;
ALTER TABLE players ALTER COLUMN username TYPE TEXT;
CREATE INDEX players_username ON players(username);
```

The down migration restores the old `username`-PK shape.

Note: this migration runs as part of engine migrations (not `pkg/auth/`), so the engine needs to know about `auth_users` first — which it doesn't, since engine migrations run before `ExtraMigrations`. The fix: make this migration conditional, or move the `players` PK migration into `pkg/auth/postgres/migrations/` since auth owns the constraint relationship.

Better: move it to `pkg/auth/postgres/migrations/002_players_user_id.up.sql`, since auth migrations run after engine migrations and after `auth_users` is created.

- [ ] **Step 2: Update `PlayerRepository` interface**

Change `Load(ctx, username)` to `Load(ctx, userID uuid.UUID)`. Add `LoadByUsername(ctx, username) (*PlayerSnapshot, error)` for cases where only username is known. Update Postgres impl.

- [ ] **Step 3: Update `internal/game.PlayerDB`**

Change in-memory cache from `map[string]*PlayerData` to `map[uuid.UUID]*PlayerData`. Update `GetOrCreate` to take `userID uuid.UUID, username string`. Mark dirty by user_id.

- [ ] **Step 4: Update `GameWorld`**

```go
// Old:
// PlayerEntities  map[uint32]ecs.Entity
// ConnToUsername  map[uint32]string

// New:
PlayerEntities  map[uint32]ecs.Entity     // unchanged
ConnToUserID    map[uint32]uuid.UUID
UserIDToConn    map[uuid.UUID]uint32      // for kick-by-username admin paths
```

- [ ] **Step 5: Update `entity_player.go` spawn signature**

```go
func SpawnPlayer(gw *GameWorld, sess *mmokit.PlayerSession) ecs.Entity {
    // sess.UserID, sess.Username
    // PlayerDB.GetOrCreate(sess.UserID, sess.Username)
    // ...
}
```

- [ ] **Step 6: Update every caller**

Run: `go vet ./internal/...`
Fix every error. The work is mechanical — changing `username` arguments to `userID` plus `username` where needed.

- [ ] **Step 7: Run game tests**

Run: `go test ./internal/...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/ pkg/persist/ pkg/auth/postgres/migrations/
git commit -m "refactor(internal/game): keying by user_id (UUID) instead of username

PlayerDB, GameWorld maps, entity_player.SpawnPlayer all migrate to UUID keys.
Username remains a denormalized display attribute. Players table PK is now
user_id with FK to auth_users."
```

---

### Task 28: Web client login/register UI

**Files:**
- Create: `examples/4node-basic/web/src/login_panel.ts`
- Modify: `examples/4node-basic/web/src/main.ts` — show login panel before world
- Regenerate: SDK (`just client-sdk examples/4node-basic`)

- [ ] **Step 1: Regenerate SDK to get auth ops**

Run: `just client-sdk examples/4node-basic`
Expected: `examples/4node-basic/web/sdk/` updates with typed `authLogin`, `authRegister`, `authValidateToken`, `authLogout`, `authChangePassword` methods.

- [ ] **Step 2: Create `login_panel.ts`**

`examples/4node-basic/web/src/login_panel.ts`:

```typescript
import type { GameClient } from "../sdk/client";

const TOKEN_KEY = "mmokit-auth-token";

export async function showLoginPanel(client: GameClient): Promise<{ userId: string; username: string }> {
    return new Promise((resolve, reject) => {
        // Try existing token first.
        const stored = localStorage.getItem(TOKEN_KEY);
        if (stored) {
            client.authValidateToken({ sessionToken: stored }).then(resp => {
                resolve({ userId: resp.userId, username: resp.username });
            }).catch(() => {
                localStorage.removeItem(TOKEN_KEY);
                renderForm(client, resolve, reject);
            });
            return;
        }
        renderForm(client, resolve, reject);
    });
}

function renderForm(
    client: GameClient,
    resolve: (id: { userId: string; username: string }) => void,
    reject: (err: Error) => void,
) {
    const overlay = document.createElement("div");
    overlay.id = "auth-overlay";
    overlay.style.cssText = "position:fixed;inset:0;background:rgba(0,0,0,0.85);display:flex;align-items:center;justify-content:center;z-index:1000;font-family:monospace;color:#eee;";
    overlay.innerHTML = `
        <div style="background:#222;padding:24px;border:1px solid #444;min-width:320px;">
            <h2 style="margin-top:0;">login</h2>
            <input id="auth-username" placeholder="username" style="width:100%;padding:8px;margin:4px 0;background:#111;color:#eee;border:1px solid #444;" />
            <input id="auth-password" type="password" placeholder="password" style="width:100%;padding:8px;margin:4px 0;background:#111;color:#eee;border:1px solid #444;" />
            <button id="auth-login" style="width:48%;padding:10px;margin-right:4%;">login</button>
            <button id="auth-register" style="width:48%;padding:10px;">register</button>
            <div id="auth-error" style="color:#f55;margin-top:8px;min-height:1.2em;"></div>
        </div>
    `;
    document.body.appendChild(overlay);

    const usernameEl = overlay.querySelector<HTMLInputElement>("#auth-username")!;
    const passwordEl = overlay.querySelector<HTMLInputElement>("#auth-password")!;
    const errorEl = overlay.querySelector<HTMLDivElement>("#auth-error")!;
    const submit = async (kind: "login" | "register") => {
        errorEl.textContent = "";
        try {
            const resp = kind === "login"
                ? await client.authLogin({ username: usernameEl.value, password: passwordEl.value })
                : await client.authRegister({ username: usernameEl.value, password: passwordEl.value });
            localStorage.setItem(TOKEN_KEY, resp.sessionToken);
            overlay.remove();
            resolve({ userId: resp.userId, username: resp.username });
        } catch (e: any) {
            errorEl.textContent = String(e?.message ?? e);
        }
    };
    overlay.querySelector("#auth-login")!.addEventListener("click", () => submit("login"));
    overlay.querySelector("#auth-register")!.addEventListener("click", () => submit("register"));
    usernameEl.focus();
}

export function clearStoredToken() { localStorage.removeItem(TOKEN_KEY); }
```

- [ ] **Step 3: Wire into `main.ts`**

```typescript
import { showLoginPanel } from "./login_panel";

async function main() {
    const client = new GameClient(/* ... */);
    await client.connect();  // WS open, no auth yet
    const { userId, username } = await showLoginPanel(client);
    console.log(`logged in as ${username} (${userId})`);
    // proceed with existing world setup
}
```

- [ ] **Step 4: Verify by running**

Run: `just dev`
Expected: browser opens to `http://localhost:8080`, sees login form, can register `alice`/`hunter22`, sees ship spawn.

- [ ] **Step 5: Commit**

```bash
git add examples/4node-basic/web/src/login_panel.ts examples/4node-basic/web/src/main.ts examples/4node-basic/web/sdk/
git commit -m "feat(4node-basic/web): login + register panel before world entry

Stores session token in localStorage; on next connect tries
ValidateToken first for transparent reconnect."
```

---

## Phase H — Tests + verification

### Task 29: Cluster integration tests

**Files:**
- Create: `pkg/universe/auth_e2e_test.go`

- [ ] **Step 1: End-to-end register → spawn**

```go
package universe_test

import (
    "context"
    "testing"

    "github.com/zenion/mmoserver/pkg/auth/authtest"
    "github.com/zenion/mmoserver/pkg/mmokit"
)

func TestAuthE2ERegisterAndSpawn(t *testing.T) {
    fixture := newClusterFixture(t).WithAuthMock(authtest.NewMock())
    defer fixture.Close()

    client := fixture.Dial(t)
    resp, err := client.AuthRegister(context.Background(), "alice", "hunter22")
    if err != nil { t.Fatal(err) }
    if resp.UserId == "" { t.Fatal("empty user_id") }

    // Wait for player spawn — assertion via fixture.OnSpawn channel
    spawn := fixture.WaitForSpawn(t, resp.UserId)
    if spawn.Username != "alice" { t.Fatalf("want alice, got %s", spawn.Username) }
}
```

- [ ] **Step 2: Reconnect with stored token**

```go
func TestAuthE2EReconnectWithToken(t *testing.T) {
    fixture := newClusterFixture(t).WithAuthMock(authtest.NewMock())
    defer fixture.Close()

    c1 := fixture.Dial(t)
    reg, _ := c1.AuthRegister(context.Background(), "bob", "hunter22")
    fixture.WaitForSpawn(t, reg.UserId)
    c1.Close()

    c2 := fixture.Dial(t)
    val, err := c2.AuthValidateToken(context.Background(), reg.SessionToken)
    if err != nil { t.Fatal(err) }
    if val.UserId != reg.UserId { t.Fatal("user_id mismatch on reconnect") }
    fixture.WaitForSpawn(t, reg.UserId)  // re-spawned in same world position
}
```

- [ ] **Step 3: Op gating**

```go
func TestAuthE2EOpGating(t *testing.T) {
    fixture := newClusterFixture(t).WithAuthMock(authtest.NewMock())
    defer fixture.Close()
    client := fixture.Dial(t)
    // Send a cell op (e.g. CE_MOVE) before auth — expect NOT_AUTHENTICATED.
    err := client.SendRawOp(/* a non-auth op */)
    if !isAuthError(err, AUTH_ERROR_NOT_AUTHENTICATED) {
        t.Fatalf("expected NOT_AUTHENTICATED, got %v", err)
    }
}
```

- [ ] **Step 4: Lockout flow**

```go
func TestAuthE2ELockout(t *testing.T) {
    fixture := newClusterFixture(t).WithAuthMock(authtest.NewMock())
    defer fixture.Close()
    client := fixture.Dial(t)
    _, _ = client.AuthRegister(context.Background(), "carol", "hunter22")
    client.AuthLogout(context.Background())

    // 5 wrong passwords → ACCOUNT_LOCKED on 6th
    for i := 0; i < 5; i++ {
        _, err := client.AuthLogin(context.Background(), "carol", "wrong")
        if !isAuthError(err, AUTH_ERROR_INVALID_CREDENTIALS) {
            t.Fatalf("attempt %d: want INVALID_CREDENTIALS, got %v", i, err)
        }
    }
    _, err := client.AuthLogin(context.Background(), "carol", "wrong")
    if !isAuthError(err, AUTH_ERROR_ACCOUNT_LOCKED) {
        t.Fatalf("want ACCOUNT_LOCKED, got %v", err)
    }
}
```

- [ ] **Step 5: Duplicate-session kick-old**

```go
func TestAuthE2EDuplicateSessionKicksOld(t *testing.T) {
    fixture := newClusterFixture(t).WithAuthMock(authtest.NewMock())
    defer fixture.Close()
    c1 := fixture.Dial(t)
    reg, _ := c1.AuthRegister(context.Background(), "dave", "hunter22")
    fixture.WaitForSpawn(t, reg.UserId)

    c2 := fixture.Dial(t)
    _, err := c2.AuthLogin(context.Background(), "dave", "hunter22")
    if err != nil { t.Fatal(err) }

    select {
    case ev := <-c1.Kicked():
        if ev.Reason != "replaced_by_new_session" { t.Fatalf("wrong kick reason: %s", ev.Reason) }
    case <-time.After(2 * time.Second):
        t.Fatal("c1 was not kicked within 2s")
    }
}
```

The cluster fixture's `WithAuthMock` injects an `authtest.RepoMock` into `mmokit.RegisterAuthServiceWithMock`. Add it to the existing `cluster_fixture_*_test.go` if not present.

- [ ] **Step 6: Run tests**

Run: `go test ./pkg/universe/ -run TestAuthE2E -v`
Expected: all PASS.

- [ ] **Step 7: Commit**

```bash
git add pkg/universe/auth_e2e_test.go pkg/universe/cluster_fixture_test.go
git commit -m "test(auth): cluster e2e — register, reconnect, gating, lockout, kick-old"
```

---

### Task 30: Smoke pass

- [ ] **Step 1: Reset dev DB**

Run: `just db-reset && just db-up`
Expected: clean Postgres.

- [ ] **Step 2: Build everything**

Run: `just build`
Expected: builds without errors.

- [ ] **Step 3: Run distributed setup**

Run: `cd examples/4node-basic && just distributed`
Expected: tmux session with coordinator + 2 hosts + gateway + 1 service-host (auth) + vite dev server.

- [ ] **Step 4: Manual checks in browser**

- Open `http://localhost:8080`
- Click "register", create user `alice`/`hunter22` → ship spawns
- Open second tab, log in as `alice`/`hunter22` → first tab is kicked
- Refresh second tab → ValidateToken auto-reconnects, no login form
- Run `auth.user.kick alice` from coord console → tab gets kicked, login form returns
- Try logging in as `alice` with wrong password 5 times → 6th attempt shows "account locked" with retry-after

- [ ] **Step 5: Kill the gateway, reconnect**

In tmux, find and kill the gateway process. The browser tab loses connection. Restart the gateway with the same command. Refresh the tab — `ValidateToken` should succeed and the ship reappears in the world.

This is the gateway-crash recovery story working end-to-end.

- [ ] **Step 6: Final commit (if any tweaks)**

If smoke pass surfaces minor wiring fixes, commit them with: `fix(auth): smoke pass adjustments`.

---

## Plan complete

Spec reference: [docs/superpowers/specs/2026-05-01-auth-service-design.md](../specs/2026-05-01-auth-service-design.md)

**Estimated total commits:** ~24 (one per task plus the smoke-pass tweak commit).
**Estimated total LOC:** ~3000 (Go) + ~150 (TS) + ~100 (proto/SQL).

Memory note: per `feedback_no_worktrees`, this plan is sequential work and runs in `main`, not a worktree. Per `project_opensource_ready` and `feedback_no_backward_compat`, deletions are absolute (no aliases, no compat shims).
