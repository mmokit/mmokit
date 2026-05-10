<script lang="ts">
  import { onMount } from "svelte";
  import { cellsStore } from "$lib/stores.svelte";
  import { stream } from "$lib/stream";
  import { layoutCells, type Layout } from "./cellmap-layout";
  import type { CellInfo } from "$lib/types";

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
  let tooltip = $state<{ x: number; y: number; cell: CellInfo } | null>(null);

  $effect(() => {
    const off = stream.subscribe("cells", (data) => {
      cells = data as CellInfo[];
      cellsStore.set(cells);
      requestAnimationFrame(redraw);
    });
    return off;
  });

  onMount(() => {
    const ro = new ResizeObserver(redraw);
    ro.observe(container);
    redraw();
    return () => ro.disconnect();
  });

  function loadColor(load: number): string {
    const stops = [
      [0.0, "#22c55e"],
      [0.5, "#84cc16"],
      [0.75, "#facc15"],
      [1.0, "#ef4444"],
    ] as const;
    for (let i = 1; i < stops.length; i++) {
      if (load <= (stops[i][0] as number)) return stops[i][1];
    }
    return stops[stops.length - 1][1];
  }

  function hostColor(hostId: string): string {
    let h = 0;
    for (let i = 0; i < hostId.length; i++) {
      h = (h * 31 + hostId.charCodeAt(i)) | 0;
    }
    const hue = ((h % 360) + 360) % 360;
    return `hsl(${hue} 65% 50%)`;
  }

  function entityColor(real: number): string {
    const t = Math.min(1, real / 200);
    const g = Math.round(255 * t);
    return `rgb(${100 + g / 2}, ${130 + g / 2}, 255)`;
  }

  function colorOf(c: CellInfo): string {
    switch (colorMode) {
      case "host": return hostColor(c.hostId || "?");
      case "entities": return entityColor(c.entities.real);
      default: return loadColor(c.load);
    }
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

    const layoutInput = cells.map((c) => ({ id: c.id, depth: c.depth, parent: c.parent }));
    layouts = layoutCells(layoutInput, { width: w, height: h, padding: 12 });

    const byId = new Map(cells.map((c) => [c.id, c]));
    const ordered = [...layouts].sort((a, b) => a.depth - b.depth);

    for (const L of ordered) {
      const cell = byId.get(L.id);
      if (!cell) continue;

      const hasChildren = layouts.some((c) => c.id.startsWith(L.id + ":"));
      if (!hasChildren) {
        ctx.fillStyle = colorOf(cell);
        ctx.fillRect(L.x + 1, L.y + 1, L.w - 2, L.h - 2);
      }

      ctx.strokeStyle = L.id === selected
        ? "rgba(125,211,252,0.95)"
        : L.id === hover
          ? "rgba(255,255,255,0.4)"
          : "rgba(0,0,0,0.3)";
      ctx.lineWidth = L.id === selected ? 2 : 1;
      ctx.strokeRect(L.x + 0.5, L.y + 0.5, L.w - 1, L.h - 1);
    }
  }

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
    const L = pickAt(e.clientX - rect.left, e.clientY - rect.top);
    const id = L?.id ?? null;
    if (id !== hover) {
      hover = id;
      requestAnimationFrame(redraw);
    }
    if (L) {
      const cell = cells.find((c) => c.id === L.id);
      tooltip = cell ? { x: e.clientX - rect.left + 10, y: e.clientY - rect.top + 10, cell } : null;
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
    requestAnimationFrame(redraw);
  }
</script>

<div bind:this={container} class="relative w-full h-full">
  <canvas
    bind:this={canvas}
    onmousemove={onMouseMove}
    onmouseleave={onLeave}
    onclick={onClick}
  ></canvas>
  {#if tooltip}
    <div
      class="absolute pointer-events-none bg-[#0d1117]/95 border border-white/10 rounded px-2 py-1 text-[10.5px] text-slate-200 shadow-xl"
      style="left: {tooltip.x}px; top: {tooltip.y}px"
    >
      <div class="font-semibold text-accent-300">{tooltip.cell.id}</div>
      <div>load {Math.round(tooltip.cell.load * 100)}% · {tooltip.cell.entities.real} ent</div>
      <div class="text-slate-400">host {tooltip.cell.hostId}</div>
    </div>
  {/if}
</div>
