package mmokit

import (
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/zenion/mmoserver/pkg/services/auth"
	"github.com/zenion/mmoserver/pkg/services/chat"
	chatpg "github.com/zenion/mmoserver/pkg/services/chat/postgres"
	pkguniverse "github.com/zenion/mmoserver/pkg/universe"
)

// ChatOpts is mmokit's facade type for chat.ServiceOpts.
type ChatOpts = chat.ServiceOpts

// ChatRepository is mmokit's facade type for chat.Repository (used by tests
// injecting an in-memory mock).
type ChatRepository = chat.Repository

// DefaultChannelDef declares one channel that the chat service should
// UPSERT at startup.
type DefaultChannelDef = chat.DefaultChannelDef

// ChannelKind classifies a chat channel's membership semantics.
type ChannelKind = chat.ChannelKind

const (
	ChannelKindSystemAll       = chat.ChannelKindSystemAll
	ChannelKindSystemPredicate = chat.ChannelKindSystemPredicate
	ChannelKindCustom          = chat.ChannelKindCustom
)

// DefaultChatOpts returns sane defaults — see pkg/services/chat/kind.go
// for the full default set (5/5s rate, 500-byte messages, 5 user-owned
// channels, 1000 members per custom channel, etc).
func DefaultChatOpts() ChatOpts { return chat.DefaultServiceOpts() }

// errChatServiceNotReady is returned by every typed-op chat handler until
// Service.Init runs and OnReady fires. Should never reach a real client —
// chat ops are post-auth, and Init has long since completed by the time
// any client sends a chat op.
var errChatServiceNotReady = errors.New("chat service not initialized")

// RegisterChatService registers the engine-tier chat service kind on the
// process, installs the gateway session hook, registers all 23 typed-op
// handlers, and appends chat's Postgres migrations to Config.ExtraMigrations.
//
// Must be called BEFORE coord.Build(). The game must include "chat" in
// --services= when running with --mode=...,service for the kind to be
// instantiated on this process. RegisterChatService auto-includes "chat"
// in cfg.ServiceKinds when RoleService is in the role set, so the common
// case (single-process dev server) works without --services= flags.
//
// Auth dependency: chat resolves the live auth.Repository via the
// process's AuthResolver (stamped by mmokit.RegisterAuthService at
// runtime). RegisterAuthService must therefore be called BEFORE
// RegisterChatService for the AuthRepoProvider wiring to succeed —
// otherwise capability checks degrade to "no auth" (deny-all for
// admin ops).
//
// If opts.UserRateMax is zero, defaults are used (DefaultChatOpts()).
//
// If opts.Repository is nil and opts.RepositoryFactory is also nil,
// RegisterChatService injects chat/postgres.New as the factory. Tests
// that want to inject an in-memory mock should set opts.Repository
// directly (see RegisterChatServiceWithMock).
func RegisterChatService(p *pkguniverse.Process, opts ChatOpts) error {
	// Defaults: a zero UserRateMax means "use defaults". Carry over the
	// caller's Repository / RepositoryFactory / DefaultChannels / AuthRepoProvider
	// so injection points survive the merge.
	if opts.UserRateMax == 0 {
		base := DefaultChatOpts()
		base.Repository = opts.Repository
		base.RepositoryFactory = opts.RepositoryFactory
		base.DefaultChannels = opts.DefaultChannels
		base.AuthRepoProvider = opts.AuthRepoProvider
		opts = base
	}
	if opts.Repository == nil && opts.RepositoryFactory == nil {
		opts.RepositoryFactory = func(pool *pgxpool.Pool) chat.Repository {
			return chatpg.New(pool)
		}
	}

	// AuthRepoProvider: if not injected by the caller, resolve lazily via
	// the process's AuthResolver. *auth.Service implements both Resolver
	// and exposes Repository(); other Resolver implementations (custom
	// auth backends, tests) won't satisfy this and chat will fall back to
	// no-auth-aware capability checking. Lazy because auth's OnReady fires
	// at Init time, after RegisterAuthService returns — chat's Init runs
	// in the same lifecycle phase, so by the time the provider is called
	// the resolver is populated.
	if opts.AuthRepoProvider == nil {
		opts.AuthRepoProvider = func() auth.Repository {
			cfg := p.Config()
			if cfg == nil || cfg.AuthResolver == nil {
				return nil
			}
			if svc, ok := cfg.AuthResolver.(*auth.Service); ok {
				return svc.Repository()
			}
			return nil
		}
	}

	// The console-handler slot bridges the gap between facade-time
	// registration (pre-Build) and the live *chat.Service that only
	// exists after Service.Init runs. Typed-op handlers below resolve
	// it lazily through the same getSvc closure.
	setSvc, getSvc := chat.NewServiceSlot()
	prev := opts.OnReady
	opts.OnReady = func(svc *chat.Service) {
		setSvc(svc)
		// Wire the per-conn typed-event sender. Chat's fanout paths
		// resolve sendEventFn at send time, so installing the closure
		// here makes every subsequent fanout reach real clients.
		svc.SetSendEventFn(func(connID uint32, event any) {
			if event == nil || p.ConnMgr == nil {
				return
			}
			frame := buildTypedEventFrameAny(event)
			if frame == nil {
				return
			}
			p.ConnMgr.SendReliable(connID, frame)
		})
		if prev != nil {
			prev(svc)
		}
	}

	kind := chat.Kind(opts)
	if err := p.RegisterService(kind); err != nil {
		return fmt.Errorf("RegisterChatService: %w", err)
	}

	p.AppendExtraMigrations(chat.MigrationsFS())

	// Register every chat-emitted typed server event with the codec
	// before Build() runs, so any later send path can resolve typeIDs.
	RegisterChatServerEvents()

	// Install the gateway session hook so login/logout drives chat
	// presence + subscription state. Both methods dispatch into the
	// live service; before Init resolves they no-op.
	p.InstallChatHook(&chatSessionHookImpl{getSvc: getSvc})

	// Register all 23 typed-op chat handlers. All RouteGatewayLocal —
	// chat doesn't need cell-routed dispatch (no ECS access, no per-
	// player cell affinity). HandleSessionEnter / HandleSessionLeave
	// are intentionally NOT registered as wire-accessible ops: they
	// represent gateway-internal presence transitions and are driven
	// by the chat session hook above. Exposing them on the wire
	// would let clients forge presence.
	registerChatTypedOps(getSvc)

	// Auto-include "chat" in cfg.ServiceKinds when this process WILL
	// host services (RoleService is in the mode), so that operators
	// running with default flags don't have to also pass --services=chat.
	cfg := p.Config()
	roles, _ := pkguniverse.ParseRoles(cfg.Mode)
	if roles.Has(pkguniverse.RoleService) {
		already := false
		for _, k := range cfg.ServiceKinds {
			if k == chat.KindName {
				already = true
				break
			}
		}
		if !already {
			cfg.ServiceKinds = append(cfg.ServiceKinds, chat.KindName)
		}
	}

	if reg := p.CmdRegistry(); reg != nil {
		if err := chat.RegisterConsoleCommands(reg, getSvc, opts.AuthRepoProvider); err != nil {
			return fmt.Errorf("RegisterChatService: console: %w", err)
		}
	}

	return nil
}

// RegisterChatServiceWithMock is the test-fixture variant that wires
// chat against an in-memory ChatRepository (typically chattest.NewMock()).
// Skips Postgres entirely — the chat kind's RequiresDB flag is automatically
// false because opts.Repository is non-nil.
//
// The auth repository can optionally be injected via authRepo (typically
// authtest.NewMock()). When nil, the AuthResolver fallback applies — useful
// when this fixture pairs with RegisterAuthServiceWithMock so chat can
// resolve auth from the live *auth.Service after Init.
func RegisterChatServiceWithMock(p *pkguniverse.Process, repo ChatRepository, authRepo auth.Repository) error {
	opts := DefaultChatOpts()
	opts.Repository = repo
	if authRepo != nil {
		opts.AuthRepoProvider = func() auth.Repository { return authRepo }
	}
	return RegisterChatService(p, opts)
}

// chatSessionHookImpl satisfies pkguniverse.ChatSessionHook and dispatches
// every login / logout event to the live *chat.Service. Before Init has
// resolved (live service nil), both callbacks no-op so out-of-order
// startup doesn't panic.
type chatSessionHookImpl struct {
	getSvc chat.ServiceProvider
}

func (h *chatSessionHookImpl) OnSessionEnter(connID uint32, userID, username, gatewayID string) {
	svc := h.getSvc()
	if svc == nil {
		return
	}
	_, _ = svc.HandleSessionEnter(nil, &chat.ChatSessionEnterRequest{
		ConnID:    connID,
		UserID:    userID,
		Username:  username,
		GatewayID: gatewayID,
	})
}

func (h *chatSessionHookImpl) OnSessionLeave(connID uint32, gatewayID string) {
	svc := h.getSvc()
	if svc == nil {
		return
	}
	_, _ = svc.HandleSessionLeave(nil, &chat.ChatSessionLeaveRequest{
		ConnID:    connID,
		GatewayID: gatewayID,
	})
}

// registerChatTypedOps wires every chat Handle* method into the typed-op
// registry. All entries are RouteGatewayLocal — chat handlers run inline
// on the gateway with no cell forwarding. The closures resolve the live
// service via getSvc so registration can happen before Service.Init.
//
// HandleSessionEnter / HandleSessionLeave are intentionally NOT registered
// here — those are driven by the gateway hook (see InstallChatHook) and
// must not be exposed on the wire (clients forging presence would be a
// trust hole).
func registerChatTypedOps(getSvc chat.ServiceProvider) {
	// --- Player ops ---
	RegisterOp[chat.ChatSendRequest, chat.ChatSendResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *chat.ChatSendRequest) (*chat.ChatSendResponse, error) {
			svc := getSvc()
			if svc == nil {
				return nil, errChatServiceNotReady
			}
			return svc.HandleSend(opCtx, req)
		})
	RegisterOp[chat.ChatSendDMRequest, chat.ChatSendDMResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *chat.ChatSendDMRequest) (*chat.ChatSendDMResponse, error) {
			svc := getSvc()
			if svc == nil {
				return nil, errChatServiceNotReady
			}
			return svc.HandleSendDM(opCtx, req)
		})
	RegisterOp[chat.ChatJoinRequest, chat.ChatJoinResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *chat.ChatJoinRequest) (*chat.ChatJoinResponse, error) {
			svc := getSvc()
			if svc == nil {
				return nil, errChatServiceNotReady
			}
			return svc.HandleJoin(opCtx, req)
		})
	RegisterOp[chat.ChatLeaveRequest, chat.ChatLeaveResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *chat.ChatLeaveRequest) (*chat.ChatLeaveResponse, error) {
			svc := getSvc()
			if svc == nil {
				return nil, errChatServiceNotReady
			}
			return svc.HandleLeave(opCtx, req)
		})
	RegisterOp[chat.ChatCreateRequest, chat.ChatCreateResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *chat.ChatCreateRequest) (*chat.ChatCreateResponse, error) {
			svc := getSvc()
			if svc == nil {
				return nil, errChatServiceNotReady
			}
			return svc.HandleCreate(opCtx, req)
		})
	RegisterOp[chat.ChatListChannelsRequest, chat.ChatListChannelsResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *chat.ChatListChannelsRequest) (*chat.ChatListChannelsResponse, error) {
			svc := getSvc()
			if svc == nil {
				return nil, errChatServiceNotReady
			}
			return svc.HandleListChannels(opCtx, req)
		})
	RegisterOp[chat.ChatListMembersRequest, chat.ChatListMembersResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *chat.ChatListMembersRequest) (*chat.ChatListMembersResponse, error) {
			svc := getSvc()
			if svc == nil {
				return nil, errChatServiceNotReady
			}
			return svc.HandleListMembers(opCtx, req)
		})
	RegisterOp[chat.ChatRenameChannelRequest, chat.ChatRenameChannelResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *chat.ChatRenameChannelRequest) (*chat.ChatRenameChannelResponse, error) {
			svc := getSvc()
			if svc == nil {
				return nil, errChatServiceNotReady
			}
			return svc.HandleRenameChannel(opCtx, req)
		})
	RegisterOp[chat.ChatSetTopicRequest, chat.ChatSetTopicResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *chat.ChatSetTopicRequest) (*chat.ChatSetTopicResponse, error) {
			svc := getSvc()
			if svc == nil {
				return nil, errChatServiceNotReady
			}
			return svc.HandleSetTopic(opCtx, req)
		})
	RegisterOp[chat.ChatSetSlowModeRequest, chat.ChatSetSlowModeResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *chat.ChatSetSlowModeRequest) (*chat.ChatSetSlowModeResponse, error) {
			svc := getSvc()
			if svc == nil {
				return nil, errChatServiceNotReady
			}
			return svc.HandleSetSlowMode(opCtx, req)
		})

	// --- Membership-mutation ops ---
	RegisterOp[chat.ChatAddMemberRequest, chat.ChatAddMemberResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *chat.ChatAddMemberRequest) (*chat.ChatAddMemberResponse, error) {
			svc := getSvc()
			if svc == nil {
				return nil, errChatServiceNotReady
			}
			return svc.HandleAddMember(opCtx, req)
		})
	RegisterOp[chat.ChatRemoveMemberRequest, chat.ChatRemoveMemberResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *chat.ChatRemoveMemberRequest) (*chat.ChatRemoveMemberResponse, error) {
			svc := getSvc()
			if svc == nil {
				return nil, errChatServiceNotReady
			}
			return svc.HandleRemoveMember(opCtx, req)
		})
	RegisterOp[chat.ChatBulkSetMembersRequest, chat.ChatBulkSetMembersResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *chat.ChatBulkSetMembersRequest) (*chat.ChatBulkSetMembersResponse, error) {
			svc := getSvc()
			if svc == nil {
				return nil, errChatServiceNotReady
			}
			return svc.HandleBulkSetMembers(opCtx, req)
		})
	RegisterOp[chat.ChatRegisterChannelRequest, chat.ChatRegisterChannelResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *chat.ChatRegisterChannelRequest) (*chat.ChatRegisterChannelResponse, error) {
			svc := getSvc()
			if svc == nil {
				return nil, errChatServiceNotReady
			}
			return svc.HandleRegisterChannel(opCtx, req)
		})
	RegisterOp[chat.ChatUnregisterChannelRequest, chat.ChatUnregisterChannelResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *chat.ChatUnregisterChannelRequest) (*chat.ChatUnregisterChannelResponse, error) {
			svc := getSvc()
			if svc == nil {
				return nil, errChatServiceNotReady
			}
			return svc.HandleUnregisterChannel(opCtx, req)
		})
	RegisterOp[chat.ChatSetMemberRoleRequest, chat.ChatSetMemberRoleResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *chat.ChatSetMemberRoleRequest) (*chat.ChatSetMemberRoleResponse, error) {
			svc := getSvc()
			if svc == nil {
				return nil, errChatServiceNotReady
			}
			return svc.HandleSetMemberRole(opCtx, req)
		})

	// --- Moderation ops ---
	RegisterOp[chat.ChatDeleteMessageRequest, chat.ChatDeleteMessageResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *chat.ChatDeleteMessageRequest) (*chat.ChatDeleteMessageResponse, error) {
			svc := getSvc()
			if svc == nil {
				return nil, errChatServiceNotReady
			}
			return svc.HandleDeleteMessage(opCtx, req)
		})
	RegisterOp[chat.ChatMuteUserRequest, chat.ChatMuteUserResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *chat.ChatMuteUserRequest) (*chat.ChatMuteUserResponse, error) {
			svc := getSvc()
			if svc == nil {
				return nil, errChatServiceNotReady
			}
			return svc.HandleMuteUser(opCtx, req)
		})
	RegisterOp[chat.ChatUnmuteUserRequest, chat.ChatUnmuteUserResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *chat.ChatUnmuteUserRequest) (*chat.ChatUnmuteUserResponse, error) {
			svc := getSvc()
			if svc == nil {
				return nil, errChatServiceNotReady
			}
			return svc.HandleUnmuteUser(opCtx, req)
		})
	RegisterOp[chat.ChatKickRequest, chat.ChatKickResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *chat.ChatKickRequest) (*chat.ChatKickResponse, error) {
			svc := getSvc()
			if svc == nil {
				return nil, errChatServiceNotReady
			}
			return svc.HandleKickFromChannel(opCtx, req)
		})
	RegisterOp[chat.ChatBanRequest, chat.ChatBanResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *chat.ChatBanRequest) (*chat.ChatBanResponse, error) {
			svc := getSvc()
			if svc == nil {
				return nil, errChatServiceNotReady
			}
			return svc.HandleBanFromChannel(opCtx, req)
		})
	RegisterOp[chat.ChatUnbanRequest, chat.ChatUnbanResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *chat.ChatUnbanRequest) (*chat.ChatUnbanResponse, error) {
			svc := getSvc()
			if svc == nil {
				return nil, errChatServiceNotReady
			}
			return svc.HandleUnbanFromChannel(opCtx, req)
		})
	RegisterOp[chat.ChatBroadcastRequest, chat.ChatBroadcastResponse](RouteGatewayLocal,
		func(opCtx *OpContext, req *chat.ChatBroadcastRequest) (*chat.ChatBroadcastResponse, error) {
			svc := getSvc()
			if svc == nil {
				return nil, errChatServiceNotReady
			}
			return svc.HandleBroadcastSystem(opCtx, req)
		})
}

var registerChatServerEventsOnce sync.Once

// RegisterChatServerEvents registers every chat-emitted typed server
// event with the mmokit codec so the wire layer can encode/decode by
// typeID and the SDK generator can emit per-event TS handlers.
//
// Idempotent. Called by RegisterChatService at facade time. Tests in
// pkg/services/chat/_test that need the registry populated without
// going through RegisterChatService can call this directly.
//
// Lives in the mmokit facade rather than pkg/services/chat so the
// chat package doesn't have to import mmokit (which would form an
// import cycle once mmokit imports chat).
func RegisterChatServerEvents() {
	registerChatServerEventsOnce.Do(func() {
		RegisterEvent[chat.ChatMessageEvent]()
		RegisterEvent[chat.ChatDMEvent]()
		RegisterEvent[chat.ChatMemberJoinedEvent]()
		RegisterEvent[chat.ChatMemberLeftEvent]()
		RegisterEvent[chat.ChatMessageDeletedEvent]()
		RegisterEvent[chat.ChatMutedEvent]()
		RegisterEvent[chat.ChatUnmutedEvent]()
		RegisterEvent[chat.ChatKickedEvent]()
		RegisterEvent[chat.ChatBannedEvent]()
		RegisterEvent[chat.ChatChannelUpdatedEvent]()
		RegisterEvent[chat.ChatChannelGoneEvent]()
		RegisterEvent[chat.ChatMemberRoleChangedEvent]()
		RegisterEvent[chat.ChatRateLimitedEvent]()
		RegisterEvent[chat.ChatChannelsHydratedEvent]()
	})
}

// buildTypedEventFrameAny is the non-generic counterpart to
// BuildTypedEventFrame[T]. Used by the chat service's per-conn sender
// (which sees events as `any`, not a typed parameter). Returns nil if
// the type was never registered via RegisterEvent[T] — chat's fanout
// silently drops in that case rather than panicking on a mis-wired
// service.
func buildTypedEventFrameAny(event any) []byte {
	if event == nil {
		return nil
	}
	t := reflect.TypeOf(event)
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if _, ok := LookupServerEventType(TypeIDOf(t)); !ok {
		return nil
	}
	id := TypeIDOf(t)
	body := pkguniverse.ReflectMarshal(event)
	return pkguniverse.EncodeTypedEventFrame(id, body)
}
