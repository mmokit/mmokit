package universe

import "sort"

// PickDBHost returns the lexicographically first live host whose process
// advertised a PlayerRepository at RegisterHost time. Returns "" if no host
// carries the DB. Used by RoutePlayerHomeOrOwner to dispatch offline player
// commands deterministically.
func (c *Process) PickDBHost() string {
	var candidates []string
	for _, h := range c.hostRegistry.LiveHosts() {
		if h.HasPlayerDB && (h.State == RemoteHostLive || h.State == RemoteHostRegistered) {
			candidates = append(candidates, h.ID)
		}
	}
	sort.Strings(candidates)
	if len(candidates) == 0 {
		return ""
	}
	return candidates[0]
}

// SetHasPlayerDB updates this process's local host record to advertise whether
// a PlayerRepository is loaded, and rebroadcasts the PeerList so remote hosts
// see the change. Called by the bootstrap right after playerDB construction.
// Safe to call before any local hosts are registered (no-op until then).
func (c *Process) SetHasPlayerDB(b bool) {
	for id := range c.Hosts {
		c.hostRegistry.SetHasPlayerDB(id, b)
	}
	c.broadcastPeerListIfReady()
}

// registerLiveHost is a test-only helper that injects a host record
// directly into the registry without going through RegisterHost. Used by
// PickDBHost / route-resolver unit tests to seed cluster state.
func (c *Process) registerLiveHost(id string, hasDB bool) {
	c.hostRegistry.RegisterLocal(id, "", nil, hasDB)
}
