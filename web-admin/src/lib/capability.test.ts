import { describe, it, expect, beforeEach } from "vitest";
import { commandsStore, hasVerb } from "./stores.svelte";
import type { CommandEntry } from "./types";

function cmd(verb: string): CommandEntry {
  return { verb, capability: "", description: "", route: "" };
}

describe("hasVerb", () => {
  beforeEach(() => {
    commandsStore.clear();
  });

  it("reports false before the boot fetch resolves", () => {
    // The store is null until app.svelte's hydrateCluster lands. Reporting
    // false means game-specific UI flickers in rather than rendering and
    // then 404-ing against a verb the cluster never registered.
    expect(hasVerb("world.list")).toBe(false);
  });

  it("reports false for a cluster that registers no world verbs", () => {
    // This is examples/simple and examples/4node-basic: they mount the same
    // framework admin dashboard but register no world.* commands.
    commandsStore.set([cmd("cell.split"), cmd("tune.list"), cmd("entity.list")]);
    expect(hasVerb("world.list")).toBe(false);
  });

  it("reports true for a cluster that registers the verb", () => {
    commandsStore.set([cmd("cell.split"), cmd("world.list"), cmd("world.place")]);
    expect(hasVerb("world.list")).toBe(true);
  });

  it("matches the verb exactly, not by prefix", () => {
    // "world.listen" must not satisfy a probe for "world.list".
    commandsStore.set([cmd("world.listen")]);
    expect(hasVerb("world.list")).toBe(false);
  });

  it("reports false for an empty registry", () => {
    commandsStore.set([]);
    expect(hasVerb("world.list")).toBe(false);
  });
});
