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
	"github.com/zenion/mmoserver/pkg/ops"
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

// ChannelCreateArgs creates a new system_all or system_predicate channel.
// Custom channels are user-owned and created via the player op, not
// console.
type ChannelCreateArgs struct {
	Slug  string `cmd:"help=new channel slug"`
	Kind  string `cmd:"help=system_all|system_predicate|custom"`
	Topic string `cmd:"optional,help=initial topic"`
}

// ChannelRenameArgs renames an existing channel.
type ChannelRenameArgs struct {
	Slug    string `cmd:"help=current channel slug"`
	NewSlug string `cmd:"help=new channel slug"`
}

// ChannelTopicArgs sets a channel's topic line.
type ChannelTopicArgs struct {
	Slug  string `cmd:"help=channel slug"`
	Topic string `cmd:"help=new topic text"`
}

// ChannelSlowModeArgs sets a channel's slow-mode (per-user min delay between
// messages). Seconds is clamped to [0, 3600] server-side.
type ChannelSlowModeArgs struct {
	Slug    string `cmd:"help=channel slug"`
	Seconds int32  `cmd:"help=slow-mode delay in seconds (0 = off, max 3600)"`
}

// ChannelMemberArgs adds/removes a member.
type ChannelMemberArgs struct {
	Slug     string `cmd:"help=channel slug"`
	Username string `cmd:"help=target username,complete=players"`
	Role     string `cmd:"optional,help=role (member|admin); default member"`
}

// ChannelMemberRemoveArgs removes a member; no role.
type ChannelMemberRemoveArgs struct {
	Slug     string `cmd:"help=channel slug"`
	Username string `cmd:"help=target username,complete=players"`
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

	// --- Channel-mutation commands ---

	if err := must(reg.Register(cmdsys.Command{
		Verb:        "chat.channel.create",
		Capability:  "chat.admin",
		Description: "create a new system_all or system_predicate channel",
		Examples: []string{
			"chat channel create world system_all",
			"chat channel create guild:alpha system_predicate \"Alpha guild\"",
		},
		Route:   cmdsys.RouteLocal,
		Args:    ChannelCreateArgs{},
		Result:  OKResult{},
		Handler: channelCreateHandler(getSvc),
	})); err != nil {
		return err
	}
	if err := must(reg.Register(cmdsys.Command{
		Verb:        "chat.channel.delete",
		Capability:  "chat.admin",
		Description: "force-delete a chat channel and all its memberships",
		Examples:    []string{"chat channel delete trade"},
		Route:       cmdsys.RouteLocal,
		Args:        ChannelSlugArgs{},
		Result:      OKResult{},
		Handler:     channelDeleteHandler(getSvc),
	})); err != nil {
		return err
	}
	if err := must(reg.Register(cmdsys.Command{
		Verb:        "chat.channel.rename",
		Capability:  "chat.admin",
		Description: "rename a chat channel (cannot rename system_all)",
		Examples:    []string{"chat channel rename trade marketplace"},
		Route:       cmdsys.RouteLocal,
		Args:        ChannelRenameArgs{},
		Result:      OKResult{},
		Handler:     channelRenameHandler(getSvc),
	})); err != nil {
		return err
	}
	if err := must(reg.Register(cmdsys.Command{
		Verb:        "chat.channel.topic",
		Capability:  "chat.admin",
		Description: "set a channel's topic",
		Examples:    []string{"chat channel topic trade \"trade goods here\""},
		Route:       cmdsys.RouteLocal,
		Args:        ChannelTopicArgs{},
		Result:      OKResult{},
		Handler:     channelTopicHandler(getSvc),
	})); err != nil {
		return err
	}
	if err := must(reg.Register(cmdsys.Command{
		Verb:        "chat.channel.slowmode",
		Capability:  "chat.admin",
		Description: "set per-user slow-mode (0 = off, max 3600)",
		Examples:    []string{"chat channel slowmode trade 5"},
		Route:       cmdsys.RouteLocal,
		Args:        ChannelSlowModeArgs{},
		Result:      OKResult{},
		Handler:     channelSlowModeHandler(getSvc),
	})); err != nil {
		return err
	}

	// --- Membership-mutation commands ---

	if err := must(reg.Register(cmdsys.Command{
		Verb:        "chat.channel.addmember",
		Capability:  "chat.admin",
		Description: "add a user to a channel (default role: member)",
		Examples:    []string{"chat channel addmember trade alice", "chat channel addmember trade bob admin"},
		Route:       cmdsys.RouteLocal,
		Args:        ChannelMemberArgs{},
		Result:      OKResult{},
		Handler:     channelAddMemberHandler(getSvc, getAuth),
	})); err != nil {
		return err
	}
	if err := must(reg.Register(cmdsys.Command{
		Verb:        "chat.channel.removemember",
		Capability:  "chat.admin",
		Description: "remove a user from a channel",
		Examples:    []string{"chat channel removemember trade alice"},
		Route:       cmdsys.RouteLocal,
		Args:        ChannelMemberRemoveArgs{},
		Result:      OKResult{},
		Handler:     channelRemoveMemberHandler(getSvc, getAuth),
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

// --- membership-mutation handlers ---

func channelAddMemberHandler(getSvc ServiceProvider, getAuth auth.RepoProvider) cmdsys.HandlerFunc {
	return func(_ context.Context, env *cmdsys.Env, raw any) (any, error) {
		svc := getSvc()
		if svc == nil {
			return nil, errSvcNotReady()
		}
		authRepo := getAuth()
		if authRepo == nil {
			return nil, errAuthRepoNotReady()
		}
		args := raw.(ChannelMemberArgs)
		chID, _, err := resolveChannelBySlug(svc, args.Slug)
		if err != nil {
			return nil, err
		}
		ctx, cancel := cmdCtx()
		defer cancel()
		target, err := resolveTargetUser(ctx, authRepo, args.Username)
		if err != nil {
			return nil, err
		}
		connID, _, err := resolveOperatorConn(svc, env)
		if err != nil {
			return nil, err
		}
		resp, _ := svc.HandleAddMember(&ops.OpContext{ConnID: connID}, &ChatAddMemberRequest{
			ChannelID: chID.String(),
			UserID:    target.UserID.String(),
			Role:      args.Role,
		})
		if resp.ErrorCode != 0 {
			return nil, chatErrToError(resp.ErrorCode, resp.ErrorMessage)
		}
		role := args.Role
		if role == "" {
			role = "member"
		}
		return OKResult{
			OK:       true,
			Username: target.Username,
			Detail:   fmt.Sprintf("added %s to %s as %s", target.Username, args.Slug, role),
		}, nil
	}
}

func channelRemoveMemberHandler(getSvc ServiceProvider, getAuth auth.RepoProvider) cmdsys.HandlerFunc {
	return func(_ context.Context, env *cmdsys.Env, raw any) (any, error) {
		svc := getSvc()
		if svc == nil {
			return nil, errSvcNotReady()
		}
		authRepo := getAuth()
		if authRepo == nil {
			return nil, errAuthRepoNotReady()
		}
		args := raw.(ChannelMemberRemoveArgs)
		chID, _, err := resolveChannelBySlug(svc, args.Slug)
		if err != nil {
			return nil, err
		}
		ctx, cancel := cmdCtx()
		defer cancel()
		target, err := resolveTargetUser(ctx, authRepo, args.Username)
		if err != nil {
			return nil, err
		}
		connID, _, err := resolveOperatorConn(svc, env)
		if err != nil {
			return nil, err
		}
		resp, _ := svc.HandleRemoveMember(&ops.OpContext{ConnID: connID}, &ChatRemoveMemberRequest{
			ChannelID: chID.String(),
			UserID:    target.UserID.String(),
		})
		if resp.ErrorCode != 0 {
			return nil, chatErrToError(resp.ErrorCode, resp.ErrorMessage)
		}
		return OKResult{
			OK:       true,
			Username: target.Username,
			Detail:   fmt.Sprintf("removed %s from %s", target.Username, args.Slug),
		}, nil
	}
}

// --- channel-mutation handlers ---

func channelCreateHandler(getSvc ServiceProvider) cmdsys.HandlerFunc {
	return func(_ context.Context, env *cmdsys.Env, raw any) (any, error) {
		svc := getSvc()
		if svc == nil {
			return nil, errSvcNotReady()
		}
		args := raw.(ChannelCreateArgs)
		kind := channelKindFromString(args.Kind)
		if kind == ChannelKindUnspecified {
			return nil, fmt.Errorf("invalid kind %q (use system_all|system_predicate|custom)", args.Kind)
		}
		if kind == ChannelKindCustom {
			return nil, errors.New("custom channels are user-owned; use the in-game create op, not console")
		}
		connID, _, err := resolveOperatorConn(svc, env)
		if err != nil {
			return nil, err
		}
		resp, _ := svc.HandleRegisterChannel(&ops.OpContext{ConnID: connID}, &ChatRegisterChannelRequest{
			Slug:  args.Slug,
			Kind:  kind,
			Topic: args.Topic,
		})
		if resp.ErrorCode != 0 {
			return nil, chatErrToError(resp.ErrorCode, resp.ErrorMessage)
		}
		return OKResult{
			OK:     true,
			Detail: fmt.Sprintf("created channel %s (%s)", args.Slug, args.Kind),
		}, nil
	}
}

func channelDeleteHandler(getSvc ServiceProvider) cmdsys.HandlerFunc {
	return func(_ context.Context, env *cmdsys.Env, raw any) (any, error) {
		svc := getSvc()
		if svc == nil {
			return nil, errSvcNotReady()
		}
		args := raw.(ChannelSlugArgs)
		chID, _, err := resolveChannelBySlug(svc, args.Slug)
		if err != nil {
			return nil, err
		}
		connID, _, err := resolveOperatorConn(svc, env)
		if err != nil {
			return nil, err
		}
		resp, _ := svc.HandleUnregisterChannel(&ops.OpContext{ConnID: connID}, &ChatUnregisterChannelRequest{
			ChannelID: chID.String(),
		})
		if resp.ErrorCode != 0 {
			return nil, chatErrToError(resp.ErrorCode, resp.ErrorMessage)
		}
		return OKResult{
			OK:     true,
			Detail: fmt.Sprintf("deleted channel %s", args.Slug),
		}, nil
	}
}

func channelRenameHandler(getSvc ServiceProvider) cmdsys.HandlerFunc {
	return func(_ context.Context, env *cmdsys.Env, raw any) (any, error) {
		svc := getSvc()
		if svc == nil {
			return nil, errSvcNotReady()
		}
		args := raw.(ChannelRenameArgs)
		chID, _, err := resolveChannelBySlug(svc, args.Slug)
		if err != nil {
			return nil, err
		}
		connID, _, err := resolveOperatorConn(svc, env)
		if err != nil {
			return nil, err
		}
		resp, _ := svc.HandleRenameChannel(&ops.OpContext{ConnID: connID}, &ChatRenameChannelRequest{
			ChannelID: chID.String(),
			NewSlug:   args.NewSlug,
		})
		if resp.ErrorCode != 0 {
			return nil, chatErrToError(resp.ErrorCode, resp.ErrorMessage)
		}
		return OKResult{
			OK:     true,
			Detail: fmt.Sprintf("renamed %s -> %s", args.Slug, args.NewSlug),
		}, nil
	}
}

func channelTopicHandler(getSvc ServiceProvider) cmdsys.HandlerFunc {
	return func(_ context.Context, env *cmdsys.Env, raw any) (any, error) {
		svc := getSvc()
		if svc == nil {
			return nil, errSvcNotReady()
		}
		args := raw.(ChannelTopicArgs)
		chID, _, err := resolveChannelBySlug(svc, args.Slug)
		if err != nil {
			return nil, err
		}
		connID, _, err := resolveOperatorConn(svc, env)
		if err != nil {
			return nil, err
		}
		resp, _ := svc.HandleSetTopic(&ops.OpContext{ConnID: connID}, &ChatSetTopicRequest{
			ChannelID: chID.String(),
			Topic:     args.Topic,
		})
		if resp.ErrorCode != 0 {
			return nil, chatErrToError(resp.ErrorCode, resp.ErrorMessage)
		}
		return OKResult{
			OK:     true,
			Detail: fmt.Sprintf("topic set on %s", args.Slug),
		}, nil
	}
}

func channelSlowModeHandler(getSvc ServiceProvider) cmdsys.HandlerFunc {
	return func(_ context.Context, env *cmdsys.Env, raw any) (any, error) {
		svc := getSvc()
		if svc == nil {
			return nil, errSvcNotReady()
		}
		args := raw.(ChannelSlowModeArgs)
		chID, _, err := resolveChannelBySlug(svc, args.Slug)
		if err != nil {
			return nil, err
		}
		connID, _, err := resolveOperatorConn(svc, env)
		if err != nil {
			return nil, err
		}
		resp, _ := svc.HandleSetSlowMode(&ops.OpContext{ConnID: connID}, &ChatSetSlowModeRequest{
			ChannelID: chID.String(),
			Seconds:   args.Seconds,
		})
		if resp.ErrorCode != 0 {
			return nil, chatErrToError(resp.ErrorCode, resp.ErrorMessage)
		}
		return OKResult{
			OK:     true,
			Detail: fmt.Sprintf("slow-mode set to %ds on %s", args.Seconds, args.Slug),
		}, nil
	}
}
