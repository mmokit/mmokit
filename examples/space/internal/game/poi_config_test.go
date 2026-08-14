package game

import "testing"

func TestRosterIdxForName(t *testing.T) {
	cases := []struct {
		name string
		want uint16
	}{
		{"Starter Arena", StarterArenaIdx},
		{"StarterArena", StarterArenaIdx}, // space-insensitive
		{"Small Skirmish", SmallSkirmishIdx},
		{"SmallSkirmish", SmallSkirmishIdx},
		{"Medium Warband", MediumWarbandIdx},
		{"Disruptor Cell", DisruptorCellIdx},
		{"Heavy Battalion", HeavyBattalionIdx},
		{"Elite Anchor", EliteAnchorIdx},
		{"nonexistent", StarterArenaIdx}, // fallback
		{"", StarterArenaIdx},            // fallback
	}
	for _, c := range cases {
		if got := RosterIdxForName(c.name); got != c.want {
			t.Errorf("RosterIdxForName(%q) = %d, want %d", c.name, got, c.want)
		}
	}
}
