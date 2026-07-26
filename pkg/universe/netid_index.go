// Package universe
//
// netIDIndex maps each netID to exactly one (ecs.Entity, presence) slot
// per cell. Presence is Live (cell authoritative) or Replica (border
// copy); Demote and Promote are the sanctioned primitives for
// transferring authority between the two. Unsolicited Enter(Replica)
// on a Live slot rejects — a stray border frame cannot silently
// downgrade a live entity. Enter(Live) on a Replica slot also rejects;
// the destination-side handoff commit must go through Promote so the
// existing ECS row is kept (same handle, same components).
package universe

import (
	"sync"

	"github.com/mlange-42/ark/ecs"
)

type EntityPresence uint8

const (
	PresenceNone EntityPresence = iota
	PresenceLive
	PresenceReplica
)

type TransitionAction uint8

const (
	ActionInstalled TransitionAction = iota
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

// Enter installs a (netID, entity, presence) slot or updates an existing
// one according to the 2×2 transition table:
//
//	current  incoming  result
//	-------  --------  --------------------------------------------------
//	(none)   Live      ActionInstalled
//	(none)   Replica   ActionInstalled
//	Live     Live      ActionDuplicate (second spawner must roll back)
//	Live     Replica   ActionRejected (use Demote for the sanctioned path)
//	Replica  Live      ActionRejected (use Promote for the sanctioned path)
//	Replica  Replica   ActionUpdated (entity handle refreshed)
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
		if to == PresenceLive {
			return TransitionResult{Action: ActionDuplicate, PrevEntity: cur.Entity}
		}
		// Live → Replica must go through Demote (explicit path).
		return TransitionResult{Action: ActionRejected}
	case PresenceReplica:
		if to == PresenceReplica {
			idx.slots[netID] = netIDSlot{Entity: entity, Presence: PresenceReplica}
			return TransitionResult{Action: ActionUpdated}
		}
		// Replica → Live must go through Promote (explicit path).
		return TransitionResult{Action: ActionRejected}
	}
	return TransitionResult{Action: ActionRejected}
}

// ExitEntity removes netID only when its slot still belongs to entity. This
// identity check is required by rollback cleanup: a rejected duplicate ECS row
// must never evict the pre-existing Live or Replica slot it collided with.
func (idx *netIDIndex) ExitEntity(netID uint32, entity ecs.Entity) bool {
	idx.mu.Lock()
	defer idx.mu.Unlock()
	cur, ok := idx.slots[netID]
	if !ok || cur.Entity != entity {
		return false
	}
	delete(idx.slots, netID)
	return true
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
// on a Replica slot rejects — the hard-cut handoff wants to PROMOTE
// the existing entity in place (same ECS handle, same components).
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
