package game

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// netIDOfECS reads the NetworkID off an ecs.Entity. Test helper for the
// multi-lock cases below — lock slots key on netID, and TargetEntity is
// typed ecs.Entity, so we need the bridge from the rich mmokit.Entity.
func netIDOfECS(gw *GameWorld, h ecs.Entity) uint32 {
	w := gw.stage.ECSWorld()
	id := ecs.NewMap1[mmokit.NetworkID](w).Get(h)
	if id == nil {
		return 0
	}
	return id.ID
}

// newLockOwner builds a minimal "owner of a TargetLock" entity at (x,y)
// with a TargetLock component and a NetworkID. No PlayerConn, so
// sendLockSlots is a no-op and we don't need a wired connection.
func newLockOwner(t *testing.T, gw *GameWorld, netID uint32, x, y float32) mmokit.Entity {
	t.Helper()
	w := gw.stage.ECSWorld()
	mapper := ecs.NewMap3[mmokit.NetworkID, mmokit.Position, gamecomp.TargetLock](w)
	handle := mapper.NewEntity(
		&mmokit.NetworkID{ID: netID},
		&mmokit.Position{X: x, Y: y},
		&gamecomp.TargetLock{},
	)
	gw.stage.RegisterLiveNetID(netID, handle)
	return mmokit.EntityFromECS(gw.stage, handle)
}

// newWiredTargetLockSystem wires TargetLockSystem against gw so it can
// be ticked directly from tests without a running game loop. Also
// pre-registers Leashing on the ECS world — the system queries it via
// mmokit.Has but never spawns it on a test entity, and ark panics on
// the first query for an unregistered component type.
func newWiredTargetLockSystem(t *testing.T, gw *GameWorld) *TargetLockSystem {
	t.Helper()
	w := gw.stage.ECSWorld()
	_ = ecs.NewMap1[gamecomp.Leashing](w) // force component registration
	sys := &TargetLockSystem{}
	mmokit.WireSystem(sys, w, gw.eng, gw.stage)
	return sys
}

// TestMultiLock_ParallelProgress verifies that locking two targets at
// once ticks both slots in parallel, not sequentially.
func TestMultiLock_ParallelProgress(t *testing.T) {
	gw, _ := newTestGameWorld()

	owner := newLockOwner(t, gw, 9001, 0, 0)
	a := gw.SpawnNPC(50, 0, ArchetypeBrawler, 0)
	b := gw.SpawnNPC(-50, 0, ArchetypeBrawler, 0)

	lock := mmokit.Get[gamecomp.TargetLock](owner)
	lock.Range = 1000
	lock.Slots = []gamecomp.LockSlot{
		{TargetNetID: netIDOfECS(gw, a.Handle()), TargetEntity: a.Handle(), Progress: 0, LockTime: 2.0},
		{TargetNetID: netIDOfECS(gw, b.Handle()), TargetEntity: b.Handle(), Progress: 0, LockTime: 2.0},
	}

	sys := newWiredTargetLockSystem(t, gw)
	// 1.0 second of simulated time at 50ms per tick.
	const dt = float32(0.05)
	for range 20 {
		gw.stage.TickOne(sys, dt)
	}

	lock = mmokit.Get[gamecomp.TargetLock](owner)
	if len(lock.Slots) != 2 {
		t.Fatalf("expected 2 slots, got %d", len(lock.Slots))
	}
	for i, s := range lock.Slots {
		if s.Progress < 0.4 || s.Progress > 0.6 {
			t.Errorf("slot %d progress %.2f outside [0.4, 0.6]", i, s.Progress)
		}
		if s.Locked {
			t.Errorf("slot %d locked too early", i)
		}
	}
}

// TestMultiLock_AutoActiveOnFirstComplete — first lock to complete
// auto-becomes active when no active was set.
func TestMultiLock_AutoActiveOnFirstComplete(t *testing.T) {
	gw, _ := newTestGameWorld()

	owner := newLockOwner(t, gw, 9002, 0, 0)
	a := gw.SpawnNPC(50, 0, ArchetypeBrawler, 0)

	lock := mmokit.Get[gamecomp.TargetLock](owner)
	lock.Range = 1000
	lock.Slots = []gamecomp.LockSlot{
		{TargetNetID: netIDOfECS(gw, a.Handle()), TargetEntity: a.Handle(), Progress: 0.9, LockTime: 1.0},
	}
	if lock.ActiveNetID != 0 {
		t.Fatalf("precondition: ActiveNetID should be 0, got %d", lock.ActiveNetID)
	}

	sys := newWiredTargetLockSystem(t, gw)
	// 0.2s of simulated time at 50ms per tick — enough to push 0.9 past 1.0.
	const dt = float32(0.05)
	for range 4 {
		gw.stage.TickOne(sys, dt)
	}

	lock = mmokit.Get[gamecomp.TargetLock](owner)
	if !lock.Slots[0].Locked {
		t.Fatal("slot did not complete")
	}
	if lock.ActiveNetID != netIDOfECS(gw, a.Handle()) {
		t.Errorf("ActiveNetID = %d, want %d", lock.ActiveNetID, netIDOfECS(gw, a.Handle()))
	}
}

// TestMultiLock_OutOfRangeDrop — moving a target out of range cancels
// in-progress and completed slots immediately.
func TestMultiLock_OutOfRangeDrop(t *testing.T) {
	gw, _ := newTestGameWorld()

	owner := newLockOwner(t, gw, 9003, 0, 0)
	a := gw.SpawnNPC(50, 0, ArchetypeBrawler, 0)

	lock := mmokit.Get[gamecomp.TargetLock](owner)
	lock.Range = 100
	lock.Slots = []gamecomp.LockSlot{
		{TargetNetID: netIDOfECS(gw, a.Handle()), TargetEntity: a.Handle(), Progress: 0.5, LockTime: 1.0},
	}

	// Move target out of range before ticking.
	pos := mmokit.Get[mmokit.Position](a)
	pos.X = 500

	sys := newWiredTargetLockSystem(t, gw)
	gw.stage.TickOne(sys, 0.05)

	lock = mmokit.Get[gamecomp.TargetLock](owner)
	if len(lock.Slots) != 0 {
		t.Errorf("expected slot dropped, got %d slots", len(lock.Slots))
	}
}
