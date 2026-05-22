<script lang="ts">
  import { onMount } from "svelte";
  import {
    worldStore,
    CELL_SIZE,
    regionColor,
    DEFAULT_REGION_COLOR,
    type Tool,
  } from "$lib/world-store.svelte";
  import { cellsStore } from "$lib/stores.svelte";
  import { stream } from "$lib/stream";
  import { apiGet } from "$lib/api";
  import { parseCellID } from "$lib/cellid";
  import type { CellInfo } from "$lib/types";

  let canvas: HTMLCanvasElement;
  let container: HTMLDivElement;

  // Cluster bounds derived from the live cells SSE topic. The world is
  // infinite in principle (int32 cell indices), but the running cluster
  // covers a finite rectangle of cells. We use these bounds to clip cell
  // boundary drawing so the editor doesn't suggest cells exist that don't.
  let clusterBounds = $state<{ minX: number; maxX: number; minY: number; maxY: number } | null>(null);

  function recomputeBoundsFrom(cells: CellInfo[]): void {
    if (!cells || cells.length === 0) {
      clusterBounds = null;
      return;
    }
    let minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity;
    for (const c of cells) {
      const xy = parseCellID(c.id);
      if (!xy) continue;
      // Splits sit inside their depth-0 parent; collapse the child XY
      // back into the parent's grid coordinate for bounding-box purposes
      // so post-split cells don't expand the cluster rectangle.
      const baseX = xy.depth === 0 ? xy.x : Math.floor(xy.x / (1 << xy.depth));
      const baseY = xy.depth === 0 ? xy.y : Math.floor(xy.y / (1 << xy.depth));
      if (baseX < minX) minX = baseX;
      if (baseX > maxX) maxX = baseX;
      if (baseY < minY) minY = baseY;
      if (baseY > maxY) maxY = baseY;
    }
    if (!isFinite(minX)) {
      clusterBounds = null;
      return;
    }
    clusterBounds = { minX, maxX, minY, maxY };
  }

  $effect(() => {
    recomputeBoundsFrom(cellsStore.value ?? []);
  });

  // Mouse / interaction state.
  let dragging = $state(false);            // shift-drag canvas pan
  let panStart = { x: 0, y: 0 };
  let panOrig = { x: 0, y: 0 };
  let mouseScreen = $state<{ x: number; y: number } | null>(null);

  // Drag-to-move state for the selected entity. When the user mousedowns
  // on an entity in select mode and moves the mouse beyond DRAG_THRESHOLD_PX,
  // we treat it as a move-drag: live ghost preview while dragging,
  // world.move RPC on release. Below threshold = click (no-op past selection).
  const DRAG_THRESHOLD_PX = 4;
  let entityDrag = $state<null | {
    type: string;
    id: string;
    startScreen: { x: number; y: number };
    startWorld: [number, number];
    currentWorld: [number, number];
    moved: boolean;
  }>(null);

  // Color palette for entity types — matches WorldPalette glyph colors.
  const COLORS = {
    station: "rgb(103, 232, 238)",   // phosphor
    poi: "rgb(244, 114, 182)",       // plasma
    dungeon: "rgb(251, 146, 60)",    // amber
    belt: "rgb(163, 230, 53)",       // lime
    decoration: "rgb(232, 237, 247)", // silver
    region: "rgb(180, 158, 250)",
  } as const;

  // Per-tool default args sent with world.place. Regions go through a
  // separate polygon-placement state machine (see polygonDraft below).
  const PLACE_DEFAULTS: Record<string, Record<string, unknown>> = {
    station:    { Name: "Station", Radius: 100 },
    poi:        { Tier: 1, Roster: "StarterArena" },
    dungeon:    { Name: "Dungeon" },
    belt:       { Radius: 80, Density: 1.0 },
    decoration: { Kind: "wreck", Variant: "" },
  };

  // In-flight polygon-placement state. Set when the user clicks while the
  // Region tool is active; vertices grow as they click; cleared on
  // commit/cancel. Rectangle mode (shift+drag) bypasses this and emits a
  // 4-vertex polygon directly on mouseup.
  type PolygonDraft = {
    vertices: [number, number][]; // world-space
  };
  let polygonDraft = $state<PolygonDraft | null>(null);

  // Rectangle-drag state for shift+drag in region mode. Stores the
  // world-space anchor; the second corner is `worldStore.cursor`.
  let rectDraft = $state<{ anchor: [number, number] } | null>(null);

  // Distance (screen px) within which a click on the first polygon vertex
  // closes the polygon.
  const POLYGON_CLOSE_PX = 6;

  // ── Transform helpers ─────────────────────────────────────────────────

  function worldToScreen(wx: number, wy: number, screenW: number, screenH: number): [number, number] {
    const z = worldStore.zoom;
    const [px, py] = worldStore.pan;
    return [screenW / 2 + (wx + px) * z, screenH / 2 + (wy + py) * z];
  }

  function screenToWorld(sx: number, sy: number, screenW: number, screenH: number): [number, number] {
    const z = worldStore.zoom;
    const [px, py] = worldStore.pan;
    return [(sx - screenW / 2) / z - px, (sy - screenH / 2) / z - py];
  }

  // ── Drawing ──────────────────────────────────────────────────────────

  function clearAndBackground(ctx: CanvasRenderingContext2D, w: number, h: number): void {
    ctx.fillStyle = "rgb(7, 10, 20)";
    ctx.fillRect(0, 0, w, h);

    // Subtle vignette / grid texture.
    if (worldStore.layers.grid) {
      ctx.strokeStyle = "rgba(148, 163, 189, 0.04)";
      ctx.lineWidth = 1;
      const step = 24;
      ctx.beginPath();
      for (let x = 0; x < w; x += step) {
        ctx.moveTo(x + 0.5, 0);
        ctx.lineTo(x + 0.5, h);
      }
      for (let y = 0; y < h; y += step) {
        ctx.moveTo(0, y + 0.5);
        ctx.lineTo(w, y + 0.5);
      }
      ctx.stroke();
    }
  }

  function drawCellBoundaries(ctx: CanvasRenderingContext2D, w: number, h: number): void {
    if (!worldStore.layers.cells) return;
    if (!clusterBounds) return; // bounds not loaded yet — the apiGet on mount fills this in a frame
    const z = worldStore.zoom;
    if (CELL_SIZE * z < 12) return; // too zoomed-out — skip

    // Only render cells that actually exist in the running cluster. The
    // engine supports negative cell indices in principle (CellCoord is
    // int32), but this game's cluster is a positive [0..MeshCellsX) ×
    // [0..MeshCellsY) rectangle, so phantom grid lines outside that
    // region are misleading and we skip them entirely.
    const { minX, maxX, minY, maxY } = clusterBounds;
    const [sxLeft, syTop] = worldToScreen(minX * CELL_SIZE, minY * CELL_SIZE, w, h);
    const [sxRight, syBottom] = worldToScreen((maxX + 1) * CELL_SIZE, (maxY + 1) * CELL_SIZE, w, h);

    ctx.save();

    // Cluster tint + bounding rectangle.
    ctx.fillStyle = "rgba(96, 165, 250, 0.05)";
    ctx.fillRect(sxLeft, syTop, sxRight - sxLeft, syBottom - syTop);
    ctx.strokeStyle = "rgba(96, 165, 250, 0.55)";
    ctx.lineWidth = 1.25;
    ctx.strokeRect(sxLeft + 0.5, syTop + 0.5, sxRight - sxLeft, syBottom - syTop);

    // Inner cell boundaries (clipped to the cluster rectangle).
    ctx.strokeStyle = "rgba(148, 163, 189, 0.32)";
    ctx.lineWidth = 1;
    ctx.beginPath();
    for (let cx = minX + 1; cx <= maxX; cx++) {
      const [sx] = worldToScreen(cx * CELL_SIZE, 0, w, h);
      ctx.moveTo(sx + 0.5, syTop);
      ctx.lineTo(sx + 0.5, syBottom);
    }
    for (let cy = minY + 1; cy <= maxY; cy++) {
      const [, sy] = worldToScreen(0, cy * CELL_SIZE, w, h);
      ctx.moveTo(sxLeft, sy + 0.5);
      ctx.lineTo(sxRight, sy + 0.5);
    }
    ctx.stroke();

    // Cell ID labels.
    if (CELL_SIZE * z >= 60) {
      ctx.font = "9.5px 'JetBrains Mono', ui-monospace, monospace";
      ctx.textAlign = "left";
      ctx.textBaseline = "top";
      ctx.fillStyle = "rgba(96, 165, 250, 0.85)";
      for (let cx = minX; cx <= maxX; cx++) {
        for (let cy = minY; cy <= maxY; cy++) {
          const [sx, sy] = worldToScreen(cx * CELL_SIZE, cy * CELL_SIZE, w, h);
          ctx.fillText(`${cx}_${cy}`, sx + 4, sy + 4);
        }
      }
    }
    ctx.restore();
  }

  // ── Region rendering ────────────────────────────────────────────────
  //
  // Regions are polygons (and future annuli) drawn under the entity layer.
  // Outline + 8% alpha fill, color-coded by kind via regionColor().

  function rgbToRgba(rgb: string, alpha: number): string {
    // Convert "rgb(r, g, b)" → "rgba(r, g, b, A)". Falls back to the
    // raw string if the input isn't the expected shape.
    const m = /^rgb\((\d+),\s*(\d+),\s*(\d+)\)$/.exec(rgb.trim());
    if (!m) return rgb;
    return `rgba(${m[1]}, ${m[2]}, ${m[3]}, ${alpha})`;
  }

  function centroidOf(vertices: [number, number][]): [number, number] {
    if (vertices.length === 0) return [0, 0];
    let sx = 0, sy = 0;
    for (const [x, y] of vertices) { sx += x; sy += y; }
    return [sx / vertices.length, sy / vertices.length];
  }

  function drawRegions(ctx: CanvasRenderingContext2D, w: number, h: number): void {
    if (!worldStore.layers.regions) return;
    const regions = worldStore.snapshot.regions;
    if (!regions || regions.length === 0) return;

    const selID = worldStore.selectedID;
    const selType = worldStore.selectedType;

    ctx.save();
    for (const r of regions) {
      if (r.shape !== "polygon" || r.vertices.length < 3) continue;
      const sel = selType === "region" && selID === r.id;
      const baseColor = regionColor(r.kind);
      const fill = rgbToRgba(baseColor, 0.08);
      const stroke = sel ? rgbToRgba(baseColor, 1.0) : rgbToRgba(baseColor, 0.7);

      ctx.beginPath();
      for (let i = 0; i < r.vertices.length; i++) {
        const [wx, wy] = r.vertices[i];
        const [sx, sy] = worldToScreen(wx, wy, w, h);
        if (i === 0) ctx.moveTo(sx, sy);
        else ctx.lineTo(sx, sy);
      }
      ctx.closePath();
      ctx.fillStyle = fill;
      ctx.fill();
      ctx.strokeStyle = stroke;
      ctx.lineWidth = sel ? 2 : 1.25;
      ctx.stroke();

      // Selection ring at each vertex when selected.
      if (sel) {
        for (const [wx, wy] of r.vertices) {
          const [sx, sy] = worldToScreen(wx, wy, w, h);
          ctx.beginPath();
          ctx.arc(sx, sy, 3, 0, Math.PI * 2);
          ctx.fillStyle = stroke;
          ctx.fill();
        }
      }

      // Label at centroid.
      if (worldStore.zoom >= 0.01) {
        const [cwx, cwy] = centroidOf(r.vertices);
        const [csx, csy] = worldToScreen(cwx, cwy, w, h);
        ctx.font = "10px 'JetBrains Mono', ui-monospace, monospace";
        ctx.textAlign = "center";
        ctx.textBaseline = "middle";
        ctx.fillStyle = rgbToRgba(baseColor, 0.95);
        ctx.fillText(r.name || r.id, csx, csy);
      }
    }
    ctx.restore();
  }

  function drawPolygonDraft(ctx: CanvasRenderingContext2D, w: number, h: number): void {
    if (!polygonDraft || polygonDraft.vertices.length === 0) return;
    const color = DEFAULT_REGION_COLOR;
    const stroke = rgbToRgba(color, 0.9);

    ctx.save();
    ctx.setLineDash([4, 3]);
    ctx.lineWidth = 1.25;
    ctx.strokeStyle = stroke;

    // Connect vertices in order.
    ctx.beginPath();
    for (let i = 0; i < polygonDraft.vertices.length; i++) {
      const [wx, wy] = polygonDraft.vertices[i];
      const [sx, sy] = worldToScreen(wx, wy, w, h);
      if (i === 0) ctx.moveTo(sx, sy);
      else ctx.lineTo(sx, sy);
    }
    // Trailing segment from last vertex to cursor.
    if (mouseScreen) {
      ctx.lineTo(mouseScreen.x, mouseScreen.y);
    }
    ctx.stroke();
    ctx.setLineDash([]);

    // Vertex dots.
    ctx.fillStyle = stroke;
    for (let i = 0; i < polygonDraft.vertices.length; i++) {
      const [wx, wy] = polygonDraft.vertices[i];
      const [sx, sy] = worldToScreen(wx, wy, w, h);
      const r = i === 0 ? 4 : 3;
      ctx.beginPath();
      ctx.arc(sx, sy, r, 0, Math.PI * 2);
      ctx.fill();
      if (i === 0) {
        // Highlight first vertex (the close target).
        ctx.strokeStyle = stroke;
        ctx.lineWidth = 1;
        ctx.beginPath();
        ctx.arc(sx, sy, POLYGON_CLOSE_PX, 0, Math.PI * 2);
        ctx.stroke();
      }
    }
    ctx.restore();
  }

  function drawRectDraft(ctx: CanvasRenderingContext2D, w: number, h: number): void {
    if (!rectDraft) return;
    const [ax, ay] = rectDraft.anchor;
    const [bx, by] = worldStore.cursor;
    const [sx1, sy1] = worldToScreen(ax, ay, w, h);
    const [sx2, sy2] = worldToScreen(bx, by, w, h);
    const x = Math.min(sx1, sx2);
    const y = Math.min(sy1, sy2);
    const ww = Math.abs(sx2 - sx1);
    const hh = Math.abs(sy2 - sy1);

    const color = DEFAULT_REGION_COLOR;
    ctx.save();
    ctx.setLineDash([4, 3]);
    ctx.strokeStyle = rgbToRgba(color, 0.95);
    ctx.lineWidth = 1.25;
    ctx.strokeRect(x + 0.5, y + 0.5, ww, hh);
    ctx.setLineDash([]);
    ctx.fillStyle = rgbToRgba(color, 0.08);
    ctx.fillRect(x, y, ww, hh);
    ctx.restore();
  }

  function drawEntities(ctx: CanvasRenderingContext2D, w: number, h: number): void {
    const snap = worldStore.snapshot;
    const selID = worldStore.selectedID;
    const selType = worldStore.selectedType;

    // Belts (drawn first — they have a radius).
    if (worldStore.layers.belts) {
      for (const b of snap.belts) {
        const [sx, sy] = worldToScreen(b.world_pos[0], b.world_pos[1], w, h);
        const r = Math.max(2, b.radius * worldStore.zoom);
        const sel = selType === "belt" && selID === b.id;
        ctx.beginPath();
        ctx.arc(sx, sy, r, 0, Math.PI * 2);
        ctx.fillStyle = "rgba(163, 230, 53, 0.10)";
        ctx.fill();
        ctx.strokeStyle = sel ? "rgba(103, 232, 238, 0.95)" : "rgba(163, 230, 53, 0.55)";
        ctx.lineWidth = sel ? 1.5 : 1;
        ctx.stroke();
        drawDot(ctx, sx, sy, COLORS.belt, sel);
      }
    }

    // Stations — large dot + radius hint.
    if (worldStore.layers.stations) {
      for (const s of snap.stations) {
        const [sx, sy] = worldToScreen(s.world_pos[0], s.world_pos[1], w, h);
        const sel = selType === "station" && selID === s.id;
        if (s.radius && s.radius > 0) {
          const r = Math.max(2, s.radius * worldStore.zoom);
          ctx.beginPath();
          ctx.arc(sx, sy, r, 0, Math.PI * 2);
          ctx.strokeStyle = "rgba(103, 232, 238, 0.30)";
          ctx.lineWidth = 1;
          ctx.stroke();
        }
        drawSquare(ctx, sx, sy, 6, COLORS.station, sel);
        drawLabel(ctx, sx, sy + 10, s.name || s.id, COLORS.station);
      }
    }

    // POIs — diamond colored by tier.
    if (worldStore.layers.pois) {
      for (const p of snap.pois) {
        const [sx, sy] = worldToScreen(p.world_pos[0], p.world_pos[1], w, h);
        const sel = selType === "poi" && selID === p.id;
        const color =
          p.tier === 3 ? "rgb(251, 113, 133)" :
          p.tier === 2 ? "rgb(251, 191, 36)" :
          COLORS.poi;
        drawDiamond(ctx, sx, sy, 5, color, sel);
      }
    }

    // Dungeons.
    if (worldStore.layers.dungeons) {
      for (const d of snap.dungeons) {
        const [sx, sy] = worldToScreen(d.world_pos[0], d.world_pos[1], w, h);
        const sel = selType === "dungeon" && selID === d.id;
        drawTriangle(ctx, sx, sy, 6, COLORS.dungeon, sel);
        drawLabel(ctx, sx, sy + 10, d.name || d.id, COLORS.dungeon);
      }
    }

    // Decorations — small ring.
    if (worldStore.layers.decorations) {
      for (const dc of snap.decorations) {
        const [sx, sy] = worldToScreen(dc.world_pos[0], dc.world_pos[1], w, h);
        const sel = selType === "decoration" && selID === dc.id;
        ctx.beginPath();
        ctx.arc(sx, sy, 3, 0, Math.PI * 2);
        ctx.strokeStyle = sel ? "rgba(103, 232, 238, 0.95)" : "rgba(232, 237, 247, 0.55)";
        ctx.lineWidth = sel ? 1.5 : 1;
        ctx.stroke();
      }
    }

    // Drag-to-move ghost: while the user is dragging the selected entity,
    // render a translucent marker at the current cursor world pos so the
    // commit position is obvious. The actual entity stays drawn at its
    // pre-move pos until the world.move RPC + refresh completes.
    if (entityDrag && entityDrag.moved) {
      const [sx, sy] = worldToScreen(entityDrag.currentWorld[0], entityDrag.currentWorld[1], w, h);
      ctx.save();
      ctx.setLineDash([4, 3]);
      ctx.strokeStyle = "rgba(103, 232, 238, 0.85)";
      ctx.lineWidth = 1.5;
      ctx.beginPath();
      ctx.arc(sx, sy, 9, 0, Math.PI * 2);
      ctx.stroke();
      ctx.setLineDash([]);
      ctx.fillStyle = "rgba(103, 232, 238, 0.95)";
      ctx.font = "9.5px 'JetBrains Mono', ui-monospace, monospace";
      ctx.textAlign = "left";
      ctx.textBaseline = "middle";
      ctx.fillText(
        `→ (${Math.round(entityDrag.currentWorld[0])}, ${Math.round(entityDrag.currentWorld[1])})`,
        sx + 12, sy,
      );
      ctx.restore();
    }
  }

  function drawDot(ctx: CanvasRenderingContext2D, sx: number, sy: number, color: string, selected: boolean): void {
    ctx.beginPath();
    ctx.arc(sx, sy, selected ? 4 : 2.5, 0, Math.PI * 2);
    ctx.fillStyle = color;
    ctx.fill();
    if (selected) {
      ctx.strokeStyle = "rgba(103, 232, 238, 0.95)";
      ctx.lineWidth = 1.5;
      ctx.stroke();
    }
  }
  function drawSquare(ctx: CanvasRenderingContext2D, sx: number, sy: number, half: number, color: string, selected: boolean): void {
    ctx.fillStyle = color;
    ctx.fillRect(sx - half, sy - half, half * 2, half * 2);
    if (selected) {
      ctx.strokeStyle = "rgba(103, 232, 238, 0.95)";
      ctx.lineWidth = 1.5;
      ctx.strokeRect(sx - half - 2, sy - half - 2, half * 2 + 4, half * 2 + 4);
    }
  }
  function drawDiamond(ctx: CanvasRenderingContext2D, sx: number, sy: number, r: number, color: string, selected: boolean): void {
    ctx.beginPath();
    ctx.moveTo(sx, sy - r);
    ctx.lineTo(sx + r, sy);
    ctx.lineTo(sx, sy + r);
    ctx.lineTo(sx - r, sy);
    ctx.closePath();
    ctx.fillStyle = color;
    ctx.fill();
    if (selected) {
      ctx.strokeStyle = "rgba(103, 232, 238, 0.95)";
      ctx.lineWidth = 1.5;
      ctx.stroke();
    }
  }
  function drawTriangle(ctx: CanvasRenderingContext2D, sx: number, sy: number, r: number, color: string, selected: boolean): void {
    ctx.beginPath();
    ctx.moveTo(sx, sy - r);
    ctx.lineTo(sx + r, sy + r * 0.85);
    ctx.lineTo(sx - r, sy + r * 0.85);
    ctx.closePath();
    ctx.fillStyle = color;
    ctx.fill();
    if (selected) {
      ctx.strokeStyle = "rgba(103, 232, 238, 0.95)";
      ctx.lineWidth = 1.5;
      ctx.stroke();
    }
  }
  function drawLabel(ctx: CanvasRenderingContext2D, sx: number, sy: number, text: string, color: string): void {
    if (worldStore.zoom < 0.02) return; // too zoomed-out
    ctx.font = "9.5px 'JetBrains Mono', ui-monospace, monospace";
    ctx.textAlign = "center";
    ctx.textBaseline = "top";
    ctx.fillStyle = color;
    ctx.fillText(text, sx, sy);
  }

  function drawCursor(ctx: CanvasRenderingContext2D, w: number, h: number): void {
    if (!mouseScreen) return;
    if (worldStore.tool === "select") return;
    ctx.save();
    ctx.strokeStyle = "rgba(34, 211, 218, 0.45)";
    ctx.lineWidth = 1;
    ctx.setLineDash([3, 3]);
    ctx.beginPath();
    ctx.moveTo(0, mouseScreen.y + 0.5);
    ctx.lineTo(w, mouseScreen.y + 0.5);
    ctx.moveTo(mouseScreen.x + 0.5, 0);
    ctx.lineTo(mouseScreen.x + 0.5, h);
    ctx.stroke();
    ctx.setLineDash([]);
    // Tool indicator at cursor.
    ctx.beginPath();
    ctx.arc(mouseScreen.x, mouseScreen.y, 6, 0, Math.PI * 2);
    ctx.strokeStyle = "rgba(103, 232, 238, 0.9)";
    ctx.lineWidth = 1.5;
    ctx.stroke();
    ctx.restore();
  }

  function redraw(): void {
    if (!canvas || !container) return;
    const dpr = window.devicePixelRatio || 1;
    const rect = container.getBoundingClientRect();
    const w = Math.max(100, Math.floor(rect.width));
    const h = Math.max(100, Math.floor(rect.height));
    canvas.width = w * dpr;
    canvas.height = h * dpr;
    canvas.style.width = `${w}px`;
    canvas.style.height = `${h}px`;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    clearAndBackground(ctx, w, h);
    drawCellBoundaries(ctx, w, h);
    drawRegions(ctx, w, h);
    drawEntities(ctx, w, h);
    drawPolygonDraft(ctx, w, h);
    drawRectDraft(ctx, w, h);
    drawCursor(ctx, w, h);
  }

  // RAF-coalesced redraw: any state change schedules at most one redraw per
  // frame. This replaces the always-on 60fps loop, which was burning CPU
  // even when nothing was changing (and stuttering at extreme zooms where
  // the per-frame draw exceeded 16ms).
  let rafPending = 0;
  function scheduleRedraw(): void {
    if (rafPending) return;
    rafPending = requestAnimationFrame(() => {
      rafPending = 0;
      redraw();
    });
  }

  onMount(() => {
    // Seed cluster bounds via a one-shot fetch — the cells SSE topic is
    // 1Hz and may not have streamed anything yet when the editor opens.
    apiGet<CellInfo[]>("/admin/api/cells")
      .then((list) => {
        cellsStore.set(list);
        recomputeBoundsFrom(list);
        scheduleRedraw();
      })
      .catch(() => {});
    // Keep bounds fresh via SSE for live split/merge.
    const offCells = stream.subscribe("cells", (data) => {
      const list = data as CellInfo[];
      cellsStore.set(list);
      recomputeBoundsFrom(list);
      scheduleRedraw();
    });
    recomputeBoundsFrom(cellsStore.value ?? []);

    const ro = new ResizeObserver(() => scheduleRedraw());
    ro.observe(container);
    scheduleRedraw();
    return () => {
      offCells();
      ro.disconnect();
      if (rafPending) cancelAnimationFrame(rafPending);
    };
  });

  // Any reactive read in this $effect schedules a redraw on change.
  $effect(() => {
    // Pull every piece of state the renderer touches so Svelte tracks them.
    void worldStore.snapshot;
    void worldStore.zoom;
    void worldStore.pan;
    void worldStore.tool;
    void worldStore.selectedID;
    void worldStore.selectedType;
    void worldStore.layers;
    void mouseScreen;
    void entityDrag;
    void polygonDraft;
    void rectDraft;
    void worldStore.cursor;
    void clusterBounds;
    scheduleRedraw();
  });

  // ── Picking ──────────────────────────────────────────────────────────

  // Standard ray-cast point-in-polygon. Returns true when (px, py) is
  // inside the polygon defined by `verts` (in any winding order).
  function pointInPolygon(px: number, py: number, verts: [number, number][]): boolean {
    let inside = false;
    for (let i = 0, j = verts.length - 1; i < verts.length; j = i++) {
      const [xi, yi] = verts[i];
      const [xj, yj] = verts[j];
      const intersect =
        ((yi > py) !== (yj > py)) &&
        (px < ((xj - xi) * (py - yi)) / (yj - yi || Number.EPSILON) + xi);
      if (intersect) inside = !inside;
    }
    return inside;
  }

  // Nearest entity within `tolWorld` world units of (wx, wy). Returns
  // {type, id} or null. O(n) — fine at editor scales (low hundreds).
  // Regions are picked separately via point-in-polygon (no nearness check)
  // and take lower priority than point-style entities — so clicking the
  // station glyph inside a region still selects the station.
  function pickAt(wx: number, wy: number, tolWorld: number): { type: string; id: string } | null {
    const snap = worldStore.snapshot;
    const tol2 = tolWorld * tolWorld;
    let bestType = "";
    let bestID = "";
    let bestD2 = Number.POSITIVE_INFINITY;

    type Layered = { id: string; world_pos: [number, number] };
    const buckets: Array<[boolean, string, Layered[]]> = [
      [worldStore.layers.stations,    "station",    snap.stations],
      [worldStore.layers.pois,        "poi",        snap.pois],
      [worldStore.layers.dungeons,    "dungeon",    snap.dungeons],
      [worldStore.layers.belts,       "belt",       snap.belts],
      [worldStore.layers.decorations, "decoration", snap.decorations],
    ];
    for (const [on, type, list] of buckets) {
      if (!on) continue;
      for (const e of list) {
        const dx = e.world_pos[0] - wx;
        const dy = e.world_pos[1] - wy;
        const d2 = dx * dx + dy * dy;
        if (d2 <= tol2 && d2 < bestD2) {
          bestD2 = d2;
          bestType = type;
          bestID = e.id;
        }
      }
    }
    if (bestType) return { type: bestType, id: bestID };

    // Region fallback — point-in-polygon test, last-drawn-wins.
    if (worldStore.layers.regions) {
      for (let i = snap.regions.length - 1; i >= 0; i--) {
        const r = snap.regions[i];
        if (r.shape !== "polygon" || r.vertices.length < 3) continue;
        if (pointInPolygon(wx, wy, r.vertices)) {
          return { type: "region", id: r.id };
        }
      }
    }
    return null;
  }

  // ── Mouse handlers ───────────────────────────────────────────────────

  function getMouseScreen(e: MouseEvent): { x: number; y: number } {
    const rect = canvas.getBoundingClientRect();
    return { x: e.clientX - rect.left, y: e.clientY - rect.top };
  }

  function onMouseDown(e: MouseEvent): void {
    const { x, y } = getMouseScreen(e);
    const rect = canvas.getBoundingClientRect();

    // Middle button always pans (no modifier needed).
    if (e.button === 1) {
      dragging = true;
      panStart = { x, y };
      panOrig = { x: worldStore.pan[0], y: worldStore.pan[1] };
      e.preventDefault();
      return;
    }

    // Region tool + shift-drag = rectangle placement.
    if (worldStore.tool === "region" && e.shiftKey && e.button === 0) {
      const [wx, wy] = screenToWorld(x, y, rect.width, rect.height);
      rectDraft = { anchor: [wx, wy] };
      // Abort any in-progress polygon when switching to rect-drag.
      polygonDraft = null;
      e.preventDefault();
      return;
    }

    // Shift-drag in any other tool = pan.
    if (e.shiftKey) {
      dragging = true;
      panStart = { x, y };
      panOrig = { x: worldStore.pan[0], y: worldStore.pan[1] };
      e.preventDefault();
      return;
    }

    // Select mode: mousedown on an entity initiates a potential drag-to-move.
    // We commit to "move" only once the cursor passes DRAG_THRESHOLD_PX.
    if (worldStore.tool === "select") {
      const [wx, wy] = screenToWorld(x, y, rect.width, rect.height);
      const tol = 10 / worldStore.zoom;
      const hit = pickAt(wx, wy, tol);
      if (hit) {
        // Select immediately so the inspector reflects the choice even on
        // a click-without-drag.
        worldStore.setSelection(hit.type, hit.id);
        // Region selections aren't draggable as a unit yet — skip the
        // entityDrag handshake. (Move-by-vertex is a v2 feature.)
        if (hit.type !== "region") {
          entityDrag = {
            type: hit.type,
            id: hit.id,
            startScreen: { x, y },
            startWorld: [wx, wy],
            currentWorld: [wx, wy],
            moved: false,
          };
        }
        e.preventDefault();
      }
    }
  }

  function onMouseMove(e: MouseEvent): void {
    const { x, y } = getMouseScreen(e);
    mouseScreen = { x, y };
    const rect = canvas.getBoundingClientRect();
    const [wx, wy] = screenToWorld(x, y, rect.width, rect.height);
    worldStore.cursor = [wx, wy];

    if (dragging) {
      const dx = (x - panStart.x) / worldStore.zoom;
      const dy = (y - panStart.y) / worldStore.zoom;
      worldStore.pan = [panOrig.x + dx, panOrig.y + dy];
      return;
    }

    if (entityDrag) {
      const dx = x - entityDrag.startScreen.x;
      const dy = y - entityDrag.startScreen.y;
      if (!entityDrag.moved && Math.hypot(dx, dy) > DRAG_THRESHOLD_PX) {
        entityDrag = { ...entityDrag, moved: true };
      }
      if (entityDrag.moved) {
        entityDrag = { ...entityDrag, currentWorld: [wx, wy] };
      }
    }
  }

  async function onMouseUp(): Promise<void> {
    dragging = false;
    const d = entityDrag;
    entityDrag = null;
    if (d && d.moved) {
      try {
        await worldStore.move(d.type, d.id, Math.round(d.currentWorld[0]), Math.round(d.currentWorld[1]));
      } catch (err) {
        console.error("world.move failed", err);
      }
    }

    // Rectangle-drag finalization. Need a non-degenerate rect — collapse to
    // a no-op if the user clicked without dragging.
    const r = rectDraft;
    rectDraft = null;
    if (r) {
      const [ax, ay] = r.anchor;
      const [bx, by] = worldStore.cursor;
      const minX = Math.min(ax, bx);
      const maxX = Math.max(ax, bx);
      const minY = Math.min(ay, by);
      const maxY = Math.max(ay, by);
      if (maxX - minX < 4 || maxY - minY < 4) return; // too small — discard
      // Clockwise: top-left → top-right → bottom-right → bottom-left.
      const verts: [number, number][] = [
        [minX, minY],
        [maxX, minY],
        [maxX, maxY],
        [minX, maxY],
      ];
      await commitRegionPolygon(verts);
    }
  }

  function onMouseLeave(): void {
    dragging = false;
    entityDrag = null;
    rectDraft = null;
    mouseScreen = null;
  }

  async function onClick(e: MouseEvent): Promise<void> {
    if (e.shiftKey) return;     // shift = pan / rect-drag
    if (dragging) return;       // a pan finished here
    // mousedown/up in select mode handles selection + drag-move. The click
    // handler only fires for place tools.
    if (worldStore.tool === "select") return;

    const { x, y } = getMouseScreen(e);
    const rect = canvas.getBoundingClientRect();
    const [wx, wy] = screenToWorld(x, y, rect.width, rect.height);

    const t = worldStore.tool;

    // Region tool: build a polygon vertex-by-vertex. Click within
    // POLYGON_CLOSE_PX of the first vertex (when ≥3 vertices exist) to
    // commit. Escape cancels (see onKey).
    if (t === "region") {
      if (polygonDraft && polygonDraft.vertices.length >= 3) {
        const [fx, fy] = polygonDraft.vertices[0];
        const [fsx, fsy] = worldToScreen(fx, fy, rect.width, rect.height);
        const d = Math.hypot(fsx - x, fsy - y);
        if (d <= POLYGON_CLOSE_PX) {
          const verts = polygonDraft.vertices.slice();
          polygonDraft = null;
          await commitRegionPolygon(verts);
          return;
        }
      }
      const next: [number, number] = [wx, wy];
      polygonDraft = polygonDraft
        ? { vertices: [...polygonDraft.vertices, next] }
        : { vertices: [next] };
      return;
    }

    const extra = PLACE_DEFAULTS[t] ?? {};
    try {
      await worldStore.place(t, Math.round(wx), Math.round(wy), extra);
    } catch (err) {
      console.error("world.place failed", err);
    }
  }

  // commitRegionPolygon POSTs world.place for a freshly-drawn polygon.
  // Vertices are world-space; sent as Vertices=<json> per the cmdsys
  // contract. Uses the first vertex as WorldX/WorldY (the handler uses
  // that pair only to derive a default auto-id cell hash; the actual
  // shape lives in Vertices).
  async function commitRegionPolygon(verts: [number, number][]): Promise<void> {
    if (verts.length < 3) return;
    const rounded = verts.map(([wx, wy]) => [Math.round(wx), Math.round(wy)] as [number, number]);
    try {
      await worldStore.place("region", rounded[0][0], rounded[0][1], {
        Shape: "polygon",
        Vertices: JSON.stringify(rounded),
        Name: "",
        Kind: "",
      });
    } catch (err) {
      console.error("world.place region failed", err);
    }
  }

  function onWheel(e: WheelEvent): void {
    e.preventDefault();
    const { x, y } = getMouseScreen(e);
    const rect = canvas.getBoundingClientRect();
    const w = rect.width, h = rect.height;
    // World point under cursor before zoom.
    const [wx, wy] = screenToWorld(x, y, w, h);
    const factor = Math.exp(-e.deltaY * 0.001);
    const newZoom = Math.max(0.005, Math.min(10, worldStore.zoom * factor));
    worldStore.zoom = newZoom;
    // Adjust pan so the same world point stays under the cursor.
    const [nsx, nsy] = worldToScreen(wx, wy, w, h);
    const dx = (x - nsx) / newZoom;
    const dy = (y - nsy) / newZoom;
    worldStore.pan = [worldStore.pan[0] + dx, worldStore.pan[1] + dy];
  }

  // ── Keyboard ─────────────────────────────────────────────────────────

  const HOTKEYS: Record<string, Tool> = {
    v: "select",
    "1": "station",
    "2": "poi",
    "3": "dungeon",
    "4": "belt",
    "5": "region",
    "6": "decoration",
  };

  function onKey(e: KeyboardEvent): void {
    const target = e.target as HTMLElement | null;
    if (target && (target.tagName === "INPUT" || target.tagName === "TEXTAREA" || target.isContentEditable)) {
      return;
    }
    if (e.key === "Escape") {
      if (polygonDraft) {
        polygonDraft = null;
        return;
      }
      if (rectDraft) {
        rectDraft = null;
        return;
      }
      worldStore.clearSelection();
      return;
    }
    if (e.key === "Enter") {
      // Commit an in-progress polygon when ≥3 vertices exist.
      if (polygonDraft && polygonDraft.vertices.length >= 3) {
        const verts = polygonDraft.vertices.slice();
        polygonDraft = null;
        void commitRegionPolygon(verts);
        e.preventDefault();
        return;
      }
    }
    const t = HOTKEYS[e.key.toLowerCase()];
    if (t) {
      // Switching away from the region tool abandons any in-flight draft.
      if (t !== "region") {
        polygonDraft = null;
        rectDraft = null;
      }
      worldStore.tool = t;
      e.preventDefault();
    }
  }
</script>

<svelte:window onkeydown={onKey} />

<div
  bind:this={container}
  class="relative grow min-w-0 min-h-0"
  style="background: rgb(7, 10, 20);"
>
  <canvas
    bind:this={canvas}
    class="block w-full h-full
      {worldStore.tool === 'select' ? 'cursor-crosshair' : 'cursor-cell'}
      {dragging ? '!cursor-grabbing' : ''}"
    onmousedown={onMouseDown}
    onmousemove={onMouseMove}
    onmouseup={onMouseUp}
    onmouseleave={onMouseLeave}
    onclick={onClick}
    onwheel={onWheel}
  ></canvas>

  <!-- Overlay legend (top-left) -->
  <div
    class="absolute top-3 left-3 pointer-events-none bg-[var(--bg-deep)]/70 backdrop-blur border border-[var(--border-subtle)] rounded px-2.5 py-1.5 font-mono text-[10px] text-[var(--text-muted)] tracking-tight flex flex-col gap-0.5"
  >
    <div>shift-drag · pan</div>
    <div>wheel · zoom</div>
    <div>V · select / 1-6 · place</div>
    <div>5 · region — click vertices, click 1st to close</div>
    <div>5 + shift-drag · rect region</div>
    <div>esc · cancel / clear selection</div>
  </div>

</div>
