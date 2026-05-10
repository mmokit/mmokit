<script lang="ts">
  import { auth } from "$lib/auth";
  import { ApiError } from "$lib/api";
  import { Loader2 } from "$lib/icons";

  type Props = { onLoggedIn: () => void };
  let { onLoggedIn }: Props = $props();

  let username = $state("");
  let password = $state("");
  let submitting = $state(false);
  let error = $state("");

  async function submit(e: SubmitEvent) {
    e.preventDefault();
    if (submitting) return;
    submitting = true;
    error = "";
    try {
      await auth.login(username.trim(), password);
      onLoggedIn();
    } catch (e) {
      if (e instanceof ApiError) {
        error =
          e.kind === "rbac"
            ? "Invalid credentials."
            : e.kind === "network"
              ? "Could not reach the server."
              : e.message;
      } else {
        error = (e as Error).message;
      }
    } finally {
      submitting = false;
    }
  }
</script>

<main class="h-full flex items-center justify-center bg-[#0a0e14]">
  <form
    class="w-[320px] bg-[#0d1117] border border-white/10 rounded-lg p-6 shadow-xl"
    onsubmit={submit}
  >
    <h1 class="text-accent-300 text-lg font-semibold mb-1">mmokit admin</h1>
    <p class="text-slate-500 text-[12px] mb-4">Sign in with operator credentials.</p>

    <label class="block text-[11px] text-slate-400 uppercase tracking-wide mb-1" for="username">
      Username
    </label>
    <input
      id="username"
      type="text"
      autocomplete="username"
      class="w-full bg-black/40 border border-white/10 rounded px-2 py-1.5 mb-3 text-slate-200 focus:outline-none focus:border-accent-300/50"
      bind:value={username}
      required
    />

    <label class="block text-[11px] text-slate-400 uppercase tracking-wide mb-1" for="password">
      Password
    </label>
    <input
      id="password"
      type="password"
      autocomplete="current-password"
      class="w-full bg-black/40 border border-white/10 rounded px-2 py-1.5 mb-4 text-slate-200 focus:outline-none focus:border-accent-300/50"
      bind:value={password}
      required
    />

    {#if error}
      <div class="text-rose-300 text-[12px] mb-3">{error}</div>
    {/if}

    <button
      type="submit"
      class="w-full bg-accent-400 hover:bg-accent-500 text-slate-950 font-semibold py-1.5 rounded flex items-center justify-center gap-2 disabled:opacity-50"
      disabled={submitting}
    >
      {#if submitting}<Loader2 class="w-3.5 h-3.5 animate-spin" />{/if}
      Sign in
    </button>
  </form>
</main>
