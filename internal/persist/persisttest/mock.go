// Package persisttest provides in-memory implementations of the
// game-side persist repository interfaces (MarketRepository,
// ConfigRepository, PlayerStateRepository) for game-domain unit tests
// that don't want to spin up Postgres. Real DB coverage lives in
// internal/persist/postgres/postgres_test.go (gated on -tags=pgtest).
package persisttest

import (
	"context"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	gamepersist "github.com/zenion/mmokit/internal/persist"
)

// MarketRepoMock is an in-memory MarketRepository. Tracks the
// highest order id seen so LoadMaxOrderID can return it for orderbook
// counter recovery.
type MarketRepoMock struct {
	mu     sync.Mutex
	maxID  uint64
	orders map[uint64]*gamepersist.OrderRecord
	trades []*gamepersist.TradeRecord
}

func NewMarketRepoMock() *MarketRepoMock {
	return &MarketRepoMock{
		orders: make(map[uint64]*gamepersist.OrderRecord),
	}
}

func (m *MarketRepoMock) PlaceOrder(ctx context.Context, o *gamepersist.OrderRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *o
	m.orders[o.ID] = &cp
	if o.ID > m.maxID {
		m.maxID = o.ID
	}
	return nil
}

func (m *MarketRepoMock) LoadMaxOrderID(ctx context.Context) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.maxID, nil
}

func (m *MarketRepoMock) UpdateQuantity(ctx context.Context, id uint64, newQty int32) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if o, ok := m.orders[id]; ok {
		o.Quantity = newQty
	}
	return nil
}

func (m *MarketRepoMock) DeleteOrder(ctx context.Context, id uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.orders, id)
	return nil
}

func (m *MarketRepoMock) RecordTrade(ctx context.Context, t *gamepersist.TradeRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *t
	m.trades = append(m.trades, &cp)
	return nil
}

func (m *MarketRepoMock) LoadActiveOrders(ctx context.Context, fn func(*gamepersist.OrderRecord) error) error {
	m.mu.Lock()
	ids := make([]uint64, 0, len(m.orders))
	for id := range m.orders {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	snapshot := make([]*gamepersist.OrderRecord, len(ids))
	for i, id := range ids {
		cp := *m.orders[id]
		snapshot[i] = &cp
	}
	m.mu.Unlock()
	now := time.Now()
	for _, o := range snapshot {
		if !o.ExpiresAt.IsZero() && !o.ExpiresAt.After(now) {
			continue
		}
		if err := fn(o); err != nil {
			return err
		}
	}
	return nil
}

// ConfigRepoMock is an in-memory ConfigRepository.
type ConfigRepoMock struct {
	mu  sync.Mutex
	rec *gamepersist.ConfigSnapshot
}

func NewConfigRepoMock() *ConfigRepoMock {
	return &ConfigRepoMock{}
}

func (m *ConfigRepoMock) Load(ctx context.Context) (*gamepersist.ConfigSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rec == nil {
		return nil, gamepersist.ErrNotFound
	}
	cp := *m.rec
	cp.Data = append([]byte(nil), m.rec.Data...)
	return &cp, nil
}

func (m *ConfigRepoMock) Save(ctx context.Context, snapshot *gamepersist.ConfigSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *snapshot
	cp.Data = append([]byte(nil), snapshot.Data...)
	m.rec = &cp
	return nil
}

// PlayerStateRepoMock is an in-memory PlayerStateRepository, keyed by
// UserID (mirroring the Postgres PK).
type PlayerStateRepoMock struct {
	mu   sync.Mutex
	rows map[uuid.UUID]*gamepersist.PlayerStateSnapshot
}

func NewPlayerStateRepoMock() *PlayerStateRepoMock {
	return &PlayerStateRepoMock{rows: make(map[uuid.UUID]*gamepersist.PlayerStateSnapshot)}
}

func (m *PlayerStateRepoMock) Load(ctx context.Context, userID uuid.UUID) (*gamepersist.PlayerStateSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.rows[userID]
	if !ok {
		return nil, gamepersist.ErrNotFound
	}
	return clonePlayerState(rec), nil
}

func (m *PlayerStateRepoMock) LoadAll(ctx context.Context, fn func(*gamepersist.PlayerStateSnapshot) error) error {
	m.mu.Lock()
	keys := make([]uuid.UUID, 0, len(m.rows))
	for k := range m.rows {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b uuid.UUID) int {
		as, bs := a.String(), b.String()
		switch {
		case as < bs:
			return -1
		case as > bs:
			return 1
		default:
			return 0
		}
	})
	snapshots := make([]*gamepersist.PlayerStateSnapshot, len(keys))
	for i, k := range keys {
		snapshots[i] = clonePlayerState(m.rows[k])
	}
	m.mu.Unlock()
	for _, s := range snapshots {
		if err := fn(s); err != nil {
			return err
		}
	}
	return nil
}

func (m *PlayerStateRepoMock) SaveBatch(ctx context.Context, snapshots []*gamepersist.PlayerStateSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range snapshots {
		m.rows[s.UserID] = clonePlayerState(s)
	}
	return nil
}

// SaveBatchTx delegates to SaveBatch; the in-memory mock has no tx scope.
func (m *PlayerStateRepoMock) SaveBatchTx(ctx context.Context, _ pgx.Tx, snapshots []*gamepersist.PlayerStateSnapshot) error {
	return m.SaveBatch(ctx, snapshots)
}

// Compile-time interface assertions.
var (
	_ gamepersist.MarketRepository      = (*MarketRepoMock)(nil)
	_ gamepersist.ConfigRepository      = (*ConfigRepoMock)(nil)
	_ gamepersist.PlayerStateRepository = (*PlayerStateRepoMock)(nil)
)

func clonePlayerState(src *gamepersist.PlayerStateSnapshot) *gamepersist.PlayerStateSnapshot {
	if src == nil {
		return nil
	}
	cp := *src
	if src.Currencies != nil {
		cp.Currencies = maps.Clone(src.Currencies)
	}
	if src.Cargo != nil {
		cp.Cargo = maps.Clone(src.Cargo)
	}
	if src.Bank != nil {
		cp.Bank = maps.Clone(src.Bank)
	}
	return &cp
}
