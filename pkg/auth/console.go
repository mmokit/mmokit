package auth

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

// RepoProvider returns the live Repository or nil before Service.Init has
// resolved it. Console handlers call it at execution time so registration
// can happen at facade-time (before Build) without holding the live repo.
type RepoProvider func() Repository

// repoSlot is a goroutine-safe holder for the live Repository. The auth
// Service's OnReady callback writes once at Init; console handlers read
// every time they execute. mmokit.RegisterAuthService creates one and
// wires both sides.
type repoSlot struct {
	mu   sync.RWMutex
	repo Repository
}

// NewRepoSlot returns an empty slot wired with a setter and a getter.
// Setter is intended for ServiceOpts.OnReady; getter for the cmdsys
// handlers in this file.
func NewRepoSlot() (set func(Repository), get RepoProvider) {
	s := &repoSlot{}
	set = func(r Repository) {
		s.mu.Lock()
		s.repo = r
		s.mu.Unlock()
	}
	get = func() Repository {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.repo
	}
	return set, get
}

// --- Args / Result types (must be exported structs for cmdsys schema gen) ---

type UsernameArgs struct {
	Username string `cmd:"help=target username,complete=players"`
}

type UsernameDurationArgs struct {
	Username string `cmd:"help=target username,complete=players"`
	Duration string `cmd:"help=lockout duration (e.g. 15m, 1h)"`
}

type AuditRecentArgs struct {
	Username string `cmd:"help=target username,complete=players"`
	Limit    int    `cmd:"optional,help=max events to return (default 50)"`
}

type UserInfoResult struct {
	UserID         string
	Username       string
	Email          string
	Status         string
	FailedAttempts int
	LockedUntil    time.Time
	LastLoginAt    time.Time
	CreatedAt      time.Time
	ActiveSessions int
}

type OKResult struct {
	OK             bool
	RevokedCount   int
	KickedSessions int
	Username       string
	Detail         string
}

type SessionsListResult struct {
	Sessions []SessionDigest
}

type SessionDigest struct {
	TokenHashPrefix string // first 8 hex chars; never the full token
	IssuedAt        time.Time
	ExpiresAt       time.Time
	LastUsedAt      time.Time
	ClientIP        string
}

type AuditRecentResult struct {
	Events []AuditDigest
}

type AuditDigest struct {
	OccurredAt        time.Time
	Event             string
	UsernameAttempted string
	IPAddr            string
	GatewayID         string
	Reason            string
}

// DisconnectFn closes any live WebSocket sessions for username and returns
// the number of sessions actually closed. Called by auth.user.kick after
// token revocation so a single command both invalidates future-use tokens
// and drops the active connection — neither alone is a complete boot.
// Implementation lives outside pkg/auth to avoid a universe import.
type DisconnectFn func(ctx context.Context, env *cmdsys.Env, username string) (int, error)

// RegisterConsoleCommands wires the auth.* command group into the cmdsys
// dispatcher. Handlers call getRepo() at execution time so registration
// can happen at facade-time before the auth Service has been constructed.
//
// disconnect may be nil — in that case auth.user.kick only revokes tokens
// without dropping live WS sessions. Production wiring should always supply
// a real implementation; nil is for tests that don't spin up a coordinator.
//
// Returns an error if any command fails to register (duplicate verb, etc).
func RegisterConsoleCommands(reg *cmdsys.Registry, getRepo RepoProvider, disconnect DisconnectFn) error {
	must := func(err error) error {
		if err != nil {
			return fmt.Errorf("auth console: %w", err)
		}
		return nil
	}

	if err := must(reg.Register(cmdsys.Command{
		Verb:        "auth.user.info",
		Capability:  "auth.user.read",
		Description: "show auth user record + active session count",
		Route:       cmdsys.RouteLocal,
		Args:        UsernameArgs{},
		Result:      UserInfoResult{},
		Handler:     userInfoHandler(getRepo),
	})); err != nil {
		return err
	}
	if err := must(reg.Register(cmdsys.Command{
		Verb:        "auth.user.lock",
		Capability:  "auth.user.write",
		Description: "lock account for a duration (e.g. 15m). Revokes all active sessions.",
		Route:       cmdsys.RouteLocal,
		Args:        UsernameDurationArgs{},
		Result:      OKResult{},
		Handler:     userLockHandler(getRepo),
	})); err != nil {
		return err
	}
	if err := must(reg.Register(cmdsys.Command{
		Verb:        "auth.user.unlock",
		Capability:  "auth.user.write",
		Description: "clear account lockout + reset failed_attempts",
		Route:       cmdsys.RouteLocal,
		Args:        UsernameArgs{},
		Result:      OKResult{},
		Handler:     userUnlockHandler(getRepo),
	})); err != nil {
		return err
	}
	if err := must(reg.Register(cmdsys.Command{
		Verb:        "auth.user.kick",
		Capability:  "auth.user.write",
		Description: "revoke all sessions AND drop any live WS for a user (does not lock the account)",
		Route:       cmdsys.RouteLocal,
		Args:        UsernameArgs{},
		Result:      OKResult{},
		Handler:     userKickHandler(getRepo, disconnect),
	})); err != nil {
		return err
	}
	if err := must(reg.Register(cmdsys.Command{
		Verb:        "auth.session.list",
		Capability:  "auth.session.read",
		Description: "list active sessions for a user (token-hash prefix only)",
		Route:       cmdsys.RouteLocal,
		Args:        UsernameArgs{},
		Result:      SessionsListResult{},
		Handler:     sessionListHandler(getRepo),
	})); err != nil {
		return err
	}
	if err := must(reg.Register(cmdsys.Command{
		Verb:        "auth.audit.recent",
		Capability:  "auth.audit.read",
		Description: "recent audit events for a user (default 50)",
		Route:       cmdsys.RouteLocal,
		Args:        AuditRecentArgs{},
		Result:      AuditRecentResult{},
		Handler:     auditRecentHandler(getRepo),
	})); err != nil {
		return err
	}
	return nil
}

// --- handler factories ---

func errRepoNotReady() error {
	return errors.New("auth service not initialized yet — try again after the cluster finishes starting")
}

func cmdCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

func userInfoHandler(getRepo RepoProvider) cmdsys.HandlerFunc {
	return func(_ context.Context, _ *cmdsys.Env, args any) (any, error) {
		repo := getRepo()
		if repo == nil {
			return nil, errRepoNotReady()
		}
		a := args.(UsernameArgs)
		ctx, cancel := cmdCtx()
		defer cancel()
		u, err := repo.GetUserByUsername(ctx, a.Username)
		if err != nil {
			return nil, err
		}
		sessions, _ := repo.ListActiveSessions(ctx, u.UserID)
		return UserInfoResult{
			UserID:         u.UserID.String(),
			Username:       u.Username,
			Email:          u.Email,
			Status:         u.Status,
			FailedAttempts: u.FailedAttempts,
			LockedUntil:    u.LockedUntil,
			LastLoginAt:    u.LastLoginAt,
			CreatedAt:      u.CreatedAt,
			ActiveSessions: len(sessions),
		}, nil
	}
}

func userLockHandler(getRepo RepoProvider) cmdsys.HandlerFunc {
	return func(_ context.Context, _ *cmdsys.Env, args any) (any, error) {
		repo := getRepo()
		if repo == nil {
			return nil, errRepoNotReady()
		}
		a := args.(UsernameDurationArgs)
		dur, err := time.ParseDuration(a.Duration)
		if err != nil {
			return nil, fmt.Errorf("invalid duration %q: %w", a.Duration, err)
		}
		ctx, cancel := cmdCtx()
		defer cancel()
		u, err := repo.GetUserByUsername(ctx, a.Username)
		if err != nil {
			return nil, err
		}
		lockedUntil := time.Now().Add(dur)
		if err := repo.SetUserStatus(ctx, u.UserID, u.Status, lockedUntil); err != nil {
			return nil, err
		}
		n, _ := repo.RevokeAllSessionsForUser(ctx, u.UserID)
		return OKResult{
			OK:           true,
			RevokedCount: n,
			Username:     u.Username,
			Detail:       fmt.Sprintf("locked until %s", lockedUntil.Format(time.RFC3339)),
		}, nil
	}
}

func userUnlockHandler(getRepo RepoProvider) cmdsys.HandlerFunc {
	return func(_ context.Context, _ *cmdsys.Env, args any) (any, error) {
		repo := getRepo()
		if repo == nil {
			return nil, errRepoNotReady()
		}
		a := args.(UsernameArgs)
		ctx, cancel := cmdCtx()
		defer cancel()
		u, err := repo.GetUserByUsername(ctx, a.Username)
		if err != nil {
			return nil, err
		}
		if err := repo.ResetFailedAttempts(ctx, u.UserID); err != nil {
			return nil, err
		}
		return OKResult{OK: true, Username: u.Username, Detail: "lockout cleared"}, nil
	}
}

func userKickHandler(getRepo RepoProvider, disconnect DisconnectFn) cmdsys.HandlerFunc {
	return func(_ context.Context, env *cmdsys.Env, args any) (any, error) {
		repo := getRepo()
		if repo == nil {
			return nil, errRepoNotReady()
		}
		a := args.(UsernameArgs)
		ctx, cancel := cmdCtx()
		defer cancel()
		u, err := repo.GetUserByUsername(ctx, a.Username)
		if err != nil {
			return nil, err
		}
		n, err := repo.RevokeAllSessionsForUser(ctx, u.UserID)
		if err != nil {
			return nil, err
		}
		// Best-effort live-session drop. Token revocation already succeeded;
		// surface a disconnect failure in Detail so the operator notices, but
		// don't fail the command — the user is locked out of any future
		// reconnect either way.
		kicked := 0
		detail := ""
		if disconnect != nil {
			k, derr := disconnect(ctx, env, u.Username)
			kicked = k
			if derr != nil {
				detail = fmt.Sprintf("token revoke OK; ws disconnect failed: %v", derr)
			}
		}
		return OKResult{
			OK:             true,
			Username:       u.Username,
			RevokedCount:   n,
			KickedSessions: kicked,
			Detail:         detail,
		}, nil
	}
}

func sessionListHandler(getRepo RepoProvider) cmdsys.HandlerFunc {
	return func(_ context.Context, _ *cmdsys.Env, args any) (any, error) {
		repo := getRepo()
		if repo == nil {
			return nil, errRepoNotReady()
		}
		a := args.(UsernameArgs)
		ctx, cancel := cmdCtx()
		defer cancel()
		u, err := repo.GetUserByUsername(ctx, a.Username)
		if err != nil {
			return nil, err
		}
		sessions, err := repo.ListActiveSessions(ctx, u.UserID)
		if err != nil {
			return nil, err
		}
		out := make([]SessionDigest, 0, len(sessions))
		for _, s := range sessions {
			out = append(out, SessionDigest{
				TokenHashPrefix: hexPrefix(s.TokenHash, 8),
				IssuedAt:        s.IssuedAt,
				ExpiresAt:       s.ExpiresAt,
				LastUsedAt:      s.LastUsedAt,
				ClientIP:        s.ClientMeta["ip"],
			})
		}
		return SessionsListResult{Sessions: out}, nil
	}
}

func auditRecentHandler(getRepo RepoProvider) cmdsys.HandlerFunc {
	return func(_ context.Context, _ *cmdsys.Env, args any) (any, error) {
		repo := getRepo()
		if repo == nil {
			return nil, errRepoNotReady()
		}
		a := args.(AuditRecentArgs)
		limit := a.Limit
		if limit <= 0 {
			limit = 50
		}
		ctx, cancel := cmdCtx()
		defer cancel()
		u, err := repo.GetUserByUsername(ctx, a.Username)
		if err != nil {
			return nil, err
		}
		evs, err := repo.RecentAudit(ctx, u.UserID, limit)
		if err != nil {
			return nil, err
		}
		out := make([]AuditDigest, 0, len(evs))
		for _, ev := range evs {
			d := AuditDigest{
				OccurredAt:        time.Time{}, // RecentAudit doesn't currently return occurred_at; left zero
				Event:             ev.Event,
				UsernameAttempted: ev.UsernameAttempted,
				GatewayID:         ev.GatewayID,
			}
			if ev.IPAddr.IsValid() {
				d.IPAddr = ev.IPAddr.String()
			}
			if reason, ok := ev.Metadata["reason"].(string); ok {
				d.Reason = reason
			}
			out = append(out, d)
		}
		return AuditRecentResult{Events: out}, nil
	}
}

const hexDigits = "0123456789abcdef"

func hexPrefix(b []byte, n int) string {
	if len(b)*2 < n {
		n = len(b) * 2
	}
	out := make([]byte, n)
	for i := 0; i < n; i += 2 {
		if i/2 >= len(b) {
			break
		}
		out[i] = hexDigits[b[i/2]>>4]
		if i+1 < n {
			out[i+1] = hexDigits[b[i/2]&0x0f]
		}
	}
	return string(out)
}
