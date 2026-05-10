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
// an N×M grid derived from their X_Y IDs; split children render as nested
// 2×2 squares inside their parent rect.
//
// Cell IDs follow `<X>_<Y>` for depth 0 and `<X>_<Y>:1..4` for splits, where
// split index 1=NW, 2=NE, 3=SW, 4=SE. The same scheme nests recursively for
// deeper splits (`0_0:1:2` is the NE quadrant of the NW quadrant of 0_0).
//
// Layout returns one entry per input cell (parents AND children) so callers
// can choose to render only leaves, only parents, or both with translucency.
export function layoutCells(input: CellLayoutInput[], opts: LayoutOpts): Layout[] {
  const baseCells = input.filter((c) => c.depth === 0);
  let maxX = 0;
  let maxY = 0;
  for (const c of baseCells) {
    const [xs, ys] = c.id.split("_");
    maxX = Math.max(maxX, Number.parseInt(xs, 10));
    maxY = Math.max(maxY, Number.parseInt(ys, 10));
  }
  const cols = maxX + 1;
  const rows = maxY + 1;
  const cellW = (opts.width - 2 * opts.padding) / cols;
  const cellH = (opts.height - 2 * opts.padding) / rows;

  const out: Layout[] = [];
  const baseRect = new Map<string, Layout>();
  for (const c of baseCells) {
    const [xs, ys] = c.id.split("_");
    const xi = Number.parseInt(xs, 10);
    const yi = Number.parseInt(ys, 10);
    const rect: Layout = {
      id: c.id,
      x: opts.padding + xi * cellW,
      y: opts.padding + yi * cellH,
      w: cellW,
      h: cellH,
      depth: 0,
    };
    out.push(rect);
    baseRect.set(c.id, rect);
  }

  // Place split children. Order by depth ascending so each split's parent
  // is already placed by the time we descend.
  const splits = input.filter((c) => c.depth > 0).sort((a, b) => a.depth - b.depth);
  const placed = new Map<string, Layout>(baseRect);
  for (const c of splits) {
    const colonIdx = c.id.lastIndexOf(":");
    if (colonIdx < 0) continue;
    const parentId = c.id.slice(0, colonIdx);
    const slot = Number.parseInt(c.id.slice(colonIdx + 1), 10);
    const parent = placed.get(parentId);
    if (!parent) continue;
    const halfW = parent.w / 2;
    const halfH = parent.h / 2;
    let dx = 0;
    let dy = 0;
    switch (slot) {
      case 1: dx = 0; dy = 0; break;        // NW
      case 2: dx = halfW; dy = 0; break;     // NE
      case 3: dx = 0; dy = halfH; break;     // SW
      case 4: dx = halfW; dy = halfH; break; // SE
    }
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
