<script lang="ts">
  import { Command, Bell, LogOut } from "$lib/icons";
  import { cellsStore, alertsStore, sessionStore, paletteOpen } from "$lib/stores.svelte";
  import { route } from "$lib/router";
  import { auth } from "$lib/auth";
  import AlertBanner from "./AlertBanner.svelte";

  async function logout() {
    try {
      await auth.logout();
    } finally {
      sessionStore.set(null);
    }
  }

  // Derive counts from the live cells SSE stream so they tick automatically.
  let cells = $derived(cellsStore.value ?? []);
  let cellCount = $derived(cells.length);
  let entityCount = $derived(cells.reduce((sum, c) => sum + c.entities.real, 0));
  let sessionCount = $derived(cells.reduce((sum, c) => sum + c.entities.connected, 0));
  let bytesSec = $derived(
    cells.reduce((sum, c) => sum + c.bytes.sent + c.bytes.recv, 0),
  );

  let alertCount = $derived((alertsStore.value ?? []).length);
  let user = $derived(sessionStore.value?.user ?? "");

  // Title from current route — phosphor breadcrumb.
  let path = $state("/cluster");
  $effect(() => {
    const off = route.subscribe((p: string) => (path = p));
    return off;
  });
  let title = $derived(routeTitle(path));

  function routeTitle(p: string): { section: string; leaf: string } {
    if (p === "/cluster")       return { section: "mesh",      leaf: "live cell map" };
    if (p.startsWith("/nodes")) return { section: "mesh",      leaf: "nodes" };
    if (p === "/players")       return { section: "people",    leaf: "players" };
    if (p === "/events")        return { section: "telemetry", leaf: "events" };
    if (p === "/audit")         return { section: "telemetry", leaf: "audit" };
    if (p === "/logs")          return { section: "telemetry", leaf: "logs" };
    if (p === "/settings")      return { section: "config",    leaf: "settings" };
    return { section: "", leaf: p };
  }

  // Health: lime if cells snapshot recent and no alerts; amber if alerts; dim while loading.
  let health = $derived(
    alertCount > 0 ? "warn" : cellCount > 0 ? "ok" : "stale",
  );

  function fmt(n: number): string {
    if (n < 1000) return `${n}`;
    if (n < 1_000_000) return `${(n / 1000).toFixed(1)}k`;
    return `${(n / 1_000_000).toFixed(1)}M`;
  }
</script>

<header
  class="relative border-b border-[var(--border-subtle)] bg-[var(--bg-deep)]/80 backdrop-blur-md flex items-center gap-5 px-5 py-2.5 text-[12px]"
>
  <!-- Breadcrumb -->
  <div class="flex items-center gap-2 font-mono text-[11px]">
    <span class="text-[var(--text-dim)] uppercase tracking-[0.16em]">
      {title.section}
    </span>
    <span class="text-[var(--text-dim)]">/</span>
    <span class="text-phosphor-300 font-semibold tracking-tight">
      {title.leaf}
    </span>
  </div>

  <!-- Center: alert banner -->
  <div class="grow flex items-center justify-center">
    <AlertBanner />
  </div>

  <!-- Right: live telemetry chips + user -->
  <div class="flex items-center gap-2">
    <!-- Status pill -->
    <div
      class="flex items-center gap-1.5 px-2.5 py-1 rounded border font-mono text-[10.5px] uppercase tracking-[0.1em]
        {health === 'ok'
          ? 'border-lime-400/30 text-lime-300 bg-lime-400/[0.04]'
          : health === 'warn'
            ? 'border-amber-400/40 text-amber-300 bg-amber-400/[0.05]'
            : 'border-[var(--border-subtle)] text-[var(--text-dim)]'}"
    >
      <span
        class="relative w-1.5 h-1.5 rounded-full
          {health === 'ok' ? 'bg-lime-400 phosphor-pulse' : health === 'warn' ? 'bg-amber-400' : 'bg-steel-500'}"
      ></span>
      <span>{health === "ok" ? "nominal" : health === "warn" ? "advisory" : "linking"}</span>
    </div>

    <!-- Telemetry chips (cells / ent / sess) -->
    {#if cellCount > 0}
      <div class="flex items-stretch border border-[var(--border-subtle)] rounded overflow-hidden font-mono text-[10.5px]">
        <div class="flex items-center gap-1 px-2 py-1 bg-[var(--bg-surface)]">
          <span class="text-[var(--text-dim)] uppercase tracking-[0.1em]">cells</span>
          <span class="text-phosphor-300 tabular-nums">{cellCount}</span>
        </div>
        <div class="flex items-center gap-1 px-2 py-1 bg-[var(--bg-surface)] border-l border-[var(--border-faint)]">
          <span class="text-[var(--text-dim)] uppercase tracking-[0.1em]">ent</span>
          <span class="text-[var(--text-default)] tabular-nums">{fmt(entityCount)}</span>
        </div>
        <div class="flex items-center gap-1 px-2 py-1 bg-[var(--bg-surface)] border-l border-[var(--border-faint)]">
          <span class="text-[var(--text-dim)] uppercase tracking-[0.1em]">sess</span>
          <span class="text-[var(--text-default)] tabular-nums">{sessionCount}</span>
        </div>
      </div>
    {/if}

    <!-- Command palette trigger -->
    <button
      type="button"
      class="flex items-center gap-1.5 px-2 py-1 rounded border border-[var(--border-subtle)] hover:border-[var(--border-bright)] hover:bg-white/[0.03] transition-colors font-mono text-[10.5px] text-[var(--text-default)]"
      title="Find cell / node / player (⌘K)"
      onclick={() => paletteOpen.set(true)}
    >
      <Command class="w-3 h-3 text-[var(--text-muted)]" />
      <span class="text-[var(--text-dim)]">⌘K</span>
    </button>

    <!-- Alerts -->
    <button
      type="button"
      class="relative flex items-center justify-center w-7 h-7 rounded border transition-colors
        {alertCount > 0
          ? 'border-ember-400/40 text-ember-300 bg-ember-400/[0.05] hover:bg-ember-400/[0.08]'
          : 'border-[var(--border-subtle)] text-[var(--text-muted)] hover:text-[var(--text-default)]'}"
      title="Active alerts"
    >
      <Bell class="w-3.5 h-3.5" />
      {#if alertCount > 0}
        <span class="absolute -top-1 -right-1 min-w-[14px] h-[14px] px-1 rounded-full bg-ember-400 text-[9px] font-bold font-mono text-[var(--bg-void)] flex items-center justify-center">
          {alertCount}
        </span>
      {/if}
    </button>

    {#if user}
      <div class="flex items-center gap-2 pl-2 ml-1 border-l border-[var(--border-subtle)]">
        <div class="flex items-center gap-1.5">
          <div class="w-6 h-6 rounded-full bg-gradient-to-br from-phosphor-400/30 to-plasma-400/20 border border-phosphor-400/40 flex items-center justify-center font-mono text-[10px] font-bold text-phosphor-200">
            {user.slice(0, 2).toUpperCase()}
          </div>
          <span class="font-mono text-[11px] text-[var(--text-default)]">{user}</span>
        </div>
        <button
          type="button"
          class="text-[var(--text-muted)] hover:text-ember-300 transition-colors p-1"
          title="Sign out"
          onclick={logout}
        >
          <LogOut class="w-3.5 h-3.5" />
        </button>
      </div>
    {/if}
  </div>

  <!-- Bottom phosphor underline — barely visible, gives the bar weight -->
  <span class="absolute bottom-0 left-0 right-0 h-px bg-gradient-to-r from-transparent via-phosphor-400/20 to-transparent pointer-events-none"></span>
</header>
