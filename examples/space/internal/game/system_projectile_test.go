package game

import (
	"testing"

	ecs "github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmokit/examples/space/internal/component"
	"github.com/zenion/mmokit/pkg/mmokit"
	"github.com/zenion/mmokit/pkg/spatial"
)

// wireProjectileSystem constructs a freshly wired ProjectileSystem
// against gw.stage. Pre-registers Leashing on the ECS world for the
// same reason as the AoE test scaffold — ApplyDamage queries it via
// mmokit.Has during the iteration and ark panics if the component
// type is registered for the first time while the world is locked
// by a running query.
func wireProjectileSystem(t *testing.T, gw *GameWorld) *ProjectileSystem {
	t.Helper()
	w := gw.stage.ECSWorld()
	_ = ecs.NewMap1[gamecomp.Leashing](w) // force component registration

	sys := &ProjectileSystem{}
	mmokit.WireSystem(sys, w, gw.eng, gw.stage)
	return sys
}

// spawnProjectileTestNPC mirrors spawnAoETestNPC: an NPC-flavored
// entity at (x, y) with Health, Shield, an NPCAI marker, and a
// matching spatial-grid registration so ProjectileSystem's
// QueryRadius returns it.
func spawnProjectileTestNPC(t *testing.T, gw *GameWorld, netID uint32, x, y float32) mmokit.Entity {
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
		&gamecomp.Shield{Current: 0, Max: 0}, // shield down so damage hits health
	)
	gw.stage.RegisterLiveNetID(netID, handle)
	e := mmokit.EntityFromECS(gw.stage, handle)
	mmokit.Set(e, gamecomp.NPCAI{})
	gw.Spatial.Register(spatial.Entry{Entity: handle, X: x, Y: y, Radius: 1})
	return e
}

// TestProjectile_LinearMotion — a projectile with no homing moves
// in a straight line according to its velocity each tick.
func TestProjectile_LinearMotion(t *testing.T) {
	gw, _ := newTestGameWorld()
	sys := wireProjectileSystem(t, gw)

	proj := gw.SpawnProjectile(0, 0, 100, 0, 5.0, gamecomp.ProjectileSpec{
		OwnerNetID: 0,
		Damage:     10,
		Type:       gamecomp.ProjectileTypePlasma,
	})

	gw.stage.TickOne(sys, 0.5)

	if !proj.Alive() {
		t.Fatal("projectile should still be alive (no impact, lifetime > 0)")
	}
	pos := mmokit.Get[mmokit.Position](proj)
	if pos == nil {
		t.Fatal("projectile lost Position component")
	}
	// Expect ~50 (100 u/s * 0.5 s); loose bound for any future drift.
	if pos.X < 49.0 || pos.X > 51.0 {
		t.Fatalf("projectile X = %.3f, want ~50.0", pos.X)
	}
	if pos.Y < -0.5 || pos.Y > 0.5 {
		t.Fatalf("projectile Y = %.3f, want ~0", pos.Y)
	}
}

// TestProjectile_HitsAndDamages — projectile collides with an entity
// in its path and applies Damage.
func TestProjectile_HitsAndDamages(t *testing.T) {
	gw, _ := newTestGameWorld()
	sys := wireProjectileSystem(t, gw)

	target := spawnProjectileTestNPC(t, gw, 9001, 50, 0)

	proj := gw.SpawnProjectile(0, 0, 200, 0, 5.0, gamecomp.ProjectileSpec{
		OwnerNetID: 0,
		Damage:     30,
		Type:       gamecomp.ProjectileTypePlasma,
	})

	// dt = 0.25 → projectile travels 50u and lands ON the target;
	// QueryRadius(4) matches a point-source target at distance 0.
	gw.stage.TickOne(sys, 0.25)

	got := mmokit.Get[gamecomp.Health](target).Current
	if got >= 100 {
		t.Fatalf("target Health = %.0f, want < 100 (projectile must hit)", got)
	}
	if got != 70 {
		t.Fatalf("target Health = %.0f, want 70 (100 - 30 damage)", got)
	}
	if proj.Alive() {
		t.Fatal("projectile should be despawned after impact")
	}
}

// TestProjectile_SplashDamage — on impact, a SplashRadius>0
// projectile spawns an AoEMarker that damages bystanders.
func TestProjectile_SplashDamage(t *testing.T) {
	gw, _ := newTestGameWorld()
	projSys := wireProjectileSystem(t, gw)
	aoeSys := wireAoESystem(t, gw)

	directHit := spawnProjectileTestNPC(t, gw, 9101, 50, 0)
	bystander := spawnProjectileTestNPC(t, gw, 9102, 50, 20) // within r=40 of impact

	gw.SpawnProjectile(0, 0, 200, 0, 5.0, gamecomp.ProjectileSpec{
		OwnerNetID:   0,
		Damage:       30,
		SplashRadius: 40,
		SplashDamage: 20,
		Type:         gamecomp.ProjectileTypeMortar,
	})

	// Tick the projectile to impact: travels 50u, hits directHit, spawns
	// AoEMarker(lifetime=0) at (50, 0).
	gw.stage.TickOne(projSys, 0.25)

	if got := mmokit.Get[gamecomp.Health](directHit).Current; got != 70 {
		t.Fatalf("direct-hit target Health = %.0f, want 70 (took 30 dmg)", got)
	}
	if got := mmokit.Get[gamecomp.Health](bystander).Current; got != 100 {
		t.Fatalf("bystander Health = %.0f, want 100 BEFORE AoE tick", got)
	}

	// Now tick the AoE system to resolve the splash marker.
	gw.stage.TickOne(aoeSys, 0.1)

	if got := mmokit.Get[gamecomp.Health](directHit).Current; got != 50 {
		t.Fatalf("direct-hit Health after splash = %.0f, want 50 (70 - 20 splash)", got)
	}
	if got := mmokit.Get[gamecomp.Health](bystander).Current; got != 80 {
		t.Fatalf("bystander Health after splash = %.0f, want 80 (100 - 20 splash)", got)
	}
}

// TestProjectile_HomingFollows — a missile with non-zero MaxTurnRate
// and a target should turn its velocity toward the target.
func TestProjectile_HomingFollows(t *testing.T) {
	gw, _ := newTestGameWorld()
	sys := wireProjectileSystem(t, gw)

	target := spawnProjectileTestNPC(t, gw, 9201, 100, 100)

	proj := gw.SpawnProjectile(0, 0, 200, 0, 5.0, gamecomp.ProjectileSpec{
		OwnerNetID:  0,
		TargetNetID: target.NetID(),
		Damage:      10,
		MaxTurnRate: 3.14, // ~180°/s
		Type:        gamecomp.ProjectileTypeMissile,
	})

	gw.stage.TickOne(sys, 0.1)

	if !proj.Alive() {
		t.Fatal("projectile despawned unexpectedly (no hit at this distance)")
	}
	vel := mmokit.Get[mmokit.Velocity](proj)
	if vel == nil {
		t.Fatal("projectile lost Velocity component")
	}
	if vel.Y <= 0 {
		t.Fatalf("velocity Y = %.3f, want > 0 (must turn toward y=100 target)", vel.Y)
	}
}
