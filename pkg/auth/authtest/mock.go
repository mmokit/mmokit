// Package authtest provides an in-memory auth.Repository for tests.
package authtest

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/zenion/mmoserver/pkg/auth"
)

type RepoMock struct {
	mu        sync.Mutex
	users     map[uuid.UUID]auth.User
	byName    map[string]uuid.UUID
	passwords map[uuid.UUID]auth.PasswordCredential
	sessions  map[string]auth.Session // keyed by string(tokenHash)
	audit     []auth.AuditEvent
}

func NewMock() *RepoMock {
	return &RepoMock{
		users:     map[uuid.UUID]auth.User{},
		byName:    map[string]uuid.UUID{},
		passwords: map[uuid.UUID]auth.PasswordCredential{},
		sessions:  map[string]auth.Session{},
	}
}

var _ auth.Repository = (*RepoMock)(nil)

func (m *RepoMock) CreateUser(_ context.Context, u auth.User, password string) (auth.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.byName[u.Username]; exists {
		return auth.User{}, auth.ErrUsernameTaken
	}
	if u.UserID == uuid.Nil {
		u.UserID = uuid.New()
	}
	u.Status = "active"
	u.CreatedAt = time.Now()
	u.UpdatedAt = u.CreatedAt
	m.users[u.UserID] = u
	m.byName[u.Username] = u.UserID
	m.passwords[u.UserID] = auth.PasswordCredential{
		UserID: u.UserID, PasswordHash: password, HashAlgorithm: "argon2id", ChangedAt: time.Now(),
	}
	return u, nil
}

func (m *RepoMock) GetUserByUsername(_ context.Context, name string) (auth.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.byName[name]
	if !ok {
		return auth.User{}, auth.ErrUserNotFound
	}
	return m.users[id], nil
}

func (m *RepoMock) GetUserByID(_ context.Context, id uuid.UUID) (auth.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return auth.User{}, auth.ErrUserNotFound
	}
	return u, nil
}

func (m *RepoMock) UpdateUserLogin(_ context.Context, id uuid.UUID, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return auth.ErrUserNotFound
	}
	u.LastLoginAt = at
	u.FailedAttempts = 0
	u.LockedUntil = time.Time{}
	u.UpdatedAt = time.Now()
	m.users[id] = u
	return nil
}

func (m *RepoMock) IncrementFailedAttempts(_ context.Context, id uuid.UUID, threshold int, dur time.Duration) (int, time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return 0, time.Time{}, auth.ErrUserNotFound
	}
	u.FailedAttempts++
	if u.FailedAttempts >= threshold {
		u.LockedUntil = time.Now().Add(dur)
	}
	u.UpdatedAt = time.Now()
	m.users[id] = u
	return u.FailedAttempts, u.LockedUntil, nil
}

func (m *RepoMock) ResetFailedAttempts(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return auth.ErrUserNotFound
	}
	u.FailedAttempts = 0
	u.LockedUntil = time.Time{}
	u.UpdatedAt = time.Now()
	m.users[id] = u
	return nil
}

func (m *RepoMock) SetUserStatus(_ context.Context, id uuid.UUID, status string, locked time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return auth.ErrUserNotFound
	}
	u.Status = status
	u.LockedUntil = locked
	u.UpdatedAt = time.Now()
	m.users[id] = u
	return nil
}

func (m *RepoMock) GetPassword(_ context.Context, id uuid.UUID) (auth.PasswordCredential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.passwords[id]
	if !ok {
		return auth.PasswordCredential{}, auth.ErrUserNotFound
	}
	return p, nil
}

func (m *RepoMock) UpdatePassword(_ context.Context, id uuid.UUID, newHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.passwords[id]
	if !ok {
		return auth.ErrUserNotFound
	}
	p.PasswordHash = newHash
	p.ChangedAt = time.Now()
	m.passwords[id] = p
	return nil
}

func (m *RepoMock) CreateSession(_ context.Context, s auth.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s.IssuedAt = time.Now()
	s.LastUsedAt = s.IssuedAt
	m.sessions[string(s.TokenHash)] = s
	return nil
}

func (m *RepoMock) GetSession(_ context.Context, h []byte) (auth.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[string(h)]
	if !ok {
		return auth.Session{}, auth.ErrSessionNotFound
	}
	return s, nil
}

func (m *RepoMock) SlideSession(_ context.Context, h []byte, newExp time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[string(h)]
	if !ok {
		return auth.ErrSessionNotFound
	}
	s.ExpiresAt = newExp
	s.LastUsedAt = time.Now()
	m.sessions[string(h)] = s
	return nil
}

func (m *RepoMock) RevokeSession(_ context.Context, h []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[string(h)]
	if !ok {
		return auth.ErrSessionNotFound
	}
	s.RevokedAt = time.Now()
	m.sessions[string(h)] = s
	return nil
}

func (m *RepoMock) RevokeAllSessionsExcept(_ context.Context, id uuid.UUID, keep []byte) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k, s := range m.sessions {
		if s.UserID == id && k != string(keep) && s.RevokedAt.IsZero() {
			s.RevokedAt = time.Now()
			m.sessions[k] = s
			n++
		}
	}
	return n, nil
}

func (m *RepoMock) RevokeAllSessionsForUser(_ context.Context, id uuid.UUID) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for k, s := range m.sessions {
		if s.UserID == id && s.RevokedAt.IsZero() {
			s.RevokedAt = time.Now()
			m.sessions[k] = s
			n++
		}
	}
	return n, nil
}

func (m *RepoMock) ListActiveSessions(_ context.Context, id uuid.UUID) ([]auth.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.Session
	for _, s := range m.sessions {
		if s.UserID == id && s.RevokedAt.IsZero() {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *RepoMock) DeleteExpiredSessions(_ context.Context, retention time.Duration) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	cutoff := time.Now().Add(-retention)
	for k, s := range m.sessions {
		if !s.RevokedAt.IsZero() || s.ExpiresAt.Before(cutoff) {
			delete(m.sessions, k)
			n++
		}
	}
	return n, nil
}

func (m *RepoMock) DeleteOldAuditRows(_ context.Context, olderThan time.Duration) (int, error) {
	return 0, nil // mock keeps audit in-memory; reaper test irrelevant here
}

func (m *RepoMock) Audit(_ context.Context, ev auth.AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audit = append(m.audit, ev)
	return nil
}

func (m *RepoMock) RecentAudit(_ context.Context, id uuid.UUID, limit int) ([]auth.AuditEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []auth.AuditEvent
	for i := len(m.audit) - 1; i >= 0 && len(out) < limit; i-- {
		if m.audit[i].UserID == id {
			out = append(out, m.audit[i])
		}
	}
	return out, nil
}

// AuditEvents returns all recorded events for test inspection.
func (m *RepoMock) AuditEvents() []auth.AuditEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]auth.AuditEvent, len(m.audit))
	copy(out, m.audit)
	return out
}
