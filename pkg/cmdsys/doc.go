// Package cmdsys is the typed, routable operator command system. One verb
// definition backs both the interactive console and the HTTP admin dashboard,
// so the two cannot drift in behaviour, RBAC, or audit trail.
//
// A command declares its argument schema, the capability required to run it,
// and a route describing where in the cluster it must execute — locally, on
// the host owning a cell, on every host, or on the coordinator. The dispatcher
// forwards it there over the mesh and collects the result.
//
// Operator mutations should be implemented here first and surfaced in the UI
// second, rather than as a bespoke HTTP handler that bypasses the audit path.
package cmdsys
