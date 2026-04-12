package universe

// Handoff timing constants are fixed and not per-tier. These thresholds
// exist to defend protocol-level guarantees (no cold-start stalls, no
// flapping) and per-tier tunability would let games accidentally defeat
// the guarantees. See the design spec at
// docs/superpowers/specs/2026-04-11-tiered-push-replication-design.md
// for the reasoning.
const (
	// MinWarmupTicks is the minimum number of full-rate update ticks
	// that the destination node must receive for a promoted entity
	// before authority can commit. 5 ticks = 250ms at 20Hz.
	MinWarmupTicks = 5

	// MaxWarmupTicks is the maximum number of ticks an entity can stay in
	// the Promoted phase before the handoff is cancelled and the shadow on
	// the destination is cleaned up. Prevents indefinite shadow accumulation
	// when an entity enters PromoteRadius but retreats before crossing.
	// 100 ticks = 5 seconds at 20Hz. Only relevant once promote-radius
	// early detection is added (v1.1+); v1 still fires Prepare+Commit
	// together at crossing time with no warmup gap.
	MaxWarmupTicks = 100

	// CrossingCooldownTicks is the duration after a successful commit
	// during which the entity cannot be handed off again in either
	// direction. Prevents thrash for entities hovering on a cell
	// boundary. 20 ticks = 1 second at 20Hz.
	CrossingCooldownTicks = 20
)

// HandoffPhase identifies one of four lifecycle states for an
// (entity, neighbor) pair during co-simulation handoff.
type HandoffPhase uint8

const (
	// HandoffUnseen is the default: entity is outside the neighbor's
	// border tier and nothing is being sent.
	HandoffUnseen HandoffPhase = iota

	// HandoffBorder is the entity within the neighbor's tier radius;
	// low-rate updates are flowing via Dispatcher.Walk.
	HandoffBorder

	// HandoffPromoted is an entity within PromoteRadius (or trajectory-
	// predicted to cross); full-rate updates are flowing and the
	// warmup counter is advancing each tick.
	HandoffPromoted

	// HandoffHandoff is the final phase: the entity has physically
	// crossed the boundary and the state machine is committing on
	// the next eligible tick. Phase 7 transitions here immediately
	// before emitting MsgHandoffCommit.
	HandoffHandoff
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
	phase         HandoffPhase
	warmupCount   int
	cooldownStart uint64 // tick when EnterCooldown was called; 0 means never
	cooldownSet   bool   // distinguishes "never set" from "set at tick 0"
}

// HandoffStateMachine tracks per-(entity,neighbor) phase and
// warmup/cooldown state. Not thread-safe — callers must serialize
// access (the border dispatcher runs on the game loop goroutine).
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

// SetState transitions the key to a new phase. Transitioning into
// HandoffPromoted resets the warmup counter to 0 so the warmup floor
// is enforced fresh on each promotion.
func (sm *HandoffStateMachine) SetState(k HandoffKey, phase HandoffPhase) {
	e := sm.entries[k]
	if e == nil {
		e = &handoffEntry{}
		sm.entries[k] = e
	}
	// Reset warmup counter on fresh promotion (including re-promotion
	// after a retreat-to-Border). A transition from non-Promoted to
	// Promoted is "fresh"; a no-op Promoted → Promoted does not reset.
	if phase == HandoffPromoted && e.phase != HandoffPromoted {
		e.warmupCount = 0
	}
	e.phase = phase
}

// WarmupCount returns the current warmup tick count for the key.
// Unknown or non-Promoted keys return 0.
func (sm *HandoffStateMachine) WarmupCount(k HandoffKey) int {
	if e := sm.entries[k]; e != nil {
		return e.warmupCount
	}
	return 0
}

// TickWarmup advances the warmup counter by one tick, but only if
// the key is currently in HandoffPromoted. Safe no-op otherwise.
func (sm *HandoffStateMachine) TickWarmup(k HandoffKey) {
	e := sm.entries[k]
	if e == nil || e.phase != HandoffPromoted {
		return
	}
	e.warmupCount++
}

// CanCommit reports whether the key is eligible to transition to
// HandoffHandoff and emit a commit message. Requires Promoted phase
// AND warmup counter at or above MinWarmupTicks.
func (sm *HandoffStateMachine) CanCommit(k HandoffKey) bool {
	e := sm.entries[k]
	return e != nil &&
		e.phase == HandoffPromoted &&
		e.warmupCount >= MinWarmupTicks
}

// EnterCooldown marks the key as entering commit cooldown at the
// given tick. Subsequent InCooldown calls will return true for ticks
// in the range (tick, tick+CrossingCooldownTicks]. The state machine
// keeps the cooldown record even if the pair transitions back to
// Unseen — Forget clears it.
func (sm *HandoffStateMachine) EnterCooldown(k HandoffKey, tick uint64) {
	e := sm.entries[k]
	if e == nil {
		e = &handoffEntry{}
		sm.entries[k] = e
	}
	e.cooldownStart = tick
	e.cooldownSet = true
}

// InCooldown reports whether currentTick is inside the active
// cooldown window for the key. The window is exclusive on the start
// tick (entering and acting on the same tick is allowed) and
// inclusive on the end tick. Returns false for keys that have never
// entered cooldown.
func (sm *HandoffStateMachine) InCooldown(k HandoffKey, currentTick uint64) bool {
	e := sm.entries[k]
	if e == nil || !e.cooldownSet {
		return false
	}
	if currentTick <= e.cooldownStart {
		return false
	}
	return currentTick-e.cooldownStart <= CrossingCooldownTicks
}

// Forget removes all state for a key. Used when an entity has drifted
// entirely out of scope for a given neighbor and the state machine
// should release its entry. After Forget, State returns HandoffUnseen
// and WarmupCount/InCooldown return their zero-state responses.
func (sm *HandoffStateMachine) Forget(k HandoffKey) {
	delete(sm.entries, k)
}

// PromotedNeighborsFor returns the neighbor IDs where the given entity
// is currently in the Promoted phase (and not in cooldown). Used by the
// handoff driver to identify shadows that need to be cancelled when the
// entity commits to a different neighbor.
func (sm *HandoffStateMachine) PromotedNeighborsFor(entityNetID uint32) []string {
	var out []string
	for k, e := range sm.entries {
		if k.EntityNetID == entityNetID && e.phase == HandoffPromoted {
			out = append(out, k.NeighborID)
		}
	}
	return out
}
