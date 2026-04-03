package system

import (
	"bytes"
	"testing"
)

func TestEntityBaseline_PushAndAdvance(t *testing.T) {
	b := &entityBaseline{
		ring: make([]sentSnapshot, 4),
	}

	// Push 3 snapshots.
	b.pushSent(1, []byte{0x01})
	b.pushSent(2, []byte{0x02})
	b.pushSent(3, []byte{0x03})

	if b.ringLen != 3 {
		t.Fatalf("expected ringLen=3, got %d", b.ringLen)
	}

	// Advance to seq 2 — should promote snapshot {0x02} to acked.
	b.advanceTo(2)
	if !bytes.Equal(b.acked, []byte{0x02}) {
		t.Fatalf("expected acked={0x02}, got %v", b.acked)
	}
	if b.ringLen != 1 {
		t.Fatalf("expected ringLen=1 after advance, got %d", b.ringLen)
	}

	// Advance to seq 3 — should promote {0x03}.
	b.advanceTo(3)
	if !bytes.Equal(b.acked, []byte{0x03}) {
		t.Fatalf("expected acked={0x03}, got %v", b.acked)
	}
	if b.ringLen != 0 {
		t.Fatalf("expected ringLen=0 after advance, got %d", b.ringLen)
	}
}

func TestEntityBaseline_AdvanceSkipsSeq(t *testing.T) {
	b := &entityBaseline{
		ring: make([]sentSnapshot, 4),
	}

	b.pushSent(10, []byte{0x10})
	b.pushSent(11, []byte{0x11})
	b.pushSent(12, []byte{0x12})

	// Advance to seq 12 — should use snapshot from seq 12, pruning all 3.
	b.advanceTo(12)
	if !bytes.Equal(b.acked, []byte{0x12}) {
		t.Fatalf("expected acked={0x12}, got %v", b.acked)
	}
	if b.ringLen != 0 {
		t.Fatalf("expected ringLen=0, got %d", b.ringLen)
	}
}

func TestEntityBaseline_AdvanceFutureSeq(t *testing.T) {
	b := &entityBaseline{
		ring: make([]sentSnapshot, 4),
	}

	b.pushSent(1, []byte{0x01})
	b.pushSent(2, []byte{0x02})

	// Advance to seq 100 — should use best match (seq 2), prune both.
	b.advanceTo(100)
	if !bytes.Equal(b.acked, []byte{0x02}) {
		t.Fatalf("expected acked={0x02}, got %v", b.acked)
	}
	if b.ringLen != 0 {
		t.Fatalf("expected ringLen=0, got %d", b.ringLen)
	}
}

func TestEntityBaseline_RingWraparound(t *testing.T) {
	b := &entityBaseline{
		ring: make([]sentSnapshot, 3),
	}

	// Fill ring and wrap around.
	b.pushSent(1, []byte{0x01})
	b.pushSent(2, []byte{0x02})
	b.pushSent(3, []byte{0x03})
	b.pushSent(4, []byte{0x04}) // overwrites seq 1

	if b.ringLen != 3 {
		t.Fatalf("expected ringLen=3, got %d", b.ringLen)
	}

	// Advance to seq 3 — should prune 2 and 3, leaving only 4.
	b.advanceTo(3)
	if !bytes.Equal(b.acked, []byte{0x03}) {
		t.Fatalf("expected acked={0x03}, got %v", b.acked)
	}
	if b.ringLen != 1 {
		t.Fatalf("expected ringLen=1, got %d", b.ringLen)
	}
}

func TestEntityBaseline_EmptyRing(t *testing.T) {
	b := &entityBaseline{
		ring: make([]sentSnapshot, 4),
	}

	// Advance on empty ring — should be no-op.
	b.advanceTo(5)
	if b.acked != nil {
		t.Fatalf("expected nil acked on empty ring, got %v", b.acked)
	}
}

func TestEntityBaseline_NoRing(t *testing.T) {
	// AckReliable mode: no ring buffer.
	b := &entityBaseline{}

	b.pushSent(1, []byte{0x01}) // should be no-op
	b.advanceTo(1)              // should be no-op
	if b.acked != nil {
		t.Fatalf("expected nil acked with no ring, got %v", b.acked)
	}
}

func TestEntityBaseline_PushCopiesData(t *testing.T) {
	b := &entityBaseline{
		ring: make([]sentSnapshot, 4),
	}

	data := []byte{0x01, 0x02}
	b.pushSent(1, data)

	// Mutate original — ring entry should be unaffected.
	data[0] = 0xFF
	b.advanceTo(1)
	if b.acked[0] != 0x01 {
		t.Fatalf("ring entry was not copied, got %v", b.acked)
	}
}

func TestConnectionState_GetAndRemovePriorityState(t *testing.T) {
	conn := newConnectionState()

	ps := conn.getPriorityState(42)
	if ps == nil {
		t.Fatal("expected non-nil priority state")
	}
	if ps.accumulator != 0 || ps.lastSentTick != 0 || ps.unchangedTicks != 0 {
		t.Fatal("expected zero-value priority state")
	}

	// Get again — should return same instance.
	ps2 := conn.getPriorityState(42)
	if ps2 != ps {
		t.Fatal("expected same priority state instance")
	}

	// Modify and verify.
	ps.accumulator = 1.5
	ps.lastSentTick = 10
	ps.unchangedTicks = 5
	ps3 := conn.getPriorityState(42)
	if ps3.accumulator != 1.5 || ps3.lastSentTick != 10 || ps3.unchangedTicks != 5 {
		t.Fatal("expected modified priority state")
	}

	conn.removePriorityState(42)
	ps4 := conn.getPriorityState(42)
	if ps4 == ps {
		t.Fatal("expected new priority state after remove")
	}
	if ps4.accumulator != 0 {
		t.Fatal("expected zero accumulator after remove")
	}
}

func TestConnectionState_GetAndRemoveBaseline(t *testing.T) {
	conn := newConnectionState()

	bl := conn.getBaseline(42, 4)
	if bl == nil {
		t.Fatal("expected non-nil baseline")
	}
	if len(bl.ring) != 4 {
		t.Fatalf("expected ring depth 4, got %d", len(bl.ring))
	}

	// Get again — should return the same.
	bl2 := conn.getBaseline(42, 4)
	if bl2 != bl {
		t.Fatal("expected same baseline instance")
	}

	conn.removeBaseline(42)
	bl3 := conn.getBaseline(42, 4)
	if bl3 == bl {
		t.Fatal("expected new baseline after remove")
	}
}
