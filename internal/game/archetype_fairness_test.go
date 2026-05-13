package game

import "testing"

// TestArchetypeAoEFairness — for any archetype emitting a telegraphed
// AoE with FairnessStrict or FairnessEffort, base-speed × cast-time ≥
// radius × fairness. FairnessFocusFire archetypes are skipped — they
// are intentionally not spatially dodgeable (counter-play is to kill
// or interrupt before resolution).
func TestArchetypeAoEFairness(t *testing.T) {
	cfg := DefaultGameConfig()
	for _, arch := range []uint8{ArchetypeBrawler, ArchetypeArtillery, ArchetypeKamikaze} {
		radius, castTime, fairness := archetypeAoEParams(&cfg, arch)
		if radius == 0 || castTime == 0 || fairness == FairnessFocusFire {
			continue
		}
		achievable := PlayerBaseSpeedForFairness * castTime
		required := radius * float32(fairness)
		if achievable < required {
			t.Errorf("archetype %d: fairness=%v requires %vu of movement during cast, "+
				"but base speed %v × cast %vs gives only %vu",
				arch, fairness, required, PlayerBaseSpeedForFairness, castTime, achievable)
		}
	}
}
