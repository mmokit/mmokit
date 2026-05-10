<script lang="ts">
  import { layoutBars } from "./BarChart.helpers";

  type Row = { label: string; value: number; valueText?: string };

  type Props = {
    rows: Row[];
    /** Pixel cap for the longest bar. */
    maxWidth?: number;
    /** Bar fill color. */
    color?: string;
  };

  let { rows, maxWidth = 220, color = "#7dd3fc" }: Props = $props();

  let bars = $derived.by(() => layoutBars(rows.map((r) => r.value), maxWidth));
</script>

<div class="space-y-1 text-[11.5px]">
  {#each rows as r, i (r.label)}
    <div class="flex items-center gap-2">
      <div class="w-32 truncate text-slate-400 font-mono">{r.label}</div>
      <div class="grow relative h-3 bg-white/5 rounded-sm overflow-hidden">
        <div
          class="absolute inset-y-0 left-0 rounded-sm"
          style:width="{bars[i]?.width ?? 0}px"
          style:background-color={color}
        ></div>
      </div>
      <div class="w-16 text-right font-mono text-slate-300">
        {r.valueText ?? r.value.toFixed(1)}
      </div>
    </div>
  {/each}
  {#if rows.length === 0}
    <div class="text-slate-500 italic">No samples yet.</div>
  {/if}
</div>
