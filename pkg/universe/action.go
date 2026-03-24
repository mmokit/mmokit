package universe

// ActionType identifies a cross-node action (game-defined).
type ActionType uint16

// CrossNodeAction is a request sent to the authoritative node for an entity.
// The originating node sends this when a local entity (e.g. a player) acts on
// a replica — the authoritative node executes the action and optionally returns
// an ActionResult.
type CrossNodeAction struct {
	Type         ActionType
	TargetNetID  uint32 // the target entity's network ID on the authoritative node
	SourceNetID  uint32 // the acting entity's network ID
	SourceNodeID string // which node originated this action
	Payload      []byte // game-serialized action data
}

// ActionResult is the response sent back to the originating node after a
// cross-node action is processed on the authoritative node.
type ActionResult struct {
	Type        ActionType
	TargetNetID uint32
	SourceNetID uint32
	Success     bool
	Payload     []byte // game-serialized result data
	SideEffects []byte // serialized []SideEffect from the authoritative node
}
