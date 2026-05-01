package auth

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	enginepb "github.com/zenion/mmoserver/gen/go/enginepb"
	"github.com/zenion/mmoserver/pkg/logger"
	"github.com/zenion/mmoserver/pkg/ops"
	"github.com/zenion/mmoserver/pkg/service"
)

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
}

func newInMemRepo() *inMemRepo {
	return &inMemRepo{
		users:     map[uuid.UUID]User{},
		byName:    map[string]uuid.UUID{},
		passwords: map[uuid.UUID]PasswordCredential{},
		sessions:  map[string]Session{},
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
	_, err := s.handleLogin(testOpCtx("1.1.1.1"), &enginepb.AuthLoginRequest{Username: "ghost", Password: "x"})
	ae, ok := err.(*authError)
	if !ok || ae.Code != enginepb.AuthError_AUTH_ERROR_INVALID_CREDENTIALS {
		t.Fatalf("want INVALID_CREDENTIALS, got %v", err)
	}
}

// --- Task 13 tests ---

func TestRegisterCreatesUserAndSession(t *testing.T) {
	s, _ := newTestService(t)
	opCtx := testOpCtx("1.1.1.1")
	resp, err := s.handleRegister(opCtx, &enginepb.AuthRegisterRequest{Username: "Alice", Password: "hunter22"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Username != "alice" {
		t.Fatalf("want lowercased; got %s", resp.Username)
	}
	if resp.SessionToken == "" {
		t.Fatal("no session token")
	}
	if _, err := s.handleLogin(opCtx, &enginepb.AuthLoginRequest{Username: "alice", Password: "hunter22"}); err != nil {
		t.Fatalf("login after register: %v", err)
	}
}

func TestRegisterDuplicateUsername(t *testing.T) {
	s, _ := newTestService(t)
	opCtx := testOpCtx("1.1.1.1")
	_, _ = s.handleRegister(opCtx, &enginepb.AuthRegisterRequest{Username: "bob", Password: "hunter22"})
	_, err := s.handleRegister(opCtx, &enginepb.AuthRegisterRequest{Username: "BOB", Password: "hunter22"})
	ae, ok := err.(*authError)
	if !ok || ae.Code != enginepb.AuthError_AUTH_ERROR_USERNAME_TAKEN {
		t.Fatalf("want USERNAME_TAKEN, got %v", err)
	}
	if ae.Metadata == nil || ae.Metadata.Canonical != "bob" {
		t.Fatalf("expected canonical='bob', got %+v", ae.Metadata)
	}
}

func TestRegisterPasswordTooWeak(t *testing.T) {
	s, _ := newTestService(t)
	_, err := s.handleRegister(testOpCtx("1.1.1.1"), &enginepb.AuthRegisterRequest{Username: "carol", Password: "short"})
	ae, ok := err.(*authError)
	if !ok || ae.Code != enginepb.AuthError_AUTH_ERROR_PASSWORD_TOO_WEAK {
		t.Fatalf("want PASSWORD_TOO_WEAK, got %v", err)
	}
}

// --- Task 14 tests ---

func TestValidateTokenReconnect(t *testing.T) {
	s, _ := newTestService(t)
	opCtx := testOpCtx("1.1.1.1")
	reg, _ := s.handleRegister(opCtx, &enginepb.AuthRegisterRequest{Username: "evan", Password: "hunter22"})

	// Sleep briefly so the slid expiry is strictly greater than the issued expiry.
	time.Sleep(2 * time.Millisecond)
	resp, err := s.handleValidateToken(opCtx, &enginepb.AuthValidateTokenRequest{SessionToken: reg.SessionToken})
	if err != nil {
		t.Fatal(err)
	}
	if resp.UserId != reg.UserId {
		t.Fatalf("user mismatch")
	}
	if resp.ExpiresAtMs <= reg.ExpiresAtMs {
		t.Fatal("expiry should slide")
	}
}

func TestValidateTokenInvalid(t *testing.T) {
	s, _ := newTestService(t)
	_, err := s.handleValidateToken(testOpCtx("1.1.1.1"), &enginepb.AuthValidateTokenRequest{SessionToken: "deadbeef"})
	ae, ok := err.(*authError)
	if !ok || ae.Code != enginepb.AuthError_AUTH_ERROR_TOKEN_INVALID {
		t.Fatalf("want TOKEN_INVALID, got %v", err)
	}
}

// --- Task 15 tests ---

func TestLogoutRevokesSession(t *testing.T) {
	s, _ := newTestService(t)
	opCtx := testOpCtx("1.1.1.1")
	reg, _ := s.handleRegister(opCtx, &enginepb.AuthRegisterRequest{Username: "fay", Password: "hunter22"})
	WithSessionToken(opCtx, reg.SessionToken)

	if _, err := s.handleLogout(opCtx, &enginepb.AuthLogoutRequest{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.handleValidateToken(opCtx, &enginepb.AuthValidateTokenRequest{SessionToken: reg.SessionToken}); err == nil {
		t.Fatal("expected error after logout")
	}
}

// --- Task 16 tests ---

func TestChangePasswordRevokesOtherSessions(t *testing.T) {
	s, _ := newTestService(t)
	opCtx := testOpCtx("1.1.1.1")
	reg, _ := s.handleRegister(opCtx, &enginepb.AuthRegisterRequest{Username: "gail", Password: "hunter22"})
	second, _ := s.handleLogin(opCtx, &enginepb.AuthLoginRequest{Username: "gail", Password: "hunter22"})

	WithSessionToken(opCtx, reg.SessionToken)
	if _, err := s.handleChangePassword(opCtx, &enginepb.AuthChangePasswordRequest{
		CurrentPassword: "hunter22", NewPassword: "newer-than-eight",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.handleValidateToken(opCtx, &enginepb.AuthValidateTokenRequest{SessionToken: reg.SessionToken}); err != nil {
		t.Fatalf("current session should survive: %v", err)
	}
	if _, err := s.handleValidateToken(opCtx, &enginepb.AuthValidateTokenRequest{SessionToken: second.SessionToken}); err == nil {
		t.Fatal("second session should be revoked")
	}
}

func TestChangePasswordCurrentMismatch(t *testing.T) {
	s, _ := newTestService(t)
	opCtx := testOpCtx("1.1.1.1")
	reg, _ := s.handleRegister(opCtx, &enginepb.AuthRegisterRequest{Username: "harry", Password: "hunter22"})
	WithSessionToken(opCtx, reg.SessionToken)
	_, err := s.handleChangePassword(opCtx, &enginepb.AuthChangePasswordRequest{
		CurrentPassword: "wrong", NewPassword: "newer-than-eight",
	})
	ae, ok := err.(*authError)
	if !ok || ae.Code != enginepb.AuthError_AUTH_ERROR_INVALID_CREDENTIALS {
		t.Fatalf("want INVALID_CREDENTIALS, got %v", err)
	}
}

// --- Task 17 reaper test ---

func TestReaperOnceRunsWithoutError(t *testing.T) {
	s, _ := newTestService(t)
	s.reapOnce()
}
