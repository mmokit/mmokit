package universe

import (
	"bytes"
	"sync/atomic"
	"testing"
	"time"
)

func TestLoopbackBridge_DeliversBorderFrame_ZeroLatency(t *testing.T) {
	lb := NewLoopbackBridge(LoopbackOpts{})

	var got []byte
	lb.SetReceiver("cell_B", func(msg CellMessage) {
		got = msg.BorderFrame
	})

	payload := []byte{0x01, 0x02, 0x03}
	lb.Send("cell_A", "cell_B", CellMessage{
		Type:        MsgBorderFrame,
		FromCellID:  "cell_A",
		BorderFrame: payload,
	})

	// Zero latency = synchronous delivery; no sleep needed.
	if !bytes.Equal(got, payload) {
		t.Fatalf("payload mismatch: got %x want %x", got, payload)
	}
}

func TestLoopbackBridge_DeepCopiesBorderFrame(t *testing.T) {
	// The sender may reuse its buffer after Send returns. LoopbackBridge
	// must deep-copy the BorderFrame bytes so the receiver sees stable data.
	lb := NewLoopbackBridge(LoopbackOpts{})

	var received CellMessage
	lb.SetReceiver("cell_B", func(msg CellMessage) {
		received = msg
	})

	payload := []byte{0x01, 0x02, 0x03}
	lb.Send("cell_A", "cell_B", CellMessage{
		Type:        MsgBorderFrame,
		BorderFrame: payload,
	})

	// Mutate the sender's buffer after Send returns.
	payload[0] = 0xFF

	if received.BorderFrame[0] != 0x01 {
		t.Fatalf("BorderFrame not deep-copied: got %x, want first byte 0x01", received.BorderFrame)
	}
}

func TestLoopbackBridge_Latency(t *testing.T) {
	lb := NewLoopbackBridge(LoopbackOpts{LatencyMs: 10})

	done := make(chan time.Time, 1)
	lb.SetReceiver("cell_B", func(msg CellMessage) {
		done <- time.Now()
	})

	start := time.Now()
	lb.Send("cell_A", "cell_B", CellMessage{Type: MsgBorderFrame})

	select {
	case deliveredAt := <-done:
		elapsed := deliveredAt.Sub(start)
		if elapsed < 8*time.Millisecond {
			t.Fatalf("latency too short: %v (expected ~10ms)", elapsed)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("message not delivered within 100ms")
	}
}

func TestLoopbackBridge_LossRateOneHundredPercent(t *testing.T) {
	lb := NewLoopbackBridge(LoopbackOpts{LossRate: 1.0})

	var delivered int64
	lb.SetReceiver("cell_B", func(msg CellMessage) {
		atomic.AddInt64(&delivered, 1)
	})

	for i := 0; i < 20; i++ {
		lb.Send("cell_A", "cell_B", CellMessage{Type: MsgBorderFrame})
	}

	// Zero latency so if any would be delivered, they'd be synchronous.
	if n := atomic.LoadInt64(&delivered); n != 0 {
		t.Fatalf("100%% loss rate delivered %d messages; expected 0", n)
	}
}

func TestLoopbackBridge_DroppedWhenReceiverUnknown(t *testing.T) {
	lb := NewLoopbackBridge(LoopbackOpts{})
	// No receiver for "cell_nowhere". Send must not panic; message is dropped.
	lb.Send("cell_A", "cell_nowhere", CellMessage{Type: MsgBorderFrame})
	// Nothing to assert other than "didn't panic"; pass if we reach here.
}

func TestLoopbackBridge_RoutesToCorrectReceiver(t *testing.T) {
	lb := NewLoopbackBridge(LoopbackOpts{})

	var gotA, gotB CellMessage
	lb.SetReceiver("cell_A", func(msg CellMessage) { gotA = msg })
	lb.SetReceiver("cell_B", func(msg CellMessage) { gotB = msg })

	lb.Send("cell_X", "cell_A", CellMessage{Type: MsgBorderFrame, FromCellID: "cell_X"})
	lb.Send("cell_X", "cell_B", CellMessage{Type: MsgHandoffCommit, FromCellID: "cell_X"})

	if gotA.Type != MsgBorderFrame || gotA.FromCellID != "cell_X" {
		t.Fatalf("cell_A got wrong message: %+v", gotA)
	}
	if gotB.Type != MsgHandoffCommit || gotB.FromCellID != "cell_X" {
		t.Fatalf("cell_B got wrong message: %+v", gotB)
	}
}

func TestLoopbackBridge_DeterministicLossWithSeed(t *testing.T) {
	// Two bridges with the same seed and same loss rate should drop the
	// same messages. This lets tests reproduce loss patterns.
	lb1 := NewLoopbackBridge(LoopbackOpts{LossRate: 0.5, Seed: 42})
	lb2 := NewLoopbackBridge(LoopbackOpts{LossRate: 0.5, Seed: 42})

	var count1, count2 int
	lb1.SetReceiver("B", func(msg CellMessage) { count1++ })
	lb2.SetReceiver("B", func(msg CellMessage) { count2++ })

	for i := 0; i < 50; i++ {
		lb1.Send("A", "B", CellMessage{Type: MsgBorderFrame})
		lb2.Send("A", "B", CellMessage{Type: MsgBorderFrame})
	}

	if count1 != count2 {
		t.Fatalf("deterministic loss did not reproduce: %d vs %d", count1, count2)
	}
}
