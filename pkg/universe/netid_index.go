package universe

import (
	"sync"

	"github.com/mlange-42/ark/ecs"
)

type EntityPresence uint8

const (
	PresenceNone EntityPresence = iota
	PresenceLive
	PresenceShadow
	PresenceReplica
)

type TransitionAction uint8

const (
	ActionInstalled TransitionAction = iota
	ActionPromoted
	ActionReplaced
	ActionUpdated
	ActionRejected
	ActionDuplicate
)

type TransitionResult struct {
	Action     TransitionAction
	PrevEntity ecs.Entity
}

type netIDIndex struct {
	mu    sync.RWMutex
	slots map[uint32]netIDSlot
}

type netIDSlot struct {
	Entity   ecs.Entity
	Presence EntityPresence
}

func newNetIDIndex() *netIDIndex {
	return &netIDIndex{slots: make(map[uint32]netIDSlot)}
}

func (idx *netIDIndex) Lookup(netID uint32) (ecs.Entity, EntityPresence, bool) {
	idx.mu.RLock()
	defer idx.mu.RUnlock()
	s, ok := idx.slots[netID]
	if !ok {
		return ecs.Entity{}, PresenceNone, false
	}
	return s.Entity, s.Presence, true
}

func (idx *netIDIndex) Enter(netID uint32, entity ecs.Entity, to EntityPresence) TransitionResult {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	cur, ok := idx.slots[netID]
	if !ok {
		idx.slots[netID] = netIDSlot{Entity: entity, Presence: to}
		return TransitionResult{Action: ActionInstalled}
	}

	switch cur.Presence {
	case PresenceLive:
		switch to {
		case PresenceLive:
			return TransitionResult{Action: ActionDuplicate, PrevEntity: cur.Entity}
		case PresenceShadow, PresenceReplica:
			return TransitionResult{Action: ActionRejected}
		}
	case PresenceShadow:
		switch to {
		case PresenceLive:
			idx.slots[netID] = netIDSlot{Entity: entity, Presence: PresenceLive}
			return TransitionResult{Action: ActionPromoted}
		case PresenceShadow:
			return TransitionResult{Action: ActionRejected}
		case PresenceReplica:
			prev := cur.Entity
			idx.slots[netID] = netIDSlot{Entity: entity, Presence: PresenceShadow}
			return TransitionResult{Action: ActionReplaced, PrevEntity: prev}
		}
	case PresenceReplica:
		switch to {
		case PresenceLive, PresenceShadow:
			prev := cur.Entity
			idx.slots[netID] = netIDSlot{Entity: entity, Presence: to}
			return TransitionResult{Action: ActionReplaced, PrevEntity: prev}
		case PresenceReplica:
			idx.slots[netID] = netIDSlot{Entity: entity, Presence: PresenceReplica}
			return TransitionResult{Action: ActionUpdated}
		}
	}
	return TransitionResult{Action: ActionRejected}
}

func (idx *netIDIndex) Exit(netID uint32) {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	delete(idx.slots, netID)
}

// Demote is the explicit Live → Replica transition used by
// DemoteLiveToReplica at handoff commit on the source cell. Unlike
// Enter(..., PresenceReplica) which rejects on a Live slot (so a stray
// border frame cannot silently downgrade a live entity), Demote is the
// sanctioned path: called by the handoff driver when the destination
// has committed and the source is converting its Live copy into a
// Replica that will be kept in sync by the destination's subsequent
// border frames.
//
// Returns ActionUpdated on success, ActionRejected if the slot is not
// currently Live for this netID.
func (idx *netIDIndex) Demote(netID uint32, entity ecs.Entity) TransitionResult {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	cur, ok := idx.slots[netID]
	if !ok || cur.Presence != PresenceLive {
		return TransitionResult{Action: ActionRejected}
	}
	idx.slots[netID] = netIDSlot{Entity: entity, Presence: PresenceReplica}
	return TransitionResult{Action: ActionUpdated, PrevEntity: cur.Entity}
}

// Promote is the explicit Replica → Live transition used by
// PromoteReplicaToLive on the destination cell at handoff commit.
// Symmetric to Demote: the sanctioned path for flipping a border
// replica into an authoritative Live slot. Enter(..., PresenceLive)
// on a Replica slot would succeed as ActionReplaced (and remove the
// previous entity), but the hard-cut handoff wants to PROMOTE the
// existing entity in place — same ECS handle, same components. Hence
// this explicit primitive.
//
// Returns ActionUpdated on success, ActionRejected if the slot is not
// currently Replica for this netID.
func (idx *netIDIndex) Promote(netID uint32, entity ecs.Entity) TransitionResult {
	idx.mu.Lock()
	defer idx.mu.Unlock()

	cur, ok := idx.slots[netID]
	if !ok || cur.Presence != PresenceReplica {
		return TransitionResult{Action: ActionRejected}
	}
	idx.slots[netID] = netIDSlot{Entity: entity, Presence: PresenceLive}
	return TransitionResult{Action: ActionUpdated, PrevEntity: cur.Entity}
}
