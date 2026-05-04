package game

import (
	"testing"

	"github.com/mlange-42/ark/ecs"

	gamepb "github.com/zenion/mmoserver/gen/go/gamepb"
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

	// Authoritative victim on this cell — needs LockedBy and a NetworkID.
	victim := gw.eng.ECS.NewEntity()
	victimNetID := uint32(101)
	gw.C.NetworkID.Add(victim, &mmokit.NetworkID{ID: victimNetID})
	gw.C.LockedBy.Add(victim, &gamecomp.LockedBy{})

	// Border replica of the attacker. The Replica component marker matches
	// what ApplyBorderFrame leaves behind on the receiving cell. TargetEntity
	// is intentionally zero — that's exactly the post-replication state the
	// old code couldn't handle.
	attacker := gw.eng.ECS.NewEntity()
	attackerNetID := uint32(202)
	gw.C.NetworkID.Add(attacker, &mmokit.NetworkID{ID: attackerNetID})
	gw.C.Replica.Add(attacker, &mmokit.Replica{SourceCellID: "neighbor", SourceNetID: attackerNetID})
	gw.C.TargetLock.Add(attacker, &gamecomp.TargetLock{
		TargetEntity: ecs.Entity{}, // zero — matches reflect_marshal.go skip behavior
		TargetNetID:  victimNetID,
		Progress:     0.6,
	})

	// SpatialSystem normally rebuilds NetIDToEntity each tick; populate
	// manually here since this test bypasses the full game loop.
	gw.NetIDToEntity[victimNetID] = victim
	gw.NetIDToEntity[attackerNetID] = attacker

	ns := wireNetworkSystemForTest(t, gw)

	ns.beforeTick(0)

	got := gw.C.LockedBy.Get(victim)
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

	victim := gw.eng.ECS.NewEntity()
	gw.C.NetworkID.Add(victim, &mmokit.NetworkID{ID: 101})
	// Pre-populated as if a previous tick wrote the lock.
	gw.C.LockedBy.Add(victim, &gamecomp.LockedBy{LockerNetID: 202, LockerProgress: 0.6})

	gw.NetIDToEntity[101] = victim

	ns := wireNetworkSystemForTest(t, gw)
	ns.beforeTick(0)

	got := gw.C.LockedBy.Get(victim)
	if got.LockerNetID != 0 || got.LockerProgress != 0 {
		t.Fatalf("LockedBy not cleared: locker=%d progress=%.2f", got.LockerNetID, got.LockerProgress)
	}
}

// TestHandleCrossCellAction_StatusEffect_EnqueuesAnimation covers the DoT
// path — slot/abilityType ride along in the action payload so the victim's
// cell can fire the cast animation alongside the status-effect application.
func TestHandleCrossCellAction_StatusEffect_EnqueuesAnimation(t *testing.T) {
	gw, _ := newTestGameWorld()

	victim := gw.eng.ECS.NewEntity()
	gw.C.NetworkID.Add(victim, &mmokit.NetworkID{ID: 101})
	gw.C.StatusEffects.Add(victim, &gamecomp.StatusEffects{})
	gw.NetIDToEntity[101] = victim

	action := &mmokit.CrossCellAction{
		Type:         ActionStatusEffect,
		TargetNetID:  101,
		SourceNetID:  202,
		SourceCellID: "neighbor",
		Payload: MarshalStatusEffectAction(&StatusEffectAction{
			EffectType:  uint8(gamecomp.StatusIonBurn),
			Slot:        2,
			AbilityType: 7,
			Duration:    5,
			Value:       3,
		}),
	}

	if res := gw.HandleCrossCellAction(action); res == nil {
		t.Fatal("HandleCrossCellAction returned nil result")
	}

	events := mmokit.Peek[*gamepb.AbilityCastResultMsg](gw.Queue)
	if len(events) != 1 {
		t.Fatalf("AbilityCastResultMsg queue: got %d events, want 1", len(events))
	}
	evt := events[0]
	if evt.Slot != 2 || evt.AbilityType != 7 {
		t.Errorf("status-effect animation slot/type mismatch: slot=%d type=%d", evt.Slot, evt.AbilityType)
	}
}

// TestStatusEffectAction_Roundtrip pins the wire format change — Slot and
// AbilityType must survive marshal/unmarshal so HandleCrossCellAction's
// animation enqueue gets the right values.
func TestStatusEffectAction_Roundtrip(t *testing.T) {
	in := &StatusEffectAction{
		EffectType:  uint8(gamecomp.StatusIonBurn),
		Slot:        3,
		AbilityType: 9,
		Duration:    4.5,
		Value:       1.25,
	}
	out, err := UnmarshalStatusEffectAction(MarshalStatusEffectAction(in))
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if *out != *in {
		t.Fatalf("roundtrip mismatch:\n got %+v\nwant %+v", out, in)
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
	ns.SetDeps(gw.eng.ECS, gw.eng, gw)
	ns.BindQueries(ns)
	ns.ctx = &gameNetContext{lockedBy: make(map[ecs.Entity]lockerInfo)}
	ns.locks.With(mmokit.IncludeAll())
	ns.lockVictims.With(mmokit.IncludeAll())
	ns.BuildQueries()
	return ns
}

