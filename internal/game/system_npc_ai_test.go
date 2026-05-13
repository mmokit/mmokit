package game

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// newWiredNPCAISystem wires NPCAISystem (and a sidecar PhysicsSystem so
// velocity integrates into position) against gw so they can be ticked
// directly from tests without a running game loop. Pre-registers
// Leashing on the ECS world — the AI system queries it via mmokit.Has
// but tests never spawn it, and ark panics on the first query for an
// unregistered component type.
func newWiredNPCAISystem(t *testing.T, gw *GameWorld) (*NPCAISystem, *mmokit.PhysicsSystem) {
	t.Helper()
	w := gw.stage.ECSWorld()
	_ = ecs.NewMap1[gamecomp.Leashing](w) // force component registration

	ai := &NPCAISystem{}
	mmokit.WireSystem(ai, w, gw.eng, gw.stage)

	phys := &mmokit.PhysicsSystem{}
	mmokit.WireSystem(phys, w, gw.eng, gw.stage)

	return ai, phys
}

// newTestPlayerAt spawns a ship-kind entity with Position, Health,
// Shield, and EntityKind=KindShip — the minimum needed for
// NPCAISystem.findNearestEnemy to consider it a valid target and for
// gw.ApplyDamage to absorb shots without exploding. Returns the
// ecs.Entity handle so callers can fetch components.
func newTestPlayerAt(t *testing.T, gw *GameWorld, netID uint32, x, y float32) ecs.Entity {
	t.Helper()
	w := gw.stage.ECSWorld()
	mapper := ecs.NewMap5[
		mmokit.Position, mmokit.NetworkID, mmokit.EntityKind,
		gamecomp.Health, gamecomp.Shield,
	](w)
	handle := mapper.NewEntity(
		&mmokit.Position{X: x, Y: y},
		&mmokit.NetworkID{ID: netID},
		&mmokit.EntityKind{Type: gamecomp.KindShip},
		&gamecomp.Health{Current: 1000, Max: 1000},
		&gamecomp.Shield{Current: 1000, Max: 1000},
	)
	gw.stage.RegisterLiveNetID(netID, handle)
	return handle
}

// tickAI advances the AI + Physics systems by n steps of dt seconds each.
// Mirrors the engine game loop ordering: AI sets velocity, Physics
// integrates velocity into position. Uses stage.TickOne so Commands
// queued in either system flush between systems just like in production.
func tickAI(stage *mmokit.Stage, ai *NPCAISystem, phys *mmokit.PhysicsSystem, dt float32, n int) {
	for range n {
		stage.TickOne(ai, dt)
		stage.TickOne(phys, dt)
	}
}

// TestNPCAI_IdleToAcquireOnTargetInRange — Brawler must transition out
// of Idle the moment a visible player ship enters its aggro radius.
func TestNPCAI_IdleToAcquireOnTargetInRange(t *testing.T) {
	gw, _ := newTestGameWorld()
	ai, phys := newWiredNPCAISystem(t, gw)

	npc := gw.SpawnNPC(0, 0, ArchetypeBrawler, 0)
	newTestPlayerAt(t, gw, 8001, 20, 0) // inside Brawler's 30u aggro

	const dt = float32(0.05)
	tickAI(gw.stage, ai, phys, dt, 2) // 0.1s — enough for Idle→Acquire (and possibly →Engage)

	aiComp := mmokit.Get[gamecomp.NPCAI](npc)
	if aiComp.State != AIStateAcquire && aiComp.State != AIStateEngage {
		t.Errorf("Brawler should acquire/engage; state=%d", aiComp.State)
	}
}

// TestNPCAI_BrawlerCharges — Brawler (MotionCharge) must accelerate
// toward an out-of-weapon-range target.
func TestNPCAI_BrawlerCharges(t *testing.T) {
	gw, _ := newTestGameWorld()
	ai, phys := newWiredNPCAISystem(t, gw)

	npc := gw.SpawnNPC(0, 0, ArchetypeBrawler, 0)
	// 70u: outside default 30u aggro AND 50u weapon range. Bump this
	// NPC's aggro to 100u so it locks at 70u and exercises the charge
	// motion (default tuning leaves no in-aggro / out-of-weapon zone).
	if aiComp := mmokit.Get[gamecomp.NPCAI](npc); aiComp != nil {
		aiComp.AggroRadius = 100
	}
	if lock := mmokit.Get[gamecomp.TargetLock](npc); lock != nil {
		lock.Range = 100
	}
	newTestPlayerAt(t, gw, 8002, 70, 0)

	const dt = float32(0.05)
	tickAI(gw.stage, ai, phys, dt, 10) // 0.5s

	pos := mmokit.Get[mmokit.Position](npc)
	if pos.X <= 0 {
		t.Errorf("Brawler should have moved toward target (+x); pos.X=%.2f", pos.X)
	}
}

// TestNPCAI_SniperHoldsRange — Sniper (MotionHoldRange) must kite away
// when a target is well inside its 600u preferred range.
func TestNPCAI_SniperHoldsRange(t *testing.T) {
	gw, _ := newTestGameWorld()
	ai, phys := newWiredNPCAISystem(t, gw)

	npc := gw.SpawnNPC(0, 0, ArchetypeSniper, 0)
	// 20u: well inside Sniper's 45u preferred range (kite away).
	newTestPlayerAt(t, gw, 8003, 20, 0)

	const dt = float32(0.05)
	tickAI(gw.stage, ai, phys, dt, 10) // 0.5s

	pos := mmokit.Get[mmokit.Position](npc)
	if pos.X >= 0 {
		t.Errorf("Sniper too close → should kite (-x); pos.X=%.2f", pos.X)
	}
}

// TestNPCAI_AggroDeescalation — AI returns to Idle after
// AggroDeescalationSec (default 6s) of no damage activity. The target
// is teleported out of weapon range mid-test so the NPC stops stamping
// LastCombatActivityAt on every shot.
func TestNPCAI_AggroDeescalation(t *testing.T) {
	gw, _ := newTestGameWorld()
	ai, phys := newWiredNPCAISystem(t, gw)

	npc := gw.SpawnNPC(0, 0, ArchetypeBrawler, 0)
	player := newTestPlayerAt(t, gw, 8004, 20, 0) // inside 30u aggro

	const dt = float32(0.05)
	tickAI(gw.stage, ai, phys, dt, 4) // 0.2s — enter Engage (acquire is near-instant at 0°)

	npcAI := mmokit.Get[gamecomp.NPCAI](npc)
	if npcAI.State == AIStateIdle {
		t.Fatal("NPC didn't engage during warmup")
	}

	// Move player well outside both weapon (100u) and aggro (800u) so
	// no firing happens and the deescalation timer advances unimpeded.
	pp := mmokit.Get[mmokit.Position](mmokit.EntityFromECS(gw.stage, player))
	pp.X = 5000
	npcAI.LastDamageByNetID = 0 // belt-and-suspenders

	tickAI(gw.stage, ai, phys, dt, 160) // 8s — longer than AggroDeescalationSec (6s)

	npcAI = mmokit.Get[gamecomp.NPCAI](npc)
	if npcAI.State != AIStateIdle {
		t.Errorf("expected Idle after deescalation; got %d", npcAI.State)
	}
}
