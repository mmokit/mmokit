// Ambient declaration so plain .ts files (e.g. main.ts) can import .svelte
// components. svelte-check's TS host doesn't always follow svelte/types' own
// `declare module '*.svelte'` through a triple-slash reference, so we mirror
// the minimal shape here. No runtime impact.
declare module "*.svelte" {
  import type { Component } from "svelte";
  const component: Component;
  export default component;
}
