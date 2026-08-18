package universe_test

import (
	"testing"

	"github.com/mmokit/mmokit"
)

// newOpTestProcess builds a headless Process for the typed-op dispatch tests.
//
// The dispatcher resolves handlers against a registry now, and the registry
// belongs to a Process — so a test that registers an op needs one. The reset
// is still required because every Process in a binary shares one registry;
// the flip removes the need for it.
func newOpTestProcess(t *testing.T) *mmokit.Process {
	t.Helper()
	p := mmokit.New(mmokit.Config{Mode: "all", CellsX: 1, CellsY: 1, Headless: true})
	p.Wire().ResetTypedOpsForTest()
	t.Cleanup(p.Wire().ResetTypedOpsForTest)
	return p
}
