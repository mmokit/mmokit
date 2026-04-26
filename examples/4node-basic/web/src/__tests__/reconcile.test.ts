import { describe, test, expect } from "bun:test";
import { pruneStaleOnFreshSnapshot } from "../reconcile";
import type { ClientEntity } from "../state";

function mkEntity(netID: number): ClientEntity {
  return {
    netID,
    entityType: 1,
    worldX: 0, worldY: 0,
    velX: 0, velY: 0,
    radius: 10, width: 20, height: 20,
    meshState: 0, ownerNode: 0,
    aoIRadius: 1000,
    name: "",
    prevX: 0, prevY: 0,
    isReplica: false, isGhost: false,
    samples: [],
    renderX: 0, renderY: 0, renderRot: 0,
  };
}

describe("pruneStaleOnFreshSnapshot", () => {
  test("drops stale entities not in the fresh visible set", () => {
    // Stale replica left over from a previous cell's perspective —
    // nothing in the fresh frame mentions it, so it must be dropped.
    const entities = new Map<number, ClientEntity>([
      [1, mkEntity(1)],
      [100, mkEntity(100)],
    ]);

    pruneStaleOnFreshSnapshot(entities, [{ netID: 1 }], 1);

    expect(entities.has(1)).toBe(true);
    expect(entities.has(100)).toBe(false);
  });

  test("keeps the player entity even when absent from the frame", () => {
    const entities = new Map<number, ClientEntity>([[1, mkEntity(1)]]);

    pruneStaleOnFreshSnapshot(entities, [], 1);

    expect(entities.has(1)).toBe(true);
  });

  test("playerNetID == 0 disables the defensive keep (no-op gate)", () => {
    const entities = new Map<number, ClientEntity>([[1, mkEntity(1)]]);

    pruneStaleOnFreshSnapshot(entities, [], 0);

    expect(entities.has(1)).toBe(false);
  });
});
