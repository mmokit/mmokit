//go:build pgtest

package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	persistpg "github.com/zenion/mmokit/pkg/persist/postgres"
	"github.com/zenion/mmokit/pkg/services/chat"
	chatpg "github.com/zenion/mmokit/pkg/services/chat/postgres"
)

// openTestRepo opens a chat.Repository backed by the test Postgres instance.
// The chat migrations are applied via WithExtraMigrations. The chat.*
// tables are truncated on every call so each test starts with a clean slate.
func openTestRepo(t *testing.T) (chat.Repository, func()) {
	t.Helper()
	url := os.Getenv("POSTGRES_URL")
	if url == "" {
		t.Skip("POSTGRES_URL not set; skipping Postgres integration test")
	}
	ctx := context.Background()
	store, err := persistpg.Open(ctx, url,
		persistpg.WithExtraMigrations(chat.MigrationsFS(), ".", "chat"),
	)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	pool := store.Pool()
	if _, err := pool.Exec(ctx, `TRUNCATE chat.channels, chat.channel_members, chat.mutes`); err != nil {
		t.Fatalf("truncate chat tables: %v", err)
	}
	return chatpg.New(pool), store.Close
}

func TestPgRepo_UpsertChannel_Idempotent(t *testing.T) {
	repo, cleanup := openTestRepo(t)
	defer cleanup()
	ctx := context.Background()
	c1, err := repo.UpsertChannel(ctx, chat.Channel{Slug: "world", Kind: "system_all", Topic: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	c2, err := repo.UpsertChannel(ctx, chat.Channel{Slug: "world", Kind: "system_all", Topic: "v2"})
	if err != nil {
		t.Fatal(err)
	}
	if c1.ChannelID != c2.ChannelID {
		t.Fatal("UPSERT changed channel_id")
	}
	if c2.Topic != "v2" {
		t.Fatalf("topic=%q want v2", c2.Topic)
	}
}

func TestPgRepo_BulkSetMembers_Replaces(t *testing.T) {
	repo, cleanup := openTestRepo(t)
	defer cleanup()
	ctx := context.Background()
	c, _ := repo.UpsertChannel(ctx, chat.Channel{Slug: "guild:1", Kind: "system_predicate"})
	a, b := uuid.New(), uuid.New()
	if err := repo.BulkSetMembers(ctx, c.ChannelID, []uuid.UUID{a, b}, "member"); err != nil {
		t.Fatal(err)
	}
	if err := repo.BulkSetMembers(ctx, c.ChannelID, []uuid.UUID{a}, "member"); err != nil {
		t.Fatal(err)
	}
	mems, _ := repo.ListMembers(ctx, c.ChannelID)
	if len(mems) != 1 {
		t.Fatalf("got %d, want 1", len(mems))
	}
}

func TestPgRepo_UpdateChannelRejectsSlugCollision(t *testing.T) {
	repo, cleanup := openTestRepo(t)
	defer cleanup()
	ctx := context.Background()
	a, _ := repo.UpsertChannel(ctx, chat.Channel{Slug: "alpha", Kind: "custom"})
	_, _ = repo.UpsertChannel(ctx, chat.Channel{Slug: "beta", Kind: "custom"})
	a.Slug = "beta"
	if err := repo.UpdateChannel(ctx, a); !errors.Is(err, chat.ErrChannelSlugInUse) {
		t.Fatalf("expected ErrChannelSlugInUse, got %v", err)
	}
}

func TestPgRepo_DeleteExpiredMutes(t *testing.T) {
	repo, cleanup := openTestRepo(t)
	defer cleanup()
	ctx := context.Background()
	_ = repo.UpsertMute(ctx, chat.Mute{UserID: uuid.New(), ChannelID: chat.MuteGlobalChannelID, ExpiresAt: time.Now().Add(-time.Minute), MutedBy: uuid.New()})
	_ = repo.UpsertMute(ctx, chat.Mute{UserID: uuid.New(), ChannelID: chat.MuteGlobalChannelID, ExpiresAt: time.Now().Add(time.Hour), MutedBy: uuid.New()})
	n, err := repo.DeleteExpiredMutes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("reaped %d, want 1", n)
	}
}
