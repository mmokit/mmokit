package jsonrepo_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mmokit/mmokit/examples/space/internal/world"
	"github.com/mmokit/mmokit/examples/space/internal/world/jsonrepo"
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
