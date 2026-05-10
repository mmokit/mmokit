<script lang="ts">
  import { Globe, Boxes, Users, Activity, List, Scroll, Settings, ShieldCheck } from "$lib/icons";
  import { navigate, route } from "$lib/router";

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
  };

  const items: Item[] = [
    { id: "cluster", label: "Cells", icon: Globe, group: "CLUSTER", path: "/cluster" },
    { id: "nodes", label: "Nodes", icon: Boxes, group: "CLUSTER", path: "/nodes" },
    { id: "players", label: "Players", icon: Users, group: "PEOPLE", path: "/players" },
    { id: "performance", label: "Performance", icon: Activity, group: "DIAGNOSE", path: "/performance" },
    { id: "events", label: "Events", icon: List, group: "DIAGNOSE", path: "/events" },
    { id: "audit", label: "Audit", icon: ShieldCheck, group: "DIAGNOSE", path: "/audit" },
    { id: "logs", label: "Logs", icon: Scroll, group: "DIAGNOSE", path: "/logs" },
    { id: "settings", label: "Settings", icon: Settings, group: "CONFIG", path: "/settings" },
  ];

  let currentPath = $state("/cluster");
  $effect(() => {
    const off = route.subscribe((p: string) => (currentPath = p));
    return off;
  });

  // Group items by group, preserving order.
  const groups: { name: string; items: Item[] }[] = [];
  for (const it of items) {
    const last = groups[groups.length - 1];
    if (last && last.name === it.group) last.items.push(it);
    else groups.push({ name: it.group, items: [it] });
  }
</script>

<aside
  class="w-44 shrink-0 bg-[#0a0e14] border-r border-white/5 py-3 text-[12px] flex flex-col"
>
  <div class="px-4 pb-3 font-semibold text-accent-300 tracking-wide text-[13px]">
    mmokit ops
  </div>
  {#each groups as g (g.name)}
    <div class="px-4 pt-2 pb-1 text-[10px] text-slate-500 tracking-[0.08em]">
      {g.name}
    </div>
    {#each g.items as it (it.id)}
      {@const active = currentPath === it.path || currentPath.startsWith(it.path + "/")}
      <button
        type="button"
        class="flex items-center gap-2 px-4 py-1.5 text-left {active
          ? 'bg-accent-300/10 text-accent-300 border-l-2 border-accent-300 pl-[14px]'
          : 'text-slate-300 hover:bg-white/5'}"
        onclick={() => navigate(it.path)}
      >
        <it.icon class="w-3.5 h-3.5" />
        <span>{it.label}</span>
      </button>
    {/each}
  {/each}
  <div class="grow"></div>
</aside>
