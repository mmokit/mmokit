package universe

import (
	"hash/fnv"
)

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
