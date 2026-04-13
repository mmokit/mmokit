//go:build pgtest

package postgres

import (
	"context"
	"errors"
	"maps"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/zenion/mmoserver/pkg/persist"
)

// openTestStore opens the live Postgres pointed at by POSTGRES_URL.
// Skips the test if the env var is unset. TRUNCATEs all tables so each
// test starts clean.
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
		TRUNCATE players, game_config, market_orders, market_trades RESTART IDENTITY
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
		Username:  "alice",
		CellID:    "cell_2_1",
		PosX:      123.5,
		PosY:      -45.25,
		Currencies: map[uint32]int64{1: 1000, 2: 50},
		Cargo:      map[uint32]int32{100: 5, 101: 10},
		Bank:       map[uint32]int32{200: 25},
		Equipment: persist.EquipmentSnapshot{
			Weapon1:  1,
			Weapon2:  2,
			Shield:   3,
			Thruster: 4,
		},
		CreatedAt: now.Add(-1 * time.Hour),
		LastLogin: now,
	}
	if err := repo.SaveBatch(ctx, []*persist.PlayerSnapshot{snap}); err != nil {
		t.Fatalf("SaveBatch: %v", err)
	}

	loaded, err := repo.Load(ctx, "alice")
	if err != nil {
		t.Fatalf("Load: %v", err)
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
	if !maps.Equal(loaded.Currencies, snap.Currencies) {
		t.Errorf("Currencies = %v, want %v", loaded.Currencies, snap.Currencies)
	}
	if !maps.Equal(loaded.Cargo, snap.Cargo) {
		t.Errorf("Cargo = %v, want %v", loaded.Cargo, snap.Cargo)
	}
	if !maps.Equal(loaded.Bank, snap.Bank) {
		t.Errorf("Bank = %v, want %v", loaded.Bank, snap.Bank)
	}
	if loaded.Equipment != snap.Equipment {
		t.Errorf("Equipment = %+v, want %+v", loaded.Equipment, snap.Equipment)
	}
}

func TestPlayerRepo_LoadNotFound(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Players()

	snap, err := repo.Load(ctx, "nonexistent")
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

	players := []*persist.PlayerSnapshot{
		{Username: "alice", CellID: "cell_0_0", PosX: 1, PosY: 2, CreatedAt: time.Now().UTC().Truncate(time.Microsecond), LastLogin: time.Now().UTC().Truncate(time.Microsecond)},
		{Username: "bob", CellID: "cell_1_0", PosX: 3, PosY: 4, CreatedAt: time.Now().UTC().Truncate(time.Microsecond), LastLogin: time.Now().UTC().Truncate(time.Microsecond)},
		{Username: "charlie", CellID: "cell_1_1", PosX: 5, PosY: 6, CreatedAt: time.Now().UTC().Truncate(time.Microsecond), LastLogin: time.Now().UTC().Truncate(time.Microsecond)},
	}
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
	sort.Slice(loaded, func(i, j int) bool {
		return loaded[i].Username < loaded[j].Username
	})
	for i, want := range players {
		if loaded[i].Username != want.Username {
			t.Errorf("[%d] Username = %q, want %q", i, loaded[i].Username, want.Username)
		}
		if loaded[i].CellID != want.CellID {
			t.Errorf("[%d] CellID = %q, want %q", i, loaded[i].CellID, want.CellID)
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
	snapshots := []*persist.PlayerSnapshot{
		{Username: "zoe", CellID: "cell_0_0", CreatedAt: now, LastLogin: now},
		{Username: "alice", CellID: "cell_0_0", CreatedAt: now, LastLogin: now},
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
	snap := &persist.PlayerSnapshot{
		Username:   "dave",
		CellID:     "cell_0_0",
		Currencies: map[uint32]int64{1: 100},
		CreatedAt:  now,
		LastLogin:  now,
	}
	if err := repo.SaveBatch(ctx, []*persist.PlayerSnapshot{snap}); err != nil {
		t.Fatalf("first SaveBatch: %v", err)
	}

	snap2 := &persist.PlayerSnapshot{
		Username:   "dave",
		CellID:     "cell_0_0",
		Currencies: map[uint32]int64{1: 200},
		CreatedAt:  now,
		LastLogin:  now,
	}
	if err := repo.SaveBatch(ctx, []*persist.PlayerSnapshot{snap2}); err != nil {
		t.Fatalf("second SaveBatch: %v", err)
	}

	loaded, err := repo.Load(ctx, "dave")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Currencies[1] != 200 {
		t.Errorf("Currencies[1] = %d, want 200 (upsert should overwrite, not accumulate)", loaded.Currencies[1])
	}
}

func TestPlayerRepo_SaveBatchEmptyMaps(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Players()

	now := time.Now().UTC().Truncate(time.Microsecond)
	snap := &persist.PlayerSnapshot{
		Username:   "eve",
		CellID:     "cell_0_0",
		Currencies: nil,
		Cargo:      nil,
		Bank:       nil,
		CreatedAt:  now,
		LastLogin:  now,
	}
	if err := repo.SaveBatch(ctx, []*persist.PlayerSnapshot{snap}); err != nil {
		t.Fatalf("SaveBatch: %v", err)
	}

	loaded, err := repo.Load(ctx, "eve")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Currencies == nil {
		t.Error("Currencies is nil, want non-nil empty map")
	}
	if loaded.Cargo == nil {
		t.Error("Cargo is nil, want non-nil empty map")
	}
	if loaded.Bank == nil {
		t.Error("Bank is nil, want non-nil empty map")
	}
	if len(loaded.Currencies) != 0 {
		t.Errorf("Currencies len = %d, want 0", len(loaded.Currencies))
	}
	if len(loaded.Cargo) != 0 {
		t.Errorf("Cargo len = %d, want 0", len(loaded.Cargo))
	}
	if len(loaded.Bank) != 0 {
		t.Errorf("Bank len = %d, want 0", len(loaded.Bank))
	}
}

// ---------------------------------------------------------------------------
// MarketRepository tests
// ---------------------------------------------------------------------------

func makeOrder() *persist.OrderRecord {
	return &persist.OrderRecord{
		Side:       0,
		Owner:      "testplayer",
		LocationID: 1,
		ItemID:     42,
		Price:      1000,
		Quantity:   10,
		OrigQty:    10,
		CreatedAt:  time.Now().UTC().Truncate(time.Microsecond),
	}
}

func TestMarketRepo_PlaceOrderAssignsID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Market()

	id1, err := repo.PlaceOrder(ctx, makeOrder())
	if err != nil {
		t.Fatalf("PlaceOrder 1: %v", err)
	}
	if id1 == 0 {
		t.Fatal("first PlaceOrder returned ID=0, want > 0")
	}

	id2, err := repo.PlaceOrder(ctx, makeOrder())
	if err != nil {
		t.Fatalf("PlaceOrder 2: %v", err)
	}
	if id2 <= id1 {
		t.Errorf("second ID %d should be > first ID %d (monotonic)", id2, id1)
	}
}

func TestMarketRepo_UpdateQuantity(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Market()

	id, err := repo.PlaceOrder(ctx, makeOrder())
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if err := repo.UpdateQuantity(ctx, id, 5); err != nil {
		t.Fatalf("UpdateQuantity: %v", err)
	}

	var loaded *persist.OrderRecord
	if err := repo.LoadActiveOrders(ctx, func(o *persist.OrderRecord) error {
		if o.ID == id {
			loaded = o
		}
		return nil
	}); err != nil {
		t.Fatalf("LoadActiveOrders: %v", err)
	}
	if loaded == nil {
		t.Fatal("order not found after UpdateQuantity")
	}
	if loaded.Quantity != 5 {
		t.Errorf("Quantity = %d, want 5", loaded.Quantity)
	}
}

func TestMarketRepo_DeleteOrder(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Market()

	id, err := repo.PlaceOrder(ctx, makeOrder())
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if err := repo.DeleteOrder(ctx, id); err != nil {
		t.Fatalf("DeleteOrder: %v", err)
	}

	var count int
	if err := repo.LoadActiveOrders(ctx, func(*persist.OrderRecord) error {
		count++
		return nil
	}); err != nil {
		t.Fatalf("LoadActiveOrders: %v", err)
	}
	if count != 0 {
		t.Errorf("LoadActiveOrders returned %d orders after delete, want 0", count)
	}
}

func TestMarketRepo_DeleteOrderUnknownID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Market()

	if err := repo.DeleteOrder(ctx, 99999999); err != nil {
		t.Fatalf("DeleteOrder with unknown ID: expected nil, got %v", err)
	}
}

func TestMarketRepo_RecordTrade(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Market()

	trade := &persist.TradeRecord{
		ItemID:     42,
		LocationID: 1,
		Price:      1000,
		Quantity:   5,
		Buyer:      "alice",
		Seller:     "bob",
		OccurredAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	if err := repo.RecordTrade(ctx, trade); err != nil {
		t.Fatalf("RecordTrade: %v", err)
	}

	var count int
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM market_trades`).Scan(&count); err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 1 {
		t.Errorf("market_trades count = %d, want 1", count)
	}
}

func TestMarketRepo_LoadActiveOrdersFiltersExpired(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Market()

	now := time.Now().UTC()

	// Already expired — should NOT appear.
	expired := makeOrder()
	expired.ExpiresAt = now.Add(-1 * time.Hour)
	if _, err := repo.PlaceOrder(ctx, expired); err != nil {
		t.Fatalf("PlaceOrder expired: %v", err)
	}

	// Never expires (zero ExpiresAt) — should appear.
	neverExpires := makeOrder()
	if _, err := repo.PlaceOrder(ctx, neverExpires); err != nil {
		t.Fatalf("PlaceOrder neverExpires: %v", err)
	}

	// Future expiry — should appear.
	futureExpiry := makeOrder()
	futureExpiry.ExpiresAt = now.Add(1 * time.Hour)
	if _, err := repo.PlaceOrder(ctx, futureExpiry); err != nil {
		t.Fatalf("PlaceOrder futureExpiry: %v", err)
	}

	var count int
	if err := repo.LoadActiveOrders(ctx, func(*persist.OrderRecord) error {
		count++
		return nil
	}); err != nil {
		t.Fatalf("LoadActiveOrders: %v", err)
	}
	if count != 2 {
		t.Errorf("LoadActiveOrders returned %d orders, want 2 (expired filtered out)", count)
	}
}

func TestMarketRepo_LoadActiveOrdersOrderedByID(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Market()

	for i := 0; i < 3; i++ {
		if _, err := repo.PlaceOrder(ctx, makeOrder()); err != nil {
			t.Fatalf("PlaceOrder %d: %v", i, err)
		}
	}

	var ids []uint64
	if err := repo.LoadActiveOrders(ctx, func(o *persist.OrderRecord) error {
		ids = append(ids, o.ID)
		return nil
	}); err != nil {
		t.Fatalf("LoadActiveOrders: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 orders, got %d", len(ids))
	}
	for i := 1; i < len(ids); i++ {
		if ids[i] <= ids[i-1] {
			t.Errorf("IDs not strictly monotonic at index %d: %v", i, ids)
		}
	}
}

// ---------------------------------------------------------------------------
// ConfigRepository tests
// ---------------------------------------------------------------------------

func TestConfigRepo_LoadEmpty(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Config()

	snap, err := repo.Load(ctx)
	if !errors.Is(err, persist.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if snap != nil {
		t.Fatalf("expected nil snapshot, got %+v", snap)
	}
}

func TestConfigRepo_RoundTrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Config()

	in := &persist.ConfigSnapshot{
		Data:    []byte("hello world"),
		Version: 42,
	}
	if err := repo.Save(ctx, in); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := repo.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if string(loaded.Data) != string(in.Data) {
		t.Errorf("Data = %q, want %q", loaded.Data, in.Data)
	}
	if loaded.Version != in.Version {
		t.Errorf("Version = %d, want %d", loaded.Version, in.Version)
	}
}

func TestConfigRepo_SaveOverwrites(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	repo := s.Config()

	if err := repo.Save(ctx, &persist.ConfigSnapshot{Data: []byte("v1"), Version: 1}); err != nil {
		t.Fatalf("Save v1: %v", err)
	}
	if err := repo.Save(ctx, &persist.ConfigSnapshot{Data: []byte("v2"), Version: 2}); err != nil {
		t.Fatalf("Save v2: %v", err)
	}

	loaded, err := repo.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.Version != 2 {
		t.Errorf("Version = %d, want 2 (second save should overwrite)", loaded.Version)
	}
	if string(loaded.Data) != "v2" {
		t.Errorf("Data = %q, want %q", loaded.Data, "v2")
	}
}

func TestConfigRepo_SingletonEnforced(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	_, err := s.pool.Exec(ctx,
		`INSERT INTO game_config (id, data, version) VALUES (2, $1, $2)`,
		[]byte{}, int64(0),
	)
	if err == nil {
		t.Fatal("expected CHECK constraint violation for id=2, got nil error")
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "game_config_singleton") && !strings.Contains(msg, "check") {
		t.Errorf("error message %q should mention game_config_singleton or check", err.Error())
	}
}
