// Package chat — typed_messages.go defines the typed Go structs that
// carry chat traffic on the typed-op (channel 0x01) and typed-event
// (channel 0x00) wire formats. No protobuf. Wire-stable typeIDs are
// derived from each struct's package-qualified name.
//
// Renames are wire-breaking; field additions are backward-compat;
// field removals are wire-breaking. Same conventions as pkg/services/auth.
package chat

// ChannelKind classifies a channel's membership semantics.
type ChannelKind uint32

const (
	ChannelKindUnspecified     ChannelKind = 0
	ChannelKindSystemAll       ChannelKind = 1 // implicit membership: every online user
	ChannelKindSystemPredicate ChannelKind = 2 // explicit members; pushed by services (guild/party/alliance)
	ChannelKindCustom          ChannelKind = 3 // explicit members; user-created
)

// ChatError is the stable error vocabulary for chat ops. Wire values
// must not change once shipped.
type ChatError uint32

const (
	ChatErrorUnspecified        ChatError = 0
	ChatErrorChannelNotFound    ChatError = 1
	ChatErrorNotAMember         ChatError = 2
	ChatErrorMuted              ChatError = 3
	ChatErrorBanned             ChatError = 4
	ChatErrorRateLimited        ChatError = 5
	ChatErrorSlowMode           ChatError = 6
	ChatErrorPayloadTooLarge    ChatError = 7
	ChatErrorInvalidPassword    ChatError = 8
	ChatErrorMessageUnknown     ChatError = 9
	ChatErrorPermissionDenied   ChatError = 10
	ChatErrorReservedSlug       ChatError = 11
	ChatErrorSlugInUse          ChatError = 12
	ChatErrorRecipientOffline   ChatError = 13
	ChatErrorMaxChannelsReached ChatError = 14
	ChatErrorMaxMembersReached  ChatError = 15
	ChatErrorInternal           ChatError = 16
)

// ErrorBlock is the standard error fields embedded on every Response
// struct. ErrorCode == 0 ⇒ success; non-zero ⇒ ChatError value.
//
// Exported so tests and internal callers can construct error responses
// directly. Field names use Go's CamelCase; they map to lowerCamelCase
// on the JS SDK side automatically.
type ErrorBlock struct {
	ErrorCode    uint32
	ErrorMessage string
	RetryAfterMs int64
}

// ChannelInfo is the public summary of a channel. Returned by Join,
// Create, RegisterChannel, ListChannels, and embedded in
// ChatChannelUpdatedEvent.
type ChannelInfo struct {
	ChannelID       string
	Slug            string
	Kind            ChannelKind
	Topic           string
	SlowModeSeconds int32
	OwnerUserID     string // empty for system channels
	MemberCount     int32
	HasPassword     bool
}

type MemberInfo struct {
	UserID     string
	Username   string // denormalized snapshot
	Role       string // "member" | "admin"
	JoinedAtMs int64
}

// --- Player ops ---

type ChatSendRequest struct{ ChannelID, Body string }
type ChatSendResponse struct {
	MsgID    string
	SentAtMs int64
	ErrorBlock
}

type ChatSendDMRequest struct{ RecipientUserID, Body string }
type ChatSendDMResponse struct {
	MsgID    string
	SentAtMs int64
	ErrorBlock
}

type ChatJoinRequest struct{ Slug, Password string }
type ChatJoinResponse struct {
	Channel ChannelInfo
	ErrorBlock
}

type ChatLeaveRequest struct{ ChannelID string }
type ChatLeaveResponse struct{ ErrorBlock }

type ChatCreateRequest struct{ Slug, Password, Topic string }
type ChatCreateResponse struct {
	Channel ChannelInfo
	ErrorBlock
}

type ChatListChannelsRequest struct{}
type ChatListChannelsResponse struct {
	Channels []ChannelInfo
	ErrorBlock
}

type ChatListMembersRequest struct{ ChannelID string }
type ChatListMembersResponse struct {
	Members []MemberInfo
	ErrorBlock
}

type ChatRenameChannelRequest struct{ ChannelID, NewSlug string }
type ChatRenameChannelResponse struct {
	Channel ChannelInfo
	ErrorBlock
}

type ChatSetTopicRequest struct{ ChannelID, Topic string }
type ChatSetTopicResponse struct{ ErrorBlock }

type ChatSetSlowModeRequest struct {
	ChannelID string
	Seconds   int32
}
type ChatSetSlowModeResponse struct{ ErrorBlock }

// --- Membership-mutation ops (capability-gated) ---

type ChatAddMemberRequest struct{ ChannelID, UserID, Role string }
type ChatAddMemberResponse struct{ ErrorBlock }

type ChatRemoveMemberRequest struct{ ChannelID, UserID string }
type ChatRemoveMemberResponse struct{ ErrorBlock }

type ChatBulkSetMembersRequest struct {
	ChannelID string
	UserIDs   []string
}
type ChatBulkSetMembersResponse struct{ ErrorBlock }

type ChatRegisterChannelRequest struct {
	Slug            string
	Kind            ChannelKind
	Topic           string
	SlowModeSeconds int32
	Password        string
}
type ChatRegisterChannelResponse struct {
	Channel ChannelInfo
	ErrorBlock
}

type ChatUnregisterChannelRequest struct{ ChannelID string }
type ChatUnregisterChannelResponse struct{ ErrorBlock }

type ChatSetMemberRoleRequest struct{ ChannelID, UserID, Role string }
type ChatSetMemberRoleResponse struct{ ErrorBlock }

// --- Moderation ops (capability-gated) ---

type ChatDeleteMessageRequest struct{ MsgID, ChannelID string }
type ChatDeleteMessageResponse struct{ ErrorBlock }

type ChatMuteUserRequest struct {
	UserID     string
	ChannelID  string // empty = global mute (chat.admin only)
	DurationMs int64
	Reason     string
}
type ChatMuteUserResponse struct{ ErrorBlock }

type ChatUnmuteUserRequest struct{ UserID, ChannelID string }
type ChatUnmuteUserResponse struct{ ErrorBlock }

type ChatKickRequest struct{ ChannelID, UserID, Reason string }
type ChatKickResponse struct{ ErrorBlock }

type ChatBanRequest struct {
	ChannelID  string
	UserID     string
	DurationMs int64
	Reason     string
}
type ChatBanResponse struct{ ErrorBlock }

type ChatUnbanRequest struct{ ChannelID, UserID string }
type ChatUnbanResponse struct{ ErrorBlock }

type ChatBroadcastRequest struct{ ChannelID, Body string }
type ChatBroadcastResponse struct{ ErrorBlock }

// --- Service-internal ops (gateway → chat) ---
//
// These are dispatched by the gateway after successful auth to drive
// presence + subscription bookkeeping in chat. NOT intended for client
// use; the gateway capability-gates them via an in-process bypass.

type ChatSessionEnterRequest struct {
	ConnID    uint32
	UserID    string
	Username  string
	GatewayID string
}
type ChatSessionEnterResponse struct{ ErrorBlock }

type ChatSessionLeaveRequest struct {
	ConnID    uint32
	GatewayID string
}
type ChatSessionLeaveResponse struct{ ErrorBlock }

// --- Chat → client server events (typed-event channel) ---

type ChatMessageEvent struct {
	MsgID          string
	ChannelID      string
	SenderUserID   string // empty = system broadcast
	SenderUsername string
	Body           string
	SentAtMs       int64
}

type ChatDMEvent struct {
	MsgID          string
	SenderUserID   string
	SenderUsername string
	Body           string
	SentAtMs       int64
}

type ChatMemberJoinedEvent struct {
	ChannelID string
	UserID    string
	Username  string
}

type ChatMemberLeftEvent struct {
	ChannelID string
	UserID    string
}

type ChatMessageDeletedEvent struct {
	MsgID           string
	ChannelID       string
	DeletedByUserID string
}

type ChatMutedEvent struct {
	ChannelID string // empty = global mute
	UntilMs   int64
	Reason    string
}

type ChatUnmutedEvent struct {
	ChannelID string // empty = global unmute
}

type ChatKickedEvent struct {
	ChannelID string
	ByUserID  string
	Reason    string
}

type ChatBannedEvent struct {
	ChannelID string
	ByUserID  string
	UntilMs   int64
	Reason    string
}

type ChatChannelUpdatedEvent struct {
	Channel ChannelInfo
}

type ChatChannelGoneEvent struct {
	ChannelID string
}

type ChatMemberRoleChangedEvent struct {
	ChannelID string
	UserID    string
	Role      string // "member" | "admin"
}

type ChatRateLimitedEvent struct {
	RetryAfterMs int64
}

type ChatChannelsHydratedEvent struct {
	Channels []ChannelInfo
}
