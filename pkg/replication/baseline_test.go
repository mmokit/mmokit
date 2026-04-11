package replication

import "testing"

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
