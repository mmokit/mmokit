//go:build pgtest

package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	persistpg "github.com/zenion/mmoserver/pkg/persist/postgres"

	"github.com/zenion/mmoserver/pkg/auth"
)

// openTestRepo opens a Repo backed by the test Postgres instance.
// The auth migrations are applied via WithExtraMigrations. The auth_*
// tables are truncated on every call so each test starts with a clean slate.
func openTestRepo(t *testing.T) *Repo {
	t.Helper()
	pool := openTestPool(t)
	return New(pool)
}

func openTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("POSTGRES_URL")
	if url == "" {
		t.Skip("POSTGRES_URL not set; skipping Postgres integration test")
	}
	ctx := context.Background()
	store, err := persistpg.Open(ctx, url,
		persistpg.WithExtraMigrations(Migrations, "migrations", "auth"),
	)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(store.Close)

	pool := store.Pool()
	if _, err := pool.Exec(ctx,
		`TRUNCATE auth_audit_log, auth_sessions, auth_identities, auth_passwords, auth_users`,
	); err != nil {
		t.Fatalf("truncate auth tables: %v", err)
	}
	return pool
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestCreateUserAndFetch(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	u, err := repo.CreateUser(ctx, auth.User{Username: "alice"}, "argon2id-hash-here")
	if err != nil {
		t.Fatal(err)
	}
	if u.UserID == uuid.Nil {
		t.Fatal("user_id is zero after CreateUser")
	}
	if u.Username != "alice" {
		t.Fatalf("username = %q, want %q", u.Username, "alice")
	}

	got, err := repo.GetUserByUsername(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != u.UserID {
		t.Fatalf("GetUserByUsername: user_id mismatch: %v vs %v", got.UserID, u.UserID)
	}

	got2, err := repo.GetUserByID(ctx, u.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if got2.Username != "alice" {
		t.Fatalf("GetUserByID: username = %q, want %q", got2.Username, "alice")
	}
}

func TestDuplicateUsername(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	if _, err := repo.CreateUser(ctx, auth.User{Username: "bob"}, "hash1"); err != nil {
		t.Fatalf("first CreateUser: %v", err)
	}
	_, err := repo.CreateUser(ctx, auth.User{Username: "bob"}, "hash2")
	if err != auth.ErrUsernameTaken {
		t.Fatalf("expected ErrUsernameTaken, got %v", err)
	}
}

func TestSessionLifecycle(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	u, err := repo.CreateUser(ctx, auth.User{Username: "carol"}, "hash")
	if err != nil {
		t.Fatal(err)
	}

	tokenHash := []byte("test-token-hash-32-bytes-padded!!")
	sess := auth.Session{
		TokenHash: tokenHash,
		UserID:    u.UserID,
		ExpiresAt: time.Now().Add(time.Hour),
	}
	if err := repo.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetSession(ctx, tokenHash)
	if err != nil {
		t.Fatal(err)
	}
	if got.UserID != u.UserID {
		t.Fatalf("session user mismatch: got %v, want %v", got.UserID, u.UserID)
	}

	newExp := time.Now().Add(2 * time.Hour)
	if err := repo.SlideSession(ctx, tokenHash, newExp); err != nil {
		t.Fatalf("SlideSession: %v", err)
	}

	if err := repo.RevokeSession(ctx, tokenHash); err != nil {
		t.Fatalf("RevokeSession: %v", err)
	}

	got2, err := repo.GetSession(ctx, tokenHash)
	if err != nil {
		t.Fatalf("GetSession after revoke: %v", err)
	}
	// epoch is the zero sentinel we use for NULL revoked_at; after revoke it must be non-epoch.
	epoch := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	if got2.RevokedAt.Equal(epoch) || got2.RevokedAt.IsZero() {
		t.Fatalf("revoked_at should be set, got %v", got2.RevokedAt)
	}
}

func TestIncrementAndLockout(t *testing.T) {
	repo := openTestRepo(t)
	ctx := context.Background()

	u, err := repo.CreateUser(ctx, auth.User{Username: "dave"}, "hash")
	if err != nil {
		t.Fatal(err)
	}

	var locked time.Time
	for i := 1; i <= 5; i++ {
		n, l, err := repo.IncrementFailedAttempts(ctx, u.UserID, 5, 15*time.Minute)
		if err != nil {
			t.Fatalf("attempt %d IncrementFailedAttempts: %v", i, err)
		}
		if n != i {
			t.Fatalf("attempt %d: count = %d, want %d", i, n, i)
		}
		locked = l
	}

	epoch := time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	if locked.Equal(epoch) || locked.Before(time.Now()) {
		t.Fatalf("expected locked_until in future after 5 attempts, got %v", locked)
	}

	if err := repo.ResetFailedAttempts(ctx, u.UserID); err != nil {
		t.Fatalf("ResetFailedAttempts: %v", err)
	}

	got, err := repo.GetUserByID(ctx, u.UserID)
	if err != nil {
		t.Fatal(err)
	}
	if got.FailedAttempts != 0 {
		t.Fatalf("FailedAttempts after reset = %d, want 0", got.FailedAttempts)
	}
}
