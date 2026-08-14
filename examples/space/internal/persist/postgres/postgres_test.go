//go:build pgtest

package postgres

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	gamepersist "github.com/zenion/mmokit/examples/space/internal/persist"
	enginepg "github.com/zenion/mmokit/pkg/persist/postgres"
)

// openTestStore opens the live Postgres pointed at by POSTGRES_URL,
// running both the engine's and the game's migrations, and returns the
// game-side Store + pool. Each test starts clean: reset(t, pool)
// TRUNCATEs the engine tables CASCADE first (which clears the
// FK-dependent space.player_state) followed by the remaining space.*
// tables. Skips the test when POSTGRES_URL is unset.
func openTestStore(t *testing.T) (*Store, *pgxpool.Pool, func()) {
	t.Helper()
	url := os.Getenv("POSTGRES_URL")
	if url == "" {
		url = "postgres://mmo:mmo@localhost:5432/mmo?sslmode=disable"
	}
	ctx := context.Background()
	engineStore, err := enginepg.Open(ctx, url,
		enginepg.WithExtraMigrations(MigrationsFS(), MigrationsRoot, MigrationsLabel),
	)
	if err != nil {
		t.Skipf("Postgres not reachable at %q (%v); skipping integration test", url, err)
	}
	pool := engineStore.Pool()
	store := New(pool)
	reset(t, pool)
	cleanup := func() { engineStore.Close() }
	t.Cleanup(cleanup)
	return store, pool, cleanup
}

// reset TRUNCATEs every table touched by these tests. engine.players is
// truncated CASCADE so the FK-dependent space.player_state row is wiped
// transparently; the remaining space.* tables are independent and
// truncated explicitly.
func reset(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		TRUNCATE engine.players, engine.admin_operators RESTART IDENTITY CASCADE
	`); err != nil {
		t.Fatalf("truncate engine: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		TRUNCATE space.market_orders, space.market_trades, space.config RESTART IDENTITY
	`); err != nil {
		t.Fatalf("truncate space: %v", err)
	}
}

// seedEnginePlayer inserts an engine.players row for the given user_id +
// username so a game-side player_state row referencing it can satisfy
// the FK. Returns the user_id so callers can key their PlayerStateSnapshots
// off it.
func seedEnginePlayer(t *testing.T, pool *pgxpool.Pool, username string) uuid.UUID {
	t.Helper()
	uid := uuid.New()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO engine.players (user_id, username) VALUES ($1, $2)`, uid, username); err != nil {
		t.Fatalf("seed engine player %q: %v", username, err)
	}
	return uid
}

// ---------------------------------------------------------------------------
// PlayerStateRepository tests
// ---------------------------------------------------------------------------

func TestPlayerStateRepo_RoundTrip(t *testing.T) {
	store, pool, _ := openTestStore(t)
	ctx := context.Background()
	repo := store.PlayerState()

	uid := seedEnginePlayer(t, pool, "alice")

	snap := &gamepersist.PlayerStateSnapshot{
		UserID:     uid,
		Currencies: map[uint32]int64{1: 500, 2: 1200},
		Cargo:      map[uint32]int32{10: 3, 11: 7},
		Bank:       map[uint32]int32{20: 1, 21: 99},
		Equipment: gamepersist.EquipmentSnapshot{
			Weapon1:  101,
			Weapon2:  102,
			Shield:   201,
			Thruster: 301,
		},
	}
	if err := repo.SaveBatch(ctx, []*gamepersist.PlayerStateSnapshot{snap}); err != nil {
		t.Fatalf("SaveBatch: %v", err)
	}

	loaded, err := repo.Load(ctx, uid)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.UserID != uid {
		t.Errorf("UserID = %v, want %v", loaded.UserID, uid)
	}
	if loaded.Currencies[1] != 500 || loaded.Currencies[2] != 1200 {
		t.Errorf("Currencies = %v, want %v", loaded.Currencies, snap.Currencies)
	}
	if loaded.Cargo[10] != 3 || loaded.Cargo[11] != 7 {
		t.Errorf("Cargo = %v, want %v", loaded.Cargo, snap.Cargo)
	}
	if loaded.Bank[20] != 1 || loaded.Bank[21] != 99 {
		t.Errorf("Bank = %v, want %v", loaded.Bank, snap.Bank)
	}
	if loaded.Equipment != snap.Equipment {
		t.Errorf("Equipment = %+v, want %+v", loaded.Equipment, snap.Equipment)
	}
}

func TestPlayerStateRepo_LoadNotFound(t *testing.T) {
	store, _, _ := openTestStore(t)
	ctx := context.Background()
	repo := store.PlayerState()

	snap, err := repo.Load(ctx, uuid.New())
	if !errors.Is(err, gamepersist.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if snap != nil {
		t.Fatalf("expected nil snapshot, got %+v", snap)
	}
}

func TestPlayerStateRepo_LoadAll(t *testing.T) {
	store, pool, _ := openTestStore(t)
	ctx := context.Background()
	repo := store.PlayerState()

	type seed struct {
		username string
		uid      uuid.UUID
		currency int64
	}
	seeds := []seed{
		{username: "alice", currency: 10},
		{username: "bob", currency: 20},
		{username: "charlie", currency: 30},
	}
	for i := range seeds {
		seeds[i].uid = seedEnginePlayer(t, pool, seeds[i].username)
	}
	snapshots := make([]*gamepersist.PlayerStateSnapshot, 0, len(seeds))
	for _, sd := range seeds {
		snapshots = append(snapshots, &gamepersist.PlayerStateSnapshot{
			UserID:     sd.uid,
			Currencies: map[uint32]int64{1: sd.currency},
		})
	}
	// SaveBatch contract: sort by UserID.
	sortSnapshots(snapshots)
	if err := repo.SaveBatch(ctx, snapshots); err != nil {
		t.Fatalf("SaveBatch: %v", err)
	}

	seen := make(map[uuid.UUID]int64)
	if err := repo.LoadAll(ctx, func(s *gamepersist.PlayerStateSnapshot) error {
		seen[s.UserID] = s.Currencies[1]
		return nil
	}); err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if len(seen) != 3 {
		t.Fatalf("LoadAll returned %d players, want 3", len(seen))
	}
	for _, sd := range seeds {
		got, ok := seen[sd.uid]
		if !ok {
			t.Errorf("LoadAll missed user %q (uid=%s)", sd.username, sd.uid)
			continue
		}
		if got != sd.currency {
			t.Errorf("LoadAll[%s]: got currency=%d, want %d", sd.username, got, sd.currency)
		}
	}
}

func sortSnapshots(snapshots []*gamepersist.PlayerStateSnapshot) {
	for i := 1; i < len(snapshots); i++ {
		for j := i; j > 0 && snapshots[j-1].UserID.String() > snapshots[j].UserID.String(); j-- {
			snapshots[j-1], snapshots[j] = snapshots[j], snapshots[j-1]
		}
	}
}

func TestPlayerStateRepo_SaveBatchEmpty(t *testing.T) {
	store, _, _ := openTestStore(t)
	ctx := context.Background()
	repo := store.PlayerState()

	if err := repo.SaveBatch(ctx, nil); err != nil {
		t.Fatalf("SaveBatch(nil): %v", err)
	}
	if err := repo.SaveBatch(ctx, []*gamepersist.PlayerStateSnapshot{}); err != nil {
		t.Fatalf("SaveBatch(empty): %v", err)
	}
}

func TestPlayerStateRepo_SaveBatchUpsert(t *testing.T) {
	store, pool, _ := openTestStore(t)
	ctx := context.Background()
	repo := store.PlayerState()

	uid := seedEnginePlayer(t, pool, "dave")

	first := &gamepersist.PlayerStateSnapshot{
		UserID:     uid,
		Currencies: map[uint32]int64{1: 100},
		Cargo:      map[uint32]int32{10: 1},
		Bank:       map[uint32]int32{20: 5},
		Equipment:  gamepersist.EquipmentSnapshot{Weapon1: 1},
	}
	if err := repo.SaveBatch(ctx, []*gamepersist.PlayerStateSnapshot{first}); err != nil {
		t.Fatalf("first SaveBatch: %v", err)
	}

	second := &gamepersist.PlayerStateSnapshot{
		UserID:     uid,
		Currencies: map[uint32]int64{1: 999},
		Cargo:      map[uint32]int32{10: 42, 11: 7},
		Bank:       map[uint32]int32{20: 50},
		Equipment:  gamepersist.EquipmentSnapshot{Weapon1: 2, Shield: 3},
	}
	if err := repo.SaveBatch(ctx, []*gamepersist.PlayerStateSnapshot{second}); err != nil {
		t.Fatalf("second SaveBatch: %v", err)
	}

	loaded, err := repo.Load(ctx, uid)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Currencies[1] != 999 {
		t.Errorf("Currencies[1] = %d, want 999 (upsert should overwrite)", loaded.Currencies[1])
	}
	if loaded.Cargo[10] != 42 || loaded.Cargo[11] != 7 {
		t.Errorf("Cargo = %v, want {10:42, 11:7} (upsert should overwrite)", loaded.Cargo)
	}
	if loaded.Bank[20] != 50 {
		t.Errorf("Bank[20] = %d, want 50 (upsert should overwrite)", loaded.Bank[20])
	}
	if loaded.Equipment.Weapon1 != 2 || loaded.Equipment.Shield != 3 {
		t.Errorf("Equipment = %+v, want {Weapon1:2, Shield:3} (upsert should overwrite)", loaded.Equipment)
	}
}

func TestPlayerStateRepo_FK_CascadesOnEngineDelete(t *testing.T) {
	store, pool, _ := openTestStore(t)
	ctx := context.Background()
	repo := store.PlayerState()

	uid := seedEnginePlayer(t, pool, "eve")
	if err := repo.SaveBatch(ctx, []*gamepersist.PlayerStateSnapshot{{
		UserID:     uid,
		Currencies: map[uint32]int64{1: 42},
	}}); err != nil {
		t.Fatalf("SaveBatch: %v", err)
	}

	// Sanity: row exists before the engine row is removed.
	if _, err := repo.Load(ctx, uid); err != nil {
		t.Fatalf("Load before delete: %v", err)
	}

	if _, err := pool.Exec(ctx,
		`DELETE FROM engine.players WHERE user_id = $1`, uid); err != nil {
		t.Fatalf("delete engine player: %v", err)
	}

	// FK ON DELETE CASCADE must have wiped the game state row.
	if _, err := repo.Load(ctx, uid); !errors.Is(err, gamepersist.ErrNotFound) {
		t.Fatalf("expected ErrNotFound after engine row delete (CASCADE), got %v", err)
	}
}

// ---------------------------------------------------------------------------
// MarketRepository tests
// ---------------------------------------------------------------------------

func TestMarketRepo_PlaceOrderAndLoadMaxID(t *testing.T) {
	store, _, _ := openTestStore(t)
	ctx := context.Background()
	repo := store.Market()

	now := time.Now().UTC().Truncate(time.Microsecond)
	order := &gamepersist.OrderRecord{
		ID:         42,
		Side:       0,
		Owner:      "alice",
		LocationID: 1,
		ItemID:     100,
		Price:      500,
		Quantity:   10,
		OrigQty:    10,
		CreatedAt:  now,
	}
	if err := repo.PlaceOrder(ctx, order); err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}

	maxID, err := repo.LoadMaxOrderID(ctx)
	if err != nil {
		t.Fatalf("LoadMaxOrderID: %v", err)
	}
	if maxID != 42 {
		t.Errorf("LoadMaxOrderID = %d, want 42", maxID)
	}
}

func TestMarketRepo_UpdateQuantity(t *testing.T) {
	store, _, _ := openTestStore(t)
	ctx := context.Background()
	repo := store.Market()

	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := repo.PlaceOrder(ctx, &gamepersist.OrderRecord{
		ID: 1, Side: 1, Owner: "alice", LocationID: 1, ItemID: 100,
		Price: 500, Quantity: 10, OrigQty: 10, CreatedAt: now,
	}); err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}

	if err := repo.UpdateQuantity(ctx, 1, 3); err != nil {
		t.Fatalf("UpdateQuantity: %v", err)
	}

	var got int32
	if err := store.pool.QueryRow(ctx,
		`SELECT quantity FROM space.market_orders WHERE id = $1`, uint64(1),
	).Scan(&got); err != nil {
		t.Fatalf("verify quantity: %v", err)
	}
	if got != 3 {
		t.Errorf("quantity = %d, want 3", got)
	}
}

func TestMarketRepo_DeleteOrder(t *testing.T) {
	store, _, _ := openTestStore(t)
	ctx := context.Background()
	repo := store.Market()

	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := repo.PlaceOrder(ctx, &gamepersist.OrderRecord{
		ID: 7, Side: 0, Owner: "alice", LocationID: 1, ItemID: 100,
		Price: 100, Quantity: 1, OrigQty: 1, CreatedAt: now,
	}); err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}

	if err := repo.DeleteOrder(ctx, 7); err != nil {
		t.Fatalf("DeleteOrder: %v", err)
	}

	var n int
	if err := store.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM space.market_orders WHERE id = $1`, uint64(7),
	).Scan(&n); err != nil {
		t.Fatalf("count after delete: %v", err)
	}
	if n != 0 {
		t.Errorf("row count after delete = %d, want 0", n)
	}
}

func TestMarketRepo_DeleteOrderUnknownID(t *testing.T) {
	store, _, _ := openTestStore(t)
	ctx := context.Background()
	repo := store.Market()

	if err := repo.DeleteOrder(ctx, 99999); err != nil {
		t.Fatalf("DeleteOrder of unknown id should be a no-op, got %v", err)
	}
}

func TestMarketRepo_RecordTrade(t *testing.T) {
	store, _, _ := openTestStore(t)
	ctx := context.Background()
	repo := store.Market()

	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := repo.RecordTrade(ctx, &gamepersist.TradeRecord{
		ItemID: 100, LocationID: 1, Price: 250, Quantity: 5,
		Buyer: "alice", Seller: "bob", OccurredAt: now,
	}); err != nil {
		t.Fatalf("RecordTrade: %v", err)
	}
	if err := repo.RecordTrade(ctx, &gamepersist.TradeRecord{
		ItemID: 101, LocationID: 1, Price: 999, Quantity: 1,
		Buyer: "alice", Seller: "charlie", OccurredAt: now,
	}); err != nil {
		t.Fatalf("RecordTrade: %v", err)
	}

	var n int
	if err := store.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM space.market_trades`,
	).Scan(&n); err != nil {
		t.Fatalf("count trades: %v", err)
	}
	if n != 2 {
		t.Errorf("trade row count = %d, want 2", n)
	}
}

func TestMarketRepo_LoadActiveOrdersFiltersExpired(t *testing.T) {
	store, _, _ := openTestStore(t)
	ctx := context.Background()
	repo := store.Market()

	now := time.Now().UTC().Truncate(time.Microsecond)
	// Expired order — ExpiresAt is set in the past.
	if err := repo.PlaceOrder(ctx, &gamepersist.OrderRecord{
		ID: 1, Side: 0, Owner: "alice", LocationID: 1, ItemID: 100,
		Price: 100, Quantity: 5, OrigQty: 5, CreatedAt: now.Add(-2 * time.Hour),
		ExpiresAt: now.Add(-1 * time.Hour),
	}); err != nil {
		t.Fatalf("PlaceOrder expired: %v", err)
	}
	// Never-expires order — ExpiresAt is the zero time.Time value.
	if err := repo.PlaceOrder(ctx, &gamepersist.OrderRecord{
		ID: 2, Side: 1, Owner: "bob", LocationID: 1, ItemID: 100,
		Price: 200, Quantity: 3, OrigQty: 3, CreatedAt: now,
	}); err != nil {
		t.Fatalf("PlaceOrder never-expires: %v", err)
	}

	var loaded []*gamepersist.OrderRecord
	if err := repo.LoadActiveOrders(ctx, func(o *gamepersist.OrderRecord) error {
		loaded = append(loaded, o)
		return nil
	}); err != nil {
		t.Fatalf("LoadActiveOrders: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("LoadActiveOrders returned %d orders, want 1 (expired must be filtered)", len(loaded))
	}
	if loaded[0].ID != 2 {
		t.Errorf("LoadActiveOrders returned id=%d, want 2", loaded[0].ID)
	}
	if !loaded[0].ExpiresAt.IsZero() {
		t.Errorf("never-expires order should roundtrip with zero ExpiresAt, got %v", loaded[0].ExpiresAt)
	}
}

func TestMarketRepo_LoadActiveOrdersOrderedByID(t *testing.T) {
	store, _, _ := openTestStore(t)
	ctx := context.Background()
	repo := store.Market()

	now := time.Now().UTC().Truncate(time.Microsecond)
	// Insert in non-monotonic order: 5, 1, 3, 2, 4.
	for _, id := range []uint64{5, 1, 3, 2, 4} {
		if err := repo.PlaceOrder(ctx, &gamepersist.OrderRecord{
			ID: id, Side: 0, Owner: "alice", LocationID: 1, ItemID: 100,
			Price: 100, Quantity: 1, OrigQty: 1, CreatedAt: now,
		}); err != nil {
			t.Fatalf("PlaceOrder id=%d: %v", id, err)
		}
	}

	var ids []uint64
	if err := repo.LoadActiveOrders(ctx, func(o *gamepersist.OrderRecord) error {
		ids = append(ids, o.ID)
		return nil
	}); err != nil {
		t.Fatalf("LoadActiveOrders: %v", err)
	}
	want := []uint64{1, 2, 3, 4, 5}
	if len(ids) != len(want) {
		t.Fatalf("LoadActiveOrders returned %d orders, want %d", len(ids), len(want))
	}
	for i, w := range want {
		if ids[i] != w {
			t.Errorf("[%d] id = %d, want %d (must be id ASC)", i, ids[i], w)
		}
	}
}

// ---------------------------------------------------------------------------
// ConfigRepository tests
// ---------------------------------------------------------------------------

func TestConfigRepo_LoadEmpty(t *testing.T) {
	store, _, _ := openTestStore(t)
	ctx := context.Background()
	repo := store.Config()

	snap, err := repo.Load(ctx)
	if !errors.Is(err, gamepersist.ErrNotFound) {
		t.Fatalf("expected ErrNotFound on empty table, got %v", err)
	}
	if snap != nil {
		t.Fatalf("expected nil snapshot, got %+v", snap)
	}
}

func TestConfigRepo_RoundTrip(t *testing.T) {
	store, _, _ := openTestStore(t)
	ctx := context.Background()
	repo := store.Config()

	want := &gamepersist.ConfigSnapshot{
		Data:    []byte(`{"shield_regen_rate":1.5,"tax_rate":0.05}`),
		Version: 7,
	}
	if err := repo.Save(ctx, want); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := repo.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(got.Data) != string(want.Data) {
		t.Errorf("Data = %q, want %q", got.Data, want.Data)
	}
	if got.Version != want.Version {
		t.Errorf("Version = %d, want %d", got.Version, want.Version)
	}
}

func TestConfigRepo_SaveOverwrites(t *testing.T) {
	store, _, _ := openTestStore(t)
	ctx := context.Background()
	repo := store.Config()

	if err := repo.Save(ctx, &gamepersist.ConfigSnapshot{
		Data:    []byte(`{"v":1}`),
		Version: 1,
	}); err != nil {
		t.Fatalf("first Save: %v", err)
	}
	if err := repo.Save(ctx, &gamepersist.ConfigSnapshot{
		Data:    []byte(`{"v":2}`),
		Version: 2,
	}); err != nil {
		t.Fatalf("second Save: %v", err)
	}

	got, err := repo.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(got.Data) != `{"v":2}` {
		t.Errorf("Data = %q, want {\"v\":2} (second Save should overwrite)", got.Data)
	}
	if got.Version != 2 {
		t.Errorf("Version = %d, want 2 (second Save should overwrite)", got.Version)
	}
}
