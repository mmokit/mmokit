package game

import (
	"testing"

	ecs "github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/item"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// wireAbilitySystem constructs a freshly-wired AbilitySystem against
// gw.stage. Pre-registers Leashing on the ECS world so ApplyDamage can
// query it without tripping ark's locked-world first-touch panic (same
// rationale as wireAoESystem / wireProjectileSystem).
func wireAbilitySystem(t *testing.T, gw *GameWorld) *AbilitySystem {
	t.Helper()
	w := gw.stage.ECSWorld()
	_ = ecs.NewMap1[gamecomp.Leashing](w)

	sys := &AbilitySystem{}
	mmokit.WireSystem(sys, w, gw.eng, gw.stage)
	return sys
}

// spawnAbilityTestPlayer creates a player ship with the components the
// AbilitySystem query needs (PlayerInput, Selection, AbilitySet,
// Equipment) plus the basics (Position, NetworkID, EntityKind, Health,
// Shield, PlayerConn). Returns the rich Entity wrapper for mutation
// (e.g. setting AbilityCast / LastCastAim before ticking).
//
// w1/w2 set the player's Weapon1 / Weapon2 equipment slots — pass an
// item.PlasmaCannon / item.BeamMortarBattery to wire up PVE-v2 weapons.
func spawnAbilityTestPlayer(t *testing.T, gw *GameWorld, netID, connID uint32, x, y float32, w1, w2 uint32) mmokit.Entity {
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
		&gamecomp.Health{Current: 100, Max: 100},
		&gamecomp.Shield{Current: 0, Max: 0}, // shield down — projectile hits land on health
	)
	gw.stage.RegisterLiveNetID(netID, handle)
	e := mmokit.EntityFromECS(gw.stage, handle)
	mmokit.Set(e, mmokit.PlayerConn{ConnID: connID})
	mmokit.Set(e, gamecomp.PlayerInput{})
	mmokit.Set(e, gamecomp.Selection{})
	mmokit.Set(e, gamecomp.AbilitySet{})
	mmokit.Set(e, gamecomp.Equipment{Weapon1: w1, Weapon2: w2})
	mmokit.Set(e, mmokit.Rotation{})
	gw.Spatial.Register(spatial.Entry{Entity: handle, X: x, Y: y, Radius: 1})
	return e
}

// spawnAbilityTestNPC mirrors spawnAoETestNPC: an NPC at (x,y) with
// Health, Shield (down), NetworkID, an NPCAI marker, and a spatial-grid
// registration. The NPC is what skillshots will damage.
func spawnAbilityTestNPC(t *testing.T, gw *GameWorld, netID uint32, x, y float32) mmokit.Entity {
	t.Helper()
	w := gw.stage.ECSWorld()
	mapper := ecs.NewMap5[
		mmokit.Position, mmokit.NetworkID, mmokit.EntityKind,
		gamecomp.Health, gamecomp.Shield,
	](w)
	handle := mapper.NewEntity(
		&mmokit.Position{X: x, Y: y},
		&mmokit.NetworkID{ID: netID},
		&mmokit.EntityKind{Type: gamecomp.KindNPC},
		&gamecomp.Health{Current: 100, Max: 100},
		&gamecomp.Shield{Current: 0, Max: 0},
	)
	gw.stage.RegisterLiveNetID(netID, handle)
	e := mmokit.EntityFromECS(gw.stage, handle)
	mmokit.Set(e, gamecomp.NPCAI{})
	gw.Spatial.Register(spatial.Entry{Entity: handle, X: x, Y: y, Radius: 1})
	return e
}

// setAbilityCast queues an ability cast on the player's PlayerInput.
// Mirrors what CastAbility wire-input handler does in production: set
// the bit corresponding to `slot` on AbilityCast, and stamp the cursor
// world coords on LastCastAimX/Y so dispatchSkillshotLine /
// dispatchSkillshotGround pick up an aim vector.
func setAbilityCast(t *testing.T, player mmokit.Entity, slot uint8, aimX, aimY float32) {
	t.Helper()
	input := mmokit.Get[gamecomp.PlayerInput](player)
	if input == nil {
		t.Fatal("player missing PlayerInput component")
	}
	input.AbilityCast |= 1 << slot
	input.LastCastAimX = aimX
	input.LastCastAimY = aimY
}

// selectTarget primes the player's Selection so the AbilitySystem
// pre-dispatch gate (which reads Selection.EntityNetID for LockOn-mode
// abilities) passes. Skillshot-mode abilities ignore Selection, but the
// helper keeps the tests symmetric with the pre-rewrite locking flow.
func selectTarget(t *testing.T, player, target mmokit.Entity) {
	t.Helper()
	sel := mmokit.Get[gamecomp.Selection](player)
	if sel == nil {
		t.Fatal("player missing Selection component")
	}
	sel.EntityNetID = target.NetID()
}

// TestSkillshotLine_AimedAtTargetHits — fire a skillshot-line ability
// (PlasmaShot, slot 0 on PlasmaCannon) aimed at an NPC. The dispatch
// path spawns a Projectile, and ProjectileSystem damages the NPC
// after a few ticks of straight-line motion.
func TestSkillshotLine_AimedAtTargetHits(t *testing.T) {
	gw, _ := newTestGameWorld()
	mmokit.Handle(gw.stage, damageHandler)

	abSys := wireAbilitySystem(t, gw)
	projSys := wireProjectileSystem(t, gw)

	player := spawnAbilityTestPlayer(t, gw, 4001, 1, 0, 0,
		item.PlasmaCannon, 0)
	target := spawnAbilityTestNPC(t, gw, 4002, 20, 0)
	selectTarget(t, player, target)

	targetPos := mmokit.Get[mmokit.Position](target)
	setAbilityCast(t, player, gamecomp.AbilityQ, targetPos.X, targetPos.Y)

	// Tick 1: AbilitySystem dispatches the skillshot, spawning a projectile.
	gw.stage.TickOne(abSys, 0.05)

	// ProjectileSystem moves the bolt at ProjectileSpeed=50 u/s. Use many
	// small ticks so the projectile's hit-radius query catches the target
	// at (20,0); a single large dt would teleport it past.
	for range 20 {
		gw.stage.TickOne(projSys, 0.05)
		if hp := mmokit.Get[gamecomp.Health](target); hp.Current < 100 {
			break // hit registered
		}
	}

	if hp := mmokit.Get[gamecomp.Health](target); hp.Current >= 100 {
		t.Fatalf("expected target damaged by skillshot-line, got HP=%.1f", hp.Current)
	}
}

// TestSkillshotGround_DropsAoEAtCursor — MortarShell (TargetingSkillshotGround,
// secondary on item 108 → slot 3=R when 108 is in Weapon2) drops an
// AoEMarker at the cursor position. After the GroundCastDelay, AoESystem
// resolves the marker and damages everyone inside the splash radius.
func TestSkillshotGround_DropsAoEAtCursor(t *testing.T) {
	gw, _ := newTestGameWorld()
	mmokit.Handle(gw.stage, damageHandler)

	abSys := wireAbilitySystem(t, gw)
	aoeSys := wireAoESystem(t, gw)
	lifeSys := &mmokit.LifetimeSystem{}
	mmokit.WireSystem(lifeSys, gw.stage.ECSWorld(), gw.eng, gw.stage)

	player := spawnAbilityTestPlayer(t, gw, 4101, 1, 0, 0,
		0, item.BeamMortarBattery)
	// Target inside MortarShell range (40u) so the cursor clamp doesn't
	// move the splash center off the target.
	target := spawnAbilityTestNPC(t, gw, 4102, 30, 0)
	selectTarget(t, player, target)

	targetPos := mmokit.Get[mmokit.Position](target)
	setAbilityCast(t, player, gamecomp.AbilityR, targetPos.X, targetPos.Y)

	// Tick 1: AbilitySystem dispatches and queues SpawnAoEMarker via
	// Commands.Defer — the marker appears on the next system flush.
	gw.stage.TickOne(abSys, 0.05)

	// MortarShell GroundCastDelay=0.6s. Each tick: AoESystem checks
	// Lifetime≤0 (no-op while >0), then LifetimeSystem decrements. AoE
	// resolves on the tick after Lifetime hits ≤0; we stop early once
	// the target took damage.
	for range 12 {
		gw.stage.TickOne(aoeSys, 0.1)
		gw.stage.TickOne(lifeSys, 0.1)
		if hp := mmokit.Get[gamecomp.Health](target); hp.Current < 100 {
			break
		}
	}

	if hp := mmokit.Get[gamecomp.Health](target); hp.Current >= 100 {
		t.Fatalf("expected MortarShell splash to damage target, got HP=%.1f", hp.Current)
	}
}

// TestProjectilePierce_HitsTwoTargets — a PierceCount=2 projectile damages
// 3 targets in a line and despawns on the third. Skips the AbilitySystem
// dispatch path and spawns the projectile directly so the test pins the
// pierce semantics in ProjectileSystem, independent of any ability config.
func TestProjectilePierce_HitsTwoTargets(t *testing.T) {
	gw, _ := newTestGameWorld()
	mmokit.Handle(gw.stage, damageHandler)

	projSys := wireProjectileSystem(t, gw)

	t1 := spawnAbilityTestNPC(t, gw, 4201, 10, 0)
	t2 := spawnAbilityTestNPC(t, gw, 4202, 20, 0)
	t3 := spawnAbilityTestNPC(t, gw, 4203, 30, 0)

	gw.SpawnProjectile(0, 0, 100, 0, 1.0, gamecomp.ProjectileSpec{
		OwnerNetID:  0,
		Damage:      30,
		PierceCount: 2,
		Type:        gamecomp.ProjectileTypePlasma,
	})

	// Tick the projectile across all three targets. Speed=100 u/s, so
	// dt=0.4s covers 0→40u. Use multiple small ticks so each NPC is
	// inside the projectileHitRadius=4 query when the projectile crosses.
	for range 8 {
		gw.stage.TickOne(projSys, 0.05)
	}

	if hp := mmokit.Get[gamecomp.Health](t1); hp.Current >= 100 {
		t.Errorf("expected t1 damaged (1st hit): HP=%.1f", hp.Current)
	}
	if hp := mmokit.Get[gamecomp.Health](t2); hp.Current >= 100 {
		t.Errorf("expected t2 damaged (2nd hit, PierceCount=2 still allows): HP=%.1f", hp.Current)
	}
	if hp := mmokit.Get[gamecomp.Health](t3); hp.Current >= 100 {
		t.Errorf("expected t3 damaged (3rd and final hit): HP=%.1f", hp.Current)
	}
}

// TestCursorPick_PicksNearestEnemyToCursor — HomingMissile (CursorPick
// mode on PlasmaCannon secondary slot) fires with aim coords pointing
// at NPC `a`; the server picks `a` (nearest to cursor), not `b`. The
// projectile homes onto its assigned target so only `a` takes damage.
func TestCursorPick_PicksNearestEnemyToCursor(t *testing.T) {
	gw, _ := newTestGameWorld()
	mmokit.Handle(gw.stage, damageHandler)

	abSys := wireAbilitySystem(t, gw)
	projSys := wireProjectileSystem(t, gw)

	player := spawnAbilityTestPlayer(t, gw, 5001, 1, 0, 0,
		item.PlasmaCannon, 0)
	// Both NPCs are inside the cursorPick search radius (5u) of NPC a's
	// position, and both are inside HomingMissile.Range=50 from caster.
	// Aim is exactly on a → server must pick a.
	a := spawnAbilityTestNPC(t, gw, 5002, 20, 0)
	b := spawnAbilityTestNPC(t, gw, 5003, 22, 3)

	aX, aY := a.Pos()
	setAbilityCast(t, player, gamecomp.AbilityW, aX, aY)

	// Tick 1: AbilitySystem dispatches cursor-pick, spawns homing missile
	// targeting a.
	gw.stage.TickOne(abSys, 0.05)

	// HomingMissile speed=30 u/s; target a is at 20u → ~0.67s travel.
	// Use small ticks so the projectile's hit-radius query catches a.
	for range 20 {
		gw.stage.TickOne(projSys, 0.05)
		if hp := mmokit.Get[gamecomp.Health](a); hp.Current < 100 {
			break
		}
	}

	if hp := mmokit.Get[gamecomp.Health](a); hp.Current >= 100 {
		t.Fatalf("expected target a (cursor-picked) damaged; got HP=%.1f", hp.Current)
	}
	if hp := mmokit.Get[gamecomp.Health](b); hp.Current < 100 {
		t.Fatalf("expected target b NOT damaged; got HP=%.1f", hp.Current)
	}
}

// TestCursorPick_EmptySpaceMisses — aim in empty space far from any
// entity. dispatchCursorPick returns false → executeAbility's caller
// does NOT stamp the cooldown, so AbilitySet.Cooldowns[slot] stays 0.
func TestCursorPick_EmptySpaceMisses(t *testing.T) {
	gw, _ := newTestGameWorld()
	mmokit.Handle(gw.stage, damageHandler)

	abSys := wireAbilitySystem(t, gw)

	player := spawnAbilityTestPlayer(t, gw, 5101, 1, 0, 0,
		item.PlasmaCannon, 0)

	// Aim far from any spawned entity; cursorPick's 5u radius is empty.
	setAbilityCast(t, player, gamecomp.AbilityW, 500.0, 500.0)

	gw.stage.TickOne(abSys, 0.05)

	ab := mmokit.Get[gamecomp.AbilitySet](player)
	if ab == nil {
		t.Fatal("player missing AbilitySet component")
	}
	if ab.Cooldowns[gamecomp.AbilityW] > 0 {
		t.Fatalf("expected slot W cooldown refunded (miss), got %v",
			ab.Cooldowns[gamecomp.AbilityW])
	}
}

// TestSelection_LeftClickSetsSelection — Selection round-trip: set
// EntityNetID, verify read; clear, verify cleared. Mirrors the
// SelectTarget wire-input handler's effect on the player's Selection
// component without exercising the full network path.
func TestSelection_LeftClickSetsSelection(t *testing.T) {
	gw, _ := newTestGameWorld()

	player := spawnAbilityTestPlayer(t, gw, 5201, 1, 0, 0, 0, 0)
	target := spawnAbilityTestNPC(t, gw, 5202, 10, 0)

	sel := mmokit.Get[gamecomp.Selection](player)
	if sel == nil {
		t.Fatal("player missing Selection component")
	}

	sel.EntityNetID = target.NetID()
	if got := mmokit.Get[gamecomp.Selection](player).EntityNetID; got != target.NetID() {
		t.Fatalf("expected selection=%v got %v", target.NetID(), got)
	}

	sel.EntityNetID = 0
	if got := mmokit.Get[gamecomp.Selection](player).EntityNetID; got != 0 {
		t.Fatalf("expected cleared selection; got %v", got)
	}
}

// TestAbility_BeamClipsAtLOSWall — a SustainedBeam channel aimed at an
// NPC must not deliver per-tick damage when a LayerStatic wall sits
// between caster and target. Without the wall, the same setup damages
// the NPC — that's the positive baseline sub-test. The LOS gate lives
// in tickChannels and uses the wider LayerStatic|LayerProp mask so
// asteroids (props) also block beam damage.
func TestAbility_BeamClipsAtLOSWall(t *testing.T) {
	run := func(t *testing.T, withWall bool) float32 {
		t.Helper()
		gw, _ := newTestGameWorld()
		mmokit.Handle(gw.stage, damageHandler)

		abSys := wireAbilitySystem(t, gw)

		// BeamMortarBattery primary = SustainedBeam (Range=30, Damage=4,
		// ChannelTickRate=10).
		player := spawnAbilityTestPlayer(t, gw, 6001, 1, 0, 0,
			item.BeamMortarBattery, 0)

		// Target sits 20u along +x — inside Range=30. NPC's spatial
		// entry has Layer=0 (test default) so it never matches the
		// LayerStatic|LayerProp raycast mask. That's exactly the case
		// the LOS gate is built for: the target is NOT itself a
		// blocker; only OTHER colliders should clip the beam.
		target := spawnAbilityTestNPC(t, gw, 6002, 20, 0)

		// Caster facing matches aim direction (along +x) so the ±120°
		// arc check in tickChannels passes.
		rot := mmokit.Get[mmokit.Rotation](player)
		rot.Angle = 0

		// Bypass dispatchSkillshotChannel + queued Commands so the test
		// is deterministic on a single tick — attach Channeling directly
		// with NextTickIn=0 so the very first tick is a damage tick.
		mmokit.Set(player, gamecomp.Channeling{
			SlotID:        gamecomp.AbilityQ, // BeamMortarBattery primary → slot Q
			RemainingTime: 3.0,
			NextTickIn:    0,
			AimX:          20,
			AimY:          0,
		})

		if withWall {
			// LayerStatic wall at (10,0) — sits squarely between caster
			// (0,0) and target (20,0). Mirrors the wall pattern in
			// system_npc_ai_test.go::TestNPCAI_LOSBlockedTargetNotAcquired.
			wallW := gw.stage.ECSWorld()
			wallE := wallW.NewEntity()
			gw.Spatial.Register(spatial.Entry{
				Entity: wallE,
				X:      10, Y: 0,
				Radius: 12, Width: 10, Height: 20,
				Shape: spatial.ShapeRect, Layer: spatial.LayerStatic,
			})
		}

		// One tick of AbilitySystem runs tickChannels — the damage gate
		// fires (NextTickIn=0) and the LOS gate either lets it through
		// or suppresses it.
		gw.stage.TickOne(abSys, 0.05)

		hp := mmokit.Get[gamecomp.Health](target)
		if hp == nil {
			t.Fatal("target missing Health after tick")
		}
		return hp.Current
	}

	// Positive baseline — beam lands.
	if got := run(t, false); got >= 100 {
		t.Fatalf("baseline (no wall): expected beam to damage target, got HP=%.1f", got)
	}

	// LOS gate — wall absorbs the beam.
	if got := run(t, true); got < 100 {
		t.Fatalf("LOS clip: expected wall to block beam (HP=100), got HP=%.1f", got)
	}
}

// TestMining_RequiresSelection — without a selection, MiningBeam toggle
// dispatch is refused by the inside-dispatchByType selection gate;
// MiningLaser.Beams[0].Active stays false and the replicated
// ActiveMining mirror is not flipped on.
func TestMining_RequiresSelection(t *testing.T) {
	gw, _ := newTestGameWorld()

	abSys := wireAbilitySystem(t, gw)

	player := spawnAbilityTestPlayer(t, gw, 5301, 1, 0, 0,
		item.StarterMiningLaser, 0)
	// Attach MiningLaser + ActiveMining so the toggle path doesn't bail
	// out early on missing components — we want the test to hit the
	// selection-required branch specifically.
	mmokit.Set(player, gamecomp.MiningLaser{})
	mmokit.Set(player, gamecomp.ActiveMining{})
	// No selection set — sel.EntityNetID == 0 → toggle should bail.

	setAbilityCast(t, player, gamecomp.AbilityQ, 0, 0)
	gw.stage.TickOne(abSys, 0.05)

	laser := mmokit.Get[gamecomp.MiningLaser](player)
	if laser == nil {
		t.Fatal("player missing MiningLaser component after spawn")
	}
	if laser.Beams[0].Active {
		t.Fatal("expected MiningBeam slot 0 NOT active without selection")
	}
	active := mmokit.Get[gamecomp.ActiveMining](player)
	if active != nil && active.Beam0Active {
		t.Fatal("expected ActiveMining.Beam0Active still false without selection")
	}
}
