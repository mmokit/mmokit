package main

import (
	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// NetworkSystem wraps the generic ReplicationSystem with slither-specific
// lifecycle handling (dead players, ghost cleanup, leaderboard, kill feed).
type NetworkSystem struct {
	mmokit.SystemBase
	gw      *SlitherWorld
	replSys *mmokit.ReplicationSystem
}

func (s *NetworkSystem) Init() {
	s.gw = s.GameWorld().(*SlitherWorld)
	gw := s.gw

	// Register entity replicators.
	replicators := mmokit.NewReplicatorRegistry()
	snakeRep := &snakeReplicator{gw: gw}
	replicators.Register(snakeRep)                              // KindPlayerSnake = 0
	replicators.RegisterForType(KindBotSnake, snakeRep)         // same handler for bots
	foodRep := &foodReplicator{gw: gw}
	replicators.Register(foodRep)                               // KindNaturalFood = 2
	replicators.RegisterForType(KindDeathFood, foodRep)         // same handler for death food

	cfg := mmokit.DefaultReplicationConfig(gw.Engine(), gw.Spatial)
	cfg.Replicators = replicators
	cfg.AoIRadius = gw.Cfg.AoIRadius
	cfg.FullRefreshInterval = 20 // keyframe every 20 ticks (~1 second)
	cfg.DormancyThreshold = 60   // 3 seconds at 20Hz
	cfg.SnapshotBufSize = 4096
	cfg.OnBeforeTick = func(tick uint32) {
		s.handleDeadAndGhostPlayers(tick)
	}
	cfg.OnAfterSend = func(viewer *mmokit.ViewerInfo, visible map[uint32]bool) {
		s.sendExtras(viewer)
	}
	cfg.OnAfterTick = func(tick uint32) {
		pm := gw.Engine().Players
		activeCount := pm.Count(mmokit.StateActive)
		if activeCount > 0 {
			gw.Engine().Log.Log(CatGameNetwork, "tick=%d sent to %d players snakes=%d",
				tick, activeCount, s.countSnakes())
		}
	}
	s.replSys = mmokit.NewReplicationSystem(cfg)
}

func (s *NetworkSystem) Update(dt float32) {
	s.replSys.Update(dt)
}

// handleDeadAndGhostPlayers cleans up players whose entities died or are transferring.
func (s *NetworkSystem) handleDeadAndGhostPlayers(tick uint32) {
	gw := s.gw
	pm := gw.Engine().Players
	ghostMap := gw.GhostMap()

	var toTransition []*mmokit.PlayerSession
	pm.ForEach(mmokit.StateActive, func(sess *mmokit.PlayerSession) {
		entity := sess.Entity
		if !gw.ECSWorld().Alive(entity) {
			// Entity was removed — send farewell with all previously visible entities.
			sendFarewell(gw, s.replSys, sess.ConnID, tick)
			toTransition = append(toTransition, sess)
			return
		}
		if ghostMap.HasAll(entity) {
			// Entity is being transferred to another node — clear from session.
			sess.Entity = ecs.Entity{}
			return
		}
	})

	for _, sess := range toTransition {
		sess.Entity = ecs.Entity{}
		_ = pm.Transition(sess, mmokit.StateDead)
	}
}

// sendExtras sends leaderboard and kill feed after each viewer's world update.
func (s *NetworkSystem) sendExtras(viewer *mmokit.ViewerInfo) {
	gw := s.gw
	cm := gw.Engine().ConnMgr
	tick := gw.Engine().Tick

	sendLeaderboard := gw.Cfg.LeaderboardInterval > 0 &&
		tick%uint32(gw.Cfg.LeaderboardInterval) == 0

	if sendLeaderboard && len(gw.Leaderboard) > 0 {
		cm.Send(viewer.ConnID, buildLeaderboard(gw.Leaderboard))
	}

	if len(gw.KillFeed) > 0 {
		cm.Send(viewer.ConnID, buildKillFeed(gw.KillFeed))
	}
}

// countSnakes returns the number of non-ghost, non-replica snake entities.
func (s *NetworkSystem) countSnakes() int {
	count := 0
	filter := ecs.NewFilter1[SnakeState](s.gw.ECSWorld()).
		Without(ecs.C[mmokit.Ghost](), ecs.C[mmokit.Replica]())
	query := filter.Query()
	for query.Next() {
		count++
	}
	return count
}
