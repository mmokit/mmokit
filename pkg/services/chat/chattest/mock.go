// Package chattest provides an in-memory chat.Repository for tests.
package chattest

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/zenion/mmoserver/pkg/services/chat"
)

type RepoMock struct {
	mu       sync.Mutex
	channels map[uuid.UUID]chat.Channel
	bySlug   map[string]uuid.UUID
	members  map[memberKey]chat.ChannelMember
	mutes    map[muteKey]chat.Mute
}

type memberKey struct{ ChannelID, UserID uuid.UUID }
type muteKey struct{ UserID, ChannelID uuid.UUID }

func NewMock() *RepoMock {
	return &RepoMock{
		channels: map[uuid.UUID]chat.Channel{},
		bySlug:   map[string]uuid.UUID{},
		members:  map[memberKey]chat.ChannelMember{},
		mutes:    map[muteKey]chat.Mute{},
	}
}

var _ chat.Repository = (*RepoMock)(nil)

// --- Channels ---

func (m *RepoMock) UpsertChannel(_ context.Context, c chat.Channel) (chat.Channel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if c.ChannelID == uuid.Nil {
		// On INSERT: lookup by slug; if slug exists with different ID, we'd fail UNIQUE
		if existingID, ok := m.bySlug[c.Slug]; ok {
			c.ChannelID = existingID
			c.UpdatedAt = time.Now()
			existing := m.channels[existingID]
			c.CreatedAt = existing.CreatedAt
			m.channels[existingID] = c
			return c, nil
		}
		c.ChannelID = uuid.New()
	}
	if existingID, ok := m.bySlug[c.Slug]; ok && existingID != c.ChannelID {
		return chat.Channel{}, chat.ErrChannelSlugInUse
	}
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now()
	}
	c.UpdatedAt = time.Now()
	m.channels[c.ChannelID] = c
	m.bySlug[c.Slug] = c.ChannelID
	return c, nil
}

func (m *RepoMock) GetChannelByID(_ context.Context, id uuid.UUID) (chat.Channel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.channels[id]
	if !ok {
		return chat.Channel{}, chat.ErrChannelNotFound
	}
	return c, nil
}

func (m *RepoMock) GetChannelBySlug(_ context.Context, slug string) (chat.Channel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.bySlug[slug]
	if !ok {
		return chat.Channel{}, chat.ErrChannelNotFound
	}
	return m.channels[id], nil
}

func (m *RepoMock) ListAllChannels(_ context.Context) ([]chat.Channel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]chat.Channel, 0, len(m.channels))
	for _, c := range m.channels {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

func (m *RepoMock) UpdateChannel(_ context.Context, c chat.Channel) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, ok := m.channels[c.ChannelID]
	if !ok {
		return chat.ErrChannelNotFound
	}
	if existing.Slug != c.Slug {
		if _, taken := m.bySlug[c.Slug]; taken {
			return chat.ErrChannelSlugInUse
		}
		delete(m.bySlug, existing.Slug)
		m.bySlug[c.Slug] = c.ChannelID
	}
	c.UpdatedAt = time.Now()
	c.CreatedAt = existing.CreatedAt
	m.channels[c.ChannelID] = c
	return nil
}

func (m *RepoMock) DeleteChannel(_ context.Context, id uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.channels[id]
	if !ok {
		return chat.ErrChannelNotFound
	}
	delete(m.channels, id)
	delete(m.bySlug, c.Slug)
	for k := range m.members {
		if k.ChannelID == id {
			delete(m.members, k)
		}
	}
	return nil
}

// --- Members ---

func (m *RepoMock) AddOrUpdateMember(_ context.Context, mem chat.ChannelMember) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.channels[mem.ChannelID]; !ok {
		return chat.ErrChannelNotFound
	}
	if mem.JoinedAt.IsZero() {
		mem.JoinedAt = time.Now()
	}
	if mem.Role == "" {
		mem.Role = "member"
	}
	m.members[memberKey{mem.ChannelID, mem.UserID}] = mem
	return nil
}

func (m *RepoMock) RemoveMember(_ context.Context, channelID, userID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memberKey{channelID, userID}
	if _, ok := m.members[k]; !ok {
		return chat.ErrMemberNotFound
	}
	delete(m.members, k)
	return nil
}

func (m *RepoMock) BulkSetMembers(_ context.Context, channelID uuid.UUID, userIDs []uuid.UUID, role string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.channels[channelID]; !ok {
		return chat.ErrChannelNotFound
	}
	for k := range m.members {
		if k.ChannelID == channelID {
			delete(m.members, k)
		}
	}
	if role == "" {
		role = "member"
	}
	now := time.Now()
	for _, uid := range userIDs {
		m.members[memberKey{channelID, uid}] = chat.ChannelMember{
			ChannelID: channelID, UserID: uid, Role: role, JoinedAt: now,
		}
	}
	return nil
}

func (m *RepoMock) ListMembers(_ context.Context, channelID uuid.UUID) ([]chat.ChannelMember, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []chat.ChannelMember
	for k, v := range m.members {
		if k.ChannelID == channelID {
			out = append(out, v)
		}
	}
	return out, nil
}

func (m *RepoMock) ListAllMembers(_ context.Context) ([]chat.ChannelMember, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]chat.ChannelMember, 0, len(m.members))
	for _, v := range m.members {
		out = append(out, v)
	}
	return out, nil
}

func (m *RepoMock) SetMemberRole(_ context.Context, channelID, userID uuid.UUID, role string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memberKey{channelID, userID}
	mem, ok := m.members[k]
	if !ok {
		return chat.ErrMemberNotFound
	}
	mem.Role = role
	m.members[k] = mem
	return nil
}

func (m *RepoMock) SetMemberBan(_ context.Context, channelID, userID, bannedBy uuid.UUID, until time.Time, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memberKey{channelID, userID}
	mem, ok := m.members[k]
	if !ok {
		mem = chat.ChannelMember{ChannelID: channelID, UserID: userID, Role: "member", JoinedAt: time.Now()}
	}
	mem.BannedUntil = until
	mem.BannedBy = bannedBy
	mem.BannedReason = reason
	m.members[k] = mem
	return nil
}

func (m *RepoMock) ClearMemberBan(_ context.Context, channelID, userID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := memberKey{channelID, userID}
	mem, ok := m.members[k]
	if !ok {
		return chat.ErrMemberNotFound
	}
	mem.BannedUntil = time.Time{}
	mem.BannedBy = uuid.Nil
	mem.BannedReason = ""
	m.members[k] = mem
	return nil
}

// --- Mutes ---

func (m *RepoMock) UpsertMute(_ context.Context, mu chat.Mute) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mu.CreatedAt.IsZero() {
		mu.CreatedAt = time.Now()
	}
	m.mutes[muteKey{mu.UserID, mu.ChannelID}] = mu
	return nil
}

func (m *RepoMock) DeleteMute(_ context.Context, userID, channelID uuid.UUID) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := muteKey{userID, channelID}
	if _, ok := m.mutes[k]; !ok {
		return chat.ErrMuteNotFound
	}
	delete(m.mutes, k)
	return nil
}

func (m *RepoMock) ListActiveMutes(_ context.Context) ([]chat.Mute, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	var out []chat.Mute
	for _, v := range m.mutes {
		if v.ExpiresAt.After(now) {
			out = append(out, v)
		}
	}
	return out, nil
}

// --- Reaper ---

func (m *RepoMock) DeleteExpiredMutes(_ context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	n := 0
	for k, v := range m.mutes {
		if !v.ExpiresAt.After(now) {
			delete(m.mutes, k)
			n++
		}
	}
	return n, nil
}

func (m *RepoMock) ClearExpiredBans(_ context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	n := 0
	for k, v := range m.members {
		if !v.BannedUntil.IsZero() && !v.BannedUntil.After(now) {
			v.BannedUntil = time.Time{}
			v.BannedBy = uuid.Nil
			v.BannedReason = ""
			m.members[k] = v
			n++
		}
	}
	return n, nil
}

// errors-wrapper passthroughs for symmetry with the postgres impl
var _ = errors.New
