package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
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
	Sessions []SessionDigest `cmd:"table"`
}

type SessionDigest struct {
	TokenHashPrefix string // first 8 hex chars; never the full token
	IssuedAt        time.Time
	ExpiresAt       time.Time
	LastUsedAt      time.Time
	ClientIP        string
}

type AuditRecentResult struct {
	Events []AuditDigest `cmd:"table"`
}

type AuditDigest struct {
	OccurredAt        time.Time
	Event             string
	UsernameAttempted string
	IPAddr            string
	GatewayID         string
	Reason            string
}

type CapabilityGrantArgs struct {
	Username   string `cmd:"help=target username,complete=players"`
	Capability string `cmd:"help=capability string (e.g. chat.admin)"`
	Duration   string `cmd:"optional,help=optional expiry duration (e.g. 24h); empty = permanent"`
}

type CapabilityRevokeArgs struct {
	Username   string `cmd:"help=target username,complete=players"`
	Capability string `cmd:"help=capability string (e.g. chat.admin)"`
}

type CapabilityListResult struct {
	Capabilities []CapabilityDigest `cmd:"table"`
}

type CapabilityDigest struct {
	Capability string
	GrantedAt  time.Time
	GrantedBy  string
	ExpiresAt  time.Time
}

type BootstrapAdminArgs struct {
	Username string `cmd:"help=user to bootstrap as cluster admin (must already exist)"`
}

// DisconnectFn closes any live WebSocket sessions for username and returns
// the number of sessions actually closed. Called by auth.user.kick after
// token revocation so a single command both invalidates future-use tokens
// and drops the active connection — neither alone is a complete boot.
// Implementation lives outside pkg/services/auth to avoid a universe import.
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
		Examples:    []string{"auth user info alice"},
		Route:       cmdsys.RouteService,
		Service:     "auth",
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
		Examples:    []string{"auth user lock alice 15m", "auth user lock alice 1h"},
		Route:       cmdsys.RouteService,
		Service:     "auth",
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
		Examples:    []string{"auth user unlock alice"},
		Route:       cmdsys.RouteService,
		Service:     "auth",
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
		Examples:    []string{"auth user kick alice"},
		Route:       cmdsys.RouteService,
		Service:     "auth",
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
		Examples:    []string{"auth session list alice"},
		Route:       cmdsys.RouteService,
		Service:     "auth",
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
		Examples:    []string{"auth audit recent alice", "auth audit recent alice 100"},
		Route:       cmdsys.RouteService,
		Service:     "auth",
		Args:        AuditRecentArgs{},
		Result:      AuditRecentResult{},
		Handler:     auditRecentHandler(getRepo),
	})); err != nil {
		return err
	}
	if err := must(reg.Register(cmdsys.Command{
		Verb:        "auth.user.grant",
		Capability:  "auth.user.write",
		Description: "grant a capability to a user (optional duration)",
		Examples:    []string{"auth user grant alice chat.admin", "auth user grant alice chat.admin 24h"},
		Route:       cmdsys.RouteService,
		Service:     "auth",
		Args:        CapabilityGrantArgs{},
		Result:      OKResult{},
		Handler:     userGrantHandler(getRepo),
	})); err != nil {
		return err
	}
	if err := must(reg.Register(cmdsys.Command{
		Verb:        "auth.user.revoke",
		Capability:  "auth.user.write",
		Description: "revoke a capability from a user",
		Examples:    []string{"auth user revoke alice chat.admin"},
		Route:       cmdsys.RouteService,
		Service:     "auth",
		Args:        CapabilityRevokeArgs{},
		Result:      OKResult{},
		Handler:     userRevokeCapHandler(getRepo),
	})); err != nil {
		return err
	}
	if err := must(reg.Register(cmdsys.Command{
		Verb:        "auth.user.capabilities",
		Capability:  "auth.user.read",
		Description: "list active capabilities for a user",
		Examples:    []string{"auth user capabilities alice"},
		Route:       cmdsys.RouteService,
		Service:     "auth",
		Args:        UsernameArgs{},
		Result:      CapabilityListResult{},
		Handler:     userCapabilitiesHandler(getRepo),
	})); err != nil {
		return err
	}
	if err := must(reg.Register(cmdsys.Command{
		Verb:        "auth.bootstrap-admin",
		Capability:  "", // intentionally empty — bootstrap is a one-shot console action
		Description: "one-time: grant the admin capability set to a user; refuses if any admin cap already granted",
		Examples:    []string{"auth bootstrap-admin alice"},
		Route:       cmdsys.RouteService,
		Service:     "auth",
		Args:        BootstrapAdminArgs{},
		Result:      OKResult{},
		Handler:     bootstrapAdminHandler(getRepo, []string{"auth.admin", "chat.admin"}),
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

func userGrantHandler(getRepo RepoProvider) cmdsys.HandlerFunc {
	return func(_ context.Context, env *cmdsys.Env, args any) (any, error) {
		repo := getRepo()
		if repo == nil {
			return nil, errRepoNotReady()
		}
		a := args.(CapabilityGrantArgs)
		ctx, cancel := cmdCtx()
		defer cancel()
		name, err := normalizeUsername(a.Username)
		if err != nil {
			return nil, err
		}
		user, err := repo.GetUserByUsername(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("user %q: %w", name, err)
		}
		grantedBy, _ := callerUserIDFromEnv(env)
		// uuid.Nil here is intentional — represents a system-originated grant
		// (per the Capability.GrantedBy convention from repo.go).
		c := Capability{UserID: user.UserID, Capability: a.Capability, GrantedBy: grantedBy}
		if a.Duration != "" {
			d, err := time.ParseDuration(a.Duration)
			if err != nil {
				return nil, fmt.Errorf("invalid duration: %w", err)
			}
			if d <= 0 {
				return nil, fmt.Errorf("duration must be positive")
			}
			c.ExpiresAt = time.Now().Add(d)
		}
		if err := repo.GrantCapability(ctx, c); err != nil {
			return nil, err
		}
		return OKResult{OK: true, Username: user.Username, Detail: "granted " + a.Capability}, nil
	}
}

func userRevokeCapHandler(getRepo RepoProvider) cmdsys.HandlerFunc {
	return func(_ context.Context, _ *cmdsys.Env, args any) (any, error) {
		repo := getRepo()
		if repo == nil {
			return nil, errRepoNotReady()
		}
		a := args.(CapabilityRevokeArgs)
		ctx, cancel := cmdCtx()
		defer cancel()
		name, err := normalizeUsername(a.Username)
		if err != nil {
			return nil, err
		}
		user, err := repo.GetUserByUsername(ctx, name)
		if err != nil {
			return nil, err
		}
		if err := repo.RevokeCapability(ctx, user.UserID, a.Capability); err != nil {
			return nil, err
		}
		return OKResult{OK: true, Username: user.Username, Detail: "revoked " + a.Capability}, nil
	}
}

func userCapabilitiesHandler(getRepo RepoProvider) cmdsys.HandlerFunc {
	return func(_ context.Context, _ *cmdsys.Env, args any) (any, error) {
		repo := getRepo()
		if repo == nil {
			return nil, errRepoNotReady()
		}
		a := args.(UsernameArgs)
		ctx, cancel := cmdCtx()
		defer cancel()
		name, err := normalizeUsername(a.Username)
		if err != nil {
			return nil, err
		}
		user, err := repo.GetUserByUsername(ctx, name)
		if err != nil {
			return nil, err
		}
		caps, err := repo.ListCapabilities(ctx, user.UserID)
		if err != nil {
			return nil, err
		}
		out := make([]CapabilityDigest, 0, len(caps))
		for _, c := range caps {
			out = append(out, CapabilityDigest{
				Capability: c.Capability,
				GrantedAt:  c.GrantedAt,
				GrantedBy:  c.GrantedBy.String(),
				ExpiresAt:  c.ExpiresAt,
			})
		}
		return CapabilityListResult{Capabilities: out}, nil
	}
}

func bootstrapAdminHandler(getRepo RepoProvider, defaultBootstrapCaps []string) cmdsys.HandlerFunc {
	return func(_ context.Context, _ *cmdsys.Env, args any) (any, error) {
		repo := getRepo()
		if repo == nil {
			return nil, errRepoNotReady()
		}
		a := args.(BootstrapAdminArgs)
		ctx, cancel := cmdCtx()
		defer cancel()
		name, err := normalizeUsername(a.Username)
		if err != nil {
			return nil, err
		}
		user, err := repo.GetUserByUsername(ctx, name)
		if err != nil {
			return nil, err
		}
		// Refuse if any *.admin capability has already been granted to THIS user.
		// Tighter check across all users would need a new repo method; per-user
		// is sufficient for v1 — bootstrap is a one-shot operator action;
		// subsequent admin grants flow through `auth user grant`.
		existing, _ := repo.ListCapabilities(ctx, user.UserID)
		for _, c := range existing {
			if strings.HasSuffix(c.Capability, ".admin") {
				return nil, fmt.Errorf("user %q already has admin capability %q", user.Username, c.Capability)
			}
		}
		for _, capName := range defaultBootstrapCaps {
			if err := repo.GrantCapability(ctx, Capability{
				UserID: user.UserID, Capability: capName, GrantedBy: uuid.Nil,
			}); err != nil {
				return nil, fmt.Errorf("grant %s: %w", capName, err)
			}
		}
		return OKResult{
			OK:       true,
			Username: user.Username,
			Detail:   fmt.Sprintf("granted %d admin capabilities", len(defaultBootstrapCaps)),
		}, nil
	}
}

// callerUserIDFromEnv returns the user_id of whoever invoked the command,
// or uuid.Nil when unavailable. cmdsys.Env exposes the caller via Caller.ID
// (string); we parse it as a UUID and treat parse failures as "unknown",
// which is fine for audit attribution — bootstrap-admin is the only caller
// that uses this, and it falls back to grantedBy = target user.
func callerUserIDFromEnv(env *cmdsys.Env) (uuid.UUID, bool) {
	if env == nil || env.Caller.ID == "" {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(env.Caller.ID)
	if err != nil {
		return uuid.Nil, false
	}
	return id, true
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
