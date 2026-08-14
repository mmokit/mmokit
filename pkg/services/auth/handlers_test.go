package auth

import (
	"context"
	"net/netip"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mmokit/mmokit/pkg/logger"
	"github.com/mmokit/mmokit/pkg/ops"
	"github.com/mmokit/mmokit/pkg/service"
)

type inMemCapKey struct {
	UserID     uuid.UUID
	Capability string
}

// inMemRepo is a local in-memory Repository for handler tests. It mirrors
// authtest.RepoMock but lives in package auth so we avoid the import cycle
// (authtest imports auth; package auth tests cannot import authtest).
type inMemRepo struct {
	mu        sync.Mutex
	users     map[uuid.UUID]User
	byName    map[string]uuid.UUID
	passwords map[uuid.UUID]PasswordCredential
	sessions  map[string]Session // keyed by string(tokenHash)
	audit     []AuditEvent
	caps      map[inMemCapKey]Capability
}

func newInMemRepo() *inMemRepo {
	return &inMemRepo{
		users:     map[uuid.UUID]User{},
		byName:    map[string]uuid.UUID{},
		passwords: map[uuid.UUID]PasswordCredential{},
		sessions:  map[string]Session{},
		caps:      map[inMemCapKey]Capability{},
	}
}

var _ Repository = (*inMemRepo)(nil)

func (m *inMemRepo) CreateUser(_ context.Context, u User, password string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, exists := m.byName[u.Username]; exists {
		return User{}, ErrUsernameTaken
	}
	if u.UserID == uuid.Nil {
		u.UserID = uuid.New()
	}
	u.Status = "active"
	u.CreatedAt = time.Now()
	u.UpdatedAt = u.CreatedAt
	m.users[u.UserID] = u
	m.byName[u.Username] = u.UserID
	m.passwords[u.UserID] = PasswordCredential{
		UserID: u.UserID, PasswordHash: password, HashAlgorithm: "argon2id", ChangedAt: time.Now(),
	}
	return u, nil
}

func (m *inMemRepo) GetUserByUsername(_ context.Context, name string) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.byName[name]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return m.users[id], nil
}

func (m *inMemRepo) GetUserByID(_ context.Context, id uuid.UUID) (User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return User{}, ErrUserNotFound
	}
	return u, nil
}

func (m *inMemRepo) UpdateUserLogin(_ context.Context, id uuid.UUID, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return ErrUserNotFound
	}
	u.LastLoginAt = at
	u.FailedAttempts = 0
	u.LockedUntil = time.Time{}
	u.UpdatedAt = time.Now()
	m.users[id] = u
	return nil
}

func (m *inMemRepo) IncrementFailedAttempts(_ context.Context, id uuid.UUID, threshold int, dur time.Duration) (int, time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return 0, time.Time{}, ErrUserNotFound
	}
	u.FailedAttempts++
	if u.FailedAttempts >= threshold {
		u.LockedUntil = time.Now().Add(dur)
	}
	u.UpdatedAt = time.Now()
	m.users[id] = u
	return u.FailedAttempts, u.LockedUntil, nil
}

func (m *inMemRepo) ResetFailedAttempts(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return ErrUserNotFound
	}
	u.FailedAttempts = 0
	u.LockedUntil = time.Time{}
	u.UpdatedAt = time.Now()
	m.users[id] = u
	return nil
}

func (m *inMemRepo) SetUserStatus(_ context.Context, id uuid.UUID, status string, locked time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[id]
	if !ok {
		return ErrUserNotFound
	}
	u.Status = status
	u.LockedUntil = locked
	u.UpdatedAt = time.Now()
	m.users[id] = u
	return nil
}

func (m *inMemRepo) GetPassword(_ context.Context, id uuid.UUID) (PasswordCredential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.passwords[id]
	if !ok {
		return PasswordCredential{}, ErrUserNotFound
	}
	return p, nil
}

func (m *inMemRepo) UpdatePassword(_ context.Context, id uuid.UUID, newHash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.passwords[id]
	if !ok {
		return ErrUserNotFound
	}
	p.PasswordHash = newHash
	p.ChangedAt = time.Now()
	m.passwords[id] = p
	return nil
}

func (m *inMemRepo) CreateSession(_ context.Context, s Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s.IssuedAt = time.Now()
	s.LastUsedAt = s.IssuedAt
	m.sessions[string(s.TokenHash)] = s
	return nil
}

func (m *inMemRepo) GetSession(_ context.Context, h []byte) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[string(h)]
	if !ok {
		return Session{}, ErrSessionNotFound
	}
	return s, nil
}

func (m *inMemRepo) SlideSession(_ context.Context, h []byte, newExp time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[string(h)]
	if !ok {
		return ErrSessionNotFound
	}
	s.ExpiresAt = newExp
	s.LastUsedAt = time.Now()
	m.sessions[string(h)] = s
	return nil
}

func (m *inMemRepo) RevokeSession(_ context.Context, h []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[string(h)]
	if !ok {
		return ErrSessionNotFound
	}
	s.RevokedAt = time.Now()
	m.sessions[string(h)] = s
	return nil
}

func (m *inMemRepo) RevokeAllSessionsExcept(_ context.Context, id uuid.UUID, keep []byte) (int, error) {
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

func (m *inMemRepo) RevokeAllSessionsForUser(_ context.Context, id uuid.UUID) (int, error) {
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

func (m *inMemRepo) ListActiveSessions(_ context.Context, id uuid.UUID) ([]Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Session
	for _, s := range m.sessions {
		if s.UserID == id && s.RevokedAt.IsZero() {
			out = append(out, s)
		}
	}
	return out, nil
}

func (m *inMemRepo) DeleteExpiredSessions(_ context.Context, retention time.Duration) (int, error) {
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

func (m *inMemRepo) DeleteOldAuditRows(_ context.Context, _ time.Duration) (int, error) {
	return 0, nil
}

func (m *inMemRepo) Audit(_ context.Context, ev AuditEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.audit = append(m.audit, ev)
	return nil
}

func (m *inMemRepo) RecentAudit(_ context.Context, id uuid.UUID, limit int) ([]AuditEvent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []AuditEvent
	for i := len(m.audit) - 1; i >= 0 && len(out) < limit; i-- {
		if m.audit[i].UserID == id {
			out = append(out, m.audit[i])
		}
	}
	return out, nil
}

func (m *inMemRepo) HasCapability(_ context.Context, userID uuid.UUID, capability string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.caps[inMemCapKey{userID, capability}]
	if !ok {
		return false, nil
	}
	if !c.ExpiresAt.IsZero() && c.ExpiresAt.Before(time.Now()) {
		return false, nil
	}
	return true, nil
}

func (m *inMemRepo) GrantCapability(_ context.Context, c Capability) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c.GrantedAt = time.Now()
	m.caps[inMemCapKey{c.UserID, c.Capability}] = c
	return nil
}

func (m *inMemRepo) RevokeCapability(_ context.Context, userID uuid.UUID, capability string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := inMemCapKey{userID, capability}
	if _, ok := m.caps[k]; !ok {
		return ErrCapabilityNotFound
	}
	delete(m.caps, k)
	return nil
}

func (m *inMemRepo) ListCapabilities(_ context.Context, userID uuid.UUID) ([]Capability, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	var out []Capability
	for k, c := range m.caps {
		if k.UserID != userID {
			continue
		}
		if !c.ExpiresAt.IsZero() && c.ExpiresAt.Before(now) {
			continue
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Capability < out[j].Capability })
	return out, nil
}

func newTestService(t *testing.T) (*Service, *inMemRepo) {
	t.Helper()
	repo := newInMemRepo()
	opts := DefaultServiceOpts()
	opts.Repository = repo
	s := newService(&service.Context{
		KindName: "auth", InstanceID: "test-0",
		Logger: logger.New(), Roles: map[string]struct{}{"service": {}},
	}, opts).(*Service)
	if err := s.Init(s.ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Shutdown(context.Background()) })
	return s, repo
}

func testOpCtx(ip string) *ops.OpContext {
	return &ops.OpContext{ConnID: 1, ClientIP: netip.MustParseAddr(ip)}
}

// --- Task 12 tests ---

func TestLoginUnknownUserReturnsInvalidCredentials(t *testing.T) {
	s, _ := newTestService(t)
	_, err := s.handleLogin(testOpCtx("1.1.1.1"), &AuthLoginRequest{Username: "ghost", Password: "x"})
	ae, ok := err.(*authError)
	if !ok || ae.Code != AuthErrorInvalidCredentials {
		t.Fatalf("want INVALID_CREDENTIALS, got %v", err)
	}
}

// --- Task 13 tests ---

func TestRegisterCreatesUserAndSession(t *testing.T) {
	s, _ := newTestService(t)
	opCtx := testOpCtx("1.1.1.1")
	resp, err := s.handleRegister(opCtx, &AuthRegisterRequest{Username: "Alice", Password: "hunter22"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Username != "alice" {
		t.Fatalf("want lowercased; got %s", resp.Username)
	}
	if resp.SessionToken == "" {
		t.Fatal("no session token")
	}
	if _, err := s.handleLogin(opCtx, &AuthLoginRequest{Username: "alice", Password: "hunter22"}); err != nil {
		t.Fatalf("login after register: %v", err)
	}
}

func TestRegisterDuplicateUsername(t *testing.T) {
	s, _ := newTestService(t)
	opCtx := testOpCtx("1.1.1.1")
	_, _ = s.handleRegister(opCtx, &AuthRegisterRequest{Username: "bob", Password: "hunter22"})
	_, err := s.handleRegister(opCtx, &AuthRegisterRequest{Username: "BOB", Password: "hunter22"})
	ae, ok := err.(*authError)
	if !ok || ae.Code != AuthErrorUsernameTaken {
		t.Fatalf("want USERNAME_TAKEN, got %v", err)
	}
	if ae.Metadata == nil || ae.Metadata.Canonical != "bob" {
		t.Fatalf("expected canonical='bob', got %+v", ae.Metadata)
	}
}

func TestRegisterPasswordTooWeak(t *testing.T) {
	s, _ := newTestService(t)
	_, err := s.handleRegister(testOpCtx("1.1.1.1"), &AuthRegisterRequest{Username: "carol", Password: "short"})
	ae, ok := err.(*authError)
	if !ok || ae.Code != AuthErrorPasswordTooWeak {
		t.Fatalf("want PASSWORD_TOO_WEAK, got %v", err)
	}
}

// --- Task 14 tests ---

func TestValidateTokenReconnect(t *testing.T) {
	s, _ := newTestService(t)
	opCtx := testOpCtx("1.1.1.1")
	reg, _ := s.handleRegister(opCtx, &AuthRegisterRequest{Username: "evan", Password: "hunter22"})

	// Sleep briefly so the slid expiry is strictly greater than the issued expiry.
	time.Sleep(2 * time.Millisecond)
	resp, err := s.handleValidateToken(opCtx, &AuthValidateTokenRequest{SessionToken: reg.SessionToken})
	if err != nil {
		t.Fatal(err)
	}
	if resp.UserID != reg.UserID {
		t.Fatalf("user mismatch")
	}
	if resp.ExpiresAtMs <= reg.ExpiresAtMs {
		t.Fatal("expiry should slide")
	}
}

func TestValidateTokenInvalid(t *testing.T) {
	s, _ := newTestService(t)
	_, err := s.handleValidateToken(testOpCtx("1.1.1.1"), &AuthValidateTokenRequest{SessionToken: "deadbeef"})
	ae, ok := err.(*authError)
	if !ok || ae.Code != AuthErrorTokenInvalid {
		t.Fatalf("want TOKEN_INVALID, got %v", err)
	}
}

// --- Task 15 tests ---

func TestLogoutRevokesSession(t *testing.T) {
	s, _ := newTestService(t)
	opCtx := testOpCtx("1.1.1.1")
	reg, _ := s.handleRegister(opCtx, &AuthRegisterRequest{Username: "fay", Password: "hunter22"})
	WithSessionToken(opCtx, reg.SessionToken)

	if _, err := s.handleLogout(opCtx, &AuthLogoutRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.handleValidateToken(opCtx, &AuthValidateTokenRequest{SessionToken: reg.SessionToken}); err == nil {
		t.Fatal("expected error after logout")
	}
}

// --- Task 16 tests ---

func TestChangePasswordRevokesOtherSessions(t *testing.T) {
	s, _ := newTestService(t)
	opCtx := testOpCtx("1.1.1.1")
	reg, _ := s.handleRegister(opCtx, &AuthRegisterRequest{Username: "gail", Password: "hunter22"})
	second, _ := s.handleLogin(opCtx, &AuthLoginRequest{Username: "gail", Password: "hunter22"})

	WithSessionToken(opCtx, reg.SessionToken)
	if _, err := s.handleChangePassword(opCtx, &AuthChangePasswordRequest{
		CurrentPassword: "hunter22", NewPassword: "newer-than-eight",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.handleValidateToken(opCtx, &AuthValidateTokenRequest{SessionToken: reg.SessionToken}); err != nil {
		t.Fatalf("current session should survive: %v", err)
	}
	if _, err := s.handleValidateToken(opCtx, &AuthValidateTokenRequest{SessionToken: second.SessionToken}); err == nil {
		t.Fatal("second session should be revoked")
	}
}

func TestChangePasswordCurrentMismatch(t *testing.T) {
	s, _ := newTestService(t)
	opCtx := testOpCtx("1.1.1.1")
	reg, _ := s.handleRegister(opCtx, &AuthRegisterRequest{Username: "harry", Password: "hunter22"})
	WithSessionToken(opCtx, reg.SessionToken)
	_, err := s.handleChangePassword(opCtx, &AuthChangePasswordRequest{
		CurrentPassword: "wrong", NewPassword: "newer-than-eight",
	})
	ae, ok := err.(*authError)
	if !ok || ae.Code != AuthErrorInvalidCredentials {
		t.Fatalf("want INVALID_CREDENTIALS, got %v", err)
	}
}

// --- Task 17 reaper test ---

func TestReaperOnceRunsWithoutError(t *testing.T) {
	s, _ := newTestService(t)
	s.reapOnce()
}
