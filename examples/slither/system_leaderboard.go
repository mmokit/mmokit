package main

import (
	"sort"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// LeaderboardSystem periodically builds a sorted top-10 leaderboard.
type LeaderboardSystem struct {
	mmokit.SystemBase
	gw     *SlitherWorld
	filter *ecs.Filter2[SnakeState, mmokit.NetworkID]
}

func (s *LeaderboardSystem) Init() {
	s.gw = s.GameWorld().(*SlitherWorld)
	// Include replicas for better coverage of cross-node snakes.
	s.filter = ecs.NewFilter2[SnakeState, mmokit.NetworkID](s.ECSWorld()).
		Without(ecs.C[mmokit.Ghost]())
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
	query := s.filter.Query()
	for query.Next() {
		state, netID := query.Get()
		entries = append(entries, entry{
			netID: netID.ID,
			state: LeaderEntry{
				Name:   state.Name,
				Mass:   state.Mass,
				SkinID: state.SkinID,
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
