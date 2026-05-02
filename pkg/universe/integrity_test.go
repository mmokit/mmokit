package universe

import (
	"strings"
	"testing"
)

func TestInvariant_CoordMapsConsistent_OK(t *testing.T) {
	c := &Process{
		Cells:     make(map[string]*Cell),
		CellOwner: make(map[CellID]string),
	}
	cell := CellID{X: 0, Y: 0}
	c.Cells["cell_0_0"] = &Cell{Cell: cell, ID: "cell_0_0"}
	c.CellOwner[cell] = "cell_0_0"

	if err := invCoordMapsConsistent.Check(c); err != nil {
		t.Fatalf("expected OK, got %v", err)
	}
}

func TestInvariant_CoordMapsConsistent_MissingCellOwner(t *testing.T) {
	c := &Process{
		Cells:     make(map[string]*Cell),
		CellOwner: make(map[CellID]string),
	}
	cell := CellID{X: 0, Y: 0}
	c.Cells["cell_0_0"] = &Cell{Cell: cell, ID: "cell_0_0"}
	// Deliberately leave CellOwner empty.

	err := invCoordMapsConsistent.Check(c)
	if err == nil {
		t.Fatal("expected violation, got nil")
	}
	if !strings.Contains(err.Error(), "cell_0_0") {
		t.Fatalf("error should mention the offending cell, got %v", err)
	}
}

func TestInvariant_HostOwnershipMatchesCoord_OK(t *testing.T) {
	host := &Host{ID: "host-a", Cells: make(map[CellID]*Cell)}
	cell := CellID{X: 0, Y: 0}
	host.Cells[cell] = &Cell{Cell: cell, ID: "cell_0_0"}
	c := &Process{
		Cells:     map[string]*Cell{"cell_0_0": host.Cells[cell]},
		CellOwner: map[CellID]string{cell: "cell_0_0"},
		Hosts:     map[string]*Host{"host-a": host},
	}
	c.Control = &ControlPlane{cellToHostMap: map[string]string{"cell_0_0": "host-a"}}

	if err := invHostOwnershipMatchesCoord.Check(c); err != nil {
		t.Fatalf("expected OK, got %v", err)
	}
}

func TestInvariant_HostOwnershipMatchesCoord_HostMissingCell(t *testing.T) {
	host := &Host{ID: "host-a", Cells: make(map[CellID]*Cell)}
	// Deliberately don't register the cell on the host.
	cell := CellID{X: 0, Y: 0}
	c := &Process{
		Cells:     map[string]*Cell{"cell_0_0": {Cell: cell, ID: "cell_0_0"}},
		CellOwner: map[CellID]string{cell: "cell_0_0"},
		Hosts:     map[string]*Host{"host-a": host},
	}
	c.Control = &ControlPlane{cellToHostMap: map[string]string{"cell_0_0": "host-a"}}

	err := invHostOwnershipMatchesCoord.Check(c)
	if err == nil {
		t.Fatal("expected violation, got nil")
	}
}

func TestInvariant_TopologyNeighborsOwned_OK(t *testing.T) {
	a := CellID{X: 0, Y: 0}
	b := CellID{X: 1, Y: 0}
	c := &Process{}
	c.Control = &ControlPlane{
		Topology: Topology{Neighbors: map[CellID][]CellID{a: {b}, b: {a}}},
		cellToHostMap: map[string]string{
			string(a.MeshID()): "host-a",
			string(b.MeshID()): "host-a",
		},
	}
	if err := invTopologyNeighborsOwned.Check(c); err != nil {
		t.Fatalf("expected OK, got %v", err)
	}
}

func TestInvariant_TopologyNeighborsOwned_OrphanNeighbor(t *testing.T) {
	a := CellID{X: 0, Y: 0}
	b := CellID{X: 1, Y: 0}
	c := &Process{}
	c.Control = &ControlPlane{
		Topology: Topology{Neighbors: map[CellID][]CellID{a: {b}}},
		cellToHostMap: map[string]string{
			string(a.MeshID()): "host-a", // deliberately omit b
		},
	}
	err := invTopologyNeighborsOwned.Check(c)
	if err == nil {
		t.Fatal("expected violation, got nil")
	}
}

func TestInvariant_SessionRouteHostLive_OK(t *testing.T) {
	c := &Process{
		hostRegistry:  NewHostRegistry(nil),
		sessionRoutes: newSessionRoutes(),
	}
	c.hostRegistry.Register("host-a", "", false)
	c.sessionRoutes.Set(&SessionRoute{
		Key:    SessionKey{GatewayID: "gw", ConnID: 1},
		HostID: "host-a",
		CellID: "cell_0_0",
	})
	if err := invSessionRouteHostLive.Check(c); err != nil {
		t.Fatalf("expected OK, got %v", err)
	}
}

func TestInvariant_SessionRouteHostLive_OrphanHost(t *testing.T) {
	c := &Process{
		hostRegistry:  NewHostRegistry(nil),
		sessionRoutes: newSessionRoutes(),
	}
	// host-a is NOT registered.
	c.sessionRoutes.Set(&SessionRoute{
		Key:    SessionKey{GatewayID: "gw", ConnID: 1},
		HostID: "host-a",
		CellID: "cell_0_0",
	})
	err := invSessionRouteHostLive.Check(c)
	if err == nil {
		t.Fatal("expected violation, got nil")
	}
}

func TestInvariant_NoDuplicatePresencePerCell_Smoke(t *testing.T) {
	// Smoke-test: empty Process has no duplicates.
	c := &Process{Cells: make(map[string]*Cell)}
	if err := invNoDuplicatePresencePerCell.Check(c); err != nil {
		t.Fatalf("empty Process should pass: %v", err)
	}
}
