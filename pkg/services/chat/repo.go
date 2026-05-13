package chat

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// MuteGlobalChannelID is the sentinel UUID used in the chat.mutes
// composite PK to represent a global (channel-wide) mute. Avoids
// Postgres NULL-in-PK semantics.
var MuteGlobalChannelID = uuid.MustParse("00000000-0000-0000-0000-000000000000")

// Errors returned by Repository implementations.
var (
	ErrChannelNotFound  = errors.New("chat: channel not found")
	ErrChannelSlugInUse = errors.New("chat: channel slug already exists")
	ErrMemberNotFound   = errors.New("chat: member not found")
	ErrMuteNotFound     = errors.New("chat: mute not found")
)

// Channel mirrors chat.channels.
type Channel struct {
	ChannelID       uuid.UUID
	Slug            string
	Kind            string // 'system_all' | 'system_predicate' | 'custom'
	Topic           string
	SlowModeSeconds int
	PasswordHash    string
	OwnerUserID     uuid.UUID // zero value for system channels
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ChannelMember mirrors chat.channel_members.
type ChannelMember struct {
	ChannelID    uuid.UUID
	UserID       uuid.UUID
	Role         string // "member" | "admin"
	JoinedAt     time.Time
	BannedUntil  time.Time // zero value = not banned
	BannedBy     uuid.UUID
	BannedReason string
}

// Mute mirrors chat.mutes. ChannelID == MuteGlobalChannelID denotes
// a global mute for that user across all channels.
type Mute struct {
	UserID    uuid.UUID
	ChannelID uuid.UUID // MuteGlobalChannelID for global
	ExpiresAt time.Time
	Reason    string
	MutedBy   uuid.UUID
	CreatedAt time.Time
}

// Repository abstracts persistence for the chat service. Postgres impl
// lives in pkg/services/chat/postgres; in-memory mock for tests is in
// pkg/services/chat/chattest.
type Repository interface {
	// Channels
	UpsertChannel(ctx context.Context, c Channel) (Channel, error) // INSERT ON CONFLICT (slug) UPDATE; returns the live row
	GetChannelByID(ctx context.Context, id uuid.UUID) (Channel, error)
	GetChannelBySlug(ctx context.Context, slug string) (Channel, error)
	ListAllChannels(ctx context.Context) ([]Channel, error)
	UpdateChannel(ctx context.Context, c Channel) error
	DeleteChannel(ctx context.Context, id uuid.UUID) error

	// Members
	AddOrUpdateMember(ctx context.Context, m ChannelMember) error
	RemoveMember(ctx context.Context, channelID, userID uuid.UUID) error
	BulkSetMembers(ctx context.Context, channelID uuid.UUID, userIDs []uuid.UUID, role string) error // wholesale replace
	ListMembers(ctx context.Context, channelID uuid.UUID) ([]ChannelMember, error)
	ListAllMembers(ctx context.Context) ([]ChannelMember, error) // bootstrap-time scan
	SetMemberRole(ctx context.Context, channelID, userID uuid.UUID, role string) error
	SetMemberBan(ctx context.Context, channelID, userID, bannedBy uuid.UUID, until time.Time, reason string) error
	ClearMemberBan(ctx context.Context, channelID, userID uuid.UUID) error

	// Mutes
	UpsertMute(ctx context.Context, m Mute) error
	DeleteMute(ctx context.Context, userID, channelID uuid.UUID) error
	ListActiveMutes(ctx context.Context) ([]Mute, error)

	// Reaper
	DeleteExpiredMutes(ctx context.Context) (int, error)
	ClearExpiredBans(ctx context.Context) (int, error)
}
