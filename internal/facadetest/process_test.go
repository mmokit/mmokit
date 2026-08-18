package facadetest

import (
	"testing"

	"github.com/mmokit/mmokit"
)

// newFacadeProcess builds the headless single-cell Process the registry tests
// register against. The wire registries hang off a Process now, so a test that
// exercises a registration verb needs one.
func newFacadeProcess(t *testing.T) *mmokit.Process {
	t.Helper()
	return mmokit.New(mmokit.Config{
		Mode:     "all",
		CellsX:   1,
		CellsY:   1,
		Headless: true,
	})
}
