package chat_test

import (
	"testing"
	"time"

	"github.com/mmokit/mmokit/pkg/ops"
	"github.com/mmokit/mmokit/pkg/services/auth/authtest"
	"github.com/mmokit/mmokit/pkg/services/chat"
	"github.com/mmokit/mmokit/pkg/services/chat/chattest"
)

// TestHandleSend_RejectsWhenMuted seeds a per-channel mute on alice and
// confirms HandleSend returns ChatErrorMuted with no fanout.
func TestHandleSend_RejectsWhenMuted(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
	})
	chid := svc.MustChannelID("world")
	alice := svc.MustOnlineFakeUser(101, "alice")
	_ = svc.MustOnlineFakeUser(102, "bob")

	svc.MustMute(alice, chid, time.Now().Add(time.Hour))

	var sent []recordedEvent
	svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

	resp, err := svc.HandleSend(&ops.OpContext{ConnID: 101},
		&chat.ChatSendRequest{ChannelID: chid.String(), Body: "muted msg"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorMuted) {
		t.Fatalf("expected ChatErrorMuted (%d), got %d msg=%q",
			chat.ChatErrorMuted, resp.ErrorCode, resp.ErrorMessage)
	}
	if resp.RetryAfterMs == 0 {
		t.Fatal("expected non-zero retry_after_ms for mute")
	}
	if len(sent) != 0 {
		t.Fatalf("expected 0 fanout sends from muted user, got %d", len(sent))
	}
}

// TestHandleSend_RejectsWhenBanned seeds a per-channel ban via the
// MustBanUser helper and confirms HandleSend returns ChatErrorBanned.
func TestHandleSend_RejectsWhenBanned(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
	})
	chid := svc.MustChannelID("world")
	alice := svc.MustOnlineFakeUser(101, "alice")

	svc.MustBanUser(chid, alice, time.Now().Add(time.Hour), "spam")

	var sent []recordedEvent
	svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

	resp, err := svc.HandleSend(&ops.OpContext{ConnID: 101},
		&chat.ChatSendRequest{ChannelID: chid.String(), Body: "banned msg"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorBanned) {
		t.Fatalf("expected ChatErrorBanned (%d), got %d msg=%q",
			chat.ChatErrorBanned, resp.ErrorCode, resp.ErrorMessage)
	}
	if resp.RetryAfterMs == 0 {
		t.Fatal("expected non-zero retry_after_ms for ban")
	}
	if len(sent) != 0 {
		t.Fatalf("expected 0 fanout sends from banned user, got %d", len(sent))
	}
}

// TestHandleSend_RejectsWhenRateLimited drives 5 successful sends (the
// DefaultServiceOpts UserRateMax) followed by a 6th that must reject with
// ChatErrorRateLimited and a non-zero retry_after_ms.
func TestHandleSend_RejectsWhenRateLimited(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
	})
	chid := svc.MustChannelID("world")
	_ = svc.MustOnlineFakeUser(101, "alice")
	svc.SetSendEventFn(func(c uint32, ev any) {})

	for i := 0; i < 5; i++ {
		resp, err := svc.HandleSend(&ops.OpContext{ConnID: 101},
			&chat.ChatSendRequest{ChannelID: chid.String(), Body: "ok"})
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if resp.ErrorCode != 0 {
			t.Fatalf("attempt %d: unexpected error %d msg=%q",
				i, resp.ErrorCode, resp.ErrorMessage)
		}
	}
	resp, err := svc.HandleSend(&ops.OpContext{ConnID: 101},
		&chat.ChatSendRequest{ChannelID: chid.String(), Body: "denied"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorRateLimited) {
		t.Fatalf("expected ChatErrorRateLimited (%d), got %d msg=%q",
			chat.ChatErrorRateLimited, resp.ErrorCode, resp.ErrorMessage)
	}
	if resp.RetryAfterMs == 0 {
		t.Fatal("expected non-zero retry_after_ms for rate limit")
	}
}

// TestHandleSend_RejectsWhenSlowMode pins the channel to 5s slow-mode,
// sends once (passes), sends again immediately and expects rejection
// with ChatErrorSlowMode + retry_after_ms ≈ 5000.
func TestHandleSend_RejectsWhenSlowMode(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, nil)
	chid, err := svc.CreateChannelDirect("slow-test", chat.ChannelKindCustom)
	if err != nil {
		t.Fatal(err)
	}
	svc.SetChannelSlowMode(chid, 5)
	alice := svc.MustOnlineFakeUser(101, "alice")
	svc.MustAddMember(chid, alice, "member")
	svc.MustSubscribe(chid, 101)
	svc.SetSendEventFn(func(c uint32, ev any) {})

	resp1, _ := svc.HandleSend(&ops.OpContext{ConnID: 101},
		&chat.ChatSendRequest{ChannelID: chid.String(), Body: "first"})
	if resp1.ErrorCode != 0 {
		t.Fatalf("first send should pass, got %d msg=%q", resp1.ErrorCode, resp1.ErrorMessage)
	}

	resp2, _ := svc.HandleSend(&ops.OpContext{ConnID: 101},
		&chat.ChatSendRequest{ChannelID: chid.String(), Body: "second"})
	if resp2.ErrorCode != uint32(chat.ChatErrorSlowMode) {
		t.Fatalf("expected ChatErrorSlowMode (%d), got %d msg=%q",
			chat.ChatErrorSlowMode, resp2.ErrorCode, resp2.ErrorMessage)
	}
	if resp2.RetryAfterMs <= 0 {
		t.Fatalf("expected positive retry_after_ms, got %d", resp2.RetryAfterMs)
	}
	// Slow-mode is 5s; retry should be ≈ 5000 ms (allow drift for elapsed
	// real time between the two calls).
	if resp2.RetryAfterMs > 5000 {
		t.Fatalf("retry_after_ms=%d should not exceed slow-mode gap (5000)", resp2.RetryAfterMs)
	}
}

// TestHandleSendDM_RejectsWhenMuted seeds a global mute and confirms
// HandleSendDM returns ChatErrorMuted.
func TestHandleSendDM_RejectsWhenMuted(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, nil)
	alice := svc.MustOnlineFakeUser(101, "alice")
	bob := svc.MustOnlineFakeUser(102, "bob")

	svc.MustMute(alice, chat.MuteGlobalChannelID, time.Now().Add(time.Hour))

	var sent []recordedEvent
	svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

	resp, err := svc.HandleSendDM(&ops.OpContext{ConnID: 101},
		&chat.ChatSendDMRequest{RecipientUserID: bob.String(), Body: "muted dm"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorMuted) {
		t.Fatalf("expected ChatErrorMuted (%d), got %d msg=%q",
			chat.ChatErrorMuted, resp.ErrorCode, resp.ErrorMessage)
	}
	if resp.RetryAfterMs == 0 {
		t.Fatal("expected non-zero retry_after_ms for global mute")
	}
	if len(sent) != 0 {
		t.Fatalf("expected 0 fanout sends from globally-muted user, got %d", len(sent))
	}
}

// TestHandleSendDM_RejectsWhenRateLimited drives 5 successful DMs
// (DefaultServiceOpts UserRateMax) and confirms the 6th rejects with
// ChatErrorRateLimited.
func TestHandleSendDM_RejectsWhenRateLimited(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, nil)
	_ = svc.MustOnlineFakeUser(101, "alice")
	bob := svc.MustOnlineFakeUser(102, "bob")
	svc.SetSendEventFn(func(c uint32, ev any) {})

	for i := 0; i < 5; i++ {
		resp, err := svc.HandleSendDM(&ops.OpContext{ConnID: 101},
			&chat.ChatSendDMRequest{RecipientUserID: bob.String(), Body: "ok"})
		if err != nil {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if resp.ErrorCode != 0 {
			t.Fatalf("attempt %d: unexpected error %d msg=%q",
				i, resp.ErrorCode, resp.ErrorMessage)
		}
	}
	resp, err := svc.HandleSendDM(&ops.OpContext{ConnID: 101},
		&chat.ChatSendDMRequest{RecipientUserID: bob.String(), Body: "denied"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorRateLimited) {
		t.Fatalf("expected ChatErrorRateLimited (%d), got %d msg=%q",
			chat.ChatErrorRateLimited, resp.ErrorCode, resp.ErrorMessage)
	}
	if resp.RetryAfterMs == 0 {
		t.Fatal("expected non-zero retry_after_ms for DM rate limit")
	}
}
