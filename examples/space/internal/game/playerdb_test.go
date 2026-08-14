package game

import (
	"testing"

	"github.com/google/uuid"
)

func TestPlayerDataAccessor_RoundTrip(t *testing.T) {
	pd := &PlayerData{Username: "alice"}
	pd.SetCell(3, -2)
	pd.SetPosition(10.5, 20.5)
	if pd.CellX != 3 || pd.CellY != -2 {
		t.Fatalf("SetCell didn't take: %d,%d", pd.CellX, pd.CellY)
	}
	if pd.X != 10.5 || pd.Y != 20.5 {
		t.Fatalf("SetPosition didn't take: %g,%g", pd.X, pd.Y)
	}
	if pd.GetUsername() != "alice" || pd.GetCellX() != 3 || pd.GetCellY() != -2 || pd.GetX() != 10.5 || pd.GetY() != 20.5 {
		t.Fatalf("Getters didn't reflect setter values: %+v", pd)
	}
}

func TestRepoLocator_HitAndMiss(t *testing.T) {
	uid := uuid.New()
	r := &PlayerRepo{
		players:    map[uuid.UUID]*PlayerData{uid: {UserID: uid, Username: "alice", CellX: 1, CellY: 2}},
		byUsername: map[string]uuid.UUID{"alice": uid},
		dirty:      map[uuid.UUID]bool{},
	}
	loc := r.Locator()

	acc, mark, ok := loc.Get("ghost")
	if ok || acc != nil || mark != nil {
		t.Fatalf("miss: got ok=%v acc!=nil:%v mark!=nil:%v, want all zero", ok, acc != nil, mark != nil)
	}

	acc, mark, ok = loc.Get("alice")
	if !ok || acc == nil || mark == nil {
		t.Fatalf("hit: got ok=%v acc!=nil:%v mark!=nil:%v", ok, acc != nil, mark != nil)
	}
	if acc.GetUsername() != "alice" || acc.GetCellX() != 1 || acc.GetCellY() != 2 {
		t.Fatalf("accessor returned wrong PlayerData: %+v", acc)
	}
	mark()
	if !r.dirty[uid] {
		t.Fatal("DirtyMark did not mark alice dirty")
	}
}
