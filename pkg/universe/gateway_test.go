package universe

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/coords"
)

// TestCellAtPosition_RoutesFreshLoginToOwningCell pins the fix for the
// pre-Location anyCellID bug. With CellSize=2000 and a 2×2 grid, a spawn
// point at world (0.85*cellSize, 0.85*cellSize) = (1700, 1700) must
// deterministically resolve to cell_0_0. Prior to the flip-day commit,
// cellAtPosition returned "" whenever coords.CellSize was stale at
// resolve time (8192 default, not yet updated by SetCellSize), and the
// gateway's anyCellID fallback scattered logins across random cells.
func TestCellAtPosition_RoutesFreshLoginToOwningCell(t *testing.T) {
	prev := coords.CellSize
	coords.SetCellSize(2000)
	defer coords.SetCellSize(prev)

	// Build a cachedTopology snapshot matching the 4node-basic layout:
	// cells 0_0 / 1_0 / 0_1 / 1_1 at depth 0.
	topo := &cachedTopology{
		cells: map[string]string{
			"cell_0_0": "host-a",
			"cell_1_0": "host-a",
			"cell_0_1": "host-b",
			"cell_1_1": "host-b",
		},
	}

	cases := []struct {
		name string
		x, y float32
		want string
	}{
		// DefaultSpawn from examples/4node-basic/main.go post-Task-13:
		// {X: CellSize * 0.85, Y: CellSize * 0.85} with CellSize=2000.
		{"default_spawn_corner_of_0_0", 2000 * 0.85, 2000 * 0.85, "cell_0_0"},
		{"center_of_0_0", 1000, 1000, "cell_0_0"},
		{"center_of_1_0", 3000, 1000, "cell_1_0"},
		{"center_of_0_1", 1000, 3000, "cell_0_1"},
		{"center_of_1_1", 3000, 3000, "cell_1_1"},
		// Just inside cell boundaries:
		{"min_corner_0_0", 0, 0, "cell_0_0"},
		{"just_inside_right_edge_of_0_0", 1999.99, 1000, "cell_0_0"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := topo.cellAtPosition(tt.x, tt.y)
			if got != tt.want {
				t.Fatalf("cellAtPosition(%v, %v) = %q, want %q", tt.x, tt.y, got, tt.want)
			}
		})
	}
}

// TestCellAtPosition_OutOfBoundsReturnsEmpty pins the other half of the
// fix: the gateway's processLogin now rejects out-of-bounds spawn points
// (instead of silently falling through to anyCellID). cellAtPosition
// MUST return "" for points outside every cell, so the gateway sees
// a clear signal to reject the login.
func TestCellAtPosition_OutOfBoundsReturnsEmpty(t *testing.T) {
	prev := coords.CellSize
	coords.SetCellSize(2000)
	defer coords.SetCellSize(prev)

	topo := &cachedTopology{
		cells: map[string]string{
			"cell_0_0": "host-a",
			"cell_1_0": "host-a",
			"cell_0_1": "host-b",
			"cell_1_1": "host-b",
		},
	}

	cases := []struct {
		name string
		x, y float32
	}{
		// The original bug: DefaultSpawn from mmokit.WorldCenterOfCell(0, 0)
		// evaluated with stale CellSize=8192 → (4096, 4096). Outside the
		// 2x2 grid of 2000-cells.
		{"stale_cellSize_bug_point", 4096, 4096},
		{"negative_x", -1, 1000},
		{"negative_y", 1000, -1},
		{"x_past_grid_edge", 4001, 1000},
		{"y_past_grid_edge", 1000, 4001},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := topo.cellAtPosition(tt.x, tt.y)
			if got != "" {
				t.Fatalf("cellAtPosition(%v, %v) = %q, want \"\"", tt.x, tt.y, got)
			}
		})
	}
}
