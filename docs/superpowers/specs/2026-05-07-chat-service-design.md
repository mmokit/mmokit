# Chat Service — Design

**Status:** Implemented with Phase 1 services event-bus migration
**Author:** Josh Stout (with Claude)
**Date:** 2026-05-07
**Updated:** 2026-05-08 (services event-bus Phase 1)
**Related memories:** `feedback_no_backward_compat`, `feedback_refactor_over_stopgaps`, `feedback_mmokit_facade_only`, `feedback_logging`, `project_opensource_ready`
**Related specs:** [2026-04-27-pluggable-services-design.md](2026-04-27-pluggable-services-design.md), [2026-05-01-auth-service-design.md](2026-05-01-auth-service-design.md)

**Note (2026-05-08):** This document describes the current chat-service implementation using event-based architecture. The **`MeshFrame.ChatSessionEnter`/`ChatSessionLeave` messages** (§8.1) represent the event-driven session-lifecycle signaling that coordinates chat presence with the gateway. This aligns with the generic services event-bus design in [2026-05-08-services-event-bus-design.md](2026-05-08-services-event-bus-design.md).

## 1. Summary

mmokit's in-engine chat plumbing was decommissioned in [9b32bc3](commit:9b32bc3) (`feat(game): decommission server-side chat`). This design fills the gap with a first-class engine-tier **chat service** (`pkg/services/chat/`) running on the pluggable-services framework, peer to the auth service.

The service is **single-instance** in v1 — RAM-authoritative for transient state (subscriptions, rate buckets, online presence) and Postgres-persistent for durable state (channel definitions, memberships, mutes). Messages and DMs are pure pass-through: receive → fanout → forget. There is no message ring buffer, no recent-history-on-join, no message persistence in v1; those are explicitly deferred to v2 alongside the framework's deferred sync-stream work.

Wired into a game with one line: `mmokit.RegisterChatService(coord, opts)`. System channels are declared as data on `ChatOpts.DefaultChannels` and the chat service UPSERTs them at startup; the game has zero runtime awareness of chat. Everything else flows from the player client (`/join`, `/dm`, `/msg`) or admin console (moderation).

## 2. Goals & non-goals

### Goals

- **First-class chat in mmokit.** Every game gets MMO-grade chat (system + guild + party + alliance + custom + DM) for free.
- **Pure-pubsub in v1.** No message persistence, no ring buffers, no history-on-join — chat ships as a clean append-and-forget pipe.
- **Self-contained social graph.** Channel definitions, memberships, and mutes survive chat-service restart. Messages don't.
- **Open membership API.** Game systems, admin console, future services (guild, party, alliance), and future REST endpoints all push memberships through the same op-code surface, gated by capability.
- **Two-role channel hierarchy.** Per-channel `member` / `admin`; global `chat.admin` capability for GMs. Channel admins can promote/demote other admins on their channels.
- **GM tooling integrated with cmdsys.** Mute, kick, ban, broadcast, delete — same shape as `auth user *` commands, accessible from console + future dashboard.
- **No backward compat tax.** Old `enginepb.ChatMsg` and the in-engine plumbing already deleted. Greenfield service.

### Non-goals (v1)

- Message persistence, paged history, recent-history-on-join (no in-memory rings — pure pass-through)
- Typing indicators, read receipts, reactions, attachments, threading, message edit
- Block lists / per-user "mute this person client-side" (defer to v2)
- Cross-instance horizontal scale-out (single-instance constraint; v2 = framework sync-stream OR Redis pub/sub adapter)
- Rich text / markdown / mention parsing — chat ships raw text; client renders
- Spam pattern detection (repeat-message, cross-channel flood, mention-flood) — v1 ships rate buckets only
- URL filter / regex blocklists / profanity filters
- Voice / video — entirely separate service
- Email / push notifications for offline DMs — DMs to offline users drop silently in v1
- Cross-cluster federation
- Multi-device "active simultaneously" — auth already enforces one game session per user_id
- Player-record migration of old chat data (none exists; old plumbing was decommissioned)

## 3. Architecture

```text
┌──────────┐  op  ┌─────────────┐ mesh ┌────────────────┐
│  client  │ ───▶ │   gateway   │ ───▶ │   chat svc    │
│          │ ◀─── │             │ ◀─── │  - channels    │ ───▶ postgres
└──────────┘ ev   │  - auth     │  ev  │  - members     │      (chat_channels,
                  │    state    │      │  - mutes       │       chat_channel_members,
                  │  - chat     │      │  - subs (RAM)  │       chat_mutes)
                  │    session  │      │  - rates (RAM) │
                  │    enter/   │      │  - online (RAM)│
                  │    leave    │      └────────────────┘
                  └──────┬──────┘             ▲
                         │                    │
                    PeerList            ServiceAnnounce
                         │                    │
                  ┌──────┴────────────┐       │
                  │   coordinator     │───────┘
                  │ + ServiceRegistry │
                  └───────────────────┘
```

The game has **zero** awareness of chat. System channels are declared as data on `ChatOpts.DefaultChannels` and the chat service idempotently UPSERTs them at startup — see §10 for the field, §12 for wiring.

**New pieces:**

1. **`pkg/services/chat/`** — engine-tier service kind: descriptor, typed wire messages, handlers, fanout, membership index, mute set, rate limiter, repository interface, Postgres implementation, console builtins.
2. **`MeshFrame.ChatSessionEnter` / `ChatSessionLeave`** — internal mesh extensions wired into the gateway's existing auth login/logout flow. Tells chat which connID belongs to which user, drives subscription bookkeeping. Never seen by clients.
3. **`mmokit.RegisterChatService(p, opts)`** — single-call wiring entrypoint that registers the kind, the gateway hooks, and the console commands.

**No protobuf for client wire.** Per the typed-event/typed-op migration that landed on this branch (commits `dfac701`, `2fa8c3f`, `8ea020e`, etc.), engine-tier services no longer use protobuf for client wire messages. Chat ops route through `mmokit.RegisterOp[Req, Res]` keyed by typeID (derived from Go's package-qualified type name); chat server events route through `mmokit.RegisterServerEvent[T]` on the same reflection codec. `Kind.OpCodes` is `nil` (no opcode claims) — same shape as auth post-migration. The TS SDK is regenerated automatically via `cmd/sdkgen` reading the runtime registries; clients get typed access without any `.proto` file or `buf generate` step.

**Server-internal mesh frames (`proto/meshpb/mesh.proto`) are still protobuf** — that's the inter-host wire format (host↔host, gateway↔host), and was not touched by the client-wire migration. The new `MeshFrame.ChatSessionEnter` / `ChatSessionLeave` variants land as additional `oneof` cases in `mesh.proto`, generated through the existing `buf generate` flow. The chat-service Go code itself never imports `gen/go/meshpb/` — it receives the decoded payload via a typed Go handler. Universe / gateway code on the cluster boundary handles the encode/decode.

**Module boundaries:**

- `pkg/services/chat/` may import: `pkg/service/`, `pkg/services/auth/` (for capability check), `pkg/persist/postgres/`, `pkg/logger/`, `pkg/metrics/`, `github.com/google/uuid`.
- Must NOT import: any `gen/go/*pb/`, `internal/`, `pkg/universe/` (avoids circular dependency).
- Game code reaches chat only through the `mmokit` facade (per `feedback_mmokit_facade_only`).

The `chat` kind name is engine-reserved.

### Identity discipline

**Username is display-only, never an identity key.**

- All persisted FK-style references use `user_id UUID`. Chat never stores a username column.
- Wire requests carry `user_id`: `recipient_user_id` on DMs, `user_id` on `ADD_MEMBER` / `MUTE_USER` / `KICK_FROM_CHANNEL` / etc.
- Wire events carry both `user_id` (canonical, for UI lookup tables and future correlation) AND `username` (denormalized snapshot at send-time, for immediate display without a username-resolution round-trip).
- Console commands accept `<username>` for operator UX; the handler does a single auth-side `UserIDByUsername` lookup and operates on the resolved user_id thereafter.
- When auth grows username-rename, no chat-side schema or membership work is needed. Live messages render with the new name on next send. Old `SE_CHAT_MESSAGE` events sitting in client UIs keep showing the snapshot — that is correct (it is what they actually said as that name).

The only chat state that is slug-keyed (string-keyed) is **channel slugs** (`world`, `guild:42`, `secretclub`). Slugs are user-facing channel names; primary key on `chat_channels` is still `channel_id UUID`. Slug changes (`RENAME_CHANNEL`) are a single `UPDATE chat_channels SET slug = $1 WHERE channel_id = $2` with the UNIQUE-constraint check.

## 4. Package layout

```text
pkg/services/chat/
  kind.go              // service.Kind + ServiceOpts (incl. DefaultChannels)
  service.go           // *Service: implements service.Service
  typed_messages.go    // ALL wire structs: requests, responses, server events, ChatError consts
  handlers.go          // op handlers — registered via mmokit.RegisterOp[Req, Res]
  events.go            // server-event registration via mmokit.RegisterServerEvent[T]
  authorization.go     // canModerate + capability checks
  ratelimit.go         // per-user token bucket + slow-mode tracker
  fanout.go            // channel-id → []connID dispatch
  membership.go        // in-memory membership index (mirror of DB)
  mute.go              // mute set + reaper
  console.go           // cmdsys console builtins
  repo.go              // ChatRepository interface
  msgindex.go          // msg_id → channel_id 5-min TTL cache
  doc.go
  postgres/
    repo.go            // Postgres impl of ChatRepository
    migrations/
      001_init.sql
  chattest/
    mock.go            // in-memory ChatRepository for tests

pkg/mmokit/
  chat.go              // facade: RegisterChatService + ChatOpts re-exports
```

No `proto/`, no `gen/`, no `buf generate` step. The TS SDK regenerates from runtime registries via `cmd/sdkgen` (already wired for auth + echo).

### Naming convention for the new directory split

- `pkg/service/` (singular) — the services *framework*: `Kind`, `Service`, `Context`, registry, router. Existing.
- `pkg/services/<name>/` (plural) — engine-tier service *implementations* shipped with mmokit. New convention.
- `examples/<game>/services/<name>/` — game-specific service implementations (already exists for echo).

This requires a one-time mechanical move of `pkg/auth/` → `pkg/services/auth/`. See §16.

## 5. Wire protocol — typed Go structs (`pkg/services/chat/typed_messages.go`)

No protobuf. All chat wire messages are pure Go structs in `pkg/services/chat/typed_messages.go`. Wire-stable typeIDs are derived from each struct's package-qualified name (e.g., `chat.ChatSendRequest`); on the wire each frame carries `{typeID, body}` where the body is encoded by the engine's reflection codec (same one used by auth and the typed-event channel).

**Wire-stability rules** (same as auth):

- Struct rename → wire-breaking (typeID changes).
- Field rename → wire-breaking (encoding key derived from field name).
- Field add → backward-compatible (older peers ignore unknown fields).
- Field remove → wire-breaking.

No opcode integers. Routing is by typeID — `mmokit.RegisterOp[Req, Res]` claims a `(Req-typeID, Res-typeID)` pair on the typed-op router; `mmokit.RegisterServerEvent[T]` claims a typeID on the typed-event channel. `Kind.OpCodes` is `nil`.

### Channel kind constants

```go
type ChannelKind uint32

const (
    ChannelKindUnspecified     ChannelKind = 0
    ChannelKindSystemAll       ChannelKind = 1  // implicit membership: every online user
    ChannelKindSystemPredicate ChannelKind = 2  // explicit members; pushed by services (guild/party/alliance)
    ChannelKindCustom          ChannelKind = 3  // explicit members; user-created
)
```

### Error vocabulary

Each handler embeds error fields directly on the response struct (mirroring auth's pattern — no separate envelope). Empty / zero on success; populated on failure.

```go
type ChatError uint32

const (
    ChatErrorUnspecified      ChatError = 0
    ChatErrorChannelNotFound  ChatError = 1
    ChatErrorNotAMember       ChatError = 2
    ChatErrorMuted            ChatError = 3
    ChatErrorBanned           ChatError = 4
    ChatErrorRateLimited      ChatError = 5
    ChatErrorSlowMode         ChatError = 6
    ChatErrorPayloadTooLarge  ChatError = 7
    ChatErrorInvalidPassword  ChatError = 8
    ChatErrorMessageUnknown   ChatError = 9
    ChatErrorPermissionDenied ChatError = 10
    ChatErrorReservedSlug     ChatError = 11
    ChatErrorSlugInUse        ChatError = 12
    ChatErrorRecipientOffline ChatError = 13  // SendDM only; informational, send still succeeds (drops)
    ChatErrorMaxChannelsReached ChatError = 14
    ChatErrorMaxMembersReached  ChatError = 15
    ChatErrorInternal         ChatError = 16
)

// errorBlock is the standard set of error fields embedded in every Response
// struct. Kept in one place so rename / addition is a single edit.
type errorBlock struct {
    ErrorCode    uint32  // 0 = success; non-zero = ChatError value
    ErrorMessage string  // human-readable diagnostic; never the source of truth
    RetryAfterMs int64   // populated for RateLimited / SlowMode / Banned / Muted
}
```

### Common shared structs

```go
type ChannelInfo struct {
    ChannelID       string
    Slug            string
    Kind            ChannelKind
    Topic           string
    SlowModeSeconds int32
    OwnerUserID     string  // empty for system channels
    MemberCount     int32
    HasPassword     bool
}

type MemberInfo struct {
    UserID     string
    Username   string  // denormalized snapshot
    Role       string  // "member" | "admin"
    JoinedAtMs int64
}
```

### Client → chat ops (typed-op channel; `mmokit.RegisterOp[Req, Res]`)

```go
// Player ops
type ChatSendRequest  struct { ChannelID, Body string }
type ChatSendResponse struct {
    MsgID    string
    SentAtMs int64
    errorBlock
}

type ChatSendDMRequest  struct { RecipientUserID, Body string }
type ChatSendDMResponse struct {
    MsgID    string
    SentAtMs int64
    errorBlock
}

type ChatJoinRequest  struct { Slug, Password string }
type ChatJoinResponse struct {
    Channel ChannelInfo
    errorBlock
}

type ChatLeaveRequest  struct { ChannelID string }
type ChatLeaveResponse struct{ errorBlock }

type ChatCreateRequest  struct { Slug, Password, Topic string }
type ChatCreateResponse struct {
    Channel ChannelInfo
    errorBlock
}

type ChatListChannelsRequest  struct{}
type ChatListChannelsResponse struct {
    Channels []ChannelInfo
    errorBlock
}

type ChatListMembersRequest  struct { ChannelID string }
type ChatListMembersResponse struct {
    Members []MemberInfo
    errorBlock
}

type ChatRenameChannelRequest  struct { ChannelID, NewSlug string }
type ChatRenameChannelResponse struct {
    Channel ChannelInfo
    errorBlock
}

type ChatSetTopicRequest    struct { ChannelID, Topic string }
type ChatSetTopicResponse   struct{ errorBlock }

type ChatSetSlowModeRequest  struct {
    ChannelID string
    Seconds   int32
}
type ChatSetSlowModeResponse struct{ errorBlock }

// Membership-mutation ops (capability-gated)
type ChatAddMemberRequest    struct { ChannelID, UserID, Role string }
type ChatAddMemberResponse   struct{ errorBlock }

type ChatRemoveMemberRequest  struct { ChannelID, UserID string }
type ChatRemoveMemberResponse struct{ errorBlock }

type ChatBulkSetMembersRequest  struct {
    ChannelID string
    UserIDs   []string
}
type ChatBulkSetMembersResponse struct{ errorBlock }

type ChatRegisterChannelRequest struct {
    Slug            string
    Kind            ChannelKind
    Topic           string
    SlowModeSeconds int32
    Password        string
}
type ChatRegisterChannelResponse struct {
    Channel ChannelInfo
    errorBlock
}

type ChatUnregisterChannelRequest  struct { ChannelID string }
type ChatUnregisterChannelResponse struct{ errorBlock }

type ChatSetMemberRoleRequest    struct { ChannelID, UserID, Role string }
type ChatSetMemberRoleResponse   struct{ errorBlock }

// Moderation ops (capability-gated)
type ChatDeleteMessageRequest  struct { MsgID, ChannelID string }
type ChatDeleteMessageResponse struct{ errorBlock }

type ChatMuteUserRequest struct {
    UserID     string
    ChannelID  string  // empty = global mute (chat.admin only)
    DurationMs int64
    Reason     string
}
type ChatMuteUserResponse struct{ errorBlock }

type ChatUnmuteUserRequest    struct { UserID, ChannelID string }
type ChatUnmuteUserResponse   struct{ errorBlock }

type ChatKickRequest    struct { ChannelID, UserID, Reason string }
type ChatKickResponse   struct{ errorBlock }

type ChatBanRequest struct {
    ChannelID  string
    UserID     string
    DurationMs int64
    Reason     string
}
type ChatBanResponse struct{ errorBlock }

type ChatUnbanRequest    struct { ChannelID, UserID string }
type ChatUnbanResponse   struct{ errorBlock }

type ChatBroadcastRequest    struct { ChannelID, Body string }
type ChatBroadcastResponse   struct{ errorBlock }
```

### Chat → client server events (typed-event channel; `mmokit.RegisterServerEvent[T]`)

```go
type ChatMessageEvent struct {
    MsgID          string
    ChannelID      string
    SenderUserID   string  // empty = system broadcast
    SenderUsername string  // denormalized at send-time
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
    MsgID            string
    ChannelID        string
    DeletedByUserID  string
}

type ChatMutedEvent struct {
    ChannelID string  // empty = global mute
    UntilMs   int64
    Reason    string
}

type ChatUnmutedEvent struct {
    ChannelID string  // empty = global unmute
}

type ChatKickedEvent struct {
    ChannelID  string
    ByUserID   string
    Reason     string
}

type ChatBannedEvent struct {
    ChannelID string
    ByUserID  string
    UntilMs   int64
    Reason    string
}

type ChatChannelUpdatedEvent struct {
    Channel ChannelInfo  // rename, topic, slow-mode all signal via this single event
}

type ChatChannelGoneEvent struct {
    ChannelID string
}

type ChatMemberRoleChangedEvent struct {
    ChannelID string
    UserID    string
    Role      string  // "member" | "admin"
}

type ChatRateLimitedEvent struct {
    RetryAfterMs int64
}

type ChatChannelsHydratedEvent struct {
    Channels []ChannelInfo  // sent on session-enter; full channel list in one shot
}
```

### Registration sketch

```go
// pkg/services/chat/handlers.go
func (s *Service) RegisterOps(router *ops.Router) error {
    mmokit.RegisterOp[ChatSendRequest, ChatSendResponse](router, s.handleSend)
    mmokit.RegisterOp[ChatSendDMRequest, ChatSendDMResponse](router, s.handleSendDM)
    mmokit.RegisterOp[ChatJoinRequest, ChatJoinResponse](router, s.handleJoin)
    // ... rest of ops
    return nil
}

// pkg/services/chat/events.go (called once at package init, mirroring registerEngineTypedEvents)
func registerChatServerEvents() {
    mmokit.RegisterServerEvent[ChatMessageEvent]()
    mmokit.RegisterServerEvent[ChatDMEvent]()
    mmokit.RegisterServerEvent[ChatMemberJoinedEvent]()
    // ... rest of events
}
```

### Wire-protocol design calls

- **`msg_id` is UUID v7** — globally unique without coordination, time-ordered for free, matches the UUID family used by auth (`auth_users.user_id`). Forward-compat for v2 persistence, edits, reactions, paged history.
- **Sender username denormalized** in fanout events: snapshotted at send-time. Renames render new in live messages, old in already-delivered ones.
- **`ChatDMEvent` carries no `ChannelID`** — DMs are not channels.
- **Server broadcasts** use `ChatMessageEvent` with `SenderUserID == ""`. Clients render with system styling.
- **`ChatRateLimitedEvent`** is informational on top of the response's `errorBlock`; lets clients show a "calm down" toast even if not directly waiting on a response.
- **`ChatChannelsHydratedEvent`** is sent on session-enter so clients learn their full channel list in one shot without a separate `ListChannels` round-trip.
- **`errorBlock` embedded in every Response struct** — same convention as auth's `AuthLoginResponse.{ErrorCode, ErrorMessage, RetryAfterMs, …}`. Lets clients branch by-typed-response without unwrapping a separate framework error envelope.

## 6. Postgres schema — `pkg/services/chat/postgres/migrations/001_init.sql`

Three tables. No FKs to `auth_users` directly — chat references user_ids loosely (auth is the source of truth for identity; chat just stores UUIDs as opaque keys). Keeps chat deployable independently of auth and avoids cross-service migration coupling.

### `chat_channels`

```sql
CREATE TABLE chat_channels (
  channel_id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  slug                TEXT NOT NULL UNIQUE,        -- 'world', 'guild:42', 'secretclub'
  kind                TEXT NOT NULL,                -- 'system_all'|'system_predicate'|'custom'
  topic               TEXT NOT NULL DEFAULT '',
  slow_mode_seconds   INT  NOT NULL DEFAULT 0,
  password_hash       TEXT,                         -- argon2id; NULL = no password
  owner_user_id       UUID,                         -- NULL for system channels
  created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  metadata            JSONB
);

CREATE INDEX chat_channels_kind ON chat_channels(kind);
```

### `chat_channel_members`

Membership row doubles as ban tombstone — `banned_until` non-null + future means user is banned. Keeps membership state in one place; reaper clears expired bans without DELETE-ing the row.

```sql
CREATE TABLE chat_channel_members (
  channel_id      UUID NOT NULL REFERENCES chat_channels(channel_id) ON DELETE CASCADE,
  user_id         UUID NOT NULL,
  role            TEXT NOT NULL DEFAULT 'member',  -- 'member' | 'admin'
  joined_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  banned_until    TIMESTAMPTZ,
  banned_by       UUID,
  banned_reason   TEXT,
  PRIMARY KEY (channel_id, user_id)
);

CREATE INDEX chat_members_user ON chat_channel_members(user_id);
CREATE INDEX chat_members_banned ON chat_channel_members(banned_until)
  WHERE banned_until IS NOT NULL;
```

### `chat_mutes`

Per-user mute, optionally scoped to a channel. Global mute uses a sentinel UUID (`00000000-0000-0000-0000-000000000000`) for `channel_id` so the PK stays simple — Postgres's NULL handling in composite PKs is a footgun we avoid.

```sql
CREATE TABLE chat_mutes (
  user_id     UUID NOT NULL,
  channel_id  UUID NOT NULL,                       -- sentinel zero-UUID = global
  expires_at  TIMESTAMPTZ NOT NULL,
  reason      TEXT,
  muted_by    UUID NOT NULL,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  PRIMARY KEY (user_id, channel_id)
);

CREATE INDEX chat_mutes_expiry ON chat_mutes(expires_at);
```

### Reaper

Background goroutine on the chat-service instance, runs every `ChatOpts.MuteReapInterval` (default 1m):

```sql
DELETE FROM chat_mutes WHERE expires_at < NOW();
UPDATE chat_channel_members
   SET banned_until = NULL, banned_by = NULL, banned_reason = NULL
 WHERE banned_until IS NOT NULL AND banned_until < NOW();
```

Both queries are idempotent and multi-instance-safe (would still be correct if v2 ran multiple chat instances).

### `pgcrypto`

`gen_random_uuid()` requires the `pgcrypto` extension. Auth's migration prelude already installs it; chat migrations assume it's present.

## 7. In-memory state model

Materialized at startup from the three tables. Source-of-truth is RAM; Postgres writes are write-through (DB first, then in-memory mutation, never the other order).

```go
type Service struct {
    repo     Repository
    authRepo auth.Repository  // for HasCapability / UserIDByUsername lookups

    mu          sync.RWMutex
    channels    map[ChannelID]*Channel              // all channels
    bySlug      map[string]ChannelID                // slug → channel_id
    membership  map[ChannelID]map[UserID]MemberRole // explicit-member channels only
    userChans   map[UserID]map[ChannelID]struct{}   // reverse index — channels per user
    mutes       map[MuteKey]MuteInfo                // (user_id, channel_id) → mute (zero-UUID for global)
    msgIDIndex  *ttlMap[MsgID]ChannelID             // 5-min TTL, for DELETE_MESSAGE resolution

    // Online presence (gateway-driven via ChatSessionEnter/Leave)
    online      map[UserID]ConnID                   // one connID per user (auth enforces)
    connIndex   map[ConnID]UserID                   // reverse lookup for session-leave / gateway-loss
    gatewayConn map[GatewayID]map[ConnID]struct{}   // for gateway-loss cleanup
    subs        map[ChannelID]map[ConnID]struct{}   // fanout map — built from membership at login

    // Rate limiting
    rateBuckets map[UserID]*tokenBucket             // per-user send rate
    slowMode    map[ChannelMember]time.Time         // (channel_id, user_id) → last_send
}
```

**Bootstrap** (`Service.Init`):

```text
1. Apply migrations (via Config.ExtraMigrations from auth's hook)
2. UPSERT each ChatOpts.DefaultChannels entry into chat_channels
   - INSERT ON CONFLICT (slug) DO UPDATE SET topic=$1, kind=$2, slow_mode_seconds=$3
   - kind transitions are an error (logged + skipped); operators must delete then recreate
3. SELECT * FROM chat_channels         → channels + bySlug
4. SELECT * FROM chat_channel_members  → membership + userChans (load ALL rows including banned_until > NOW; ban check is per-op at runtime so post-expiry auto-unban works without a re-load)
5. SELECT * FROM chat_mutes WHERE expires_at > NOW → mutes
6. Start reaper goroutine
7. Start msg_id TTL sweeper goroutine
```

**Write-through pattern** (every mutation):

```go
func (s *Service) AddMember(ctx, channelID, userID, role) error {
    s.mu.Lock()
    defer s.mu.Unlock()
    if err := s.repo.AddMember(ctx, channelID, userID, role); err != nil {
        return err  // DB failed → no in-memory mutation
    }
    s.membership[channelID][userID] = role
    s.userChans[userID][channelID] = struct{}{}
    if connID, ok := s.online[userID]; ok {
        s.subs[channelID][connID] = struct{}{}
        s.fanoutEvent(channelID, &ChatMemberJoinedEvent{...})
    }
    return nil
}
```

DB-first ordering ensures memory state is always a subset of DB state on partial failure — never the other way around.

For `system_all` channels, `s.membership[channelID]` is nil; `fanoutEvent` checks `kind == SYSTEM_ALL` and walks `s.online` directly. `s.subs[channelID]` is also nil for system_all — every connected user is implicitly subscribed via their presence in `s.online`.

## 8. End-to-end flows

### Login auto-subscribe — `MeshFrame.ChatSessionEnter`

When the gateway authenticates a connection (auth-service flow), it announces the session to chat:

```text
auth login success on gateway
  → gateway sets authStates[connID] = {user_id, username, ...}
  → gateway dispatches PlayerAssignment to cell  (existing flow)
  → gateway sends MeshFrame.ChatSessionEnter {gateway_id, conn_id, user_id, username} to chat

chat.handleSessionEnter:
  s.online[user_id]      = connID
  s.connIndex[connID]    = user_id
  s.gatewayConn[gw][cid] = present

  channels := s.userChans[user_id]   // explicit memberships (custom + system_predicate)
  for each channelID in channels:
    s.subs[channelID][connID] = present
    fanout SE_CHAT_MEMBER_JOINED to other members  (presence broadcast)

  // SYSTEM_ALL channels are implicit — no subs[] entry needed; fanout walks online[]

  // Build hydration payload: explicit channels + every system_all channel
  hydration := []ChannelInfo{...}
  send SE_CHAT_CHANNELS_HYDRATED to connID
```

On disconnect:

```text
gateway detects WS close
  → gateway sends MeshFrame.ChatSessionLeave {gateway_id, conn_id} to chat

chat.handleSessionLeave:
  user_id := s.connIndex[connID]
  for each channelID where s.subs[channelID][connID]:
    delete s.subs[channelID][connID]
    fanout SE_CHAT_MEMBER_LEFT to other members
  delete s.online[user_id]
  delete s.connIndex[connID]
  delete s.gatewayConn[gw][connID]
  // membership rows untouched — user is still "in" the channel, just offline.
```

The `ChatSessionEnter` / `ChatSessionLeave` messages are `MeshFrame` extensions, not op codes. Same architectural pattern as auth's `PlayerAssignment`. Internal-only; clients never see them.

### Send-message flow

```text
1. Client → CHAT_OP_SEND_MESSAGE {channel_id, body}
2. Gateway: routes to chat (services-framework hash-affinity; v1 has N=1)
3. Chat handler:
   a. sender := authStates[connID].user_id (lifted from gateway-attached OpContext)
   b. validate len(body) <= MaxMessageLen → else PAYLOAD_TOO_LARGE
   c. validate channel exists → else CHANNEL_NOT_FOUND
   d. authorize: user is member (or kind == SYSTEM_ALL && online) → else NOT_A_MEMBER
   e. mute check: lookup mutes[user_id, ZERO_UUID] OR mutes[user_id, channel_id] → if active, MUTED + retry_after_ms
   f. ban check: chat_channel_members.banned_until > NOW → BANNED + retry_after_ms
   g. rate check (per-user token bucket): allow → else RATE_LIMITED + retry_after_ms
   h. slow-mode check (channel+user last_send): if NOW - lastSend < slow_mode_seconds → SLOW_MODE + retry_after_ms
   i. msg_id := uuid.NewV7()
   j. msgIDIndex.Put(msg_id, channel_id)  // 5-min TTL for DELETE resolution
   k. fanoutEvent(channel_id, &ChatMessageEvent{msg_id, sender_id, sender_username, body, sent_at_ms})
   l. slowMode[(channel_id, user_id)] = NOW
   m. reply ChatSendResponse{msg_id, sent_at_ms}
```

`fanoutEvent` walks `s.subs[channel_id]` (or `s.online` for `SYSTEM_ALL`), groups recipient connIDs by owning gateway via PeerList, sends one MeshFrame per gateway with the event payload + recipient connID list. Each gateway dispatches to its local connections.

### Send-DM flow

```text
1. Client → CHAT_OP_SEND_DM {recipient_user_id, body}
2. Chat handler:
   a. validate body length
   b. mute check: global mute on sender → MUTED
   c. rate check: per-user bucket (DMs share the bucket with channel sends)
   d. msg_id := uuid.NewV7()
   e. if recipient is in s.online: send SE_CHAT_DM to their connID
   f. ALSO send SE_CHAT_DM back to sender (multi-window UIs see their own sends)
   g. if recipient offline: drop silently (no offline DM in v1)
   h. reply ChatSendDMResponse{msg_id, sent_at_ms}
```

### Custom-channel create / join

```text
CREATE:
  client → CHAT_OP_CREATE_CHANNEL {slug, password?, topic?}
  handler:
    - validate slug format (see §15.1)
    - validate slug doesn't use reserved namespace prefix — reserved set: `guild:`, `party:`, `alliance:`, `raid:`, `system:`. Configurable via ChatOpts.ReservedSlugPrefixes
    - if password set: validate len(password) >= MinChannelPasswordLen (default 4); else INVALID_PASSWORD
    - check MaxChannelsPerUser limit (counts custom channels owner_user_id == caller AND not yet deleted)
    - INSERT chat_channels (kind=CUSTOM, owner=caller, password_hash=argon2id(password) or NULL)
    - INSERT chat_channel_members (channel_id, caller, role='admin')
    - reply ChannelInfo
    - if caller online: subs[channel_id][connID] = present (auto-subscribe)

JOIN:
  client → CHAT_OP_JOIN_CHANNEL {slug, password?}
  handler:
    - resolve slug → channel via bySlug
    - validate kind == CUSTOM (system channels can't be self-joined)
    - if password_hash set: argon2id-verify(password, password_hash) → else INVALID_PASSWORD
    - check MaxMembersPerCustomChannel
    - INSERT chat_channel_members (channel_id, caller, role='member')
    - if caller online: subs[channel_id][connID] = present
    - fanout SE_CHAT_MEMBER_JOINED to other members
    - reply ChannelInfo
```

### Moderation flows (mute / kick / ban / delete)

All follow the same shape: capability check → DB write → in-memory mutation → fanout event. Representative example:

```text
MUTE (channel admin OR chat.admin):
  client → CHAT_OP_MUTE_USER {user_id, channel_id?, duration_ms, reason}
  handler:
    - canModerate(caller, channel_id) check (or hasGlobalAdmin if channel_id empty)
    - INSERT INTO chat_mutes (user_id, channel_id|=ZERO_UUID, expires_at, reason, muted_by)
      ON CONFLICT (user_id, channel_id) DO UPDATE SET expires_at = ...
    - mutes[(user_id, channel_id)] = {expires_at, reason}
    - if target online: send SE_CHAT_MUTED to target's connID
    - reply ok

DELETE (channel admin OR chat.admin):
  client → CHAT_OP_DELETE_MESSAGE {msg_id, channel_id}
  handler:
    - msgIDIndex.Get(msg_id) → expected_channel_id
    - if expired or mismatched: return MESSAGE_UNKNOWN
    - canModerate(caller, channel_id) check
    - fanoutEvent(channel_id, ChatMessageDeletedEvent{msg_id, channel_id, deleted_by})
    - reply ok

KICK (channel admin OR chat.admin):
  client → CHAT_OP_KICK_FROM_CHANNEL {channel_id, user_id, reason}
  handler:
    - canModerate(caller, channel_id) check
    - DELETE FROM chat_channel_members WHERE channel_id=$1 AND user_id=$2
    - membership[channel_id].delete(user_id); userChans[user_id].delete(channel_id)
    - if target online: subs[channel_id].delete(target.connID); send SE_CHAT_KICKED to target
    - fanoutEvent(channel_id, ChatMemberLeftEvent{channel_id, user_id})
    - reply ok

BAN (channel admin OR chat.admin):
  client → CHAT_OP_BAN_FROM_CHANNEL {channel_id, user_id, duration_ms, reason}
  handler:
    - canModerate(caller, channel_id) check
    - INSERT INTO chat_channel_members (..., banned_until=NOW+duration, banned_by=caller, banned_reason=reason)
      ON CONFLICT (channel_id, user_id) DO UPDATE SET banned_until=..., banned_by=..., banned_reason=...
    - if target online: subs[channel_id].delete(target.connID); send SE_CHAT_BANNED to target
    - fanoutEvent(channel_id, ChatMemberLeftEvent{...})  // ban behaves as kick from membership-state POV
    - reply ok
```

Delete is best-effort: only messages still within the 5-minute msgIDIndex TTL can be deleted. Late joiners who never received the message won't see the deletion event either — that's correct (they have nothing to delete from their UI).

## 9. Authorization

### Helper

```go
func (s *Service) canModerate(userID UserID, channelID ChannelID) bool {
    if s.hasGlobalAdmin(userID) { return true }
    role, ok := s.membership[channelID][userID]
    return ok && role == RoleAdmin
}

func (s *Service) hasGlobalAdmin(userID UserID) bool {
    return s.authRepo.HasCapability(userID, "chat.admin")
}
```

### `auth.Repository.HasCapability`

Auth gains a fine-grained capability table. Capabilities are atomic permission strings (`chat.admin`, future `chat.broadcaster`, `auth.admin`, `guild.admin`, etc.) granted individually per user.

```sql
CREATE TABLE auth_capabilities (
  user_id     UUID NOT NULL REFERENCES auth_users(user_id) ON DELETE CASCADE,
  capability  TEXT NOT NULL,
  granted_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  granted_by  UUID NOT NULL,                       -- bootstrap admin grants self-grant; capture caller_id
  expires_at  TIMESTAMPTZ,                         -- optional; NULL = permanent
  PRIMARY KEY (user_id, capability)
);

CREATE INDEX auth_capabilities_user ON auth_capabilities(user_id);
CREATE INDEX auth_capabilities_expiry ON auth_capabilities(expires_at)
  WHERE expires_at IS NOT NULL;
```

`auth.Repository.HasCapability(userID, capability) bool` translates to:

```sql
SELECT EXISTS(
  SELECT 1 FROM auth_capabilities
   WHERE user_id = $1 AND capability = $2
     AND (expires_at IS NULL OR expires_at > NOW())
);
```

A small in-memory TTL cache (default 30s expiry, keyed by `user_id+capability`) sits in front of the SELECT to avoid round-trips on every chat op. Cache invalidation: any `Grant` / `Revoke` clears the affected entry process-locally; cross-process invalidation isn't needed in v1 (auth is single-instance), and 30s of staleness on a revoke is acceptable.

**Bootstrap.** When the cluster has no users yet, the first user is regular (no special capabilities). An operator runs `auth bootstrap-admin <username>` from the console exactly once to grant the full admin capability set (`auth.admin` + `chat.admin` + every other `*.admin` registered by services in the binary) to a named existing user. Subsequent admin grants flow through `auth user grant`.

**Console commands** (auth):

- `auth user grant <username> <capability>` — adds row; `granted_by = caller_id`
- `auth user revoke <username> <capability>` — deletes row
- `auth user capabilities <username>` — lists granted capabilities + expirations
- `auth bootstrap-admin <username>` — one-time, errors if any admin capability is already granted to anyone (must be cluster-fresh)

**Service-account capabilities.** The same table holds capabilities for service-account identities (separate UUIDs in `auth_users` flagged as non-loginable, or in a parallel `auth_service_accounts` table — implementation detail). v1 uses the bypass path described in §15.6; the table is ready for v2 service-account auth without schema work.

For chat specifically, only `chat.admin` exists as a capability in v1. Future fine-grained capabilities (`chat.broadcaster`, `chat.global_mute`, `chat.delete_message`, etc.) are additive — just new strings, no schema change.

### Per-op authorization rules

| Op | Allowed if |
|---|---|
| `SEND_MESSAGE` | Member of channel (or kind==SYSTEM_ALL && online) AND not muted AND not banned |
| `SEND_DM` | Always (block lists deferred to v2) |
| `JOIN_CHANNEL` | Channel kind == CUSTOM AND password matches AND not banned |
| `LEAVE_CHANNEL` | Always |
| `CREATE_CHANNEL` | Any logged-in user (under MaxChannelsPerUser limit) |
| `LIST_CHANNELS` / `LIST_MEMBERS` | Member of channel |
| `RENAME_CHANNEL` / `SET_TOPIC` / `SET_SLOW_MODE` | canModerate(caller, channel) |
| `ADD_MEMBER` / `REMOVE_MEMBER` / `BULK_SET_MEMBERS` / `SET_MEMBER_ROLE` | canModerate(caller, channel) |
| `REGISTER_CHANNEL` / `UNREGISTER_CHANNEL` | hasGlobalAdmin(caller) OR caller has service-account capability `chat.membership.write` |
| `DELETE_MESSAGE` / `MUTE_USER` (channel-scoped) / `UNMUTE_USER` / `KICK_FROM_CHANNEL` / `BAN_FROM_CHANNEL` / `UNBAN_FROM_CHANNEL` | canModerate(caller, channel) |
| `MUTE_USER` (global, channel_id empty) | hasGlobalAdmin(caller) |
| `BROADCAST_SYSTEM` | hasGlobalAdmin(caller) |

**v1 callers of membership-mutation ops:**

| Op | v1 caller |
|---|---|
| `CHAT_OP_REGISTER_CHANNEL` | Admin console (`chat channel create`) — system-channel UPSERT at startup runs in-process via `ChatOpts.DefaultChannels`, not over the wire |
| `CHAT_OP_UNREGISTER_CHANNEL` | Admin console only |
| `CHAT_OP_ADD_MEMBER` | Admin console only; player self-add via `JOIN_CHANNEL` for custom |
| `CHAT_OP_REMOVE_MEMBER` | Admin console only; player self-remove via `LEAVE_CHANNEL` for custom |
| `CHAT_OP_BULK_SET_MEMBERS` | Admin console only |
| `CHAT_OP_SET_MEMBER_ROLE` | Channel admin (via `/op`/`/deop`) + admin console |

The membership-mutation ops also exist on the wire from day one for future service-to-service callers (guild service, party service, alliance service). When those services ship, they call these unchanged ops with their own service-account capability grants. No chat-side changes required.

## 10. Rate limiting

```go
type tokenBucket struct {
    tokens    int
    lastFill  time.Time
    capacity  int           // ChatOpts.UserRateMax (default 5)
    refillDur time.Duration // ChatOpts.UserRateWindow (default 5s)
}

func (b *tokenBucket) takeOrFail(now time.Time) (ok bool, retryAfter time.Duration) {
    refillRate := b.refillDur / time.Duration(b.capacity)
    b.tokens = min(b.capacity, b.tokens + int(now.Sub(b.lastFill) / refillRate))
    b.lastFill = now
    if b.tokens == 0 {
        return false, refillRate
    }
    b.tokens--
    return true, 0
}
```

- One bucket per user_id. Created on first send.
- Bucket eviction: idle > 1h.
- DMs share the per-user bucket with channel sends (combined ceiling). Decision rationale: simpler operationally; DM-flood hasn't been observed as a vector. Splittable in v1.5 if needed.
- Slow-mode is independent of the rate bucket: even if the bucket has tokens, slow-mode of a slow channel still blocks.

### Per-channel slow-mode

`chat_channels.slow_mode_seconds` (persisted) sets minimum gap between any user's sends in that channel. `slowMode[(channel_id, user_id)] time.Time` (in-memory only) tracks last-send. Loss on restart just gives users a free message; not worth persisting.

Channel admins set per-channel via `CHAT_OP_SET_SLOW_MODE`. Operators set defaults via `ChatOpts.DefaultSlowMode` (default 0 = off). New channels inherit the default at registration time.

### Message size limits

```go
type ChatOpts struct {
    UserRateMax       int            // default 5
    UserRateWindow    time.Duration  // default 5s
    DefaultSlowMode   time.Duration  // default 0 (off)
    MaxMessageLen     int            // default 500
    MaxTopicLen       int            // default 200
    MaxChannelSlugLen int            // default 32
    MaxChannelsPerUser int           // default 5 (counts owned custom channels per user; deletion frees a slot)
    MaxMembersPerCustomChannel int   // default 1000
    MinChannelPasswordLen int        // default 4 (lower bar than account passwords; channel passwords are low-stakes)
    MuteReapInterval  time.Duration  // default 1m
    MsgIDTTL          time.Duration  // default 5m (DELETE_MESSAGE resolution window)
    ReservedSlugPrefixes []string    // default: ['guild:', 'party:', 'alliance:', 'raid:', 'system:']

    // Channels created at startup. Idempotent UPSERT: existing rows with the
    // same slug have their topic/kind/slow_mode reconciled to the declared
    // values. Removing a slug from the list does NOT delete its row — operators
    // delete via `chat channel delete` console command. Default: empty.
    DefaultChannels []DefaultChannelDef
}

type DefaultChannelDef struct {
    Slug             string
    Kind             ChannelKind   // SYSTEM_ALL or SYSTEM_PREDICATE
    Topic            string
    SlowModeSeconds  int
}

func DefaultChatOpts() ChatOpts { /* ... */ }
```

## 11. Console, cmdsys, observability

### Console commands (registered on `RoleCoordinator`)

All cmdsys-typed; JSON-Schema visible at `GET /commands/chat.*`. Same shape as `auth user *` commands. Username args resolve via auth-side `UserIDByUsername` lookup before the chat handler runs.

| Command | Args | RouteKind | Purpose |
|---|---|---|---|
| `chat channel list` | (none) | RouteAllHosts | Cluster-wide channel roster |
| `chat channel info` | `<slug>` | RouteAllHosts | Detail: kind, topic, member count, slow-mode, recent activity |
| `chat channel create` | `<slug> <kind> [topic]` | RouteAllHosts | Server-side channel registration |
| `chat channel delete` | `<slug>` | RouteAllHosts | Force unregister + cascade members |
| `chat channel rename` | `<slug> <new-slug>` | RouteAllHosts | Rename |
| `chat channel topic` | `<slug> <text>` | RouteAllHosts | Set topic |
| `chat channel slowmode` | `<slug> <seconds>` | RouteAllHosts | Set slow-mode (0 = off) |
| `chat channel addmember` | `<slug> <username> [role]` | RouteAllHosts | Add member (default role=member) |
| `chat channel removemember` | `<slug> <username>` | RouteAllHosts | Remove member |
| `chat user mute` | `<username> [<channel>] <dur>` | RouteAllHosts | Mute (channel-scoped or global) |
| `chat user unmute` | `<username> [<channel>]` | RouteAllHosts | Clear mute |
| `chat user kick` | `<username> <channel>` | RouteAllHosts | Kick from one channel |
| `chat user ban` | `<username> <channel> <dur>` | RouteAllHosts | Ban from channel |
| `chat user unban` | `<username> <channel>` | RouteAllHosts | Clear ban |
| `chat broadcast` | `<channel> <text>` | RouteAllHosts | System announcement |
| `chat msg delete` | `<msg_id> <channel>` | RouteAllHosts | Best-effort in-flight delete (within msg_id TTL) |

### In-game slash commands (client-side parsing)

Slash parsing lives in the client. Chat handlers see typed ops only.

| Slash | Translates to |
|---|---|
| `/w <user> <text>` (alias `/whisper`, `/dm`, `/msg`) | `CHAT_OP_SEND_DM` |
| `/join <slug> [pw]` | `CHAT_OP_JOIN_CHANNEL` |
| `/leave <slug>` | `CHAT_OP_LEAVE_CHANNEL` |
| `/create <slug> [pw]` | `CHAT_OP_CREATE_CHANNEL` |
| `/topic <text>` (in active channel) | `CHAT_OP_SET_TOPIC` |
| `/rename <new-slug>` | `CHAT_OP_RENAME_CHANNEL` |
| `/slowmode <seconds>` | `CHAT_OP_SET_SLOW_MODE` |
| `/mute <user> [<dur>]` | `CHAT_OP_MUTE_USER` |
| `/unmute <user>` | `CHAT_OP_UNMUTE_USER` |
| `/kick <user>` | `CHAT_OP_KICK_FROM_CHANNEL` |
| `/ban <user> [<dur>]` | `CHAT_OP_BAN_FROM_CHANNEL` |
| `/unban <user>` | `CHAT_OP_UNBAN_FROM_CHANNEL` |
| `/delete <msg_id>` | `CHAT_OP_DELETE_MESSAGE` |
| `/broadcast <text>` | `CHAT_OP_BROADCAST_SYSTEM` (GM-only) |
| `/op <user>` | `CHAT_OP_SET_MEMBER_ROLE { role: 'admin' }` |
| `/deop <user>` | `CHAT_OP_SET_MEMBER_ROLE { role: 'member' }` |

### Metrics (auto-registered on `/metrics`)

```text
chat_messages_sent_total{channel_kind}
chat_messages_dropped_total{reason="muted"|"banned"|"rate_limited"|"slow_mode"|"too_long"}
chat_dms_sent_total{}
chat_active_channels{kind}
chat_active_subscriptions{}
chat_online_users{}
chat_op_duration_seconds{op_code}
chat_fanout_recipients_per_message{kind}  // histogram
chat_mute_active{}
chat_ban_active{}
```

### Logging

Per `feedback_logging`, all significant chat events log via `ctx.Logger.Log(cat, ...)`. Log categories auto-registered on `Service.Init`:

- `chat:lifecycle` — channel create/delete/register/unregister
- `chat:moderation` — mutes, kicks, bans, deletes, broadcasts
- `chat:membership` — add/remove/role changes
- `chat:rate` — rate-limit and slow-mode rejections
- `chat:presence` — session enter/leave, gateway-loss cleanup
- `chat:errors` — DB / fanout / unexpected failures

Every log line includes `user_id` and (where applicable) `channel_id` + `target_user_id`.

## 12. Game integration

### Wiring (`main.go`)

The game's only chat-side line is the `RegisterChatService` call. System channels are declared as data in `ChatOpts.DefaultChannels`; the chat service UPSERTs them itself at startup. There is no `coord.OnReady` chat callback, no `mmokit.ChatClient` import, no game-side knowledge of which channels exist.

```go
mmokit.RegisterChatService(coord, mmokit.ChatOpts{
    DefaultChannels: []mmokit.DefaultChannelDef{
        {Slug: "world", Kind: mmokit.ChannelKindSystemAll, Topic: "World chat"},
        {Slug: "help",  Kind: mmokit.ChannelKindSystemAll, Topic: "Help chat. Be patient."},
        {Slug: "trade", Kind: mmokit.ChannelKindSystemAll, Topic: "Trade chat."},
    },
    UserRateMax:    5,
    MaxMessageLen:  500,
})
```

Operators with no want for `help` / `trade`: set `DefaultChannels` to whatever subset they want, or `nil` for "no defaults at all" (custom-channel-only chat).

Run with the chat kind included:

```bash
./bin/4node-basic --mode=coordinator,host,gateway,service --services=auth,chat
# or distributed:
./bin/4node-basic --mode=service --services=chat --coordinator-addr=...
```

### `mmokit.ChatClient`

A thin Go-side typed client that wraps the chat op codes and dispatches via the existing services-framework op routing. Used by:

- Tests that want to drive chat from the test process
- Future guild / party / alliance services (when they ship) to push memberships

**No v1 game caller.** The game has zero awareness of chat — the only call site is the `RegisterChatService` configuration line. `ChatClient` is shipped because tests need it and v2 service-to-service callers will need it; it carries no run-time cost when unused.

Internal implementation: constructs op envelopes and sends them through the coordinator's op router with a service-account capability — same wire path a remote caller would use, no in-process shortcut.

### v1 vs future

In v1 the game does not push memberships because there are no guilds/parties/alliances yet to push.

Future service-to-service push: when the guild service ships, *it* (not the game) calls `chat.RegisterChannel("guild:42", system_predicate)` on guild creation and `chat.AddMember(...)` on join. The chat service treats those calls identically to client ops; authorization is by capability — the guild service is granted `chat.membership.write` at registration. Game-side, a `GuildAffiliation` ECS component can cache a thin slice of guild membership for fast in-tick reads (rendering, replication), updated via `SE_GUILD_*` events from the guild service. The component is read-only from the game's perspective; the guild service is the source of truth.

## 13. Testing strategy

| Layer | What | Where |
|---|---|---|
| Unit | Token-bucket rate limit (refill, edge cases, eviction) | `pkg/services/chat/ratelimit_test.go` |
| Unit | Slow-mode tracker (channel+user, expiry) | `pkg/services/chat/ratelimit_test.go` |
| Unit | `canModerate` authorization table | `pkg/services/chat/authorization_test.go` |
| Unit | msg_id TTL index (insert, expire, sweep) | `pkg/services/chat/msgindex_test.go` |
| Unit | Op handlers against `chattest.RepoMock` | `pkg/services/chat/handlers_test.go` |
| Postgres integration | Migrations apply cleanly; CRUD on three tables | `pkg/services/chat/postgres/postgres_test.go` (build tag `pgtest`) |
| Postgres integration | Mute reaper deletes only past-expiry rows | Same |
| Postgres integration | Ban reaper clears banned_until on expiry | Same |
| Postgres integration | Bootstrap: write rows, restart service, assert in-memory state matches | Same |
| Cluster integration | Send→fanout: 3 connected users in `world`, A sends, B+C receive | `pkg/universe/chat_e2e_test.go` (new) |
| Cluster integration | DM round-trip: A→B online, B receives; A→C offline, dropped | Same |
| Cluster integration | Custom channel: A creates `cool`, B joins, A sends, B receives | Same |
| Cluster integration | Membership push: `AddMember(guild:42, alice)` → alice receives next send to that channel | Same |
| Cluster integration | Mute: GM mutes alice for 60s in `world`, alice's send returns MUTED, others don't see msg | Same |
| Cluster integration | Slow-mode: 5s slow on `world`, alice sends 2 in 1s → 2nd returns SLOW_MODE | Same |
| Cluster integration | Rate limit: 6 sends in 1s → 6th returns RATE_LIMITED, retry_after_ms present | Same |
| Cluster integration | Auth gate: unauth'd connID → CHAT_OP_SEND_MESSAGE returns NOT_AUTHENTICATED at gateway | Same |
| Cluster integration | Gateway-loss cleanup: kill gateway holding a session, chat clears `online[user]` within 1 PeerList hop | Same |
| Cluster integration | Channel-admin promotion: A creates channel, A `/op`s B, B kicks C | Same |
| Smoke (manual) | `just distributed`, web client connects, sends in world, opens DM, GM mutes, etc | `examples/4node-basic` |

The cluster fixture's `WithServiceHost("chat", 1)` stands up a real chat instance against the test Postgres for end-to-end coverage. `WithChatStub()` for tests that don't care about chat — wires `chattest.NoopService` so other-service tests aren't entangled.

## 14. What dies, what changes

### Dies

Nothing — the in-engine chat plumbing was already deleted in [9b32bc3](commit:9b32bc3). This is greenfield work.

### Changes

- **`pkg/auth/` → `pkg/services/auth/`** — mechanical rename, prerequisite to chat work. Lands as a separate small commit.
- **`auth_capabilities` table** — new migration adds the capability storage (see §9 schema). Holds human + future service-account grants, with optional time-bound expiration.
- **`auth.Repository.HasCapability(userID, capability)`** — new repo method backed by a SELECT against the capability table, with a 30s in-process TTL cache. v1 ships with `chat.admin` as the only registered capability; capabilities are atomic strings, so adding new ones is data-only.
- **`auth.Repository.GrantCapability` / `RevokeCapability` / `ListCapabilities`** — new repo methods feeding the console commands.
- **`auth user grant <username> <capability>`** / **`auth user revoke <username> <capability>`** / **`auth user capabilities <username>`** / **`auth bootstrap-admin <username>`** — new auth console commands.
- **`MeshFrame.ChatSessionEnter` / `ChatSessionLeave`** — new internal mesh-frame variants. Wired into the gateway's existing auth login/logout path.
- Chat learns the connID → gateway-id mapping at session-enter (the MeshFrame carries `gateway_id`) and stores it in `s.gatewayConn`. PeerList changes drive gateway-loss cleanup; per-op handlers don't need a `GatewayID` on `OpContext`.

## 15. Open questions

### 15.1 Slug character class

Channel slugs need a defined character class. Options:

- `[a-z0-9_-]` (ASCII-only, kebab/snake) — simplest
- `[a-z0-9_:-]` — includes `:` so guild/party namespacing (`guild:42`) works literally as the slug
- Unicode-permissive — invites visual-spoofing (`world` vs `wоrld` with Cyrillic `о`)

**Recommendation:** `[a-z0-9_:-]`, max 32 chars, lowercase forced (matches auth's username convention). Reserve a `:` namespacing for service-prefixed channels (`guild:42`, `party:abc`, `alliance:xyz`). Custom user-created slugs forbidden from using `:` (reserved for services).

### 15.2 Custom-channel access mode

v1 ships password-only access for custom channels. Invite-only is a v2 add: `chat_channels.access_mode TEXT DEFAULT 'open'|'password'|'invite'`. Doesn't block anything in v1.

### 15.3 Leaving system channels

Should players be able to `LEAVE_CHANNEL` for `system_all` channels? Some MMOs let you mute world chat; others force you to keep it visible.

**Recommendation:** v1 does NOT allow leaving `system_all`. Mandatory channels for emergency announcements. v2 adds a `chat_user_optouts(user_id, channel_id)` table if user feedback demands it.

### 15.4 DM rate limiting

Combined per-user bucket (DMs + channel sends share) vs. separate buckets per category.

**Recommendation:** start combined. One knob is better than two until DM-flood is observed as a vector. Splittable in v1.5.

### 15.5 Custom-channel password hashing

Chat uses argon2id for custom-channel passwords (same library as auth). Recommended params: `m=8192 (8 MiB), t=2, p=2` — significantly lighter than auth's account-password params (`m=65536, t=3, p=4`) because channel passwords are lower-stakes (an attacker who guesses one gets channel-membership, not account access). Implementation re-uses auth's `argon2id.Hash` / `argon2id.Verify` helpers but with chat's own `ArgonParams` constant defined in `pkg/services/chat/password.go`.

### 15.6 Service-account authentication (capability *storage* shipped in v1)

The capability *table* lands in v1 (see §9) and is ready to hold service-account grants alongside human grants. What's deferred is service-account *authentication* — the mechanism by which an inbound op proves "I am the guild service" so the auth-side can resolve a service-account UUID and run `HasCapability` against it.

- **v1:** human users authenticate at login and receive their `user_id`. Op handlers run `HasCapability(user_id, "chat.admin")` against the capability table. Wire callers without `chat.admin` are rejected. The chat process's own internal callers (cmdsys console handlers, the in-process startup hook for system-channel registration) are recognized via an in-process bypass — they never traverse the wire and don't need a capability check.
- **v2** (when guild / party / alliance services ship): each service-host registers a service-account UUID with auth, signs outbound mesh-frames (or holds a pre-shared service token), and the receiving gateway/host resolves the signature → service-account user_id before dispatching to the handler. The handler's `HasCapability` call is unchanged — it just sees a service-account UUID instead of a human user_id.

The implication for v1: the capability table can already store rows like `(guild-service-uuid, "chat.membership.write", granted_at, granted_by)`. Those rows do nothing yet because no inbound op carries a service-account identity. v2 lights them up purely on the authentication side.

## 16. Migration & rollout

**Prerequisites (small standalone PR):**
1. `pkg/auth/` → `pkg/services/auth/` mechanical rename. Pure import-path churn.
2. `auth_capabilities` table migration + `auth.Repository.HasCapability` / `GrantCapability` / `RevokeCapability` / `ListCapabilities` (with 30s TTL cache).
3. Auth console commands: `auth user grant <user> <cap>`, `auth user revoke <user> <cap>`, `auth user capabilities <user>`, `auth bootstrap-admin <user>`.

**Chat work:**
4. `pkg/services/chat/typed_messages.go` — all wire structs (requests, responses, server events, ChatError consts, ChannelKind consts, ChannelInfo, MemberInfo).
5. `pkg/services/chat/` package: kind, service, handlers, events, fanout, membership, mute, ratelimit, authorization, repo, msgindex, console.
6. `pkg/services/chat/postgres/` + 001_init.sql migration.
7. `pkg/services/chat/chattest/` + repo mock + noop service.
8. `pkg/mmokit/chat.go` facade (`RegisterChatService`, `ChatOpts`, `ChatClient`).
9. Gateway extension: `MeshFrame.ChatSessionEnter`/`ChatSessionLeave` wired into auth login/logout.
10. Console commands (`chat *`).
11. Cluster fixture: `WithServiceHost("chat", 1)`, `WithChatStub()`.
12. `examples/4node-basic` integration: pass `DefaultChannels` (`world`, `help`, `trade`) to `RegisterChatService` in `main.go`; web client chat panel (input box, channel tabs, DM tab) using the auto-generated SDK.
13. End-to-end smoke pass via `just distributed`.

Estimated scope: ~1800–2200 LOC across `pkg/services/chat/` (new — incl. all wire structs in `typed_messages.go`), `pkg/universe/` (small auth-hook extension for `MeshFrame.ChatSessionEnter`/`Leave`), `examples/4node-basic/web/` (chat UI), tests. Slightly smaller than the original protobuf-flavored estimate since there's no proto file or generated bindings to maintain.

## 17. Future work (deferred from v1)

- **Message persistence** — `chat_messages` table, ring-buffer rehydration on startup, recent-history-on-join, paged backwards history.
- **DM persistence** — per-user inbox table, offline-DM delivery on next login.
- **Block lists** — per-user block lists; cosmetic v1, server-enforced v2.
- **Typing indicators** — `SE_CHAT_TYPING`, high-frequency but trivial state.
- **Reactions / emoji** on messages — depends on message persistence.
- **Threaded replies** — depends on message persistence.
- **Attachments** — file-upload service required.
- **Mentions** — `@user` parsing + offline-mention store.
- **Message edit** — depends on persistence.
- **Channel banner / avatar metadata** — `chat_channels.metadata` JSONB extension.
- **Multi-instance horizontal scale-out** — either v2 sync-stream in `pkg/service/` (`Context.SyncStream`, `Service.Snapshot()` / `Apply(delta)`) OR Redis pub/sub adapter. Routing strategy on `Kind` becomes `hash-by-channel-id` rather than `hash-by-conn-id`.
- **Voice / video** — entirely separate service.
- **Cross-cluster federation** — XMPP-like inter-cluster bridge.
- **Anti-spam pattern detection** — repeat-message, cross-channel flood, mention-flood. Requires stateful pattern tracking.
- **URL filter / regex blocklist / profanity filter** — per-channel configurable.
- **Time-based slow-mode escalation** — auto-tighten when channel is on fire.
- **Guild service** — separate `pkg/services/guild/` calling `chat.RegisterChannel("guild:N")` + `chat.AddMember(...)` via service-account capability.
- **Party / alliance services** — same pattern as guild.
- **`chat.admin` fine-grained capability split** — `chat.moderator`, `chat.broadcaster`, `chat.global_mute`, `chat.delete_message`, etc. Additive (just new strings in the existing capability table); no schema change.
- **Service-account authentication** — signed mesh-frames or pre-shared service tokens for service-to-service auth (capability *storage* already in v1; this lights up the wire side).
- **Opt-out from system channels** — `chat_user_optouts` table; per-user world-chat / help-chat hiding.
- **Email / push notifications for offline DMs** — out of scope, separate notification service.
