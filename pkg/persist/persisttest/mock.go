// Package persisttest provides in-memory implementations of the
// engine-side persist repository interfaces for game-domain tests.
// Real database integration coverage lives in
// pkg/persist/postgres/postgres_test.go (gated on -tags=pgtest).
package persisttest

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/zenion/mmokit/pkg/persist"
)

// PlayerRepoMock is an in-memory PlayerRepository. Primary index is
// keyed on UserID; byUsername is a secondary index for LoadByUsername
// and the username-keyed debug-flag helpers (the underlying schema
// has UNIQUE(username) so the lookup is still single-row).
type PlayerRepoMock struct {
	mu         sync.Mutex
	rows       map[uuid.UUID]*persist.PlayerSnapshot
	byUsername map[string]uuid.UUID
}

func NewPlayerRepoMock() *PlayerRepoMock {
	return &PlayerRepoMock{
		rows:       make(map[uuid.UUID]*persist.PlayerSnapshot),
		byUsername: make(map[string]uuid.UUID),
	}
}

func (m *PlayerRepoMock) Load(ctx context.Context, userID uuid.UUID) (*persist.PlayerSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.rows[userID]
	if !ok {
		return nil, persist.ErrNotFound
	}
	return clonePlayer(rec), nil
}

func (m *PlayerRepoMock) LoadByUsername(ctx context.Context, username string) (*persist.PlayerSnapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	uid, ok := m.byUsername[username]
	if !ok {
		return nil, persist.ErrNotFound
	}
	rec, ok := m.rows[uid]
	if !ok {
		return nil, persist.ErrNotFound
	}
	return clonePlayer(rec), nil
}

func (m *PlayerRepoMock) LoadAll(ctx context.Context, fn func(*persist.PlayerSnapshot) error) error {
	m.mu.Lock()
	keys := make([]uuid.UUID, 0, len(m.rows))
	for k := range m.rows {
		keys = append(keys, k)
	}
	// Stable iteration order — UUIDs sorted by string form.
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
		// Maintain the byUsername index — drop any prior username for
		// this user_id (rename support), then re-point.
		if prev, ok := m.rows[s.UserID]; ok && prev.Username != s.Username {
			delete(m.byUsername, prev.Username)
		}
		m.rows[s.UserID] = clonePlayer(s)
		m.byUsername[s.Username] = s.UserID
	}
	return nil
}

// SaveBatchTx delegates to SaveBatch; the in-memory mock has no tx scope.
func (m *PlayerRepoMock) SaveBatchTx(ctx context.Context, _ pgx.Tx, snapshots []*persist.PlayerSnapshot) error {
	return m.SaveBatch(ctx, snapshots)
}

// LoadDebugFlags returns a copy of the persisted debug-flag list for
// the given user. Returns (nil, ErrNotFound) if the user doesn't exist;
// (empty, nil) if the user exists but no flags are set.
func (m *PlayerRepoMock) LoadDebugFlags(ctx context.Context, username string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	uid, ok := m.byUsername[username]
	if !ok {
		return nil, persist.ErrNotFound
	}
	rec := m.rows[uid]
	if rec == nil {
		return nil, persist.ErrNotFound
	}
	if len(rec.DebugFlags) == 0 {
		return []string{}, nil
	}
	return slices.Clone(rec.DebugFlags), nil
}

// SaveDebugFlags writes the flag list to the user's snapshot.
//
// The Postgres implementation requires the row to exist (we can't
// fabricate a user_id). Mirror that semantic here: return ErrNotFound
// for unknown usernames.
func (m *PlayerRepoMock) SaveDebugFlags(ctx context.Context, username string, flags []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	uid, ok := m.byUsername[username]
	if !ok {
		return persist.ErrNotFound
	}
	rec, ok := m.rows[uid]
	if !ok {
		return persist.ErrNotFound
	}
	if len(flags) == 0 {
		rec.DebugFlags = nil
	} else {
		rec.DebugFlags = slices.Clone(flags)
	}
	return nil
}

// LoadAllDebugFlags returns a copy of every user's flag list whose
// list is non-empty, keyed by username.
func (m *PlayerRepoMock) LoadAllDebugFlags(ctx context.Context) (map[string][]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string][]string)
	for _, rec := range m.rows {
		if rec == nil || len(rec.DebugFlags) == 0 {
			continue
		}
		out[rec.Username] = slices.Clone(rec.DebugFlags)
	}
	return out, nil
}

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

// Compile-time interface assertions.
var (
	_ persist.PlayerRepository        = (*PlayerRepoMock)(nil)
	_ persist.AdminOperatorRepository = (*AdminOperatorRepoMock)(nil)
)

// clonePlayer returns a deep copy of a PlayerSnapshot. Used by the
// mock to prevent test code from mutating the stored copy.
func clonePlayer(src *persist.PlayerSnapshot) *persist.PlayerSnapshot {
	if src == nil {
		return nil
	}
	cp := *src
	if src.DebugFlags != nil {
		cp.DebugFlags = slices.Clone(src.DebugFlags)
	}
	return &cp
}
