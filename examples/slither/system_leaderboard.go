package main

import (
	"sort"

	"github.com/zenion/mmoserver/pkg/mmokit"
)

// LeaderboardSystem periodically builds a sorted top-10 leaderboard.
type LeaderboardSystem struct {
	mmokit.SystemBase
	gw       *SlitherWorld
	entities mmokit.Query[struct {
		State *SnakeState
		NetID *mmokit.NetworkID
	}]
}

func (s *LeaderboardSystem) Init() {
	s.gw = s.GameWorld().(*SlitherWorld)
	// Include replicas for better coverage of cross-cell snakes.
	s.entities.Init(s, mmokit.IncludeAll(), mmokit.Without[mmokit.Ghost]())
}

func (s *LeaderboardSystem) Update(dt float32) {
	gw := s.gw
	interval := gw.Cfg.LeaderboardInterval
	if interval <= 0 || gw.Engine().Tick%uint32(interval) != 0 {
		return
	}

	type entry struct {
		netID uint32
		state LeaderEntry
	}

	var entries []entry
	for _, b := range s.entities.All() {
		entries = append(entries, entry{
			netID: b.NetID.ID,
			state: LeaderEntry{
				Name:   b.State.Name,
				Mass:   b.State.Mass,
				SkinID: b.State.SkinID,
			},
		})
	}

	// Sort by mass descending.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].state.Mass > entries[j].state.Mass
	})

	// Deduplicate by NetworkID and keep top 10.
	seen := make(map[uint32]bool)
	gw.Leaderboard = gw.Leaderboard[:0]
	for _, e := range entries {
		if seen[e.netID] {
			continue
		}
		seen[e.netID] = true
		gw.Leaderboard = append(gw.Leaderboard, e.state)
		if len(gw.Leaderboard) >= 10 {
			break
		}
	}
}
