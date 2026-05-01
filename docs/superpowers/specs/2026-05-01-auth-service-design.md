# Auth Service — Design

**Status:** Draft for review
**Author:** Josh Stout (with Claude)
**Date:** 2026-05-01
**Related memories:** `feedback_no_backward_compat`, `feedback_refactor_over_stopgaps`, `feedback_mmokit_facade_only`, `feedback_enginepb_import`, `feedback_logging`, `feedback_proto_field_cleanup`, `project_opensource_ready`
**Related specs:** [2026-04-27-pluggable-services-design.md](2026-04-27-pluggable-services-design.md), [2026-04-18-role-separation-design.md](2026-04-18-role-separation-design.md)

## 1. Summary

mmokit today logs players in via a per-process `LoginHandler` that the coordinator runs inline against an incoming `CE_LOGIN` event. Username only — no password, no real identity, no session token, no audit. A gateway crash forces every client to full re-login.

This design replaces that with a first-class engine-tier **auth service** (`pkg/auth/`) running on the new pluggable services framework. The service owns identity (`auth_users`), credentials (`auth_passwords` for v1; `auth_identities` reserved for OIDC v2), opaque session tokens (`auth_sessions`), an audit trail (`auth_audit_log`), per-account lockout, and per-IP rate limiting. Auth is wire-protocol-agnostic — the gateway holds per-connection auth state and any future per-message authentication (UDP, DTLS, AEAD) lives entirely in the gateway with no auth-service involvement.

**Scope:** full identity service (verify, register, token issuance, password change, audit, lockout, MFA schema seam) using **opaque server-side tokens** with **30d sliding TTL**, **UUIDs everywhere** for primary keys, and a schema designed so OIDC providers slot in additively.

The bespoke `LoginHandler` machinery (`pkg/universe/login.go` in its entirety) is deleted. Login becomes a regular service op on the operations channel like every other service kind.

## 2. Goals & non-goals

### Goals

- **First-class auth in mmokit.** Every game built on mmokit gets production-grade auth with `mmokit.RegisterAuthService(coord, opts)` — one line.
- **Wire-protocol-agnostic identity.** Auth knows nothing about WebSockets, UDP, DTLS, AEAD, or sequence numbers. Per-message authentication is the gateway's job.
- **Gateway-crash transparent reconnect.** Sliding-TTL session tokens mean a gateway crash → client reconnects with same token → fully transparent.
- **OIDC-ready.** Identity / credentials split lets OIDC v2 land additively without schema migrations or wire breakage.
- **Pure delete of legacy.** `LoginHandler`, `HandleLogin`, `ValidateUsername`, `loginService` all disappear with no backward-compat aliases (per `feedback_no_backward_compat`).

### Non-goals (v1)

- OIDC / OAuth implementations (schema seam present; runtime work deferred)
- MFA implementations (schema seam present; v1 service ignores `mfa_code` field)
- Email verification, password reset via email
- Per-message UDP authentication / DTLS / AEAD (entirely the gateway's concern, future)
- Multi-device "active simultaneously" sessions (one active game session per user_id; new connect kicks the old)
- Cross-cluster session federation
- HaveIBeenPwned breach-list lookups
- Password complexity rules beyond min length (modern guidance: length > complexity)
- Pre-connect HTTPS auth endpoint (every auth op rides the existing operations channel; HTTP listener arrives with OIDC v2)
- Player-record migration from existing dev DB (wipe + recreate; solo dev, no real users)

## 3. Architecture

```text
┌──────────┐    op envelope     ┌──────────────┐  mesh  ┌──────────────┐
│  client  │ ─────────────────▶ │   gateway    │ ─────▶ │ auth service │
│  (browser/                    │ - authStates │        │ - handlers   │
│   future                      │ - auth-op    │        │ - argon2id   │
│   Unity)  │ ◀────────────────  │   gating     │ ◀────  │ - rate limit │
└──────────┘   responses + WS   │ - PlayerAssign        │ - audit log  │
                                │   dispatch   │        └──────┬───────┘
                                └──────┬───────┘               │
                                       │ PlayerAssignment      │ AuthRepository
                                       ▼                       ▼
                                  ┌────────┐              ┌─────────┐
                                  │  cell  │              │postgres │
                                  └────────┘              │ auth_*  │
                                                          └─────────┘
```

**Five new pieces:**

1. **`pkg/auth/`** — engine-tier service kind: descriptor, handlers, password hashing, token primitives, rate limiting, repository interface, Postgres implementation, console builtins.
2. **`proto/enginepb/auth.proto`** — five v1 op codes (1–5) + four reserved for OIDC v2 (6–10).
3. **Gateway extensions** in `pkg/universe/gateway.go`: per-connection auth-state map, auth-kind op gating before routing, response-interception hook (`pkg/auth.GatewayHook`) that updates state and dispatches `PlayerAssignment` after successful auth.
4. **`mmokit.RegisterAuthService(p, opts)`** — single-call wiring entrypoint that registers the kind, the gateway hook, and the console commands.
5. **`Config.ExtraMigrations fs.FS` hook** (open question §15.4 from the pluggable-services spec resolved) — `pkg/auth/postgres/migrations/*.sql` ships its own migrations, layered on top of engine migrations at startup.

**Module boundaries:**

- `pkg/auth/` may import: `gen/go/enginepb/`, `pkg/service/`, `pkg/persist/postgres/`, `pkg/logger/`, `pkg/metrics/`, `github.com/google/uuid`, `golang.org/x/crypto/argon2`.
- `pkg/auth/` must NOT import: game protos (`gamepb`, `basicpb`), `internal/`, `pkg/universe/` (avoids circular dependency — universe imports auth via the gateway-hook indirection).
- Game code reaches auth only through the `mmokit` facade (`feedback_mmokit_facade_only`).

## 4. Package layout

```text
pkg/auth/
  kind.go            // service.Kind descriptor + ServiceOpts
  service.go         // *Service: implements service.Service
  handlers.go        // op handlers: Login, Register, Validate, Logout, ChangePassword
  password.go        // argon2id hash/verify with versioned params
  token.go           // 32-byte random + SHA-256 hash + base64url
  ratelimit.go       // per-IP token bucket + per-account failure counter
  gateway_hook.go    // GatewayHook: response interception → connID auth state
  repo.go            // AuthRepository interface (typed, domain-specific)
  console.go         // console builtins (auth user/session list, lock, kick)
  doc.go             // package overview
  postgres/
    repo.go          // Postgres implementation of AuthRepository
    migrations/
      001_init.sql
  authtest/
    mock.go          // in-memory AuthRepository for tests (mirrors persisttest pattern)

proto/enginepb/
  auth.proto         // op codes + request/response messages
gen/go/enginepb/
  auth.pb.go         // regenerated by buf

pkg/mmokit/
  auth.go            // facade: RegisterAuthService + AuthOpts re-exports
```

The `auth` kind name is engine-reserved — registration validation rejects games attempting to register a service kind named `"auth"` for other purposes.

## 5. Wire protocol — `proto/enginepb/auth.proto`

Op codes 1–15 are reserved for engine-tier auth. v1 uses 1–5; OIDC v2 reserves 6–10.

```proto
syntax = "proto3";
package enginepb;
option go_package = "github.com/zenion/mmoserver/gen/go/enginepb";

enum AuthOpCode {
  AUTH_OPCODE_UNSPECIFIED         = 0;
  AUTH_OPCODE_LOGIN               = 1;
  AUTH_OPCODE_REGISTER            = 2;
  AUTH_OPCODE_VALIDATE_TOKEN      = 3;
  AUTH_OPCODE_LOGOUT              = 4;
  AUTH_OPCODE_CHANGE_PASSWORD     = 5;
  AUTH_OPCODE_OIDC_BEGIN          = 6;   // reserved, v2
  AUTH_OPCODE_OIDC_COMPLETE       = 7;   // reserved, v2
  AUTH_OPCODE_OIDC_LINK           = 8;   // reserved, v2
  AUTH_OPCODE_OIDC_UNLINK         = 9;   // reserved, v2
  AUTH_OPCODE_SET_PASSWORD        = 10;  // reserved, v2 (OIDC-only users adding password)
}

message AuthLoginRequest {
  string username = 1;
  string password = 2;
  string mfa_code = 3;  // ignored when mfa_enabled=false; v2 enforces
}
message AuthLoginResponse {
  string user_id        = 1;
  string username       = 2;
  string session_token  = 3;  // base64url, 32 bytes raw
  int64  expires_at_ms  = 4;
}

message AuthRegisterRequest {
  string username = 1;
  string password = 2;
  string email    = 3;  // optional
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
  int64  expires_at_ms = 3;  // post-slide
}

message AuthLogoutRequest  {}
message AuthLogoutResponse {}

message AuthChangePasswordRequest  { string current_password = 1; string new_password = 2; }
message AuthChangePasswordResponse {}
```

**Auto-login on register** — `AuthRegisterResponse` carries a session token so the client gets to playable state in one round-trip.

**Error model** — services-framework convention: op envelope carries a status code on failure. Auth-specific error codes:

```
INVALID_CREDENTIALS, USERNAME_TAKEN, USERNAME_INVALID, PASSWORD_TOO_WEAK,
ACCOUNT_LOCKED, RATE_LIMITED, MFA_REQUIRED, MFA_INVALID,
TOKEN_INVALID, TOKEN_EXPIRED, NOT_AUTHENTICATED, INTERNAL
```

`ACCOUNT_LOCKED` and `RATE_LIMITED` carry `retry_after_ms` in error metadata so clients can show a meaningful "try again in N seconds" message.

## 6. Postgres schema — `pkg/auth/postgres/migrations/001_init.sql`

Five tables. Identity is decoupled from credentials so OIDC links additively in v2.

### `auth_users` — canonical identity (no credential material)

```sql
CREATE TABLE auth_users (
  user_id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  username         TEXT NOT NULL UNIQUE,            -- always required (display name)
  email            TEXT,                            -- optional
  email_verified   BOOLEAN NOT NULL DEFAULT FALSE,
  mfa_secret       BYTEA,                           -- v1 always NULL
  mfa_enabled      BOOLEAN NOT NULL DEFAULT FALSE,  -- v1 always FALSE
  status           TEXT NOT NULL DEFAULT 'active',  -- active | locked | disabled
  failed_attempts  INT NOT NULL DEFAULT 0,          -- password+MFA failures only
  locked_until     TIMESTAMPTZ,
  last_login_at    TIMESTAMPTZ,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

Username is always lowercase (existing convention from `ValidateUsername`); enforced by service-side normalization, UNIQUE constraint catches collisions.

### `auth_passwords` — password credential, 0..1 per user

```sql
CREATE TABLE auth_passwords (
  user_id        UUID PRIMARY KEY REFERENCES auth_users(user_id) ON DELETE CASCADE,
  password_hash  TEXT NOT NULL,                      -- argon2id encoded ($argon2id$v=19$...)
  hash_algorithm TEXT NOT NULL DEFAULT 'argon2id',   -- migration column
  changed_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

v1 always creates one row per user on register. OIDC-only users (v2) have zero rows here.

### `auth_identities` — federated identities, 0..N per user (v1 unused)

```sql
CREATE TABLE auth_identities (
  provider      TEXT NOT NULL,                       -- 'google' | 'discord' | 'github' | ...
  subject       TEXT NOT NULL,                       -- stable provider-specific user ID
  user_id       UUID NOT NULL REFERENCES auth_users(user_id) ON DELETE CASCADE,
  email         TEXT,
  display_name  TEXT,
  linked_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (provider, subject)
);
CREATE INDEX auth_identities_user ON auth_identities(user_id);
```

Schema present from day one so v2 OIDC is purely additive (no migrations).

### `auth_sessions` — opaque session tokens

```sql
CREATE TABLE auth_sessions (
  token_hash       BYTEA PRIMARY KEY,                -- SHA-256(raw_token); raw never stored
  user_id          UUID NOT NULL REFERENCES auth_users(user_id) ON DELETE CASCADE,
  issued_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  expires_at       TIMESTAMPTZ NOT NULL,             -- slides on every Validate
  last_used_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  revoked_at       TIMESTAMPTZ,                      -- non-null = revoked
  client_meta      JSONB                             -- {ip, ua, gateway_id}
);
CREATE INDEX auth_sessions_user_active ON auth_sessions(user_id) WHERE revoked_at IS NULL;
CREATE INDEX auth_sessions_expiry      ON auth_sessions(expires_at) WHERE revoked_at IS NULL;
```

Token raw value is **only** ever in memory and on the wire (TLS-protected). DB only ever sees the SHA-256 hash, so a DB leak yields no usable tokens.

### `auth_audit_log` — security trail

```sql
CREATE TABLE auth_audit_log (
  audit_id            BIGSERIAL PRIMARY KEY,
  occurred_at         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  event               TEXT NOT NULL,
  user_id             UUID REFERENCES auth_users(user_id) ON DELETE SET NULL,
  username_attempted  TEXT,                          -- captures failed lookups
  ip_addr             INET,
  user_agent          TEXT,
  gateway_id          TEXT,
  metadata            JSONB
);
CREATE INDEX auth_audit_user_recent  ON auth_audit_log(user_id, occurred_at DESC);
CREATE INDEX auth_audit_event_recent ON auth_audit_log(event, occurred_at DESC);
```

Event types: `login_success`, `login_failure`, `register`, `logout`, `password_change`, `lockout_triggered`, `lockout_cleared_admin`, `token_revoked_admin`, `token_validate_failure`.

### Background reaper

A goroutine on every auth-service instance runs every `AuthOpts.ReapInterval` (default 1h):

- `DELETE FROM auth_sessions WHERE revoked_at IS NOT NULL OR expires_at < now() - INTERVAL '7 days'`
- `DELETE FROM auth_audit_log WHERE occurred_at < now() - AuthOpts.AuditRetention` (default 90d)

Multi-instance safe — `DELETE` with `WHERE` is idempotent across instances.

### `pgcrypto` extension

`gen_random_uuid()` requires `pgcrypto`. The migration prelude (`000_init.sql` in engine migrations, or 001's prelude) runs `CREATE EXTENSION IF NOT EXISTS pgcrypto` so auth and game tables can both use it.

## 7. End-to-end flows

### Connection state machine on the gateway

Each `connID` is in one of two states:

- **Unauthenticated** (default on new WS): only ops in the auth kind's op-code set are routed. Any other op gets `NOT_AUTHENTICATED` returned at the gateway before routing; no audit, no service-side processing (high-volume vector).
- **Authenticated**: gateway holds `{user_id, username, session_token, expires_at}`. Every op routes normally.

Transitions:

- Unauth → Auth: successful response from `AUTH_LOGIN` / `AUTH_REGISTER` / `AUTH_VALIDATE_TOKEN`.
- Auth → Unauth: WS close (cleared from gateway memory), or successful `AUTH_LOGOUT` (gateway closes WS).

### Flow A — First-time register + auto-login

```text
client                gateway                 auth-service              cell
  ─── WS open ──────▶ │
                     │  connID = unauthenticated
  ─── REGISTER ─────▶ │  ─── route to "auth" ──▶ │
                     │                           │  validate username/password,
                     │                           │  per-IP rate-limit check,
                     │                           │  argon2id hash password,
                     │                           │  INSERT auth_users + auth_passwords,
                     │                           │  generate token, INSERT auth_sessions,
                     │                           │  audit "register",
                     │  ◀── RegisterResponse ─── │
                     │  intercept (GatewayHook):
                     │    authStates[connID] = {userID, username, token, expiresAt}
  ◀── RegisterResp ── │
                     │  PlayerRouter(userID, username) → cellID
                     │  ─── PlayerAssignment ──────────────────────────▶ │
                     │                                                    OnPlayerJoin{userID, username}
                     │                                                    PlayerRepo.GetOrCreate(userID)
                     │                                                    SpawnEntity → CE_INITIAL_STATE
```

### Flow B — Returning user with credentials

Identical to Flow A but op is `AUTH_LOGIN`; password is verified against `auth_passwords`; `auth_users.last_login_at` updated; `auth_users.failed_attempts = 0`, `locked_until = NULL` on success.

### Flow C — Reconnect with stored token

```text
client                gateway                 auth-service              cell
  ─── WS open ──────▶ │
                     │  connID = unauthenticated
  ─── VALIDATE ─────▶ │  ─── route to "auth" ──▶ │
   {session_token}   │                           │  hash(token) → SELECT auth_sessions
                     │                           │  check: revoked_at IS NULL, expires_at > now
                     │                           │  UPDATE expires_at = now + ttl,
                     │                           │         last_used_at = now
                     │                           │  (no audit row on success — see §9)
                     │  ◀── ValidateResponse ─── │
                     │  intercept: authStates[connID] = ...
  ◀── ValidateResp ── │
                     │  PlayerRouter → PlayerAssignment → cell ...
```

**The cell sees Flow A, B, C identically** — `OnPlayerJoin` always receives `{userID, username}` and calls `PlayerRepo.GetOrCreate(userID)`. Reconnect semantics are entirely auth+gateway concern.

### Flow D — Logout

```text
client → AUTH_LOGOUT → gateway → auth:
  - look up session by token from authStates[connID].sessionToken
  - UPDATE auth_sessions SET revoked_at = now() WHERE token_hash = ...
  - audit "logout"
auth → response → gateway → forward to client → close WS
cell sees normal disconnect (PlayerLeft path)
```

### Flow E — Change password (authenticated)

```text
client → AUTH_CHANGE_PASSWORD {current, new} → gateway (auth check OK) → auth:
  - verify current_password against auth_passwords (argon2id verify)
  - if invalid: return INVALID_CREDENTIALS (no session impact)
  - argon2id hash new, UPDATE auth_passwords
  - UPDATE auth_sessions SET revoked_at = now()
       WHERE user_id = $1 AND token_hash != $2 AND revoked_at IS NULL
  - audit "password_change" with metadata.sessions_revoked
auth → ok → gateway → forward to client. WS stays open, current session untouched.
```

Auto-revoking other sessions is the security default. Other devices are forced to re-login with the new password.

### Flow F — Auth failures

| Trigger | Response | Side-effects |
|---|---|---|
| Wrong password | `INVALID_CREDENTIALS` | `failed_attempts++`, audit `login_failure {reason: "wrong_password"}`. If `failed_attempts >= LockoutThreshold`: `locked_until = now + LockoutDuration`, audit `lockout_triggered`. |
| Username doesn't exist | `INVALID_CREDENTIALS` (do NOT distinguish — username enumeration defense) | audit `login_failure {reason: "no_such_user"}`. IP rate limit still applies. |
| Account locked | `ACCOUNT_LOCKED` (with `retry_after_ms` in metadata) | audit `login_failure {reason: "account_locked"}`. Counter NOT incremented (don't extend lockout from continued attempts on a locked account). |
| Per-IP rate limit hit | `RATE_LIMITED` (with `retry_after_ms`) | audit `login_failure {reason: "ip_ratelimit"}`. Auth never sees the password. |
| Token expired | `TOKEN_EXPIRED` | audit `token_validate_failure {reason: "expired"}`. Client clears local token, full re-login required. |
| Token invalid | `TOKEN_INVALID` | audit `token_validate_failure {reason: "revoked" \| "unknown"}`. Same client behavior. |
| Op on unauth'd connID | `NOT_AUTHENTICATED` | gateway-level rejection before routing; no audit (high-volume vector). |

Successful login resets `failed_attempts = 0` and clears `locked_until` if past.

### Duplicate-session policy

Today's coordinator-side `activeUsers` map prevents simultaneous sessions per username. Behavior changes:

- Key: `username` → `userID`
- Behavior: "reject duplicate" → **"kick old, accept new"**
- Old connection gets `SE_KICKED` event with reason `replaced_by_new_session`, then close
- Better reconnect UX — stale connections from a prior session don't lock out an actively-trying-to-connect user

Multi-device "active simultaneously" is a v2 product call, not a v1 default.

## 8. Gateway integration

### Connection auth state

```go
// pkg/universe/gateway.go
type connAuthState struct {
    authenticated bool
    userID        uuid.UUID
    username      string
    sessionToken  string         // raw token; only in memory, never logged
    expiresAt     time.Time
    authedAt      time.Time
}

// Per-gateway map; cleared on WS close
authStates map[ConnID]connAuthState
```

Gateway crash drops all state. Clients reconnect with their stored token via `AUTH_VALIDATE_TOKEN`, regain authenticated state on a new gateway. This is the gateway-crash recovery story end-to-end.

### Op-receive flow (gating step)

```text
1. Decode op envelope → opCode
2. authState := authStates[connID]
3. opRouting.opToKind[opCode] → kind (existing services routing)
4. if !authState.authenticated:
       if kind != "auth":
           return NOT_AUTHENTICATED to client; no routing
5. Forward to chosen kind-instance via existing services-framework MeshData path
6. (Response arrives; see interception below)
```

Step 4 is the only new line vs. today's services-framework routing.

### Response interception via `pkg/auth.GatewayHook`

The gateway has **zero** knowledge of auth proto types. `pkg/auth` registers a `GatewayHook` at service-registration time:

```go
// pkg/auth/gateway_hook.go
type GatewayHook struct {
    OnSuccess func(connID ConnID, userID uuid.UUID, username, token string, expiresAt time.Time)
    OnLogout  func(connID ConnID)
}

func (h *GatewayHook) ProcessResponse(connID ConnID, opCode uint32, payload []byte) {
    switch enginepb.AuthOpCode(opCode) {
    case AUTH_OPCODE_LOGIN, AUTH_OPCODE_REGISTER, AUTH_OPCODE_VALIDATE_TOKEN:
        // unmarshal AuthLoginResponse / AuthRegisterResponse / AuthValidateTokenResponse,
        // extract {user_id, username, session_token, expires_at_ms}, call OnSuccess
    case AUTH_OPCODE_LOGOUT:
        h.OnLogout(connID)
    }
}
```

The gateway forwarder:

```go
if g.authHook != nil && route.Kind == "auth" {
    g.authHook.ProcessResponse(connID, opCode, payload)
}
g.sendToClient(connID, payload)
```

Adding ops to auth never requires gateway changes.

### Post-auth dispatch

`OnSuccess` callback runs after auth succeeds:

```go
authStates[connID] = connAuthState{authenticated: true, userID, username, ...}
cellID := playerRouter(userID, username)
gateway.dispatchPlayerAssignment(MeshFrame_PlayerAssignment{
    GatewayID:    g.id,
    ConnID:       connID,
    UserID:       userID,
    Username:     username,
    CellID:       cellID,
    SessionToken: sessionToken,
})
gateway.coord.AnnounceSession(...)  // existing async path
```

Same MeshData infrastructure as today; only the *origin* of the assignment is different.

### Multi-instance auth-service routing

Services-framework handles via `hash(connID) % len(authInstances)`. All instances share the same Postgres so any can validate any token. Hash-affinity gives free per-connection cache locality on auth-side prepared statements and IP rate-limit buckets.

### IP source for rate limiting

`OpContext` (services-framework) gains `ClientIP netip.Addr`, populated by the gateway from `ws.RemoteAddr` (or `X-Forwarded-For` when `AuthOpts.TrustedProxyHeader=true`). Tiny services-framework extension: one new field, populated for every routed op, available to every service handler.

### Admin revocation paths

Console commands (cmdsys-routed):

- `auth user list` — list all users (paginated)
- `auth user info <username>` — detail: status, last_login_at, active session count, failed_attempts
- `auth user lock <username> <duration>` — sets `status='locked'` (or `locked_until`), revokes all sessions, kicks any active WS
- `auth user unlock <username>` — clears locked_until + failed_attempts, audits `lockout_cleared_admin`
- `auth session list <username>` — list active sessions (token-prefix only, never full token)
- `auth session revoke <token-prefix>` — marks revoked_at, kicks holding WS
- `auth user kick <username>` — revokes all sessions, kicks any active WS

Kick path: cmdsys handler → updates auth DB → finds gateway holding session via coordinator's `sessionRoutes` → `CoordMessage.KickConnection` → gateway closes WS.

Mid-connection revocation isn't pushed to gateways via PeerList (low admin-action volume; explicit kick path is fine).

### Failure handling

- **Auth-service unreachable**: gateway returns `SERVICE_UNAVAILABLE` to client (existing services-framework error).
- **PlayerAssignment dispatch fails** (cell host down): gateway holds auth state, returns "world unavailable, retry shortly". Client retries with same token.
- **Network partition during auth**: client retries; idempotent at auth-service level (`failed_attempts` only increments on actual password mismatch; ValidateToken is pure-read except for the slide).

## 9. Security policy

### Password hashing — argon2id

Default parameters (OWASP 2024 minimum, ~50–100ms per hash):

```go
type ArgonParams struct {
    Memory      uint32  // 64 MiB (m=65536)
    Iterations  uint32  // 3 (t=3)
    Parallelism uint8   // 4 (p=4)
    SaltLen     uint32  // 16
    KeyLen      uint32  // 32
}
```

Stored encoded format `$argon2id$v=19$m=65536,t=3,p=4$<b64-salt>$<b64-hash>` — self-describing, so we can raise defaults later without breaking old hashes. On login, if a user's stored hash uses old params, transparently re-hash with current params after the verify succeeds.

`auth_passwords.hash_algorithm` is the migration column for any future change of algorithm family.

### Per-IP rate limiting (in-memory)

```go
type ipRateLimiter struct {
    mu      sync.Mutex
    buckets map[netip.Addr]*ipBucket
}
type ipBucket struct {
    failures      int
    windowStart   time.Time
    lockedUntil   time.Time
}
```

Default policy:

- ≤10 failed attempts per IP per 60s window
- Hitting limit locks IP for 5min — `AUTH_LOGIN`/`AUTH_REGISTER`/`AUTH_VALIDATE_TOKEN` from that IP all return `RATE_LIMITED`
- **Successful auth resets the IP bucket** — typo'd-twice-then-correct user not penalized
- Background sweep every 5min evicts buckets idle >1h to bound memory

In-memory only; no DB writes. Per-IP state doesn't survive a service restart (acceptable: any active attacker resumes on a fresh counter, but the per-account counter persists).

### Per-account lockout (DB-persistent)

`auth_users.failed_attempts` and `auth_users.locked_until`, updated transactionally on every `AUTH_LOGIN` attempt.

Default policy:

- 5 consecutive failed logins → `locked_until = now + 15min`
- During lockout: any password attempt for that user returns `ACCOUNT_LOCKED` with `retry_after_ms`; counter is NOT incremented (anti-extension)
- Successful login → `failed_attempts = 0`, `locked_until = NULL`
- Admin unlock: `auth user unlock <username>`

Persisted because account-level brute-force can come from rotating IPs.

### Password policy

- Minimum length: 8 (configurable via `AuthOpts.PasswordMinLen`)
- No max length explicitly (argon2id handles up to several KB; gateway WS frame size limits cap it)
- No complexity rules (modern guidance: length > complexity)
- No breach-list check (defer)

Returns `PASSWORD_TOO_WEAK` on register/change-password if shorter than min.

### Audit log granularity

| event | user_id | username_attempted | ip_addr | gateway_id | metadata |
|---|:---:|:---:|:---:|:---:|---|
| `login_success` | ✓ | ✓ | ✓ | ✓ | `{}` |
| `login_failure` | nullable¹ | ✓ | ✓ | ✓ | `{reason}` |
| `register` | ✓ | ✓ | ✓ | ✓ | `{}` |
| `logout` | ✓ | ✓ | ✓ | ✓ | `{}` |
| `password_change` | ✓ | ✓ | ✓ | ✓ | `{sessions_revoked: N}` |
| `lockout_triggered` | ✓ | ✓ | ✓ | ✓ | `{failed_attempts, locked_until}` |
| `lockout_cleared_admin` | ✓ | ✓ | (admin) | ✓ | `{cleared_by}` |
| `token_revoked_admin` | ✓ | ✓ | (admin) | ✓ | `{revoked_by, reason}` |
| `token_validate_failure` | nullable² | (empty) | ✓ | ✓ | `{reason}` |

¹ NULL when username doesn't exist. ² NULL because token doesn't resolve to a user.

**Successful token validates are NOT audited** — would 10x row count for no security gain. Only failures.

`token_validate_failure` with `reason=unknown` flooding from a single IP is a token-stealing canary.

### `AuthOpts`

```go
// pkg/auth/kind.go (mirrored as pkg/mmokit.AuthOpts)
type AuthOpts struct {
    SessionTTL          time.Duration   // default 30d
    
    PasswordMinLen      int             // default 8
    Argon2id            ArgonParams     // default OWASP-2024
    
    IPRateLimitMax      int             // default 10 failures
    IPRateLimitWindow   time.Duration   // default 60s
    IPLockoutDuration   time.Duration   // default 5min
    
    LockoutThreshold    int             // default 5
    LockoutDuration     time.Duration   // default 15min
    
    AuditRetention      time.Duration   // default 90d
    ReapInterval        time.Duration   // default 1h
    
    TrustedProxyHeader  bool            // default false; if true, read X-Forwarded-For
    
    OIDCProviders       map[string]OIDCProviderConfig  // empty in v1
}

func DefaultAuthOpts() AuthOpts { /* ... */ }
```

## 10. Per-message authentication & UDP future (informational)

Auth is **wire-protocol-agnostic by design**. The auth service exposes only identity verification and session token issuance. Per-message authentication for any wire protocol is the gateway's concern.

| Layer | WebSocket + TLS (today) | UDP (future) |
|---|---|---|
| Channel security | TLS + TCP. Connection IS the auth boundary. | Gone. Each datagram stands alone. |
| Identity binding | `connID → {user_id, session_token}` in gateway memory | `{client_ip:port, session_id} → {user_id, session_key}` |
| Per-message auth | Implicit (TLS+TCP guarantees it) | **Explicit.** AEAD encryption + monotonic 32-bit sequence + sliding 1024-entry anti-replay window (à la IPSec/Netcode.io) |

**Critical architectural call:** session keys never live in the auth service. Auth knows about *identity proof* (passwords, tokens). Per-channel crypto state lives entirely on the gateway. Splitting it this way means:

1. Auth's DB stays free of secret material requiring rotation/zeroization
2. UDP work lands entirely on the gateway later — auth doesn't change
3. Session keys naturally rotate per-connection — every reconnect gets a fresh key
4. No new auth-service coupling when introducing DTLS/QUIC

**Future UDP flow:**

```
1. Client → auth (over TLS-WS):  AUTH_LOGIN → {session_token, gateway_udp_addr}
2. Client → gateway (UDP):       ChallengeRequest{session_token}
   gateway → auth:               AUTH_VALIDATE_TOKEN → {user_id}
   gateway: generates random session_key, stores {ip:port → user_id, key, seq_window}
   gateway → client (over TLS-WS, still open as control channel):  session_key
3. Every UDP packet:             {u32 seq, AEAD(payload, session_key, nonce=seq||sender)}
   gateway: verify MAC, check seq ∈ window, slide, decrypt, dispatch
```

The TLS-WS connection stays open as the control channel for the lifetime of the session (token refresh, key rotation, low-rate ops). UDP is purely the data plane (input, position, abilities at 60Hz).

This design does NOT implement any of this. It documents the seam so a future UDP implementation doesn't require re-touching auth.

## 11. What dies, what changes

### Deleted (no aliases per `feedback_no_backward_compat`)

**`pkg/universe/login.go`** — entire file:
- `ErrLoginPending`, `LoginHandler` type, `HandleLogin`, `ValidateUsername`
- `loginService`, `pendingConn`, `processLogins`, `loginResult`

**`pkg/universe/coordinator.go`:**
- `Config.LoginHandler` field
- The login-drain phase in coordinator's tick loop

**`pkg/mmokit/`:**
- Re-exports of `HandleLogin`, `ValidateUsername`, `LoginHandler`, `ErrLoginPending`

**`examples/4node-basic/`:**
- `proto/basicpb/basic.proto`: delete `LoginMsg`, delete `BCE_LOGIN` enum value
- `main.go`: delete `cfg.LoginHandler = mmokit.HandleLogin(...)` setup
- Replace inline-login UI with login/register form (`web/src/login_panel.ts`)

### Signature changes

**`PlayerSession`:**

```go
// Before
type PlayerSession struct {
    ConnID   ConnID
    Username string
    Data     any         // ← deleted
    SpawnLocation Location
    ...
}

// After
type PlayerSession struct {
    ConnID    ConnID
    UserID    uuid.UUID   // ← new
    Username  string
    SpawnLocation Location
    ...
}
```

`Data any` carried game-specific session data extracted by `LoginHandler`. Game-side state now flows through `PlayerRepository` lookup keyed by `user_id` at spawn time, or via auth metadata if it's identity-related.

**`PlayerRouter`:**

```go
// Before: type PlayerRouter func(username string) string
// After:  type PlayerRouter func(userID uuid.UUID, username string) string
```

**`PlayerAssignment` mesh frame** (`proto/meshpb/mesh.proto`):

- Adds `string user_id` and `string session_token` fields
- Renumber from 1 (per `feedback_proto_field_cleanup`); no `reserved`

**`Coordinator.ActiveUsers()`** and friends:

- Key: `username` → `userID`
- Returns `map[uuid.UUID]CellID`
- Existing duplicate-detection callers update to user-ID keying

**`OpContext`** (services-framework):

- Adds `ClientIP netip.Addr` populated by gateway for every routed op

### Game-side (`internal/game/`) ripple

Implementation scope, not design scope, but flagging:

- `GameWorld.PlayerEntities`: `ConnID → entity` (unchanged)
- `GameWorld.ConnToUsername` → `ConnToUserID map[ConnID]uuid.UUID`
- `GameWorld.PlayerDB`: `PlayerRepository` keyed by `user_id`
- `internal/game/entity_player.go`: spawn functions take `user_id`
- `players` table: PK changes from `username` → `user_id`, FK to `auth_users(user_id)`. Username denormalized as a column for display read perf.

### Existing-data migration

**Wipe + recreate** (solo dev, no real users, per `project_opensource_ready`):

1. `just db-reset` — resets docker-compose Postgres volume
2. Bring up cluster — auth + game migrations apply automatically
3. Register fresh accounts from web client

No data-migration script written. If/when real users exist, that's a separate one-shot SQL transformation.

### `Config.ExtraMigrations fs.FS`

The `Config.ExtraMigrations` hook (open question §15.4 from the pluggable-services-framework spec) lands as part of this work because `pkg/auth/` needs it. Implementation: extend `golang-migrate` driver chain to layer auth migrations after engine migrations. ~20 LOC.

This is the only reason the hook lands now vs. with the eventual echo-demo migrations.

## 12. `mmokit.RegisterAuthService` — game wiring

```go
// pkg/mmokit/auth.go
type AuthOpts = auth.ServiceOpts
type AuthRepository = auth.Repository

// RegisterAuthService wires the auth service kind, gateway hook, and
// console commands into the coordinator. Idempotent. Game must include
// "auth" in --services= when running with --mode=...,service.
func RegisterAuthService(p *Process, opts AuthOpts) error
```

Game wiring becomes one line in `main.go`:

```go
mmokit.RegisterAuthService(coord, mmokit.DefaultAuthOpts())
// or with overrides:
mmokit.RegisterAuthService(coord, mmokit.AuthOpts{
    SessionTTL:        14 * 24 * time.Hour,
    PasswordMinLen:    12,
    LockoutThreshold:  3,
})

coord.SetPlayerRouter(func(userID uuid.UUID, username string) string {
    return coord.CellAtPosition(spawnX, spawnY)
})

coord.OnPlayerJoin(func(s *mmokit.PlayerSession, stage *mmokit.Stage) {
    stage.SpawnPlayer(s, ...)  // s.UserID + s.Username available
})
```

The auth-service is included by adding `auth` to `--services=`:

```bash
./bin/4node-basic --mode=coordinator,host,gateway,service --services=auth
# or in distributed mode:
./bin/4node-basic --mode=service --services=auth --coordinator-addr=...
```

In single-process dev (`--mode=all`), `--services=auth` makes the auth kind run colocated.

## 13. Console commands

Registered on `RoleCoordinator`. All cmdsys-typed; JSON-Schema visible at `GET /commands/auth.*`.

| Command | RouteKind | Purpose |
|---|---|---|
| `auth user list` | RouteAllHosts | Cluster-wide user roster (paginated) |
| `auth user info <username>` | RouteAllHosts | Detail: status, last_login, active sessions, failed_attempts |
| `auth user lock <username> <dur>` | RouteAllHosts | Sets locked_until + revokes sessions + kicks WS |
| `auth user unlock <username>` | RouteAllHosts | Clears locked_until + failed_attempts |
| `auth user kick <username>` | RouteAllHosts | Revokes all sessions + kicks any active WS |
| `auth session list <username>` | RouteAllHosts | Active sessions (token-prefix only) |
| `auth session revoke <prefix>` | RouteAllHosts | Mark revoked + kick WS |
| `auth audit recent <username>` | RouteAllHosts | Last N audit events (default 50) |

Tokens are NEVER displayed in full — `auth session list` shows the first 8 chars of the token-hash hex only.

## 14. Testing strategy

| Layer | What | Where |
|---|---|---|
| Unit | argon2id hash/verify roundtrip; encoded-format parse; param-bump triggers re-hash | `pkg/auth/password_test.go` |
| Unit | Token generate (entropy), hash (SHA-256 stability), base64url encode/decode | `pkg/auth/token_test.go` |
| Unit | Per-IP rate-limit bucket: refill, lockout-on-threshold, eviction, reset-on-success | `pkg/auth/ratelimit_test.go` |
| Unit | Op handlers against in-memory `AuthRepository` mock (`authtest.RepoMock`) | `pkg/auth/service_test.go` |
| Postgres integration | Migrations apply cleanly; all five tables round-trip | `pkg/auth/postgres/postgres_test.go` (build tag `pgtest`) |
| Postgres integration | Lockout state persists across simulated service restart | Same harness |
| Postgres integration | Audit reaper deletes only past-retention rows | Same harness |
| Cluster integration | Register → spawn into cell → assert player entity | `pkg/universe/auth_e2e_test.go` |
| Cluster integration | Reconnect-with-token: register, capture token, close WS, reopen, validate, assert spawn | Same |
| Cluster integration | **Gateway crash recovery**: register, capture token, kill gateway, reconnect via different gateway, validate, assert spawn | Reuses `s7_graceful_shutdown_test.go` patterns |
| Cluster integration | Multi-instance auth: hash-affinity holds across N=2; one dies → retry on survivor | Reuses services-framework `WithServiceHost("auth", n)` |
| Cluster integration | Duplicate-session: same user_id connects twice → old gets `SE_KICKED`, new takes over | New |
| Cluster integration | Op gating: unauth'd connID → only auth ops accepted; cell ops return `NOT_AUTHENTICATED` | New |
| Cluster integration | Bad-credentials → 5 wrong → `ACCOUNT_LOCKED` → expire → correct works | New |
| Smoke (manual) | `just distributed`, register from web client, kill gateway process, reconnect → seamless | Existing pattern |

**Test fixture seam:** `mmokit.RegisterAuthServiceWithMock(coord)` for tests that don't want a real auth service. Uses `authtest.RepoMock` (in-memory `AuthRepository`). Replaces today's `stubLoginHandler` pattern across `pkg/universe/*_test.go`.

The cluster fixture's `WithServiceHost("auth", n)` stands up real auth instances against the test Postgres for end-to-end coverage.

## 15. Open questions

### 15.1 Username normalization on the wire

Today's `ValidateUsername` lowercases + trims at the gateway. With the auth service: do we lowercase on the client side (TS SDK helper), the gateway (early reject), or the auth service (canonical normalization)?

**Recommendation:** auth service is canonical. Gateway forwards the raw username; auth normalizes. Client SDK lowercases as a UX nicety (so the username field in the form auto-lowercases as the user types) but doesn't gate-keep. Single source of truth = auth.

### 15.2 Username uniqueness collision UX

If user "alice" exists and someone registers "ALICE", the lowercased UNIQUE constraint rejects with `USERNAME_TAKEN`. That's the right outcome. Does the error metadata include the canonical (lowercased) form so the client can show a useful message?

**Recommendation:** yes — `USERNAME_TAKEN` carries `metadata.canonical` with the lowercased form so the client can say "the username 'alice' is already taken" rather than the user-typed casing.

### 15.3 Token entropy parameter

Default 32 bytes (256 bits) of CSPRNG output → base64url encoded → 43 chars. Configurable via `AuthOpts.TokenBytes`?

**Recommendation:** not configurable. 256 bits is the right answer; offering a knob invites someone to drop it.

### 15.4 "Username changes" — schema seam

User said earlier name changes will be a thing. v1 doesn't ship the rename op, but the schema should support it. With `user_id` as PK and `username` as a UNIQUE column, rename is a single `UPDATE auth_users SET username = $1 WHERE user_id = $2` (with collision check). No schema change needed.

The game-side ripple is bigger: anywhere that caches or displays a username needs to refresh. That's a separate v2 design — out of scope here. The auth schema is ready.

### 15.5 Audit retention vs. compliance

Default 90 days. If anyone ever wants longer (legal hold, GDPR access logs, etc.), `AuthOpts.AuditRetention` is the knob. Future need: a separate "archive" path that exports rows beyond retention to cold storage instead of deleting.

**Recommendation:** out of scope for v1; document the knob, defer the archive.

## 16. Migration & rollout

- `Config.ExtraMigrations` lands first (mechanical hook in `pkg/persist/postgres/`)
- `pkg/auth/` lands as a new package; `proto/enginepb/auth.proto` + regenerated bindings
- Coordinator + gateway changes (auth-state map, op gating, GatewayHook indirection, OpContext.ClientIP) land together
- `pkg/universe/login.go` deletion + `Config.LoginHandler` removal + signature changes (PlayerSession.UserID, PlayerRouter, PlayerAssignment) land together — single PR-style commit since they're entangled
- `examples/4node-basic/` adapter: `mmokit.RegisterAuthService` wiring, web login/register UI, deletion of `LoginMsg` from basicpb
- Smoke pass: `just distributed`, register via web client, gateway-crash test

Estimated scope: ~2500–3500 LOC across `pkg/auth/` (new), `pkg/universe/` (modified), `proto/enginepb/` (new file), `proto/meshpb/` (renumber), `examples/4node-basic/web/` (login UI), tests.

## 17. Future work (deferred)

- **OIDC v2** — per §5 op codes 6–10 reserved; `auth_identities` schema present; HTTP listener on auth service for callback URLs; `OIDCProviders` config
- **MFA implementation** — `mfa_secret`/`mfa_enabled` columns present; `AuthLoginRequest.mfa_code` field present; v2 enforces TOTP/WebAuthn
- **Email verification + password reset via email** — requires SMTP integration; `email_verified` column present
- **Per-message UDP authentication** (gateway concern only) — AEAD + sequence + replay-window per §10
- **Username change op** — `AUTH_OPCODE_CHANGE_USERNAME` (op code 11+); v2 design covers display refresh ripple
- **Account merge** — link two `auth_users` rows (e.g., user registered with password, later wants to link Steam OIDC)
- **HaveIBeenPwned breach-list check** at register/change-password
- **Session telemetry** — periodic re-validate from gateway → auth to enforce mid-connection revocation without explicit kick
- **Hot-revocation push** via PeerList for high-volume admin-action environments
