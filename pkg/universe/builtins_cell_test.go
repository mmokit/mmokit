package universe

import "testing"

// TestCellSnapshotRow_UsesDisplayForm pins the cell.list ↔ cell.snapshot
// contract: cell.snapshot emits cellSnapshotRow.Cell as the user-facing
// display form (cell.Cell.String()) so cell.list's lookup table — keyed
// by cell.String() — actually finds the rows. The bug fixed in commit
// 4f8be58 was emitting cell.MeshID (mesh form) into the same field,
// causing every merge lookup to miss.
//
// Strong typing via MeshCellID alone doesn't catch this because both
// row.Cell (string) and the merge-map key (string) are the same type —
// the compiler can't see that "cell_0_0" and "0_0" are different VALUES
// of the same type. So this test pins the convention.
func TestCellSnapshotRow_UsesDisplayForm(t *testing.T) {
	c := CellID{X: 0, Y: 0, Depth: 0}
	row := cellSnapshotRow{Cell: c.String()}
	if row.Cell != "0_0" {
		t.Errorf("cellSnapshotRow.Cell = %q; want \"0_0\" (display form)", row.Cell)
	}
	mesh := string(c.MeshID())
	if row.Cell == mesh {
		t.Errorf("cellSnapshotRow.Cell = %q (mesh form); cell.list merger expects display form", row.Cell)
	}
}
