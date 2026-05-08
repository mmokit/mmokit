package service

import (
	"reflect"
	"sync"
)

// RemotePublishCall describes one Bus event that needs to be fanned out
// to remote subscribers. Universe-side code installs a RemotePublishFunc
// via Bus.SetRemotePublish to receive these calls.
//
// Payload is the original event value. The callback owns marshalling
// + dispatch via the peer mesh; pkg/service stays leaf-level (no
// universe import).
type RemotePublishCall struct {
	TypeName string
	PeerIDs  []string
	Payload  any
	Sequence uint64
}

// RemotePublishFunc is the callback shape Universe installs onto the Bus.
type RemotePublishFunc func(call RemotePublishCall)

// SubscriptionFlushFunc is the callback shape Universe installs to
// receive whole-set updates of the local subscribed type names. The Bus
// invokes this after every Subscribe / Unsubscribe (debounced upstream
// in the universe wiring; the Bus itself fires synchronously per change
// and lets universe debounce to coord).
type SubscriptionFlushFunc func(typeNames []string)

// SetRemotePublish installs the cross-process dispatch callback. Calling
// twice replaces the previous callback (last-write-wins). nil disables
// remote dispatch.
func (b *Bus) SetRemotePublish(fn RemotePublishFunc) {
	b.remoteMu.Lock()
	defer b.remoteMu.Unlock()
	b.remotePublish = fn
}

// SetSubscriptionFlush installs the typeset-change callback. Universe
// uses this to debounce + ship ServiceEventSubscribe to coord.
func (b *Bus) SetSubscriptionFlush(fn SubscriptionFlushFunc) {
	b.remoteMu.Lock()
	defer b.remoteMu.Unlock()
	b.subscriptionFlush = fn
}

// SetRoutingCache atomically replaces the local view of the cluster's
// type-name → []processID routing table. Called by universe whenever
// a fresh PeerList lands. Whole-map replace, not patch — mirrors the
// coord-side whole-set semantics.
func (b *Bus) SetRoutingCache(table map[string][]string) {
	b.remoteMu.Lock()
	defer b.remoteMu.Unlock()
	// Defensive copy so the caller's map can't mutate live state.
	cp := make(map[string][]string, len(table))
	for k, v := range table {
		out := make([]string, len(v))
		copy(out, v)
		cp[k] = out
	}
	b.routingCache = cp
}

// RoutingCacheSnapshot returns a defensive copy of the current routing
// cache. For tests + diagnostic console output. Never call this on a
// hot path — it allocates.
func (b *Bus) RoutingCacheSnapshot() map[string][]string {
	b.remoteMu.RLock()
	defer b.remoteMu.RUnlock()
	cp := make(map[string][]string, len(b.routingCache))
	for k, v := range b.routingCache {
		cp[k] = append([]string(nil), v...)
	}
	return cp
}

// SubscribedTypeNames returns the sorted set of currently-active type
// names this Bus has live subscribers for. Used by universe to build
// the ServiceEventSubscribe message.
func (b *Bus) SubscribedTypeNames() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]string, 0, len(b.handlers))
	for t, slots := range b.handlers {
		live := false
		for _, s := range slots {
			if !s.dead {
				live = true
				break
			}
		}
		if live {
			out = append(out, typeName(t))
		}
	}
	sortStrings(out)
	return out
}

// PublishAny is the type-erased entry point used by the wire receive
// path. typ MUST be the concrete reflect.Type of ev (no interface
// values). Local subscribers fire synchronously; no remote fan-out
// (re-broadcast loops are prevented at this layer — the publisher's
// dispatch path already handled remote delivery).
func PublishAny(b *Bus, typ reflect.Type, ev any) {
	publishAny(b, typ, ev)
}

// nextSeq returns a monotonic per-Bus sequence number for ServiceEvent.sequence.
func (b *Bus) nextSeq() uint64 {
	b.remoteMu.Lock()
	defer b.remoteMu.Unlock()
	b.seq++
	return b.seq
}

// peerIDsExceptSelf returns the routing-cache entry for typeName minus
// b.processID. Returns nil if there are no remote subscribers OR if the
// type is not wire-eligible (a process-local-only event has no
// cross-process delivery, even when remote subscribers exist).
func (b *Bus) peerIDsExceptSelf(typeName string) []string {
	if !IsWireEligible(typeName) {
		return nil
	}
	b.remoteMu.RLock()
	all := b.routingCache[typeName]
	self := b.processID
	b.remoteMu.RUnlock()
	if len(all) == 0 {
		return nil
	}
	out := make([]string, 0, len(all))
	for _, id := range all {
		if id == self {
			continue
		}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// notifyRemote invokes the registered RemotePublishFunc (if any) for the
// given event and resolved peer set. Called by Publish after local
// dispatch completes; PublishLocal skips this path entirely.
func (b *Bus) notifyRemote(name string, peers []string, payload any) {
	b.remoteMu.RLock()
	fn := b.remotePublish
	b.remoteMu.RUnlock()
	if fn == nil || len(peers) == 0 {
		return
	}
	fn(RemotePublishCall{
		TypeName: name,
		PeerIDs:  peers,
		Payload:  payload,
		Sequence: b.nextSeq(),
	})
}

// notifySubscriptionChanged invokes the registered SubscriptionFlushFunc
// with the current full type-name set. Called from Subscribe and from
// the Unsubscribe closure when slot.dead is set. Universe is expected
// to debounce.
func (b *Bus) notifySubscriptionChanged() {
	b.remoteMu.RLock()
	fn := b.subscriptionFlush
	b.remoteMu.RUnlock()
	if fn == nil {
		return
	}
	fn(b.SubscribedTypeNames())
}

// sortStrings is a small in-package helper to avoid importing "sort"
// in the hot path repeatedly.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}

// remoteState bundles the Phase 3 fields. Embedded into Bus so the
// Phase 1 file (bus.go) doesn't have to be edited beyond the field
// addition + a tiny hook in publishAny.
//
// Held under b.remoteMu, separate from b.mu (which guards handlers).
// processID lives here too so SetProcessID and peerIDsExceptSelf both
// take the same lock — universe re-stamps processID post-Build to
// align with the gateway-derived identifier.
type remoteState struct {
	remoteMu          sync.RWMutex
	processID         string
	remotePublish     RemotePublishFunc
	subscriptionFlush SubscriptionFlushFunc
	routingCache      map[string][]string
	seq               uint64
}
