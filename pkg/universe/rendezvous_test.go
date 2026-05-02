package universe

import (
	"fmt"
	"math"
	"reflect"
	"testing"
)

func TestAssignCellToHostStability(t *testing.T) {
	// Calling AssignCellToHost with the same (cellID, hosts) returns the
	// same host regardless of input order. This is the core rendezvous
	// guarantee: stable under restart.
	hosts1 := []string{"h1", "h2", "h3"}
	hosts2 := []string{"h3", "h1", "h2"}
	for _, cellID := range []string{"cell_0_0", "cell_1_0", "cell_2_3", "cell_9_9"} {
		a := AssignCellToHost(cellID, hosts1)
		b := AssignCellToHost(cellID, hosts2)
		if a != b {
			t.Errorf("%s: order-sensitive! got %q vs %q", cellID, a, b)
		}
	}
}

func TestAssignCellToHostEmpty(t *testing.T) {
	// Empty host list returns "" (not a panic).
	if got := AssignCellToHost("cell_0_0", nil); got != "" {
		t.Errorf("empty hosts: got %q, want \"\"", got)
	}
	if got := AssignCellToHost("cell_0_0", []string{}); got != "" {
		t.Errorf("empty slice: got %q, want \"\"", got)
	}
}

func TestAssignCellToHostDistribution(t *testing.T) {
	// Run 1000 synthetic cell IDs across 4 hosts. Each host's count
	// should be within ±15% of the fair share (250). Rendezvous is
	// known to produce very even distributions.
	const (
		numCells  = 1000
		numHosts  = 4
	)
	fairShare := numCells / numHosts
	toleranceHi := int(float64(fairShare) * 1.15)
	toleranceLo := int(float64(fairShare) * 0.85)
	hosts := []string{"h1", "h2", "h3", "h4"}
	counts := make(map[string]int, numHosts)
	for i := 0; i < numCells; i++ {
		cellID := fmt.Sprintf("cell_%d_%d", i, i*7+3)
		host := AssignCellToHost(cellID, hosts)
		counts[host]++
	}
	if len(counts) != numHosts {
		t.Fatalf("some hosts received no cells: counts=%v", counts)
	}
	for h, c := range counts {
		if c < toleranceLo || c > toleranceHi {
			t.Errorf("host %s count %d outside [%d, %d]", h, c, toleranceLo, toleranceHi)
		}
	}
}

func TestAssignCellsAcrossHostsDeterministic(t *testing.T) {
	// Two calls with identical inputs must return identical maps.
	cells := []string{"cell_0_0", "cell_1_0", "cell_2_0", "cell_0_1", "cell_1_1", "cell_2_1"}
	hosts := []string{"h-a", "h-b", "h-c"}
	a := AssignCellsAcrossHosts(cells, hosts)
	b := AssignCellsAcrossHosts(cells, hosts)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("non-deterministic:\n  a=%v\n  b=%v", a, b)
	}
}

func TestAssignCellToHostMinimalRebalance(t *testing.T) {
	// Assign 100 cells across 4 hosts, then remove one host. Assert the
	// 3 survivors keep approximately 3/4 of their prior assignments.
	// The cells that had the removed host as winner must migrate to
	// another host; cells that had a different host as winner must stay.
	hosts4 := []string{"h1", "h2", "h3", "h4"}
	hosts3 := []string{"h1", "h2", "h3"} // h4 removed

	const numCells = 1000 // larger sample for stable statistics
	beforeAssignments := make(map[string]string, numCells)
	for i := 0; i < numCells; i++ {
		cid := fmt.Sprintf("cell_%d_%d", i, i*7+3)
		beforeAssignments[cid] = AssignCellToHost(cid, hosts4)
	}
	afterAssignments := make(map[string]string, numCells)
	for cid := range beforeAssignments {
		afterAssignments[cid] = AssignCellToHost(cid, hosts3)
	}

	// Count: how many cells changed winners?
	changed := 0
	for cid, before := range beforeAssignments {
		if afterAssignments[cid] != before {
			changed++
		}
	}

	// Expected: roughly 1/4 of cells (those assigned to h4) migrate.
	expected := numCells / 4
	// Allow ±20% tolerance for HRW's distribution variance.
	tolerance := int(math.Abs(float64(expected)) * 0.20)
	lo := expected - tolerance
	hi := expected + tolerance
	if changed < lo || changed > hi {
		t.Errorf("reassignment count %d outside expected [%d, %d] for 1/%d removal", changed, lo, hi, len(hosts4))
	}

	// Stronger: every cell whose "before" winner was NOT h4 must have
	// the same "after" winner. Rendezvous guarantees this: removing a
	// non-winner from the input set cannot change the winner.
	for cid, before := range beforeAssignments {
		if before != "h4" && afterAssignments[cid] != before {
			t.Errorf("cell %s had winner %s (not h4), should not migrate but became %s", cid, before, afterAssignments[cid])
		}
	}
}

func TestAssignCellToHostSingleton(t *testing.T) {
	// A single-host roster always returns that host regardless of cellID.
	for _, cid := range []string{"cell_0_0", "cell_5_5", "cell_99_0"} {
		got := AssignCellToHost(cid, []string{"only-host"})
		if got != "only-host" {
			t.Errorf("%s: got %q, want \"only-host\"", cid, got)
		}
	}
}

func TestAssignCellsAcrossHostsWithLocalityNilCallbacks(t *testing.T) {
	// Passing nil neighborsOf / cellOwner must degrade gracefully to the
	// plain rendezvous assignment. This guarantees callers can feature-
	// flag locality off by nil-ing out one callback without re-plumbing.
	cells := []string{"cell_0_0", "cell_1_0", "cell_0_1", "cell_1_1"}
	hosts := []string{"h1", "h2", "h3"}
	want := AssignCellsAcrossHosts(cells, hosts)
	got := AssignCellsAcrossHostsWithLocality(cells, hosts, nil, nil)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("nil callbacks should equal plain assignment:\n  got=%v\n  want=%v", got, want)
	}
}

func TestAssignCellsAcrossHostsWithLocalityBonus(t *testing.T) {
	// Synthetic scenario: pick a (cell, hosts) triple where the plain
	// rendezvous winner is NOT the host that owns a neighbor, then attach
	// the neighbor to the loser and assert the locality bonus flips the
	// winner. This proves the bonus actually moves the needle without
	// having to hand-tune the fnv64a outputs.
	hosts := []string{"h1", "h2", "h3"}
	cellID := "cell_3_3"

	// Baseline plain winner:
	plainWinner := AssignCellToHost(cellID, hosts)

	// Find a candidate loser — any host that isn't the plain winner.
	var loser string
	for _, h := range hosts {
		if h != plainWinner {
			loser = h
			break
		}
	}

	// Synthetic neighborsOf: cellID has a single neighbor ("cell_4_3"),
	// and cellOwner reports the loser as that neighbor's owner. We don't
	// care whether 15% is enough to flip THIS specific triple — instead
	// we crank up the "neighbor count" by pointing the cellOwner at the
	// same loser for multiple neighbor IDs and verify that:
	//   (a) multiple neighbors owned by the same host do NOT stack
	//       (a single bonus is applied), and
	//   (b) a single neighbor is enough to flip the outcome for at least
	//       some cell IDs.
	//
	// Strategy (b): iterate through a bunch of cell IDs until we find
	// one where "baseline winner != loser" and "with neighbor-bonus the
	// loser wins". Since 15% is a ~0.6-bit shift, roughly 1 in 5 cells
	// should see a flip if the bonus is applied at all. Try 100 and
	// fail if none flip.
	flips := 0
	checkedLoser := loser
	for i := 0; i < 100; i++ {
		cid := fmt.Sprintf("cell_%d_%d", i, i+1)
		base := AssignCellToHost(cid, hosts)
		if base == checkedLoser {
			continue
		}
		// Pretend checkedLoser owns cid's neighbor.
		neighborsOf := func(string) []string { return []string{"neighbor_" + cid} }
		cellOwner := func(n string) string {
			if n == "neighbor_"+cid {
				return checkedLoser
			}
			return ""
		}
		out := assignCellToHostWithLocality(cid, hosts, neighborsOf, cellOwner)
		if out == checkedLoser && out != base {
			flips++
		}
	}
	if flips == 0 {
		t.Errorf("locality bonus never flipped any cell assignment — bonus not applied (cellID=%s loser=%s)", cellID, loser)
	}

	// (a) Multiple neighbors owned by the same host must NOT stack.
	// The score bump is "is this host a neighbor-owner? yes/no", not
	// "how many neighbors does this host own". Verify this by running a
	// cell where one host owns all 8 neighbors vs. the same host owning
	// just one — both must pick the same winner.
	oneNeighborOwnedBy := func(host string) func(string) string {
		return func(n string) string {
			if n == "neighbor_0" {
				return host
			}
			return ""
		}
	}
	allNeighborsOwnedBy := func(host string) func(string) string {
		return func(string) string { return host }
	}
	neighborsOf := func(string) []string {
		return []string{"neighbor_0", "neighbor_1", "neighbor_2", "neighbor_3", "neighbor_4", "neighbor_5", "neighbor_6", "neighbor_7"}
	}
	for _, cid := range []string{"cell_0_0", "cell_5_5", "cell_7_2", "cell_1_9"} {
		one := assignCellToHostWithLocality(cid, hosts, neighborsOf, oneNeighborOwnedBy("h1"))
		many := assignCellToHostWithLocality(cid, hosts, neighborsOf, allNeighborsOwnedBy("h1"))
		if one != many {
			t.Errorf("cell %s: bonus stacks (1 nbr -> %s, 8 nbrs -> %s)", cid, one, many)
		}
	}
}

func TestAssignCellsAcrossHostsWithLocalityDeterministic(t *testing.T) {
	// Repeated calls with the same inputs return identical maps — the
	// locality-weighted variant must preserve determinism.
	cells := []string{"cell_0_0", "cell_1_0", "cell_2_0", "cell_0_1", "cell_1_1", "cell_2_1"}
	hosts := []string{"h-a", "h-b", "h-c"}
	ownership := map[string]string{"cell_0_0": "h-a", "cell_1_0": "h-a"}
	neighborsOf := func(cellID string) []string {
		cid, err := ParseCellID(cellID)
		if err != nil {
			return nil
		}
		out := make([]string, 0, 8)
		for _, n := range cid.Neighbors() {
			out = append(out, string(n.MeshID()))
		}
		return out
	}
	cellOwner := func(cellID string) string { return ownership[cellID] }
	a := AssignCellsAcrossHostsWithLocality(cells, hosts, neighborsOf, cellOwner)
	b := AssignCellsAcrossHostsWithLocality(cells, hosts, neighborsOf, cellOwner)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("non-deterministic:\n  a=%v\n  b=%v", a, b)
	}
}
