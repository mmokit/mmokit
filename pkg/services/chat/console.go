package chat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/services/auth"
)

// ServiceProvider returns the live *Service or nil before Service.Init has
// resolved it. Console handlers call it at execution time so registration
// can happen at facade-time (before Build) without holding the live service.
type ServiceProvider func() *Service

// serviceSlot is a goroutine-safe holder for the live *Service. The chat
// Service's OnReady callback writes once at Init; console handlers read
// every time they execute. mmokit.RegisterChatService creates one and
// wires both sides.
type serviceSlot struct {
	mu  sync.RWMutex
	svc *Service
}

// NewServiceSlot returns an empty slot wired with a setter and a getter.
// Setter is intended for ServiceOpts.OnReady; getter for the cmdsys
// handlers in this file.
func NewServiceSlot() (set func(*Service), get ServiceProvider) {
	s := &serviceSlot{}
	set = func(svc *Service) {
		s.mu.Lock()
		s.svc = svc
		s.mu.Unlock()
	}
	get = func() *Service {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.svc
	}
	return set, get
}

// --- Args / Result types (must be exported structs for cmdsys schema gen) ---

// ChannelSlugArgs is used by commands that act on a single channel
// identified by slug (info, delete, etc).
type ChannelSlugArgs struct {
	Slug string `cmd:"help=channel slug,complete=chat-channels"`
}

// ChannelInfoResult is the console-friendly mirror of ChannelInfo.
// Field ordering and types are stable for cmdsys schema generation.
type ChannelInfoResult struct {
	ChannelID       string
	Slug            string
	Kind            string
	Topic           string
	SlowModeSeconds int32
	OwnerUserID     string
	MemberCount     int32
	HasPassword     bool
}

// ChannelListResult is the result of chat.channel.list.
type ChannelListResult struct {
	Channels []ChannelInfoResult
}

// OKResult is the standard success-with-detail result for mutating console
// commands. Username is populated when the command targeted a specific user.
type OKResult struct {
	OK       bool
	Username string
	Detail   string
}

// --- RegisterConsoleCommands ---

// RegisterConsoleCommands wires the chat.* command group into the cmdsys
// dispatcher. Handlers call getSvc()/getAuth() at execution time so
// registration can happen at facade-time before the chat Service has been
// constructed.
//
// Returns an error if any command fails to register (duplicate verb,
// schema-hash failure, etc).
//
// Subsequent commits append channel-mutation, membership, moderation, and
// broadcast/msg.delete commands to this single registration entrypoint.
func RegisterConsoleCommands(reg *cmdsys.Registry, getSvc ServiceProvider, getAuth auth.RepoProvider) error {
	must := func(err error) error {
		if err != nil {
			return fmt.Errorf("chat console: %w", err)
		}
		return nil
	}
	_ = getAuth // wired in subsequent commits (resolves usernames → auth.User)

	// --- Read-only commands (no operator-online check needed) ---

	if err := must(reg.Register(cmdsys.Command{
		Verb:        "chat.channel.list",
		Capability:  "chat.admin",
		Description: "list every chat channel known to the cluster",
		Examples:    []string{"chat channel list"},
		Route:       cmdsys.RouteLocal,
		Args:        struct{}{},
		Result:      ChannelListResult{},
		Handler:     channelListHandler(getSvc),
	})); err != nil {
		return err
	}
	if err := must(reg.Register(cmdsys.Command{
		Verb:        "chat.channel.info",
		Capability:  "chat.admin",
		Description: "show details for a single chat channel",
		Examples:    []string{"chat channel info world"},
		Route:       cmdsys.RouteLocal,
		Args:        ChannelSlugArgs{},
		Result:      ChannelInfoResult{},
		Handler:     channelInfoHandler(getSvc),
	})); err != nil {
		return err
	}
	return nil
}

// --- handler shared helpers ---

func errSvcNotReady() error {
	return errors.New("chat service not initialized yet — try again after the cluster finishes starting")
}

func errAuthRepoNotReady() error {
	return errors.New("auth repository not wired (chat console needs the auth service to resolve usernames)")
}

func cmdCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 10*time.Second)
}

// resolveOperatorConn looks up the cmdsys caller's user_id in the chat
// service's online map and returns their connID. Console moderation
// commands require the operator to be online (logged in to the game),
// since the underlying Handle* methods resolve callerID from connID via
// connIndex. Returns an error string the operator can act on.
//
// The caller's user_id is parsed from env.Caller.ID (cmdsys.Caller.ID is
// set to the operator's UUID string when authenticated via the auth
// service).
//
// Note: read-only commands (list, info) do not require operator-online —
// they don't dispatch through Handle* methods.
func resolveOperatorConn(svc *Service, env *cmdsys.Env) (uint32, uuid.UUID, error) {
	if env == nil || env.Caller.ID == "" {
		return 0, uuid.Nil, errors.New("no operator identity in env (caller ID missing)")
	}
	operatorID, err := uuid.Parse(env.Caller.ID)
	if err != nil {
		return 0, uuid.Nil, fmt.Errorf("invalid operator user_id %q: %w", env.Caller.ID, err)
	}
	connID, online := svc.OnlineConnIDForUser(operatorID)
	if !online {
		return 0, uuid.Nil, errors.New("operator must be logged into the game to use chat console moderation commands")
	}
	return connID, operatorID, nil
}

// resolveChannelBySlug translates a slug to a channelID + Channel snapshot.
func resolveChannelBySlug(svc *Service, slug string) (uuid.UUID, Channel, error) {
	id, ok := svc.ChannelIDBySlug(slug)
	if !ok {
		return uuid.Nil, Channel{}, fmt.Errorf("channel %q not found", slug)
	}
	c, ok := svc.ChannelByID(id)
	if !ok {
		// Race window: slug found but channel was just deleted. Treat as
		// not-found.
		return uuid.Nil, Channel{}, fmt.Errorf("channel %q not found", slug)
	}
	return id, c, nil
}

// resolveTargetUser returns the auth.User row for username (after lower-
// casing/trimming).
func resolveTargetUser(ctx context.Context, authRepo auth.Repository, username string) (auth.User, error) {
	name := strings.ToLower(strings.TrimSpace(username))
	if name == "" {
		return auth.User{}, errors.New("empty username")
	}
	u, err := authRepo.GetUserByUsername(ctx, name)
	if err != nil {
		return auth.User{}, fmt.Errorf("user %q: %w", name, err)
	}
	return u, nil
}

// fromChannelInfo converts the wire-shape ChannelInfo to the console
// result shape (string Kind for human-readability).
func fromChannelInfo(ci ChannelInfo) ChannelInfoResult {
	return ChannelInfoResult{
		ChannelID:       ci.ChannelID,
		Slug:            ci.Slug,
		Kind:            channelKindToString(ci.Kind),
		Topic:           ci.Topic,
		SlowModeSeconds: ci.SlowModeSeconds,
		OwnerUserID:     ci.OwnerUserID,
		MemberCount:     ci.MemberCount,
		HasPassword:     ci.HasPassword,
	}
}

// chatErrToError converts a non-zero ChatError ErrorBlock to a Go error.
func chatErrToError(code uint32, msg string) error {
	if code == 0 {
		return nil
	}
	if msg == "" {
		return fmt.Errorf("chat error %d", code)
	}
	return fmt.Errorf("%s (chat error %d)", msg, code)
}

// --- read-only handlers ---

func channelListHandler(getSvc ServiceProvider) cmdsys.HandlerFunc {
	return func(_ context.Context, _ *cmdsys.Env, _ any) (any, error) {
		svc := getSvc()
		if svc == nil {
			return nil, errSvcNotReady()
		}
		chans := svc.ListChannelsSnapshot()
		out := make([]ChannelInfoResult, 0, len(chans))
		for _, c := range chans {
			ci := channelInfoOf(c, svc.MemberCountForChannel(c.ChannelID))
			out = append(out, fromChannelInfo(ci))
		}
		return ChannelListResult{Channels: out}, nil
	}
}

func channelInfoHandler(getSvc ServiceProvider) cmdsys.HandlerFunc {
	return func(_ context.Context, _ *cmdsys.Env, raw any) (any, error) {
		svc := getSvc()
		if svc == nil {
			return nil, errSvcNotReady()
		}
		args := raw.(ChannelSlugArgs)
		id, c, err := resolveChannelBySlug(svc, args.Slug)
		if err != nil {
			return nil, err
		}
		ci := channelInfoOf(c, svc.MemberCountForChannel(id))
		return fromChannelInfo(ci), nil
	}
}

// channelInfoOf is the unlocked sibling of channelInfoOfLocked (in
// handlers.go). Caller is responsible for thread-safe access; we use
// it from console handlers that hold neither read nor write lock — the
// inputs are already snapshots from the lock-holding accessors.
func channelInfoOf(c Channel, memberCount int) ChannelInfo {
	owner := ""
	if c.OwnerUserID != uuid.Nil {
		owner = c.OwnerUserID.String()
	}
	return ChannelInfo{
		ChannelID:       c.ChannelID.String(),
		Slug:            c.Slug,
		Kind:            channelKindFromString(c.Kind),
		Topic:           c.Topic,
		SlowModeSeconds: int32(c.SlowModeSeconds),
		OwnerUserID:     owner,
		MemberCount:     int32(memberCount),
		HasPassword:     c.PasswordHash != "",
	}
}

// Sentinel placeholders to prevent "declared and not used" warnings on
// helpers that are defined in this commit but only consumed by the
// channel-mutation / membership / moderation / broadcast commits to follow.
// Each subsequent commit removes the entry it now actually uses.
var (
	_ = resolveOperatorConn
	_ = resolveTargetUser
	_ = errAuthRepoNotReady
	_ = cmdCtx
	_ = chatErrToError
)
