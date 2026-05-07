package chat_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/zenion/mmoserver/pkg/services/auth/authtest"
	"github.com/zenion/mmoserver/pkg/services/chat"
	"github.com/zenion/mmoserver/pkg/services/chat/chattest"
)

// TestTokenBucket_TakeAndRefill exercises the Service-level rate-limit
// helper. DefaultServiceOpts caps at UserRateMax=5 / UserRateWindow=5s,
// so 5 takes succeed, 6th fails with non-zero retry-after.
func TestTokenBucket_TakeAndRefill(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, nil)
	uid := uuid.New()
	for i := 0; i < 5; i++ {
		ok, _ := svc.RateLimitTake(uid)
		if !ok {
			t.Fatalf("attempt %d: expected ok", i)
		}
	}
	ok, retry := svc.RateLimitTake(uid)
	if ok {
		t.Fatal("expected rate limit to deny on 6th take")
	}
	if retry == 0 {
		t.Fatal("expected non-zero retryAfter")
	}
}

// TestSlowMode_BlocksWhenWithinGap pins a custom channel's slow_mode at
// 5s, sends once (passes), sends again immediately (blocked with non-zero
// retry).
func TestSlowMode_BlocksWhenWithinGap(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, nil)
	chid, err := svc.CreateChannelDirect("test-slow", chat.ChannelKindCustom)
	if err != nil {
		t.Fatal(err)
	}
	svc.SetChannelSlowMode(chid, 5)
	uid := uuid.New()
	if ok, _ := svc.SlowModeCheck(chid, uid); !ok {
		t.Fatal("first send should pass")
	}
	ok, retry := svc.SlowModeCheck(chid, uid)
	if ok {
		t.Fatal("second send within slow-mode gap should be blocked")
	}
	if retry == 0 {
		t.Fatal("expected non-zero retry")
	}
}
