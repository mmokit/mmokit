// Package world holds the data layer for the hand-authored world editor:
// per-type entity manifests, the in-memory Snapshot, and the Repository
// interface implemented by jsonrepo.
package world

// CellID identifies a cell in the infinite grid. Mirrors coords.CellCoord
// without depending on it — pkg/world is a pure data layer.
type CellID struct {
	X, Y int32
}

// Pos2 is a 2D point in either world-space or cell-local space depending
// on context. Used as a transparent serialization shape — JSON sees [x, y].
type Pos2 = [2]float32

type Snapshot struct {
	Stations    *Stations
	POIs        *POIs
	Dungeons    *Dungeons
	Belts       *Belts
	Decorations *Decorations
	Regions     *Regions
}

type Stations struct {
	Version  int       `json:"version"`
	Stations []Station `json:"stations"`
}

type Station struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	WorldPos Pos2    `json:"world_pos"`
	Radius   float32 `json:"radius,omitempty"`
}

type POIs struct {
	Version int   `json:"version"`
	POIs    []POI `json:"pois"`
}

type POI struct {
	ID           string  `json:"id"`
	WorldPos     Pos2    `json:"world_pos"`
	Type         string  `json:"type"`
	Tier         uint8   `json:"tier"`
	Roster       string  `json:"roster"`
	SpreadRadius float32 `json:"spread_radius,omitempty"`
}

type Dungeons struct {
	Version  int       `json:"version"`
	Dungeons []Dungeon `json:"dungeons"`
}

type Dungeon struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	WorldPos Pos2   `json:"world_pos"`
	Seed     int64  `json:"seed,omitempty"`
}

type Belts struct {
	Version int    `json:"version"`
	Belts   []Belt `json:"belts"`
}

type Belt struct {
	ID       string  `json:"id"`
	WorldPos Pos2    `json:"world_pos"`
	Radius   float32 `json:"radius"`
	Density  float32 `json:"density"`
}

type Decorations struct {
	Version     int          `json:"version"`
	Decorations []Decoration `json:"decorations"`
}

type Decoration struct {
	ID       string `json:"id"`
	WorldPos Pos2   `json:"world_pos"`
	Kind     string `json:"kind"`
	Variant  string `json:"variant,omitempty"`
}

type Regions struct {
	Version int      `json:"version"`
	Regions []Region `json:"regions"`
}

type Region struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Kind     string       `json:"kind"`
	Shape    string       `json:"shape"` // "polygon" | "annulus"
	Vertices [][2]float32 `json:"vertices,omitempty"`
	Center   Pos2         `json:"center,omitempty"`
	InnerR   float32      `json:"inner_r,omitempty"`
	OuterR   float32      `json:"outer_r,omitempty"`
	Faction  string       `json:"faction,omitempty"`
}
