package game

import (
	"testing"

	gamecomp "github.com/zenion/mmokit/examples/space/internal/component"
	"github.com/zenion/mmokit/pkg/mmokit"
)

// TestHeal_RestoresHP exercises gw.Heal against a same-cell target — same
// pattern as TestDamage_SameCell_AppliesViaSend (verb_damage_test.go). The
// handler is registered manually here because newTestGameWorld doesn't go
// through GameSetup → RegisterHealVerb; production wires it via factory.go.
func TestHeal_RestoresHP(t *testing.T) {
	gw, _ := newTestGameWorld()
	mmokit.Handle(gw.stage, healHandler)

	target := gw.SpawnNPC(0, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{})
	h := mmokit.Get[gamecomp.Health](target)
	h.Current = h.Max / 2
	prevHP := h.Current

	caster := gw.SpawnNPC(50, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{})

	gw.Heal(caster, target, 30)

	got := mmokit.Get[gamecomp.Health](target).Current
	want := prevHP + 30
	if absf(got-want) > 0.1 {
		t.Fatalf("HP after heal = %v, want %v", got, want)
	}
}

func TestHeal_CapsAtMax(t *testing.T) {
	gw, _ := newTestGameWorld()
	mmokit.Handle(gw.stage, healHandler)

	target := gw.SpawnNPC(0, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{})
	h := mmokit.Get[gamecomp.Health](target)
	h.Current = h.Max - 5
	hMax := h.Max

	caster := gw.SpawnNPC(50, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{})
	gw.Heal(caster, target, 100)

	got := mmokit.Get[gamecomp.Health](target).Current
	if absf(got-hMax) > 0.1 {
		t.Fatalf("HP after over-heal = %v, want max=%v", got, hMax)
	}
}

func TestHeal_DeadTargetNoOp(t *testing.T) {
	gw, _ := newTestGameWorld()
	mmokit.Handle(gw.stage, healHandler)

	target := gw.SpawnNPC(0, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{})
	mmokit.Get[gamecomp.Health](target).Current = 0

	caster := gw.SpawnNPC(50, 0, ArchetypeBrawler, 0, NPCSpawnModifiers{})
	gw.Heal(caster, target, 30) // must not raise from 0

	got := mmokit.Get[gamecomp.Health](target).Current
	if got != 0 {
		t.Fatalf("dead target healed: HP = %v, want 0", got)
	}
}
