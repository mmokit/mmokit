import { Container } from "pixi.js";
import { ENTITY_COLORS } from "../constants";
import type { ClientEntity, EntityDisplayObject } from "../types";
import { getAsteroid } from "../entity-accessors";
import { createShipDisplay } from "./ship";
import { createAsteroidDisplay } from "./asteroid";
import { createProjectileDisplay } from "./projectile";
import { createStationDisplay } from "./station";
import { createLootCrateDisplay } from "./loot-crate";
import { createNpcDisplay } from "./npc";
import { createPoiDisplay } from "./poi";
import { EntityType } from "../../sdk/index.js";

export class EntityManager {
  private displayObjects = new Map<number, EntityDisplayObject>();
  private entityContainer: Container;
  private uiContainer: Container; // Non-rotating layer for ship bars/names

  constructor(entityContainer: Container, uiContainer: Container) {
    this.entityContainer = entityContainer;
    this.uiContainer = uiContainer;
  }

  sync(entities: Map<number, ClientEntity>, myEntityId: number, now: number): void {
    // Remove display objects for entities no longer present
    for (const [id, obj] of this.displayObjects) {
      if (!entities.has(id)) {
        this.entityContainer.removeChild(obj.container);
        // Remove UI container if ship
        if ("uiContainer" in obj) {
          this.uiContainer.removeChild((obj as any).uiContainer);
        }
        obj.destroy();
        this.displayObjects.delete(id);
      }
    }

    // Add/update display objects
    for (const [id, ent] of entities) {
      let obj = this.displayObjects.get(id);

      if (!obj) {
        obj = this.createDisplayObject(ent);
        this.displayObjects.set(id, obj);
        this.entityContainer.addChild(obj.container);
        // Add ship UI containers to non-rotating layer
        if ("uiContainer" in obj) {
          this.uiContainer.addChild((obj as any).uiContainer);
        }
      }

      const isMe = id === myEntityId;

      // Update position/rotation
      obj.container.position.set(ent.renderX, ent.renderY);
      // Loot crates handle their own rotation in update()
      if (ent.current.entityType !== EntityType.LootCrate) {
        obj.container.rotation = ent.renderRot;
      }

      // Entity-specific updates
      obj.update(ent, isMe, now);
    }
  }

  private createDisplayObject(ent: ClientEntity): EntityDisplayObject {
    const e = ent.current;
    switch (e.entityType) {
      case EntityType.Ship:
        return createShipDisplay();
      case EntityType.Asteroid: {
        const asteroid = getAsteroid(ent);
        return createAsteroidDisplay(asteroid?.itemID ?? 0, e.radius || 0.7);
      }
      case EntityType.Station:
        return createStationDisplay(e.radius || 5);
      case EntityType.LootCrate:
        return createLootCrateDisplay(e.radius || 0.4);
      case EntityType.NPC:
        return createNpcDisplay();
      case EntityType.POI:
        return createPoiDisplay();
    }
    // Unreachable for known SDK entity kinds — fall back to projectile-style dot.
    return createProjectileDisplay(ENTITY_COLORS[0] || 0xffffff);
  }

  clear(): void {
    for (const [, obj] of this.displayObjects) {
      this.entityContainer.removeChild(obj.container);
      if ("uiContainer" in obj) {
        this.uiContainer.removeChild((obj as any).uiContainer);
      }
      obj.destroy();
    }
    this.displayObjects.clear();
  }
}
