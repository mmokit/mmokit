package universe

import (
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/zenion/mmokit/pkg/logger"
)

// RemoteHostState tracks the lifecycle of a registered remote node.
type RemoteHostState int

const (
	RemoteHostUnknown RemoteHostState = iota
	// RemoteHostRegistered — RegisterHost received; settle window may still
	// be running, no cells assigned yet.
	RemoteHostRegistered
	// RemoteHostLive — at least one Heartbeat seen or at least one cell
	// reported ready. Eligible for cell assignment.
	RemoteHostLive
	// RemoteHostDead — failed to heartbeat within the dead threshold.
	// Cells are being reassigned. Entry stays in the map for console
	// observability until replaced.
	RemoteHostDead
	// RemoteHostLeaving — graceful shutdown in progress. The node sent
	// CellStopped for every owned cell and the stream is closing.
	RemoteHostLeaving
)

// String returns a short human-readable name for logs + admin output.
func (s RemoteHostState) String() string {
	switch s {
	case RemoteHostRegistered:
		return "Registered"
	case RemoteHostLive:
		return "Live"
	case RemoteHostDead:
		return "Dead"
	case RemoteHostLeaving:
		return "Leaving"
	default:
		return "Unknown"
	}
}

// RemoteHost holds coordinator-side state for one registered node.
type RemoteHost struct {
	ID            string
	GrpcAddr      string // MeshData peer address (for PeerList distribution)
	RegisteredAt  time.Time
	LastHeartbeat time.Time
	State         RemoteHostState
	// OwnedCells is the set of mesh-form cell IDs currently assigned to this
	// host. Populated by the registry as CellAssign / CellRelease messages
	// arrive over MeshControl.
	OwnedCells map[MeshCellID]bool

	// Local marks an in-process host that does not heartbeat (it lives and
	// dies with the coordinator process). Local hosts DO participate in
	// rendezvous rebalance on equal footing with remote nodes. Set by
	// `all` preset mode when auto-registering its local Hosts via
	// RegisterLocal.
	Local bool

	// HasPlayerDB indicates that this host's process loaded a PlayerRepository
	// at startup. Commands targeting offline players are dispatched to the
	// lex-first live DB-bearing host via Coordinator.PickDBHost.
	HasPlayerDB bool

	// ServiceOnly is true if this host registered with --mode=service
	// alone (no RoleHost, no RoleGateway). Service-only hosts are
	// dialable for ServiceEvent peer-mesh but MUST NOT receive cell
	// assignments — they have no executor, systemDefs, or VCM. The
	// assignment engine filters these out via cellBearingHosts().
	ServiceOnly bool
}

// HostRegistry is the coordinator's view of all registered remote nodes.
// Guarded by its own RWMutex so registration and liveness updates don't
// contend with the coordinator's main mu. All public methods take the
// lock internally; callers do not need to hold it.
type HostRegistry struct {
	mu    sync.RWMutex
	hosts map[string]*RemoteHost
	log   *logger.Logger

	// onOwnershipChanged fires on AssignCell / ReleaseCell with the
	// affected cellID. Fires outside r.mu so the callback is free to
	// acquire other locks without lock-ordering hazards.
	onOwnershipChanged func(cellID MeshCellID)
}

// NewHostRegistry constructs an empty registry with an attached logger.
func NewHostRegistry(log *logger.Logger) *HostRegistry {
	return &HostRegistry{
		hosts: make(map[string]*RemoteHost),
		log:   log,
	}
}

// SetOwnershipChangedCallback registers a function invoked after every
// AssignCell / ReleaseCell. The callback runs without r.mu held.
func (r *HostRegistry) SetOwnershipChangedCallback(fn func(cellID MeshCellID)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onOwnershipChanged = fn
}

// Register inserts or replaces a remote host entry with the given ID
// and grpc address. Returns the entry. If a prior entry existed (e.g.
// a reconnection after crash), OwnedCells is reset — the node is
// expected to re-announce via CellReady messages during reassignment.
func (r *HostRegistry) Register(hostID, grpcAddr string, hasPlayerDB bool, serviceOnly bool) *RemoteHost {
	r.mu.Lock()
	defer r.mu.Unlock()
	host := &RemoteHost{
		ID:            hostID,
		GrpcAddr:      grpcAddr,
		RegisteredAt:  time.Now(),
		LastHeartbeat: time.Now(),
		State:         RemoteHostRegistered,
		OwnedCells:    make(map[MeshCellID]bool),
		HasPlayerDB:   hasPlayerDB,
		ServiceOnly:   serviceOnly,
	}
	r.hosts[hostID] = host
	return host
}

// Touch updates LastHeartbeat and transitions state from Registered to
// Live on the first heartbeat. No-op if the host is unknown.
func (r *HostRegistry) Touch(hostID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	host, ok := r.hosts[hostID]
	if !ok {
		return
	}
	host.LastHeartbeat = time.Now()
	if host.State == RemoteHostRegistered {
		host.State = RemoteHostLive
	}
}

// MarkDead transitions a host to the Dead state. Does NOT delete the
// entry — reassignment logic needs the OwnedCells set to run rendezvous
// over surviving hosts. Call Remove once reassignment is complete.
func (r *HostRegistry) MarkDead(hostID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if host, ok := r.hosts[hostID]; ok {
		host.State = RemoteHostDead
	}
}

// MarkLeaving transitions a host to the Leaving state, used during a
// graceful shutdown handshake.
func (r *HostRegistry) MarkLeaving(hostID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if host, ok := r.hosts[hostID]; ok {
		host.State = RemoteHostLeaving
	}
}

// Remove deletes the host entry. Idempotent.
func (r *HostRegistry) Remove(hostID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.hosts, hostID)
}

// Get returns a COPY of the host entry for the given ID, or nil if
// unknown. The copy is safe to read without holding the registry lock.
// OwnedCells is deep-copied into a fresh map.
func (r *HostRegistry) Get(hostID string) *RemoteHost {
	r.mu.RLock()
	defer r.mu.RUnlock()
	host, ok := r.hosts[hostID]
	if !ok {
		return nil
	}
	return r.cloneHost(host)
}

// LiveHosts returns snapshot copies of every host in the registry
// (regardless of state). Callers filter by state as needed. Returned
// slice is safe to iterate without holding the registry lock.
//
// Name kept as "LiveHosts" for ergonomic call-sites even though we
// return ALL states — callers almost always need the full roster for
// rendezvous calculations and then filter. If a state-filtered helper
// is needed later, add LiveOnly() returning only RemoteHostLive.
func (r *HostRegistry) LiveHosts() []*RemoteHost {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*RemoteHost, 0, len(r.hosts))
	for _, host := range r.hosts {
		out = append(out, r.cloneHost(host))
	}
	return out
}

// cellBearingHosts returns LiveHosts() filtered to entries where
// ServiceOnly == false. These are the only hosts eligible for cell
// assignment — service-only processes (--mode=service alone) lack the
// executor, systemDefs, and VirtualConnManager needed to run a cell.
//
// Use this anywhere the assignment engine or cell-transfer logic is
// picking destinations. PeerList broadcasts, ServiceEvent dispatch,
// and the host list console command should keep using LiveHosts() so
// service-only hosts remain visible for diagnostics + dispatchable for
// service traffic.
func (r *HostRegistry) cellBearingHosts() []*RemoteHost {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*RemoteHost, 0, len(r.hosts))
	for _, host := range r.hosts {
		if host.ServiceOnly {
			continue
		}
		out = append(out, r.cloneHost(host))
	}
	return out
}

// AssignCell records that the given cell is now assigned to the host.
// Returns an error if the host is unknown.
func (r *HostRegistry) AssignCell(hostID string, cellID MeshCellID) error {
	r.mu.Lock()
	host, ok := r.hosts[hostID]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("assign cell %q: unknown host %q", cellID, hostID)
	}
	host.OwnedCells[cellID] = true
	cb := r.onOwnershipChanged
	r.mu.Unlock()
	if cb != nil {
		cb(cellID)
	}
	return nil
}

// ReleaseCell clears the ownership entry. No-op if absent.
func (r *HostRegistry) ReleaseCell(hostID string, cellID MeshCellID) {
	r.mu.Lock()
	if host, ok := r.hosts[hostID]; ok {
		delete(host.OwnedCells, cellID)
	}
	cb := r.onOwnershipChanged
	r.mu.Unlock()
	if cb != nil {
		cb(cellID)
	}
}

// HostForCell returns the hostID currently owning the given cell, or
// "" if the cell has no owner in the registry. O(N) linear scan — fine
// for S4 cluster sizes.
func (r *HostRegistry) HostForCell(cellID MeshCellID) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for hostID, host := range r.hosts {
		if host.OwnedCells[cellID] {
			return hostID
		}
	}
	return ""
}

// RegisterLocal inserts an in-process host entry that is immediately Live
// and never heartbeats (local hosts live and die with the coordinator
// process). Local hosts DO participate in rendezvous rebalance — the
// ring is the single source of truth for cell ownership regardless of
// whether a host is in-process or remote. Used by `all` preset mode to
// populate the HostRegistry with its local Hosts so that "host list",
// PeerList broadcasts, and rebalance all see them alongside any remote
// nodes that join via MeshControl.
func (r *HostRegistry) RegisterLocal(hostID, grpcAddr string, ownedCells []MeshCellID, hasPlayerDB bool) *RemoteHost {
	r.mu.Lock()
	defer r.mu.Unlock()
	cells := make(map[MeshCellID]bool, len(ownedCells))
	for _, c := range ownedCells {
		cells[c] = true
	}
	host := &RemoteHost{
		ID:            hostID,
		GrpcAddr:      grpcAddr,
		RegisteredAt:  time.Now(),
		LastHeartbeat: time.Now(),
		State:         RemoteHostLive,
		OwnedCells:    cells,
		Local:         true,
		HasPlayerDB:   hasPlayerDB,
	}
	r.hosts[hostID] = host
	return host
}

// SetHasPlayerDB updates the HasPlayerDB flag on an existing host entry.
// No-op if the host is not registered. Safe to call from any goroutine.
func (r *HostRegistry) SetHasPlayerDB(hostID string, b bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if h, ok := r.hosts[hostID]; ok {
		h.HasPlayerDB = b
	}
}

// cloneHost returns a deep copy of a RemoteHost (including OwnedCells)
// suitable for returning from methods without leaking a pointer into
// the map. Caller must hold r.mu.
func (r *HostRegistry) cloneHost(h *RemoteHost) *RemoteHost {
	cells := make(map[MeshCellID]bool, len(h.OwnedCells))
	maps.Copy(cells, h.OwnedCells)
	return &RemoteHost{
		ID:            h.ID,
		GrpcAddr:      h.GrpcAddr,
		RegisteredAt:  h.RegisteredAt,
		LastHeartbeat: h.LastHeartbeat,
		State:         h.State,
		OwnedCells:    cells,
		Local:         h.Local,
		HasPlayerDB:   h.HasPlayerDB,
		ServiceOnly:   h.ServiceOnly,
	}
}
