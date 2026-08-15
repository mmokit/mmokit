// Package system holds the generic, game-agnostic simulation systems the
// framework installs into every cell: movement and physics integration,
// lifetime expiry, spatial-index maintenance, and replication.
//
// These run in a fixed, semantic order. Games add their own systems around
// them through [github.com/mmokit/mmokit]; order matters, and the
// framework flushes deferred structural commands between systems so system N's
// changes are visible to system N+1 within the same tick.
package system
