package main

import (
	"math/rand"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// DeathSystem processes pending kills: scatters food, updates kill feed, removes dead entities.
type DeathSystem struct {
	mmokit.SystemBase
	gw        *SlitherWorld
	playerMap *ecs.Map1[mmokit.PlayerConn]
}

func (s *DeathSystem) Init() {
	s.gw = s.GameWorld().(*SlitherWorld)
	s.playerMap = ecs.NewMap1[mmokit.PlayerConn](s.ECSWorld())
}

func (s *DeathSystem) Update(dt float32) {
	gw := s.gw

	cfg := &gw.Cfg

	for _, kill := range gw.PendingKills {
		if !gw.ECSWorld().Alive(kill.Victim) {
			continue
		}

		// Get victim info for food scattering
		var victimName string
		var killerName string

		if gw.SnakeStateMap.HasAll(kill.Victim) {
			victimName = gw.SnakeStateMap.Get(kill.Victim).Name
		}
		if kill.HasKiller && gw.ECSWorld().Alive(kill.Killer) && gw.SnakeStateMap.HasAll(kill.Killer) {
			killerName = gw.SnakeStateMap.Get(kill.Killer).Name
		}

		// Scatter food along the body — every 2nd segment plus random scatter
		if gw.SnakeBodyMap.HasAll(kill.Victim) {
			body := gw.SnakeBodyMap.Get(kill.Victim)
			cellSize := mmokit.CellSize()

			numFood := 0
			for i := 0; i < body.Length; i += 2 {
				numFood++
			}
			if numFood < 1 {
				numFood = 1
			}
			baseFoodValue := kill.Mass / float32(numFood)
			if baseFoodValue < 0.2 {
				baseFoodValue = 0.2
			}

			spawned := 0
			for i := 0; i < body.Length; i += 2 {
				seg := body.GetSegment(i)
				// Scatter offset so food doesn't line up perfectly
				segX := seg.X + (rand.Float32()-0.5)*40
				segY := seg.Y + (rand.Float32()-0.5)*40
				// Vary individual piece size (0.6x to 1.4x base)
				value := baseFoodValue * (0.6 + rand.Float32()*0.8)

				// Clamp to cell bounds
				if segX < 0 {
					segX = 0
				} else if segX >= cellSize {
					segX = cellSize - 1
				}
				if segY < 0 {
					segY = 0
				} else if segY >= cellSize {
					segY = cellSize - 1
				}
				gw.SpawnDeathFood(segX, segY, value)
				spawned++
			}

			gw.Engine().Log.Log(CatSnakeDeath, "snake=%d scattered %d food pieces (total mass=%.1f)", kill.VictimNet, spawned, kill.Mass)
		}

		// Remove player from tracking maps
		isPlayer := false
		if s.playerMap.HasAll(kill.Victim) {
			conn := s.playerMap.Get(kill.Victim)
			if conn.ConnID != 0 {
				isPlayer = true
				// Don't delete from Players here — let NetworkSystem send one
				// final world update (with the entity in the removed list) before
				// moving the connection to PendingConns.
			}
		}

		// Check if victim was a bot and enqueue respawn
		if gw.BotMap.HasAll(kill.Victim) {
			mmokit.Enqueue(gw.Queue, BotRespawn{
				Delay: cfg.BotRespawnDelay,
				NodeX: kill.PosX,
				NodeY: kill.PosY,
			})
			gw.Engine().Log.Log(CatSnakeDeath, "bot snake=%d died, respawn queued (delay=%.1fs)", kill.VictimNet, cfg.BotRespawnDelay)
		}

		// Add to kill feed
		gw.KillFeed = append(gw.KillFeed, KillFeedEntry{
			VictimName: victimName,
			KillerName: killerName,
			VictimMass: kill.Mass,
		})

		// Mark for removal
		gw.MarkForRemoval(kill.Victim)

		if kill.HasKiller {
			gw.Engine().Log.Log(CatSnakeDeath, "snake=%d killed by snake=%d mass=%.1f player=%v", kill.VictimNet, kill.KillerNet, kill.Mass, isPlayer)
		} else {
			gw.Engine().Log.Log(CatSnakeDeath, "snake=%d died mass=%.1f player=%v", kill.VictimNet, kill.Mass, isPlayer)
		}
	}
}
