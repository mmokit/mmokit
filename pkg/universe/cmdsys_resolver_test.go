package universe

import (
	"testing"

	"github.com/zenion/mmoserver/pkg/cmdsys"
)

func TestProcess_ImplementsLocalProcess(t *testing.T) {
	var lp cmdsys.LocalProcess = (*Process)(nil)
	_ = lp
}
