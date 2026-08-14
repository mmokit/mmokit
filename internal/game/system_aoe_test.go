package game

import (
	"testing"

	ecs "github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmokit/internal/component"
	"github.com/zenion/mmokit/pkg/mmokit"
	"github.com/zenion/mmokit/pkg/spatial"
)

// wireAoESystem constructs a freshly wired AoESystem against gw.stage. The
// system reads gw via mmokit.State[GameWorld](stage) in Init, which
// newTestGameWorld already wires up.
//
// Pre-registers Leashing on the ECS world — ApplyDamage queries it via
// mmokit.Has during the AoESystem iteration, and ark panics if the
// component type is registered for the first time while the world is
// locked by a running query.
func wireAoESystem(t *testing.T, gw *GameWorld) *AoESystem {
	t.Helper()
	w := gw.stage.ECSWorld()
	_ = ecs.NewMap1[gamecomp.Leashing](w) // force component registration

	sys := &AoESystem{}
	mmokit.WireSystem(sys, w, gw.eng, gw.stage)
	return sys
}

// spawnAoETestNPC creates an NPC-flavored entity at (x, y) with Health,
// Shield, an NPCAI marker (so factionMatch recognizes it as NPC), and a
// matching spatial-grid registration so QueryRadius returns it. Returns
// the rich Entity wrapper for assertions.
func spawnAoETestNPC(t *testing.T, gw *GameWorld, netID uint32, x, y float32) mmokit.Entity {
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
		&gamecomp.Shield{Current: 0, Max: 0}, // shield down so all damage hits health
	)
	gw.stage.RegisterLiveNetID(netID, handle)
	e := mmokit.EntityFromECS(gw.stage, handle)
	// Mark as NPC for AoESystem.factionMatch.
	mmokit.Set(e, gamecomp.NPCAI{})
	gw.Spatial.Register(spatial.Entry{Entity: handle, X: x, Y: y, Radius: 1})
	return e
}

// spawnAoETestPlayer creates a player-flavored entity at (x, y) with
// Health, Shield, a PlayerConn (so factionMatch recognizes it as player),
// and a spatial-grid registration.
func spawnAoETestPlayer(t *testing.T, gw *GameWorld, netID, connID uint32, x, y float32) mmokit.Entity {
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
		&gamecomp.Shield{Current: 0, Max: 0},
	)
	gw.stage.RegisterLiveNetID(netID, handle)
	e := mmokit.EntityFromECS(gw.stage, handle)
	mmokit.Set(e, mmokit.PlayerConn{ConnID: connID})
	gw.Spatial.Register(spatial.Entry{Entity: handle, X: x, Y: y, Radius: 1})
	return e
}

// TestAoESystem_NotExpired_NoDamage — a marker with positive lifetime
// must not apply damage on tick.
func TestAoESystem_NotExpired_NoDamage(t *testing.T) {
	gw, _ := newTestGameWorld()
	sys := wireAoESystem(t, gw)

	npc := spawnAoETestNPC(t, gw, 5001, 100, 100)

	// Marker with positive lifetime — must not resolve yet.
	gw.SpawnAoEMarker(100, 100, 1.0, 50, 40, 0, FactionMaskAll, 0)

	gw.stage.TickOne(sys, 0.1)

	if got := mmokit.Get[gamecomp.Health](npc).Current; got != 100 {
		t.Fatalf("NPC Health = %.0f, want 100 (marker not expired)", got)
	}
}

// TestAoESystem_Expired_DamagesInRadius — when lifetime hits 0, all
// non-owner entities in radius take damage; entities outside radius
// are untouched.
func TestAoESystem_Expired_DamagesInRadius(t *testing.T) {
	gw, _ := newTestGameWorld()
	sys := wireAoESystem(t, gw)

	inside := spawnAoETestNPC(t, gw, 6001, 100, 100)  // at center
	outside := spawnAoETestNPC(t, gw, 6002, 300, 300) // ~283u away — outside r=50

	gw.SpawnAoEMarker(100, 100, 0, 50, 40, 0, FactionMaskAll, 0)

	gw.stage.TickOne(sys, 0.1)

	if got := mmokit.Get[gamecomp.Health](inside).Current; got != 60 {
		t.Fatalf("inside NPC Health = %.0f, want 60 (took 40 dmg)", got)
	}
	if got := mmokit.Get[gamecomp.Health](outside).Current; got != 100 {
		t.Fatalf("outside NPC Health = %.0f, want 100 (out of radius)", got)
	}
}

// TestAoESystem_OwnerNotDamaged — the entity matching OwnerNetID is
// always skipped, even if inside the radius.
func TestAoESystem_OwnerNotDamaged(t *testing.T) {
	gw, _ := newTestGameWorld()
	sys := wireAoESystem(t, gw)

	owner := spawnAoETestNPC(t, gw, 7001, 100, 100)

	gw.SpawnAoEMarker(100, 100, 0, 50, 40, owner.NetID(), FactionMaskAll, 0)

	gw.stage.TickOne(sys, 0.1)

	if got := mmokit.Get[gamecomp.Health](owner).Current; got != 100 {
		t.Fatalf("owner Health = %.0f, want 100 (owner must be skipped)", got)
	}
}

// TestAoESystem_ProductionOrder_LifetimeDecrementThenResolve — pins the
// system-order contract from factory.go: LifetimeSystem runs BEFORE
// AoESystem so that on the tick where Remaining drops to 0, AoESystem
// can still see the marker (FlushRemovals hasn't run yet) and apply
// damage. The reverse order silently dropped every non-instant AoE
// because the marker was despawned by LifetimeSystem's removal queue
// before AoESystem next looked at it.
func TestAoESystem_ProductionOrder_LifetimeDecrementThenResolve(t *testing.T) {
	gw, _ := newTestGameWorld()
	mmokit.Handle(gw.stage, damageHandler)
	aoeSys := wireAoESystem(t, gw)
	lifeSys := &mmokit.LifetimeSystem{}
	mmokit.WireSystem(lifeSys, gw.stage.ECSWorld(), gw.eng, gw.stage)

	npc := spawnAoETestNPC(t, gw, 9001, 100, 100)

	// 0.6s lifetime mirrors the player Mortar GroundCastDelay.
	gw.SpawnAoEMarker(100, 100, 0.6, 8, 40, 0, FactionMaskAll, 0)

	// Production order: LifetimeSystem decrements, then AoESystem
	// resolves expired markers. Tick at 20Hz (dt=0.05) for ~14 ticks so
	// the 0.6s window is reliably crossed.
	for range 14 {
		gw.stage.TickOne(lifeSys, 0.05)
		gw.stage.TickOne(aoeSys, 0.05)
		if hp := mmokit.Get[gamecomp.Health](npc).Current; hp < 100 {
			break
		}
	}

	if hp := mmokit.Get[gamecomp.Health](npc).Current; hp >= 100 {
		t.Fatalf("expected NPC damaged after 0.6s lifetime expired in production order; got HP=%.1f", hp)
	}
}

// TestAoESystem_FactionMask_NPCOnly — a marker with FactionMaskNPC
// hits NPCs but not players.
func TestAoESystem_FactionMask_NPCOnly(t *testing.T) {
	gw, _ := newTestGameWorld()
	sys := wireAoESystem(t, gw)

	npc := spawnAoETestNPC(t, gw, 8001, 100, 100)
	player := spawnAoETestPlayer(t, gw, 8002, 1, 110, 110)

	gw.SpawnAoEMarker(100, 100, 0, 50, 40, 0, FactionMaskNPC, 0)

	gw.stage.TickOne(sys, 0.1)

	if got := mmokit.Get[gamecomp.Health](npc).Current; got != 60 {
		t.Fatalf("NPC Health = %.0f, want 60 (NPC must be hit)", got)
	}
	if got := mmokit.Get[gamecomp.Health](player).Current; got != 100 {
		t.Fatalf("player Health = %.0f, want 100 (player must be skipped under FactionMaskNPC)", got)
	}
}
