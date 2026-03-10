package game

import "time"

// PlayerData holds persistent player state that survives disconnect/death.
type PlayerData struct {
	Username  string     `json:"username"`
	Flux      float64    `json:"flux"`
	X         float32    `json:"x"`
	Y         float32    `json:"y"`
	Resources [4]float32 `json:"resources"`
	HasSave   bool       `json:"has_save"`
	CreatedAt time.Time  `json:"created_at"`
	LastLogin time.Time  `json:"last_login"`
}
