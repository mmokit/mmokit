package replication

import "testing"

func TestPriority_AccumulatesOnSkip(t *testing.T) {
	s := NewBaselineStore(AckReliable)
	p := s.Priority(1)
	if p.Accumulator != 0 {
		t.Fatal("fresh priority should have zero accumulator")
	}

	AccumulatePriority(p, ReplicationTier{BaseWeight: 1.5}, 1.0 /* distance factor */)
	if p.Accumulator != 1.5 {
		t.Fatalf("one accumulation: got %v want 1.5", p.Accumulator)
	}

	AccumulatePriority(p, ReplicationTier{BaseWeight: 1.5}, 1.0)
	if p.Accumulator != 3.0 {
		t.Fatalf("two accumulations: got %v want 3.0", p.Accumulator)
	}
}

func TestPriority_DistanceFactorScales(t *testing.T) {
	// A priority accumulation with a 0.5 distance factor should contribute
	// half as much as one with a factor of 1.0.
	s := NewBaselineStore(AckReliable)
	p := s.Priority(1)
	AccumulatePriority(p, ReplicationTier{BaseWeight: 2.0}, 0.5)
	if p.Accumulator != 1.0 {
		t.Fatalf("distance-scaled accumulation: got %v want 1.0", p.Accumulator)
	}
}

func TestPriority_ZeroDistanceFactorContributesNothing(t *testing.T) {
	s := NewBaselineStore(AckReliable)
	p := s.Priority(1)
	AccumulatePriority(p, ReplicationTier{BaseWeight: 5.0}, 0.0)
	if p.Accumulator != 0 {
		t.Fatalf("zero distance factor: got %v want 0", p.Accumulator)
	}
}

func TestPriority_ResetOnSend(t *testing.T) {
	s := NewBaselineStore(AckReliable)
	p := s.Priority(1)
	AccumulatePriority(p, ReplicationTier{BaseWeight: 2.0}, 1.0)
	AccumulatePriority(p, ReplicationTier{BaseWeight: 2.0}, 1.0)
	if p.Accumulator == 0 {
		t.Fatal("precondition: accumulator should be non-zero before reset")
	}
	ResetPriority(p)
	if p.Accumulator != 0 {
		t.Fatalf("reset should zero accumulator, got %v", p.Accumulator)
	}
}

func TestPriority_NilSafe(t *testing.T) {
	// The helpers are called from hot-path dispatcher loops; they must
	// not panic on a nil pointer (defensive behavior for edge cases).
	AccumulatePriority(nil, ReplicationTier{BaseWeight: 1.0}, 1.0)
	ResetPriority(nil)
}
