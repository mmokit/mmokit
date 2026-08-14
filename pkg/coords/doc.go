// Package coords converts between world coordinates and the cell grid that
// partitions them.
//
// Cells partition the ground plane and are effectively infinite vertical
// columns: a cell identifier is {X, Y, Depth}, with four children per split and
// eight neighbours. Verticality is simulated, never partitioned.
//
// Clients never see any of this. They receive absolute world coordinates, and
// cell identity is a server-internal concern.
package coords
