package universe

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/component"
	"github.com/zenion/mmoserver/pkg/coords"
)

// ── entity.spawn ────────────────────────────────────────────────────────────

type entitySpawnArgs struct {
	Kind   string  `cmd:"help=registered entity kind name"`
	Count  int32   `cmd:"help=number of entities to spawn"`
	X      float32 `cmd:"help=center world X"`
	Y      float32 `cmd:"help=center world Y"`
	Radius float32 `cmd:"optional,help=randomization radius (0=exact center)"`
}

type entitySpawnResult struct {
	Kind    string
	Count   int32
	CellID  string
	HostID  string
	Spawned int32
}

// ── entity.despawn ───────────────────────────────────────────────────────────

type entityDespawnArgs struct {
	NetID uint32 `cmd:"help=entity network ID"`
}

type entityDespawnResult struct {
	NetID  uint32
	Kind   string
	CellID string
	HostID string
}

// ── entity.list ─────────────────────────────────────────────────────────────

type entityListArgs struct {
	Kind string `cmd:"optional,help=filter by registered kind name"`
}

type entityRow struct {
	NetID  uint32
	Kind   string
	WorldX float32
	WorldY float32
	CellID string
	HostID string
}

type entityListResult struct {
	Entities []entityRow `cmd:"table"`
}

// ── entity.tp ────────────────────────────────────────────────────────────────

type entityTpArgs struct {
	NetID uint32  `cmd:"help=entity network ID"`
	X     float32 `cmd:"help=target world X"`
	Y     float32 `cmd:"help=target world Y"`
}

type entityTpResult struct {
	NetID      uint32
	Kind       string
	PrevWorldX float32
	PrevWorldY float32
	NewWorldX  float32
	NewWorldY  float32
	HostID     string
}

// ── helpers ──────────────────────────────────────────────────────────────────

// resolveKindName returns the kind name string for a given kind type byte,
// or a fallback "kind_N" if the kind is not registered.
func resolveKindName(stage *Stage, kindType uint8) string {
	if stage == nil {
		return fmt.Sprintf("kind_%d", kindType)
	}
	if def, ok := stage.EntityKindDefs()[kindType]; ok && def.Name != "" {
		return def.Name
	}
	return fmt.Sprintf("kind_%d", kindType)
}

// findKindByName looks up the kind name in the stage's kind def map and
// returns the numeric kind ID. Returns (0, false) if not found.
func findKindByName(stage *Stage, kindName string) (uint8, bool) {
	for id, def := range stage.EntityKindDefs() {
		if def.Name == kindName {
			return id, true
		}
	}
	return 0, false
}

// worldToLocal converts a world coordinate to cell indices and cell-local offsets.
func worldToLocal(worldX, worldY float32) (cellX, cellY int32, localX, localY float32) {
	cs := coords.CellSize
	cx := int32(worldX / cs)
	cy := int32(worldY / cs)
	if worldX < 0 && worldX != float32(cx)*cs {
		cx--
	}
	if worldY < 0 && worldY != float32(cy)*cs {
		cy--
	}
	return cx, cy, worldX - float32(cx)*cs, worldY - float32(cy)*cs
}

// localToWorld converts a cell-local coordinate to a world coordinate.
func localToWorld(cellX, cellY int32, localX, localY float32) (float32, float32) {
	cs := coords.CellSize
	return float32(cellX)*cs + localX, float32(cellY)*cs + localY
}

// localHostIDFor returns the host ID that owns the given cell in the Process.
// Returns empty string if not found.
func localHostIDFor(coord *Process, cell *Cell) string {
	for hostID, h := range coord.Hosts {
		h.mu.RLock()
		for _, hc := range h.Cells {
			if hc == cell {
				h.mu.RUnlock()
				return hostID
			}
		}
		h.mu.RUnlock()
	}
	return ""
}

// findCellOwningPos iterates coord.Cells to find the cell that owns the given
// world position via its Bridge. Returns (cell, cellID, ok).
func findCellOwningPos(coord *Process, worldX, worldY float32) (*Cell, string, bool) {
	coord.mu.RLock()
	cells := make([]*Cell, 0, len(coord.Cells))
	cellIDs := make([]string, 0, len(coord.Cells))
	for id, c := range coord.Cells {
		cells = append(cells, c)
		cellIDs = append(cellIDs, id)
	}
	coord.mu.RUnlock()

	for i, c := range cells {
		if c.Stage == nil {
			continue
		}
		ownerID := c.Stage.Bridge().CellOwnerAtPos(worldX, worldY)
		if ownerID == "" {
			continue
		}
		coord.mu.RLock()
		dest, ok := coord.Cells[ownerID]
		coord.mu.RUnlock()
		if ok {
			return dest, ownerID, true
		}
		// Bridge returned an ID that matches this cell's own ID.
		if ownerID == cellIDs[i] || ownerID == c.ID {
			return c, c.ID, true
		}
	}
	return nil, "", false
}

// findCellOwningNetID iterates coord.Cells to find the cell that has the
// given netID as a Live entity. Returns (cell, cellID, ok).
func findCellOwningNetID(coord *Process, netID uint32) (*Cell, string, bool) {
	coord.mu.RLock()
	cells := make([]*Cell, 0, len(coord.Cells))
	cellIDs := make([]string, 0, len(coord.Cells))
	for id, c := range coord.Cells {
		cells = append(cells, c)
		cellIDs = append(cellIDs, id)
	}
	coord.mu.RUnlock()

	for i, c := range cells {
		if c.Stage == nil {
			continue
		}
		_, pres, ok := c.Stage.LookupNetID(netID)
		if ok && pres == PresenceLive {
			return c, cellIDs[i], true
		}
	}
	return nil, "", false
}

// runOnCell schedules fn on the cell's game loop and returns its typed result.
// When no loop is active (e.g. tests, headless bootstrap) fn is called inline,
// matching the same gate used by the perf builtins.
func runOnCell[R any](ctx context.Context, cell *Cell, fn func() (R, error)) (R, error) {
	if cell.Engine.HasLoopRunning() {
		return cmdsys.OnLoop(ctx, cell.Engine, fn)
	}
	return fn()
}

// ── registerEntityCommands ───────────────────────────────────────────────────

// registerEntityCommands registers entity.spawn, entity.despawn, entity.list,
// and entity.tp on coord.registry. Called from Process.registerAllBuiltins.
func registerEntityCommands(coord *Process) error {
	reg := coord.registry
	if err := reg.Register(cmdsys.Command{
		Verb:        "entity.spawn",
		Capability:  "entity.spawn",
		Description: "spawn N entities of a registered kind at a world location",
		// RouteLocal: the handler resolves the destination cell from (x,y)
		// internally via Bridge().CellOwnerAtPos. RouteSpecificCell would
		// require a CellID arg in entitySpawnArgs; we accept world coords
		// instead and resolve once per call.
		Route:   cmdsys.RouteLocal,
		Args:    entitySpawnArgs{},
		Result:  entitySpawnResult{},
		Handler: entitySpawnHandler(coord),
	}); err != nil {
		return fmt.Errorf("entity.spawn: %w", err)
	}
	if err := reg.Register(cmdsys.Command{
		Verb:        "entity.despawn",
		Capability:  "entity.despawn",
		Description: "despawn an entity by network ID",
		Route:       cmdsys.RouteEntityOwner,
		Args:        entityDespawnArgs{},
		Result:      entityDespawnResult{},
		Handler:     entityDespawnHandler(coord),
	}); err != nil {
		return fmt.Errorf("entity.despawn: %w", err)
	}
	if err := reg.Register(cmdsys.Command{
		Verb:        "entity.list",
		Capability:  "entity.list",
		Description: "list live entities across all local cells",
		Route:       cmdsys.RouteAllHosts,
		Args:        entityListArgs{},
		Result:      entityListResult{},
		Handler:     entityListHandler(coord),
	}); err != nil {
		return fmt.Errorf("entity.list: %w", err)
	}
	if err := reg.Register(cmdsys.Command{
		Verb:        "entity.tp",
		Capability:  "entity.tp",
		Description: "teleport an entity to a world position",
		Route:       cmdsys.RouteEntityOwner,
		Args:        entityTpArgs{},
		Result:      entityTpResult{},
		Handler:     entityTpHandler(coord),
	}); err != nil {
		return fmt.Errorf("entity.tp: %w", err)
	}
	return nil
}

// ── handlers ─────────────────────────────────────────────────────────────────

func entitySpawnHandler(coord *Process) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(entitySpawnArgs)
		if args.Count <= 0 {
			return nil, fmt.Errorf("entity.spawn: count must be >= 1")
		}

		destCell, destCellID, ok := findCellOwningPos(coord, args.X, args.Y)
		if !ok {
			return nil, fmt.Errorf("entity.spawn: no cell owns world position (%g,%g)", args.X, args.Y)
		}

		kindID, found := findKindByName(destCell.Stage, args.Kind)
		if !found {
			return nil, fmt.Errorf("entity.spawn: kind %q not registered", args.Kind)
		}

		hostID := localHostIDFor(coord, destCell)
		cellX, cellY, _, _ := worldToLocal(args.X, args.Y)

		spawned, err := runOnCell(ctx, destCell, func() (int32, error) {
			rng := rand.New(rand.NewSource(int64(args.Count) * time.Now().UnixNano()))
			var n int32
			for range int(args.Count) {
				ex, ey := args.X, args.Y
				if args.Radius > 0 {
					theta := rng.Float64() * 2 * math.Pi
					r := math.Sqrt(rng.Float64()) * float64(args.Radius)
					ex += float32(math.Cos(theta) * r)
					ey += float32(math.Sin(theta) * r)
				}
				localX := ex - float32(cellX)*coords.CellSize
				localY := ey - float32(cellY)*coords.CellSize
				destCell.Stage.SpawnEntity(
					component.Position{X: localX, Y: localY},
					WithEntityKind(kindID),
				)
				n++
			}
			return n, nil
		})
		if err != nil {
			return nil, err
		}
		return entitySpawnResult{
			Kind:    args.Kind,
			Count:   args.Count,
			CellID:  destCellID,
			HostID:  hostID,
			Spawned: spawned,
		}, nil
	}
}

func entityDespawnHandler(coord *Process) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(entityDespawnArgs)

		ownerCell, ownerCellID, ok := findCellOwningNetID(coord, args.NetID)
		if !ok {
			return nil, fmt.Errorf("entity.despawn: netID %d not found on any local cell", args.NetID)
		}

		hostID := localHostIDFor(coord, ownerCell)

		result, err := runOnCell(ctx, ownerCell, func() (entityDespawnResult, error) {
			entity, pres, exists := ownerCell.Stage.LookupNetID(args.NetID)
			if !exists || pres != PresenceLive {
				return entityDespawnResult{}, fmt.Errorf("entity.despawn: netID %d not live in cell %s", args.NetID, ownerCellID)
			}

			// Determine kind name before removal.
			kindMap := ecs.NewMap1[component.EntityKind](ownerCell.Stage.ECSWorld())
			kindName := ""
			if kindMap.HasAll(entity) {
				k := kindMap.Get(entity)
				kindName = resolveKindName(ownerCell.Stage, k.Type)
			}

			ownerCell.Stage.MarkForRemoval(entity)
			return entityDespawnResult{
				NetID:  args.NetID,
				Kind:   kindName,
				CellID: ownerCellID,
				HostID: hostID,
			}, nil
		})
		return result, err
	}
}

func entityListHandler(coord *Process) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(entityListArgs)

		coord.mu.RLock()
		cells := make([]*Cell, 0, len(coord.Cells))
		cellIDs := make([]string, 0, len(coord.Cells))
		for id, c := range coord.Cells {
			cells = append(cells, c)
			cellIDs = append(cellIDs, id)
		}
		cellHost := make(map[*Cell]string, len(cells))
		for hostID, h := range coord.Hosts {
			h.mu.RLock()
			for _, hc := range h.Cells {
				cellHost[hc] = hostID
			}
			h.mu.RUnlock()
		}
		coord.mu.RUnlock()

		var allRows []entityRow
		for i, c := range cells {
			if c.Stage == nil || c.Engine == nil {
				continue
			}

			cellID := cellIDs[i]
			hostID := cellHost[c]
			cid := c.Stage.Cell()
			cX, cY := cid.X, cid.Y
			stage := c.Stage

			rows, err := runOnCell(ctx, c, func() ([]entityRow, error) {
				w := stage.ECSWorld()
				filter := ecs.NewFilter2[component.NetworkID, component.EntityKind](w)
				query := filter.Query()
				defer query.Close()

				var out []entityRow
				for query.Next() {
					nid, kind := query.Get()
					kindName := resolveKindName(stage, kind.Type)
					if args.Kind != "" && kindName != args.Kind {
						continue
					}
					// Only include live entities.
					_, pres, found := stage.LookupNetID(nid.ID)
					if !found || pres != PresenceLive {
						continue
					}

					entity := query.Entity()
					var worldX, worldY float32
					posMap := stage.PositionMap()
					if posMap.HasAll(entity) {
						pos := posMap.Get(entity)
						worldX, worldY = localToWorld(cX, cY, pos.X, pos.Y)
					}
					out = append(out, entityRow{
						NetID:  nid.ID,
						Kind:   kindName,
						WorldX: worldX,
						WorldY: worldY,
						CellID: cellID,
						HostID: hostID,
					})
				}
				return out, nil
			})
			if err != nil {
				return nil, err
			}
			allRows = append(allRows, rows...)
		}

		return entityListResult{Entities: allRows}, nil
	}
}

func entityTpHandler(coord *Process) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(entityTpArgs)

		ownerCell, _, ok := findCellOwningNetID(coord, args.NetID)
		if !ok {
			return nil, fmt.Errorf("entity.tp: netID %d not found on any local cell", args.NetID)
		}

		hostID := localHostIDFor(coord, ownerCell)

		result, err := runOnCell(ctx, ownerCell, func() (entityTpResult, error) {
			entity, pres, exists := ownerCell.Stage.LookupNetID(args.NetID)
			if !exists || pres != PresenceLive {
				return entityTpResult{}, fmt.Errorf("entity.tp: netID %d not live", args.NetID)
			}

			// Read current position.
			var prevWorldX, prevWorldY float32
			cid := ownerCell.Stage.Cell()
			posMap := ownerCell.Stage.PositionMap()
			if posMap.HasAll(entity) {
				pos := posMap.Get(entity)
				prevWorldX, prevWorldY = localToWorld(cid.X, cid.Y, pos.X, pos.Y)
			}

			// Determine kind name.
			kindMap := ecs.NewMap1[component.EntityKind](ownerCell.Stage.ECSWorld())
			kindName := ""
			if kindMap.HasAll(entity) {
				k := kindMap.Get(entity)
				kindName = resolveKindName(ownerCell.Stage, k.Type)
			}

			// Teleport — bypass handoff cooldown for an explicit tp.
			if err := ownerCell.Stage.MoveEntityTo(entity, args.X, args.Y, MoveBypassCooldown()); err != nil {
				return entityTpResult{}, fmt.Errorf("entity.tp: %w", err)
			}

			return entityTpResult{
				NetID:      args.NetID,
				Kind:       kindName,
				PrevWorldX: prevWorldX,
				PrevWorldY: prevWorldY,
				NewWorldX:  args.X,
				NewWorldY:  args.Y,
				HostID:     hostID,
			}, nil
		})
		return result, err
	}
}
