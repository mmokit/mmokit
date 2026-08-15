package universe

// CellsMatching returns this process's local cells that pass the operator
// --node / --cell filters. Both filters are optional; an empty string matches
// everything, so CellsMatching("", "") is every local cell.
//
// The cell filter accepts any format ParseCellID understands and is
// canonicalised to a MeshCellID before comparison, so "0,0" and "cell_0_0"
// select the same cell. An unparseable value is compared verbatim rather than
// silently matching everything — a typo returns nothing instead of fanning the
// operation across the cluster.
//
// The built-in operator verbs (wasm.*, tune.*) are RouteAllHosts: every host
// runs the handler and iterates only the cells it owns, which is why this is
// scoped to p.Cells rather than the cluster-wide ownership map.
func (p *Process) CellsMatching(node, cell string) []*Cell {
	var wantCell string
	if cell != "" {
		if canon, err := ParseCellID(cell); err == nil {
			wantCell = string(canon.MeshID())
		} else {
			wantCell = cell
		}
	}
	var out []*Cell
	for id, c := range p.Cells {
		if node != "" && p.HostIDForCell(c) != node {
			continue
		}
		if cell != "" && string(id) != wantCell {
			continue
		}
		out = append(out, c)
	}
	return out
}
