<script lang="ts">
  import { onMount } from "svelte";
  import { scaleSeries } from "./Sparkline.helpers";

  type Props = {
    values: number[];
    width?: number;
    height?: number;
    color?: string;
    /** Optional clamps so unrelated sparklines share the same y-scale. */
    min?: number;
    max?: number;
    /** Optional label drawn in the top-left corner. */
    label?: string;
    /** Optional value drawn in the top-right corner (last sample formatted). */
    valueText?: string;
  };

  let {
    values,
    width = 160,
    height = 36,
    color = "#7dd3fc",
    min,
    max,
    label,
    valueText,
  }: Props = $props();

  let canvas: HTMLCanvasElement | undefined = $state();
  let dpr = 1;

  function draw() {
    if (!canvas) return;
    const ctx = canvas.getContext("2d");
    if (!ctx) return;
    canvas.width = width * dpr;
    canvas.height = height * dpr;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, width, height);

    const pts = scaleSeries(values, width, height, { min, max });
    if (pts.length < 2) return;

    // Faint baseline.
    ctx.strokeStyle = "rgba(255,255,255,0.06)";
    ctx.beginPath();
    ctx.moveTo(0, height - 0.5);
    ctx.lineTo(width, height - 0.5);
    ctx.stroke();

    // Polyline.
    ctx.strokeStyle = color;
    ctx.lineWidth = 1.5;
    ctx.beginPath();
    ctx.moveTo(pts[0].x, pts[0].y);
    for (let i = 1; i < pts.length; i++) ctx.lineTo(pts[i].x, pts[i].y);
    ctx.stroke();

    // Last-value dot.
    const last = pts[pts.length - 1];
    ctx.fillStyle = color;
    ctx.beginPath();
    ctx.arc(last.x, last.y, 1.8, 0, Math.PI * 2);
    ctx.fill();
  }

  onMount(() => {
    dpr = window.devicePixelRatio || 1;
    draw();
  });

  $effect(() => {
    // Re-render whenever inputs change.
    void values;
    void width;
    void height;
    void color;
    void min;
    void max;
    draw();
  });
</script>

<div class="relative inline-block" style:width="{width}px" style:height="{height}px">
  <canvas bind:this={canvas} style:width="{width}px" style:height="{height}px"></canvas>
  {#if label}
    <span class="absolute top-0 left-1 text-[9.5px] text-slate-500 leading-none">{label}</span>
  {/if}
  {#if valueText}
    <span class="absolute top-0 right-1 text-[9.5px] font-mono text-slate-300 leading-none">{valueText}</span>
  {/if}
</div>
