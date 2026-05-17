package game

import "testing"

func minimalDungeonCfg() *GameConfig {
	return &GameConfig{
		DungeonChamberCountMin:  5,
		DungeonChamberCountMax:  8,
		DungeonChamberRadiusMin: 120,
		DungeonChamberRadiusMax: 240,
		DungeonAsteroidRadius:   1800,
		DungeonCorridorWidth:    100,
		DungeonEntranceCount:    3,
		DungeonWallThickness:    30,
	}
}

func TestDungeonGen_DeterministicGraph(t *testing.T) {
	cfg := minimalDungeonCfg()
	g1 := buildDungeonGraph(cfg, 12345)
	g2 := buildDungeonGraph(cfg, 12345)
	if len(g1.chambers) != len(g2.chambers) {
		t.Fatalf("seed 12345 produced different chamber counts")
	}
	for i := range g1.chambers {
		if g1.chambers[i].role != g2.chambers[i].role {
			t.Fatalf("seed 12345 produced different chamber roles at %d", i)
		}
	}
}

func TestDungeonGen_ChamberCountInRange(t *testing.T) {
	cfg := minimalDungeonCfg()
	for seed := uint64(0); seed < 100; seed++ {
		g := buildDungeonGraph(cfg, seed)
		n := len(g.chambers)
		if n < cfg.DungeonChamberCountMin || n > cfg.DungeonChamberCountMax {
			t.Fatalf("seed %d produced chamber count %d outside [%d,%d]", seed, n, cfg.DungeonChamberCountMin, cfg.DungeonChamberCountMax)
		}
	}
}

func TestDungeonGen_ExactlyOneEntryAndOneTerminal(t *testing.T) {
	cfg := minimalDungeonCfg()
	for seed := uint64(0); seed < 50; seed++ {
		g := buildDungeonGraph(cfg, seed)
		entryCount, termCount := 0, 0
		for _, c := range g.chambers {
			if c.isEntry {
				entryCount++
			}
			if c.role == ChamberTerminal {
				termCount++
			}
		}
		if entryCount != 1 {
			t.Fatalf("seed %d: expected 1 entry chamber, got %d", seed, entryCount)
		}
		if termCount != 1 {
			t.Fatalf("seed %d: expected 1 terminal chamber, got %d", seed, termCount)
		}
	}
}

func TestDungeonGen_WallCountUnder40(t *testing.T) {
	cfg := minimalDungeonCfg()
	for seed := uint64(0); seed < 30; seed++ {
		g := buildDungeonGraph(cfg, seed)
		walls := generateWalls(g, cfg)
		if len(walls) > 40 {
			t.Fatalf("seed %d produced %d walls (>40)", seed, len(walls))
		}
	}
}

// Every chamber must be reachable from chamber 0 via the edge graph.
func TestDungeonGen_AllChambersReachable(t *testing.T) {
	cfg := minimalDungeonCfg()
	for seed := uint64(0); seed < 1000; seed++ {
		g := buildDungeonGraph(cfg, seed)
		d := bfsDistances(g, 0)
		for i, dist := range d {
			if dist < 0 {
				t.Fatalf("seed %d: chamber %d unreachable", seed, i)
			}
		}
	}
}
