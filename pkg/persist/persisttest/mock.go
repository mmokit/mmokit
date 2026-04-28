// Package persisttest provides in-memory implementations of the
// persist repository interfaces for game-domain tests. Real database
// integration coverage lives in pkg/persist/postgres/postgres_test.go
// (gated on -tags=pgtest).
package persisttest

import (
	"context"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/zenion/mmoserver/pkg/persist"
)

// PlayerRepoMock is an in-memory PlayerRepository.
type PlayerRepoMock struct {
	mu   sync.Mutex
	rows map[string]*persist.PlayerSnapshot
}

func NewPlayerRepoMock() *PlayerRepoMock {
	return &PlayerRepoMock{rows: make(map[string]*persist.PlayerSnapshot)}
}

func (m *PlayerRepoMock) Load(ctx context.Context, username string) (*persist.PlayerSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.rows[username]
	if !ok {
		return nil, persist.ErrNotFound
	}
	return clonePlayer(rec), nil
}

func (m *PlayerRepoMock) LoadAll(ctx context.Context, fn func(*persist.PlayerSnapshot) error) error {
	m.mu.Lock()
	keys := make([]string, 0, len(m.rows))
	for k := range m.rows {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	snapshots := make([]*persist.PlayerSnapshot, len(keys))
	for i, k := range keys {
		snapshots[i] = clonePlayer(m.rows[k])
	}
	m.mu.Unlock()
	for _, s := range snapshots {
		if err := fn(s); err != nil {
			return err
		}
	}
	return nil
}

func (m *PlayerRepoMock) SaveBatch(ctx context.Context, snapshots []*persist.PlayerSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range snapshots {
		m.rows[s.Username] = clonePlayer(s)
	}
	return nil
}

// LoadDebugFlags returns a copy of the persisted debug-flag list for
// the given user. Returns (nil, ErrNotFound) if the user doesn't exist;
// (empty, nil) if the user exists but no flags are set.
func (m *PlayerRepoMock) LoadDebugFlags(ctx context.Context, username string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.rows[username]
	if !ok {
		return nil, persist.ErrNotFound
	}
	if len(rec.DebugFlags) == 0 {
		return []string{}, nil
	}
	return slices.Clone(rec.DebugFlags), nil
}

// SaveDebugFlags writes the flag list to the user's snapshot. No-ops if
// the user doesn't exist, mirroring the Postgres UPDATE semantics.
func (m *PlayerRepoMock) SaveDebugFlags(ctx context.Context, username string, flags []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.rows[username]
	if !ok {
		return nil
	}
	if len(flags) == 0 {
		rec.DebugFlags = nil
	} else {
		rec.DebugFlags = slices.Clone(flags)
	}
	return nil
}

// MarketRepoMock is an in-memory MarketRepository. Tracks the
// highest order id seen so LoadMaxOrderID can return it for orderbook
// counter recovery.
type MarketRepoMock struct {
	mu     sync.Mutex
	maxID  uint64
	orders map[uint64]*persist.OrderRecord
	trades []*persist.TradeRecord
}

func NewMarketRepoMock() *MarketRepoMock {
	return &MarketRepoMock{
		orders: make(map[uint64]*persist.OrderRecord),
	}
}

func (m *MarketRepoMock) PlaceOrder(ctx context.Context, o *persist.OrderRecord) error {
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

func (m *MarketRepoMock) RecordTrade(ctx context.Context, t *persist.TradeRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *t
	m.trades = append(m.trades, &cp)
	return nil
}

func (m *MarketRepoMock) LoadActiveOrders(ctx context.Context, fn func(*persist.OrderRecord) error) error {
	m.mu.Lock()
	ids := make([]uint64, 0, len(m.orders))
	for id := range m.orders {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	snapshot := make([]*persist.OrderRecord, len(ids))
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
	rec *persist.ConfigSnapshot
}

func NewConfigRepoMock() *ConfigRepoMock {
	return &ConfigRepoMock{}
}

func (m *ConfigRepoMock) Load(ctx context.Context) (*persist.ConfigSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rec == nil {
		return nil, persist.ErrNotFound
	}
	cp := *m.rec
	cp.Data = append([]byte(nil), m.rec.Data...)
	return &cp, nil
}

func (m *ConfigRepoMock) Save(ctx context.Context, snapshot *persist.ConfigSnapshot) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *snapshot
	cp.Data = append([]byte(nil), snapshot.Data...)
	m.rec = &cp
	return nil
}

// Compile-time interface assertions.
var (
	_ persist.PlayerRepository = (*PlayerRepoMock)(nil)
	_ persist.MarketRepository = (*MarketRepoMock)(nil)
	_ persist.ConfigRepository = (*ConfigRepoMock)(nil)
)

// clonePlayer returns a deep copy of a PlayerSnapshot. Used by the
// mock to prevent test code from mutating the stored copy.
func clonePlayer(src *persist.PlayerSnapshot) *persist.PlayerSnapshot {
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
	if src.DebugFlags != nil {
		cp.DebugFlags = slices.Clone(src.DebugFlags)
	}
	return &cp
}
