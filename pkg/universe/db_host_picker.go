package universe

import "sort"

// PickDBHost returns the lexicographically first live host whose process
// advertised a PlayerRepository at RegisterHost time. Returns "" if no host
// carries the DB. Used by RoutePlayerHomeOrOwner to dispatch offline player
// commands deterministically.
func (c *Process) PickDBHost() string {
	if c.hostRegistry == nil {
		return ""
	}
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

// SetHasPlayerDB updates this process's local host record(s) to advertise
// whether a PlayerRepository is loaded, and rebroadcasts the PeerList so
// remote hosts see the change. Called by the bootstrap right after the
// playerDB is constructed — typically BEFORE Build(), when no local hosts
// are registered yet. The flag is stashed on the Process so Build() can
// pick it up when calling RegisterLocal.
func (c *Process) SetHasPlayerDB(b bool) {
	c.hasPlayerDB.Store(b)
	if c.hostRegistry != nil {
		for id := range c.Hosts {
			c.hostRegistry.SetHasPlayerDB(id, b)
		}
	}
	c.broadcastPeerListIfReady()
}
