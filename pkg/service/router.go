package service

import (
	"fmt"
	"sort"
	"sync"
)

// InstanceRoute is the gateway's routing entry for a single live
// service instance.
type InstanceRoute struct {
	InstanceID string
	HostID     string
}

// ServiceRecord is the package-local mirror of meshpb.ServiceRecord.
// Universe code converts proto messages to []ServiceRecord at the
// PeerList apply boundary so pkg/service avoids importing meshpb.
type ServiceRecord struct {
	Kind       string
	InstanceID string
	HostID     string
	OpCodes    []uint32
}

// RoutingIndex is the gateway's view of which kind owns each op code
// and which instances of each kind are live. Built from PeerList.
//
// Concurrency: read-heavy. Updated atomically on PeerList apply by
// rebuilding the entire index, which is then swapped under the mu.
type RoutingIndex struct {
	mu        sync.RWMutex
	opToKind  map[uint32]string
	instances map[string][]InstanceRoute // sorted lex by InstanceID
}

// NewRoutingIndex returns an empty index.
func NewRoutingIndex() *RoutingIndex {
	return &RoutingIndex{
		opToKind:  map[uint32]string{},
		instances: map[string][]InstanceRoute{},
	}
}

// Apply rebuilds the index from a slice of ServiceRecords. Returns an
// error if any op code is claimed by two different kinds in the same
// snapshot — that's a coordinator-side validation bug since it should
// have rejected the conflicting registration.
func (r *RoutingIndex) Apply(records []ServiceRecord) error {
	op := map[uint32]string{}
	inst := map[string][]InstanceRoute{}
	for _, rec := range records {
		for _, code := range rec.OpCodes {
			if owner, exists := op[code]; exists && owner != rec.Kind {
				return fmt.Errorf("RoutingIndex.Apply: op code %d claimed by %q and %q", code, owner, rec.Kind)
			}
			op[code] = rec.Kind
		}
		inst[rec.Kind] = append(inst[rec.Kind], InstanceRoute{
			InstanceID: rec.InstanceID,
			HostID:     rec.HostID,
		})
	}
	for k := range inst {
		sort.Slice(inst[k], func(i, j int) bool {
			return inst[k][i].InstanceID < inst[k][j].InstanceID
		})
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.opToKind = op
	r.instances = inst
	return nil
}

// LookupKind returns the kind name owning opCode, or "" if unclaimed.
func (r *RoutingIndex) LookupKind(opCode uint32) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.opToKind[opCode]
}

// PickInstance picks an instance of kind for the given affinity key
// (typically the connID). Routing is hash(connID) % N where N is the
// instance count, with instances sorted lex by InstanceID so all
// gateways converge on the same pick without coordination.
//
// Returns the chosen route and true, or an empty route and false if
// no instances are live.
func (r *RoutingIndex) PickInstance(kind string, connID uint32) (InstanceRoute, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	insts := r.instances[kind]
	if len(insts) == 0 {
		return InstanceRoute{}, false
	}
	return insts[connID%uint32(len(insts))], true
}

// InstancesOfKind returns a defensive copy of the live instances for kind.
func (r *RoutingIndex) InstancesOfKind(kind string) []InstanceRoute {
	r.mu.RLock()
	defer r.mu.RUnlock()
	src := r.instances[kind]
	out := make([]InstanceRoute, len(src))
	copy(out, src)
	return out
}

// AllOps returns the full opCode → kind mapping (defensive copy).
func (r *RoutingIndex) AllOps() map[uint32]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[uint32]string, len(r.opToKind))
	for k, v := range r.opToKind {
		out[k] = v
	}
	return out
}

// Kinds returns the sorted list of kinds with at least one live instance.
func (r *RoutingIndex) Kinds() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.instances))
	for k := range r.instances {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
