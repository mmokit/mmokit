# Chat Service Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the engine-tier chat service (`pkg/services/chat/`) per [docs/superpowers/specs/2026-05-07-chat-service-design.md](../specs/2026-05-07-chat-service-design.md): single-instance pubsub-style chat with Postgres-persistent channels/memberships/mutes, ephemeral messages, capability-gated moderation, and one-line game wiring.

**Architecture:** Mirrors `pkg/auth/` post-typed-op-migration. All wire types live in `pkg/services/chat/typed_messages.go` as Go structs (no protobuf); ops register via `mmokit.RegisterOp[Req, Res]`; server events register via `mmokit.RegisterServerEvent[T]`. Persistence is a `Repository` interface with Postgres + in-memory mock impls. Service bootstraps system channels declaratively from `ChatOpts.DefaultChannels`. Gateway calls service-internal typed ops on chat (`ChatSessionEnter` / `ChatSessionLeave`) after auth login/logout to drive presence + subscription bookkeeping.

**Tech Stack:** Go 1.21+ (generics), pgx/v5 + golang-migrate, google/uuid (UUID v7 for msg_id), reflection codec via `mmokit.RegisterOp` / `RegisterServerEvent`, cmdsys for console commands, argon2id (re-using auth's helper) for custom-channel passwords.

**Plans this builds on (commits on main):**
- `054966b` — Plan 2 (operations channel) merge
- `dfac701` — proto retirement (auth + marketplace + ops)
- `2fa8c3f` — auth migrated to typed-op channel
- `8ea020e` — echo demo migrated to typed RegisterOp
- `9b32bc3` — old in-engine chat plumbing decommissioned

**Out of scope** (per spec §2 non-goals):
- Message persistence, paged history, recent-history-on-join
- Block lists, typing indicators, reactions, attachments, threading, message edit
- Cross-instance horizontal scale-out (single-instance v1)
- Rich text / mentions parsing, URL filtering, profanity filters
- Voice / video, cross-cluster federation
- Offline DM persistence

---

## File Structure

**Phase 1 — Auth-side prerequisites:**

- Move: `pkg/auth/` → `pkg/services/auth/` (entire tree). Update import paths repo-wide.
- Create: `pkg/services/auth/postgres/migrations/002_capabilities.up.sql` — `auth_capabilities` table.
- Create: `pkg/services/auth/postgres/migrations/002_capabilities.down.sql`.
- Modify: `pkg/services/auth/repo.go` — add `HasCapability`, `GrantCapability`, `RevokeCapability`, `ListCapabilities`, `Capability` struct.
- Modify: `pkg/services/auth/postgres/repo.go` — implement the four new methods.
- Modify: `pkg/services/auth/authtest/mock.go` — implement the four new methods.
- Create: `pkg/services/auth/capability_cache.go` — 30s TTL cache wrapper around `HasCapability`.
- Modify: `pkg/services/auth/console.go` — add `auth.user.grant` / `auth.user.revoke` / `auth.user.capabilities` / `auth.bootstrap-admin` commands.
- Modify: `pkg/mmokit/auth.go` — re-exports unchanged in shape, but import path adjusts.

**Phase 2-12 — Chat package:**

- Create: `pkg/services/chat/doc.go` — package overview comment.
- Create: `pkg/services/chat/typed_messages.go` — all wire structs (requests, responses, server events, ChatError, ChannelKind, errorBlock, ChannelInfo, MemberInfo).
- Create: `pkg/services/chat/kind.go` — `ServiceOpts`, `DefaultServiceOpts`, `Kind`, `KindName`, `MigrationsFS`, `DefaultChannelDef`.
- Create: `pkg/services/chat/repo.go` — `Repository` interface, `Channel`, `ChannelMember`, `Mute` row types, errors.
- Create: `pkg/services/chat/postgres/migrations/001_init.up.sql` — `chat_channels`, `chat_channel_members`, `chat_mutes`.
- Create: `pkg/services/chat/postgres/migrations/001_init.down.sql`.
- Create: `pkg/services/chat/postgres/repo.go` — Postgres `Repository` implementation.
- Create: `pkg/services/chat/postgres/repo_test.go` — pgtest integration.
- Create: `pkg/services/chat/chattest/mock.go` — in-memory Repository mock.
- Create: `pkg/services/chat/service.go` — `*Service` implementing `service.Service`.
- Create: `pkg/services/chat/membership.go` — in-memory membership index, presence map, slow-mode tracker.
- Create: `pkg/services/chat/fanout.go` — channel-id → []connID dispatch primitive.
- Create: `pkg/services/chat/handlers.go` — typed-op handler functions.
- Create: `pkg/services/chat/handlers_test.go` — handler tests.
- Create: `pkg/services/chat/events.go` — `mmokit.RegisterServerEvent[T]` for the 14 chat events.
- Create: `pkg/services/chat/authorization.go` — `canModerate` helper + capability check via `auth.Resolver`.
- Create: `pkg/services/chat/ratelimit.go` — token bucket + slow-mode tracker.
- Create: `pkg/services/chat/ratelimit_test.go` — bucket + slow-mode unit tests.
- Create: `pkg/services/chat/mute.go` — mute set + reaper goroutine.
- Create: `pkg/services/chat/msgindex.go` — msg_id → channel_id 5-min TTL index.
- Create: `pkg/services/chat/msgindex_test.go` — TTL eviction tests.
- Create: `pkg/services/chat/password.go` — argon2id wrapper using lighter params for channel passwords.
- Create: `pkg/services/chat/console.go` — cmdsys command registrations.
- Create: `pkg/mmokit/chat.go` — facade: `RegisterChatService`, `ChatOpts`, `DefaultChatOpts`, `ChatRepository`, `RegisterChatServiceWithMock`, `ChatClient`.
- Create: `pkg/services/chat/session_signals.go` — `ChatSessionEnter` / `ChatSessionLeave` typed-op definitions, gateway-side helper.
- Modify: `pkg/universe/gateway.go` — invoke `ChatSessionEnter` after auth login success and `ChatSessionLeave` on WS close (when chat is registered).
- Modify: `examples/4node-basic/main.go` — pass `DefaultChannels` to `RegisterChatService`.
- Modify: `examples/4node-basic/justfile` — add `--services=chat` to relevant recipes.
- Create: `examples/4node-basic/web/src/chat_panel.ts` — chat input/output UI.
- Create: `pkg/universe/chat_e2e_test.go` — end-to-end cluster integration tests.

---

## Phase 0 — Setup

### Task 0.1: Create branch from main

**Files:** none (git only)

- [ ] **Step 1: Verify clean tree on main**

```bash
git checkout main && git status
```

Expected: `On branch main`, `nothing to commit, working tree clean`. If you're on a different branch, stash or commit first.

- [ ] **Step 2: Create branch**

```bash
git checkout -b feat/mmokit-chat-service
```

- [ ] **Step 3: Verify build is clean**

```bash
just build && go vet ./...
```

Expected: build succeeds, no vet errors. This is the baseline — every subsequent task should keep this green.

---

## Phase 1 — Auth-side prerequisites

The chat work depends on `pkg/auth/` having been moved to `pkg/services/auth/` and on auth gaining a capabilities table + `HasCapability` repo method. Land these first as a coherent prereq, then chat code can import the new path from the start.

### Task 1.1: Move pkg/auth/ to pkg/services/auth/

**Files:**
- Move: `pkg/auth/**` → `pkg/services/auth/**` (entire subtree)

- [ ] **Step 1: Create the destination parent**

```bash
mkdir -p pkg/services
```

- [ ] **Step 2: Move the tree with git so history is preserved**

```bash
git mv pkg/auth pkg/services/auth
```

- [ ] **Step 3: Update import paths repo-wide**

```bash
grep -rl '"github.com/zenion/mmokit/pkg/auth' --include='*.go' | \
  xargs sed -i 's|"github.com/zenion/mmokit/pkg/auth|"github.com/zenion/mmokit/pkg/services/auth|g'
```

This rewrites every `import` line plus any string occurrences of the path. Verify a sample:

```bash
grep -rn 'pkg/services/auth' pkg/mmokit/auth.go | head -5
```

Expected: every line that referenced `pkg/auth` now references `pkg/services/auth`.

- [ ] **Step 4: Verify build**

```bash
just build && go vet ./...
```

Expected: clean build. If a missed import path appears, fix and re-run.

- [ ] **Step 5: Run unit tests**

```bash
go test ./pkg/services/auth/... ./pkg/mmokit/...
```

Expected: PASS (excluding `pgtest` build tag which needs Postgres — covered separately).

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "$(cat <<'EOF'
chore(auth): move pkg/auth/ to pkg/services/auth/

Mechanical rename to establish the pkg/services/<name>/ convention for
engine-tier service implementations. No semantic change.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 1.2: Add Capability struct + repo method signatures

**Files:**
- Modify: `pkg/services/auth/repo.go`

- [ ] **Step 1: Write a failing test for the new error**

Append to `pkg/services/auth/repo_errors_test.go` (create the file):

```go
package auth_test

import (
	"errors"
	"testing"

	"github.com/zenion/mmokit/pkg/services/auth"
)

func TestErrCapabilityNotFound_Defined(t *testing.T) {
	if auth.ErrCapabilityNotFound == nil {
		t.Fatal("auth.ErrCapabilityNotFound must be a non-nil sentinel")
	}
	if !errors.Is(auth.ErrCapabilityNotFound, auth.ErrCapabilityNotFound) {
		t.Fatal("sentinel must satisfy errors.Is identity")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./pkg/services/auth/... -run TestErrCapabilityNotFound_Defined -count=1
```

Expected: FAIL — `undefined: auth.ErrCapabilityNotFound`.

- [ ] **Step 3: Add the Capability struct, error, and repo method signatures**

Edit `pkg/services/auth/repo.go`. Append `ErrCapabilityNotFound` to the existing error block and add the Capability struct + four method signatures to the `Repository` interface:

```go
// Errors returned by Repository implementations.
var (
	ErrUserNotFound        = errors.New("auth: user not found")
	ErrUsernameTaken       = errors.New("auth: username taken")
	ErrSessionNotFound     = errors.New("auth: session not found")
	ErrCapabilityNotFound  = errors.New("auth: capability grant not found")
)

// Capability is a single granted capability row. Mirrors auth_capabilities.
type Capability struct {
	UserID     uuid.UUID
	Capability string
	GrantedAt  time.Time
	GrantedBy  uuid.UUID
	ExpiresAt  time.Time // zero value = no expiry
}
```

Then in the `Repository` interface block, add:

```go
	// Capabilities
	HasCapability(ctx context.Context, userID uuid.UUID, capability string) (bool, error)
	GrantCapability(ctx context.Context, c Capability) error
	RevokeCapability(ctx context.Context, userID uuid.UUID, capability string) error
	ListCapabilities(ctx context.Context, userID uuid.UUID) ([]Capability, error)
```

- [ ] **Step 4: Build to verify the interface compiles**

```bash
go vet ./pkg/services/auth/...
```

Expected: this WILL fail in `pkg/services/auth/authtest/mock.go` (RepoMock doesn't implement the new methods) and `pkg/services/auth/postgres/repo.go` (same). That's expected — next tasks fix those.

- [ ] **Step 5: Run the sentinel test**

```bash
go test ./pkg/services/auth/... -run TestErrCapabilityNotFound_Defined -count=1
```

Expected: PASS (the sentinel is now defined; the implementations don't compile yet but that test doesn't depend on them).

- [ ] **Step 6: Don't commit yet**

We'll commit at the end of Task 1.4 once impls and mock build cleanly.


### Task 1.3: Add auth_capabilities migration

**Files:**
- Create: `pkg/services/auth/postgres/migrations/002_capabilities.up.sql`
- Create: `pkg/services/auth/postgres/migrations/002_capabilities.down.sql`

- [ ] **Step 1: Write the up-migration**

`pkg/services/auth/postgres/migrations/002_capabilities.up.sql`:

```sql
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
```

- [ ] **Step 2: Write the down-migration**

`pkg/services/auth/postgres/migrations/002_capabilities.down.sql`:

```sql
DROP TABLE IF EXISTS auth_capabilities;
```

- [ ] **Step 3: Verify the migration is embedded by `MigrationsFS`**

```bash
go run -mod=mod ./pkg/services/auth/postgres/... 2>&1 | head -5 || true
ls pkg/services/auth/postgres/migrations/
```

Expected: both `002_capabilities.up.sql` and `002_capabilities.down.sql` listed alongside the `001_init.*.sql` files.

### Task 1.4: Implement capability methods on Postgres repo + mock

**Files:**
- Modify: `pkg/services/auth/postgres/repo.go`
- Modify: `pkg/services/auth/authtest/mock.go`

- [ ] **Step 1: Write a failing pgtest for HasCapability + GrantCapability**

Append to `pkg/services/auth/postgres/repo_test.go`:

```go
//go:build pgtest

func TestRepoCapabilities_GrantThenHas(t *testing.T) {
	repo, cleanup := newTestRepo(t)
	defer cleanup()
	ctx := context.Background()
	user, err := repo.CreateUser(ctx, auth.User{Username: "alice"}, "$argon2id$dummy")
	if err != nil { t.Fatal(err) }

	if has, _ := repo.HasCapability(ctx, user.UserID, "chat.admin"); has {
		t.Fatal("HasCapability=true before grant")
	}
	if err := repo.GrantCapability(ctx, auth.Capability{
		UserID: user.UserID, Capability: "chat.admin", GrantedBy: user.UserID,
	}); err != nil {
		t.Fatal(err)
	}
	has, err := repo.HasCapability(ctx, user.UserID, "chat.admin")
	if err != nil { t.Fatal(err) }
	if !has { t.Fatal("HasCapability=false after grant") }
}

func TestRepoCapabilities_RevokeAndList(t *testing.T) {
	repo, cleanup := newTestRepo(t)
	defer cleanup()
	ctx := context.Background()
	u, _ := repo.CreateUser(ctx, auth.User{Username: "bob"}, "$argon2id$dummy")
	for _, c := range []string{"chat.admin", "auth.admin"} {
		if err := repo.GrantCapability(ctx, auth.Capability{UserID: u.UserID, Capability: c, GrantedBy: u.UserID}); err != nil {
			t.Fatal(err)
		}
	}
	caps, _ := repo.ListCapabilities(ctx, u.UserID)
	if len(caps) != 2 { t.Fatalf("got %d caps, want 2", len(caps)) }
	if err := repo.RevokeCapability(ctx, u.UserID, "chat.admin"); err != nil { t.Fatal(err) }
	caps, _ = repo.ListCapabilities(ctx, u.UserID)
	if len(caps) != 1 { t.Fatalf("got %d caps after revoke, want 1", len(caps)) }
	if has, _ := repo.HasCapability(ctx, u.UserID, "chat.admin"); has { t.Fatal("HasCapability=true after revoke") }
}

func TestRepoCapabilities_ExpiredGrantNotPresent(t *testing.T) {
	repo, cleanup := newTestRepo(t)
	defer cleanup()
	ctx := context.Background()
	u, _ := repo.CreateUser(ctx, auth.User{Username: "carol"}, "$argon2id$dummy")
	if err := repo.GrantCapability(ctx, auth.Capability{
		UserID: u.UserID, Capability: "chat.admin", GrantedBy: u.UserID,
		ExpiresAt: time.Now().Add(-time.Hour), // already expired
	}); err != nil { t.Fatal(err) }
	if has, _ := repo.HasCapability(ctx, u.UserID, "chat.admin"); has {
		t.Fatal("HasCapability=true for expired grant")
	}
}
```

- [ ] **Step 2: Run pgtest to verify failure**

```bash
just test-pg -run TestRepoCapabilities -count=1
```

Expected: FAIL — methods don't exist yet on `*pgRepo`.

- [ ] **Step 3: Implement the four methods on the Postgres repo**

Append to `pkg/services/auth/postgres/repo.go`:

```go
func (r *pgRepo) HasCapability(ctx context.Context, userID uuid.UUID, capability string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(
		  SELECT 1 FROM auth_capabilities
		   WHERE user_id = $1 AND capability = $2
		     AND (expires_at IS NULL OR expires_at > NOW())
		)`, userID, capability).Scan(&exists)
	return exists, err
}

func (r *pgRepo) GrantCapability(ctx context.Context, c auth.Capability) error {
	var expires any
	if !c.ExpiresAt.IsZero() {
		expires = c.ExpiresAt
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO auth_capabilities (user_id, capability, granted_by, expires_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (user_id, capability) DO UPDATE
		  SET granted_at = NOW(), granted_by = EXCLUDED.granted_by, expires_at = EXCLUDED.expires_at`,
		c.UserID, c.Capability, c.GrantedBy, expires)
	return err
}

func (r *pgRepo) RevokeCapability(ctx context.Context, userID uuid.UUID, capability string) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM auth_capabilities WHERE user_id=$1 AND capability=$2`, userID, capability)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return auth.ErrCapabilityNotFound
	}
	return nil
}

func (r *pgRepo) ListCapabilities(ctx context.Context, userID uuid.UUID) ([]auth.Capability, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT user_id, capability, granted_at, granted_by, expires_at
		  FROM auth_capabilities
		 WHERE user_id=$1
		   AND (expires_at IS NULL OR expires_at > NOW())
		 ORDER BY capability`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []auth.Capability
	for rows.Next() {
		var c auth.Capability
		var exp *time.Time
		if err := rows.Scan(&c.UserID, &c.Capability, &c.GrantedAt, &c.GrantedBy, &exp); err != nil {
			return nil, err
		}
		if exp != nil { c.ExpiresAt = *exp }
		out = append(out, c)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Implement the same four methods on `authtest/mock.go`**

Append to `pkg/services/auth/authtest/mock.go`:

```go
type capKey struct {
	UserID     uuid.UUID
	Capability string
}

// (extend RepoMock fields with `caps map[capKey]auth.Capability`)
// In NewMock(), initialize: caps: map[capKey]auth.Capability{}

func (m *RepoMock) HasCapability(_ context.Context, userID uuid.UUID, capability string) (bool, error) {
	m.mu.Lock(); defer m.mu.Unlock()
	c, ok := m.caps[capKey{userID, capability}]
	if !ok { return false, nil }
	if !c.ExpiresAt.IsZero() && c.ExpiresAt.Before(time.Now()) {
		return false, nil
	}
	return true, nil
}

func (m *RepoMock) GrantCapability(_ context.Context, c auth.Capability) error {
	m.mu.Lock(); defer m.mu.Unlock()
	if c.GrantedAt.IsZero() { c.GrantedAt = time.Now() }
	m.caps[capKey{c.UserID, c.Capability}] = c
	return nil
}

func (m *RepoMock) RevokeCapability(_ context.Context, userID uuid.UUID, capability string) error {
	m.mu.Lock(); defer m.mu.Unlock()
	k := capKey{userID, capability}
	if _, ok := m.caps[k]; !ok { return auth.ErrCapabilityNotFound }
	delete(m.caps, k)
	return nil
}

func (m *RepoMock) ListCapabilities(_ context.Context, userID uuid.UUID) ([]auth.Capability, error) {
	m.mu.Lock(); defer m.mu.Unlock()
	now := time.Now()
	var out []auth.Capability
	for k, c := range m.caps {
		if k.UserID != userID { continue }
		if !c.ExpiresAt.IsZero() && c.ExpiresAt.Before(now) { continue }
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Capability < out[j].Capability })
	return out, nil
}
```

Add `"sort"` to the import block. Add `caps: map[capKey]auth.Capability{},` to `NewMock`'s struct literal. Add `caps  map[capKey]auth.Capability` to the `RepoMock` struct definition.

- [ ] **Step 5: Run unit tests + pgtest**

```bash
go test ./pkg/services/auth/...                         # mock tests
just test-pg -run TestRepoCapabilities -count=1         # postgres tests
```

Expected: both PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/services/auth/repo.go \
        pkg/services/auth/repo_errors_test.go \
        pkg/services/auth/postgres/migrations/002_*.sql \
        pkg/services/auth/postgres/repo.go \
        pkg/services/auth/postgres/repo_test.go \
        pkg/services/auth/authtest/mock.go
git commit -m "$(cat <<'EOF'
feat(auth): add auth_capabilities table + Has/Grant/Revoke/List

Lands the capability storage that chat (and future services) needs
for authorization checks. Migration is additive; existing auth_users
rows unchanged. Capabilities are atomic permission strings with
optional time-bound expiration.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

### Task 1.5: Add 30s TTL cache for HasCapability

**Files:**
- Create: `pkg/services/auth/capability_cache.go`
- Create: `pkg/services/auth/capability_cache_test.go`

- [ ] **Step 1: Write a failing test**

`pkg/services/auth/capability_cache_test.go`:

```go
package auth_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zenion/mmokit/pkg/services/auth"
)

type counterRepo struct {
	auth.Repository
	calls int64
}

func (c *counterRepo) HasCapability(ctx context.Context, u uuid.UUID, cap string) (bool, error) {
	atomic.AddInt64(&c.calls, 1)
	return cap == "chat.admin", nil
}

func TestCapabilityCache_HitsAreCached(t *testing.T) {
	r := &counterRepo{}
	cache := auth.NewCapabilityCache(r, 30*time.Second)
	uid := uuid.New()
	for i := 0; i < 5; i++ {
		has, _ := cache.HasCapability(context.Background(), uid, "chat.admin")
		if !has { t.Fatal("expected cached hit to remain true") }
	}
	if got := atomic.LoadInt64(&r.calls); got != 1 {
		t.Fatalf("expected 1 underlying call, got %d", got)
	}
}

func TestCapabilityCache_InvalidateClearsEntry(t *testing.T) {
	r := &counterRepo{}
	cache := auth.NewCapabilityCache(r, 30*time.Second)
	uid := uuid.New()
	_, _ = cache.HasCapability(context.Background(), uid, "chat.admin")
	cache.Invalidate(uid, "chat.admin")
	_, _ = cache.HasCapability(context.Background(), uid, "chat.admin")
	if got := atomic.LoadInt64(&r.calls); got != 2 {
		t.Fatalf("expected 2 calls (invalidate forces re-fetch), got %d", got)
	}
}
```

- [ ] **Step 2: Run test (FAIL)**

```bash
go test ./pkg/services/auth/... -run TestCapabilityCache -count=1
```

Expected: FAIL — `undefined: auth.NewCapabilityCache`.

- [ ] **Step 3: Implement the cache**

`pkg/services/auth/capability_cache.go`:

```go
package auth

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// CapabilityCache wraps a Repository's HasCapability behind a TTL'd
// in-process cache. Reduces Postgres pressure when the same user/cap
// pair is checked often (e.g. on every chat op).
//
// Cache entries are stored only on positive results; negatives are
// cached too (for the same TTL) so repeated unauthorized callers don't
// hammer the DB. Invalidate clears a specific entry — call it from
// Grant/Revoke paths so revocation propagates within the cache TTL.
type CapabilityCache struct {
	repo Repository
	ttl  time.Duration

	mu    sync.RWMutex
	items map[capCacheKey]capCacheEntry
}

type capCacheKey struct {
	UserID     uuid.UUID
	Capability string
}

type capCacheEntry struct {
	Has       bool
	ExpiresAt time.Time
}

func NewCapabilityCache(repo Repository, ttl time.Duration) *CapabilityCache {
	return &CapabilityCache{
		repo:  repo,
		ttl:   ttl,
		items: map[capCacheKey]capCacheEntry{},
	}
}

func (c *CapabilityCache) HasCapability(ctx context.Context, userID uuid.UUID, capability string) (bool, error) {
	k := capCacheKey{userID, capability}
	now := time.Now()

	c.mu.RLock()
	if e, ok := c.items[k]; ok && now.Before(e.ExpiresAt) {
		c.mu.RUnlock()
		return e.Has, nil
	}
	c.mu.RUnlock()

	has, err := c.repo.HasCapability(ctx, userID, capability)
	if err != nil {
		return false, err
	}
	c.mu.Lock()
	c.items[k] = capCacheEntry{Has: has, ExpiresAt: now.Add(c.ttl)}
	c.mu.Unlock()
	return has, nil
}

func (c *CapabilityCache) Invalidate(userID uuid.UUID, capability string) {
	c.mu.Lock()
	delete(c.items, capCacheKey{userID, capability})
	c.mu.Unlock()
}
```

- [ ] **Step 4: Run test (PASS)**

```bash
go test ./pkg/services/auth/... -run TestCapabilityCache -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/services/auth/capability_cache.go pkg/services/auth/capability_cache_test.go
git commit -m "feat(auth): add CapabilityCache (30s TTL wrapper for HasCapability)"
```

### Task 1.6: Add capability console commands

**Files:**
- Modify: `pkg/services/auth/console.go`

- [ ] **Step 1: Write the Args/Result structs + commands**

Append to `pkg/services/auth/console.go` (next to existing UsernameArgs etc.):

```go
type CapabilityGrantArgs struct {
	Username   string `cmd:"help=target username,complete=players"`
	Capability string `cmd:"help=capability string (e.g. chat.admin)"`
	Duration   string `cmd:"optional,help=optional expiry duration (e.g. 24h); empty = permanent"`
}

type CapabilityRevokeArgs struct {
	Username   string `cmd:"help=target username,complete=players"`
	Capability string `cmd:"help=capability string (e.g. chat.admin)"`
}

type CapabilityListResult struct {
	Capabilities []CapabilityDigest
}

type CapabilityDigest struct {
	Capability string
	GrantedAt  time.Time
	GrantedBy  string
	ExpiresAt  time.Time
}

type BootstrapAdminArgs struct {
	Username string `cmd:"help=user to bootstrap as cluster admin (must already exist)"`
}
```

Then in `RegisterConsoleCommands`, add four new `reg.Register` blocks following the existing pattern. Use these handler closures (define them as helpers below `RegisterConsoleCommands`):

```go
func userGrantHandler(getRepo RepoProvider) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(*CapabilityGrantArgs)
		repo := getRepo()
		if repo == nil { return nil, errors.New("auth service not initialized") }
		name := strings.ToLower(strings.TrimSpace(args.Username))
		user, err := repo.GetUserByUsername(ctx, name)
		if err != nil { return nil, fmt.Errorf("user %q: %w", name, err) }
		grantedBy, _ := callerUserIDFromEnv(env) // best-effort; falls back to user.UserID for self-grants
		if grantedBy == uuid.Nil { grantedBy = user.UserID }
		c := Capability{UserID: user.UserID, Capability: args.Capability, GrantedBy: grantedBy}
		if args.Duration != "" {
			d, err := time.ParseDuration(args.Duration)
			if err != nil { return nil, fmt.Errorf("invalid duration: %w", err) }
			c.ExpiresAt = time.Now().Add(d)
		}
		if err := repo.GrantCapability(ctx, c); err != nil { return nil, err }
		return &OKResult{OK: true, Username: name, Detail: "granted " + args.Capability}, nil
	}
}

func userRevokeCapHandler(getRepo RepoProvider) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(*CapabilityRevokeArgs)
		repo := getRepo()
		if repo == nil { return nil, errors.New("auth service not initialized") }
		user, err := repo.GetUserByUsername(ctx, strings.ToLower(args.Username))
		if err != nil { return nil, err }
		if err := repo.RevokeCapability(ctx, user.UserID, args.Capability); err != nil {
			return nil, err
		}
		return &OKResult{OK: true, Username: user.Username, Detail: "revoked " + args.Capability}, nil
	}
}

func userCapabilitiesHandler(getRepo RepoProvider) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(*UsernameArgs)
		repo := getRepo()
		if repo == nil { return nil, errors.New("auth service not initialized") }
		user, err := repo.GetUserByUsername(ctx, strings.ToLower(args.Username))
		if err != nil { return nil, err }
		caps, err := repo.ListCapabilities(ctx, user.UserID)
		if err != nil { return nil, err }
		out := make([]CapabilityDigest, 0, len(caps))
		for _, c := range caps {
			out = append(out, CapabilityDigest{
				Capability: c.Capability, GrantedAt: c.GrantedAt,
				GrantedBy: c.GrantedBy.String(), ExpiresAt: c.ExpiresAt,
			})
		}
		return &CapabilityListResult{Capabilities: out}, nil
	}
}

func bootstrapAdminHandler(getRepo RepoProvider, defaultBootstrapCaps []string) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(*BootstrapAdminArgs)
		repo := getRepo()
		if repo == nil { return nil, errors.New("auth service not initialized") }
		user, err := repo.GetUserByUsername(ctx, strings.ToLower(args.Username))
		if err != nil { return nil, err }
		// Fail-safe: refuse if any *.admin capability has already been granted to anyone.
		// Implementation: ListCapabilities for THIS user; if non-empty AND any ends in .admin,
		// already bootstrapped. (A tighter check across all users would need a new repo
		// method; for v1 the per-user check is sufficient — the bootstrap path is a one-shot
		// operator action and any subsequent grants flow through `auth user grant`.)
		existing, _ := repo.ListCapabilities(ctx, user.UserID)
		for _, c := range existing {
			if strings.HasSuffix(c.Capability, ".admin") {
				return nil, fmt.Errorf("user %q already has admin capability %q", user.Username, c.Capability)
			}
		}
		for _, cap := range defaultBootstrapCaps {
			if err := repo.GrantCapability(ctx, Capability{
				UserID: user.UserID, Capability: cap, GrantedBy: user.UserID,
			}); err != nil {
				return nil, fmt.Errorf("grant %s: %w", cap, err)
			}
		}
		return &OKResult{OK: true, Username: user.Username,
			Detail: fmt.Sprintf("granted %d admin capabilities", len(defaultBootstrapCaps))}, nil
	}
}

// callerUserIDFromEnv returns the user_id of whoever invoked the command,
// or uuid.Nil when unavailable (e.g., bootstrap from a fresh console with
// no auth context).
func callerUserIDFromEnv(env *cmdsys.Env) (uuid.UUID, bool) {
	if env == nil || env.CallerUserID == "" { return uuid.Nil, false }
	id, err := uuid.Parse(env.CallerUserID)
	if err != nil { return uuid.Nil, false }
	return id, true
}
```

Then register the commands inside `RegisterConsoleCommands`:

```go
if err := must(reg.Register(cmdsys.Command{
	Verb:        "auth.user.grant",
	Capability:  "auth.user.write",
	Description: "grant a capability to a user (optional duration)",
	Examples:    []string{"auth user grant alice chat.admin", "auth user grant alice chat.admin 24h"},
	Route:       cmdsys.RouteLocal,
	Args:        CapabilityGrantArgs{},
	Result:      OKResult{},
	Handler:     userGrantHandler(getRepo),
})); err != nil { return err }

if err := must(reg.Register(cmdsys.Command{
	Verb:        "auth.user.revoke",
	Capability:  "auth.user.write",
	Description: "revoke a capability from a user",
	Examples:    []string{"auth user revoke alice chat.admin"},
	Route:       cmdsys.RouteLocal,
	Args:        CapabilityRevokeArgs{},
	Result:      OKResult{},
	Handler:     userRevokeCapHandler(getRepo),
})); err != nil { return err }

if err := must(reg.Register(cmdsys.Command{
	Verb:        "auth.user.capabilities",
	Capability:  "auth.user.read",
	Description: "list active capabilities for a user",
	Examples:    []string{"auth user capabilities alice"},
	Route:       cmdsys.RouteLocal,
	Args:        UsernameArgs{},
	Result:      CapabilityListResult{},
	Handler:     userCapabilitiesHandler(getRepo),
})); err != nil { return err }

if err := must(reg.Register(cmdsys.Command{
	Verb:        "auth.bootstrap-admin",
	Capability:  "",  // intentionally empty: bootstrap is a one-shot console action with no caller capability requirement
	Description: "one-time: grant the admin capability set to a user; refuses if any admin cap already granted",
	Examples:    []string{"auth bootstrap-admin alice"},
	Route:       cmdsys.RouteLocal,
	Args:        BootstrapAdminArgs{},
	Result:      OKResult{},
	Handler:     bootstrapAdminHandler(getRepo, []string{"auth.admin", "chat.admin"}),
})); err != nil { return err }
```

The `defaultBootstrapCaps` slice grows additively as more services land — chat adds `"chat.admin"`; future guild service adds `"guild.admin"`; etc. For v1 we hardcode the auth + chat pair.

- [ ] **Step 2: Verify build**

```bash
go vet ./pkg/services/auth/... ./pkg/mmokit/...
```

Expected: clean. If `cmdsys.Env.CallerUserID` doesn't exist, fallback to `uuid.Nil` (the helper already handles it).

- [ ] **Step 3: Run unit tests**

```bash
go test ./pkg/services/auth/...
```

Expected: PASS — existing tests still run; no new test added at this layer (handlers are exercised by integration tests in Phase 11).

- [ ] **Step 4: Commit**

```bash
git add pkg/services/auth/console.go
git commit -m "feat(auth): add grant/revoke/capabilities/bootstrap-admin console commands"
```

### Task 1.7: Smoke-verify the auth service still works end-to-end

**Files:** none (verification)

- [ ] **Step 1: Run the full auth test suite**

```bash
go test ./pkg/services/auth/... ./pkg/mmokit/... -count=1
```

Expected: PASS.

- [ ] **Step 2: Run pgtest integration**

```bash
just test-pg
```

Expected: PASS — existing auth tests + the three new capability tests.

- [ ] **Step 3: Run the auth-cookie cluster test**

```bash
go test ./pkg/universe/... -run AuthCookie -count=1
```

Expected: PASS — confirms the gateway hook + service registration still work after the move.

- [ ] **Step 4: No commit needed**

Phase 1 prereq is complete; pause here for review before Phase 2.

---

## Phase 2 — Chat package skeleton + typed messages

### Task 2.1: Create chat package directory + doc.go

**Files:**
- Create: `pkg/services/chat/doc.go`

- [ ] **Step 1: Create the directory + doc file**

```bash
mkdir -p pkg/services/chat
```

`pkg/services/chat/doc.go`:

```go
// Package chat is mmokit's engine-tier chat service.
//
// Single-instance v1: RAM-authoritative for transient state (subscriptions,
// rate buckets, online presence, message-id TTL index) and Postgres-backed
// for durable state (channel definitions, memberships, mutes). Messages and
// DMs are pure pass-through — no ring buffer, no recent-history-on-join.
//
// Wired into a game with one line:
//
//	mmokit.RegisterChatService(coord, mmokit.ChatOpts{
//	    DefaultChannels: []mmokit.DefaultChannelDef{
//	        {Slug: "world", Kind: mmokit.ChannelKindSystemAll, Topic: "World chat"},
//	    },
//	})
//
// See docs/superpowers/specs/2026-05-07-chat-service-design.md.
package chat
```

- [ ] **Step 2: Verify it compiles as a (currently empty) package**

```bash
go vet ./pkg/services/chat/...
```

Expected: clean (no other files yet).

- [ ] **Step 3: Commit**

```bash
git add pkg/services/chat/doc.go
git commit -m "feat(chat): scaffold pkg/services/chat package"
```

### Task 2.2: Define typed wire messages

**Files:**
- Create: `pkg/services/chat/typed_messages.go`

- [ ] **Step 1: Write the wire structs**

`pkg/services/chat/typed_messages.go` — full content per spec §5. Includes the const sets, errorBlock, ChannelInfo, MemberInfo, all 25 request/response struct pairs, all 14 server-event structs.

```go
// Package chat — typed_messages.go defines the typed Go structs that
// carry chat traffic on the typed-op (channel 0x01) and typed-event
// (channel 0x00) wire formats. No protobuf. Wire-stable typeIDs are
// derived from each struct's package-qualified name.
//
// Renames are wire-breaking; field additions are backward-compat;
// field removals are wire-breaking. Same conventions as pkg/services/auth.
package chat

// ChannelKind classifies a channel's membership semantics.
type ChannelKind uint32

const (
	ChannelKindUnspecified     ChannelKind = 0
	ChannelKindSystemAll       ChannelKind = 1 // implicit membership: every online user
	ChannelKindSystemPredicate ChannelKind = 2 // explicit members; pushed by services (guild/party/alliance)
	ChannelKindCustom          ChannelKind = 3 // explicit members; user-created
)

// ChatError is the stable error vocabulary for chat ops. Wire values
// must not change once shipped.
type ChatError uint32

const (
	ChatErrorUnspecified        ChatError = 0
	ChatErrorChannelNotFound    ChatError = 1
	ChatErrorNotAMember         ChatError = 2
	ChatErrorMuted              ChatError = 3
	ChatErrorBanned             ChatError = 4
	ChatErrorRateLimited        ChatError = 5
	ChatErrorSlowMode           ChatError = 6
	ChatErrorPayloadTooLarge    ChatError = 7
	ChatErrorInvalidPassword    ChatError = 8
	ChatErrorMessageUnknown     ChatError = 9
	ChatErrorPermissionDenied   ChatError = 10
	ChatErrorReservedSlug       ChatError = 11
	ChatErrorSlugInUse          ChatError = 12
	ChatErrorRecipientOffline   ChatError = 13
	ChatErrorMaxChannelsReached ChatError = 14
	ChatErrorMaxMembersReached  ChatError = 15
	ChatErrorInternal           ChatError = 16
)

// ErrorBlock is the standard error fields embedded on every Response
// struct. ErrorCode == 0 ⇒ success; non-zero ⇒ ChatError value.
//
// Exported so tests and internal callers can construct error responses
// directly. Field names use Go's CamelCase; they map to lowerCamelCase
// on the JS SDK side automatically.
type ErrorBlock struct {
	ErrorCode    uint32
	ErrorMessage string
	RetryAfterMs int64
}

// ChannelInfo is the public summary of a channel. Returned by Join,
// Create, RegisterChannel, ListChannels, and embedded in
// ChatChannelUpdatedEvent.
type ChannelInfo struct {
	ChannelID       string
	Slug            string
	Kind            ChannelKind
	Topic           string
	SlowModeSeconds int32
	OwnerUserID     string // empty for system channels
	MemberCount     int32
	HasPassword     bool
}

type MemberInfo struct {
	UserID     string
	Username   string // denormalized snapshot
	Role       string // "member" | "admin"
	JoinedAtMs int64
}

// --- Player ops ---

type ChatSendRequest  struct { ChannelID, Body string }
type ChatSendResponse struct {
	MsgID    string
	SentAtMs int64
	ErrorBlock
}

type ChatSendDMRequest  struct { RecipientUserID, Body string }
type ChatSendDMResponse struct {
	MsgID    string
	SentAtMs int64
	ErrorBlock
}

type ChatJoinRequest  struct { Slug, Password string }
type ChatJoinResponse struct {
	Channel ChannelInfo
	ErrorBlock
}

type ChatLeaveRequest  struct { ChannelID string }
type ChatLeaveResponse struct{ ErrorBlock }

type ChatCreateRequest  struct { Slug, Password, Topic string }
type ChatCreateResponse struct {
	Channel ChannelInfo
	ErrorBlock
}

type ChatListChannelsRequest  struct{}
type ChatListChannelsResponse struct {
	Channels []ChannelInfo
	ErrorBlock
}

type ChatListMembersRequest  struct{ ChannelID string }
type ChatListMembersResponse struct {
	Members []MemberInfo
	ErrorBlock
}

type ChatRenameChannelRequest  struct{ ChannelID, NewSlug string }
type ChatRenameChannelResponse struct {
	Channel ChannelInfo
	ErrorBlock
}

type ChatSetTopicRequest    struct{ ChannelID, Topic string }
type ChatSetTopicResponse   struct{ ErrorBlock }

type ChatSetSlowModeRequest  struct{
	ChannelID string
	Seconds   int32
}
type ChatSetSlowModeResponse struct{ ErrorBlock }

// --- Membership-mutation ops (capability-gated) ---

type ChatAddMemberRequest    struct{ ChannelID, UserID, Role string }
type ChatAddMemberResponse   struct{ ErrorBlock }

type ChatRemoveMemberRequest  struct{ ChannelID, UserID string }
type ChatRemoveMemberResponse struct{ ErrorBlock }

type ChatBulkSetMembersRequest  struct{
	ChannelID string
	UserIDs   []string
}
type ChatBulkSetMembersResponse struct{ ErrorBlock }

type ChatRegisterChannelRequest struct {
	Slug            string
	Kind            ChannelKind
	Topic           string
	SlowModeSeconds int32
	Password        string
}
type ChatRegisterChannelResponse struct {
	Channel ChannelInfo
	ErrorBlock
}

type ChatUnregisterChannelRequest  struct{ ChannelID string }
type ChatUnregisterChannelResponse struct{ ErrorBlock }

type ChatSetMemberRoleRequest    struct{ ChannelID, UserID, Role string }
type ChatSetMemberRoleResponse   struct{ ErrorBlock }

// --- Moderation ops (capability-gated) ---

type ChatDeleteMessageRequest  struct{ MsgID, ChannelID string }
type ChatDeleteMessageResponse struct{ ErrorBlock }

type ChatMuteUserRequest struct {
	UserID     string
	ChannelID  string // empty = global mute (chat.admin only)
	DurationMs int64
	Reason     string
}
type ChatMuteUserResponse struct{ ErrorBlock }

type ChatUnmuteUserRequest    struct{ UserID, ChannelID string }
type ChatUnmuteUserResponse   struct{ ErrorBlock }

type ChatKickRequest    struct{ ChannelID, UserID, Reason string }
type ChatKickResponse   struct{ ErrorBlock }

type ChatBanRequest struct {
	ChannelID  string
	UserID     string
	DurationMs int64
	Reason     string
}
type ChatBanResponse struct{ ErrorBlock }

type ChatUnbanRequest    struct{ ChannelID, UserID string }
type ChatUnbanResponse   struct{ ErrorBlock }

type ChatBroadcastRequest    struct{ ChannelID, Body string }
type ChatBroadcastResponse   struct{ ErrorBlock }

// --- Service-internal ops (gateway → chat) ---
//
// These are dispatched by the gateway after successful auth to drive
// presence + subscription bookkeeping in chat. NOT intended for client
// use; the gateway capability-gates them via an in-process bypass.

type ChatSessionEnterRequest struct {
	ConnID    uint32
	UserID    string
	Username  string
	GatewayID string
}
type ChatSessionEnterResponse struct{ ErrorBlock }

type ChatSessionLeaveRequest struct {
	ConnID    uint32
	GatewayID string
}
type ChatSessionLeaveResponse struct{ ErrorBlock }

// --- Chat → client server events (typed-event channel) ---

type ChatMessageEvent struct {
	MsgID          string
	ChannelID      string
	SenderUserID   string // empty = system broadcast
	SenderUsername string
	Body           string
	SentAtMs       int64
}

type ChatDMEvent struct {
	MsgID          string
	SenderUserID   string
	SenderUsername string
	Body           string
	SentAtMs       int64
}

type ChatMemberJoinedEvent struct {
	ChannelID string
	UserID    string
	Username  string
}

type ChatMemberLeftEvent struct {
	ChannelID string
	UserID    string
}

type ChatMessageDeletedEvent struct {
	MsgID           string
	ChannelID       string
	DeletedByUserID string
}

type ChatMutedEvent struct {
	ChannelID string // empty = global mute
	UntilMs   int64
	Reason    string
}

type ChatUnmutedEvent struct {
	ChannelID string // empty = global unmute
}

type ChatKickedEvent struct {
	ChannelID string
	ByUserID  string
	Reason    string
}

type ChatBannedEvent struct {
	ChannelID string
	ByUserID  string
	UntilMs   int64
	Reason    string
}

type ChatChannelUpdatedEvent struct {
	Channel ChannelInfo
}

type ChatChannelGoneEvent struct {
	ChannelID string
}

type ChatMemberRoleChangedEvent struct {
	ChannelID string
	UserID    string
	Role      string // "member" | "admin"
}

type ChatRateLimitedEvent struct {
	RetryAfterMs int64
}

type ChatChannelsHydratedEvent struct {
	Channels []ChannelInfo
}
```

- [ ] **Step 2: Verify it compiles**

```bash
go vet ./pkg/services/chat/...
```

Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add pkg/services/chat/typed_messages.go
git commit -m "feat(chat): define typed wire messages (no protobuf)"
```

### Task 2.3: Register the 14 server-event types

**Files:**
- Create: `pkg/services/chat/events.go`

- [ ] **Step 1: Write a failing test**

`pkg/services/chat/events_test.go`:

```go
package chat_test

import (
	"testing"

	"github.com/zenion/mmokit/pkg/mmokit"
	"github.com/zenion/mmokit/pkg/services/chat"
)

func TestRegisterChatServerEvents_TypeIDsRegistered(t *testing.T) {
	chat.RegisterChatServerEvents()
	for _, sample := range []any{
		(*chat.ChatMessageEvent)(nil),
		(*chat.ChatDMEvent)(nil),
		(*chat.ChatMemberJoinedEvent)(nil),
		(*chat.ChatMemberLeftEvent)(nil),
		(*chat.ChatMessageDeletedEvent)(nil),
		(*chat.ChatMutedEvent)(nil),
		(*chat.ChatUnmutedEvent)(nil),
		(*chat.ChatKickedEvent)(nil),
		(*chat.ChatBannedEvent)(nil),
		(*chat.ChatChannelUpdatedEvent)(nil),
		(*chat.ChatChannelGoneEvent)(nil),
		(*chat.ChatMemberRoleChangedEvent)(nil),
		(*chat.ChatRateLimitedEvent)(nil),
		(*chat.ChatChannelsHydratedEvent)(nil),
	} {
		if id := mmokit.TypeIDOfPtr(sample); id == 0 {
			t.Errorf("typeID for %T not registered", sample)
		}
	}
}
```

- [ ] **Step 2: Run test (FAIL)**

```bash
go test ./pkg/services/chat/... -run TestRegisterChatServerEvents -count=1
```

Expected: FAIL — `undefined: chat.RegisterChatServerEvents`. (Or a `mmokit.TypeIDOfPtr`-not-defined error if that helper needs adding. If so, look at how auth introspects typeIDs and use the same approach — search `mmokit.TypeIDOf` in `pkg/mmokit/`.)

- [ ] **Step 3: Implement RegisterChatServerEvents**

`pkg/services/chat/events.go`:

```go
package chat

import (
	"sync"

	"github.com/zenion/mmokit/pkg/mmokit"
)

var registerChatServerEventsOnce sync.Once

// RegisterChatServerEvents registers every chat-emitted typed event so
// the server-event codec can encode/decode by typeID and the SDK
// generator can emit typed handlers. Idempotent. Called by Service.Init
// and by tests directly.
func RegisterChatServerEvents() {
	registerChatServerEventsOnce.Do(func() {
		mmokit.RegisterEvent[ChatMessageEvent]()
		mmokit.RegisterEvent[ChatDMEvent]()
		mmokit.RegisterEvent[ChatMemberJoinedEvent]()
		mmokit.RegisterEvent[ChatMemberLeftEvent]()
		mmokit.RegisterEvent[ChatMessageDeletedEvent]()
		mmokit.RegisterEvent[ChatMutedEvent]()
		mmokit.RegisterEvent[ChatUnmutedEvent]()
		mmokit.RegisterEvent[ChatKickedEvent]()
		mmokit.RegisterEvent[ChatBannedEvent]()
		mmokit.RegisterEvent[ChatChannelUpdatedEvent]()
		mmokit.RegisterEvent[ChatChannelGoneEvent]()
		mmokit.RegisterEvent[ChatMemberRoleChangedEvent]()
		mmokit.RegisterEvent[ChatRateLimitedEvent]()
		mmokit.RegisterEvent[ChatChannelsHydratedEvent]()
	})
}
```

If `mmokit.RegisterEvent[T]()` is the wrong helper name in this codebase, look at how `pkg/mmokit/event_messages.go::registerEngineTypedEvents` is implemented and use the same primitive (likely `mmokit.RegisterServerEvent[T]` or similar). The exact spelling is whatever the codebase uses today; this plan defers to the actual API surface.

- [ ] **Step 4: Run test (PASS)**

```bash
go test ./pkg/services/chat/... -run TestRegisterChatServerEvents -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/services/chat/events.go pkg/services/chat/events_test.go
git commit -m "feat(chat): register 14 server-event types via mmokit reflection codec"
```

---

## Phase 3 — Schema, Repository interface, Postgres impl, in-memory mock

### Task 3.1: Define Repository interface + row types + errors

**Files:**
- Create: `pkg/services/chat/repo.go`

- [ ] **Step 1: Write the interface**

`pkg/services/chat/repo.go`:

```go
package chat

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// MuteGlobalChannelID is the sentinel UUID used in the chat_mutes
// composite PK to represent a global (channel-wide) mute. Avoids
// Postgres NULL-in-PK semantics.
var MuteGlobalChannelID = uuid.MustParse("00000000-0000-0000-0000-000000000000")

// Errors returned by Repository implementations.
var (
	ErrChannelNotFound    = errors.New("chat: channel not found")
	ErrChannelSlugInUse   = errors.New("chat: channel slug already exists")
	ErrMemberNotFound     = errors.New("chat: member not found")
	ErrMuteNotFound       = errors.New("chat: mute not found")
)

// Channel mirrors chat_channels.
type Channel struct {
	ChannelID       uuid.UUID
	Slug            string
	Kind            string // 'system_all' | 'system_predicate' | 'custom'
	Topic           string
	SlowModeSeconds int
	PasswordHash    string
	OwnerUserID     uuid.UUID // zero value for system channels
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ChannelMember mirrors chat_channel_members.
type ChannelMember struct {
	ChannelID    uuid.UUID
	UserID       uuid.UUID
	Role         string // "member" | "admin"
	JoinedAt     time.Time
	BannedUntil  time.Time // zero value = not banned
	BannedBy     uuid.UUID
	BannedReason string
}

// Mute mirrors chat_mutes. ChannelID == MuteGlobalChannelID denotes
// a global mute for that user across all channels.
type Mute struct {
	UserID    uuid.UUID
	ChannelID uuid.UUID // MuteGlobalChannelID for global
	ExpiresAt time.Time
	Reason    string
	MutedBy   uuid.UUID
	CreatedAt time.Time
}

// Repository abstracts persistence for the chat service. Postgres impl
// lives in pkg/services/chat/postgres; in-memory mock for tests is in
// pkg/services/chat/chattest.
type Repository interface {
	// Channels
	UpsertChannel(ctx context.Context, c Channel) (Channel, error) // INSERT ON CONFLICT (slug) UPDATE; returns the live row
	GetChannelByID(ctx context.Context, id uuid.UUID) (Channel, error)
	GetChannelBySlug(ctx context.Context, slug string) (Channel, error)
	ListAllChannels(ctx context.Context) ([]Channel, error)
	UpdateChannel(ctx context.Context, c Channel) error
	DeleteChannel(ctx context.Context, id uuid.UUID) error

	// Members
	AddOrUpdateMember(ctx context.Context, m ChannelMember) error
	RemoveMember(ctx context.Context, channelID, userID uuid.UUID) error
	BulkSetMembers(ctx context.Context, channelID uuid.UUID, userIDs []uuid.UUID, role string) error // wholesale replace
	ListMembers(ctx context.Context, channelID uuid.UUID) ([]ChannelMember, error)
	ListAllMembers(ctx context.Context) ([]ChannelMember, error) // bootstrap-time scan
	SetMemberRole(ctx context.Context, channelID, userID uuid.UUID, role string) error
	SetMemberBan(ctx context.Context, channelID, userID, bannedBy uuid.UUID, until time.Time, reason string) error
	ClearMemberBan(ctx context.Context, channelID, userID uuid.UUID) error

	// Mutes
	UpsertMute(ctx context.Context, m Mute) error
	DeleteMute(ctx context.Context, userID, channelID uuid.UUID) error
	ListActiveMutes(ctx context.Context) ([]Mute, error)

	// Reaper
	DeleteExpiredMutes(ctx context.Context) (int, error)
	ClearExpiredBans(ctx context.Context) (int, error)
}
```

- [ ] **Step 2: Verify it compiles**

```bash
go vet ./pkg/services/chat/...
```

Expected: clean.

- [ ] **Step 3: Commit**

```bash
git add pkg/services/chat/repo.go
git commit -m "feat(chat): define Repository interface + row types"
```

### Task 3.2: Add Postgres migrations

**Files:**
- Create: `pkg/services/chat/postgres/migrations/001_init.up.sql`
- Create: `pkg/services/chat/postgres/migrations/001_init.down.sql`
- Create: `pkg/services/chat/postgres/migrations.go` — embeds the FS

- [ ] **Step 1: Write `001_init.up.sql`**

```sql
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
```

- [ ] **Step 2: Write `001_init.down.sql`**

```sql
DROP TABLE IF EXISTS chat_mutes;
DROP TABLE IF EXISTS chat_channel_members;
DROP TABLE IF EXISTS chat_channels;
```

- [ ] **Step 3: Wire MigrationsFS in kind.go (we'll create kind.go in next task; for now create it minimally)**

Create `pkg/services/chat/kind.go` with just the migrations FS hook so tests can find it:

```go
package chat

import (
	"embed"
	"io/fs"
)

const KindName = "chat"

//go:embed postgres/migrations/*.sql
var pgMigrations embed.FS

// MigrationsFS returns the chat-package's Postgres migrations.
func MigrationsFS() fs.FS {
	sub, err := fs.Sub(pgMigrations, "postgres/migrations")
	if err != nil {
		panic(err)
	}
	return sub
}
```

- [ ] **Step 4: Verify embed**

```bash
go vet ./pkg/services/chat/...
go test ./pkg/services/chat/... -run TestMigrationsFS -count=1 || true
```

`go vet` should pass. (Test is added in next step.)

- [ ] **Step 5: Add a smoke test that the FS contains both files**

`pkg/services/chat/migrations_test.go`:

```go
package chat_test

import (
	"io/fs"
	"testing"

	"github.com/zenion/mmokit/pkg/services/chat"
)

func TestMigrationsFS_Embeds001(t *testing.T) {
	fsys := chat.MigrationsFS()
	for _, name := range []string{"001_init.up.sql", "001_init.down.sql"} {
		f, err := fsys.Open(name)
		if err != nil { t.Fatalf("open %s: %v", name, err) }
		_ = f.Close()
	}
	// also walk to ensure no unexpected extras in v1
	count := 0
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil { return err }
		if !d.IsDir() { count++ }
		return nil
	})
	if count != 2 {
		t.Fatalf("expected exactly 2 SQL files in v1 migrations, got %d", count)
	}
}
```

```bash
go test ./pkg/services/chat/... -run TestMigrationsFS -count=1
```

Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add pkg/services/chat/postgres/migrations/ pkg/services/chat/kind.go pkg/services/chat/migrations_test.go
git commit -m "feat(chat): add 001_init migration + MigrationsFS hook"
```

### Task 3.3: Implement in-memory Repository mock

**Files:**
- Create: `pkg/services/chat/chattest/mock.go`

- [ ] **Step 1: Write the mock**

`pkg/services/chat/chattest/mock.go`:

```go
// Package chattest provides an in-memory chat.Repository for tests.
package chattest

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/zenion/mmokit/pkg/services/chat"
)

type RepoMock struct {
	mu       sync.Mutex
	channels map[uuid.UUID]chat.Channel
	bySlug   map[string]uuid.UUID
	members  map[memberKey]chat.ChannelMember
	mutes    map[muteKey]chat.Mute
}

type memberKey struct{ ChannelID, UserID uuid.UUID }
type muteKey struct{ UserID, ChannelID uuid.UUID }

func NewMock() *RepoMock {
	return &RepoMock{
		channels: map[uuid.UUID]chat.Channel{},
		bySlug:   map[string]uuid.UUID{},
		members:  map[memberKey]chat.ChannelMember{},
		mutes:    map[muteKey]chat.Mute{},
	}
}

var _ chat.Repository = (*RepoMock)(nil)

// --- Channels ---

func (m *RepoMock) UpsertChannel(_ context.Context, c chat.Channel) (chat.Channel, error) {
	m.mu.Lock(); defer m.mu.Unlock()
	if c.ChannelID == uuid.Nil {
		// On INSERT: lookup by slug; if slug exists with different ID, we'd fail UNIQUE
		if existingID, ok := m.bySlug[c.Slug]; ok {
			c.ChannelID = existingID
			c.UpdatedAt = time.Now()
			existing := m.channels[existingID]
			c.CreatedAt = existing.CreatedAt
			m.channels[existingID] = c
			return c, nil
		}
		c.ChannelID = uuid.New()
	}
	if existingID, ok := m.bySlug[c.Slug]; ok && existingID != c.ChannelID {
		return chat.Channel{}, chat.ErrChannelSlugInUse
	}
	if c.CreatedAt.IsZero() { c.CreatedAt = time.Now() }
	c.UpdatedAt = time.Now()
	m.channels[c.ChannelID] = c
	m.bySlug[c.Slug] = c.ChannelID
	return c, nil
}

func (m *RepoMock) GetChannelByID(_ context.Context, id uuid.UUID) (chat.Channel, error) {
	m.mu.Lock(); defer m.mu.Unlock()
	c, ok := m.channels[id]
	if !ok { return chat.Channel{}, chat.ErrChannelNotFound }
	return c, nil
}

func (m *RepoMock) GetChannelBySlug(_ context.Context, slug string) (chat.Channel, error) {
	m.mu.Lock(); defer m.mu.Unlock()
	id, ok := m.bySlug[slug]
	if !ok { return chat.Channel{}, chat.ErrChannelNotFound }
	return m.channels[id], nil
}

func (m *RepoMock) ListAllChannels(_ context.Context) ([]chat.Channel, error) {
	m.mu.Lock(); defer m.mu.Unlock()
	out := make([]chat.Channel, 0, len(m.channels))
	for _, c := range m.channels { out = append(out, c) }
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

func (m *RepoMock) UpdateChannel(_ context.Context, c chat.Channel) error {
	m.mu.Lock(); defer m.mu.Unlock()
	existing, ok := m.channels[c.ChannelID]
	if !ok { return chat.ErrChannelNotFound }
	if existing.Slug != c.Slug {
		if _, taken := m.bySlug[c.Slug]; taken { return chat.ErrChannelSlugInUse }
		delete(m.bySlug, existing.Slug)
		m.bySlug[c.Slug] = c.ChannelID
	}
	c.UpdatedAt = time.Now()
	c.CreatedAt = existing.CreatedAt
	m.channels[c.ChannelID] = c
	return nil
}

func (m *RepoMock) DeleteChannel(_ context.Context, id uuid.UUID) error {
	m.mu.Lock(); defer m.mu.Unlock()
	c, ok := m.channels[id]
	if !ok { return chat.ErrChannelNotFound }
	delete(m.channels, id)
	delete(m.bySlug, c.Slug)
	for k := range m.members {
		if k.ChannelID == id { delete(m.members, k) }
	}
	return nil
}

// --- Members ---

func (m *RepoMock) AddOrUpdateMember(_ context.Context, mem chat.ChannelMember) error {
	m.mu.Lock(); defer m.mu.Unlock()
	if _, ok := m.channels[mem.ChannelID]; !ok { return chat.ErrChannelNotFound }
	if mem.JoinedAt.IsZero() { mem.JoinedAt = time.Now() }
	if mem.Role == "" { mem.Role = "member" }
	m.members[memberKey{mem.ChannelID, mem.UserID}] = mem
	return nil
}

func (m *RepoMock) RemoveMember(_ context.Context, channelID, userID uuid.UUID) error {
	m.mu.Lock(); defer m.mu.Unlock()
	k := memberKey{channelID, userID}
	if _, ok := m.members[k]; !ok { return chat.ErrMemberNotFound }
	delete(m.members, k)
	return nil
}

func (m *RepoMock) BulkSetMembers(_ context.Context, channelID uuid.UUID, userIDs []uuid.UUID, role string) error {
	m.mu.Lock(); defer m.mu.Unlock()
	if _, ok := m.channels[channelID]; !ok { return chat.ErrChannelNotFound }
	for k := range m.members {
		if k.ChannelID == channelID { delete(m.members, k) }
	}
	if role == "" { role = "member" }
	now := time.Now()
	for _, uid := range userIDs {
		m.members[memberKey{channelID, uid}] = chat.ChannelMember{
			ChannelID: channelID, UserID: uid, Role: role, JoinedAt: now,
		}
	}
	return nil
}

func (m *RepoMock) ListMembers(_ context.Context, channelID uuid.UUID) ([]chat.ChannelMember, error) {
	m.mu.Lock(); defer m.mu.Unlock()
	var out []chat.ChannelMember
	for k, v := range m.members {
		if k.ChannelID == channelID { out = append(out, v) }
	}
	return out, nil
}

func (m *RepoMock) ListAllMembers(_ context.Context) ([]chat.ChannelMember, error) {
	m.mu.Lock(); defer m.mu.Unlock()
	out := make([]chat.ChannelMember, 0, len(m.members))
	for _, v := range m.members { out = append(out, v) }
	return out, nil
}

func (m *RepoMock) SetMemberRole(_ context.Context, channelID, userID uuid.UUID, role string) error {
	m.mu.Lock(); defer m.mu.Unlock()
	k := memberKey{channelID, userID}
	mem, ok := m.members[k]
	if !ok { return chat.ErrMemberNotFound }
	mem.Role = role
	m.members[k] = mem
	return nil
}

func (m *RepoMock) SetMemberBan(_ context.Context, channelID, userID, bannedBy uuid.UUID, until time.Time, reason string) error {
	m.mu.Lock(); defer m.mu.Unlock()
	k := memberKey{channelID, userID}
	mem, ok := m.members[k]
	if !ok {
		mem = chat.ChannelMember{ChannelID: channelID, UserID: userID, Role: "member", JoinedAt: time.Now()}
	}
	mem.BannedUntil = until
	mem.BannedBy = bannedBy
	mem.BannedReason = reason
	m.members[k] = mem
	return nil
}

func (m *RepoMock) ClearMemberBan(_ context.Context, channelID, userID uuid.UUID) error {
	m.mu.Lock(); defer m.mu.Unlock()
	k := memberKey{channelID, userID}
	mem, ok := m.members[k]
	if !ok { return chat.ErrMemberNotFound }
	mem.BannedUntil = time.Time{}
	mem.BannedBy = uuid.Nil
	mem.BannedReason = ""
	m.members[k] = mem
	return nil
}

// --- Mutes ---

func (m *RepoMock) UpsertMute(_ context.Context, mu chat.Mute) error {
	m.mu.Lock(); defer m.mu.Unlock()
	if mu.CreatedAt.IsZero() { mu.CreatedAt = time.Now() }
	m.mutes[muteKey{mu.UserID, mu.ChannelID}] = mu
	return nil
}

func (m *RepoMock) DeleteMute(_ context.Context, userID, channelID uuid.UUID) error {
	m.mu.Lock(); defer m.mu.Unlock()
	k := muteKey{userID, channelID}
	if _, ok := m.mutes[k]; !ok { return chat.ErrMuteNotFound }
	delete(m.mutes, k)
	return nil
}

func (m *RepoMock) ListActiveMutes(_ context.Context) ([]chat.Mute, error) {
	m.mu.Lock(); defer m.mu.Unlock()
	now := time.Now()
	var out []chat.Mute
	for _, v := range m.mutes {
		if v.ExpiresAt.After(now) { out = append(out, v) }
	}
	return out, nil
}

// --- Reaper ---

func (m *RepoMock) DeleteExpiredMutes(_ context.Context) (int, error) {
	m.mu.Lock(); defer m.mu.Unlock()
	now := time.Now()
	n := 0
	for k, v := range m.mutes {
		if !v.ExpiresAt.After(now) { delete(m.mutes, k); n++ }
	}
	return n, nil
}

func (m *RepoMock) ClearExpiredBans(_ context.Context) (int, error) {
	m.mu.Lock(); defer m.mu.Unlock()
	now := time.Now()
	n := 0
	for k, v := range m.members {
		if !v.BannedUntil.IsZero() && !v.BannedUntil.After(now) {
			v.BannedUntil = time.Time{}
			v.BannedBy = uuid.Nil
			v.BannedReason = ""
			m.members[k] = v
			n++
		}
	}
	return n, nil
}

// errors-wrapper passthroughs for symmetry with the postgres impl
var _ = errors.New
```

- [ ] **Step 2: Run a quick smoke test on the mock**

`pkg/services/chat/chattest/mock_test.go`:

```go
package chattest_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zenion/mmokit/pkg/services/chat"
	"github.com/zenion/mmokit/pkg/services/chat/chattest"
)

func TestRepoMock_UpsertAndGetChannel(t *testing.T) {
	m := chattest.NewMock()
	ctx := context.Background()
	c, err := m.UpsertChannel(ctx, chat.Channel{Slug: "world", Kind: "system_all"})
	if err != nil { t.Fatal(err) }
	got, err := m.GetChannelBySlug(ctx, "world")
	if err != nil { t.Fatal(err) }
	if got.ChannelID != c.ChannelID { t.Fatal("channel ID mismatch") }
}

func TestRepoMock_BulkSetMembersReplacesAll(t *testing.T) {
	m := chattest.NewMock()
	ctx := context.Background()
	c, _ := m.UpsertChannel(ctx, chat.Channel{Slug: "guild:42", Kind: "system_predicate"})
	a, b, x := uuid.New(), uuid.New(), uuid.New()
	_ = m.BulkSetMembers(ctx, c.ChannelID, []uuid.UUID{a, b, x}, "member")
	mems, _ := m.ListMembers(ctx, c.ChannelID)
	if len(mems) != 3 { t.Fatalf("got %d members, want 3", len(mems)) }
	_ = m.BulkSetMembers(ctx, c.ChannelID, []uuid.UUID{a}, "member")
	mems, _ = m.ListMembers(ctx, c.ChannelID)
	if len(mems) != 1 { t.Fatalf("got %d after replace, want 1", len(mems)) }
}

func TestRepoMock_DeleteExpiredMutes(t *testing.T) {
	m := chattest.NewMock()
	ctx := context.Background()
	_ = m.UpsertMute(ctx, chat.Mute{UserID: uuid.New(), ChannelID: chat.MuteGlobalChannelID, ExpiresAt: time.Now().Add(-time.Minute), MutedBy: uuid.New()})
	_ = m.UpsertMute(ctx, chat.Mute{UserID: uuid.New(), ChannelID: chat.MuteGlobalChannelID, ExpiresAt: time.Now().Add(time.Hour), MutedBy: uuid.New()})
	n, _ := m.DeleteExpiredMutes(ctx)
	if n != 1 { t.Fatalf("reaped %d, want 1", n) }
}
```

```bash
go test ./pkg/services/chat/chattest/... -count=1
```

Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add pkg/services/chat/chattest/
git commit -m "feat(chat): add chattest.RepoMock in-memory Repository"
```

### Task 3.4: Implement Postgres Repository

**Files:**
- Create: `pkg/services/chat/postgres/repo.go`
- Create: `pkg/services/chat/postgres/repo_test.go` (build tag `pgtest`)

- [ ] **Step 1: Write the Postgres impl**

`pkg/services/chat/postgres/repo.go`:

```go
// Package postgres is the chat Repository's Postgres implementation.
package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zenion/mmokit/pkg/services/chat"
)

type pgRepo struct{ pool *pgxpool.Pool }

// New returns a Postgres-backed chat.Repository.
func New(pool *pgxpool.Pool) chat.Repository { return &pgRepo{pool: pool} }

// --- Channels ---

func (r *pgRepo) UpsertChannel(ctx context.Context, c chat.Channel) (chat.Channel, error) {
	const q = `
		INSERT INTO chat_channels (channel_id, slug, kind, topic, slow_mode_seconds, password_hash, owner_user_id)
		VALUES (COALESCE($1, gen_random_uuid()), $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, $8))
		ON CONFLICT (slug) DO UPDATE
		  SET kind = EXCLUDED.kind,
		      topic = EXCLUDED.topic,
		      slow_mode_seconds = EXCLUDED.slow_mode_seconds,
		      password_hash = EXCLUDED.password_hash,
		      owner_user_id = EXCLUDED.owner_user_id,
		      updated_at = NOW()
		RETURNING channel_id, slug, kind, topic, slow_mode_seconds,
		          COALESCE(password_hash, ''),
		          COALESCE(owner_user_id, $8),
		          created_at, updated_at`
	var idArg any
	if c.ChannelID != uuid.Nil { idArg = c.ChannelID } else { idArg = nil }
	row := r.pool.QueryRow(ctx, q,
		idArg, c.Slug, c.Kind, c.Topic, c.SlowModeSeconds,
		c.PasswordHash, c.OwnerUserID, uuid.Nil,
	)
	var out chat.Channel
	if err := row.Scan(&out.ChannelID, &out.Slug, &out.Kind, &out.Topic, &out.SlowModeSeconds,
		&out.PasswordHash, &out.OwnerUserID, &out.CreatedAt, &out.UpdatedAt); err != nil {
		return chat.Channel{}, err
	}
	return out, nil
}

func (r *pgRepo) GetChannelByID(ctx context.Context, id uuid.UUID) (chat.Channel, error) {
	return r.scanOneChannel(ctx, `WHERE channel_id = $1`, id)
}

func (r *pgRepo) GetChannelBySlug(ctx context.Context, slug string) (chat.Channel, error) {
	return r.scanOneChannel(ctx, `WHERE slug = $1`, slug)
}

func (r *pgRepo) scanOneChannel(ctx context.Context, where string, args ...any) (chat.Channel, error) {
	q := `SELECT channel_id, slug, kind, topic, slow_mode_seconds,
		COALESCE(password_hash, ''), COALESCE(owner_user_id, $$00000000-0000-0000-0000-000000000000$$::uuid),
		created_at, updated_at FROM chat_channels ` + where
	row := r.pool.QueryRow(ctx, q, args...)
	var c chat.Channel
	if err := row.Scan(&c.ChannelID, &c.Slug, &c.Kind, &c.Topic, &c.SlowModeSeconds,
		&c.PasswordHash, &c.OwnerUserID, &c.CreatedAt, &c.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) { return chat.Channel{}, chat.ErrChannelNotFound }
		return chat.Channel{}, err
	}
	return c, nil
}

func (r *pgRepo) ListAllChannels(ctx context.Context) ([]chat.Channel, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT channel_id, slug, kind, topic, slow_mode_seconds,
		       COALESCE(password_hash, ''),
		       COALESCE(owner_user_id, '00000000-0000-0000-0000-000000000000'::uuid),
		       created_at, updated_at
		  FROM chat_channels
		 ORDER BY slug`)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []chat.Channel
	for rows.Next() {
		var c chat.Channel
		if err := rows.Scan(&c.ChannelID, &c.Slug, &c.Kind, &c.Topic, &c.SlowModeSeconds,
			&c.PasswordHash, &c.OwnerUserID, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (r *pgRepo) UpdateChannel(ctx context.Context, c chat.Channel) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE chat_channels
		   SET slug=$2, kind=$3, topic=$4, slow_mode_seconds=$5,
		       password_hash=NULLIF($6, ''),
		       owner_user_id=NULLIF($7, '00000000-0000-0000-0000-000000000000'::uuid),
		       updated_at=NOW()
		 WHERE channel_id=$1`,
		c.ChannelID, c.Slug, c.Kind, c.Topic, c.SlowModeSeconds,
		c.PasswordHash, c.OwnerUserID)
	if err != nil { return err }
	if tag.RowsAffected() == 0 { return chat.ErrChannelNotFound }
	return nil
}

func (r *pgRepo) DeleteChannel(ctx context.Context, id uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM chat_channels WHERE channel_id=$1`, id)
	if err != nil { return err }
	if tag.RowsAffected() == 0 { return chat.ErrChannelNotFound }
	return nil
}

// --- Members ---

func (r *pgRepo) AddOrUpdateMember(ctx context.Context, m chat.ChannelMember) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO chat_channel_members (channel_id, user_id, role)
		VALUES ($1, $2, COALESCE(NULLIF($3, ''), 'member'))
		ON CONFLICT (channel_id, user_id) DO UPDATE SET role = EXCLUDED.role`,
		m.ChannelID, m.UserID, m.Role)
	return err
}

func (r *pgRepo) RemoveMember(ctx context.Context, channelID, userID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM chat_channel_members WHERE channel_id=$1 AND user_id=$2`, channelID, userID)
	if err != nil { return err }
	if tag.RowsAffected() == 0 { return chat.ErrMemberNotFound }
	return nil
}

func (r *pgRepo) BulkSetMembers(ctx context.Context, channelID uuid.UUID, userIDs []uuid.UUID, role string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil { return err }
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM chat_channel_members WHERE channel_id = $1`, channelID); err != nil {
		return err
	}
	if role == "" { role = "member" }
	for _, uid := range userIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO chat_channel_members (channel_id, user_id, role) VALUES ($1, $2, $3)`,
			channelID, uid, role); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (r *pgRepo) ListMembers(ctx context.Context, channelID uuid.UUID) ([]chat.ChannelMember, error) {
	return r.scanMembers(ctx, `WHERE channel_id = $1`, channelID)
}

func (r *pgRepo) ListAllMembers(ctx context.Context) ([]chat.ChannelMember, error) {
	return r.scanMembers(ctx, ``)
}

func (r *pgRepo) scanMembers(ctx context.Context, where string, args ...any) ([]chat.ChannelMember, error) {
	q := `SELECT channel_id, user_id, role, joined_at,
		COALESCE(banned_until, 'epoch'::timestamptz),
		COALESCE(banned_by, '00000000-0000-0000-0000-000000000000'::uuid),
		COALESCE(banned_reason, '')
		FROM chat_channel_members ` + where
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []chat.ChannelMember
	for rows.Next() {
		var m chat.ChannelMember
		if err := rows.Scan(&m.ChannelID, &m.UserID, &m.Role, &m.JoinedAt,
			&m.BannedUntil, &m.BannedBy, &m.BannedReason); err != nil {
			return nil, err
		}
		// Treat 'epoch' sentinel as zero time
		if m.BannedUntil.Year() <= 1970 { m.BannedUntil = time.Time{} }
		out = append(out, m)
	}
	return out, rows.Err()
}

func (r *pgRepo) SetMemberRole(ctx context.Context, channelID, userID uuid.UUID, role string) error {
	tag, err := r.pool.Exec(ctx, `UPDATE chat_channel_members SET role=$3 WHERE channel_id=$1 AND user_id=$2`,
		channelID, userID, role)
	if err != nil { return err }
	if tag.RowsAffected() == 0 { return chat.ErrMemberNotFound }
	return nil
}

func (r *pgRepo) SetMemberBan(ctx context.Context, channelID, userID, bannedBy uuid.UUID, until time.Time, reason string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO chat_channel_members (channel_id, user_id, role, banned_until, banned_by, banned_reason)
		VALUES ($1, $2, 'member', $3, $4, $5)
		ON CONFLICT (channel_id, user_id) DO UPDATE
		  SET banned_until=EXCLUDED.banned_until, banned_by=EXCLUDED.banned_by, banned_reason=EXCLUDED.banned_reason`,
		channelID, userID, until, bannedBy, reason)
	return err
}

func (r *pgRepo) ClearMemberBan(ctx context.Context, channelID, userID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE chat_channel_members
		   SET banned_until=NULL, banned_by=NULL, banned_reason=NULL
		 WHERE channel_id=$1 AND user_id=$2`, channelID, userID)
	if err != nil { return err }
	if tag.RowsAffected() == 0 { return chat.ErrMemberNotFound }
	return nil
}

// --- Mutes ---

func (r *pgRepo) UpsertMute(ctx context.Context, m chat.Mute) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO chat_mutes (user_id, channel_id, expires_at, reason, muted_by)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5)
		ON CONFLICT (user_id, channel_id) DO UPDATE
		  SET expires_at=EXCLUDED.expires_at, reason=EXCLUDED.reason, muted_by=EXCLUDED.muted_by`,
		m.UserID, m.ChannelID, m.ExpiresAt, m.Reason, m.MutedBy)
	return err
}

func (r *pgRepo) DeleteMute(ctx context.Context, userID, channelID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM chat_mutes WHERE user_id=$1 AND channel_id=$2`, userID, channelID)
	if err != nil { return err }
	if tag.RowsAffected() == 0 { return chat.ErrMuteNotFound }
	return nil
}

func (r *pgRepo) ListActiveMutes(ctx context.Context) ([]chat.Mute, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT user_id, channel_id, expires_at, COALESCE(reason, ''), muted_by, created_at
		  FROM chat_mutes
		 WHERE expires_at > NOW()`)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []chat.Mute
	for rows.Next() {
		var mu chat.Mute
		if err := rows.Scan(&mu.UserID, &mu.ChannelID, &mu.ExpiresAt, &mu.Reason, &mu.MutedBy, &mu.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, mu)
	}
	return out, rows.Err()
}

// --- Reaper ---

func (r *pgRepo) DeleteExpiredMutes(ctx context.Context) (int, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM chat_mutes WHERE expires_at <= NOW()`)
	if err != nil { return 0, err }
	return int(tag.RowsAffected()), nil
}

func (r *pgRepo) ClearExpiredBans(ctx context.Context) (int, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE chat_channel_members
		   SET banned_until=NULL, banned_by=NULL, banned_reason=NULL
		 WHERE banned_until IS NOT NULL AND banned_until <= NOW()`)
	if err != nil { return 0, err }
	return int(tag.RowsAffected()), nil
}
```

- [ ] **Step 2: Write minimal pgtest coverage**

`pkg/services/chat/postgres/repo_test.go`:

```go
//go:build pgtest

package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zenion/mmokit/pkg/persist/postgres" // for migrate helper
	"github.com/zenion/mmokit/pkg/services/chat"
	chatpg "github.com/zenion/mmokit/pkg/services/chat/postgres"
)

func newTestRepo(t *testing.T) (chat.Repository, func()) {
	t.Helper()
	url := os.Getenv("POSTGRES_URL")
	if url == "" { url = "postgres://mmo:mmo@localhost:5432/mmo?sslmode=disable" }
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil { t.Fatal(err) }
	if err := postgres.ApplyMigrationsFS(context.Background(), pool, chat.MigrationsFS(), "chat"); err != nil {
		t.Fatal(err)
	}
	// Truncate to isolate tests
	_, _ = pool.Exec(context.Background(), `TRUNCATE chat_channels CASCADE`)
	_, _ = pool.Exec(context.Background(), `TRUNCATE chat_mutes CASCADE`)
	return chatpg.New(pool), func() {
		_, _ = pool.Exec(context.Background(), `TRUNCATE chat_channels CASCADE`)
		_, _ = pool.Exec(context.Background(), `TRUNCATE chat_mutes CASCADE`)
		pool.Close()
	}
}

func TestPgRepo_UpsertChannel_Idempotent(t *testing.T) {
	repo, cleanup := newTestRepo(t)
	defer cleanup()
	ctx := context.Background()
	c1, err := repo.UpsertChannel(ctx, chat.Channel{Slug: "world", Kind: "system_all", Topic: "v1"})
	if err != nil { t.Fatal(err) }
	c2, err := repo.UpsertChannel(ctx, chat.Channel{Slug: "world", Kind: "system_all", Topic: "v2"})
	if err != nil { t.Fatal(err) }
	if c1.ChannelID != c2.ChannelID { t.Fatal("UPSERT changed channel_id") }
	if c2.Topic != "v2" { t.Fatalf("topic=%q want v2", c2.Topic) }
}

func TestPgRepo_BulkSetMembers_Replaces(t *testing.T) {
	repo, cleanup := newTestRepo(t)
	defer cleanup()
	ctx := context.Background()
	c, _ := repo.UpsertChannel(ctx, chat.Channel{Slug: "guild:1", Kind: "system_predicate"})
	a, b := uuid.New(), uuid.New()
	if err := repo.BulkSetMembers(ctx, c.ChannelID, []uuid.UUID{a, b}, "member"); err != nil { t.Fatal(err) }
	if err := repo.BulkSetMembers(ctx, c.ChannelID, []uuid.UUID{a}, "member"); err != nil { t.Fatal(err) }
	mems, _ := repo.ListMembers(ctx, c.ChannelID)
	if len(mems) != 1 { t.Fatalf("got %d, want 1", len(mems)) }
}

func TestPgRepo_DeleteExpiredMutes(t *testing.T) {
	repo, cleanup := newTestRepo(t)
	defer cleanup()
	ctx := context.Background()
	_ = repo.UpsertMute(ctx, chat.Mute{UserID: uuid.New(), ChannelID: chat.MuteGlobalChannelID, ExpiresAt: time.Now().Add(-time.Minute), MutedBy: uuid.New()})
	_ = repo.UpsertMute(ctx, chat.Mute{UserID: uuid.New(), ChannelID: chat.MuteGlobalChannelID, ExpiresAt: time.Now().Add(time.Hour), MutedBy: uuid.New()})
	n, err := repo.DeleteExpiredMutes(ctx)
	if err != nil { t.Fatal(err) }
	if n != 1 { t.Fatalf("reaped %d, want 1", n) }
}
```

If `pkg/persist/postgres.ApplyMigrationsFS` doesn't exist with that exact name, look for the equivalent in `pkg/persist/postgres/` (auth uses it via `Config.ExtraMigrations`); the actual helper name may be `RunMigrationsFromFS` or similar. Adjust the import.

- [ ] **Step 3: Run pgtest**

```bash
just db-up   # ensure docker postgres is running
just test-pg -run TestPgRepo -count=1
```

Expected: PASS.

- [ ] **Step 4: Run unit tests + vet**

```bash
go test ./pkg/services/chat/...
go vet ./pkg/services/chat/...
```

Expected: PASS / clean.

- [ ] **Step 5: Commit**

```bash
git add pkg/services/chat/postgres/
git commit -m "feat(chat): add Postgres Repository implementation + pgtest"
```

---

## Phase 4 — kind.go, ServiceOpts, Service skeleton, bootstrap

### Task 4.1: Complete kind.go with ServiceOpts + DefaultChannelDef + Kind descriptor

**Files:**
- Modify: `pkg/services/chat/kind.go`

- [ ] **Step 1: Replace the minimal kind.go from Task 3.2 with the full version**

`pkg/services/chat/kind.go`:

```go
package chat

import (
	"embed"
	"io/fs"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zenion/mmokit/pkg/service"
	"github.com/zenion/mmokit/pkg/services/auth"
)

const KindName = "chat"

//go:embed postgres/migrations/*.sql
var pgMigrations embed.FS

func MigrationsFS() fs.FS {
	sub, err := fs.Sub(pgMigrations, "postgres/migrations")
	if err != nil {
		panic(err)
	}
	return sub
}

// DefaultChannelDef declares one channel that the chat service should
// UPSERT at startup. Idempotent: existing rows with the same slug are
// reconciled to the declared values (kind transition is rejected as
// an error logged at warn level).
type DefaultChannelDef struct {
	Slug            string
	Kind            ChannelKind // SYSTEM_ALL or SYSTEM_PREDICATE
	Topic           string
	SlowModeSeconds int
}

// ServiceOpts configures the chat service.
type ServiceOpts struct {
	// Repository is an injected Repository (e.g. an in-memory mock).
	// When non-nil, RepositoryFactory is ignored and RequiresDB is false.
	Repository Repository

	// RepositoryFactory builds the live Repository from a pgx pool.
	// Set by mmokit.RegisterChatService to chat/postgres.New so
	// pkg/services/chat doesn't import pkg/services/chat/postgres.
	RepositoryFactory func(*pgxpool.Pool) Repository

	// AuthRepoProvider returns the live auth.Repository (used for
	// HasCapability calls + UserIDByUsername lookups). Wired by the
	// mmokit facade after auth.OnReady fires.
	AuthRepoProvider func() auth.Repository

	// OnReady fires from Service.Init after the live Repository has
	// resolved and bootstrap has finished. Used by mmokit.RegisterChatService
	// to wire the gateway hook + capture the live *Service for console
	// command handlers.
	OnReady func(svc *Service)

	// Capacity / policy knobs
	UserRateMax                int
	UserRateWindow             time.Duration
	DefaultSlowMode            time.Duration
	MaxMessageLen              int
	MaxTopicLen                int
	MaxChannelSlugLen          int
	MaxChannelsPerUser         int
	MaxMembersPerCustomChannel int
	MinChannelPasswordLen      int
	MuteReapInterval           time.Duration
	MsgIDTTL                   time.Duration
	ReservedSlugPrefixes       []string

	// Channels created at startup; idempotent UPSERT.
	DefaultChannels []DefaultChannelDef
}

func DefaultServiceOpts() ServiceOpts {
	return ServiceOpts{
		UserRateMax:                5,
		UserRateWindow:             5 * time.Second,
		DefaultSlowMode:            0,
		MaxMessageLen:              500,
		MaxTopicLen:                200,
		MaxChannelSlugLen:          32,
		MaxChannelsPerUser:         5,
		MaxMembersPerCustomChannel: 1000,
		MinChannelPasswordLen:      4,
		MuteReapInterval:           time.Minute,
		MsgIDTTL:                   5 * time.Minute,
		ReservedSlugPrefixes:       []string{"guild:", "party:", "alliance:", "raid:", "system:"},
	}
}

// Kind returns the service.Kind descriptor for the chat service.
//
// OpCodes is nil — chat ops live on the typed-op channel
// (mmokit.RegisterOp[Req, Res]) and route by request typeID, not opcode.
func Kind(opts ServiceOpts) service.Kind {
	return service.Kind{
		Name:        KindName,
		OpCodes:     nil,
		Factory:     func(ctx *service.Context) service.Service { return newService(ctx, opts) },
		RequiresDB:  opts.Repository == nil,
		Description: "engine-tier chat service: pure-pubsub messaging, persisted channels/members/mutes, capability-gated moderation",
	}
}
```

- [ ] **Step 2: Verify build**

```bash
go vet ./pkg/services/chat/...
```

Expected: clean (newService is referenced but not defined yet — next task fixes).

If vet errors on `newService`, that's expected — Task 4.2 defines it.

- [ ] **Step 3: Don't commit yet** — wait for Task 4.2 to land newService.

### Task 4.2: Implement Service skeleton with Init/Shutdown/RegisterOps

**Files:**
- Create: `pkg/services/chat/service.go`

- [ ] **Step 1: Write a failing test that constructs the service**

`pkg/services/chat/service_test.go`:

```go
package chat_test

import (
	"context"
	"testing"

	"github.com/zenion/mmokit/pkg/service"
	"github.com/zenion/mmokit/pkg/services/chat"
	"github.com/zenion/mmokit/pkg/services/chat/chattest"
)

func TestService_InitWithMockRepo(t *testing.T) {
	opts := chat.DefaultServiceOpts()
	opts.Repository = chattest.NewMock()
	svc := chat.Kind(opts).Factory(&service.Context{InstanceID: "test"})
	if err := svc.Init(&service.Context{InstanceID: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := svc.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestService_InitBootstrapsDefaultChannels(t *testing.T) {
	repo := chattest.NewMock()
	opts := chat.DefaultServiceOpts()
	opts.Repository = repo
	opts.DefaultChannels = []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll, Topic: "World chat"},
		{Slug: "help",  Kind: chat.ChannelKindSystemAll, Topic: "Help chat"},
	}
	svc := chat.Kind(opts).Factory(&service.Context{InstanceID: "test"})
	if err := svc.Init(&service.Context{InstanceID: "test"}); err != nil { t.Fatal(err) }
	defer svc.Shutdown(context.Background())

	all, _ := repo.ListAllChannels(context.Background())
	if len(all) != 2 { t.Fatalf("got %d channels, want 2 from DefaultChannels", len(all)) }
}
```

- [ ] **Step 2: Run test (FAIL)**

```bash
go test ./pkg/services/chat/... -run TestService_ -count=1
```

Expected: FAIL — `undefined: newService` and/or constructor not implementing service.Service.

- [ ] **Step 3: Implement service.go**

`pkg/services/chat/service.go`:

```go
package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/zenion/mmokit/pkg/ops"
	"github.com/zenion/mmokit/pkg/service"
	"github.com/zenion/mmokit/pkg/services/auth"
)

const logCat = "services:chat"

// Service is the running chat service instance.
type Service struct {
	ctx      *service.Context
	opts     ServiceOpts
	repo     Repository
	authRepo auth.Repository
	authCap  *auth.CapabilityCache

	mu          sync.RWMutex
	channels    map[uuid.UUID]Channel
	bySlug      map[string]uuid.UUID
	membership  map[uuid.UUID]map[uuid.UUID]string // channelID → userID → role
	userChans   map[uuid.UUID]map[uuid.UUID]struct{}
	mutes       map[muteKey]Mute
	online      map[uuid.UUID]uint32                 // userID → connID
	connIndex   map[uint32]uuid.UUID                 // connID → userID
	gatewayConn map[string]map[uint32]struct{}       // gatewayID → connIDs
	subs        map[uuid.UUID]map[uint32]struct{}    // channelID → connIDs

	rateBuckets map[uuid.UUID]*tokenBucket
	slowMode    map[slowModeKey]time.Time

	msgIDIndex *msgIDTTLMap

	reapCh chan struct{}
	reapWG sync.WaitGroup
}

type muteKey struct{ UserID, ChannelID uuid.UUID }
type slowModeKey struct{ ChannelID, UserID uuid.UUID }

func newService(ctx *service.Context, opts ServiceOpts) service.Service {
	return &Service{ctx: ctx, opts: opts}
}

// Repository returns the live Repository for the mmokit facade's console wiring.
func (s *Service) Repository() Repository { return s.repo }

func (s *Service) Init(ctx *service.Context) error {
	s.ctx = ctx
	if s.opts.Repository != nil {
		s.repo = s.opts.Repository
	} else {
		if ctx.DB == nil {
			return errors.New("chat.Init: DB required (RequiresDB=true should have caught this)")
		}
		if s.opts.RepositoryFactory == nil {
			return errors.New("chat.Init: RepositoryFactory must be set when no Repository is injected")
		}
		s.repo = s.opts.RepositoryFactory(ctx.DB.Pool())
	}

	// Auth repo for capability checks. Optional in tests; required in production.
	if s.opts.AuthRepoProvider != nil {
		s.authRepo = s.opts.AuthRepoProvider()
		if s.authRepo != nil {
			s.authCap = auth.NewCapabilityCache(s.authRepo, 30*time.Second)
		}
	}

	// Initialize in-memory maps
	s.channels = map[uuid.UUID]Channel{}
	s.bySlug = map[string]uuid.UUID{}
	s.membership = map[uuid.UUID]map[uuid.UUID]string{}
	s.userChans = map[uuid.UUID]map[uuid.UUID]struct{}{}
	s.mutes = map[muteKey]Mute{}
	s.online = map[uuid.UUID]uint32{}
	s.connIndex = map[uint32]uuid.UUID{}
	s.gatewayConn = map[string]map[uint32]struct{}{}
	s.subs = map[uuid.UUID]map[uint32]struct{}{}
	s.rateBuckets = map[uuid.UUID]*tokenBucket{}
	s.slowMode = map[slowModeKey]time.Time{}
	s.msgIDIndex = newMsgIDTTLMap(s.opts.MsgIDTTL)

	// 1. Bootstrap DefaultChannels via UPSERT (idempotent)
	if err := s.bootstrapDefaultChannels(context.Background()); err != nil {
		return fmt.Errorf("bootstrap default channels: %w", err)
	}

	// 2. Hydrate channels
	chans, err := s.repo.ListAllChannels(context.Background())
	if err != nil {
		return fmt.Errorf("hydrate channels: %w", err)
	}
	for _, c := range chans {
		s.channels[c.ChannelID] = c
		s.bySlug[c.Slug] = c.ChannelID
		s.subs[c.ChannelID] = map[uint32]struct{}{}
		if c.Kind != "system_all" {
			s.membership[c.ChannelID] = map[uuid.UUID]string{}
		}
	}

	// 3. Hydrate members (load all rows; ban check is per-op at runtime)
	mems, err := s.repo.ListAllMembers(context.Background())
	if err != nil {
		return fmt.Errorf("hydrate members: %w", err)
	}
	for _, m := range mems {
		if s.membership[m.ChannelID] == nil { continue } // SYSTEM_ALL doesn't track explicit members
		s.membership[m.ChannelID][m.UserID] = m.Role
		if s.userChans[m.UserID] == nil {
			s.userChans[m.UserID] = map[uuid.UUID]struct{}{}
		}
		s.userChans[m.UserID][m.ChannelID] = struct{}{}
	}

	// 4. Hydrate mutes
	mutes, err := s.repo.ListActiveMutes(context.Background())
	if err != nil {
		return fmt.Errorf("hydrate mutes: %w", err)
	}
	for _, mu := range mutes {
		s.mutes[muteKey{mu.UserID, mu.ChannelID}] = mu
	}

	// 5. Start reaper goroutine
	s.reapCh = make(chan struct{})
	s.reapWG.Add(1)
	go s.reapLoop()

	// 6. Register typed server-event types so the codec knows them
	RegisterChatServerEvents()

	if s.opts.OnReady != nil {
		s.opts.OnReady(s)
	}
	ctx.Logger.Log(logCat, "chat service initialized: instance=%s channels=%d members=%d mutes=%d",
		ctx.InstanceID, len(s.channels), len(mems), len(s.mutes))
	return nil
}

func (s *Service) bootstrapDefaultChannels(ctx context.Context) error {
	for _, def := range s.opts.DefaultChannels {
		kindStr := channelKindToString(def.Kind)
		if kindStr == "" {
			s.ctx.Logger.Log(logCat, "bootstrap: skipping invalid kind for slug=%s", def.Slug)
			continue
		}
		existing, err := s.repo.GetChannelBySlug(ctx, def.Slug)
		if err != nil && !errors.Is(err, ErrChannelNotFound) {
			return err
		}
		if err == nil && existing.Kind != kindStr {
			s.ctx.Logger.Log(logCat, "bootstrap: ERROR slug=%s kind transition %s→%s rejected; delete + recreate",
				def.Slug, existing.Kind, kindStr)
			continue
		}
		c := Channel{
			Slug: def.Slug, Kind: kindStr,
			Topic: def.Topic, SlowModeSeconds: def.SlowModeSeconds,
		}
		if existing.ChannelID != uuid.Nil { c.ChannelID = existing.ChannelID }
		if _, err := s.repo.UpsertChannel(ctx, c); err != nil {
			return fmt.Errorf("upsert %s: %w", def.Slug, err)
		}
	}
	return nil
}

func channelKindToString(k ChannelKind) string {
	switch k {
	case ChannelKindSystemAll:        return "system_all"
	case ChannelKindSystemPredicate:  return "system_predicate"
	case ChannelKindCustom:           return "custom"
	default:                          return ""
	}
}

func channelKindFromString(s string) ChannelKind {
	switch strings.ToLower(s) {
	case "system_all":       return ChannelKindSystemAll
	case "system_predicate": return ChannelKindSystemPredicate
	case "custom":           return ChannelKindCustom
	default:                 return ChannelKindUnspecified
	}
}

// RegisterOps is the service.Service hook. Chat handlers live on the
// typed-op channel; the mmokit facade registers them against the typed-op
// router rather than the per-kind opcode router. Returning nil here is
// intentional — Kind.OpCodes is also nil.
func (s *Service) RegisterOps(_ *ops.Router) error { return nil }

func (s *Service) Shutdown(ctx context.Context) error {
	if s.reapCh != nil { close(s.reapCh) }
	done := make(chan struct{})
	go func() { s.reapWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
	}
	if s.ctx != nil && s.ctx.Logger != nil {
		s.ctx.Logger.Log(logCat, "chat service shutdown: instance=%s", s.ctx.InstanceID)
	}
	return nil
}

func (s *Service) reapLoop() {
	defer s.reapWG.Done()
	t := time.NewTicker(s.opts.MuteReapInterval)
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
	if n, err := s.repo.DeleteExpiredMutes(ctx); err == nil && n > 0 {
		s.ctx.Logger.Log(logCat, "reaper: removed %d expired mutes", n)
	}
	if n, err := s.repo.ClearExpiredBans(ctx); err == nil && n > 0 {
		s.ctx.Logger.Log(logCat, "reaper: cleared %d expired bans", n)
	}
	if n := s.msgIDIndex.Sweep(time.Now()); n > 0 {
		s.ctx.Logger.Log(logCat, "reaper: evicted %d expired msg_id index entries", n)
	}
	// In-memory mute eviction
	s.mu.Lock()
	now := time.Now()
	for k, v := range s.mutes {
		if v.ExpiresAt.Before(now) { delete(s.mutes, k) }
	}
	s.mu.Unlock()
}

var _ service.Service = (*Service)(nil)
```

The test references `msgIDTTLMap` (next task) and `tokenBucket` (Phase 9). Add stub files now so this compiles:

`pkg/services/chat/msgindex.go` (stub — full impl in later task):

```go
package chat

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

type msgIDTTLMap struct {
	ttl   time.Duration
	mu    sync.Mutex
	items map[string]msgIDEntry
}

type msgIDEntry struct {
	ChannelID uuid.UUID
	ExpiresAt time.Time
}

func newMsgIDTTLMap(ttl time.Duration) *msgIDTTLMap {
	return &msgIDTTLMap{ttl: ttl, items: map[string]msgIDEntry{}}
}

func (m *msgIDTTLMap) Put(msgID string, channelID uuid.UUID) {
	m.mu.Lock(); defer m.mu.Unlock()
	m.items[msgID] = msgIDEntry{ChannelID: channelID, ExpiresAt: time.Now().Add(m.ttl)}
}

func (m *msgIDTTLMap) Get(msgID string) (uuid.UUID, bool) {
	m.mu.Lock(); defer m.mu.Unlock()
	e, ok := m.items[msgID]
	if !ok || time.Now().After(e.ExpiresAt) {
		return uuid.Nil, false
	}
	return e.ChannelID, true
}

func (m *msgIDTTLMap) Sweep(now time.Time) int {
	m.mu.Lock(); defer m.mu.Unlock()
	n := 0
	for k, e := range m.items {
		if now.After(e.ExpiresAt) { delete(m.items, k); n++ }
	}
	return n
}
```

`pkg/services/chat/ratelimit.go` (stub — full impl in Phase 9):

```go
package chat

import "time"

type tokenBucket struct {
	tokens    int
	lastFill  time.Time
	capacity  int
	refillDur time.Duration
}
```

- [ ] **Step 4: Run test (PASS)**

```bash
go test ./pkg/services/chat/... -run TestService_ -count=1
```

Expected: PASS.

- [ ] **Step 5: Run vet across the package**

```bash
go vet ./pkg/services/chat/...
```

Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add pkg/services/chat/kind.go \
        pkg/services/chat/service.go \
        pkg/services/chat/service_test.go \
        pkg/services/chat/msgindex.go \
        pkg/services/chat/ratelimit.go
git commit -m "feat(chat): Service skeleton with Init/Shutdown/bootstrap + msgIDTTL stub"
```

---

## Phase 5 — First vertical slice: SEND_MESSAGE on SYSTEM_ALL

The goal of this phase is one working flow: a connected user sends a `ChatSendRequest` for `world`, every other connected user receives a `ChatMessageEvent`. No moderation, no rate limit, no auth checks — just the plumbing.

### Task 5.1: Add fanout primitive

**Files:**
- Create: `pkg/services/chat/fanout.go`

- [ ] **Step 1: Write a failing test**

`pkg/services/chat/fanout_test.go`:

```go
package chat_test

import (
	"testing"

	"github.com/zenion/mmokit/pkg/services/chat"
)

func TestFanout_RecordsRecipientsForSystemAll(t *testing.T) {
	// We assert at the abstract level: given a service with a SYSTEM_ALL
	// channel and 3 online users, fanoutEvent visits all 3 connIDs.
	// (This is a *unit* test against an exported FanoutTargets helper —
	// the actual send call is wired to the gateway in integration tests.)
	svc := chat.NewTestService(t, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll, Topic: ""},
	})
	chid := svc.MustChannelID("world")
	svc.MustOnlineFakeUser(101, "alice")
	svc.MustOnlineFakeUser(102, "bob")
	svc.MustOnlineFakeUser(103, "carol")

	conns := svc.FanoutTargets(chid)
	if len(conns) != 3 {
		t.Fatalf("expected 3 conn targets, got %d", len(conns))
	}
}
```

- [ ] **Step 2: Run test (FAIL)**

```bash
go test ./pkg/services/chat/... -run TestFanout_ -count=1
```

Expected: FAIL — `undefined: chat.NewTestService`.

- [ ] **Step 3: Implement test helpers + FanoutTargets**

`pkg/services/chat/testhelpers_test.go` (test-only helpers in a `_test.go` file so they don't ship):

Actually, since the test references `chat.NewTestService` in package `chat_test`, the helpers need to be exported from package `chat` (not `chat_test`). Use a build-tag-free file with a `// +build` test-only constraint OR just expose them as plain methods. Cleanest: put them in a `chat` package file gated by a test-friendly comment but exported.

Create `pkg/services/chat/testing.go`:

```go
package chat

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/zenion/mmokit/pkg/logger"
	"github.com/zenion/mmokit/pkg/service"
	"github.com/zenion/mmokit/pkg/services/chat/chattest"
)

// TestService wraps *Service with helpers for unit tests. Not part of
// the production public API; available because tests in chat_test live
// in a different package and need exported entry points.
type TestService struct{ *Service }

// NewTestService builds a chat Service against an in-memory repo +
// the supplied default channels. Caller does not need to call Shutdown.
func NewTestService(t *testing.T, defaults []DefaultChannelDef) *TestService {
	t.Helper()
	opts := DefaultServiceOpts()
	opts.Repository = chattest.NewMock()
	opts.DefaultChannels = defaults
	svc := &Service{ctx: &service.Context{InstanceID: "test", Logger: logger.NewNoop()}, opts: opts}
	if err := svc.Init(svc.ctx); err != nil {
		t.Fatalf("svc.Init: %v", err)
	}
	t.Cleanup(func() { _ = svc.Shutdown(context.Background()) })
	return &TestService{Service: svc}
}

// MustChannelID resolves a slug or fails the test.
func (t *TestService) MustChannelID(slug string) uuid.UUID {
	t.mu.RLock(); defer t.mu.RUnlock()
	id, ok := t.bySlug[slug]
	if !ok { panic("test: unknown slug " + slug) }
	return id
}

// MustOnlineFakeUser inserts a fake user into the presence + subs maps.
// Used by tests that don't go through ChatSessionEnter.
func (t *TestService) MustOnlineFakeUser(connID uint32, _ string) uuid.UUID {
	uid := uuid.New()
	t.mu.Lock(); defer t.mu.Unlock()
	t.online[uid] = connID
	t.connIndex[connID] = uid
	// SYSTEM_ALL channels: presence in `online[]` is sufficient; explicit
	// subs[] entries are only used for non-SYSTEM_ALL channels.
	return uid
}

// FanoutTargets returns the connIDs that would receive a fanout to channelID.
func (t *TestService) FanoutTargets(channelID uuid.UUID) []uint32 {
	t.mu.RLock(); defer t.mu.RUnlock()
	c, ok := t.channels[channelID]
	if !ok { return nil }
	if c.Kind == "system_all" {
		out := make([]uint32, 0, len(t.online))
		for _, c := range t.online { out = append(out, c) }
		return out
	}
	subs := t.subs[channelID]
	out := make([]uint32, 0, len(subs))
	for c := range subs { out = append(out, c) }
	return out
}
```

If `pkg/logger` doesn't have a `NewNoop()` constructor, look for the equivalent (`logger.New()` with a nil io.Writer, or a similar helper); follow whatever the codebase uses for tests today.

`pkg/services/chat/fanout.go` (production primitive):

```go
package chat

import (
	"github.com/google/uuid"
)

// fanoutEvent dispatches a typed server event to every connection
// subscribed to channelID. SYSTEM_ALL walks the online[] map directly;
// other channels walk subs[channelID].
//
// The actual send is performed by sendEventFn — wired by the mmokit
// facade to the gateway's per-conn typed-event sender. In unit tests
// it can be replaced with a recorder.
func (s *Service) fanoutEvent(channelID uuid.UUID, event any) {
	s.mu.RLock()
	c, ok := s.channels[channelID]
	if !ok { s.mu.RUnlock(); return }
	var conns []uint32
	if c.Kind == "system_all" {
		conns = make([]uint32, 0, len(s.online))
		for _, cid := range s.online { conns = append(conns, cid) }
	} else {
		subs := s.subs[channelID]
		conns = make([]uint32, 0, len(subs))
		for cid := range subs { conns = append(conns, cid) }
	}
	send := s.sendEventFn
	s.mu.RUnlock()
	if send == nil { return } // unit-test path — no gateway wired
	for _, cid := range conns {
		send(cid, event)
	}
}

// fanoutToOne sends an event to a single connection.
func (s *Service) fanoutToOne(connID uint32, event any) {
	s.mu.RLock()
	send := s.sendEventFn
	s.mu.RUnlock()
	if send == nil { return }
	send(connID, event)
}
```

Add to `service.go` Service struct:

```go
	// sendEventFn is set by the mmokit facade to the gateway's per-conn
	// typed-event sender (e.g. process.SendTypedEvent). Lifted from the
	// gateway after Init via OnReady. nil in unit tests.
	sendEventFn func(connID uint32, event any)
```

And expose a setter for the facade:

```go
// SetSendEventFn wires the per-conn typed-event sender. Called by
// mmokit.RegisterChatService from OnReady.
func (s *Service) SetSendEventFn(fn func(connID uint32, event any)) {
	s.mu.Lock(); defer s.mu.Unlock()
	s.sendEventFn = fn
}
```

- [ ] **Step 4: Run test (PASS)**

```bash
go test ./pkg/services/chat/... -run TestFanout_ -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/services/chat/fanout.go pkg/services/chat/testing.go pkg/services/chat/fanout_test.go pkg/services/chat/service.go
git commit -m "feat(chat): fanout primitive + test helpers"
```

### Task 5.2: Implement handleSend (no auth/rate-limit/mute checks yet)

**Files:**
- Create: `pkg/services/chat/handlers.go`
- Create: `pkg/services/chat/handlers_test.go`

- [ ] **Step 1: Write the failing test**

`pkg/services/chat/handlers_test.go`:

```go
package chat_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/zenion/mmokit/pkg/ops"
	"github.com/zenion/mmokit/pkg/services/chat"
)

func TestHandleSend_FansOutToAllOnlineForSystemAll(t *testing.T) {
	svc := chat.NewTestService(t, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll, Topic: ""},
	})
	chid := svc.MustChannelID("world")
	senderID := svc.MustOnlineFakeUser(101, "alice")
	_ = svc.MustOnlineFakeUser(102, "bob")
	_ = svc.MustOnlineFakeUser(103, "carol")

	var recv []recordedEvent
	svc.SetSendEventFn(func(connID uint32, ev any) {
		recv = append(recv, recordedEvent{ConnID: connID, Event: ev})
	})

	opCtx := &ops.OpContext{ConnID: 101, UserID: senderID.String()}
	resp, err := svc.HandleSend(opCtx, &chat.ChatSendRequest{ChannelID: chid.String(), Body: "hi"})
	if err != nil { t.Fatal(err) }
	if resp.ErrorCode != 0 { t.Fatalf("got error code %d msg=%q", resp.ErrorCode, resp.ErrorMessage) }
	if resp.MsgID == "" { t.Fatal("expected msg_id") }
	if len(recv) != 3 { t.Fatalf("expected 3 fanout sends, got %d", len(recv)) }
	for _, r := range recv {
		ev, ok := r.Event.(*chat.ChatMessageEvent)
		if !ok { t.Fatalf("unexpected event type %T", r.Event) }
		if ev.Body != "hi" || ev.ChannelID != chid.String() {
			t.Fatalf("unexpected event: %#v", ev)
		}
	}
}

type recordedEvent struct {
	ConnID uint32
	Event  any
}

// Smoke that uuid still imports cleanly even if no test references it directly.
var _ = uuid.Nil
var _ = context.TODO
```

- [ ] **Step 2: Run test (FAIL)**

```bash
go test ./pkg/services/chat/... -run TestHandleSend_ -count=1
```

Expected: FAIL — `undefined: svc.HandleSend`.

- [ ] **Step 3: Implement the handler**

`pkg/services/chat/handlers.go`:

```go
package chat

import (
	"github.com/google/uuid"

	"github.com/zenion/mmokit/pkg/ops"
)

// HandleSend handles CHAT_OP_SEND_MESSAGE. Fans out a ChatMessageEvent
// to every member of the channel.
//
// v1 first-cut: no rate limit, no mute, no ban check, no auth gate —
// those land in subsequent tasks. Validation is just (channel exists,
// payload size).
func (s *Service) HandleSend(opCtx *ops.OpContext, req *ChatSendRequest) (*ChatSendResponse, error) {
	if len(req.Body) > s.opts.MaxMessageLen {
		return errResp[ChatSendResponse](ChatErrorPayloadTooLarge, "body exceeds max length", 0)
	}
	chID, err := uuid.Parse(req.ChannelID)
	if err != nil {
		return errResp[ChatSendResponse](ChatErrorChannelNotFound, "invalid channel_id", 0)
	}
	s.mu.RLock()
	c, ok := s.channels[chID]
	if !ok {
		s.mu.RUnlock()
		return errResp[ChatSendResponse](ChatErrorChannelNotFound, "channel not found", 0)
	}
	senderID, _ := uuid.Parse(opCtx.UserID)
	senderUsername := s.usernameForUserLocked(senderID)
	s.mu.RUnlock()

	msgID, err := uuid.NewV7()
	if err != nil {
		return errResp[ChatSendResponse](ChatErrorInternal, "msg_id: "+err.Error(), 0)
	}
	s.msgIDIndex.Put(msgID.String(), chID)

	now := timeNowMs()
	ev := &ChatMessageEvent{
		MsgID:          msgID.String(),
		ChannelID:      chID.String(),
		SenderUserID:   senderID.String(),
		SenderUsername: senderUsername,
		Body:           req.Body,
		SentAtMs:       now,
	}
	s.fanoutEvent(chID, ev)
	_ = c // future: per-channel logging

	return &ChatSendResponse{MsgID: msgID.String(), SentAtMs: now}, nil
}

// usernameForUserLocked returns the in-memory cached username for a
// user (snapshotted at session-enter). Returns empty string when not
// online — callers can fall back to a repo lookup if needed.
//
// Caller MUST hold s.mu (R or W).
func (s *Service) usernameForUserLocked(userID uuid.UUID) string {
	// usernames map is added when ChatSessionEnter handler lands; for now
	// fall through to empty string (test fakes set it via session-enter).
	return s.usernames[userID]
}

// errResp builds an error response of type R with the given fields. The
// generic helper avoids repeating the boilerplate `&ChatXResponse{ErrorBlock:...}`
// for every handler.
func errResp[R any](code ChatError, msg string, retryAfterMs int64) (*R, error) {
	r := new(R)
	// Set ErrorBlock via reflection-free type assertion — every Response
	// embeds ErrorBlock, so we do a small switch:
	if eb, ok := any(r).(interface {
		setErrorBlock(uint32, string, int64)
	}); ok {
		eb.setErrorBlock(uint32(code), msg, retryAfterMs)
	} else {
		// Defensive fallback: we can't reach here for any registered Response
		// because every one embeds ErrorBlock. Panicking here surfaces wiring
		// bugs early.
		panic("chat: response type does not embed ErrorBlock")
	}
	return r, nil
}
```

This breaks because the Go switch above doesn't work without explicit `setErrorBlock` methods. Alternative: use unsafe reflection, or add a `setErrorBlock` method to each Response type via a generated `errorblock.go` file, or use a different approach entirely.

**Recommended simplification:** define `errResp` per-response-type inline, OR use reflection. Use reflection:

```go
import "reflect"

func errResp[R any](code ChatError, msg string, retryAfterMs int64) (*R, error) {
	r := new(R)
	v := reflect.ValueOf(r).Elem()
	eb := v.FieldByName("ErrorBlock")
	if !eb.IsValid() {
		panic("chat: response type missing embedded ErrorBlock field")
	}
	eb.FieldByName("ErrorCode").SetUint(uint64(code))
	eb.FieldByName("ErrorMessage").SetString(msg)
	eb.FieldByName("RetryAfterMs").SetInt(retryAfterMs)
	return r, nil
}
```

This is a once-per-handler call so the reflection cost is fine.

Add `usernames map[uuid.UUID]string` to the Service struct + initialize it in Init alongside the other maps:

```go
	s.usernames = map[uuid.UUID]string{}
```

In MustOnlineFakeUser (testing.go), populate it:

```go
	t.usernames[uid] = "fakeuser"
```

`timeNowMs` helper — add to a new `pkg/services/chat/time.go`:

```go
package chat

import "time"

func timeNowMs() int64 { return time.Now().UnixMilli() }
```

- [ ] **Step 4: Run test (PASS)**

```bash
go test ./pkg/services/chat/... -run TestHandleSend_ -count=1
```

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/services/chat/handlers.go pkg/services/chat/handlers_test.go pkg/services/chat/service.go pkg/services/chat/testing.go pkg/services/chat/time.go
git commit -m "feat(chat): handleSend (SYSTEM_ALL fanout, no checks yet)"
```

### Task 5.3: Implement ChatSessionEnter / ChatSessionLeave handlers

**Files:**
- Modify: `pkg/services/chat/handlers.go`
- Modify: `pkg/services/chat/handlers_test.go`

- [ ] **Step 1: Write a failing test**

Append to `handlers_test.go`:

```go
func TestHandleSessionEnter_AddsToOnlineAndSendsHydration(t *testing.T) {
	svc := chat.NewTestService(t, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll, Topic: "World"},
		{Slug: "help",  Kind: chat.ChannelKindSystemAll, Topic: "Help"},
	})

	var recv []recordedEvent
	svc.SetSendEventFn(func(connID uint32, ev any) {
		recv = append(recv, recordedEvent{ConnID: connID, Event: ev})
	})

	resp, err := svc.HandleSessionEnter(nil, &chat.ChatSessionEnterRequest{
		ConnID: 200, UserID: uuid.NewString(), Username: "alice", GatewayID: "gw-a",
	})
	if err != nil { t.Fatal(err) }
	if resp.ErrorCode != 0 { t.Fatalf("err: %s", resp.ErrorMessage) }
	if got := svc.OnlineCount(); got != 1 { t.Fatalf("online=%d, want 1", got) }
	if len(recv) != 1 { t.Fatalf("expected 1 hydration event, got %d", len(recv)) }
	if _, ok := recv[0].Event.(*chat.ChatChannelsHydratedEvent); !ok {
		t.Fatalf("expected ChatChannelsHydratedEvent, got %T", recv[0].Event)
	}
}

func TestHandleSessionLeave_RemovesFromOnline(t *testing.T) {
	svc := chat.NewTestService(t, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll, Topic: ""},
	})
	uid := uuid.NewString()
	_, _ = svc.HandleSessionEnter(nil, &chat.ChatSessionEnterRequest{ConnID: 200, UserID: uid, Username: "alice", GatewayID: "gw-a"})
	resp, err := svc.HandleSessionLeave(nil, &chat.ChatSessionLeaveRequest{ConnID: 200, GatewayID: "gw-a"})
	if err != nil { t.Fatal(err) }
	if resp.ErrorCode != 0 { t.Fatalf("err: %s", resp.ErrorMessage) }
	if got := svc.OnlineCount(); got != 0 { t.Fatalf("online=%d, want 0", got) }
}
```

Add `OnlineCount` exported helper to testing.go:

```go
func (t *TestService) OnlineCount() int {
	t.mu.RLock(); defer t.mu.RUnlock()
	return len(t.online)
}
```

- [ ] **Step 2: Run test (FAIL)**

```bash
go test ./pkg/services/chat/... -run TestHandleSession -count=1
```

Expected: FAIL — `undefined: svc.HandleSessionEnter` / `HandleSessionLeave`.

- [ ] **Step 3: Implement the handlers**

Append to `handlers.go`:

```go
// HandleSessionEnter is invoked by the gateway after successful auth.
// Builds presence + subscription state for the connID; sends back a
// ChatChannelsHydratedEvent so the client knows its full channel list.
//
// opCtx may be nil — this op is invoked in-process from the gateway,
// not through the typed-op router for client traffic. Capability is
// implicit (in-process bypass per spec §15.6).
func (s *Service) HandleSessionEnter(_ *ops.OpContext, req *ChatSessionEnterRequest) (*ChatSessionEnterResponse, error) {
	uid, err := uuid.Parse(req.UserID)
	if err != nil {
		return errResp[ChatSessionEnterResponse](ChatErrorInternal, "invalid user_id", 0)
	}
	s.mu.Lock()
	s.online[uid] = req.ConnID
	s.connIndex[req.ConnID] = uid
	if s.usernames == nil { s.usernames = map[uuid.UUID]string{} }
	s.usernames[uid] = req.Username
	if req.GatewayID != "" {
		if s.gatewayConn[req.GatewayID] == nil {
			s.gatewayConn[req.GatewayID] = map[uint32]struct{}{}
		}
		s.gatewayConn[req.GatewayID][req.ConnID] = struct{}{}
	}
	// Subscribe connID to every explicit-membership channel the user belongs to
	if userChs := s.userChans[uid]; userChs != nil {
		for chid := range userChs {
			if s.subs[chid] == nil { s.subs[chid] = map[uint32]struct{}{} }
			s.subs[chid][req.ConnID] = struct{}{}
		}
	}
	// Build hydration payload: explicit-membership channels + every SYSTEM_ALL
	hydration := make([]ChannelInfo, 0, len(s.channels))
	for _, c := range s.channels {
		if c.Kind == "system_all" || s.membership[c.ChannelID][uid] != "" {
			hydration = append(hydration, channelInfoOfLocked(c, len(s.membership[c.ChannelID])))
		}
	}
	s.mu.Unlock()

	// Fanout SE_CHAT_MEMBER_JOINED to other members of explicit channels
	// (deferred to membership-aware tests in Phase 7; simple single-conn
	// hydration alone is sufficient for v1 SYSTEM_ALL).

	s.fanoutToOne(req.ConnID, &ChatChannelsHydratedEvent{Channels: hydration})
	return &ChatSessionEnterResponse{}, nil
}

func (s *Service) HandleSessionLeave(_ *ops.OpContext, req *ChatSessionLeaveRequest) (*ChatSessionLeaveResponse, error) {
	s.mu.Lock()
	uid, ok := s.connIndex[req.ConnID]
	if !ok {
		s.mu.Unlock()
		return &ChatSessionLeaveResponse{}, nil // idempotent
	}
	delete(s.connIndex, req.ConnID)
	delete(s.online, uid)
	for chid, members := range s.subs {
		if _, present := members[req.ConnID]; present {
			delete(members, req.ConnID)
			// fanout MEMBER_LEFT to remaining members of explicit channels (Phase 7)
			_ = chid
		}
	}
	if g := s.gatewayConn[req.GatewayID]; g != nil {
		delete(g, req.ConnID)
	}
	s.mu.Unlock()
	return &ChatSessionLeaveResponse{}, nil
}

// channelInfoOfLocked builds a ChannelInfo for the wire from an internal
// Channel row + memberCount. Caller MUST hold s.mu (R or W).
func channelInfoOfLocked(c Channel, memberCount int) ChannelInfo {
	owner := ""
	if c.OwnerUserID != uuid.Nil { owner = c.OwnerUserID.String() }
	return ChannelInfo{
		ChannelID:       c.ChannelID.String(),
		Slug:            c.Slug,
		Kind:            channelKindFromString(c.Kind),
		Topic:           c.Topic,
		SlowModeSeconds: int32(c.SlowModeSeconds),
		OwnerUserID:     owner,
		MemberCount:     int32(memberCount),
		HasPassword:     c.PasswordHash != "",
	}
}
```

- [ ] **Step 4: Run tests (PASS)**

```bash
go test ./pkg/services/chat/... -count=1
```

Expected: all PASS.

- [ ] **Step 5: Commit**

```bash
git add pkg/services/chat/handlers.go pkg/services/chat/handlers_test.go pkg/services/chat/testing.go
git commit -m "feat(chat): HandleSessionEnter/Leave + hydration event"
```

---

## Phase 6 — Custom channels (CREATE/JOIN/LEAVE) + DMs

Pattern for every handler in this phase:
1. Write a `TestHandleX_*` table-style test driving via `*TestService`.
2. Run (FAIL).
3. Implement the handler in `handlers.go`.
4. Run (PASS).
5. Commit.

Handlers added in this phase. Each lands as its own task following the pattern above.

### Task 6.1: HandleCreate

**Behavior:**
- Validate slug: lowercase, `[a-z0-9_-]`, max `MaxChannelSlugLen`, no `:` (reserved for service-prefixed slugs).
- Reject if slug starts with any `ReservedSlugPrefixes` entry → `ChatErrorReservedSlug`.
- Enforce `MaxChannelsPerUser` for caller (count of existing custom channels with `owner_user_id == caller`) → `ChatErrorMaxChannelsReached`.
- Validate password length if non-empty → `ChatErrorInvalidPassword` if shorter than `MinChannelPasswordLen`.
- Hash password with chat's lighter argon2id params (Phase 9 wires the helper; for now defer to `password.HashChannelPassword`).
- `repo.UpsertChannel` with `Kind=custom`, `OwnerUserID=caller`, `Topic=req.Topic`.
- `repo.AddOrUpdateMember` for caller with `role=admin`.
- Update in-memory: `channels`, `bySlug`, `membership`, `userChans`, `subs[chid][callerConnID]`.
- Fanout `ChatMemberJoinedEvent` to no-one (creator alone).
- Return `ChannelInfo`.

Test cases: happy path; reserved slug; over-cap; bad password length; idempotent slug-collision.

### Task 6.2: HandleJoin

**Behavior:**
- Look up channel by `slug`.
- Reject if `Kind != custom` → `ChatErrorPermissionDenied`.
- Verify password if `password_hash` non-empty (`password.VerifyChannelPassword`).
- Reject if user is currently banned in that channel (`banned_until > NOW`) → `ChatErrorBanned`.
- Enforce `MaxMembersPerCustomChannel` → `ChatErrorMaxMembersReached`.
- `repo.AddOrUpdateMember` with `role=member`.
- Update in-memory: `membership`, `userChans`, `subs[chid][connID]`.
- Fanout `ChatMemberJoinedEvent` to other members.
- Return `ChannelInfo`.

### Task 6.3: HandleLeave

**Behavior:**
- Reject if not a member → `ChatErrorNotAMember`.
- Reject if Kind == SYSTEM_ALL → `ChatErrorPermissionDenied` (per spec §15.3 — system channels mandatory in v1).
- `repo.RemoveMember`.
- Update in-memory: drop from `membership`, `userChans`, `subs`.
- Fanout `ChatMemberLeftEvent` to other members.

### Task 6.4: HandleSendDM

**Behavior:**
- Validate body length.
- Look up `recipient_user_id` in online map. If not online: return `ChatErrorRecipientOffline` BUT still set `ErrorCode=0` (success-with-flag) — the response carries `ErrorCode = 13` (RecipientOffline) as informational; a client UI can show "delivered when online" but per v1 spec we DROP. Re-read spec: "drop silently" — so don't return an error code; just don't fan out. The msg_id is still allocated and returned for symmetry.

   Actually correct v1 behavior per spec §8 Send-DM: "if recipient offline: drop silently". Return success with the msg_id; the sender sees their own DM but recipient drops.

- `msg_id := uuid.NewV7()`.
- Send `ChatDMEvent` to recipient (if online) AND to sender (echo so multi-window sees own sends).
- Return `ChatSendDMResponse{MsgID, SentAtMs}`.

### Task 6.5: Tests for the four ops

`pkg/services/chat/handlers_dm_test.go`:

```go
package chat_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/zenion/mmokit/pkg/ops"
	"github.com/zenion/mmokit/pkg/services/chat"
)

func TestHandleSendDM_DeliversToOnlineRecipientAndEchoesToSender(t *testing.T) {
	svc := chat.NewTestService(t, nil)
	alice := svc.MustOnlineFakeUser(101, "alice")
	bob := svc.MustOnlineFakeUser(102, "bob")

	var sent []recordedEvent
	svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

	resp, err := svc.HandleSendDM(&ops.OpContext{ConnID: 101, UserID: alice.String()},
		&chat.ChatSendDMRequest{RecipientUserID: bob.String(), Body: "hi bob"})
	if err != nil { t.Fatal(err) }
	if resp.ErrorCode != 0 { t.Fatalf("err: %s", resp.ErrorMessage) }
	if len(sent) != 2 { t.Fatalf("expected 2 sends (recipient + echo), got %d", len(sent)) }
}

func TestHandleSendDM_DropsSilentlyWhenRecipientOffline(t *testing.T) {
	svc := chat.NewTestService(t, nil)
	alice := svc.MustOnlineFakeUser(101, "alice")
	offline := uuid.New()

	var sent []recordedEvent
	svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

	resp, err := svc.HandleSendDM(&ops.OpContext{ConnID: 101, UserID: alice.String()},
		&chat.ChatSendDMRequest{RecipientUserID: offline.String(), Body: "into the void"})
	if err != nil { t.Fatal(err) }
	if resp.ErrorCode != 0 { t.Fatalf("err: %s", resp.ErrorMessage) }
	if len(sent) != 0 { t.Fatalf("expected 0 sends to offline recipient (no echo either since spec drops silent), got %d", len(sent)) }
}
```

(Echo-to-sender behavior matches the spec; the test asserts both events fire when recipient is online.)

Run each test → fail → implement → pass → commit. Commit message format:

```
feat(chat): handleCreate (custom channel registration)
feat(chat): handleJoin (password + ban check)
feat(chat): handleLeave (membership removal + fanout)
feat(chat): handleSendDM (online-only delivery + sender echo)
```

---

## Phase 7 — Membership-mutation ops (capability-gated)

Authorization helper lands first (Task 7.1), then six ops follow.

### Task 7.1: Add canModerate helper

**Files:**
- Create: `pkg/services/chat/authorization.go`

- [ ] **Step 1: Failing test in `pkg/services/chat/authorization_test.go`**

```go
package chat_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/zenion/mmokit/pkg/services/auth"
	"github.com/zenion/mmokit/pkg/services/chat"
)

func TestCanModerate_GlobalAdminAlwaysWins(t *testing.T) {
	svc, repo := chat.NewTestServiceWithAuth(t, nil)
	chid := makeChannelHelper(t, svc, "secret", chat.ChannelKindCustom)
	admin := uuid.New()
	_ = repo.GrantCapability(context.Background(), auth.Capability{
		UserID: admin, Capability: "chat.admin", GrantedBy: admin,
	})
	if !svc.CanModerate(admin, chid) { t.Fatal("global admin should always pass") }
}

func TestCanModerate_ChannelAdminPasses(t *testing.T) {
	svc, _ := chat.NewTestServiceWithAuth(t, nil)
	chid := makeChannelHelper(t, svc, "secret", chat.ChannelKindCustom)
	user := uuid.New()
	svc.MustAddMember(chid, user, "admin")
	if !svc.CanModerate(user, chid) { t.Fatal("channel admin should pass") }
}

func TestCanModerate_PlainMemberFails(t *testing.T) {
	svc, _ := chat.NewTestServiceWithAuth(t, nil)
	chid := makeChannelHelper(t, svc, "secret", chat.ChannelKindCustom)
	user := uuid.New()
	svc.MustAddMember(chid, user, "member")
	if svc.CanModerate(user, chid) { t.Fatal("plain member must NOT pass") }
}
```

Helper `makeChannelHelper`:

```go
func makeChannelHelper(t *testing.T, svc *chat.TestService, slug string, kind chat.ChannelKind) uuid.UUID {
	t.Helper()
	id, err := svc.CreateChannelDirect(slug, kind)
	if err != nil { t.Fatal(err) }
	return id
}
```

Add `NewTestServiceWithAuth`, `CreateChannelDirect`, `MustAddMember`, `CanModerate` to `testing.go`. `NewTestServiceWithAuth` plumbs an `authtest.NewMock()` through `AuthRepoProvider` and returns `(*TestService, auth.Repository)`.

- [ ] **Step 2: Run (FAIL)**

```bash
go test ./pkg/services/chat/... -run TestCanModerate -count=1
```

- [ ] **Step 3: Implement**

`pkg/services/chat/authorization.go`:

```go
package chat

import (
	"context"

	"github.com/google/uuid"
)

// canModerate returns true if userID has either:
//   - the global "chat.admin" capability (cached), or
//   - role == "admin" on the specific channel.
//
// Returns false on any auth-side lookup error (defensive — the right
// thing for an authorization check is to deny on error).
func (s *Service) canModerate(userID, channelID uuid.UUID) bool {
	if s.hasGlobalAdmin(userID) { return true }
	s.mu.RLock(); defer s.mu.RUnlock()
	role := s.membership[channelID][userID]
	return role == "admin"
}

func (s *Service) hasGlobalAdmin(userID uuid.UUID) bool {
	if s.authCap == nil { return false }
	has, err := s.authCap.HasCapability(context.Background(), userID, "chat.admin")
	if err != nil { return false }
	return has
}

// CanModerate is the exported test-facing form of canModerate.
func (s *Service) CanModerate(userID, channelID uuid.UUID) bool {
	return s.canModerate(userID, channelID)
}
```

- [ ] **Step 4: Run (PASS)**

- [ ] **Step 5: Commit** — `feat(chat): canModerate helper (global admin OR channel-admin role)`

### Tasks 7.2–7.7: Membership ops

For each: failing test → impl → pass → commit. Pattern: every handler does

```go
func (s *Service) HandleX(opCtx *ops.OpContext, req *ChatXRequest) (*ChatXResponse, error) {
    chID, _ := uuid.Parse(req.ChannelID)
    callerID, _ := uuid.Parse(opCtx.UserID)
    if !s.canModerate(callerID, chID) {
        return errResp[ChatXResponse](ChatErrorPermissionDenied, "denied", 0)
    }
    // ... actual mutation via repo + in-memory + fanout ...
    return &ChatXResponse{}, nil
}
```

| Task | Op | Repo call | In-memory updates | Fanout event |
|---|---|---|---|---|
| 7.2 | `HandleAddMember` | `AddOrUpdateMember` | `membership`, `userChans`, `subs` (if online) | `ChatMemberJoinedEvent` |
| 7.3 | `HandleRemoveMember` | `RemoveMember` | drop from `membership`, `userChans`, `subs` | `ChatMemberLeftEvent` |
| 7.4 | `HandleBulkSetMembers` | `BulkSetMembers` | wholesale-replace `membership[chid]`, rebuild `subs[chid]` from online intersect | `ChatMemberJoinedEvent` for new + `ChatMemberLeftEvent` for departed |
| 7.5 | `HandleRegisterChannel` | `UpsertChannel` (caller = nil owner; only `chat.admin` allowed) | `channels`, `bySlug`, `subs` | none (no members yet) |
| 7.6 | `HandleUnregisterChannel` | `DeleteChannel` | drop `channels[chid]`, `bySlug`, `membership`, `subs`, plus walk `userChans` | `ChatChannelGoneEvent` to all subscribers |
| 7.7 | `HandleSetMemberRole` | `SetMemberRole` | update `membership[chid][uid]` | `ChatMemberRoleChangedEvent` |

Tests for each follow the pattern: set up a custom channel + admin caller, call the handler, assert DB + in-memory + fanout side-effects.

Commit messages: `feat(chat): handleAddMember`, `feat(chat): handleRemoveMember`, etc.

---

## Phase 8 — Channel-mutation ops (RENAME / SET_TOPIC / SET_SLOW_MODE) + LIST_CHANNELS / LIST_MEMBERS

Same pattern as Phase 7. All capability-gated except the two LIST ops which only require channel membership.

### Task 8.1: HandleRenameChannel

- canModerate gate.
- Validate new slug (same rules as CREATE).
- `repo.UpdateChannel` with new slug.
- Update `bySlug` map (delete old, insert new).
- Fanout `ChatChannelUpdatedEvent`.

### Task 8.2: HandleSetTopic

- canModerate gate.
- Validate topic length.
- `repo.UpdateChannel` with new topic.
- Fanout `ChatChannelUpdatedEvent`.

### Task 8.3: HandleSetSlowMode

- canModerate gate.
- Clamp seconds to [0, 3600].
- `repo.UpdateChannel`.
- Fanout `ChatChannelUpdatedEvent`.

### Task 8.4: HandleListChannels

- Returns `[]ChannelInfo` for every channel the caller is in: SYSTEM_ALL channels + `userChans[caller]` keys.
- No DB call — pure in-memory walk.

### Task 8.5: HandleListMembers

- Reject if caller is not a member of the channel (or if Kind == SYSTEM_ALL — list-online instead).
- For SYSTEM_ALL: walk `online[]`, return `MemberInfo` per online user with role="member".
- For other kinds: walk `membership[chid]`, build `MemberInfo`. Username comes from `s.usernames[uid]` cache; if missing, fall back to `auth.Repository.GetUserByID` (Phase 9 wires the auth repo for this lookup).

Tests + commits per task.

---

## Phase 9 — Rate limit, slow-mode, mute checks (full ratelimit.go)

### Task 9.1: Token bucket implementation

Replace the stub `ratelimit.go` from Task 4.2:

```go
package chat

import (
	"sync"
	"time"

	"github.com/google/uuid"
)

type tokenBucket struct {
	tokens    int
	lastFill  time.Time
	capacity  int
	refillDur time.Duration // time to fully refill from 0
}

func newTokenBucket(capacity int, refillDur time.Duration) *tokenBucket {
	return &tokenBucket{tokens: capacity, lastFill: time.Now(), capacity: capacity, refillDur: refillDur}
}

// take attempts to consume one token. Returns (true, 0) on success;
// (false, retryAfter) when empty.
func (b *tokenBucket) take(now time.Time) (bool, time.Duration) {
	if b.capacity <= 0 { return true, 0 }
	perToken := b.refillDur / time.Duration(b.capacity)
	if perToken <= 0 { perToken = time.Millisecond }
	elapsed := now.Sub(b.lastFill)
	gained := int(elapsed / perToken)
	if gained > 0 {
		b.tokens += gained
		if b.tokens > b.capacity { b.tokens = b.capacity }
		b.lastFill = b.lastFill.Add(time.Duration(gained) * perToken)
	}
	if b.tokens <= 0 { return false, perToken }
	b.tokens--
	return true, 0
}

// rateLimitTake is a Service helper: looks up or creates the bucket
// for userID, takes one token, returns (ok, retryAfter).
func (s *Service) rateLimitTake(userID uuid.UUID) (bool, time.Duration) {
	s.mu.Lock(); defer s.mu.Unlock()
	b, ok := s.rateBuckets[userID]
	if !ok {
		b = newTokenBucket(s.opts.UserRateMax, s.opts.UserRateWindow)
		s.rateBuckets[userID] = b
	}
	return b.take(time.Now())
}

// slowModeCheck returns (ok, retryAfter). ok==true when last-send for
// (channelID, userID) is older than channel's slow_mode_seconds (or
// when slow-mode is 0 / disabled). Updates last-send on success.
func (s *Service) slowModeCheck(channelID, userID uuid.UUID) (bool, time.Duration) {
	s.mu.Lock(); defer s.mu.Unlock()
	c, ok := s.channels[channelID]
	if !ok || c.SlowModeSeconds <= 0 { return true, 0 }
	gap := time.Duration(c.SlowModeSeconds) * time.Second
	now := time.Now()
	last, present := s.slowMode[slowModeKey{channelID, userID}]
	if present {
		since := now.Sub(last)
		if since < gap { return false, gap - since }
	}
	s.slowMode[slowModeKey{channelID, userID}] = now
	return true, 0
}

// muteCheck returns (active, retryAfter). active==true means caller
// is muted (channel-scoped or globally) and may not send. Caller
// must hold s.mu.RLock or call without holding s.mu (function takes
// its own RLock).
func (s *Service) muteCheck(userID, channelID uuid.UUID) (bool, time.Duration) {
	s.mu.RLock(); defer s.mu.RUnlock()
	now := time.Now()
	for _, key := range []muteKey{{userID, MuteGlobalChannelID}, {userID, channelID}} {
		if mu, ok := s.mutes[key]; ok && mu.ExpiresAt.After(now) {
			return true, time.Until(mu.ExpiresAt)
		}
	}
	return false, 0
}

// banCheck returns (active, retryAfter) for channel-scoped bans.
// Looks up `chat_channel_members.banned_until` via the in-memory
// shadow (loaded at Init + maintained by SetMemberBan handler).
func (s *Service) banCheck(userID, channelID uuid.UUID) (bool, time.Duration) {
	// in-memory bans: stored as a separate map for speed
	s.mu.RLock(); defer s.mu.RUnlock()
	if b, ok := s.bans[banKey{channelID, userID}]; ok && b.Until.After(time.Now()) {
		return true, time.Until(b.Until)
	}
	return false, 0
}

type banKey struct{ ChannelID, UserID uuid.UUID }
type banEntry struct{ Until time.Time }

// Sweep is called by the reaper; the bans map is also pruned when bans
// expire so the next muteCheck path is fast.
var _ = mu_unused

// (mu_unused is a placeholder to suppress "imported and not used" if
// `sync` ends up unused in this file due to other refactors; remove if
// not needed.)
var mu_unused sync.Mutex
```

Add to `service.go`:

```go
// In Service struct:
bans map[banKey]banEntry  // shadowed from chat_channel_members rows where banned_until > NOW

// In Init (alongside other map initializers):
s.bans = map[banKey]banEntry{}
// ... and after hydrating mems:
for _, m := range mems {
    if !m.BannedUntil.IsZero() && m.BannedUntil.After(time.Now()) {
        s.bans[banKey{m.ChannelID, m.UserID}] = banEntry{Until: m.BannedUntil}
    }
}
```

### Task 9.2: Wire rate-limit + slow-mode + mute + ban checks into HandleSend

Update `HandleSend` to perform the four checks in order:

```go
// (after channel lookup, before msg_id allocation)
if active, retry := s.muteCheck(senderID, chID); active {
    return errResp[ChatSendResponse](ChatErrorMuted, "muted", retry.Milliseconds())
}
if active, retry := s.banCheck(senderID, chID); active {
    return errResp[ChatSendResponse](ChatErrorBanned, "banned", retry.Milliseconds())
}
if ok, retry := s.rateLimitTake(senderID); !ok {
    return errResp[ChatSendResponse](ChatErrorRateLimited, "rate limited", retry.Milliseconds())
}
if ok, retry := s.slowModeCheck(chID, senderID); !ok {
    return errResp[ChatSendResponse](ChatErrorSlowMode, "slow mode active", retry.Milliseconds())
}
```

Apply the same four checks to `HandleSendDM` (mute global only, no slow-mode, no ban).

### Task 9.3: Tests

Each check gets its own test:

```go
func TestHandleSend_RejectsWhenRateLimited(t *testing.T) {
    svc := chat.NewTestService(t, []chat.DefaultChannelDef{{Slug: "world", Kind: chat.ChannelKindSystemAll}})
    chid := svc.MustChannelID("world")
    alice := svc.MustOnlineFakeUser(101, "alice")
    svc.SetSendEventFn(func(uint32, any) {})
    // Default UserRateMax=5: 5 sends OK, 6th rate-limited
    for i := 0; i < 5; i++ {
        resp, _ := svc.HandleSend(&ops.OpContext{ConnID: 101, UserID: alice.String()},
            &chat.ChatSendRequest{ChannelID: chid.String(), Body: "msg"})
        if resp.ErrorCode != 0 { t.Fatalf("unexpected rate-limit at i=%d", i) }
    }
    resp, _ := svc.HandleSend(&ops.OpContext{ConnID: 101, UserID: alice.String()},
        &chat.ChatSendRequest{ChannelID: chid.String(), Body: "msg"})
    if resp.ErrorCode != uint32(chat.ChatErrorRateLimited) {
        t.Fatalf("expected RateLimited, got code=%d", resp.ErrorCode)
    }
    if resp.RetryAfterMs == 0 {
        t.Fatal("expected non-zero RetryAfterMs")
    }
}
```

Similar tests for slow-mode, mute, ban. Each is its own task: failing test → impl → pass → commit.

Commit messages:
- `feat(chat): rate-limit + slow-mode helpers (token bucket)`
- `feat(chat): wire rate-limit + slow-mode + mute + ban into handleSend`
- `feat(chat): wire rate-limit + mute into handleSendDM`

---

## Phase 10 — Moderation ops

Each handler follows the canModerate pattern from Phase 7. Pattern:

```go
func (s *Service) HandleX(opCtx *ops.OpContext, req *ChatXRequest) (*ChatXResponse, error) {
    chID, _ := uuid.Parse(req.ChannelID)
    callerID, _ := uuid.Parse(opCtx.UserID)
    if !s.canModerate(callerID, chID) {
        return errResp[ChatXResponse](ChatErrorPermissionDenied, "denied", 0)
    }
    // ... mutation + fanout ...
    return &ChatXResponse{}, nil
}
```

| Task | Op | DB | In-memory | Fanout |
|---|---|---|---|---|
| 10.1 | `HandleMuteUser` | `repo.UpsertMute` | `s.mutes[muteKey{user,chid}]` (use `MuteGlobalChannelID` if `req.ChannelID == ""`); only admins can global-mute | `ChatMutedEvent` to target connID |
| 10.2 | `HandleUnmuteUser` | `repo.DeleteMute` | drop `s.mutes` entry | `ChatUnmutedEvent` to target connID |
| 10.3 | `HandleKickFromChannel` | `repo.RemoveMember` | drop `membership`, `userChans`, `subs` | `ChatKickedEvent` to target + `ChatMemberLeftEvent` to remaining members |
| 10.4 | `HandleBanFromChannel` | `repo.SetMemberBan` | set `s.bans` entry; drop from `subs` if online | `ChatBannedEvent` to target + `ChatMemberLeftEvent` to remaining |
| 10.5 | `HandleUnbanFromChannel` | `repo.ClearMemberBan` | drop `s.bans` entry | none |
| 10.6 | `HandleDeleteMessage` | none (message is RAM-only) | look up `msgIDIndex.Get(msg_id)`; if expired/unknown → `ChatErrorMessageUnknown` | `ChatMessageDeletedEvent` to channel members |
| 10.7 | `HandleBroadcastSystem` | none | requires `chat.admin` capability (NOT canModerate — global only); validate body length | `ChatMessageEvent` with `SenderUserID=""` to all channel members |

For `HandleMuteUser` with `req.ChannelID == ""`, the authorization is `s.hasGlobalAdmin(callerID)` rather than `canModerate`. Wire that branch explicitly.

For each task: failing test (asserting either fanout or DB-side-effect or both) → implement → pass → commit. Sample test for HandleMuteUser:

```go
func TestHandleMute_TargetReceivesMutedEventAndCanNotSend(t *testing.T) {
    svc, repo := chat.NewTestServiceWithAuth(t, []chat.DefaultChannelDef{
        {Slug: "world", Kind: chat.ChannelKindSystemAll},
    })
    chid := svc.MustChannelID("world")
    admin := svc.MustOnlineFakeUser(100, "admin")
    target := svc.MustOnlineFakeUser(101, "target")
    _ = repo.GrantCapability(context.Background(), auth.Capability{
        UserID: admin, Capability: "chat.admin", GrantedBy: admin,
    })

    var sent []recordedEvent
    svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

    resp, _ := svc.HandleMuteUser(&ops.OpContext{ConnID: 100, UserID: admin.String()},
        &chat.ChatMuteUserRequest{UserID: target.String(), ChannelID: chid.String(), DurationMs: 60_000, Reason: "spam"})
    if resp.ErrorCode != 0 { t.Fatalf("err: %s", resp.ErrorMessage) }

    var muted *chat.ChatMutedEvent
    for _, r := range sent {
        if ev, ok := r.Event.(*chat.ChatMutedEvent); ok && r.ConnID == 101 { muted = ev }
    }
    if muted == nil { t.Fatal("target did not receive ChatMutedEvent") }

    sent = nil
    sendResp, _ := svc.HandleSend(&ops.OpContext{ConnID: 101, UserID: target.String()},
        &chat.ChatSendRequest{ChannelID: chid.String(), Body: "should be blocked"})
    if sendResp.ErrorCode != uint32(chat.ChatErrorMuted) {
        t.Fatalf("expected Muted error, got code=%d", sendResp.ErrorCode)
    }
}
```

Commit each task: `feat(chat): handleMute/Unmute/Kick/Ban/Unban/DeleteMessage/Broadcast`.

---

## Phase 11 — Console commands

### Task 11.1: Args/Result types + RegisterConsoleCommands

**Files:**
- Create: `pkg/services/chat/console.go`

Pattern follows `pkg/services/auth/console.go` exactly. Define:

```go
type ChannelSlugArgs struct {
    Slug string `cmd:"help=channel slug,complete=chat-channels"`
}

type ChannelCreateArgs struct {
    Slug string `cmd:"help=new channel slug"`
    Kind string `cmd:"help=system_all|system_predicate|custom"`
    Topic string `cmd:"optional,help=initial topic"`
}

type ChannelTopicArgs struct {
    Slug  string `cmd:"help=channel slug"`
    Topic string `cmd:"help=new topic text"`
}

type ChannelSlowModeArgs struct {
    Slug    string `cmd:"help=channel slug"`
    Seconds int32  `cmd:"help=slow-mode delay in seconds (0 = off)"`
}

type ChannelInfoResult struct { /* mirrors ChannelInfo for cmdsys schema */ }
type ChannelListResult  struct { Channels []ChannelInfoResult }

type UserChannelDurArgs struct {
    Username  string `cmd:"help=target username,complete=players"`
    Channel   string `cmd:"optional,help=channel slug; empty = global mute"`
    Duration  string `cmd:"help=duration (e.g. 5m, 1h)"`
}

type UserChannelArgs struct {
    Username string `cmd:"help=target username,complete=players"`
    Channel  string `cmd:"help=channel slug"`
}

type BroadcastArgs struct {
    Channel string `cmd:"help=channel slug"`
    Body    string `cmd:"help=message body"`
}

type MsgDeleteArgs struct {
    MsgID   string `cmd:"help=message UUID"`
    Channel string `cmd:"help=channel slug"`
}

type ServiceProvider func() *Service
```

`RegisterConsoleCommands(reg *cmdsys.Registry, getSvc ServiceProvider, getAuth auth.RepoProvider)` registers 14 commands:

```
chat.channel.list / .info / .create / .delete / .rename / .topic / .slowmode / .addmember / .removemember
chat.user.mute / .unmute / .kick / .ban / .unban
chat.broadcast
chat.msg.delete
```

Each handler resolves caller capability (`getSvc().hasGlobalAdmin(adminUserID)`), looks up the target channel by slug → ChannelID, looks up target user by username → UserID via `getAuth().GetUserByUsername`, dispatches to the corresponding `Handle*` method on the live service. Result types follow auth's pattern (use `OKResult` for success/no-data; typed result for list/info).

Each command lands as its own task:

11.1 — Args/Result type definitions
11.2 — `chat.channel.list/info`
11.3 — `chat.channel.create/delete/rename/topic/slowmode`
11.4 — `chat.channel.addmember/removemember`
11.5 — `chat.user.mute/unmute`
11.6 — `chat.user.kick/ban/unban`
11.7 — `chat.broadcast/msg.delete`

For each: failing test against `cmdsys.Dispatcher.Invoke` (mirror auth's pattern in `pkg/services/auth/console_test.go`) → implement → pass → commit.

---

## Phase 12 — Gateway integration + mmokit facade

### Task 12.1: mmokit.RegisterChatService facade

**Files:**
- Create: `pkg/mmokit/chat.go`

Mirror `pkg/mmokit/auth.go`. The facade:

1. Fills `opts.RepositoryFactory = chatpg.New` if not injected.
2. Wires `opts.AuthRepoProvider` from the live auth service via the existing `p.Config().AuthResolver` plumbing, OR (if a separate `getAuthRepo` provider exists) from that.
3. Captures `liveService` via `OnReady`.
4. Registers all the typed-op handlers via `mmokit.RegisterOp[Req, Res]` — 25 handlers total (Send, SendDM, Join, Leave, Create, ListChannels, ListMembers, RenameChannel, SetTopic, SetSlowMode, AddMember, RemoveMember, BulkSetMembers, RegisterChannel, UnregisterChannel, SetMemberRole, DeleteMessage, MuteUser, UnmuteUser, Kick, Ban, Unban, Broadcast, SessionEnter, SessionLeave). All use `RouteGatewayLocal`.
5. After `OnReady`, calls `liveService.SetSendEventFn(p.SendTypedEvent)` (or the equivalent process-level send-typed-event helper — confirm name in `pkg/universe/process.go`).
6. Calls `p.AppendExtraMigrations(chat.MigrationsFS())`.
7. Auto-includes `"chat"` in `cfg.ServiceKinds` when `RoleService` is in mode (same logic as auth).
8. Calls `chat.RegisterConsoleCommands(reg, getSvc, getAuth)` if `p.CmdRegistry()` is non-nil.

Code shape (abbreviated; mirror auth.go for the boilerplate):

```go
func RegisterChatService(p *universe.Process, opts ChatOpts) error {
    if opts.UserRateMax == 0 {
        base := DefaultChatOpts()
        base.Repository = opts.Repository
        base.RepositoryFactory = opts.RepositoryFactory
        base.DefaultChannels = opts.DefaultChannels
        opts = base
    }
    if opts.Repository == nil && opts.RepositoryFactory == nil {
        opts.RepositoryFactory = func(pool *pgxpool.Pool) chat.Repository {
            return chatpg.New(pool)
        }
    }
    var liveService *chat.Service
    prev := opts.OnReady
    opts.OnReady = func(svc *chat.Service) {
        liveService = svc
        svc.SetSendEventFn(func(connID uint32, ev any) {
            p.SendTypedEvent(connID, ev) // exact call name TBD by codebase
        })
        if prev != nil { prev(svc) }
    }
    if opts.AuthRepoProvider == nil {
        opts.AuthRepoProvider = func() auth.Repository {
            // fall through to whatever the process exposes; auth must be
            // registered first
            if r, ok := p.Config().AuthResolver.(interface{ Repository() auth.Repository }); ok {
                return r.Repository()
            }
            return nil
        }
    }
    kind := chat.Kind(opts)
    if err := p.RegisterService(kind); err != nil {
        return fmt.Errorf("RegisterChatService: %w", err)
    }
    p.AppendExtraMigrations(chat.MigrationsFS())

    // Register every typed op against the live service (lazy lookup via closure)
    RegisterOp[chat.ChatSendRequest, chat.ChatSendResponse](RouteGatewayLocal,
        func(opCtx *OpContext, req *chat.ChatSendRequest) (*chat.ChatSendResponse, error) {
            if liveService == nil { return nil, errChatServiceNotReady }
            return liveService.HandleSend(opCtx, req)
        })
    // ... 24 more RegisterOp calls following the same template ...

    cfg := p.Config()
    roles, _ := universe.ParseRoles(cfg.Mode)
    if roles.Has(universe.RoleService) {
        already := false
        for _, k := range cfg.ServiceKinds {
            if k == chat.KindName { already = true; break }
        }
        if !already { cfg.ServiceKinds = append(cfg.ServiceKinds, chat.KindName) }
    }

    if reg := p.CmdRegistry(); reg != nil {
        if err := chat.RegisterConsoleCommands(reg,
            func() *chat.Service { return liveService },
            opts.AuthRepoProvider,
        ); err != nil {
            return fmt.Errorf("RegisterChatService: console: %w", err)
        }
    }
    return nil
}

var errChatServiceNotReady = errors.New("chat service not initialized")
```

### Task 12.2: Wire ChatSessionEnter/Leave into the gateway

**Files:**
- Modify: `pkg/universe/gateway.go` — after the auth login-success path, dispatch `ChatSessionEnter`. On WS close, dispatch `ChatSessionLeave`.

Approach: define a thin `ChatSessionHook` interface in `pkg/services/chat` similar to auth's gateway hook:

```go
package chat

// SessionHook is the gateway-side adapter that drives chat presence
// after auth login/logout. mmokit.RegisterChatService installs an
// implementation that dispatches to the live Service.
type SessionHook interface {
    OnSessionEnter(connID uint32, userID, username, gatewayID string)
    OnSessionLeave(connID uint32, gatewayID string)
}
```

Gateway holds an `*chat.SessionHook` reference (nil-safe — when chat isn't registered, hook is nil). Login-success path:

```go
if g.chatHook != nil {
    g.chatHook.OnSessionEnter(sess.ConnID, sess.UserID.String(), sess.Username, g.id)
}
```

WS-close path:

```go
if g.chatHook != nil {
    g.chatHook.OnSessionLeave(connID, g.id)
}
```

mmokit.RegisterChatService installs the hook via `p.InstallChatHook(...)` (new method on `*universe.Process`). Hook implementation calls `liveService.HandleSessionEnter` / `HandleSessionLeave` directly (in-process bypass — no wire round-trip).

Tasks:

12.2a — Add `chat.SessionHook` interface in `pkg/services/chat/session_hook.go`.
12.2b — Add `*Process.InstallChatHook(hook chat.SessionHook)` and per-Process storage in `pkg/universe/process.go`.
12.2c — Modify `pkg/universe/gateway.go` to invoke the hook after login-success and on WS close.
12.2d — Modify `pkg/mmokit/chat.go` RegisterChatService to install a hook impl that calls `liveService.HandleSessionEnter` / `HandleSessionLeave`.
12.2e — Cluster integration test: `pkg/universe/chat_e2e_test.go::TestChatSessionEnterFiresAfterLogin` — register chat + auth, log a user in, assert `ChatChannelsHydratedEvent` arrives over the typed-event channel.

### Task 12.3: 4node-basic example wiring

**Files:**
- Modify: `examples/4node-basic/main.go`
- Modify: `examples/4node-basic/justfile`

In `main.go`, add chat registration alongside auth:

```go
if err := mmokit.RegisterChatService(coord, mmokit.ChatOpts{
    DefaultChannels: []mmokit.DefaultChannelDef{
        {Slug: "world", Kind: mmokit.ChannelKindSystemAll, Topic: "World chat"},
        {Slug: "help",  Kind: mmokit.ChannelKindSystemAll, Topic: "Help chat. Be patient."},
        {Slug: "trade", Kind: mmokit.ChannelKindSystemAll, Topic: "Trade chat."},
    },
}); err != nil {
    log.Fatalf("RegisterChatService: %v", err)
}
```

In `justfile`, add `chat` to `--services=` in the `distributed` and `dev` recipes:

```
--services=auth,chat,echo
```

### Task 12.4: Web client chat panel

**Files:**
- Create: `examples/4node-basic/web/src/chat_panel.ts`

Minimal chat UI (collapsible top-right panel toggled by `c` key):

- Channel tabs (one per channel from `ChatChannelsHydratedEvent`)
- DM tab (one per DM partner, dynamically created on first DM exchange)
- Input box that detects leading `/` and dispatches slash commands per spec §11
- Subscribes to `ChatMessageEvent`, `ChatDMEvent`, `ChatMemberJoinedEvent`, `ChatMemberLeftEvent`, `ChatChannelsHydratedEvent`, `ChatChannelUpdatedEvent`, `ChatChannelGoneEvent`, `ChatMessageDeletedEvent`, `ChatMutedEvent`, `ChatKickedEvent`, `ChatBannedEvent` via the auto-generated SDK's typed-event broadcasts
- Calls auto-generated client methods: `client.chatSend({...})`, `client.chatSendDm({...})`, `client.chatJoin({...})`, etc.
- Renders raw text only (no rich text in v1)

Hand-roll the DOM (no framework). ~150–200 LOC. Add a CSS hook for system-broadcast styling (when `senderUserId == ""`).

Wire it into `main.ts`:

```ts
import { mountChatPanel } from "./chat_panel.js";
mountChatPanel(client);
```

### Task 12.5: SDK regeneration

```bash
just client-sdk examples/4node-basic
```

Verify `examples/4node-basic/web/sdk/` now contains chat-related typed methods + event broadcasts. Inspect a sample:

```bash
grep -n "chatSend\|ChatMessageEvent" examples/4node-basic/web/sdk/client.ts | head -10
```

Expected: typed methods like `chatSend(req: ChatSendRequest): Promise<ChatSendResponse>` and broadcast handlers for each `Chat*Event`.

---

## Phase 13 — End-to-end smoke + final verification

### Task 13.1: Cluster integration test suite

**Files:**
- Create: `pkg/universe/chat_e2e_test.go`

Minimum coverage (one test function per scenario; all use `WithServiceHost("auth", 1)` + `WithServiceHost("chat", 1)` against test Postgres):

| Test | Scenario |
|---|---|
| `TestChat_E2E_SendInWorld` | 3 connected users in `world`; A sends → B + C receive |
| `TestChat_E2E_DMRoundTrip` | A→B online: B receives + A echoes; A→offline-C: silent drop |
| `TestChat_E2E_CustomChannel` | A creates `cool` (password=secret), B joins (correct pw), wrong-pw rejected, A sends, B receives |
| `TestChat_E2E_Mute` | GM mutes alice for 60s in `world`; alice's send returns Muted; others don't see it |
| `TestChat_E2E_SlowMode` | 2s slow-mode on `world`; alice's 2nd send within 2s returns SlowMode |
| `TestChat_E2E_RateLimit` | 6 sends in 1s → 6th returns RateLimited with non-zero RetryAfterMs |
| `TestChat_E2E_AuthGate` | Unauth'd connID's chat send returns NotAuthenticated at the typed-op router |
| `TestChat_E2E_GatewayLossCleanup` | Kill the gateway holding alice's session; chat clears alice from `online[]` within 1 PeerList tick (~1s) |
| `TestChat_E2E_ChannelAdminPromotion` | A creates `cool`, A sends `chat.set_member_role` for B → admin, B kicks C |
| `TestChat_E2E_BootstrapDefaultChannels` | Restart chat service; assert `world`/`help`/`trade` all reappear in DB |

For each test: failing scaffold → implement (mostly fixture wiring) → pass → commit.

### Task 13.2: Manual smoke via `just distributed`

- [ ] **Step 1: Start the cluster**

```bash
just db-reset
just distributed
```

- [ ] **Step 2: Browser walkthrough**

1. Open http://localhost:8080. Register `alice` / `password`. Confirm `world`/`help`/`trade` tabs appear (from hydration).
2. Open another browser (or incognito). Register `bob`. Confirm bob also has the three system tabs.
3. From alice's browser: `/w bob hello` → bob sees DM in a new DM tab.
4. From alice: `/create raidnight pwhereisthegate` → channel created, alice joins as admin.
5. From bob: `/join raidnight pwheresthegate` → bob joins.
6. Alice in raidnight: `/op bob` → bob is now admin.
7. From alice: `/kick bob` (in raidnight) — bob is removed; bob's UI receives `ChatKickedEvent`.
8. From the **server console**: `auth bootstrap-admin alice` (errors first time if alice already has admin caps; otherwise grants). Then test `chat broadcast world The realm is closing soon!` — every connected user sees a system-styled message in world.

- [ ] **Step 3: Run `just test-pg` one final time**

```bash
just test-pg
```

Expected: every chat + auth pgtest passes.

- [ ] **Step 4: No commit needed** — verification only.

### Task 13.3: Final cleanup — no proto leakage check

```bash
grep -rn 'enginepb\|proto/enginepb\|gen/go/enginepb' pkg/services/chat/ examples/4node-basic/web/src/chat_panel.ts examples/4node-basic/main.go || echo CLEAN
```

Expected: `CLEAN`. (Chat code should not reference any client-side proto.)

```bash
grep -rn 'enginepb\.' pkg/mmokit/chat.go
```

Expected: no matches. The chat facade only imports `pkg/services/chat`, `pkg/services/chat/postgres`, `pkg/universe`, `pkg/services/auth`.

### Task 13.4: Commit final smoke verification + open PR

After all tests + manual smoke pass:

```bash
git log --oneline | head -30  # confirm reasonable commit history
git push -u origin feat/mmokit-chat-service
gh pr create --title "feat: chat service (single-instance, pure-pubsub v1)" \
  --body "$(cat <<'EOF'
## Summary
- Engine-tier chat service at `pkg/services/chat/` mirroring `pkg/services/auth/`.
- Single in-cluster instance v1: persisted channels/members/mutes; ephemeral messages/DMs.
- Capability-gated moderation via `auth_capabilities` table (also lands).
- One-line game wiring: `mmokit.RegisterChatService(coord, opts)`.
- 4node-basic example: web chat panel (channel tabs + DM tab + slash commands).

## Test plan
- [ ] All unit + pgtest passes (`just test-pg`)
- [ ] All chat e2e cluster tests pass (`go test ./pkg/universe/... -run Chat_E2E`)
- [ ] Manual smoke via `just distributed`: register, send in world, DM round-trip, custom-channel create/join/kick, GM mute, broadcast.

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-Review Notes

This plan covers the spec's 17 sections in 13 phases. Key coverage map:

- Spec §1 Summary / §2 Goals → Phase 1+ (auth prereq) and Phase 2+ (chat skeleton)
- Spec §3 Architecture → Phase 12 (gateway hook + facade)
- Spec §4 Package layout → Phase 2-12 (every file listed in spec layout has a creation task)
- Spec §5 Wire protocol → Phase 2 (typed_messages.go)
- Spec §6 Schema → Phase 3 (migrations + repo)
- Spec §7 In-memory state → Phase 4 (Service struct + bootstrap)
- Spec §8 End-to-end flows → Phase 5-10 handlers
- Spec §9 Authorization → Phase 7.1 (canModerate) + capability cache from Phase 1.5
- Spec §10 Rate limiting → Phase 9
- Spec §11 Console / metrics / logging → Phase 11; logging-categories registered alongside handlers
- Spec §12 Game integration → Phase 12.3 (4node-basic main.go)
- Spec §13 Testing strategy → Phase 13.1 (cluster e2e tests cover every row in the spec table)
- Spec §14 What dies/changes → reflected in Phase 1 (auth-side migrations + capability methods)
- Spec §15 Open questions → resolved in spec recommendations; no plan tasks needed
- Spec §16 Migration & rollout → mirrored in Phase 0/Phase 1 prereq sequencing
- Spec §17 Future work → out of scope (called out in plan header)

**Type consistency:** every handler signature matches `func (s *Service) HandleX(opCtx *ops.OpContext, req *ChatXRequest) (*ChatXResponse, error)`. Every `ChatXResponse` embeds `ErrorBlock`. The `errResp[R]` helper in Phase 5.2 sets the embedded fields via reflection so the helper is reusable across all 25 handlers.

**Proto-free:** the only protobuf reference in this plan is the existing `proto/meshpb/` (server-internal mesh frames) which the chat service never imports. Phase 13.3 has an explicit grep check.

