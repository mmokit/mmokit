package universe

import (
	"hash/fnv"
)

// localityBonus is the rendezvous-score multiplier applied to a candidate
// host when it already owns at least one neighbor of the cell being
// assigned. A 15% bump is strong enough to keep adjacent cells co-located
// when loads are roughly equal, but not so strong that it overrides a
// genuinely better-scoring host under skew. See
// AssignCellsAcrossHostsWithLocality.
const localityBonus = 0.15

// AssignCellToHost picks the highest-scoring host for a given cell via
// rendezvous (HRW — Highest Random Weight) hashing. For each host, the
// score is fnv64a(hostID || 0x00 || cellID); the host with the greatest
// score wins. Returns "" if hosts is empty.
//
// Stable under restart: given the same (cellID, hosts) input, the same
// host always wins regardless of the order in which hosts appear in
// the slice. Tolerant to duplicates — repeated entries score the same
// and are effectively idempotent.
//
// Rendezvous hashing minimizes reassignment when the host set changes:
// adding or removing one host only affects cells whose previous winner
// was the changed host. No ring maintenance is needed, unlike consistent
// hashing. For cluster sizes under ~100 hosts this is simpler and
// produces more even load distribution than consistent hashing with
// virtual nodes.
func AssignCellToHost(cellID string, hosts []string) string {
	if len(hosts) == 0 {
		return ""
	}
	var bestHost string
	var bestScore uint64
	for _, h := range hosts {
		score := hrwScore(cellID, h)
		if bestHost == "" || score > bestScore {
			bestHost = h
			bestScore = score
		}
	}
	return bestHost
}

// AssignCellsAcrossHosts runs AssignCellToHost for every cell ID in
// the input and returns a (cellID -> hostID) map. Used by the
// assignment engine after the settle window closes or when the host
// roster changes. Output map is deterministic for a given input.
func AssignCellsAcrossHosts(cellIDs, hostIDs []string) map[string]string {
	out := make(map[string]string, len(cellIDs))
	for _, cid := range cellIDs {
		out[cid] = AssignCellToHost(cid, hostIDs)
	}
	return out
}

// AssignCellsAcrossHostsWithLocality is a locality-biased variant of
// AssignCellsAcrossHosts. For each cell, the base rendezvous score for a
// host is multiplied by (1 + localityBonus) whenever that host already
// owns at least one Moore-neighborhood (8-connected) neighbor of the
// cell being assigned, as reported by the cellOwner callback.
//
// neighborsOf returns the neighbor cell ID strings for a given cell ID
// string. Left as a callback so this function stays pure and testable
// without a Coordinator: the caller plugs in quadtree-aware geometry
// (universe.CellID.Neighbors + MeshCellID) while the core just applies
// the weighting rule. Return nil or an empty slice to disable locality
// for a particular cell.
//
// cellOwner returns the host that currently owns the given cell ID, or
// "" if the cell is unowned / unknown. The function is consulted once per
// (cell, neighbor) pair, so callers should back it with an O(1) map
// lookup.
//
// Multiple neighbors owned by the same host count as a single bonus (the
// bump is not stacked) — the rule is "this host owns at least one of my
// neighbors", not "this host owns many of my neighbors". This keeps the
// weighting proportional to rendezvous score regardless of neighbor
// density.
//
// When neighborsOf or cellOwner is nil, the function degrades to plain
// AssignCellsAcrossHosts.
func AssignCellsAcrossHostsWithLocality(
	cellIDs, hostIDs []string,
	neighborsOf func(cellID string) []string,
	cellOwner func(cellID string) string,
) map[string]string {
	if neighborsOf == nil || cellOwner == nil {
		return AssignCellsAcrossHosts(cellIDs, hostIDs)
	}
	out := make(map[string]string, len(cellIDs))
	for _, cid := range cellIDs {
		out[cid] = assignCellToHostWithLocality(cid, hostIDs, neighborsOf, cellOwner)
	}
	return out
}

// assignCellToHostWithLocality is the per-cell helper used by
// AssignCellsAcrossHostsWithLocality. Exposed as an unexported function
// so tests can exercise the per-cell decision in isolation if needed.
func assignCellToHostWithLocality(
	cellID string,
	hosts []string,
	neighborsOf func(cellID string) []string,
	cellOwner func(cellID string) string,
) string {
	if len(hosts) == 0 {
		return ""
	}

	// Collect the set of hosts that qualify for the locality bonus: those
	// that own at least one neighbor of cellID. Built once up front so the
	// per-host scoring loop is a plain map lookup.
	var bonusHosts map[string]struct{}
	for _, nID := range neighborsOf(cellID) {
		owner := cellOwner(nID)
		if owner == "" {
			continue
		}
		if bonusHosts == nil {
			bonusHosts = make(map[string]struct{}, 4)
		}
		bonusHosts[owner] = struct{}{}
	}

	var (
		bestHost  string
		bestScore float64
	)
	for _, h := range hosts {
		raw := hrwScore(cellID, h)
		// Convert to float64 so we can apply the multiplicative bonus
		// without losing the full 64-bit entropy. The comparison is then
		// over doubles, which gives us ~52 bits of mantissa — plenty for
		// ranking a cluster of hundreds of hosts with low collision risk.
		score := float64(raw)
		if _, ok := bonusHosts[h]; ok {
			score += score * localityBonus
		}
		if bestHost == "" || score > bestScore {
			bestHost = h
			bestScore = score
		}
	}
	return bestHost
}

// hrwScore is the per-(cell, host) weight used by AssignCellToHost.
// fnv64a is deterministic and fast. We write hostID first so that each
// host acts as a distinct prefix — this ensures the hashes diverge
// early and produce a uniform spread across hosts. The 0x00 separator
// prevents aliasing between pairs like ("ab", "cd") and ("a", "bcd").
func hrwScore(cellID, hostID string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(hostID))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(cellID))
	return h.Sum64()
}
