package universe

import (
	"sync"
	"testing"
)

// TestCellIdentity_IsAtomicallyConsistent asserts a reader can never observe a
// cell's mesh ID paired with the wrong coordinate.
//
// The old shape wrote MeshID and Cell as two separate field assignments, so
// even a reader that somehow synchronized correctly could straddle them. The
// identity is now one immutable record swapped atomically.
func TestCellIdentity_IsAtomicallyConsistent(t *testing.T) {
	c := NewCell(CellID{X: 0, Y: 0}.MeshID(), CellID{X: 0, Y: 0})

	stop := make(chan struct{})
	var wg sync.WaitGroup

	// Reader: every observation must be self-consistent.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			mesh, id := c.Identity()
			if mesh != id.MeshID() {
				t.Errorf("torn identity: MeshID=%q but CellID=%v (MeshID()=%q)", mesh, id, id.MeshID())
				return
			}
		}
	}()

	// Writer: rename repeatedly.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 5000; i++ {
			id := CellID{X: int32(i % 7), Y: int32(i % 5), Depth: uint8(i % 3)}
			c.setIdentity(id.MeshID(), id)
		}
		close(stop)
	}()

	wg.Wait()
}

// TestHostCellByID_ResolvesThroughTheMapKey is the regression for the race the
// detector actually caught.
//
// renameCellOnNode re-keys Host.Cells under h.mu BEFORE it swaps the cell's own
// identity on the game loop. CellByID used to scan on the identity field, which
// (a) read a field the rename wrote with no shared lock, and (b) returned the
// pre-rename answer during the window. Resolving through the map key removes
// both problems.
func TestHostCellByID_ResolvesThroughTheMapKey(t *testing.T) {
	h := NewHost("host-a")
	from := CellID{X: 0, Y: 0}
	to := CellID{X: 1, Y: 0}

	cell := NewCell(from.MeshID(), from)
	h.AddCell(from, cell)

	if got := h.CellByID(from.MeshID()); got != cell {
		t.Fatalf("CellByID(%s) = %v, want the registered cell", from.MeshID(), got)
	}
	if got := h.CellByID(to.MeshID()); got != nil {
		t.Fatalf("CellByID(%s) = %v, want nil before the rename", to.MeshID(), got)
	}

	// Reproduce the rename window: the map is re-keyed first, the cell's own
	// identity is swapped afterwards.
	h.RemoveCell(from)
	h.AddCell(to, cell)

	if got := h.CellByID(to.MeshID()); got != cell {
		t.Fatalf("CellByID(%s) = %v mid-rename; the map key is the authority, "+
			"and the coordinator's Cells/CellOwner maps have already moved on", to.MeshID(), got)
	}
	if got := h.CellByID(from.MeshID()); got != nil {
		t.Fatalf("CellByID(%s) = %v mid-rename, want nil", from.MeshID(), got)
	}

	cell.setIdentity(to.MeshID(), to)
	if got := h.CellByID(to.MeshID()); got != cell {
		t.Fatalf("CellByID(%s) = %v after the identity swap", to.MeshID(), got)
	}
}

// TestHostCellByID_RejectsAMalformedID pins that an unparseable mesh ID is a
// miss rather than a panic — the lookup now parses where it used to compare
// strings.
func TestHostCellByID_RejectsAMalformedID(t *testing.T) {
	h := NewHost("host-a")
	h.AddCell(CellID{X: 0, Y: 0}, NewCell(CellID{X: 0, Y: 0}.MeshID(), CellID{X: 0, Y: 0}))

	for _, bad := range []MeshCellID{"", "nonsense", "cell_", "cell_x_y", "cell_0"} {
		if got := h.CellByID(bad); got != nil {
			t.Errorf("CellByID(%q) = %v, want nil", bad, got)
		}
	}
}

// TestHostCellByID_ConcurrentWithRename runs the lookup against a live rename.
// Under -race this is the direct regression for the reported failure:
// Host.CellByID reading identity from a gRPC goroutine while
// renameCellOnNode wrote it from the cell's game loop.
func TestHostCellByID_ConcurrentWithRename(t *testing.T) {
	h := NewHost("host-a")
	a := CellID{X: 0, Y: 0}
	b := CellID{X: 1, Y: 0}
	cell := NewCell(a.MeshID(), a)
	h.AddCell(a, cell)

	stop := make(chan struct{})
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			// Both halves of what the mesh data plane does per inbound frame.
			_ = h.CellByID(a.MeshID())
			_ = h.CellByID(b.MeshID())
			_ = cell.MeshID()
			_, _ = cell.Identity()
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 2000; i++ {
			from, to := a, b
			if i%2 == 1 {
				from, to = b, a
			}
			h.RemoveCell(from)
			h.AddCell(to, cell)
			cell.setIdentity(to.MeshID(), to)
		}
		close(stop)
	}()

	wg.Wait()
}

// TestCellIdentity_SeparateAccessorsMayStraddleARename documents the reason
// Identity() exists.
//
// MeshID() and CellID() are two independent atomic loads, so calling both
// across a concurrent rename can legitimately return values from different
// generations. That is not a bug — it is why any caller needing both halves
// consistently must take them from one Identity() call.
func TestCellIdentity_SeparateAccessorsMayStraddleARename(t *testing.T) {
	a := CellID{X: 0, Y: 0}
	b := CellID{X: 9, Y: 9, Depth: 2}
	c := NewCell(a.MeshID(), a)

	// Single load: always self-consistent.
	for i := 0; i < 100; i++ {
		id := a
		if i%2 == 1 {
			id = b
		}
		c.setIdentity(id.MeshID(), id)
		mesh, got := c.Identity()
		if mesh != got.MeshID() {
			t.Fatalf("Identity() returned a torn pair: %q vs %v", mesh, got)
		}
	}
}

// TestHostCellByID_AcceptsBothIDForms pins a deliberate widening introduced
// when CellByID moved from an exact string comparison to ParseCellID + map
// lookup: the display form ("0_0") now resolves as well as the mesh form
// ("cell_0_0").
//
// This is the sanctioned behaviour, not an accident. ParseCellID exists
// precisely to be the inverse of BOTH CellID.String() and CellID.MeshID() —
// its own doc records that accepting only one form used to silently drop
// every CellAssign message — and the repo rule is to parse cell IDs at
// boundaries with it rather than inventing another syntax.
func TestHostCellByID_AcceptsBothIDForms(t *testing.T) {
	h := NewHost("host-a")
	id := CellID{X: 3, Y: 4}
	cell := NewCell(id.MeshID(), id)
	h.AddCell(id, cell)

	for _, form := range []MeshCellID{"cell_3_4", "3_4"} {
		if got := h.CellByID(form); got != cell {
			t.Errorf("CellByID(%q) = %v, want the registered cell", form, got)
		}
	}
	// A different cell must still miss.
	if got := h.CellByID("cell_4_3"); got != nil {
		t.Errorf("CellByID(cell_4_3) = %v, want nil", got)
	}
}
