// Package universe - session_routes.go
//
// sessionRoutes is a composite-keyed map of SessionRoute values, guarded by
// its own RWMutex so gateway-proxy hot-path reads don't contend with the
// broader Process.mu that protects topology, cell maps, and player state.
//
// SessionKey combines a GatewayID with a ConnID because connIDs are only
// unique within a single gateway process. When RoleGateway is set alongside
// RoleCoordinator (the default `all` preset or an explicit
// `coordinator,gateway` combo), the embedded gateway uses
// InprocGatewayID = "inproc". Standalone `--mode=gateway` processes set
// their own unique GatewayID at startup.
//
// SessionRoute.CellID carries the cell that currently owns the session — the
// same value that the old connIndex[connID] map stored. SessionRoute.HostID is
// intentionally left empty for now; cross-host tasks (T7+) populate it once
// the host-level routing layer lands.

package universe

import (
	"strconv"
	"sync"
)

// InprocGatewayID is the gateway ID used when the gateway role runs in the
// same process as the coordinator — either the default `all` preset or an
// explicit `--mode=coordinator,gateway`. Standalone `--mode=gateway`
// processes choose their own unique IDs.
const InprocGatewayID = "inproc"

// SessionKey uniquely identifies a client connection across N gateway processes.
type SessionKey struct {
	GatewayID string
	ConnID    uint32
}

// String returns "<gatewayID>:<connID>" for log lines.
func (k SessionKey) String() string {
	return k.GatewayID + ":" + strconv.FormatUint(uint64(k.ConnID), 10)
}

// SessionRoute records where a session currently lives in the mesh.
//
// CellID is the cell currently owning the session (e.g. "cell_0_0").
// HostID is the host running that cell.
// Epoch is a fencing token bumped by Migrate; callers that detect a stale
// epoch can discard the handoff.
type SessionRoute struct {
	Key      SessionKey
	Username string
	HostID   string
	CellID   MeshCellID
	Epoch    uint64
}

// sessionRoutes is the authoritative connID→cell routing table.
// It is safe for concurrent use.
type sessionRoutes struct {
	mu     sync.RWMutex
	routes map[SessionKey]*SessionRoute
}

func newSessionRoutes() *sessionRoutes {
	return &sessionRoutes{
		routes: make(map[SessionKey]*SessionRoute),
	}
}

// Set stores route by its Key. If the key already exists, it is replaced
// entirely (the caller is responsible for pre-filling Epoch as needed).
func (r *sessionRoutes) Set(route *SessionRoute) {
	r.mu.Lock()
	r.routes[route.Key] = route
	r.mu.Unlock()
}

// UpdateCell changes only the CellID field of an existing route, preserving
// HostID and Epoch. Used by same-host transfers where the gateway routing
// doesn't change — the session just moved to a different cell on the same host.
// Returns false if the key doesn't exist.
func (r *sessionRoutes) UpdateCell(key SessionKey, newCell MeshCellID) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.routes[key]
	if !ok {
		return false
	}
	v.CellID = newCell
	return true
}

// Get returns a deep copy of the route for key, or (nil, false) if absent.
// Callers may freely mutate the returned struct. The copy happens under the
// read lock so concurrent writers (Migrate, remapCell, remapHostCell) can't
// tear the struct fields mid-read.
func (r *sessionRoutes) Get(key SessionKey) (*SessionRoute, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.routes[key]
	if !ok {
		return nil, false
	}
	cp := *v
	return &cp, true
}

// ForEach calls fn with a deep-copied SessionRoute for every entry. The
// iteration holds the read lock, so fn MUST NOT call back into sessionRoutes
// methods that take the write lock (Set/Remove/Migrate) — that would deadlock.
// Returning false from fn stops iteration.
func (r *sessionRoutes) ForEach(fn func(*SessionRoute) bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, v := range r.routes {
		cp := *v
		if !fn(&cp) {
			return
		}
	}
}

// Len returns the number of active session routes.
func (r *sessionRoutes) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.routes)
}

// Remove deletes the route for key (no-op if absent).
func (r *sessionRoutes) Remove(key SessionKey) {
	r.mu.Lock()
	delete(r.routes, key)
	r.mu.Unlock()
}

// Migrate atomically increments the Epoch for key and updates HostID and
// CellID. Returns the new Epoch and true on success, or (0, false) if the
// key is unknown.
func (r *sessionRoutes) Migrate(key SessionKey, newHost string, newCell MeshCellID) (uint64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.routes[key]
	if !ok {
		return 0, false
	}
	v.Epoch++
	v.HostID = newHost
	v.CellID = newCell
	return v.Epoch, true
}

// RemoveByGateway removes all routes whose GatewayID equals gatewayID and
// returns the number of entries deleted. Used for gateway crash cleanup.
func (r *sessionRoutes) RemoveByGateway(gatewayID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for k := range r.routes {
		if k.GatewayID == gatewayID {
			delete(r.routes, k)
			n++
		}
	}
	return n
}

// RemoveByHost removes all routes whose HostID equals hostID and returns the
// number of entries deleted. Used for host crash cleanup.
func (r *sessionRoutes) RemoveByHost(hostID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for k, v := range r.routes {
		if v.HostID == hostID {
			delete(r.routes, k)
			n++
		}
	}
	return n
}

// remapCell rewrites the CellID of every route for which pred(CellID) returns
// true, setting it to newCellID, and returns the list of affected SessionKeys.
// Used by the partition merge path and the migrate commit path so the caller
// can dispatch UpstreamSwitch notifications to the owning gateways.
func (r *sessionRoutes) remapCell(pred func(cellID MeshCellID) bool, newCellID MeshCellID) []SessionKey {
	r.mu.Lock()
	defer r.mu.Unlock()
	var affected []SessionKey
	for k, v := range r.routes {
		if pred(v.CellID) {
			v.CellID = newCellID
			affected = append(affected, k)
		}
	}
	return affected
}

// remapCellPerRoute rewrites CellID per-route, letting the caller decide
// the destination cell based on the full route (e.g. by Username). decide
// returns (newCellID, true) to remap the route, or ("", false) to leave it
// alone. Used by applySplitCommit to route each player's session to the
// child that actually adopted their entity.
func (r *sessionRoutes) remapCellPerRoute(decide func(route *SessionRoute) (MeshCellID, bool)) []SessionKey {
	r.mu.Lock()
	defer r.mu.Unlock()
	var affected []SessionKey
	for k, v := range r.routes {
		if newCell, ok := decide(v); ok {
			v.CellID = newCell
			affected = append(affected, k)
		}
	}
	return affected
}

// remapHostCell rewrites both the HostID and CellID for every route for which
// pred(CellID) returns true, bumping Epoch as well. Returns the affected keys
// with their new epochs so the caller can target UpstreamSwitch dispatches.
// Used by the migrate commit path where every route on the source cell moves
// to the same new host.
func (r *sessionRoutes) remapHostCell(pred func(cellID MeshCellID) bool, newHost string, newCell MeshCellID) []remapResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []remapResult
	for k, v := range r.routes {
		if pred(v.CellID) {
			v.Epoch++
			v.HostID = newHost
			v.CellID = newCell
			out = append(out, remapResult{Key: k, Epoch: v.Epoch})
		}
	}
	return out
}

// remapResult pairs a rewritten session key with its post-bump epoch so the
// caller can issue a targeted UpstreamSwitch without re-reading the route.
type remapResult struct {
	Key   SessionKey
	Epoch uint64
}
