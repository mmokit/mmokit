package commands

import (
	"context"
	"fmt"
	"strings"

	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

type NPCSpawnArgs struct {
	X         float32 `cmd:"help=world X coordinate"`
	Y         float32 `cmd:"help=world Y coordinate"`
	Archetype string  `cmd:"help=brawler"`
}

type NPCSpawnResult struct {
	NetID     uint32
	Archetype string
	CellID    string
}

// registerNPCSpawn wires the npc.spawn console verb. Spawns a single NPC of
// the given archetype at world position (X,Y) with poiNetID=0 (no POI anchor),
// resolving the destination cell from the world coordinates. Console-only:
// for ad-hoc archetype testing without driving a full POI through the
// spawner.
func registerNPCSpawn(reg *cmdsys.Registry, coord *mmokit.Process) error {
	return reg.Register(cmdsys.Command{
		Verb:        "npc.spawn",
		Capability:  "npc.spawn",
		Description: "spawn a test NPC at world (X,Y) of the given archetype (no POI anchor)",
		Examples:    []string{"npc.spawn 100 200 brawler"},
		// RouteLocal: the handler resolves the destination cell from (X,Y)
		// internally via coord.CellAtPosition. Mirrors entity.spawn —
		// RouteSpecificCell would require a CellID arg in the schema.
		Route:  cmdsys.RouteLocal,
		Args:   NPCSpawnArgs{},
		Result: NPCSpawnResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(NPCSpawnArgs)
			archetype, err := resolveArchetype(args.Archetype)
			if err != nil {
				return nil, err
			}
			cellID := coord.CellAtPosition(args.X, args.Y)
			if cellID == "" {
				return nil, fmt.Errorf("npc.spawn: no cell owns world position (%g,%g)", args.X, args.Y)
			}
			cell := coord.CellByID(mmokit.MeshCellID(cellID))
			if cell == nil || cell.Stage == nil {
				return nil, fmt.Errorf("npc.spawn: cell %q not local to this host", cellID)
			}
			gw := gwForStage(coord, cell.Stage)
			if gw == nil {
				return nil, fmt.Errorf("npc.spawn: not a game-world cell")
			}

			// Convert world coords to cell-local coords for SpawnNPC.
			minX, minY, _, _ := cell.CellID().WorldBounds(mmokit.CellSize())
			localX := args.X - minX
			localY := args.Y - minY

			return mmokit.CmdOnLoop(ctx, cell.Engine, func() (NPCSpawnResult, error) {
				e := gw.SpawnNPC(localX, localY, archetype, 0, game.NPCSpawnModifiers{})
				return NPCSpawnResult{
					NetID:     e.NetID(),
					Archetype: strings.ToLower(args.Archetype),
					CellID:    cellID,
				}, nil
			})
		},
	})
}

// resolveArchetype maps the archetype string to its game-side const.
// Unknown values produce a clear error listing the valid choices.
func resolveArchetype(name string) (uint8, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "brawler":
		return game.ArchetypeBrawler, nil
	default:
		return 0, fmt.Errorf("unknown archetype %q (valid: brawler)", name)
	}
}
