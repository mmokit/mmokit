package main

// Log categories for debug logging.
const (
	CatSnakeMove      = "snake:move"
	CatSnakeEat       = "snake:eat"
	CatSnakeCollision = "snake:collision"
	CatSnakeDeath     = "snake:death"
	CatSnakeBoost     = "snake:boost"
	CatGameBot        = "game:bot"
	CatGameFood       = "game:food"
	CatGameNetwork    = "game:network"
	CatGameTransfer   = "game:transfer"
)

var SlitherCategories = []string{
	CatSnakeMove, CatSnakeEat, CatSnakeCollision, CatSnakeDeath, CatSnakeBoost,
	CatGameBot, CatGameFood, CatGameNetwork, CatGameTransfer,
}
