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

<main class="h-full relative flex items-center justify-center overflow-hidden">
  <!-- decorative scan ring -->
  <div
    class="absolute inset-0 pointer-events-none opacity-50"
    style="background:
      radial-gradient(circle at 50% 50%, rgba(34, 211, 218, 0.06) 0%, transparent 40%),
      conic-gradient(from 180deg at 50% 50%, rgba(244, 114, 182, 0.03), rgba(34, 211, 218, 0.05), rgba(163, 230, 53, 0.02), rgba(244, 114, 182, 0.03));"
  ></div>

  <!-- decorative corners -->
  <span class="absolute top-6 left-6 w-5 h-5 border-t-2 border-l-2 border-phosphor-400/30"></span>
  <span class="absolute top-6 right-6 w-5 h-5 border-t-2 border-r-2 border-phosphor-400/30"></span>
  <span class="absolute bottom-6 left-6 w-5 h-5 border-b-2 border-l-2 border-phosphor-400/30"></span>
  <span class="absolute bottom-6 right-6 w-5 h-5 border-b-2 border-r-2 border-phosphor-400/30"></span>

  <form
    class="w-[360px] relative z-10 page-in"
    onsubmit={submit}
  >
    <!-- Logo -->
    <div class="flex items-center gap-2.5 mb-7">
      <svg width="28" height="28" viewBox="0 0 22 22" class="shrink-0">
        <rect x="2" y="2" width="7" height="7" fill="#22d3da" />
        <rect x="13" y="2" width="7" height="7" fill="none" stroke="#22d3da" stroke-width="1.4" />
        <rect x="2" y="13" width="7" height="7" fill="none" stroke="#22d3da" stroke-width="1.4" />
        <rect x="13" y="13" width="7" height="7" fill="#22d3da" opacity="0.4" />
      </svg>
      <div>
        <div class="font-mono text-[15px] font-bold text-phosphor-300 tracking-tight leading-none">
          mmokit
        </div>
        <div class="font-mono text-[9.5px] text-[var(--text-dim)] tracking-[0.18em] uppercase mt-1">
          operations console
        </div>
      </div>
    </div>

    <div class="surface p-6 backdrop-blur-sm" style="background: linear-gradient(180deg, rgba(15, 20, 34, 0.7) 0%, rgba(10, 13, 22, 0.85) 100%);">
      <div class="eyebrow mb-1">auth · operator</div>
      <h1 class="font-mono text-[15px] text-[var(--text-bright)] font-semibold mb-5 tracking-tight">
        sign in
      </h1>

      <label class="block font-mono text-[9.5px] text-[var(--text-muted)] uppercase tracking-[0.14em] mb-1.5" for="username">
        username
      </label>
      <input
        id="username"
        type="text"
        autocomplete="username"
        class="field w-full mb-3"
        bind:value={username}
        required
      />

      <label class="block font-mono text-[9.5px] text-[var(--text-muted)] uppercase tracking-[0.14em] mb-1.5" for="password">
        password
      </label>
      <input
        id="password"
        type="password"
        autocomplete="current-password"
        class="field w-full mb-5"
        bind:value={password}
        required
      />

      {#if error}
        <div class="font-mono text-[10.5px] text-ember-300 mb-3 px-2 py-1 bg-ember-400/[0.06] border border-ember-400/30 rounded tracking-tight">
          ✕ {error}
        </div>
      {/if}

      <button
        type="submit"
        class="btn-phosphor w-full flex items-center justify-center gap-2"
        disabled={submitting}
      >
        {#if submitting}<Loader2 class="w-3.5 h-3.5 animate-spin" />{/if}
        authenticate
      </button>
    </div>

    <!-- Footer telemetry -->
    <div class="mt-4 flex items-center justify-center gap-3 font-mono text-[9.5px] text-[var(--text-dim)] tracking-tight">
      <span class="flex items-center gap-1">
        <span class="w-1 h-1 bg-phosphor-400 rounded-full breathe"></span>
        link nominal
      </span>
      <span>·</span>
      <span class="uppercase tracking-[0.14em]">tls · argon2id</span>
    </div>
  </form>
</main>
