package universe

import (
	"strings"
	"testing"
)

// The check that motivates the whole thing: a split whose destination
// serializes nothing. §7.3 names this as the failure a 3D binding set would
// produce silently — framework code that statically names component.Position
// matches zero entities, the split moves nothing, and none of the five
// existing invariants notices because all of them are satisfied by an empty
// cell.
func TestEntityConservation(t *testing.T) {
	const (
		parent = MeshCellID("cell_0_0")
		childA = MeshCellID("cell_d1_0_0")
		src    = MeshCellID("cell_1_1")
	)

	for _, c := range []struct {
		name         string
		before       map[uint32]MeshCellID
		after        map[uint32]MeshCellID
		departed     MeshCellID
		wantLoss     bool
		wantContains string
	}{
		{
			name:   "split that moves every entity is conserved",
			before: map[uint32]MeshCellID{1: parent, 2: parent},
			after:  map[uint32]MeshCellID{1: childA, 2: childA},
		},
		{
			name:         "split that serializes nothing loses everything",
			before:       map[uint32]MeshCellID{1: parent, 2: parent},
			after:        map[uint32]MeshCellID{},
			wantLoss:     true,
			wantContains: "2 of 2 authoritative entities vanished",
		},
		{
			name:         "split that drops one entity",
			before:       map[uint32]MeshCellID{1: parent, 2: parent, 3: parent},
			after:        map[uint32]MeshCellID{1: childA, 3: childA},
			wantLoss:     true,
			wantContains: "1 of 3",
		},
		{
			// A migrate hands its cell to another host, so those entities
			// leaving is the operation succeeding, not a loss.
			name:     "migrate may lose exactly the migrating cell",
			before:   map[uint32]MeshCellID{1: src, 2: src, 3: parent},
			after:    map[uint32]MeshCellID{3: parent},
			departed: src,
		},
		{
			// …but only that cell. An entity elsewhere vanishing during a
			// migrate is still a bug.
			name:         "migrate may not lose another cell's entities",
			before:       map[uint32]MeshCellID{1: src, 2: parent},
			after:        map[uint32]MeshCellID{},
			departed:     src,
			wantLoss:     true,
			wantContains: "1 of 2",
		},
		{
			// Gaining is legal: a migrate destination receives entities, and
			// duplicates are invNoDuplicatePresencePerCell's job.
			name:   "gaining entities is not a violation",
			before: map[uint32]MeshCellID{1: parent},
			after:  map[uint32]MeshCellID{1: parent, 9: childA},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := checkEntityConservation(c.before, c.after, c.departed)
			if c.wantLoss && err == nil {
				t.Fatal("expected a conservation violation, got none")
			}
			if !c.wantLoss && err != nil {
				t.Fatalf("unexpected violation: %v", err)
			}
			if c.wantContains != "" && !strings.Contains(err.Error(), c.wantContains) {
				t.Errorf("message %q does not contain %q", err, c.wantContains)
			}
		})
	}
}

// The message has to name where the entities were, because the first question
// on a real violation is whether they all came from one cell.
func TestEntityConservationMessageNamesTheSourceCells(t *testing.T) {
	err := checkEntityConservation(
		map[uint32]MeshCellID{1: "cell_0_0", 2: "cell_1_0"},
		map[uint32]MeshCellID{},
		"",
	)
	if err == nil {
		t.Fatal("expected a violation")
	}
	for _, want := range []string{"cell_0_0", "cell_1_0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message does not name %s: %v", want, err)
		}
	}
}
