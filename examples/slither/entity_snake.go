package main

import (
	"log"
	"math"
	"math/rand"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

type snakeMappers struct {
	base *ecs.Map8[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.NetworkID, mmokit.EntityKind, mmokit.Collider, mmokit.CellCoord, mmokit.PlayerConn]
}

func newSnakeMappers(w *ecs.World) *snakeMappers {
	return &snakeMappers{
		base: ecs.NewMap8[mmokit.Position, mmokit.Velocity, mmokit.Rotation, mmokit.NetworkID, mmokit.EntityKind, mmokit.Collider, mmokit.CellCoord, mmokit.PlayerConn](w),
	}
}

// SpawnPlayerSnake creates a player-controlled snake and returns the entity.
func (gw *SlitherWorld) SpawnPlayerSnake(connID uint32, name string, skinID uint8) ecs.Entity {
	x, y := gw.findClearSpawnPos()
	angle := rand.Float32() * 2 * math.Pi

	netID := gw.Engine().NextNetID()
	entity := gw.snakeMappers.base.NewEntity(
		&mmokit.Position{X: x, Y: y},
		&mmokit.Velocity{},
		&mmokit.Rotation{Angle: angle},
		&mmokit.NetworkID{ID: netID},
		&mmokit.EntityKind{Type: KindPlayerSnake},
		&mmokit.Collider{Radius: gw.Cfg.HeadCollisionRadius, Layer: LayerSnakeHead},
		&mmokit.CellCoord{CellX: gw.Cell().X, CellY: gw.Cell().Y},
		&mmokit.PlayerConn{ConnID: connID},
	)

	body := &SnakeBody{Length: gw.Cfg.MinSegments}
	for i := 0; i < body.Length; i++ {
		body.PushHead(x, y)
	}
	gw.SnakeBodyMap.Add(entity, body)

	gw.SnakeStateMap.Add(entity, &SnakeState{
		Mass:        gw.Cfg.StartingMass,
		TargetAngle: angle,
		Speed:       gw.Cfg.BaseSpeed,
		TurnRate:    gw.Cfg.TurnRate,
		SkinID:      skinID,
		Name:        name,
	})
	gw.SnakeInputMap.Add(entity, &SnakeInput{})

	log.Printf("[%s] spawned player snake: connID=%d netID=%d name=%s at (%.0f,%.0f)",
		gw.NodeID(), connID, netID, name, x, y)

	gw.SendSpawnedMsg(connID, netID)
	return entity
}

// SpawnBotSnake creates an AI-controlled snake.
func (gw *SlitherWorld) SpawnBotSnake(x, y float32) {
	angle := rand.Float32() * 2 * math.Pi
	netID := gw.Engine().NextNetID()

	botNames := []string{
		"snek", "danger-noodle", "nope-rope", "wiggle", "slithers",
		"hissy", "scales", "venom", "cobra", "python", "mamba", "boa",
		"noodle", "sidewinder", "rattles", "fang", "asp", "adder",
		"taipan", "krait", "copperhead", "cottonmouth", "kingsnake",
		"anaconda", "viper", "basilisk", "wyrm", "serpent", "ouroboros",
		"medusa", "hydra", "jormungandr", "quetzalcoatl", "apophis",
		"nagini", "kaa", "ssslayer", "coils", "hiss-teria",
		"slinky", "squiggles", "wriggles", "twisty", "zigzag",
		"sneky-snek", "long-boi", "tube-dude", "floor-spaghet",
		"murder-spagurder", "boop-snoot", "scale-face", "tongue-flicker",
		"belly-slider", "no-legs", "grass-ghost", "sand-swimmer",
		"tree-hugger", "rock-hider", "night-crawler", "sun-basker",
		"mouse-muncher", "egg-eater", "frog-finder", "rat-racer",
		"shadow", "phantom", "spectre", "eclipse", "tempest",
		"blitz", "havoc", "chaos", "fury", "rampage",
		"apex", "omega", "titan", "colossus", "leviathan",
		"kraken", "behemoth", "goliath", "juggernaut", "nemesis",
	}
	name := botNames[rand.Intn(len(botNames))]
	skinID := uint8(rand.Intn(8))

	entity := gw.snakeMappers.base.NewEntity(
		&mmokit.Position{X: x, Y: y},
		&mmokit.Velocity{},
		&mmokit.Rotation{Angle: angle},
		&mmokit.NetworkID{ID: netID},
		&mmokit.EntityKind{Type: KindBotSnake},
		&mmokit.Collider{Radius: gw.Cfg.HeadCollisionRadius, Layer: LayerSnakeHead},
		&mmokit.CellCoord{CellX: gw.Cell().X, CellY: gw.Cell().Y},
		&mmokit.PlayerConn{ConnID: 0},
	)

	body := &SnakeBody{Length: gw.Cfg.MinSegments}
	for i := 0; i < body.Length; i++ {
		body.PushHead(x, y)
	}
	gw.SnakeBodyMap.Add(entity, body)

	gw.SnakeStateMap.Add(entity, &SnakeState{
		Mass:        gw.Cfg.StartingMass,
		TargetAngle: angle,
		Speed:       gw.Cfg.BaseSpeed,
		TurnRate:    gw.Cfg.TurnRate,
		SkinID:      skinID,
		Name:        name,
	})
	gw.SnakeInputMap.Add(entity, &SnakeInput{})

	gw.BotMap.Add(entity, &Bot{
		WanderBias: (rand.Float32() - 0.5) * 2,
	})

	log.Printf("[%s] spawned bot snake: netID=%d name=%s at (%.0f,%.0f)",
		gw.NodeID(), netID, name, x, y)
}

// findClearSpawnPos picks a random position that isn't near any existing snake.
// Tries up to 10 times, then falls back to the last random position.
func (gw *SlitherWorld) findClearSpawnPos() (float32, float32) {
	cellSize := mmokit.CellSize()
	const clearRadius float32 = 300
	var buf []mmokit.SpatialEntry

	for attempt := 0; attempt < 10; attempt++ {
		x := rand.Float32()*cellSize*0.6 + cellSize*0.2
		y := rand.Float32()*cellSize*0.6 + cellSize*0.2

		buf = gw.Spatial.QueryRadius(x, y, clearRadius, buf[:0])
		clear := true
		for _, entry := range buf {
			if entry.Layer == LayerSnakeHead || entry.Layer == LayerSnakeBody {
				clear = false
				break
			}
		}
		if clear {
			return x, y
		}
	}

	// Fallback: random position
	x := rand.Float32()*cellSize*0.6 + cellSize*0.2
	y := rand.Float32()*cellSize*0.6 + cellSize*0.2
	return x, y
}
