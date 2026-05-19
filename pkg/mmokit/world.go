package mmokit

import "github.com/zenion/mmoserver/pkg/world"

// World data layer re-exports — game code imports mmokit, never pkg/world directly.

type WorldSnapshot = world.Snapshot
type WorldCellID = world.CellID
type WorldCellBucket = world.CellBucket
type WorldRepository = world.Repository

type WorldStation = world.Station
type WorldPOI = world.POI
type WorldDungeon = world.Dungeon
type WorldBelt = world.Belt
type WorldDecoration = world.Decoration
type WorldRegion = world.Region

type WorldStations = world.Stations
type WorldPOIs = world.POIs
type WorldDungeons = world.Dungeons
type WorldBelts = world.Belts
type WorldDecorations = world.Decorations
type WorldRegions = world.Regions

type WorldBucketedStation = world.BucketedStation
type WorldBucketedPOI = world.BucketedPOI
type WorldBucketedDungeon = world.BucketedDungeon
type WorldBucketedBelt = world.BucketedBelt
type WorldBucketedDecoration = world.BucketedDecoration
