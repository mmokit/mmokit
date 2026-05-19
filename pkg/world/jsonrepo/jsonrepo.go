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
