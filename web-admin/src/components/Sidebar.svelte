<script lang="ts">
  import {
    Globe, Boxes, Users, List, Scroll, Settings, ShieldCheck,
    Bot, Database, Gauge, Layers, Zap, Server, Cpu, Radio,
  } from "$lib/icons";
  import { navigate, route } from "$lib/router";
  import { panelsStore } from "$lib/stores.svelte";
  import type { PanelDef } from "$lib/types";

  // lucide-svelte 0.460 components don't conform to Svelte 5's Component<T>
  // type yet. Use a loose type for the icon slot — they render correctly.
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  type IconComponent = any;

  type Item = {
    id: string;
    label: string;
    icon: IconComponent;
    group: string;
    path: string;
    /** One-letter mnemonic shown in the rail margin. */
    glyph: string;
  };

  // Map PanelDef.Icon string names → Svelte components. Unknown names
  // fall back to Boxes. Game panels declare Icon by lucide name.
  const iconMap: Record<string, IconComponent> = {
    Bot, Database, Gauge, Layers, Zap, Server, Cpu, Radio,
    Globe, Boxes, Users, List, Scroll, Settings, ShieldCheck,
  };

  function iconFor(name: string): IconComponent {
    return iconMap[name] ?? Boxes;
  }

  const builtinItems: Item[] = [
    { id: "cluster",  label: "Cells",    icon: Globe,       group: "MESH",      path: "/cluster",  glyph: "C" },
    { id: "nodes",    label: "Nodes",    icon: Boxes,       group: "MESH",      path: "/nodes",    glyph: "N" },
    { id: "players",  label: "Players",  icon: Users,       group: "PEOPLE",    path: "/players",  glyph: "P" },
    { id: "events",   label: "Events",   icon: List,        group: "TELEMETRY", path: "/events",   glyph: "E" },
    { id: "audit",    label: "Audit",    icon: ShieldCheck, group: "TELEMETRY", path: "/audit",    glyph: "A" },
    { id: "logs",     label: "Logs",     icon: Scroll,      group: "TELEMETRY", path: "/logs",     glyph: "L" },
    { id: "settings", label: "Settings", icon: Settings,    group: "CONFIG",    path: "/settings", glyph: "S" },
  ];

  let panels = $derived<PanelDef[]>(panelsStore.value ?? []);

  // Merge game-registered panels under their declared group (upper-cased
  // to match the builtin group convention). Glyph auto-derived from the
  // first letter of the label.
  //
  // pkg/admin's PanelRegistry also ships defs for cluster/hosts/gateways/
  // players/events/logs/settings — we dedup against builtinItems by id so
  // the SPA's hand-styled entries win and game-only panels (like "bots")
  // pass through.
  let items = $derived.by<Item[]>(() => {
    const builtinIDs = new Set(builtinItems.map((b) => b.id));
    const extras: Item[] = [];
    for (const p of panels) {
      if (builtinIDs.has(p.id)) continue;
      extras.push({
        id: p.id,
        label: p.label,
        icon: iconFor(p.icon),
        group: (p.group || "PANELS").toUpperCase(),
        path: `/panel/${p.id}`,
        glyph: (p.label[0] ?? "?").toUpperCase(),
      });
    }
    return [...builtinItems, ...extras];
  });

  let currentPath = $state("/cluster");
  $effect(() => {
    const off = route.subscribe((p: string) => (currentPath = p));
    return off;
  });

  // Group items by group, preserving insertion order.
  let groups = $derived.by<{ name: string; items: Item[] }[]>(() => {
    const out: { name: string; items: Item[] }[] = [];
    for (const it of items) {
      const last = out[out.length - 1];
      if (last && last.name === it.group) last.items.push(it);
      else out.push({ name: it.group, items: [it] });
    }
    return out;
  });

  // Current YYYY-MM-DD HH:MM:SS for the rail footer — "ops console clock".
  let now = $state(formatNow());
  $effect(() => {
    const i = window.setInterval(() => (now = formatNow()), 1000);
    return () => window.clearInterval(i);
  });

  function formatNow(): string {
    const d = new Date();
    const pad = (n: number) => n.toString().padStart(2, "0");
    return `${d.getFullYear()}.${pad(d.getMonth() + 1)}.${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
  }
</script>

<aside
  class="w-52 shrink-0 relative flex flex-col text-[12px] border-r border-[var(--border-subtle)] bg-gradient-to-b from-[#070a14] to-[#04060b]"
>
  <!-- Brand block -->
  <div class="px-4 pt-4 pb-3 border-b border-[var(--border-faint)]">
    <div class="flex items-center gap-2">
      <svg width="22" height="22" viewBox="0 0 22 22" class="shrink-0">
        <rect x="2" y="2" width="7" height="7" fill="#22d3da" />
        <rect x="13" y="2" width="7" height="7" fill="none" stroke="#22d3da" stroke-width="1.2" />
        <rect x="2" y="13" width="7" height="7" fill="none" stroke="#22d3da" stroke-width="1.2" />
        <rect x="13" y="13" width="7" height="7" fill="#22d3da" opacity="0.4" />
      </svg>
      <div class="leading-tight">
        <div class="font-mono text-[13px] font-bold text-phosphor-300 tracking-tight">
          mmokit
        </div>
        <div class="font-mono text-[9px] text-[var(--text-dim)] tracking-[0.18em] uppercase mt-px">
          ops console
        </div>
      </div>
    </div>
  </div>

  <nav class="flex-1 py-2 overflow-y-auto">
    {#each groups as g, gi (g.name)}
      <div
        class="px-4 pt-3 pb-1.5 font-mono text-[9.5px] text-[var(--text-dim)] tracking-[0.2em] flex items-center gap-2"
      >
        <span>{g.name}</span>
        <span class="grow h-px bg-[var(--border-faint)]"></span>
      </div>
      {#each g.items as it, ii (it.id)}
        {@const active = currentPath === it.path || currentPath.startsWith(it.path + "/")}
        <button
          type="button"
          aria-current={active ? "page" : undefined}
          class="group w-full flex items-center gap-2.5 px-4 py-1.5 text-left transition-colors relative
            {active
              ? 'text-phosphor-300 bg-phosphor-300/[0.06]'
              : 'text-[var(--text-default)] hover:text-[var(--text-bright)] hover:bg-white/[0.025]'}"
          style:--idx={gi * 5 + ii}
          onclick={() => navigate(it.path)}
        >
          {#if active}
            <span
              class="absolute left-0 top-0 bottom-0 w-[2px] bg-phosphor-400"
              style="box-shadow: 0 0 8px var(--phosphor-glow);"
            ></span>
          {/if}
          <it.icon class="w-3.5 h-3.5 shrink-0 {active ? 'text-phosphor-300' : 'text-[var(--text-muted)] group-hover:text-[var(--text-default)]'}" />
          <span class="grow">{it.label}</span>
          <span
            class="font-mono text-[9.5px] {active ? 'text-phosphor-300/70' : 'text-[var(--text-dim)] group-hover:text-[var(--text-muted)]'}"
          >·{it.glyph}</span>
        </button>
      {/each}
    {/each}
  </nav>

  <!-- Footer: clock -->
  <div class="px-4 py-2.5 border-t border-[var(--border-faint)] font-mono text-[9.5px] text-[var(--text-dim)] tracking-tight">
    <div class="flex items-center gap-1.5 mb-0.5">
      <span class="w-1 h-1 bg-lime-400 rounded-full breathe"></span>
      <span class="uppercase tracking-[0.16em]">sync</span>
    </div>
    <div class="tabular-nums">{now}</div>
  </div>
</aside>
