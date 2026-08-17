package world

import "github.com/mmokit/mmokit/pkg/coords"

// CellBucket is the cell-local view of a snapshot — every entity in the
// snapshot whose world position falls inside a single cell, with its
// position translated to cell-local coordinates. Produced by BucketByCell;
// consumed by the cell-bootstrap spawn pipeline.
type CellBucket struct {
	Stations    []BucketedStation
	POIs        []BucketedPOI
	Dungeons    []BucketedDungeon
	Belts       []BucketedBelt
	Decorations []BucketedDecoration
}

type BucketedStation struct {
	Def      Station
	LocalPos Pos2
}
type BucketedPOI struct {
	Def      POI
	LocalPos Pos2
}
type BucketedDungeon struct {
	Def      Dungeon
	LocalPos Pos2
}
type BucketedBelt struct {
	Def      Belt
	LocalPos Pos2
}
type BucketedDecoration struct {
	Def      Decoration
	LocalPos Pos2
}

// BucketByCell groups every placed entity by the cell that currently owns
// its world position, using the given cell size. The returned map is
// keyed by CellID; missing keys mean "no manifest content for that cell."
//
// Regions are intentionally not bucketed: they are world-coord polygons /
// annuli that span multiple cells, so callers do point-in-region tests
// against the global Regions list.
func (s *Snapshot) BucketByCell(cellSize float32) map[CellID]*CellBucket {
	out := map[CellID]*CellBucket{}
	get := func(c CellID) *CellBucket {
		b, ok := out[c]
		if !ok {
			b = &CellBucket{}
			out[c] = b
		}
		return b
	}

	if s.Stations != nil {
		for _, st := range s.Stations.Stations {
			c, local := cellAndLocal(st.WorldPos, cellSize)
			b := get(c)
			b.Stations = append(b.Stations, BucketedStation{Def: st, LocalPos: local})
		}
	}
	if s.POIs != nil {
		for _, p := range s.POIs.POIs {
			c, local := cellAndLocal(p.WorldPos, cellSize)
			b := get(c)
			b.POIs = append(b.POIs, BucketedPOI{Def: p, LocalPos: local})
		}
	}
	if s.Dungeons != nil {
		for _, d := range s.Dungeons.Dungeons {
			c, local := cellAndLocal(d.WorldPos, cellSize)
			b := get(c)
			b.Dungeons = append(b.Dungeons, BucketedDungeon{Def: d, LocalPos: local})
		}
	}
	if s.Belts != nil {
		for _, bl := range s.Belts.Belts {
			c, local := cellAndLocal(bl.WorldPos, cellSize)
			b := get(c)
			b.Belts = append(b.Belts, BucketedBelt{Def: bl, LocalPos: local})
		}
	}
	if s.Decorations != nil {
		for _, dc := range s.Decorations.Decorations {
			c, local := cellAndLocal(dc.WorldPos, cellSize)
			b := get(c)
			b.Decorations = append(b.Decorations, BucketedDecoration{Def: dc, LocalPos: local})
		}
	}
	return out
}

func cellAndLocal(world Pos2, cellSize float32) (CellID, Pos2) {
	w := coords.FromFlat(float64(world[0]), float64(world[1]), cellSize)
	return CellID{X: w.CellX, Y: w.CellY}, Pos2{w.LocalX, w.LocalY}
}
