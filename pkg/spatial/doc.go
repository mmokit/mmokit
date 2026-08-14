// Package spatial provides an incremental spatial hash grid for
// area-of-interest queries, broad-phase collision detection, shape-aware
// overlap tests, and segment raycasts.
//
// HashGrid is not synchronized. A cell owns its grid and updates it on the
// cell loop goroutine.
//
// Scope: this package is broad-phase and query machinery, not a physics
// engine. There is no dynamic rigid-body response and no triangle-mesh
// collider, and that is structural rather than unfinished — entities near a
// cell boundary exist on the neighbouring host as quantized, dead-reckoned,
// up-to-one-tick-stale replicas, so two hosts running a solver over them would
// disagree continuously. Games bring their own solver.
package spatial
