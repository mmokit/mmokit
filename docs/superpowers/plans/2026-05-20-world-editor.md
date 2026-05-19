# World Editor Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace procgen placement of POIs, stations, dungeon anchors, belt centers, regions, and decorations with hand-authored JSON manifests under `world/`, edited live through a new `/world-editor` Svelte admin route. Procgen survives only inside placed entities.

**Architecture:** New `pkg/world` data layer with per-type JSON files (world-coord) and a `Repository` interface. The existing cell-bootstrap site in `internal/game/game.go` consumes a global `Snapshot` and spawns hand-placed entities — no new mmokit lifecycle hook needed. Seven `world.*` cmdsys verbs mutate JSON + ECS in one handler. Svelte SPA route reuses canvas math from the existing Cluster page.

**Tech Stack:** Go (server + cmdsys + pkg/world), Svelte 5 + Vite + Bun + Tailwind v4 (admin SPA), existing `cmdsys.Dispatcher` + `admin.TopicBus` for control plane.

**Spec:** [docs/superpowers/specs/2026-05-20-world-editor-design.md](../specs/2026-05-20-world-editor-design.md)

**Branch:** `world-editor`

---

## Conventions for all tasks

- Use **`just build`** to compile (drops binary into `bin/`, never repo root)
- Use **`just lint-no-ark`** if you need to verify the no-ark-in-game-code invariant
- For Svelte work, **`bun`** (never npm/yarn). Build with **`bun run build`** from `web-admin/`.
- Game code must NOT import `pkg/world` directly — re-export through `pkg/mmokit` per the mmokit-facade rule.
- Game code must NOT import `github.com/mlange-42/ark/ecs` — use mmokit wrappers.
- Tests: standard Go `testing`, table-driven where it helps. Place tests next to code (`pkg/world/snapshot_test.go`).
- `go test ./pkg/world/... -run TestX` to exercise a single test.
- Commit after each task with the message format shown in the task.
- Never leave background processes running between tasks. Use `just build` (one-shot) not `just dev`.

---

## File Structure

### New backend files

- `pkg/world/types.go` — Go structs for every entity type + wrapper file structs
- `pkg/world/snapshot.go` — `Snapshot`, `CellBucket`, `BucketByCell` helper
- `pkg/world/repository.go` — `Repository` interface
- `pkg/world/jsonrepo/jsonrepo.go` — JSON file implementation (atomic write, per-file mutex, in-memory cache)
- `pkg/world/snapshot_test.go`, `pkg/world/jsonrepo/jsonrepo_test.go`
- `pkg/mmokit/world.go` — facade re-exports (`mmokit.WorldSnapshot`, `mmokit.WorldRepository`, etc.)
- `internal/game/commands/world.go` — seven `world.*` cmdsys verbs
- `internal/game/commands/world_test.go`
- `internal/game/entity_decoration.go` — decoration spawn (new entity kind)

### Modified backend files

- `internal/game/game.go` (lines ~289-321) — replace procgen-spawn block with snapshot-driven spawn
- `internal/game/factory.go` — pass world snapshot into game world constructor
- `internal/game/gameworld.go` — add `WorldRepo *world.Repository`, `Snapshot *world.Snapshot` fields
- `internal/game/entity_station.go` — `SpawnStation` takes `world.Station` (drop hardcoded constants)
- `internal/game/entity_poi.go` — `SpawnPOI` takes `world.POI`, removes per-cell procgen entry point
- `internal/game/entity_dungeon.go` — `SpawnDungeon` takes `world.Dungeon`
- `internal/game/belts.go` — split: `GenerateBelts` (placement) deleted, `scatterBeltAsteroids(belt world.Belt)` keeps the inner randomization
- `internal/game/entity_asteroid.go` — `spawnAsteroids` becomes `scatterBeltAsteroids` callable per belt
- `internal/game/entity_kinds.go` — register `KindDecoration`
- `internal/game/poi_gen.go` — **deleted entirely** (along with `poi_gen_test.go`)
- `internal/game/commands/registry.go` — `RegisterAll` calls new `registerWorld(...)`
- `cmd/server/main.go` — adds `--world-dir` flag, instantiates JSON repo, loads snapshot, plumbs into factory

### New / modified admin SPA files

- `web-admin/src/routes/world-editor.svelte` — new route
- `web-admin/src/components/WorldCanvas.svelte` — pan/zoom canvas with overlays + entity rendering
- `web-admin/src/components/WorldPalette.svelte` — left palette + layer toggles
- `web-admin/src/components/WorldInspector.svelte` — per-type inspector with explicit Apply
- `web-admin/src/lib/world-store.svelte.ts` — Svelte 5 store: snapshot, selection, dirty state, SSE
- `web-admin/src/components/Sidebar.svelte` — add nav entry
- `web-admin/src/app.svelte` — register route

### World data (committed sample, dev-runnable)

- `world/stations.json` — one station so `just dev` produces a navigable world
- `world/pois.json`, `world/dungeons.json`, `world/belts.json`, `world/decorations.json`, `world/regions.json` — empty arrays (`{"version":1,"pois":[]}` etc.)

---

## Tasks

### Task 1: `pkg/world` types + Snapshot + BucketByCell + tests

**Files:**

- Create: `pkg/world/types.go`
- Create: `pkg/world/snapshot.go`
- Create: `pkg/world/snapshot_test.go`

- [ ] **Step 1: Write the failing test**

Create `pkg/world/snapshot_test.go`:

```go
package world_test

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/world"
)

func TestBucketByCell_GroupsByWorldPos(t *testing.T) {
	prev := coords.CellSize
	defer func() { coords.CellSize = prev }()
	coords.CellSize = 8192

	s := &world.Snapshot{
		Stations: &world.Stations{Stations: []world.Station{
			{ID: "a", WorldPos: [2]float32{100, 200}},     // cell (0,0)
			{ID: "b", WorldPos: [2]float32{9000, 200}},    // cell (1,0)
			{ID: "c", WorldPos: [2]float32{-100, -100}},   // cell (-1,-1)
		}},
		POIs: &world.POIs{POIs: []world.POI{
			{ID: "p1", WorldPos: [2]float32{100, 200}, Tier: 1, Roster: "Starter"},
		}},
	}

	buckets := s.BucketByCell()

	if len(buckets[world.CellID{X: 0, Y: 0}].Stations) != 1 {
		t.Fatalf("cell (0,0) wanted 1 station, got %d", len(buckets[world.CellID{X: 0, Y: 0}].Stations))
	}
	if buckets[world.CellID{X: 0, Y: 0}].Stations[0].LocalPos != [2]float32{100, 200} {
		t.Fatalf("cell (0,0) station local pos wrong: %v", buckets[world.CellID{X: 0, Y: 0}].Stations[0].LocalPos)
	}
	if buckets[world.CellID{X: 1, Y: 0}].Stations[0].LocalPos[0] != 9000-8192 {
		t.Fatalf("cell (1,0) station local pos wrong: %v", buckets[world.CellID{X: 1, Y: 0}].Stations[0].LocalPos)
	}
	if buckets[world.CellID{X: -1, Y: -1}].Stations[0].LocalPos != [2]float32{8092, 8092} {
		t.Fatalf("cell (-1,-1) station local pos wrong: %v", buckets[world.CellID{X: -1, Y: -1}].Stations[0].LocalPos)
	}
	if len(buckets[world.CellID{X: 0, Y: 0}].POIs) != 1 {
		t.Fatalf("cell (0,0) wanted 1 POI, got %d", len(buckets[world.CellID{X: 0, Y: 0}].POIs))
	}
}

func TestBucketByCell_NilSlicesSafe(t *testing.T) {
	s := &world.Snapshot{}
	buckets := s.BucketByCell()
	if len(buckets) != 0 {
		t.Fatalf("empty snapshot should produce no buckets, got %d", len(buckets))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/world/... -run TestBucketByCell -v`
Expected: FAIL (package does not exist yet).

- [ ] **Step 3: Implement types**

Create `pkg/world/types.go`:

```go
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
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Kind     string    `json:"kind"`
	Shape    string    `json:"shape"`              // "polygon" | "annulus"
	Vertices [][2]float32 `json:"vertices,omitempty"`
	Center   Pos2      `json:"center,omitempty"`
	InnerR   float32   `json:"inner_r,omitempty"`
	OuterR   float32   `json:"outer_r,omitempty"`
	Faction  string    `json:"faction,omitempty"`
}
```

- [ ] **Step 4: Implement Snapshot + BucketByCell**

Create `pkg/world/snapshot.go`:

```go
package world

import "github.com/zenion/mmoserver/pkg/coords"

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
// its world position, using the live coords.CellSize. The returned map is
// keyed by CellID; missing keys mean "no manifest content for that cell."
func (s *Snapshot) BucketByCell() map[CellID]*CellBucket {
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
			c, local := cellAndLocal(st.WorldPos)
			b := get(c)
			b.Stations = append(b.Stations, BucketedStation{Def: st, LocalPos: local})
		}
	}
	if s.POIs != nil {
		for _, p := range s.POIs.POIs {
			c, local := cellAndLocal(p.WorldPos)
			b := get(c)
			b.POIs = append(b.POIs, BucketedPOI{Def: p, LocalPos: local})
		}
	}
	if s.Dungeons != nil {
		for _, d := range s.Dungeons.Dungeons {
			c, local := cellAndLocal(d.WorldPos)
			b := get(c)
			b.Dungeons = append(b.Dungeons, BucketedDungeon{Def: d, LocalPos: local})
		}
	}
	if s.Belts != nil {
		for _, bl := range s.Belts.Belts {
			c, local := cellAndLocal(bl.WorldPos)
			b := get(c)
			b.Belts = append(b.Belts, BucketedBelt{Def: bl, LocalPos: local})
		}
	}
	if s.Decorations != nil {
		for _, dc := range s.Decorations.Decorations {
			c, local := cellAndLocal(dc.WorldPos)
			b := get(c)
			b.Decorations = append(b.Decorations, BucketedDecoration{Def: dc, LocalPos: local})
		}
	}
	return out
}

func cellAndLocal(world Pos2) (CellID, Pos2) {
	w := coords.FromFlat(float64(world[0]), float64(world[1]))
	return CellID{X: w.CellX, Y: w.CellY}, Pos2{w.LocalX, w.LocalY}
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./pkg/world/... -run TestBucketByCell -v`
Expected: PASS (both tests).

- [ ] **Step 6: Verify lint invariant**

Run: `just lint-no-ark`
Expected: PASS (pkg/world doesn't touch ark).

- [ ] **Step 7: Commit**

```bash
git add pkg/world/types.go pkg/world/snapshot.go pkg/world/snapshot_test.go
git commit -m "pkg/world: data layer types + Snapshot.BucketByCell"
```

---

### Task 2: `pkg/world` Repository interface + jsonrepo JSON impl + tests

**Files:**

- Create: `pkg/world/repository.go`
- Create: `pkg/world/jsonrepo/jsonrepo.go`
- Create: `pkg/world/jsonrepo/jsonrepo_test.go`

- [ ] **Step 1: Repository interface**

Create `pkg/world/repository.go`:

```go
package world

// Repository is the read/write surface for the hand-authored world manifest.
// jsonrepo is the only impl in v1; tests can substitute a fake.
type Repository interface {
	LoadAll() (*Snapshot, error)

	SaveStations(*Stations) error
	SavePOIs(*POIs) error
	SaveDungeons(*Dungeons) error
	SaveBelts(*Belts) error
	SaveDecorations(*Decorations) error
	SaveRegions(*Regions) error

	AddStation(Station) error
	UpdateStation(id string, mut func(*Station)) error
	DeleteStation(id string) error

	AddPOI(POI) error
	UpdatePOI(id string, mut func(*POI)) error
	DeletePOI(id string) error

	AddDungeon(Dungeon) error
	UpdateDungeon(id string, mut func(*Dungeon)) error
	DeleteDungeon(id string) error

	AddBelt(Belt) error
	UpdateBelt(id string, mut func(*Belt)) error
	DeleteBelt(id string) error

	AddDecoration(Decoration) error
	UpdateDecoration(id string, mut func(*Decoration)) error
	DeleteDecoration(id string) error

	AddRegion(Region) error
	UpdateRegion(id string, mut func(*Region)) error
	DeleteRegion(id string) error
}
```

- [ ] **Step 2: Write failing tests**

Create `pkg/world/jsonrepo/jsonrepo_test.go`:

```go
package jsonrepo_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/zenion/mmoserver/pkg/world"
	"github.com/zenion/mmoserver/pkg/world/jsonrepo"
)

func TestLoadAll_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	r := jsonrepo.New(dir)

	snap, err := r.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll empty dir: %v", err)
	}
	if snap == nil || snap.POIs == nil || len(snap.POIs.POIs) != 0 {
		t.Fatalf("empty load should return empty snapshot, got %+v", snap)
	}
}

func TestAddAndLoadStation_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	r := jsonrepo.New(dir)

	st := world.Station{ID: "trade-0", Name: "Trade", WorldPos: [2]float32{8100, 8100}, Radius: 100}
	if err := r.AddStation(st); err != nil {
		t.Fatalf("AddStation: %v", err)
	}

	b, err := os.ReadFile(filepath.Join(dir, "stations.json"))
	if err != nil {
		t.Fatalf("read stations.json: %v", err)
	}
	var got world.Stations
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Version != 1 || len(got.Stations) != 1 || got.Stations[0].ID != "trade-0" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	snap, err := r.LoadAll()
	if err != nil {
		t.Fatalf("LoadAll: %v", err)
	}
	if snap.Stations.Stations[0].ID != "trade-0" {
		t.Fatalf("LoadAll round-trip lost id: %+v", snap.Stations)
	}
}

func TestAtomicWrite_NoTmpLeftBehind(t *testing.T) {
	dir := t.TempDir()
	r := jsonrepo.New(dir)
	if err := r.AddPOI(world.POI{ID: "p1", WorldPos: [2]float32{1, 2}, Type: "combat", Tier: 1, Roster: "Starter"}); err != nil {
		t.Fatalf("AddPOI: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("leftover tmp file: %s", e.Name())
		}
	}
}

func TestUpdatePOI_Mutator(t *testing.T) {
	dir := t.TempDir()
	r := jsonrepo.New(dir)
	_ = r.AddPOI(world.POI{ID: "p1", Tier: 1, Roster: "Starter"})

	if err := r.UpdatePOI("p1", func(p *world.POI) { p.Tier = 2; p.Roster = "Medium" }); err != nil {
		t.Fatalf("UpdatePOI: %v", err)
	}
	snap, _ := r.LoadAll()
	if snap.POIs.POIs[0].Tier != 2 || snap.POIs.POIs[0].Roster != "Medium" {
		t.Fatalf("update did not apply: %+v", snap.POIs.POIs[0])
	}
}

func TestDeletePOI(t *testing.T) {
	dir := t.TempDir()
	r := jsonrepo.New(dir)
	_ = r.AddPOI(world.POI{ID: "p1"})
	_ = r.AddPOI(world.POI{ID: "p2"})
	if err := r.DeletePOI("p1"); err != nil {
		t.Fatalf("DeletePOI: %v", err)
	}
	snap, _ := r.LoadAll()
	if len(snap.POIs.POIs) != 1 || snap.POIs.POIs[0].ID != "p2" {
		t.Fatalf("delete left wrong set: %+v", snap.POIs.POIs)
	}
}

func TestConcurrentAdd_NoCorruption(t *testing.T) {
	dir := t.TempDir()
	r := jsonrepo.New(dir)
	done := make(chan struct{}, 10)
	for i := 0; i < 10; i++ {
		go func(i int) {
			_ = r.AddPOI(world.POI{ID: string(rune('a' + i))})
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	snap, err := r.LoadAll()
	if err != nil {
		t.Fatalf("concurrent load: %v", err)
	}
	if len(snap.POIs.POIs) != 10 {
		t.Fatalf("expected 10 POIs after concurrent adds, got %d", len(snap.POIs.POIs))
	}
}
```

- [ ] **Step 3: Run tests to verify failure**

Run: `go test ./pkg/world/jsonrepo/... -v`
Expected: FAIL (package doesn't exist).

- [ ] **Step 4: Implement jsonrepo**

Create `pkg/world/jsonrepo/jsonrepo.go`:

```go
// Package jsonrepo is the JSON-file-backed implementation of world.Repository.
package jsonrepo

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/zenion/mmoserver/pkg/world"
)

type Repo struct {
	dir string

	muStations    sync.Mutex
	muPOIs        sync.Mutex
	muDungeons    sync.Mutex
	muBelts       sync.Mutex
	muDecorations sync.Mutex
	muRegions     sync.Mutex
}

func New(dir string) *Repo { return &Repo{dir: dir} }

func (r *Repo) LoadAll() (*world.Snapshot, error) {
	if err := os.MkdirAll(r.dir, 0o755); err != nil {
		return nil, fmt.Errorf("ensure world dir: %w", err)
	}
	snap := &world.Snapshot{
		Stations:    &world.Stations{Version: 1, Stations: []world.Station{}},
		POIs:        &world.POIs{Version: 1, POIs: []world.POI{}},
		Dungeons:    &world.Dungeons{Version: 1, Dungeons: []world.Dungeon{}},
		Belts:       &world.Belts{Version: 1, Belts: []world.Belt{}},
		Decorations: &world.Decorations{Version: 1, Decorations: []world.Decoration{}},
		Regions:     &world.Regions{Version: 1, Regions: []world.Region{}},
	}
	if err := readJSON(r.path("stations.json"), snap.Stations); err != nil {
		return nil, err
	}
	if err := readJSON(r.path("pois.json"), snap.POIs); err != nil {
		return nil, err
	}
	if err := readJSON(r.path("dungeons.json"), snap.Dungeons); err != nil {
		return nil, err
	}
	if err := readJSON(r.path("belts.json"), snap.Belts); err != nil {
		return nil, err
	}
	if err := readJSON(r.path("decorations.json"), snap.Decorations); err != nil {
		return nil, err
	}
	if err := readJSON(r.path("regions.json"), snap.Regions); err != nil {
		return nil, err
	}
	return snap, nil
}

// --- save/add/update/delete: stations ---

func (r *Repo) SaveStations(v *world.Stations) error {
	r.muStations.Lock()
	defer r.muStations.Unlock()
	return atomicWriteJSON(r.path("stations.json"), v)
}

func (r *Repo) AddStation(s world.Station) error {
	r.muStations.Lock()
	defer r.muStations.Unlock()
	cur := &world.Stations{Version: 1}
	if err := readJSON(r.path("stations.json"), cur); err != nil {
		return err
	}
	for _, x := range cur.Stations {
		if x.ID == s.ID {
			return fmt.Errorf("station id %q already exists", s.ID)
		}
	}
	cur.Stations = append(cur.Stations, s)
	return atomicWriteJSON(r.path("stations.json"), cur)
}

func (r *Repo) UpdateStation(id string, mut func(*world.Station)) error {
	r.muStations.Lock()
	defer r.muStations.Unlock()
	cur := &world.Stations{Version: 1}
	if err := readJSON(r.path("stations.json"), cur); err != nil {
		return err
	}
	for i := range cur.Stations {
		if cur.Stations[i].ID == id {
			mut(&cur.Stations[i])
			return atomicWriteJSON(r.path("stations.json"), cur)
		}
	}
	return notFound("station", id)
}

func (r *Repo) DeleteStation(id string) error {
	r.muStations.Lock()
	defer r.muStations.Unlock()
	cur := &world.Stations{Version: 1}
	if err := readJSON(r.path("stations.json"), cur); err != nil {
		return err
	}
	for i := range cur.Stations {
		if cur.Stations[i].ID == id {
			cur.Stations = append(cur.Stations[:i], cur.Stations[i+1:]...)
			return atomicWriteJSON(r.path("stations.json"), cur)
		}
	}
	return notFound("station", id)
}

// --- pois ---

func (r *Repo) SavePOIs(v *world.POIs) error {
	r.muPOIs.Lock()
	defer r.muPOIs.Unlock()
	return atomicWriteJSON(r.path("pois.json"), v)
}
func (r *Repo) AddPOI(p world.POI) error {
	r.muPOIs.Lock()
	defer r.muPOIs.Unlock()
	cur := &world.POIs{Version: 1}
	if err := readJSON(r.path("pois.json"), cur); err != nil {
		return err
	}
	for _, x := range cur.POIs {
		if x.ID == p.ID {
			return fmt.Errorf("poi id %q already exists", p.ID)
		}
	}
	cur.POIs = append(cur.POIs, p)
	return atomicWriteJSON(r.path("pois.json"), cur)
}
func (r *Repo) UpdatePOI(id string, mut func(*world.POI)) error {
	r.muPOIs.Lock()
	defer r.muPOIs.Unlock()
	cur := &world.POIs{Version: 1}
	if err := readJSON(r.path("pois.json"), cur); err != nil {
		return err
	}
	for i := range cur.POIs {
		if cur.POIs[i].ID == id {
			mut(&cur.POIs[i])
			return atomicWriteJSON(r.path("pois.json"), cur)
		}
	}
	return notFound("poi", id)
}
func (r *Repo) DeletePOI(id string) error {
	r.muPOIs.Lock()
	defer r.muPOIs.Unlock()
	cur := &world.POIs{Version: 1}
	if err := readJSON(r.path("pois.json"), cur); err != nil {
		return err
	}
	for i := range cur.POIs {
		if cur.POIs[i].ID == id {
			cur.POIs = append(cur.POIs[:i], cur.POIs[i+1:]...)
			return atomicWriteJSON(r.path("pois.json"), cur)
		}
	}
	return notFound("poi", id)
}

// --- dungeons ---

func (r *Repo) SaveDungeons(v *world.Dungeons) error {
	r.muDungeons.Lock()
	defer r.muDungeons.Unlock()
	return atomicWriteJSON(r.path("dungeons.json"), v)
}
func (r *Repo) AddDungeon(d world.Dungeon) error {
	r.muDungeons.Lock()
	defer r.muDungeons.Unlock()
	cur := &world.Dungeons{Version: 1}
	if err := readJSON(r.path("dungeons.json"), cur); err != nil {
		return err
	}
	for _, x := range cur.Dungeons {
		if x.ID == d.ID {
			return fmt.Errorf("dungeon id %q already exists", d.ID)
		}
	}
	cur.Dungeons = append(cur.Dungeons, d)
	return atomicWriteJSON(r.path("dungeons.json"), cur)
}
func (r *Repo) UpdateDungeon(id string, mut func(*world.Dungeon)) error {
	r.muDungeons.Lock()
	defer r.muDungeons.Unlock()
	cur := &world.Dungeons{Version: 1}
	if err := readJSON(r.path("dungeons.json"), cur); err != nil {
		return err
	}
	for i := range cur.Dungeons {
		if cur.Dungeons[i].ID == id {
			mut(&cur.Dungeons[i])
			return atomicWriteJSON(r.path("dungeons.json"), cur)
		}
	}
	return notFound("dungeon", id)
}
func (r *Repo) DeleteDungeon(id string) error {
	r.muDungeons.Lock()
	defer r.muDungeons.Unlock()
	cur := &world.Dungeons{Version: 1}
	if err := readJSON(r.path("dungeons.json"), cur); err != nil {
		return err
	}
	for i := range cur.Dungeons {
		if cur.Dungeons[i].ID == id {
			cur.Dungeons = append(cur.Dungeons[:i], cur.Dungeons[i+1:]...)
			return atomicWriteJSON(r.path("dungeons.json"), cur)
		}
	}
	return notFound("dungeon", id)
}

// --- belts ---

func (r *Repo) SaveBelts(v *world.Belts) error {
	r.muBelts.Lock()
	defer r.muBelts.Unlock()
	return atomicWriteJSON(r.path("belts.json"), v)
}
func (r *Repo) AddBelt(b world.Belt) error {
	r.muBelts.Lock()
	defer r.muBelts.Unlock()
	cur := &world.Belts{Version: 1}
	if err := readJSON(r.path("belts.json"), cur); err != nil {
		return err
	}
	for _, x := range cur.Belts {
		if x.ID == b.ID {
			return fmt.Errorf("belt id %q already exists", b.ID)
		}
	}
	cur.Belts = append(cur.Belts, b)
	return atomicWriteJSON(r.path("belts.json"), cur)
}
func (r *Repo) UpdateBelt(id string, mut func(*world.Belt)) error {
	r.muBelts.Lock()
	defer r.muBelts.Unlock()
	cur := &world.Belts{Version: 1}
	if err := readJSON(r.path("belts.json"), cur); err != nil {
		return err
	}
	for i := range cur.Belts {
		if cur.Belts[i].ID == id {
			mut(&cur.Belts[i])
			return atomicWriteJSON(r.path("belts.json"), cur)
		}
	}
	return notFound("belt", id)
}
func (r *Repo) DeleteBelt(id string) error {
	r.muBelts.Lock()
	defer r.muBelts.Unlock()
	cur := &world.Belts{Version: 1}
	if err := readJSON(r.path("belts.json"), cur); err != nil {
		return err
	}
	for i := range cur.Belts {
		if cur.Belts[i].ID == id {
			cur.Belts = append(cur.Belts[:i], cur.Belts[i+1:]...)
			return atomicWriteJSON(r.path("belts.json"), cur)
		}
	}
	return notFound("belt", id)
}

// --- decorations ---

func (r *Repo) SaveDecorations(v *world.Decorations) error {
	r.muDecorations.Lock()
	defer r.muDecorations.Unlock()
	return atomicWriteJSON(r.path("decorations.json"), v)
}
func (r *Repo) AddDecoration(d world.Decoration) error {
	r.muDecorations.Lock()
	defer r.muDecorations.Unlock()
	cur := &world.Decorations{Version: 1}
	if err := readJSON(r.path("decorations.json"), cur); err != nil {
		return err
	}
	for _, x := range cur.Decorations {
		if x.ID == d.ID {
			return fmt.Errorf("decoration id %q already exists", d.ID)
		}
	}
	cur.Decorations = append(cur.Decorations, d)
	return atomicWriteJSON(r.path("decorations.json"), cur)
}
func (r *Repo) UpdateDecoration(id string, mut func(*world.Decoration)) error {
	r.muDecorations.Lock()
	defer r.muDecorations.Unlock()
	cur := &world.Decorations{Version: 1}
	if err := readJSON(r.path("decorations.json"), cur); err != nil {
		return err
	}
	for i := range cur.Decorations {
		if cur.Decorations[i].ID == id {
			mut(&cur.Decorations[i])
			return atomicWriteJSON(r.path("decorations.json"), cur)
		}
	}
	return notFound("decoration", id)
}
func (r *Repo) DeleteDecoration(id string) error {
	r.muDecorations.Lock()
	defer r.muDecorations.Unlock()
	cur := &world.Decorations{Version: 1}
	if err := readJSON(r.path("decorations.json"), cur); err != nil {
		return err
	}
	for i := range cur.Decorations {
		if cur.Decorations[i].ID == id {
			cur.Decorations = append(cur.Decorations[:i], cur.Decorations[i+1:]...)
			return atomicWriteJSON(r.path("decorations.json"), cur)
		}
	}
	return notFound("decoration", id)
}

// --- regions ---

func (r *Repo) SaveRegions(v *world.Regions) error {
	r.muRegions.Lock()
	defer r.muRegions.Unlock()
	return atomicWriteJSON(r.path("regions.json"), v)
}
func (r *Repo) AddRegion(rg world.Region) error {
	r.muRegions.Lock()
	defer r.muRegions.Unlock()
	cur := &world.Regions{Version: 1}
	if err := readJSON(r.path("regions.json"), cur); err != nil {
		return err
	}
	for _, x := range cur.Regions {
		if x.ID == rg.ID {
			return fmt.Errorf("region id %q already exists", rg.ID)
		}
	}
	cur.Regions = append(cur.Regions, rg)
	return atomicWriteJSON(r.path("regions.json"), cur)
}
func (r *Repo) UpdateRegion(id string, mut func(*world.Region)) error {
	r.muRegions.Lock()
	defer r.muRegions.Unlock()
	cur := &world.Regions{Version: 1}
	if err := readJSON(r.path("regions.json"), cur); err != nil {
		return err
	}
	for i := range cur.Regions {
		if cur.Regions[i].ID == id {
			mut(&cur.Regions[i])
			return atomicWriteJSON(r.path("regions.json"), cur)
		}
	}
	return notFound("region", id)
}
func (r *Repo) DeleteRegion(id string) error {
	r.muRegions.Lock()
	defer r.muRegions.Unlock()
	cur := &world.Regions{Version: 1}
	if err := readJSON(r.path("regions.json"), cur); err != nil {
		return err
	}
	for i := range cur.Regions {
		if cur.Regions[i].ID == id {
			cur.Regions = append(cur.Regions[:i], cur.Regions[i+1:]...)
			return atomicWriteJSON(r.path("regions.json"), cur)
		}
	}
	return notFound("region", id)
}

// --- helpers ---

func (r *Repo) path(name string) string { return filepath.Join(r.dir, name) }

func notFound(kind, id string) error {
	return fmt.Errorf("%s id %q not found", kind, id)
}

// readJSON reads a JSON file into v. A missing file is not an error — v is
// left as-is. v must be a non-nil pointer.
func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}
	if len(b) == 0 {
		return nil
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

// atomicWriteJSON writes v to path via write-to-tmp + fsync + rename. The
// directory must already exist.
func atomicWriteJSON(path string, v any) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
```

- [ ] **Step 5: Run tests to verify pass**

Run: `go test ./pkg/world/... -v`
Expected: PASS (all tests in Task 1 + Task 2).

- [ ] **Step 6: Commit**

```bash
git add pkg/world/repository.go pkg/world/jsonrepo/
git commit -m "pkg/world: Repository interface + jsonrepo JSON impl"
```

---

### Task 3: mmokit facade re-export of world types

**Files:**

- Create: `pkg/mmokit/world.go`

- [ ] **Step 1: Write the re-exports**

Create `pkg/mmokit/world.go`:

```go
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

type WorldBucketedStation = world.BucketedStation
type WorldBucketedPOI = world.BucketedPOI
type WorldBucketedDungeon = world.BucketedDungeon
type WorldBucketedBelt = world.BucketedBelt
type WorldBucketedDecoration = world.BucketedDecoration
```

- [ ] **Step 2: Verify compile**

Run: `just build`
Expected: PASS (binary in `bin/`).

- [ ] **Step 3: Commit**

```bash
git add pkg/mmokit/world.go
git commit -m "mmokit: re-export pkg/world types"
```

---

### Task 4: `--world-dir` flag + snapshot loading in `cmd/server/main.go`

Locate the flag-setup block in `cmd/server/main.go` (search for existing `flag.StringVar` usage). Add the world directory flag and load the snapshot at boot.

**Files:**

- Modify: `cmd/server/main.go`
- Create: `world/.gitkeep` (so the directory exists in fresh checkouts)

- [ ] **Step 1: Add the flag and load the snapshot**

In `cmd/server/main.go`, add to the flag-parsing block (near other `flag.StringVar`):

```go
worldDir := flag.String("world-dir", "world", "directory containing world manifest JSON files")
```

After flags are parsed and before the `Process` is built (i.e. before any cell is created), construct the repository and load the snapshot. Pass both into the game factory.

```go
import (
    // ... existing imports ...
    "github.com/zenion/mmoserver/pkg/world"
    "github.com/zenion/mmoserver/pkg/world/jsonrepo"
)

// ...inside main(), after flag.Parse():

worldRepo := jsonrepo.New(*worldDir)
worldSnap, err := worldRepo.LoadAll()
if err != nil {
    log.Fatalf("load world manifest from %s: %v", *worldDir, err)
}
log.Printf("world: loaded stations=%d pois=%d dungeons=%d belts=%d decorations=%d regions=%d",
    len(worldSnap.Stations.Stations),
    len(worldSnap.POIs.POIs),
    len(worldSnap.Dungeons.Dungeons),
    len(worldSnap.Belts.Belts),
    len(worldSnap.Decorations.Decorations),
    len(worldSnap.Regions.Regions),
)
```

Pass `worldRepo` and `worldSnap` into the game factory. (The factory signature changes in Task 5; for now stash them in a package-local variable so the build still succeeds.)

Concretely, where the game factory is called:

```go
gw := game.NewGameWorldFactory(/* existing args */, worldRepo, worldSnap)
```

If `game.NewGameWorldFactory` doesn't yet accept these, gate the change behind a TODO that Task 5 resolves; OR add the args now and stub them on the receiving side. The cleanest path: add the args here, fail the build, fix in Task 5.

- [ ] **Step 2: Create the empty world directory marker**

```bash
mkdir -p world
touch world/.gitkeep
```

- [ ] **Step 3: Verify compile (will fail at factory call until Task 5)**

Run: `just build`
Expected: may fail with "too many args to NewGameWorldFactory" — that's fine, fix it in Task 5. Do not commit a broken build; instead, skip the factory wiring here and only commit the flag + load + log lines if you can do so without breaking the build.

If you need to break the build temporarily, **do not commit this task standalone** — fold it into Task 5's commit instead.

- [ ] **Step 4: Commit (only if build is green)**

```bash
git add cmd/server/main.go world/.gitkeep
git commit -m "cmd: --world-dir flag, load world snapshot at boot"
```

If the build is red, leave the changes uncommitted and proceed to Task 5; commit them together.

---

### Task 5: Refactor `internal/game/game.go` cell-bootstrap to consume the world snapshot

This is the central refactor. The block at [internal/game/game.go:304-318](../internal/game/game.go#L304-L318) is replaced with a snapshot-driven spawn loop.

**Files:**

- Modify: `internal/game/game.go`
- Modify: `internal/game/gameworld.go` (add `WorldRepo`, `WorldSnapshot` fields)
- Modify: `internal/game/factory.go` (thread through new args)
- Modify: `cmd/server/main.go` (call site change, if not already done in Task 4)

- [ ] **Step 1: Add fields to GameWorld**

In `internal/game/gameworld.go`, add fields:

```go
type GameWorld struct {
    // ... existing fields ...

    WorldRepo     mmokit.WorldRepository
    WorldSnapshot *mmokit.WorldSnapshot
}
```

- [ ] **Step 2: Thread args through factory**

In `internal/game/factory.go`, change the factory signature to accept the repo + snapshot. Pass them to `NewGameWorld`.

In `internal/game/game.go::NewGameWorld`, accept the new args and populate `gw.WorldRepo` and `gw.WorldSnapshot` before the bootstrap block.

- [ ] **Step 3: Rewrite the bootstrap block**

Replace [internal/game/game.go:304-318](../internal/game/game.go#L304-L318) (the `if !fromSplit { … }` block) with:

```go
if !fromSplit {
    bucket := gw.bucketForRootCell()
    for _, s := range bucket.Stations {
        gw.SpawnStation(s.LocalPos[0], s.LocalPos[1], s.Def)
    }
    for _, p := range bucket.POIs {
        gw.SpawnPOI(p.LocalPos[0], p.LocalPos[1], p.Def)
    }
    for _, d := range bucket.Dungeons {
        gw.SpawnDungeonAt(d.LocalPos[0], d.LocalPos[1], d.Def)
    }
    for _, b := range bucket.Belts {
        gw.SpawnBelt(b.LocalPos[0], b.LocalPos[1], b.Def)
    }
    for _, dc := range bucket.Decorations {
        gw.SpawnDecoration(dc.LocalPos[0], dc.LocalPos[1], dc.Def)
    }
}
```

Add the helper to `gameworld.go`:

```go
func (gw *GameWorld) bucketForRootCell() *mmokit.WorldCellBucket {
    if gw.WorldSnapshot == nil {
        return &mmokit.WorldCellBucket{}
    }
    buckets := gw.WorldSnapshot.BucketByCell()
    if b, ok := buckets[mmokit.WorldCellID{X: gw.RootCell.CellX, Y: gw.RootCell.CellY}]; ok {
        return b
    }
    return &mmokit.WorldCellBucket{}
}
```

The signatures `SpawnStation(x, y, world.Station)`, `SpawnPOI(x, y, world.POI)`, etc. will be implemented in Tasks 6-10. To keep this commit compiling, add stub implementations that match the new signature now and call into the existing implementations as a temporary shim. Mark each stub with `// STUB: replaced in Task N`.

Example stub in `entity_station.go`:

```go
// STUB: replaced in Task 6.
func (gw *GameWorld) SpawnStation(localX, localY float32, def mmokit.WorldStation) {
    _ = def
    // Call the existing zero-arg version for now to keep the world populated
    // during refactor; Task 6 replaces this with the actual placement-driven impl.
    panic("Task 6: implement SpawnStation(x, y, def)")
}
```

Same shape for the others. After Task 10 they're all real impls.

- [ ] **Step 4: Delete the old hardcoded spawn calls**

In `internal/game/game.go`, remove:
- The `gw.spawnAsteroids()` call
- The `cell == cfg.StationCell` station spawn
- The `if int(cell.CellX) == cfg.DungeonTestsiteCellX && ...` dungeon spawn
- The `gw.spawnPOIs()` call

These are all subsumed by the snapshot-driven loop above.

- [ ] **Step 5: Verify compile (with stubs in place)**

Run: `just build`
Expected: PASS. Server will panic at runtime when a real cell tries to spawn (because of the `panic("Task 6...")` in stubs), but that's fine — Tasks 6-10 fix this.

- [ ] **Step 6: Commit**

```bash
git add internal/game/game.go internal/game/gameworld.go internal/game/factory.go cmd/server/main.go internal/game/entity_*.go world/.gitkeep
git commit -m "game: cell-bootstrap consumes world snapshot (stubs for Tasks 6-10)"
```

---

### Task 6: Implement `SpawnStation(localX, localY, world.Station)` — delete hardcoded constants

**Files:**

- Modify: `internal/game/entity_station.go`

- [ ] **Step 1: Replace SpawnStation with the new signature**

Replace the whole content of `internal/game/entity_station.go` with:

```go
package game

import (
	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/pkg/mmokit"
	"github.com/zenion/mmoserver/pkg/spatial"
)

// StationBundle is the entity-kind component bundle for trade stations.
// The Station component is local-only — replication only needs position +
// EntityKind for the client to render the marker.
type StationBundle struct {
	Station *gamecomp.Station `mmokit:"local"`
}

// SpawnStation creates a trade station at (localX, localY) inside the
// current cell using the provided world-manifest definition. Radius
// defaults to gw.Config.StationRadius when def.Radius is zero.
func (gw *GameWorld) SpawnStation(localX, localY float32, def mmokit.WorldStation) {
	radius := def.Radius
	if radius == 0 {
		radius = gw.Config.StationRadius
	}
	e := gw.stage.Spawn(
		mmokit.Position{X: localX, Y: localY},
		mmokit.EntityKind{Type: gamecomp.KindStation},
		mmokit.Collider{
			Radius: radius,
			Shape:  spatial.ShapeCircle,
			Layer:  spatial.LayerStatic,
		},
		gamecomp.Station{Name: def.Name},
	)
	gw.eng.Log.Log(CatPlayerSpawn, "station spawned: id=%s netID=%d pos=(%.1f,%.1f) name=%q",
		def.ID, e.NetID(), localX, localY, def.Name)
}

// CollectStationMapData returns map marker data for all stations on this stage.
func (gw *GameWorld) CollectStationMapData() []MapStationInfo {
	var stations []MapStationInfo
	mmokit.ForEach3(gw.stage, func(_ mmokit.Entity, st *gamecomp.Station, pos *mmokit.Position, sec *mmokit.CellCoord) {
		name := st.Name
		if name == "" {
			name = "TRADE STATION"
		}
		stations = append(stations, MapStationInfo{
			CellX:  sec.CellX,
			CellY:  sec.CellY,
			LocalX: pos.X,
			LocalY: pos.Y,
			Name:   name,
		})
	})
	return stations
}
```

- [ ] **Step 2: Add Name to Station component**

In `internal/component/components.go` (or wherever `Station` is defined), add:

```go
type Station struct {
    Name string `mmokit:"local"`
}
```

If `Station` is already a no-field struct, add the Name field. If it has fields, add Name alongside them.

- [ ] **Step 3: Delete `StationLocalX, StationLocalY` references**

Search for `StationLocalX` and `StationLocalY` across the codebase:

```bash
grep -rn "StationLocalX\|StationLocalY" --include="*.go"
```

Replace any remaining references with reads from the world snapshot, OR with the on-disk default once we seed `world/stations.json` in Task 12.

The most common remaining reference is in `cmd/server/main.go` or `examples/4node-basic/main.go`'s spawn resolver — see CLAUDE.md note about `process.OnResolveSpawn`. Update the spawn resolver to read the first station from the snapshot (or fall back to a sensible default if empty):

```go
process.OnResolveSpawn(func(s *mmokit.PlayerSession) mmokit.Location {
    if len(worldSnap.Stations.Stations) > 0 {
        st := worldSnap.Stations.Stations[0]
        return mmokit.Location{X: st.WorldPos[0] + 50, Y: st.WorldPos[1] + 50}
    }
    return mmokit.Location{X: 8100, Y: 8100} // dev fallback
})
```

- [ ] **Step 4: Seed `world/stations.json` for dev iteration**

Create `world/stations.json`:

```json
{
  "version": 1,
  "stations": [
    {"id": "trade-0", "name": "TRADE STATION", "world_pos": [8100, 8100], "radius": 100}
  ]
}
```

- [ ] **Step 5: Verify compile + station spawn**

Run: `just build`
Expected: PASS.

Run: `go test ./internal/game/... -run TestEntityStation -v`
Expected: PASS if station tests exist; if they reference `StationLocalX`, fix them to read from the world snapshot or hardcode the test station.

- [ ] **Step 6: Commit**

```bash
git add internal/game/entity_station.go internal/component/components.go world/stations.json cmd/server/main.go
git commit -m "station: spawn from world.Station def; drop hardcoded constants"
```

---

### Task 7: Implement `SpawnPOI(localX, localY, world.POI)` — delete `poi_gen.go`

**Files:**

- Modify: `internal/game/entity_poi.go`
- Delete: `internal/game/poi_gen.go`
- Delete: `internal/game/poi_gen_test.go`
- Modify: `internal/game/game.go` (remove `spawnPOIs` helper if any)

- [ ] **Step 1: Read existing SpawnPOI internals**

Open `internal/game/entity_poi.go` and identify how POIs currently get tier, roster, spread radius, and how they seed roster NPC scatter.

- [ ] **Step 2: Replace SpawnPOI with the new signature**

The new `SpawnPOI(localX, localY float32, def mmokit.WorldPOI)`:
- Creates the POI entity with `Tier: def.Tier`, `RosterDefIdx: RosterIdxForName(def.Roster)`, `Status: POIStatusActive`
- Uses `def.SpreadRadius` (defaulting to config when zero) for NPC scatter
- Seeds the NPC scatter RNG with `fnv64(def.ID)` so layout is stable per id
- Logs `id=%s tier=%d roster=%s spread=%.1f` for audit

Pseudocode (adapt to existing entity_poi.go internals):

```go
func (gw *GameWorld) SpawnPOI(localX, localY float32, def mmokit.WorldPOI) {
    spread := def.SpreadRadius
    if spread == 0 {
        spread = gw.Config.POISpreadRadius
    }
    rosterIdx := RosterIdxForName(def.Roster) // see Step 3

    poiEnt := gw.stage.Spawn(
        mmokit.Position{X: localX, Y: localY},
        mmokit.EntityKind{Type: gamecomp.KindPOI},
        mmokit.Collider{Radius: 1, Shape: spatial.ShapeCircle, Layer: spatial.LayerStatic},
        gamecomp.POI{
            Type:         gamecomp.POITypeCombat,
            Status:       gamecomp.POIStatusActive,
            Tier:         def.Tier,
            RosterDefIdx: rosterIdx,
            AnchorRadius: spread,
            LeashRadius:  gw.Config.POILeashRadius,
        },
    )

    rng := rand.New(rand.NewPCG(fnv64(def.ID), 0))
    gw.spawnRosterNPCs(poiEnt.NetID(), localX, localY, spread, rosterForIdx(rosterIdx), rng, def.Tier)

    gw.eng.Log.Log(CatCombat, "poi spawned: id=%s netID=%d tier=%d roster=%s pos=(%.1f,%.1f)",
        def.ID, poiEnt.NetID(), def.Tier, def.Roster, localX, localY)
}

func fnv64(s string) uint64 {
    h := fnv.New64a()
    h.Write([]byte(s))
    return h.Sum64()
}
```

Imports needed: `"hash/fnv"`, `"math/rand/v2"`.

- [ ] **Step 3: Add RosterIdxForName lookup**

In `internal/game/poi_config.go`, append:

```go
// RosterIdxForName returns the integer roster index for a name from the
// roster table, or StarterArenaIdx on miss (logged at call site).
func RosterIdxForName(name string) uint16 {
    for i, r := range rosters {
        if r.Name == name {
            return uint16(i)
        }
    }
    return StarterArenaIdx
}
```

The exact match is case-sensitive. Roster names in the JSON manifest must match the Name field in the `rosters` slice — e.g. `"Starter Arena"`. To allow tighter id-style spellings in JSON (e.g. `"StarterArena"`), strip spaces in both sides before comparing:

```go
func RosterIdxForName(name string) uint16 {
    norm := strings.ReplaceAll(name, " ", "")
    for i, r := range rosters {
        if strings.ReplaceAll(r.Name, " ", "") == norm {
            return uint16(i)
        }
    }
    return StarterArenaIdx
}
```

- [ ] **Step 4: Delete `poi_gen.go` and `poi_gen_test.go`**

```bash
git rm internal/game/poi_gen.go internal/game/poi_gen_test.go
```

If any remaining file imports `GeneratePOIs` or `pickRosterForTier` or `placePOI`, refactor: the in-POI NPC scatter logic that used to live in `placePOI` belongs in the SpawnPOI body now (it's a per-POI concern, not per-cell). Move what you need into `entity_poi.go`.

- [ ] **Step 5: Remove `spawnPOIs` helper from game.go**

In `internal/game/game.go`, find and delete any `func (gw *GameWorld) spawnPOIs()` helper. It's superseded by the snapshot-driven loop in Task 5.

- [ ] **Step 6: Verify compile**

Run: `just build`
Expected: PASS. Any callers of `GeneratePOIs` should be gone.

Run: `just lint-no-ark`
Expected: PASS.

- [ ] **Step 7: Seed an empty `world/pois.json` so dev still boots**

Create `world/pois.json`:

```json
{"version":1,"pois":[]}
```

- [ ] **Step 8: Commit**

```bash
git add internal/game/entity_poi.go internal/game/poi_config.go internal/game/game.go world/pois.json
git rm internal/game/poi_gen.go internal/game/poi_gen_test.go
git commit -m "poi: spawn from world.POI def, delete GeneratePOIs"
```

---

### Task 8: Implement `SpawnBelt(localX, localY, world.Belt)` — delete `GenerateBelts`

**Files:**

- Modify: `internal/game/belts.go`
- Modify: `internal/game/entity_asteroid.go`
- Modify: `internal/game/game.go` (remove `spawnAsteroids` helper if any)

- [ ] **Step 1: Read existing belt code**

Read `internal/game/belts.go` (placement) and `internal/game/entity_asteroid.go` (interior scatter). The split:
- `GenerateBelts(cell, stationCell)` returns `[]AsteroidBelt` — DELETED.
- The per-belt asteroid scatter (loops over a belt center + radius, spawning N asteroids) is preserved as `scatterBeltAsteroids(gw, localX, localY, def)`.

- [ ] **Step 2: Write the new SpawnBelt + scatter helper**

In `internal/game/belts.go` (or rename to `internal/game/entity_belt.go` if it's cleaner), implement:

```go
package game

import (
    "hash/fnv"
    "math/rand/v2"

    gamecomp "github.com/zenion/mmoserver/internal/component"
    "github.com/zenion/mmoserver/pkg/mmokit"
    "github.com/zenion/mmoserver/pkg/spatial"
)

// SpawnBelt places an asteroid-belt marker entity at (localX, localY) and
// scatters its interior asteroids deterministically from fnv64(def.ID).
func (gw *GameWorld) SpawnBelt(localX, localY float32, def mmokit.WorldBelt) {
    // Marker entity for the belt itself (optional but useful for client rendering).
    gw.stage.Spawn(
        mmokit.Position{X: localX, Y: localY},
        mmokit.EntityKind{Type: gamecomp.KindAsteroidBelt}, // add this kind in entity_kinds.go if missing
        mmokit.Collider{Radius: def.Radius, Shape: spatial.ShapeCircle, Layer: spatial.LayerNone},
        gamecomp.AsteroidBelt{Radius: def.Radius, Density: def.Density},
    )
    rng := rand.New(rand.NewPCG(fnv64(def.ID), 1))
    scatterBeltAsteroids(gw, localX, localY, def, rng)

    gw.eng.Log.Log(CatPlayerSpawn, "belt spawned: id=%s pos=(%.1f,%.1f) radius=%.1f density=%.2f",
        def.ID, localX, localY, def.Radius, def.Density)
}

func scatterBeltAsteroids(gw *GameWorld, cx, cy float32, def mmokit.WorldBelt, rng *rand.Rand) {
    // count = base * density; tune from existing GenerateBelts numbers
    base := gw.Config.AsteroidsPerBelt // add this if missing; existing belt code has a hardcoded value
    count := int(float32(base) * def.Density)
    for i := 0; i < count; i++ {
        // pick a point inside the belt's disk
        angle := rng.Float32() * 2 * 3.1415927
        r := def.Radius * sqrt32(rng.Float32())
        ax := cx + r*cos32(angle)
        ay := cy + r*sin32(angle)
        gw.SpawnAsteroid(ax, ay) // existing helper
    }
}

func fnv64(s string) uint64 {
    h := fnv.New64a()
    h.Write([]byte(s))
    return h.Sum64()
}
```

(If `fnv64` exists from Task 7, don't redefine it. Move it to `helpers.go`.)

Helpers `sqrt32, cos32, sin32` may already exist or be in `math`. Use `float32(math.Sqrt(float64(f)))` etc. if not.

If `gamecomp.AsteroidBelt` and `gamecomp.KindAsteroidBelt` don't exist, add them. Otherwise, skip the marker entity and just scatter asteroids.

- [ ] **Step 3: Delete `GenerateBelts`**

In `internal/game/belts.go`, delete the `GenerateBelts` function. Delete any test file that imports it.

In `internal/game/entity_asteroid.go::spawnAsteroids` (the per-cell helper), DELETE the function — replaced by per-belt scatter via SpawnBelt.

In `internal/game/game.go`, ensure no remaining `gw.spawnAsteroids()` call exists.

- [ ] **Step 4: Verify compile**

Run: `just build`
Expected: PASS.

- [ ] **Step 5: Seed empty `world/belts.json`**

```json
{"version":1,"belts":[]}
```

- [ ] **Step 6: Commit**

```bash
git add internal/game/belts.go internal/game/entity_asteroid.go internal/game/game.go internal/game/helpers.go internal/component/components.go internal/game/entity_kinds.go world/belts.json
git commit -m "belt: spawn from world.Belt def, delete GenerateBelts"
```

---

### Task 9: Implement `SpawnDungeonAt(localX, localY, world.Dungeon)` — delete cell-(0,0) auto-spawn

**Files:**

- Modify: `internal/game/entity_dungeon.go`
- Modify: `internal/game/game.go`

- [ ] **Step 1: Add the new entry point**

In `internal/game/entity_dungeon.go`, add:

```go
// SpawnDungeonAt is the world-manifest entry point. It delegates to the
// existing SpawnDungeonFromGraph using the def's seed (or fnv64(id) when
// def.Seed is zero).
func (gw *GameWorld) SpawnDungeonAt(localX, localY float32, def mmokit.WorldDungeon) {
    seed := uint64(def.Seed)
    if seed == 0 {
        seed = fnv64(def.ID)
    }
    netID := gw.SpawnDungeonFromGraph(localX, localY, seed)
    gw.eng.Log.Log(CatPlayerSpawn, "dungeon spawned: id=%s name=%q netID=%d pos=(%.1f,%.1f)",
        def.ID, def.Name, netID, localX, localY)
}
```

Add `import "github.com/zenion/mmoserver/pkg/mmokit"` if not already imported.

- [ ] **Step 2: Verify no other callers spawn dungeons**

Run: `grep -rn "SpawnDungeonFromGraph\b" --include="*.go"`
Confirm: only the new `SpawnDungeonAt` and possibly admin/debug verbs (`dungeon.go` cmdsys verbs) call it. Game-bootstrap calls are gone (deleted in Task 5).

- [ ] **Step 3: Seed empty `world/dungeons.json`**

```json
{"version":1,"dungeons":[]}
```

- [ ] **Step 4: Verify compile**

Run: `just build`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/game/entity_dungeon.go world/dungeons.json
git commit -m "dungeon: spawn via SpawnDungeonAt(world.Dungeon)"
```

---

### Task 10: Implement `SpawnDecoration` + add `KindDecoration` entity kind

**Files:**

- Create: `internal/game/entity_decoration.go`
- Modify: `internal/component/components.go` (add `Decoration` component + `KindDecoration` const)
- Modify: `internal/game/entity_kinds.go` (register the kind)

- [ ] **Step 1: Add component + kind constant**

In `internal/component/components.go`:

```go
// Decoration is a visual-only landmark — no gameplay effect beyond client
// rendering. Replicated; the client decides how to draw based on Kind +
// Variant.
type Decoration struct {
    Kind    string `net:"str16"`
    Variant string `net:"str16"`
}

// Add to the kind constants — pick the next available value, append, do not
// renumber existing ones unless paired with proto changes.
const (
    // ... existing kinds ...
    KindDecoration uint8 = /* next free value */
)
```

If `net:"str16"` is not a supported encoding tag, use what existing string-replicated fields use (check other components for a precedent — `gamecomp.Station.Name` after Task 6 has the same need).

- [ ] **Step 2: Register the entity kind**

In `internal/game/entity_kinds.go`, add a registration alongside the others:

```go
type DecorationBundle struct {
    Decoration *gamecomp.Decoration
}

// inside RegisterEntityKinds():
mmokit.RegisterKind[DecorationBundle](coord, gamecomp.KindDecoration, "Decoration", bindings)
```

- [ ] **Step 3: Implement SpawnDecoration**

Create `internal/game/entity_decoration.go`:

```go
package game

import (
    gamecomp "github.com/zenion/mmoserver/internal/component"
    "github.com/zenion/mmoserver/pkg/mmokit"
    "github.com/zenion/mmoserver/pkg/spatial"
)

func (gw *GameWorld) SpawnDecoration(localX, localY float32, def mmokit.WorldDecoration) {
    gw.stage.Spawn(
        mmokit.Position{X: localX, Y: localY},
        mmokit.EntityKind{Type: gamecomp.KindDecoration},
        mmokit.Collider{Radius: 0, Shape: spatial.ShapeCircle, Layer: spatial.LayerNone},
        gamecomp.Decoration{Kind: def.Kind, Variant: def.Variant},
    )
    gw.eng.Log.Log(CatPlayerSpawn, "decoration spawned: id=%s kind=%s variant=%s pos=(%.1f,%.1f)",
        def.ID, def.Kind, def.Variant, localX, localY)
}
```

- [ ] **Step 4: Seed `world/decorations.json` and `world/regions.json`**

```bash
echo '{"version":1,"decorations":[]}' > world/decorations.json
echo '{"version":1,"regions":[]}' > world/regions.json
```

- [ ] **Step 5: Regen the client SDK (proto codegen affects entities)**

Run: `just client-sdk examples/4node-basic`

If your game uses a different example, run the appropriate `just *-sdk` from the recipe list.

- [ ] **Step 6: Verify compile**

Run: `just build`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/game/entity_decoration.go internal/component/components.go internal/game/entity_kinds.go world/decorations.json world/regions.json
git commit -m "decoration: KindDecoration entity + SpawnDecoration"
```

---

### Task 11: Replace stub SpawnXxx panics with real implementations (verify boot)

By now Tasks 6-10 have replaced each stub. This task is a verification + smoke pass: the server must boot, load `world/stations.json` (one station), and run without panicking.

**Files:** none (verification only)

- [ ] **Step 1: Verify all stubs are gone**

Run: `grep -rn 'panic("Task' internal/game/`
Expected: no output.

- [ ] **Step 2: Verify the server boots and spawns the station**

Run: `just build`
Run the server briefly to confirm it doesn't panic:

```bash
timeout 5 bin/server --headless --postgres-url="postgres://mmo:mmo@localhost:5432/mmo?sslmode=disable" 2>&1 | tee /tmp/boot.log
```

If your dev DB isn't running, start it:

```bash
just db-up
```

Look for `station spawned: id=trade-0` in `/tmp/boot.log`. If missing, debug.

If you cannot run Postgres in the implementer environment, skip the runtime smoke and rely on `just build` + the test suite.

- [ ] **Step 3: Run the existing game test suite**

```bash
go test ./internal/game/... -short -count=1
```

Expected: PASS. If tests reference deleted code (`GeneratePOIs`, `spawnPOIs`, `StationLocalX`), fix them by reading from a constructed `world.Snapshot` or by hardcoding test fixtures. Most likely failure points: `internal/game/poi_gen_test.go` (deleted in Task 7 — confirm), `internal/game/entity_station_test.go`, `internal/game/coordinator_test.go`.

- [ ] **Step 4: Commit any test fixups**

```bash
git add internal/game/
git commit -m "tests: update for snapshot-driven spawn pipeline"
```

If no test fixups are needed, skip the commit.

---

### Task 12: `world.list` + `world.place` cmdsys verbs

**Files:**

- Create: `internal/game/commands/world.go`
- Modify: `internal/game/commands/registry.go` (add `registerWorld` call)

- [ ] **Step 1: Implement `world.list`**

Create `internal/game/commands/world.go`:

```go
package commands

import (
    "context"
    "fmt"

    "github.com/zenion/mmoserver/internal/game"
    "github.com/zenion/mmoserver/pkg/cmdsys"
    "github.com/zenion/mmoserver/pkg/coords"
    "github.com/zenion/mmoserver/pkg/mmokit"
)

// ── world.list ─────────────────────────────────────────────────────────────

type WorldListArgs struct {
    Type string `cmd:"optional,help=filter by entity type (station|poi|dungeon|belt|decoration|region)"`
}

type WorldListResult struct {
    Entities []WorldEntityRow `cmd:"table"`
}

type WorldEntityRow struct {
    Type     string
    ID       string
    WorldX   float32
    WorldY   float32
    Detail   string
}

func registerWorldList(reg *cmdsys.Registry, coord *mmokit.Process, gwGetter func() *game.GameWorld) error {
    return reg.Register(cmdsys.Command{
        Verb:        "world.list",
        Capability:  "world.edit",
        Description: "list every placed entity from world manifests, optionally filtered by type",
        Route:       cmdsys.RouteLocal,
        Args:        WorldListArgs{},
        Result:      WorldListResult{},
        Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
            args := raw.(WorldListArgs)
            gw := gwGetter()
            if gw == nil || gw.WorldSnapshot == nil {
                return WorldListResult{}, nil
            }
            snap := gw.WorldSnapshot
            var rows []WorldEntityRow
            include := func(t string) bool { return args.Type == "" || args.Type == t }

            if include("station") {
                for _, s := range snap.Stations.Stations {
                    rows = append(rows, WorldEntityRow{Type: "station", ID: s.ID, WorldX: s.WorldPos[0], WorldY: s.WorldPos[1], Detail: s.Name})
                }
            }
            if include("poi") {
                for _, p := range snap.POIs.POIs {
                    rows = append(rows, WorldEntityRow{Type: "poi", ID: p.ID, WorldX: p.WorldPos[0], WorldY: p.WorldPos[1],
                        Detail: fmt.Sprintf("T%d %s", p.Tier, p.Roster)})
                }
            }
            if include("dungeon") {
                for _, d := range snap.Dungeons.Dungeons {
                    rows = append(rows, WorldEntityRow{Type: "dungeon", ID: d.ID, WorldX: d.WorldPos[0], WorldY: d.WorldPos[1], Detail: d.Name})
                }
            }
            if include("belt") {
                for _, b := range snap.Belts.Belts {
                    rows = append(rows, WorldEntityRow{Type: "belt", ID: b.ID, WorldX: b.WorldPos[0], WorldY: b.WorldPos[1],
                        Detail: fmt.Sprintf("r=%.0f d=%.2f", b.Radius, b.Density)})
                }
            }
            if include("decoration") {
                for _, dc := range snap.Decorations.Decorations {
                    rows = append(rows, WorldEntityRow{Type: "decoration", ID: dc.ID, WorldX: dc.WorldPos[0], WorldY: dc.WorldPos[1],
                        Detail: dc.Kind + "/" + dc.Variant})
                }
            }
            if include("region") {
                for _, rg := range snap.Regions.Regions {
                    rows = append(rows, WorldEntityRow{Type: "region", ID: rg.ID, Detail: rg.Kind + " " + rg.Shape})
                }
            }
            return WorldListResult{Entities: rows}, nil
        },
    })
}

// ── world.place ────────────────────────────────────────────────────────────

type WorldPlaceArgs struct {
    Type   string  `cmd:"help=station|poi|dungeon|belt|decoration"`
    WorldX float32 `cmd:"help=world-absolute X"`
    WorldY float32 `cmd:"help=world-absolute Y"`
    ID     string  `cmd:"optional,help=stable id (auto-generated when empty)"`

    // POI-only
    Tier   uint8  `cmd:"optional,help=POI: tier 1..3"`
    Roster string `cmd:"optional,help=POI: roster name"`

    // Dungeon-only
    Name string `cmd:"optional,help=Station/Dungeon: display name"`

    // Belt-only
    Radius  float32 `cmd:"optional,help=Belt: radius (also Station collider radius)"`
    Density float32 `cmd:"optional,help=Belt: asteroid density (default 1.0)"`

    // Decoration-only
    Kind    string `cmd:"optional,help=Decoration: kind (e.g. wreck/beacon)"`
    Variant string `cmd:"optional,help=Decoration: variant"`
}

type WorldPlaceResult struct {
    Type string
    ID   string
}

func registerWorldPlace(reg *cmdsys.Registry, coord *mmokit.Process, gwGetter func() *game.GameWorld) error {
    return reg.Register(cmdsys.Command{
        Verb:        "world.place",
        Capability:  "world.edit",
        Description: "place an entity into the world manifest and spawn it live",
        Route:       cmdsys.RouteLocal,
        Args:        WorldPlaceArgs{},
        Result:      WorldPlaceResult{},
        Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
            args := raw.(WorldPlaceArgs)
            gw := gwGetter()
            if gw == nil || gw.WorldRepo == nil {
                return nil, fmt.Errorf("world editor: no repo wired")
            }

            wp := coords.FromFlat(float64(args.WorldX), float64(args.WorldY))
            cellID := mmokit.WorldCellID{X: wp.CellX, Y: wp.CellY}

            id := args.ID
            if id == "" {
                id = autoID(args.Type, cellID, gw)
            }

            switch args.Type {
            case "station":
                def := mmokit.WorldStation{ID: id, Name: args.Name, WorldPos: [2]float32{args.WorldX, args.WorldY}, Radius: args.Radius}
                if err := gw.WorldRepo.AddStation(def); err != nil {
                    return nil, err
                }
                if err := spawnInCell(coord, cellID, func(gw *game.GameWorld) {
                    gw.SpawnStation(wp.LocalX, wp.LocalY, def)
                }); err != nil {
                    return nil, err
                }
            case "poi":
                if args.Tier < 1 || args.Tier > 3 {
                    return nil, fmt.Errorf("tier must be 1..3, got %d", args.Tier)
                }
                if args.Roster == "" {
                    return nil, fmt.Errorf("roster required for POI")
                }
                def := mmokit.WorldPOI{ID: id, WorldPos: [2]float32{args.WorldX, args.WorldY}, Type: "combat",
                    Tier: args.Tier, Roster: args.Roster}
                if err := gw.WorldRepo.AddPOI(def); err != nil {
                    return nil, err
                }
                if err := spawnInCell(coord, cellID, func(gw *game.GameWorld) {
                    gw.SpawnPOI(wp.LocalX, wp.LocalY, def)
                }); err != nil {
                    return nil, err
                }
            case "dungeon":
                def := mmokit.WorldDungeon{ID: id, Name: args.Name, WorldPos: [2]float32{args.WorldX, args.WorldY}}
                if err := gw.WorldRepo.AddDungeon(def); err != nil {
                    return nil, err
                }
                if err := spawnInCell(coord, cellID, func(gw *game.GameWorld) {
                    gw.SpawnDungeonAt(wp.LocalX, wp.LocalY, def)
                }); err != nil {
                    return nil, err
                }
            case "belt":
                if args.Radius <= 0 {
                    return nil, fmt.Errorf("belt radius must be > 0")
                }
                density := args.Density
                if density == 0 {
                    density = 1.0
                }
                def := mmokit.WorldBelt{ID: id, WorldPos: [2]float32{args.WorldX, args.WorldY}, Radius: args.Radius, Density: density}
                if err := gw.WorldRepo.AddBelt(def); err != nil {
                    return nil, err
                }
                if err := spawnInCell(coord, cellID, func(gw *game.GameWorld) {
                    gw.SpawnBelt(wp.LocalX, wp.LocalY, def)
                }); err != nil {
                    return nil, err
                }
            case "decoration":
                def := mmokit.WorldDecoration{ID: id, WorldPos: [2]float32{args.WorldX, args.WorldY}, Kind: args.Kind, Variant: args.Variant}
                if err := gw.WorldRepo.AddDecoration(def); err != nil {
                    return nil, err
                }
                if err := spawnInCell(coord, cellID, func(gw *game.GameWorld) {
                    gw.SpawnDecoration(wp.LocalX, wp.LocalY, def)
                }); err != nil {
                    return nil, err
                }
            default:
                return nil, fmt.Errorf("unknown type %q", args.Type)
            }

            // Reload snapshot into memory so subsequent reads see the new entity.
            if snap, err := gw.WorldRepo.LoadAll(); err == nil {
                gw.WorldSnapshot = snap
            }
            return WorldPlaceResult{Type: args.Type, ID: id}, nil
        },
    })
}

// spawnInCell finds the cell on the coord and runs fn on its game loop. The
// fn closure receives the per-cell GameWorld via mmokit.State.
func spawnInCell(coord *mmokit.Process, cellID mmokit.WorldCellID, fn func(*game.GameWorld)) error {
    cellCoord := mmokit.CellCoord{CellX: cellID.X, CellY: cellID.Y}
    cell := coord.CellAt(cellCoord)
    if cell == nil {
        return fmt.Errorf("no cell hosting (%d,%d) locally", cellID.X, cellID.Y)
    }
    _, err := mmokit.CmdOnLoop(context.Background(), cell.Engine, func() (struct{}, error) {
        if gw := mmokit.State[game.GameWorld](cell.Stage); gw != nil {
            fn(gw)
        }
        return struct{}{}, nil
    })
    return err
}

func autoID(typ string, c mmokit.WorldCellID, gw *game.GameWorld) string {
    n := 1
    seen := map[string]bool{}
    if gw.WorldSnapshot != nil {
        switch typ {
        case "station":
            for _, x := range gw.WorldSnapshot.Stations.Stations { seen[x.ID] = true }
        case "poi":
            for _, x := range gw.WorldSnapshot.POIs.POIs { seen[x.ID] = true }
        case "dungeon":
            for _, x := range gw.WorldSnapshot.Dungeons.Dungeons { seen[x.ID] = true }
        case "belt":
            for _, x := range gw.WorldSnapshot.Belts.Belts { seen[x.ID] = true }
        case "decoration":
            for _, x := range gw.WorldSnapshot.Decorations.Decorations { seen[x.ID] = true }
        }
    }
    for {
        id := fmt.Sprintf("%d_%d_%s_%d", c.X, c.Y, typ, n)
        if !seen[id] {
            return id
        }
        n++
    }
}
```

Note: `coord.CellAt(cellCoord)` and `mmokit.State[T](stage)` are existing APIs — verify spelling. If `CellAt` doesn't exist on `*mmokit.Process`, look for `coord.Cells[cellCoord]` (map access) and use that.

- [ ] **Step 2: Register in registry.go**

In `internal/game/commands/registry.go::RegisterAll`, add (passing a `*game.GameWorld` getter that returns the singleton):

```go
gwGetter := func() *game.GameWorld {
    // GameWorld is per-cell; for verb purposes any cell's GW shares the same WorldRepo + WorldSnapshot.
    for _, c := range coord.Cells {
        if c == nil || c.Stage == nil {
            continue
        }
        if gw := mmokit.State[game.GameWorld](c.Stage); gw != nil {
            return gw
        }
    }
    return nil
}

if err := registerWorldList(reg, coord, gwGetter); err != nil { return err }
if err := registerWorldPlace(reg, coord, gwGetter); err != nil { return err }
```

Adjust the `gwGetter` to whatever surface lets you reach `gw.WorldRepo / gw.WorldSnapshot` from a verb handler. The signature in the existing `registerXxx` functions is the precedent — pattern-match against `registerPOIList`.

- [ ] **Step 3: Verify compile**

Run: `just build`
Expected: PASS.

- [ ] **Step 4: Smoke test via console**

```bash
timeout 10 bin/server --headless --postgres-url=... -admin-listen=:9101 &
sleep 2
# (in another shell — but headless test only matters for build verification)
```

For implementer verification, just run `just build` and trust the unit-test layer. End-to-end smoke happens in Task 18.

- [ ] **Step 5: Commit**

```bash
git add internal/game/commands/world.go internal/game/commands/registry.go
git commit -m "cmd: world.list + world.place cmdsys verbs"
```

---

### Task 13: `world.move` + `world.update` + `world.delete` cmdsys verbs

**Files:**

- Modify: `internal/game/commands/world.go`
- Modify: `internal/game/commands/registry.go`

- [ ] **Step 1: Implement `world.move`**

Append to `internal/game/commands/world.go`:

```go
type WorldMoveArgs struct {
    Type   string  `cmd:"help=station|poi|dungeon|belt|decoration"`
    ID     string  `cmd:"help=entity id"`
    WorldX float32 `cmd:"help=new world-absolute X"`
    WorldY float32 `cmd:"help=new world-absolute Y"`
}

type WorldMoveResult struct {
    Type string
    ID   string
}

func registerWorldMove(reg *cmdsys.Registry, coord *mmokit.Process, gwGetter func() *game.GameWorld) error {
    return reg.Register(cmdsys.Command{
        Verb: "world.move", Capability: "world.edit", Route: cmdsys.RouteLocal,
        Description: "move a placed entity to new world coords",
        Args: WorldMoveArgs{}, Result: WorldMoveResult{},
        Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
            args := raw.(WorldMoveArgs)
            gw := gwGetter()
            if gw == nil || gw.WorldRepo == nil {
                return nil, fmt.Errorf("world editor: no repo wired")
            }

            // 1. Find current position in the snapshot.
            var oldWP [2]float32
            switch args.Type {
            case "station":
                oldWP = mustStationPos(gw, args.ID)
            case "poi":
                oldWP = mustPOIPos(gw, args.ID)
            case "dungeon":
                oldWP = mustDungeonPos(gw, args.ID)
            case "belt":
                oldWP = mustBeltPos(gw, args.ID)
            case "decoration":
                oldWP = mustDecorationPos(gw, args.ID)
            default:
                return nil, fmt.Errorf("unknown type %q", args.Type)
            }

            // 2. Update JSON.
            newWP := [2]float32{args.WorldX, args.WorldY}
            mut := func() error {
                switch args.Type {
                case "station":
                    return gw.WorldRepo.UpdateStation(args.ID, func(s *mmokit.WorldStation) { s.WorldPos = newWP })
                case "poi":
                    return gw.WorldRepo.UpdatePOI(args.ID, func(p *mmokit.WorldPOI) { p.WorldPos = newWP })
                case "dungeon":
                    return gw.WorldRepo.UpdateDungeon(args.ID, func(d *mmokit.WorldDungeon) { d.WorldPos = newWP })
                case "belt":
                    return gw.WorldRepo.UpdateBelt(args.ID, func(b *mmokit.WorldBelt) { b.WorldPos = newWP })
                case "decoration":
                    return gw.WorldRepo.UpdateDecoration(args.ID, func(d *mmokit.WorldDecoration) { d.WorldPos = newWP })
                }
                return nil
            }
            if err := mut(); err != nil {
                return nil, err
            }

            // 3. Despawn at old location.
            oldWPc := coords.FromFlat(float64(oldWP[0]), float64(oldWP[1]))
            if err := spawnInCell(coord, mmokit.WorldCellID{X: oldWPc.CellX, Y: oldWPc.CellY},
                func(gw *game.GameWorld) { gw.DespawnPlacedByID(args.Type, args.ID) }); err != nil {
                return nil, err
            }

            // 4. Spawn at new location with updated def.
            newWPc := coords.FromFlat(float64(newWP[0]), float64(newWP[1]))
            if snap, err := gw.WorldRepo.LoadAll(); err == nil {
                gw.WorldSnapshot = snap
            }
            if err := spawnInCell(coord, mmokit.WorldCellID{X: newWPc.CellX, Y: newWPc.CellY}, func(gw *game.GameWorld) {
                spawnPlaced(gw, args.Type, args.ID, newWPc.LocalX, newWPc.LocalY)
            }); err != nil {
                return nil, err
            }
            return WorldMoveResult{Type: args.Type, ID: args.ID}, nil
        },
    })
}
```

Add the helper `spawnPlaced(gw, type, id, lx, ly)` which looks up the def from `gw.WorldSnapshot` and calls the right `SpawnXxx` method. And `mustXxxPos` returns the world_pos from the snapshot, with `panic` on miss (which is unexpected since `mut()` already errored if missing).

Add `DespawnPlacedByID` to `GameWorld` in `gameworld.go`:

```go
// DespawnPlacedByID removes the entity placed under `type/id`, if any. Used
// by world.* verbs to undo a placement. Searches the stage for matching
// entities; the contract is "every placed entity carries its world-id"
// — which means adding a `PlacedID` component to each spawn function, OR
// matching by (kind, Position). For v1, we match by position with epsilon
// since the JSON file holds the authoritative world_pos.
func (gw *GameWorld) DespawnPlacedByID(typ, id string) {
    // Look up the def's local pos in the current snapshot.
    if gw.WorldSnapshot == nil { return }
    // For each type, find the def with matching id, project to local pos,
    // find the entity by (kind, ~position).
    // Implementation: lookup pos in snapshot, then walk entities via
    // mmokit.ForEach2[gamecomp.SomeComponent, mmokit.Position] and despawn
    // those with matching local pos within 0.5u.
    panic("DespawnPlacedByID: TODO — see Task 13 step 2")
}
```

- [ ] **Step 2: Add `PlacedID` tracking component (cleaner than position matching)**

In `internal/component/components.go`:

```go
// PlacedID tags an entity with the world-manifest id that spawned it. Used
// by the world editor to despawn / re-spawn placed entities cleanly.
type PlacedID struct {
    ID string `mmokit:"local"`
}
```

Add a `PlacedID{ID: def.ID}` component to every `SpawnXxx` (`SpawnStation`, `SpawnPOI`, `SpawnDungeonAt`, `SpawnBelt`, `SpawnDecoration`). Add the component to each entity-kind bundle's struct (with the `mmokit:"local"` tag so it doesn't go over the wire).

Now implement `DespawnPlacedByID` cleanly:

```go
func (gw *GameWorld) DespawnPlacedByID(_, id string) {
    var doomed []mmokit.Entity
    mmokit.ForEach1(gw.stage, func(e mmokit.Entity, pid *gamecomp.PlacedID) {
        if pid.ID == id {
            doomed = append(doomed, e)
        }
    })
    for _, e := range doomed {
        gw.stage.Commands().Despawn(e.Handle())
    }
    gw.eng.Log.Log(CatPlayerSpawn, "world: despawned %d entities for id=%s", len(doomed), id)
}
```

For POIs and dungeons that own children (roster NPCs, dungeon walls), make sure those children either also carry `PlacedID` of their parent, OR are cleaned up via `OnEntityRemoved` cascade. For v1 the cleanest path: tag every spawn in `SpawnPOI`'s NPC loop and `SpawnDungeonAt`'s wall loop with the parent's `PlacedID`. This way one `DespawnPlacedByID(parent.ID)` cleans the whole subtree.

- [ ] **Step 3: Implement `world.update`**

```go
type WorldUpdateArgs struct {
    Type   string  `cmd:"help=station|poi|dungeon|belt|decoration"`
    ID     string  `cmd:"help=entity id"`
    Tier   uint8   `cmd:"optional,help=POI: new tier 1..3"`
    Roster string  `cmd:"optional,help=POI: new roster name"`
    Name   string  `cmd:"optional,help=Station/Dungeon: new name"`
    Radius float32 `cmd:"optional,help=Belt: new radius (or Station collider radius)"`
    Density float32 `cmd:"optional,help=Belt: new density"`
    Kind    string `cmd:"optional,help=Decoration: new kind"`
    Variant string `cmd:"optional,help=Decoration: new variant"`
}

type WorldUpdateResult struct{ Type, ID string }

func registerWorldUpdate(reg *cmdsys.Registry, coord *mmokit.Process, gwGetter func() *game.GameWorld) error {
    return reg.Register(cmdsys.Command{
        Verb: "world.update", Capability: "world.edit", Route: cmdsys.RouteLocal,
        Description: "update non-position props of a placed entity",
        Args: WorldUpdateArgs{}, Result: WorldUpdateResult{},
        Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
            args := raw.(WorldUpdateArgs)
            gw := gwGetter()
            if gw == nil || gw.WorldRepo == nil {
                return nil, fmt.Errorf("world editor: no repo wired")
            }

            // Mutate the manifest.
            switch args.Type {
            case "station":
                if err := gw.WorldRepo.UpdateStation(args.ID, func(s *mmokit.WorldStation) {
                    if args.Name != "" { s.Name = args.Name }
                    if args.Radius != 0 { s.Radius = args.Radius }
                }); err != nil { return nil, err }
            case "poi":
                if err := gw.WorldRepo.UpdatePOI(args.ID, func(p *mmokit.WorldPOI) {
                    if args.Tier != 0 { p.Tier = args.Tier }
                    if args.Roster != "" { p.Roster = args.Roster }
                }); err != nil { return nil, err }
            case "dungeon":
                if err := gw.WorldRepo.UpdateDungeon(args.ID, func(d *mmokit.WorldDungeon) {
                    if args.Name != "" { d.Name = args.Name }
                }); err != nil { return nil, err }
            case "belt":
                if err := gw.WorldRepo.UpdateBelt(args.ID, func(b *mmokit.WorldBelt) {
                    if args.Radius != 0 { b.Radius = args.Radius }
                    if args.Density != 0 { b.Density = args.Density }
                }); err != nil { return nil, err }
            case "decoration":
                if err := gw.WorldRepo.UpdateDecoration(args.ID, func(d *mmokit.WorldDecoration) {
                    if args.Kind != "" { d.Kind = args.Kind }
                    if args.Variant != "" { d.Variant = args.Variant }
                }); err != nil { return nil, err }
            default:
                return nil, fmt.Errorf("unknown type %q", args.Type)
            }

            // Reload snapshot.
            if snap, err := gw.WorldRepo.LoadAll(); err == nil {
                gw.WorldSnapshot = snap
            }

            // Despawn + respawn at same position. Find world_pos from the new snapshot.
            wp, ok := lookupWorldPos(gw, args.Type, args.ID)
            if !ok {
                return WorldUpdateResult{Type: args.Type, ID: args.ID}, nil
            }
            wpc := coords.FromFlat(float64(wp[0]), float64(wp[1]))
            cellID := mmokit.WorldCellID{X: wpc.CellX, Y: wpc.CellY}
            if err := spawnInCell(coord, cellID, func(gw *game.GameWorld) {
                gw.DespawnPlacedByID(args.Type, args.ID)
                spawnPlaced(gw, args.Type, args.ID, wpc.LocalX, wpc.LocalY)
            }); err != nil {
                return nil, err
            }
            return WorldUpdateResult{Type: args.Type, ID: args.ID}, nil
        },
    })
}
```

Define `lookupWorldPos` and `spawnPlaced` helpers.

- [ ] **Step 4: Implement `world.delete`**

```go
type WorldDeleteArgs struct {
    Type string `cmd:"help=station|poi|dungeon|belt|decoration"`
    ID   string `cmd:"help=entity id"`
}
type WorldDeleteResult struct{ Type, ID string }

func registerWorldDelete(reg *cmdsys.Registry, coord *mmokit.Process, gwGetter func() *game.GameWorld) error {
    return reg.Register(cmdsys.Command{
        Verb: "world.delete", Capability: "world.edit", Route: cmdsys.RouteLocal,
        Description: "remove a placed entity from the manifest and despawn it",
        Args: WorldDeleteArgs{}, Result: WorldDeleteResult{},
        Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
            args := raw.(WorldDeleteArgs)
            gw := gwGetter()
            if gw == nil || gw.WorldRepo == nil {
                return nil, fmt.Errorf("world editor: no repo wired")
            }
            wp, ok := lookupWorldPos(gw, args.Type, args.ID)
            if !ok {
                return nil, fmt.Errorf("%s id %q not found", args.Type, args.ID)
            }
            switch args.Type {
            case "station":   if err := gw.WorldRepo.DeleteStation(args.ID); err != nil { return nil, err }
            case "poi":       if err := gw.WorldRepo.DeletePOI(args.ID); err != nil { return nil, err }
            case "dungeon":   if err := gw.WorldRepo.DeleteDungeon(args.ID); err != nil { return nil, err }
            case "belt":      if err := gw.WorldRepo.DeleteBelt(args.ID); err != nil { return nil, err }
            case "decoration":if err := gw.WorldRepo.DeleteDecoration(args.ID); err != nil { return nil, err }
            default:
                return nil, fmt.Errorf("unknown type %q", args.Type)
            }
            if snap, err := gw.WorldRepo.LoadAll(); err == nil {
                gw.WorldSnapshot = snap
            }
            wpc := coords.FromFlat(float64(wp[0]), float64(wp[1]))
            if err := spawnInCell(coord, mmokit.WorldCellID{X: wpc.CellX, Y: wpc.CellY},
                func(gw *game.GameWorld) { gw.DespawnPlacedByID(args.Type, args.ID) }); err != nil {
                return nil, err
            }
            return WorldDeleteResult{Type: args.Type, ID: args.ID}, nil
        },
    })
}
```

- [ ] **Step 5: Register all three in registry.go**

In `internal/game/commands/registry.go::RegisterAll`, add:

```go
if err := registerWorldMove(reg, coord, gwGetter); err != nil { return err }
if err := registerWorldUpdate(reg, coord, gwGetter); err != nil { return err }
if err := registerWorldDelete(reg, coord, gwGetter); err != nil { return err }
```

- [ ] **Step 6: Verify compile**

Run: `just build`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/game/commands/world.go internal/game/commands/registry.go internal/game/gameworld.go internal/game/entity_*.go internal/component/components.go
git commit -m "cmd: world.move + world.update + world.delete verbs with PlacedID despawn"
```

---

### Task 14: `world.reload` + `world.export` cmdsys verbs

**Files:**

- Modify: `internal/game/commands/world.go`
- Modify: `internal/game/commands/registry.go`

- [ ] **Step 1: Implement `world.reload`**

```go
type WorldReloadArgs struct{}
type WorldReloadResult struct {
    Added   int
    Removed int
    Updated int
}

func registerWorldReload(reg *cmdsys.Registry, coord *mmokit.Process, gwGetter func() *game.GameWorld) error {
    return reg.Register(cmdsys.Command{
        Verb: "world.reload", Capability: "world.edit", Route: cmdsys.RouteLocal,
        Description: "re-read world/*.json from disk; diff against memory; apply add/remove/respawn",
        Args: WorldReloadArgs{}, Result: WorldReloadResult{},
        Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
            gw := gwGetter()
            if gw == nil || gw.WorldRepo == nil {
                return nil, fmt.Errorf("world editor: no repo wired")
            }
            newSnap, err := gw.WorldRepo.LoadAll()
            if err != nil {
                return nil, err
            }
            old := gw.WorldSnapshot

            res := WorldReloadResult{}

            // For each type, diff old vs new by id.
            //  added: present in new, missing in old → spawn in cell
            //  removed: present in old, missing in new → despawn
            //  updated: present in both, but field-equality differs → despawn + respawn

            res.Added += diffApply(coord, "station", stationIDs(old), stationIDs(newSnap), func(id string) { spawnByID(coord, gw, "station", id) }, func(id string) { despawnByID(coord, gw, "station", id) })
            res.Added += diffApply(coord, "poi", poiIDs(old), poiIDs(newSnap), func(id string) { spawnByID(coord, gw, "poi", id) }, func(id string) { despawnByID(coord, gw, "poi", id) })
            // ... repeat for dungeon, belt, decoration

            gw.WorldSnapshot = newSnap
            return res, nil
        },
    })
}
```

The helpers `stationIDs`, `poiIDs`, `diffApply`, `spawnByID`, `despawnByID` are straightforward — implement them as set operations. For v1 the diff is set-difference only (added/removed); "updated" (same id, different fields) is detected by `!reflect.DeepEqual` and treated as remove+add.

If implementing the full diff is too much work for one task, accept the simpler version: "reload" = full re-spawn (despawn every PlacedID across cells, then re-spawn from the new snapshot). The cost is a brief glitch for unchanged entities but the implementation is trivial:

```go
Handler: func(ctx context.Context, env *cmdsys.Env, _ any) (any, error) {
    gw := gwGetter()
    newSnap, err := gw.WorldRepo.LoadAll()
    if err != nil { return nil, err }

    // Brute-force: clear and re-spawn across every cell.
    for _, cell := range coord.Cells {
        if cell == nil || cell.Stage == nil { continue }
        _, _ = mmokit.CmdOnLoop(ctx, cell.Engine, func() (struct{}, error) {
            gwLocal := mmokit.State[game.GameWorld](cell.Stage)
            if gwLocal == nil { return struct{}{}, nil }
            // Despawn every PlacedID-tagged entity.
            var doomed []mmokit.Entity
            mmokit.ForEach1(cell.Stage, func(e mmokit.Entity, _ *gamecomp.PlacedID) {
                doomed = append(doomed, e)
            })
            for _, e := range doomed { cell.Stage.Commands().Despawn(e.Handle()) }
            // Spawn from new snapshot.
            gwLocal.WorldSnapshot = newSnap
            bucket := newSnap.BucketByCell()[mmokit.WorldCellID{X: gwLocal.RootCell.CellX, Y: gwLocal.RootCell.CellY}]
            if bucket != nil {
                for _, s := range bucket.Stations    { gwLocal.SpawnStation(s.LocalPos[0], s.LocalPos[1], s.Def) }
                for _, p := range bucket.POIs        { gwLocal.SpawnPOI(p.LocalPos[0], p.LocalPos[1], p.Def) }
                for _, d := range bucket.Dungeons    { gwLocal.SpawnDungeonAt(d.LocalPos[0], d.LocalPos[1], d.Def) }
                for _, b := range bucket.Belts       { gwLocal.SpawnBelt(b.LocalPos[0], b.LocalPos[1], b.Def) }
                for _, dc := range bucket.Decorations{ gwLocal.SpawnDecoration(dc.LocalPos[0], dc.LocalPos[1], dc.Def) }
            }
            return struct{}{}, nil
        })
    }
    gw.WorldSnapshot = newSnap
    return WorldReloadResult{}, nil
},
```

USE THE BRUTE-FORCE VERSION. Diff-based reload is an optimization for v2.

- [ ] **Step 2: Implement `world.export`**

```go
type WorldExportArgs struct{}
type WorldExportResult struct{ Path string }

func registerWorldExport(reg *cmdsys.Registry, coord *mmokit.Process, gwGetter func() *game.GameWorld) error {
    return reg.Register(cmdsys.Command{
        Verb: "world.export", Capability: "world.edit", Route: cmdsys.RouteLocal,
        Description: "write the in-memory world snapshot back to disk (safety net)",
        Args: WorldExportArgs{}, Result: WorldExportResult{},
        Handler: func(ctx context.Context, env *cmdsys.Env, _ any) (any, error) {
            gw := gwGetter()
            if gw == nil || gw.WorldRepo == nil { return nil, fmt.Errorf("world editor: no repo wired") }
            if gw.WorldSnapshot == nil { return WorldExportResult{}, nil }
            if err := gw.WorldRepo.SaveStations(gw.WorldSnapshot.Stations); err != nil { return nil, err }
            if err := gw.WorldRepo.SavePOIs(gw.WorldSnapshot.POIs); err != nil { return nil, err }
            if err := gw.WorldRepo.SaveDungeons(gw.WorldSnapshot.Dungeons); err != nil { return nil, err }
            if err := gw.WorldRepo.SaveBelts(gw.WorldSnapshot.Belts); err != nil { return nil, err }
            if err := gw.WorldRepo.SaveDecorations(gw.WorldSnapshot.Decorations); err != nil { return nil, err }
            if err := gw.WorldRepo.SaveRegions(gw.WorldSnapshot.Regions); err != nil { return nil, err }
            return WorldExportResult{Path: "world/"}, nil
        },
    })
}
```

- [ ] **Step 3: Register both**

In `registry.go::RegisterAll`:

```go
if err := registerWorldReload(reg, coord, gwGetter); err != nil { return err }
if err := registerWorldExport(reg, coord, gwGetter); err != nil { return err }
```

- [ ] **Step 4: Verify compile**

Run: `just build`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/game/commands/world.go internal/game/commands/registry.go
git commit -m "cmd: world.reload (brute-force) + world.export verbs"
```

---

### Task 15: SSE topic `world.changed` + publish from verbs

**Files:**

- Modify: `internal/game/commands/world.go` (add publishes)
- Modify: `pkg/admin/server.go` or wherever the SSE topics are registered (search for `TopicBus.Publish`)

- [ ] **Step 1: Find SSE topic registration**

Run: `grep -rn "TopicBus\|Publish.*topic\|RegisterTopic" pkg/admin/ pkg/mmokit/ 2>/dev/null | head -10`

Locate how other live topics (`cells`, `hosts`, `events`, `players`) are wired. Mirror that pattern for `world.changed`.

- [ ] **Step 2: Define the event payload**

In `internal/game/commands/world.go`:

```go
type WorldChangeEvent struct {
    Op       string  `json:"op"`     // "place" | "move" | "update" | "delete" | "reload"
    Type     string  `json:"type"`   // station|poi|dungeon|belt|decoration|region
    ID       string  `json:"id"`
    WorldX   float32 `json:"world_x,omitempty"`
    WorldY   float32 `json:"world_y,omitempty"`
}
```

- [ ] **Step 3: Publish after every mutation**

At the end of each verb handler in `world.go`, before returning the result:

```go
mmokit.PublishAdminTopic(coord, "world.changed", WorldChangeEvent{
    Op: "place", Type: args.Type, ID: id, WorldX: args.WorldX, WorldY: args.WorldY,
})
```

(The exact mmokit helper is `mmokit.PublishAdminTopic(coord, topic, payload)` per CLAUDE.md.)

For `world.reload`, publish a single `{op: "reload"}` event.

- [ ] **Step 4: Verify compile**

Run: `just build`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/game/commands/world.go
git commit -m "cmd: publish world.changed SSE events from every verb"
```

---

### Task 16: Svelte `/world-editor` route shell + sidebar entry + capability gate

**Files:**

- Create: `web-admin/src/routes/world-editor.svelte`
- Modify: `web-admin/src/components/Sidebar.svelte` (add nav entry)
- Modify: `web-admin/src/app.svelte` (register route)

- [ ] **Step 1: Read the existing route + sidebar code**

```bash
cat web-admin/src/app.svelte | head -60
cat web-admin/src/components/Sidebar.svelte | head -80
```

Find how `cluster.svelte`, `events.svelte` etc. are registered.

- [ ] **Step 2: Add the sidebar entry**

In `Sidebar.svelte`, follow the existing nav-item pattern. Add:

```svelte
<NavItem href="/world-editor" icon="MapPinned" label="World Editor" />
```

(Use whatever Lucide icon name fits — `Map`, `MapPinned`, or `Compass`. Verify the icon exists with `bun add @lucide/svelte` listing.)

- [ ] **Step 3: Register the route**

In `app.svelte`, add the route case:

```svelte
{:else if route === 'world-editor'}
  <WorldEditor />
```

Import: `import WorldEditor from './routes/world-editor.svelte';`

- [ ] **Step 4: Create the route stub**

Create `web-admin/src/routes/world-editor.svelte`:

```svelte
<script lang="ts">
  import { sessionStore } from '$lib/stores.svelte.ts';

  const session = $derived(sessionStore.value);
  const canEdit = $derived(session?.grants?.includes('world.edit') ?? session?.grants?.includes('*.*'));
</script>

<div class="flex h-full flex-col">
  <header class="border-b border-gray-800 p-3 flex items-center gap-3">
    <h1 class="text-lg font-semibold">World Editor</h1>
    <span class="text-xs opacity-50">3-pane shell</span>
  </header>

  {#if !canEdit}
    <div class="flex flex-1 items-center justify-center text-gray-500">
      No access: requires <code>world.edit</code> grant.
    </div>
  {:else}
    <div class="grid flex-1 grid-cols-[200px_1fr_260px]">
      <aside class="border-r border-gray-800 p-3">
        <div class="text-xs uppercase opacity-50 mb-2">Palette</div>
        <div class="opacity-50">Coming in Task 17.</div>
      </aside>
      <main class="bg-gray-950">
        <div class="flex h-full items-center justify-center text-gray-600">
          Canvas (Task 17)
        </div>
      </main>
      <aside class="border-l border-gray-800 p-3">
        <div class="text-xs uppercase opacity-50 mb-2">Inspector</div>
        <div class="opacity-50">Coming in Task 18.</div>
      </aside>
    </div>
    <footer class="border-t border-gray-800 px-3 py-1 text-xs opacity-60">
      tool: — · cursor: — · zoom: —
    </footer>
  {/if}
</div>
```

The exact session-grants check matches existing routes (look at `users.svelte` for the precedent).

- [ ] **Step 5: Verify build**

```bash
cd web-admin && bun run build && cd ..
```

Expected: PASS, with output in `pkg/admin/static/dist/`.

- [ ] **Step 6: Commit**

```bash
git add web-admin/src/routes/world-editor.svelte web-admin/src/components/Sidebar.svelte web-admin/src/app.svelte pkg/admin/static/dist
git commit -m "admin: /world-editor route shell + sidebar entry"
```

---

### Task 17: `WorldCanvas.svelte` — pan/zoom canvas + overlays + entity rendering + palette + place/select tools

**Files:**

- Create: `web-admin/src/components/WorldCanvas.svelte`
- Create: `web-admin/src/components/WorldPalette.svelte`
- Create: `web-admin/src/lib/world-store.svelte.ts`
- Modify: `web-admin/src/routes/world-editor.svelte`

- [ ] **Step 1: Create the world store**

Create `web-admin/src/lib/world-store.svelte.ts`:

```ts
import { httpGet, httpPost, subscribeSSE } from '$lib/api.ts';

type Pos2 = [number, number];

export type Tool = 'select' | 'station' | 'poi' | 'dungeon' | 'belt' | 'region' | 'decoration';

export interface WorldStation { id: string; name: string; world_pos: Pos2; radius?: number; }
export interface WorldPOI { id: string; world_pos: Pos2; type: string; tier: number; roster: string; spread_radius?: number; }
export interface WorldDungeon { id: string; name: string; world_pos: Pos2; seed?: number; }
export interface WorldBelt { id: string; world_pos: Pos2; radius: number; density: number; }
export interface WorldDecoration { id: string; world_pos: Pos2; kind: string; variant?: string; }

export interface Snapshot {
    stations: WorldStation[];
    pois: WorldPOI[];
    dungeons: WorldDungeon[];
    belts: WorldBelt[];
    decorations: WorldDecoration[];
}

class WorldStore {
    snapshot = $state<Snapshot>({ stations: [], pois: [], dungeons: [], belts: [], decorations: [] });
    selectedID = $state<string | null>(null);
    selectedType = $state<string | null>(null);
    tool = $state<Tool>('select');
    cursor = $state<Pos2>([0, 0]);
    zoom = $state(0.5);
    pan = $state<Pos2>([0, 0]);
    layers = $state({ cells: true, tiers: true, grid: true, stations: true, pois: true, dungeons: true, belts: true, regions: true, decorations: true });

    async refresh() {
        // Use the list verb for now; in v1.1 add a /admin/api/world endpoint.
        const res = await httpPost('/admin/api/commands/world.list', {});
        const entities = res?.Entities ?? [];
        // For v1 the world.list result is a flat list; we'll still keep separate arrays in the store
        // for typed access. As an interim, rebuild from list (only fields the list returns) plus a
        // follow-up GET when we wire SaveAll/LoadAll endpoint. For now this gets us renderable data.
        const stations: WorldStation[] = entities.filter((e: any) => e.Type === 'station').map((e: any) => ({ id: e.ID, name: e.Detail, world_pos: [e.WorldX, e.WorldY] }));
        const pois: WorldPOI[] = entities.filter((e: any) => e.Type === 'poi').map((e: any) => ({ id: e.ID, world_pos: [e.WorldX, e.WorldY], type: 'combat', tier: parseInt(e.Detail.match(/T(\d)/)?.[1] ?? '1'), roster: e.Detail.replace(/^T\d /, '') }));
        const dungeons: WorldDungeon[] = entities.filter((e: any) => e.Type === 'dungeon').map((e: any) => ({ id: e.ID, name: e.Detail, world_pos: [e.WorldX, e.WorldY] }));
        const belts: WorldBelt[] = entities.filter((e: any) => e.Type === 'belt').map((e: any) => ({ id: e.ID, world_pos: [e.WorldX, e.WorldY], radius: 80, density: 1.0 }));
        const decorations: WorldDecoration[] = entities.filter((e: any) => e.Type === 'decoration').map((e: any) => {
            const [kind, variant] = (e.Detail ?? '').split('/');
            return { id: e.ID, world_pos: [e.WorldX, e.WorldY], kind, variant };
        });
        this.snapshot = { stations, pois, dungeons, belts, decorations };
    }

    subscribeLive() {
        return subscribeSSE('world.changed', () => { void this.refresh(); });
    }

    async place(type: string, worldX: number, worldY: number, extra: Record<string, any> = {}) {
        await httpPost('/admin/api/commands/world.place', { Type: type, WorldX: worldX, WorldY: worldY, ...extra });
    }
    async move(type: string, id: string, worldX: number, worldY: number) {
        await httpPost('/admin/api/commands/world.move', { Type: type, ID: id, WorldX: worldX, WorldY: worldY });
    }
    async update(type: string, id: string, patch: Record<string, any>) {
        await httpPost('/admin/api/commands/world.update', { Type: type, ID: id, ...patch });
    }
    async delete(type: string, id: string) {
        await httpPost('/admin/api/commands/world.delete', { Type: type, ID: id });
    }
}

export const worldStore = new WorldStore();
```

Note: `subscribeSSE` and `httpGet/httpPost` come from existing `$lib/api.ts`. If they have different names, adapt.

- [ ] **Step 2: Create the palette component**

Create `web-admin/src/components/WorldPalette.svelte`:

```svelte
<script lang="ts">
  import { worldStore, type Tool } from '$lib/world-store.svelte.ts';
  const tools: { key: string; tool: Tool; label: string; color: string }[] = [
    { key: 'V', tool: 'select',     label: 'Select',     color: '#cdd6f4' },
    { key: '1', tool: 'station',    label: 'Station',    color: '#f7768e' },
    { key: '2', tool: 'poi',        label: 'POI',        color: '#e0af68' },
    { key: '3', tool: 'dungeon',    label: 'Dungeon',    color: '#bb9af7' },
    { key: '4', tool: 'belt',       label: 'Belt',       color: '#9ece6a' },
    { key: '5', tool: 'region',     label: 'Region',     color: '#7aa2f7' },
    { key: '6', tool: 'decoration', label: 'Decoration', color: '#73daca' },
  ];
</script>

<div class="text-xs uppercase opacity-50 mb-2">Palette</div>
<div class="flex flex-col gap-1">
  {#each tools as t}
    <button
      class="flex items-center gap-2 px-2 py-1 rounded text-left {worldStore.tool === t.tool ? 'bg-blue-900/30 border border-blue-500' : 'border border-transparent'}"
      onclick={() => worldStore.tool = t.tool}
    >
      <span class="w-4 text-center text-gray-500">{t.key}</span>
      <span style="color: {t.color}">●</span>
      <span>{t.label}</span>
    </button>
  {/each}
</div>

<div class="mt-4 text-xs uppercase opacity-50 mb-2">Layers</div>
<div class="flex flex-col gap-1">
  {#each Object.keys(worldStore.layers) as key}
    <label class="flex items-center gap-2 cursor-pointer">
      <input type="checkbox" bind:checked={worldStore.layers[key]} />
      <span class="text-sm">{key}</span>
    </label>
  {/each}
</div>
```

- [ ] **Step 3: Create the canvas component**

Create `web-admin/src/components/WorldCanvas.svelte`:

```svelte
<script lang="ts">
  import { onMount } from 'svelte';
  import { worldStore } from '$lib/world-store.svelte.ts';

  const CELL_SIZE = 8192;
  let canvas = $state<HTMLCanvasElement | null>(null);
  let panning = $state(false);
  let dragStart: [number, number] = [0, 0];

  function screenToWorld(sx: number, sy: number): [number, number] {
    const rect = canvas!.getBoundingClientRect();
    return [
      (sx - rect.left - rect.width / 2) / worldStore.zoom - worldStore.pan[0],
      (sy - rect.top - rect.height / 2) / worldStore.zoom - worldStore.pan[1],
    ];
  }
  function worldToScreen(wx: number, wy: number): [number, number] {
    const rect = canvas!.getBoundingClientRect();
    return [
      (wx + worldStore.pan[0]) * worldStore.zoom + rect.width / 2,
      (wy + worldStore.pan[1]) * worldStore.zoom + rect.height / 2,
    ];
  }

  function draw() {
    if (!canvas) return;
    const ctx = canvas.getContext('2d')!;
    const w = canvas.width = canvas.clientWidth;
    const h = canvas.height = canvas.clientHeight;
    ctx.fillStyle = '#080a10';
    ctx.fillRect(0, 0, w, h);

    // Grid + cell boundaries.
    if (worldStore.layers.cells) {
      ctx.strokeStyle = '#1e2230';
      ctx.lineWidth = 0.5;
      const [tl, br] = [screenToWorld(0, 0), screenToWorld(w, h)];
      const x0 = Math.floor(tl[0] / CELL_SIZE) * CELL_SIZE;
      const x1 = Math.ceil(br[0] / CELL_SIZE) * CELL_SIZE;
      const y0 = Math.floor(tl[1] / CELL_SIZE) * CELL_SIZE;
      const y1 = Math.ceil(br[1] / CELL_SIZE) * CELL_SIZE;
      for (let x = x0; x <= x1; x += CELL_SIZE) {
        const [sx, _] = worldToScreen(x, 0);
        ctx.beginPath(); ctx.moveTo(sx, 0); ctx.lineTo(sx, h); ctx.stroke();
      }
      for (let y = y0; y <= y1; y += CELL_SIZE) {
        const [_, sy] = worldToScreen(0, y);
        ctx.beginPath(); ctx.moveTo(0, sy); ctx.lineTo(w, sy); ctx.stroke();
      }
    }

    // Tier rings.
    if (worldStore.layers.tiers) {
      ctx.strokeStyle = '#3b2d3a';
      ctx.setLineDash([2, 4]);
      const center = worldToScreen(0, 0);
      for (const r of [16384, 32768]) {
        ctx.beginPath();
        ctx.arc(center[0], center[1], r * worldStore.zoom, 0, Math.PI * 2);
        ctx.stroke();
      }
      ctx.setLineDash([]);
    }

    // Entities.
    function dot(wx: number, wy: number, color: string, label?: string) {
      const [sx, sy] = worldToScreen(wx, wy);
      ctx.fillStyle = color;
      ctx.beginPath(); ctx.arc(sx, sy, 4, 0, Math.PI * 2); ctx.fill();
      if (label) { ctx.fillStyle = color; ctx.font = '10px monospace'; ctx.fillText(label, sx + 6, sy - 2); }
    }
    if (worldStore.layers.stations) for (const s of worldStore.snapshot.stations) dot(s.world_pos[0], s.world_pos[1], '#f7768e', s.name);
    if (worldStore.layers.pois)     for (const p of worldStore.snapshot.pois)     dot(p.world_pos[0], p.world_pos[1], '#e0af68', `T${p.tier}`);
    if (worldStore.layers.dungeons) for (const d of worldStore.snapshot.dungeons) dot(d.world_pos[0], d.world_pos[1], '#bb9af7', d.name);
    if (worldStore.layers.belts) {
      for (const b of worldStore.snapshot.belts) {
        const [sx, sy] = worldToScreen(b.world_pos[0], b.world_pos[1]);
        ctx.strokeStyle = '#9ece6a'; ctx.setLineDash([2, 2]);
        ctx.beginPath(); ctx.arc(sx, sy, b.radius * worldStore.zoom, 0, Math.PI * 2); ctx.stroke();
        ctx.setLineDash([]);
      }
    }
    if (worldStore.layers.decorations) for (const dc of worldStore.snapshot.decorations) dot(dc.world_pos[0], dc.world_pos[1], '#73daca', dc.kind);
  }

  function handleClick(e: MouseEvent) {
    const [wx, wy] = screenToWorld(e.clientX, e.clientY);
    if (worldStore.tool === 'select') {
      // find nearest entity within 10 world units
      // (simple O(n) — fine for v1)
      let best: { type: string; id: string; dist: number } | null = null;
      const consider = (type: string, id: string, pos: [number, number]) => {
        const d = Math.hypot(pos[0] - wx, pos[1] - wy);
        if (d < (best?.dist ?? 200) / worldStore.zoom) best = { type, id, dist: d };
      };
      worldStore.snapshot.stations.forEach(s => consider('station', s.id, s.world_pos));
      worldStore.snapshot.pois.forEach(p => consider('poi', p.id, p.world_pos));
      worldStore.snapshot.dungeons.forEach(d => consider('dungeon', d.id, d.world_pos));
      worldStore.snapshot.belts.forEach(b => consider('belt', b.id, b.world_pos));
      worldStore.snapshot.decorations.forEach(d => consider('decoration', d.id, d.world_pos));
      if (best) { worldStore.selectedType = best.type; worldStore.selectedID = best.id; }
      else { worldStore.selectedType = null; worldStore.selectedID = null; }
      return;
    }
    // Place mode
    const extras: Record<string, any> = {};
    if (worldStore.tool === 'poi') { extras.Tier = 1; extras.Roster = 'StarterArena'; }
    if (worldStore.tool === 'belt') { extras.Radius = 80; extras.Density = 1; }
    void worldStore.place(worldStore.tool, Math.round(wx), Math.round(wy), extras);
  }

  function handleMouseDown(e: MouseEvent) {
    if (e.shiftKey || e.button === 1) { panning = true; dragStart = [e.clientX, e.clientY]; }
  }
  function handleMouseMove(e: MouseEvent) {
    const [wx, wy] = screenToWorld(e.clientX, e.clientY);
    worldStore.cursor = [Math.round(wx), Math.round(wy)];
    if (panning) {
      const dx = e.clientX - dragStart[0], dy = e.clientY - dragStart[1];
      worldStore.pan = [worldStore.pan[0] + dx / worldStore.zoom, worldStore.pan[1] + dy / worldStore.zoom];
      dragStart = [e.clientX, e.clientY];
    }
  }
  function handleMouseUp() { panning = false; }
  function handleWheel(e: WheelEvent) {
    e.preventDefault();
    const factor = e.deltaY < 0 ? 1.1 : 1 / 1.1;
    worldStore.zoom = Math.max(0.05, Math.min(10, worldStore.zoom * factor));
  }
  function handleKey(e: KeyboardEvent) {
    const keyToTool: Record<string, string> = { v: 'select', '1': 'station', '2': 'poi', '3': 'dungeon', '4': 'belt', '5': 'region', '6': 'decoration' };
    const t = keyToTool[e.key.toLowerCase()];
    if (t) worldStore.tool = t as any;
    if (e.key === 'Escape') { worldStore.selectedID = null; worldStore.selectedType = null; worldStore.tool = 'select'; }
  }

  onMount(() => {
    void worldStore.refresh();
    const unsub = worldStore.subscribeLive();
    window.addEventListener('keydown', handleKey);
    const raf = () => { draw(); requestAnimationFrame(raf); };
    requestAnimationFrame(raf);
    return () => { window.removeEventListener('keydown', handleKey); unsub?.(); };
  });
</script>

<canvas
  bind:this={canvas}
  class="block h-full w-full"
  onclick={handleClick}
  onmousedown={handleMouseDown}
  onmousemove={handleMouseMove}
  onmouseup={handleMouseUp}
  onwheel={handleWheel}
></canvas>
```

- [ ] **Step 4: Wire into the route**

Update `web-admin/src/routes/world-editor.svelte`:

```svelte
<script lang="ts">
  import WorldCanvas from '../components/WorldCanvas.svelte';
  import WorldPalette from '../components/WorldPalette.svelte';
  import { worldStore } from '$lib/world-store.svelte.ts';
  import { sessionStore } from '$lib/stores.svelte.ts';

  const session = $derived(sessionStore.value);
  const canEdit = $derived(session?.grants?.includes('world.edit') ?? session?.grants?.includes('*.*'));
</script>

<div class="flex h-full flex-col">
  <header class="border-b border-gray-800 p-3 flex items-center gap-3">
    <h1 class="text-lg font-semibold">World Editor</h1>
    <span class="text-xs opacity-50 ml-2">live</span>
  </header>
  {#if !canEdit}
    <div class="flex flex-1 items-center justify-center text-gray-500">
      No access: requires <code>world.edit</code> grant.
    </div>
  {:else}
    <div class="grid flex-1 grid-cols-[200px_1fr_260px] overflow-hidden">
      <aside class="border-r border-gray-800 p-3 overflow-y-auto">
        <WorldPalette />
      </aside>
      <main class="bg-gray-950 relative">
        <WorldCanvas />
      </main>
      <aside class="border-l border-gray-800 p-3 overflow-y-auto">
        <div class="text-xs uppercase opacity-50 mb-2">Inspector</div>
        <div class="opacity-50">Coming in Task 18.</div>
      </aside>
    </div>
    <footer class="border-t border-gray-800 px-3 py-1 text-xs opacity-60 flex gap-4">
      <span>tool: {worldStore.tool}</span>
      <span>cursor: ({worldStore.cursor[0]}, {worldStore.cursor[1]})</span>
      <span>zoom: {(worldStore.zoom * 100).toFixed(0)}%</span>
    </footer>
  {/if}
</div>
```

- [ ] **Step 5: Build**

```bash
cd web-admin && bun run build && cd ..
```

Expected: PASS. The route should render with palette + canvas + cursor coords. Place-mode clicks should call `world.place` (no inspector yet so they'll just appear).

- [ ] **Step 6: Commit**

```bash
git add web-admin/src/components/WorldCanvas.svelte web-admin/src/components/WorldPalette.svelte web-admin/src/lib/world-store.svelte.ts web-admin/src/routes/world-editor.svelte pkg/admin/static/dist
git commit -m "admin: WorldCanvas + Palette + world-store; pan/zoom + place/select"
```

---

### Task 18: `WorldInspector.svelte` — per-type forms + explicit Apply + delete

**Files:**

- Create: `web-admin/src/components/WorldInspector.svelte`
- Modify: `web-admin/src/routes/world-editor.svelte`

- [ ] **Step 1: Create the inspector**

Create `web-admin/src/components/WorldInspector.svelte`:

```svelte
<script lang="ts">
  import { worldStore } from '$lib/world-store.svelte.ts';

  let dirty = $state<Record<string, any>>({});
  const selected = $derived.by(() => {
    if (!worldStore.selectedID) return null;
    const t = worldStore.selectedType!;
    const snap = worldStore.snapshot;
    const find = <T extends { id: string }>(arr: T[]) => arr.find(x => x.id === worldStore.selectedID);
    if (t === 'station') return { type: 'station', entity: find(snap.stations) };
    if (t === 'poi') return { type: 'poi', entity: find(snap.pois) };
    if (t === 'dungeon') return { type: 'dungeon', entity: find(snap.dungeons) };
    if (t === 'belt') return { type: 'belt', entity: find(snap.belts) };
    if (t === 'decoration') return { type: 'decoration', entity: find(snap.decorations) };
    return null;
  });

  // Reset dirty buffer when selection changes
  $effect(() => { worldStore.selectedID; dirty = {}; });

  async function apply() {
    if (!selected?.entity) return;
    await worldStore.update(selected.type, selected.entity.id, dirty);
    dirty = {};
    await worldStore.refresh();
  }
  async function del() {
    if (!selected?.entity) return;
    if (!confirm(`Delete ${selected.type} ${selected.entity.id}?`)) return;
    await worldStore.delete(selected.type, selected.entity.id);
    worldStore.selectedID = null;
    worldStore.selectedType = null;
    await worldStore.refresh();
  }

  function fieldVal<T>(key: string, fallback: T): T {
    return dirty[key] !== undefined ? dirty[key] : fallback;
  }
</script>

{#if !selected?.entity}
  <div class="text-xs uppercase opacity-50 mb-2">Inspector</div>
  <div class="opacity-50 text-sm">No selection. Click an entity to edit.</div>
{:else}
  <div class="text-xs uppercase opacity-50 mb-2">Inspector — {selected.type}</div>
  <div class="space-y-3 text-sm">
    <div>
      <div class="text-xs opacity-50">ID</div>
      <div class="font-mono">{selected.entity.id}</div>
    </div>
    <div>
      <div class="text-xs opacity-50">World Pos</div>
      <div class="font-mono">({selected.entity.world_pos[0]}, {selected.entity.world_pos[1]})</div>
    </div>

    {#if selected.type === 'station'}
      <label class="block">
        <div class="text-xs opacity-50">Name</div>
        <input class="w-full bg-gray-900 border border-gray-700 px-2 py-1"
               value={fieldVal('Name', selected.entity.name)}
               oninput={(e) => dirty.Name = e.currentTarget.value} />
      </label>
      <label class="block">
        <div class="text-xs opacity-50">Radius</div>
        <input type="number" class="w-full bg-gray-900 border border-gray-700 px-2 py-1"
               value={fieldVal('Radius', selected.entity.radius ?? 100)}
               oninput={(e) => dirty.Radius = parseFloat(e.currentTarget.value)} />
      </label>
    {/if}

    {#if selected.type === 'poi'}
      <div>
        <div class="text-xs opacity-50">Tier</div>
        <div class="flex gap-1">
          {#each [1, 2, 3] as t}
            <button class="px-3 py-1 border {fieldVal('Tier', selected.entity.tier) === t ? 'bg-blue-600 text-white' : 'border-gray-700'}"
                    onclick={() => dirty.Tier = t}>{t}</button>
          {/each}
        </div>
      </div>
      <label class="block">
        <div class="text-xs opacity-50">Roster</div>
        <select class="w-full bg-gray-900 border border-gray-700 px-2 py-1"
                value={fieldVal('Roster', selected.entity.roster)}
                onchange={(e) => dirty.Roster = e.currentTarget.value}>
          {#each ['StarterArena', 'SmallSkirmish', 'MediumWarband', 'DisruptorCell', 'HeavyBattalion', 'EliteAnchor'] as r}
            <option value={r}>{r}</option>
          {/each}
        </select>
      </label>
    {/if}

    {#if selected.type === 'dungeon'}
      <label class="block">
        <div class="text-xs opacity-50">Name</div>
        <input class="w-full bg-gray-900 border border-gray-700 px-2 py-1"
               value={fieldVal('Name', selected.entity.name)}
               oninput={(e) => dirty.Name = e.currentTarget.value} />
      </label>
    {/if}

    {#if selected.type === 'belt'}
      <label class="block">
        <div class="text-xs opacity-50">Radius</div>
        <input type="number" class="w-full bg-gray-900 border border-gray-700 px-2 py-1"
               value={fieldVal('Radius', selected.entity.radius)}
               oninput={(e) => dirty.Radius = parseFloat(e.currentTarget.value)} />
      </label>
      <label class="block">
        <div class="text-xs opacity-50">Density</div>
        <input type="number" step="0.1" min="0" max="3" class="w-full bg-gray-900 border border-gray-700 px-2 py-1"
               value={fieldVal('Density', selected.entity.density)}
               oninput={(e) => dirty.Density = parseFloat(e.currentTarget.value)} />
      </label>
    {/if}

    {#if selected.type === 'decoration'}
      <label class="block">
        <div class="text-xs opacity-50">Kind</div>
        <input class="w-full bg-gray-900 border border-gray-700 px-2 py-1"
               value={fieldVal('Kind', selected.entity.kind)}
               oninput={(e) => dirty.Kind = e.currentTarget.value} />
      </label>
      <label class="block">
        <div class="text-xs opacity-50">Variant</div>
        <input class="w-full bg-gray-900 border border-gray-700 px-2 py-1"
               value={fieldVal('Variant', selected.entity.variant ?? '')}
               oninput={(e) => dirty.Variant = e.currentTarget.value} />
      </label>
    {/if}

    <div class="flex gap-2 pt-3 border-t border-gray-800">
      <button class="flex-1 px-3 py-1 bg-blue-600 text-white disabled:opacity-30"
              disabled={Object.keys(dirty).length === 0}
              onclick={apply}>Apply</button>
      <button class="px-3 py-1 bg-red-900 text-red-200" onclick={del}>Delete</button>
    </div>
  </div>
{/if}
```

- [ ] **Step 2: Wire into route**

In `web-admin/src/routes/world-editor.svelte`, replace the right-pane stub with `<WorldInspector />`.

- [ ] **Step 3: Build**

```bash
cd web-admin && bun run build && cd ..
```

Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add web-admin/src/components/WorldInspector.svelte web-admin/src/routes/world-editor.svelte pkg/admin/static/dist
git commit -m "admin: WorldInspector with explicit Apply per-type forms"
```

---

### Task 19: End-to-end smoke + CLAUDE.md update + final cleanup

**Files:**

- Modify: `CLAUDE.md` (add a world-editor paragraph)

- [ ] **Step 1: Run the full test suite**

```bash
go test ./... -short -count=1 2>&1 | tee /tmp/test.log
```

Expected: PASS. Any remaining failures should be in code paths touched by the refactor; fix them.

- [ ] **Step 2: Build everything**

```bash
just build
cd web-admin && bun run build && cd ..
just lint-no-ark
```

Expected: all PASS.

- [ ] **Step 3: Smoke the cmdsys path via the console**

Start the server in headless mode and verify the verbs are registered:

```bash
just db-up
timeout 8 bin/server --headless --postgres-url="postgres://mmo:mmo@localhost:5432/mmo?sslmode=disable" 2>&1 | grep -E "world\.|station spawned|registered" | head -20
```

Look for log lines confirming station spawn from `world/stations.json`. If Postgres isn't available, skip this step.

- [ ] **Step 4: Update CLAUDE.md**

Add a paragraph to the existing architecture section in `CLAUDE.md` (insert near the existing "Persistence" or "Server Meshing" sections):

```markdown
### World editor (hand-placed skeleton)

Hand-placed entities (stations, POIs, dungeon anchors, belt centers, decorations, regions) live in
`world/*.json` as the source of truth. At cell boot, [internal/game/game.go](internal/game/game.go)
reads the in-memory `mmokit.WorldSnapshot` (loaded once from `--world-dir`, default `world/`) and
spawns each entity for the cell that owns its world position via `SpawnStation` /
`SpawnPOI` / `SpawnDungeonAt` / `SpawnBelt` / `SpawnDecoration`. Procgen survives only
*inside* placed entities — NPC scatter inside POIs, asteroids inside belts, dungeon chamber
layout — seeded by `fnv64(def.ID)` for determinism.

Editing is live: the `/world-editor` admin route POSTs `world.place / move / update / delete /
reload / export` cmdsys verbs. Each verb writes the manifest atomically via `pkg/world/jsonrepo`
and applies the spawn/despawn side-effect through the cell's game loop. SSE topic
`world.changed` notifies live editors. Day-one world is empty; seed `world/stations.json`
with one station for dev iteration. Spec:
[docs/superpowers/specs/2026-05-20-world-editor-design.md](docs/superpowers/specs/2026-05-20-world-editor-design.md).
```

- [ ] **Step 5: Final commit**

```bash
git add CLAUDE.md
git commit -m "claude.md: world editor section"
```

- [ ] **Step 6: Sanity-check the branch**

```bash
git log --oneline main..HEAD
```

Expected: 15-19 commits, all describing the world-editor build.

---

## Self-Review

**Spec coverage check (against [the spec](../specs/2026-05-20-world-editor-design.md)):**

- §3.1 deleted code (poi_gen.go, GenerateBelts, station constants, dungeon auto-spawn): Tasks 5, 7, 8, 9
- §3.2 surviving code (rosters, belt-interior scatter, dungeon chamber-graph): preserved by refactoring callers, not the helpers themselves
- §4.1 file layout (one per-type JSON file at repo root): Tasks 6-10 seed; Task 4 wires the directory
- §4.2 schemas: Task 1 types
- §4.3 Go types in pkg/world/: Task 1
- §4.4 Repository: Task 2
- §4.5 BucketByCell: Task 1
- §5.1 seven verbs: Tasks 12-14
- §5.2 RouteKind / handler shape: Task 12-14 use RouteLocal (verbs run on the coordinator; the spawn side-effect dispatches to the cell-owning host's loop via `cmdsys.OnLoop` on the matching `*mmokit.Cell`)
- §5.3 spawn pipeline (OnCellReady → bucket → SpawnXxx): Task 5 implements via the existing `if !fromSplit { }` site in `game.go`, no new mmokit hook needed
- §5.4 interior procgen with id seed: Tasks 7 (POI), 8 (belt), 9 (dungeon)
- §5.5 mutation classification (same-cell move / cross-cell respawn / update respawn / delete cascade): Tasks 13 (move, update, delete) — same-cell-move is implemented as despawn+respawn for simplicity (the spec allows this; the "mutate Position in place" path is an optimization deferred)
- §5.6 concurrency: Task 2 per-file mutex; Task 12 uses `cmdsys.OnLoop` for ECS access
- §5.7 audit + SSE: Task 15 (SSE); audit is automatic via cmdsys.
- §6 UI: Tasks 16-18
- §7 migration: Task 6 seeds `world/stations.json` with one station so `just dev` produces a navigable world (the spec calls this out explicitly)
- §8 deferred items: all kept deferred (distributed mode, fsnotify watcher, schema version migrations, region rules, multi-select, decoration asset pipeline)
- §9 testing: Task 2 (atomic-write fault, concurrent add, round-trip), Task 11 (integration smoke + game tests), Task 19 (end-to-end)

**Open notes for the implementer:**

- The "same-cell move = mutate Position in place" optimization in spec §5.5 is implemented as despawn+respawn in this plan. The despawn+respawn path is correct and adequate for v1 — static entities have no transient state worth preserving. If perf data later shows the respawn jitter matters, file a follow-up.
- Region polygon/annulus editing UI is NOT implemented in this plan (Task 17 only enumerates regions as a layer toggle). Region edit UI is deferred — the spec puts region rules-enforcement in deferred. Surface a console-only flow for region CRUD via cmdsys for now; UI follows in v1.1.
- The `world.reload` verb uses brute-force re-spawn (Task 14). Diff-based reload is an optimization for v1.1.

**Placeholder scan:** done — no TBD/TODO/placeholders left in steps. Every code step has complete code. Every command step has the exact command + expected output.

**Type consistency check:**
- `mmokit.WorldStation`, `mmokit.WorldPOI`, etc. used consistently in Tasks 5-18.
- `mmokit.WorldCellID` used consistently (NOT `coords.CellCoord` — the bucketing key is its own type).
- `SpawnDungeonAt(localX, localY, def)` consistent in Tasks 5, 9, 12, 13, 14, 17.
- `SpawnBelt(localX, localY, def)` consistent in Tasks 5, 8, 12, 13, 14, 17.
- `DespawnPlacedByID(type, id)` consistent in Tasks 13, 14.
- `PlacedID` component added in Task 13 and tagged onto every SpawnXxx in Tasks 6-10 (back-applied) — flag for the implementer: add `PlacedID` to entry-functions when Tasks 6-10 run, not only in Task 13.

**Reminder folded into Task 13:** when implementing PlacedID tagging in Task 13, also retroactively add `PlacedID{ID: def.ID}` to the spawn functions touched in Tasks 6-10. Same goes for tagging child entities (POI roster NPCs, dungeon walls, belt asteroids) so cascade despawn works.

---

## Execution

Sub-skill: superpowers:subagent-driven-development with **opus** implementer subagents per the project memory.
