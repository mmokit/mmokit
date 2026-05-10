<script lang="ts">
  import { sessionStore } from "$lib/stores.svelte";
  import { auth } from "$lib/auth";

  let session = $derived(sessionStore.value);

  function fmtTime(ts?: string): string {
    if (!ts) return "—";
    return new Date(ts).toLocaleString(undefined, { hour12: false });
  }

  async function signOut() {
    try {
      await auth.logout();
    } finally {
      sessionStore.set(null);
    }
  }
</script>

<main class="p-4 space-y-4">
  <h2 class="text-accent-300 text-[11px] uppercase tracking-wide">Settings</h2>

  <div class="bg-[#0d1117] border border-white/10 rounded-lg p-4 space-y-2 max-w-2xl">
    <h3 class="text-[12px] text-slate-200 font-semibold">Session</h3>
    <div class="grid grid-cols-[120px_1fr] gap-x-3 gap-y-1 text-[12px]">
      <span class="text-slate-500">Operator</span>
      <span class="font-mono text-slate-200">{session?.user ?? "—"}</span>
      <span class="text-slate-500">Grants</span>
      <span class="font-mono text-slate-300">
        {(session?.grants ?? []).join(", ") || "—"}
      </span>
      <span class="text-slate-500">Expires</span>
      <span class="text-slate-300">{fmtTime(session?.expiresAt)}</span>
    </div>
    <div class="pt-2">
      <button
        type="button"
        class="px-3 py-1 text-[12px] bg-rose-500/15 hover:bg-rose-500/25 text-rose-200 border border-rose-500/30 rounded"
        onclick={signOut}
      >
        Sign out
      </button>
    </div>
  </div>

  <div class="bg-[#0d1117] border border-white/10 rounded-lg p-4 space-y-2 max-w-2xl">
    <h3 class="text-[12px] text-slate-200 font-semibold">About</h3>
    <p class="text-slate-400 text-[12px]">
      mmokit admin dashboard · embedded into the coordinator at startup. Operator
      passwords are argon2id-hashed; sessions live in memory and expire on
      coordinator restart. The audit log is a bounded ring; persistence is
      Phase 2.
    </p>
    <p class="text-slate-500 text-[11.5px]">
      Server-side configuration (operator list, lockout window, listen address) is
      file-driven via <code>Config.Admin</code>; this page only displays the live
      session view. Active-session table and per-operator administration land in
      Phase 2.
    </p>
  </div>
</main>
