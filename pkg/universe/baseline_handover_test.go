package universe

import (
	"bytes"
	"testing"

	"github.com/zenion/mmoserver/pkg/replication"
)

// --- Case A: non-player entity handoff ---

func TestCollectBaselinesForEntity_EmptyInput(t *testing.T) {
	// No client stores -> empty output.
	out := CollectBaselinesForEntity(nil, 7)
	if len(out) != 0 {
		t.Fatalf("expected 0 entries for nil input, got %d", len(out))
	}
}

func TestCollectBaselinesForEntity_MultipleClients(t *testing.T) {
	// Three client stores, two of which have a baseline for entity 7.
	store1 := replication.NewBaselineStore(replication.AckReliable)
	bl1 := store1.GetOrCreateBaseline(7, 0)
	bl1.Acked = []byte{0xAA, 0xBB}

	store2 := replication.NewBaselineStore(replication.AckReliable)
	bl2 := store2.GetOrCreateBaseline(7, 0)
	bl2.Acked = []byte{0xCC}

	store3 := replication.NewBaselineStore(replication.AckReliable)
	// store3 has no baseline for entity 7 — intentionally skipped.

	stores := map[uint32]*replication.BaselineStore{
		10: store1,
		11: store2,
		12: store3,
	}

	out := CollectBaselinesForEntity(stores, 7)
	if len(out) != 2 {
		t.Fatalf("expected 2 entries (clients 10, 11), got %d", len(out))
	}

	// Find entries by ConnID.
	byConn := map[uint32]*ClientBaselineEntry{}
	for i := range out {
		byConn[out[i].ConnID] = &out[i]
	}

	if e := byConn[10]; e == nil || !bytes.Equal(e.LastAcked, []byte{0xAA, 0xBB}) || e.EntityNetID != 7 {
		t.Fatalf("conn 10 entry wrong: %+v", e)
	}
	if e := byConn[11]; e == nil || !bytes.Equal(e.LastAcked, []byte{0xCC}) || e.EntityNetID != 7 {
		t.Fatalf("conn 11 entry wrong: %+v", e)
	}
	if _, ok := byConn[12]; ok {
		t.Fatal("conn 12 should not have an entry (no baseline for entity 7)")
	}
}

func TestCollectBaselinesForEntity_DeepCopiesAcked(t *testing.T) {
	// The handover payload must own stable bytes. Mutating the source
	// baseline after collection must not affect the payload.
	store := replication.NewBaselineStore(replication.AckReliable)
	bl := store.GetOrCreateBaseline(7, 0)
	bl.Acked = []byte{0xAA, 0xBB, 0xCC}

	stores := map[uint32]*replication.BaselineStore{10: store}
	out := CollectBaselinesForEntity(stores, 7)
	if len(out) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(out))
	}

	// Mutate the source after collection.
	bl.Acked[0] = 0xFF

	if out[0].LastAcked[0] != 0xAA {
		t.Fatalf("LastAcked not deep-copied: got %x, want first byte 0xAA", out[0].LastAcked)
	}
}

// --- Case B: player handoff ---

func TestCollectBaselinesForPlayer_EmptyStore(t *testing.T) {
	store := replication.NewBaselineStore(replication.AckReliable)
	out := CollectBaselinesForPlayer(42, store)
	if len(out) != 0 {
		t.Fatalf("empty store should produce 0 entries, got %d", len(out))
	}
}

func TestCollectBaselinesForPlayer_NilStoreIsSafe(t *testing.T) {
	out := CollectBaselinesForPlayer(42, nil)
	if out != nil && len(out) != 0 {
		t.Fatal("nil store should yield empty slice, not panic")
	}
}

func TestCollectBaselinesForPlayer_MultipleEntities(t *testing.T) {
	// Player 42's baseline store has three entities.
	store := replication.NewBaselineStore(replication.AckReliable)
	store.GetOrCreateBaseline(100, 0).Acked = []byte{0x01}
	store.GetOrCreateBaseline(101, 0).Acked = []byte{0x02, 0x03}
	store.GetOrCreateBaseline(102, 0).Acked = []byte{0x04}
	// One baseline with nil Acked — should still be emitted (receiver
	// will recreate on first post-commit tick).
	store.GetOrCreateBaseline(103, 0)

	out := CollectBaselinesForPlayer(42, store)
	if len(out) != 4 {
		t.Fatalf("expected 4 entries, got %d", len(out))
	}

	// Every entry must be keyed to conn 42.
	for i, e := range out {
		if e.ConnID != 42 {
			t.Fatalf("entry %d ConnID = %d, want 42", i, e.ConnID)
		}
	}

	// Find by EntityNetID.
	byEntity := map[uint32]*ClientBaselineEntry{}
	for i := range out {
		byEntity[out[i].EntityNetID] = &out[i]
	}
	if e := byEntity[100]; e == nil || !bytes.Equal(e.LastAcked, []byte{0x01}) {
		t.Fatalf("entity 100 wrong: %+v", e)
	}
	if e := byEntity[101]; e == nil || !bytes.Equal(e.LastAcked, []byte{0x02, 0x03}) {
		t.Fatalf("entity 101 wrong: %+v", e)
	}
	if e := byEntity[102]; e == nil || !bytes.Equal(e.LastAcked, []byte{0x04}) {
		t.Fatalf("entity 102 wrong: %+v", e)
	}
	// Entity 103's Acked was never set; the entry should exist but with
	// empty LastAcked (or nil — either is acceptable).
	if e := byEntity[103]; e == nil {
		t.Fatal("entity 103 entry missing")
	}
}

func TestCollectBaselinesForPlayer_DeepCopiesAcked(t *testing.T) {
	store := replication.NewBaselineStore(replication.AckReliable)
	bl := store.GetOrCreateBaseline(100, 0)
	bl.Acked = []byte{0xAA, 0xBB}

	out := CollectBaselinesForPlayer(42, store)
	if len(out) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(out))
	}

	bl.Acked[0] = 0xFF

	if out[0].LastAcked[0] != 0xAA {
		t.Fatalf("LastAcked not deep-copied: got %x", out[0].LastAcked)
	}
}
