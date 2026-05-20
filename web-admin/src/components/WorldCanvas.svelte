<script lang="ts">
  import { onMount } from "svelte";
  import {
    worldStore,
    CELL_SIZE,
    TIER_2_RADIUS,
    TIER_3_RADIUS,
    type Tool,
  } from "$lib/world-store.svelte";
  import { cellsStore } from "$lib/stores.svelte";
  import { stream } from "$lib/stream";
  import { apiGet } from "$lib/api";
  import type { CellInfo } from "$lib/types";

  let canvas: HTMLCanvasElement;
  let container: HTMLDivElement;

  // Cluster bounds derived from the live cells SSE topic. The world is
  // infinite in principle (int32 cell indices), but the running cluster
  // covers a finite rectangle of cells. We use these bounds to clip cell
  // boundary drawing so the editor doesn't suggest cells exist that don't.
  let clusterBounds = $state<{ minX: number; maxX: number; minY: number; maxY: number } | null>(null);

  function parseCellID(id: string): { x: number; y: number } | null {
    // Format is "X_Y" at depth 0; "X_Y:N" for split children — we use the
    // root XY for bounding-box purposes since splits sit inside their parent.
    const head = id.split(":")[0];
    const parts = head.split("_");
    if (parts.length !== 2) return null;
    const x = parseInt(parts[0], 10);
    const y = parseInt(parts[1], 10);
    if (!isFinite(x) || !isFinite(y)) return null;
    return { x, y };
  }

  function recomputeBoundsFrom(cells: CellInfo[]): void {
    if (!cells || cells.length === 0) {
      clusterBounds = null;
      return;
    }
    let minX = Infinity, maxX = -Infinity, minY = Infinity, maxY = -Infinity;
    for (const c of cells) {
      const xy = parseCellID(c.id);
      if (!xy) continue;
      if (xy.x < minX) minX = xy.x;
      if (xy.x > maxX) maxX = xy.x;
      if (xy.y < minY) minY = xy.y;
      if (xy.y > maxY) maxY = xy.y;
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

  // Per-tool default args sent with world.place.
  const PLACE_DEFAULTS: Record<string, Record<string, unknown>> = {
    station:    { Name: "Station", Radius: 100 },
    poi:        { Tier: 1, Roster: "StarterArena" },
    dungeon:    { Name: "Dungeon" },
    belt:       { Radius: 80, Density: 1.0 },
    decoration: { Kind: "wreck", Variant: "" },
    // region not supported by world.place yet — treat as no-op.
    region:     {},
  };

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
    const z = worldStore.zoom;
    if (CELL_SIZE * z < 12) return; // too zoomed-out — skip

    // Cluster-aware view: the running cluster occupies a finite rectangle of
    // cells; we draw boundaries / labels for THOSE cells brightly and tint
    // the region. Cells outside the cluster get faint grid lines so the
    // editor still shows the world-coord grid for orientation — they just
    // visually fade into the background.
    const [wxMin, wyMin] = screenToWorld(0, 0, w, h);
    const [wxMax, wyMax] = screenToWorld(w, h, w, h);
    const cxMin = Math.floor(wxMin / CELL_SIZE) - 1;
    const cxMax = Math.ceil(wxMax / CELL_SIZE) + 1;
    const cyMin = Math.floor(wyMin / CELL_SIZE) - 1;
    const cyMax = Math.ceil(wyMax / CELL_SIZE) + 1;

    ctx.save();

    // Cluster tint + bounding rectangle (when we know the bounds).
    if (clusterBounds) {
      const { minX, maxX, minY, maxY } = clusterBounds;
      const [sxLeft, syTop] = worldToScreen(minX * CELL_SIZE, minY * CELL_SIZE, w, h);
      const [sxRight, syBottom] = worldToScreen((maxX + 1) * CELL_SIZE, (maxY + 1) * CELL_SIZE, w, h);
      ctx.fillStyle = "rgba(96, 165, 250, 0.05)";
      ctx.fillRect(sxLeft, syTop, sxRight - sxLeft, syBottom - syTop);
      ctx.strokeStyle = "rgba(96, 165, 250, 0.55)";
      ctx.lineWidth = 1.25;
      ctx.strokeRect(sxLeft + 0.5, syTop + 0.5, sxRight - sxLeft, syBottom - syTop);
    }

    // All grid lines (full viewport range). Cells inside the cluster get a
    // brighter stroke; cells outside get a dim stroke so the world-coord
    // grid is still visible for navigation.
    ctx.lineWidth = 1;
    function inClusterX(cx: number): boolean {
      return !!clusterBounds && cx >= clusterBounds.minX && cx <= clusterBounds.maxX + 1;
    }
    function inClusterY(cy: number): boolean {
      return !!clusterBounds && cy >= clusterBounds.minY && cy <= clusterBounds.maxY + 1;
    }
    for (let cx = cxMin; cx <= cxMax; cx++) {
      const [sx] = worldToScreen(cx * CELL_SIZE, 0, w, h);
      ctx.beginPath();
      ctx.moveTo(sx + 0.5, 0);
      ctx.lineTo(sx + 0.5, h);
      ctx.strokeStyle = inClusterX(cx) ? "rgba(148, 163, 189, 0.32)" : "rgba(148, 163, 189, 0.10)";
      ctx.stroke();
    }
    for (let cy = cyMin; cy <= cyMax; cy++) {
      const [, sy] = worldToScreen(0, cy * CELL_SIZE, w, h);
      ctx.beginPath();
      ctx.moveTo(0, sy + 0.5);
      ctx.lineTo(w, sy + 0.5);
      ctx.strokeStyle = inClusterY(cy) ? "rgba(148, 163, 189, 0.32)" : "rgba(148, 163, 189, 0.10)";
      ctx.stroke();
    }

    // Cell ID labels — bright inside cluster, faint outside.
    if (CELL_SIZE * z >= 60) {
      ctx.font = "9.5px 'JetBrains Mono', ui-monospace, monospace";
      ctx.textAlign = "left";
      ctx.textBaseline = "top";
      for (let cx = cxMin; cx <= cxMax - 1; cx++) {
        for (let cy = cyMin; cy <= cyMax - 1; cy++) {
          const inCluster =
            !!clusterBounds &&
            cx >= clusterBounds.minX && cx <= clusterBounds.maxX &&
            cy >= clusterBounds.minY && cy <= clusterBounds.maxY;
          const [sx, sy] = worldToScreen(cx * CELL_SIZE, cy * CELL_SIZE, w, h);
          ctx.fillStyle = inCluster ? "rgba(96, 165, 250, 0.85)" : "rgba(148, 163, 189, 0.30)";
          ctx.fillText(`${cx}_${cy}`, sx + 4, sy + 4);
        }
      }
    }
    ctx.restore();
  }

  function drawTierRings(ctx: CanvasRenderingContext2D, w: number, h: number): void {
    if (!worldStore.layers.tiers) return;
    const [cx, cy] = worldToScreen(0, 0, w, h);
    const z = worldStore.zoom;

    ctx.save();
    ctx.setLineDash([6, 4]);
    ctx.lineWidth = 1;

    // T1/T2 boundary — amber.
    ctx.strokeStyle = "rgba(251, 191, 36, 0.5)";
    ctx.beginPath();
    ctx.arc(cx, cy, TIER_2_RADIUS * z, 0, Math.PI * 2);
    ctx.stroke();

    // T2/T3 boundary — ember.
    ctx.strokeStyle = "rgba(251, 113, 133, 0.55)";
    ctx.beginPath();
    ctx.arc(cx, cy, TIER_3_RADIUS * z, 0, Math.PI * 2);
    ctx.stroke();

    ctx.setLineDash([]);

    // Labels on the X axis at right side of each ring.
    if (z * TIER_2_RADIUS > 40) {
      ctx.fillStyle = "rgba(251, 191, 36, 0.85)";
      ctx.font = "9.5px 'JetBrains Mono', ui-monospace, monospace";
      ctx.textAlign = "left";
      ctx.textBaseline = "middle";
      const [t2x] = worldToScreen(TIER_2_RADIUS, 0, w, h);
      ctx.fillText("T1/T2", t2x + 4, cy);
      ctx.fillStyle = "rgba(251, 113, 133, 0.9)";
      const [t3x] = worldToScreen(TIER_3_RADIUS, 0, w, h);
      ctx.fillText("T2/T3", t3x + 4, cy);
    }

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
    drawTierRings(ctx, w, h);
    drawEntities(ctx, w, h);
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
    void clusterBounds;
    scheduleRedraw();
  });

  // ── Picking ──────────────────────────────────────────────────────────

  // Nearest entity within `tolWorld` world units of (wx, wy). Returns
  // {type, id} or null. O(n) — fine at editor scales (low hundreds).
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
    return bestType ? { type: bestType, id: bestID } : null;
  }

  // ── Mouse handlers ───────────────────────────────────────────────────

  function getMouseScreen(e: MouseEvent): { x: number; y: number } {
    const rect = canvas.getBoundingClientRect();
    return { x: e.clientX - rect.left, y: e.clientY - rect.top };
  }

  function onMouseDown(e: MouseEvent): void {
    const { x, y } = getMouseScreen(e);
    const rect = canvas.getBoundingClientRect();

    // Shift-drag = pan. Middle button also pans.
    if (e.shiftKey || e.button === 1) {
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
        entityDrag = {
          type: hit.type,
          id: hit.id,
          startScreen: { x, y },
          startWorld: [wx, wy],
          currentWorld: [wx, wy],
          moved: false,
        };
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
  }

  function onMouseLeave(): void {
    dragging = false;
    entityDrag = null;
    mouseScreen = null;
  }

  async function onClick(e: MouseEvent): Promise<void> {
    if (e.shiftKey) return;     // shift = pan
    if (dragging) return;       // a pan finished here
    // mousedown/up in select mode handles selection + drag-move. The click
    // handler only fires for place tools.
    if (worldStore.tool === "select") return;

    const { x, y } = getMouseScreen(e);
    const rect = canvas.getBoundingClientRect();
    const [wx, wy] = screenToWorld(x, y, rect.width, rect.height);

    const t = worldStore.tool;
    if (t === "region") return; // not supported by world.place yet
    const extra = PLACE_DEFAULTS[t] ?? {};
    try {
      await worldStore.place(t, Math.round(wx), Math.round(wy), extra);
    } catch (err) {
      console.error("world.place failed", err);
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
      worldStore.clearSelection();
      return;
    }
    const t = HOTKEYS[e.key.toLowerCase()];
    if (t) {
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
    <div>esc · clear selection</div>
  </div>
</div>
