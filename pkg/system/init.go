package system

import (
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
)

func init() {
	component.SetDefaultCellSize(coords.CellSize)
}
