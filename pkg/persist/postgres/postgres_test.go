//go:build pgtest

package postgres

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"

	"github.com/mmokit/mmokit/pkg/persist"
)

// openTestStore opens the live Postgres pointed at by POSTGRES_URL.
// Skips the test if the env var is unset. TRUNCATEs all engine tables
// so each test starts clean. CASCADE drops any rows in game-side tables
// (e.g. space.player_state) that FK to engine.players — harmless when
// the game schema is absent.
func openTestStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("POSTGRES_URL")
	if url == "" {
		t.Skip("POSTGRES_URL not set; skipping Postgres integration test")
	}
	ctx := context.Background()
	s, err := Open(ctx, url)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(s.Close)
	if _, err := s.pool.Exec(ctx, `
		TRUNCATE engine.players, engine.admin_operators RESTART IDENTITY CASCADE
	`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s
}

// ---------------------------------------------------------------------------
// PlayerRepository tests
// ---------------------------------------------------------------------------

func TestPlayerRepo_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Players()

	now := time.Now().UTC().Truncate(time.Microsecond)
	snap := &persist.PlayerSnapshot{
		UserID:    uuid.New(),
		Username:  "alice",
		CellID:    "cell_2_1",
		PosX:      123.5,
		PosY:      -45.25,
		CreatedAt: now.Add(-1 * time.Hour),
		LastLogin: now,
	}
	if err := repo.SaveBatch(ctx, []*persist.PlayerSnapshot{snap}); err != nil {
		t.Fatalf("SaveBatch: %v", err)
	}

	loaded, err := repo.Load(ctx, snap.UserID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.UserID != snap.UserID {
		t.Errorf("UserID = %v, want %v", loaded.UserID, snap.UserID)
	}
	if loaded.Username != snap.Username {
		t.Errorf("Username = %q, want %q", loaded.Username, snap.Username)
	}
	if loaded.CellID != snap.CellID {
		t.Errorf("CellID = %q, want %q", loaded.CellID, snap.CellID)
	}
	if loaded.PosX != snap.PosX {
		t.Errorf("PosX = %v, want %v", loaded.PosX, snap.PosX)
	}
	if loaded.PosY != snap.PosY {
		t.Errorf("PosY = %v, want %v", loaded.PosY, snap.PosY)
	}
	if !loaded.LastLogin.Equal(snap.LastLogin) {
		t.Errorf("LastLogin = %v, want %v", loaded.LastLogin, snap.LastLogin)
	}
	if !loaded.CreatedAt.Equal(snap.CreatedAt) {
		t.Errorf("CreatedAt = %v, want %v", loaded.CreatedAt, snap.CreatedAt)
	}
}

func TestPlayerRepo_LoadByUsername(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Players()

	now := time.Now().UTC().Truncate(time.Microsecond)
	snap := &persist.PlayerSnapshot{
		UserID:    uuid.New(),
		Username:  "bob",
		CellID:    "cell_0_0",
		CreatedAt: now,
		LastLogin: now,
	}
	if err := repo.SaveBatch(ctx, []*persist.PlayerSnapshot{snap}); err != nil {
		t.Fatalf("SaveBatch: %v", err)
	}

	loaded, err := repo.LoadByUsername(ctx, "bob")
	if err != nil {
		t.Fatalf("LoadByUsername: %v", err)
	}
	if loaded.UserID != snap.UserID {
		t.Errorf("UserID = %v, want %v", loaded.UserID, snap.UserID)
	}
	if loaded.Username != "bob" {
		t.Errorf("Username = %q, want %q", loaded.Username, "bob")
	}

	if _, err := repo.LoadByUsername(ctx, "nobody"); !errors.Is(err, persist.ErrNotFound) {
		t.Fatalf("LoadByUsername(unknown): expected ErrNotFound, got %v", err)
	}
}

func TestPlayerRepo_LoadNotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Players()

	snap, err := repo.Load(ctx, uuid.New())
	if !errors.Is(err, persist.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if snap != nil {
		t.Fatalf("expected nil snapshot, got %+v", snap)
	}
}

func TestPlayerRepo_LoadAll(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Players()

	now := time.Now().UTC().Truncate(time.Microsecond)
	uidAlice, uidBob, uidCharlie := uuid.New(), uuid.New(), uuid.New()
	players := []*persist.PlayerSnapshot{
		{UserID: uidAlice, Username: "alice", CellID: "cell_0_0", PosX: 1, PosY: 2, CreatedAt: now, LastLogin: now},
		{UserID: uidBob, Username: "bob", CellID: "cell_1_0", PosX: 3, PosY: 4, CreatedAt: now, LastLogin: now},
		{UserID: uidCharlie, Username: "charlie", CellID: "cell_1_1", PosX: 5, PosY: 6, CreatedAt: now, LastLogin: now},
	}
	// Pre-sort by UserID so SaveBatch's contract is satisfied.
	sort.Slice(players, func(i, j int) bool {
		return players[i].UserID.String() < players[j].UserID.String()
	})
	if err := repo.SaveBatch(ctx, players); err != nil {
		t.Fatalf("SaveBatch: %v", err)
	}

	var loaded []*persist.PlayerSnapshot
	if err := repo.LoadAll(ctx, func(s *persist.PlayerSnapshot) error {
		loaded = append(loaded, s)
		return nil
	}); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}

	if len(loaded) != 3 {
		t.Fatalf("LoadAll returned %d players, want 3", len(loaded))
	}
	byUser := make(map[uuid.UUID]*persist.PlayerSnapshot, len(loaded))
	for _, p := range loaded {
		byUser[p.UserID] = p
	}
	for _, want := range players {
		got := byUser[want.UserID]
		if got == nil {
			t.Errorf("LoadAll missed user %s (username=%s)", want.UserID, want.Username)
			continue
		}
		if got.Username != want.Username {
			t.Errorf("[%s] Username = %q, want %q", want.UserID, got.Username, want.Username)
		}
		if got.CellID != want.CellID {
			t.Errorf("[%s] CellID = %q, want %q", want.UserID, got.CellID, want.CellID)
		}
	}
}

func TestPlayerRepo_LoadAllEmpty(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Players()

	callCount := 0
	err := repo.LoadAll(ctx, func(*persist.PlayerSnapshot) error {
		callCount++
		return nil
	})
	if err != nil {
		t.Fatalf("LoadAll on empty table: %v", err)
	}
	if callCount != 0 {
		t.Fatalf("fn called %d times, want 0", callCount)
	}
}

func TestPlayerRepo_SaveBatchEmpty(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Players()

	if err := repo.SaveBatch(ctx, nil); err != nil {
		t.Fatalf("SaveBatch(nil): %v", err)
	}
	if err := repo.SaveBatch(ctx, []*persist.PlayerSnapshot{}); err != nil {
		t.Fatalf("SaveBatch(empty): %v", err)
	}
}

func TestPlayerRepo_SaveBatchUnsorted(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Players()

	now := time.Now().UTC()
	// Pick two UUIDs whose string form is in deliberately reversed order.
	high := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	low := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	snapshots := []*persist.PlayerSnapshot{
		{UserID: high, Username: "zoe", CellID: "cell_0_0", CreatedAt: now, LastLogin: now},
		{UserID: low, Username: "alice", CellID: "cell_0_0", CreatedAt: now, LastLogin: now},
	}
	err := repo.SaveBatch(ctx, snapshots)
	if err == nil {
		t.Fatal("expected error for unsorted input, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "deadlock") && !strings.Contains(msg, "sorted") {
		t.Errorf("error message %q should mention deadlock prevention or sorted", msg)
	}
}

func TestPlayerRepo_SaveBatchUpsert(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Players()

	now := time.Now().UTC().Truncate(time.Microsecond)
	userID := uuid.New()
	snap := &persist.PlayerSnapshot{
		UserID:    userID,
		Username:  "dave",
		CellID:    "cell_0_0",
		PosX:      10,
		PosY:      20,
		CreatedAt: now,
		LastLogin: now,
	}
	if err := repo.SaveBatch(ctx, []*persist.PlayerSnapshot{snap}); err != nil {
		t.Fatalf("first SaveBatch: %v", err)
	}

	snap2 := &persist.PlayerSnapshot{
		UserID:    userID,
		Username:  "dave-renamed", // verify the username column upserts too
		CellID:    "cell_1_1",
		PosX:      99,
		PosY:      77,
		CreatedAt: now,
		LastLogin: now,
	}
	if err := repo.SaveBatch(ctx, []*persist.PlayerSnapshot{snap2}); err != nil {
		t.Fatalf("second SaveBatch: %v", err)
	}

	loaded, err := repo.Load(ctx, userID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Username != "dave-renamed" {
		t.Errorf("Username = %q, want %q (upsert should overwrite)", loaded.Username, "dave-renamed")
	}
	if loaded.CellID != "cell_1_1" {
		t.Errorf("CellID = %q, want %q (upsert should overwrite)", loaded.CellID, "cell_1_1")
	}
	if loaded.PosX != 99 || loaded.PosY != 77 {
		t.Errorf("Pos = (%v, %v), want (99, 77) (upsert should overwrite)", loaded.PosX, loaded.PosY)
	}
}

func TestPlayerRepo_DebugFlagsRoundtrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Players()

	const username = "alice-debug"
	userID := uuid.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := repo.SaveBatch(ctx, []*persist.PlayerSnapshot{{
		UserID:    userID,
		Username:  username,
		CellID:    "cell_0_0",
		PosX:      100,
		PosY:      100,
		CreatedAt: now,
		LastLogin: now,
	}}); err != nil {
		t.Fatalf("seed SaveBatch: %v", err)
	}

	// Default is empty (column DEFAULT '[]'::jsonb).
	got, err := repo.LoadDebugFlags(ctx, username)
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("default debug_flags: got %v, want []", got)
	}

	// Save a set.
	want := []string{"topology", "perf"}
	if err := repo.SaveDebugFlags(ctx, username, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err = repo.LoadDebugFlags(ctx, username)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 2 || got[0] != "topology" || got[1] != "perf" {
		t.Errorf("roundtrip: got %v, want %v", got, want)
	}

	// Clear (nil should normalize to []).
	if err := repo.SaveDebugFlags(ctx, username, nil); err != nil {
		t.Fatalf("save nil: %v", err)
	}
	got, err = repo.LoadDebugFlags(ctx, username)
	if err != nil {
		t.Fatalf("load after clear: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("after clear: got %v, want []", got)
	}

	// Unknown user: Load returns ErrNotFound and Save returns ErrNotFound.
	if _, err := repo.LoadDebugFlags(ctx, "no-such-user"); !errors.Is(err, persist.ErrNotFound) {
		t.Errorf("LoadDebugFlags unknown: got %v, want ErrNotFound", err)
	}
	if err := repo.SaveDebugFlags(ctx, "no-such-user", []string{"topology"}); !errors.Is(err, persist.ErrNotFound) {
		t.Errorf("SaveDebugFlags unknown: got %v, want ErrNotFound", err)
	}
}

func TestPlayerRepo_LoadIncludesDebugFlags(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Players()

	const username = "alice-debug-load"
	userID := uuid.New()
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := repo.SaveBatch(ctx, []*persist.PlayerSnapshot{{
		UserID:     userID,
		Username:   username,
		CellID:     "cell_0_0",
		PosX:       100,
		PosY:       100,
		CreatedAt:  now,
		LastLogin:  now,
		DebugFlags: []string{"topology"},
	}}); err != nil {
		t.Fatalf("SaveBatch: %v", err)
	}
	snap, err := repo.Load(ctx, userID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(snap.DebugFlags) != 1 || snap.DebugFlags[0] != "topology" {
		t.Errorf("Load did not roundtrip DebugFlags: got %v", snap.DebugFlags)
	}
}

// ---------------------------------------------------------------------------
// ExtraMigrations test
// ---------------------------------------------------------------------------

func TestExtraMigrationsAppliedAfterEngine(t *testing.T) {
	url := os.Getenv("POSTGRES_URL")
	if url == "" {
		t.Skip("POSTGRES_URL not set; skipping Postgres integration test")
	}

	extras := fstest.MapFS{
		"001_extra.up.sql":   &fstest.MapFile{Data: []byte("CREATE TABLE IF NOT EXISTS extra_test (id INT PRIMARY KEY);")},
		"001_extra.down.sql": &fstest.MapFile{Data: []byte("DROP TABLE IF EXISTS extra_test;")},
	}

	ctx := context.Background()
	s, err := Open(ctx, url, WithExtraMigrations(extras, ".", "test_extra_migrations"))
	if err != nil {
		t.Fatalf("Open with extra migrations: %v", err)
	}

	// Drop the table afterwards so repeated test runs start clean.
	// Must run drops BEFORE Close() in a single t.Cleanup (defer Close
	// would run first and Close the pool, making the drops no-op).
	t.Cleanup(func() {
		ctx := context.Background()
		_, _ = s.pool.Exec(ctx, "DROP TABLE IF EXISTS extra_test")
		_, _ = s.pool.Exec(ctx, "DROP TABLE IF EXISTS schema_migrations_test_extra_migrations")
		s.Close()
	})

	var n int
	if err := s.pool.QueryRow(ctx, "SELECT COUNT(*) FROM extra_test").Scan(&n); err != nil {
		t.Fatalf("extra_test table missing after extra migration: %v", err)
	}
}

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
