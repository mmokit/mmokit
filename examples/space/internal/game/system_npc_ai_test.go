package game

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	"github.com/mmokit/mmokit"
	gamecomp "github.com/mmokit/mmokit/examples/space/internal/component"
	"github.com/mmokit/mmokit/pkg/spatial"
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

	npc := gw.SpawnNPC(0, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{})
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

	npc := gw.SpawnNPC(0, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{})
	// 70u: outside default 30u aggro AND 50u weapon range. Bump this
	// NPC's aggro to 100u so it locks at 70u and exercises the charge
	// motion (default tuning leaves no in-aggro / out-of-weapon zone).
	if aiComp := mmokit.Get[gamecomp.NPCAI](npc); aiComp != nil {
		aiComp.AggroRadius = 100
	}
	newTestPlayerAt(t, gw, 8002, 70, 0)

	const dt = float32(0.05)
	tickAI(gw.stage, ai, phys, dt, 10) // 0.5s

	pos := mmokit.Get[mmokit.Position](npc)
	if pos.X <= 0 {
		t.Errorf("Brawler should have moved toward target (+x); pos.X=%.2f", pos.X)
	}
}

// TestNPCAI_LOSBlockedTargetNotAcquired — a Brawler whose only candidate
// player sits inside AggroRadius but on the far side of a LayerStatic
// wall must NOT acquire the target. The Euclidean-distance scan finds
// the player; the LOS gate added in Task 8 rejects it because the wall
// blocks the line.
func TestNPCAI_LOSBlockedTargetNotAcquired(t *testing.T) {
	gw, _ := newTestGameWorld()
	ai, phys := newWiredNPCAISystem(t, gw)

	// Brawler at origin; default 30u aggro radius.
	npc := gw.SpawnNPC(0, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{})
	// Player at (20, 0) — inside aggro (matches TestNPCAI_IdleToAcquireOnTargetInRange).
	newTestPlayerAt(t, gw, 8101, 20, 0)

	// Wall at (10, 0): rect 10 wide, 20 tall, LayerStatic. Sits squarely
	// on the segment NPC(0,0) → Player(20,0) so Raycast against
	// LayerStatic must hit it. We register the wall directly in the
	// spatial grid since SpawnDungeonWall doesn't exist yet (Task 16).
	wallW := gw.stage.ECSWorld()
	wallE := wallW.NewEntity()
	gw.Spatial.Register(spatial.Entry{
		Entity: wallE,
		X:      10, Y: 0,
		Radius: 12, Width: 10, Height: 20,
		Shape: spatial.ShapeRect, Layer: spatial.LayerStatic,
	})

	const dt = float32(0.05)
	tickAI(gw.stage, ai, phys, dt, 2) // same window as the positive test

	aiComp := mmokit.Get[gamecomp.NPCAI](npc)
	if aiComp.State != AIStateIdle {
		t.Errorf("Brawler must stay Idle when target is LOS-blocked; got state=%d", aiComp.State)
	}
}

// TestNPCAI_LOSLostDropsTargetAfterTimeout — once a Brawler is actively
// engaging, dropping a LayerStatic wall between it and its target must
// cause the NPC to drop back to Idle after AILosLossDropSec (3.0s default)
// of continuously blocked LOS. The deescalation timer is bumped to a large
// value so the only path out of Engage is the new LOS-loss path.
func TestNPCAI_LOSLostDropsTargetAfterTimeout(t *testing.T) {
	gw, _ := newTestGameWorld()
	ai, phys := newWiredNPCAISystem(t, gw)

	// Push the deescalation timer way out so it can't be what drops the
	// target — we want to prove the LOS-loss path fires on its own.
	gw.Config.AggroDeescalationSec = 600

	npc := gw.SpawnNPC(0, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{})
	player := newTestPlayerAt(t, gw, 8201, 20, 0) // inside 30u aggro, no wall yet

	const dt = float32(0.05)

	// Phase 1: warm up until the NPC is in Engage with LOS still clear.
	tickAI(gw.stage, ai, phys, dt, 4) // 0.2s — Idle → Acquire → Engage
	aiComp := mmokit.Get[gamecomp.NPCAI](npc)
	if aiComp.State != AIStateEngage {
		t.Fatalf("setup: NPC did not reach Engage; state=%d", aiComp.State)
	}

	// Phase 2: drop a LayerStatic wall directly on the sight line. After
	// AILosLossDropSec of continuous LOS blockage the NPC must drop to Idle.
	wallW := gw.stage.ECSWorld()
	wallE := wallW.NewEntity()
	gw.Spatial.Register(spatial.Entry{
		Entity: wallE,
		X:      10, Y: 0,
		Radius: 12, Width: 10, Height: 20,
		Shape: spatial.ShapeRect, Layer: spatial.LayerStatic,
	})

	// Also park the player so the brawler doesn't follow it out from
	// behind the wall and re-establish LOS.
	pp := mmokit.Get[mmokit.Position](mmokit.EntityFromECS(gw.stage, player))
	pp.X, pp.Y = 20, 0

	// Tick well past AILosLossDropSec (3.0s). The first recheck fires at
	// ~0.5s (AILosRecheckIntervalSec) after the wall goes up, and the drop
	// fires at the first recheck where now-LOSLostAt >= 3.0s — so we need
	// roughly 3.5s + one recheck interval to be safe. 4.0s = 80 ticks.
	tickAI(gw.stage, ai, phys, dt, 80)

	aiComp = mmokit.Get[gamecomp.NPCAI](npc)
	if aiComp.State != AIStateIdle {
		t.Errorf("Brawler must drop to Idle after sustained LOS loss; got state=%d", aiComp.State)
	}
	if aiComp.TargetNetID != 0 {
		t.Errorf("TargetNetID must be cleared on LOS-loss drop; got %d", aiComp.TargetNetID)
	}
}

// TestNPCAI_AggroDeescalation — AI returns to Idle after
// AggroDeescalationSec (default 6s) of no damage activity. The target
// is teleported out of weapon range mid-test so the NPC stops stamping
// LastCombatActivityAt on every shot.
func TestNPCAI_AggroDeescalation(t *testing.T) {
	gw, _ := newTestGameWorld()
	ai, phys := newWiredNPCAISystem(t, gw)

	npc := gw.SpawnNPC(0, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{})
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

// TestSpawnNPC_EliteStatsApplied — confirms the Elite modifier on
// SpawnNPC multiplies HP / MaxSpeed / DamagePerShot by the BossSolo
// multipliers. Comparison is against a vanilla Brawler at the same
// archetype with no modifiers.
func TestSpawnNPC_EliteStatsApplied(t *testing.T) {
	gw, _ := newTestGameWorld()

	normal := gw.SpawnNPC(0, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{})
	elite := gw.SpawnNPC(50, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{Elite: true})

	nAI := mmokit.Get[gamecomp.NPCAI](normal)
	eAI := mmokit.Get[gamecomp.NPCAI](elite)
	if nAI == nil || eAI == nil {
		t.Fatal("expected NPCAI on both spawns")
	}
	nH := mmokit.Get[gamecomp.Health](normal)
	eH := mmokit.Get[gamecomp.Health](elite)
	if nH == nil || eH == nil {
		t.Fatal("expected Health on both spawns")
	}

	cfg := gw.Config
	wantHP := nH.Max * cfg.BossSoloHPMultiplier
	wantSpeed := nAI.MaxSpeed * cfg.BossSoloSpeedMultiplier
	wantDmg := nAI.DamagePerShot * cfg.BossSoloDmgMultiplier
	const eps = 1e-3

	if absF32(eH.Max-wantHP) > eps {
		t.Errorf("elite HP: got %.2f, want %.2f (normal=%.2f x %.2f)",
			eH.Max, wantHP, nH.Max, cfg.BossSoloHPMultiplier)
	}
	if absF32(eH.Current-wantHP) > eps {
		t.Errorf("elite HP.Current: got %.2f, want %.2f", eH.Current, wantHP)
	}
	if absF32(eAI.MaxSpeed-wantSpeed) > eps {
		t.Errorf("elite MaxSpeed: got %.2f, want %.2f", eAI.MaxSpeed, wantSpeed)
	}
	if absF32(eAI.DamagePerShot-wantDmg) > eps {
		t.Errorf("elite DamagePerShot: got %.2f, want %.2f", eAI.DamagePerShot, wantDmg)
	}
}

// absF32 — local helper so the test file doesn't pull in math just for this.
func absF32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}

// TestBossGuardian_StationaryNeverMoves — a BossGuardian with a player
// well outside WeaponRange must hold position. MotionStationary keeps
// vel.X/Y = 0 even when AI is in Approach/Acquire/Engage (whichever the
// state machine settles into without a target in lock range).
func TestBossGuardian_StationaryNeverMoves(t *testing.T) {
	gw, _ := newTestGameWorld()
	ai, phys := newWiredNPCAISystem(t, gw)

	npc := gw.SpawnNPC(0, 0, ArchetypeBossGuardian, 0, NPCSpawnModifiers{})
	// Place a player inside the boss's aggro radius so it enters Engage;
	// stationary motion must still zero out velocity.
	npcAI := mmokit.Get[gamecomp.NPCAI](npc)
	aggro := npcAI.AggroRadius
	newTestPlayerAt(t, gw, 9101, aggro*0.3, 0)

	startX, startY := float32(0), float32(0)
	if p := mmokit.Get[mmokit.Position](npc); p != nil {
		startX, startY = p.X, p.Y
	}

	const dt = float32(0.05)
	tickAI(gw.stage, ai, phys, dt, 40) // 2 seconds

	p := mmokit.Get[mmokit.Position](npc)
	if p == nil {
		t.Fatal("BossGuardian lost position")
	}
	if absF32(p.X-startX) > 0.1 || absF32(p.Y-startY) > 0.1 {
		t.Errorf("BossGuardian moved (Stationary policy): start=(%.2f,%.2f) now=(%.2f,%.2f)",
			startX, startY, p.X, p.Y)
	}
}

// TestBossGuardian_SpawnsAddsAtHPThresholds — driving the boss's HP
// below each Config.BossMainAddSpawnThresholds entry must spawn 2 add
// NPCs once per threshold. Re-crossing the same threshold is a no-op.
func TestBossGuardian_SpawnsAddsAtHPThresholds(t *testing.T) {
	gw, _ := newTestGameWorld()
	aiSys, phys := newWiredNPCAISystem(t, gw)

	npc := gw.SpawnNPC(0, 0, ArchetypeBossGuardian, 0, NPCSpawnModifiers{})
	bossH := mmokit.Get[gamecomp.Health](npc)
	bossAI := mmokit.Get[gamecomp.NPCAI](npc)
	if bossH == nil || bossAI == nil {
		t.Fatal("expected Health + NPCAI on BossGuardian")
	}

	// Give the boss a target so tickEngage runs (the add-spawn check
	// lives in the engaged branch).
	newTestPlayerAt(t, gw, 9201, bossAI.AggroRadius*0.3, 0)

	// Warm up: enough ticks for Idle → Acquire → Engage. The boss is
	// stationary so turn-to-face is the only obstacle.
	const dt = float32(0.05)
	tickAI(gw.stage, aiSys, phys, dt, 20)
	if bossAI.State != AIStateEngage {
		t.Fatalf("expected BossGuardian in Engage before damage; state=%d", bossAI.State)
	}

	// countLancers returns the number of live Lancer NPCs anchored to
	// the boss's dungeon (0 here since this is a console-spawned boss)
	// or anywhere on the stage by archetype — we just count Lancers.
	countLancers := func() int {
		n := 0
		mmokit.ForEach1(gw.stage, func(e mmokit.Entity, ai *gamecomp.NPCAI) {
			if ai.Archetype == ArchetypeLancer {
				n++
			}
		})
		return n
	}

	thresholds := gw.Config.BossMainAddSpawnThresholds
	prior := countLancers()

	for i, t0 := range thresholds {
		// Drive HP just below threshold i (within (t-1%) ≈ t-0.01 of Max).
		bossH.Current = bossH.Max * (t0 - 0.01)
		tickAI(gw.stage, aiSys, phys, dt, 2) // one engage tick + flush
		got := countLancers()
		if got-prior != 2 {
			t.Errorf("threshold %d (frac<%.2f): expected +2 Lancers, got +%d (total=%d)",
				i, t0, got-prior, got)
		}
		prior = got

		// Crossing the same threshold again must not respawn adds.
		bossH.Current = bossH.Max * (t0 - 0.02) // stay below
		tickAI(gw.stage, aiSys, phys, dt, 2)
		got2 := countLancers()
		if got2 != prior {
			t.Errorf("threshold %d re-crossing should be no-op; total went %d→%d",
				i, prior, got2)
		}
	}
}
