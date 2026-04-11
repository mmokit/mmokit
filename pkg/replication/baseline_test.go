package replication

import (
	"bytes"
	"testing"
)

func TestBaselineStore_InitialState(t *testing.T) {
	s := NewBaselineStore(AckReliable)
	if s.Mode() != AckReliable {
		t.Fatal("mode mismatch")
	}
	if s.Baseline(42) != nil {
		t.Fatal("fresh store should have no baselines")
	}
	if s.LastHash(42) != 0 {
		t.Fatal("fresh store should have zero hash")
	}
	if s.HasLastHash(42) {
		t.Fatal("fresh store should report HasLastHash=false")
	}
}

// HasLastHash distinguishes "hash never recorded" from "hash recorded as
// zero". The dispatcher's dormancy branch relies on this to avoid
// short-circuiting on a first-sighting entity whose hash happens to be 0.
func TestBaselineStore_HasLastHash(t *testing.T) {
	s := NewBaselineStore(AckReliable)

	// Never recorded.
	if s.HasLastHash(1) {
		t.Fatal("fresh entity should not have a recorded hash")
	}

	// Recorded as zero — still counts as "has".
	s.SetLastHash(1, 0)
	if !s.HasLastHash(1) {
		t.Fatal("entity with explicitly-set zero hash should have HasLastHash=true")
	}
	if s.LastHash(1) != 0 {
		t.Fatalf("LastHash(1) = %d, want 0", s.LastHash(1))
	}

	// Recorded as non-zero.
	s.SetLastHash(2, 0xDEAD)
	if !s.HasLastHash(2) {
		t.Fatal("entity with non-zero hash should have HasLastHash=true")
	}

	// DropBaseline clears the recorded hash.
	s.DropBaseline(1)
	if s.HasLastHash(1) {
		t.Fatal("DropBaseline should clear HasLastHash")
	}
}

func TestBaselineStore_SetAndDrop(t *testing.T) {
	s := NewBaselineStore(AckExplicit)
	b := &EntityBaseline{}
	s.SetBaseline(7, b)
	if s.Baseline(7) != b {
		t.Fatal("baseline not retrievable")
	}
	s.SetLastHash(7, 12345)
	if s.LastHash(7) != 12345 {
		t.Fatal("hash not retrievable")
	}
	s.DropBaseline(7)
	if s.Baseline(7) != nil || s.LastHash(7) != 0 {
		t.Fatal("drop did not clear state")
	}
}

func TestBaselineStore_PriorityAllocatesOnce(t *testing.T) {
	s := NewBaselineStore(AckReliable)
	p1 := s.Priority(5)
	p2 := s.Priority(5)
	if p1 != p2 {
		t.Fatal("Priority should return same pointer on repeat access")
	}
}

func TestBaselineStore_GetOrCreateBaseline(t *testing.T) {
	s := NewBaselineStore(AckExplicit)
	b := s.GetOrCreateBaseline(99, 4)
	if b == nil {
		t.Fatal("expected non-nil baseline")
	}
	if len(b.Ring) != 4 {
		t.Fatalf("expected ring depth 4, got %d", len(b.Ring))
	}
	// Second call returns same pointer.
	b2 := s.GetOrCreateBaseline(99, 0)
	if b2 != b {
		t.Fatal("GetOrCreateBaseline should return same pointer")
	}
}

func TestEntityBaseline_PushSent(t *testing.T) {
	b := &EntityBaseline{Ring: make([]SentSnapshot, 2)}
	b.PushSent(1, []byte{0xAA})
	b.PushSent(2, []byte{0xBB})
	if b.RingLen != 2 {
		t.Fatalf("ring len: got %d want 2", b.RingLen)
	}
}

func TestEntityBaseline_PushSent_HeadWrapAndCopy(t *testing.T) {
	b := &EntityBaseline{Ring: make([]SentSnapshot, 2)}

	// First push.
	payload1 := []byte{0xAA}
	b.PushSent(1, payload1)
	if b.RingHead != 1 {
		t.Fatalf("after 1st push: RingHead=%d, want 1", b.RingHead)
	}
	if b.Ring[0].Seq != 1 || b.Ring[0].Data[0] != 0xAA {
		t.Fatalf("after 1st push: ring[0]=%+v", b.Ring[0])
	}

	// Mutation safety: the push must have copied the caller's slice, not
	// aliased it. Mutate the caller's slice and verify Ring is unaffected.
	payload1[0] = 0xFF
	if b.Ring[0].Data[0] != 0xAA {
		t.Fatalf("push did not copy payload: ring[0].Data[0]=%#x", b.Ring[0].Data[0])
	}

	// Second push fills the ring.
	b.PushSent(2, []byte{0xBB})
	if b.RingLen != 2 {
		t.Fatalf("after 2nd push: RingLen=%d, want 2", b.RingLen)
	}
	if b.RingHead != 0 {
		t.Fatalf("after 2nd push: RingHead=%d, want 0 (wrapped)", b.RingHead)
	}

	// Third push overwrites the oldest slot (seq 1) without growing the ring.
	b.PushSent(3, []byte{0xCC})
	if b.RingLen != 2 {
		t.Fatalf("after 3rd push: RingLen=%d, want 2 (capped)", b.RingLen)
	}
	if b.RingHead != 1 {
		t.Fatalf("after 3rd push: RingHead=%d, want 1", b.RingHead)
	}
	if b.Ring[0].Seq != 3 || b.Ring[0].Data[0] != 0xCC {
		t.Fatalf("after 3rd push: ring[0]=%+v, want seq=3 data=0xCC", b.Ring[0])
	}
	if b.Ring[1].Seq != 2 {
		t.Fatalf("after 3rd push: ring[1].Seq=%d, want 2 (unchanged)", b.Ring[1].Seq)
	}
}

func TestEntityBaseline_AdvanceTo_ExactMatch(t *testing.T) {
	b := &EntityBaseline{Ring: make([]SentSnapshot, 4)}
	b.PushSent(1, []byte{0x01})
	b.PushSent(2, []byte{0x02})
	b.PushSent(3, []byte{0x03})

	// Advance to seq 2. Expect Acked == data(2), ring retains only seq 3.
	b.AdvanceTo(2)
	if len(b.Acked) != 1 || b.Acked[0] != 0x02 {
		t.Fatalf("Acked after AdvanceTo(2): %x, want [02]", b.Acked)
	}
	if b.RingLen != 1 {
		t.Fatalf("RingLen after AdvanceTo(2): %d, want 1", b.RingLen)
	}

	// Next push lands in the correct next slot and the view of remaining
	// data is consistent.
	b.PushSent(4, []byte{0x04})
	if b.RingLen != 2 {
		t.Fatalf("RingLen after next push: %d, want 2", b.RingLen)
	}
}

func TestEntityBaseline_AdvanceTo_BeforeOldest(t *testing.T) {
	b := &EntityBaseline{Ring: make([]SentSnapshot, 4)}
	b.PushSent(5, []byte{0xAA})
	b.PushSent(6, []byte{0xBB})
	priorAcked := b.Acked
	priorLen := b.RingLen

	b.AdvanceTo(3) // seq < oldest

	if !bytes.Equal(b.Acked, priorAcked) {
		t.Fatalf("AdvanceTo(seq<oldest) changed Acked: %x", b.Acked)
	}
	if b.RingLen != priorLen {
		t.Fatalf("AdvanceTo(seq<oldest) pruned ring: RingLen=%d, want %d", b.RingLen, priorLen)
	}
}

func TestEntityBaseline_AdvanceTo_AfterNewest(t *testing.T) {
	b := &EntityBaseline{Ring: make([]SentSnapshot, 4)}
	b.PushSent(1, []byte{0x01})
	b.PushSent(2, []byte{0x02})
	b.PushSent(3, []byte{0x03})

	b.AdvanceTo(100) // well beyond newest

	if len(b.Acked) != 1 || b.Acked[0] != 0x03 {
		t.Fatalf("Acked after AdvanceTo(newest+): %x, want [03]", b.Acked)
	}
	if b.RingLen != 0 {
		t.Fatalf("RingLen after full-prune AdvanceTo: %d, want 0", b.RingLen)
	}

	// Fresh push after full prune works correctly.
	b.PushSent(4, []byte{0x04})
	if b.RingLen != 1 {
		t.Fatalf("RingLen after push into fully-pruned ring: %d, want 1", b.RingLen)
	}
}

func TestEntityBaseline_AdvanceTo_AfterWrap(t *testing.T) {
	// Force the ring head to wrap around, then AdvanceTo and verify
	// the start-index math still finds the correct best match.
	b := &EntityBaseline{Ring: make([]SentSnapshot, 3)}
	b.PushSent(1, []byte{0x01})
	b.PushSent(2, []byte{0x02})
	b.PushSent(3, []byte{0x03})
	b.PushSent(4, []byte{0x04}) // overwrites seq 1
	// Ring now contains seqs 2, 3, 4 in some rotation.

	b.AdvanceTo(3)
	if len(b.Acked) != 1 || b.Acked[0] != 0x03 {
		t.Fatalf("Acked after wrapped AdvanceTo(3): %x, want [03]", b.Acked)
	}
	if b.RingLen != 1 {
		t.Fatalf("RingLen after wrapped AdvanceTo(3): %d, want 1 (only seq 4 remains)", b.RingLen)
	}
}

func TestEntityBaseline_AdvanceTo_EmptyRing(t *testing.T) {
	// AdvanceTo on an empty ring should be a no-op, not panic.
	b := &EntityBaseline{Ring: make([]SentSnapshot, 4)}
	b.AdvanceTo(42)
	if b.Acked != nil {
		t.Fatalf("empty AdvanceTo set Acked: %x", b.Acked)
	}
	if b.RingLen != 0 {
		t.Fatalf("empty AdvanceTo changed RingLen: %d", b.RingLen)
	}

	// Also: zero-length ring.
	b2 := &EntityBaseline{}
	b2.AdvanceTo(42) // must not panic
}

func TestBaselineStore_ForEachBaseline(t *testing.T) {
	s := NewBaselineStore(AckReliable)
	s.GetOrCreateBaseline(1, 0)
	s.GetOrCreateBaseline(2, 0)
	s.GetOrCreateBaseline(3, 0)

	seen := make(map[uint32]int)
	s.ForEachBaseline(func(netID uint32, b *EntityBaseline) {
		if b == nil {
			t.Fatalf("ForEachBaseline passed nil baseline for netID %d", netID)
		}
		seen[netID]++
	})

	if len(seen) != 3 {
		t.Fatalf("ForEachBaseline visited %d baselines, want 3", len(seen))
	}
	for _, id := range []uint32{1, 2, 3} {
		if seen[id] != 1 {
			t.Fatalf("netID %d visited %d times, want 1", id, seen[id])
		}
	}
}

func TestBaselineStore_DropBaselineClearsPriority(t *testing.T) {
	s := NewBaselineStore(AckReliable)
	s.SetLastHash(7, 12345)
	s.GetOrCreateBaseline(7, 0)
	p := s.Priority(7) // allocates
	p.Accumulator = 99

	s.DropBaseline(7)

	if s.Baseline(7) != nil {
		t.Fatal("DropBaseline did not clear baseline")
	}
	if s.LastHash(7) != 0 {
		t.Fatal("DropBaseline did not clear last hash")
	}
	// Priority should have been dropped; asking for it again returns a
	// freshly-allocated zero-valued state.
	p2 := s.Priority(7)
	if p2 == p {
		t.Fatal("DropBaseline did not clear priority: got same pointer")
	}
	if p2.Accumulator != 0 {
		t.Fatalf("fresh priority has non-zero accumulator: %v", p2.Accumulator)
	}
}
