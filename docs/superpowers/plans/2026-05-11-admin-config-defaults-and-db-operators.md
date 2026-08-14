# Admin Config Defaults + DB-Backed Operators Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `AdminConfig` self-defaulting (no struct boilerplate in `main.go`) and move operator credentials from in-code Go literals into a new Postgres `admin_operators` table that the admin server seeds with `admin`/`admin` on first run.

**Architecture:** A new `persist.AdminOperatorRepository` interface (with Postgres + in-memory mock implementations) replaces the in-code `[]AdminOperatorConfig` list. `pkg/admin.Server` queries the repo at login time. `mmokit.New()` defaults `Admin.Enabled=true` and auto-wires `DefaultAdminServerFactory()` when nil. `DefaultAdminServerFactory` pulls the repo from `cfg.DBStore.AdminOperators()` and passes it into `ServerOpts`. `NewServer` seeds the default `admin`/`admin` operator with `*.*` grants when the table is empty, and the seeding logs a banner that includes the credentials. The `--admin-hash-password` CLI flag is removed entirely — operator management is a future admin-UI feature; for now, `admin`/`admin` is the only operator on a fresh DB and is expected to be replaced/rotated in production.

**Tech Stack:** Go 1.22+, pgx/v5, golang-migrate (engine migrations in `pkg/persist/postgres/migrations/`), argon2id (via `pkg/services/auth.HashPassword` / `VerifyPassword`).

---

## File Structure

**Created:**
- `pkg/persist/postgres/migrations/003_admin_operators.up.sql` — table DDL
- `pkg/persist/postgres/migrations/003_admin_operators.down.sql` — drop DDL
- `pkg/persist/postgres/admin_operator_repo.go` — Postgres impl + `Store.AdminOperators()` accessor

**Modified:**
- `pkg/persist/repository.go` — add `AdminOperator` DTO + `AdminOperatorRepository` interface
- `pkg/persist/persisttest/mock.go` — add `AdminOperatorRepoMock`
- `pkg/persist/postgres/postgres.go` — add `AdminOperators()` accessor on `Store`
- `pkg/persist/postgres/postgres_test.go` (pgtest) — new admin_operators tests; add `admin_operators` to TRUNCATE list
- `pkg/admin/admin.go` — drop `Config.Operators`, `OperatorConfig`, `Server.operators` map; add `Server.operatorRepo persist.AdminOperatorRepository`; seed default admin in `NewServer`
- `pkg/admin/api_auth.go` — `handleLogin` queries the repo instead of the map
- `pkg/admin/api_auth_test.go` — use `persisttest.NewAdminOperatorRepoMock()` instead of literal map
- `pkg/admin/admin_e2e_test.go` — same; use mock repo
- `pkg/mmokit/admin.go` — `DefaultAdminServerFactory` pulls repo from `c.Cfg().DBStore.AdminOperators()`; drop `AdminOperatorConfig` alias and `ops` conversion
- `pkg/mmokit/mmokit.go` — `New()` defaults `Admin.Enabled=true` and `Admin.ServerFactory=DefaultAdminServerFactory()` when zero
- `pkg/universe/coordinator.go` — `AdminConfig`: remove `Operators []AdminOperatorConfig` field; remove `AdminOperatorConfig` struct; remove `AdminHashPassword` field; default `SessionTTL`/`LockoutMaxAttempts`/`LockoutWindow`/`AuditCap` in `Build()` (or at New); add `Build()`-time error if `Admin.Enabled && cfg.DBStore == nil`
- `pkg/universe/bootstrap.go` — drop `--admin-hash-password` flag registration; flip `--admin-enabled` default to `true`
- `pkg/universe/coordinator.go` — remove the `if c.cfg.AdminHashPassword { … }` early-exit branch in `Start`
- `examples/4node-basic/main.go` — delete the `Admin:` block (defaults handle everything)

**Deleted:**
- `pkg/universe/admin_hash.go` — `promptAndPrintAdminHash` no longer needed

---

## Conventions

- **Username casing:** Per CLAUDE.md / project memory, usernames are forced lowercase everywhere. The repo's `GetByUsername` callers MUST lowercase the input. The repo itself stores whatever it's handed; the contract is "lowercase in, lowercase out."
- **`net:"..."` tags / JSONB shape:** Grants are stored as a JSONB string array (like `players.debug_flags`), defaulting to `'[]'::jsonb`.
- **Logging:** All seeding/login activity logs via `*logger.Logger` under category `"admin"` (already registered by `pkg/admin/admin.go`).
- **TDD discipline:** Every task here is "write failing test → implement → re-run → commit." Where a test requires Postgres (`-tags=pgtest`), the in-memory mock is exercised in unit tests first to keep iteration cycles fast.

---

## Task 1: Add `AdminOperator` DTO and `AdminOperatorRepository` interface

**Files:**
- Modify: `pkg/persist/repository.go` (append new type + interface at the end of the file)

- [ ] **Step 1: Add the DTO and interface**

Append to `pkg/persist/repository.go`:

```go
// AdminOperator is the persistence-layer representation of one admin
// dashboard operator. PasswordHash is the encoded argon2id string
// produced by pkg/services/auth.HashPassword. Grants follow the
// cmdsys grant syntax (e.g. "*.*", "cell.*", "bot.spawn").
type AdminOperator struct {
	Username     string
	PasswordHash string
	Grants       []string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// AdminOperatorRepository persists admin dashboard operators. The
// admin server queries this at login time and seeds a default
// admin/admin operator on first run when Count returns 0.
//
// Usernames are stored lowercase by contract (the admin server
// lowercases at every call site). Implementations should not
// re-normalize.
type AdminOperatorRepository interface {
	// GetByUsername returns the operator with the given username.
	// Returns (nil, ErrNotFound) when no row matches.
	GetByUsername(ctx context.Context, username string) (*AdminOperator, error)

	// Create inserts a new operator. Returns an error if the
	// username already exists (caller should pre-check with Count
	// for seeding flows, or GetByUsername for explicit creates).
	Create(ctx context.Context, op *AdminOperator) error

	// List returns every operator. Used by the future admin UI
	// "operators" page; today's only caller is tests.
	List(ctx context.Context) ([]*AdminOperator, error)

	// Delete removes an operator by username. No error when the
	// username doesn't exist.
	Delete(ctx context.Context, username string) error

	// UpdatePasswordHash rotates the password hash. No-op when the
	// username doesn't exist (returns ErrNotFound).
	UpdatePasswordHash(ctx context.Context, username, hash string) error

	// Count returns the total number of operator rows. Backs the
	// "if 0, seed admin/admin" check at NewServer startup.
	Count(ctx context.Context) (int, error)
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go vet ./pkg/persist/...`
Expected: no output, exit 0.

- [ ] **Step 3: Commit**

```bash
git add pkg/persist/repository.go
git commit -m "persist: add AdminOperatorRepository interface + DTO"
```

---

## Task 2: Add Postgres migration for `admin_operators`

**Files:**
- Create: `pkg/persist/postgres/migrations/003_admin_operators.up.sql`
- Create: `pkg/persist/postgres/migrations/003_admin_operators.down.sql`

- [ ] **Step 1: Write the up migration**

```sql
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
```

Write to `pkg/persist/postgres/migrations/003_admin_operators.up.sql`.

- [ ] **Step 2: Write the down migration**

```sql
DROP TABLE IF EXISTS admin_operators;
```

Write to `pkg/persist/postgres/migrations/003_admin_operators.down.sql`.

- [ ] **Step 3: Verify the migration loads by booting the dev DB**

The migrations are embedded via `//go:embed migrations/*.sql` in `pkg/persist/postgres/migrate.go`. The next `Open` call will apply 003 automatically. Confirm the file exists:

Run: `ls pkg/persist/postgres/migrations/`
Expected output includes `003_admin_operators.up.sql` and `003_admin_operators.down.sql`.

- [ ] **Step 4: Commit**

```bash
git add pkg/persist/postgres/migrations/003_admin_operators.up.sql pkg/persist/postgres/migrations/003_admin_operators.down.sql
git commit -m "persist: add admin_operators migration"
```

---

## Task 3: Implement Postgres `adminOperatorRepo`

**Files:**
- Create: `pkg/persist/postgres/admin_operator_repo.go`
- Modify: `pkg/persist/postgres/postgres.go` (add `AdminOperators()` accessor)

- [ ] **Step 1: Write the implementation**

Create `pkg/persist/postgres/admin_operator_repo.go`:

```go
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/mmokit/mmokit/pkg/persist"
)

type adminOperatorRepo struct {
	pool *pgxpool.Pool
}

var _ persist.AdminOperatorRepository = (*adminOperatorRepo)(nil)

const adminOperatorSelectColumns = `username, password_hash, grants, created_at, updated_at`

func (r *adminOperatorRepo) GetByUsername(ctx context.Context, username string) (*persist.AdminOperator, error) {
	row := r.pool.QueryRow(ctx,
		`SELECT `+adminOperatorSelectColumns+` FROM admin_operators WHERE username = $1`,
		username,
	)
	op, err := scanAdminOperator(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, persist.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("adminOperatorRepo.GetByUsername %q: %w", username, err)
	}
	return op, nil
}

func (r *adminOperatorRepo) Create(ctx context.Context, op *persist.AdminOperator) error {
	grantsJSON, err := marshalGrants(op.Grants)
	if err != nil {
		return fmt.Errorf("adminOperatorRepo.Create marshal grants: %w", err)
	}
	_, err = r.pool.Exec(ctx,
		`INSERT INTO admin_operators (username, password_hash, grants) VALUES ($1, $2, $3::jsonb)`,
		op.Username, op.PasswordHash, grantsJSON,
	)
	if err != nil {
		return fmt.Errorf("adminOperatorRepo.Create %q: %w", op.Username, err)
	}
	return nil
}

func (r *adminOperatorRepo) List(ctx context.Context) ([]*persist.AdminOperator, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+adminOperatorSelectColumns+` FROM admin_operators ORDER BY username`,
	)
	if err != nil {
		return nil, fmt.Errorf("adminOperatorRepo.List: %w", err)
	}
	defer rows.Close()

	var out []*persist.AdminOperator
	for rows.Next() {
		op, err := scanAdminOperator(rows)
		if err != nil {
			return nil, fmt.Errorf("adminOperatorRepo.List scan: %w", err)
		}
		out = append(out, op)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("adminOperatorRepo.List rows: %w", err)
	}
	return out, nil
}

func (r *adminOperatorRepo) Delete(ctx context.Context, username string) error {
	if _, err := r.pool.Exec(ctx,
		`DELETE FROM admin_operators WHERE username = $1`, username,
	); err != nil {
		return fmt.Errorf("adminOperatorRepo.Delete %q: %w", username, err)
	}
	return nil
}

func (r *adminOperatorRepo) UpdatePasswordHash(ctx context.Context, username, hash string) error {
	tag, err := r.pool.Exec(ctx,
		`UPDATE admin_operators SET password_hash = $1, updated_at = NOW() WHERE username = $2`,
		hash, username,
	)
	if err != nil {
		return fmt.Errorf("adminOperatorRepo.UpdatePasswordHash %q: %w", username, err)
	}
	if tag.RowsAffected() == 0 {
		return persist.ErrNotFound
	}
	return nil
}

func (r *adminOperatorRepo) Count(ctx context.Context) (int, error) {
	var n int
	if err := r.pool.QueryRow(ctx, `SELECT COUNT(*) FROM admin_operators`).Scan(&n); err != nil {
		return 0, fmt.Errorf("adminOperatorRepo.Count: %w", err)
	}
	return n, nil
}

func scanAdminOperator(scanner interface {
	Scan(dest ...any) error
}) (*persist.AdminOperator, error) {
	var op persist.AdminOperator
	var grantsBytes []byte
	if err := scanner.Scan(
		&op.Username,
		&op.PasswordHash,
		&grantsBytes,
		&op.CreatedAt,
		&op.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if len(grantsBytes) > 0 {
		if err := json.Unmarshal(grantsBytes, &op.Grants); err != nil {
			return nil, fmt.Errorf("unmarshal grants %q: %w", op.Username, err)
		}
	}
	return &op, nil
}

// marshalGrants returns "[]" for nil/empty input so the JSONB column
// always holds a valid array (matching the schema's DEFAULT '[]'::jsonb).
func marshalGrants(grants []string) ([]byte, error) {
	if len(grants) == 0 {
		return []byte(`[]`), nil
	}
	b, err := json.Marshal(grants)
	if err != nil {
		return nil, err
	}
	if len(b) == 0 || string(b) == "null" {
		return []byte(`[]`), nil
	}
	return b, nil
}
```

- [ ] **Step 2: Add the `AdminOperators()` accessor on `Store`**

Edit `pkg/persist/postgres/postgres.go`. Find the existing `Config()` accessor and append immediately after it:

```go
// AdminOperators returns the AdminOperatorRepository implementation.
func (s *Store) AdminOperators() persist.AdminOperatorRepository {
	return &adminOperatorRepo{pool: s.pool}
}
```

- [ ] **Step 3: Verify it compiles**

Run: `go vet ./pkg/persist/...`
Expected: no output, exit 0.

- [ ] **Step 4: Commit**

```bash
git add pkg/persist/postgres/admin_operator_repo.go pkg/persist/postgres/postgres.go
git commit -m "persist/postgres: AdminOperatorRepository impl"
```

---

## Task 4: Add `AdminOperatorRepoMock` to `persisttest`

**Files:**
- Modify: `pkg/persist/persisttest/mock.go`

- [ ] **Step 1: Append the mock**

Append to `pkg/persist/persisttest/mock.go` (before the `Compile-time interface assertions` block):

```go
// AdminOperatorRepoMock is an in-memory AdminOperatorRepository.
type AdminOperatorRepoMock struct {
	mu   sync.Mutex
	rows map[string]*persist.AdminOperator
}

func NewAdminOperatorRepoMock() *AdminOperatorRepoMock {
	return &AdminOperatorRepoMock{rows: make(map[string]*persist.AdminOperator)}
}

func (m *AdminOperatorRepoMock) GetByUsername(ctx context.Context, username string) (*persist.AdminOperator, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.rows[username]
	if !ok {
		return nil, persist.ErrNotFound
	}
	return cloneAdminOperator(rec), nil
}

func (m *AdminOperatorRepoMock) Create(ctx context.Context, op *persist.AdminOperator) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.rows[op.Username]; exists {
		return fmt.Errorf("adminOperatorRepoMock.Create: %q already exists", op.Username)
	}
	cp := *op
	if cp.CreatedAt.IsZero() {
		cp.CreatedAt = time.Now().UTC()
	}
	if cp.UpdatedAt.IsZero() {
		cp.UpdatedAt = cp.CreatedAt
	}
	cp.Grants = slices.Clone(op.Grants)
	m.rows[op.Username] = &cp
	return nil
}

func (m *AdminOperatorRepoMock) List(ctx context.Context) ([]*persist.AdminOperator, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := make([]string, 0, len(m.rows))
	for k := range m.rows {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	out := make([]*persist.AdminOperator, len(keys))
	for i, k := range keys {
		out[i] = cloneAdminOperator(m.rows[k])
	}
	return out, nil
}

func (m *AdminOperatorRepoMock) Delete(ctx context.Context, username string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.rows, username)
	return nil
}

func (m *AdminOperatorRepoMock) UpdatePasswordHash(ctx context.Context, username, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.rows[username]
	if !ok {
		return persist.ErrNotFound
	}
	rec.PasswordHash = hash
	rec.UpdatedAt = time.Now().UTC()
	return nil
}

func (m *AdminOperatorRepoMock) Count(ctx context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.rows), nil
}

func cloneAdminOperator(src *persist.AdminOperator) *persist.AdminOperator {
	if src == nil {
		return nil
	}
	cp := *src
	cp.Grants = slices.Clone(src.Grants)
	return &cp
}
```

Then add this line to the existing compile-time assertions block at the bottom of the file:

```go
	_ persist.AdminOperatorRepository = (*AdminOperatorRepoMock)(nil)
```

The block becomes:

```go
var (
	_ persist.PlayerRepository        = (*PlayerRepoMock)(nil)
	_ persist.MarketRepository        = (*MarketRepoMock)(nil)
	_ persist.ConfigRepository        = (*ConfigRepoMock)(nil)
	_ persist.AdminOperatorRepository = (*AdminOperatorRepoMock)(nil)
)
```

Add `"fmt"` to the import block (it's not currently imported).

- [ ] **Step 2: Verify it compiles**

Run: `go vet ./pkg/persist/...`
Expected: no output, exit 0.

- [ ] **Step 3: Commit**

```bash
git add pkg/persist/persisttest/mock.go
git commit -m "persisttest: AdminOperatorRepoMock"
```

---

## Task 5: Pgtest coverage for `adminOperatorRepo`

**Files:**
- Modify: `pkg/persist/postgres/postgres_test.go`

- [ ] **Step 1: Add `admin_operators` to the TRUNCATE list**

Edit `pkg/persist/postgres/postgres_test.go`. In `openTestStore`, change the TRUNCATE statement from:

```go
	if _, err := s.pool.Exec(ctx, `
		TRUNCATE players, game_config, market_orders, market_trades RESTART IDENTITY
	`); err != nil {
```

to:

```go
	if _, err := s.pool.Exec(ctx, `
		TRUNCATE players, game_config, market_orders, market_trades, admin_operators RESTART IDENTITY
	`); err != nil {
```

- [ ] **Step 2: Append admin_operators tests**

Append to the bottom of `pkg/persist/postgres/postgres_test.go` (after the `ExtraMigrations test` section):

```go
// ---------------------------------------------------------------------------
// AdminOperatorRepository tests
// ---------------------------------------------------------------------------

func TestAdminOperatorRepo_CreateAndGet(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.AdminOperators()

	op := &persist.AdminOperator{
		Username:     "alice",
		PasswordHash: "$argon2id$v=19$m=65536,t=3,p=4$XXXX$YYYY",
		Grants:       []string{"*.*"},
	}
	if err := repo.Create(ctx, op); err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := repo.GetByUsername(ctx, "alice")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if got.Username != "alice" {
		t.Errorf("Username = %q, want %q", got.Username, "alice")
	}
	if got.PasswordHash != op.PasswordHash {
		t.Errorf("PasswordHash = %q, want %q", got.PasswordHash, op.PasswordHash)
	}
	if len(got.Grants) != 1 || got.Grants[0] != "*.*" {
		t.Errorf("Grants = %v, want [*.*]", got.Grants)
	}
	if got.CreatedAt.IsZero() {
		t.Errorf("CreatedAt should be auto-populated")
	}
}

func TestAdminOperatorRepo_GetUnknown(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if _, err := s.AdminOperators().GetByUsername(ctx, "nobody"); !errors.Is(err, persist.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestAdminOperatorRepo_CountEmpty(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	n, err := s.AdminOperators().Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 0 {
		t.Errorf("Count on empty table = %d, want 0", n)
	}
}

func TestAdminOperatorRepo_Count(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.AdminOperators()
	for _, name := range []string{"alice", "bob", "charlie"} {
		if err := repo.Create(ctx, &persist.AdminOperator{Username: name, PasswordHash: "x"}); err != nil {
			t.Fatalf("Create %q: %v", name, err)
		}
	}
	n, err := repo.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 3 {
		t.Errorf("Count = %d, want 3", n)
	}
}

func TestAdminOperatorRepo_List(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.AdminOperators()
	for _, name := range []string{"zoe", "alice", "bob"} {
		if err := repo.Create(ctx, &persist.AdminOperator{Username: name, PasswordHash: "x"}); err != nil {
			t.Fatalf("Create %q: %v", name, err)
		}
	}
	out, err := repo.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("len = %d, want 3", len(out))
	}
	// List returns sorted by username.
	wantOrder := []string{"alice", "bob", "zoe"}
	for i, want := range wantOrder {
		if out[i].Username != want {
			t.Errorf("[%d] Username = %q, want %q", i, out[i].Username, want)
		}
	}
}

func TestAdminOperatorRepo_DuplicateCreateRejected(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.AdminOperators()
	op := &persist.AdminOperator{Username: "dup", PasswordHash: "x"}
	if err := repo.Create(ctx, op); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	if err := repo.Create(ctx, op); err == nil {
		t.Fatalf("expected error on duplicate Create, got nil")
	}
}

func TestAdminOperatorRepo_Delete(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.AdminOperators()
	if err := repo.Create(ctx, &persist.AdminOperator{Username: "alice", PasswordHash: "x"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.Delete(ctx, "alice"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := repo.GetByUsername(ctx, "alice"); !errors.Is(err, persist.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after Delete, got %v", err)
	}
}

func TestAdminOperatorRepo_DeleteUnknownIsNoop(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.AdminOperators().Delete(ctx, "no-such-user"); err != nil {
		t.Fatalf("Delete of unknown user should be no-op, got %v", err)
	}
}

func TestAdminOperatorRepo_UpdatePasswordHash(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.AdminOperators()
	if err := repo.Create(ctx, &persist.AdminOperator{Username: "alice", PasswordHash: "v1"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := repo.UpdatePasswordHash(ctx, "alice", "v2"); err != nil {
		t.Fatalf("UpdatePasswordHash: %v", err)
	}
	got, err := repo.GetByUsername(ctx, "alice")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if got.PasswordHash != "v2" {
		t.Errorf("PasswordHash = %q, want v2", got.PasswordHash)
	}
}

func TestAdminOperatorRepo_UpdateUnknownReturnsErrNotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.AdminOperators().UpdatePasswordHash(ctx, "no-such-user", "x"); !errors.Is(err, persist.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestAdminOperatorRepo_GrantsDefaultEmptyArray(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.AdminOperators()
	// Insert without grants — default '[]'::jsonb should produce an empty slice (or nil).
	if err := repo.Create(ctx, &persist.AdminOperator{Username: "nogrants", PasswordHash: "x"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := repo.GetByUsername(ctx, "nogrants")
	if err != nil {
		t.Fatalf("GetByUsername: %v", err)
	}
	if len(got.Grants) != 0 {
		t.Errorf("Grants = %v, want empty", got.Grants)
	}
}
```

- [ ] **Step 3: Run pgtest**

Run: `just test-pg -run TestAdminOperatorRepo`
Expected: all 10 admin operator tests PASS.

If `just test-pg` is unfamiliar, the underlying command is `POSTGRES_URL=postgres://mmo:mmo@localhost:5432/mmo?sslmode=disable go test -tags=pgtest -run TestAdminOperatorRepo ./pkg/persist/postgres/...`. Ensure `just db-up` has been run first.

- [ ] **Step 4: Commit**

```bash
git add pkg/persist/postgres/postgres_test.go
git commit -m "persist/postgres: AdminOperatorRepo pgtest coverage"
```

---

## Task 6: Refactor `pkg/admin` to use the repo

**Files:**
- Modify: `pkg/admin/admin.go` — drop `Operators` field, `OperatorConfig` type, and `Server.operators` map
- Modify: `pkg/admin/api_auth.go` — query repo at login

This task changes the production code; tests are updated in Task 7. Some unit tests will break in the middle of this task — that's expected and resolved before commit.

- [ ] **Step 1: Update `pkg/admin/admin.go`**

The two key changes:

(a) Drop the `Operators` field from `Config` and the `OperatorConfig` type entirely.

Edit `pkg/admin/admin.go`. Replace this block:

```go
// Config is the construction-time bundle for NewServer.
type Config struct {
	BindAddr   string        // for cookie Secure-flag relaxing on loopback
	SessionTTL time.Duration // default 8h
	CookieOpts CookieOpts    // default: Path=/admin, Secure, SameSite=Strict
	LockoutMax int           // default 5
	LockoutWin time.Duration // default 15m
	AuditCap   int           // default 4096
	LogRingCap int           // default 4096

	Operators []OperatorConfig
}

// OperatorConfig is one entry in the Admin operators list.
type OperatorConfig struct {
	Username     string
	PasswordHash string
	Grants       []string
}
```

with:

```go
// Config is the construction-time bundle for NewServer.
type Config struct {
	BindAddr   string        // for cookie Secure-flag relaxing on loopback
	SessionTTL time.Duration // default 8h
	CookieOpts CookieOpts    // default: Path=/admin, Secure, SameSite=Strict
	LockoutMax int           // default 5
	LockoutWin time.Duration // default 15m
	AuditCap   int           // default 4096
	LogRingCap int           // default 4096
}
```

(b) Replace `Server.operators map[string]OperatorConfig` with `Server.operatorRepo persist.AdminOperatorRepository`. In the `Server` struct definition, change:

```go
	operators  map[string]OperatorConfig
```

to:

```go
	operatorRepo persist.AdminOperatorRepository
```

(c) Add `OperatorRepo persist.AdminOperatorRepository` to `ServerOpts`. Append it after the `Config` field:

```go
	// OperatorRepo persists admin operators. When NewServer detects
	// an empty table (Count == 0), a default admin/admin operator is
	// seeded with grants ["*.*"]. Required — pass an in-memory mock
	// from pkg/persist/persisttest for tests.
	OperatorRepo persist.AdminOperatorRepository
```

(d) Replace the operators-map construction in `NewServer`. Find:

```go
	ops := make(map[string]OperatorConfig, len(cfg.Operators))
	for _, o := range cfg.Operators {
		ops[strings.ToLower(o.Username)] = o
	}
```

Delete that block entirely.

In the `s := &Server{...}` literal, change:

```go
		operators:  ops,
```

to:

```go
		operatorRepo: opts.OperatorRepo,
```

(e) Add seeding logic at the end of `NewServer`, just before `return s`:

```go
	// Seed a default admin/admin operator if the table is empty.
	// First-run dev convenience: production deployments rotate via
	// future admin-UI user management.
	if err := seedDefaultOperator(ctx, opts.OperatorRepo, opts.Logger); err != nil {
		if opts.Logger != nil {
			opts.Logger.Log("admin", "seed default operator: %v", err)
		}
	}
```

(f) Add the `seedDefaultOperator` helper at the bottom of `admin.go` (after `mustSubFS`):

```go
// seedDefaultOperator inserts a default admin/admin operator with the
// wildcard grant when the table is empty. Idempotent: a non-zero Count
// short-circuits the insert. Used on first run so the admin dashboard
// is reachable without manual seeding.
//
// Logs a banner with the credentials so operators don't need to grep
// the source — the expectation is rotation in production.
func seedDefaultOperator(ctx context.Context, repo persist.AdminOperatorRepository, log *logger.Logger) error {
	if repo == nil {
		return nil
	}
	n, err := repo.Count(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	hash, err := auth.HashPassword("admin", auth.DefaultArgonParams())
	if err != nil {
		return err
	}
	if err := repo.Create(ctx, &persist.AdminOperator{
		Username:     "admin",
		PasswordHash: hash,
		Grants:       []string{"*.*"},
	}); err != nil {
		return err
	}
	if log != nil {
		log.Log("admin", "seeded default operator: admin / admin (CHANGE IN PRODUCTION)")
	}
	return nil
}
```

(g) Update the `pkg/admin/admin.go` imports. Add `"github.com/mmokit/mmokit/pkg/persist"` and `"github.com/mmokit/mmokit/pkg/services/auth"`. Remove `"strings"` (no longer used since the operators-map went away).

The final import block should be:

```go
import (
	"context"
	"io/fs"
	"net/http"
	"time"

	"github.com/mmokit/mmokit/pkg/admin/static"
	"github.com/mmokit/mmokit/pkg/cmdsys"
	"github.com/mmokit/mmokit/pkg/logger"
	"github.com/mmokit/mmokit/pkg/persist"
	"github.com/mmokit/mmokit/pkg/services/auth"
	"github.com/mmokit/mmokit/pkg/universe"
)
```

- [ ] **Step 2: Update `pkg/admin/api_auth.go` to query the repo**

In `handleLogin`, find:

```go
	op, ok := s.operators[req.Username]
	if !ok {
		// already counted as a failure by the Check above
		s.audit.Append(AuditEntry{
			Username: req.Username, IP: remoteAddr(r).String(), Verb: "auth.login",
			OK: false, Error: "unknown user",
			StartedAt: time.Now(), FinishedAt: time.Now(),
		})
		s.logger.Log("admin", "login-fail user=%s ip=%s reason=unknown-user", req.Username, remoteAddr(r))
		writeJSONError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
```

Replace with:

```go
	op, err := s.operatorRepo.GetByUsername(r.Context(), strings.ToLower(req.Username))
	if err != nil {
		// already counted as a failure by the Check above
		s.audit.Append(AuditEntry{
			Username: req.Username, IP: remoteAddr(r).String(), Verb: "auth.login",
			OK: false, Error: "unknown user",
			StartedAt: time.Now(), FinishedAt: time.Now(),
		})
		s.logger.Log("admin", "login-fail user=%s ip=%s reason=unknown-user", req.Username, remoteAddr(r))
		writeJSONError(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
```

Add `"strings"` to the `pkg/admin/api_auth.go` imports if not already present. The new `op` is `*persist.AdminOperator` — the existing `op.PasswordHash` and `op.Grants` references continue to work because the field names match.

- [ ] **Step 3: Run `go vet` and admin unit tests (expected: api_auth_test.go FAILS — fixed in Task 7)**

Run: `go vet ./pkg/admin/...`
Expected: no output, exit 0.

Run: `go test ./pkg/admin/... -count=1`
Expected: `TestLogin_*` tests fail because `newTestServer` still constructs the Server with an `operators` map. This is fixed in Task 7.

- [ ] **Step 4: Do not commit yet** — proceed to Task 7 to fix the tests, then commit Tasks 6 + 7 together to avoid landing a broken build.

---

## Task 7: Update `pkg/admin` tests to use the mock repo

**Files:**
- Modify: `pkg/admin/api_auth_test.go`
- Modify: `pkg/admin/admin_e2e_test.go`

- [ ] **Step 1: Replace `newTestServer` in `api_auth_test.go`**

Replace the body of `newTestServer` (and add the seeded mock to imports):

```go
func newTestServer(t *testing.T) *Server {
	t.Helper()
	hash, err := auth.HashPassword("p@ssw0rd!", auth.DefaultArgonParams())
	if err != nil {
		t.Fatal(err)
	}
	repo := persisttest.NewAdminOperatorRepoMock()
	if err := repo.Create(context.Background(), &persist.AdminOperator{
		Username:     "josh",
		PasswordHash: hash,
		Grants:       []string{"*.*"},
	}); err != nil {
		t.Fatal(err)
	}
	return &Server{
		sessions:     NewMemorySessionStore(),
		audit:        NewAuditLog(256),
		lockout:      NewLockout(5, 15*time.Minute),
		operatorRepo: repo,
		logger:       logger.New(),
		cfg: Config{
			SessionTTL: time.Hour,
			CookieOpts: defaultCookieOpts(),
		},
	}
}
```

Add to `pkg/admin/api_auth_test.go` imports:

```go
	"context"

	"github.com/mmokit/mmokit/pkg/persist"
	"github.com/mmokit/mmokit/pkg/persist/persisttest"
```

- [ ] **Step 2: Update `mountE2EAdmin` in `admin_e2e_test.go`**

Replace this block:

```go
	mux := http.NewServeMux()
	srv := admin.NewServer(admin.ServerOpts{
		View:         admin.NewLocalClusterView(p),
		Registry:     p.CmdRegistry(),
		Dispatcher:   p.CmdDispatcher(),
		SessionStore: admin.NewMemorySessionStore(),
		Panels:       admin.NewPanelRegistry(),
		Logger:       p.Log,
		Process:      p,
		Config: admin.Config{
			BindAddr:   "127.0.0.1:0",
			SessionTTL: time.Hour,
			Operators: []admin.OperatorConfig{
				{Username: username, PasswordHash: hash, Grants: []string{"*.*"}},
			},
		},
	})
```

with:

```go
	repo := persisttest.NewAdminOperatorRepoMock()
	if err := repo.Create(context.Background(), &persist.AdminOperator{
		Username:     username,
		PasswordHash: hash,
		Grants:       []string{"*.*"},
	}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	srv := admin.NewServer(admin.ServerOpts{
		View:         admin.NewLocalClusterView(p),
		Registry:     p.CmdRegistry(),
		Dispatcher:   p.CmdDispatcher(),
		SessionStore: admin.NewMemorySessionStore(),
		Panels:       admin.NewPanelRegistry(),
		Logger:       p.Log,
		Process:      p,
		OperatorRepo: repo,
		Config: admin.Config{
			BindAddr:   "127.0.0.1:0",
			SessionTTL: time.Hour,
		},
	})
```

Add to `pkg/admin/admin_e2e_test.go` imports:

```go
	"github.com/mmokit/mmokit/pkg/persist"
	"github.com/mmokit/mmokit/pkg/persist/persisttest"
```

- [ ] **Step 3: Add a new test that exercises the auto-seeding path**

Append to `pkg/admin/api_auth_test.go`:

```go
func TestNewServer_SeedsDefaultOperator(t *testing.T) {
	t.Parallel()
	repo := persisttest.NewAdminOperatorRepoMock()

	// Build a minimal Server via NewServer so the seeding path fires.
	srv := NewServer(ServerOpts{
		SessionStore: NewMemorySessionStore(),
		Panels:       NewPanelRegistry(),
		Logger:       logger.New(),
		OperatorRepo: repo,
		Config:       Config{SessionTTL: time.Hour},
	})
	t.Cleanup(srv.Stop)

	got, err := repo.GetByUsername(context.Background(), "admin")
	if err != nil {
		t.Fatalf("default admin operator not seeded: %v", err)
	}
	if len(got.Grants) != 1 || got.Grants[0] != "*.*" {
		t.Errorf("default admin grants = %v, want [*.*]", got.Grants)
	}
	ok, verr := auth.VerifyPassword("admin", got.PasswordHash)
	if verr != nil {
		t.Fatalf("VerifyPassword: %v", verr)
	}
	if !ok {
		t.Errorf("default admin password should verify against 'admin'")
	}
}

func TestNewServer_DoesNotReseedWhenOperatorsExist(t *testing.T) {
	t.Parallel()
	repo := persisttest.NewAdminOperatorRepoMock()
	if err := repo.Create(context.Background(), &persist.AdminOperator{
		Username:     "alice",
		PasswordHash: "preexisting",
		Grants:       []string{"cell.*"},
	}); err != nil {
		t.Fatal(err)
	}

	srv := NewServer(ServerOpts{
		SessionStore: NewMemorySessionStore(),
		Panels:       NewPanelRegistry(),
		Logger:       logger.New(),
		OperatorRepo: repo,
		Config:       Config{SessionTTL: time.Hour},
	})
	t.Cleanup(srv.Stop)

	// "admin" should NOT have been seeded — non-empty table short-circuits.
	if _, err := repo.GetByUsername(context.Background(), "admin"); err == nil {
		t.Fatalf("seed fired even though table was non-empty")
	}
	// "alice" should still be there.
	if _, err := repo.GetByUsername(context.Background(), "alice"); err != nil {
		t.Fatalf("pre-existing alice operator vanished: %v", err)
	}
}
```

- [ ] **Step 4: Run admin tests**

Run: `go test ./pkg/admin/... -count=1`
Expected: all PASS, including the two new `TestNewServer_*` tests.

- [ ] **Step 5: Commit Tasks 6 + 7 together**

```bash
git add pkg/admin/admin.go pkg/admin/api_auth.go pkg/admin/api_auth_test.go pkg/admin/admin_e2e_test.go
git commit -m "admin: switch operators to persist.AdminOperatorRepository + seed admin/admin"
```

---

## Task 8: Remove operators from `pkg/universe.AdminConfig` and apply defaults in `Build()`

**Files:**
- Modify: `pkg/universe/coordinator.go`

- [ ] **Step 1: Drop the `Operators` field and `AdminOperatorConfig` type**

Edit `pkg/universe/coordinator.go`. Replace this block:

```go
type AdminConfig struct {
	Enabled            bool
	SessionTTL         time.Duration
	LockoutMaxAttempts int
	LockoutWindow      time.Duration
	AuditCap           int
	Operators          []AdminOperatorConfig

	// ServerFactory builds the admin server when Enabled. Provided by
	// mmokit.DefaultAdminServerFactory or a custom factory. Nil = the
	// admin section is a no-op even when Enabled=true.
	ServerFactory func(*Process) AdminServer
}

// AdminOperatorConfig is one entry in AdminConfig.Operators. PasswordHash
// is the encoded argon2id string from `<server> --admin-hash-password`.
type AdminOperatorConfig struct {
	Username     string
	PasswordHash string
	Grants       []string
}
```

with:

```go
type AdminConfig struct {
	Enabled            bool          // default true (auto-set in Build when AdminListen is non-empty)
	SessionTTL         time.Duration // default 8h
	LockoutMaxAttempts int           // default 5
	LockoutWindow      time.Duration // default 15m
	AuditCap           int           // default 4096

	// ServerFactory builds the admin server when Enabled. Defaulted by
	// mmokit.New to DefaultAdminServerFactory() when nil. Games may
	// override with a custom factory.
	ServerFactory func(*Process) AdminServer
}
```

- [ ] **Step 2: Remove the `AdminHashPassword` field**

Find and delete this block (around line 299–303):

```go
	// AdminHashPassword, when true, causes Process.Start to prompt for a
	// password (stdin), print its argon2id-encoded hash, and exit. Used to
	// generate operator entries for AdminConfig.Operators. Engine-owned via
	// the --admin-hash-password flag in BindFlags.
	AdminHashPassword bool
```

- [ ] **Step 3: Apply `AdminConfig` defaults in `Build()`**

Find the existing `if cfg.DBStore == nil && cfg.PostgresURL != "" { … }` block in `Build()`. Immediately after the closing brace of that block (and before the `if cfg.DBStore != nil { registerDebugCommands(c) … }` block), insert:

```go
	// Apply AdminConfig defaults so games don't have to spell them out.
	// AdminListen non-empty implies "user wants admin" — enable unless
	// they explicitly disabled it via --admin-enabled=false (which sets
	// Enabled to false at flag-parse time). The flag default is true.
	if cfg.AdminListen != "" && cfg.Admin.Enabled {
		if cfg.Admin.SessionTTL == 0 {
			cfg.Admin.SessionTTL = 8 * time.Hour
		}
		if cfg.Admin.LockoutMaxAttempts == 0 {
			cfg.Admin.LockoutMaxAttempts = 5
		}
		if cfg.Admin.LockoutWindow == 0 {
			cfg.Admin.LockoutWindow = 15 * time.Minute
		}
		if cfg.Admin.AuditCap == 0 {
			cfg.Admin.AuditCap = 4096
		}
		if cfg.DBStore == nil {
			panic(fmt.Errorf("coordinator: Admin.Enabled requires a database — set --postgres-url"))
		}
		c.cfg = cfg
	}
```

- [ ] **Step 4: Remove the `AdminHashPassword` early-exit branch in `Start`**

Find and delete the block in `Start`:

```go
	// admin-hash-password runs BEFORE Build() because the operator hash
	// is itself a config value the user needs to compute before populating
	// AdminConfig.Operators.
	if c.cfg.AdminHashPassword {
		if err := promptAndPrintAdminHash(); err != nil {
			fmt.Fprintf(os.Stderr, "admin-hash-password: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
```

(Comment text on adjacent lines may need light cleanup — leave nothing dangling.)

- [ ] **Step 5: Verify it compiles (will fail until Task 9–10 patch mmokit + 4node-basic)**

Run: `go vet ./pkg/universe/...`
Expected: no output from the universe package alone.

Run: `go vet ./...`
Expected: errors in `pkg/mmokit/admin.go` (`AdminOperatorConfig undefined`) and `examples/4node-basic/main.go` (same). These are fixed in Tasks 9–11.

- [ ] **Step 6: Do not commit yet** — finish Tasks 9–11 first, then commit Tasks 8–11 together.

---

## Task 9: Drop `--admin-hash-password` flag and flip `--admin-enabled` default

**Files:**
- Modify: `pkg/universe/bootstrap.go`
- Delete: `pkg/universe/admin_hash.go`

- [ ] **Step 1: Remove the flag registration from `BindFlags`**

Edit `pkg/universe/bootstrap.go`. Find and delete:

```go
	flag.BoolVar(&c.AdminHashPassword, "admin-hash-password", false,
		"interactively prompt for a password and print its argon2id hash, then exit")
```

- [ ] **Step 2: Flip the `--admin-enabled` default**

Find:

```go
	// Default mirrors what the Config literal already set so games that
	// hardcode Admin.Enabled = true don't need to also pass --admin-enabled.
	flag.BoolVar(&c.Admin.Enabled, "admin-enabled", c.Admin.Enabled,
		"enable the admin dashboard at /admin/* (requires --admin-listen)")
```

Replace with:

```go
	// Default-on: admin dashboard mounts whenever --admin-listen is set.
	// Operators can explicitly disable via --admin-enabled=false to get
	// the operational endpoints (/metrics, /commands, /events) without
	// the auth/UI surface.
	flag.BoolVar(&c.Admin.Enabled, "admin-enabled", true,
		"enable the admin dashboard at /admin/* (requires --admin-listen)")
```

Note: hard-coding the default to `true` overrides any pre-set Config-literal value at parse time. The new contract is "admin is on by default; pass `--admin-enabled=false` to disable." Per project memory ([No backward compat](memory/feedback_no_backward_compat.md)) this trade-off is acceptable — games that genuinely need admin off can pass the flag or build a custom config wrapper.

- [ ] **Step 3: Delete `pkg/universe/admin_hash.go`**

Run: `git rm pkg/universe/admin_hash.go`
Expected: file removed.

- [ ] **Step 4: Verify it compiles**

Run: `go vet ./pkg/universe/...`
Expected: no output from the universe package; cross-package errors still surface from mmokit + 4node-basic.

- [ ] **Step 5: Do not commit yet** — see Task 11 batch commit.

---

## Task 10: Update `pkg/mmokit` to wire repo + auto-default ServerFactory

**Files:**
- Modify: `pkg/mmokit/admin.go`
- Modify: `pkg/mmokit/mmokit.go`

- [ ] **Step 1: Update `DefaultAdminServerFactory` in `pkg/mmokit/admin.go`**

Replace this block:

```go
		ops := make([]admin.OperatorConfig, 0, len(ac.Operators))
		for _, o := range ac.Operators {
			ops = append(ops, admin.OperatorConfig{
				Username:     o.Username,
				PasswordHash: o.PasswordHash,
				Grants:       o.Grants,
			})
		}
```

with:

```go
		// OperatorRepo is sourced from the cluster Postgres store. Build
		// panics earlier if DBStore is nil when Admin.Enabled is set, so
		// the dereference here is safe.
		operatorRepo := cfg.DBStore.AdminOperators()
```

In the `admin.NewServer(admin.ServerOpts{...})` literal, replace:

```go
			Config: admin.Config{
				BindAddr:   cfg.AdminListen,
				SessionTTL: ac.SessionTTL,
				LockoutMax: ac.LockoutMaxAttempts,
				LockoutWin: ac.LockoutWindow,
				AuditCap:   ac.AuditCap,
				Operators:  ops,
			},
```

with:

```go
			OperatorRepo: operatorRepo,
			Config: admin.Config{
				BindAddr:   cfg.AdminListen,
				SessionTTL: ac.SessionTTL,
				LockoutMax: ac.LockoutMaxAttempts,
				LockoutWin: ac.LockoutWindow,
				AuditCap:   ac.AuditCap,
			},
```

- [ ] **Step 2: Drop the `AdminOperatorConfig` alias**

In the same file, delete:

```go
// AdminOperatorConfig is the facade alias for universe.AdminOperatorConfig.
type AdminOperatorConfig = universe.AdminOperatorConfig
```

- [ ] **Step 3: Auto-default `Admin.ServerFactory` in `mmokit.New`**

Look at `pkg/mmokit/mmokit.go` to find the `New` function (it's the facade wrapper around `universe.New`). Locate the function and insert the following block immediately before the call to `universe.New(cfg)`:

If the existing code looks like:

```go
func New(cfg Config) *Process {
	return universe.New(cfg)
}
```

(check the actual function — it may be wrapped slightly differently), replace with:

```go
func New(cfg Config) *Process {
	// Auto-wire DefaultAdminServerFactory when the game enables admin
	// without supplying its own factory. Games can still pass a custom
	// factory; this only fills the nil case.
	if cfg.AdminListen != "" && cfg.Admin.Enabled && cfg.Admin.ServerFactory == nil {
		cfg.Admin.ServerFactory = DefaultAdminServerFactory()
	}
	return universe.New(cfg)
}
```

If `New` is currently a plain `var New = universe.New` alias (verify this — search `grep -n "^var New\|^func New" pkg/mmokit/mmokit.go`), convert it to the function form above.

- [ ] **Step 4: Verify it compiles**

Run: `go vet ./pkg/mmokit/... ./pkg/universe/...`
Expected: no output, exit 0.

Run: `go vet ./...`
Expected: errors only in `examples/4node-basic/main.go` (the `Admin: …` block still references the old types). Fixed in Task 11.

- [ ] **Step 5: Do not commit yet** — batch commit at end of Task 11.

---

## Task 11: Simplify `examples/4node-basic/main.go`

**Files:**
- Modify: `examples/4node-basic/main.go`

- [ ] **Step 1: Delete the `Admin:` block entirely**

Find:

```go
		// Admin dashboard at /admin/* on the AdminListen port (defaults to
		// :9101 when --admin-listen is set; --admin-enabled flips Enabled
		// at the flag layer too). The hash below is for password
		// "localdev"; regenerate with `./bin/server --admin-hash-password`
		// if you want a different one.
		Admin: mmokit.AdminConfig{
			Enabled:            true,
			SessionTTL:         8 * time.Hour,
			LockoutMaxAttempts: 5,
			LockoutWindow:      15 * time.Minute,
			Operators: []mmokit.AdminOperatorConfig{
				{
					Username:     "josh",
					PasswordHash: "$argon2id$v=19$m=65536,t=3,p=4$ArtLNjQQrFf3vzfkEQ7lXw$5xmMsnQJ5vxY6B7QieFNKOVX3HnedHA4uXdJIA+uZu0",
					Grants:       []string{"*.*"},
				},
			},
			ServerFactory: mmokit.DefaultAdminServerFactory(),
		},
```

Delete this block. Defaults handle the entire wiring.

- [ ] **Step 2: Remove the now-unused `time` import**

If `time` is no longer referenced in `examples/4node-basic/main.go`, remove it from the import block.

Verify: `grep -n "time\." examples/4node-basic/main.go`
Expected: no matches → safe to remove the import.

- [ ] **Step 3: Verify everything compiles**

Run: `go vet ./...`
Expected: no output, exit 0.

Run: `just build`
Expected: builds successfully into `bin/`.

- [ ] **Step 4: Commit Tasks 8–11 together**

```bash
git add pkg/universe/coordinator.go pkg/universe/bootstrap.go pkg/mmokit/admin.go pkg/mmokit/mmokit.go examples/4node-basic/main.go
git rm pkg/universe/admin_hash.go
git commit -m "admin: drop in-code operators + --admin-hash-password; AdminConfig self-defaults"
```

---

## Task 12: End-to-end verification

- [ ] **Step 1: Full test suite**

Run: `go test ./...`
Expected: all PASS.

- [ ] **Step 2: Pgtest suite**

Run: `just db-up` (if not already running) then `just test-pg`
Expected: all admin_operators + existing tests PASS.

- [ ] **Step 3: Manual smoke — fresh DB seeds default admin**

Run:

```bash
just db-reset
cd examples/4node-basic && just dev
```

In the server log, look for: `seeded default operator: admin / admin (CHANGE IN PRODUCTION)`

In a browser, visit `http://localhost:9101/admin` and log in with `admin` / `admin`. Confirm the dashboard loads.

(Don't claim the manual smoke is "done" here — the plan does not run the dev server itself. The implementer should run this step and verify the output, then check the box.)

- [ ] **Step 4: Manual smoke — re-running does not duplicate seed**

Quit and restart `just dev` (DB persists). Confirm:
- No "seeded default operator" log line on second start.
- `admin` / `admin` still logs in.
- `psql` query: `SELECT username, grants FROM admin_operators;` returns exactly one row.

- [ ] **Step 5: Commit any documentation drift**

If the smoke test surfaces a missed call site or doc reference (e.g. a CLAUDE.md mention of `--admin-hash-password`), grep for it:

```bash
grep -rn "admin-hash-password\|AdminOperatorConfig\|AdminConfig.Operators" .
```

Fix any stragglers in a single commit:

```bash
git commit -m "docs: clean up references to removed admin-hash-password flag"
```

---

## Self-Review

**1. Spec coverage:**
- ✅ Admin should have sane defaults → Task 8 (`Build()` defaults SessionTTL/Lockout/AuditCap) + Task 10 (`mmokit.New` defaults ServerFactory)
- ✅ Enabled by default → Task 9 (`--admin-enabled` flag default flipped to true) + Task 8 (Build doesn't error when AdminListen is non-empty)
- ✅ Default user + options in the DB → Tasks 1–5 (interface, migration, Postgres impl, mock, tests) + Tasks 6–7 (admin wires repo + seeds)
- ✅ Static admin/admin, printed on first run → Task 6 (`seedDefaultOperator` + log banner)
- ✅ User management via admin UI later → confirmed by dropping `--admin-hash-password` in Task 9; no CLI surface for create/delete operators today
- ✅ No backward compat → all in-code operator paths removed; old types deleted (per project memory [No backward compat](memory/feedback_no_backward_compat.md))

**2. Placeholder scan:**
No "TBD", "implement later", "similar to Task N" — every step has actual code, file paths, and commands.

**3. Type consistency:**
- `persist.AdminOperator` fields (`Username`, `PasswordHash`, `Grants []string`, `CreatedAt`, `UpdatedAt`) match exactly across Task 1, 3, 4, 5, 6, 7.
- `persist.AdminOperatorRepository` method signatures (`GetByUsername`, `Create`, `List`, `Delete`, `UpdatePasswordHash`, `Count`) match exactly across Task 1, 3, 4.
- `admin.ServerOpts.OperatorRepo` field name matches between Task 6 (definition) and Task 7 (test usage).
- `admin.Server.operatorRepo` field name matches between Task 6 (struct + NewServer assignment) and Task 7 (test literal).
- `seedDefaultOperator` function name matches between Task 6 (definition) and Task 6 (call site).

---

**Plan complete.** Tasks 1–12 deliver: sane `AdminConfig` defaults, `admin_operators` Postgres table seeded with a default `admin`/`admin` on empty DB, removal of `--admin-hash-password` and in-code operator literals, and a one-line `Admin: …` block (or nothing at all) in game `main.go`.
