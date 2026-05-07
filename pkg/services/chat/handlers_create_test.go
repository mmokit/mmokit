package chat_test

import (
	"context"
	"testing"

	"github.com/zenion/mmoserver/pkg/ops"
	"github.com/zenion/mmoserver/pkg/services/chat"
	"github.com/zenion/mmoserver/pkg/services/chat/chattest"
)

func TestHandleCreate_HappyPath(t *testing.T) {
	repo := chattest.NewMock()
	svc := chat.NewTestService(t, repo, nil)
	uid := svc.MustOnlineFakeUser(101, "alice")

	resp, err := svc.HandleCreate(&ops.OpContext{ConnID: 101},
		&chat.ChatCreateRequest{Slug: "cool", Topic: "Cool Hangout"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: code=%d msg=%q", resp.ErrorCode, resp.ErrorMessage)
	}
	if resp.Channel.Slug != "cool" {
		t.Fatalf("got slug=%q want %q", resp.Channel.Slug, "cool")
	}
	if resp.Channel.Kind != chat.ChannelKindCustom {
		t.Fatalf("got kind=%v want custom", resp.Channel.Kind)
	}
	if resp.Channel.OwnerUserID != uid.String() {
		t.Fatalf("got owner=%q want %q", resp.Channel.OwnerUserID, uid.String())
	}
	if resp.Channel.HasPassword {
		t.Fatal("expected HasPassword=false (no password supplied)")
	}

	// Channel persisted via repo
	stored, err := repo.GetChannelBySlug(context.Background(), "cool")
	if err != nil {
		t.Fatalf("repo.GetChannelBySlug: %v", err)
	}
	if stored.OwnerUserID != uid {
		t.Fatalf("stored owner=%v want %v", stored.OwnerUserID, uid)
	}

	// Caller is admin member
	mems, _ := repo.ListMembers(context.Background(), stored.ChannelID)
	if len(mems) != 1 {
		t.Fatalf("got %d members, want 1", len(mems))
	}
	if mems[0].UserID != uid || mems[0].Role != "admin" {
		t.Fatalf("member=%+v, want uid=%v role=admin", mems[0], uid)
	}
}

func TestHandleCreate_RejectsReservedSlug(t *testing.T) {
	svc := chat.NewTestService(t, chattest.NewMock(), nil)
	_ = svc.MustOnlineFakeUser(101, "alice")

	resp, err := svc.HandleCreate(&ops.OpContext{ConnID: 101},
		&chat.ChatCreateRequest{Slug: "guild:foo"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorReservedSlug) {
		t.Fatalf("got error_code=%d, want ChatErrorReservedSlug=%d", resp.ErrorCode, chat.ChatErrorReservedSlug)
	}
}

func TestHandleCreate_RejectsInvalidSlugFormat(t *testing.T) {
	svc := chat.NewTestService(t, chattest.NewMock(), nil)
	_ = svc.MustOnlineFakeUser(101, "alice")

	cases := []string{"UPPERCASE", "has space", "has:colon", "", "way-too-long-slug-that-exceeds-the-32-char-limit"}
	for _, slug := range cases {
		resp, err := svc.HandleCreate(&ops.OpContext{ConnID: 101},
			&chat.ChatCreateRequest{Slug: slug})
		if err != nil {
			t.Fatalf("slug=%q err=%v", slug, err)
		}
		if resp.ErrorCode == 0 {
			t.Fatalf("slug=%q expected error_code != 0", slug)
		}
	}
}

func TestHandleCreate_RejectsBadPasswordLength(t *testing.T) {
	svc := chat.NewTestService(t, chattest.NewMock(), nil)
	_ = svc.MustOnlineFakeUser(101, "alice")

	resp, err := svc.HandleCreate(&ops.OpContext{ConnID: 101},
		&chat.ChatCreateRequest{Slug: "private", Password: "ab"}) // shorter than MinChannelPasswordLen=4
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorInvalidPassword) {
		t.Fatalf("got error_code=%d, want ChatErrorInvalidPassword=%d", resp.ErrorCode, chat.ChatErrorInvalidPassword)
	}
}

func TestHandleCreate_HitsMaxChannelsPerUser(t *testing.T) {
	svc := chat.NewTestService(t, chattest.NewMock(), nil)
	_ = svc.MustOnlineFakeUser(101, "alice")

	// Default MaxChannelsPerUser = 5. Create exactly that many, then assert the next one fails.
	for i := 0; i < 5; i++ {
		slug := []string{"alpha", "beta", "gamma", "delta", "epsilon"}[i]
		resp, err := svc.HandleCreate(&ops.OpContext{ConnID: 101},
			&chat.ChatCreateRequest{Slug: slug})
		if err != nil {
			t.Fatalf("create slug=%q err=%v", slug, err)
		}
		if resp.ErrorCode != 0 {
			t.Fatalf("create slug=%q error_code=%d msg=%q", slug, resp.ErrorCode, resp.ErrorMessage)
		}
	}

	resp, err := svc.HandleCreate(&ops.OpContext{ConnID: 101},
		&chat.ChatCreateRequest{Slug: "zeta"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorMaxChannelsReached) {
		t.Fatalf("got error_code=%d, want ChatErrorMaxChannelsReached=%d", resp.ErrorCode, chat.ChatErrorMaxChannelsReached)
	}
}

func TestHandleCreate_PasswordIsHashed(t *testing.T) {
	repo := chattest.NewMock()
	svc := chat.NewTestService(t, repo, nil)
	_ = svc.MustOnlineFakeUser(101, "alice")

	resp, err := svc.HandleCreate(&ops.OpContext{ConnID: 101},
		&chat.ChatCreateRequest{Slug: "private", Password: "hunter2"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}
	if !resp.Channel.HasPassword {
		t.Fatal("expected HasPassword=true")
	}
	stored, err := repo.GetChannelBySlug(context.Background(), "private")
	if err != nil {
		t.Fatal(err)
	}
	if stored.PasswordHash == "" {
		t.Fatal("expected non-empty password hash")
	}
	if stored.PasswordHash == "hunter2" {
		t.Fatal("password stored in plaintext!")
	}
	ok, err := chat.VerifyChannelPassword("hunter2", stored.PasswordHash)
	if err != nil || !ok {
		t.Fatalf("VerifyChannelPassword(hunter2): ok=%v err=%v", ok, err)
	}
}
