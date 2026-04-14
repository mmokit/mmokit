package universe

// gateway_registry.go — GatewayRegistry tracks registered gateway processes.
//
// Parallel to HostRegistry. Gateways connect to the coordinator via the same
// MeshControl bidi stream as nodes, but their first message is RegisterGateway
// (not RegisterHost). The coordinator tracks them in a separate registry with a
// 5s dead-threshold (vs 3s for nodes) — gateway death is more user-visible
// (live player connections drop) so we grant more grace time for restarts before
// cleaning up sessions.
//
// In T4, Sessions is always empty — session tracking lands in T5 once
// SessionAnnounce messages are acted upon. The map is present so T5 can
// populate it without a struct change.

import (
	"maps"
	"sync"
	"time"

	"github.com/zenion/mmoserver/pkg/logger"
)

// RemoteGatewayState tracks the lifecycle of a registered remote gateway.
type RemoteGatewayState uint8

const (
	RemoteGatewayUnknown RemoteGatewayState = iota
	// RemoteGatewayRegistered — RegisterGateway received; no heartbeat yet.
	RemoteGatewayRegistered
	// RemoteGatewayLive — at least one Heartbeat seen. Eligible for session routing.
	RemoteGatewayLive
	// RemoteGatewayDead — failed to heartbeat within the gateway dead threshold.
	// Entry stays in the map for console observability until explicitly removed.
	RemoteGatewayDead
	// RemoteGatewayLeaving — graceful shutdown in progress (all sessions closed).
	RemoteGatewayLeaving
)

// String returns a short human-readable name for logs and admin output.
func (s RemoteGatewayState) String() string {
	switch s {
	case RemoteGatewayRegistered:
		return "Registered"
	case RemoteGatewayLive:
		return "Live"
	case RemoteGatewayDead:
		return "Dead"
	case RemoteGatewayLeaving:
		return "Leaving"
	default:
		return "Unknown"
	}
}

// RemoteGateway holds coordinator-side state for one registered gateway process.
type RemoteGateway struct {
	ID            string
	WSAddr        string              // where clients connect (WebSocket)
	GRPCAddr      string              // for MeshData streams from nodes to this gateway
	RegisteredAt  time.Time
	LastHeartbeat time.Time
	State         RemoteGatewayState
	// Local is true for the embedded in-process gateway (coord+gateway
	// mode). Local gateways don't heartbeat — they're always live by
	// virtue of sharing the coordinator process — and the liveness
	// watcher skips them.
	Local    bool
	Sessions map[SessionKey]bool // SessionKeys terminated by this gateway (populated T5+)
}

// GatewayRegistry is the coordinator's view of all registered gateway processes.
// Guarded by its own RWMutex so registration and liveness updates don't contend
// with the coordinator's main mu. All public methods take the lock internally;
// callers do not need to hold it.
type GatewayRegistry struct {
	mu       sync.RWMutex
	gateways map[string]*RemoteGateway
	log      *logger.Logger
}

// NewGatewayRegistry constructs an empty registry with an attached logger.
func NewGatewayRegistry(log *logger.Logger) *GatewayRegistry {
	return &GatewayRegistry{
		gateways: make(map[string]*RemoteGateway),
		log:      log,
	}
}

// Register inserts or replaces a gateway entry with the given ID, WebSocket
// address, and gRPC address. Returns the entry. If a prior entry existed
// (e.g. reconnection after crash), Sessions is reset — the gateway is expected
// to re-announce sessions via SessionAnnounce messages.
func (r *GatewayRegistry) Register(gatewayID, wsAddr, grpcAddr string) *RemoteGateway {
	r.mu.Lock()
	defer r.mu.Unlock()
	gw := &RemoteGateway{
		ID:            gatewayID,
		WSAddr:        wsAddr,
		GRPCAddr:      grpcAddr,
		RegisteredAt:  time.Now(),
		LastHeartbeat: time.Now(),
		State:         RemoteGatewayRegistered,
		Sessions:      make(map[SessionKey]bool),
	}
	r.gateways[gatewayID] = gw
	return gw
}

// RegisterLocal registers an embedded in-process gateway. Marks the entry
// Local (liveness watcher skips it) and Live (immediately eligible for
// session routing + PeerList inclusion). The grpcAddr is the gateway's own
// HostNetwork listener — remote nodes dial it to stream ClientFrames back.
func (r *GatewayRegistry) RegisterLocal(gatewayID, grpcAddr string) *RemoteGateway {
	r.mu.Lock()
	defer r.mu.Unlock()
	gw := &RemoteGateway{
		ID:            gatewayID,
		WSAddr:        "", // embedded: no separate WS listener address
		GRPCAddr:      grpcAddr,
		RegisteredAt:  time.Now(),
		LastHeartbeat: time.Now(),
		State:         RemoteGatewayLive,
		Local:         true,
		Sessions:      make(map[SessionKey]bool),
	}
	r.gateways[gatewayID] = gw
	return gw
}

// Touch updates LastHeartbeat and transitions state from Registered to Live
// on the first heartbeat. No-op if the gateway is unknown.
func (r *GatewayRegistry) Touch(gatewayID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	gw, ok := r.gateways[gatewayID]
	if !ok {
		return
	}
	gw.LastHeartbeat = time.Now()
	if gw.State == RemoteGatewayRegistered {
		gw.State = RemoteGatewayLive
	}
}

// MarkDead transitions a gateway to the Dead state. Does NOT delete the
// entry — it stays for console observability. Call Remove once no longer needed.
func (r *GatewayRegistry) MarkDead(gatewayID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if gw, ok := r.gateways[gatewayID]; ok {
		gw.State = RemoteGatewayDead
	}
}

// MarkLeaving transitions a gateway to the Leaving state, used during graceful shutdown.
func (r *GatewayRegistry) MarkLeaving(gatewayID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if gw, ok := r.gateways[gatewayID]; ok {
		gw.State = RemoteGatewayLeaving
	}
}

// AddSession records a SessionKey as belonging to the given gateway. Used by
// the coordinator when processing a SessionAnnounce message so that the crash
// cleanup path (RemoveByGateway) can find all live sessions.
func (r *GatewayRegistry) AddSession(gatewayID string, key SessionKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	gw, ok := r.gateways[gatewayID]
	if !ok {
		return
	}
	if gw.Sessions == nil {
		gw.Sessions = make(map[SessionKey]bool)
	}
	gw.Sessions[key] = true
}

// RemoveSession removes a SessionKey from the given gateway's session set.
// Used when a session disconnects cleanly so the crash cleanup path doesn't
// treat the stream close as a crash.
func (r *GatewayRegistry) RemoveSession(gatewayID string, key SessionKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	gw, ok := r.gateways[gatewayID]
	if !ok {
		return
	}
	delete(gw.Sessions, key)
}

// Remove deletes the gateway entry. Idempotent.
func (r *GatewayRegistry) Remove(gatewayID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.gateways, gatewayID)
}

// Get returns a COPY of the gateway entry for the given ID, or nil if unknown.
// The copy is safe to read without holding the registry lock.
// Sessions is deep-copied into a fresh map.
func (r *GatewayRegistry) Get(gatewayID string) *RemoteGateway {
	r.mu.RLock()
	defer r.mu.RUnlock()
	gw, ok := r.gateways[gatewayID]
	if !ok {
		return nil
	}
	return r.cloneGateway(gw)
}

// LiveGateways returns snapshot copies of every gateway in the registry
// (regardless of state). Callers filter by state as needed. Returned slice
// is safe to iterate without holding the registry lock.
//
// Name kept as "LiveGateways" for ergonomic call-sites even though we return
// ALL states (mirroring HostRegistry.LiveHosts).
func (r *GatewayRegistry) LiveGateways() []*RemoteGateway {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*RemoteGateway, 0, len(r.gateways))
	for _, gw := range r.gateways {
		out = append(out, r.cloneGateway(gw))
	}
	return out
}

// cloneGateway returns a deep copy of a RemoteGateway (including Sessions map)
// suitable for returning from methods without leaking a pointer into the map.
// Sessions is only allocated when the source has entries — callers that receive
// a nil Sessions map can still safely range over it or call len on it.
// Caller must hold r.mu.
func (r *GatewayRegistry) cloneGateway(gw *RemoteGateway) *RemoteGateway {
	var sessions map[SessionKey]bool
	if len(gw.Sessions) > 0 {
		sessions = make(map[SessionKey]bool, len(gw.Sessions))
		maps.Copy(sessions, gw.Sessions)
	}
	return &RemoteGateway{
		ID:            gw.ID,
		WSAddr:        gw.WSAddr,
		GRPCAddr:      gw.GRPCAddr,
		RegisteredAt:  gw.RegisteredAt,
		LastHeartbeat: gw.LastHeartbeat,
		State:         gw.State,
		Local:         gw.Local,
		Sessions:      sessions,
	}
}
