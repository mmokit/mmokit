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

// UserMuteArgs mutes a user globally (Channel="") or per-channel.
type UserMuteArgs struct {
	Username string `cmd:"help=target username,complete=players"`
	Duration string `cmd:"help=mute duration (e.g. 5m, 1h)"`
	Channel  string `cmd:"optional,help=channel slug; empty = global mute"`
}

// UserUnmuteArgs unmutes globally or per-channel.
type UserUnmuteArgs struct {
	Username string `cmd:"help=target username,complete=players"`
	Channel  string `cmd:"optional,help=channel slug; empty = clear global mute"`
}

// UserKickArgs kicks a user from a channel (no duration).
type UserKickArgs struct {
	Username string `cmd:"help=target username,complete=players"`
	Channel  string `cmd:"help=channel slug"`
}

// UserBanArgs bans a user from a channel for a duration.
type UserBanArgs struct {
	Username string `cmd:"help=target username,complete=players"`
	Channel  string `cmd:"help=channel slug"`
	Duration string `cmd:"help=ban duration (e.g. 1h, 24h)"`
}

// UserUnbanArgs lifts a channel ban.
type UserUnbanArgs struct {
	Username string `cmd:"help=target username,complete=players"`
	Channel  string `cmd:"help=channel slug"`
}

// BroadcastArgs sends a system-authored message to a channel.
type BroadcastArgs struct {
	Channel string `cmd:"help=channel slug"`
	Body    string `cmd:"help=message body"`
}

// MsgDeleteArgs removes a previously-sent message from clients' views.
type MsgDeleteArgs struct {
	MsgID   string `cmd:"help=message UUID"`
	Channel string `cmd:"help=channel slug"`
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
	Channels []ChannelInfoResult `cmd:"table"`
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

	// --- Read-only commands (no operator-online check needed) ---

	if err := must(reg.Register(cmdsys.Command{
		Verb:        "chat.channel.list",
		Capability:  "chat.admin",
		Description: "list every chat channel known to the cluster",
		Examples:    []string{"chat channel list"},
		Route:       cmdsys.RouteService,
		Service:     "chat",
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
		Route:       cmdsys.RouteService,
		Service:     "chat",
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
		Route:   cmdsys.RouteService,
		Service: "chat",
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
		Route:       cmdsys.RouteService,
		Service:     "chat",
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
		Route:       cmdsys.RouteService,
		Service:     "chat",
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
		Route:       cmdsys.RouteService,
		Service:     "chat",
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
		Route:       cmdsys.RouteService,
		Service:     "chat",
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
		Route:       cmdsys.RouteService,
		Service:     "chat",
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
		Route:       cmdsys.RouteService,
		Service:     "chat",
		Args:        ChannelMemberRemoveArgs{},
		Result:      OKResult{},
		Handler:     channelRemoveMemberHandler(getSvc, getAuth),
	})); err != nil {
		return err
	}

	// --- Per-user moderation commands ---

	if err := must(reg.Register(cmdsys.Command{
		Verb:        "chat.user.mute",
		Capability:  "chat.admin",
		Description: "mute a user globally (no channel) or in a specific channel",
		Examples:    []string{"chat user mute alice 15m", "chat user mute alice 1h trade"},
		Route:       cmdsys.RouteService,
		Service:     "chat",
		Args:        UserMuteArgs{},
		Result:      OKResult{},
		Handler:     userMuteHandler(getSvc, getAuth),
	})); err != nil {
		return err
	}
	if err := must(reg.Register(cmdsys.Command{
		Verb:        "chat.user.unmute",
		Capability:  "chat.admin",
		Description: "lift a global or per-channel mute",
		Examples:    []string{"chat user unmute alice", "chat user unmute alice trade"},
		Route:       cmdsys.RouteService,
		Service:     "chat",
		Args:        UserUnmuteArgs{},
		Result:      OKResult{},
		Handler:     userUnmuteHandler(getSvc, getAuth),
	})); err != nil {
		return err
	}
	if err := must(reg.Register(cmdsys.Command{
		Verb:        "chat.user.kick",
		Capability:  "chat.admin",
		Description: "kick a user from a channel (no duration)",
		Examples:    []string{"chat user kick alice trade"},
		Route:       cmdsys.RouteService,
		Service:     "chat",
		Args:        UserKickArgs{},
		Result:      OKResult{},
		Handler:     userKickHandler(getSvc, getAuth),
	})); err != nil {
		return err
	}
	if err := must(reg.Register(cmdsys.Command{
		Verb:        "chat.user.ban",
		Capability:  "chat.admin",
		Description: "ban a user from a channel for a duration",
		Examples:    []string{"chat user ban alice trade 24h"},
		Route:       cmdsys.RouteService,
		Service:     "chat",
		Args:        UserBanArgs{},
		Result:      OKResult{},
		Handler:     userBanHandler(getSvc, getAuth),
	})); err != nil {
		return err
	}
	if err := must(reg.Register(cmdsys.Command{
		Verb:        "chat.user.unban",
		Capability:  "chat.admin",
		Description: "lift a per-channel ban",
		Examples:    []string{"chat user unban alice trade"},
		Route:       cmdsys.RouteService,
		Service:     "chat",
		Args:        UserUnbanArgs{},
		Result:      OKResult{},
		Handler:     userUnbanHandler(getSvc, getAuth),
	})); err != nil {
		return err
	}

	// --- Cluster-wide moderation commands ---

	if err := must(reg.Register(cmdsys.Command{
		Verb:        "chat.broadcast",
		Capability:  "chat.admin",
		Description: "broadcast a system message to a channel (sender shown as system)",
		Examples:    []string{"chat broadcast world \"server restarting in 5 minutes\""},
		Route:       cmdsys.RouteService,
		Service:     "chat",
		Args:        BroadcastArgs{},
		Result:      OKResult{},
		Handler:     broadcastHandler(getSvc),
	})); err != nil {
		return err
	}
	if err := must(reg.Register(cmdsys.Command{
		Verb:        "chat.msg.delete",
		Capability:  "chat.admin",
		Description: "delete a previously-sent channel message from clients' views",
		Examples:    []string{"chat msg delete 0190abcd-... trade"},
		Route:       cmdsys.RouteService,
		Service:     "chat",
		Args:        MsgDeleteArgs{},
		Result:      OKResult{},
		Handler:     msgDeleteHandler(getSvc),
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

// operatorOpContext builds the *ops.OpContext that console moderation
// wrappers pass into chat Handle* methods. Two paths:
//
//   - Bare interactive console (cmdsys.SourceConsole, sentinel ID like
//     "console" that doesn't parse as a UUID): returns SystemTrusted=true.
//     Authorization came from the cmdsys grant check upstream; chat
//     handlers see callerID == SystemCallerID and bypass connIndex /
//     hasGlobalAdmin checks.
//
//   - Authenticated operator (cmdsys caller carries a real UUID, e.g.
//     from MeshControl or test fixtures): looks the operator up in
//     chat's online map and pins opCtx.ConnID so the handler can
//     resolve callerID via connIndex and re-check hasGlobalAdmin
//     (defense in depth). Errors when the operator isn't online.
//
// Read-only commands (list, info) don't dispatch through Handle*
// methods and skip this helper entirely.
func operatorOpContext(svc *Service, env *cmdsys.Env) (*ops.OpContext, uuid.UUID, error) {
	if env == nil || env.Caller.ID == "" {
		return nil, uuid.Nil, errors.New("no operator identity in env (caller ID missing)")
	}
	operatorID, err := uuid.Parse(env.Caller.ID)
	if err != nil {
		// Non-UUID caller ID is only legitimate for the bare interactive
		// console (env.Caller.ID == "console"). Trust comes from cmdsys
		// grants, not chat-side identity.
		if env.Caller.Source == cmdsys.SourceConsole {
			return &ops.OpContext{SystemTrusted: true}, uuid.Nil, nil
		}
		return nil, uuid.Nil, fmt.Errorf("invalid operator user_id %q: %w", env.Caller.ID, err)
	}
	connID, online := svc.OnlineConnIDForUser(operatorID)
	if !online {
		return nil, uuid.Nil, errors.New("operator must be logged into the game to use chat console moderation commands")
	}
	return &ops.OpContext{ConnID: connID}, operatorID, nil
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
		opCtx, _, err := operatorOpContext(svc, env)
		if err != nil {
			return nil, err
		}
		resp, _ := svc.HandleAddMember(opCtx, &ChatAddMemberRequest{
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
		opCtx, _, err := operatorOpContext(svc, env)
		if err != nil {
			return nil, err
		}
		resp, _ := svc.HandleRemoveMember(opCtx, &ChatRemoveMemberRequest{
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

// --- per-user moderation handlers ---

func userMuteHandler(getSvc ServiceProvider, getAuth auth.RepoProvider) cmdsys.HandlerFunc {
	return func(_ context.Context, env *cmdsys.Env, raw any) (any, error) {
		svc := getSvc()
		if svc == nil {
			return nil, errSvcNotReady()
		}
		authRepo := getAuth()
		if authRepo == nil {
			return nil, errAuthRepoNotReady()
		}
		args := raw.(UserMuteArgs)
		dur, err := time.ParseDuration(args.Duration)
		if err != nil {
			return nil, fmt.Errorf("invalid duration %q: %w", args.Duration, err)
		}
		if dur <= 0 {
			return nil, errors.New("duration must be positive")
		}
		ctx, cancel := cmdCtx()
		defer cancel()
		target, err := resolveTargetUser(ctx, authRepo, args.Username)
		if err != nil {
			return nil, err
		}
		var chIDStr string
		var scope string
		if args.Channel != "" {
			id, _, err := resolveChannelBySlug(svc, args.Channel)
			if err != nil {
				return nil, err
			}
			chIDStr = id.String()
			scope = "in " + args.Channel
		} else {
			scope = "globally"
		}
		opCtx, _, err := operatorOpContext(svc, env)
		if err != nil {
			return nil, err
		}
		resp, _ := svc.HandleMuteUser(opCtx, &ChatMuteUserRequest{
			UserID:     target.UserID.String(),
			ChannelID:  chIDStr,
			DurationMs: dur.Milliseconds(),
		})
		if resp.ErrorCode != 0 {
			return nil, chatErrToError(resp.ErrorCode, resp.ErrorMessage)
		}
		return OKResult{
			OK:       true,
			Username: target.Username,
			Detail:   fmt.Sprintf("muted %s %s for %s", target.Username, scope, dur),
		}, nil
	}
}

func userUnmuteHandler(getSvc ServiceProvider, getAuth auth.RepoProvider) cmdsys.HandlerFunc {
	return func(_ context.Context, env *cmdsys.Env, raw any) (any, error) {
		svc := getSvc()
		if svc == nil {
			return nil, errSvcNotReady()
		}
		authRepo := getAuth()
		if authRepo == nil {
			return nil, errAuthRepoNotReady()
		}
		args := raw.(UserUnmuteArgs)
		ctx, cancel := cmdCtx()
		defer cancel()
		target, err := resolveTargetUser(ctx, authRepo, args.Username)
		if err != nil {
			return nil, err
		}
		var chIDStr string
		var scope string
		if args.Channel != "" {
			id, _, err := resolveChannelBySlug(svc, args.Channel)
			if err != nil {
				return nil, err
			}
			chIDStr = id.String()
			scope = "in " + args.Channel
		} else {
			scope = "globally"
		}
		opCtx, _, err := operatorOpContext(svc, env)
		if err != nil {
			return nil, err
		}
		resp, _ := svc.HandleUnmuteUser(opCtx, &ChatUnmuteUserRequest{
			UserID:    target.UserID.String(),
			ChannelID: chIDStr,
		})
		if resp.ErrorCode != 0 {
			return nil, chatErrToError(resp.ErrorCode, resp.ErrorMessage)
		}
		return OKResult{
			OK:       true,
			Username: target.Username,
			Detail:   fmt.Sprintf("unmuted %s %s", target.Username, scope),
		}, nil
	}
}

func userKickHandler(getSvc ServiceProvider, getAuth auth.RepoProvider) cmdsys.HandlerFunc {
	return func(_ context.Context, env *cmdsys.Env, raw any) (any, error) {
		svc := getSvc()
		if svc == nil {
			return nil, errSvcNotReady()
		}
		authRepo := getAuth()
		if authRepo == nil {
			return nil, errAuthRepoNotReady()
		}
		args := raw.(UserKickArgs)
		chID, _, err := resolveChannelBySlug(svc, args.Channel)
		if err != nil {
			return nil, err
		}
		ctx, cancel := cmdCtx()
		defer cancel()
		target, err := resolveTargetUser(ctx, authRepo, args.Username)
		if err != nil {
			return nil, err
		}
		opCtx, _, err := operatorOpContext(svc, env)
		if err != nil {
			return nil, err
		}
		resp, _ := svc.HandleKickFromChannel(opCtx, &ChatKickRequest{
			ChannelID: chID.String(),
			UserID:    target.UserID.String(),
		})
		if resp.ErrorCode != 0 {
			return nil, chatErrToError(resp.ErrorCode, resp.ErrorMessage)
		}
		return OKResult{
			OK:       true,
			Username: target.Username,
			Detail:   fmt.Sprintf("kicked %s from %s", target.Username, args.Channel),
		}, nil
	}
}

func userBanHandler(getSvc ServiceProvider, getAuth auth.RepoProvider) cmdsys.HandlerFunc {
	return func(_ context.Context, env *cmdsys.Env, raw any) (any, error) {
		svc := getSvc()
		if svc == nil {
			return nil, errSvcNotReady()
		}
		authRepo := getAuth()
		if authRepo == nil {
			return nil, errAuthRepoNotReady()
		}
		args := raw.(UserBanArgs)
		dur, err := time.ParseDuration(args.Duration)
		if err != nil {
			return nil, fmt.Errorf("invalid duration %q: %w", args.Duration, err)
		}
		if dur <= 0 {
			return nil, errors.New("duration must be positive")
		}
		chID, _, err := resolveChannelBySlug(svc, args.Channel)
		if err != nil {
			return nil, err
		}
		ctx, cancel := cmdCtx()
		defer cancel()
		target, err := resolveTargetUser(ctx, authRepo, args.Username)
		if err != nil {
			return nil, err
		}
		opCtx, _, err := operatorOpContext(svc, env)
		if err != nil {
			return nil, err
		}
		resp, _ := svc.HandleBanFromChannel(opCtx, &ChatBanRequest{
			ChannelID:  chID.String(),
			UserID:     target.UserID.String(),
			DurationMs: dur.Milliseconds(),
		})
		if resp.ErrorCode != 0 {
			return nil, chatErrToError(resp.ErrorCode, resp.ErrorMessage)
		}
		return OKResult{
			OK:       true,
			Username: target.Username,
			Detail:   fmt.Sprintf("banned %s from %s for %s", target.Username, args.Channel, dur),
		}, nil
	}
}

func userUnbanHandler(getSvc ServiceProvider, getAuth auth.RepoProvider) cmdsys.HandlerFunc {
	return func(_ context.Context, env *cmdsys.Env, raw any) (any, error) {
		svc := getSvc()
		if svc == nil {
			return nil, errSvcNotReady()
		}
		authRepo := getAuth()
		if authRepo == nil {
			return nil, errAuthRepoNotReady()
		}
		args := raw.(UserUnbanArgs)
		chID, _, err := resolveChannelBySlug(svc, args.Channel)
		if err != nil {
			return nil, err
		}
		ctx, cancel := cmdCtx()
		defer cancel()
		target, err := resolveTargetUser(ctx, authRepo, args.Username)
		if err != nil {
			return nil, err
		}
		opCtx, _, err := operatorOpContext(svc, env)
		if err != nil {
			return nil, err
		}
		resp, _ := svc.HandleUnbanFromChannel(opCtx, &ChatUnbanRequest{
			ChannelID: chID.String(),
			UserID:    target.UserID.String(),
		})
		if resp.ErrorCode != 0 {
			return nil, chatErrToError(resp.ErrorCode, resp.ErrorMessage)
		}
		return OKResult{
			OK:       true,
			Username: target.Username,
			Detail:   fmt.Sprintf("unbanned %s from %s", target.Username, args.Channel),
		}, nil
	}
}

// --- broadcast / msg.delete handlers ---

func broadcastHandler(getSvc ServiceProvider) cmdsys.HandlerFunc {
	return func(_ context.Context, env *cmdsys.Env, raw any) (any, error) {
		svc := getSvc()
		if svc == nil {
			return nil, errSvcNotReady()
		}
		args := raw.(BroadcastArgs)
		chID, _, err := resolveChannelBySlug(svc, args.Channel)
		if err != nil {
			return nil, err
		}
		opCtx, _, err := operatorOpContext(svc, env)
		if err != nil {
			return nil, err
		}
		resp, _ := svc.HandleBroadcastSystem(opCtx, &ChatBroadcastRequest{
			ChannelID: chID.String(),
			Body:      args.Body,
		})
		if resp.ErrorCode != 0 {
			return nil, chatErrToError(resp.ErrorCode, resp.ErrorMessage)
		}
		return OKResult{
			OK:     true,
			Detail: fmt.Sprintf("broadcast to %s (%d bytes)", args.Channel, len(args.Body)),
		}, nil
	}
}

func msgDeleteHandler(getSvc ServiceProvider) cmdsys.HandlerFunc {
	return func(_ context.Context, env *cmdsys.Env, raw any) (any, error) {
		svc := getSvc()
		if svc == nil {
			return nil, errSvcNotReady()
		}
		args := raw.(MsgDeleteArgs)
		chID, _, err := resolveChannelBySlug(svc, args.Channel)
		if err != nil {
			return nil, err
		}
		opCtx, _, err := operatorOpContext(svc, env)
		if err != nil {
			return nil, err
		}
		resp, _ := svc.HandleDeleteMessage(opCtx, &ChatDeleteMessageRequest{
			MsgID:     args.MsgID,
			ChannelID: chID.String(),
		})
		if resp.ErrorCode != 0 {
			return nil, chatErrToError(resp.ErrorCode, resp.ErrorMessage)
		}
		return OKResult{
			OK:     true,
			Detail: fmt.Sprintf("deleted msg %s in %s", args.MsgID, args.Channel),
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
		opCtx, _, err := operatorOpContext(svc, env)
		if err != nil {
			return nil, err
		}
		resp, _ := svc.HandleRegisterChannel(opCtx, &ChatRegisterChannelRequest{
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
		opCtx, _, err := operatorOpContext(svc, env)
		if err != nil {
			return nil, err
		}
		resp, _ := svc.HandleUnregisterChannel(opCtx, &ChatUnregisterChannelRequest{
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
		opCtx, _, err := operatorOpContext(svc, env)
		if err != nil {
			return nil, err
		}
		resp, _ := svc.HandleRenameChannel(opCtx, &ChatRenameChannelRequest{
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
		opCtx, _, err := operatorOpContext(svc, env)
		if err != nil {
			return nil, err
		}
		resp, _ := svc.HandleSetTopic(opCtx, &ChatSetTopicRequest{
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
		opCtx, _, err := operatorOpContext(svc, env)
		if err != nil {
			return nil, err
		}
		resp, _ := svc.HandleSetSlowMode(opCtx, &ChatSetSlowModeRequest{
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
