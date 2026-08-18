package universe

// globalWire is the migration seam for CE-010 part B, and nothing else.
//
// Part B moves four package-global wire registries onto a per-Process
// WireRegistry. Doing that in one commit would change the registry's storage,
// every read site in this package, the signature of five registration verbs,
// and ~28 game call sites at once — with no green step in between and no way
// to tell a threading mistake from a scoping change.
//
// So the storage moves first and stays global: one WireRegistry that every
// read site and every verb shares, which is behaviourally identical to four
// global maps by construction. Process.Wire() and Stage.Wire() return it,
// which lets the call sites take their final shape before the value behind
// them stops being shared. The last step points those two accessors at a
// per-Process registry and deletes this file — the only step in the sequence
// where --dump-schema can legitimately move.
//
// Part A used the same seam for coords.CellSize.
var globalWire = NewWireRegistry()

// GlobalWire returns the shared registry. Exported only because the mmokit
// facade's registration verbs do not take a *Process yet; the step that gives
// them one deletes this along with the rest of the file.
func GlobalWire() *WireRegistry { return globalWire }

// Wire returns the process's client-facing message registry.
//
// Still process-independent — see globalWire.
func (c *Process) Wire() *WireRegistry { return globalWire }

// Wire returns the registry of the process that owns this stage.
//
// Still process-independent — see globalWire.
func (b *Stage) Wire() *WireRegistry { return globalWire }
