package game

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// TestNetworkSystem_LockedByPopulated_FromReplicaLocker is the regression test
// for the cross-cell-lock-visibility bug: when the locker is a replica on the
// victim's cell (i.e. attacker authoritative on a neighboring cell), beforeTick
// must still populate the local victim's LockedBy via NetID lookup. Previously
// the loop used the locker's TargetEntity field, which the cross-cell
// component-tail codec skips (it's an ecs.Entity handle), leaving it zero on
// the replica — so the victim never saw the "you are being locked" state.
func TestNetworkSystem_LockedByPopulated_FromReplicaLocker(t *testing.T) {
	gw, _ := newTestGameWorld()

	w := gw.stage.ECSWorld()
	netIDMap := ecs.NewMap1[mmokit.NetworkID](w)

	// Authoritative victim on this cell — needs LockedBy and a NetworkID.
	victim := w.NewEntity()
	victimNetID := uint32(101)
	netIDMap.Add(victim, &mmokit.NetworkID{ID: victimNetID})
	gw.stage.RegisterLiveNetID(victimNetID, victim)
	mmokit.Set(mmokit.EntityFromECS(gw.stage, victim), gamecomp.LockedBy{})

	// Border replica of the attacker. The Replica component marker matches
	// what ApplyBorderFrame leaves behind on the receiving cell. TargetEntity
	// is intentionally zero — that's exactly the post-replication state the
	// old code couldn't handle.
	attacker := w.NewEntity()
	attackerNetID := uint32(202)
	netIDMap.Add(attacker, &mmokit.NetworkID{ID: attackerNetID})
	gw.stage.RegisterLiveNetID(attackerNetID, attacker)
	attackerE := mmokit.EntityFromECS(gw.stage, attacker)
	mmokit.Set(attackerE, mmokit.Replica{SourceCellID: "neighbor", SourceNetID: attackerNetID})
	mmokit.Set(attackerE, gamecomp.TargetLock{
		TargetEntity: ecs.Entity{}, // zero — matches reflect_marshal.go skip behavior
		TargetNetID:  victimNetID,
		Progress:     0.6,
	})

	ns := wireNetworkSystemForTest(t, gw)

	ns.beforeTick(0)

	got := mmokit.Get[gamecomp.LockedBy](mmokit.EntityFromECS(gw.stage, victim))
	if got.LockerNetID != attackerNetID {
		t.Fatalf("LockedBy.LockerNetID: got %d, want %d (cross-cell locker via replica)", got.LockerNetID, attackerNetID)
	}
	if got.LockerProgress != 0.6 {
		t.Fatalf("LockedBy.LockerProgress: got %.2f, want 0.6", got.LockerProgress)
	}
}

// TestNetworkSystem_LockedByCleared_WhenLockerStops verifies the symmetric
// teardown: once the replica's TargetLock no longer references the victim,
// the next beforeTick must zero LockedBy. Without the cross-cell fix, a stale
// LockerNetID written via the local-replica-of-victim path could persist.
func TestNetworkSystem_LockedByCleared_WhenLockerStops(t *testing.T) {
	gw, _ := newTestGameWorld()

	w := gw.stage.ECSWorld()
	victim := w.NewEntity()
	ecs.NewMap1[mmokit.NetworkID](w).Add(victim, &mmokit.NetworkID{ID: 101})
	gw.stage.RegisterLiveNetID(101, victim)
	// Pre-populated as if a previous tick wrote the lock.
	mmokit.Set(mmokit.EntityFromECS(gw.stage, victim), gamecomp.LockedBy{LockerNetID: 202, LockerProgress: 0.6})

	ns := wireNetworkSystemForTest(t, gw)
	ns.beforeTick(0)

	got := mmokit.Get[gamecomp.LockedBy](mmokit.EntityFromECS(gw.stage, victim))
	if got.LockerNetID != 0 || got.LockerProgress != 0 {
		t.Fatalf("LockedBy not cleared: locker=%d progress=%.2f", got.LockerNetID, got.LockerProgress)
	}
}

// wireNetworkSystemForTest constructs a NetworkSystem and runs only the
// state initialization beforeTick depends on — query bindings, ctx,
// IncludeAll for replica visibility. Skips NetworkSystem.Init (which
// builds the full ReplicationSystem and needs a Process / ClusterClock /
// viewer source the unit-test world doesn't have).
func wireNetworkSystemForTest(t *testing.T, gw *GameWorld) *NetworkSystem {
	t.Helper()
	ns := &NetworkSystem{}
	ns.SetDeps(gw.eng.ECS, gw.eng)
	ns.InitStage(gw.stage)
	ns.gw = gw
	ns.BindQueries(ns)
	ns.ctx = &gameNetContext{lockedBy: make(map[ecs.Entity]lockerInfo)}
	ns.locks.With(mmokit.IncludeAll())
	ns.lockVictims.With(mmokit.IncludeAll())
	ns.BuildQueries()
	return ns
}

