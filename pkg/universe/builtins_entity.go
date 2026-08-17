package universe

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"time"

	"github.com/mlange-42/ark/ecs"
	"github.com/mmokit/mmokit/pkg/cmdsys"
	"github.com/mmokit/mmokit/pkg/component"
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

// ── entity.inspect ───────────────────────────────────────────────────────────

type entityInspectArgs struct {
	NetID uint32 `cmd:"help=entity network ID"`
}

type entityInspectRow struct {
	Component string
	Field     string // dotted path WITHIN the component (maps to entity.modify)
	Type      string
	Value     string
	Editable  bool
}

type entityInspectResult struct {
	NetID      uint32
	Kind       string
	Components []entityInspectRow `cmd:"table"`
}

// ── entity.modify ────────────────────────────────────────────────────────────

type entityModifyArgs struct {
	NetID     uint32 `cmd:"help=entity network ID"`
	Component string `cmd:"help=component name, e.g. Health"`
	Field     string `cmd:"help=dotted field path within the component, e.g. Current"`
	Value     string `cmd:"help=new value (coerced to the field's type)"`
}

type entityModifyResult struct {
	NetID     uint32
	Component string
	Field     string
	Old       string
	New       string
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

// ── entity.summary ───────────────────────────────────────────────────────────

type entitySummaryArgs struct{}

type entitySummaryRow struct {
	Kind  string
	Count int32
}

type entitySummaryResult struct {
	Rows  []entitySummaryRow `cmd:"table"`
	Total int32
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
func worldToLocal(worldX, worldY, cs float32) (cellX, cellY int32, localX, localY float32) {
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
func localToWorld(cellX, cellY int32, localX, localY, cs float32) (float32, float32) {
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
		cellIDs = append(cellIDs, string(id))
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
		dest, ok := coord.Cells[MeshCellID(ownerID)]
		coord.mu.RUnlock()
		if ok {
			return dest, ownerID, true
		}
		// Bridge returned an ID that matches this cell's own ID.
		if ownerID == cellIDs[i] || ownerID == string(c.MeshID()) {
			return c, string(c.MeshID()), true
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
		cellIDs = append(cellIDs, string(id))
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
	if err := reg.Register(cmdsys.Command{
		Verb:        "entity.summary",
		Capability:  "entity.summary",
		Description: "count live entities by kind across the cluster",
		Route:       cmdsys.RouteAllHosts,
		Args:        entitySummaryArgs{},
		Result:      entitySummaryResult{},
		Handler:     entitySummaryHandler(coord),
	}); err != nil {
		return fmt.Errorf("entity.summary: %w", err)
	}
	if err := reg.Register(cmdsys.Command{
		Verb:        "entity.inspect",
		Capability:  "entity.inspect",
		Description: "list an entity's components and field values by network ID",
		Route:       cmdsys.RouteEntityOwner,
		Args:        entityInspectArgs{},
		Result:      entityInspectResult{},
		Handler:     entityInspectHandler(coord),
	}); err != nil {
		return fmt.Errorf("entity.inspect: %w", err)
	}
	if err := reg.Register(cmdsys.Command{
		Verb:        "entity.modify",
		Capability:  "entity.modify",
		Description: "set a scalar field on a component of a live entity by network ID",
		Route:       cmdsys.RouteEntityOwner,
		Args:        entityModifyArgs{},
		Result:      entityModifyResult{},
		Handler:     entityModifyHandler(coord),
	}); err != nil {
		return fmt.Errorf("entity.modify: %w", err)
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
		cellX, cellY, _, _ := worldToLocal(args.X, args.Y, coord.CellSize())

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
				cs := coord.CellSize()
				localX := ex - float32(cellX)*cs
				localY := ey - float32(cellY)*cs
				destCell.Stage.Spawn(
					component.Position{X: localX, Y: localY},
					component.EntityKind{Type: kindID},
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

func entityInspectHandler(coord *Process) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(entityInspectArgs)

		ownerCell, ownerCellID, ok := findCellOwningNetID(coord, args.NetID)
		if !ok {
			return nil, fmt.Errorf("entity.inspect: netID %d not found on any local cell", args.NetID)
		}

		return runOnCell(ctx, ownerCell, func() (entityInspectResult, error) {
			entity, pres, exists := ownerCell.Stage.LookupNetID(args.NetID)
			if !exists || pres != PresenceLive {
				return entityInspectResult{}, fmt.Errorf("entity.inspect: netID %d not live in cell %s", args.NetID, ownerCellID)
			}

			kindMap := ecs.NewMap1[component.EntityKind](ownerCell.Stage.ECSWorld())
			if !kindMap.HasAll(entity) {
				return entityInspectResult{}, fmt.Errorf("entity.inspect: netID %d has no EntityKind", args.NetID)
			}
			kindType := kindMap.Get(entity).Type
			def, ok := ownerCell.Stage.EntityKindDefs()[kindType]
			if !ok {
				return entityInspectResult{}, fmt.Errorf("entity.inspect: kind %d not registered", kindType)
			}

			var rows []entityInspectRow
			for _, acc := range def.ComponentAccessors() {
				comp, present := acc.Get(entity)
				if !present {
					continue
				}
				for _, fi := range ListFields(comp) {
					rows = append(rows, entityInspectRow{
						Component: acc.Name,
						Field:     fi.Path,
						Type:      fi.Type,
						Value:     fi.Value,
						Editable:  fi.Editable,
					})
				}
			}
			return entityInspectResult{
				NetID:      args.NetID,
				Kind:       resolveKindName(ownerCell.Stage, kindType),
				Components: rows,
			}, nil
		})
	}
}

func entityModifyHandler(coord *Process) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		args := raw.(entityModifyArgs)

		ownerCell, ownerCellID, ok := findCellOwningNetID(coord, args.NetID)
		if !ok {
			return nil, fmt.Errorf("entity.modify: netID %d not found on any local cell", args.NetID)
		}

		return runOnCell(ctx, ownerCell, func() (entityModifyResult, error) {
			entity, pres, exists := ownerCell.Stage.LookupNetID(args.NetID)
			if !exists || pres != PresenceLive {
				return entityModifyResult{}, fmt.Errorf("entity.modify: netID %d not live in cell %s", args.NetID, ownerCellID)
			}

			kindMap := ecs.NewMap1[component.EntityKind](ownerCell.Stage.ECSWorld())
			if !kindMap.HasAll(entity) {
				return entityModifyResult{}, fmt.Errorf("entity.modify: netID %d has no EntityKind", args.NetID)
			}
			kindType := kindMap.Get(entity).Type
			def, ok := ownerCell.Stage.EntityKindDefs()[kindType]
			if !ok {
				return entityModifyResult{}, fmt.Errorf("entity.modify: kind %d not registered", kindType)
			}

			var comp any
			var found bool
			var available []string
			for _, acc := range def.ComponentAccessors() {
				available = append(available, acc.Name)
				if acc.Name == args.Component {
					c, present := acc.Get(entity)
					if !present {
						return entityModifyResult{}, fmt.Errorf("entity.modify: entity lacks component %q", args.Component)
					}
					comp, found = c, true
					break
				}
			}
			if !found {
				return entityModifyResult{}, fmt.Errorf("entity.modify: unknown component %q (available: %v)", args.Component, available)
			}

			oldStr, newStr, err := SetFieldByPath(comp, args.Field, args.Value)
			if err != nil {
				return entityModifyResult{}, fmt.Errorf("entity.modify: %w", err)
			}
			return entityModifyResult{
				NetID:     args.NetID,
				Component: args.Component,
				Field:     args.Field,
				Old:       oldStr,
				New:       newStr,
			}, nil
		})
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
			cellIDs = append(cellIDs, string(id))
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
				filter := ecs.NewFilter1[component.NetworkID](w)
				query := filter.Query()
				defer query.Close()

				kindMap := stage.EntityKindMap()
				posMap := stage.PositionMap()

				var out []entityRow
				for query.Next() {
					nid := query.Get()
					entity := query.Entity()

					var kindName string
					if kindMap.HasAll(entity) {
						kindName = resolveKindName(stage, kindMap.Get(entity).Type)
					}
					if args.Kind != "" && kindName != args.Kind {
						continue
					}
					// Only include live entities.
					_, pres, found := stage.LookupNetID(nid.ID)
					if !found || pres != PresenceLive {
						continue
					}

					var worldX, worldY float32
					if posMap.HasAll(entity) {
						pos := posMap.Get(entity)
						worldX, worldY = localToWorld(cX, cY, pos.X, pos.Y, coord.CellSize())
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
				prevWorldX, prevWorldY = localToWorld(cid.X, cid.Y, pos.X, pos.Y, coord.CellSize())
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

func entitySummaryHandler(coord *Process) cmdsys.HandlerFunc {
	return func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
		coord.mu.RLock()
		cells := make([]*Cell, 0, len(coord.Cells))
		for _, c := range coord.Cells {
			cells = append(cells, c)
		}
		coord.mu.RUnlock()

		counts := make(map[string]int32)
		var total int32
		for _, c := range cells {
			if c.Stage == nil || c.Engine == nil {
				continue
			}
			stage := c.Stage
			cellCounts, err := runOnCell(ctx, c, func() (map[string]int32, error) {
				w := stage.ECSWorld()
				filter := ecs.NewFilter1[component.NetworkID](w)
				query := filter.Query()
				defer query.Close()
				kindMap := stage.EntityKindMap()
				out := make(map[string]int32)
				for query.Next() {
					nid := query.Get()
					_, pres, found := stage.LookupNetID(nid.ID)
					if !found || pres != PresenceLive {
						continue
					}
					entity := query.Entity()
					var kindName string
					if kindMap.HasAll(entity) {
						kindName = resolveKindName(stage, kindMap.Get(entity).Type)
					}
					out[kindName]++
				}
				return out, nil
			})
			if err != nil {
				return nil, err
			}
			for k, n := range cellCounts {
				counts[k] += n
				total += n
			}
		}
		rows := make([]entitySummaryRow, 0, len(counts))
		for k, n := range counts {
			rows = append(rows, entitySummaryRow{Kind: k, Count: n})
		}
		return entitySummaryResult{Rows: rows, Total: total}, nil
	}
}
