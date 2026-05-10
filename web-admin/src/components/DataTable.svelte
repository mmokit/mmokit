<script lang="ts" generics="T">
  import { ChevronDown, ChevronRight } from "$lib/icons";
  import { sortRows, type SortDir } from "./DataTable.helpers";

  type Column<U> = {
    key: string;            // unique key (used for sort state + as :key)
    label: string;
    /** Cell accessor — used for sorting; render() controls display. */
    accessor?: (row: U) => string | number | undefined | null;
    /** Optional render override; defaults to String(accessor(row)). */
    render?: (row: U) => unknown;
    align?: "left" | "right" | "center";
    width?: string;        // CSS width hint, e.g. "120px" / "20%"
  };

  type Props = {
    rows: T[];
    columns: Column<T>[];
    initialSortKey?: string;
    initialSortDir?: SortDir;
    /** Empty-state text shown when rows.length === 0. */
    emptyText?: string;
    /** Optional click-row handler. */
    onRowClick?: (row: T) => void;
  };

  let {
    rows,
    columns,
    initialSortKey,
    initialSortDir = "asc",
    emptyText = "No data.",
    onRowClick,
  }: Props = $props();

  let sortKey = $state(initialSortKey ?? columns[0]?.key ?? "");
  let sortDir = $state<SortDir>(initialSortDir);

  let sorted = $derived.by(() => {
    const col = columns.find((c) => c.key === sortKey);
    if (!col?.accessor) return rows;
    return sortRows(rows, col.accessor, sortDir);
  });

  function toggleSort(key: string) {
    if (sortKey === key) {
      sortDir = sortDir === "asc" ? "desc" : "asc";
    } else {
      sortKey = key;
      sortDir = "asc";
    }
  }
</script>

{#snippet renderCell(value: unknown)}
  {#if value == null}—{:else}{value}{/if}
{/snippet}

<div class="overflow-x-auto">
  <table class="w-full text-[12px] border-collapse">
    <thead>
      <tr class="text-left text-[10.5px] uppercase tracking-wide text-slate-500 border-b border-white/10">
        {#each columns as col (col.key)}
          <th
            class="py-1.5 px-2 font-medium cursor-pointer hover:text-slate-300 select-none"
            style:width={col.width ?? "auto"}
            style:text-align={col.align ?? "left"}
            onclick={() => col.accessor && toggleSort(col.key)}
          >
            <span class="inline-flex items-center gap-1">
              {col.label}
              {#if col.key === sortKey && col.accessor}
                {#if sortDir === "asc"}
                  <ChevronRight class="w-3 h-3" />
                {:else}
                  <ChevronDown class="w-3 h-3" />
                {/if}
              {/if}
            </span>
          </th>
        {/each}
      </tr>
    </thead>
    <tbody>
      {#each sorted as row, i (i)}
        <tr
          class="border-b border-white/5 hover:bg-white/5 {onRowClick ? 'cursor-pointer' : ''}"
          onclick={() => onRowClick?.(row)}
        >
          {#each columns as col (col.key)}
            <td class="py-1.5 px-2" style:text-align={col.align ?? "left"}>
              {#if col.render}
                {@render renderCell(col.render(row))}
              {:else if col.accessor}
                {col.accessor(row) ?? "—"}
              {:else}
                —
              {/if}
            </td>
          {/each}
        </tr>
      {:else}
        <tr>
          <td colspan={columns.length} class="py-4 text-center text-slate-500">
            {emptyText}
          </td>
        </tr>
      {/each}
    </tbody>
  </table>
</div>
