// World-editor reactive store. One module-level singleton — every component
// (canvas, palette, inspector) reads & writes the same state.
//
// Data flow:
//   1. `refresh()` POSTs world.list → typed snapshot.
//   2. `subscribeLive()` opens an SSE subscription on `world.changed`;
//      every event triggers another refresh. Coarse-grained but cheap.
//   3. UI mutations (place/move/update/del) POST cmdsys verbs and rely on
//      the SSE round-trip to update the snapshot.

import { apiPost } from "./api";
import { stream } from "./stream";

type Pos2 = [number, number];

export type Tool =
  | "select"
  | "station"
  | "poi"
  | "dungeon"
  | "belt"
  | "region"
  | "decoration";

export interface WorldStation {
  id: string;
  name: string;
  world_pos: Pos2;
  radius?: number;
}

export interface WorldPOI {
  id: string;
  world_pos: Pos2;
  type: string;
  tier: number;
  roster: string;
  spread_radius?: number;
}

export interface WorldDungeon {
  id: string;
  name: string;
  world_pos: Pos2;
  seed?: number;
}

export interface WorldBelt {
  id: string;
  world_pos: Pos2;
  radius: number;
  density: number;
}

export interface WorldDecoration {
  id: string;
  world_pos: Pos2;
  kind: string;
  variant?: string;
}

export interface WorldRegion {
  id: string;
  name: string;
  kind: string;
  shape: string; // "polygon" | "annulus"
  vertices: Pos2[]; // for polygon shape
}

export interface Snapshot {
  stations: WorldStation[];
  pois: WorldPOI[];
  dungeons: WorldDungeon[];
  belts: WorldBelt[];
  decorations: WorldDecoration[];
  regions: WorldRegion[];
}

export interface LayerFlags {
  cells: boolean;
  grid: boolean;
  stations: boolean;
  pois: boolean;
  dungeons: boolean;
  belts: boolean;
  regions: boolean;
  decorations: boolean;
}

// world.list returns the table rows shape from internal/game/commands/world.go.
interface WorldEntityRow {
  Type: string;
  ID: string;
  WorldX: number;
  WorldY: number;
  Detail: string;
}

// The admin /commands/<verb> endpoint wraps the handler's typed Result as
// {ok, result, traceId}. world.list's Result is {Entities: [...]}.
interface CmdInvokeResp<T> {
  ok: boolean;
  result?: T;
  traceId?: string;
}

interface WorldListResult {
  Entities: WorldEntityRow[] | null;
}

class WorldStore {
  snapshot = $state<Snapshot>({
    stations: [],
    pois: [],
    dungeons: [],
    belts: [],
    decorations: [],
    regions: [],
  });
  selectedID = $state<string | null>(null);
  selectedType = $state<string | null>(null);
  tool = $state<Tool>("select");
  cursor = $state<Pos2>([0, 0]);
  // start zoomed-out: at 0.05 px/world-unit a single 8192u cell is ~410px wide.
  zoom = $state(0.05);
  pan = $state<Pos2>([0, 0]);
  layers = $state<LayerFlags>({
    cells: true,
    grid: true,
    stations: true,
    pois: true,
    dungeons: true,
    belts: true,
    regions: true,
    decorations: true,
  });

  async refresh(): Promise<void> {
    const res = await apiPost<CmdInvokeResp<WorldListResult>>(
      "/admin/api/commands/world.list",
      {},
    );
    const entities = res?.result?.Entities ?? [];

    const stations: WorldStation[] = [];
    const pois: WorldPOI[] = [];
    const dungeons: WorldDungeon[] = [];
    const belts: WorldBelt[] = [];
    const decorations: WorldDecoration[] = [];
    const regions: WorldRegion[] = [];

    for (const e of entities) {
      const wp: Pos2 = [e.WorldX, e.WorldY];
      switch (e.Type) {
        case "station":
          stations.push({ id: e.ID, name: e.Detail ?? "", world_pos: wp });
          break;
        case "poi": {
          // Detail format from registerWorldList: "T<tier> <roster>"
          const m = /^T(\d)\s+(.*)$/.exec(e.Detail ?? "");
          pois.push({
            id: e.ID,
            world_pos: wp,
            type: "combat",
            tier: m ? Number.parseInt(m[1], 10) : 1,
            roster: m ? m[2] : "StarterArena",
          });
          break;
        }
        case "dungeon":
          dungeons.push({ id: e.ID, name: e.Detail ?? "", world_pos: wp });
          break;
        case "belt": {
          // Detail format: "r=<radius> d=<density>"
          const r = Number.parseFloat(
            /r=([\d.]+)/.exec(e.Detail ?? "")?.[1] ?? "80",
          );
          const d = Number.parseFloat(
            /d=([\d.]+)/.exec(e.Detail ?? "")?.[1] ?? "1",
          );
          belts.push({ id: e.ID, world_pos: wp, radius: r, density: d });
          break;
        }
        case "decoration": {
          const [kind, variant] = (e.Detail ?? "").split("/");
          decorations.push({
            id: e.ID,
            world_pos: wp,
            kind: kind ?? "",
            variant: variant ?? "",
          });
          break;
        }
        case "region": {
          // Detail is a JSON blob: {"name":..., "kind":..., "shape":...,
          // "vertices":[[wx,wy], ...]}. See registerWorldList in
          // internal/game/commands/world.go.
          let parsed: {
            name?: string;
            kind?: string;
            shape?: string;
            vertices?: Pos2[];
          } = {};
          try {
            parsed = JSON.parse(e.Detail ?? "{}");
          } catch {
            // tolerate malformed; treat as an empty region
          }
          regions.push({
            id: e.ID,
            name: parsed.name ?? "",
            kind: parsed.kind ?? "",
            shape: parsed.shape ?? "polygon",
            vertices: Array.isArray(parsed.vertices) ? parsed.vertices : [],
          });
          break;
        }
        default:
          break;
      }
    }

    this.snapshot = { stations, pois, dungeons, belts, decorations, regions };
  }

  subscribeLive(): () => void {
    return stream.subscribe("world.changed", () => {
      void this.refresh();
    });
  }

  selected(): {
    type: string;
    entity:
      | WorldStation
      | WorldPOI
      | WorldDungeon
      | WorldBelt
      | WorldDecoration
      | WorldRegion;
  } | null {
    if (!this.selectedID || !this.selectedType) return null;
    const t = this.selectedType;
    const id = this.selectedID;
    const lookup: Record<
      string,
      (
        | WorldStation
        | WorldPOI
        | WorldDungeon
        | WorldBelt
        | WorldDecoration
        | WorldRegion
      )[]
    > = {
      station: this.snapshot.stations,
      poi: this.snapshot.pois,
      dungeon: this.snapshot.dungeons,
      belt: this.snapshot.belts,
      decoration: this.snapshot.decorations,
      region: this.snapshot.regions,
    };
    const list = lookup[t];
    if (!list) return null;
    const found = list.find((e) => e.id === id);
    return found ? { type: t, entity: found } : null;
  }

  setSelection(type: string | null, id: string | null): void {
    this.selectedType = type;
    this.selectedID = id;
  }

  clearSelection(): void {
    this.selectedID = null;
    this.selectedType = null;
  }

  async place(
    type: string,
    worldX: number,
    worldY: number,
    extra: Record<string, unknown> = {},
  ): Promise<void> {
    await apiPost("/admin/api/commands/world.place", {
      Type: type,
      WorldX: worldX,
      WorldY: worldY,
      ...extra,
    });
  }

  async move(
    type: string,
    id: string,
    worldX: number,
    worldY: number,
  ): Promise<void> {
    await apiPost("/admin/api/commands/world.move", {
      Type: type,
      ID: id,
      WorldX: worldX,
      WorldY: worldY,
    });
  }

  async update(
    type: string,
    id: string,
    patch: Record<string, unknown>,
  ): Promise<void> {
    await apiPost("/admin/api/commands/world.update", {
      Type: type,
      ID: id,
      ...patch,
    });
  }

  async del(type: string, id: string): Promise<void> {
    await apiPost("/admin/api/commands/world.delete", {
      Type: type,
      ID: id,
    });
  }
}

export const worldStore = new WorldStore();

// World constants (mirror pkg/coords/coords.go::CellSize).
export const CELL_SIZE = 8192;

// Known roster names from internal/game/poi_config.go.
export const ROSTERS = [
  "StarterArena",
  "SmallSkirmish",
  "MediumWarband",
  "DisruptorCell",
  "HeavyBattalion",
  "EliteAnchor",
] as const;

// Default lavender region color (matches the Region tool glyph in
// WorldPalette). Returned for unknown kinds and the empty kind.
export const DEFAULT_REGION_COLOR = "rgb(180, 158, 250)";

// regionColor returns the canvas color for a region by its `kind`
// freeform tag. The lookup is shared between the canvas renderer and any
// future legend / palette that wants the same palette.
export function regionColor(kind: string): string {
  switch (kind) {
    case "t1":
      return "rgb(251, 191, 36)"; // amber
    case "t2":
      return "rgb(251, 146, 60)"; // orange
    case "t3":
      return "rgb(251, 113, 133)"; // rose
    case "safe":
      return "rgb(74, 222, 128)"; // green
    case "pvp":
      return "rgb(239, 68, 68)"; // red
    case "faction":
      return "rgb(34, 211, 238)"; // cyan
    default:
      return DEFAULT_REGION_COLOR;
  }
}
