package game

import (
	"testing"

	"github.com/mmokit/mmokit"
	gamecomp "github.com/mmokit/mmokit/examples/space/internal/component"
)

// TestSupport_HealsLowestHPRosterAlly — a Support NPC anchored to a POI
// must, after its first heal cooldown, raise the HP of the lowest-HP
// roster ally (excluding itself, dead targets, and full-HP allies). A
// healthy roster ally must not be touched.
func TestSupport_HealsLowestHPRosterAlly(t *testing.T) {
	gw, _ := newTestGameWorld()
	defer gw.Shutdown()
	mmokit.Handle(gw.stage, healHandler) // newTestGameWorld doesn't wire RegisterHealVerb

	poiNetID := gw.SpawnPOIWithRoster(500, 500, 0, StarterArenaIdx, 1)
	support := gw.SpawnNPC(500, 500, ArchetypeSupport, poiNetID, NPCSpawnModifiers{})

	// Find two non-Support roster members anchored to this POI.
	var injured, healthy mmokit.Entity
	mmokit.ForEach1(gw.stage, func(e mmokit.Entity, _ *gamecomp.NPCAI) {
		if e.NetID() == support.NetID() {
			return
		}
		a := mmokit.Get[gamecomp.DungeonAnchor](e)
		if a == nil || a.DungeonNetID != poiNetID {
			return
		}
		if injured.NetID() == 0 {
			injured = e
		} else if healthy.NetID() == 0 {
			healthy = e
		}
	})
	if !injured.Alive() || !healthy.Alive() {
		t.Skip("not enough roster members for test")
	}

	// Damage `injured` to half HP.
	hi := mmokit.Get[gamecomp.Health](injured)
	hi.Current = hi.Max / 2
	prevInjured := hi.Current
	hh := mmokit.Get[gamecomp.Health](healthy)
	prevHealthy := hh.Current

	// Tick the AI system long enough to fire at least one heal. Each
	// tick is SupportHealCooldown/50 (~80ms at default 4.0s cooldown);
	// 100 ticks = ~8s = 2 heal windows.
	ai, _ := newWiredNPCAISystem(t, gw)
	dt := gw.Config.SupportHealCooldown / 50
	for i := 0; i < 100; i++ {
		gw.stage.TickOne(ai, dt)
	}

	gotInjured := mmokit.Get[gamecomp.Health](injured).Current
	gotHealthy := mmokit.Get[gamecomp.Health](healthy).Current
	if gotInjured <= prevInjured {
		t.Fatalf("injured HP %v not increased from %v", gotInjured, prevInjured)
	}
	if absf(gotHealthy-prevHealthy) > 0.1 {
		t.Fatalf("healthy ally HP changed: %v != %v (support should pick lowest)",
			gotHealthy, prevHealthy)
	}
}

// TestSupport_NoTargetWhenAllAtFull — if every roster ally is at full HP,
// the Support NPC must NOT heal anyone (no spurious heal overcap nor
// any other HP perturbation).
func TestSupport_NoTargetWhenAllAtFull(t *testing.T) {
	gw, _ := newTestGameWorld()
	defer gw.Shutdown()
	mmokit.Handle(gw.stage, healHandler)

	poiNetID := gw.SpawnPOIWithRoster(500, 500, 0, StarterArenaIdx, 1)
	support := gw.SpawnNPC(500, 500, ArchetypeSupport, poiNetID, NPCSpawnModifiers{})

	hpBefore := map[uint32]float32{}
	mmokit.ForEach1(gw.stage, func(e mmokit.Entity, _ *gamecomp.NPCAI) {
		if e.NetID() == support.NetID() {
			return
		}
		a := mmokit.Get[gamecomp.DungeonAnchor](e)
		if a == nil || a.DungeonNetID != poiNetID {
			return
		}
		hpBefore[e.NetID()] = mmokit.Get[gamecomp.Health](e).Current
	})

	ai, _ := newWiredNPCAISystem(t, gw)
	dt := gw.Config.SupportHealCooldown / 50
	for i := 0; i < 100; i++ {
		gw.stage.TickOne(ai, dt)
	}

	mmokit.ForEach1(gw.stage, func(e mmokit.Entity, _ *gamecomp.NPCAI) {
		if e.NetID() == support.NetID() {
			return
		}
		want, ok := hpBefore[e.NetID()]
		if !ok {
			return
		}
		got := mmokit.Get[gamecomp.Health](e).Current
		if absf(got-want) > 0.1 {
			t.Fatalf("full-HP ally %d changed: %v -> %v", e.NetID(), want, got)
		}
	})
}
