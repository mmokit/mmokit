import { parseCellID, meshID, parentID, type CellCoord } from "$lib/cellid";

export type CellLayoutInput = {
  id: string;
  depth: number;
  parent?: string;
};

export type Layout = {
  id: string;
  x: number;
  y: number;
  w: number;
  h: number;
  depth: number;
};

export type LayoutOpts = {
  width: number;
  height: number;
  padding: number; // outer padding in pixels
};

// layoutCells lays cells out as a quadtree-aware 2D grid. Depth-0 cells form
// an N×M grid derived from their X/Y coords; deeper cells nest as 2×2
// quadrants inside their parent rect, recursively, derived from each cell's
// X/Y mod 2 (X mod 2 == 0 → west, == 1 → east; same for Y → north/south).
//
// Each input must have its `depth` field set correctly by the producer (the
// admin DTO's `cellInfoFromSnapshot` parses the depth from the ID format).
// `parent` is optional — when present it's used as the placement key; when
// absent the helper computes the parent meshID itself via parentID().
//
// Layout returns one entry per input cell (parents AND children) so callers
// can choose to render only leaves, only parents, or both with translucency.

// parseXY is kept as a thin compat shim — historical callers (and tests)
// expect a {x,y} return. New code should use parseCellID directly so depth
// is preserved.
export function parseXY(id: string): { x: number; y: number } | null {
  const c = parseCellID(id);
  return c ? { x: c.x, y: c.y } : null;
}

function parentIDFor(c: CellLayoutInput): string | null {
  if (c.parent) return c.parent;
  const coord = parseCellID(c.id);
  if (!coord) return null;
  return parentID(coord);
}

export function layoutCells(input: CellLayoutInput[], opts: LayoutOpts): Layout[] {
  const baseCells = input.filter((c) => c.depth === 0);
  let maxX = 0;
  let maxY = 0;
  for (const c of baseCells) {
    const xy = parseCellID(c.id);
    if (!xy) continue;
    maxX = Math.max(maxX, xy.x);
    maxY = Math.max(maxY, xy.y);
  }
  const cols = maxX + 1;
  const rows = maxY + 1;
  const cellW = (opts.width - 2 * opts.padding) / cols;
  const cellH = (opts.height - 2 * opts.padding) / rows;

  const out: Layout[] = [];
  const placed = new Map<string, Layout>();

  for (const c of baseCells) {
    const xy = parseCellID(c.id);
    if (!xy) continue;
    const rect: Layout = {
      id: c.id,
      x: opts.padding + xy.x * cellW,
      y: opts.padding + xy.y * cellH,
      w: cellW,
      h: cellH,
      depth: 0,
    };
    out.push(rect);
    placed.set(c.id, rect);
  }

  // Synthesize missing ancestor cells for any deep child whose chain of
  // parents isn't fully present in the input. The synthesized parents are
  // NOT pushed to `out` (callers only render real cells), but they ARE
  // placed in `placed` so the recursive nesting math can chain through them.
  // This handles the post-split case where the parent was removed from the
  // backend's cells map but the children need a placement anchor.
  const splits = input.filter((c) => c.depth > 0).sort((a, b) => a.depth - b.depth);
  for (const c of splits) {
    const xy = parseCellID(c.id);
    if (!xy) continue;
    const parentKey = parentIDFor(c);
    if (!parentKey) continue;
    let parent: Layout | null | undefined = placed.get(parentKey);
    if (!parent) {
      // Parent missing — synthesize from the depth-0 grid by walking up.
      parent = synthesizeAncestor(parentKey, c.depth - 1, opts, cellW, cellH, placed);
      if (!parent) continue;
    }
    const halfW = parent.w / 2;
    const halfH = parent.h / 2;
    const dx = (xy.x % 2) * halfW;
    const dy = (xy.y % 2) * halfH;
    const rect: Layout = {
      id: c.id,
      x: parent.x + dx,
      y: parent.y + dy,
      w: halfW,
      h: halfH,
      depth: c.depth,
    };
    out.push(rect);
    placed.set(c.id, rect);
  }

  return out;
}

// synthesizeAncestor walks up from the given (id, depth) until it hits a
// placed cell, then walks back down placing intermediate parents. Used when
// a split cell exists in the input but the original depth-0 parent has been
// removed from the cells map (post-split: the parent is replaced by its
// children).
function synthesizeAncestor(
  id: string,
  depth: number,
  opts: LayoutOpts,
  cellW: number,
  cellH: number,
  placed: Map<string, Layout>,
): Layout | null {
  if (placed.has(id)) return placed.get(id)!;
  const xy = parseCellID(id);
  if (!xy) return null;

  if (depth === 0) {
    // Couldn't find a depth-0 parent — synthesize from the grid math.
    const rect: Layout = {
      id,
      x: opts.padding + xy.x * cellW,
      y: opts.padding + xy.y * cellH,
      w: cellW,
      h: cellH,
      depth: 0,
    };
    placed.set(id, rect);
    return rect;
  }

  const parentCoord: CellCoord = { x: Math.floor(xy.x / 2), y: Math.floor(xy.y / 2), depth: depth - 1 };
  const parent = synthesizeAncestor(meshID(parentCoord), depth - 1, opts, cellW, cellH, placed);
  if (!parent) return null;

  const halfW = parent.w / 2;
  const halfH = parent.h / 2;
  const rect: Layout = {
    id,
    x: parent.x + (xy.x % 2) * halfW,
    y: parent.y + (xy.y % 2) * halfH,
    w: halfW,
    h: halfH,
    depth,
  };
  placed.set(id, rect);
  return rect;
}
