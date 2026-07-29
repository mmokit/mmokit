package universe

import (
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Registration admission. Without it, registerHostStream / registerGatewayStream
// resolve a same-ID collision last-writer-wins: the newcomer takes the map entry
// and the incumbent's kill channel is closed. Two processes sharing a --host-id
// therefore flap forever, each evicting the other every backoff interval, and a
// peer that merely knows a victim's ID can take its place.
//
// The fix is NOT "reject if an entry exists". Three verified scenarios break
// under that rule and each is closed by a distinct term below. See
// docs/superpowers/specs/2026-07-28-mesh-authentication-design.md §4.2.
//
// What admission deliberately does NOT do is freeze the registry entry. An
// admitted registration still performs the full field refresh — GrpcAddr,
// State, OwnedCells/Sessions, LastHeartbeat — because crash-reconnect depends
// on all four. In particular every production NewHostNetwork binds ":0", so the
// advertised address changes on every restart; freezing GrpcAddr would leave a
// restarted host permanently unreachable on the payload plane while its control
// stream looked healthy.

// killClosed reports whether someone has already ordered cs down — the
// host.kill console verb via cancelStream, an eviction in flight, or the
// liveness watcher having declared the peer dead.
//
// This term is load-bearing. cancelStream closes the kill channel but does NOT
// delete the map entry; the delete happens later in the handler's own defer. A
// host killed by an operator redials after ~200ms, well inside deadThreshold,
// so a predicate looking only at map presence and heartbeat freshness would
// reject it and turn a documented ~3s reassignment into a lockout.
func killClosed(cs *controlStream) bool {
	select {
	case <-cs.kill:
		return true
	default:
		return false
	}
}

// admitHostRegistration returns nil to admit hostID, or a status error to
// reject. Call BEFORE registerHostStream, which unconditionally swaps the map
// entry and closes the predecessor's kill channel.
func (s *meshControlServer) admitHostRegistration(hostID string) error {
	var h *RemoteHost
	if s.registry != nil {
		h = s.registry.Get(hostID)
	}

	// (a) A Local entry can never be replaced by a remote stream, and this
	//     clause is unconditional on purpose. checkLiveness skips Local hosts
	//     BEFORE the staleness test, and Touch is unreachable without a
	//     control stream, so a Local entry's LastHeartbeat is frozen at
	//     RegisterLocal time and reads as stale forever. A staleness-only
	//     rule would therefore HAND the well-known IDs "local" and "inproc"
	//     to any caller after a few seconds of process uptime.
	if h != nil && h.Local {
		return status.Errorf(codes.AlreadyExists,
			"host id %q belongs to the coordinator's in-process host", hostID)
	}

	s.mu.RLock()
	cs, gwCS := s.streams[hostID], s.gatewayStreams[hostID]
	s.mu.RUnlock()

	// (b) Cross-map collision. sendCoordMessageToHost falls back to
	//     gatewayStreams when a host ID has no host stream, on the documented
	//     but unenforced assumption that host and gateway IDs never collide.
	//     A guard consulting only its own map is bypassable by registering a
	//     host under a live gateway's ID.
	if gwCS != nil && !killClosed(gwCS) {
		return status.Errorf(codes.AlreadyExists,
			"id %q is held by a live gateway control stream", hostID)
	}

	// (c) Still being defended: a stream record exists, nobody has ordered it
	//     down, and the registry says it is heartbeating.
	//
	//     State != Dead is sufficient but NOT necessary, which is why it is
	//     combined with staleness rather than used alone: checkLiveness
	//     requires State == Live before it even tests staleness, so a host
	//     that registers and never heartbeats stays Registered forever and is
	//     never marked Dead. Only the staleness term admits that one.
	if cs != nil && !killClosed(cs) &&
		h != nil && h.State != RemoteHostDead &&
		time.Since(h.LastHeartbeat) <= deadThreshold {
		return status.Errorf(codes.AlreadyExists,
			"host %q is already registered and heartbeated %s ago",
			hostID, time.Since(h.LastHeartbeat).Round(time.Millisecond))
	}

	return nil
}

// admitGatewayRegistration is admitHostRegistration for the gateway side.
// It uses gatewayDeadThreshold so the guard and checkGatewayLiveness agree on
// what "still alive" means, and cross-checks the host map for the same reason
// clause (b) exists above.
func (s *meshControlServer) admitGatewayRegistration(gatewayID string) error {
	var g *RemoteGateway
	if s.gatewayRegistry != nil {
		g = s.gatewayRegistry.Get(gatewayID)
	}

	if g != nil && g.Local {
		return status.Errorf(codes.AlreadyExists,
			"gateway id %q belongs to the coordinator's in-process gateway", gatewayID)
	}

	s.mu.RLock()
	gwCS, cs := s.gatewayStreams[gatewayID], s.streams[gatewayID]
	s.mu.RUnlock()

	if cs != nil && !killClosed(cs) {
		return status.Errorf(codes.AlreadyExists,
			"id %q is held by a live host control stream", gatewayID)
	}

	if gwCS != nil && !killClosed(gwCS) &&
		g != nil && time.Since(g.LastHeartbeat) <= gatewayDeadThreshold {
		return status.Errorf(codes.AlreadyExists,
			"gateway %q is already registered and heartbeated %s ago",
			gatewayID, time.Since(g.LastHeartbeat).Round(time.Millisecond))
	}

	return nil
}

// cancelGatewayStream is cancelStream for the gateway side.
//
// Without it there is no way to unstick a wedged gateway ID, and admission
// makes that fatal rather than untidy: a locked-out gateway sends resolveSpawn
// down its 2s-deadline fallback, so every reconnecting player becomes a fresh
// spawn at the centre of cell (0,0) instead of resuming its live entity.
func (s *meshControlServer) cancelGatewayStream(gatewayID string) bool {
	s.mu.Lock()
	cs, ok := s.gatewayStreams[gatewayID]
	s.mu.Unlock()
	if !ok {
		return false
	}
	cs.closeKill()
	return true
}
