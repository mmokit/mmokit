// Package engine is the low-level ECS runtime shared by every cell. It owns
// the Ark world, player sessions, the fixed-timestep loop, deferred entity
// removal, the loop-job queue, profiling, and the interactive console
// foundations.
//
// Game code should normally use [github.com/zenion/mmokit/pkg/mmokit] instead.
// The universe layer constructs and wires one Engine per cell; this package is
// the machinery underneath that, exported because the framework spans several
// packages rather than because games are expected to reach for it.
//
// Concurrency: ECS state belongs to the owning cell loop's goroutine.
// Off-loop work — admin commands, service callbacks — must be routed through
// RunOnLoop rather than touching the world directly.
package engine
