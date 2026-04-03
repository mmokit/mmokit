package main

// SlitherConfig holds all tunable game parameters.
type SlitherConfig struct {
	// Movement
	BaseSpeed  float32 // units/sec (default 400)
	BoostSpeed float32 // units/sec while boosting (default 800)
	TurnRate   float32 // max radians/sec (default 4.0)

	// Growth
	StartingMass   float32 // initial mass for new snakes (default 30)
	MassPerSegment float32 // mass per body segment (default 3.0)
	MinSegments    int     // minimum visible segments (default 15)
	MaxSegments    int     // ring buffer capacity (default 512)

	// Decay
	DecayRate float32 // mass lost per second (default 0.1)
	MinMass   float32 // minimum mass before death (default 25.0)

	// Boost
	BoostMassCost  float32 // mass spent per second while boosting (default 2.0)
	BoostFoodValue float32 // value of each food dropped while boosting (default 0.3)

	// Food
	FoodPerNode      int     // target food count per node (default 500)
	NaturalFoodValue float32 // mass gained from natural food (default 1.0)
	EatingRadius     float32 // distance to eat food (default 30)

	// Collision
	HeadCollisionRadius float32 // head hitbox radius (default 15)
	BodyCollisionRadius float32 // body segment hitbox radius (default 12)

	// Bots
	BotsPerNode     int     // AI snakes per node (default 5)
	BotRespawnDelay float32 // seconds before bot respawn (default 3.0)

	// Network
	AoIRadius           float32 // area of interest radius (default 3000)
	SegmentSubsample    int     // send every Nth segment (default 3)
	LeaderboardInterval int     // ticks between leaderboard updates (default 40)
}

// DefaultConfig returns the default game configuration.
func DefaultConfig() SlitherConfig {
	return SlitherConfig{
		BaseSpeed:  300,
		BoostSpeed: 600,
		TurnRate:   8.0,

		StartingMass:   30,
		MassPerSegment: 3.0,
		MinSegments:    10,
		MaxSegments:    512,

		DecayRate: 0.1,
		MinMass:   25.0,

		BoostMassCost:  2.0,
		BoostFoodValue: 0.3,

		FoodPerNode:      500,
		NaturalFoodValue: 1.0,
		EatingRadius:     50,

		HeadCollisionRadius: 15,
		BodyCollisionRadius: 12,

		BotsPerNode:     30,
		BotRespawnDelay: 10.0,

		AoIRadius:           5000,
		SegmentSubsample:    3,
		LeaderboardInterval: 40,
	}
}
