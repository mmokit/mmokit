package chattest_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/zenion/mmokit/pkg/services/chat"
	"github.com/zenion/mmokit/pkg/services/chat/chattest"
)

func TestRepoMock_UpsertAndGetChannel(t *testing.T) {
	m := chattest.NewMock()
	ctx := context.Background()
	c, err := m.UpsertChannel(ctx, chat.Channel{Slug: "world", Kind: "system_all"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.GetChannelBySlug(ctx, "world")
	if err != nil {
		t.Fatal(err)
	}
	if got.ChannelID != c.ChannelID {
		t.Fatal("channel ID mismatch")
	}
}

func TestRepoMock_BulkSetMembersReplacesAll(t *testing.T) {
	m := chattest.NewMock()
	ctx := context.Background()
	c, _ := m.UpsertChannel(ctx, chat.Channel{Slug: "guild:42", Kind: "system_predicate"})
	a, b, x := uuid.New(), uuid.New(), uuid.New()
	_ = m.BulkSetMembers(ctx, c.ChannelID, []uuid.UUID{a, b, x}, "member")
	mems, _ := m.ListMembers(ctx, c.ChannelID)
	if len(mems) != 3 {
		t.Fatalf("got %d members, want 3", len(mems))
	}
	_ = m.BulkSetMembers(ctx, c.ChannelID, []uuid.UUID{a}, "member")
	mems, _ = m.ListMembers(ctx, c.ChannelID)
	if len(mems) != 1 {
		t.Fatalf("got %d after replace, want 1", len(mems))
	}
}

func TestRepoMock_UpdateChannelRejectsSlugCollision(t *testing.T) {
	m := chattest.NewMock()
	ctx := context.Background()
	a, _ := m.UpsertChannel(ctx, chat.Channel{Slug: "alpha", Kind: "custom"})
	_, _ = m.UpsertChannel(ctx, chat.Channel{Slug: "beta", Kind: "custom"})
	a.Slug = "beta"
	if err := m.UpdateChannel(ctx, a); !errors.Is(err, chat.ErrChannelSlugInUse) {
		t.Fatalf("expected ErrChannelSlugInUse, got %v", err)
	}
}

func TestRepoMock_DeleteExpiredMutes(t *testing.T) {
	m := chattest.NewMock()
	ctx := context.Background()
	_ = m.UpsertMute(ctx, chat.Mute{UserID: uuid.New(), ChannelID: chat.MuteGlobalChannelID, ExpiresAt: time.Now().Add(-time.Minute), MutedBy: uuid.New()})
	_ = m.UpsertMute(ctx, chat.Mute{UserID: uuid.New(), ChannelID: chat.MuteGlobalChannelID, ExpiresAt: time.Now().Add(time.Hour), MutedBy: uuid.New()})
	n, _ := m.DeleteExpiredMutes(ctx)
	if n != 1 {
		t.Fatalf("reaped %d, want 1", n)
	}
}
