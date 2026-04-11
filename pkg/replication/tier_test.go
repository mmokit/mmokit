package replication

import "testing"

func TestTier_DefaultPromoteRadius(t *testing.T) {
	// Unset PromoteRadius defaults to half of Radius.
	tier := ReplicationTier{Radius: 1000}
	if got := DefaultPromoteRadius(tier); got != 500 {
		t.Fatalf("default promote radius: got %v want 500", got)
	}

	// Explicit PromoteRadius is respected.
	tier.PromoteRadius = 750
	if got := DefaultPromoteRadius(tier); got != 750 {
		t.Fatalf("explicit promote radius: got %v want 750", got)
	}
}

func TestTier_DefaultPromoteLookahead(t *testing.T) {
	// Unset PromoteLookahead defaults to 10 ticks.
	tier := ReplicationTier{Radius: 1000}
	if got := DefaultPromoteLookahead(tier); got != 10 {
		t.Fatalf("default lookahead: got %v want 10", got)
	}

	// Explicit PromoteLookahead is respected.
	tier.PromoteLookahead = 25
	if got := DefaultPromoteLookahead(tier); got != 25 {
		t.Fatalf("explicit lookahead: got %v want 25", got)
	}
}

func TestTier_InsideRadius(t *testing.T) {
	tier := ReplicationTier{Radius: 100}

	// Point at exactly radius distance is still inside (inclusive).
	if !InsideRadius(tier, 0, 0, 100, 0) {
		t.Fatal("point at distance 100 should be inside radius 100")
	}
	if !InsideRadius(tier, 0, 0, 50, 0) {
		t.Fatal("point at distance 50 should be inside radius 100")
	}
	if InsideRadius(tier, 0, 0, 150, 0) {
		t.Fatal("point at distance 150 should not be inside radius 100")
	}

	// Diagonal distance.
	if !InsideRadius(tier, 0, 0, 60, 80) {
		t.Fatal("point at distance 100 (60,80) should be inside radius 100")
	}
	if InsideRadius(tier, 0, 0, 70, 80) {
		t.Fatal("point at distance ~106 should not be inside radius 100")
	}
}

func TestTier_SkipThisTick_DivisorThree(t *testing.T) {
	tier := ReplicationTier{UpdateDivisor: 3}
	if SkipThisTick(tier, 0) {
		t.Fatal("tick 0 should not be skipped with divisor 3")
	}
	if !SkipThisTick(tier, 1) {
		t.Fatal("tick 1 should be skipped with divisor 3")
	}
	if !SkipThisTick(tier, 2) {
		t.Fatal("tick 2 should be skipped with divisor 3")
	}
	if SkipThisTick(tier, 3) {
		t.Fatal("tick 3 should not be skipped")
	}
	if SkipThisTick(tier, 6) {
		t.Fatal("tick 6 should not be skipped")
	}
}

func TestTier_SkipThisTick_DivisorZeroOrOneNeverSkips(t *testing.T) {
	// Divisors 0 and 1 both mean "send every tick" (no skipping).
	for _, d := range []int{0, 1} {
		tier := ReplicationTier{UpdateDivisor: d}
		for tick := uint64(0); tick < 10; tick++ {
			if SkipThisTick(tier, tick) {
				t.Fatalf("divisor %d tick %d should never skip", d, tick)
			}
		}
	}
}
