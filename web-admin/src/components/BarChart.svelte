<script lang="ts">
  import { layoutBars } from "./BarChart.helpers";

  type Row = { label: string; value: number; valueText?: string };

  type Props = {
    rows: Row[];
    /** Pixel cap for the longest bar. */
    maxWidth?: number;
    /** Bar fill color (highest bar uses ember for emphasis). */
    color?: string;
    /** Column widths — labels can run long with system names. */
    labelWidth?: number;
    valueWidth?: number;
  };

  let {
    rows,
    maxWidth = 220,
    color = "#22d3da",
    labelWidth = 96,
    valueWidth = 78,
  }: Props = $props();

  let bars = $derived.by(() => layoutBars(rows.map((r) => r.value), maxWidth));
  let maxIdx = $derived.by(() => {
    let i = -1;
    let mv = -Infinity;
    for (let k = 0; k < rows.length; k++) {
      if (rows[k].value > mv) { mv = rows[k].value; i = k; }
    }
    return i;
  });
</script>

<div class="space-y-[3px] font-mono">
  {#each rows as r, i (r.label)}
    {@const isMax = i === maxIdx && rows.length > 1}
    {@const fill = isMax ? "#fb7185" : color}
    <div class="flex items-center gap-2 text-[10.5px] group">
      <div
        class="truncate text-[var(--text-muted)] tracking-tight"
        style:width="{labelWidth}px"
        title={r.label}
      >{r.label}</div>
      <div
        class="grow relative h-[8px] bg-white/[0.04] rounded-[2px] overflow-hidden border border-white/[0.03]"
      >
        <div
          class="absolute inset-y-0 left-0 rounded-[2px] transition-all duration-300"
          style:width="{bars[i]?.width ?? 0}px"
          style:background={isMax
            ? `linear-gradient(90deg, ${fill}, #fbbf24)`
            : `linear-gradient(90deg, ${fill}, ${fill}cc)`}
          style:box-shadow={isMax
            ? `0 0 6px ${fill}55`
            : "none"}
        ></div>
      </div>
      <div
        class="text-right tabular-nums {isMax ? 'text-ember-300' : 'text-[var(--text-default)]'}"
        style:width="{valueWidth}px"
      >
        {r.valueText ?? r.value.toFixed(1)}
      </div>
    </div>
  {/each}
  {#if rows.length === 0}
    <div class="text-[var(--text-dim)] text-[10.5px] italic tracking-tight">No samples yet.</div>
  {/if}
</div>
