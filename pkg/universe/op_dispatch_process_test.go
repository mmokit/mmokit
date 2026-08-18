package universe_test

import (
	"testing"

	"github.com/mmokit/mmokit"
)

// newOpTestProcess builds a headless Process for the typed-op dispatch tests.
//
// The dispatcher resolves handlers against a registry, and the registry belongs
// to a Process — so a test that registers an op needs one. No reset and no
// cleanup: the registry is this Process's alone, so it starts with only what
// mmokit.New put on it and nothing this test registers escapes to the next.
func newOpTestProcess(t *testing.T) *mmokit.Process {
	t.Helper()
	return mmokit.New(mmokit.Config{Mode: "all", CellsX: 1, CellsY: 1, Headless: true})
}
