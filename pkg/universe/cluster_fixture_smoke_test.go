package universe

import (
	"context"
	"testing"
	"time"
)

// TestFixtureSmoke_Ownership exercises CellOwner + HostOwnsCell + CellOn
// across the default 2×2 / 1-host layout (all cells on "host-a").
func TestFixtureSmoke_Ownership(t *testing.T) {
	forEachTopology(t, FixtureConfig{}, func(t *testing.T, fx clusterFixture) {
		// Default config has 1 host ("host-a"); all 4 cells land there.
		cells := []string{
			string(CellID{X: 0, Y: 0}.MeshID()),
			string(CellID{X: 1, Y: 0}.MeshID()),
			string(CellID{X: 0, Y: 1}.MeshID()),
			string(CellID{X: 1, Y: 1}.MeshID()),
		}
		for _, key := range cells {
			if got := fx.CellOwner(key); got != "host-a" {
				t.Errorf("CellOwner(%s) = %q, want %q", key, got, "host-a")
			}
			if !fx.HostOwnsCell("host-a", key) {
				t.Errorf("HostOwnsCell(host-a, %s) = false, want true", key)
			}
			if fx.CellOn("host-a", key) == nil {
				t.Errorf("CellOn(host-a, %s) returned nil for owner", key)
			}
		}
	})
}

// TestFixtureSmoke_HostIDs checks HostIDs returns the declared list in order.
func TestFixtureSmoke_HostIDs(t *testing.T) {
	forEachTopology(t, FixtureConfig{HostIDs: []string{"host-a", "host-b"}}, func(t *testing.T, fx clusterFixture) {
		ids := fx.HostIDs()
		if len(ids) != 2 || ids[0] != "host-a" || ids[1] != "host-b" {
			t.Errorf("HostIDs = %v, want [host-a host-b]", ids)
		}
	})
}

// TestFixtureSmoke_CoordIsCoordRole asserts that fx.Coord() returns a
// Process with the coord role (and the orchestrator wired).
func TestFixtureSmoke_CoordIsCoordRole(t *testing.T) {
	forEachTopology(t, FixtureConfig{}, func(t *testing.T, fx clusterFixture) {
		c := fx.Coord()
		if c == nil {
			t.Fatal("Coord() is nil")
		}
		if c.orchestrator == nil {
			t.Fatal("Coord().orchestrator is nil — not a coord-role Process")
		}
		if !c.Roles().Has(RoleCoordinator) {
			t.Errorf("Coord().Roles()=%s, missing RoleCoordinator", c.Roles())
		}
	})
}

// TestFixtureSmoke_WaitForCellOwner is a no-op on freshly-seeded layouts
// (the cell is already owned) but ensures the API doesn't hang or error.
func TestFixtureSmoke_WaitForCellOwner(t *testing.T) {
	forEachTopology(t, FixtureConfig{}, func(t *testing.T, fx clusterFixture) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		key := string(CellID{X: 0, Y: 0}.MeshID())
		if err := fx.WaitForCellOwner(ctx, key, "host-a"); err != nil {
			t.Errorf("WaitForCellOwner: %v", err)
		}
	})
}

// TestFixtureSmoke_MissingHost ensures HostOwnsCell returns false for
// an unknown host ID rather than panicking.
func TestFixtureSmoke_MissingHost(t *testing.T) {
	forEachTopology(t, FixtureConfig{}, func(t *testing.T, fx clusterFixture) {
		key := string(CellID{X: 0, Y: 0}.MeshID())
		if fx.HostOwnsCell("host-ghost", key) {
			t.Error("HostOwnsCell returned true for unknown host")
		}
		if fx.CellOn("host-ghost", key) != nil {
			t.Error("CellOn returned non-nil for unknown host")
		}
	})
}
