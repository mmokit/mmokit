package universe

// ActionTypedMessage is the engine-internal opcode for a typed cross-cell
// message dispatched via mmokit.Send. Uses a high constant value (100) to
// avoid collision with game-defined ActionTypes (which start at 1).
const ActionTypedMessage ActionType = 100
