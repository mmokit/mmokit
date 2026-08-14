// Package universe owns the distributed layer: processes, cells, roles,
// topology, host assignment, entity handoff, and cluster integrity.
//
// A Process is one running binary. It carries any combination of four roles:
//
//   - coordinator — the cluster control plane and assignment state
//   - host        — owns cells and runs their ECS loops
//   - gateway     — terminates client WebSocket/UDP connections and routes
//   - service     — hosts selected service kinds such as auth or chat
//
// The default "all" preset carries all four in a single process, which is what
// local development runs. In a distributed deployment, per-tick and service
// payloads travel directly between the relevant gateways, hosts, and services;
// the coordinator is a control plane and never a payload relay.
//
// A Cell is one square of the partitioned world with its own Engine and loop
// goroutine. Cells split under load and merge when quiet, and can migrate
// between hosts. Entities crossing a boundary are handed off with an explicit
// authority epoch, so exactly one host simulates an entity at any tick.
//
// # What games should use instead
//
// This package is exported because the framework spans several packages, not
// because games are expected to assemble it. Import
// [github.com/zenion/mmokit/pkg/mmokit] instead — Process, Stage, and the
// registration verbs are all re-exported there with the sharp edges covered.
//
// # Locking
//
// Where both are needed, acquire Process.mu before Control.mu. ECS state
// belongs to its cell loop's goroutine; route off-loop work through RunOnLoop.
package universe
