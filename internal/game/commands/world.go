package commands

import (
	"context"
	"encoding/json"
	"fmt"

	gamecomp "github.com/zenion/mmoserver/internal/component"
	"github.com/zenion/mmoserver/internal/game"
	"github.com/zenion/mmoserver/pkg/cmdsys"
	"github.com/zenion/mmoserver/pkg/coords"
	"github.com/zenion/mmoserver/pkg/mmokit"
)

// WorldChangeEvent is the SSE-payload published on topic "world.changed"
// after every successful world.* verb. The admin SPA subscribes to keep
// the editor canvas in sync with on-disk manifest mutations issued from
// console / other operators.
type WorldChangeEvent struct {
	Op     string  `json:"op"`   // "place" | "move" | "update" | "delete" | "reload"
	Type   string  `json:"type"` // station|poi|dungeon|belt|decoration
	ID     string  `json:"id"`
	WorldX float32 `json:"world_x,omitempty"`
	WorldY float32 `json:"world_y,omitempty"`
}

// ── world.list ─────────────────────────────────────────────────────────────

// WorldListArgs filters the manifest dump by type when non-empty. An
// empty Type returns every entity across every type.
type WorldListArgs struct {
	Type string `cmd:"optional,help=filter by entity type (station|poi|dungeon|belt|decoration|region)"`
}

// WorldListResult is the flat tabular dump of every placed entity from
// the in-memory snapshot. Detail is a per-type free-form column so the
// table renderer stays uniform across heterogeneous types.
type WorldListResult struct {
	Entities []WorldEntityRow `cmd:"table"`
}

// WorldEntityRow is one entity in the world-list table.
type WorldEntityRow struct {
	Type   string
	ID     string
	WorldX float32
	WorldY float32
	Detail string
}

func registerWorldList(reg *cmdsys.Registry, coord *mmokit.Process, we *worldEditor) error {
	return reg.Register(cmdsys.Command{
		Verb:        "world.list",
		Capability:  "world.edit",
		Description: "list every placed entity from world manifests, optionally filtered by type",
		Route:       cmdsys.RouteLocal,
		Args:        WorldListArgs{},
		Result:      WorldListResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(WorldListArgs)
			snap := we.getSnap()
			if snap == nil {
				return WorldListResult{}, nil
			}
			var rows []WorldEntityRow
			include := func(t string) bool { return args.Type == "" || args.Type == t }

			if include("station") && snap.Stations != nil {
				for _, s := range snap.Stations.Stations {
					rows = append(rows, WorldEntityRow{
						Type: "station", ID: s.ID,
						WorldX: s.WorldPos[0], WorldY: s.WorldPos[1],
						Detail: s.Name,
					})
				}
			}
			if include("poi") && snap.POIs != nil {
				for _, p := range snap.POIs.POIs {
					rows = append(rows, WorldEntityRow{
						Type: "poi", ID: p.ID,
						WorldX: p.WorldPos[0], WorldY: p.WorldPos[1],
						Detail: fmt.Sprintf("T%d %s", p.Tier, p.Roster),
					})
				}
			}
			if include("dungeon") && snap.Dungeons != nil {
				for _, d := range snap.Dungeons.Dungeons {
					rows = append(rows, WorldEntityRow{
						Type: "dungeon", ID: d.ID,
						WorldX: d.WorldPos[0], WorldY: d.WorldPos[1],
						Detail: d.Name,
					})
				}
			}
			if include("belt") && snap.Belts != nil {
				for _, b := range snap.Belts.Belts {
					rows = append(rows, WorldEntityRow{
						Type: "belt", ID: b.ID,
						WorldX: b.WorldPos[0], WorldY: b.WorldPos[1],
						Detail: fmt.Sprintf("r=%.0f d=%.2f", b.Radius, b.Density),
					})
				}
			}
			if include("decoration") && snap.Decorations != nil {
				for _, dc := range snap.Decorations.Decorations {
					rows = append(rows, WorldEntityRow{
						Type: "decoration", ID: dc.ID,
						WorldX: dc.WorldPos[0], WorldY: dc.WorldPos[1],
						Detail: dc.Kind + "/" + dc.Variant,
					})
				}
			}
			if include("region") && snap.Regions != nil {
				for _, rg := range snap.Regions.Regions {
					// Encode region geometry in Detail as JSON so the admin
					// SPA can render the polygon without a separate fetch.
					// WorldX/WorldY default to the first vertex (handy for
					// sorting/bucketing); rendering uses Vertices.
					var wx, wy float32
					if len(rg.Vertices) > 0 {
						wx, wy = rg.Vertices[0][0], rg.Vertices[0][1]
					}
					payload, _ := json.Marshal(struct {
						Name     string       `json:"name"`
						Kind     string       `json:"kind"`
						Shape    string       `json:"shape"`
						Vertices [][2]float32 `json:"vertices,omitempty"`
					}{
						Name:     rg.Name,
						Kind:     rg.Kind,
						Shape:    rg.Shape,
						Vertices: rg.Vertices,
					})
					rows = append(rows, WorldEntityRow{
						Type: "region", ID: rg.ID,
						WorldX: wx, WorldY: wy,
						Detail: string(payload),
					})
				}
			}
			return WorldListResult{Entities: rows}, nil
		},
	})
}

// ── world.place ────────────────────────────────────────────────────────────

// WorldPlaceArgs takes per-type fields with optional defaults. ID can
// be left empty and the handler auto-generates a stable id from the
// (type, cell, ordinal) tuple.
type WorldPlaceArgs struct {
	Type   string  `cmd:"help=station|poi|dungeon|belt|decoration|region"`
	WorldX float32 `cmd:"help=world-absolute X"`
	WorldY float32 `cmd:"help=world-absolute Y"`
	ID     string  `cmd:"optional,help=stable id (auto-generated when empty)"`

	// POI-only
	Tier   uint8  `cmd:"optional,help=POI: tier 1..3"`
	Roster string `cmd:"optional,help=POI: roster name"`

	// Station/Dungeon/Region
	Name string `cmd:"optional,help=Station/Dungeon/Region: display name"`

	// Belt + Station collider radius
	Radius  float32 `cmd:"optional,help=Belt: radius (also Station collider radius)"`
	Density float32 `cmd:"optional,help=Belt: asteroid density (default 1.0)"`

	// Decoration / Region kind
	Kind    string `cmd:"optional,help=Decoration: kind (e.g. wreck/beacon); Region: free-form kind tag (t1|t2|t3|safe|pvp|faction|...)"`
	Variant string `cmd:"optional,help=Decoration: variant"`

	// Region-only
	Shape    string `cmd:"optional,help=Region: polygon|annulus (default polygon)"`
	Vertices string `cmd:"optional,help=Region: JSON-encoded [[wx,wy], ...] polygon vertices"`
}

// WorldPlaceResult reports the assigned ID — useful when the caller
// left WorldPlaceArgs.ID empty and the handler auto-generated one.
type WorldPlaceResult struct {
	Type string
	ID   string
}

func registerWorldPlace(reg *cmdsys.Registry, coord *mmokit.Process, we *worldEditor, disp *cmdsys.Dispatcher) error {
	return reg.Register(cmdsys.Command{
		Verb:        "world.place",
		Capability:  "world.edit",
		Description: "place an entity into the world manifest and spawn it live",
		Route:       cmdsys.RouteLocal,
		Args:        WorldPlaceArgs{},
		Result:      WorldPlaceResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(WorldPlaceArgs)
			if we.repo == nil {
				return nil, fmt.Errorf("world editor: no repo wired")
			}

			wp := coords.FromFlat(float64(args.WorldX), float64(args.WorldY))
			cellID := mmokit.WorldCellID{X: wp.CellX, Y: wp.CellY}

			id := args.ID
			if id == "" {
				id = autoID(args.Type, cellID, we.getSnap())
			}

			switch args.Type {
			case "station":
				def := mmokit.WorldStation{
					ID: id, Name: args.Name,
					WorldPos: [2]float32{args.WorldX, args.WorldY},
					Radius:   args.Radius,
				}
				if err := we.repo.AddStation(def); err != nil {
					return nil, err
				}
			case "poi":
				if args.Tier < 1 || args.Tier > 3 {
					return nil, fmt.Errorf("tier must be 1..3, got %d", args.Tier)
				}
				if args.Roster == "" {
					return nil, fmt.Errorf("roster required for POI")
				}
				def := mmokit.WorldPOI{
					ID: id, WorldPos: [2]float32{args.WorldX, args.WorldY},
					Type: "combat", Tier: args.Tier, Roster: args.Roster,
				}
				if err := we.repo.AddPOI(def); err != nil {
					return nil, err
				}
			case "dungeon":
				def := mmokit.WorldDungeon{
					ID: id, Name: args.Name,
					WorldPos: [2]float32{args.WorldX, args.WorldY},
				}
				if err := we.repo.AddDungeon(def); err != nil {
					return nil, err
				}
			case "belt":
				if args.Radius <= 0 {
					return nil, fmt.Errorf("belt radius must be > 0")
				}
				density := args.Density
				if density == 0 {
					density = 1.0
				}
				def := mmokit.WorldBelt{
					ID: id, WorldPos: [2]float32{args.WorldX, args.WorldY},
					Radius: args.Radius, Density: density,
				}
				if err := we.repo.AddBelt(def); err != nil {
					return nil, err
				}
			case "decoration":
				def := mmokit.WorldDecoration{
					ID: id, WorldPos: [2]float32{args.WorldX, args.WorldY},
					Kind: args.Kind, Variant: args.Variant,
				}
				if err := we.repo.AddDecoration(def); err != nil {
					return nil, err
				}
			case "region":
				shape := args.Shape
				if shape == "" {
					shape = "polygon"
				}
				if shape != "polygon" {
					return nil, fmt.Errorf("region shape %q not yet supported (polygon only)", shape)
				}
				if args.Vertices == "" {
					return nil, fmt.Errorf("region: Vertices required")
				}
				var verts [][2]float32
				if err := json.Unmarshal([]byte(args.Vertices), &verts); err != nil {
					return nil, fmt.Errorf("region: Vertices must be JSON [[x,y], ...]: %w", err)
				}
				if len(verts) < 3 {
					return nil, fmt.Errorf("region polygon needs at least 3 vertices, got %d", len(verts))
				}
				def := mmokit.WorldRegion{
					ID:       id,
					Name:     args.Name,
					Kind:     args.Kind,
					Shape:    shape,
					Vertices: verts,
				}
				if err := we.repo.AddRegion(def); err != nil {
					return nil, err
				}
				// Regions are annotation-only — no ECS spawn.
			default:
				return nil, fmt.Errorf("unknown type %q", args.Type)
			}

			we.reload(coord)
			mmokit.PublishAdminTopic(coord, "world.changed", WorldChangeEvent{
				Op: "place", Type: args.Type, ID: id,
				WorldX: args.WorldX, WorldY: args.WorldY,
			})
			fanOutReload(ctx, env, disp, coord)
			return WorldPlaceResult{Type: args.Type, ID: id}, nil
		},
	})
}

// ── world.move ─────────────────────────────────────────────────────────────

// WorldMoveArgs identifies the entity to move + its new world coords.
// Regions don't have a single position — move them by re-placing.
type WorldMoveArgs struct {
	Type   string  `cmd:"help=station|poi|dungeon|belt|decoration (regions: re-place instead)"`
	ID     string  `cmd:"help=entity id"`
	WorldX float32 `cmd:"help=new world-absolute X"`
	WorldY float32 `cmd:"help=new world-absolute Y"`
}

type WorldMoveResult struct {
	Type string
	ID   string
}

func registerWorldMove(reg *cmdsys.Registry, coord *mmokit.Process, we *worldEditor, disp *cmdsys.Dispatcher) error {
	return reg.Register(cmdsys.Command{
		Verb:        "world.move",
		Capability:  "world.edit",
		Route:       cmdsys.RouteLocal,
		Description: "move a placed entity to new world coords",
		Args:        WorldMoveArgs{},
		Result:      WorldMoveResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(WorldMoveArgs)
			if we.repo == nil {
				return nil, fmt.Errorf("world editor: no repo wired")
			}
			if args.Type == "region" {
				return nil, fmt.Errorf("regions have no single position — delete + re-place to move")
			}

			// 1. Validate the entity exists in the snapshot.
			if _, ok := lookupWorldPos(we.getSnap(), args.Type, args.ID); !ok {
				return nil, fmt.Errorf("%s id %q not found", args.Type, args.ID)
			}

			// 2. Mutate the JSON.
			newWP := [2]float32{args.WorldX, args.WorldY}
			if err := mutateWorldPos(we.repo, args.Type, args.ID, newWP); err != nil {
				return nil, err
			}

			// 3. Refresh the in-memory snapshot on the coord. The fanned-out
			//    world.reload (below) handles the actual despawn + respawn on
			//    every host's ECS — including this process when it owns cells.
			we.reload(coord)

			mmokit.PublishAdminTopic(coord, "world.changed", WorldChangeEvent{
				Op: "move", Type: args.Type, ID: args.ID,
				WorldX: args.WorldX, WorldY: args.WorldY,
			})
			fanOutReload(ctx, env, disp, coord)
			return WorldMoveResult{Type: args.Type, ID: args.ID}, nil
		},
	})
}

// ── world.update ───────────────────────────────────────────────────────────

// WorldUpdateArgs accepts every optional per-type field. Zero values
// are interpreted as "don't change" — the handler only writes fields
// the caller explicitly set.
type WorldUpdateArgs struct {
	Type    string  `cmd:"help=station|poi|dungeon|belt|decoration|region"`
	ID      string  `cmd:"help=entity id"`
	Tier    uint8   `cmd:"optional,help=POI: new tier 1..3"`
	Roster  string  `cmd:"optional,help=POI: new roster name"`
	Name    string  `cmd:"optional,help=Station/Dungeon/Region: new name"`
	Radius  float32 `cmd:"optional,help=Belt: new radius (or Station collider radius)"`
	Density float32 `cmd:"optional,help=Belt: new density"`
	Kind    string  `cmd:"optional,help=Decoration/Region: new kind"`
	Variant string  `cmd:"optional,help=Decoration: new variant"`
}

type WorldUpdateResult struct {
	Type string
	ID   string
}

func registerWorldUpdate(reg *cmdsys.Registry, coord *mmokit.Process, we *worldEditor, disp *cmdsys.Dispatcher) error {
	return reg.Register(cmdsys.Command{
		Verb:        "world.update",
		Capability:  "world.edit",
		Route:       cmdsys.RouteLocal,
		Description: "update non-position props of a placed entity",
		Args:        WorldUpdateArgs{},
		Result:      WorldUpdateResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(WorldUpdateArgs)
			if we.repo == nil {
				return nil, fmt.Errorf("world editor: no repo wired")
			}

			// Mutate the manifest in place.
			switch args.Type {
			case "station":
				if err := we.repo.UpdateStation(args.ID, func(s *mmokit.WorldStation) {
					if args.Name != "" {
						s.Name = args.Name
					}
					if args.Radius != 0 {
						s.Radius = args.Radius
					}
				}); err != nil {
					return nil, err
				}
			case "poi":
				if err := we.repo.UpdatePOI(args.ID, func(p *mmokit.WorldPOI) {
					if args.Tier != 0 {
						p.Tier = args.Tier
					}
					if args.Roster != "" {
						p.Roster = args.Roster
					}
				}); err != nil {
					return nil, err
				}
			case "dungeon":
				if err := we.repo.UpdateDungeon(args.ID, func(d *mmokit.WorldDungeon) {
					if args.Name != "" {
						d.Name = args.Name
					}
				}); err != nil {
					return nil, err
				}
			case "belt":
				if err := we.repo.UpdateBelt(args.ID, func(b *mmokit.WorldBelt) {
					if args.Radius != 0 {
						b.Radius = args.Radius
					}
					if args.Density != 0 {
						b.Density = args.Density
					}
				}); err != nil {
					return nil, err
				}
			case "decoration":
				if err := we.repo.UpdateDecoration(args.ID, func(d *mmokit.WorldDecoration) {
					if args.Kind != "" {
						d.Kind = args.Kind
					}
					if args.Variant != "" {
						d.Variant = args.Variant
					}
				}); err != nil {
					return nil, err
				}
			case "region":
				if err := we.repo.UpdateRegion(args.ID, func(r *mmokit.WorldRegion) {
					if args.Name != "" {
						r.Name = args.Name
					}
					// Allow blanking the kind via the sentinel "-" — empty
					// kind is the default, so we can't tell "leave alone"
					// from "clear it" via the zero value alone. The admin
					// SPA always sends Kind=<current> on Apply, so this
					// path mostly just persists the current value.
					if args.Kind == "-" {
						r.Kind = ""
					} else if args.Kind != "" {
						r.Kind = args.Kind
					}
				}); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("unknown type %q", args.Type)
			}

			we.reload(coord)

			// Regions are annotation-only — skip the despawn/respawn cycle
			// that re-creates live ECS entities for the other types.
			if args.Type == "region" {
				mmokit.PublishAdminTopic(coord, "world.changed", WorldChangeEvent{
					Op: "update", Type: args.Type, ID: args.ID,
				})
				// Region updates carry no ECS side-effect, but fanning out
				// the reload still keeps every host's in-memory snapshot in
				// lockstep with the JSON file.
				fanOutReload(ctx, env, disp, coord)
				return WorldUpdateResult{Type: args.Type, ID: args.ID}, nil
			}

			// Look up the current world_pos (unchanged by this verb) so the
			// SSE event carries it for the editor UI. The fanned-out
			// world.reload below handles the actual despawn + respawn on
			// every host's ECS so the live entity reflects the new def fields
			// (roster, tier, radius, etc.).
			snap := we.getSnap()
			wp, _ := lookupWorldPos(snap, args.Type, args.ID)

			mmokit.PublishAdminTopic(coord, "world.changed", WorldChangeEvent{
				Op: "update", Type: args.Type, ID: args.ID,
				WorldX: wp[0], WorldY: wp[1],
			})
			fanOutReload(ctx, env, disp, coord)
			return WorldUpdateResult{Type: args.Type, ID: args.ID}, nil
		},
	})
}

// ── world.delete ───────────────────────────────────────────────────────────

type WorldDeleteArgs struct {
	Type string `cmd:"help=station|poi|dungeon|belt|decoration|region"`
	ID   string `cmd:"help=entity id"`
}

type WorldDeleteResult struct {
	Type string
	ID   string
}

func registerWorldDelete(reg *cmdsys.Registry, coord *mmokit.Process, we *worldEditor, disp *cmdsys.Dispatcher) error {
	return reg.Register(cmdsys.Command{
		Verb:        "world.delete",
		Capability:  "world.edit",
		Route:       cmdsys.RouteLocal,
		Description: "remove a placed entity from the manifest and despawn it",
		Args:        WorldDeleteArgs{},
		Result:      WorldDeleteResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, raw any) (any, error) {
			args := raw.(WorldDeleteArgs)
			if we.repo == nil {
				return nil, fmt.Errorf("world editor: no repo wired")
			}

			// Snapshot the position before mutating so the SSE payload
			// carries the now-vanishing entity's last known coords for the
			// editor UI. Regions don't have a single world_pos — skip the
			// lookup for them and rely on the repo delete to surface a
			// not-found error if applicable.
			var wp [2]float32
			if args.Type != "region" {
				var ok bool
				wp, ok = lookupWorldPos(we.getSnap(), args.Type, args.ID)
				if !ok {
					return nil, fmt.Errorf("%s id %q not found", args.Type, args.ID)
				}
			}

			switch args.Type {
			case "station":
				if err := we.repo.DeleteStation(args.ID); err != nil {
					return nil, err
				}
			case "poi":
				if err := we.repo.DeletePOI(args.ID); err != nil {
					return nil, err
				}
			case "dungeon":
				if err := we.repo.DeleteDungeon(args.ID); err != nil {
					return nil, err
				}
			case "belt":
				if err := we.repo.DeleteBelt(args.ID); err != nil {
					return nil, err
				}
			case "decoration":
				if err := we.repo.DeleteDecoration(args.ID); err != nil {
					return nil, err
				}
			case "region":
				if err := we.repo.DeleteRegion(args.ID); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("unknown type %q", args.Type)
			}

			we.reload(coord)

			mmokit.PublishAdminTopic(coord, "world.changed", WorldChangeEvent{
				Op: "delete", Type: args.Type, ID: args.ID,
				WorldX: wp[0], WorldY: wp[1],
			})
			fanOutReload(ctx, env, disp, coord)
			return WorldDeleteResult{Type: args.Type, ID: args.ID}, nil
		},
	})
}

// ── world.reload ───────────────────────────────────────────────────────────

type WorldReloadArgs struct{}

type WorldReloadResult struct {
	Cells int // number of cells that had their PlacedID set rebuilt
}

func registerWorldReload(reg *cmdsys.Registry, coord *mmokit.Process, we *worldEditor) error {
	return reg.Register(cmdsys.Command{
		Verb:        "world.reload",
		Capability:  "world.edit",
		Route:       cmdsys.RouteAllHosts,
		Description: "re-read world/*.json from disk; brute-force despawn + re-spawn every placed entity; fanned out to every host so distributed-mode edits propagate without a restart",
		Args:        WorldReloadArgs{}, Result: WorldReloadResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, _ any) (any, error) {
			if we.repo == nil {
				return nil, fmt.Errorf("world editor: no repo wired")
			}
			newSnap, err := we.repo.LoadAll()
			if err != nil {
				return nil, err
			}

			// Brute-force reload across every cell: despawn every entity
			// that carries a PlacedID, then re-spawn from the per-cell
			// bucket of the new snapshot. The cost is a brief glitch for
			// unchanged entities; diff-based reload is a v2 optimization.
			cells := 0
			for _, cell := range coord.Cells {
				if cell == nil || cell.Stage == nil || cell.Engine == nil {
					continue
				}
				_, err := mmokit.CmdOnLoop(ctx, cell.Engine, func() (struct{}, error) {
					gwLocal := mmokit.State[game.GameWorld](cell.Stage)
					if gwLocal == nil {
						return struct{}{}, nil
					}
					// 1. Despawn every PlacedID-tagged entity.
					var doomed []mmokit.Entity
					mmokit.ForEach1(cell.Stage, func(e mmokit.Entity, _ *gamecomp.PlacedID) {
						doomed = append(doomed, e)
					})
					for _, e := range doomed {
						cell.Stage.Commands().Despawn(e.Handle())
					}
					// 2. Re-spawn from the new snapshot's bucket for this cell.
					gwLocal.WorldSnapshot = newSnap
					bucket, ok := newSnap.BucketByCell()[mmokit.WorldCellID{
						X: gwLocal.RootCell.CellX, Y: gwLocal.RootCell.CellY,
					}]
					if ok && bucket != nil {
						for _, s := range bucket.Stations {
							gwLocal.SpawnStation(s.LocalPos[0], s.LocalPos[1], s.Def)
						}
						for _, p := range bucket.POIs {
							gwLocal.SpawnPOI(p.LocalPos[0], p.LocalPos[1], p.Def)
						}
						for _, d := range bucket.Dungeons {
							gwLocal.SpawnDungeonAt(d.LocalPos[0], d.LocalPos[1], d.Def)
						}
						for _, b := range bucket.Belts {
							gwLocal.SpawnBelt(b.LocalPos[0], b.LocalPos[1], b.Def)
						}
						for _, dc := range bucket.Decorations {
							gwLocal.SpawnDecoration(dc.LocalPos[0], dc.LocalPos[1], dc.Def)
						}
					}
					return struct{}{}, nil
				})
				if err != nil {
					return nil, err
				}
				cells++
			}
			// Install the freshly-loaded snapshot on the worldEditor so
			// subsequent world.list calls see the new manifest.
			we.mu.Lock()
			we.snap = newSnap
			we.mu.Unlock()
			mmokit.PublishAdminTopic(coord, "world.changed", WorldChangeEvent{Op: "reload"})
			return WorldReloadResult{Cells: cells}, nil
		},
	})
}

// ── world.export ───────────────────────────────────────────────────────────

type WorldExportArgs struct{}

type WorldExportResult struct {
	Path string
}

func registerWorldExport(reg *cmdsys.Registry, coord *mmokit.Process, we *worldEditor) error {
	return reg.Register(cmdsys.Command{
		Verb:        "world.export",
		Capability:  "world.edit",
		Route:       cmdsys.RouteLocal,
		Description: "write the in-memory world snapshot back to disk (safety net)",
		Args:        WorldExportArgs{},
		Result:      WorldExportResult{},
		Handler: func(ctx context.Context, env *cmdsys.Env, _ any) (any, error) {
			if we.repo == nil {
				return nil, fmt.Errorf("world editor: no repo wired")
			}
			snap := we.getSnap()
			if snap == nil {
				return WorldExportResult{}, nil
			}
			if snap.Stations != nil {
				if err := we.repo.SaveStations(snap.Stations); err != nil {
					return nil, err
				}
			}
			if snap.POIs != nil {
				if err := we.repo.SavePOIs(snap.POIs); err != nil {
					return nil, err
				}
			}
			if snap.Dungeons != nil {
				if err := we.repo.SaveDungeons(snap.Dungeons); err != nil {
					return nil, err
				}
			}
			if snap.Belts != nil {
				if err := we.repo.SaveBelts(snap.Belts); err != nil {
					return nil, err
				}
			}
			if snap.Decorations != nil {
				if err := we.repo.SaveDecorations(snap.Decorations); err != nil {
					return nil, err
				}
			}
			if snap.Regions != nil {
				if err := we.repo.SaveRegions(snap.Regions); err != nil {
					return nil, err
				}
			}
			return WorldExportResult{Path: "world/"}, nil
		},
	})
}

// ── helpers ────────────────────────────────────────────────────────────────

// fanOutReload invokes world.reload via the dispatcher's InvokeInternal so
// every host in the cluster re-reads the JSON from shared FS and rebuilds
// its local PlacedID-tagged ECS entities. This is the *only* signal remote
// hosts get that a coordinator-side world.* mutation happened — without it,
// edits stay dormant on remote hosts until a server restart.
//
// In single-process mode the coord IS a host, so the local cell(s) get the
// same reload — that's the correct behavior; world.reload's despawn-+-re-spawn
// is idempotent (everything PlacedID-tagged is wiped and re-seeded from the
// freshly-loaded snapshot bucket), so the double apply is cheap and correct.
//
// Errors are logged but never propagated: the JSON is already persisted to
// disk, and the next mutation's (or a manual) world.reload will catch any
// lagging host.
func fanOutReload(ctx context.Context, env *cmdsys.Env, disp *cmdsys.Dispatcher, coord *mmokit.Process) {
	if disp == nil || env == nil {
		return
	}
	if _, err := disp.InvokeInternal(ctx, env, "world.reload", WorldReloadArgs{}); err != nil {
		if coord != nil && coord.Log != nil {
			coord.Log.Log("world", "world.reload fan-out failed: %v (JSON already written; next reload will resync)", err)
		}
	}
}

// lookupWorldPos returns the manifest world_pos for (type, id) from the
// given snapshot, or false if not found.
func lookupWorldPos(snap *mmokit.WorldSnapshot, typ, id string) ([2]float32, bool) {
	if snap == nil {
		return [2]float32{}, false
	}
	switch typ {
	case "station":
		if snap.Stations != nil {
			for _, s := range snap.Stations.Stations {
				if s.ID == id {
					return s.WorldPos, true
				}
			}
		}
	case "poi":
		if snap.POIs != nil {
			for _, p := range snap.POIs.POIs {
				if p.ID == id {
					return p.WorldPos, true
				}
			}
		}
	case "dungeon":
		if snap.Dungeons != nil {
			for _, d := range snap.Dungeons.Dungeons {
				if d.ID == id {
					return d.WorldPos, true
				}
			}
		}
	case "belt":
		if snap.Belts != nil {
			for _, b := range snap.Belts.Belts {
				if b.ID == id {
					return b.WorldPos, true
				}
			}
		}
	case "decoration":
		if snap.Decorations != nil {
			for _, dc := range snap.Decorations.Decorations {
				if dc.ID == id {
					return dc.WorldPos, true
				}
			}
		}
	}
	return [2]float32{}, false
}

// mutateWorldPos updates the persisted world_pos for (type, id) in the
// repo. Returns whatever error the repo surfaces (typically not-found).
func mutateWorldPos(repo mmokit.WorldRepository, typ, id string, newWP [2]float32) error {
	switch typ {
	case "station":
		return repo.UpdateStation(id, func(s *mmokit.WorldStation) { s.WorldPos = newWP })
	case "poi":
		return repo.UpdatePOI(id, func(p *mmokit.WorldPOI) { p.WorldPos = newWP })
	case "dungeon":
		return repo.UpdateDungeon(id, func(d *mmokit.WorldDungeon) { d.WorldPos = newWP })
	case "belt":
		return repo.UpdateBelt(id, func(b *mmokit.WorldBelt) { b.WorldPos = newWP })
	case "decoration":
		return repo.UpdateDecoration(id, func(d *mmokit.WorldDecoration) { d.WorldPos = newWP })
	}
	return fmt.Errorf("unknown type %q", typ)
}

// autoID generates a stable id for a newly-placed entity from the
// (cell, type, ordinal) tuple. Probes the existing snapshot to avoid
// collisions.
func autoID(typ string, c mmokit.WorldCellID, snap *mmokit.WorldSnapshot) string {
	seen := map[string]bool{}
	if snap != nil {
		switch typ {
		case "station":
			if snap.Stations != nil {
				for _, x := range snap.Stations.Stations {
					seen[x.ID] = true
				}
			}
		case "poi":
			if snap.POIs != nil {
				for _, x := range snap.POIs.POIs {
					seen[x.ID] = true
				}
			}
		case "dungeon":
			if snap.Dungeons != nil {
				for _, x := range snap.Dungeons.Dungeons {
					seen[x.ID] = true
				}
			}
		case "belt":
			if snap.Belts != nil {
				for _, x := range snap.Belts.Belts {
					seen[x.ID] = true
				}
			}
		case "decoration":
			if snap.Decorations != nil {
				for _, x := range snap.Decorations.Decorations {
					seen[x.ID] = true
				}
			}
		case "region":
			if snap.Regions != nil {
				for _, x := range snap.Regions.Regions {
					seen[x.ID] = true
				}
			}
		}
	}
	for n := 1; ; n++ {
		id := fmt.Sprintf("%d_%d_%s_%d", c.X, c.Y, typ, n)
		if !seen[id] {
			return id
		}
	}
}
