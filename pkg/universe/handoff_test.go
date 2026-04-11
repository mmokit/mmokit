package universe

import "testing"

func TestHandoffState_FreshKeyIsUnseen(t *testing.T) {
	sm := NewHandoffStateMachine()
	key := HandoffKey{EntityNetID: 1, NeighborID: "node_1_0"}
	if sm.State(key) != HandoffUnseen {
		t.Fatalf("fresh key should be Unseen, got %v", sm.State(key))
	}
}

func TestHandoffState_BorderPromotedTransition(t *testing.T) {
	sm := NewHandoffStateMachine()
	key := HandoffKey{EntityNetID: 1, NeighborID: "node_1_0"}

	sm.SetState(key, HandoffBorder)
	if sm.State(key) != HandoffBorder {
		t.Fatal("Border state did not persist")
	}

	sm.SetState(key, HandoffPromoted)
	if sm.State(key) != HandoffPromoted {
		t.Fatal("Promoted state did not persist")
	}
	if sm.WarmupCount(key) != 0 {
		t.Fatalf("warmup should start at 0 on promotion, got %d", sm.WarmupCount(key))
	}
}

func TestHandoffState_WarmupTickAccumulates(t *testing.T) {
	sm := NewHandoffStateMachine()
	key := HandoffKey{EntityNetID: 1, NeighborID: "node_1_0"}
	sm.SetState(key, HandoffPromoted)

	for i := 0; i < 5; i++ {
		sm.TickWarmup(key)
	}
	if sm.WarmupCount(key) != 5 {
		t.Fatalf("warmup count: got %d want 5", sm.WarmupCount(key))
	}
}

func TestHandoffState_TickWarmupOnlyAppliesToPromoted(t *testing.T) {
	sm := NewHandoffStateMachine()
	key := HandoffKey{EntityNetID: 1, NeighborID: "node_1_0"}

	// Border state: TickWarmup is a no-op.
	sm.SetState(key, HandoffBorder)
	sm.TickWarmup(key)
	if sm.WarmupCount(key) != 0 {
		t.Fatalf("Border TickWarmup should not advance; got %d", sm.WarmupCount(key))
	}

	// Promote and tick.
	sm.SetState(key, HandoffPromoted)
	sm.TickWarmup(key)
	if sm.WarmupCount(key) != 1 {
		t.Fatal("Promoted TickWarmup should advance")
	}

	// Transitioning back to Border from Promoted doesn't reset the
	// counter — only a fresh Promoted transition does. (Phase 7 will
	// call SetState(HandoffPromoted) again if a retreated entity
	// re-promotes later.)
	sm.SetState(key, HandoffBorder)
	sm.TickWarmup(key)
	if sm.WarmupCount(key) != 1 {
		t.Fatalf("Border TickWarmup after retreat should not advance; got %d", sm.WarmupCount(key))
	}

	// Re-promoting resets warmup to 0.
	sm.SetState(key, HandoffPromoted)
	if sm.WarmupCount(key) != 0 {
		t.Fatal("re-promotion should reset warmup to 0")
	}
}

func TestHandoffState_CanCommitRequiresPromotedAndWarmupFloor(t *testing.T) {
	sm := NewHandoffStateMachine()
	key := HandoffKey{EntityNetID: 1, NeighborID: "node_1_0"}

	// Fresh Unseen: can't commit.
	if sm.CanCommit(key) {
		t.Fatal("Unseen should not be committable")
	}

	// Border: can't commit.
	sm.SetState(key, HandoffBorder)
	if sm.CanCommit(key) {
		t.Fatal("Border should not be committable")
	}

	// Promoted with too-few warmup ticks.
	sm.SetState(key, HandoffPromoted)
	for i := 0; i < MinWarmupTicks-1; i++ {
		sm.TickWarmup(key)
	}
	if sm.CanCommit(key) {
		t.Fatalf("Promoted at warmup=%d should not be committable (floor=%d)",
			MinWarmupTicks-1, MinWarmupTicks)
	}

	// One more tick hits the floor exactly.
	sm.TickWarmup(key)
	if !sm.CanCommit(key) {
		t.Fatalf("Promoted at warmup=%d should be committable", MinWarmupTicks)
	}
}

func TestHandoffState_CooldownWindow(t *testing.T) {
	sm := NewHandoffStateMachine()
	key := HandoffKey{EntityNetID: 1, NeighborID: "node_1_0"}

	// Cooldown enters at tick 100.
	sm.EnterCooldown(key, 100)

	// Same tick: no cooldown active yet (cooldown fires on future ticks).
	if sm.InCooldown(key, 100) {
		t.Fatal("cooldown should not report active on the entry tick itself")
	}

	// 1 tick later: in cooldown.
	if !sm.InCooldown(key, 101) {
		t.Fatal("cooldown should be active 1 tick after entry")
	}

	// Halfway through the window.
	if !sm.InCooldown(key, 100+CrossingCooldownTicks/2) {
		t.Fatal("cooldown should be active mid-window")
	}

	// At the last valid tick.
	if !sm.InCooldown(key, 100+CrossingCooldownTicks) {
		t.Fatal("cooldown should be active at final window tick")
	}

	// One tick past: expired.
	if sm.InCooldown(key, 100+CrossingCooldownTicks+1) {
		t.Fatal("cooldown should expire one tick after CrossingCooldownTicks")
	}
}

func TestHandoffState_CooldownAbsentWhenNotEntered(t *testing.T) {
	sm := NewHandoffStateMachine()
	key := HandoffKey{EntityNetID: 1, NeighborID: "node_1_0"}

	// Never entered cooldown: InCooldown is always false, regardless of tick.
	if sm.InCooldown(key, 0) {
		t.Fatal("fresh key should never be in cooldown")
	}
	if sm.InCooldown(key, 100) {
		t.Fatal("fresh key should never be in cooldown at large tick")
	}
}

func TestHandoffState_Forget(t *testing.T) {
	sm := NewHandoffStateMachine()
	key := HandoffKey{EntityNetID: 1, NeighborID: "node_1_0"}

	sm.SetState(key, HandoffPromoted)
	sm.TickWarmup(key)
	sm.EnterCooldown(key, 50)

	sm.Forget(key)

	// After Forget, state is Unseen and all counters are reset.
	if sm.State(key) != HandoffUnseen {
		t.Fatalf("after Forget, state should be Unseen, got %v", sm.State(key))
	}
	if sm.WarmupCount(key) != 0 {
		t.Fatalf("after Forget, warmup should be 0, got %d", sm.WarmupCount(key))
	}
	if sm.InCooldown(key, 100) {
		t.Fatal("after Forget, cooldown should be gone")
	}
}

func TestHandoffState_Constants(t *testing.T) {
	if MinWarmupTicks != 5 {
		t.Fatalf("MinWarmupTicks = %d, want 5 (250ms at 20Hz per spec)", MinWarmupTicks)
	}
	if CrossingCooldownTicks != 20 {
		t.Fatalf("CrossingCooldownTicks = %d, want 20 (1s at 20Hz per spec)", CrossingCooldownTicks)
	}
}
