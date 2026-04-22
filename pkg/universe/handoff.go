package universe

// HandoffPhase identifies the lifecycle state of an (entity, neighbor)
// pair during handoff. Phase G of the Replication Timeline Redesign
// will finish removing the enum; for now the bare HandoffUnseen value
// remains so surrounding code compiles.
type HandoffPhase uint8

const (
	// HandoffUnseen is the default: entity is outside the neighbor's
	// border tier and nothing is being sent.
	HandoffUnseen HandoffPhase = iota
)

// HandoffKey identifies one (entity, neighbor) pair in the state
// machine. The same entity has a separate state for each neighbor it
// might be handed off to.
type HandoffKey struct {
	EntityNetID uint32
	NeighborID  string
}

// handoffEntry holds the per-pair state. Unexported: callers go
// through HandoffStateMachine methods.
type handoffEntry struct {
	phase HandoffPhase
}

// HandoffStateMachine tracks per-(entity,neighbor) phase. Not
// thread-safe — callers must serialize access (the border dispatcher
// runs on the game loop goroutine).
type HandoffStateMachine struct {
	entries map[HandoffKey]*handoffEntry
}

// NewHandoffStateMachine creates an empty state machine.
func NewHandoffStateMachine() *HandoffStateMachine {
	return &HandoffStateMachine{
		entries: make(map[HandoffKey]*handoffEntry),
	}
}

// State returns the current phase for a key. Unknown keys return
// HandoffUnseen.
func (sm *HandoffStateMachine) State(k HandoffKey) HandoffPhase {
	if e := sm.entries[k]; e != nil {
		return e.phase
	}
	return HandoffUnseen
}

// SetState transitions the key to a new phase.
func (sm *HandoffStateMachine) SetState(k HandoffKey, phase HandoffPhase) {
	e := sm.entries[k]
	if e == nil {
		e = &handoffEntry{}
		sm.entries[k] = e
	}
	e.phase = phase
}

// Forget removes all state for a key. Used when an entity has drifted
// entirely out of scope for a given neighbor and the state machine
// should release its entry. After Forget, State returns HandoffUnseen.
func (sm *HandoffStateMachine) Forget(k HandoffKey) {
	delete(sm.entries, k)
}
