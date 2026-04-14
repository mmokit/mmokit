// Package universe - session_routes.go
//
// sessionRoutes is a composite-keyed map of SessionRoute values, guarded by
// its own RWMutex so gateway-proxy hot-path reads don't contend with the
// broader Coordinator.mu that protects topology, cell maps, and player state.
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
	CellID   string
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

// Get returns a deep copy of the route for key, or (nil, false) if absent.
// Callers may freely mutate the returned struct.
func (r *sessionRoutes) Get(key SessionKey) (*SessionRoute, bool) {
	r.mu.RLock()
	v, ok := r.routes[key]
	r.mu.RUnlock()
	if !ok {
		return nil, false
	}
	cp := *v
	return &cp, true
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
func (r *sessionRoutes) Migrate(key SessionKey, newHost, newCell string) (uint64, bool) {
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
// true, setting it to newCellID. Returns the number of routes updated.
// Used exclusively by the partition merge path.
func (r *sessionRoutes) remapCell(pred func(cellID string) bool, newCellID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, v := range r.routes {
		if pred(v.CellID) {
			v.CellID = newCellID
			n++
		}
	}
	return n
}
