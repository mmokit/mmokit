package mmokit_test

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

type pingMsg struct{ Note string }

func TestHandle_RegistersHandlerInvokedBySend(t *testing.T) {
	stage, _ := newTestStage(t)
	registerTestKind(t, stage)
	e := mmokit.Spawn(stage, testKindID, mmokit.Pos{})

	var got string
	mmokit.Handle[pingMsg](stage, func(target mmokit.Entity, msg *pingMsg) {
		if target.NetID() != e.NetID() {
			t.Fatalf("target.NetID = %d, want %d", target.NetID(), e.NetID())
		}
		got = msg.Note
	})

	e.Send(pingMsg{Note: "hello"})

	if got != "hello" {
		t.Fatalf("handler did not run; got=%q", got)
	}
}

type damageMsg struct {
	Amount float32
	Dealt  float32 // result, mutated by handler
}

func TestSend_HandlerMutatesMessage(t *testing.T) {
	stage, _ := newTestStage(t)
	registerTestKind(t, stage)
	e := mmokit.Spawn(stage, testKindID, mmokit.Pos{})

	mmokit.Handle[damageMsg](stage, func(target mmokit.Entity, msg *damageMsg) {
		msg.Dealt = msg.Amount
	})

	msg := &damageMsg{Amount: 25}
	e.Send(msg)

	if msg.Dealt != 25 {
		t.Fatalf("after Send, msg.Dealt = %v, want 25", msg.Dealt)
	}
}

