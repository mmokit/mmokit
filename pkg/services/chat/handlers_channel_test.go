package chat_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/zenion/mmokit/pkg/ops"
	"github.com/zenion/mmokit/pkg/services/auth"
	"github.com/zenion/mmokit/pkg/services/auth/authtest"
	"github.com/zenion/mmokit/pkg/services/chat"
	"github.com/zenion/mmokit/pkg/services/chat/chattest"
)

// --- HandleRenameChannel ---

func TestHandleRenameChannel_HappyPath(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, nil)

	// Create a custom channel via HandleCreate so the caller is the admin.
	aliceUID := svc.MustOnlineFakeUser(101, "alice")
	createResp, err := svc.HandleCreate(&ops.OpContext{ConnID: 101}, &chat.ChatCreateRequest{
		Slug:  "cool",
		Topic: "Cool channel",
	})
	if err != nil {
		t.Fatal(err)
	}
	if createResp.ErrorCode != 0 {
		t.Fatalf("create err: %s", createResp.ErrorMessage)
	}
	chid, err := uuid.Parse(createResp.Channel.ChannelID)
	if err != nil {
		t.Fatal(err)
	}
	_ = aliceUID

	var sent []recordedEvent
	svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

	resp, err := svc.HandleRenameChannel(&ops.OpContext{ConnID: 101}, &chat.ChatRenameChannelRequest{
		ChannelID: chid.String(),
		NewSlug:   "cooler",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}
	if resp.Channel.Slug != "cooler" {
		t.Fatalf("got slug=%q want cooler", resp.Channel.Slug)
	}

	// bySlug map updated: old gone, new resolves.
	if got := svc.MustChannelID("cooler"); got != chid {
		t.Fatalf("bySlug[cooler] = %s want %s", got, chid)
	}
	// Old slug should no longer resolve.
	defer func() { _ = recover() }()
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Fatal("bySlug[cool] should panic (old slug removed)")
			}
		}()
		svc.MustChannelID("cool")
	}()

	// Stored row reflects new slug.
	stored, _ := chatRepo.GetChannelByID(context.Background(), chid)
	if stored.Slug != "cooler" {
		t.Fatalf("stored slug=%q want cooler", stored.Slug)
	}

	// Fanout: subscriber (alice) receives ChatChannelUpdatedEvent.
	gotUpdated := false
	for _, r := range sent {
		ev, ok := r.Event.(*chat.ChatChannelUpdatedEvent)
		if !ok {
			continue
		}
		if r.ConnID == 101 && ev.Channel.Slug == "cooler" {
			gotUpdated = true
		}
	}
	if !gotUpdated {
		t.Fatal("expected ChatChannelUpdatedEvent to subscriber")
	}
}

func TestHandleRenameChannel_PermissionDeniedForNonAdmin(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "guild:alpha", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("guild:alpha")

	_ = svc.MustOnlineFakeUser(101, "alice") // not a channel admin, no chat.admin

	resp, err := svc.HandleRenameChannel(&ops.OpContext{ConnID: 101}, &chat.ChatRenameChannelRequest{
		ChannelID: chid.String(),
		NewSlug:   "guild:beta",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorPermissionDenied) {
		t.Fatalf("got code=%d want PermissionDenied", resp.ErrorCode)
	}
}

func TestHandleRenameChannel_SlugCollisionReturnsSlugInUse(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, nil)

	aliceUID := svc.MustOnlineFakeUser(101, "alice")
	if err := authRepo.GrantCapability(context.Background(), auth.Capability{
		UserID: aliceUID, Capability: "chat.admin",
	}); err != nil {
		t.Fatalf("GrantCapability: %v", err)
	}

	// Create two custom channels.
	a, err := svc.HandleCreate(&ops.OpContext{ConnID: 101}, &chat.ChatCreateRequest{Slug: "alpha"})
	if err != nil || a.ErrorCode != 0 {
		t.Fatalf("create alpha: %v %s", err, a.ErrorMessage)
	}
	b, err := svc.HandleCreate(&ops.OpContext{ConnID: 101}, &chat.ChatCreateRequest{Slug: "beta"})
	if err != nil || b.ErrorCode != 0 {
		t.Fatalf("create beta: %v %s", err, b.ErrorMessage)
	}

	// Rename beta → alpha (collision).
	resp, err := svc.HandleRenameChannel(&ops.OpContext{ConnID: 101}, &chat.ChatRenameChannelRequest{
		ChannelID: b.Channel.ChannelID,
		NewSlug:   "alpha",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorSlugInUse) {
		t.Fatalf("got code=%d want SlugInUse", resp.ErrorCode)
	}
}

func TestHandleRenameChannel_BadFormatReturnsReservedSlug(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, nil)

	aliceUID := svc.MustOnlineFakeUser(101, "alice")
	create, err := svc.HandleCreate(&ops.OpContext{ConnID: 101}, &chat.ChatCreateRequest{Slug: "cool"})
	if err != nil || create.ErrorCode != 0 {
		t.Fatalf("create: %v %s", err, create.ErrorMessage)
	}
	_ = aliceUID

	// Custom channel with ":" in new slug → invalid.
	resp, err := svc.HandleRenameChannel(&ops.OpContext{ConnID: 101}, &chat.ChatRenameChannelRequest{
		ChannelID: create.Channel.ChannelID,
		NewSlug:   "Foo:Bar",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorReservedSlug) {
		t.Fatalf("got code=%d want ReservedSlug", resp.ErrorCode)
	}
}

// --- HandleSetTopic ---

func TestHandleSetTopic_HappyPath(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, nil)

	_ = svc.MustOnlineFakeUser(101, "alice")
	create, err := svc.HandleCreate(&ops.OpContext{ConnID: 101}, &chat.ChatCreateRequest{
		Slug: "cool", Topic: "old topic",
	})
	if err != nil || create.ErrorCode != 0 {
		t.Fatalf("create: %v %s", err, create.ErrorMessage)
	}
	chid, _ := uuid.Parse(create.Channel.ChannelID)

	var sent []recordedEvent
	svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

	resp, err := svc.HandleSetTopic(&ops.OpContext{ConnID: 101}, &chat.ChatSetTopicRequest{
		ChannelID: chid.String(),
		Topic:     "new topic",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}

	stored, _ := chatRepo.GetChannelByID(context.Background(), chid)
	if stored.Topic != "new topic" {
		t.Fatalf("stored topic=%q want %q", stored.Topic, "new topic")
	}

	gotUpdated := false
	for _, r := range sent {
		ev, ok := r.Event.(*chat.ChatChannelUpdatedEvent)
		if !ok {
			continue
		}
		if r.ConnID == 101 && ev.Channel.Topic == "new topic" {
			gotUpdated = true
		}
	}
	if !gotUpdated {
		t.Fatal("expected ChatChannelUpdatedEvent with new topic")
	}
}

func TestHandleSetTopic_TooLongReturnsPayloadTooLarge(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, nil)

	_ = svc.MustOnlineFakeUser(101, "alice")
	create, err := svc.HandleCreate(&ops.OpContext{ConnID: 101}, &chat.ChatCreateRequest{Slug: "cool"})
	if err != nil || create.ErrorCode != 0 {
		t.Fatalf("create: %v %s", err, create.ErrorMessage)
	}

	// MaxTopicLen default is 200.
	long := make([]byte, 500)
	for i := range long {
		long[i] = 'x'
	}
	resp, err := svc.HandleSetTopic(&ops.OpContext{ConnID: 101}, &chat.ChatSetTopicRequest{
		ChannelID: create.Channel.ChannelID,
		Topic:     string(long),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorPayloadTooLarge) {
		t.Fatalf("got code=%d want PayloadTooLarge", resp.ErrorCode)
	}
}

func TestHandleSetTopic_PermissionDeniedForNonAdmin(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "guild:alpha", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("guild:alpha")

	_ = svc.MustOnlineFakeUser(101, "alice")

	resp, err := svc.HandleSetTopic(&ops.OpContext{ConnID: 101}, &chat.ChatSetTopicRequest{
		ChannelID: chid.String(),
		Topic:     "new",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorPermissionDenied) {
		t.Fatalf("got code=%d want PermissionDenied", resp.ErrorCode)
	}
}

// --- HandleSetSlowMode ---

func TestHandleSetSlowMode_HappyPath(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, nil)

	_ = svc.MustOnlineFakeUser(101, "alice")
	create, err := svc.HandleCreate(&ops.OpContext{ConnID: 101}, &chat.ChatCreateRequest{Slug: "cool"})
	if err != nil || create.ErrorCode != 0 {
		t.Fatalf("create: %v %s", err, create.ErrorMessage)
	}
	chid, _ := uuid.Parse(create.Channel.ChannelID)

	var sent []recordedEvent
	svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

	resp, err := svc.HandleSetSlowMode(&ops.OpContext{ConnID: 101}, &chat.ChatSetSlowModeRequest{
		ChannelID: chid.String(),
		Seconds:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}

	stored, _ := chatRepo.GetChannelByID(context.Background(), chid)
	if stored.SlowModeSeconds != 10 {
		t.Fatalf("stored slow_mode=%d want 10", stored.SlowModeSeconds)
	}

	gotUpdated := false
	for _, r := range sent {
		ev, ok := r.Event.(*chat.ChatChannelUpdatedEvent)
		if !ok {
			continue
		}
		if r.ConnID == 101 && ev.Channel.SlowModeSeconds == 10 {
			gotUpdated = true
		}
	}
	if !gotUpdated {
		t.Fatal("expected ChatChannelUpdatedEvent with slow_mode=10")
	}
}

func TestHandleSetSlowMode_NegativeClampsToZero(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, nil)

	_ = svc.MustOnlineFakeUser(101, "alice")
	create, err := svc.HandleCreate(&ops.OpContext{ConnID: 101}, &chat.ChatCreateRequest{Slug: "cool"})
	if err != nil || create.ErrorCode != 0 {
		t.Fatalf("create: %v %s", err, create.ErrorMessage)
	}
	chid, _ := uuid.Parse(create.Channel.ChannelID)

	resp, err := svc.HandleSetSlowMode(&ops.OpContext{ConnID: 101}, &chat.ChatSetSlowModeRequest{
		ChannelID: chid.String(),
		Seconds:   -5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}
	stored, _ := chatRepo.GetChannelByID(context.Background(), chid)
	if stored.SlowModeSeconds != 0 {
		t.Fatalf("stored slow_mode=%d want 0", stored.SlowModeSeconds)
	}
}

func TestHandleSetSlowMode_OverThresholdClampsTo3600(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, nil)

	_ = svc.MustOnlineFakeUser(101, "alice")
	create, err := svc.HandleCreate(&ops.OpContext{ConnID: 101}, &chat.ChatCreateRequest{Slug: "cool"})
	if err != nil || create.ErrorCode != 0 {
		t.Fatalf("create: %v %s", err, create.ErrorMessage)
	}
	chid, _ := uuid.Parse(create.Channel.ChannelID)

	resp, err := svc.HandleSetSlowMode(&ops.OpContext{ConnID: 101}, &chat.ChatSetSlowModeRequest{
		ChannelID: chid.String(),
		Seconds:   10000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}
	stored, _ := chatRepo.GetChannelByID(context.Background(), chid)
	if stored.SlowModeSeconds != 3600 {
		t.Fatalf("stored slow_mode=%d want 3600", stored.SlowModeSeconds)
	}
}

func TestHandleSetSlowMode_PermissionDeniedForNonAdmin(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "guild:alpha", Kind: chat.ChannelKindSystemPredicate},
	})
	chid := svc.MustChannelID("guild:alpha")

	_ = svc.MustOnlineFakeUser(101, "alice")

	resp, err := svc.HandleSetSlowMode(&ops.OpContext{ConnID: 101}, &chat.ChatSetSlowModeRequest{
		ChannelID: chid.String(),
		Seconds:   30,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorPermissionDenied) {
		t.Fatalf("got code=%d want PermissionDenied", resp.ErrorCode)
	}
}

func TestHandleRenameChannel_RejectsSystemAll(t *testing.T) {
	chatRepo := chattest.NewMock()
	authRepo := authtest.NewMock()
	svc := chat.NewTestServiceWithAuth(t, chatRepo, authRepo, []chat.DefaultChannelDef{
		{Slug: "world", Kind: chat.ChannelKindSystemAll},
	})
	chid := svc.MustChannelID("world")

	adminUID := svc.MustOnlineFakeUser(101, "admin")
	if err := authRepo.GrantCapability(context.Background(), auth.Capability{
		UserID: adminUID, Capability: "chat.admin",
	}); err != nil {
		t.Fatalf("GrantCapability: %v", err)
	}

	resp, err := svc.HandleRenameChannel(&ops.OpContext{ConnID: 101}, &chat.ChatRenameChannelRequest{
		ChannelID: chid.String(),
		NewSlug:   "world2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorPermissionDenied) {
		t.Fatalf("got code=%d want PermissionDenied", resp.ErrorCode)
	}
}
