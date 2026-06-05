<script lang="ts">
  import { Close } from "$lib/icons";

  // onDespawned is declared in the prop type but unused in this placeholder;
  // Task 7 replaces this component with the full inspector that wires it up.
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  let {
    netID,
    onClose,
    onDespawned,
  }: {
    netID: number;
    onClose: () => void;
    onDespawned: () => void;
  } = $props();
</script>

<aside class="flex w-96 flex-col border-l border-white/10 bg-black/30">
  <header class="flex items-center justify-between border-b border-white/10 p-3">
    <div class="font-semibold">Entity {netID}</div>
    <button class="opacity-70 hover:opacity-100" onclick={onClose} aria-label="Close">
      <Close class="w-4 h-4" />
    </button>
  </header>
  <div class="flex-1 overflow-auto p-3 text-sm opacity-60">
    Inspector loads in Task 7.
  </div>
</aside>
