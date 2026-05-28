<script lang="ts">
  import { onMount } from "svelte";
  import { cellsStore, metricsHistoryStore } from "$lib/stores.svelte";
  import { stream } from "$lib/stream";
  import { layoutCells, type Layout } from "./cellmap-layout";
  import { parseCellID } from "$lib/cellid";
  import type { CellInfo, MetricsSample } from "$lib/types";

  type Props = {
    onSelect?: (id: string | null) => void;
    selected?: string | null;
    colorMode?: "load" | "host" | "entities";
  };
  let { onSelect, selected, colorMode = "load" }: Props = $props();

  let canvas: HTMLCanvasElement;
  let container: HTMLDivElement;
  let layouts = $state<Layout[]>([]);
  let cells = $state<CellInfo[]>([]);
  let hover = $state<string | null>(null);
  let tooltip = $state<{
    x: number;
    y: number;
    cell: CellInfo;
  } | null>(null);
  let mouse = $state<{ x: number; y: number } | null>(null);
  let frameTime = $state(0);

  $effect(() => {
    const off = stream.subscribe("cells", (data) => {
      cells = data as CellInfo[];
      cellsStore.set(cells);
    });
    return off;
  });

  onMount(() => {
    const ro = new ResizeObserver(() => requestAnimationFrame(redraw));
    ro.observe(container);
    // continuous redraw at 30fps for breathing / pulse animations + history-driven sparklines
    let raf = 0;
    let last = 0;
    const tick = (t: number) => {
      if (t - last >= 33) {
        last = t;
        frameTime = t;
        redraw();
      }
      raf = requestAnimationFrame(tick);
    };
    raf = requestAnimationFrame(tick);
    return () => {
      ro.disconnect();
      cancelAnimationFrame(raf);
    };
  });

  // === Color helpers ===

  function loadColor(load: number, alpha = 1): string {
    // Lime → amber → ember (matches CSS --load-* tokens).
    const stops: Array<[number, [number, number, number]]> = [
      [0.0, [163, 230, 53]],
      [0.5, [212, 214, 58]],
      [0.7, [251, 191, 36]],
      [0.85, [251, 146, 60]],
      [1.0, [251, 113, 133]],
    ];
    for (let i = 1; i < stops.length; i++) {
      const [hi, hc] = stops[i];
      if (load <= hi) {
        const [lo, lc] = stops[i - 1];
        const t = (load - lo) / Math.max(0.0001, hi - lo);
        const r = Math.round(lc[0] + (hc[0] - lc[0]) * t);
        const g = Math.round(lc[1] + (hc[1] - lc[1]) * t);
        const b = Math.round(lc[2] + (hc[2] - lc[2]) * t);
        return `rgba(${r},${g},${b},${alpha})`;
      }
    }
    return `rgba(251,113,133,${alpha})`;
  }

  // hostColor — stable hue per host via FNV-1a + golden-ratio scramble.
  function hostColor(hostId: string, alpha = 1): string {
    let h = 2166136261 >>> 0;
    for (let i = 0; i < hostId.length; i++) {
      h = (h ^ hostId.charCodeAt(i)) >>> 0;
      h = Math.imul(h, 16777619) >>> 0;
    }
    h = Math.imul(h, 2654435769) >>> 0;
    const hue = h % 360;
    return `hsla(${hue}, 70%, 55%, ${alpha})`;
  }

  function entityColor(real: number, alpha = 1): string {
    const t = Math.min(1, real / 200);
    const r = Math.round(70 + 60 * t);
    const g = Math.round(180 + 50 * t);
    const b = 220;
    return `rgba(${r},${g},${b},${alpha})`;
  }

  function fillColorOf(c: CellInfo): string {
    switch (colorMode) {
      case "host": return hostColor(c.hostId || "?", 0.18);
      case "entities": return entityColor(c.entities.real, 0.2);
      default: return loadColor(c.load, 0.22);
    }
  }

  // Deterministic small hash → 0..1, used to scatter entity dots.
  function rand01(seed: number): number {
    seed = (seed * 1103515245 + 12345) & 0x7fffffff;
    seed = (seed ^ (seed >>> 13)) & 0x7fffffff;
    return ((seed * 1597) & 0x7fffffff) / 0x7fffffff;
  }
  function strHash(s: string): number {
    let h = 2166136261 >>> 0;
    for (let i = 0; i < s.length; i++) {
      h = (h ^ s.charCodeAt(i)) >>> 0;
      h = Math.imul(h, 16777619) >>> 0;
    }
    return h;
  }

  function redraw() {
    if (!canvas || !container) return;
    const dpr = window.devicePixelRatio || 1;
    const rect = container.getBoundingClientRect();
    const w = Math.max(100, Math.floor(rect.width));
    const h = Math.max(100, Math.floor(rect.height));
    canvas.width = w * dpr;
    canvas.height = h * dpr;
    canvas.style.width = `${w}px`;
    canvas.style.height = `${h}px`;
    const ctx = canvas.getContext("2d")!;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, w, h);

    // === Layout ===
    // Reserve a left/top gutter for axis labels (24px each).
    const gutterL = 26;
    const gutterT = 22;
    const mapPadding = 8;
    const layoutInput = cells.map((c) => ({ id: c.id, depth: c.depth, parent: c.parent }));
    layouts = layoutCells(layoutInput, {
      width: w - gutterL,
      height: h - gutterT,
      padding: mapPadding,
    }).map((L) => ({ ...L, x: L.x + gutterL, y: L.y + gutterT }));

    // === Background grid (subtle) ===
    drawBackgroundGrid(ctx, gutterL, gutterT, w, h);

    // === Axis labels ===
    drawAxisLabels(ctx, gutterL, gutterT);

    const byId = new Map(cells.map((c) => [c.id, c]));
    const history = metricsHistoryStore.value;

    // Sort by depth ascending so children render over parents.
    const ordered = [...layouts].sort((a, b) => a.depth - b.depth);

    for (const L of ordered) {
      const cell = byId.get(L.id);
      if (!cell) continue;
      const hasChildren = layouts.some((c) => c.id.startsWith(L.id + ":"));
      if (hasChildren) {
        // Container cell — show only a faint outline and a label.
        ctx.strokeStyle = "rgba(148,163,189,0.08)";
        ctx.lineWidth = 1;
        ctx.setLineDash([4, 4]);
        ctx.strokeRect(L.x + 0.5, L.y + 0.5, L.w - 1, L.h - 1);
        ctx.setLineDash([]);
        continue;
      }
      drawCellCard(ctx, L, cell, history[L.id] ?? []);
    }

    // Hover crosshair across the full map (over the gutter + map area).
    if (mouse && !hover) {
      // Skip — only show crosshair when hovering a cell, handled below.
    }
    if (hover && mouse) {
      drawCrosshair(ctx, mouse.x, mouse.y, w, h, gutterL, gutterT);
    }

    // Scanline overlay — extremely subtle.
    drawScanlines(ctx, w, h);
  }

  function drawBackgroundGrid(
    ctx: CanvasRenderingContext2D, gx: number, gy: number, w: number, h: number,
  ) {
    ctx.save();
    ctx.strokeStyle = "rgba(148, 163, 189, 0.04)";
    ctx.lineWidth = 1;
    const step = 24;
    ctx.beginPath();
    for (let x = gx; x < w; x += step) {
      ctx.moveTo(x + 0.5, gy);
      ctx.lineTo(x + 0.5, h);
    }
    for (let y = gy; y < h; y += step) {
      ctx.moveTo(gx, y + 0.5);
      ctx.lineTo(w, y + 0.5);
    }
    ctx.stroke();
    ctx.restore();
  }

  function drawAxisLabels(ctx: CanvasRenderingContext2D, gx: number, gy: number) {
    if (layouts.length === 0) return;
    // Group base-cells by column / row and label their x/y in the gutter.
    ctx.save();
    ctx.fillStyle = "rgba(148, 163, 189, 0.36)";
    ctx.font = "9.5px 'JetBrains Mono', ui-monospace, monospace";

    const baseCols = new Map<number, Layout>();
    const baseRows = new Map<number, Layout>();
    for (const L of layouts) {
      if (L.depth !== 0) continue;
      const xy = parseCellID(L.id);
      if (!xy) continue;
      if (!baseCols.has(xy.x)) baseCols.set(xy.x, L);
      if (!baseRows.has(xy.y)) baseRows.set(xy.y, L);
    }
    // Top axis (X)
    ctx.textAlign = "center";
    ctx.textBaseline = "middle";
    for (const [x, L] of baseCols) {
      ctx.fillText(`X${x}`, L.x + L.w / 2, gy / 2);
    }
    // Left axis (Y)
    ctx.textAlign = "center";
    for (const [y, L] of baseRows) {
      ctx.fillText(`Y${y}`, gx / 2, L.y + L.h / 2);
    }
    ctx.restore();
  }

  function drawCellCard(
    ctx: CanvasRenderingContext2D, L: Layout, c: CellInfo, hist: MetricsSample[],
  ) {
    const px = L.x + 1;
    const py = L.y + 1;
    const pw = L.w - 2;
    const ph = L.h - 2;
    const isSelected = c.id === selected;
    const isHover = c.id === hover;

    // === Background ===
    // Subtle vertical gradient for depth.
    const grad = ctx.createLinearGradient(0, py, 0, py + ph);
    grad.addColorStop(0, "rgba(15, 20, 34, 0.85)");
    grad.addColorStop(1, "rgba(7, 9, 15, 0.92)");
    ctx.fillStyle = grad;
    ctx.fillRect(px, py, pw, ph);

    // Color-mode tint
    ctx.fillStyle = fillColorOf(c);
    ctx.fillRect(px, py, pw, ph);

    // === Hot-cell breathing pulse (load > 0.6) ===
    if (c.load > 0.6 && !isSelected) {
      const phase = (Math.sin(frameTime / (c.load > 0.85 ? 300 : 600)) + 1) / 2;
      const alpha = 0.04 + phase * (c.load > 0.85 ? 0.1 : 0.05);
      ctx.fillStyle = loadColor(c.load, alpha);
      ctx.fillRect(px, py, pw, ph);
    }

    // === Top host stripe ===
    ctx.fillStyle = hostColor(c.hostId || "?", 0.85);
    ctx.fillRect(px, py, pw, 3);

    // === Border (selection / hover / depth) ===
    ctx.lineWidth = isSelected ? 1.5 : 1;
    ctx.strokeStyle = isSelected
      ? "rgba(34, 211, 218, 0.9)"
      : isHover
        ? "rgba(148, 163, 189, 0.4)"
        : c.depth > 0
          ? "rgba(244, 114, 182, 0.25)"
          : "rgba(148, 163, 189, 0.16)";
    ctx.strokeRect(px + 0.5, py + 0.5, pw - 1, ph - 1);

    if (isSelected) {
      // Phosphor glow halo
      ctx.save();
      ctx.shadowColor = "rgba(34, 211, 218, 0.6)";
      ctx.shadowBlur = 14;
      ctx.strokeStyle = "rgba(34, 211, 218, 0.95)";
      ctx.lineWidth = 1.2;
      ctx.strokeRect(px + 0.5, py + 0.5, pw - 1, ph - 1);
      ctx.restore();
    }

    // === Corner brackets — gives the HUD look ===
    drawCornerBrackets(ctx, px, py, pw, ph,
      isSelected ? "rgba(34, 211, 218, 0.9)"
      : isHover ? "rgba(148, 163, 189, 0.55)"
      : "rgba(148, 163, 189, 0.22)",
    );

    // === Skip detailed rendering if too small ===
    if (pw < 60 || ph < 50) {
      // Just show ID centered
      if (pw > 30 && ph > 16) {
        ctx.fillStyle = "rgba(232, 237, 247, 0.7)";
        ctx.font = "9px 'JetBrains Mono', ui-monospace, monospace";
        ctx.textAlign = "center";
        ctx.textBaseline = "middle";
        const short = compactId(c.id);
        ctx.fillText(short, px + pw / 2, py + ph / 2);
      }
      return;
    }

    // === Cell ID (top-left) ===
    ctx.fillStyle = isSelected ? "rgba(103, 232, 238, 1)" : "rgba(232, 237, 247, 0.95)";
    ctx.font = "600 11px 'JetBrains Mono', ui-monospace, monospace";
    ctx.textAlign = "left";
    ctx.textBaseline = "top";
    ctx.fillText(compactId(c.id), px + 8, py + 8);

    // === Load % (top-right) ===
    const loadPct = Math.round(c.load * 100);
    ctx.fillStyle = loadColor(c.load, 1);
    ctx.font = "500 13px 'JetBrains Mono', ui-monospace, monospace";
    ctx.textAlign = "right";
    ctx.textBaseline = "top";
    ctx.fillText(`${loadPct}%`, px + pw - 8, py + 6);

    // === Entity dot scatter (deterministic) ===
    drawEntityField(ctx, c, px + 8, py + 24, pw - 32, ph - 56);

    // === Right-edge load bar (vertical) ===
    drawLoadBar(ctx, c, px + pw - 6, py + 22, 4, ph - 30);

    // === Bottom row: counts + tick sparkline ===
    drawBottomRow(ctx, c, hist, px + 8, py + ph - 18, pw - 16);

    // === Replica indicator (top-right, below load %) ===
    if (c.entities.replica > 0 && pw > 90) {
      ctx.fillStyle = "rgba(244, 114, 182, 0.85)";
      ctx.font = "10px 'JetBrains Mono', ui-monospace, monospace";
      ctx.textAlign = "right";
      ctx.fillText(`R·${c.entities.replica}`, px + pw - 8, py + 22);
    }

    // === Session indicator (top-right of bottom-row area)
    if (c.entities.connected > 0 && pw > 90) {
      ctx.fillStyle = "rgba(103, 232, 238, 0.95)";
      ctx.font = "10px 'JetBrains Mono', ui-monospace, monospace";
      ctx.textAlign = "right";
      ctx.fillText(`◉ ${c.entities.connected}`, px + pw - 8, py + 36);
    }
  }

  function compactId(id: string): string {
    // "cell_0_0" → "0,0"; "cell_d1_0_0" → "d1·0,0"
    const c = parseCellID(id);
    if (!c) return id;
    if (c.depth === 0) return `${c.x},${c.y}`;
    return `d${c.depth}·${c.x},${c.y}`;
  }

  function drawCornerBrackets(
    ctx: CanvasRenderingContext2D, x: number, y: number, w: number, h: number, color: string,
  ) {
    ctx.strokeStyle = color;
    ctx.lineWidth = 1.2;
    const L = Math.min(8, w / 4, h / 4);
    ctx.beginPath();
    // top-left
    ctx.moveTo(x, y + L); ctx.lineTo(x, y); ctx.lineTo(x + L, y);
    // top-right
    ctx.moveTo(x + w - L, y); ctx.lineTo(x + w, y); ctx.lineTo(x + w, y + L);
    // bottom-left
    ctx.moveTo(x, y + h - L); ctx.lineTo(x, y + h); ctx.lineTo(x + L, y + h);
    // bottom-right
    ctx.moveTo(x + w - L, y + h); ctx.lineTo(x + w, y + h); ctx.lineTo(x + w, y + h - L);
    ctx.stroke();
  }

  function drawEntityField(
    ctx: CanvasRenderingContext2D, c: CellInfo, x: number, y: number, w: number, h: number,
  ) {
    if (w <= 0 || h <= 0) return;
    // Cap dots so dense cells don't murder fps. Scale dot count log-ish with real count.
    const real = c.entities.real;
    const replica = c.entities.replica;
    const ghost = c.entities.ghost;
    const total = real + replica + ghost;
    if (total === 0) return;

    // Realistic representation: max 60 dots, biased toward real entities.
    const maxDots = 60;
    const totalDots = Math.min(maxDots, Math.ceil(Math.log10(total + 1) * 18) + Math.min(20, total));
    const realDots = Math.round((real / total) * totalDots);
    const replicaDots = Math.round((replica / total) * totalDots);
    const ghostDots = totalDots - realDots - replicaDots;

    const seed = strHash(c.id);
    let i = 0;

    // Real (phosphor) — slightly larger, brighter
    ctx.fillStyle = "rgba(103, 232, 238, 0.85)";
    for (let n = 0; n < realDots; n++, i++) {
      const px = x + rand01(seed + i * 7) * w;
      const py = y + rand01(seed + i * 13 + 1) * h;
      ctx.beginPath();
      ctx.arc(px, py, 1.4, 0, Math.PI * 2);
      ctx.fill();
    }
    // Replica (plasma magenta) — slight opacity
    ctx.fillStyle = "rgba(244, 114, 182, 0.55)";
    for (let n = 0; n < replicaDots; n++, i++) {
      const px = x + rand01(seed + i * 7) * w;
      const py = y + rand01(seed + i * 13 + 1) * h;
      ctx.beginPath();
      ctx.arc(px, py, 1, 0, Math.PI * 2);
      ctx.fill();
    }
    // Ghost (dim steel)
    ctx.fillStyle = "rgba(148, 163, 189, 0.35)";
    for (let n = 0; n < ghostDots; n++, i++) {
      const px = x + rand01(seed + i * 7) * w;
      const py = y + rand01(seed + i * 13 + 1) * h;
      ctx.beginPath();
      ctx.arc(px, py, 0.9, 0, Math.PI * 2);
      ctx.fill();
    }
  }

  function drawLoadBar(
    ctx: CanvasRenderingContext2D, c: CellInfo, x: number, y: number, w: number, h: number,
  ) {
    // Track
    ctx.fillStyle = "rgba(148, 163, 189, 0.08)";
    ctx.fillRect(x, y, w, h);
    // Fill (bottom-up)
    const fillH = Math.min(h, Math.max(0, c.load) * h);
    ctx.fillStyle = loadColor(c.load, 0.95);
    ctx.fillRect(x, y + h - fillH, w, fillH);
    // Threshold ticks at 60% and 85%
    ctx.fillStyle = "rgba(255,255,255,0.25)";
    ctx.fillRect(x, y + h - h * 0.6 - 0.5, w, 1);
    ctx.fillRect(x, y + h - h * 0.85 - 0.5, w, 1);
  }

  function drawBottomRow(
    ctx: CanvasRenderingContext2D, c: CellInfo, hist: MetricsSample[],
    x: number, y: number, w: number,
  ) {
    // Left: tick p99 numeric
    const p99ms = (c.tickP99Us / 1000).toFixed(1);
    ctx.fillStyle = "rgba(148, 163, 189, 0.7)";
    ctx.font = "9.5px 'JetBrains Mono', ui-monospace, monospace";
    ctx.textAlign = "left";
    ctx.textBaseline = "bottom";
    ctx.fillText(`p99 ${p99ms}ms`, x, y + 14);

    // Mini sparkline of tick p99 history — only when there's enough room.
    if (w < 80) return;
    if (hist.length < 2) return;
    const sparkX = x + 56;
    const sparkW = w - 56;
    const sparkY = y + 3;
    const sparkH = 11;
    // Domain
    let lo = Infinity;
    let hi = -Infinity;
    for (const s of hist) {
      if (s.tickP99Us < lo) lo = s.tickP99Us;
      if (s.tickP99Us > hi) hi = s.tickP99Us;
    }
    if (lo === hi) {
      hi = lo + 1;
    }
    ctx.beginPath();
    for (let i = 0; i < hist.length; i++) {
      const t = i / (hist.length - 1);
      const v = (hist[i].tickP99Us - lo) / (hi - lo);
      const px = sparkX + t * sparkW;
      const py = sparkY + (1 - v) * sparkH;
      if (i === 0) ctx.moveTo(px, py);
      else ctx.lineTo(px, py);
    }
    ctx.strokeStyle = loadColor(c.load, 0.85);
    ctx.lineWidth = 1;
    ctx.stroke();
  }

  function drawCrosshair(
    ctx: CanvasRenderingContext2D, mx: number, my: number, w: number, h: number, gx: number, gy: number,
  ) {
    ctx.save();
    ctx.strokeStyle = "rgba(34, 211, 218, 0.18)";
    ctx.lineWidth = 1;
    ctx.setLineDash([2, 3]);
    ctx.beginPath();
    ctx.moveTo(gx, my + 0.5);
    ctx.lineTo(w, my + 0.5);
    ctx.moveTo(mx + 0.5, gy);
    ctx.lineTo(mx + 0.5, h);
    ctx.stroke();
    ctx.setLineDash([]);
    ctx.restore();
  }

  function drawScanlines(ctx: CanvasRenderingContext2D, w: number, h: number) {
    ctx.save();
    ctx.fillStyle = "rgba(0, 0, 0, 0.025)";
    for (let y = 0; y < h; y += 3) {
      ctx.fillRect(0, y, w, 1);
    }
    ctx.restore();
  }

  // === Mouse interaction ===

  function pickAt(px: number, py: number): Layout | null {
    let best: Layout | null = null;
    for (const L of layouts) {
      if (px >= L.x && px <= L.x + L.w && py >= L.y && py <= L.y + L.h) {
        if (!best || L.depth > best.depth) best = L;
      }
    }
    return best;
  }

  function onMouseMove(e: MouseEvent) {
    const rect = canvas.getBoundingClientRect();
    const mx = e.clientX - rect.left;
    const my = e.clientY - rect.top;
    mouse = { x: mx, y: my };
    const L = pickAt(mx, my);
    const id = L?.id ?? null;
    if (id !== hover) hover = id;
    if (L) {
      const cell = cells.find((c) => c.id === L.id);
      tooltip = cell
        ? { x: mx + 14, y: my + 14, cell }
        : null;
    } else {
      tooltip = null;
    }
  }

  function onClick(e: MouseEvent) {
    const rect = canvas.getBoundingClientRect();
    const L = pickAt(e.clientX - rect.left, e.clientY - rect.top);
    onSelect?.(L?.id ?? null);
  }

  function onLeave() {
    hover = null;
    tooltip = null;
    mouse = null;
  }

  function loadLabel(load: number): string {
    if (load >= 0.85) return "critical";
    if (load >= 0.6) return "warm";
    if (load >= 0.35) return "nominal";
    return "idle";
  }
</script>

<div bind:this={container} class="relative w-full h-full">
  <canvas
    bind:this={canvas}
    onmousemove={onMouseMove}
    onmouseleave={onLeave}
    onclick={onClick}
    class="cursor-crosshair"
  ></canvas>

  {#if tooltip}
    {@const t = tooltip}
    <div
      class="absolute pointer-events-none bg-[var(--bg-elevated)]/95 backdrop-blur border border-phosphor-400/30 rounded px-3 py-2 shadow-[0_8px_24px_-8px_rgba(34,211,218,0.4)] min-w-[180px]"
      style="left: {t.x}px; top: {t.y}px"
    >
      <div class="flex items-center justify-between gap-3 mb-1.5">
        <span class="font-mono text-[11.5px] font-semibold text-phosphor-300 tracking-tight">
          {t.cell.id}
        </span>
        <span
          class="font-mono text-[9px] uppercase tracking-[0.12em] px-1.5 py-0.5 rounded
            {t.cell.load >= 0.85
              ? 'bg-ember-400/15 text-ember-300'
              : t.cell.load >= 0.6
                ? 'bg-amber-400/15 text-amber-300'
                : 'bg-lime-400/10 text-lime-300'}"
        >
          {loadLabel(t.cell.load)}
        </span>
      </div>
      <div class="grid grid-cols-2 gap-x-3 gap-y-0.5 font-mono text-[10.5px] tabular-nums">
        <span class="text-[var(--text-dim)]">load</span>
        <span class="text-right text-[var(--text-bright)]">{Math.round(t.cell.load * 100)}%</span>
        <span class="text-[var(--text-dim)]">tick p99</span>
        <span class="text-right text-[var(--text-bright)]">{(t.cell.tickP99Us / 1000).toFixed(2)}ms</span>
        <span class="text-[var(--text-dim)]">entities</span>
        <span class="text-right text-[var(--text-bright)]">{t.cell.entities.real}</span>
        <span class="text-[var(--text-dim)]">replicas</span>
        <span class="text-right text-plasma-300">{t.cell.entities.replica}</span>
        <span class="text-[var(--text-dim)]">sessions</span>
        <span class="text-right text-phosphor-300">{t.cell.entities.connected}</span>
      </div>
      <div class="mt-1.5 pt-1.5 border-t border-[var(--border-faint)] font-mono text-[9.5px] text-[var(--text-muted)] tracking-tight truncate">
        host · <span class="text-[var(--text-default)]">{t.cell.hostId || "—"}</span>
      </div>
    </div>
  {/if}

  <!-- Legend (bottom-right) — explains the dot colors -->
  <div
    class="absolute bottom-2 right-3 flex items-center gap-3 font-mono text-[9.5px] text-[var(--text-muted)] bg-[var(--bg-deep)]/70 backdrop-blur px-2.5 py-1 rounded border border-[var(--border-subtle)] pointer-events-none"
  >
    <span class="flex items-center gap-1">
      <span class="w-1.5 h-1.5 rounded-full bg-phosphor-300"></span>real
    </span>
    <span class="flex items-center gap-1">
      <span class="w-1.5 h-1.5 rounded-full bg-plasma-400 opacity-60"></span>replica
    </span>
    <span class="flex items-center gap-1">
      <span class="w-1.5 h-1.5 rounded-full bg-steel-400 opacity-50"></span>ghost
    </span>
  </div>
</div>
