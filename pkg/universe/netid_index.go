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
