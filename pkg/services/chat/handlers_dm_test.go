package chat_test

import (
	"testing"

	"github.com/google/uuid"

	"github.com/mmokit/mmokit/pkg/ops"
	"github.com/mmokit/mmokit/pkg/services/chat"
	"github.com/mmokit/mmokit/pkg/services/chat/chattest"
)

func TestHandleSendDM_DeliversToOnlineRecipientAndEchoesToSender(t *testing.T) {
	svc := chat.NewTestService(t, chattest.NewMock(), nil)
	alice := svc.MustOnlineFakeUser(101, "alice")
	bob := svc.MustOnlineFakeUser(102, "bob")

	var sent []recordedEvent
	svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

	resp, err := svc.HandleSendDM(&ops.OpContext{ConnID: 101},
		&chat.ChatSendDMRequest{RecipientUserID: bob.String(), Body: "hi bob"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}
	if resp.MsgID == "" {
		t.Fatal("expected msg_id")
	}
	if len(sent) != 2 {
		t.Fatalf("expected 2 sends (recipient + echo), got %d", len(sent))
	}
	// Validate event content + that both went to the right conns.
	gotConns := map[uint32]bool{}
	for _, r := range sent {
		ev, ok := r.Event.(*chat.ChatDMEvent)
		if !ok {
			t.Fatalf("expected *chat.ChatDMEvent, got %T", r.Event)
		}
		if ev.SenderUserID != alice.String() {
			t.Fatalf("sender_user_id=%q want %q", ev.SenderUserID, alice.String())
		}
		if ev.Body != "hi bob" {
			t.Fatalf("body=%q want %q", ev.Body, "hi bob")
		}
		gotConns[r.ConnID] = true
	}
	if !gotConns[101] || !gotConns[102] {
		t.Fatalf("expected sends to conns 101 (sender echo) and 102 (recipient); got %v", gotConns)
	}
}

func TestHandleSendDM_DropsSilentlyWhenRecipientOffline(t *testing.T) {
	svc := chat.NewTestService(t, chattest.NewMock(), nil)
	_ = svc.MustOnlineFakeUser(101, "alice")
	offline := uuid.New()

	var sent []recordedEvent
	svc.SetSendEventFn(func(c uint32, ev any) { sent = append(sent, recordedEvent{c, ev}) })

	resp, err := svc.HandleSendDM(&ops.OpContext{ConnID: 101},
		&chat.ChatSendDMRequest{RecipientUserID: offline.String(), Body: "into the void"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != 0 {
		t.Fatalf("err: %s", resp.ErrorMessage)
	}
	if resp.MsgID == "" {
		t.Fatal("expected msg_id even on silent drop")
	}
	if len(sent) != 0 {
		t.Fatalf("expected 0 sends to offline recipient (silent drop), got %d", len(sent))
	}
}

func TestHandleSendDM_RejectsBodyTooLarge(t *testing.T) {
	svc := chat.NewTestService(t, chattest.NewMock(), nil)
	_ = svc.MustOnlineFakeUser(101, "alice")
	bob := svc.MustOnlineFakeUser(102, "bob")

	// Default MaxMessageLen=500 → push 600 bytes
	body := make([]byte, 600)
	for i := range body {
		body[i] = 'x'
	}
	resp, err := svc.HandleSendDM(&ops.OpContext{ConnID: 101},
		&chat.ChatSendDMRequest{RecipientUserID: bob.String(), Body: string(body)})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode != uint32(chat.ChatErrorPayloadTooLarge) {
		t.Fatalf("got code=%d want ChatErrorPayloadTooLarge=%d", resp.ErrorCode, chat.ChatErrorPayloadTooLarge)
	}
}

func TestHandleSendDM_InvalidRecipientID(t *testing.T) {
	svc := chat.NewTestService(t, chattest.NewMock(), nil)
	_ = svc.MustOnlineFakeUser(101, "alice")

	resp, err := svc.HandleSendDM(&ops.OpContext{ConnID: 101},
		&chat.ChatSendDMRequest{RecipientUserID: "not-a-uuid", Body: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ErrorCode == 0 {
		t.Fatal("expected non-zero error_code for invalid recipient_user_id")
	}
}
