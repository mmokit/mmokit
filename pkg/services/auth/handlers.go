package auth

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/zenion/mmoserver/pkg/ops"
)

// authError is the typed error handlers return. The router maps it to the
// AuthError code on the op envelope.
type authError struct {
	Code     AuthError
	Msg      string
	Metadata *AuthErrorMetadata
}

func (e *authError) Error() string { return e.Msg }

func errorf(c AuthError, f string, a ...any) error {
	return &authError{Code: c, Msg: fmt.Sprintf(f, a...)}
}

func errorWithRetry(c AuthError, retry time.Duration, f string, a ...any) error {
	return &authError{
		Code:     c,
		Msg:      fmt.Sprintf(f, a...),
		Metadata: &AuthErrorMetadata{RetryAfterMs: retry.Milliseconds()},
	}
}

func normalizeUsername(raw string) (string, error) {
	name := strings.ToLower(strings.TrimSpace(raw))
	if name == "" || len(name) > 32 {
		return "", errorf(AuthErrorUsernameInvalid, "username invalid")
	}
	return name, nil
}

// SessionToken context-bag plumbing — gateway sets, Logout/ChangePassword reads.

type ctxKey int

const sessionTokenKey ctxKey = 1

// SessionTokenFrom retrieves the session token stored in the OpContext bag by
// the gateway (or test code via WithSessionToken).
func SessionTokenFrom(opCtx *ops.OpContext) string {
	v, _ := opCtx.Bag().Load(sessionTokenKey)
	s, _ := v.(string)
	return s
}

// WithSessionToken stores a session token in the OpContext bag. Used by the
// gateway after validating a session, and by tests to simulate authenticated
// connections.
func WithSessionToken(opCtx *ops.OpContext, token string) {
	opCtx.Bag().Store(sessionTokenKey, token)
}

func clientMeta(opCtx *ops.OpContext, ip netip.Addr) map[string]string {
	m := map[string]string{}
	if ip.IsValid() {
		m["ip"] = ip.String()
	}
	return m
}

// audit writes an audit event using a fresh background context so handler
// cancellations don't interfere with audit writes.
func (s *Service) audit(opCtx *ops.OpContext, event string, userID uuid.UUID, name string, meta map[string]any) {
	if err := s.repo.Audit(context.Background(), AuditEvent{
		Event: event, UserID: userID, UsernameAttempted: name,
		IPAddr: opCtx.ClientIP, GatewayID: "", Metadata: meta,
	}); err != nil {
		s.ctx.Logger.Log(logCat, "audit write failed: %v", err)
	}
}

// --- handleLogin ---

func (s *Service) handleLogin(opCtx *ops.OpContext, req *AuthLoginRequest) (*AuthLoginResponse, error) {
	ip := opCtx.ClientIP
	if locked, retry := s.rl.CheckAndCount(ip, false); locked {
		s.audit(opCtx, "login_failure", uuid.Nil, req.Username, map[string]any{"reason": "ip_ratelimit"})
		return nil, errorWithRetry(AuthErrorRateLimited, retry, "too many attempts")
	}

	name, err := normalizeUsername(req.Username)
	if err != nil {
		return nil, err
	}

	ctx := context.Background()
	user, err := s.repo.GetUserByUsername(ctx, name)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			s.audit(opCtx, "login_failure", uuid.Nil, name, map[string]any{"reason": "no_such_user"})
			return nil, errorf(AuthErrorInvalidCredentials, "invalid credentials")
		}
		return nil, errorf(AuthErrorInternal, "lookup: %v", err)
	}

	if user.Status == "disabled" {
		s.audit(opCtx, "login_failure", user.UserID, name, map[string]any{"reason": "disabled"})
		return nil, errorf(AuthErrorAccountLocked, "account disabled")
	}
	if !user.LockedUntil.IsZero() && user.LockedUntil.After(time.Now()) {
		retry := time.Until(user.LockedUntil)
		s.audit(opCtx, "login_failure", user.UserID, name, map[string]any{"reason": "account_locked"})
		return nil, errorWithRetry(AuthErrorAccountLocked, retry, "account locked")
	}

	pw, err := s.repo.GetPassword(ctx, user.UserID)
	if err != nil {
		return nil, errorf(AuthErrorInternal, "password lookup: %v", err)
	}
	ok, err := VerifyPassword(req.Password, pw.PasswordHash)
	if err != nil || !ok {
		n, lockedUntil, _ := s.repo.IncrementFailedAttempts(ctx, user.UserID, s.opts.LockoutThreshold, s.opts.LockoutDuration)
		meta := map[string]any{"reason": "wrong_password", "failed_attempts": n}
		s.audit(opCtx, "login_failure", user.UserID, name, meta)
		if n >= s.opts.LockoutThreshold {
			s.audit(opCtx, "lockout_triggered", user.UserID, name, map[string]any{
				"failed_attempts": n, "locked_until": lockedUntil,
			})
		}
		return nil, errorf(AuthErrorInvalidCredentials, "invalid credentials")
	}

	// Re-hash if argon2id params have drifted from current defaults.
	if !HashUsesParams(pw.PasswordHash, s.opts.Argon2id) {
		if newHash, err := HashPassword(req.Password, s.opts.Argon2id); err == nil {
			_ = s.repo.UpdatePassword(ctx, user.UserID, newHash)
		}
	}

	_ = s.repo.UpdateUserLogin(ctx, user.UserID, time.Now())
	s.rl.CheckAndCount(ip, true) // reset bucket on success

	tok, hash, err := NewToken()
	if err != nil {
		return nil, errorf(AuthErrorInternal, "token: %v", err)
	}
	expires := time.Now().Add(s.opts.SessionTTL)
	if err := s.repo.CreateSession(ctx, Session{
		TokenHash: hash, UserID: user.UserID, ExpiresAt: expires,
		ClientMeta: clientMeta(opCtx, ip),
	}); err != nil {
		return nil, errorf(AuthErrorInternal, "session: %v", err)
	}

	s.audit(opCtx, "login_success", user.UserID, name, nil)
	s.ctx.Logger.Log(logCat, "login: user=%s ip=%s", name, ip)
	return &AuthLoginResponse{
		UserID: user.UserID.String(), Username: user.Username,
		SessionToken: tok, ExpiresAtMs: expires.UnixMilli(),
	}, nil
}

// --- handleRegister ---

func (s *Service) handleRegister(opCtx *ops.OpContext, req *AuthRegisterRequest) (*AuthRegisterResponse, error) {
	ip := opCtx.ClientIP
	if locked, retry := s.rl.CheckAndCount(ip, false); locked {
		return nil, errorWithRetry(AuthErrorRateLimited, retry, "too many attempts")
	}

	name, err := normalizeUsername(req.Username)
	if err != nil {
		return nil, err
	}

	if len(req.Password) < s.opts.PasswordMinLen {
		return nil, errorf(AuthErrorPasswordTooWeak,
			"password must be at least %d chars", s.opts.PasswordMinLen)
	}
	hash, err := HashPassword(req.Password, s.opts.Argon2id)
	if err != nil {
		return nil, errorf(AuthErrorInternal, "hash: %v", err)
	}

	ctx := context.Background()
	user, err := s.repo.CreateUser(ctx, User{Username: name, Email: req.Email}, hash)
	if err != nil {
		if errors.Is(err, ErrUsernameTaken) {
			return nil, &authError{
				Code:     AuthErrorUsernameTaken,
				Msg:      "username taken",
				Metadata: &AuthErrorMetadata{Canonical: name},
			}
		}
		return nil, errorf(AuthErrorInternal, "create: %v", err)
	}

	s.rl.CheckAndCount(ip, true)
	_ = s.repo.UpdateUserLogin(ctx, user.UserID, time.Now())

	tok, h, err := NewToken()
	if err != nil {
		return nil, errorf(AuthErrorInternal, "token: %v", err)
	}
	expires := time.Now().Add(s.opts.SessionTTL)
	if err := s.repo.CreateSession(ctx, Session{
		TokenHash: h, UserID: user.UserID, ExpiresAt: expires, ClientMeta: clientMeta(opCtx, ip),
	}); err != nil {
		return nil, errorf(AuthErrorInternal, "session: %v", err)
	}

	s.audit(opCtx, "register", user.UserID, name, nil)
	s.ctx.Logger.Log(logCat, "register: user=%s ip=%s", name, ip)
	return &AuthRegisterResponse{
		UserID: user.UserID.String(), Username: user.Username,
		SessionToken: tok, ExpiresAtMs: expires.UnixMilli(),
	}, nil
}

// --- handleValidateToken ---

func (s *Service) handleValidateToken(opCtx *ops.OpContext, req *AuthValidateTokenRequest) (*AuthValidateTokenResponse, error) {
	ip := opCtx.ClientIP
	if locked, retry := s.rl.CheckAndCount(ip, false); locked {
		return nil, errorWithRetry(AuthErrorRateLimited, retry, "rate limited")
	}
	if req.SessionToken == "" {
		return nil, errorf(AuthErrorTokenInvalid, "empty token")
	}

	ctx := context.Background()
	h := HashToken(req.SessionToken)
	sess, err := s.repo.GetSession(ctx, h)
	if err != nil {
		s.audit(opCtx, "token_validate_failure", uuid.Nil, "", map[string]any{"reason": "unknown"})
		return nil, errorf(AuthErrorTokenInvalid, "token unknown")
	}
	if !sess.RevokedAt.IsZero() {
		s.audit(opCtx, "token_validate_failure", sess.UserID, "", map[string]any{"reason": "revoked"})
		return nil, errorf(AuthErrorTokenInvalid, "token revoked")
	}
	if time.Now().After(sess.ExpiresAt) {
		s.audit(opCtx, "token_validate_failure", sess.UserID, "", map[string]any{"reason": "expired"})
		return nil, errorf(AuthErrorTokenExpired, "token expired")
	}

	user, err := s.repo.GetUserByID(ctx, sess.UserID)
	if err != nil {
		return nil, errorf(AuthErrorInternal, "user lookup: %v", err)
	}
	if user.Status == "disabled" || (!user.LockedUntil.IsZero() && user.LockedUntil.After(time.Now())) {
		return nil, errorf(AuthErrorAccountLocked, "account not active")
	}

	newExp := time.Now().Add(s.opts.SessionTTL)
	_ = s.repo.SlideSession(ctx, h, newExp)
	s.rl.CheckAndCount(ip, true)
	return &AuthValidateTokenResponse{
		UserID: user.UserID.String(), Username: user.Username, ExpiresAtMs: newExp.UnixMilli(),
	}, nil
}

// --- handleLogout ---

func (s *Service) handleLogout(opCtx *ops.OpContext, _ *AuthLogoutRequest) (*AuthLogoutResponse, error) {
	tok := SessionTokenFrom(opCtx)
	if tok == "" {
		return nil, errorf(AuthErrorNotAuthenticated, "no bound session")
	}
	h := HashToken(tok)
	ctx := context.Background()
	sess, err := s.repo.GetSession(ctx, h)
	if err != nil {
		return &AuthLogoutResponse{}, nil
	}
	_ = s.repo.RevokeSession(ctx, h)
	s.audit(opCtx, "logout", sess.UserID, "", nil)
	s.ctx.Logger.Log(logCat, "logout: user=%s", sess.UserID)
	return &AuthLogoutResponse{}, nil
}

// --- handleChangePassword ---

func (s *Service) handleChangePassword(opCtx *ops.OpContext, req *AuthChangePasswordRequest) (*AuthChangePasswordResponse, error) {
	tok := SessionTokenFrom(opCtx)
	if tok == "" {
		return nil, errorf(AuthErrorNotAuthenticated, "no bound session")
	}
	h := HashToken(tok)
	ctx := context.Background()
	sess, err := s.repo.GetSession(ctx, h)
	if err != nil || !sess.RevokedAt.IsZero() {
		return nil, errorf(AuthErrorNotAuthenticated, "invalid session")
	}
	pw, err := s.repo.GetPassword(ctx, sess.UserID)
	if err != nil {
		return nil, errorf(AuthErrorInternal, "password lookup: %v", err)
	}
	ok, _ := VerifyPassword(req.CurrentPassword, pw.PasswordHash)
	if !ok {
		return nil, errorf(AuthErrorInvalidCredentials, "current password mismatch")
	}
	if len(req.NewPassword) < s.opts.PasswordMinLen {
		return nil, errorf(AuthErrorPasswordTooWeak, "new password too short")
	}
	newHash, err := HashPassword(req.NewPassword, s.opts.Argon2id)
	if err != nil {
		return nil, errorf(AuthErrorInternal, "hash: %v", err)
	}
	if err := s.repo.UpdatePassword(ctx, sess.UserID, newHash); err != nil {
		return nil, errorf(AuthErrorInternal, "save: %v", err)
	}
	n, _ := s.repo.RevokeAllSessionsExcept(ctx, sess.UserID, h)
	s.audit(opCtx, "password_change", sess.UserID, "", map[string]any{"sessions_revoked": n})
	s.ctx.Logger.Log(logCat, "password change: user=%s sessions_revoked=%d", sess.UserID, n)
	return &AuthChangePasswordResponse{}, nil
}
