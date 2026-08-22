package pathfinding

// LOSChecker reports whether a straight line from a to b is unobstructed.
// In production callers pass a closure that wraps spatial.HashGrid.RaycastXY
// with the appropriate layer mask.
type LOSChecker interface {
	Clear(a, b Vec2) bool
}

// SmoothLOS post-processes a waypoint list by greedily skipping waypoints
// reachable from an earlier waypoint via direct LOS. Returns a new slice;
// the input is unmodified. The first and last waypoints are always
// preserved.
//
// O(n²) in the worst case; n is the path length so this is fine in
// practice for the path lengths AStar produces (≤ a few hundred).
func SmoothLOS(in []Vec2, los LOSChecker) []Vec2 {
	if len(in) <= 2 {
		return append([]Vec2(nil), in...)
	}
	out := []Vec2{in[0]}
	i := 0
	for i < len(in)-1 {
		// farthest j reachable from i with LOS clear
		far := i + 1
		for j := len(in) - 1; j > i; j-- {
			if los.Clear(in[i], in[j]) {
				far = j
				break
			}
		}
		out = append(out, in[far])
		i = far
	}
	return out
}
